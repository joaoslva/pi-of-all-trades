package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
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
	CPUPercent float64 `json:"cpu_percent"`
}

// parseCPU returns idle and total jiffies from the aggregate "cpu" line of /proc/stat.
func parseCPU(stat string) (idle, total uint64, err error) {
	for _, line := range strings.Split(stat, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		for i, v := range f[1:] {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return 0, 0, err
			}
			if i == 8 { // guest time is already counted in user
				break
			}
			total += n
			if i == 3 || i == 4 { // idle, iowait
				idle += n
			}
		}
		return idle, total, nil
	}
	return 0, 0, fmt.Errorf("no cpu line in /proc/stat")
}

// cpuSampler reports busy percent since its previous call; the first call covers the time since boot.
type cpuSampler struct {
	mu          sync.Mutex
	idle, total uint64
}

func (c *cpuSampler) percent(stat string) (float64, error) {
	idle, total, err := parseCPU(stat)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	di, dt := idle-c.idle, total-c.total
	c.idle, c.total = idle, total
	if dt == 0 || di > dt {
		return 0, nil
	}
	return 100 * float64(dt-di) / float64(dt), nil
}

var cpu cpuSampler

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
	st, err := os.ReadFile("/proc/stat")
	if err != nil {
		return Stats{}, err
	}
	s, err := ParseStats(in[0], in[1], in[2], in[3])
	if err != nil {
		return s, err
	}
	if s.CPUPercent, err = cpu.percent(string(st)); err != nil {
		return s, err
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs("/", &fs); err != nil {
		return s, err
	}
	s.DiskTotal, s.DiskFree = fs.Blocks*uint64(fs.Bsize), fs.Bavail*uint64(fs.Bsize)
	return s, nil
}
