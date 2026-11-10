package main

import (
	"fmt"
	"strings"
)

const (
	maxBackground = 3
	lowMemory     = 120 << 20 // bytes
)

// Usage is one service's class and whether any of its units is running.
type Usage struct {
	ID      string
	Class   Class
	Running bool
}

// Warning is advice only: nothing in the hub refuses an action because of one.
type Warning struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// Assess is pure. memAvailable is in bytes.
func Assess(usages []Usage, memAvailable uint64) []Warning {
	var ex, bg []string
	for _, u := range usages {
		switch {
		case !u.Running:
		case u.Class == Exclusive:
			ex = append(ex, u.ID)
		default:
			bg = append(bg, u.ID)
		}
	}
	w := []Warning{}
	if len(ex) > 1 {
		w = append(w, Warning{"many-exclusive", fmt.Sprintf("%d heavy services are running at once: %s", len(ex), strings.Join(ex, ", "))})
	}
	if len(bg) > maxBackground {
		w = append(w, Warning{"many-background", fmt.Sprintf("%d background services are running (more than %d): %s", len(bg), maxBackground, strings.Join(bg, ", "))})
	}
	if memAvailable < lowMemory {
		w = append(w, Warning{"low-memory", fmt.Sprintf("Only %d MB of memory is free", memAvailable>>20)})
	}
	return w
}
