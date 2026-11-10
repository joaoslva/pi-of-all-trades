package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	a     *Auth
	h     http.Handler
	clock time.Time
	ran   []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{clock: time.Unix(1_000_000, 0)}
	hash, err := hashPassword("correct horse", 10) // low count: speed only
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "admin.hash")
	os.WriteFile(file, []byte(hash+"\n"), 0o640)
	f.a = NewAuth(file)
	f.a.now = func() time.Time { return f.clock }
	f.a.run = func(args ...string) error { f.ran = append(f.ran, strings.Join(args, " ")); return nil }
	f.h = newHandler(nil, f.a, nil)
	return f
}

func (f *fixture) do(method, path, body string, hdr map[string]string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "10.0.0.5:1234"
	req.Host = "pi.test"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) login(pw string) *httptest.ResponseRecorder {
	return f.do("POST", "/admin/api/login", url.Values{"password": {pw}}.Encode(),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, nil)
}

func (f *fixture) session(t *testing.T) *http.Cookie {
	t.Helper()
	rec := f.login("correct horse")
	if rec.Code != 204 || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("login: %d", rec.Code)
	}
	return rec.Result().Cookies()[0]
}

func TestHashRoundTrip(t *testing.T) {
	h, _ := hashPassword("pw", 5)
	if !strings.HasPrefix(h, "5$") || !verifyPassword(h, "pw") || verifyPassword(h, "px") || verifyPassword("junk", "pw") {
		t.Fatalf("bad hash handling: %s", h)
	}
}

func TestLogin(t *testing.T) {
	f := newFixture(t)
	if rec := f.login("wrong"); rec.Code != 401 || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	c := f.session(t)
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie flags: %+v", c)
	}
}

func TestSixthLoginBlocked(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 5; i++ {
		if rec := f.login("wrong"); rec.Code != 401 {
			t.Fatalf("attempt %d: %d", i+1, rec.Code)
		}
	}
	if rec := f.login("correct horse"); rec.Code != 429 {
		t.Fatalf("sixth attempt: %d, want 429", rec.Code)
	}
	f.clock = f.clock.Add(61 * time.Second)
	if rec := f.login("correct horse"); rec.Code != 204 {
		t.Fatalf("after a minute: %d", rec.Code)
	}
}

func TestCrossSitePostRejected(t *testing.T) {
	f := newFixture(t)
	c := f.session(t)
	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": "cross-site"}
	if rec := f.do("POST", "/admin/api/login", "password=correct+horse", form, nil); rec.Code != 403 {
		t.Fatalf("cross-site login: %d", rec.Code)
	}
	if rec := f.do("POST", "/admin/api/power", `{"action":"reboot"}`, map[string]string{"Sec-Fetch-Site": "same-site"}, c); rec.Code != 403 {
		t.Fatalf("same-site (other port) power: %d", rec.Code)
	}
	if rec := f.do("POST", "/admin/api/power", `{"action":"reboot"}`, map[string]string{"Sec-Fetch-Site": "same-origin"}, c); rec.Code != 200 {
		t.Fatalf("same-origin power: %d", rec.Code)
	}
}

func TestStatsNeedsSession(t *testing.T) {
	f := newFixture(t)
	if rec := f.do("GET", "/admin/api/stats", "", nil, nil); rec.Code != 401 {
		t.Fatalf("no cookie: %d", rec.Code)
	}
	if rec := f.do("GET", "/admin/api/stats", "", nil, &http.Cookie{Name: cookieName, Value: "forged"}); rec.Code != 401 {
		t.Fatalf("forged cookie: %d", rec.Code)
	}
}

func TestSessionExpires(t *testing.T) {
	f := newFixture(t)
	c := f.session(t)
	f.clock = f.clock.Add(sessionIdle - time.Minute)
	if f.a.valid(reqWith(c)) == false {
		t.Fatal("session died early")
	}
	f.clock = f.clock.Add(sessionIdle + time.Second) // idle past the limit since the last use
	if rec := f.do("GET", "/admin/api/stats", "", nil, c); rec.Code != 401 {
		t.Fatalf("expired session: %d", rec.Code)
	}
}

func reqWith(c *http.Cookie) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	return r
}

func TestPowerNonceSingleUse(t *testing.T) {
	f := newFixture(t)
	c := f.session(t)
	nonceFor := func(action string) string {
		rec := f.do("POST", "/admin/api/power", `{"action":"`+action+`"}`, nil, c)
		s := rec.Body.String()
		i := strings.Index(s, `"nonce":"`) + 9
		return s[i : i+64]
	}
	n := nonceFor("reboot")
	if len(f.ran) != 0 {
		t.Fatal("first step ran systemctl")
	}
	if rec := f.do("POST", "/admin/api/power", `{"action":"poweroff","nonce":"`+n+`"}`, nil, c); rec.Code != 400 {
		t.Fatalf("nonce for a different action: %d", rec.Code)
	}
	n = nonceFor("reboot")
	if rec := f.do("POST", "/admin/api/power", `{"action":"reboot","nonce":"`+n+`"}`, nil, c); rec.Code != 202 {
		t.Fatalf("confirm: %d", rec.Code)
	}
	if rec := f.do("POST", "/admin/api/power", `{"action":"reboot","nonce":"`+n+`"}`, nil, c); rec.Code != 400 {
		t.Fatalf("reused nonce: %d", rec.Code)
	}
	if len(f.ran) != 1 || f.ran[0] != "reboot" {
		t.Fatalf("systemctl calls: %v", f.ran)
	}
	n = nonceFor("reboot")
	f.clock = f.clock.Add(nonceTTL + time.Second)
	if rec := f.do("POST", "/admin/api/power", `{"action":"reboot","nonce":"`+n+`"}`, nil, c); rec.Code != 400 {
		t.Fatalf("expired nonce: %d", rec.Code)
	}
}

func TestAdminWithoutPassword(t *testing.T) {
	f := newFixture(t)
	f.a.hashFile = filepath.Join(t.TempDir(), "missing")
	rec := f.do("GET", "/admin", "", nil, nil)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "pihub passwd") {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
}
