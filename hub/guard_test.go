package main

import (
	"reflect"
	"testing"
)

func TestAssess(t *testing.T) {
	const mb = 1 << 20
	ex := func(id string, run bool) Usage { return Usage{id, Exclusive, run} }
	bg := func(id string, run bool) Usage { return Usage{id, Background, run} }
	cases := []struct {
		name string
		in   []Usage
		mem  uint64
		want []Warning
	}{
		{"nothing running", []Usage{ex("camera", false), ex("game", false)}, 500 * mb, []Warning{}},
		{"one exclusive", []Usage{ex("camera", true), ex("game", false), bg("offload", true)}, 500 * mb, []Warning{}},
		{"two exclusive", []Usage{ex("camera", true), ex("game", true)}, 500 * mb,
			[]Warning{{"many-exclusive", "2 heavy services are running at once: camera, game"}}},
		{"three background is fine", []Usage{bg("a", true), bg("b", true), bg("c", true), bg("d", false)}, 500 * mb, []Warning{}},
		{"four background", []Usage{bg("a", true), bg("b", true), bg("c", true), bg("d", true)}, 500 * mb,
			[]Warning{{"many-background", "4 background services are running (more than 3): a, b, c, d"}}},
		{"low memory plus two exclusive", []Usage{ex("camera", true), ex("game", true)}, 119 * mb,
			[]Warning{{"many-exclusive", "2 heavy services are running at once: camera, game"}, {"low-memory", "Only 119 MB of memory is free"}}},
		{"exactly 120 MB is fine", nil, 120 * mb, []Warning{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Assess(c.in, c.mem); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v\nwant %+v", got, c.want)
			}
		})
	}
}
