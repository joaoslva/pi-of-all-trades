package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// UnitInfo is what the hub needs to know about one unit.
type UnitInfo struct {
	Active      string // systemd ActiveState: active, activating, inactive, failed, ...
	MemoryBytes uint64
	MemoryOK    bool // false when cgroup memory accounting is off ("[not set]" or max uint64)
}

func (u UnitInfo) up() bool { return u.Active == "active" || u.Active == "activating" }

// Systemd is the only way the hub touches units, so tests can fake it.
type Systemd interface {
	Show(unit string) (UnitInfo, error)
	Start(unit string) error
	Stop(unit string) error
}

// isTemplate: "camera-offload@.service" stands for any instance of the template.
func isTemplate(unit string) bool { return strings.Contains(unit, "@.") }

// parseShow reads `systemctl show -p ActiveState,SubState,MemoryCurrent` output.
func parseShow(out string) UnitInfo {
	var u UnitInfo
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "ActiveState":
			u.Active = v
		case "MemoryCurrent":
			if n, err := strconv.ParseUint(v, 10, 64); err == nil && n != math.MaxUint64 {
				u.MemoryBytes, u.MemoryOK = n, true
			}
		}
	}
	return u
}

type systemctl struct{}

func (systemctl) Show(unit string) (UnitInfo, error) {
	if isTemplate(unit) {
		out, err := exec.Command("systemctl", "list-units", strings.Replace(unit, "@.", "@*.", 1),
			"--state=active,activating", "--no-legend").Output()
		if err != nil {
			return UnitInfo{}, err
		}
		if strings.TrimSpace(string(out)) == "" {
			return UnitInfo{Active: "inactive"}, nil
		}
		return UnitInfo{Active: "active"}, nil
	}
	out, err := exec.Command("systemctl", "show", "-p", "ActiveState,SubState,MemoryCurrent", unit).Output()
	if err != nil {
		return UnitInfo{}, err
	}
	return parseShow(string(out)), nil
}

func (systemctl) Start(unit string) error { return exec.Command("systemctl", "start", unit).Run() }
func (systemctl) Stop(unit string) error  { return exec.Command("systemctl", "stop", unit).Run() }

// Control switches Exclusive services. A service counts as running when any of its units is up.
type Control struct {
	sd        Systemd
	services  []Service
	statePath string
	mem       func() (uint64, error) // MemAvailable in bytes

	mu sync.Mutex
}

func NewControl(sd Systemd, services []Service, stateDir string) *Control {
	return &Control{sd: sd, services: services, statePath: filepath.Join(stateDir, "state.json"),
		mem: func() (uint64, error) {
			s, err := CollectStats()
			return uint64(s.MemAvailKB) << 10, err
		}}
}

type state struct {
	Active string `json:"active"` // "" means switched off on purpose
}

// readState: exists is false when no state file was ever written.
func (c *Control) readState() (st state, exists bool, err error) {
	b, err := os.ReadFile(c.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	return st, true, json.Unmarshal(b, &st)
}

func (c *Control) writeState(id string) error {
	b, _ := json.Marshal(state{id})
	tmp := c.statePath + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.statePath)
}

func (c *Control) exclusive(id string) (Service, error) {
	for _, s := range c.services {
		if s.ID == id && s.Class == Exclusive {
			return s, nil
		}
	}
	return Service{}, fmt.Errorf("%q is not an exclusive service", id)
}

func (c *Control) running(s Service) (bool, error) {
	for _, u := range s.Units {
		info, err := c.sd.Show(u)
		if err != nil {
			return false, err
		}
		if info.up() {
			return true, nil
		}
	}
	return false, nil
}

func (c *Control) stopUnits(s Service) error {
	for _, u := range s.Units {
		info, err := c.sd.Show(u)
		if err != nil {
			return err
		}
		if info.up() {
			if err := c.sd.Stop(u); err != nil {
				return err
			}
		}
	}
	return nil
}

