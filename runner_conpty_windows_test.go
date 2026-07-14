//go:build windows

package main

import (
	"strings"
	"testing"
	"time"
)

func TestStripANSI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text unaffected", "READY", "READY"},
		{"csi color codes removed", "\x1b[31mhello\x1b[0m", "hello"},
		{"osc title sequence removed", "\x1b]0;claude\x07status line", "status line"},
		{"cursor move sequences removed", "\x1b[2K\x1b[1Gtext", "text"},
		{"mixed real-world chunk", "\x1b[1mPress up\x1b[0m to edit queued messages \xc2\xb7 esc to interrupt", "Press up to edit queued messages \xc2\xb7 esc to interrupt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(stripANSI([]byte(c.in)))
			if got != c.want {
				t.Errorf("stripANSI(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestLooksLikeOnboardingPrompt(t *testing.T) {
	if !looksLikeOnboardingPrompt("Do you trust the files in this folder?") {
		t.Error("expected trust-folder prompt to be detected")
	}
	if !looksLikeOnboardingPrompt("noise\nChoose the text style that looks best\nmore noise") {
		t.Error("expected theme-picker prompt to be detected")
	}
	if looksLikeOnboardingPrompt("READY") {
		t.Error("did not expect a normal reply to look like onboarding")
	}
}

func TestCleanTurnOutput(t *testing.T) {
	got := cleanTurnOutput("  \n  hello world  \n\n")
	if got != "hello world" {
		t.Errorf("cleanTurnOutput = %q, want %q", got, "hello world")
	}
}

func TestSafeBufferSince(t *testing.T) {
	b := &safeBuffer{}
	b.Write([]byte("first"))
	offset := b.Len()
	b.Write([]byte("second"))
	if got := b.Since(offset); got != "second" {
		t.Errorf("Since(%d) = %q, want %q", offset, got, "second")
	}
	if got := b.Since(0); got != "firstsecond" {
		t.Errorf("Since(0) = %q, want %q", got, "firstsecond")
	}
	// offset past current length must not panic or underflow.
	if got := b.Since(1000); got != "" {
		t.Errorf("Since(huge) = %q, want empty", got)
	}
}

func TestIdleTracker(t *testing.T) {
	tr := newIdleTracker()
	if tr.idleFor() < 0 {
		t.Error("idleFor should never be negative")
	}
	tr.touch()
	if tr.idleFor() > 100*time.Millisecond {
		t.Errorf("idleFor right after touch should be tiny, got %v", tr.idleFor())
	}
}

func TestWindowsQuoteArg(t *testing.T) {
	cases := map[string]string{
		"simple":              "simple",
		"":                    `""`,
		"with space":          `"with space"`,
		`has"quote`:           `"has\"quote"`,
		`trailing\`:           `trailing\`,
		`C:\Program Files\x`:  `"C:\Program Files\x"`,
	}
	for in, want := range cases {
		if got := windowsQuoteArg(in); got != want {
			t.Errorf("windowsQuoteArg(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildWindowsCommandLine(t *testing.T) {
	got := buildWindowsCommandLine(`C:\Program Files\claude.exe`, []string{"--model", "sonnet", "--dangerously-skip-permissions"})
	want := `"C:\Program Files\claude.exe" --model sonnet --dangerously-skip-permissions`
	if got != want {
		t.Errorf("buildWindowsCommandLine = %q, want %q", got, want)
	}
}

func TestInteractiveSessionArgsIncludesIsolationAndModel(t *testing.T) {
	cfg := &Config{}
	req := RunRequest{Model: "opus"}
	args := interactiveSessionArgs(cfg, req)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--dangerously-skip-permissions") {
		t.Errorf("expected --dangerously-skip-permissions in args, got %v", args)
	}
	if !strings.Contains(joined, "--model opus") {
		t.Errorf("expected --model opus in args, got %v", args)
	}
	if !strings.Contains(joined, "--strict-mcp-config") {
		t.Errorf("expected isolationArgs to be included, got %v", args)
	}
}

func TestSessionEnvAddsOwnerLabelOnlyWhenSet(t *testing.T) {
	withLabel := sessionEnv("web:42")
	found := false
	for _, e := range withLabel {
		if e == "AGLINK_OWNER_LABEL=web:42" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected AGLINK_OWNER_LABEL=web:42 in env, got %v", withLabel)
	}

	withoutLabel := sessionEnv("")
	for _, e := range withoutLabel {
		if strings.HasPrefix(e, "AGLINK_OWNER_LABEL=") {
			t.Errorf("did not expect AGLINK_OWNER_LABEL in env when ownerLabel is empty, got %v", withoutLabel)
		}
	}
}
