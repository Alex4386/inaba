package main

import (
	"fmt"
	"slices"
	"testing"
)

func TestParseProxies(t *testing.T) {
	got, err := parseProxies([]string{"10.0.0.0/8", "192.168.1.7", "::ffff:172.16.0.1", "fd00::1/64"})
	if err != nil || fmt.Sprint(got) != "[10.0.0.0/8 192.168.1.7/32 172.16.0.1/32 fd00::/64]" {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"proxy.example.com", "10.0.0.0/33", ""} {
		if _, err := parseProxies([]string{bad}); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestICEURLs(t *testing.T) {
	for _, c := range []struct {
		configured            []string
		publicURL, stunListen string
		want                  []string
	}{
		{[]string{"stun:a:1"}, "https://inaba.example.com", ":3478", []string{"stun:a:1"}},
		{[]string{"stun:a:1"}, "https://inaba.example.com", "", []string{"stun:a:1"}},
		{nil, "https://inaba.example.com", ":3479", []string{"stun:inaba.example.com:3479"}},
		{nil, "https://inaba.example.com", "", []string{publicSTUN}},
		{nil, "", ":3478", nil},
		{nil, "", "", []string{publicSTUN}},
	} {
		if got := iceURLs(c.configured, c.publicURL, c.stunListen); !slices.Equal(got, c.want) {
			t.Errorf("iceURLs(%v, %q, %q) = %v, want %v", c.configured, c.publicURL, c.stunListen, got, c.want)
		}
	}
}
