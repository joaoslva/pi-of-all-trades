package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture returns a fake camera tree and an empty Pi data dir.
func fixture(t *testing.T) (p paths, cam string, lg logger, out *bytes.Buffer) {
	t.Helper()
	d := t.TempDir()
	p = paths{Root: filepath.Join(d, "srv"), Run: filepath.Join(d, "run"), Lock: filepath.Join(d, "lock")}
	cam = filepath.Join(d, "cam")
	out = &bytes.Buffer{}
	lg = logger{out: out, file: p.logFile(), mkdir: p.Root}
	write(t, filepath.Join(cam, "DCIM/100A/IMG_0001.JPG"), "photo-one")
	write(t, filepath.Join(cam, "DCIM/101B/IMG_0001.JPG"), "photo-two-same-name")
	write(t, filepath.Join(cam, "DCIM/100A/MOV_0002.AVI"), "video")
	write(t, filepath.Join(cam, "DCIM/100A/notes.txt"), "not media")
	write(t, filepath.Join(cam, "System Volume Information/IMG_9999.JPG"), "windows litter")
	write(t, filepath.Join(cam, ".Trashes/IMG_8888.JPG"), "mac litter")
	return
}

func TestOffloadCopiesVerifiesAndSkipsOnSecondRun(t *testing.T) {
	p, cam, lg, _ := fixture(t)
	if code := offloadTree(p, lg, cam, &Led{}, offloadHooks{}); code != 0 {
		t.Fatalf("first run exit %d", code)
	}
	index := readJSON(p.index())
	if len(index) != 3 {
		t.Fatalf("want 3 indexed files (ignored dirs and .txt skipped), got %v", index)
	}
	st := readJSON(p.status())
	if st["last_copied"].(interface{ String() string }).String() != "3" || st["state"] != "idle" || st["last_folder"] == nil {
		t.Fatalf("status after first run: %v", st)
	}
	for _, k := range []string{"state", "total", "done", "last_result", "last_copied", "last_skipped", "last_failed", "last_bytes", "last_folder", "updated"} {
		if _, ok := st[k]; !ok {
			t.Errorf("status.json lacks key %q", k)
		}
	}

	before, _ := os.ReadFile(p.index())
	if code := offloadTree(p, lg, cam, &Led{}, offloadHooks{}); code != 0 {
		t.Fatalf("second run exit %d", code)
	}
	st = readJSON(p.status())
	if st["last_result"] != "copied 0, skipped 3 already held" || st["last_folder"] != nil {
		t.Fatalf("second run should skip everything: %v", st)
	}
	after, _ := os.ReadFile(p.index())
	if !bytes.Equal(before, after) {
		t.Fatal("index changed on a no-op run")
	}
	// the camera tree is untouched
	if len(collectFiles(cam)) != 3 {
		t.Fatal("offload must never delete from the camera")
	}
}

func TestOffloadNameClashGetsHashSuffix(t *testing.T) {
	p, cam, lg, _ := fixture(t)
	offloadTree(p, lg, cam, &Led{}, offloadHooks{})
	var rels []string
	for _, v := range readJSON(p.index()) {
		rels = append(rels, v.(string))
	}
	suffixed := 0
	for _, r := range rels {
		if strings.Contains(filepath.Base(r), "IMG_0001_") && strings.HasSuffix(r, ".JPG") {
			suffixed++
		}
	}
	if suffixed != 1 {
		t.Fatalf("exactly one of the two IMG_0001.JPG should carry _<hash8>: %v", rels)
	}
}

func TestOffloadCorruptedCopyIsRemovedAndCountedFailed(t *testing.T) {
	p, cam, lg, _ := fixture(t)
	h := offloadHooks{afterCopy: func(dest string) {
		if strings.HasSuffix(dest, "MOV_0002.AVI") {
			os.WriteFile(dest, []byte("bit rot"), 0o644)
		}
	}}
	if code := offloadTree(p, lg, cam, &Led{}, h); code != 1 {
		t.Fatalf("want exit 1 on a failed verify, got %d", code)
	}
	st := readJSON(p.status())
	if st["last_result"] != "copied 2, skipped 0 already held, 1 FAILED" {
		t.Fatalf("status: %v", st)
	}
	if len(readJSON(p.index())) != 2 {
		t.Fatal("a failed copy must not be indexed")
	}
	var left []string
	filepath.WalkDir(p.media(), func(path string, d os.DirEntry, _ error) error {
		if !d.IsDir() && strings.HasSuffix(path, ".AVI") {
			left = append(left, path)
		}
		return nil
	})
	if len(left) != 0 {
		t.Fatalf("corrupt copy still on disk: %v", left)
	}
}

func TestCollectFilesOrdersLikePythonPaths(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "a b/x.jpg"), "1")
	write(t, filepath.Join(d, "a/x.jpg"), "2")
	write(t, filepath.Join(d, ".jpg"), "no suffix, not media")
	got := collectFiles(d)
	if len(got) != 2 || !strings.HasSuffix(got[0], "a/x.jpg") {
		t.Fatalf("got %v", got)
	}
}
