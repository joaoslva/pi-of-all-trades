package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func fixtureText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "nmcli", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParsers(t *testing.T) {
	if got := parseWifiConnections(fixtureText(t, "connections.txt")); !reflect.DeepEqual(got, []string{"camera-drive-wifi", "camera-drive-ap"}) {
		t.Fatalf("connections %v", got)
	}
	if ssid, mode := parseWifiProps(fixtureText(t, "props-client.txt")); ssid != "HomeNet" || mode != "infrastructure" {
		t.Fatalf("%q %q", ssid, mode)
	}
	if ssid, mode := parseWifiProps(fixtureText(t, "props-ap.txt")); ssid != "camera-drive" || mode != "ap" {
		t.Fatalf("%q %q", ssid, mode)
	}
	if got := parseActive(fixtureText(t, "active-client.txt"), "wlan0"); got != "camera-drive-wifi" {
		t.Fatalf("active %q", got)
	}
	if got := parseActive(fixtureText(t, "active-hotspot.txt"), "wlan0"); got != "camera-drive-ap" {
		t.Fatalf("active %q", got)
	}
	if got := parseActive("lo:lo\n", "wlan0"); got != "" {
		t.Fatalf("active %q", got)
	}
	if got := parseScan(fixtureText(t, "scan-real.txt")); !reflect.DeepEqual(got, []Network{{"HomeNet", 89, "WPA1 WPA2", true}}) {
		t.Fatalf("scan %v", got)
	}
	want := []Network{{"HomeNet", 89, "WPA1 WPA2", true}, {"Cafe: Free", 71, "WPA2", false}, {"OpenNet", 55, "", false}}
	if got := parseScan(fixtureText(t, "scan-mixed.txt")); !reflect.DeepEqual(got, want) {
		t.Fatalf("scan %v", got)
	}
	if got := parseIP(fixtureText(t, "ip.txt")); got != "192.0.2.10" {
		t.Fatalf("ip %q", got)
	}
	if got := parseIP("fe80::1/64|192.0.2.9/24"); got != "fe80::1" || parseIP("") != "" {
		t.Fatalf("ip %q", got)
	}
}

func TestValidation(t *testing.T) {
	for _, c := range []struct {
		ssid, pw string
		ok       bool
	}{
		{"a", "", true},
		{strings.Repeat("a", 32), "12345678", true},
		{strings.Repeat("a", 33), "", false},
		{"", "", false},
		{"x\ny", "", false},
		{"ok", "1234567", false},
		{"ok", strings.Repeat("p", 63), true},
		{"ok", strings.Repeat("p", 64), false},
	} {
		err := errors.Join(validateSSID(c.ssid), validatePassword(c.pw))
		if (err == nil) != c.ok {
			t.Errorf("ssid %d bytes pw %d chars: err %v", len(c.ssid), len(c.pw), err)
		}
	}
}

// fakeNM is a tiny NetworkManager: profiles, one active, and a log of every call.
type fakeNM struct {
	conns  []SavedNet // Name, SSID, Hotspot
	active string
	calls  []string
	failUp map[string]bool // connection up fails for these profiles (and leaves nothing active)
}

func (f *fakeNM) Run(args ...string) (string, error) {
	j := strings.Join(args, " ")
	f.calls = append(f.calls, j)
	switch {
	case strings.HasPrefix(j, "-t -f NAME,UUID,TYPE,DEVICE connection show"):
		var b strings.Builder
		for _, c := range f.conns {
			b.WriteString(c.Name + ":uuid:802-11-wireless:\n")
		}
		return b.String(), nil
	case j == "-t -f NAME,DEVICE connection show --active":
		if f.active == "" {
			return "lo:lo\n", nil
		}
		return f.active + ":wlan0\nlo:lo\n", nil
	case strings.HasPrefix(j, "-t -f 802-11-wireless.ssid,802-11-wireless.mode connection show id "):
		name := strings.TrimPrefix(j, "-t -f 802-11-wireless.ssid,802-11-wireless.mode connection show id ")
		for _, c := range f.conns {
			if c.Name == name {
				mode := "infrastructure"
				if c.Hotspot {
					mode = "ap"
				}
				return "802-11-wireless.ssid:" + c.SSID + "\n802-11-wireless.mode:" + mode + "\n", nil
			}
		}
		return "", errors.New("no such connection")
	case strings.HasPrefix(j, "-w 20 connection up id "):
		name := strings.TrimPrefix(j, "-w 20 connection up id ")
		if f.failUp[name] {
			f.active = ""
			return "", errors.New("nmcli: Connection activation failed")
		}
		f.active = name
		return "", nil
	case strings.HasPrefix(j, "connection add "):
		f.conns = append(f.conns, SavedNet{Name: args[6], SSID: args[len(args)-1]})
		return "", nil
	case strings.HasPrefix(j, "connection delete id "):
		return "", nil
	case strings.Contains(j, "device wifi list"):
		return "*:80:WPA2:Other\n", nil
	case strings.Contains(j, "IP4.ADDRESS"):
		return "192.0.2.20/24\n", nil
	}
	return "", errors.New("unexpected nmcli " + j)
}

