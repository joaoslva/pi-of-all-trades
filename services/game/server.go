package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const keepalive = 15 * time.Second

// historyLine is one line of history.jsonl: the public entry plus the secret ids, which /api/history never shows.
type historyLine struct {
	HistoryEntry
	Chars []string `json:"chars,omitempty"`
}

type Server struct {
	mu       sync.Mutex
	g        *Game
	subs     map[string]map[chan []byte]struct{}
	timer    *time.Timer
	history  []HistoryEntry // newest first
	histPath string
	mediaDir string
	web      http.Handler
	now      func() time.Time
}

func NewServer(g *Game, dataDir, contentDir, webDir string) (*Server, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	s := &Server{
		g:        g,
		subs:     map[string]map[chan []byte]struct{}{},
		histPath: filepath.Join(dataDir, "history.jsonl"),
		mediaDir: filepath.Join(contentDir, "media"),
		web:      noCache(http.FileServer(http.Dir(webDir))),
		now:      time.Now,
	}
	s.timer = time.AfterFunc(time.Hour, s.wake)
	s.timer.Stop()
	s.loadHistory()
	var recent []string
	for _, e := range s.history {
		recent = append(recent, e.Chars...)
	}
	g.Remember(recent)
	return s, nil
}

// noCache makes phones revalidate the page on every load (FileServer answers 304), so a deploy is picked up at once.
func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) loadHistory() {
	f, err := os.Open(s.histPath)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l historyLine
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			e := l.HistoryEntry
			e.Chars = l.Chars
			s.history = append([]HistoryEntry{e}, s.history...)
			if len(s.history) > HistoryKeep {
				s.history = s.history[:HistoryKeep]
			}
		}
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/join", s.join)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("POST /api/action", s.action)
	mux.HandleFunc("GET /api/history", s.getHistory)
	mux.HandleFunc("GET /m/{opaque}/{which}", s.media)
	mux.Handle("/", s.web)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	if e, ok := err.(*apiError); ok {
		writeJSON(w, e.Status, e)
		return
	}
	writeJSON(w, 400, &apiError{400, "bad_request", err.Error()})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(v); err != nil {
		writeErr(w, &apiError{400, "bad_request", "Malformed JSON body."})
		return false
	}
	return true
}

// changed must be called with s.mu held after every state mutation.
func (s *Server) changed(now time.Time) {
	if e := s.g.TakeFinished(); e != nil {
		s.history = append([]HistoryEntry{*e}, s.history...)
		if len(s.history) > HistoryKeep {
			s.history = s.history[:HistoryKeep]
		}
		s.appendHistory(e)
	}
	for _, p := range s.g.players {
		if len(s.subs[p.Token]) == 0 {
			continue
		}
		b, _ := json.Marshal(s.g.View(p.Token, now))
		for ch := range s.subs[p.Token] {
			select {
			case ch <- b:
			default:
				select {
				case <-ch:
				default:
				}
				ch <- b
			}
		}
	}
	s.timer.Stop()
	if t := s.g.NextWake(); !t.IsZero() {
		s.timer.Reset(max(t.Sub(now), time.Millisecond))
	}
}

func (s *Server) appendHistory(e *HistoryEntry) {
	b, _ := json.Marshal(historyLine{*e, e.Chars})
	f, err := os.OpenFile(s.histPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		log.Printf("history write: %v", err)
	}
}

func (s *Server) wake() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.g.Tick(now)
	s.changed(now)
}

func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, Token string }
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.g.Tick(now)
	token, err := s.g.Join(req.Token, req.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.changed(now)
	writeJSON(w, 200, map[string]any{"token": token, "view": s.g.View(token, now)})
}

func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	var a Action
	if !decode(w, r, &a) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if err := s.g.Act(token, a, now); err != nil {
		writeErr(w, err)
		return
	}
	if a.Type == "leave" {
		for ch := range s.subs[token] {
			close(ch)
		}
		delete(s.subs, token)
	}
	s.changed(now)
	if a.Type == "leave" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, 200, s.g.View(token, now))
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, &apiError{500, "stream", "Streaming unsupported."})
		return
	}
	ch := make(chan []byte, 1)
	s.mu.Lock()
	if s.g.player(token) == nil {
		s.mu.Unlock()
		writeErr(w, errUnknown)
		return
	}
	if s.subs[token] == nil {
		s.subs[token] = map[chan []byte]struct{}{}
	}
	s.subs[token][ch] = struct{}{}
	s.g.SetConnected(token, true, s.now())
	s.changed(s.now())
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if set, ok := s.subs[token]; ok {
			delete(set, ch)
			if len(set) == 0 {
				delete(s.subs, token)
				s.g.SetConnected(token, false, s.now())
				s.changed(s.now())
			}
		}
	}()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	fl.Flush()
	tick := time.NewTicker(keepalive)
	defer tick.Stop()
	for {
		select {
		case b, open := <-ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "event: view\ndata: %s\n\n", b)
		case <-tick.C:
			fmt.Fprint(w, ": keepalive\n\n")
		case <-r.Context().Done():
			return
		}
		fl.Flush()
	}
}

func (s *Server) getHistory(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	h := append([]HistoryEntry{}, s.history...)
	s.mu.Unlock()
	writeJSON(w, 200, h)
}

func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	file, ok := s.g.Media(r.PathValue("opaque"), r.PathValue("which"))
	s.mu.Unlock()
	if !ok || (r.PathValue("which") != "clip" && r.PathValue("which") != "poster") || strings.ContainsAny(file, `/\`) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.mediaDir, file))
}
