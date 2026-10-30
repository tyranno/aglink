package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A made-up token in the setup-token format.
const fakeOAT = "sk-ant-oat01-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-wAAA"

func TestValidClaudeToken(t *testing.T) {
	if !validClaudeToken(fakeOAT) {
		t.Fatal("a real-shaped token was rejected")
	}
	for _, bad := range []string{"", "sk-ant-oat01-short", " " + fakeOAT, fakeOAT + " x", "sk-ant-api03-" + strings.Repeat("a", 40)} {
		if validClaudeToken(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
	if got := tokenTail(fakeOAT); got != "…wAAA" {
		t.Errorf("tokenTail = %q", got)
	}
}

func TestScanSetupTokenOutput(t *testing.T) {
	// The terminal wrapped the token across two lines.
	out := "Opening browser to sign in…\nhttps://claude.ai/oauth/authorize?code=true&client_id=abc&state=xyz\n" +
		"Paste code here if prompted >\n" +
		"Your OAuth token (valid for 1 year):\n" + fakeOAT[:50] + "\n" + fakeOAT[50:] + "\n"
	token, url, wants := scanSetupTokenOutput(out)
	if token != fakeOAT {
		t.Errorf("token = %q", token)
	}
	if url != "https://claude.ai/oauth/authorize?code=true&client_id=abc&state=xyz" {
		t.Errorf("url = %q", url)
	}
	if !wants {
		t.Error("the paste-code prompt was not noticed")
	}
	if tok, _, w := scanSetupTokenOutput("Opening browser…\n"); tok != "" || w {
		t.Errorf("nothing yet: token=%q wants=%v", tok, w)
	}
}

func TestSaveClaudeTokenPersistsAndClearsTheFailure(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &Config{TelegramBotToken: "123:test", AllowedUserIDs: []int64{1}} // the smallest config that validates
	noteClaudeAuthFailure("401 Unauthorized: token expired")
	if st := buildClaudeAuthStatus(cfg); st.LastFailureAt == "" || st.TokenSet {
		t.Fatalf("before save: %+v", st)
	}

	var bad struct {
		OK    bool
		Error string
	}
	_ = json.Unmarshal(saveClaudeToken(cfgPath, cfg, "not-a-token"), &bad)
	if bad.OK || !strings.Contains(bad.Error, "sk-ant-oat01-") {
		t.Fatalf("a malformed token must be refused: %+v", bad)
	}

	var ok struct {
		OK    bool
		Error string
	}
	_ = json.Unmarshal(saveClaudeToken(cfgPath, cfg, "  "+fakeOAT+"\n"), &ok)
	if !ok.OK {
		t.Fatalf("save failed: %s", ok.Error)
	}
	raw, _ := os.ReadFile(cfgPath)
	saved, err := unmarshalConfigYAML(raw)
	if err != nil || saved.ClaudeOauthToken != fakeOAT {
		t.Fatalf("config.yaml does not hold the token: %v", err)
	}
	if st := buildClaudeAuthStatus(saved); !st.TokenSet || st.TokenTail != "…wAAA" || st.LastFailureAt != "" {
		t.Fatalf("after save: %+v", st)
	}
	// The status sent to the UI never carries the token itself.
	b, _ := json.Marshal(buildClaudeAuthStatus(saved))
	if strings.Contains(string(b), "AbCdEfGh") {
		t.Fatalf("status leaks the token: %s", b)
	}
}

func TestAuthJobRunsOneAtATime(t *testing.T) {
	j := &authJob{state: authJobState{Phase: "idle"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := j.begin("issue", "…", cancel); err != nil {
		t.Fatal(err)
	}
	if err := j.begin("verify", "…", cancel); err == nil {
		t.Fatal("a second job must not start while one runs")
	}
	j.stop()
	if ctx.Err() == nil {
		t.Fatal("stop must cancel the running job")
	}
	j.finish("done", "ok")
	if err := j.begin("verify", "…", func() {}); err != nil {
		t.Fatalf("a finished job must free the slot: %v", err)
	}
}

func TestSendCodeWithoutAJob(t *testing.T) {
	var r struct {
		OK    bool
		Error string
	}
	_ = json.Unmarshal(sendClaudeAuthCode("abc"), &r)
	if r.OK || !strings.Contains(r.Error, "발급 작업이 없습니다") {
		t.Fatalf("got %+v", r)
	}
}