type wifiRig struct {
	w     *Wifi
	nm    *fakeNM
	sd    *fakeSD
	clock time.Time
	armed []string
}

func newWifiRig(t *testing.T, active string) *wifiRig {
	t.Helper()
	r := &wifiRig{nm: &fakeNM{active: active, conns: []SavedNet{
		{Name: "home", SSID: "HomeNet"}, {Name: "cafe", SSID: "Cafe"}, {Name: hotspotCon, SSID: "camera-drive", Hotspot: true}}},
		sd: &fakeSD{up: map[string]bool{fallbackTimer: true}}, clock: time.Unix(5_000, 0)}
	r.w = NewWifi(r.sd, 60, t.TempDir())
	r.w.nm, r.w.hostname = r.nm, "pi.local"
	r.w.now = func() time.Time { return r.clock }
	r.w.flagPath = filepath.Join(t.TempDir(), "run", "wifi-switching")
	r.w.holdPath = filepath.Join(filepath.Dir(r.w.flagPath), "hotspot-hold")
	r.w.armRevert = func(sec int, prev string) error {
		r.armed = append(r.armed, prev)
		r.sd.up[revertUnit+".timer"] = true
		return nil
	}
	return r
}

// fire plays what the transient unit does: bring prev back, remove the flag.
func (r *wifiRig) fire(prev string) {
	r.sd.up[revertUnit+".timer"] = false
	r.nm.active = prev
	os.Remove(r.w.flagPath)
}

func (r *wifiRig) flag() bool { _, err := os.Stat(r.w.flagPath); return err == nil }
func (r *wifiRig) hold() bool { _, err := os.Stat(r.w.holdPath); return err == nil }
func (r *wifiRig) setHold(t *testing.T) {
	t.Helper()
	if err := r.w.setHold(); err != nil {
		t.Fatal(err)
	}
}
func (r *wifiRig) last() string {
	b, _ := os.ReadFile(r.w.statePath)
	return string(b)
}

func TestStatusModes(t *testing.T) {
	st, err := newWifiRig(t, "home").w.Status()
	if err != nil || st.Mode != "client" || st.SSID != "HomeNet" || st.IP != "192.0.2.20" || st.Fallback != "active" || len(st.Saved) != 3 {
		t.Fatalf("%+v %v", st, err)
	}
	if st, _ = newWifiRig(t, hotspotCon).w.Status(); st.Mode != ModeHotspotFallback || st.SSID != "camera-drive" || st.IP != "" {
		t.Fatalf("%+v", st)
	}
	if st, _ = newWifiRig(t, "").w.Status(); st.Mode != ModeNone {
		t.Fatalf("%+v", st)
	}
	r := newWifiRig(t, hotspotCon)
	r.setHold(t)
	if st, _ = r.w.Status(); st.Mode != ModeHotspotDemand {
		t.Fatalf("%+v", st)
	}
}

