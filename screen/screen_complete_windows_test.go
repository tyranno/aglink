//go:build windows

package main

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/mark3labs/mcp-go/mcp"
	"golang.org/x/sys/windows"
)

// TestManualRealCompleteNotice renders the REAL green completion overlay (the one
// part the fake-runner tests cannot cover — actual green pixels + text on screen).
// It calls the overlay directly, bypassing showControlComplete's away-skip gate, so
// it always shows. Gated behind AGLINK_MANUAL_UI=1 so normal `go test` stays
// headless. Run: AGLINK_MANUAL_UI=1 go test -run TestManualRealCompleteNotice -v
func TestManualRealCompleteNotice(t *testing.T) {
	if os.Getenv("AGLINK_MANUAL_UI") == "" {
		t.Skip("set AGLINK_MANUAL_UI=1 to run the real green completion notice test")
	}
	noticeShowing.Store(false)
	noticeCloseRequested.Store(false)

	// In production the process is already DPI-aware by the time any toast shows
	// (input/capture call ensureDPIAware first); replicate that here so the overlay
	// lands at physical-pixel coordinates instead of virtualized ones.
	ensureDPIAware()

	if !registerNoticeClass() {
		t.Fatal("registerNoticeClass failed — overlay cannot be created in this environment")
	}

	ch := showNoticeOverlay(&completeStyle, 6000) // REAL green overlay bottom-right
	if ch == nil {
		t.Fatal("expected the completion notice to show")
	}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("real green notice never signaled first paint within 2s")
	}
	t.Log("green completion toast is on screen")

	// Hold it up so an external screen capture can catch it, then let it fade.
	for i := 0; i < 140 && noticeShowing.Load(); i++ {
		time.Sleep(50 * time.Millisecond)
	}
}

func TestCompleteDurationMS(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int
	}{
		{"default (empty)", "", completeDefaultMS},
		{"explicit", "2500", 2500},
		{"below min clamps", "500", completeMinMS},
		{"over max clamps", "999999", completeMaxMS},
		{"invalid falls back to default", "xyz", completeDefaultMS},
	}
	for _, c := range cases {
		t.Setenv("AGLINK_NOTICE_COMPLETE_MS", c.env)
		if got := completeDurationMS(); got != c.want {
			t.Errorf("%s: completeDurationMS() with env %q = %d, want %d", c.name, c.env, got, c.want)
		}
	}
}

// TestCompleteTextFitsBox is the completion-ack counterpart to TestNoticeTextFitsBox:
// paintNotice draws completeText with the same DrawText/no-ellipsis path, so an
// overflowing string would silently clip. This guards wording changes.
func TestCompleteTextFitsBox(t *testing.T) {
	if !registerNoticeClass() {
		t.Skip("notice window class/font unavailable")
	}
	hdc, _, _ := procGetDCN.Call(0)
	if hdc == 0 {
		t.Skip("no screen DC available")
	}
	defer procReleaseDCN.Call(0, hdc)
	if noticeFont != 0 {
		old, _, _ := procSelectObjectN.Call(hdc, noticeFont)
		defer procSelectObjectN.Call(hdc, old)
	}

	u := windows.StringToUTF16(completeText)
	var sz struct{ CX, CY int32 }
	procGetTextExtentPoint32N.Call(hdc,
		uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1),
		uintptr(unsafe.Pointer(&sz)))

	const avail = noticeW - 40 - 16
	t.Logf("completeText %q measured %dpx wide; text area is %dpx", completeText, sz.CX, avail)
	if int(sz.CX) > avail {
		t.Errorf("completeText is %dpx wide, exceeds the %dpx text area — it will clip; shorten the wording or widen noticeW", sz.CX, avail)
	}
}

// TestEnsureControlNoticeAdvancesSyntheticCount verifies the signal the tool
// middleware relies on: every control op that reaches ensureControlNotice bumps
// syntheticInputCount, so the middleware can tell a screen-driving tool call from
// a read-only one.
func TestEnsureControlNoticeAdvancesSyntheticCount(t *testing.T) {
	t.Setenv("AGLINK_NOTICE_LEAD_MS", "0")
	origRunner := noticeShow
	t.Cleanup(func() {
		noticeShow = origRunner
		noticeShowing.Store(false)
		lastSyntheticInput.Store(0)
	})
	noticeShow = func() { signalNoticeShown() }
	noticeShowing.Store(false)
	// Mid-session (recent input) so no toast is shown/waited on — isolate the count.
	lastSyntheticInput.Store(time.Now().UnixNano())

	before := syntheticInputCount.Load()
	ensureControlNotice()
	if got := syntheticInputCount.Load(); got != before+1 {
		t.Errorf("syntheticInputCount = %d, want %d (ensureControlNotice must count the op)", got, before+1)
	}
}

