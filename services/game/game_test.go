package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

var t0 = time.Unix(1760000000, 0)

func testChars() []Character {
	var cs []Character
	for i := 0; i < 24; i++ {
		c := Character{ID: fmt.Sprintf("char-%02d", i), Name: fmt.Sprintf("Hero %02d", i), Kind: "rider"}
		if i < 2 {
			c.Clip, c.Poster = fmt.Sprintf("c%d.mp4", i), fmt.Sprintf("c%d.jpg", i)
		}
		cs = append(cs, c)
	}
	return cs
}

func newGame() *Game { return NewGame(testChars(), rand.New(rand.NewPCG(1, 2))) }

func mustJoin(t *testing.T, g *Game, name string) string {
	t.Helper()
	tok, err := g.Join("", name)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func act(t *testing.T, g *Game, tok, typ string, now time.Time) {
	t.Helper()
	if err := g.Act(tok, Action{Type: typ}, now); err != nil {
		t.Fatalf("%s: %v", typ, err)
	}
}

// startedGame returns a game in Drawing, round 1.
func startedGame(t *testing.T, rounds int) (*Game, string, string) {
	t.Helper()
	g := newGame()
	a, b := mustJoin(t, g, "A"), mustJoin(t, g, "B")
	if err := g.Act(a, Action{Type: "set_rounds", Rounds: rounds}, t0); err != nil {
		t.Fatal(err)
	}
	act(t, g, a, "start", t0)
	return g, a, b
}

func playing(t *testing.T, rounds int) (*Game, string, string) {
	g, a, b := startedGame(t, rounds)
	act(t, g, a, "draw", t0)
	act(t, g, b, "draw", t0)
	return g, a, b
}

func TestJoinRules(t *testing.T) {
	g := newGame()
	a := mustJoin(t, g, "  Violet ")
	if g.players[0].Name != "Violet" {
		t.Fatalf("name not trimmed: %q", g.players[0].Name)
	}
	cases := []struct {
		name, token string
		want        *apiError
	}{
		{"", "", errName},
		{strings.Repeat("x", 21), "", errName},
		{"violet", "", errTaken},
		{"ignored", a, nil},
		{"B", "", nil},
		{"C", "", errBonded},
	}
	for _, c := range cases {
		tok, err := g.Join(c.token, c.name)
		if c.want == nil && (err != nil || tok == "") || c.want != nil && err != c.want {
			t.Errorf("join(%q,%q) = %q, %v; want %v", c.name, c.token, tok, err, c.want)
		}
	}
	if errBonded.Message != "The dragons have decided not to bond with you. Try again next year..." {
		t.Error("bonded message drifted from API.md")
	}
}

func TestLobbyRules(t *testing.T) {
	g := newGame()
	a := mustJoin(t, g, "A")
	if err := g.Act(a, Action{Type: "start"}, t0); err != errPlayers {
		t.Errorf("solo start: %v", err)
	}
	mustJoin(t, g, "B")
	for _, n := range []int{0, 11, 13} {
		if err := g.Act(a, Action{Type: "set_rounds", Rounds: n}, t0); err != errRounds {
			t.Errorf("rounds %d: %v", n, err)
		}
	}
	for _, typ := range []string{"draw", "got_it", "forfeit", "next", "back_to_lobby"} {
		if err := g.Act(a, Action{Type: typ}, t0); err != errPhase {
			t.Errorf("%s in lobby: %v", typ, err)
		}
	}
	if err := g.Act("nope", Action{Type: "start"}, t0); err != errUnknown {
		t.Errorf("unknown token: %v", err)
	}
	if err := g.Act(a, Action{Type: "zzz"}, t0); err != errAction {
		t.Errorf("unknown action: %v", err)
	}
}

func TestScoring(t *testing.T) {
	cases := []struct {
		name         string
		after        time.Duration
		aRes, bRes   string
		gainA, gainB int
	}{
		{"instant", 0, gotIt, forfeit, 3, 0},
		{"remaining 91s", 89 * time.Second, gotIt, gotIt, 2, 2},
		{"remaining 90s", 90 * time.Second, gotIt, gotIt, 2, 2},
		{"remaining 80s", 100 * time.Second, gotIt, gotIt, 1, 1},
		{"remaining 1s", 179 * time.Second, gotIt, gotIt, 1, 1},
		{"forfeit both", 5 * time.Second, forfeit, forfeit, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, a, b := playing(t, 1)
			now := t0.Add(c.after)
			act(t, g, a, c.aRes, now)
			act(t, g, b, c.bRes, now)
			if g.phase != RoundEnd {
				t.Fatalf("phase %s", g.phase)
			}
			if g.players[0].Score != c.gainA || g.players[1].Score != c.gainB {
				t.Errorf("scores %d,%d want %d,%d", g.players[0].Score, g.players[1].Score, c.gainA, c.gainB)
			}
		})
	}
}

