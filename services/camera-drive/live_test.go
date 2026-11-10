package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as camera-drive so integration tests can send it real signals.
func TestMain(m *testing.M) {
	if os.Getenv("CD_LIVE_HELPER") == "1" {
		os.Exit(cmdLive(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func TestFFmpegArgsGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/ffmpeg-argv-python.golden")
	if err != nil {
		t.Fatal(err)
	}
	rec := "/srv/camera-drive/video/2026-01-02_030405.mkv"
	live := "/run/camera-drive/live.jpg"
	got := map[string][]string{
		"preview":               ffmpegArgs("ffmpeg", "/dev/video7", "", "", live),
		"preview-audio-ignored": ffmpegArgs("ffmpeg", "/dev/video7", "hw:9,0", "", live),
		"record-noaudio":        ffmpegArgs("ffmpeg", "/dev/video7", "", rec, live),
		"record-audio":          ffmpegArgs("ffmpeg", "/dev/video7", "hw:9,0", rec, live),
	}
	want := map[string][]string{}
	var name string
	for _, l := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if n, ok := strings.CutPrefix(l, "== "); ok {
			name = n
		} else {
			want[name] = append(want[name], l)
		}
	}
	if len(want) != len(got) {
		t.Fatalf("golden has %d cases, test has %d", len(want), len(got))
	}
	for n, w := range want {
		if g := strings.Join(got[n], "\n"); g != strings.Join(w, "\n") {
			t.Errorf("%s argv differs from the Python original\n got: %v\nwant: %v", n, got[n], w)
		}
	}
}

func TestStateTable(t *testing.T) {
	cases := []struct {
		preview, rec bool
		want         recState
	}{
		{false, false, recState{Idle, false}},
		{true, false, recState{Live, false}},
		{false, true, recState{Live, true}}, // recording implies capturing
		{true, true, recState{Live, true}},
	}
	for _, c := range cases {
		if got := wanted(c.preview, c.rec); got != c.want {
			t.Errorf("wanted(%v,%v) = %v, want %v", c.preview, c.rec, got, c.want)
		}
	}
}

func TestPickDevices(t *testing.T) {
	const arecord = `**** List of CAPTURE Hardware Devices ****
card 0: vc4hdmi [vc4-hdmi], device 0: MAI PCM i2s-hifi-0 []
card 1: Headset [USB Headset], device 0: USB Audio [USB Audio]
card 2: C3200 [Cam 3200], device 0: USB Audio [USB Audio]`
	for in, want := range map[string]string{
		arecord: "hw:2,0", // card name beats an earlier USB card
		"card 1: Headset [USB Headset], device 0: USB Audio []": "hw:1,0", // USB fallback
		"card 0: vc4hdmi [vc4-hdmi], device 0: x []":            "",
		"": "",
	} {
		if got := pickAudio(in); got != want {
			t.Errorf("pickAudio(%q) = %q, want %q", in, got, want)
		}
	}
	for info, want := range map[string]bool{
		"Card type : Cam 3200\nDevice Caps : Video Capture, Streaming": true,
		"Card type : Slim 3200\nVideo Capture":                         true,
		"Card type : AIPTEK\nvideo capture":                            true,
		"Card type : Cam 3200\nVideo Output":                           false, // name without capture
		"Card type : bcm2835-codec\nVideo Capture":                     false, // capture without name
	} {
		if isCameraInfo(info) != want {
			t.Errorf("isCameraInfo(%q) = %v", info, !want)
		}
	}
}

func TestControlReadModifyWriteRace(t *testing.T) {
	c := liveCfg{Run: t.TempDir()}
	const n = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // the web app bumping the snapshot counter
		defer wg.Done()
		for i := 0; i < n; i++ {
			c.updateControl(func(m map[string]any) { m["snapshot_seq"] = ctlInt(m, "snapshot_seq") + 1 })
		}
	}()
	go func() { // camera-live clearing recording
		defer wg.Done()
		for i := 0; i < n; i++ {
			c.updateControl(func(m map[string]any) { m["recording"] = false; m["preview"] = true })
		}
	}()
	wg.Wait()
	m := readJSON(c.control())
	if ctlInt(m, "snapshot_seq") != n || !ctlBool(m, "preview") {
		t.Fatalf("lost an update: %v", m)
	}
}

// ---- integration with stub programs ------------------------------------------

const stubFFmpeg = `#!/bin/sh
# last arg is live.jpg; the arg after "-f matroska -y" is the recording
for a in "$@"; do prev2=$prev; prev=$last; last=$a; done
rec=""; while [ $# -gt 0 ]; do [ "$1" = matroska ] && rec=$3; shift; done
jpg=$last
printf '\377\330%02000d\377\331' 0 > "$jpg"
[ -n "$rec" ] && echo data > "$rec"
[ -n "$STUB_LOG" ] && echo "$STUB_NAME start" >> "$STUB_LOG"
fin() { [ -n "$rec" ] && echo finalized >> "$rec"; [ -n "$STUB_LOG" ] && echo "$1" >> "$STUB_LOG"; }
if [ -n "$STUB_IGNORE_INT" ]; then trap 'fin INT' INT; else trap 'fin INT; exit 0' INT; fi
if [ -n "$STUB_IGNORE_TERM" ]; then trap 'fin TERM' TERM; else trap 'fin TERM; exit 0' TERM; fi
while :; do sleep 0.05; done
`

const stubV4L2 = `#!/bin/sh
case "$2" in
  *video7) printf 'Card type : Cam 3200\nDevice Caps : Video Capture\n' ;;
  *) printf 'Card type : bcm2835-codec\nDevice Caps : Video Capture\n' ;;
esac
`

type rig struct {
	t                   *testing.T
	run, root, dev, log string
	args                []string
}

func newRig(t *testing.T) *rig {
	d := t.TempDir()
	r := &rig{t: t, run: d + "/run", root: d + "/srv", dev: d + "/dev", log: d + "/stub.log"}
	for _, p := range []string{r.run, r.root, r.dev} {
		os.MkdirAll(p, 0o755)
	}
	os.WriteFile(r.dev+"/video3", nil, 0o644)
	os.WriteFile(r.dev+"/video7", nil, 0o644)
	for name, body := range map[string]string{"ffmpeg": stubFFmpeg, "v4l2-ctl": stubV4L2,
		"arecord": "#!/bin/sh\necho 'card 1: C3200 [Cam 3200], device 0: USB Audio [USB Audio]'\n"} {
		os.WriteFile(d+"/"+name, []byte(body), 0o755)
	}
	r.args = []string{"-run-dir", r.run, "-root", r.root, "-dev-dir", r.dev,
		"-ffmpeg", d + "/ffmpeg", "-v4l2-ctl", d + "/v4l2-ctl", "-arecord", d + "/arecord",
		"-poll", "30ms", "-settle", "150ms", "-int-wait", "300ms", "-term-wait", "300ms",
		"-detect-interval", "5ms", "-min-free", "1"}
	t.Setenv("STUB_LOG", r.log)
	return r
}

func (r *rig) start(extra ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], append(r.args, extra...)...)
	cmd.Env = append(os.Environ(), "CD_LIVE_HELPER=1")
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	return cmd
}

