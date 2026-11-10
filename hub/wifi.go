package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	hotspotCon    = "camera-drive-ap"
	fallbackTimer = "camera-net-fallback.timer" // read-only: the hub only ever Shows it
	switchFlag    = "/run/pihub/wifi-switching"
	revertUnit    = "pihub-wifi-revert"
	wifiIface     = "wlan0"
	// The fallback script ignores the flag after this long (see camera-net-fallback),
	// so the revert has to fire before it.
	flagStale = 90 * time.Second
)

// NMCLI runs nmcli; tests fake it. Output is stdout only, errors carry stderr.
type NMCLI interface {
	Run(args ...string) (string, error)
}

type nmcli struct{}

func (nmcli) Run(args ...string) (string, error) {
	out, err := exec.Command("nmcli", args...).Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		err = fmt.Errorf("nmcli: %s", strings.TrimSpace(string(ee.Stderr)))
	}
	return string(out), err
}

// userErr is a refusal the operator can fix; everything else is a 500.
type userErr string

func (e userErr) Error() string { return string(e) }

// splitTerse splits one `nmcli -t` line on unescaped colons.
func splitTerse(line string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
		case c == ':':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(out, cur.String())
}

func lines(out string) []string {
	var ls []string
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			ls = append(ls, l)
		}
	}
	return ls
}

// parseWifiConnections reads `-t -f NAME,UUID,TYPE,DEVICE connection show`, keeping Wi-Fi profiles.
func parseWifiConnections(out string) (names []string) {
	for _, l := range lines(out) {
		if f := splitTerse(l); len(f) >= 3 && f[2] == "802-11-wireless" {
			names = append(names, f[0])
		}
	}
	return names
}

// parseWifiProps reads `-t -f 802-11-wireless.ssid,802-11-wireless.mode connection show id X`.
func parseWifiProps(out string) (ssid, mode string) {
	for _, l := range lines(out) {
		f := splitTerse(l)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "802-11-wireless.ssid":
			ssid = f[1]
		case "802-11-wireless.mode":
			mode = f[1]
		}
	}
	return ssid, mode
}

// parseActive reads `-t -f NAME,DEVICE connection show --active` and returns the profile on iface.
func parseActive(out, iface string) string {
	for _, l := range lines(out) {
		if f := splitTerse(l); len(f) >= 2 && f[1] == iface {
			return f[0]
		}
	}
	return ""
}

// Network is one entry of a scan.
type Network struct {
	SSID     string `json:"ssid"`
	Signal   int    `json:"signal"`
	Security string `json:"security"` // "" means open
	InUse    bool   `json:"in_use"`
}

// parseScan reads `-t -f IN-USE,SIGNAL,SECURITY,SSID device wifi list`: hidden networks
// (empty SSID) are dropped, duplicates keep the strongest, result sorted by signal.
func parseScan(out string) []Network {
	best := map[string]Network{}
	for _, l := range lines(out) {
		f := splitTerse(l)
		if len(f) < 4 || f[3] == "" {
			continue
		}
		sig, _ := strconv.Atoi(f[1])
		n := Network{SSID: f[3], Signal: sig, Security: strings.TrimSpace(f[2]), InUse: f[0] == "*"}
		if old, ok := best[n.SSID]; !ok || n.Signal > old.Signal {
			n.InUse = n.InUse || old.InUse
			best[n.SSID] = n
		}
	}
	nets := make([]Network, 0, len(best))
	for _, n := range best {
		nets = append(nets, n)
	}
	sort.Slice(nets, func(i, j int) bool {
		if nets[i].Signal != nets[j].Signal {
			return nets[i].Signal > nets[j].Signal
		}
		return nets[i].SSID < nets[j].SSID
	})
	return nets
}

// parseIP reads `-t -g IP4.ADDRESS device show` and returns the first address without its prefix.
func parseIP(out string) string {
	for _, l := range strings.FieldsFunc(out, func(r rune) bool { return r == '\n' || r == '|' }) {
		if l = strings.TrimSpace(l); l != "" {
			addr, _, _ := strings.Cut(l, "/")
			return addr
		}
	}
	return ""
}

