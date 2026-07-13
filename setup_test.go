package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestWriteConfigFile_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml") // also tests dir creation
	if err := writeConfigFile(path, writeConfigOpts{token: "123:ABC", userID: 6723802240, backend: "claude"}); err != nil {
		t.Fatalf("writeConfigFile: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("generated config must be loadable: %v", err)
	}
	if cfg.TelegramBotToken != "123:ABC" {
		t.Errorf("token = %q", cfg.TelegramBotToken)
	}
	if len(cfg.AllowedUserIDs) != 1 || cfg.AllowedUserIDs[0] != 6723802240 {
		t.Errorf("ids = %v", cfg.AllowedUserIDs)
	}
	if cfg.ManagerModel != "haiku" || cfg.TimeoutMinutes != 10 || !cfg.ManagerAlways {
		t.Errorf("defaults wrong: %+v", cfg)
	}
	if cfg.ClaudeOauthToken != "" {
		t.Errorf("expected no oauth token, got %q", cfg.ClaudeOauthToken)
	}
	if cfg.DefaultBackend != "claude" {
		t.Errorf("backend = %q, want claude", cfg.DefaultBackend)
	}
}

func TestWriteConfigFile_WithOauthToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeConfigFile(path, writeConfigOpts{token: "123:ABC", userID: 1, claudeToken: "sk-ant-oat01-TESTONLY", backend: "claude"}); err != nil {
		t.Fatalf("writeConfigFile: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClaudeOauthToken != "sk-ant-oat01-TESTONLY" {
		t.Errorf("oauth token = %q", cfg.ClaudeOauthToken)
	}
}

func TestWriteConfigFile_CodexOnlyBackend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeConfigFile(path, writeConfigOpts{token: "123:ABC", userID: 1, backend: "codex"}); err != nil {
		t.Fatalf("writeConfigFile: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultBackend != "codex" {
		t.Errorf("backend = %q, want codex", cfg.DefaultBackend)
	}
}

// TestWriteConfigFile_WebOnly pins the gap this was added to close: a
// web-chat-only setup (no Telegram token) must still produce a config that
// passes validate() by enabling a web frontend, and AglinkChat must imply
// ChatControl on the file it writes (not just after a subsequent LoadConfig
// re-derives it) so the on-disk config isn't misleading about its own state.
func TestWriteConfigFile_WebOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	opts := writeConfigOpts{
		token:      "", // no Telegram
		userID:     webOnlyPlaceholderUserID,
		backend:    "claude",
		aglinkChat: true,
		webControl: true,
	}
	if err := writeConfigFile(path, opts); err != nil {
		t.Fatalf("writeConfigFile: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("web-only config must still pass validate(): %v", err)
	}
	if cfg.TelegramBotToken != "" {
		t.Errorf("token = %q, want empty", cfg.TelegramBotToken)
	}
	if !cfg.AglinkChat || !cfg.ChatControl {
		t.Errorf("expected AglinkChat and ChatControl both enabled, got AglinkChat=%v ChatControl=%v", cfg.AglinkChat, cfg.ChatControl)
	}
	if !cfg.WebControl {
		t.Error("expected WebControl enabled")
	}
	if cfg.ScreenControl {
		t.Error("expected ScreenControl to default off even when other aglink features are on")
	}
}

// TestMergeTelegramUserID pins RunTelegramSetup's id-merge logic: a
// web-only setup's placeholder id must be replaced (not left alongside a
// real Telegram id), a genuinely new id gets appended, and re-linking the
// same id already present is a no-op rather than a duplicate.
func TestMergeTelegramUserID(t *testing.T) {
	cases := []struct {
		name     string
		existing []int64
		userID   int64
		want     []int64
	}{
		{"replaces web-only placeholder", []int64{webOnlyPlaceholderUserID}, 6723802240, []int64{6723802240}},
		{"appends to existing real ids", []int64{111, 222}, 333, []int64{111, 222, 333}},
		{"no duplicate for already-present id", []int64{111, 222}, 222, []int64{111, 222}},
		{"empty existing gets the new id", nil, 42, []int64{42}},
		// The placeholder must never survive alongside a real id, however the
		// mixed list arose (e.g. a hand-edited config).
		{"strips placeholder mixed with a real id", []int64{webOnlyPlaceholderUserID, 100}, 200, []int64{100, 200}},
		{"strips placeholder when re-linking an already-present id", []int64{webOnlyPlaceholderUserID, 100}, 100, []int64{100}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeTelegramUserID(tc.existing, tc.userID)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("mergeTelegramUserID(%v, %d) = %v, want %v", tc.existing, tc.userID, got, tc.want)
			}
		})
	}
}
