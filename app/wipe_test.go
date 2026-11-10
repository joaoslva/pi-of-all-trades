package main

import (
	"os"
	"path/filepath"
	"testing"
)

// held puts content on the Pi side and in the index, as offload would.
func held(t *testing.T, p paths, index map[string]any, content, rel string) {
	t.Helper()
	write(t, filepath.Join(p.media(), rel), content)
	d, _ := sha256File(filepath.Join(p.media(), rel))
	index[d] = rel
}

func TestWipeKeepsUnknownAndMissingDeletesHeld(t *testing.T) {
	p, cam, lg, _ := fixture(t)
	index := map[string]any{}
	held(t, p, index, "photo-one", "d/IMG_0001.JPG")
	held(t, p, index, "video", "d/MOV_0002.AVI")
	// photo-two-same-name: not in the index at all.
	// A third entry whose Pi copy has vanished:
	d, _ := sha256File(filepath.Join(cam, "DCIM/100A/MOV_0002.AVI"))
	index[d] = "d/gone.AVI"
	os.Remove(filepath.Join(p.media(), "d/MOV_0002.AVI"))

	res := wipeTree(p, lg, cam, index, true)

	want := `{"ok": false, "deleted": ["IMG_0001.JPG"], "kept": ["MOV_0002.AVI", "IMG_0001.JPG"], "errors": ["MOV_0002.AVI: index points at missing file"], "committed": true}`
	if got := res.json(); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// Only the held photo is gone from the camera; the unknown one and the
	// one whose Pi copy vanished are still there.
	if _, err := os.Stat(filepath.Join(cam, "DCIM/100A/IMG_0001.JPG")); !os.IsNotExist(err) {
		t.Error("held photo should be deleted")
	}
	for _, f := range []string{"DCIM/101B/IMG_0001.JPG", "DCIM/100A/MOV_0002.AVI", "System Volume Information/IMG_9999.JPG"} {
		if _, err := os.Stat(filepath.Join(cam, f)); err != nil {
			t.Errorf("%s must survive: %v", f, err)
		}
	}
}

func TestWipeDryRunDeletesNothing(t *testing.T) {
	p, cam, lg, _ := fixture(t)
	index := map[string]any{}
	held(t, p, index, "photo-one", "d/a.JPG")
	res := wipeTree(p, lg, cam, index, false)
	want := `{"ok": true, "deleted": ["IMG_0001.JPG"], "kept": ["MOV_0002.AVI", "IMG_0001.JPG"], "errors": [], "committed": false}`
	// photo-two and video are unknown -> kept; only photo-one would go.
	if got := res.json(); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if len(collectFiles(cam)) != 3 {
		t.Fatal("dry run deleted something")
	}
}

func TestWipeEmptyIndexKeepsEverything(t *testing.T) {
	p, cam, lg, _ := fixture(t)
	res := wipeTree(p, lg, cam, map[string]any{}, true)
	if len(res.deleted) != 0 || len(res.kept) != 3 || len(collectFiles(cam)) != 3 {
		t.Fatalf("%+v", res)
	}
}

func TestWipeJSONMatchesPythonEscaping(t *testing.T) {
	r := wipeResult{ok: true, kept: []string{"é\"😀.JPG"}}
	want := `{"ok": true, "deleted": [], "kept": ["\u00e9\"\ud83d\ude00.JPG"], "errors": [], "committed": false}`
	if got := r.json(); got != want {
		t.Fatalf("got %s", got)
	}
}

func TestReadIndexRejectsGarbage(t *testing.T) {
	p, _, _, _ := fixture(t)
	for _, c := range []string{"", "not json", "[1]"} {
		write(t, p.index(), c)
		if _, err := readIndex(p.index()); err == nil {
			t.Errorf("index %q should be rejected", c)
		}
	}
	write(t, p.index(), "{}")
	if _, err := readIndex(p.index()); err != nil {
		t.Error(err)
	}
}
