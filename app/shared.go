package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
)

var mediaSuffixes = map[string]bool{".jpg": true, ".jpeg": true, ".avi": true, ".mov": true, ".mp4": true, ".wav": true, ".png": true}

var ignoredDirs = map[string]bool{"System Volume Information": true, ".fseventsd": true,
	".Spotlight-V100": true, ".Trashes": true, "LOST.DIR": true, "FOUND.000": true}

// paths holds every location the tools touch, so tests can run in a temp dir.
type paths struct{ Root, Run, Lock string }

func (p paths) media() string     { return filepath.Join(p.Root, "media") }
func (p paths) index() string     { return filepath.Join(p.Root, "index.json") }
func (p paths) status() string    { return filepath.Join(p.Root, "status.json") }
func (p paths) logFile() string   { return filepath.Join(p.Root, "offload.log") }
func (p paths) mount() string     { return filepath.Join(p.Run, "mnt") }
func (p paths) wipeMount() string { return filepath.Join(p.Run, "wipe-mnt") }

func pathFlags(fs *flag.FlagSet) *paths {
	p := &paths{}
	fs.StringVar(&p.Root, "root", "/srv/camera-drive", "data directory")
	fs.StringVar(&p.Run, "run-dir", "/run/camera-drive", "runtime directory (mount points)")
	fs.StringVar(&p.Lock, "lock", "/run/camera-drive.lock", "single-instance lock file")
	return p
}

// logger prints each line and appends it to offload.log. Offload stamps UTC
// ISO-8601, wipe stamps local time with a [wipe] tag, as the Python tools did.
type logger struct {
	out   io.Writer
	file  string
	wipe  bool
	mkdir string // created before each append when set (offload only)
}

func (l logger) logf(format string, a ...any) {
	var line string
	if l.wipe {
		line = time.Now().Format("2006-01-02T15:04:05-0700") + " [wipe] " + fmt.Sprintf(format, a...)
	} else {
		line = time.Now().UTC().Format("2006-01-02T15:04:05+00:00") + " " + fmt.Sprintf(format, a...)
	}
	fmt.Fprintln(l.out, line)
	if l.mkdir != "" {
		os.MkdirAll(l.mkdir, 0o755)
	}
	if f, err := os.OpenFile(l.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666); err == nil {
		fmt.Fprintln(f, line)
		f.Close()
	}
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 1<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// pySuffix mirrors pathlib.Path.suffix: ".jpg" alone has no suffix.
func pySuffix(name string) string {
	if i := strings.LastIndex(name, "."); i > 0 && i < len(name)-1 {
		return name[i:]
	}
	return ""
}

// collectFiles lists media files under root, skipping the OS litter
// directories, ordered like Python's sorted(Path) (component by component).
func collectFiles(root string) []string {
	var found []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != root && ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if mediaSuffixes[strings.ToLower(pySuffix(d.Name()))] {
			found = append(found, p)
		}
		return nil
	})
	sort.Slice(found, func(i, j int) bool {
		a, b := strings.Split(found[i], "/"), strings.Split(found[j], "/")
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
	return found
}

func readJSON(path string) map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	m := map[string]any{}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if d.Decode(&m) != nil || m == nil {
		return map[string]any{}
	}
	return m
}

// writeJSON replaces path atomically: a half-written index is worse than none.
func writeJSON(path string, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	_, err = f.Write(bytes.TrimRight(buf.Bytes(), "\n"))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// pyStr and pyList render JSON the way Python's json.dumps does (ensure_ascii,
// ", " separators), because the web app and operators have only ever seen that.
func pyStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r >= ' ' && r < 0x7f:
			b.WriteRune(r)
		case r > 0xffff:
			r1, r2 := utf16.EncodeRune(r)
			fmt.Fprintf(&b, `\u%04x\u%04x`, r1, r2)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func pyList(l []string) string {
	q := make([]string, len(l))
	for i, s := range l {
		q[i] = pyStr(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// isMount mirrors os.path.ismount: the path is on a different device than its parent.
func isMount(path string) bool {
	var a, b syscall.Stat_t
	if syscall.Lstat(path, &a) != nil || syscall.Lstat(filepath.Join(path, ".."), &b) != nil {
		return false
	}
	return a.Dev != b.Dev || a.Ino == b.Ino
}

func mountDev(mp, dev, opts string, lg logger, what string) bool {
	if os.MkdirAll(mp, 0o755) != nil {
		return false
	}
	if isMount(mp) {
		exec.Command("umount", mp).Run()
	}
	out, err := exec.Command("mount", "-o", opts, dev, mp).CombinedOutput()
	if err != nil {
		lg.logf("%s failed: %s", what, strings.TrimSpace(string(out)))
		return false
	}
	return true
}

func umountRetry(mp string, lg logger, warning string) {
	for i := 0; i < 5; i++ {
		if !isMount(mp) {
			return
		}
		if exec.Command("umount", mp).Run() == nil {
			return
		}
		time.Sleep(time.Second)
	}
	lg.logf("%s", warning)
}

// Led is the only feedback channel on a headless box. A zero Led does nothing.
type Led struct{ dir, saved string }

func newLed(sysDir string) *Led {
	for _, name := range []string{"ACT", "led0"} {
		d := filepath.Join(sysDir, name)
		if _, err := os.Stat(filepath.Join(d, "brightness")); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(d, "trigger"))
		if err != nil {
			return &Led{}
		}
		l := &Led{dir: d}
		for _, tok := range strings.Fields(string(raw)) {
			if strings.HasPrefix(tok, "[") {
				l.saved = strings.Trim(tok, "[]")
			}
		}
		if os.WriteFile(filepath.Join(d, "trigger"), []byte("none"), 0o644) != nil {
			return &Led{}
		}
		return l
	}
	return &Led{}
}

func (l *Led) set(on bool) {
	if l.dir != "" {
		os.WriteFile(filepath.Join(l.dir, "brightness"), []byte(map[bool]string{true: "1", false: "0"}[on]), 0o644)
	}
}

func (l *Led) blink(times int, on, off time.Duration) {
	if l.dir == "" {
		return
	}
	for i := 0; i < times; i++ {
		l.set(true)
		time.Sleep(on)
		l.set(false)
		time.Sleep(off)
	}
}

func (l *Led) restore() {
	if l.dir != "" && l.saved != "" {
		os.WriteFile(filepath.Join(l.dir, "trigger"), []byte(l.saved), 0o644)
	}
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }
