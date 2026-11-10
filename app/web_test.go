package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fixture builds a data tree, a run dir and a fake sysfs USB tree.
func webFixture(t *testing.T) (webCfg, string) {
	t.Helper()
	root, run, usb := t.TempDir(), t.TempDir(), t.TempDir()
	put(t, filepath.Join(root, "media/2026-01-02_030405/IMG_0001.JPG"), "12345")
	put(t, filepath.Join(root, "media/2026-01-02_030405/CLIP.AVI"), "1234567")
	put(t, filepath.Join(root, "media/2026-01-02_030405/notes.txt"), "ignored")
	put(t, filepath.Join(root, "media/odd/a.png"), "1")
	put(t, filepath.Join(root, "media/snapshots/SNAP_2026-01-03_101010.jpg"), "123")
	put(t, filepath.Join(root, "video/2026-01-03_090000.mkv"), "12345678")
	put(t, filepath.Join(root, "proxy/2026-01-03_090000.mp4"), "p")
	put(t, filepath.Join(root, "status.json"), `{"state": "idle", "total": 3}`)
	put(t, filepath.Join(root, "recstatus.json"), `{"state": "live", "recording": false}`)
	put(t, filepath.Join(root, "index.json"), `{"aa": "x", "bb": "y"}`)
	put(t, filepath.Join(run, "control.json"), `{"preview": true, "recording": false, "snapshot_seq": 4}`)
	put(t, filepath.Join(usb, "1-1/idVendor"), "08ca\n")
	put(t, filepath.Join(usb, "1-1/idProduct"), "2022\n")
	put(t, filepath.Join(usb, "1-1:1.0/bInterfaceClass"), "0e\n")
	return webCfg{Root: root, Run: run, USBDir: usb, Poll: 5 * time.Millisecond, IdleLimit: 60, FFmpeg: "ffmpeg"}, run
}

