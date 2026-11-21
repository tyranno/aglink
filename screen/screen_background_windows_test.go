//go:build windows

package main

import (
	"strings"
	"testing"
	"time"
)

func stubFindTopWindow(t *testing.T, fn func(string) (uintptr, bool)) {
	t.Helper()
	orig := findTopWindowProbe
	findTopWindowProbe = fn
	t.Cleanup(func() { findTopWindowProbe = orig })
}

func TestResolveTargetWindow(t *testing.T) {
	var asked []string
	stubFindTopWindow(t, func(s string) (uintptr, bool) {
		asked = append(asked, s)
		if s == "Notepad" {
			return 0x1234, true
		}
		return 0, false
	})

	if h, err := resolveTargetWindow(""); err != nil || h != 0 {
		t.Fatalf(`resolveTargetWindow("") = %x, %v; want 0, nil (foreground)`, h, err)
	}
	if h, err := resolveTargetWindow("   "); err != nil || h != 0 {
		t.Fatalf("blank window should mean foreground, got %x, %v", h, err)
	}
	if len(asked) != 0 {
		t.Fatalf("empty window must not search windows, searched %v", asked)
	}
	if h, err := resolveTargetWindow("  Notepad "); err != nil || h != 0x1234 {
		t.Fatalf("resolveTargetWindow(Notepad) = %x, %v; want 0x1234", h, err)
	}
	if asked[0] != "Notepad" {
		t.Errorf("window should be trimmed before matching, got %q", asked[0])
	}
	if _, err := resolveTargetWindow("Missing"); err == nil || !strings.Contains(err.Error(), "Missing") {
		t.Fatalf("missing window should error naming it, got %v", err)
	}
}

func TestBackgroundSuffix(t *testing.T) {
	if s := backgroundSuffix(0); s != "" {
		t.Errorf("foreground op should add nothing, got %q", s)
	}
}

func TestBeginUIAOpBackgroundTakesOnlyLease(t *testing.T) {
	orig := beginBackgroundOp
	t.Cleanup(func() { beginBackgroundOp = orig })
	calls := 0
	beginBackgroundOp = func() error { calls++; return nil }
	before := syntheticInputCount.Load()
	if err := beginUIAOp(0x1234); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("background op should take the lease once, got %d", calls)
	}
	if syntheticInputCount.Load() != before {
		t.Error("background op must not go through the synthetic-input notice gate")
	}
}

// wait_for_control with a window: the window is re-resolved every poll and its
// handle reaches the UIA probe.
func TestUiaWaitForControlInPassesWindow(t *testing.T) {
	origProbe := uiaControlExistsProbe
	origPoll := uiaWaitForControlPollEvery
	t.Cleanup(func() {
		uiaControlExistsProbe = origProbe
		uiaWaitForControlPollEvery = origPoll
	})
	uiaWaitForControlPollEvery = 5 * time.Millisecond

	polls := 0
	stubFindTopWindow(t, func(s string) (uintptr, bool) {
		polls++
		if s != "Dialog" {
			t.Errorf("window = %q, want Dialog", s)
		}
		return 0xBEEF, polls >= 2 // window appears on the second poll
	})
	var gotHwnd uintptr
	uiaControlExistsProbe = func(hwnd uintptr, name string) (bool, error) {
		gotHwnd = hwnd
		return name == "OK", nil
	}
	if _, err := uiaWaitForControlIn("Dialog", "OK", 1000); err != nil {
		t.Fatal(err)
	}
	if gotHwnd != 0xBEEF {
		t.Errorf("probe got hwnd %x, want 0xBEEF", gotHwnd)
	}

	uiaControlExistsProbe = func(hwnd uintptr, name string) (bool, error) { return true, nil }
	stubFindTopWindow(t, func(string) (uintptr, bool) { return 0, false })
	_, err := uiaWaitForControlIn("Gone", "OK", 30)
	if err == nil || !strings.Contains(err.Error(), "no window matching") {
		t.Fatalf("missing window should time out naming the window, got %v", err)
	}
}

