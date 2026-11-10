package main

// Everything the web app knows about files on disk: which ones may be served,
// how they are listed, thumbnails and the names inside a zip.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	imageSuffixes = map[string]bool{".jpg": true, ".jpeg": true, ".png": true}
	videoSuffixes = map[string]bool{".avi": true, ".mov": true, ".mp4": true, ".mkv": true}
)

const thumbPx = 320

func init() {
	// mkv is forced to what Python 3.13's mimetypes says (the system table says x-matroska);
	// the rest are only filled in where the system table lacks them.
	mime.AddExtensionType(".mkv", "video/matroska")
	for ext, typ := range map[string]string{".avi": "video/x-msvideo", ".mov": "video/quicktime", ".mp4": "video/mp4", ".webm": "video/webm", ".mp3": "audio/mpeg", ".zip": "application/zip"} {
		if mime.TypeByExtension(ext) == "" {
			mime.AddExtensionType(ext, typ)
		}
	}
}

func (c webCfg) media() string { return filepath.Join(c.Root, "media") }
func (c webCfg) video() string { return filepath.Join(c.Root, "video") }

// safeMediaPath is the only thing between a crafted URL or form field and the
// rest of the filesystem. rel is relative to Root and must name a regular file
// that, symlinks followed, still lives under media/ or video/.
func (c webCfg) safeMediaPath(rel string) (string, bool) {
	if rel == "" || strings.ContainsRune(rel, 0) || strings.HasPrefix(rel, "/") {
		return "", false
	}
	segs := strings.Split(rel, "/")
	for _, s := range segs {
		if s == ".." {
			return "", false
		}
	}
	if segs[0] != "media" && segs[0] != "video" {
		return "", false
	}
	target, err := filepath.EvalSymlinks(filepath.Join(c.Root, rel))
	if err != nil {
		return "", false
	}
	for _, base := range []string{c.media(), c.video()} {
		b, err := filepath.EvalSymlinks(base)
		if err == nil && strings.HasPrefix(target, b+"/") {
			if st, err := os.Stat(target); err == nil && st.Mode().IsRegular() {
				return target, true
			}
			return "", false
		}
	}
	return "", false
}

func pyStem(name string) string { return strings.TrimSuffix(name, pySuffix(name)) }

// proxyPath is where the cached H.264 copy of a video lives. Tapes are unique
// by timestamp; camera clips restart at IMG_0001 so they carry their dump folder.
func (c webCfg) proxyPath(rel string) string {
	parts := strings.Split(rel, "/")
	stem := pyStem(parts[len(parts)-1])
	if parts[0] == "video" {
		return filepath.Join(c.Root, "proxy", stem+".mp4")
	}
	return filepath.Join(c.Root, "proxy", strings.Join(parts[1:len(parts)-1], "_")+"_"+stem+".mp4")
}

func (c webCfg) hasProxy(rel string) bool {
	_, err := os.Stat(c.proxyPath(rel))
	return err == nil
}

type item struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Kind  string `json:"kind"`
	Proxy *bool  `json:"proxy,omitempty"`
}

type dump struct {
	Folder string `json:"folder"`
	Label  string `json:"label"`
	Count  int    `json:"count"`
	Bytes  int64  `json:"bytes"`
	Items  []item `json:"items"`
}

type group struct {
	Day   string `json:"day"`
	Label string `json:"label"`
	Items []item `json:"items"`
}

func prettyFolder(name string) string {
	if name == "snapshots" {
		return "Snapshots"
	}
	if t, err := time.Parse("2006-01-02_150405", name); err == nil {
		return t.Format("02 Jan 2006, 15:04")
	}
	return name
}

func sortedNames(dir string, reverse bool) []os.DirEntry {
	ents, _ := os.ReadDir(dir) // ReadDir sorts by name already
	if reverse {
		sort.Slice(ents, func(i, j int) bool { return ents[i].Name() > ents[j].Name() })
	}
	return ents
}

func (c webCfg) listDumps() []dump {
	dumps := []dump{}
	for _, folder := range sortedNames(c.media(), true) {
		// Snapshots were shot through the app, so they live under Captures.
		if st, err := os.Stat(filepath.Join(c.media(), folder.Name())); err != nil || !st.IsDir() || folder.Name() == "snapshots" {
			continue
		}
		d := dump{Folder: folder.Name(), Label: prettyFolder(folder.Name()), Items: []item{}}
		for _, f := range sortedNames(filepath.Join(c.media(), folder.Name()), true) {
			suffix := strings.ToLower(pySuffix(f.Name()))
			isVideo := videoSuffixes[suffix]
			if !isVideo && !imageSuffixes[suffix] {
				continue
			}
			st, err := os.Stat(filepath.Join(c.media(), folder.Name(), f.Name()))
			if err != nil {
				continue
			}
			rel := "media/" + folder.Name() + "/" + f.Name()
			proxy := isVideo && c.hasProxy(rel)
			it := item{Name: f.Name(), Path: rel, Size: st.Size(), Kind: "image", Proxy: &proxy}
			if isVideo {
				it.Kind = "video"
			}
			d.Items = append(d.Items, it)
			d.Bytes += st.Size()
		}
		if d.Count = len(d.Items); d.Count > 0 {
			dumps = append(dumps, d)
		}
	}
	return dumps
}

