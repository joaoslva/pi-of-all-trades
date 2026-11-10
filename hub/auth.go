package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// pbkdf2Iterations is stored inside each hash, so it can change later.
// Measured on the Pi Zero 2 W (see docs/admin.md).
const pbkdf2Iterations = 25000

const (
	cookieName   = "pihub_session"
	sessionIdle  = 30 * time.Minute
	nonceTTL     = 60 * time.Second
	loginLimit   = 5
	loginWindow  = time.Minute
	minPasswordN = 8
)

func hashPassword(pw string, iter int) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, iter, 32)
	if err != nil {
		return "", err
	}
	e := base64.RawStdEncoding
	return fmt.Sprintf("%d$%s$%s", iter, e.EncodeToString(salt), e.EncodeToString(key)), nil
}

func verifyPassword(stored, pw string) bool {
	parts := strings.Split(strings.TrimSpace(stored), "$")
	if len(parts) != 3 {
		return false
	}
	iter, err := strconv.Atoi(parts[0])
	salt, err2 := base64.RawStdEncoding.DecodeString(parts[1])
	want, err3 := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || err2 != nil || err3 != nil || iter < 1 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// Auth holds the admin sessions, power nonces and login rate limit, all in memory.
type Auth struct {
	hashFile string
	now      func() time.Time
	run      func(args ...string) error // systemctl; swapped in tests

	mu       sync.Mutex
	sessions map[string]time.Time // token -> last seen
	nonces   map[string]nonce
	logins   map[string][]time.Time // client -> recent attempts
}

type nonce struct {
	action  string
	expires time.Time
}

func NewAuth(hashFile string) *Auth {
	return &Auth{
		hashFile: hashFile,
		now:      time.Now,
		run:      func(args ...string) error { return exec.Command("systemctl", args...).Run() },
		sessions: map[string]time.Time{},
		nonces:   map[string]nonce{},
		logins:   map[string][]time.Time{},
	}
}

func randomHex() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// originOK rejects cross-site writes. Sec-Fetch-Site is set by browsers and
// cannot be forged by pages; Origin is the fallback; clients sending neither
// are not browsers and cannot carry someone else's cookie.
func originOK(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" {
		return s == "same-origin" || s == "none"
	}
	if o := r.Header.Get("Origin"); o != "" {
		return strings.EqualFold(o, "http://"+r.Host)
	}
	return true
}

// allow counts every login attempt, right or wrong, in the last minute.
func (a *Auth) allow(client string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for c, ts := range a.logins { // drop idle clients so the map cannot grow
		if now.Sub(ts[len(ts)-1]) > loginWindow {
			delete(a.logins, c)
		}
	}
	recent := a.logins[client]
	for len(recent) > 0 && now.Sub(recent[0]) > loginWindow {
		recent = recent[1:]
	}
	if len(recent) >= loginLimit {
		a.logins[client] = recent
		return false
	}
	a.logins[client] = append(recent, now)
	return true
}

func (a *Auth) valid(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	seen, ok := a.sessions[c.Value]
	if !ok {
		return false
	}
	if a.now().Sub(seen) > sessionIdle {
		delete(a.sessions, c.Value)
		return false
	}
	a.sessions[c.Value] = a.now()
	return true
}

// require wraps a handler: cross-site writes get 403, no session gets 401.
func (a *Auth) require(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !originOK(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		if !a.valid(r) {
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	if !originOK(r) {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	client, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !a.allow(client) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}
	stored, err := os.ReadFile(a.hashFile)
	if err != nil {
		http.Error(w, "no admin password set", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !verifyPassword(string(stored), r.PostFormValue("password")) {
		http.Error(w, "wrong password", http.StatusUnauthorized)
		return
	}
	tok := randomHex()
	a.mu.Lock()
	for t, seen := range a.sessions {
		if a.now().Sub(seen) > sessionIdle {
			delete(a.sessions, t)
		}
	}
	a.sessions[tok] = a.now()
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

// issueNonce returns a one-time token that confirms exactly this action for nonceTTL.
func (a *Auth) issueNonce(action string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := randomHex()
	a.nonces[n] = nonce{action, a.now().Add(nonceTTL)}
	for k, v := range a.nonces {
		if a.now().After(v.expires) {
			delete(a.nonces, k)
		}
	}
	return n
}

// spendNonce is true once per token, and only for the action it was issued for.
func (a *Auth) spendNonce(token, action string) bool {
	a.mu.Lock()
	n, ok := a.nonces[token]
	delete(a.nonces, token) // single use, even when it fails below
	a.mu.Unlock()
	return ok && n.action == action && !a.now().After(n.expires)
}

// power is two steps: {"action":"reboot"} returns a one-time nonce, then
// {"action":"reboot","nonce":"..."} runs it.
func (a *Auth) power(w http.ResponseWriter, r *http.Request) {
	var req struct{ Action, Nonce string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil ||
		(req.Action != "reboot" && req.Action != "poweroff") {
		http.Error(w, `action must be "reboot" or "poweroff"`, http.StatusBadRequest)
		return
	}
	if req.Nonce == "" {
		json.NewEncoder(w).Encode(map[string]string{"nonce": a.issueNonce(req.Action)})
		return
	}
	if !a.spendNonce(req.Nonce, req.Action) {
		http.Error(w, "nonce invalid or expired", http.StatusBadRequest)
		return
	}
	if err := a.run(req.Action); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (a *Auth) stats(w http.ResponseWriter, r *http.Request) {
	s, err := CollectStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(s)
}

// adminPage answers 503 with the setup command until a password exists.
func (a *Auth) adminPage(w http.ResponseWriter, r *http.Request) {
	if _, err := os.Stat(a.hashFile); err != nil {
		http.Error(w, "No admin password yet. On the Pi run: sudo pihub passwd", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(adminHTML)
}

// setPassword is `pihub passwd`: run as root, writes the hash readable by the hub's group only.
func setPassword(file, group, pw string) error {
	if len(pw) < minPasswordN {
		return fmt.Errorf("password must be at least %d characters", minPasswordN)
	}
	h, err := hashPassword(pw, pbkdf2Iterations)
	if err != nil {
		return err
	}
	g, err := user.LookupGroup(group)
	if err != nil {
		return err
	}
	gid, _ := strconv.Atoi(g.Gid)
	tmp := filepath.Join(filepath.Dir(file), ".admin.hash.tmp")
	if err := os.WriteFile(tmp, []byte(h+"\n"), 0o640); err != nil {
		return err
	}
	if err := errors.Join(os.Chmod(tmp, 0o640), os.Chown(tmp, 0, gid)); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, file)
}