func TestFullGameAndHistory(t *testing.T) {
	g, a, b := playing(t, 2)
	act(t, g, a, gotIt, t0)
	act(t, g, b, forfeit, t0)
	act(t, g, a, "next", t0)
	if g.phase != Drawing || g.round != 2 {
		t.Fatalf("after next: %s round %d", g.phase, g.round)
	}
	act(t, g, a, "draw", t0)
	act(t, g, b, "draw", t0)
	act(t, g, a, forfeit, t0)
	act(t, g, b, gotIt, t0)
	if g.TakeFinished() != nil {
		t.Fatal("history before final")
	}
	act(t, g, b, "next", t0)
	if g.phase != Final {
		t.Fatalf("phase %s", g.phase)
	}
	e := g.TakeFinished()
	if e == nil || e.Rounds != 2 || e.Players[0].Score != 3 || e.Players[1].Score != 3 {
		t.Fatalf("entry %+v", e)
	}
	if g.TakeFinished() != nil {
		t.Error("history handed out twice")
	}
	if v := g.View(a, t0); !v.Final.Tie || v.Final.Winner != "" {
		t.Errorf("final %+v", v.Final)
	}
	act(t, g, a, "back_to_lobby", t0)
	if g.phase != Lobby || len(g.players) != 2 || g.players[0].Score != 0 {
		t.Errorf("back_to_lobby: %s %d", g.phase, len(g.players))
	}
}

func TestSecretsDistinctAcrossGame(t *testing.T) {
	g, a, b := startedGame(t, 10)
	seen := map[string]bool{}
	for r := 1; r <= 10; r++ {
		for _, p := range g.players {
			if seen[p.secret.ID] {
				t.Fatalf("round %d repeats %s", r, p.secret.ID)
			}
			seen[p.secret.ID] = true
		}
		act(t, g, a, "draw", t0)
		act(t, g, b, "draw", t0)
		act(t, g, a, forfeit, t0)
		act(t, g, b, forfeit, t0)
		act(t, g, a, "next", t0)
	}
	if g.phase != Final {
		t.Fatal(g.phase)
	}
}

func TestIdempotence(t *testing.T) {
	g, a, b := playing(t, 2)
	act(t, g, a, gotIt, t0)
	act(t, g, a, gotIt, t0.Add(time.Minute))
	act(t, g, a, forfeit, t0.Add(time.Minute))
	if g.players[0].gain != 3 || g.players[0].resolved != gotIt || g.phase != Playing {
		t.Fatalf("double press changed state: %+v", g.players[0])
	}
	act(t, g, b, forfeit, t0)
	act(t, g, a, gotIt, t0) // stale press after roundEnd
	act(t, g, a, "next", t0)
	snap := fmt.Sprintf("%s %d %s %s", g.phase, g.round, g.players[0].secret.ID, g.players[1].secret.ID)
	act(t, g, a, "next", t0)
	act(t, g, b, "next", t0)
	if got := fmt.Sprintf("%s %d %s %s", g.phase, g.round, g.players[0].secret.ID, g.players[1].secret.ID); got != snap {
		t.Fatalf("double next changed state: %s -> %s", snap, got)
	}
	act(t, g, a, "draw", t0)
	act(t, g, a, "draw", t0)
	if g.phase != Drawing || g.players[1].drawn {
		t.Error("double draw advanced the round")
	}
}

func TestTimeoutInjectedClock(t *testing.T) {
	g, a, b := playing(t, 1)
	act(t, g, a, gotIt, t0.Add(10*time.Second))
	if g.Tick(t0.Add(179*time.Second)) || g.phase != Playing {
		t.Fatal("ticked early")
	}
	if wake := g.NextWake(); !wake.Equal(t0.Add(RoundSeconds * time.Second)) {
		t.Fatalf("wake %v", wake)
	}
	if !g.Tick(t0.Add(180 * time.Second)) {
		t.Fatal("no tick at deadline")
	}
	if g.phase != RoundEnd || g.players[1].resolved != forfeit || g.players[1].gain != 0 || g.players[0].Score != 2 {
		t.Fatalf("after timeout: %s %+v", g.phase, g.players[1])
	}
	if !g.NextWake().IsZero() {
		t.Error("wake after roundEnd")
	}
	_ = b
}

func TestLatePressCountsAsTimeout(t *testing.T) {
	g, a, _ := playing(t, 1)
	act(t, g, a, gotIt, t0.Add(181*time.Second))
	if g.phase != RoundEnd || g.players[0].gain != 0 {
		t.Fatalf("late got_it: %s gain %d", g.phase, g.players[0].gain)
	}
}