// Switch makes id the only running Exclusive service. The state is written
// first, so a crash anywhere after it is repaired by Reconcile.
func (c *Control) Switch(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.switchLocked(id)
}

func (c *Control) switchLocked(id string) error {
	target, err := c.exclusive(id)
	if err != nil {
		return err
	}
	if err := c.writeState(id); err != nil {
		return err
	}
	for _, s := range c.services {
		if s.Class == Exclusive && s.ID != id {
			if err := c.stopUnits(s); err != nil {
				return err
			}
		}
	}
	up, err := c.running(target)
	if err != nil || up {
		return err
	}
	for _, u := range target.Units {
		if err := c.sd.Start(u); err != nil {
			return err
		}
	}
	return nil
}

// Stop switches one Exclusive service off and remembers that choice.
func (c *Control) Stop(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.exclusive(id)
	if err != nil {
		return err
	}
	if err := c.stopUnits(s); err != nil {
		return err
	}
	if st, _, _ := c.readState(); st.Active == id {
		return c.writeState("")
	}
	return nil
}

// Reconcile runs at startup. With a state file it converges on it; without
// one it adopts the single running Exclusive service, else leaves things alone.
func (c *Control) Reconcile() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, exists, err := c.readState()
	if err != nil {
		return err
	}
	if exists {
		if st.Active == "" {
			return nil
		}
		return c.switchLocked(st.Active)
	}
	var up []string
	for _, s := range c.services {
		if r, err := c.running(s); s.Class == Exclusive && err == nil && r {
			up = append(up, s.ID)
		}
	}
	if len(up) == 1 {
		return c.writeState(up[0])
	}
	return nil
}

type unitView struct {
	Unit  string `json:"unit"`
	State string `json:"state"`
}

type serviceView struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Class       Class      `json:"class"`
	Port        int        `json:"port"`
	Running     bool       `json:"running"`
	Selected    bool       `json:"selected"`     // the one the state file names
	MemoryBytes *uint64    `json:"memory_bytes"` // null when unknown: the page shows n/a
	Units       []unitView `json:"units"`
}

func (c *Control) view() (views []serviceView, warnings []Warning) {
	st, _, _ := c.readState()
	var usages []Usage
	for _, s := range c.services {
		v := serviceView{ID: s.ID, Name: s.Name, Class: s.Class, Port: s.Port, Selected: s.ID == st.Active, Units: []unitView{}}
		var mem uint64
		known := true
		for _, u := range s.Units {
			info, err := c.sd.Show(u)
			if err != nil {
				log.Printf("show %s: %v", u, err)
				v.Units = append(v.Units, unitView{u, "unknown"})
				continue
			}
			v.Units = append(v.Units, unitView{u, info.Active})
			if info.up() {
				v.Running = true
				mem += info.MemoryBytes
				known = known && info.MemoryOK
			}
		}
		if v.Running && known {
			v.MemoryBytes = &mem
		}
		views = append(views, v)
		usages = append(usages, Usage{s.ID, s.Class, v.Running})
	}
	avail, err := c.mem()
	if err != nil {
		avail = math.MaxUint64 // unknown memory is not a warning
	}
	return views, Assess(usages, avail)
}

func (c *Control) list(w http.ResponseWriter, r *http.Request) {
	views, warnings := c.view()
	json.NewEncoder(w).Encode(map[string]any{"services": views, "warnings": warnings})
}

// twoStep serves {"id"} -> nonce, then {"id","nonce"} -> run. The nonce is
// bound to verb:id, so a token for one switch cannot confirm another.
func (c *Control) twoStep(a *Auth, verb string, run func(id string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct{ ID, Nonce string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if _, err := c.exclusive(req.ID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		action := verb + ":" + req.ID
		if req.Nonce == "" {
			json.NewEncoder(w).Encode(map[string]string{"nonce": a.issueNonce(action)})
			return
		}
		if !a.spendNonce(req.Nonce, action) {
			http.Error(w, "nonce invalid or expired", http.StatusBadRequest)
			return
		}
		if err := run(req.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
