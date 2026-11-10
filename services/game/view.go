package main

import "time"

type Pair[T any] struct {
	You   T `json:"you"`
	Other T `json:"other"`
}

type View struct {
	Phase     Phase         `json:"phase"`
	ServerNow int64         `json:"serverNow"`
	You       YouView       `json:"you"`
	Other     *OtherView    `json:"other"`
	Rounds    int           `json:"rounds"`
	Round     int           `json:"round"`
	CanStart  bool          `json:"canStart"`
	Drawn     Pair[bool]    `json:"drawn"`
	Resolved  Pair[*string] `json:"resolved"`
	Deadline  int64         `json:"deadline,omitempty"`
	Secret    *SecretView   `json:"secret,omitempty"`
	Reveal    *RevealView   `json:"reveal,omitempty"`
	Gain      *Pair[int]    `json:"gain,omitempty"`
	Final     *FinalView    `json:"final,omitempty"`
}

type YouView struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
}

type OtherView struct {
	Name      string `json:"name"`
	Score     int    `json:"score"`
	Connected bool   `json:"connected"`
}

type CharView struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type SecretView struct {
	CharView
	Clip   string `json:"clip,omitempty"`
	Poster string `json:"poster,omitempty"`
}

type RevealView struct {
	YourGuess  CharView `json:"yourGuess"`
	TheirGuess CharView `json:"theirGuess"`
}

type FinalView struct {
	Winner string `json:"winner"`
	Tie    bool   `json:"tie"`
}

func resolvedView(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (g *Game) View(token string, now time.Time) *View {
	p := g.player(token)
	if p == nil {
		return nil
	}
	o := g.other(p)
	v := &View{
		Phase:     g.phase,
		ServerNow: now.UnixMilli(),
		You:       YouView{p.Name, p.Score},
		Rounds:    g.rounds,
		Round:     g.round,
		CanStart:  g.phase == Lobby && len(g.players) == MaxPlayers,
	}
	if o == nil {
		return v
	}
	v.Other = &OtherView{o.Name, o.Score, o.Connected}
	if g.phase == Lobby {
		return v
	}
	v.Drawn = Pair[bool]{p.drawn, o.drawn}
	v.Resolved = Pair[*string]{resolvedView(p.resolved), resolvedView(o.resolved)}
	switch g.phase {
	case Drawing, Playing:
		if g.phase == Playing {
			v.Deadline = g.deadline.UnixMilli()
		}
		if p.drawn {
			v.Secret = &SecretView{CharView{p.secret.Name, p.secret.Kind}, "", ""}
			if p.secret.Clip != "" {
				v.Secret.Clip = "/m/" + p.opaque + "/clip"
			}
			if p.secret.Poster != "" {
				v.Secret.Poster = "/m/" + p.opaque + "/poster"
			}
		}
	case RoundEnd, Final:
		v.Reveal = &RevealView{
			YourGuess:  CharView{o.secret.Name, o.secret.Kind},
			TheirGuess: CharView{p.secret.Name, p.secret.Kind},
		}
		v.Gain = &Pair[int]{p.gain, o.gain}
		if g.phase == Final {
			v.Final = &FinalView{Tie: p.Score == o.Score}
			switch {
			case p.Score > o.Score:
				v.Final.Winner = p.Name
			case o.Score > p.Score:
				v.Final.Winner = o.Name
			}
		}
	}
	return v
}
