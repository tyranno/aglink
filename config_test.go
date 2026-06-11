package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.txt")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfig_OK(t *testing.T) {
	p := writeTemp(t, `
# comment
TELEGRAM_BOT_TOKEN=123:ABC
ALLOWED_USER_IDS=111, 222
MANAGER_MODEL=haiku
TIMEOUT_MINUTES=5
`)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TelegramBotToken != "123:ABC" {
		t.Errorf("token = %q", cfg.TelegramBotToken)
	}
	if len(cfg.AllowedUserIDs) != 2 || cfg.AllowedUserIDs[0] != 111 || cfg.AllowedUserIDs[1] != 222 {
		t.Errorf("ids = %v", cfg.AllowedUserIDs)
	}
	if cfg.TimeoutMinutes != 5 {
		t.Errorf("timeout = %d", cfg.TimeoutMinutes)
	}
	if !cfg.ManagerAlways {
		t.Errorf("ManagerAlways default should be true")
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	p := writeTemp(t, "TELEGRAM_BOT_TOKEN=t\nALLOWED_USER_IDS=1\n")
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ManagerModel != "haiku" {
		t.Errorf("default ManagerModel = %q", cfg.ManagerModel)
	}
	if cfg.TimeoutMinutes != 10 {
		t.Errorf("default timeout = %d", cfg.TimeoutMinutes)
	}
}

func TestLoadConfig_MissingToken(t *testing.T) {
	p := writeTemp(t, "ALLOWED_USER_IDS=1\n")
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestLoadConfig_MissingAllowlist(t *testing.T) {
	p := writeTemp(t, "TELEGRAM_BOT_TOKEN=t\n")
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("expected error for empty allowlist")
	}
}

func TestLoadConfig_BadUserID(t *testing.T) {
	p := writeTemp(t, "TELEGRAM_BOT_TOKEN=t\nALLOWED_USER_IDS=abc\n")
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("expected error for bad user id")
	}
}

func TestIsAllowed(t *testing.T) {
	c := &Config{AllowedUserIDs: []int64{42}}
	if !c.IsAllowed(42) {
		t.Error("42 should be allowed")
	}
	if c.IsAllowed(99) {
		t.Error("99 should not be allowed")
	}
}

func TestLoadConfig_MaxWorkers(t *testing.T) {
	p := writeTemp(t, "TELEGRAM_BOT_TOKEN=t\nALLOWED_USER_IDS=1\nMAX_WORKERS=5\n")
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxWorkers != 5 {
		t.Errorf("MaxWorkers = %d, want 5", cfg.MaxWorkers)
	}
}

func TestLoadConfig_MaxWorkers_Default(t *testing.T) {
	p := writeTemp(t, "TELEGRAM_BOT_TOKEN=t\nALLOWED_USER_IDS=1\n")
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxWorkers != 3 {
		t.Errorf("MaxWorkers default = %d, want 3", cfg.MaxWorkers)
	}
}

func TestLoadConfig_MaxWorkers_Invalid(t *testing.T) {
	for _, bad := range []string{"0", "-1", "abc"} {
		p := writeTemp(t, "TELEGRAM_BOT_TOKEN=t\nALLOWED_USER_IDS=1\nMAX_WORKERS="+bad+"\n")
		if _, err := LoadConfig(p); err == nil {
			t.Errorf("MAX_WORKERS=%q: expected error, got nil", bad)
		}
	}
}