func (r *rig) ctl(k string, v any) {
	if err := (liveCfg{Run: r.run}).updateControl(func(m map[string]any) { m[k] = v }); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) status() map[string]any { return readJSON(r.root + "/recstatus.json") }

func (r *rig) waitFor(what string, f func() bool) {
	r.t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	r.t.Fatalf("timed out waiting for %s; status=%v", what, r.status())
}

func (r *rig) state(s string, rec bool) func() bool {
	return func() bool { st := r.status(); return st["state"] == s && st["recording"] == rec }
}

func TestLivePreviewSnapshotRecord(t *testing.T) {
	r := newRig(t)
	cmd := r.start()
	r.waitFor("idle", r.state("idle", false))
	if st := r.status(); st["audio"] != true {
		t.Errorf("audio not detected: %v", st)
	}
	if _, err := os.Stat(r.run + "/live.jpg"); err == nil {
		t.Error("ffmpeg ran while idle")
	}

	r.ctl("preview", true)
	r.waitFor("live", r.state("live", false))
	frame, err := os.ReadFile(r.run + "/live.jpg")
	if err != nil || len(frame) < 1024 || frame[len(frame)-2] != 0xff || frame[len(frame)-1] != 0xd9 {
		t.Fatalf("live.jpg not a complete JPEG: %v len %d", err, len(frame))
	}

	r.ctl("snapshot_seq", 1)
	r.waitFor("snapshot", func() bool { return r.status()["last_snapshot"] != nil })
	snaps, _ := filepath.Glob(r.root + "/media/snapshots/SNAP_*.jpg")
	if len(snaps) != 1 {
		t.Fatalf("snapshots: %v", snaps)
	}
	if b, _ := os.ReadFile(snaps[0]); string(b) != string(frame) {
		t.Error("snapshot is not a verbatim copy of live.jpg")
	}
	if left, _ := filepath.Glob(r.root + "/media/snapshots/*.part"); len(left) != 0 {
		t.Errorf(".part left behind: %v", left)
	}

	r.ctl("recording", true)
	r.waitFor("recording", r.state("live", true))
	rec := r.root + "/video/" + r.status()["file"].(string)
	if _, err := os.Stat(rec); err != nil || !strings.HasSuffix(rec, ".mkv") {
		t.Fatalf("recording file: %v", err)
	}

	r.ctl("recording", false)
	r.waitFor("recording stop", r.state("live", false))
	if b, _ := os.ReadFile(rec); !strings.Contains(string(b), "finalized") {
		t.Error("recording was not stopped with SIGINT")
	}

	r.ctl("preview", false)
	r.waitFor("idle again", r.state("idle", false))
	if _, err := os.Stat(r.run + "/live.jpg"); err == nil {
		t.Error("stale live.jpg left after sensor idle")
	}
	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()
}

