package main

import (
	"errors"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

// fakeSD records Start/Stop calls; Show reads the up set.
type fakeSD struct {
	up      map[string]bool
	calls   []string
	failOn  string // "start:b1" fails once, then clears
	showErr error
}

func (f *fakeSD) Show(u string) (UnitInfo, error) {
	if f.showErr != nil {
		return UnitInfo{}, f.showErr
	}
	if f.up[u] {
		return UnitInfo{Active: "active", MemoryBytes: 1 << 20, MemoryOK: true}, nil
	}
	return UnitInfo{Active: "inactive"}, nil
}
func (f *fakeSD) do(verb, u string, up bool) error {
	f.calls = append(f.calls, verb+":"+u)
	if f.failOn == verb+":"+u {
		f.failOn = ""
		return errors.New("injected")
	}
	f.up[u] = up
	return nil
}
func (f *fakeSD) Start(u string) error { return f.do("start", u, true) }
func (f *fakeSD) Stop(u string) error  { return f.do("stop", u, false) }

func testServices() []Service {
	return []Service{
		{ID: "a", Name: "A", Class: Exclusive, Units: []string{"a1", "a2"}, Port: 9001},
		{ID: "b", Name: "B", Class: Exclusive, Units: []string{"b1", "b2"}, Port: 9002},
		{ID: "bg", Name: "BG", Class: Background, Units: []string{"bg@.service"}},
	}
}

func newControl(t *testing.T, up ...string) (*Control, *fakeSD) {
	t.Helper()
	sd := &fakeSD{up: map[string]bool{}}
	for _, u := range up {
		sd.up[u] = true
	}
	c := NewControl(sd, testServices(), t.TempDir())
	c.mem = func() (uint64, error) { return 500 << 20, nil }
	return c, sd
}

func stateOf(t *testing.T, c *Control) string {
	t.Helper()
	b, err := os.ReadFile(c.statePath)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestSwitchStopsOldBeforeStartingNew(t *testing.T) {
	c, sd := newControl(t, "a1", "a2")
	if err := c.Switch("b"); err != nil {
		t.Fatal(err)
	}
	want := []string{"stop:a1", "stop:a2", "start:b1", "start:b2"}
	if !reflect.DeepEqual(sd.calls, want) {
		t.Fatalf("calls %v, want %v", sd.calls, want)
	}
	if got := stateOf(t, c); got != `{"active":"b"}` {
		t.Fatalf("state %s", got)
	}
}

func TestSwitchTwiceMakesNoSecondCall(t *testing.T) {
	c, sd := newControl(t, "a1")
	c.Switch("b")
	n := len(sd.calls)
	if err := c.Switch("b"); err != nil || len(sd.calls) != n {
		t.Fatalf("err %v, calls %v", err, sd.calls[n:])
	}
}

func TestCrashAfterStopThenReconcile(t *testing.T) {
	c, sd := newControl(t, "a1", "a2")
	sd.failOn = "start:b1" // "crash": A is already stopped, B never came up
	if err := c.Switch("b"); err == nil {
		t.Fatal("want the injected error")
	}
	if sd.up["a1"] || sd.up["b1"] {
		t.Fatalf("expected nothing running, got %v", sd.up)
	}
	if err := c.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if !sd.up["b1"] || !sd.up["b2"] || sd.up["a1"] || sd.up["a2"] {
		t.Fatalf("did not converge on b: %v", sd.up)
	}
}

func TestReconcileRepairsBothRunning(t *testing.T) {
	c, sd := newControl(t, "a1", "b1")
	c.writeState("b")
	c.Reconcile()
	if sd.up["a1"] || !sd.up["b1"] {
		t.Fatalf("got %v", sd.up)
	}
}

func TestReconcileAdopts(t *testing.T) {
	c, _ := newControl(t, "a1")
	c.Reconcile()
	if got := stateOf(t, c); got != `{"active":"a"}` {
		t.Fatalf("state %s", got)
	}
	c, sd := newControl(t, "a1", "b1") // two running, no state: leave alone
	c.Reconcile()
	if _, err := os.Stat(c.statePath); err == nil || len(sd.calls) != 0 {
		t.Fatalf("should do nothing: err %v calls %v", err, sd.calls)
	}
}

func TestStopClearsStateAndStaysOffAfterReconcile(t *testing.T) {
	c, sd := newControl(t, "a1", "a2")
	c.Switch("a")
	if err := c.Stop("a"); err != nil {
		t.Fatal(err)
	}
	c.Reconcile()
	if sd.up["a1"] || sd.up["a2"] || stateOf(t, c) != `{"active":""}` {
		t.Fatalf("up %v state %s", sd.up, stateOf(t, c))
	}
}

func TestRejectsUnknownAndBackground(t *testing.T) {
	c, sd := newControl(t)
	for _, id := range []string{"nope", "bg", ""} {
		if err := c.Switch(id); err == nil {
			t.Errorf("Switch(%q) accepted", id)
		}
		if err := c.Stop(id); err == nil {
			t.Errorf("Stop(%q) accepted", id)
		}
	}
	if len(sd.calls) != 0 {
		t.Fatalf("calls %v", sd.calls)
	}
}

func TestParseShow(t *testing.T) {
	cases := map[string]UnitInfo{
		"ActiveState=active\nSubState=running\nMemoryCurrent=12345\n":     {"active", 12345, true},
		"ActiveState=active\nSubState=running\nMemoryCurrent=[not set]\n": {Active: "active"},
		"ActiveState=inactive\nMemoryCurrent=18446744073709551615\n":      {Active: "inactive"},
	}
	for in, want := range cases {
		if got := parseShow(in); got != want {
			t.Errorf("%q: got %+v want %+v", in, got, want)
		}
	}
}

func TestViewMemoryUnknownIsNull(t *testing.T) {
	c, _ := newControl(t, "a1")
	views, warn := c.view()
	if !views[0].Running || views[0].MemoryBytes == nil || *views[0].MemoryBytes != 1<<20 || len(warn) != 0 {
		t.Fatalf("%+v %v", views[0], warn)
	}
	if views[1].Running || views[1].MemoryBytes != nil {
		t.Fatalf("%+v", views[1])
	}
}

// HTTP: a nonce for switch:a must not run switch:b, and a Background id is refused.
func TestSwitchNonceBoundToAction(t *testing.T) {
	f := newFixture(t)
	c, sd := newControl(t)
	f.h = newHandler(testServices(), f.a, c)
	cookie := f.session(t)
	step := func(path, body string) (int, string) {
		rec := f.do("POST", path, body, nil, cookie)
		return rec.Code, rec.Body.String()
	}
	_, out := step("/admin/api/switch", `{"id":"a"}`)
	n := strings.Split(out, `"`)[3]
	if code, _ := step("/admin/api/switch", `{"id":"b","nonce":"`+n+`"}`); code != http.StatusBadRequest {
		t.Fatalf("nonce for a ran b: %d", code)
	}
	if len(sd.calls) != 0 {
		t.Fatalf("calls %v", sd.calls)
	}
	_, out = step("/admin/api/switch", `{"id":"a"}`)
	n = strings.Split(out, `"`)[3]
	if code, _ := step("/admin/api/stop", `{"id":"a","nonce":"`+n+`"}`); code != http.StatusBadRequest {
		t.Fatalf("switch nonce ran stop: %d", code)
	}
	_, out = step("/admin/api/switch", `{"id":"a"}`)
	n = strings.Split(out, `"`)[3]
	if code, _ := step("/admin/api/switch", `{"id":"a","nonce":"`+n+`"}`); code != http.StatusNoContent || !sd.up["a1"] {
		t.Fatalf("confirmed switch: %d %v", code, sd.up)
	}
	if code, _ := step("/admin/api/switch", `{"id":"bg"}`); code != http.StatusBadRequest {
		t.Fatalf("background accepted: %d", code)
	}
	if rec := f.do("GET", "/admin/api/services", "", nil, nil); rec.Code != 401 {
		t.Fatalf("services without cookie: %d", rec.Code)
	}
	if rec := f.do("GET", "/admin/api/services", "", nil, cookie); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"warnings":[]`) {
		t.Fatalf("services: %d %s", rec.Code, rec.Body)
	}
}

func TestLandingHidesBackgroundAndPortless(t *testing.T) {
	dir := writeDir(t, map[string]string{
		"a.json": `{"id":"a","name":"Alpha","class":"exclusive","units":["a"],"port":9001}`,
		"o.json": `{"id":"o","name":"Offl","class":"background","units":["o@.service"],"port":0}`,
		"p.json": `{"id":"p","name":"Pback","class":"background","units":["p"],"port":9005}`,
	})
	services, err := LoadServices(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.h = newHandler(services, f.a, nil)
	body := f.do("GET", "/", "", nil, nil).Body.String()
	if !strings.Contains(body, "Alpha") || strings.Contains(body, "Offl") || strings.Contains(body, "Pback") {
		t.Fatalf("landing:\n%s", body)
	}
}