func TestDeriveMode(t *testing.T) {
	for _, c := range []struct {
		active        string
		hotspot, hold bool
		want          WifiMode
	}{
		{"home", false, false, ModeClient},
		{"home", false, true, ModeClient}, // a leftover hold means nothing on a client profile
		{hotspotCon, true, true, ModeHotspotDemand},
		{hotspotCon, true, false, ModeHotspotFallback},
		{"", false, true, ModeNone}, // the hold never makes "nothing active" look like a hotspot
		{"", false, false, ModeNone},
	} {
		if got := deriveMode(c.active, c.hotspot, c.hold); got != c.want {
			t.Errorf("%+v: got %s", c, got)
		}
	}
}

func TestScanRefusedInHotspotMode(t *testing.T) {
	r := newWifiRig(t, hotspotCon)
	_, err := r.w.Scan()
	if err == nil || !strings.Contains(err.Error(), "enter the SSID by hand") {
		t.Fatalf("err %v", err)
	}
	for _, c := range r.nm.calls {
		if strings.Contains(c, "wifi list") {
			t.Fatalf("scanned in hotspot mode: %v", r.nm.calls)
		}
	}
	r = newWifiRig(t, "home")
	if nets, err := r.w.Scan(); err != nil || len(nets) != 1 {
		t.Fatalf("%v %v", nets, err)
	}
}

func TestForgetRefusals(t *testing.T) {
	r := newWifiRig(t, "home")
	for _, name := range []string{"home", hotspotCon, "nope"} {
		if err := r.w.Forget(name); err == nil {
			t.Errorf("forgot %q", name)
		}
	}
	if err := r.w.Forget("cafe"); err != nil {
		t.Fatal(err)
	}
	r = newWifiRig(t, hotspotCon) // even while hosting, the hotspot profile stays
	if err := r.w.Forget(hotspotCon); err == nil {
		t.Fatal("forgot the hotspot")
	}
	for _, c := range r.nm.calls {
		if strings.Contains(c, "delete") {
			t.Fatalf("delete issued: %v", r.nm.calls)
		}
	}
}

func TestAdd(t *testing.T) {
	r := newWifiRig(t, "home")
	if err := r.w.Add("cafe", "12345678"); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := r.w.Add("New", "short"); err == nil {
		t.Fatal("short password accepted")
	}
	if err := r.w.Add("New", "12345678"); err != nil {
		t.Fatal(err)
	}
	want := "connection add type wifi con-name New ifname wlan0 ssid New wifi-sec.key-mgmt wpa-psk wifi-sec.psk 12345678"
	if got := r.nm.calls[len(r.nm.calls)-1]; got != want {
		t.Fatalf("got %q", got)
	}
}

