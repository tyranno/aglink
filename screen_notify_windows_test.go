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

func TestNoticeLeadMS(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int
	}{
		{"default (empty)", "", noticeDefaultLeadMS},
		{"explicit", "1500", 1500},
		{"zero disables", "0", 0},
		{"negative clamps to 0", "-200", 0},
		{"over max clamps", "999999", noticeMaxLeadMS},
		{"invalid falls back to default", "abc", noticeDefaultLeadMS},
	}
	for _, c := range cases {
		t.Setenv("AGLINK_NOTICE_LEAD_MS", c.env)
		if got := noticeLeadMS(); got != c.want {
			t.Errorf("%s: noticeLeadMS() with env %q = %d, want %d", c.name, c.env, got, c.want)
		}
	}
}

func TestNoticeDurationMS(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int
	}{
		{"default (empty)", "", noticeDefaultMS},
		{"explicit", "4500", 4500},
		{"below min clamps", "500", noticeMinMS},
		{"over max clamps", "999999", noticeMaxMS},
		{"invalid falls back to default", "xyz", noticeDefaultMS},
	}
	for _, c := range cases {
		t.Setenv("AGLINK_NOTICE_DURATION_MS", c.env)
		if got := noticeDurationMS(); got != c.want {
			t.Errorf("%s: noticeDurationMS() with env %q = %d, want %d", c.name, c.env, got, c.want)
		}
	}
}
