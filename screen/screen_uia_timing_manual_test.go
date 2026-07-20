//go:build windows

package main

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// TestManualUIASnapshotTiming measures how long uiaSnapshot actually takes
// against whatever window is currently focused — diagnostic for how much of
// the per-call latency budget UIA's per-element, per-property COM round trips
// (no build-cache) consume on a real, complex app window. Gated behind
// AGLINK_MANUAL_UI=1 since it depends on real on-screen state.
// Run with:  AGLINK_MANUAL_UI=1 go test -run TestManualUIASnapshotTiming -v
func TestManualUIASnapshotTiming(t *testing.T) {
	if os.Getenv("AGLINK_MANUAL_UI") == "" {
		t.Skip("set AGLINK_MANUAL_UI=1 to run against the real foreground window")
	}
	for i := 0; i < 3; i++ {
		start := time.Now()
		text, err := uiaSnapshot(200)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("uiaSnapshot failed: %v", err)
		}
		t.Logf("run %d: uiaSnapshot(200) took %v, %d bytes of output", i+1, elapsed, len(text))
	}
}

// TestManualWinControlsTiming measures listControls (the Win32-enumeration
// path, no COM/UIA involved) against the current foreground window, as a
// baseline to compare uiaSnapshot's COM round-trip cost against.
// Run with:  AGLINK_MANUAL_UI=1 go test -run TestManualWinControlsTiming -v
func TestManualWinControlsTiming(t *testing.T) {
	if os.Getenv("AGLINK_MANUAL_UI") == "" {
		t.Skip("set AGLINK_MANUAL_UI=1 to run against the real foreground window")
	}
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		t.Fatal("no foreground window")
	}
	for i := 0; i < 3; i++ {
		start := time.Now()
		ctrls, err := listControls(fmt.Sprintf("0x%x", hwnd), false)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("listControls failed: %v", err)
		}
		t.Logf("run %d: listControls took %v, %d controls", i+1, elapsed, len(ctrls))
	}
}

// TestManualCaptureWindowTiming measures captureWindow (GDI BitBlt, no COM)
// against the current foreground window, as another comparison point.
// Run with:  AGLINK_MANUAL_UI=1 go test -run TestManualCaptureWindowTiming -v
func TestManualCaptureWindowTiming(t *testing.T) {
	if os.Getenv("AGLINK_MANUAL_UI") == "" {
		t.Skip("set AGLINK_MANUAL_UI=1 to run against the real foreground window")
	}
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		t.Fatal("no foreground window")
	}
	for i := 0; i < 3; i++ {
		start := time.Now()
		png, _, _, _, _, err := captureWindow(fmt.Sprintf("0x%x", hwnd))
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("captureWindow failed: %v", err)
		}
		t.Logf("run %d: captureWindow took %v, %d bytes PNG", i+1, elapsed, len(png))
	}
}