func TestUiaWaitForControlForegroundUnchanged(t *testing.T) {
	origProbe := uiaControlExistsProbe
	t.Cleanup(func() { uiaControlExistsProbe = origProbe })
	stubFindTopWindow(t, func(string) (uintptr, bool) {
		t.Error("no window given: must not search windows")
		return 0, false
	})
	uiaControlExistsProbe = func(hwnd uintptr, name string) (bool, error) {
		if hwnd != 0 {
			t.Errorf("foreground search should pass hwnd 0, got %x", hwnd)
		}
		return true, nil
	}
	if _, err := uiaWaitForControl("OK", 100); err != nil {
		t.Fatal(err)
	}
}

func TestChooseClickPath(t *testing.T) {
	invokable := func() bool { return true }
	notInvokable := func() bool { return false }
	cases := []struct {
		name string
		bg   bool
		t    clickTarget
		uia  func() bool
		want clickPath
	}{
		{"opt-out", false, clickTarget{Class: "Button", Enabled: true}, invokable, clickPathFocus},
		{"push button", true, clickTarget{Class: "Button", Style: 0x0, Enabled: true}, nil, clickPathBMClick},
		{"checkbox", true, clickTarget{Class: "Button", Style: 0x3, Enabled: true}, nil, clickPathBMClick},
		{"radio", true, clickTarget{Class: "Button", Style: 0x9, Enabled: true}, nil, clickPathBMClick},
		{"winforms button", true, clickTarget{Class: "WindowsForms10.BUTTON.app.0.141b42a_r9_ad1", Enabled: true}, nil, clickPathBMClick},
		{"group box", true, clickTarget{Class: "Button", Style: 0x50000007, Enabled: true}, invokable, clickPathFocus},
		{"disabled", true, clickTarget{Class: "Button", Enabled: false}, invokable, clickPathFocus},
		{"invokable other", true, clickTarget{Class: "SysLink", Enabled: true}, invokable, clickPathUIAInvoke},
		{"plain static", true, clickTarget{Class: "Static", Enabled: true}, notInvokable, clickPathFocus},
	}
	for _, c := range cases {
		got, reason := chooseClickPath(c.bg, c.t, c.uia)
		if got != c.want {
			t.Errorf("%s: path = %v, want %v", c.name, got, c.want)
		}
		if got == clickPathFocus && reason == "" {
			t.Errorf("%s: a fallback must say why", c.name)
		}
		if got != clickPathFocus && reason != "" {
			t.Errorf("%s: background path should have no reason, got %q", c.name, reason)
		}
	}
}

func TestChooseClickPathSkipsUIAForButtons(t *testing.T) {
	chooseClickPath(true, clickTarget{Class: "Button", Enabled: true}, func() bool {
		t.Error("a button must not need a UIA lookup")
		return false
	})
}

func TestMatchControl(t *testing.T) {
	ctrls := []control{
		{Class: "Button", Text: "&Save"},
		{Class: "Static", Text: "Save location"},
		{Class: "Button", Text: "Cancel"},
	}
	c, n, idx, ok := matchControl(ctrls, "save", 1)
	if !ok || n != 2 || idx != 1 || c.Text != "Save location" {
		t.Fatalf("matchControl nth=1 = %+v %d %d %v", c, n, idx, ok)
	}
	if c, _, idx, _ := matchControl(ctrls, "save", 9); idx != 0 || c.Text != "&Save" {
		t.Errorf("out-of-range nth should pick the first, got %q idx %d", c.Text, idx)
	}
	if _, _, _, ok := matchControl(ctrls, "ok", 0); ok {
		t.Error("no match should report false")
	}
}

func TestRunSequenceWindowPlumbing(t *testing.T) {
	stubFindTopWindow(t, func(string) (uintptr, bool) { return 0, false })
	for _, js := range []string{
		`[{"action":"invoke","window":"Nowhere","name":"OK"}]`,
		`[{"action":"set_value","window":"Nowhere","name":"Field","text":"x"}]`,
	} {
		res, err := runSequence(js, 0)
		if err == nil || !strings.Contains(err.Error(), `no window matching "Nowhere"`) {
			t.Fatalf("%s: want a missing-window error before any UIA call, got %v", js, err)
		}
		if len(res) != 1 || res[0].OK {
			t.Errorf("%s: want one failed step, got %+v", js, res)
		}
	}
}

func TestRunSequenceNoteStep(t *testing.T) {
	res, err := runSequence(`[{"action":"note","text":"password skipped"}]`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].OK || !strings.Contains(res[0].Detail, "password skipped") {
		t.Errorf("note step = %+v", res[0])
	}
}
