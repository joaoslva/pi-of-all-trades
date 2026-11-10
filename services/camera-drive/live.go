package main

// camera-drive live owns the camera while it is in live mode. Only one process
// can hold /dev/video0, so one ffmpeg reads the camera once and fans out to an
// optional Matroska recording and to live.jpg (both -c:v copy, so the preview
// costs nothing and a snapshot is the camera's own JPEG). The web app talks to
// it through files in the run dir: control.json in, live.jpg and recstatus.json out.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	liveWidth, liveHeight = 464, 480
	startRetries          = 4
)

var cameraNameHints = []string{"cam 3200", "slim 3200", "aiptek"}

// liveCfg holds everything environment-dependent so tests can use stubs and temp dirs.
type liveCfg struct {
	Run, Root, FFmpeg, V4L2, Arecord, DevDir string
	MinFree                                  int64
	Poll, Settle, IntWait, TermWait, Detect  time.Duration
}

func (c liveCfg) liveJPG() string    { return filepath.Join(c.Run, "live.jpg") }
func (c liveCfg) control() string    { return filepath.Join(c.Run, "control.json") }
func (c liveCfg) videoDir() string   { return filepath.Join(c.Root, "video") }
func (c liveCfg) snapDir() string    { return filepath.Join(c.Root, "media", "snapshots") }
func (c liveCfg) statusPath() string { return filepath.Join(c.Root, "recstatus.json") }

func (c liveCfg) logf(format string, a ...any) {
	line := time.Now().UTC().Format("2006-01-02T15:04:05+00:00") + " [live] " + fmt.Sprintf(format, a...)
	fmt.Println(line)
	if f, err := os.OpenFile(filepath.Join(c.Root, "record.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666); err == nil {
		fmt.Fprintln(f, line)
		f.Close()
	}
}

func liveFlags(fs *flag.FlagSet) *liveCfg {
	c := &liveCfg{}
	fs.StringVar(&c.Run, "run-dir", "/run/camera-drive", "runtime directory (control.json, live.jpg)")
	fs.StringVar(&c.Root, "root", "/srv/camera-drive", "data directory")
	fs.StringVar(&c.FFmpeg, "ffmpeg", "ffmpeg", "ffmpeg program")
	fs.StringVar(&c.V4L2, "v4l2-ctl", "v4l2-ctl", "v4l2-ctl program")
	fs.StringVar(&c.Arecord, "arecord", "arecord", "arecord program")
	fs.StringVar(&c.DevDir, "dev-dir", "/dev", "directory holding video* nodes")
	fs.Int64Var(&c.MinFree, "min-free", 512<<20, "refuse to record under this many free bytes")
	fs.DurationVar(&c.Poll, "poll", 400*time.Millisecond, "control.json poll interval")
	fs.DurationVar(&c.Settle, "settle", 4*time.Second, "how long ffmpeg must stay up to count as started")
	fs.DurationVar(&c.IntWait, "int-wait", 15*time.Second, "wait after SIGINT before SIGTERM")
	fs.DurationVar(&c.TermWait, "term-wait", 2*time.Second, "wait after SIGTERM before SIGKILL")
	fs.DurationVar(&c.Detect, "detect-interval", 500*time.Millisecond, "wait between device discovery attempts")
	return c
}

// ---- ffmpeg arguments -------------------------------------------------------

// ffmpegArgs is the one place the command line is built; testdata holds the Python original's output.
func ffmpegArgs(bin, video, audio, rec, liveJPG string) []string {
	// No -input_format: ffmpeg has no name for this driver's JPEG fourcc.
	// No -use_wallclock_as_timestamps: it yields non-monotonic DTS here.
	a := []string{bin, "-hide_banner", "-loglevel", "warning", "-nostdin",
		"-f", "v4l2", "-video_size", fmt.Sprintf("%dx%d", liveWidth, liveHeight), "-i", video}
	if audio != "" && rec != "" {
		// -channels, not -ac: -ac never reaches the ALSA demuxer and a mono-only mic then fails.
		a = append(a, "-f", "alsa", "-channels", "1", "-sample_rate", "48000",
			"-thread_queue_size", "1024", "-i", audio)
	}
	if rec != "" {
		a = append(a, "-map", "0:v", "-c:v", "copy")
		if audio != "" {
			// Aligns the v4l2 and ALSA clocks; without it the file claims a duration several times too long.
			a = append(a, "-map", "1:a", "-af", "aresample=async=1:first_pts=0", "-c:a", "pcm_s16le")
		}
		// Matroska, not AVI: recordings usually end by the power being cut.
		a = append(a, "-f", "matroska", "-y", rec)
	}
	return append(a, "-map", "0:v", "-c:v", "copy", "-f", "image2", "-update", "1", "-y", liveJPG)
}

// ---- device discovery -------------------------------------------------------

func isCameraInfo(info string) bool {
	info = strings.ToLower(info)
	if !strings.Contains(info, "capture") {
		return false
	}
	for _, h := range cameraNameHints {
		if strings.Contains(info, h) {
			return true
		}
	}
	return false
}

var (
	cardRe    = regexp.MustCompile(`^card (\d+): (\S+)`)
	cardUSBRe = regexp.MustCompile(`^card (\d+): (\S+).*USB`)
)

// pickAudio chooses by card name, then falls back to any USB card.
func pickAudio(arecordList string) string {
	lines := strings.Split(arecordList, "\n")
	for _, l := range lines {
		if m := cardRe.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			n := strings.ToLower(m[2])
			for _, h := range []string{"c3200", "cam3200", "aiptek"} {
				if strings.Contains(n, h) {
					return "hw:" + m[1] + ",0"
				}
			}
		}
	}
	for _, l := range lines {
		if m := cardUSBRe.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			return "hw:" + m[1] + ",0"
		}
	}
	return ""
}

