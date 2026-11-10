package main

import "testing"

func TestParseStats(t *testing.T) {
	got, err := ParseStats(
		"0.52 0.40 0.31 1/123 4567\n",
		"MemTotal:         415600 kB\nMemFree:           12000 kB\nMemAvailable:     201234 kB\nBuffers: 5 kB\n",
		"48312\n",
		"3600.55 7000.10\n")
	want := Stats{Load1: 0.52, MemTotalKB: 415600, MemAvailKB: 201234, TempC: 48.312, UptimeS: 3600.55}
	if err != nil || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
}

func TestParseStatsBad(t *testing.T) {
	if _, err := ParseStats("", "", "", ""); err == nil {
		t.Fatal("empty input parsed")
	}
	if _, err := ParseStats("0.1", "MemTotal: 5 kB\n", "1000", "1"); err == nil {
		t.Fatal("meminfo without MemAvailable parsed")
	}
}

func TestCPUPercent(t *testing.T) {
	// user nice system idle iowait irq softirq steal guest guest_nice
	s1 := "cpu  100 0 100 700 100 0 0 0 5 0\ncpu0 1 2 3 4 5 6 7 8 9 10\n"
	s2 := "cpu  200 0 150 750 100 0 0 0 5 0\n"
	var c cpuSampler
	if p, err := c.percent(s1); err != nil || p != 20 { // since boot: 200 busy of 1000
		t.Fatalf("first: %v %v", p, err)
	}
	if p, err := c.percent(s2); err != nil || p != 75 { // 150 busy of 200
		t.Fatalf("second: %v %v", p, err)
	}
	if p, _ := c.percent(s2); p != 0 {
		t.Fatalf("no time passed: %v", p)
	}
	if _, err := c.percent("intr 5\n"); err == nil {
		t.Fatal("no cpu line parsed")
	}
}
