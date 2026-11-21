//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestManualNumbersAndOCR is a live smoke test of show_numbers / click_number /
// ocr against a fresh Notepad: numbers its elements (badges for 3s, plus a
// numbered capture written to the temp dir), opens the File menu by number via
// background UIA invoke (no mouse), OCRs the window and looks the menu word up
// with the click_text matcher (no click), then closes Notepad.
// Run with:  AGLINK_MANUAL_UI=1 go test -run TestManualNumbersAndOCR -v
func TestManualNumbersAndOCR(t *testing.T) {
	if os.Getenv("AGLINK_MANUAL_UI") == "" {
		t.Skip("set AGLINK_MANUAL_UI=1 to run against a real Notepad window")
	}
	before := map[uintptr]bool{}
	for _, w := range enumWindows() {
		before[w.HWND] = true
	}
	if err := exec.Command("notepad.exe").Start(); err != nil {
		t.Fatal(err)
	}
	var hwnd uintptr
	for i := 0; i < 50 && hwnd == 0; i++ {
		time.Sleep(200 * time.Millisecond)
		for _, w := range enumWindows() {
			if !before[w.HWND] && (strings.Contains(w.Title, "메모장") || strings.Contains(w.Title, "Notepad")) {
				hwnd = w.HWND
			}
		}
	}
	if hwnd == 0 {
		t.Fatal("notepad window did not appear")
	}
	defer closeWindow(fmt.Sprintf("0x%x", hwnd))
	time.Sleep(800 * time.Millisecond)
	win := fmt.Sprintf("0x%x", hwnd)

	start := time.Now()
	text, png, caption, err := showNumbers(win, 3, defaultMarkMax, true)
	if err != nil {
		t.Fatalf("showNumbers: %v", err)
	}
	t.Logf("show_numbers took %v\n%s\n%s", time.Since(start), text, caption)
	if png != nil {
		p := filepath.Join(os.TempDir(), "aglink-marks-smoke.png")
		_ = os.WriteFile(p, png, 0o644)
		t.Logf("numbered capture: %s (%d bytes)", p, len(png))
	}
	time.Sleep(1500 * time.Millisecond) // let a human see the badges

	ms, _, err := marks.lookup(hwnd, 1, time.Now(), markWindowAlive)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	fileN := 0
	for _, it := range ms.Items {
		if it.Invokable && (it.Name == "파일" || it.Name == "File") {
			fileN = it.N
		}
	}
	if fileN == 0 {
		t.Log("no File menu item numbered; skipping click_number")
	} else {
		res, err := clickNumber(win, fileN, false, false, true)
		t.Logf("click_number(%d): %s %v", fileN, res, err)
		time.Sleep(700 * time.Millisecond)
	}

	start = time.Now()
	out, err := ocrTool(ocrRegion{Window: win}, "")
	if err != nil {
		t.Fatalf("ocr: %v", err)
	}
	t.Logf("ocr took %v\n%s", time.Since(start), out)
}
