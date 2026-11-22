//go:build windows

package main

import (
	"os"
	"testing"
)

// TestManualClipboardRoundTrip sets and reads back the REAL clipboard, then
// restores the previous text. Gated behind AGLINK_MANUAL_UI=1 because it
// touches the user's clipboard (non-text content such as an image cannot be
// restored).
// Run with:  AGLINK_MANUAL_UI=1 go test -run TestManualClipboardRoundTrip -v
func TestManualClipboardRoundTrip(t *testing.T) {
	if os.Getenv("AGLINK_MANUAL_UI") == "" {
		t.Skip("set AGLINK_MANUAL_UI=1 to run against the real clipboard")
	}
	prev, hadText, err := clipboardGetText()
	if err != nil {
		t.Fatalf("clipboardGetText: %v", err)
	}
	t.Cleanup(func() {
		if hadText {
			if err := clipboardSetText(prev); err != nil {
				t.Errorf("restoring the clipboard failed: %v", err)
			}
		}
	})
	want := "aglink 클립보드 테스트 ✓\r\nline 2"
	if err := clipboardSetText(want); err != nil {
		t.Fatalf("clipboardSetText: %v", err)
	}
	got, ok, err := clipboardGetText()
	if err != nil || !ok || got != want {
		t.Fatalf("round trip = %q, %v, %v; want %q", got, ok, err, want)
	}
	if err := clipboardSetText("a\x00b"); err == nil {
		t.Error("text with NUL must be rejected")
	}
}
