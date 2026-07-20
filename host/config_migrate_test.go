package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrMigrate_FromTxt(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "config.txt")
	os.WriteFile(txt, []byte("TELEGRAM_BOT_TOKEN=123:ABC\nALLOWED_USER_IDS=42\nWORKER_MODEL=sonnet\n"), 0o600)

	cfg, used, err := LoadOrMigrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TelegramBotToken != "123:ABC" || cfg.WorkerModel != "sonnet" {
		t.Errorf("cfg = %+v", cfg)
	}
	if filepath.Base(used) != "config.yaml" {
		t.Errorf("used = %s, want config.yaml", used)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err != nil {
		t.Error("config.yaml not created")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.txt.bak")); err != nil {
		t.Error("config.txt.bak not created")
	}
}

func TestLoadOrMigrate_YAMLWins(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.txt"), []byte("TELEGRAM_BOT_TOKEN=TXT\nALLOWED_USER_IDS=1\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("telegram:\n  bot_token: YAML\n  allowed_user_ids: [1]\n"), 0o600)
	cfg, _, err := LoadOrMigrate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TelegramBotToken != "YAML" {
		t.Errorf("expected yaml to win, got %q", cfg.TelegramBotToken)
	}
}

func TestLoadOrMigrate_NeitherExists(t *testing.T) {
	if _, _, err := LoadOrMigrate(t.TempDir()); err == nil {
		t.Fatal("expected error when no config exists")
	}
}
