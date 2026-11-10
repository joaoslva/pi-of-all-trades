package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type client struct {
	t     *testing.T
	base  string
	token string
	views chan View
	stop  context.CancelFunc
}

func newTestServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	chars, err := loadCharacters("testdata/content/characters.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(NewGame(chars, rand.New(rand.NewPCG(3, 4))), dir, "testdata/content", "testdata/web")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(ts.CloseClientConnections)
	return s, ts, dir
}

func post(t *testing.T, url string, body any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (c *client) join(name, token string) (int, []byte) {
	code, body := post(c.t, c.base+"/api/join", map[string]string{"name": name, "token": token})
	if code == 200 {
		var r struct{ Token string }
		json.Unmarshal(body, &r)
		c.token = r.Token
	}
	return code, body
}

func (c *client) connect() {
	ctx, cancel := context.WithCancel(context.Background())
	c.stop = cancel
	c.views = make(chan View, 64)
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+"/api/events?token="+c.token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		c.t.Fatalf("events: %v %v", err, resp)
	}
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var v View
				json.Unmarshal([]byte(d), &v)
				c.views <- v
			}
		}
	}()
}

// await returns the first streamed view satisfying ok.
func (c *client) await(ok func(View) bool) View {
	c.t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case v := <-c.views:
			if ok(v) {
				return v
			}
		case <-timeout:
			c.t.Fatal("timed out waiting for view")
		}
	}
}

func (c *client) act(typ string, extra map[string]any) (int, []byte) {
	if extra == nil {
		extra = map[string]any{}
	}
	extra["type"] = typ
	return post(c.t, c.base+"/api/action?token="+c.token, extra)
}

func (c *client) must(typ string) {
	c.t.Helper()
	if code, body := c.act(typ, nil); code/100 != 2 {
		c.t.Fatalf("%s: %d %s", typ, code, body)
	}
}

func inPhase(p Phase) func(View) bool { return func(v View) bool { return v.Phase == p } }

func TestTwoClientGame(t *testing.T) {
	_, ts, dir := newTestServer(t)
	a, b := &client{t: t, base: ts.URL}, &client{t: t, base: ts.URL}
	if code, _ := a.join("Ann", ""); code != 200 {
		t.Fatal(code)
	}
	b.join("Bob", "")
	code, body := (&client{t: t, base: ts.URL}).join("Cat", "")
	var e apiError
	json.Unmarshal(body, &e)
	if code != 409 || e.Code != "bonded" || e.Message != "The dragons have decided not to bond with you. Try again next year..." {
		t.Fatalf("third join: %d %s", code, body)
	}
	if code, _ := a.join("whatever", a.token); code != 200 {
		t.Fatal("rejoin refused")
	}
	if code, _ := (&client{t: t, base: ts.URL, token: "x"}).act("start", nil); code != 401 {
		t.Fatalf("bad token action: %d", code)
	}
	a.connect()
	b.connect()
	a.await(func(v View) bool { return v.Other != nil && v.Other.Connected })

	a.act("set_rounds", map[string]any{"rounds": 2})
	a.must("start")
	a.await(inPhase(Drawing))
	b.await(inPhase(Drawing))
	a.must("draw")
	va := a.await(func(v View) bool { return v.Secret != nil })
	b.must("draw")
	vb := b.await(func(v View) bool { return v.Phase == Playing && v.Secret != nil })

	if va.Secret.Clip != "" {
		resp, err := http.Get(ts.URL + va.Secret.Clip)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("clip fetch: %v %v", err, resp)
		}
		resp.Body.Close()
	}
	if resp, _ := http.Get(ts.URL + "/m/deadbeef/clip"); resp.StatusCode != 404 {
		t.Fatal("bogus media token served")
	}
	_ = vb

	a.must("got_it")
	a.must("got_it")
	b.must("forfeit")
	end := a.await(inPhase(RoundEnd))
	if end.Reveal == nil || end.Reveal.YourGuess.Name != vb.Secret.Name || end.Reveal.TheirGuess.Name != va.Secret.Name {
		t.Fatalf("reveal %+v", end.Reveal)
	}
	if end.You.Score != 3 || end.Other.Score != 0 {
		t.Fatalf("scores %+v %+v", end.You, end.Other)
	}
	if va.Secret.Clip != "" {
		if resp, _ := http.Get(ts.URL + va.Secret.Clip); resp.StatusCode != 404 {
			t.Fatal("media alive after roundEnd")
		}
	}

	// mid-game disconnect and rejoin keeps state
	b.stop()
	a.await(func(v View) bool { return v.Other != nil && !v.Other.Connected })
	b2 := &client{t: t, base: ts.URL, token: b.token}
	if code, body := b2.join("", b.token); code != 200 || !strings.Contains(string(body), `"roundEnd"`) {
		t.Fatalf("rejoin: %d %s", code, body)
	}
	b2.connect()
	b2.await(func(v View) bool { return v.Phase == RoundEnd && v.Round == 1 && v.Other.Score == 3 })

	b2.must("next")
	b2.must("next")
	a.await(inPhase(Drawing))
	if v := a.await(func(v View) bool { return v.Round == 2 }); v.Phase != Drawing {
		t.Fatal(v.Phase)
	}
	if hist, _ := os.ReadFile(filepath.Join(dir, "history.jsonl")); len(hist) != 0 {
		t.Fatal("history written before final")
	}
	a.must("draw")
	b2.must("draw")
	b2.await(inPhase(Playing))
	a.must("forfeit")
	b2.must("got_it")
	a.await(inPhase(RoundEnd))
	a.must("next")
	f := a.await(inPhase(Final))
	if f.Final == nil || !f.Final.Tie {
		t.Fatalf("final %+v", f.Final)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "history.jsonl"))
	if strings.Count(string(raw), "\n") != 1 {
		t.Fatalf("history file: %q", raw)
	}
	resp, _ := http.Get(ts.URL + "/api/history")
	var h []HistoryEntry
	json.NewDecoder(resp.Body).Decode(&h)
	if len(h) != 1 || h[0].Rounds != 2 || h[0].Players[0].Name != "Ann" || h[0].Players[0].Score != 2 {
		t.Fatalf("history %+v", h)
	}
	a.must("back_to_lobby")
	a.await(inPhase(Lobby))
	if resp, _ := http.Get(ts.URL + "/"); resp.StatusCode != 200 {
		t.Fatal("static not served")
	}
}

func TestRealTimerTimeout(t *testing.T) {
	s, ts, _ := newTestServer(t)
	a, b := &client{t: t, base: ts.URL}, &client{t: t, base: ts.URL}
	a.join("Ann", "")
	b.join("Bob", "")
	a.connect()
	a.must("start")
	a.must("draw")
	b.must("draw")
	// move the deadline into the past; the armed timer must end the round on its own
	s.mu.Lock()
	s.g.deadline = time.Now().Add(50 * time.Millisecond)
	s.changed(time.Now())
	s.mu.Unlock()
	end := a.await(inPhase(RoundEnd))
	if *end.Resolved.You != forfeit || *end.Resolved.Other != forfeit {
		t.Fatalf("resolved %+v", end.Resolved)
	}
}