func doReq(h http.Handler, method, target string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestStateLiteral(t *testing.T) {
	c, run := webFixture(t)
	put(t, filepath.Join(run, "live.jpg"), "x")
	rec := doReq(c.handler(), "GET", "/api/state", nil)
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	disk := got["disk"].(map[string]any)
	for _, k := range []string{"total", "used", "free", "pct"} {
		if _, ok := disk[k]; !ok {
			t.Errorf("disk lacks %s", k)
		}
	}
	delete(got, "disk")
	const want = `{
	 "camera": {"connected": true, "mode": "live"},
	 "status": {"state": "idle", "total": 3},
	 "recstatus": {"state": "live", "recording": false},
	 "control": {"preview": true, "recording": false, "snapshot_seq": 4},
	 "live": true, "held": 2,
	 "dumps": [
	  {"folder": "odd", "label": "odd", "count": 1, "bytes": 1, "items": [
	    {"name": "a.png", "path": "media/odd/a.png", "size": 1, "kind": "image", "proxy": false}]},
	  {"folder": "2026-01-02_030405", "label": "02 Jan 2026, 03:04", "count": 2, "bytes": 12, "items": [
	    {"name": "IMG_0001.JPG", "path": "media/2026-01-02_030405/IMG_0001.JPG", "size": 5, "kind": "image", "proxy": false},
	    {"name": "CLIP.AVI", "path": "media/2026-01-02_030405/CLIP.AVI", "size": 7, "kind": "video", "proxy": false}]}],
	 "captures": [
	  {"day": "2026-01-03", "label": "03 Jan 2026", "items": [
	    {"name": "2026-01-03_090000.mkv", "path": "video/2026-01-03_090000.mkv", "size": 8, "kind": "video", "proxy": true},
	    {"name": "SNAP_2026-01-03_101010.jpg", "path": "media/snapshots/SNAP_2026-01-03_101010.jpg", "size": 3, "kind": "image"}]}]
	}`
	// the snapshot (10:10) is newer than the tape (09:00)
	var w map[string]any
	json.Unmarshal([]byte(want), &w)
	items := w["captures"].([]any)[0].(map[string]any)["items"].([]any)
	items[0], items[1] = items[1], items[0]
	if !reflect.DeepEqual(got, w) {
		g, _ := json.MarshalIndent(got, "", " ")
		e, _ := json.MarshalIndent(w, "", " ")
		t.Errorf("state differs\n got: %s\nwant: %s", g, e)
	}
}

func TestStateEmptyTree(t *testing.T) {
	c := webCfg{Root: t.TempDir(), Run: t.TempDir(), USBDir: t.TempDir()}
	var got map[string]any
	json.Unmarshal(doReq(c.handler(), "GET", "/api/state", nil).Body.Bytes(), &got)
	if !reflect.DeepEqual(got["dumps"], []any{}) || !reflect.DeepEqual(got["captures"], []any{}) ||
		got["live"] != false || !reflect.DeepEqual(got["camera"], map[string]any{"connected": false, "mode": "none"}) {
		t.Errorf("empty state: %v", got)
	}
}

func TestControl(t *testing.T) {
	c, run := webFixture(t)
	h := c.handler()
	post := func(body string) (int, map[string]any) {
		rec := doReq(h, "POST", "/api/control", strings.NewReader(body))
		var m map[string]any
		json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	if code, _ := post(`{}`); code != 400 {
		t.Errorf("empty body: %d", code)
	}
	if code, _ := post(`not json`); code != 400 {
		t.Errorf("bad body: %d", code)
	}
	os.Remove(filepath.Join(run, "control.json"))
	code, m := post(`{"recording": true}`)
	if ctl := m["control"].(map[string]any); code != 200 || ctl["recording"] != true || ctl["preview"] != true {
		t.Errorf("recording should also set preview: %d %v", code, m)
	}
	_, m = post(`{"preview": false}`)
	if m["control"].(map[string]any)["preview"] != false || m["control"].(map[string]any)["recording"] != true {
		t.Errorf("preview off: %v", m)
	}
	post(`{"snapshot": true}`)
	_, m = post(`{"snapshot": 1}`)
	if m["control"].(map[string]any)["snapshot_seq"].(float64) != 2 {
		t.Errorf("snapshot_seq: %v", m)
	}
	if code, _ := post(`{"snapshot": false}`); code != 400 {
		t.Errorf("snapshot false should be nothing: %d", code)
	}
	onDisk := readJSON(filepath.Join(run, "control.json"))
	if ctlInt(onDisk, "snapshot_seq") != 2 || !ctlBool(onDisk, "recording") {
		t.Errorf("control.json: %v", onDisk)
	}
}

func TestStreamEndsAfterIdleLimit(t *testing.T) {
	c, run := webFixture(t)
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	if resp, _ := http.Get(srv.URL + "/api/stream"); resp.StatusCode != 503 {
		t.Errorf("no live.jpg: %d", resp.StatusCode)
	}
	frame := append(bytes.Repeat([]byte{0xff, 0xd8, 0xff, 1}, 400), 0xff, 0xd9)
	os.WriteFile(filepath.Join(run, "live.jpg"), frame, 0o644)
	start := time.Now()
	resp, err := http.Get(srv.URL + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if n := bytes.Count(body, []byte("--frame\r\n")); n != 1 {
		t.Errorf("want one frame (unchanged file is not resent), got %d", n)
	}
	if d := time.Since(start); d < 61*5*time.Millisecond || d > 3*time.Second {
		t.Errorf("stream lasted %v", d)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "multipart/x-mixed-replace; boundary=frame" {
		t.Errorf("content type %q", ct)
	}
	// a torn frame (no EOI) is never sent
	os.WriteFile(filepath.Join(run, "live.jpg"), frame[:len(frame)-2], 0o644)
	resp, _ = http.Get(srv.URL + "/api/stream")
	body, _ = io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Errorf("torn frame was sent")
	}
	if rec := doReq(c.handler(), "GET", "/api/live.jpg", nil); rec.Code != 503 {
		t.Errorf("live.jpg with torn frame: %d", rec.Code)
	}
}

func TestStreamStopsWhenClientLeaves(t *testing.T) {
	c, run := webFixture(t)
	c.IdleLimit = 100000
	frame := append(bytes.Repeat([]byte{0xff, 0xd8, 0xff, 1}, 400), 0xff, 0xd9)
	os.WriteFile(filepath.Join(run, "live.jpg"), frame, 0o644)
	done := make(chan struct{})
	h := c.handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r); close(done) }))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 100)
	resp.Body.Read(buf)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler still running after the client left")
	}
}

