package main

import (
	"path/filepath"
	"testing"
)

func TestWriteConfigFile_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml") // also tests dir creation
	if err := writeConfigFile(path, "123:ABC", 6723802240, ""); err != nil {
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
}

func TestWriteConfigFile_WithOauthToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeConfigFile(path, "123:ABC", 1, "sk-ant-oat01-TESTONLY"); err != nil {
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
