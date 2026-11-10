package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseOSName(t *testing.T) {
	in := "NAME=\"Debian GNU/Linux\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\n"
	if got := parseOSName(in); got != "Debian GNU/Linux 12 (bookworm)" {
		t.Fatalf("got %q", got)
	}
	if parseOSName("ID=x") != "" {
		t.Fatal("missing PRETTY_NAME")
	}
}

func TestParseCamStatus(t *testing.T) {
	c := parseCamStatus([]byte(`{"state":"idle","last_result":"ok","updated":"2026-01-02T03:04:05","total":3}`))
	if c.State != "idle" || c.LastResult != "ok" || c.Updated != "2026-01-02T03:04:05" {
		t.Fatalf("%+v", c)
	}
	for _, in := range []string{"", "not json", "[1]", `{"state":5}`} {
		c := parseCamStatus([]byte(in))
		if in == `{"state":5}` {
			if c.State != 5.0 {
				t.Fatalf("%+v", c)
			}
			continue
		}
		if c.State != nil || c.LastResult != nil || c.Updated != nil {
			t.Fatalf("%q: %+v", in, c)
		}
	}
}

func TestParseBoot(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	got, ok := parseBoot("3600.55 7000.10\n", now)
	if !ok || !got.Equal(time.Date(2026, 1, 1, 10, 59, 59, 0, time.UTC)) {
		t.Fatalf("%v %v", got, ok)
	}
	if _, ok := parseBoot("", now); ok {
		t.Fatal("empty parsed")
	}
}

func TestSystemEndpoint(t *testing.T) {
	f := newFixture(t)
	c := f.session(t)
	dir := t.TempDir()
	f.a.cameraDir = dir
	get := func() map[string]any {
		rec := f.do("GET", "/admin/api/system", "", nil, c)
		var m map[string]any
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &m) != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return m
	}
	m := get() // no status.json: nulls, not an error
	cam := m["camera"].(map[string]any)
	if cam["state"] != nil || cam["total"].(float64) <= 0 || cam["free"].(float64) <= 0 {
		t.Fatalf("camera %v", cam)
	}
	for _, k := range []string{"hostname", "addresses", "os", "kernel", "arch", "revision", "modified", "boot_time", "time", "zone"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %s", k)
		}
	}
	os.WriteFile(filepath.Join(dir, "status.json"), []byte(`{"state":"copying","last_result":"ok"}`), 0o644)
	if cam := get()["camera"].(map[string]any); cam["state"] != "copying" || cam["last_result"] != "ok" || cam["updated"] != nil {
		t.Fatalf("camera %v", cam)
	}
	if f.do("GET", "/admin/api/system", "", nil, nil).Code != 401 {
		t.Fatal("system without session")
	}
}