func TestLiveSigtermMidRecording(t *testing.T) {
	r := newRig(t)
	cmd := r.start()
	r.waitFor("idle", r.state("idle", false))
	r.ctl("recording", true)
	r.waitFor("recording", r.state("live", true))
	rec := r.root + "/video/" + r.status()["file"].(string)
	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if b, _ := os.ReadFile(rec); !strings.Contains(string(b), "finalized") {
		t.Error("ffmpeg did not get to finalise the file")
	}
	if st := r.status(); st["state"] != "off" || st["recording"] != false {
		t.Errorf("final status: %v", st)
	}
	if _, err := os.Stat(r.run + "/live.jpg"); err == nil {
		t.Error("live.jpg left behind")
	}
}

func TestLiveNoDevice(t *testing.T) {
	r := newRig(t)
	os.Remove(r.dev + "/video7")
	cmd := r.start()
	if ee, ok := cmd.Wait().(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Fatalf("want exit 1, got %v", ee)
	}
	if b, _ := os.ReadFile(r.root + "/record.log"); !strings.Contains(string(b), "no camera video device found") {
		t.Errorf("log: %s", b)
	}
	if r.status()["state"] != "off" {
		t.Errorf("status: %v", r.status())
	}
}

func TestLiveLowDiskRefusesRecording(t *testing.T) {
	r := newRig(t)
	cmd := r.start("-min-free", "9223372036854775807")
	r.waitFor("idle", r.state("idle", false))
	r.ctl("recording", true)
	r.waitFor("refusal", func() bool { return r.status()["last_error"] == "card nearly full" })
	r.waitFor("control cleared", func() bool { return !ctlBool(readJSON(r.run+"/control.json"), "recording") })
	if st := r.status(); st["state"] != "idle" {
		t.Errorf("status: %v", st)
	}
	if files, _ := filepath.Glob(r.root + "/video/*"); len(files) != 0 {
		t.Errorf("recorded despite low disk: %v", files)
	}
	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()
}

func TestStopEscalates(t *testing.T) {
	for _, c := range []struct{ env, want string }{
		{"STUB_IGNORE_INT", "INT\nTERM\n"},
		{"STUB_IGNORE_TERM", "INT\nTERM\n"}, // ignores TERM too, so only KILL ends it; plus INT ignored below
	} {
		r := newRig(t)
		t.Setenv("STUB_IGNORE_INT", "1")
		if c.env == "STUB_IGNORE_TERM" {
			t.Setenv(c.env, "1")
		} else {
			t.Setenv("STUB_IGNORE_TERM", "")
		}
		p, err := startProc(ffmpegArgs(r.run+"/../ffmpeg", "/dev/video7", "", "", r.run+"/live.jpg"))
		if err != nil {
			t.Fatal(err)
		}
		r.waitFor("stub up", func() bool { _, err := os.Stat(r.log); return err == nil })
		t0 := time.Now()
		p.stop(300*time.Millisecond, 300*time.Millisecond)
		if !p.exited() {
			t.Fatal("still running after stop")
		}
		if b, _ := os.ReadFile(r.log); !strings.HasSuffix(string(b), c.want) {
			t.Errorf("%s: signals seen:\n%s", c.env, b)
		}
		if d := time.Since(t0); c.env == "STUB_IGNORE_TERM" && d < 550*time.Millisecond {
			t.Errorf("KILL came after %v, before INT and TERM waits elapsed", d)
		}
	}
}
