//go:build windows

package main

import (
	"os"
	"sync/atomic"
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

// TestEnsureControlNoticeWaitsForShown is the regression test for the race where
// synthetic input could begin before the "control in progress" toast was actually
// on screen. It substitutes a fake overlay runner that delays before signaling
// "shown", and asserts ensureControlNotice does not return (i.e. does not let
// input proceed) until that signal arrives.
func TestEnsureControlNoticeWaitsForShown(t *testing.T) {
	t.Setenv("AGLINK_NOTICE_LEAD_MS", "0") // isolate the shown-wait from the lead sleep

	origRunner := noticeShow
	origWaitMax := noticeShownWaitMax
	t.Cleanup(func() {
		noticeShow = origRunner
		noticeShownWaitMax = origWaitMax
		noticeShowing.Store(false)
		lastSyntheticInput.Store(0)
	})
	noticeShownWaitMax = 2 * time.Second

	const runnerDelay = 120 * time.Millisecond
	var shownAt atomic.Int64
	noticeShow = func() {
		time.Sleep(runnerDelay) // simulate the overlay taking time to appear
		shownAt.Store(time.Now().UnixNano())
		signalNoticeShown()
	}

	lastSyntheticInput.Store(0) // fresh session so the notice is due
	noticeShowing.Store(false)

	start := time.Now()
	ensureControlNotice()
	elapsed := time.Since(start)
	returnedAt := time.Now().UnixNano()

	if shownAt.Load() == 0 {
		t.Fatal("overlay runner never signaled shown")
	}
	if elapsed < runnerDelay {
		t.Errorf("ensureControlNotice returned after %v; expected to block until the toast was shown (>= %v) — input would start before the toast", elapsed, runnerDelay)
	}
	if returnedAt < shownAt.Load() {
		t.Error("ensureControlNotice returned before the shown signal — the race is not fixed")
	}
}

// TestEnsureControlNoticeShownTimeout verifies the safety bound: if the overlay
// never signals it was shown (e.g. a stalled UI thread), ensureControlNotice
// still proceeds after noticeShownWaitMax instead of blocking input forever.
func TestEnsureControlNoticeShownTimeout(t *testing.T) {
	t.Setenv("AGLINK_NOTICE_LEAD_MS", "0")

	origRunner := noticeShow
	origWaitMax := noticeShownWaitMax
	blocked := make(chan struct{})
	t.Cleanup(func() {
		close(blocked) // release the stalled runner goroutine
		noticeShow = origRunner
		noticeShownWaitMax = origWaitMax
		noticeShowing.Store(false)
		lastSyntheticInput.Store(0)
	})
	noticeShownWaitMax = 150 * time.Millisecond
	noticeShow = func() { <-blocked } // never signals shown

	lastSyntheticInput.Store(0)
	noticeShowing.Store(false)

	start := time.Now()
	ensureControlNotice()
	elapsed := time.Since(start)

	if elapsed < noticeShownWaitMax {
		t.Errorf("returned after %v, expected to wait for the safety timeout ~%v", elapsed, noticeShownWaitMax)
	}
	if elapsed > noticeShownWaitMax+time.Second {
		t.Errorf("returned after %v — safety timeout did not bound the wait", elapsed)
	}
}

// TestManualRealNoticePaintsBeforeInput exercises the REAL Win32 overlay (not the
// fake runner): it shows an actual toast and asserts the shown-signal fires from a
// real WM_PAINT, which is the only part the fake-runner tests cannot cover. It also
// drives one real, harmless synthetic input (a cursor move to the current spot)
// AFTER the toast is confirmed on screen, to walk the true end-to-end path. Gated
// behind AGLINK_MANUAL_UI=1 so normal `go test` stays deterministic/headless.
// Run with:  AGLINK_MANUAL_UI=1 go test -run TestManualRealNotice -v -count=3
func TestManualRealNoticePaintsBeforeInput(t *testing.T) {
	if os.Getenv("AGLINK_MANUAL_UI") == "" {
		t.Skip("set AGLINK_MANUAL_UI=1 to run the real on-screen notice test")
	}
	t.Setenv("AGLINK_NOTICE_LEAD_MS", "0")
	noticeShowing.Store(false)
	lastSyntheticInput.Store(0)

	t0 := time.Now()
	ch := showControlNotice() // REAL runNoticeWindow — an actual toast appears bottom-right
	if ch == nil {
		t.Fatal("expected a fresh notice to be shown")
	}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("real notice window never signaled first paint within 2s")
	}
	paintLatency := time.Since(t0)
	shownAt := time.Now()
	t.Logf("real toast first-painted %v after showControlNotice()", paintLatency)

	// The channel is closed => the toast is on screen. This is exactly the gate
	// ensureControlNotice now waits on before any synthetic input. Fire one real
	// cursor move (harmless) to walk the real SendInput path afterwards.
	x, y := cursorPos()
	if err := mouseMove(x, y); err != nil {
		t.Fatalf("real mouseMove failed: %v", err)
	}
	inputAt := time.Now()
	if inputAt.Before(shownAt) {
		t.Fatal("synthetic input ran before the toast was shown")
	}
	t.Logf("synthetic input ran %v after the toast was shown (correct order)", inputAt.Sub(shownAt))

	// Let the toast finish its fade so successive -count runs each show cleanly
	// and a human watching sees the full envelope.
	for i := 0; i < 100 && noticeShowing.Load(); i++ {
		time.Sleep(50 * time.Millisecond)
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
