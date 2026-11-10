package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	cameraVendor  = "08ca"
	cameraStorage = "2023"
)

// wipeResult is printed as the last stdout line; the web app trusts only that.
type wipeResult struct {
	ok                    bool
	deleted, kept, errors []string
	committed             bool
}

func (r wipeResult) json() string {
	b := map[bool]string{true: "true", false: "false"}
	return fmt.Sprintf(`{"ok": %s, "deleted": %s, "kept": %s, "errors": %s, "committed": %s}`,
		b[r.ok], pyList(r.deleted), pyList(r.kept), pyList(r.errors), b[r.committed])
}

const wipeUsage = `camera-drive wipe --dry-run   report what would be deleted
camera-drive wipe --commit    actually delete

A file is deleted from the camera ONLY IF its SHA-256 is already in the Pi's
index. There is no override flag.`

func cmdWipe(args []string) int {
	fs := flag.NewFlagSet("wipe", flag.ContinueOnError)
	p := pathFlags(fs)
	commit := fs.Bool("commit", false, "actually delete")
	dry := fs.Bool("dry-run", false, "report only")
	// Tests and verify.sh point this at a loop device; the vendor/product check still applies.
	devGlob := fs.String("dev-glob", "/dev/sd[a-z][0-9]", "candidate partitions")
	if fs.Parse(args) != nil || (!*commit && !*dry) {
		fmt.Println(wipeUsage)
		return 2
	}
	res := wipeMain(*p, *devGlob, *commit)
	fmt.Println(res.json())
	if res.ok {
		return 0
	}
	return 1
}

func wipeMain(p paths, devGlob string, commit bool) wipeResult {
	res := wipeResult{committed: commit}
	fail := func(msg string) wipeResult { res.errors = append(res.errors, msg); return res }
	lg := logger{out: os.Stdout, file: p.logFile(), wipe: true}

	index, err := readIndex(p.index())
	if err != nil {
		return fail("cannot read index: " + err.Error())
	}
	dev := findCamera(devGlob)
	if dev == "" {
		return fail("camera not connected in storage mode")
	}
	if !mountDev(p.wipeMount(), dev, "rw,noatime,nosuid,nodev,noexec", lg, "mount rw") {
		return fail("could not mount " + dev + " read-write")
	}
	defer func() {
		syscall.Sync()
		umountRetry(p.wipeMount(), lg, "warning: could not unmount after wipe")
	}()
	return wipeTree(p, lg, p.wipeMount(), index, commit)
}

func readIndex(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := readJSON(path)
	if len(m) == 0 && strings.TrimSpace(string(b)) != "{}" {
		return nil, fmt.Errorf("not a JSON object")
	}
	return m, nil
}

// udevProp asks udev about a device rather than trusting /dev/sda to be the camera.
func udevProp(dev, prop string) string {
	out, _ := exec.Command("udevadm", "info", "--query=property", "--name="+dev).Output()
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && k == prop {
			return v
		}
	}
	return ""
}

// findCamera locates the camera by USB vendor/product, never by device name.
func findCamera(glob string) string {
	devs, _ := filepath.Glob(glob)
	for _, d := range devs { // Glob returns them sorted
		if strings.ToLower(udevProp(d, "ID_VENDOR_ID")) == cameraVendor &&
			strings.ToLower(udevProp(d, "ID_MODEL_ID")) == cameraStorage {
			return d
		}
	}
	return ""
}

// wipeTree is the pure core. A file is unlinked only if its hash is in the
// index AND the Pi's copy still exists; everything else is kept.
func wipeTree(p paths, lg logger, src string, index map[string]any, commit bool) wipeResult {
	res := wipeResult{committed: commit}
	files := collectFiles(src)
	mode := "dry-run"
	if commit {
		mode = "COMMIT"
	}
	lg.logf("%s: %d file(s) on camera", mode, len(files))

	for _, f := range files {
		name := filepath.Base(f)
		digest, err := sha256File(f)
		if err != nil {
			res.errors = append(res.errors, fmt.Sprintf("%s: unreadable (%v)", name, err))
			res.kept = append(res.kept, name)
			continue
		}
		entry, held := index[digest]
		if !held {
			lg.logf("KEEPING %s -- no verified copy on the Pi", name)
			res.kept = append(res.kept, name)
			continue
		}
		heldAt, isStr := entry.(string)
		if !isStr {
			res.errors = append(res.errors, name+": index entry is not a path")
			res.kept = append(res.kept, name)
			continue
		}
		copyPath := filepath.Join(p.media(), heldAt)
		if filepath.IsAbs(heldAt) { // pathlib joins an absolute right-hand side as-is
			copyPath = heldAt
		}
		if _, err := os.Stat(copyPath); err != nil {
			lg.logf("KEEPING %s -- index claims %s but it is gone", name, heldAt)
			res.kept = append(res.kept, name)
			res.errors = append(res.errors, name+": index points at missing file")
			continue
		}
		if commit {
			if err := os.Remove(f); err != nil {
				res.errors = append(res.errors, fmt.Sprintf("%s: delete failed (%v)", name, err))
				res.kept = append(res.kept, name)
				continue
			}
			lg.logf("deleted %s (held at %s)", name, heldAt)
		} else {
			lg.logf("would delete %s (held at %s)", name, heldAt)
		}
		res.deleted = append(res.deleted, name)
	}
	if commit {
		syscall.Sync()
	}
	res.ok = len(res.errors) == 0
	return res
}
