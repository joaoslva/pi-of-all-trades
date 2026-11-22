package main

import (
	"encoding/hex"
	"math/rand/v2"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxPlayers    = 2
	RoundSeconds  = 180
	DefaultRounds = 3
	MinRounds     = 1
	MaxRounds     = 10
	MaxNameLen    = 20
	ReapAfter     = 10 * time.Minute
	HistoryKeep   = 20
)

type Phase string

const (
	Lobby    Phase = "lobby"
	Drawing  Phase = "drawing"
	Playing  Phase = "playing"
	RoundEnd Phase = "roundEnd"
	Final    Phase = "final"
)

const (
	gotIt   = "got_it"
	forfeit = "forfeit"
)

type Character struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Clip    string   `json:"clip,omitempty"`
	Poster  string   `json:"poster,omitempty"`
	Color   string   `json:"color,omitempty"`
	Tips    []string `json:"tips,omitempty"`
	Partner string   `json:"partner,omitempty"`
}

type Player struct {
	Token     string
	Name      string
	Connected bool
	Score     int

	secret   *Character
	opaque   string
	drawn    bool
	resolved string
	gain     int
}

type HistoryPlayer struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
}

type HistoryEntry struct {
	Finished string          `json:"finished"`
	Rounds   int             `json:"rounds"`
	Players  []HistoryPlayer `json:"players"`
	Chars    []string        `json:"-"` // secret ids in draw order; only the history file carries them
}

type Action struct {
	Type   string `json:"type"`
	Rounds int    `json:"rounds"`
}

type apiError struct {
	Status  int    `json:"-"`
	Code    string `json:"error"`
	Message string `json:"message"`
}

func (e *apiError) Error() string { return e.Code }

var (
	errPhase   = &apiError{409, "phase", "That is not possible right now."}
	errBonded  = &apiError{409, "bonded", "The dragons have decided not to bond with you. Try again next year..."}
	errName    = &apiError{400, "name", "Names are 1 to 20 characters."}
	errTaken   = &apiError{409, "name_taken", "Someone here already has that name."}
	errRounds  = &apiError{400, "rounds", "Pick a number of rounds the deck can support."}
	errPlayers = &apiError{409, "players", "Two players are needed to start."}
	errUnknown = &apiError{401, "unknown_token", "Unknown token."}
	errAction  = &apiError{400, "action", "Unknown action."}
)

// Game is the pure state machine: time and randomness are inputs, nothing else is touched.
type Game struct {
	rng       *rand.Rand
	chars     []Character
	phase     Phase
	players   []*Player
	rounds    int
	round     int
	used      map[string]bool // the shuffle bag: drawn since the last refill, kept across games
	drawn     []string        // this game's secret ids
	deadline  time.Time
	downSince time.Time
	finished  *HistoryEntry
}

func NewGame(chars []Character, rng *rand.Rand) *Game {
	return &Game{rng: rng, chars: chars, phase: Lobby, rounds: DefaultRounds, used: map[string]bool{}}
}

func (g *Game) hexToken() string {
	var b [16]byte
	for i := range b {
		b[i] = byte(g.rng.UintN(256))
	}
	return hex.EncodeToString(b[:])
}

func (g *Game) player(token string) *Player {
	for _, p := range g.players {
		if p.Token == token {
			return p
		}
	}
	return nil
}

func (g *Game) other(p *Player) *Player {
	for _, q := range g.players {
		if q != p {
			return q
		}
	}
	return nil
}

func (g *Game) maxRounds() int { return min(MaxRounds, len(g.chars)/MaxPlayers) }

// Remember seeds the bag with recently drawn ids, newest first, leaving at least one default game's worth free.
func (g *Game) Remember(ids []string) {
	for _, id := range ids {
		if len(g.used) >= len(g.chars)-DefaultRounds*MaxPlayers {
			return
		}
		g.used[id] = true
	}
}

func (g *Game) free() int {
	n := 0
	for i := range g.chars {
		if !g.used[g.chars[i].ID] {
			n++
		}
	}
	return n
}

// Join returns the token to use: the given one on a rejoin, a fresh one for a new player.
func (g *Game) Join(token, name string) (string, error) {
	if token != "" && g.player(token) != nil {
		return token, nil
	}
	if g.phase != Lobby || len(g.players) >= MaxPlayers {
		return "", errBonded
	}
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > MaxNameLen {
		return "", errName
	}
	for _, p := range g.players {
		if strings.EqualFold(p.Name, name) {
			return "", errTaken
		}
	}
	p := &Player{Token: g.hexToken(), Name: name}
	g.players = append(g.players, p)
	return p.Token, nil
}

func (g *Game) SetConnected(token string, up bool, now time.Time) {
	p := g.player(token)
	if p == nil {
		return
	}
	p.Connected = up
	g.downSince = time.Time{}
	for _, q := range g.players {
		if q.Connected {
			return
		}
	}
	g.downSince = now
}

// Tick applies the round timeout and the idle reap. It reports whether anything changed.
func (g *Game) Tick(now time.Time) bool {
	switch {
	case g.phase == Playing && !now.Before(g.deadline):
		for _, p := range g.players {
			if p.resolved == "" {
				p.resolved = forfeit
			}
		}
		g.endRound()
		return true
	case g.phase != Lobby && !g.downSince.IsZero() && now.Sub(g.downSince) >= ReapAfter:
		g.players = nil
		g.reset()
		return true
	}
	return false
}