func runOut(timeout time.Duration, name string, args ...string) string {
	cmd := exec.Command(name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if cmd.Start() != nil {
		return ""
	}
	t := time.AfterFunc(timeout, func() { cmd.Process.Kill() })
	cmd.Wait()
	t.Stop()
	return buf.String()
}

// findVideo matches on card name, never number: the Pi has a dozen codec/ISP nodes of its own.
func (c liveCfg) findVideo() string {
	nodes, _ := filepath.Glob(filepath.Join(c.DevDir, "video*"))
	num := func(s string) int { n, _ := strconv.Atoi(strings.TrimLeft(filepath.Base(s), "video")); return n }
	sort.Slice(nodes, func(i, j int) bool { return num(nodes[i]) < num(nodes[j]) })
	for _, n := range nodes {
		if isCameraInfo(runOut(5*time.Second, c.V4L2, "-d", n, "--info")) {
			return n
		}
	}
	return ""
}

func (c liveCfg) findAudio() string { return pickAudio(runOut(5*time.Second, c.Arecord, "-l")) }

func (c liveCfg) freeBytes() int64 {
	var st syscall.Statfs_t
	if syscall.Statfs(c.Root, &st) != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// ---- control.json (two writers: this process and the web app) ----------------

// updateControl is a read-modify-write under an flock, replaced atomically,
// so a snapshot bump from the web app and our own recording:false cannot lose each other.
func (c liveCfg) updateControl(fn func(m map[string]any)) error {
	lk, err := os.OpenFile(filepath.Join(c.Run, "control.lock"), os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return err
	}
	defer lk.Close()
	if err := syscall.Flock(int(lk.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	m := readJSON(c.control())
	fn(m)
	return writeJSON(c.control(), m)
}

func ctlBool(m map[string]any, k string) bool {
	switch v := m[k].(type) {
	case bool:
		return v
	case json.Number:
		f, _ := v.Float64()
		return f != 0
	}
	return false
}

func ctlInt(m map[string]any, k string) int64 {
	if v, ok := m[k].(json.Number); ok {
		if n, err := v.Int64(); err == nil {
			return n
		}
		f, _ := v.Float64()
		return int64(f)
	}
	return 0
}

// ---- state machine ----------------------------------------------------------

type phase int

const (
	Off phase = iota
	Idle
	Live
	Error
)

func (p phase) String() string { return [...]string{"off", "idle", "live", "error"}[p] }

// recState: recording is only meaningful on Live.
type recState struct {
	Phase     phase
	Recording bool
}

// wanted maps a control request to the state we should be in. Recording implies capturing.
func wanted(preview, recording bool) recState {
	if recording {
		return recState{Live, true}
	}
	if preview {
		return recState{Live, false}
	}
	return recState{Idle, false}
}

// ---- ffmpeg process ---------------------------------------------------------

type tailBuf struct{ b []byte }

func (t *tailBuf) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 8192 {
		t.b = t.b[len(t.b)-8192:]
	}
	return len(p), nil
}

type proc struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  tailBuf
}

func startProc(argv []string) (*proc, error) {
	p := &proc{cmd: exec.Command(argv[0], argv[1:]...), done: make(chan struct{})}
	p.cmd.Stderr = &p.err
	if err := p.cmd.Start(); err != nil {
		return nil, err
	}
	go func() { p.cmd.Wait(); close(p.done) }()
	return p, nil
}

func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *proc) waitFor(d time.Duration) bool {
	select {
	case <-p.done:
		return true
	case <-time.After(d):
		return false
	}
}

// stop: SIGINT lets ffmpeg finalise the Matroska file, which takes real time; then TERM, then KILL.
func (p *proc) stop(intWait, termWait time.Duration) {
	if p == nil || p.exited() {
		return
	}
	p.cmd.Process.Signal(syscall.SIGINT)
	if p.waitFor(intWait) {
		return
	}
	p.cmd.Process.Signal(syscall.SIGTERM)
	if p.waitFor(termWait) {
		return
	}
	p.cmd.Process.Kill()
	<-p.done
}

// ---- recorder ----------------------------------------------------------------

type recorder struct {
	cfg          liveCfg
	video, audio string
	st           recState
	p            *proc
	dest         string // current recording path, "" when not recording
	seenSeq      int64
	stopping     atomic.Bool
	stopCh       chan struct{}
}

func (r *recorder) setStatus(kv map[string]any) {
	cur := readJSON(r.cfg.statusPath())
	for k, v := range kv {
		cur[k] = v
	}
	cur["updated"] = time.Now().UTC().Format("2006-01-02T15:04:05+00:00")
	if err := writeJSON(r.cfg.statusPath(), cur); err != nil {
		r.cfg.logf("could not write status: %v", err)
	}
}

func (r *recorder) sleep(d time.Duration) {
	select {
	case <-time.After(d):
	case <-r.stopCh:
	}
}

// start spawns ffmpeg and treats it as running once it survives the settle period.
func (r *recorder) start(rec string) bool {
	argv := ffmpegArgs(r.cfg.FFmpeg, r.video, r.audio, rec, r.cfg.liveJPG())
	if rec != "" {
		r.cfg.logf("starting ffmpeg (recording=yes -> %s)", filepath.Base(rec))
	} else {
		r.cfg.logf("starting ffmpeg (recording=no)")
	}
	for attempt := 1; attempt <= startRetries; attempt++ {
		p, err := startProc(argv)
		if err != nil {
			r.cfg.logf("start attempt %d/%d: %v", attempt, startRetries, err)
		} else {
			r.p = p
			if !p.waitFor(r.cfg.Settle) {
				return true
			}
			var tail []string
			for _, l := range strings.Split(strings.TrimSpace(string(p.err.b)), "\n") {
				if l != "" && !strings.Contains(l, "QBUF") {
					tail = append(tail, l)
				}
			}
			if len(tail) > 3 {
				tail = tail[len(tail)-3:]
			}
			if len(tail) == 0 {
				tail = []string{"(no output)"}
			}
			r.cfg.logf("start attempt %d/%d: %s", attempt, startRetries, strings.Join(tail, " | "))
		}
		if attempt < startRetries {
			r.sleep(2 * time.Second)
		}
	}
	r.st.Phase = Error
	r.setStatus(map[string]any{"state": Error.String(), "last_error": "ffmpeg would not start"})
	return false
}

func (r *recorder) stopFFmpeg() { r.p.stop(r.cfg.IntWait, r.cfg.TermWait) }

func (r *recorder) finished() {
	if st, err := os.Stat(r.dest); r.dest != "" && err == nil {
		r.cfg.logf("finished %s (%d bytes)", filepath.Base(r.dest), st.Size())
	}
}

// snapshot copies the newest preview frame, the camera's own JPEG, via .part so readers never see half a photo.
func (r *recorder) snapshot() any {
	b, err := os.ReadFile(r.cfg.liveJPG())
	if err != nil || len(b) < 1024 {
		r.cfg.logf("snapshot requested but no live frame available")
		return nil
	}
	os.MkdirAll(r.cfg.snapDir(), 0o755)
	name := time.Now().Format("SNAP_2006-01-02_150405.jpg")
	dest := filepath.Join(r.cfg.snapDir(), name)
	if os.WriteFile(dest+".part", b, 0o666) != nil || os.Rename(dest+".part", dest) != nil {
		r.cfg.logf("snapshot %s failed", name)
		return nil
	}
	r.cfg.logf("snapshot %s (%d bytes)", name, len(b))
	return name
}

func (r *recorder) newRecPath() string {
	return filepath.Join(r.cfg.videoDir(), time.Now().Format("2006-01-02_150405")+".mkv")
}

func (r *recorder) publish() {
	var file any
	if r.dest != "" {
		file = filepath.Base(r.dest)
	}
	r.setStatus(map[string]any{"state": r.st.Phase.String(), "recording": r.st.Recording, "file": file, "last_error": nil})
}

// tick runs one poll; false means the loop must end.
func (r *recorder) tick() bool {
	c := r.cfg
	capturing := r.st.Phase == Live
	if capturing && r.p.exited() {
		if _, err := os.Stat(r.video); err != nil {
			c.logf("camera disconnected")
			r.setStatus(map[string]any{"state": Error.String(), "last_error": "camera disconnected"})
			return false
		}
		c.logf("ffmpeg exited unexpectedly, restarting")
		r.finished()
		r.dest = ""
		if r.st.Recording {
			r.dest = r.newRecPath() // reusing the old name would -y overwrite what was recorded
		}
		if !r.start(r.dest) {
			return false
		}
		r.publish()
		return true
	}
	if !capturing {
		if _, err := os.Stat(r.video); err != nil {
			c.logf("camera disconnected while idle")
			return false
		}
	}

	ctl := readJSON(c.control())
	if seq := ctlInt(ctl, "snapshot_seq"); seq != r.seenSeq {
		r.seenSeq = seq
		if capturing {
			r.setStatus(map[string]any{"last_snapshot": r.snapshot()})
		} else {
			c.logf("snapshot ignored: viewfinder is off")
		}
	}

	want := wanted(ctlBool(ctl, "preview"), ctlBool(ctl, "recording"))
	if want.Recording && !r.st.Recording && c.freeBytes() < c.MinFree {
		c.logf("refusing to record: card nearly full")
		r.setStatus(map[string]any{"last_error": "card nearly full"})
		c.updateControl(func(m map[string]any) { m["recording"] = false })
		return true
	}
	if want == r.st {
		return true
	}

	// Any change to the output set means restarting ffmpeg.
	r.stopFFmpeg()
	r.finished()
	r.dest = ""
	if want.Recording {
		r.dest = r.newRecPath()
	}
	if want.Phase == Live {
		r.st = want
		if !r.start(r.dest) {
			return false
		}
	} else {
		os.Remove(c.liveJPG()) // a frozen frame must not look live
		c.logf("sensor idle")
	}
	r.st = want
	r.publish()
	return true
}

func cmdLive(args []string) int {
	fs := flag.NewFlagSet("live", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := liveFlags(fs)
	if fs.Parse(args) != nil {
		fmt.Fprintln(os.Stderr, "usage: camera-drive live [flags]")
		return 2
	}
	c := *cfg
	r := &recorder{cfg: c, stopCh: make(chan struct{})}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sig
		c.logf("got signal %d, shutting down", s.(syscall.Signal))
		r.stopping.Store(true)
		close(r.stopCh)
	}()

	os.MkdirAll(c.Run, 0o755)
	os.MkdirAll(c.videoDir(), 0o755)

	// udev fires when the USB device appears; the V4L2 node and ALSA card follow a beat later.
	for i := 0; i < 20 && r.video == ""; i++ {
		if r.video = c.findVideo(); r.video == "" {
			time.Sleep(c.Detect)
		}
	}
	if r.video == "" {
		c.logf("no camera video device found")
		r.setStatus(map[string]any{"state": Off.String(), "last_error": "no V4L2 capture device"})
		return 1
	}
	for i := 0; i < 6 && r.audio == ""; i++ {
		if r.audio = c.findAudio(); r.audio == "" {
			time.Sleep(c.Detect)
		}
	}
	audio := r.audio
	if audio == "" {
		audio = "none"
	}
	c.logf("video=%s audio=%s", r.video, audio)

	// Start idle: no ffmpeg until someone asks for the viewfinder or record.
	c.updateControl(func(m map[string]any) {
		for k := range m {
			delete(m, k)
		}
		m["preview"], m["recording"], m["snapshot_seq"] = false, false, 0
	})
	os.Remove(c.liveJPG())
	r.st = recState{Idle, false}
	r.setStatus(map[string]any{"state": "idle", "recording": false, "file": nil, "audio": r.audio != "", "last_error": nil})

	for !r.stopping.Load() {
		r.sleep(c.Poll)
		if r.stopping.Load() || !r.tick() {
			break
		}
	}

	r.stopFFmpeg()
	r.finished()
	os.Remove(c.liveJPG())
	r.setStatus(map[string]any{"state": Off.String(), "recording": false, "file": nil})
	c.logf("stopped")
	return 0
}
