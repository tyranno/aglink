package main

import (
	"net/http"
	"net/http/httptest"
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

func TestHandleConfig_GetMasksAndPutValidates(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &Config{TelegramBotToken: "S3CRET", WebChatAddr: "127.0.0.1:1717", ChatControlAddr: "127.0.0.1:17170"}
	raw, err := marshalConfigYAML(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	s := &webServer{token: "tok", cfgPath: cfgPath, holder: NewConfigHolder(cfg)}

	// GET masks the secret.
	getReq := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	getReq.Header.Set("Authorization", "Bearer tok")
	getRR := httptest.NewRecorder()
	s.handleConfig(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("GET status %d", getRR.Code)
	}
	if strings.Contains(getRR.Body.String(), "S3CRET") {
		t.Errorf("GET leaked secret")
	}

	// PUT invalid YAML → 400, file unchanged.
	badReq := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader("::: not yaml :::"))
	badReq.Header.Set("Authorization", "Bearer tok")
	badRR := httptest.NewRecorder()
	s.handleConfig(badRR, badReq)
	if badRR.Code != http.StatusBadRequest {
		t.Errorf("bad PUT status = %d, want 400", badRR.Code)
	}
	after, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(after), "not yaml") {
		t.Errorf("invalid config was written")
	}
}
