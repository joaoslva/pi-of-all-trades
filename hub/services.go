package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Class is closed: Exclusive services run one at a time, Background ones may run alongside.
type Class string

const (
	Exclusive  Class = "exclusive"
	Background Class = "background"
)

type Service struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Class       Class    `json:"class"`
	Units       []string `json:"units"`
	Port        int      `json:"port"`
}

// LoadServices reads every *.json file in dir. It is the only parser.
func LoadServices(dir string) ([]Service, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []Service
	ids := map[string]string{}
	ports := map[int]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var s Service
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if err := s.validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if p, ok := ids[s.ID]; ok {
			return nil, fmt.Errorf("%s: duplicate id %q (also in %s)", f, s.ID, p)
		}
		if p, ok := ports[s.Port]; ok {
			return nil, fmt.Errorf("%s: port %d already used by %s", f, s.Port, p)
		}
		ids[s.ID], ports[s.Port] = f, f
		out = append(out, s)
	}
	return out, nil
}

func (s Service) validate() error {
	switch {
	case s.ID == "":
		return fmt.Errorf("missing id")
	case s.Name == "":
		return fmt.Errorf("missing name")
	case len(s.Units) == 0:
		return fmt.Errorf("missing units")
	case s.Port < 1 || s.Port > 65535:
		return fmt.Errorf("port %d out of range", s.Port)
	case s.Class != Exclusive && s.Class != Background:
		return fmt.Errorf("unknown class %q", s.Class)
	}
	for _, u := range s.Units {
		if u == "" {
			return fmt.Errorf("empty unit name")
		}
	}
	return nil
}
