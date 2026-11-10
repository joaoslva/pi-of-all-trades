package main

// camera-drive web is the phone-facing page: state, live view, playback,
// downloads and wipe. It runs unprivileged and talks to camera-live only
// through control.json and live.jpg; wipe is a sudo call that enforces its own
// safety rule. Export and transcoding live in export.go.

import (
	"archive/zip"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed web/page.html
var pageTemplate string

// Pixel-art camera on a 16x16 grid, as in the Python original.
const iconBody = "<rect x='5' y='1' width='4' height='2' fill='{o}'/>" +
	"<rect x='1' y='3' width='14' height='11' fill='{o}'/>" +
	"<rect x='4' y='5' width='8' height='8' fill='{d}'/>" +
	"<rect x='5' y='6' width='6' height='6' fill='{g}'/>" +
	"<rect x='7' y='8' width='2' height='2' fill='{l}'/>" +
	"<rect x='12' y='4' width='2' height='2' fill='{r}'/>"

func iconBodyWith(o, d, g, l, r string) string {
	return strings.NewReplacer("{o}", o, "{d}", d, "{g}", g, "{l}", l, "{r}", r).Replace(iconBody)
}

func renderPage(tpl string) string {
	inline := "<svg viewBox='0 0 16 16' shape-rendering='crispEdges'>" +
		iconBodyWith("#DFA05D", "#17150F", "#658761", "#EDE4D3", "#AC5045") + "</svg>"
	uri := "data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16' shape-rendering='crispEdges'>" +
		"<rect width='16' height='16' fill='%2317150F'/>" +
		iconBodyWith("%23DFA05D", "%2317150F", "%23658761", "%23EDE4D3", "%23AC5045") + "</svg>"
	return strings.ReplaceAll(strings.ReplaceAll(tpl, "__ICON_INLINE__", inline), "__ICON_URI__", uri)
}

// wipeRunner runs `camera-drive wipe <flag>` as root and returns its output.
type wipeRunner func(ctx context.Context, flag string) (stdout, stderr string, err error)

type webCfg struct {
	Listen, Root, Run, FFmpeg, FFProbe, Sudo, WipeBin, USBDir string
	Poll                                                      time.Duration
	IdleLimit                                                 int
	Wipe                                                      wipeRunner // nil: sudo -n WipeBin wipe <flag>
	exp                                                       *exporter  // made by handler() when nil
}

func (c webCfg) liveJPG() string { return filepath.Join(c.Run, "live.jpg") }

func webFlags(fs *flag.FlagSet) *webCfg {
	c := &webCfg{IdleLimit: 60}
	fs.StringVar(&c.Listen, "listen", "0.0.0.0:8081", "address to listen on")
	fs.StringVar(&c.Root, "root", "/srv/camera-drive", "data directory")
	fs.StringVar(&c.Run, "run-dir", "/run/camera-drive", "runtime directory (control.json, live.jpg)")
	fs.StringVar(&c.FFmpeg, "ffmpeg", "ffmpeg", "ffmpeg program")
	fs.StringVar(&c.FFProbe, "ffprobe", "ffprobe", "ffprobe program")
	fs.StringVar(&c.Sudo, "sudo", "sudo", "sudo program")
	fs.StringVar(&c.WipeBin, "wipe-cmd", "/usr/local/bin/camera-drive", "program run as `sudo -n <wipe-cmd> wipe`")
	fs.StringVar(&c.USBDir, "usb-dir", "/sys/bus/usb/devices", "sysfs USB device directory")
	fs.DurationVar(&c.Poll, "poll", 80*time.Millisecond, "live.jpg poll interval for /api/stream")
	return c
}

func cmdWeb(args []string) int {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := webFlags(fs)
	if fs.Parse(args) != nil {
		fmt.Fprintln(os.Stderr, "usage: camera-drive web [flags]")
		return 2
	}
	for _, d := range []string{"thumbs", "tmp", "proxy"} {
		os.MkdirAll(filepath.Join(cfg.Root, d), 0o755)
	}
	cfg.startupCleanup()
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// No WriteTimeout: streams live for as long as the viewer stays.
	srv := &http.Server{Handler: cfg.handler(), ReadHeaderTimeout: 10 * time.Second, WriteTimeout: 0}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-sig; srv.Close() }()
	log.Printf("camera-drive web listening on %s", cfg.Listen)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func (c webCfg) handler() http.Handler {
	if c.exp == nil {
		c.exp = newExporter(c)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, pagePrepared)
	})
	mux.HandleFunc("GET /api/state", c.apiState)
	mux.HandleFunc("GET /api/stream", c.apiStream)
	mux.HandleFunc("GET /api/live.jpg", c.apiLiveJPG)
	mux.HandleFunc("GET /api/play/{rel...}", c.apiPlay)
	mux.HandleFunc("POST /api/control", c.apiControl)
	mux.HandleFunc("POST /api/wipe", c.apiWipe)
	mux.HandleFunc("GET /media/{rel...}", c.apiMedia)
	mux.HandleFunc("GET /thumb/{rel...}", c.apiThumb)
	mux.HandleFunc("POST /api/zip", c.apiZip)
	mux.HandleFunc("GET /api/mp4/{rel...}", c.apiMP4)
	mux.HandleFunc("POST /api/export", c.apiExportStart)
	mux.HandleFunc("GET /api/export/{id}", c.apiExportStatus)
	mux.HandleFunc("DELETE /api/export/{id}", c.apiExportCancel)
	mux.HandleFunc("GET /api/export/{id}/file", c.apiExportFile)
	// ServeMux would answer a dirty path with a 301 to the cleaned one; a
	// traversal attempt gets 404 instead.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.ContainsRune(p, 0) || (p != "/" && path.Clean(p) != strings.TrimSuffix(p, "/")) {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

