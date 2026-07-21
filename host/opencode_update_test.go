package main

import "testing"

func TestSemverNewer(t *testing.T) {
	cases := []struct {
		installed, latest string
		want              bool
	}{
		{"1.18.4", "1.18.4", false},
		{"1.18.4", "1.18.5", true},
		{"1.18.4", "1.19.0", true},
		{"1.18.4", "2.0.0", true},
		{"1.18.5", "1.18.4", false},
		{"v1.18.4", "1.18.5", true},   // leading v tolerated
		{"1.18.4", "1.18.5-beta", true}, // prerelease suffix dropped → 1.18.5
		{"", "1.18.5", false},          // unknown installed → never nag
		{"1.18.4", "", false},          // unknown latest → never nag
		{"garbage", "1.0.0", false},    // unparseable → never nag
	}
	for _, c := range cases {
		if got := semverNewer(c.installed, c.latest); got != c.want {
			t.Errorf("semverNewer(%q,%q)=%v want %v", c.installed, c.latest, got, c.want)
		}
	}
}

func TestParseVersionLine(t *testing.T) {
	if got := parseVersionLine("1.18.4"); got != "1.18.4" {
		t.Errorf("got %q", got)
	}
	if got := parseVersionLine("opencode 1.18.4"); got != "1.18.4" {
		t.Errorf("got %q", got)
	}
	if got := parseVersionLine("v1.18.4"); got != "1.18.4" {
		t.Errorf("leading v not stripped: %q", got)
	}
}
