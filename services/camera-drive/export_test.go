package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFFmpegAttempts(t *testing.T) {
	want := map[string][][]string{
		"mp4": {
			{"-v", "error", "-i", "in.mkv", "-c:v", "h264_v4l2m2m", "-b:v", "1500k", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", "-f", "mp4", "-y", "o.part"},
			{"-v", "error", "-i", "in.mkv", "-c:v", "libx264", "-preset", "veryfast", "-crf", "26", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", "-f", "mp4", "-y", "o.part"},
		},
		"mov": {{"-v", "error", "-i", "in.mkv", "-c", "copy", "-movflags", "+faststart", "-f", "mov", "-y", "o.part"}},
		"webm": {{"-v", "error", "-i", "in.mkv", "-c:v", "libvpx-vp9", "-deadline", "realtime", "-cpu-used", "8", "-row-mt", "1", "-b:v", "1000k",
			"-pix_fmt", "yuv420p", "-c:a", "libopus", "-b:a", "96k", "-f", "webm", "-y", "o.part"}},
		"mp3": {{"-v", "error", "-i", "in.mkv", "-vn", "-c:a", "libmp3lame", "-q:a", "3", "-f", "mp3", "-y", "o.part"}},
	}
	for format, w := range want {
		if got := ffmpegAttempts(format, "in.mkv", "o.part"); !reflect.DeepEqual(got, w) {
			t.Errorf("%s:\n got %q\nwant %q", format, got, w)
		}
	}
}

// fake writes a stand-in ffmpeg and ffprobe. ffmpeg logs its argv, notes when
// two runs overlap, fails when failOn appears in its arguments, and writes
// 2000 bytes to its last argument after sleeping; "exec" replaces the shell so a kill is final.
func fake(t *testing.T, sleep, failOn string) (webCfg, string) {
	t.Helper()
	c, _ := webFixture(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %[1]s
mkdir %[1]s.busy 2>/dev/null || echo overlap >> %[1]s.overlap
case "$*" in *%[3]q*) rmdir %[1]s.busy; exit 1;; esac
for a; do out=$a; done
sleep %[2]s
head -c 2000 /dev/zero > "$out"
rmdir %[1]s.busy
`, log, sleep, failOn)
	put(t, filepath.Join(dir, "ffmpeg"), script)
	put(t, filepath.Join(dir, "ffprobe"), "#!/bin/sh\necho 1.0\n")
	os.Chmod(filepath.Join(dir, "ffmpeg"), 0o755)
	os.Chmod(filepath.Join(dir, "ffprobe"), 0o755)
	c.FFmpeg, c.FFProbe = filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "ffprobe")
	c.exp = newExporter(c)
	put(t, filepath.Join(c.Root, "video/2026-01-04_100000.mkv"), "v1")
	put(t, filepath.Join(c.Root, "video/2026-01-04_110000.mkv"), "v2")
	return c, log
}

func logLines(log string) []string {
	b, _ := os.ReadFile(log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func waitState(t *testing.T, e *exporter, id, want string) map[string]any {
	t.Helper()
	for i := 0; i < 300; i++ {
		if j := e.get(id); j != nil {
			if s := j.status(); s["state"] == want {
				return s
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s never reached %s", id, want)
	return nil
}

func TestMP4FallbackSequence(t *testing.T) {
	c, log := fake(t, "0", "h264_v4l2m2m")
	src := filepath.Join(c.Root, "video/2026-01-04_100000.mkv")
	dest := c.exp.ensureMP4(context.Background(), src, "video/2026-01-04_100000.mkv", nil)
	if dest != filepath.Join(c.Root, "proxy/2026-01-04_100000.mp4") || !fileExists(dest) {
		t.Fatalf("dest %q", dest)
	}
	part := strings.TrimSuffix(dest, ".mp4") + ".part"
	var got []string
	got = logLines(log)
	if len(got) != 2 || !strings.Contains(got[0], "h264_v4l2m2m") || !strings.Contains(got[1], "libx264") ||
		!strings.HasSuffix(got[1], "-y "+part) {
		t.Errorf("attempts %q", got)
	}
	if fileExists(part) {
		t.Error(".part left behind")
	}
	// A second call is a cache hit: no more ffmpeg runs.
	c.exp.ensureMP4(context.Background(), src, "video/2026-01-04_100000.mkv", nil)
	if n := len(logLines(log)); n != 2 {
		t.Errorf("cache miss, %d runs", n)
	}
}

func TestJobStateTable(t *testing.T) {
	ok := map[[2]jobState]bool{
		{jobQueued, jobRunning}: true, {jobQueued, jobCancelled}: true,
		{jobRunning, jobDone}: true, {jobRunning, jobFailed}: true, {jobRunning, jobCancelled}: true,
	}
	for from := jobQueued; from <= jobCancelled; from++ {
		for to := jobQueued; to <= jobCancelled; to++ {
			j := &exportJob{state: from}
			if got := j.move(to); got != ok[[2]jobState{from, to}] {
				t.Errorf("%d -> %d: %v", from, to, got)
			}
		}
	}
}

func pidsOf(c webCfg) string {
	out, _ := exec.Command("pgrep", "-f", c.FFmpeg).Output()
	return strings.TrimSpace(string(out))
}

func TestCancelRunningKillsFFmpegAndRemovesFiles(t *testing.T) {
	c, _ := fake(t, "30", "x-never")
	h := c.handler()
	rec := doReq(h, "POST", "/api/export", strings.NewReader(`{"format":"mp4","paths":["video/2026-01-04_100000.mkv"]}`))
	var r struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &r)
	for i := 0; pidsOf(c) == ""; i++ {
		if i > 100 {
			t.Fatal("ffmpeg never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s := c.exp.get(r.ID).status(); s["state"] != "running" {
		t.Errorf("state %v", s["state"])
	}
	doReq(h, "DELETE", "/api/export/"+r.ID, nil)
	waitState(t, c.exp, r.ID, "cancelled")
	for i := 0; pidsOf(c) != "" || fileExists(c.exp.workdir(r.ID)); i++ {
		if i > 150 {
			t.Fatalf("ffmpeg %q or workdir still there", pidsOf(c))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if fileExists(filepath.Join(c.Root, "proxy/2026-01-04_100000.part")) || fileExists(filepath.Join(c.Root, "proxy/2026-01-04_100000.mp4")) {
		t.Error("partial output left in proxy/")
	}
	// A second DELETE removes the cancelled record.
	doReq(h, "DELETE", "/api/export/"+r.ID, nil)
	if rec := doReq(h, "GET", "/api/export/"+r.ID, nil); rec.Code != 404 {
		t.Errorf("after remove: %d", rec.Code)
	}
}

func TestCancelWhileQueued(t *testing.T) {
	c, log := fake(t, "0", "x-never")
	c.exp.lock <- struct{}{} // another encode holds the lock
	j := c.exp.start("webm", []exportItem{{"video/2026-01-04_100000.mkv", filepath.Join(c.Root, "video/2026-01-04_100000.mkv")}})
	time.Sleep(100 * time.Millisecond)
	if s := j.status(); s["state"] != "running" || j.state != jobQueued {
		t.Fatalf("want queued (shown as running), got %v / %d", s["state"], j.state)
	}
	j.cancel(time.Now())
	waitState(t, c.exp, j.ID, "cancelled")
	for i := 0; fileExists(c.exp.workdir(j.ID)); i++ {
		if i > 100 {
			t.Fatal("workdir not removed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	<-c.exp.lock
	if fileExists(log) {
		t.Error("ffmpeg ran for a job cancelled in the queue")
	}
}

func TestTTLWithFakeClock(t *testing.T) {
	c, _ := fake(t, "0", "x-never")
	now := time.Unix(1_000_000, 0)
	c.exp.now = func() time.Time { return now }
	photo := filepath.Join(c.Root, "media/2026-01-02_030405/IMG_0001.JPG")
	mk := func() *exportJob {
		j := c.exp.start("mp4", []exportItem{{"media/2026-01-02_030405/IMG_0001.JPG", photo}})
		waitState(t, c.exp, j.ID, "done")
		return j
	}
	a := mk()
	now = now.Add(exportTTL - time.Second)
	if c.exp.get(a.ID) == nil {
		t.Fatal("expired before the finish TTL")
	}
	now = now.Add(2 * time.Second)
	if c.exp.get(a.ID) != nil || fileExists(c.exp.workdir(a.ID)) {
		t.Error("finished job outlived the finish TTL")
	}
	// A fetched job goes 300 s after the fetch, not 1800 s after finishing.
	c2, _ := fake(t, "0", "x-never")
	c2.exp.now = func() time.Time { return now }
	j := c2.exp.start("mp4", []exportItem{{"media/2026-01-02_030405/IMG_0001.JPG", photo}})
	waitState(t, c2.exp, j.ID, "done")
	if rec := doReq(c2.handler(), "GET", "/api/export/"+j.ID+"/file", nil); rec.Code != 200 {
		t.Fatalf("file %d", rec.Code)
	}
	now = now.Add(exportFetchedTTL - time.Second)
	if c2.exp.get(j.ID) == nil {
		t.Fatal("expired before the after-fetch TTL")
	}
	now = now.Add(2 * time.Second)
	if c2.exp.get(j.ID) != nil {
		t.Error("fetched job outlived the after-fetch TTL")
	}
}

func TestPathCapAndStatusShape(t *testing.T) {
	c, _ := fake(t, "0", "x-never")
	h := c.handler()
	paths := make([]string, 600)
	for i := range paths {
		paths[i] = "media/2026-01-02_030405/IMG_0001.JPG"
	}
	body, _ := json.Marshal(map[string]any{"format": "mp4", "paths": paths})
	var r struct{ ID string }
	json.Unmarshal(doReq(h, "POST", "/api/export", bytes.NewReader(body)).Body.Bytes(), &r)
	s := waitState(t, c.exp, r.ID, "done")
	if s["total"] != 500 || s["done"] != 500 {
		t.Errorf("total %v done %v", s["total"], s["done"])
	}
	for _, bad := range []string{`{"format":"gif","paths":["a"]}`, `{"format":"mp4","paths":[]}`, `{"format":"mp4"}`, `nope`} {
		if rec := doReq(h, "POST", "/api/export", strings.NewReader(bad)); rec.Code != 400 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	if rec := doReq(h, "POST", "/api/export", strings.NewReader(`{"format":"mp4","paths":["../index.json"]}`)); rec.Code != 404 {
		t.Errorf("traversal: %d", rec.Code)
	}
}

func TestExportOverHTTPSinglePhotoAndZipClash(t *testing.T) {
	c, _ := fake(t, "0", "x-never")
	h := c.handler()
	put(t, filepath.Join(c.Root, "media/other/IMG_0001.JPG"), "abc")
	start := func(paths string) string {
		rec := doReq(h, "POST", "/api/export", strings.NewReader(`{"format":"mp3","paths":`+paths+`}`))
		var r struct {
			OK bool
			ID string
		}
		json.Unmarshal(rec.Body.Bytes(), &r)
		if !r.OK || len(r.ID) != 16 {
			t.Fatalf("start: %d %s", rec.Code, rec.Body)
		}
		return r.ID
	}
	id := start(`["media/2026-01-02_030405/IMG_0001.JPG"]`)
	waitState(t, c.exp, id, "done")
	want := `{"current":null,"done":1,"error":null,"failed":[],"name":"IMG_0001.JPG","size":5,"state":"done","total":1}`
	if got := strings.TrimSpace(doReq(h, "GET", "/api/export/"+id, nil).Body.String()); !jsonEq(got, want) {
		t.Errorf("status %s", got)
	}
	rec := doReq(h, "GET", "/api/export/"+id+"/file", nil)
	if rec.Code != 200 || rec.Body.String() != "12345" || rec.Header().Get("Content-Disposition") != `attachment; filename=IMG_0001.JPG` {
		t.Errorf("file %d %q %q", rec.Code, rec.Body, rec.Header())
	}

	id = start(`["media/2026-01-02_030405/IMG_0001.JPG","media/other/IMG_0001.JPG","media/odd/a.png"]`)
	s := waitState(t, c.exp, id, "done")
	if name, _ := s["name"].(string); !strings.HasPrefix(name, "camera-drive_") || !strings.HasSuffix(name, "_mp3.zip") {
		t.Errorf("zip name %v", s["name"])
	}
	rec = doReq(h, "GET", "/api/export/"+id+"/file", nil)
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.Method != zip.Store {
			t.Errorf("%s is compressed", f.Name)
		}
	}
	if !reflect.DeepEqual(names, []string{"2026-01-02_030405_IMG_0001.JPG", "other_IMG_0001.JPG", "a.png"}) {
		t.Errorf("zip names %q", names)
	}
}

func TestExportFileNotReadyIs409AndMP4Route(t *testing.T) {
	c, _ := fake(t, "0.3", "x-never")
	h := c.handler()
	rec := doReq(h, "POST", "/api/export", strings.NewReader(`{"format":"webm","paths":["video/2026-01-04_100000.mkv"]}`))
	var r struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &r)
	if rec := doReq(h, "GET", "/api/export/"+r.ID+"/file", nil); rec.Code != 409 {
		t.Errorf("early file: %d", rec.Code)
	}
	if rec := doReq(h, "GET", "/api/export/nope", nil); rec.Code != 404 {
		t.Errorf("unknown: %d", rec.Code)
	}
	waitState(t, c.exp, r.ID, "done")
	if rec := doReq(h, "GET", "/api/mp4/media/odd/a.png", nil); rec.Code != 400 {
		t.Errorf("mp4 of a photo: %d", rec.Code)
	}
	rec = doReq(h, "GET", "/api/mp4/video/2026-01-04_110000.mkv", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "video/mp4" || rec.Body.Len() != 2000 {
		t.Errorf("mp4: %d %q %d", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	if rec := doReq(h, "GET", "/api/mp4/video/nope.mkv", nil); rec.Code != 404 {
		t.Errorf("mp4 missing: %d", rec.Code)
	}
}

func TestFailedWhenFFmpegFails(t *testing.T) {
	c, _ := fake(t, "0", "-f webm")
	j := c.exp.start("webm", []exportItem{{"video/2026-01-04_100000.mkv", filepath.Join(c.Root, "video/2026-01-04_100000.mkv")}})
	s := waitState(t, c.exp, j.ID, "error")
	if !reflect.DeepEqual(s["failed"], []string{"2026-01-04_100000.mkv"}) {
		t.Errorf("failed %v", s["failed"])
	}
}

// Two jobs started together must never run ffmpeg at the same moment.
func TestEncodesNeverOverlap(t *testing.T) {
	c, log := fake(t, "0.2", "x-never")
	var ids []string
	var wg sync.WaitGroup
	for _, f := range []string{"webm", "mp3", "mp4"} {
		for _, v := range []string{"video/2026-01-04_100000.mkv", "video/2026-01-04_110000.mkv"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				j := c.exp.start(f, []exportItem{{v, filepath.Join(c.Root, v)}})
				c.exp.mu.Lock()
				ids = append(ids, j.ID)
				c.exp.mu.Unlock()
			}()
		}
	}
	wg.Wait()
	for _, id := range ids {
		waitState(t, c.exp, id, "done")
	}
	if fileExists(log + ".overlap") {
		t.Error("two ffmpeg processes ran at once")
	}
	if n := len(logLines(log)); n != 6 {
		t.Errorf("%d ffmpeg runs, want 6", n)
	}
}

func TestStartupCleanup(t *testing.T) {
	c, _ := webFixture(t)
	put(t, filepath.Join(c.Root, "tmp/export-abc/export.zip"), "z")
	put(t, filepath.Join(c.Root, "tmp/export-def/x.mp3"), "z")
	put(t, filepath.Join(c.Root, "tmp/other.zip"), "keep")
	put(t, filepath.Join(c.Root, "proxy/gone.mp4"), "stale")
	put(t, filepath.Join(c.Root, "proxy/2026-01-03_090000.part"), "partial")
	put(t, filepath.Join(c.Root, "media/2026-01-02_030405/CLIP.AVI"), "1234567")
	put(t, filepath.Join(c.Root, "proxy/2026-01-02_030405_CLIP.mp4"), "kept")
	c.startupCleanup()
	for p, want := range map[string]bool{"tmp/export-abc": false, "tmp/export-def": false, "tmp/other.zip": true,
		"proxy/gone.mp4": false, "proxy/2026-01-03_090000.part": false, "proxy/2026-01-03_090000.mp4": true, "proxy/2026-01-02_030405_CLIP.mp4": true} {
		if got := fileExists(filepath.Join(c.Root, p)); got != want {
			t.Errorf("%s exists=%v want %v", p, got, want)
		}
	}
}