var pagePrepared = renderPage(pageTemplate)

func writeJSONResp(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// ---- /api/state ---------------------------------------------------------------

func (c webCfg) cameraState() map[string]any {
	ents, err := os.ReadDir(c.USBDir)
	if err != nil {
		return map[string]any{"connected": false, "mode": "unknown"}
	}
	var storage, live bool
	for _, e := range ents {
		v, _ := os.ReadFile(filepath.Join(c.USBDir, e.Name(), "idVendor"))
		p, _ := os.ReadFile(filepath.Join(c.USBDir, e.Name(), "idProduct"))
		switch strings.ToLower(strings.TrimSpace(string(v)) + ":" + strings.TrimSpace(string(p))) {
		case "08ca:2023":
			storage = true
		case "08ca:2022":
			live = true
		}
	}
	switch {
	case storage:
		return map[string]any{"connected": true, "mode": "storage"}
	case live:
		return map[string]any{"connected": true, "mode": "live"}
	}
	return map[string]any{"connected": false, "mode": "none"}
}

func (c webCfg) diskState() map[string]any {
	var st syscall.Statfs_t
	if syscall.Statfs(c.Root, &st) != nil {
		return map[string]any{"total": 0, "used": 0, "free": 0, "pct": 0.0}
	}
	total := int64(st.Blocks) * int64(st.Frsize)
	free := int64(st.Bavail) * int64(st.Frsize)
	pct := 0.0
	if total > 0 {
		pct = math.RoundToEven(float64(total-free)/float64(total)*1000) / 10
	}
	return map[string]any{"total": total, "used": total - free, "free": free, "pct": pct}
}

func (c webCfg) apiState(w http.ResponseWriter, r *http.Request) {
	_, liveErr := os.Stat(c.liveJPG())
	writeJSONResp(w, 200, map[string]any{
		"camera":    c.cameraState(),
		"disk":      c.diskState(),
		"status":    readJSON(filepath.Join(c.Root, "status.json")),
		"recstatus": readJSON(filepath.Join(c.Root, "recstatus.json")),
		"control":   readJSON(filepath.Join(c.Run, "control.json")),
		"live":      liveErr == nil,
		"held":      len(readJSON(filepath.Join(c.Root, "index.json"))),
		"dumps":     c.listDumps(),
		"captures":  c.listCaptures(),
	})
}

// ---- live view and playback -----------------------------------------------------

var (
	jpegSOI = []byte{0xff, 0xd8, 0xff}
	jpegEOI = []byte{0xff, 0xd9}
)

// readLiveFrame reads live.jpg into buf (reused between polls: 12 frames a second
// of fresh allocations is most of this process's CPU). It returns nil for
// frames that do not end in EOI, because ffmpeg rewrites the file in place and
// a reader can catch it half written.
func (c webCfg) readLiveFrame(buf []byte) []byte {
	f, err := os.Open(c.liveJPG())
	if err != nil {
		return nil
	}
	defer f.Close()
	buf = buf[:0]
	for {
		if len(buf) == cap(buf) {
			grown := make([]byte, len(buf), 2*cap(buf)+(64<<10))
			copy(grown, buf)
			buf = grown
		}
		n, err := f.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if err != nil {
			break
		}
	}
	if len(buf) < 1024 || !bytes.HasSuffix(buf, jpegEOI) {
		return nil
	}
	return buf
}

// writeFrame sends one multipart part in a single write and flushes it; out is scratch space.
func writeFrame(w http.ResponseWriter, out *[]byte, frame []byte) error {
	b := fmt.Appendf((*out)[:0], "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame))
	b = append(append(b, frame...), '\r', '\n')
	*out = b
	if _, err := w.Write(b); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}

const multipartType = "multipart/x-mixed-replace; boundary=frame"

func (c webCfg) apiStream(w http.ResponseWriter, r *http.Request) {
	if _, err := os.Stat(c.liveJPG()); err != nil {
		http.Error(w, "live view is off", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", multipartType)
	var cur, last, out []byte
	idle := 0
	tick := time.NewTicker(c.Poll)
	defer tick.Stop()
	for {
		if frame := c.readLiveFrame(cur); frame != nil && !bytes.Equal(frame, last) {
			cur, last, idle = last, frame, 0
			if writeFrame(w, &out, frame) != nil {
				return
			}
		} else if idle++; idle > c.IdleLimit {
			// Viewfinder switched off or camera unplugged: end rather than hold the connection forever.
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

func (c webCfg) apiLiveJPG(w http.ResponseWriter, r *http.Request) {
	frame := c.readLiveFrame(nil)
	if frame == nil {
		http.Error(w, "no frame", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(frame)
}

// apiPlay unwraps the JPEG frames a recording already contains and streams
// them as multipart. -re paces ffmpeg at the file's own rate; -c:v copy means
// nothing is decoded anywhere.
func (c webCfg) apiPlay(w http.ResponseWriter, r *http.Request) {
	src, ok := c.safeMediaPath(r.PathValue("rel"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !videoSuffixes[strings.ToLower(pySuffix(src))] {
		http.Error(w, "not a video", http.StatusBadRequest)
		return
	}
	cmd := exec.CommandContext(r.Context(), c.FFmpeg, "-v", "error", "-re", "-i", src, "-c:v", "copy", "-f", "image2pipe", "-")
	out, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		http.Error(w, "cannot start ffmpeg", http.StatusInternalServerError)
		return
	}
	// Reached on a write error too: the context only ends when the client is gone.
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	w.Header().Set("Content-Type", multipartType)
	var buf, scratch []byte
	chunk := make([]byte, 65536)
	for {
		n, err := out.Read(chunk)
		buf = append(buf, chunk[:n]...)
		for {
			end := bytes.Index(buf, jpegEOI)
			if end == -1 {
				break
			}
			frame := buf[:end+2]
			buf = append([]byte(nil), buf[end+2:]...)
			if start := bytes.Index(frame, jpegSOI); start != -1 && writeFrame(w, &scratch, frame[start:]) != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// ---- control ------------------------------------------------------------------------

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case json.Number:
		f, _ := x.Float64()
		return f != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func (c webCfg) apiControl(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	d.UseNumber()
	if d.Decode(&body) != nil || body == nil {
		body = map[string]any{}
	}
	_, hasPreview := body["preview"]
	_, hasRecording := body["recording"]
	snapshot := truthy(body["snapshot"])
	if !hasPreview && !hasRecording && !snapshot {
		writeJSONResp(w, 400, map[string]any{"ok": false, "error": "nothing to do"})
		return
	}
	os.MkdirAll(c.Run, 0o755)
	var out map[string]any
	err := liveCfg{Run: c.Run}.updateControl(func(m map[string]any) {
		if len(m) == 0 {
			m["preview"], m["recording"], m["snapshot_seq"] = false, false, int64(0)
		}
		if hasPreview {
			m["preview"] = truthy(body["preview"])
		}
		if hasRecording {
			rec := truthy(body["recording"])
			m["recording"] = rec
			if rec { // recording needs the capture pipeline, so it turns the viewfinder on too
				m["preview"] = true
			}
		}
		if snapshot {
			m["snapshot_seq"] = ctlInt(m, "snapshot_seq") + 1
		}
		out = m
	})
	if err != nil {
		writeJSONResp(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSONResp(w, 200, map[string]any{"ok": true, "control": out})
}

// ---- wipe ---------------------------------------------------------------------------

func (c webCfg) runWipe(ctx context.Context, flag string) (string, string, error) {
	if c.Wipe != nil {
		return c.Wipe(ctx, flag)
	}
	var so, se bytes.Buffer
	cmd := exec.CommandContext(ctx, c.Sudo, "-n", c.WipeBin, "wipe", flag)
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ctx.Err() == nil {
		err = nil // a non-zero exit still prints its JSON verdict
	}
	return so.String(), se.String(), err
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func (c webCfg) apiWipe(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	flag := "--dry-run"
	if truthy(body["commit"]) {
		flag = "--commit"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 900*time.Second)
	defer cancel()
	stdout, stderr, err := c.runWipe(ctx, flag)
	if err != nil {
		writeJSONResp(w, 500, map[string]any{"ok": false, "errors": []string{err.Error()}})
		return
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if !json.Valid([]byte(last)) {
		writeJSONResp(w, 500, map[string]any{"ok": false, "errors": []string{
			"could not parse camera-drive wipe output", tail(stdout, 400), tail(stderr, 400)}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, last+"\n")
}

// ---- files --------------------------------------------------------------------------

func attachment(name string) string {
	return mime.FormatMediaType("attachment", map[string]string{"filename": name})
}

func serveFile(w http.ResponseWriter, r *http.Request, p, contentType, disposition string) {
	f, err := os.Open(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	if disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func (c webCfg) apiMedia(w http.ResponseWriter, r *http.Request) {
	p, ok := c.safeMediaPath(r.PathValue("rel"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	typ := mime.TypeByExtension(strings.ToLower(filepath.Ext(p)))
	if typ == "" {
		typ = "application/octet-stream"
	}
	// Browsers cannot decode MJPEG/Matroska and show a bare "no supported
	// source"; send video as a download. In-app playback goes through /api/play.
	disp := mime.FormatMediaType("inline", map[string]string{"filename": filepath.Base(p)})
	if _, dl := r.URL.Query()["dl"]; dl || videoSuffixes[strings.ToLower(pySuffix(p))] {
		disp = attachment(filepath.Base(p))
	}
	serveFile(w, r, p, typ, disp)
}

func (c webCfg) apiThumb(w http.ResponseWriter, r *http.Request) {
	t, ok := c.thumbFor(r.PathValue("rel"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "max-age=86400")
	serveFile(w, r, t, "image/jpeg", "")
}

// apiZip bundles the selection. ZIP_STORED, not deflate: JPEG and MJPEG are
// already compressed and deflating burns CPU on the Pi for almost nothing.
func (c webCfg) apiZip(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if r.ParseForm() != nil || len(r.PostForm["path"]) == 0 {
		http.Error(w, "no paths", http.StatusBadRequest)
		return
	}
	rels := r.PostForm["path"]
	if len(rels) > 500 {
		rels = rels[:500]
	}
	var srcs, names []string
	for _, rel := range rels {
		p, ok := c.safeMediaPath(rel)
		if !ok {
			http.NotFound(w, r)
			return
		}
		srcs, names = append(srcs, p), append(names, filepath.Base(p))
	}
	tmpDir := filepath.Join(c.Root, "tmp")
	os.MkdirAll(tmpDir, 0o755)
	tmp, err := os.CreateTemp(tmpDir, "*.zip")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	zw := zip.NewWriter(tmp)
	for i, arc := range archiveNames(srcs, names) {
		if err := addStored(zw, srcs[i], arc); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	if err := zw.Close(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	st, err := tmp.Stat()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", attachment("camera-drive_"+time.Now().Format("2006-01-02_1504")+".zip"))
	http.ServeContent(w, r, "", st.ModTime(), tmp)
}

func addStored(zw *zip.Writer, src, arc string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	h, err := zip.FileInfoHeader(st)
	if err != nil {
		return err
	}
	h.Name, h.Method = arc, zip.Store
	dst, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, f)
	return err
}