func TestPowerRouteAbsentAndPageClean(t *testing.T) {
	c, _ := webFixture(t)
	h := c.handler()
	for _, m := range []string{"POST", "GET"} {
		if rec := doReq(h, m, "/api/power", nil); rec.Code != 404 {
			t.Errorf("%s /api/power: %d", m, rec.Code)
		}
	}
	page := doReq(h, "GET", "/", nil).Body.String()
	if strings.Contains(page, `id="power"`) || strings.Contains(page, "/api/power") || strings.Contains(page, "__ICON") {
		t.Error("page still mentions power or an unsubstituted icon placeholder")
	}
	if !strings.Contains(page, "[hidden]{display:none !important}") {
		t.Error("hidden rule missing")
	}
}

func TestWipeWithFakeRunner(t *testing.T) {
	c, _ := webFixture(t)
	var flags []string
	out := "log line\n{\"ok\": true, \"deleted\": [\"a\"]}\n"
	c.Wipe = func(ctx context.Context, f string) (string, string, error) {
		flags = append(flags, f)
		return out, "", nil
	}
	h := c.handler()
	rec := doReq(h, "POST", "/api/wipe", strings.NewReader(`{"commit": true}`))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"ok": true, "deleted": ["a"]}` {
		t.Errorf("commit: %d %q", rec.Code, rec.Body.String())
	}
	doReq(h, "POST", "/api/wipe", strings.NewReader(`{}`))
	if !reflect.DeepEqual(flags, []string{"--commit", "--dry-run"}) {
		t.Errorf("flags %v", flags)
	}
	out = "not json\n"
	rec = doReq(h, "POST", "/api/wipe", strings.NewReader(`{}`))
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "could not parse camera-drive wipe output") {
		t.Errorf("unparseable: %d %s", rec.Code, rec.Body)
	}
	c.Wipe = func(context.Context, string) (string, string, error) { return "", "", errors.New("timed out") }
	rec = doReq(c.handler(), "POST", "/api/wipe", strings.NewReader(`{}`))
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "timed out") {
		t.Errorf("runner error: %d %s", rec.Code, rec.Body)
	}
}

func jsonEq(a, b string) bool {
	var x, y any
	return json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil && reflect.DeepEqual(x, y)
}

func TestTraversalIs404(t *testing.T) {
	c, _ := webFixture(t)
	h := c.handler()
	put(t, filepath.Join(c.Root, "secret.txt"), "s")
	for _, p := range []string{
		"/media/../secret.txt", "/media/%2e%2e/secret.txt", "/media/%2e%2e%2fsecret.txt", "/media/%252e%252e/secret.txt",
		"/media//etc/passwd", "/media/%2fetc/passwd", "/media/d%00/x", "/thumb/../secret.txt", "/api/play/../secret.txt",
		"/media/media/2026-01-02_030405/", "/media/", "/media/secret.txt", "/thumb/secret.txt",
	} {
		for _, m := range []string{"GET"} {
			if rec := doReq(h, m, p, nil); rec.Code != 404 {
				t.Errorf("%s %s: %d", m, p, rec.Code)
			}
		}
	}
	form := "path=..%2Fsecret.txt"
	req := httptest.NewRequest("POST", "/api/zip", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("zip traversal: %d", rec.Code)
	}
}

func TestMediaRangeAndDisposition(t *testing.T) {
	c, _ := webFixture(t)
	h := c.handler()
	req := httptest.NewRequest("GET", "/media/media/2026-01-02_030405/IMG_0001.JPG", nil)
	req.Header.Set("Range", "bytes=1-3")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 206 || rec.Body.String() != "234" || rec.Header().Get("Content-Range") != "bytes 1-3/5" ||
		rec.Header().Get("Content-Type") != "image/jpeg" || rec.Header().Get("Content-Disposition") != "inline; filename=IMG_0001.JPG" {
		t.Errorf("range: %d %q %v", rec.Code, rec.Body, rec.Header())
	}
	rec = doReq(h, "GET", "/media/media/2026-01-02_030405/CLIP.AVI", nil)
	if cd := rec.Header().Get("Content-Disposition"); cd != "attachment; filename=CLIP.AVI" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "video/") {
		t.Errorf("video: %q %q", cd, rec.Header().Get("Content-Type"))
	}
	rec = doReq(h, "GET", "/media/video/2026-01-03_090000.mkv", nil)
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "video/") {
		t.Errorf("mkv type %q", rec.Header().Get("Content-Type"))
	}
	rec = doReq(h, "GET", "/media/media/2026-01-02_030405/IMG_0001.JPG?dl", nil)
	if rec.Header().Get("Content-Disposition") != "attachment; filename=IMG_0001.JPG" {
		t.Errorf("?dl: %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestZipClash(t *testing.T) {
	c, _ := webFixture(t)
	put(t, filepath.Join(c.Root, "media/dumpb/IMG_0001.JPG"), "bbbb")
	form := "path=media/2026-01-02_030405/IMG_0001.JPG&path=media/dumpb/IMG_0001.JPG&path=video/2026-01-03_090000.mkv"
	req := httptest.NewRequest("POST", "/api/zip", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c.handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment; filename=camera-drive_") {
		t.Fatalf("zip: %d %v", rec.Code, rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.Method != zip.Store {
			t.Errorf("%s not stored", f.Name)
		}
	}
	want := []string{"2026-01-02_030405_IMG_0001.JPG", "dumpb_IMG_0001.JPG", "2026-01-03_090000.mkv"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names %v want %v", names, want)
	}
	if left, _ := filepath.Glob(filepath.Join(c.Root, "tmp", "*.zip")); len(left) != 0 {
		t.Errorf("temp zip left behind: %v", left)
	}
	req = httptest.NewRequest("POST", "/api/zip", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	c.handler().ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("empty zip: %d", rec.Code)
	}
}

func TestPlayStreamsFramesAndStopsFFmpeg(t *testing.T) {
	c, _ := webFixture(t)
	bin := t.TempDir()
	// Fake ffmpeg: two frames with junk between them, then it would sleep (as -re would).
	ff := put(t, filepath.Join(bin, "ffmpeg"), "#!/bin/sh\necho $$ > "+bin+"/pid\nprintf 'junk\\377\\330\\377AAAA\\377\\331\\377\\330\\377BB\\377\\331'\nexec sleep 30\n")
	os.Chmod(ff, 0o755)
	c.FFmpeg = ff
	srv := httptest.NewServer(c.handler())
	defer srv.Close()
	if resp, _ := http.Get(srv.URL + "/api/play/media/2026-01-02_030405/IMG_0001.JPG"); resp.StatusCode != 400 {
		t.Errorf("image to play: %d", resp.StatusCode)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/play/video/2026-01-03_090000.mkv", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 0, 512)
	buf := make([]byte, 512)
	for bytes.Count(got, []byte("--frame")) < 2 {
		n, err := resp.Body.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			t.Fatalf("read: %v (%q)", err, got)
		}
	}
	if !bytes.Contains(got, []byte("Content-Length: 9\r\n\r\n\xff\xd8\xffAAAA\xff\xd9\r\n")) {
		t.Errorf("first frame wrong: %q", got)
	}
	pidb, _ := os.ReadFile(filepath.Join(bin, "pid"))
	pid := strings.TrimSpace(string(pidb))
	cancel()
	for i := 0; ; i++ {
		if exec.Command("kill", "-0", pid).Run() != nil {
			break
		}
		if i > 30 {
			t.Fatal("ffmpeg still running 3 s after the client left")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
