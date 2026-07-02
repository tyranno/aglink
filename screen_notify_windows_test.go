//go:build windows

package main

import (
	"testing"
	"time"
)

func TestNoticeDue(t *testing.T) {
	gap := int64(controlNoticeGap)
	now := int64(1_000_000_000_000)

	cases := []struct {
		name string
		prev int64
		now  int64
		want bool
	}{
		{"first ever (prev=0)", 0, now, true},
		{"just after previous input", now - int64(50*time.Millisecond), now, false},
		{"within gap", now - gap + 1, now, false},
		{"exactly at gap", now - gap, now, true},
		{"long idle", now - 10*gap, now, true},
	}
	for _, c := range cases {
		if got := noticeDue(c.prev, c.now); got != c.want {
			t.Errorf("%s: noticeDue(%d,%d) = %v, want %v", c.name, c.prev, c.now, got, c.want)
		}
	}
}
