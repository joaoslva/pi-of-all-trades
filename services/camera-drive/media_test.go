package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, p, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSafeMediaPath(t *testing.T) {
	root := t.TempDir()
	outside := put(t, filepath.Join(t.TempDir(), "secret.jpg"), "secret")
	put(t, filepath.Join(root, "media/d1/IMG_1.JPG"), "x")
	put(t, filepath.Join(root, "video/t.mkv"), "x")
	put(t, filepath.Join(root, "status.json"), "{}")
	os.Symlink(outside, filepath.Join(root, "media/d1/escape.jpg"))
	os.Symlink(filepath.Join(root, "status.json"), filepath.Join(root, "video/status.mkv"))
	os.Symlink("IMG_1.JPG", filepath.Join(root, "media/d1/inside.jpg"))
	os.Symlink(filepath.Dir(outside), filepath.Join(root, "media/linkdir"))
	c := webCfg{Root: root}

	bad := []string{
		"media/../status.json", "../etc/passwd", "/etc/passwd", "/srv/camera-drive/media/d1/IMG_1.JPG",
		"media/%2e%2e/status.json", "media/%252e%252e/status.json", "media/d1/IMG_1.JPG\x00.txt",
		"media/d1/escape.jpg", "video/status.mkv", "media/linkdir/secret.jpg",
		"media/d1", "media", "video", "media/d1/missing.jpg", "", "status.json", "thumbs/x.jpg",
	}
	for _, rel := range bad {
		if p, ok := c.safeMediaPath(rel); ok {
			t.Errorf("safeMediaPath(%q) = %q, want refusal", rel, p)
		}
	}
	for _, rel := range []string{"media/d1/IMG_1.JPG", "video/t.mkv", "media/d1/inside.jpg"} {
		if _, ok := c.safeMediaPath(rel); !ok {
			t.Errorf("safeMediaPath(%q) refused", rel)
		}
	}
}

func TestArchiveNames(t *testing.T) {
	got := archiveNames(
		[]string{"/r/media/a/IMG_1.AVI", "/r/media/b/IMG_1.AVI", "/r/media/a/x.jpg"},
		[]string{"IMG_1.AVI", "IMG_1.AVI", "x.jpg"})
	if strings.Join(got, ",") != "a_IMG_1.AVI,b_IMG_1.AVI,x.jpg" {
		t.Errorf("got %v", got)
	}
}

func TestThumbCacheAndVideoRetry(t *testing.T) {
	root, bin := t.TempDir(), t.TempDir()
	put(t, filepath.Join(root, "media/d/a.jpg"), "img")
	put(t, filepath.Join(root, "media/d/c.avi"), "vid")
	// Fake ffmpeg: logs argv, fails when asked to seek, otherwise writes the last argument.
	ff := put(t, filepath.Join(bin, "ffmpeg"), "#!/bin/sh\necho \"$*\" >> "+bin+"/log\ncase \"$*\" in *-ss*) exit 1;; esac\nfor l; do :; done\nprintf JPEG > \"$l\"\n")
	os.Chmod(ff, 0o755)
	c := webCfg{Root: root, FFmpeg: ff}

	img, ok := c.thumbFor("media/d/a.jpg")
	if !ok || !strings.HasPrefix(img, filepath.Join(root, "thumbs")) {
		t.Fatalf("image thumb: %q %v", img, ok)
	}
	if _, ok := c.thumbFor("media/d/a.jpg"); !ok {
		t.Fatal("cached thumb lost")
	}
	if _, ok := c.thumbFor("media/d/c.avi"); !ok {
		t.Fatal("video thumb should succeed on the retry without -ss")
	}
	log, _ := os.ReadFile(filepath.Join(bin, "log"))
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) != 3 || strings.Contains(lines[0], "-ss") || !strings.Contains(lines[1], "-ss 1 ") ||
		!strings.Contains(lines[0], "scale=320:-1") || strings.Contains(lines[2], "-ss") {
		t.Errorf("ffmpeg calls (image, video+ss, video): %q", lines)
	}
	if _, ok := c.thumbFor("media/d/../../x"); ok {
		t.Error("traversal produced a thumb")
	}
	if es, _ := os.ReadDir(filepath.Join(root, "thumbs")); len(es) != 2 {
		t.Errorf("thumbs dir has %d files, want 2 (no .part left)", len(es))
	}
}