// validateSSID and validatePassword: the limits of WPA-PSK. An empty password means an open network.
func validateSSID(s string) error {
	if len(s) < 1 || len(s) > 32 {
		return userErr("SSID must be 1 to 32 bytes")
	}
	if !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\r\n") {
		return userErr("SSID has characters that cannot be used")
	}
	return nil
}

func validatePassword(p string) error {
	if p == "" {
		return nil
	}
	if n := utf8.RuneCountInString(p); n < 8 || n > 63 {
		return userErr("password must be 8 to 63 characters, or empty for an open network")
	}
	if strings.ContainsAny(p, "\x00\r\n") {
		return userErr("password has characters that cannot be used")
	}
	return nil
}

// revertArgs is the systemd-run command line that brings prev back and then removes the
// flag, whatever happens to the nmcli call (no flag left means the fallback may act again).
func revertArgs(seconds int, prev string) []string {
	return []string{"--quiet", "--collect", "--unit=" + revertUnit, "--on-active=" + strconv.Itoa(seconds),
		"sh", "-c", `nmcli -w 30 connection up id "$1"; rm -f ` + switchFlag, "sh", prev}
}

// Wifi manages saved Wi-Fi profiles. It never touches the fallback: the only thing it
// does with camera-net-fallback.timer is read whether it is active.
type Wifi struct {
	nm        NMCLI
	sd        Systemd
	armRevert func(seconds int, prev string) error
	now       func() time.Time
	flagPath  string
	revertSec int
	hostname  string

	mu sync.Mutex
}

func NewWifi(sd Systemd, revertSec int) *Wifi {
	host, _ := os.Hostname()
	return &Wifi{nm: nmcli{}, sd: sd, now: time.Now, flagPath: switchFlag, revertSec: revertSec, hostname: host + ".local",
		armRevert: func(seconds int, prev string) error {
			out, err := exec.Command("systemd-run", revertArgs(seconds, prev)...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("arming the revert: %v: %s", err, strings.TrimSpace(string(out)))
			}
			return nil
		}}
}

type SavedNet struct {
	Name    string `json:"name"`
	SSID    string `json:"ssid"`
	Active  bool   `json:"active"`
	Hotspot bool   `json:"hotspot"`
}

type WifiStatus struct {
	Mode        string     `json:"mode"` // "client", "hotspot" or "none"
	SSID        string     `json:"ssid"`
	Active      string     `json:"active"`
	IP          string     `json:"ip"`
	Hostname    string     `json:"hostname"`
	Saved       []SavedNet `json:"saved"`
	Fallback    string     `json:"fallback_timer"`
	RevertArmed bool       `json:"revert_armed"`
	RevertSec   int        `json:"revert_seconds"`
}

func (w *Wifi) Status() (WifiStatus, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.statusLocked()
}

func (w *Wifi) statusLocked() (st WifiStatus, err error) {
	out, err := w.nm.Run("-t", "-f", "NAME,UUID,TYPE,DEVICE", "connection", "show")
	if err != nil {
		return st, err
	}
	act, err := w.nm.Run("-t", "-f", "NAME,DEVICE", "connection", "show", "--active")
	if err != nil {
		return st, err
	}
	st.Active = parseActive(act, wifiIface)
	st.Mode, st.Hostname, st.RevertSec, st.Saved = "none", w.hostname, w.revertSec, []SavedNet{}
	for _, name := range parseWifiConnections(out) {
		props, err := w.nm.Run("-t", "-f", "802-11-wireless.ssid,802-11-wireless.mode", "connection", "show", "id", name)
		if err != nil {
			return st, err
		}
		ssid, mode := parseWifiProps(props)
		s := SavedNet{Name: name, SSID: ssid, Active: name == st.Active, Hotspot: mode == "ap" || name == hotspotCon}
		if s.Active {
			st.SSID, st.Mode = ssid, "client"
			if s.Hotspot {
				st.Mode = "hotspot"
			}
		}
		st.Saved = append(st.Saved, s)
	}
	if st.Mode == "client" {
		ip, _ := w.nm.Run("-t", "-g", "IP4.ADDRESS", "device", "show", wifiIface)
		st.IP = parseIP(ip)
	}
	if info, err := w.sd.Show(fallbackTimer); err == nil {
		st.Fallback = info.Active
	}
	if info, err := w.sd.Show(revertUnit + ".timer"); err == nil {
		st.RevertArmed = info.up()
	}
	return st, nil
}

