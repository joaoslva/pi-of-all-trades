package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (f *fixture) change(body string, c *http.Cookie, hdr map[string]string) int {
	return f.do("POST", "/admin/api/password", body, hdr, c).Code
}

func (f *fixture) hintOf(t *testing.T, c *http.Cookie) string {
	t.Helper()
	if c := f.do("GET", "/admin/api/hint", "", nil, nil).Code; c != 401 {
		t.Fatalf("hint without a session: %d", c)
	}
	rec := f.do("GET", "/admin/api/hint", "", nil, c)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("hint Cache-Control: %q", rec.Header().Get("Cache-Control"))
	}
	var v struct{ Hint string }
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v.Hint
}

func TestChangePassword(t *testing.T) {
	f := newFixture(t)
	mine, other := f.session(t), f.session(t)
	before, _ := os.ReadFile(f.a.hashFile)

	if c := f.change(`{"current":"nope nope","new":"brand new pw"}`, mine, nil); c != 401 {
		t.Fatalf("wrong current: %d", c)
	}
	if after, _ := os.ReadFile(f.a.hashFile); string(after) != string(before) {
		t.Fatal("wrong current changed the hash")
	}
	if c := f.change(`{"current":"correct horse","new":"short"}`, mine, nil); c != 400 {
		t.Fatalf("short new: %d", c)
	}
	if c := f.change(`{"current":"correct horse","new":"correct horse"}`, mine, nil); c != 400 {
		t.Fatalf("same new: %d", c)
	}
	if c := f.change(`{"current":"correct horse","new":"brand new pw"}`, mine, map[string]string{"Sec-Fetch-Site": "cross-site"}); c != 403 {
		t.Fatalf("cross-site: %d", c)
	}
	if c := f.change(`{"current":"correct horse","new":"brand new pw"}`, nil, nil); c != 401 {
		t.Fatalf("no session: %d", c)
	}
	f.clock = f.clock.Add(2 * loginWindow)
	if c := f.change(`{"current":"correct horse","new":"brand new pw"}`, mine, nil); c != 204 {
		t.Fatalf("change: %d", c)
	}
	if f.do("GET", "/admin/api/system", "", nil, mine).Code != 200 {
		t.Fatal("caller's session was dropped")
	}
	if f.do("GET", "/admin/api/system", "", nil, other).Code != 401 {
		t.Fatal("other session survived")
	}
	f.clock = f.clock.Add(2 * loginWindow) // forget the attempts above
	if f.login("correct horse").Code != 401 || f.login("brand new pw").Code != 204 {
		t.Fatal("old password still works or new one does not")
	}
	st, _ := os.Stat(f.a.hashFile)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(f.a.hashFile), ".*tmp*"))
	if len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestChangePasswordRateLimit(t *testing.T) {
	f := newFixture(t)
	c := f.session(t) // one attempt used
	var codes []int
	for i := 0; i < 5; i++ {
		codes = append(codes, f.change(`{"current":"bad","new":"brand new pw"}`, c, nil))
	}
	if want := []int{401, 401, 401, 401, 429}; !equalInts(codes, want) {
		t.Fatalf("got %v want %v", codes, want)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestHint(t *testing.T) {
	f := newFixture(t)
	c := f.session(t)
	if h := f.hintOf(t, c); h != "" {
		t.Fatalf("hint before: %q", h)
	}
	if f.change(`{"current":"correct horse","new":"brand new pw","hint":"hint text"}`, c, nil) != 204 || f.hintOf(t, c) != "hint text" {
		t.Fatal("hint not stored")
	}
	st, _ := os.Stat(f.a.hintFile())
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("hint mode %v", st.Mode())
	}
	// absent keeps it
	if f.change(`{"current":"brand new pw","new":"another pw 1"}`, c, nil) != 204 || f.hintOf(t, c) != "hint text" {
		t.Fatal("absent hint did not keep the stored one")
	}
	// empty clears it
	if f.change(`{"current":"another pw 1","new":"another pw 2","hint":""}`, c, nil) != 204 || f.hintOf(t, c) != "" {
		t.Fatal("empty hint did not clear")
	}
	if _, err := os.Stat(f.a.hintFile()); !os.IsNotExist(err) {
		t.Fatal("hint file not removed")
	}
	f.clock = f.clock.Add(2 * loginWindow)
	for name, body := range map[string]string{
		"too long": `{"current":"another pw 2","new":"yet another 3","hint":"` + strings.Repeat("x", 121) + `"}`,
		"control":  `{"current":"another pw 2","new":"yet another 3","hint":"a\u0007b"}`,
		"newline":  `{"current":"another pw 2","new":"yet another 3","hint":"a\nb"}`,
		"has new":  `{"current":"another pw 2","new":"yet another 3","hint":"it is YET Another 3 ok"}`,
		"has cur":  `{"current":"another pw 2","new":"yet another 3","hint":"was Another PW 2"}`,
	} {
		if code := f.change(body, c, nil); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	f.clock = f.clock.Add(2 * loginWindow)
	if f.change(`{"current":"another pw 2","new":"yet another 3","hint":"`+strings.Repeat("x", 120)+`"}`, c, nil) != 204 {
		t.Fatal("120 characters refused")
	}
	// a stored hint that spells the new password is refused even when the hint is not resent
	f.clock = f.clock.Add(2 * loginWindow)
	if f.change(`{"current":"yet another 3","new":"xxxxxxxxxx"}`, c, nil) != 400 {
		t.Fatal("stored hint containing the new password accepted")
	}
}

func TestHintLeaksNoPassword(t *testing.T) {
	f := newFixture(t)
	c := f.session(t)
	f.change(`{"current":"correct horse","new":"brand new pw","hint":"hint text"}`, c, nil)
	b := f.do("GET", "/admin/api/hint", "", nil, c).Body.String()
	if b != "{\"hint\":\"hint text\"}\n" {
		t.Fatalf("hint body %q", b)
	}
}

func TestMigrateHash(t *testing.T) {
	d := t.TempDir()
	old, nw := filepath.Join(d, "old"), filepath.Join(d, "new")
	os.WriteFile(old, []byte("legacy\n"), 0o640)
	migrateHash(old, nw)
	b, _ := os.ReadFile(nw)
	st, _ := os.Stat(nw)
	if string(b) != "legacy\n" || st.Mode().Perm() != 0o600 {
		t.Fatalf("copy: %q %v", b, st.Mode())
	}
	os.WriteFile(old, []byte("changed\n"), 0o640)
	migrateHash(old, nw)
	if b, _ := os.ReadFile(nw); string(b) != "legacy\n" {
		t.Fatal("existing hash overwritten")
	}
	migrateHash(filepath.Join(d, "none"), filepath.Join(d, "other"))
	if _, err := os.Stat(filepath.Join(d, "other")); err == nil {
		t.Fatal("created from nothing")
	}
}