func TestConnectKeepCancelsRevert(t *testing.T) {
	r := newWifiRig(t, "home")
	res, err := r.w.Connect("cafe")
	if err != nil || res.IP != "192.0.2.20" || res.Hostname != "pi.local" || res.Deadline != 5_060 {
		t.Fatalf("%+v %v", res, err)
	}
	if !reflect.DeepEqual(r.armed, []string{"home"}) || !r.flag() || r.nm.active != "cafe" {
		t.Fatalf("armed %v flag %v active %s", r.armed, r.flag(), r.nm.active)
	}
	if b, _ := os.ReadFile(r.w.flagPath); string(b) != "5000\n" {
		t.Fatalf("flag %q", b)
	}
	if err := r.w.Keep(); err != nil {
		t.Fatal(err)
	}
	if r.sd.up[revertUnit+".timer"] || r.flag() {
		t.Fatalf("timer still armed or flag left")
	}
	if got := r.sd.calls; !reflect.DeepEqual(got, []string{"stop:" + revertUnit + ".timer"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestConnectFailureRecoversAtOnce(t *testing.T) {
	r := newWifiRig(t, "home")
	r.nm.failUp = map[string]bool{"cafe": true} // wrong password
	_, err := r.w.Connect("cafe")
	if err == nil || !strings.Contains(err.Error(), `back on "home"`) {
		t.Fatalf("err %v", err)
	}
	if r.nm.active != "home" || r.flag() || r.sd.up[revertUnit+".timer"] || r.hold() {
		t.Fatalf("active %s flag %v timer %v hold %v: recovery must be immediate and clean", r.nm.active, r.flag(), r.sd.up[revertUnit+".timer"], r.hold())
	}
	if err := r.w.Keep(); err == nil {
		t.Fatal("Keep after the recovery must say there is nothing to keep")
	}
	// A second switch while one waits is refused.
	r = newWifiRig(t, "home")
	r.w.Connect("cafe")
	if _, err := r.w.Connect("home"); err == nil {
		t.Fatal("second switch accepted while waiting for Keep")
	}
}

func TestConnectFailureLastResortHotspotWithoutHold(t *testing.T) {
	r := newWifiRig(t, "home")
	r.nm.failUp = map[string]bool{"cafe": true, "home": true}
	_, err := r.w.Connect("cafe")
	if err == nil || !strings.Contains(err.Error(), "raised its hotspot") {
		t.Fatalf("err %v", err)
	}
	if r.nm.active != hotspotCon || r.hold() {
		t.Fatalf("active %s hold %v: the last resort must not set the hold", r.nm.active, r.hold())
	}
	if !r.sd.up[revertUnit+".timer"] || !r.flag() {
		t.Fatal("the armed revert and flag stay as the safety net")
	}
	r = newWifiRig(t, "home")
	r.nm.failUp = map[string]bool{"cafe": true, "home": true, hotspotCon: true}
	if _, err := r.w.Connect("cafe"); err == nil || !strings.Contains(err.Error(), "no network") || !r.sd.up[revertUnit+".timer"] {
		t.Fatalf("err %v", err)
	}
}

func TestHotspotOn(t *testing.T) {
	r := newWifiRig(t, "home")
	res, err := r.w.HotspotOn()
	if err != nil || res.SSID != "camera-drive" || res.URL != "http://10.42.0.1/admin" {
		t.Fatalf("%+v %v", res, err)
	}
	if r.nm.active != hotspotCon || !r.hold() || r.flag() || len(r.armed) != 0 {
		t.Fatalf("active %s hold %v flag %v armed %v", r.nm.active, r.hold(), r.flag(), r.armed)
	}
	if _, err := r.w.HotspotOn(); err == nil {
		t.Fatal("hotspot on twice")
	}
	// Failure: no hold, previous client back.
	r = newWifiRig(t, "home")
	r.nm.failUp = map[string]bool{hotspotCon: true}
	if _, err := r.w.HotspotOn(); err == nil || !strings.Contains(err.Error(), `back on "home"`) {
		t.Fatalf("err %v", err)
	}
	if r.hold() || r.flag() || r.nm.active != "home" {
		t.Fatalf("hold %v flag %v active %s", r.hold(), r.flag(), r.nm.active)
	}
	// The fallback already raised it: only the hold is added, nothing is re-activated.
	r = newWifiRig(t, hotspotCon)
	if _, err := r.w.HotspotOn(); err != nil || !r.hold() {
		t.Fatalf("%v hold %v", err, r.hold())
	}
	for _, c := range r.nm.calls {
		if strings.Contains(c, "connection up") {
			t.Fatalf("re-activated the running hotspot: %v", r.nm.calls)
		}
	}
}

func TestHotspotOffGoesToLastClient(t *testing.T) {
	r := newWifiRig(t, "home")
	r.w.Status() // home is the last working client network
	if _, err := r.w.HotspotOff(); err == nil {
		t.Fatal("hotspot off while on a client network")
	}
	if _, err := r.w.HotspotOn(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.w.HotspotOff(); err != nil {
		t.Fatal(err)
	}
	if r.nm.active != "home" || r.hold() || !reflect.DeepEqual(r.armed, []string{hotspotCon}) || !r.flag() {
		t.Fatalf("active %s hold %v armed %v flag %v", r.nm.active, r.hold(), r.armed, r.flag())
	}
	// Failure: back on the hotspot, hold restored, timer cancelled, flag gone.
	r = newWifiRig(t, "home")
	r.w.Status()
	r.w.HotspotOn()
	r.nm.failUp = map[string]bool{"home": true}
	if _, err := r.w.HotspotOff(); err == nil || !strings.Contains(err.Error(), `back on "camera-drive-ap"`) {
		t.Fatalf("err %v", err)
	}
	if r.nm.active != hotspotCon || !r.hold() || r.flag() || r.sd.up[revertUnit+".timer"] {
		t.Fatalf("active %s hold %v flag %v timer %v", r.nm.active, r.hold(), r.flag(), r.sd.up[revertUnit+".timer"])
	}
	// Nothing known to go to.
	r = newWifiRig(t, hotspotCon)
	if _, err := r.w.HotspotOff(); err == nil || !strings.Contains(err.Error(), "no working client network") {
		t.Fatalf("err %v", err)
	}
}

func TestRevertToHotspotRestoresHold(t *testing.T) {
	if got := strings.Join(revertArgs(30, hotspotCon), "|"); !strings.HasSuffix(got, "; touch /run/pihub/hotspot-hold|sh|camera-drive-ap") {
		t.Fatalf("got %s", got)
	}
}

func TestLastClientPersisted(t *testing.T) {
	r := newWifiRig(t, "home")
	st, _ := r.w.Status()
	if st.LastClient != "home" || !strings.Contains(r.last(), `"last_client":"home"`) {
		t.Fatalf("%q %s", st.LastClient, r.last())
	}
	// An unkept switch does not become the last working network; Keep does.
	r.w.Connect("cafe")
	if st, _ = r.w.Status(); st.LastClient != "home" {
		t.Fatalf("unkept switch counted: %q", st.LastClient)
	}
	if err := r.w.Keep(); err != nil {
		t.Fatal(err)
	}
	if st, _ = r.w.Status(); st.LastClient != "cafe" {
		t.Fatalf("kept switch not recorded: %q", st.LastClient)
	}
	// A new hub reads the file back; hotspot mode never overwrites it.
	w2 := NewWifi(r.sd, 60, filepath.Dir(r.w.statePath))
	w2.nm, w2.holdPath = r.nm, r.w.holdPath
	r.nm.active = hotspotCon
	if st, _ = w2.Status(); st.LastClient != "cafe" {
		t.Fatalf("not loaded: %q", st.LastClient)
	}
}

func TestSwitchRefusals(t *testing.T) {
	r := newWifiRig(t, hotspotCon)
	for _, name := range []string{hotspotCon, "nope"} {
		if _, err := r.w.Connect(name); err == nil {
			t.Errorf("switched to %q", name)
		}
	}
	// Switching from the hotspot to a client network is allowed and arms a revert to the hotspot.
	if _, err := r.w.Connect("cafe"); err != nil || !reflect.DeepEqual(r.armed, []string{hotspotCon}) {
		t.Fatalf("%v %v", err, r.armed)
	}
}

func TestConnectArmFailureAndNoPrevious(t *testing.T) {
	r := newWifiRig(t, "home")
	r.w.armRevert = func(int, string) error { return errors.New("denied") }
	if _, err := r.w.Connect("cafe"); err == nil || r.flag() || r.nm.active != "home" {
		t.Fatalf("err %v flag %v active %s: must not switch without a revert", err, r.flag(), r.nm.active)
	}
	r = newWifiRig(t, "") // nothing active: no revert to arm, flag gone when done
	if _, err := r.w.Connect("cafe"); err != nil || len(r.armed) != 0 || r.flag() {
		t.Fatalf("err %v armed %v flag %v", err, r.armed, r.flag())
	}
	r = newWifiRig(t, "home")
	for _, name := range []string{hotspotCon, "home", "nope"} {
		if _, err := r.w.Connect(name); err == nil {
			t.Errorf("connected to %q", name)
		}
	}
}

func TestRevertArgs(t *testing.T) {
	got := strings.Join(revertArgs(60, "my home"), "|")
	want := `--quiet|--collect|--unit=pihub-wifi-revert|--on-active=60|sh|-c|nmcli -w 30 connection up id "$1"; rm -f /run/pihub/wifi-switching|sh|my home`
	if got != want {
		t.Fatalf("got %s", got)
	}
}

func TestWifiNonceBoundToOpAndTarget(t *testing.T) {
	r := newWifiRig(t, "home")
	f := newFixture(t)
	f.h = newHandler(nil, f.a, nil, r.w)
	cookie := f.session(t)
	step := func(op, body string) (int, string) {
		rec := f.do("POST", "/admin/api/wifi/"+op, body, nil, cookie)
		return rec.Code, rec.Body.String()
	}
	nonce := func(op, body string) string {
		_, out := step(op, body)
		return strings.Split(out, `"`)[3]
	}
	n := nonce("connect", `{"name":"cafe"}`)
	if code, _ := step("forget", `{"name":"cafe","nonce":"`+n+`"}`); code != http.StatusBadRequest {
		t.Fatalf("connect nonce ran forget: %d", code)
	}
	n = nonce("connect", `{"name":"cafe"}`)
	if code, _ := step("connect", `{"name":"home","nonce":"`+n+`"}`); code != http.StatusBadRequest {
		t.Fatalf("nonce for cafe ran another target: %d", code)
	}
	if len(r.armed) != 0 {
		t.Fatal("something ran")
	}
	// Step one refuses what step two would refuse, before issuing a nonce.
	if code, _ := step("forget", `{"name":"home"}`); code != http.StatusBadRequest {
		t.Fatalf("forget of the active network got a nonce: %d", code)
	}
	if code, _ := step("add", `{"ssid":"x","password":"short"}`); code != http.StatusBadRequest {
		t.Fatalf("bad password got a nonce: %d", code)
	}
	n = nonce("hotspot-on", `{}`)
	if code, _ := step("hotspot-off", `{"nonce":"`+n+`"}`); code != http.StatusBadRequest {
		t.Fatalf("hotspot-on nonce ran hotspot-off: %d", code)
	}
	if code, _ := step("keep", `{"nonce":"`+n+`"}`); code != http.StatusBadRequest {
		t.Fatalf("hotspot-on nonce ran keep: %d", code)
	}
	if code, _ := step("hotspot-off", `{}`); code != http.StatusBadRequest {
		t.Fatalf("hotspot-off on a client network got a nonce: %d", code)
	}
	n = nonce("connect", `{"name":"cafe"}`)
	if code, body := step("connect", `{"name":"cafe","nonce":"`+n+`"}`); code != 200 || !strings.Contains(body, `"ip":"192.0.2.20"`) {
		t.Fatalf("connect: %d %s", code, body)
	}
	if code, _ := step("nope", `{}`); code != http.StatusNotFound {
		t.Fatalf("unknown op: %d", code)
	}
	if rec := f.do("POST", "/admin/api/wifi/connect", `{"name":"cafe"}`, nil, nil); rec.Code != 401 {
		t.Fatalf("no cookie: %d", rec.Code)
	}
	if rec := f.do("GET", "/admin/api/wifi", "", nil, cookie); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"fallback_timer":"active"`) {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do("GET", "/admin/api/wifi/scan", "", nil, nil); rec.Code != 401 {
		t.Fatalf("scan no cookie: %d", rec.Code)
	}
}

// No route reaches the fallback timer, and the hub only ever reads it.
func TestNoRouteTouchesFallback(t *testing.T) {
	main, _ := os.ReadFile("main.go")
	routes := regexp.MustCompile(`(?m)^\s*mux\.HandleFunc\(.*$`).FindAllString(string(main), -1)
	if len(routes) < 10 {
		t.Fatalf("route table not found: %v", routes)
	}
	for _, l := range routes {
		if strings.Contains(strings.ToLower(l), "fallback") {
			t.Errorf("route mentions the fallback: %s", l)
		}
	}
	src, _ := os.ReadFile("wifi.go")
	for _, l := range strings.Split(string(src), "\n") {
		if strings.Contains(l, "fallbackTimer") && !strings.Contains(l, "Show(") && !strings.Contains(l, "fallbackTimer ") {
			t.Errorf("wifi.go uses the fallback beyond reading it: %s", l)
		}
	}
	r := newWifiRig(t, "home")
	f := newFixture(t)
	f.h = newHandler(nil, f.a, nil, r.w)
	cookie := f.session(t)
	for _, p := range []string{"/admin/api/fallback", "/admin/api/wifi/fallback", "/admin/api/wifi/stop"} {
		if rec := f.do("POST", p, `{}`, nil, cookie); rec.Code != 404 && rec.Code != 405 {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	for _, c := range append(r.nm.calls, r.sd.calls...) {
		if strings.Contains(c, "fallback") {
			t.Errorf("call touched the fallback: %s", c)
		}
	}
}
