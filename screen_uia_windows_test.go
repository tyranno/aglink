//go:build windows

package main

import (
	"testing"
	"time"
)

// TestUiaWaitForControlFindsItAfterPolling mirrors
// TestWaitForWindowFindsItAfterPolling (screen_apps_windows_test.go) for the
// UIA-element case: the probe returns false a couple of times, then true —
// uiaWaitForControl must keep retrying and return as soon as it succeeds.
func TestUiaWaitForControlFindsItAfterPolling(t *testing.T) {
	origProbe := uiaControlExistsProbe
	origPoll := uiaWaitForControlPollEvery
	t.Cleanup(func() {
		uiaControlExistsProbe = origProbe
		uiaWaitForControlPollEvery = origPoll
	})
	uiaWaitForControlPollEvery = 10 * time.Millisecond

	calls := 0
	uiaControlExistsProbe = func(name string) (bool, error) {
		calls++
		return calls >= 3, nil
	}

	start := time.Now()
	got, err := uiaWaitForControl("Send", 2000)
	if err != nil {
		t.Fatalf("uiaWaitForControl returned error: %v", err)
	}
	if calls < 3 {
		t.Errorf("expected at least 3 probe calls before success, got %d", calls)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("uiaWaitForControl took %v — should have returned as soon as the probe succeeded", elapsed)
	}
	if got != `ok: found "Send"` {
		t.Errorf("got %q, want ok: found \"Send\"", got)
	}
}

// TestUiaWaitForControlTimesOut verifies the bound: if the element never
// appears, uiaWaitForControl gives up after timeoutMs instead of blocking
// forever.
func TestUiaWaitForControlTimesOut(t *testing.T) {
	origProbe := uiaControlExistsProbe
	origPoll := uiaWaitForControlPollEvery
	t.Cleanup(func() {
		uiaControlExistsProbe = origProbe
		uiaWaitForControlPollEvery = origPoll
	})
	uiaWaitForControlPollEvery = 10 * time.Millisecond
	uiaControlExistsProbe = func(name string) (bool, error) { return false, nil }

	start := time.Now()
	_, err := uiaWaitForControl("NeverAppears", 100)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error when the element never appears")
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("returned after %v, expected to wait out the 100ms timeout", elapsed)
	}
	if elapsed > 100*time.Millisecond+500*time.Millisecond {
		t.Errorf("returned after %v — the timeout did not bound the wait", elapsed)
	}
}
