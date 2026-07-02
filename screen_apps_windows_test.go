//go:build windows

package main

import "testing"

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
