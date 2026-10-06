package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClampLimit(t *testing.T) {
	cases := []struct{ v, want int }{{0, 100}, {-5, 100}, {50, 50}, {100, 100}, {999, 500}}
	for _, c := range cases {
		if got := clampLimit(c.v, 100, 500); got != c.want {
			t.Errorf("clampLimit(%d) = %d, want %d", c.v, got, c.want)
		}
	}
}

func TestRuneWindowWholeTextNoNote(t *testing.T) {
	body, note := runeWindow("hello", 0, 100, true)
	if body != "hello" || note != "" {
		t.Errorf("short text should be returned whole with no note, got %q %q", body, note)
	}
}

func TestRuneWindowTruncatesWithContinuation(t *testing.T) {
	s := strings.Repeat("a", 25)
	body, note := runeWindow(s, 0, 10, true)
	if body != strings.Repeat("a", 10) {
		t.Errorf("body = %q", body)
	}
	for _, want := range []string{"잘림", "전체 25자", "0–10", "offset=10"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q missing %q", note, want)
		}
	}
	// Continue from the reported offset.
	body, note = runeWindow(s, 10, 10, true)
	if len(body) != 10 || !strings.Contains(note, "offset=20") {
		t.Errorf("second page: body len %d note %q", len(body), note)
	}
	// Last page reaches the end: no "continue" offset, but says it's partial.
	body, note = runeWindow(s, 20, 10, true)
	if len(body) != 5 || strings.Contains(note, "offset=25") || !strings.Contains(note, "끝까지") {
		t.Errorf("last page: body %q note %q", body, note)
	}
}

func TestRuneWindowRuneBoundaries(t *testing.T) {
	s := strings.Repeat("한글", 10) // 20 runes, 60 bytes
	body, note := runeWindow(s, 3, 5, true)
	if !utf8.ValidString(body) || utf8.RuneCountInString(body) != 5 {
		t.Errorf("must cut on rune boundaries, got %q", body)
	}
	if body != "글한글한글" {
		t.Errorf("window 3..8 = %q", body)
	}
	if !strings.Contains(note, "전체 20자") || !strings.Contains(note, "offset=8") {
		t.Errorf("note counts runes, not bytes: %q", note)
	}
}

func TestRuneWindowTail(t *testing.T) {
	s := "0123456789"
	body, note := runeWindow(s, -3, 100, true)
	if body != "789" || !strings.Contains(note, "7–10") {
		t.Errorf("tail -3: %q %q", body, note)
	}
	// Tail larger than max_chars is capped to max_chars (newest content).
	body, _ = runeWindow(s, -8, 4, true)
	if body != "6789" {
		t.Errorf("tail capped by maxChars: %q", body)
	}
	// Tail larger than the text returns all of it with no note.
	body, note = runeWindow(s, -50, 100, true)
	if body != s || note != "" {
		t.Errorf("oversized tail: %q %q", body, note)
	}
}

func TestRuneWindowOffsetPastEnd(t *testing.T) {
	body, note := runeWindow("abc", 10, 5, true)
	if body != "" || !strings.Contains(note, "offset=10") {
		t.Errorf("past end: %q %q", body, note)
	}
}

func TestRuneWindowUnknownTotal(t *testing.T) {
	// Fetch was cut at a ceiling: even a whole-window read must say there may be more.
	body, note := runeWindow("abcdef", 0, 100, false)
	if body != "abcdef" || !strings.Contains(note, "6자 이상") || !strings.Contains(note, "offset=6") {
		t.Errorf("unknown total: %q %q", body, note)
	}
}

func TestRuneWindowDefaultMax(t *testing.T) {
	s := strings.Repeat("x", defaultTextMaxChars+10)
	body, note := runeWindow(s, 0, 0, true)
	if utf8.RuneCountInString(body) != defaultTextMaxChars || note == "" {
		t.Errorf("maxChars<=0 should use the default cap, got %d", utf8.RuneCountInString(body))
	}
}

func TestClampFieldValue(t *testing.T) {
	if v, note := clampFieldValue("short", true); v != "short" || note != "" {
		t.Errorf("under cap unchanged, got %q %q", v, note)
	}
	over := strings.Repeat("y", maxFieldValueChars+500)
	v, note := clampFieldValue(over, true)
	if utf8.RuneCountInString(v) != maxFieldValueChars {
		t.Errorf("value should be cut to %d runes, got %d", maxFieldValueChars, utf8.RuneCountInString(v))
	}
	if !strings.Contains(note, "get_text") || !strings.Contains(note, "offset=2000") || !strings.Contains(note, "2500자") {
		t.Errorf("note should point at get_text continuation: %q", note)
	}
	// Fetch-cut text: total unknown.
	if _, note := clampFieldValue(strings.Repeat("z", maxFieldValueChars+1), false); !strings.Contains(note, "이상") {
		t.Errorf("unknown total should say 이상: %q", note)
	}
}

func TestPageBounds(t *testing.T) {
	cases := []struct{ off, lim, total, s, e int }{
		{0, 10, 25, 0, 10},
		{20, 10, 25, 20, 25},
		{-3, 10, 25, 0, 10},
		{30, 10, 25, 25, 25},
		{0, 0, 25, 0, 25},
	}
	for _, c := range cases {
		s, e := pageBounds(c.off, c.lim, c.total)
		if s != c.s || e != c.e {
			t.Errorf("pageBounds(%d,%d,%d) = %d,%d want %d,%d", c.off, c.lim, c.total, s, e, c.s, c.e)
		}
	}
}

func TestPageNote(t *testing.T) {
	if n := pageNote("elements", 0, 50, 50, ""); n != "" {
		t.Errorf("complete first page needs no note, got %q", n)
	}
	n := pageNote("elements", 0, 150, 400, "max_elements=150")
	for _, want := range []string{"잘림", "max_elements=150", "400개", "offset=150"} {
		if !strings.Contains(n, want) {
			t.Errorf("note %q missing %q", n, want)
		}
	}
	if n := pageNote("controls", 200, 250, 250, ""); !strings.Contains(n, "끝까지") || strings.Contains(n, "offset=") {
		t.Errorf("final page note: %q", n)
	}
}
