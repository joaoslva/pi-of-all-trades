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
