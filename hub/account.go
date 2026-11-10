package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxHint = 120

func registerAccount(mux *http.ServeMux, a *Auth) {
	mux.HandleFunc("GET /admin/api/hint", a.hint)
	mux.HandleFunc("POST /admin/api/password", a.require(a.changePassword))
	mux.HandleFunc("GET /admin/api/system", a.require(a.system))
}

// The hint lives next to the hash, so a throwaway instance with its own --hash-file stays isolated.
func (a *Auth) hintFile() string { return filepath.Join(filepath.Dir(a.hashFile), "password-hint.txt") }

func (a *Auth) readHint() string {
	b, _ := os.ReadFile(a.hintFile())
	return strings.TrimSpace(string(b))
}

// hint is unauthenticated on purpose: the login screen shows it.
func (a *Auth) hint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]string{"hint": a.readHint()})
}

func validHint(h string) string {
	if utf8.RuneCountInString(h) > maxHint {
		return "hint is longer than 120 characters"
	}
	if strings.IndexFunc(h, unicode.IsControl) >= 0 {
		return "hint must not contain control characters"
	}
	return ""
}

func (a *Auth) changePassword(w http.ResponseWriter, r *http.Request) {
	client, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !a.allow(client) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}
	var req struct {
		Current, New string
		Hint         *string
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	stored, err := os.ReadFile(a.hashFile)
	if err != nil {
		http.Error(w, "no admin password set", http.StatusServiceUnavailable)
		return
	}
	if !verifyPassword(string(stored), req.Current) {
		http.Error(w, "wrong current password", http.StatusUnauthorized)
		return
	}
	if utf8.RuneCountInString(req.New) < minPasswordN {
		http.Error(w, "new password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	if req.New == req.Current {
		http.Error(w, "new password must differ from the current one", http.StatusBadRequest)
		return
	}
	hint := a.readHint()
	if req.Hint != nil {
		hint = strings.TrimSpace(*req.Hint)
	}
	if msg := validHint(hint); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	lower := strings.ToLower(hint)
	if hint != "" && (strings.Contains(lower, strings.ToLower(req.Current)) || strings.Contains(lower, strings.ToLower(req.New))) {
		http.Error(w, "hint must not contain the password", http.StatusBadRequest)
		return
	}
	h, err := hashPassword(req.New, pbkdf2Iterations)
	if err == nil {
		err = writeFileAtomic(a.hashFile, []byte(h+"\n"), 0o600, -1, -1)
	}
	if err != nil {
		http.Error(w, "could not store the password", http.StatusInternalServerError)
		return
	}
	if c, err := r.Cookie(cookieName); err == nil {
		a.dropOtherSessions(c.Value)
	}
	if req.Hint != nil {
		if hint == "" {
			err = os.Remove(a.hintFile())
			if os.IsNotExist(err) {
				err = nil
			}
		} else {
			err = writeFileAtomic(a.hintFile(), []byte(hint+"\n"), 0o600, -1, -1)
		}
		if err != nil {
			http.Error(w, "password changed, but the hint could not be stored", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