// TestShowControlCompleteDisabled verifies AGLINK_NO_CONTROL_NOTICE suppresses the
// completion ack too (not just the start warning).
func TestShowControlCompleteDisabled(t *testing.T) {
	origRunner := noticeShow
	origOff := controlNoticeOff
	t.Cleanup(func() {
		noticeShow = origRunner
		controlNoticeOff = origOff
		noticeShowing.Store(false)
		activeStyle = &startStyle
	})
	var shows atomic.Int64
	noticeShow = func() { signalNoticeShown(); shows.Add(1) }
	controlNoticeOff = true
	noticeShowing.Store(false)

	showControlComplete()
	time.Sleep(50 * time.Millisecond) // give any erroneously-spawned overlay a chance
	if shows.Load() != 0 {
		t.Errorf("showControlComplete ran the overlay despite disabled notices; shows=%d", shows.Load())
	}
	if noticeShowing.Load() {
		t.Error("no overlay should be showing when notices are disabled")
	}
}

// TestShowControlCompletePreemptsStartToast is the core interaction: when a start
// warning is still on screen, the completion ack tears it down and replaces it,
// switching the active style from amber to green. Uses a fake overlay runner that
// honors noticeCloseRequested exactly as the real ~60fps timer does.
func TestShowControlCompletePreemptsStartToast(t *testing.T) {
	origRunner := noticeShow
	origOff := controlNoticeOff
	t.Cleanup(func() {
		noticeCloseRequested.Store(true) // release any looping fake
		for i := 0; i < 300 && noticeShowing.Load(); i++ {
			time.Sleep(2 * time.Millisecond)
		}
		noticeShow = origRunner
		controlNoticeOff = origOff
		noticeShowing.Store(false)
		noticeCloseRequested.Store(false)
		activeStyle = &startStyle
		lastSyntheticInput.Store(0)
	})
	controlNoticeOff = false

	// Fake overlay: stays "on screen" until preempted (like the real timer),
	// self-terminating after a bounded time so a missed preempt can't hang.
	noticeShow = func() {
		signalNoticeShown()
		for i := 0; i < 500 && !noticeCloseRequested.Load(); i++ {
			time.Sleep(2 * time.Millisecond)
		}
	}

	noticeShowing.Store(false)
	activeStyle = &startStyle

	// Show the amber start toast and wait until it is "on screen".
	ch := showControlNotice()
	if ch == nil {
		t.Fatal("expected start notice to show")
	}
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("start notice never signaled shown")
	}
	if activeStyle != &startStyle {
		t.Fatal("active style should be start (amber) while the start toast shows")
	}
	if !noticeShowing.Load() {
		t.Fatal("start toast should be marked showing")
	}

	// Completion preempts it.
	showControlComplete()

	if activeStyle != &completeStyle {
		t.Errorf("after showControlComplete, active style is not the green completion style — the start toast was not preempted/replaced")
	}
	if !noticeShowing.Load() {
		t.Error("completion overlay should be marked showing after preemption")
	}
}

// TestControlCompleteMiddleware drives the actual middleware: a tool call that
// bumps the synthetic-input counter must trigger the completion ack, and a
// read-only one (counter unchanged) must not.
func TestControlCompleteMiddleware(t *testing.T) {
	origRunner := noticeShow
	origOff := controlNoticeOff
	t.Cleanup(func() {
		noticeShow = origRunner
		controlNoticeOff = origOff
		noticeShowing.Store(false)
		activeStyle = &startStyle
	})
	controlNoticeOff = false
	var shows atomic.Int64
	noticeShow = func() { signalNoticeShown(); shows.Add(1) }
	noticeShowing.Store(false)

	ctx := context.Background()

	// A screen-driving tool: bumps the counter → completion should fire.
	ctrl := controlCompleteMiddleware(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		syntheticInputCount.Add(1)
		return &mcp.CallToolResult{}, nil
	})
	if _, err := ctrl(ctx, mcp.CallToolRequest{}); err != nil {
		t.Fatalf("control handler returned error: %v", err)
	}
	fired := false
	for i := 0; i < 100; i++ {
		if shows.Load() >= 1 {
			fired = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !fired {
		t.Error("completion notice did not fire after a screen-driving tool call")
	}
	// Let that overlay finish before the next check.
	for i := 0; i < 300 && noticeShowing.Load(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	prev := shows.Load()

	// A read-only tool: counter unchanged → no completion.
	ro := controlCompleteMiddleware(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	if _, err := ro(ctx, mcp.CallToolRequest{}); err != nil {
		t.Fatalf("read-only handler returned error: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if shows.Load() != prev {
		t.Errorf("completion notice fired for a read-only tool call (shows %d -> %d)", prev, shows.Load())
	}
}