// NextWake is when Tick next has something to do; zero if never.
func (g *Game) NextWake() time.Time {
	var t time.Time
	if g.phase == Playing {
		t = g.deadline
	}
	if g.phase != Lobby && !g.downSince.IsZero() {
		if r := g.downSince.Add(ReapAfter); t.IsZero() || r.Before(t) {
			t = r
		}
	}
	return t
}

func (g *Game) Act(token string, a Action, now time.Time) error {
	p := g.player(token)
	if p == nil {
		return errUnknown
	}
	g.Tick(now)
	switch a.Type {
	case "set_rounds":
		if g.phase != Lobby {
			return errPhase
		}
		if a.Rounds < MinRounds || a.Rounds > g.maxRounds() {
			return errRounds
		}
		g.rounds = a.Rounds
	case "start":
		if g.phase != Lobby {
			return errPhase
		}
		if len(g.players) < MaxPlayers {
			return errPlayers
		}
		if g.rounds > g.maxRounds() {
			return errRounds
		}
		// refill up front so secrets never repeat within one game
		if g.free() < g.rounds*MaxPlayers {
			g.used = map[string]bool{}
		}
		g.round = 0
		g.startRound()
	case "draw":
		if g.phase != Drawing && g.phase != Playing {
			return errPhase
		}
		if g.phase == Drawing {
			p.drawn = true
			if g.other(p).drawn {
				g.phase = Playing
				g.deadline = now.Add(RoundSeconds * time.Second)
			}
		}
	case gotIt, forfeit:
		return g.resolve(p, a.Type, now)
	case "next":
		switch g.phase {
		case Lobby:
			return errPhase
		case RoundEnd:
			if g.round >= g.rounds {
				g.enterFinal(now)
			} else {
				g.startRound()
			}
		}
	case "back_to_lobby":
		if g.phase != Final {
			return errPhase
		}
		g.reset()
	case "leave":
		g.remove(p)
		if g.phase != Lobby {
			g.reset()
		}
	default:
		return errAction
	}
	return nil
}

func (g *Game) resolve(p *Player, res string, now time.Time) error {
	switch g.phase {
	case RoundEnd:
		return nil
	case Playing:
	default:
		return errPhase
	}
	if p.resolved != "" {
		return nil
	}
	p.resolved = res
	if res == gotIt {
		remaining := max(g.deadline.Sub(now), 0)
		p.gain = 1 + min(2, int(3*remaining/(RoundSeconds*time.Second)))
	}
	for _, q := range g.players {
		if q.resolved == "" {
			return nil
		}
	}
	g.endRound()
	return nil
}

func (g *Game) startRound() {
	g.round++
	g.phase = Drawing
	g.deadline = time.Time{}
	var picked []*Character
	clash := func(c *Character, partners bool) bool {
		for _, q := range picked {
			if c == q || partners && (c.Partner == q.ID || q.Partner == c.ID) {
				return true
			}
		}
		return false
	}
	for _, p := range g.players {
		// pass 0: unused and no partner pair; 1: refill the bag; 2: allow a partner pair (tiny all-partnered pools)
		var free []*Character
		for pass := 0; len(free) == 0 && pass < 3; pass++ {
			if pass == 1 {
				g.used = map[string]bool{}
				for _, q := range picked {
					g.used[q.ID] = true
				}
			}
			for i := range g.chars {
				if c := &g.chars[i]; !g.used[c.ID] && !clash(c, pass < 2) {
					free = append(free, c)
				}
			}
		}
		p.secret = free[g.rng.IntN(len(free))]
		picked = append(picked, p.secret)
		g.used[p.secret.ID] = true
		g.drawn = append(g.drawn, p.secret.ID)
		p.opaque = g.hexToken()
		p.drawn, p.resolved, p.gain = false, "", 0
	}
}

func (g *Game) endRound() {
	g.phase = RoundEnd
	g.deadline = time.Time{}
	for _, p := range g.players {
		p.Score += p.gain
		p.opaque = ""
	}
}

func (g *Game) enterFinal(now time.Time) {
	g.phase = Final
	e := &HistoryEntry{Finished: now.UTC().Format(time.RFC3339), Rounds: g.rounds, Chars: g.drawn}
	for _, p := range g.players {
		e.Players = append(e.Players, HistoryPlayer{p.Name, p.Score})
	}
	g.finished = e
}

// TakeFinished hands over the game result once, after entering Final.
func (g *Game) TakeFinished() *HistoryEntry {
	e := g.finished
	g.finished = nil
	return e
}

func (g *Game) remove(p *Player) {
	for i, q := range g.players {
		if q == p {
			g.players = append(g.players[:i], g.players[i+1:]...)
			return
		}
	}
}

func (g *Game) reset() {
	g.phase = Lobby
	g.round = 0
	g.rounds = min(g.rounds, g.maxRounds())
	g.drawn = nil
	g.deadline = time.Time{}
	g.finished = nil
	for _, p := range g.players {
		*p = Player{Token: p.Token, Name: p.Name, Connected: p.Connected}
	}
}

// Media maps an opaque token to a file name under content/media. Valid only for the holder's live round.
func (g *Game) Media(opaque, which string) (string, bool) {
	if g.phase != Drawing && g.phase != Playing {
		return "", false
	}
	for _, p := range g.players {
		if p.opaque == "" || p.opaque != opaque || !p.drawn {
			continue
		}
		f := p.secret.Clip
		if which == "poster" {
			f = p.secret.Poster
		}
		return f, f != ""
	}
	return "", false
}