// Scan is refused in hotspot mode: on this single radio a scan drops the hotspot.
func (w *Wifi) Scan() ([]Network, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	st, err := w.statusLocked()
	if err != nil {
		return nil, err
	}
	if st.Mode == "hotspot" {
		return nil, userErr("scanning is off in hotspot mode, enter the SSID by hand")
	}
	out, err := w.nm.Run("-t", "-f", "IN-USE,SIGNAL,SECURITY,SSID", "device", "wifi", "list", "ifname", wifiIface, "--rescan", "yes")
	if err != nil {
		return nil, err
	}
	return parseScan(out), nil
}

func (w *Wifi) saved(st WifiStatus, name string) (SavedNet, error) {
	for _, s := range st.Saved {
		if s.Name == name {
			return s, nil
		}
	}
	return SavedNet{}, userErr(fmt.Sprintf("no saved Wi-Fi network named %q", name))
}

// Add saves a WPA-PSK (or open) profile named after the SSID. It does not connect.
func (w *Wifi) Add(ssid, password string) error {
	if err := errors.Join(validateSSID(ssid), validatePassword(password)); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	st, err := w.statusLocked()
	if err != nil {
		return err
	}
	if _, err := w.saved(st, ssid); err == nil {
		return userErr(fmt.Sprintf("%q is already saved; forget it first to change its password", ssid))
	}
	args := []string{"connection", "add", "type", "wifi", "con-name", ssid, "ifname", wifiIface, "ssid", ssid}
	if password != "" {
		args = append(args, "wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", password)
	}
	_, err = w.nm.Run(args...)
	return err
}

func (w *Wifi) checkForget(name string) (WifiStatus, error) {
	st, err := w.statusLocked()
	if err != nil {
		return st, err
	}
	s, err := w.saved(st, name)
	switch {
	case err != nil:
		return st, err
	case s.Hotspot:
		return st, userErr("the hotspot profile cannot be forgotten here")
	case s.Active:
		return st, userErr("this network is in use; connect to another one first")
	}
	return st, nil
}

func (w *Wifi) Forget(name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.checkForget(name); err != nil {
		return err
	}
	_, err := w.nm.Run("connection", "delete", "id", name)
	return err
}

type ConnectResult struct {
	Hostname  string `json:"hostname"`
	IP        string `json:"ip"`
	RevertSec int    `json:"revert_seconds"`
	Deadline  int64  `json:"deadline"` // unix seconds when the previous network comes back unless kept
}

func (w *Wifi) checkConnect(name string) (st WifiStatus, err error) {
	if st, err = w.statusLocked(); err != nil {
		return st, err
	}
	s, err := w.saved(st, name)
	switch {
	case err != nil:
		return st, err
	case s.Hotspot:
		return st, userErr("the hotspot is raised by the fallback, not from here")
	case s.Active:
		return st, userErr("already connected to this network")
	case st.RevertArmed:
		return st, userErr("a switch is waiting to be kept; keep it or wait for the revert")
	}
	return st, nil
}

// Connect switches networks with a safety net: flag first (the fallback stands down), then a
// transient timer that brings the previous profile back, then the switch. Keep cancels the timer.
func (w *Wifi) Connect(name string) (res ConnectResult, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	st, err := w.checkConnect(name)
	if err != nil {
		return res, err
	}
	if err := os.MkdirAll(filepath.Dir(w.flagPath), 0o755); err != nil {
		return res, err
	}
	if err := os.WriteFile(w.flagPath, []byte(strconv.FormatInt(w.now().Unix(), 10)+"\n"), 0o644); err != nil {
		return res, err
	}
	armed := false
	defer func() {
		if !armed { // nothing will remove the flag later, so do it now
			os.Remove(w.flagPath)
		}
	}()
	if st.Active != "" {
		if err := w.armRevert(w.revertSec, st.Active); err != nil {
			return res, err
		}
		armed = true
	}
	res = ConnectResult{Hostname: w.hostname, RevertSec: w.revertSec, Deadline: w.now().Add(time.Duration(w.revertSec) * time.Second).Unix()}
	// The revert stays armed on failure: it restores the previous network.
	if _, err := w.nm.Run("-w", "20", "connection", "up", "id", name); err != nil {
		return res, fmt.Errorf("%w (the previous network returns in about %d s)", err, w.revertSec)
	}
	ip, _ := w.nm.Run("-t", "-g", "IP4.ADDRESS", "device", "show", wifiIface)
	res.IP = parseIP(ip)
	return res, nil
}