func TestLeaveAndReap(t *testing.T) {
	g, a, b := playing(t, 2)
	act(t, g, a, gotIt, t0)
	act(t, g, b, "leave", t0)
	if g.phase != Lobby || len(g.players) != 1 || g.players[0].Token != a || g.players[0].Score != 0 || g.round != 0 {
		t.Fatalf("leave mid-game: %s %d", g.phase, len(g.players))
	}
	act(t, g, a, "leave", t0)
	if len(g.players) != 0 {
		t.Fatal("lobby leave kept player")
	}

	g, a, b = startedGame(t, 1)
	g.SetConnected(a, true, t0)
	g.SetConnected(b, true, t0)
	g.SetConnected(a, false, t0.Add(time.Minute))
	g.SetConnected(b, false, t0.Add(2*time.Minute))
	if g.Tick(t0.Add(11*time.Minute)) || g.phase != Drawing {
		t.Fatal("reaped before ReapAfter")
	}
	if !g.NextWake().Equal(t0.Add(2*time.Minute + ReapAfter)) {
		t.Fatalf("wake %v", g.NextWake())
	}
	g.SetConnected(a, true, t0.Add(13*time.Minute))
	if g.Tick(t0.Add(40 * time.Minute)) {
		t.Fatal("reaped with a player connected")
	}
	g.SetConnected(a, false, t0.Add(41*time.Minute))
	if !g.Tick(t0.Add(41*time.Minute+ReapAfter)) || g.phase != Lobby || len(g.players) != 0 {
		t.Fatal("not reaped")
	}
}

// leaked reports which forbidden strings appear in the JSON.
func leaked(v *View, forbidden []string) []string {
	b, _ := json.Marshal(v)
	var out []string
	for _, f := range forbidden {
		if f != "" && strings.Contains(string(b), f) {
			out = append(out, f)
		}
	}
	return out
}

func secretTokens(p *Player) []string {
	s := p.secret
	return []string{s.Name, s.ID, s.Clip, s.Poster, p.opaque}
}

func TestNoLeakBeforeRoundEnd(t *testing.T) {
	g, a, b := startedGame(t, 2)
	// force media on both secrets so clip/poster names are checked too
	for i, p := range g.players {
		g.chars[i].Clip, g.chars[i].Poster = fmt.Sprintf("c%d.mp4", i), fmt.Sprintf("c%d.jpg", i)
		p.secret = &g.chars[i]
	}
	toks := []string{a, b}
	check := func(label string) {
		for i, p := range g.players {
			other := toks[1-i]
			if got := leaked(g.View(other, t0), secretTokens(p)); len(got) > 0 {
				t.Errorf("%s: view of %s leaks holder %s secret: %v", label, other, p.Token, got)
			}
		}
	}
	check("drawing, none drawn")
	act(t, g, a, "draw", t0)
	check("drawing, A drawn")
	act(t, g, b, "draw", t0)
	check("playing")
	act(t, g, a, gotIt, t0)
	check("playing, A resolved")

	// the holder does see their own secret, so the check has teeth
	if len(leaked(g.View(a, t0), secretTokens(g.players[0]))) == 0 {
		t.Error("holder view missing own secret")
	}
	act(t, g, b, forfeit, t0)
	if len(leaked(g.View(a, t0), []string{g.players[1].secret.Name})) == 0 {
		t.Error("roundEnd must reveal")
	}
}

func TestLeakDetectorNegativeControl(t *testing.T) {
	g, a, b := playing(t, 1)
	v := g.View(b, t0)
	secret := g.players[0].secret.Name
	v.Secret = &SecretView{CharView: CharView{Name: secret}}
	if len(leaked(v, []string{secret})) == 0 {
		t.Fatal("detector missed an injected secret")
	}
	_ = a
}

func TestMediaTokenLifecycle(t *testing.T) {
	g, a, b := startedGame(t, 1)
	g.chars[0].Clip, g.chars[0].Poster = "x.mp4", "x.jpg"
	g.players[0].secret = &g.chars[0]
	op := g.players[0].opaque
	if _, ok := g.Media(op, "clip"); ok {
		t.Fatal("served before draw")
	}
	act(t, g, a, "draw", t0)
	if f, ok := g.Media(op, "clip"); !ok || f != "x.mp4" {
		t.Fatalf("clip %q %v", f, ok)
	}
	if f, _ := g.Media(op, "poster"); f != "x.jpg" {
		t.Fatalf("poster %q", f)
	}
	act(t, g, b, "draw", t0)
	act(t, g, a, forfeit, t0)
	act(t, g, b, forfeit, t0)
	if _, ok := g.Media(op, "clip"); ok {
		t.Fatal("served after roundEnd")
	}
}
