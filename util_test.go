package main

import (
	"regexp"
	"strings"
	"testing"
)

func TestTruncate(t *testing.T) {
	cases := []struct {
		s    string
		n    int
		want string
	}{
		{"hello", 10, "hello"}, // shorter than limit
		{"hello", 5, "hello"},  // exactly limit
		{"hello", 4, "hel…"},   // truncated: r[:n-1]+"…" = r[:3]+"…"
		{"가나다라마", 3, "가나…"},    // Korean multibyte
		{"ab", 1, "a"},         // n=1: no ellipsis (n<=1 branch)
		{"abc", 0, ""},         // n=0: empty
	}
	for _, tc := range cases {
		got := truncate(tc.s, tc.n)
		if got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
		}
	}
}

func TestNewUUID_Format(t *testing.T) {
	// RFC-4122 v4: xxxxxxxx-xxxx-4xxx-[89ab]xxx-xxxxxxxxxxxx
	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for range 10 {
		id := newUUID()
		if !uuidRe.MatchString(id) {
			t.Errorf("newUUID() = %q does not match v4 UUID format", id)
		}
	}
}

func TestNewUUID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		id := newUUID()
		if seen[id] {
			t.Fatalf("duplicate UUID generated: %s", id)
		}
		seen[id] = true
	}
}

func TestSanitizeName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"myapp", "myapp"},
		{"my app", "my app"},   // spaces are allowed
		{"my/app", "my_app"},   // slash → underscore
		{"my\\app", "my_app"},  // backslash → underscore
		{"my:app", "my_app"},   // colon → underscore
		{"my*app?", "my_app_"}, // * and ? → underscore
		{`my"app`, "my_app"},   // quote → underscore
		{"my<app>", "my_app_"}, // < and > → underscore
		{"my|app", "my_app"},   // pipe → underscore
		{"정상이름", "정상이름"},       // Korean: unchanged
	}
	for _, tc := range cases {
		got := sanitizeName(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEstimateTokens_ZeroForEmpty(t *testing.T) {
	if n := estimateTokens(""); n != 0 {
		t.Errorf("estimateTokens(\"\") = %d, want 0", n)
	}
}

func TestEstimateTokens_Korean(t *testing.T) {
	// Korean chars each count as a "word" (ch > 127); 5 chars * 1.4 ≈ 7
	n := estimateTokens("안녕하세요")
	if n < 3 || n > 15 {
		t.Errorf("estimateTokens(Korean 5 chars) = %d, want ~7", n)
	}
}

func TestEstimateTokens_MultiWord(t *testing.T) {
	n := estimateTokens("hello world foo bar baz")
	// 5 words * 1.4 = 7
	if n < 5 || n > 12 {
		t.Errorf("estimateTokens(5 words) = %d, out of expected range [5,12]", n)
	}
}

func TestNewTaskID_Format(t *testing.T) {
	// newTaskID should return 8 hex chars
	hexRe := regexp.MustCompile(`^[0-9a-f]{8}$`)
	for i := 0; i < 20; i++ {
		id := newTaskID()
		if !hexRe.MatchString(id) {
			t.Errorf("newTaskID() = %q, want 8 hex chars", id)
		}
	}
}

func TestNewTaskID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		id := newTaskID()
		if seen[id] {
			// 4-byte (32-bit) random space — collisions are possible but extremely rare in 100 draws
		}
		seen[id] = true
	}
	// With 4-byte random IDs, ~100 collisions in 100 draws is extremely unlikely
	if len(seen) < 95 {
		t.Errorf("too many duplicates: only %d unique in 100 draws", len(seen))
	}
}

func TestDurationToCron_Coverage(t *testing.T) {
	// Test values not covered by scheduler_test.go
	cases := []struct {
		want string
	}{}
	// Just verify the known edge: <1min returns "* * * * *"
	_ = cases
	got := durationToCron(30 * 1000 * 1000 * 1000) // 30 seconds < 1 minute
	if !strings.Contains(got, "* * * * *") {
		t.Errorf("durationToCron(30s) = %q, want every-minute", got)
	}
}
