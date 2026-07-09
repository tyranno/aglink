//go:build windows

package main

import (
	"testing"
	"time"
)

// TestMatchWindowTitleNumeric guards the fix that makes numeric window titles
// addressable by name: a window literally titled "2024" must resolve by title
// rather than being shadowed by decimal-HWND parsing.
func TestMatchWindowTitleNumeric(t *testing.T) {
	wins := []win{
		{Title: "2024", HWND: 0xAAAA},
		{Title: "Notepad", HWND: 0xBBBB},
	}
	if h, ok := matchWindowTitle(wins, "2024"); !ok || h != 0xAAAA {
		t.Fatalf("matchWindowTitle(\"2024\") = %#x, %v; want 0xAAAA, true", h, ok)
	}
}

// TestMatchWindowTitleExactBeatsPartial confirms an exact title wins over a
// substring match, regardless of enumeration order.
func TestMatchWindowTitleExactBeatsPartial(t *testing.T) {
	wins := []win{
		{Title: "Settings - Advanced", HWND: 0x1111}, // partial match for "settings"
		{Title: "Settings", HWND: 0x2222},            // exact match
	}
	if h, ok := matchWindowTitle(wins, "settings"); !ok || h != 0x2222 {
		t.Fatalf("matchWindowTitle(\"settings\") = %#x, %v; want 0x2222 (exact), true", h, ok)
	}
}

func TestMatchWindowTitleNoMatch(t *testing.T) {
	wins := []win{{Title: "Notepad", HWND: 0x1}}
	if h, ok := matchWindowTitle(wins, "chrome"); ok {
		t.Fatalf("matchWindowTitle(no match) = %#x, %v; want 0, false", h, ok)
	}
}

func TestParseHexHWND(t *testing.T) {
	if h, ok := parseHexHWND("0x1234"); !ok || h != 0x1234 {
		t.Fatalf("parseHexHWND(0x1234) = %#x, %v; want 0x1234, true", h, ok)
	}
	if _, ok := parseHexHWND("1234"); ok {
		t.Fatalf("parseHexHWND(\"1234\") should not parse a non-0x string as hex")
	}
	if _, ok := parseHexHWND("0xZZ"); ok {
		t.Fatalf("parseHexHWND(\"0xZZ\") should fail on invalid hex")
	}
}

func TestParseDecimalHWND(t *testing.T) {
	if h, ok := parseDecimalHWND("4660"); !ok || h != 4660 {
		t.Fatalf("parseDecimalHWND(4660) = %#x, %v; want 4660, true", h, ok)
	}
	if _, ok := parseDecimalHWND("notanumber"); ok {
		t.Fatalf("parseDecimalHWND(\"notanumber\") should fail")
	}
}

// TestWaitForWindowFindsItAfterPolling simulates a window that doesn't exist
// yet and appears after a couple of polls — waitForWindow must keep retrying
// (not just check once) and return as soon as findTopWindowProbe succeeds.
func TestWaitForWindowFindsItAfterPolling(t *testing.T) {
	origProbe := findTopWindowProbe
	origPoll := waitForWindowPollEvery
	t.Cleanup(func() {
		findTopWindowProbe = origProbe
		waitForWindowPollEvery = origPoll
	})
	waitForWindowPollEvery = 10 * time.Millisecond

	calls := 0
	findTopWindowProbe = func(titleOrHwnd string) (uintptr, bool) {
		calls++
		if calls < 3 {
			return 0, false
		}
		return 0xCAFE, true
	}

	start := time.Now()
	got, err := waitForWindow("Notepad", 2000)
	if err != nil {
		t.Fatalf("waitForWindow returned error: %v", err)
	}
	if calls < 3 {
		t.Errorf("expected at least 3 probe calls before success, got %d", calls)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waitForWindow took %v — should have returned as soon as the probe succeeded", elapsed)
	}
	if got == "" {
		t.Error("expected a non-empty result line")
	}
}

// TestWindowStateFlag guards the state-name-to-SW_* mapping window_state
// relies on, including its aliases and the rejection of "close" (deliberately
// unsupported — see setWindowState's doc comment).
func TestWindowStateFlag(t *testing.T) {
	cases := []struct {
		in   string
		want int32
	}{
		{"minimize", swMinimize},
		{"Minimized", swMinimize},
		{"min", swMinimize},
		{"maximize", swMaximize},
		{"MAX", swMaximize},
		{"restore", swRestore},
		{"normal", swRestore},
	}
	for _, c := range cases {
		got, err := windowStateFlag(c.in)
		if err != nil {
			t.Errorf("windowStateFlag(%q) returned error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("windowStateFlag(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	if _, err := windowStateFlag("close"); err == nil {
		t.Error("windowStateFlag(\"close\") should be rejected — close is deliberately unsupported")
	}
	if _, err := windowStateFlag("bogus"); err == nil {
		t.Error("windowStateFlag(\"bogus\") should return an error")
	}
}

// TestWaitForWindowTimesOut verifies the bound: if the window never appears,
// waitForWindow gives up after timeoutMs instead of blocking forever.
func TestWaitForWindowTimesOut(t *testing.T) {
	origProbe := findTopWindowProbe
	origPoll := waitForWindowPollEvery
	t.Cleanup(func() {
		findTopWindowProbe = origProbe
		waitForWindowPollEvery = origPoll
	})
	waitForWindowPollEvery = 10 * time.Millisecond
	findTopWindowProbe = func(titleOrHwnd string) (uintptr, bool) { return 0, false }

	start := time.Now()
	_, err := waitForWindow("NeverAppears", 100)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error when the window never appears")
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("returned after %v, expected to wait out the 100ms timeout", elapsed)
	}
	if elapsed > 100*time.Millisecond+500*time.Millisecond {
		t.Errorf("returned after %v — the timeout did not bound the wait", elapsed)
	}
}
