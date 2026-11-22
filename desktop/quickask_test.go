package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestQuickAskPayload(t *testing.T) {
	if got := quickAskPayload("  안녕하세요 \n", true)["clipboard"]; got != "안녕하세요" {
		t.Fatalf("trimmed clipboard = %q", got)
	}
	if got := quickAskPayload("무시", false)["clipboard"]; got != "" {
		t.Fatalf("unavailable clipboard must be empty, got %q", got)
	}
	long := strings.Repeat("가", maxQuickAskClipRunes+50)
	got, _ := quickAskPayload(long, true)["clipboard"].(string)
	if n := utf8.RuneCountInString(got); n != maxQuickAskClipRunes {
		t.Fatalf("capped to %d runes, want %d", n, maxQuickAskClipRunes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("cap must not split a rune")
	}
	if quickAskPayload("", true)["hotkey"] != QuickAskHotkeyLabel {
		t.Fatal("payload must carry the hotkey label")
	}
}