// Keep cancels the revert and removes the flag; the flag goes even if the timer is already gone.
func (w *Wifi) Keep() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	info, err := w.sd.Show(revertUnit + ".timer")
	stopErr := error(nil)
	if err == nil && info.up() {
		stopErr = w.sd.Stop(revertUnit + ".timer")
	} else if err == nil {
		stopErr = userErr("nothing to keep: the revert already ran")
	} else {
		stopErr = err
	}
	return errors.Join(stopErr, removeIfExists(w.flagPath))
}

func removeIfExists(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// --- HTTP ---

func (w *Wifi) fail(rw http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	var ue userErr
	if errors.As(err, &ue) {
		code = http.StatusBadRequest
		if strings.HasPrefix(ue.Error(), "scanning is off") {
			code = http.StatusConflict
		}
	}
	http.Error(rw, err.Error(), code)
}

func (w *Wifi) status(rw http.ResponseWriter, r *http.Request) {
	st, err := w.Status()
	if err != nil {
		w.fail(rw, err)
		return
	}
	json.NewEncoder(rw).Encode(st)
}

func (w *Wifi) scan(rw http.ResponseWriter, r *http.Request) {
	nets, err := w.Scan()
	if err != nil {
		w.fail(rw, err)
		return
	}
	json.NewEncoder(rw).Encode(map[string]any{"networks": nets})
}

// op serves POST /admin/api/wifi/{op}. Step one (no nonce) checks the request and returns a
// nonce bound to "wifi-<op>:<target>"; step two runs it.
func (w *Wifi) op(a *Auth) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		op := r.PathValue("op")
		var req struct{ Name, SSID, Password, Nonce string }
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 4096)).Decode(&req); err != nil {
			http.Error(rw, "bad request", http.StatusBadRequest)
			return
		}
		var target string
		var check func() error
		var run func() (any, error)
		switch op {
		case "add":
			target = req.SSID
			check = func() error { return errors.Join(validateSSID(req.SSID), validatePassword(req.Password)) }
			run = func() (any, error) { return nil, w.Add(req.SSID, req.Password) }
		case "forget":
			target = req.Name
			check = func() error { w.mu.Lock(); defer w.mu.Unlock(); _, err := w.checkForget(req.Name); return err }
			run = func() (any, error) { return nil, w.Forget(req.Name) }
		case "connect":
			target = req.Name
			check = func() error { w.mu.Lock(); defer w.mu.Unlock(); _, err := w.checkConnect(req.Name); return err }
			run = func() (any, error) { return w.Connect(req.Name) }
		case "keep":
			check = func() error { return nil }
			run = func() (any, error) { return nil, w.Keep() }
		default:
			http.NotFound(rw, r)
			return
		}
		action := "wifi-" + op + ":" + target
		if req.Nonce == "" {
			if err := check(); err != nil {
				w.fail(rw, err)
				return
			}
			json.NewEncoder(rw).Encode(map[string]string{"nonce": a.issueNonce(action)})
			return
		}
		if !a.spendNonce(req.Nonce, action) {
			http.Error(rw, "nonce invalid or expired", http.StatusBadRequest)
			return
		}
		res, err := run()
		if err != nil {
			w.fail(rw, err)
			return
		}
		if res == nil {
			rw.WriteHeader(http.StatusNoContent)
			return
		}
		json.NewEncoder(rw).Encode(res)
	}
}