// stampOf: "2026-08-17_175004.mkv" and "SNAP_2026-08-17_183539.jpg" both give
// "2026-08-17_175004", so tapes and snapshots sort together.
func stampOf(name string) string { return strings.TrimPrefix(pyStem(name), "SNAP_") }

// listCaptures is what was shot through the app, newest first, grouped by day.
func (c webCfg) listCaptures() []group {
	var items []item
	for _, f := range sortedNames(c.video(), false) {
		st, err := os.Stat(filepath.Join(c.video(), f.Name()))
		if err == nil && st.Mode().IsRegular() && videoSuffixes[strings.ToLower(pySuffix(f.Name()))] {
			rel := "video/" + f.Name()
			proxy := c.hasProxy(rel)
			items = append(items, item{Name: f.Name(), Path: rel, Size: st.Size(), Kind: "video", Proxy: &proxy})
		}
	}
	snaps := filepath.Join(c.media(), "snapshots")
	for _, f := range sortedNames(snaps, false) {
		st, err := os.Stat(filepath.Join(snaps, f.Name()))
		if err == nil && st.Mode().IsRegular() && imageSuffixes[strings.ToLower(pySuffix(f.Name()))] {
			items = append(items, item{Name: f.Name(), Path: "media/snapshots/" + f.Name(), Size: st.Size(), Kind: "image"})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return stampOf(items[i].Name) > stampOf(items[j].Name) })
	groups := []group{}
	for _, it := range items {
		day := stampOf(it.Name)
		if len(day) > 10 {
			day = day[:10]
		}
		if len(groups) == 0 || groups[len(groups)-1].Day != day {
			label := day
			if t, err := time.Parse("2006-01-02", day); err == nil {
				label = t.Format("02 Jan 2006")
			}
			groups = append(groups, group{Day: day, Label: label, Items: []item{}})
		}
		g := &groups[len(groups)-1]
		g.Items = append(g.Items, it)
	}
	return groups
}

// archiveNames gives the names inside a zip from (source path, wanted name)
// pairs. Camera clips from different dumps share names and a zip holding two
// IMG_0001.AVI silently loses one on extraction, so clashes get the dump folder.
func archiveNames(srcs, names []string) []string {
	count := map[string]int{}
	for _, n := range names {
		count[n]++
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n
		if count[n] > 1 {
			out[i] = filepath.Base(filepath.Dir(srcs[i])) + "_" + n
		}
	}
	return out
}

// thumbSlots keeps a page full of thumbnails from starting dozens of ffmpegs on a 415 MB Pi.
var thumbSlots = make(chan struct{}, 2)

// thumbFor returns the cached 320px JPEG for rel, making it on first use.
func (c webCfg) thumbFor(rel string) (string, bool) {
	src, ok := c.safeMediaPath(rel)
	if !ok {
		return "", false
	}
	suffix := strings.ToLower(pySuffix(src))
	if !imageSuffixes[suffix] && !videoSuffixes[suffix] {
		return "", false
	}
	st, err := os.Stat(src)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", rel, st.ModTime().UnixNano())))
	dir := filepath.Join(c.Root, "thumbs")
	dest := filepath.Join(dir, hex.EncodeToString(sum[:])[:32]+".jpg")
	if _, err := os.Stat(dest); err == nil {
		return dest, true
	}
	os.MkdirAll(dir, 0o755)
	thumbSlots <- struct{}{}
	defer func() { <-thumbSlots }()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	tmp := fmt.Sprintf("%s.%d.part.jpg", strings.TrimSuffix(dest, ".jpg"), time.Now().UnixNano())
	defer os.Remove(tmp)
	// Videos seek in a little: frame 0 is often the lens cap or an unsettled exposure.
	tries := [][]string{{"-i", src}}
	if videoSuffixes[suffix] {
		tries = [][]string{{"-ss", "1", "-i", src}, {"-i", src}}
	}
	for _, in := range tries {
		args := append([]string{"-hide_banner", "-loglevel", "error"}, in...)
		args = append(args, "-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:-1", thumbPx), "-y", tmp)
		if exec.CommandContext(ctx, c.FFmpeg, args...).Run() == nil {
			if s, err := os.Stat(tmp); err == nil && s.Size() > 0 && os.Rename(tmp, dest) == nil {
				return dest, true
			}
		}
	}
	return "", false
}
