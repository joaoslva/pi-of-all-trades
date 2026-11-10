package main

// Export and transcoding for the web app. A job is one object with a small
// state machine (queued, running, done, failed, cancelled); ffmpeg's arguments
// come from one pure function; one global lock lets a single encode run at a
// time, because the Pi has 512 MB and one hardware encoder.

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	exportTTL        = 1800 * time.Second // a finished export waits this long to be downloaded
	exportFetchedTTL = 300 * time.Second  // and this long after the first fetch, for a retry
	exportMaxPaths   = 500
)

var exportExt = map[string]string{"mp4": ".mp4", "mov": ".mov", "webm": ".webm", "mp3": ".mp3"}

// ffmpegAttempts is every ffmpeg command line (without the program name) to
// try, in order, to turn `in` into `out` as `format`. Only mp4 has a second
// attempt: h264_v4l2m2m (hardware) first, libx264 if that ever fails.
func ffmpegAttempts(format, in, out string) [][]string {
	var codec [][]string
	switch format {
	case "mp4":
		// -f mp4 is required: out is a ".part" name, which ffmpeg cannot pick a muxer from.
		common := []string{"-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", "-f", "mp4"}
		codec = [][]string{
			append([]string{"-c:v", "h264_v4l2m2m", "-b:v", "1500k"}, common...),
			append([]string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "26"}, common...),
		}
	case "mov": // in is the cached mp4, so this is a remux
		codec = [][]string{{"-c", "copy", "-movflags", "+faststart", "-f", "mov"}}
	case "webm": // realtime/cpu-used 8 keeps VP9 near realtime on four A53s
		codec = [][]string{{"-c:v", "libvpx-vp9", "-deadline", "realtime", "-cpu-used", "8", "-row-mt", "1", "-b:v", "1000k",
			"-pix_fmt", "yuv420p", "-c:a", "libopus", "-b:a", "96k", "-f", "webm"}}
	case "mp3":
		codec = [][]string{{"-vn", "-c:a", "libmp3lame", "-q:a", "3", "-f", "mp3"}}
	}
	var out2 [][]string
	for _, c := range codec {
		a := append([]string{"-v", "error", "-i", in}, c...)
		out2 = append(out2, append(a, "-y", out))
	}
	return out2
}

type jobState int

const (
	jobQueued jobState = iota // waiting for the encode lock
	jobRunning
	jobDone
	jobFailed
	jobCancelled
)

// The page treats any state but "running" and "done" as a failure, so queued is
// reported as running and failed as "error", as the Python app did.
var wireState = map[jobState]string{jobQueued: "running", jobRunning: "running", jobDone: "done", jobFailed: "error", jobCancelled: "cancelled"}

type exportItem struct{ rel, src string }

type exportJob struct {
	ID, Format string
	items      []exportItem
	ctx        context.Context
	stop       context.CancelFunc

	mu                 sync.Mutex
	state              jobState
	done               int
	current            string
	failed             []string
	name, path, errMsg string
	size               int64
	finished, fetched  time.Time
}

// move changes state if the transition is allowed; the caller holds mu.
func (j *exportJob) move(to jobState) bool {
	ok := (j.state == jobQueued && (to == jobRunning || to == jobCancelled)) ||
		(j.state == jobRunning && (to == jobDone || to == jobFailed || to == jobCancelled))
	if ok {
		j.state = to
	}
	return ok
}

func (j *exportJob) markRunning() {
	j.mu.Lock()
	j.move(jobRunning)
	j.mu.Unlock()
}

// cancel stops a queued or running job; it reports whether there was anything to stop.
func (j *exportJob) cancel(now time.Time) bool {
	j.mu.Lock()
	ok := j.move(jobCancelled)
	if ok {
		j.finished = now
	}
	j.mu.Unlock()
	j.stop()
	return ok
}

func (j *exportJob) finish(to jobState, now time.Time, errMsg string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.move(to) {
		return false
	}
	j.finished, j.errMsg = now, errMsg
	return true
}

