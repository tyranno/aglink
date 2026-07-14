package main

import (
	"os"
	"testing"
)

// The AGLINK_OWNER_LABEL env carries a conversation's identity into the
// aglink-screen control lease (docs/control-ownership.md §5). These tests pin
// the label format and the env-injection so a refactor can't silently drop the
// owner tag — which would make every busy/status message identify owners by
// bare PID only.

func TestScreenOwnerLabel(t *testing.T) {
	cases := []struct {
		name   string
		chatID int64
		conv   *Conversation
		want   string
	}{
		{"web conv", 7, &Conversation{ID: "w3"}, "chat:7/conv:w3"},
		{"telegram conv", 42, &Conversation{ID: "tg-1"}, "chat:42/conv:tg-1"},
		{"nil conv", 5, nil, "chat:5"},
		{"empty id", 5, &Conversation{}, "chat:5"},
	}
	for _, c := range cases {
		if got := screenOwnerLabel(c.chatID, c.conv); got != c.want {
			t.Errorf("%s: screenOwnerLabel(%d, %v) = %q, want %q", c.name, c.chatID, c.conv, got, c.want)
		}
	}
}

func TestWorkerCmdEnv_InjectsOwnerLabel(t *testing.T) {
	env := workerCmdEnv("", "chat:7/conv:w3")
	if env == nil {
		t.Fatal("workerCmdEnv returned nil despite an owner label — AGLINK_OWNER_LABEL would never reach aglink-screen")
	}
	if !hasEnv(env, "AGLINK_OWNER_LABEL=chat:7/conv:w3") {
		t.Errorf("AGLINK_OWNER_LABEL missing from worker env: %v", tailEnv(env, 3))
	}
	// The parent environment must still be inherited, not replaced.
	if len(env) <= len(os.Environ()) {
		t.Errorf("worker env len %d did not extend the parent env len %d", len(env), len(os.Environ()))
	}
}

func TestWorkerCmdEnv_BothTokensAndLabel(t *testing.T) {
	env := workerCmdEnv("tok-123", "chat:1")
	if !hasEnv(env, "CLAUDE_CODE_OAUTH_TOKEN=tok-123") {
		t.Error("OAuth token missing when both token and label are supplied")
	}
	if !hasEnv(env, "AGLINK_OWNER_LABEL=chat:1") {
		t.Error("owner label missing when both token and label are supplied")
	}
}

func TestWorkerCmdEnv_NilWhenNothingToAdd(t *testing.T) {
	// No token, no label → inherit the parent environment unchanged (nil).
	if env := workerCmdEnv("", ""); env != nil {
		t.Errorf("workerCmdEnv(\"\", \"\") = non-nil (%d entries), want nil (inherit unchanged)", len(env))
	}
}

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func tailEnv(env []string, n int) []string {
	if len(env) <= n {
		return env
	}
	return env[len(env)-n:]
}
