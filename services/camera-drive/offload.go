package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// offloadHooks lets tests inject faults; production passes the zero value.
type offloadHooks struct {
	afterCopy func(dest string) // runs before the destination is re-hashed
}

func cmdOffload(args []string) int {
	fs := flag.NewFlagSet("offload", flag.ContinueOnError)
	p := pathFlags(fs)
	if fs.Parse(args) != nil || fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: camera-drive offload [--root D --run-dir D --lock F] /dev/sdXN")
		return 2
	}
	return offloadMain(*p, fs.Arg(0), "/sys/class/leds")
}

func offloadMain(p paths, dev, ledDir string) (code int) {
	lg := logger{out: os.Stdout, file: p.logFile(), mkdir: p.Root}
	if err := os.MkdirAll(p.media(), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// udev can fire more than once for the same insertion; serialise.
	lock, err := os.OpenFile(p.Lock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if err == syscall.EWOULDBLOCK {
			lg.logf("another offload is already running, exiting")
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	led := newLed(ledDir)
	defer led.restore()
	defer func() {
		if r := recover(); r != nil { // last resort, must not crash silently
			lg.logf("unhandled error: %v", r)
			setStatus(p, map[string]any{"state": "error", "last_result": fmt.Sprint(r)})
			led.blink(10, ms(50), ms(50))
			code = 1
		}
	}()
	lg.logf("=== offload starting for %s ===", dev)
	return offloadDevice(p, lg, dev, led, offloadHooks{})
}

func offloadDevice(p paths, lg logger, dev string, led *Led, h offloadHooks) int {
	if !waitForDevice(dev, 15*time.Second) {
		lg.logf("device %s never appeared", dev)
		return 1
	}
	// Read-only: this path never writes to the camera, and a read-only mount
	// cannot corrupt FAT on power loss.
	if !mountDev(p.mount(), dev, "ro,noatime,nosuid,nodev,noexec,uid=0,gid=0", lg, "mount") {
		return 1
	}
	defer func() {
		syscall.Sync()
		umountRetry(p.mount(), lg, "warning: could not unmount cleanly")
	}()
	return offloadTree(p, lg, p.mount(), led, h)
}

func waitForDevice(dev string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(dev); err == nil {
			return true
		}
		time.Sleep(400 * time.Millisecond)
	}
	return false
}

func setStatus(p paths, kv map[string]any) {
	st := readJSON(p.status())
	for k, v := range kv {
		st[k] = v
	}
	st["updated"] = time.Now().UTC().Format("2006-01-02T15:04:05+00:00")
	if err := writeJSON(p.status(), st); err != nil {
		panic(err)
	}
}

// offloadTree is the pure core: copy every new media file under src into
// media/<ingest stamp>/ and verify it. It never deletes anything from src.
func offloadTree(p paths, lg logger, src string, led *Led, h offloadHooks) int {
	files := collectFiles(src)
	lg.logf("found %d media file(s) on the camera", len(files))
	if len(files) == 0 {
		setStatus(p, map[string]any{"state": "idle", "last_result": "nothing to copy", "last_copied": 0, "last_skipped": 0})
		led.blink(2, ms(400), ms(200))
		return 0
	}

	index := readJSON(p.index())
	stamp := time.Now().Format("2006-01-02_150405") // camera clock is dead: ingest time only
	destDir := filepath.Join(p.media(), stamp)
	var copied, skipped, failed int
	var totalBytes int64
	setStatus(p, map[string]any{"state": "copying", "total": len(files), "done": 0})

	// Hash the next source file while the current one is copied and verified:
	// Go's SHA-256 is ~3x slower than OpenSSL's on the Zero 2 W (no SHA2
	// instructions), and the board has spare cores.
	type hashed struct {
		digest string
		err    error
	}
	ahead := make(chan hashed, 1)
	go func() {
		for _, f := range files {
			d, err := sha256File(f)
			ahead <- hashed{d, err}
		}
	}()

	for n, f := range files {
		n++
		name := filepath.Base(f)
		r := <-ahead
		digest, err := r.digest, r.err
		if err != nil {
			lg.logf("unreadable %s: %v", name, err)
			failed++
			continue
		}
		if _, held := index[digest]; held {
			skipped++
			lg.logf("[%d/%d] %s already held, skipping", n, len(files), name)
			setStatus(p, map[string]any{"done": n})
			continue
		}

		if err := os.MkdirAll(destDir, 0o755); err != nil {
			panic(err)
		}
		dest := filepath.Join(destDir, name)
		if _, err := os.Lstat(dest); err == nil {
			ext := pySuffix(name)
			dest = filepath.Join(destDir, name[:len(name)-len(ext)]+"_"+digest[:8]+ext)
		}

		if err := copyFile(f, dest); err != nil {
			lg.logf("copy failed %s: %v", name, err)
			failed++
			continue
		}
		if h.afterCopy != nil {
			h.afterCopy(dest)
		}
		// Verify what actually landed on disk, not what we think we wrote.
		if got, err := sha256File(dest); err != nil || got != digest {
			if err != nil {
				lg.logf("copy failed %s: %v", name, err)
			} else {
				lg.logf("VERIFY FAILED for %s, removing copy", name)
				os.Remove(dest)
			}
			failed++
			continue
		}

		syscall.Sync()
		rel, _ := filepath.Rel(p.media(), dest)
		index[digest] = rel
		copied++
		if st, err := os.Stat(dest); err == nil {
			totalBytes += st.Size()
		}
		lg.logf("[%d/%d] %s -> %s ok", n, len(files), name, rel)
		setStatus(p, map[string]any{"done": n})
		led.blink(1, ms(30), 0)
	}

	if err := writeJSON(p.index(), index); err != nil {
		panic(err)
	}

	if copied > 0 {
		chownTree(destDir)
	}

	result := fmt.Sprintf("copied %d, skipped %d already held", copied, skipped)
	if failed > 0 {
		result += fmt.Sprintf(", %d FAILED", failed)
	}
	lg.logf("%s", result)
	var folder any
	if copied > 0 {
		folder = stamp
	}
	setStatus(p, map[string]any{"state": "idle", "last_result": result, "last_copied": copied,
		"last_skipped": skipped, "last_failed": failed, "last_bytes": totalBytes, "last_folder": folder})

	if failed > 0 {
		led.blink(10, ms(50), ms(50))
		return 1
	}
	led.blink(3, ms(400), ms(250))
	return 0
}

// chownTree hands the dump to the unprivileged web user; the first failure stops it.
func chownTree(dir string) {
	if os.Chown(dir, 1000, 1000) != nil {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if os.Chown(filepath.Join(dir, e.Name()), 1000, 1000) != nil {
			return
		}
	}
}

// copyFile is shutil.copy2 for a regular file: content, mode and mtime.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	_, err = io.CopyBuffer(out, in, make([]byte, 1<<20))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(dst, st.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(dst, st.ModTime(), st.ModTime())
}