func (j *exportJob) status() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	opt := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	var size any
	if j.state == jobDone {
		size = j.size
	}
	failed := j.failed
	if failed == nil {
		failed = []string{}
	}
	return map[string]any{"state": wireState[j.state], "done": j.done, "total": len(j.items), "current": opt(j.current),
		"failed": failed, "name": opt(j.name), "size": size, "error": opt(j.errMsg)}
}

type exporter struct {
	cfg  webCfg
	now  func() time.Time
	lock chan struct{} // the one global encode lock
	mu   sync.Mutex
	jobs map[string]*exportJob
}

func newExporter(c webCfg) *exporter {
	if c.FFProbe == "" {
		c.FFProbe = "ffprobe"
	}
	return &exporter{cfg: c, now: time.Now, lock: make(chan struct{}, 1), jobs: map[string]*exportJob{}}
}

func (e *exporter) acquire(ctx context.Context) bool {
	select {
	case e.lock <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (e *exporter) release() { <-e.lock }

func (e *exporter) workdir(id string) string { return filepath.Join(e.cfg.Root, "tmp", "export-"+id) }

// runFFmpeg runs one ffmpeg into out and reports whether ffprobe can read the
// result; whatever it leaves behind otherwise is removed.
func (e *exporter) runFFmpeg(ctx context.Context, argv []string, out string) bool {
	if exec.CommandContext(ctx, e.cfg.FFmpeg, argv...).Run() == nil {
		if st, err := os.Stat(out); err == nil && st.Size() > 1024 {
			probe, err := exec.CommandContext(ctx, e.cfg.FFProbe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", out).Output()
			if err == nil && strings.TrimSpace(string(probe)) != "" {
				return true
			}
		}
	}
	os.Remove(out)
	return false
}

// encode runs the attempts for one format under the encode lock; started is
// called once the lock is held (the job leaves the queue then).
func (e *exporter) encode(ctx context.Context, attempts [][]string, out string, started func()) bool {
	if !e.acquire(ctx) {
		return false
	}
	defer e.release()
	if started != nil {
		started()
	}
	for _, argv := range attempts {
		if ctx.Err() != nil {
			return false
		}
		if e.runFFmpeg(ctx, argv, out) {
			return true
		}
	}
	return false
}

// ensureMP4 is the cached H.264 + AAC copy of a video, made on first use. It
// returns "" if every attempt failed or ctx ended.
func (e *exporter) ensureMP4(ctx context.Context, src, rel string, started func()) string {
	dest := e.cfg.proxyPath(rel)
	if fileExists(dest) {
		return dest
	}
	if !e.acquire(ctx) {
		return ""
	}
	defer e.release()
	if started != nil {
		started()
	}
	if fileExists(dest) { // another request made it while this one waited
		return dest
	}
	os.MkdirAll(filepath.Dir(dest), 0o755)
	part := strings.TrimSuffix(dest, ".mp4") + ".part" // so a half-written transcode is never served
	for _, argv := range ffmpegAttempts("mp4", src, part) {
		if ctx.Err() != nil {
			return ""
		}
		if e.runFFmpeg(ctx, argv, part) {
			if os.Rename(part, dest) == nil {
				return dest
			}
			os.Remove(part)
		}
	}
	return ""
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// exportVideo makes one video in `format`. MP4 is the shared cache itself; the
// rest are written into the job's own directory.
func (e *exporter) exportVideo(j *exportJob, it exportItem, workdir string) string {
	if j.Format == "mp4" {
		return e.ensureMP4(j.ctx, it.src, it.rel, j.markRunning)
	}
	in := it.src
	if j.Format == "mov" { // building on the cache makes this a remux and a later MP4 export free
		if in = e.ensureMP4(j.ctx, it.src, it.rel, j.markRunning); in == "" {
			return ""
		}
	}
	out := filepath.Join(workdir, randHex(4)+exportExt[j.Format])
	if e.encode(j.ctx, ffmpegAttempts(j.Format, in, out), out, j.markRunning) {
		return out
	}
	return ""
}

type exportOutput struct{ file, src, name string }

func (e *exporter) run(j *exportJob) {
	workdir := e.workdir(j.ID)
	os.MkdirAll(workdir, 0o755)
	var outs []exportOutput
	for n, it := range j.items {
		if j.ctx.Err() != nil {
			break
		}
		base := filepath.Base(it.src)
		j.mu.Lock()
		j.done, j.current = n, base
		j.mu.Unlock()
		if !videoSuffixes[strings.ToLower(pySuffix(base))] { // photos pass through as they are
			outs = append(outs, exportOutput{it.src, it.src, base})
			continue
		}
		if out := e.exportVideo(j, it, workdir); out != "" {
			outs = append(outs, exportOutput{out, it.src, pyStem(base) + exportExt[j.Format]})
		} else if j.ctx.Err() == nil {
			j.mu.Lock()
			j.failed = append(j.failed, base)
			j.mu.Unlock()
		}
	}
	j.markRunning() // a job of photos never needed the lock
	j.mu.Lock()
	j.done, j.current = len(j.items), ""
	j.mu.Unlock()
	if j.ctx.Err() != nil {
		os.RemoveAll(workdir)
		return
	}
	if len(outs) == 0 {
		j.finish(jobFailed, e.now(), "")
		return
	}
	path, name := outs[0].file, outs[0].name
	if len(outs) > 1 {
		path, name = filepath.Join(workdir, "export.zip"), "camera-drive_"+e.now().Format("2006-01-02_1504")+"_"+j.Format+".zip"
		var srcs, names []string
		for _, o := range outs {
			srcs, names = append(srcs, o.src), append(names, o.name)
		}
		if err := writeStoredZip(path, outs, archiveNames(srcs, names)); err != nil {
			j.finish(jobFailed, e.now(), err.Error())
			os.RemoveAll(workdir)
			return
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		j.finish(jobFailed, e.now(), err.Error())
		return
	}
	j.mu.Lock()
	j.path, j.name, j.size = path, name, st.Size()
	j.mu.Unlock()
	if !j.finish(jobDone, e.now(), "") { // cancelled in the last instant
		os.RemoveAll(workdir)
	}
}

func writeStoredZip(path string, outs []exportOutput, arcs []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for i, o := range outs {
		if err := addStored(zw, o.file, arcs[i]); err != nil {
			return err
		}
	}
	return zw.Close()
}

// sweep forgets exports nobody came back for; tapes run to gigabytes and this is an SD card.
func (e *exporter) sweep() {
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, j := range e.jobs {
		j.mu.Lock()
		gone := (!j.fetched.IsZero() && now.Sub(j.fetched) > exportFetchedTTL) ||
			(!j.finished.IsZero() && now.Sub(j.finished) > exportTTL)
		j.mu.Unlock()
		if gone {
			delete(e.jobs, id)
			os.RemoveAll(e.workdir(id))
		}
	}
}

func (e *exporter) start(format string, items []exportItem) *exportJob {
	e.sweep()
	ctx, stop := context.WithCancel(context.Background())
	j := &exportJob{ID: randHex(8), Format: format, items: items, ctx: ctx, stop: stop}
	e.mu.Lock()
	e.jobs[j.ID] = j
	e.mu.Unlock()
	go e.run(j)
	return j
}

func (e *exporter) get(id string) *exportJob {
	e.sweep()
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.jobs[id]
}

// remove drops a job (and its files) that is not running.
func (e *exporter) remove(id string) {
	e.mu.Lock()
	delete(e.jobs, id)
	e.mu.Unlock()
	os.RemoveAll(e.workdir(id))
}

// startupCleanup removes what a previous run left behind: jobs live in memory,
// so their export-* directories are orphans, and proxies whose source is gone.
func (c webCfg) startupCleanup() {
	ents, _ := os.ReadDir(filepath.Join(c.Root, "tmp"))
	for _, d := range ents {
		if strings.HasPrefix(d.Name(), "export-") {
			os.RemoveAll(filepath.Join(c.Root, "tmp", d.Name()))
		}
	}
	keep := map[string]bool{}
	for _, f := range sortedNames(c.video(), false) {
		if videoSuffixes[strings.ToLower(pySuffix(f.Name()))] {
			keep[c.proxyPath("video/"+f.Name())] = true
		}
	}
	for _, dir := range sortedNames(c.media(), false) {
		for _, f := range sortedNames(filepath.Join(c.media(), dir.Name()), false) {
			if videoSuffixes[strings.ToLower(pySuffix(f.Name()))] {
				keep[c.proxyPath("media/"+dir.Name()+"/"+f.Name())] = true
			}
		}
	}
	proxyDir := filepath.Join(c.Root, "proxy")
	ents, _ = os.ReadDir(proxyDir)
	for _, p := range ents {
		if full := filepath.Join(proxyDir, p.Name()); !keep[full] {
			os.Remove(full)
		}
	}
}

// ---- HTTP ---------------------------------------------------------------------------

func (c webCfg) apiMP4(w http.ResponseWriter, r *http.Request) {
	rel := r.PathValue("rel")
	src, ok := c.safeMediaPath(rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !videoSuffixes[strings.ToLower(pySuffix(src))] {
		http.Error(w, "not a video", http.StatusBadRequest)
		return
	}
	// Not tied to the request: a phone that gives up should still leave the cache filled.
	dest := c.exp.ensureMP4(context.Background(), src, rel, nil)
	if dest == "" {
		http.Error(w, "transcode failed", http.StatusInternalServerError)
		return
	}
	serveFile(w, r, dest, "video/mp4", "")
}

// apiExportStart takes a selection; photos in it pass through, videos become `format`.
func (c webCfg) apiExportStart(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	format, _ := body["format"].(string)
	rels, _ := body["paths"].([]any)
	if exportExt[format] == "" || len(rels) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(rels) > exportMaxPaths {
		rels = rels[:exportMaxPaths]
	}
	items := make([]exportItem, len(rels))
	for i, v := range rels {
		rel, ok := v.(string)
		if !ok {
			rel = fmt.Sprint(v)
		}
		src, ok := c.safeMediaPath(rel)
		if !ok {
			http.NotFound(w, r)
			return
		}
		items[i] = exportItem{rel, src}
	}
	writeJSONResp(w, 200, map[string]any{"ok": true, "id": c.exp.start(format, items).ID})
}

func (c webCfg) apiExportStatus(w http.ResponseWriter, r *http.Request) {
	j := c.exp.get(r.PathValue("id"))
	if j == nil {
		http.NotFound(w, r)
		return
	}
	writeJSONResp(w, 200, j.status())
}

// apiExportCancel stops a queued or running job (killing its ffmpeg), or removes a finished one.
func (c webCfg) apiExportCancel(w http.ResponseWriter, r *http.Request) {
	j := c.exp.get(r.PathValue("id"))
	if j == nil {
		http.NotFound(w, r)
		return
	}
	if !j.cancel(c.exp.now()) {
		c.exp.remove(j.ID)
	}
	writeJSONResp(w, 200, map[string]any{"ok": true})
}

func (c webCfg) apiExportFile(w http.ResponseWriter, r *http.Request) {
	j := c.exp.get(r.PathValue("id"))
	if j == nil {
		http.NotFound(w, r)
		return
	}
	j.mu.Lock()
	state, path, name := j.state, j.path, j.name
	if state == jobDone && j.fetched.IsZero() { // kept a few minutes so an interrupted phone download can retry
		j.fetched = c.exp.now()
	}
	j.mu.Unlock()
	if state != jobDone {
		http.Error(w, "not ready", http.StatusConflict)
		return
	}
	typ := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if typ == "" {
		typ = "application/octet-stream"
	}
	serveFile(w, r, path, typ, attachment(name))
}
