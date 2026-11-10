package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type addr struct {
	Iface string `json:"iface"`
	IP    string `json:"ip"`
}

type camInfo struct {
	State      any    `json:"state"`
	LastResult any    `json:"last_result"`
	Updated    any    `json:"updated"`
	Free       uint64 `json:"free"`
	Total      uint64 `json:"total"`
}

type sysInfo struct {
	Hostname string  `json:"hostname"`
	Addrs    []addr  `json:"addresses"`
	OS       string  `json:"os"`
	Kernel   string  `json:"kernel"`
	Arch     string  `json:"arch"`
	Revision string  `json:"revision"`
	Modified bool    `json:"modified"`
	Boot     string  `json:"boot_time"`
	Now      string  `json:"time"`
	Zone     string  `json:"zone"`
	Camera   camInfo `json:"camera"`
}

// parseOSName returns PRETTY_NAME from os-release content, or "".
func parseOSName(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// parseCamStatus reads status.json; missing or garbage fields stay nil (null), never an error.
func parseCamStatus(b []byte) (c camInfo) {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return
	}
	c.State, c.LastResult, c.Updated = m["state"], m["last_result"], m["updated"]
	return
}

func parseBoot(uptime string, now time.Time) (time.Time, bool) {
	f := strings.Fields(uptime)
	if len(f) == 0 {
		return time.Time{}, false
	}
	up, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return time.Time{}, false
	}
	return now.Add(-time.Duration(up * float64(time.Second))).Truncate(time.Second), true
}

func buildRevision() (rev string, modified bool) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value[:min(8, len(s.Value))]
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return
}

func (a *Auth) system(w http.ResponseWriter, r *http.Request) {
	rd := func(p string) string { b, _ := os.ReadFile(p); return strings.TrimSpace(string(b)) }
	now := a.now()
	info := sysInfo{OS: parseOSName(rd("/etc/os-release")), Kernel: rd("/proc/sys/kernel/osrelease"),
		Arch: runtime.GOARCH, Now: now.Format(time.RFC3339), Addrs: []addr{}}
	info.Zone, _ = now.Zone()
	info.Hostname, _ = os.Hostname()
	info.Revision, info.Modified = buildRevision()
	if t, ok := parseBoot(rd("/proc/uptime"), now); ok {
		info.Boot = t.Format(time.RFC3339)
	}
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		as, _ := ifc.Addrs()
		for _, ad := range as {
			if ipn, ok := ad.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				info.Addrs = append(info.Addrs, addr{ifc.Name, ipn.IP.String()})
			}
		}
	}
	b, _ := os.ReadFile(a.cameraDir + "/status.json")
	info.Camera = parseCamStatus(b)
	var fs syscall.Statfs_t
	if syscall.Statfs(a.cameraDir, &fs) == nil {
		info.Camera.Total, info.Camera.Free = fs.Blocks*uint64(fs.Bsize), fs.Bavail*uint64(fs.Bsize)
	}
	json.NewEncoder(w).Encode(info)
}
