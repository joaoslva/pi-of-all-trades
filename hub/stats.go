package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

type Stats struct {
	Load1      float64 `json:"load1"`
	MemTotalKB int64   `json:"mem_total_kb"`
	MemAvailKB int64   `json:"mem_available_kb"`
	TempC      float64 `json:"temp_c"`
	DiskTotal  uint64  `json:"disk_total"`
	DiskFree   uint64  `json:"disk_free"`
	UptimeS    float64 `json:"uptime_s"`
}

// ParseStats is pure over the text of the /proc and /sys files.
func ParseStats(loadavg, meminfo, thermal, uptime string) (s Stats, err error) {
	f := strings.Fields(loadavg)
	if len(f) == 0 {
		return s, fmt.Errorf("empty loadavg")
	}
	if s.Load1, err = strconv.ParseFloat(f[0], 64); err != nil {
		return s, err
	}
	for _, line := range strings.Split(meminfo, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, _ := strconv.ParseInt(strings.Fields(v + " ")[0], 10, 64)
		switch k {
		case "MemTotal":
			s.MemTotalKB = n
		case "MemAvailable":
			s.MemAvailKB = n
		}
	}
	if s.MemTotalKB == 0 || s.MemAvailKB == 0 {
		return s, fmt.Errorf("meminfo lacks MemTotal/MemAvailable")
	}
	milli, err := strconv.ParseFloat(strings.TrimSpace(thermal), 64)
	if err != nil {
		return s, err
	}
	s.TempC = milli / 1000
	f = strings.Fields(uptime)
	if len(f) == 0 {
		return s, fmt.Errorf("empty uptime")
	}
	s.UptimeS, err = strconv.ParseFloat(f[0], 64)
	return s, err
}

func CollectStats() (Stats, error) {
	var in [4]string
	for i, p := range []string{"/proc/loadavg", "/proc/meminfo", "/sys/class/thermal/thermal_zone0/temp", "/proc/uptime"} {
		b, err := os.ReadFile(p)
		if err != nil {
			return Stats{}, err
		}
		in[i] = string(b)
	}
	s, err := ParseStats(in[0], in[1], in[2], in[3])
	if err != nil {
		return s, err
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs("/", &fs); err != nil {
		return s, err
	}
	s.DiskTotal, s.DiskFree = fs.Blocks*uint64(fs.Bsize), fs.Bavail*uint64(fs.Bsize)
	return s, nil
}
