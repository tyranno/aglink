package main

import (
	"slices"
	"testing"
)

func TestOpencodeManagerModelFallback(t *testing.T) {
	if got := opencodeManagerModel(&Config{OpencodeModel: "anthropic/claude-sonnet-5"}); got != "anthropic/claude-sonnet-5" {
		t.Errorf("empty manager model should fall back to worker model, got %q", got)
	}
	if got := opencodeManagerModel(&Config{OpencodeModel: "a/b", OpencodeManagerModel: "groq/llama"}); got != "groq/llama" {
		t.Errorf("explicit manager model should win, got %q", got)
	}
}

func TestOpencodeRunArgs(t *testing.T) {
	// Full support: model + resume session + json.
	got := opencodeRunArgs("ollama/qwen2.5", "ses_123", true, true, true, true)
	want := []string{"run", "--print-logs", "--session", "ses_123", "--model", "ollama/qwen2.5"}
	if !slices.Equal(got, want) {
		t.Errorf("full args = %v, want %v", got, want)
	}

	// New turn (no resume) omits --session even with a stale id.
	got = opencodeRunArgs("m", "ses_x", false, false, true, true)
	want = []string{"run", "--model", "m"}
	if !slices.Equal(got, want) {
		t.Errorf("new-turn args = %v, want %v", got, want)
	}

	// Unsupported flags are never emitted (capability probe returned false).
	got = opencodeRunArgs("m", "ses_x", true, false, false, false)
	want = []string{"run"}
	if !slices.Equal(got, want) {
		t.Errorf("unsupported-flag args = %v, want %v", got, want)
	}
}

func TestParseOpencodeSessionID(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"sessionID field", `{"type":"start","sessionID":"ses_abc"}`, "ses_abc"},
		{"session field", `noise
{"session":"ses_def"}`, "ses_def"},
		{"info.id nested", `{"info":{"id":"ses_ghi"}}`, "ses_ghi"},
		{"bare ses-prefixed id", `{"id":"ses_jkl"}`, "ses_jkl"},
		{"non-ses id ignored", `{"id":"msg_x"}`, ""},
		{"no json", "just plain text answer", ""},
	}
	for _, c := range cases {
		if got := parseOpencodeSessionID(c.in); got != c.want {
			t.Errorf("%s: parseOpencodeSessionID=%q want %q", c.name, got, c.want)
		}
	}
}

func TestParseOpencodeTextPlain(t *testing.T) {
	if got := parseOpencodeText("  hello world  \n", false); got != "hello world" {
		t.Errorf("plain text should be trimmed stdout, got %q", got)
	}
}

func TestHelpMentionsFlag(t *testing.T) {
	help := "Usage: opencode run [message..]\n  --model <model>  the model\n  --session <id>   resume"
	if !helpMentionsFlag(help, "--session") {
		t.Error("expected --session detected")
	}
	if helpMentionsFlag(help, "--nonexistent") {
		t.Error("did not expect --nonexistent detected")
	}
}

func TestParseOpencodeVersion(t *testing.T) {
	if got := parseOpencodeVersion("opencode 0.4.12"); got != "0.4.12" {
		t.Errorf("got %q", got)
	}
	if got := parseOpencodeVersion("no version here"); got != "" {
		t.Errorf("got %q", got)
	}
}
