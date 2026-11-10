package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadValid(t *testing.T) {
	got, err := LoadServices("testdata/services.d")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "camera" || got[0].Class != Exclusive || got[0].Port != 8081 || got[1].Class != Background {
		t.Fatalf("unexpected services: %+v", got)
	}
}

func TestLoadRejects(t *testing.T) {
	ok := `{"id":"a","name":"A","class":"exclusive","units":["a"],"port":9001}`
	cases := map[string]struct {
		files map[string]string
		want  string
	}{
		"duplicate id":  {map[string]string{"a.json": ok, "b.json": strings.Replace(ok, "9001", "9002", 1)}, "duplicate id"},
		"port clash":    {map[string]string{"a.json": ok, "b.json": strings.Replace(ok, `"a"`, `"b"`, 2)}, "port 9001"},
		"missing units": {map[string]string{"a.json": strings.Replace(ok, `["a"]`, `[]`, 1)}, "missing units"},
		"unknown class": {map[string]string{"a.json": strings.Replace(ok, "exclusive", "heavy", 1)}, "unknown class"},
		"missing id":    {map[string]string{"a.json": strings.Replace(ok, `"id":"a",`, "", 1)}, "missing id"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadServices(writeDir(t, c.files))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestIndexHref(t *testing.T) {
	services, err := LoadServices("testdata/services.d")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "pi.test:80"
	rec := httptest.NewRecorder()
	newHandler(services, NewAuth(""), nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `href="http://pi.test:8081/"`) {
		t.Fatalf("camera link missing:\n%s", body)
	}
}
