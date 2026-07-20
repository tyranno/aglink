package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaskAndRestore_RoundTrip(t *testing.T) {
	c := &Config{
		TelegramBotToken: "123:SECRET",
		ClaudeOauthToken: "oauth-xyz",
		WebChatToken:     "webtok",
		ChatControlToken: "ctltok",
	}
	raw := []byte("telegram:\n  bot_token: 123:SECRET\nclaude:\n  oauth_token: oauth-xyz\nweb_chat:\n  token: webtok\nchat_control:\n  token: ctltok\n")

	masked := maskConfigSecrets(raw, c)
	for _, secret := range []string{"123:SECRET", "oauth-xyz", "webtok", "ctltok"} {
		if strings.Contains(string(masked), secret) {
			t.Errorf("masked output still contains secret %q", secret)
		}
	}
	if !strings.Contains(string(masked), configSecretSentinel) {
		t.Errorf("masked output missing sentinel")
	}

	restored := restoreConfigSecrets(masked, c)
	if string(restored) != string(raw) {
		t.Errorf("round-trip mismatch:\n got: %s\nwant: %s", restored, raw)
	}
}

func TestMask_EmptySecretsNotMasked(t *testing.T) {
	c := &Config{TelegramBotToken: ""} // empty → nothing to mask
	raw := []byte("telegram:\n  bot_token: \n")
	masked := maskConfigSecrets(raw, c)
	if strings.Contains(string(masked), configSecretSentinel) {
		t.Errorf("empty secret should not introduce a sentinel")
	}
}

func TestRestore_UserChangedValueKept(t *testing.T) {
	c := &Config{TelegramBotToken: "old"}
	edited := []byte("telegram:\n  bot_token: brand-new\n") // user typed a real new value
	restored := restoreConfigSecrets(edited, c)
	if !strings.Contains(string(restored), "brand-new") {
		t.Errorf("user-changed value must be preserved, got %s", restored)
	}
}

func TestReadMaskedAndWriteValidatedConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &Config{TelegramBotToken: "S3CRET", AllowedUserIDs: []int64{123}, WebChatAddr: "127.0.0.1:27271", ChatControlAddr: "127.0.0.1:27270"}
	raw, err := marshalConfigYAML(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// readMaskedConfig masks the secret.
	masked, err := readMaskedConfig(cfgPath, cfg)
	if err != nil {
		t.Fatalf("readMaskedConfig: %v", err)
	}
	if strings.Contains(string(masked), "S3CRET") {
		t.Errorf("masked config leaked secret")
	}

	// writeValidatedConfig rejects invalid YAML and leaves the file untouched.
	if err := writeValidatedConfig(cfgPath, cfg, []byte("::: not yaml :::")); err == nil {
		t.Errorf("invalid YAML should return an error")
	}
	after, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(after), "not yaml") {
		t.Errorf("invalid config was written")
	}

	// A valid edit (masked secret preserved) writes and round-trips.
	if err := writeValidatedConfig(cfgPath, cfg, masked); err != nil {
		t.Errorf("valid config should write, got %v", err)
	}
	back, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(back), "S3CRET") {
		t.Errorf("masked sentinel should have been restored to the real secret on write")
	}
}
