package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Claude CLI sign-in, managed from the desktop app's settings.
//
// Workers authenticate with CLAUDE_CODE_OAUTH_TOKEN, a one-year token
// `claude setup-token` issues (config: claude.oauth_token). When it expired the
// only way to fix it was to find a terminal, run setup-token, copy the token out
// and edit config.yaml by hand. These control verbs make it a couple of clicks:
//
//	claude_auth_status   token set? last auth failure? the running job's state
//	claude_auth_issue    run `claude setup-token` and save the token it prints
//	claude_auth_code     type the code the browser showed, if the CLI asks
//	claude_auth_save     save a pasted token
//	claude_auth_verify   check the token with a tiny real claude call
//	claude_auth_clear    remove it (fall back to claude's own login)
//	claude_auth_cancel   stop the running issue/verify job
//
// Issuing and verifying take far longer than a control request may (the user
// approves in a browser; a verify call is tens of seconds), so they run as one
// background job whose state the UI polls through claude_auth_status.

// oauthTokenRE matches a token as setup-token prints it.
var oauthTokenRE = regexp.MustCompile(`sk-ant-oat01-[A-Za-z0-9_-]{20,}`)

// validClaudeToken reports whether s looks like a setup-token token. Pasting is
// error-prone (a trailing space, half the token), and a wrong value would only
// show up as failing turns later.
func validClaudeToken(s string) bool {
	m := oauthTokenRE.FindString(s)
	return m != "" && m == s
}

// tokenTail shows just enough of a token to tell two apart: its last 4 chars.
func tokenTail(s string) string {
	if len(s) <= 4 {
		return ""
	}
	return "…" + s[len(s)-4:]
}

// ---- last auth failure ------------------------------------------------------

var authFailure struct {
	sync.Mutex
	at  time.Time
	msg string
}

// noteClaudeAuthFailure records that a worker turn failed to authenticate, so
// the settings screen can say the token has likely expired.
func noteClaudeAuthFailure(msg string) {
	authFailure.Lock()
	authFailure.at = time.Now()
	authFailure.msg = truncate(redactSecrets(strings.TrimSpace(msg)), 200)
	authFailure.Unlock()
}

func clearClaudeAuthFailure() {
	authFailure.Lock()
	authFailure.at, authFailure.msg = time.Time{}, ""
	authFailure.Unlock()
}

// ---- background job -----------------------------------------------------------

// authJobState is what the UI shows while issuing or verifying.
type authJobState struct {
	Kind    string `json:"kind,omitempty"`    // "issue" | "verify"
	Phase   string `json:"phase"`             // "idle" | "running" | "need_code" | "done" | "error"
	Message string `json:"message,omitempty"` // what to tell the user now
	URL     string `json:"url,omitempty"`     // sign-in page, if the browser did not open by itself
}

type authJob struct {
	mu     sync.Mutex
	state  authJobState
	cancel context.CancelFunc
	input  func(string) error // types into the running setup-token, when it asks for a code
}

var claudeAuthJob = &authJob{state: authJobState{Phase: "idle"}}

func (j *authJob) get() authJobState {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state
}

func (j *authJob) set(fn func(*authJobState)) {
	j.mu.Lock()
	fn(&j.state)
	j.mu.Unlock()
}

// begin claims the job slot for kind; only one issue/verify runs at a time.
func (j *authJob) begin(kind, msg string, cancel context.CancelFunc) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state.Phase == "running" || j.state.Phase == "need_code" {
		return fmt.Errorf("이미 진행 중인 작업이 있습니다 (%s)", j.state.Kind)
	}
	j.state = authJobState{Kind: kind, Phase: "running", Message: msg}
	j.cancel, j.input = cancel, nil
	return nil
}

func (j *authJob) finish(phase, msg string) {
	j.mu.Lock()
	j.state.Phase, j.state.Message, j.state.URL = phase, msg, ""
	j.cancel, j.input = nil, nil
	j.mu.Unlock()
}

func (j *authJob) stop() {
	j.mu.Lock()
	cancel := j.cancel
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ---- control verbs ------------------------------------------------------------

type claudeAuthStatus struct {
	TokenSet       bool         `json:"tokenSet"`
	TokenTail      string       `json:"tokenTail,omitempty"`
	LastFailureAt  string       `json:"lastFailureAt,omitempty"`
	LastFailureMsg string       `json:"lastFailureMsg,omitempty"`
	CanIssue       bool         `json:"canIssue"` // this host can run setup-token for the user
	Job            authJobState `json:"job"`
}

func buildClaudeAuthStatus(cfg *Config) claudeAuthStatus {
	st := claudeAuthStatus{
		TokenSet:  cfg.ClaudeOauthToken != "",
		TokenTail: tokenTail(cfg.ClaudeOauthToken),
		CanIssue:  claudeSetupTokenSupported,
		Job:       claudeAuthJob.get(),
	}
	authFailure.Lock()
	if !authFailure.at.IsZero() {
		st.LastFailureAt = authFailure.at.Format(time.RFC3339)
		st.LastFailureMsg = authFailure.msg
	}
	authFailure.Unlock()
	return st
}

func authReply(ok bool, msg string) json.RawMessage {
	if !ok {
		return settingsFail(msg)
	}
	d, _ := json.Marshal(map[string]any{"ok": true, "message": msg})
	return d
}

// saveClaudeToken writes token (or "" to remove it) to config.yaml through the
// same validated path as every other settings writer; it is live from the next
// turn on.
func saveClaudeToken(cfgPath string, cfg *Config, token string) json.RawMessage {
	token = strings.TrimSpace(token)
	if token != "" && !validClaudeToken(token) {
		return settingsFail("토큰 형식이 아닙니다 — sk-ant-oat01- 로 시작하는 값 전체를 붙여넣어 주세요.")
	}
	newCfg := *cfg
	newCfg.ClaudeOauthToken = token
	reply := persistConfig(cfgPath, &newCfg)
	var r struct{ OK bool }
	if json.Unmarshal(reply, &r) == nil && r.OK {
		clearClaudeAuthFailure()
		if token == "" {
			log.Printf("[claude-auth] token removed")
		} else {
			log.Printf("[claude-auth] token saved (%s)", tokenTail(token))
		}
	}
	return reply
}

// startClaudeVerify checks the configured token in the background.
func startClaudeVerify(cfg *Config) json.RawMessage {
	token := cfg.ClaudeOauthToken
	if token == "" {
		return settingsFail("저장된 토큰이 없습니다.")
	}
	claudePath, err := findClaude(cfg.ClaudePath)
	if err != nil {
		return settingsFail("claude CLI 를 찾지 못했습니다: " + err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	if err := claudeAuthJob.begin("verify", "Claude 에 짧은 요청을 보내 토큰을 확인하는 중…", cancel); err != nil {
		cancel()
		return settingsFail(err.Error())
	}
	go func() {
		defer cancel()
		if err := verifyClaudeTokenCtx(ctx, claudePath, token); err != nil {
			if ctx.Err() == context.Canceled {
				claudeAuthJob.finish("idle", "")
				return
			}
			claudeAuthJob.finish("error", "인증 실패 — 토큰이 만료됐거나 잘못됐습니다. '토큰 새로 받기'로 다시 발급해 주세요. ("+truncate(redactSecrets(err.Error()), 160)+")")
			return
		}
		clearClaudeAuthFailure()
		claudeAuthJob.finish("done", "토큰이 정상입니다 ✓")
	}()
	return authReply(true, "확인을 시작했습니다")
}

// startClaudeIssue runs `claude setup-token` and saves what it prints.
func startClaudeIssue(cfg *Config, save func(token string) error) json.RawMessage {
	if !claudeSetupTokenSupported {
		return settingsFail("이 기기에서는 자동 발급을 지원하지 않습니다. 다른 PC 의 터미널에서 claude setup-token 을 실행해 나온 토큰을 붙여넣어 주세요.")
	}
	claudePath, err := findClaude(cfg.ClaudePath)
	if err != nil {
		return settingsFail("claude CLI 를 찾지 못했습니다: " + err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), setupTokenTimeout)
	if err := claudeAuthJob.begin("issue", "브라우저에서 Claude 로그인을 승인해 주세요…", cancel); err != nil {
		cancel()
		return settingsFail(err.Error())
	}
	go func() {
		defer cancel()
		token, err := runSetupToken(ctx, claudePath, claudeAuthJob)
		switch {
		case ctx.Err() == context.Canceled:
			claudeAuthJob.finish("idle", "")
		case ctx.Err() == context.DeadlineExceeded:
			claudeAuthJob.finish("error", "시간이 초과됐습니다. 다시 시도해 주세요.")
		case err != nil:
			claudeAuthJob.finish("error", "발급하지 못했습니다: "+truncate(redactSecrets(err.Error()), 300))
		default:
			if serr := save(token); serr != nil {
				claudeAuthJob.finish("error", "발급은 됐지만 저장하지 못했습니다: "+serr.Error())
				return
			}
			claudeAuthJob.finish("done", "새 토큰("+tokenTail(token)+")을 저장했습니다 ✓ 다음 메시지부터 적용됩니다.")
		}
	}()
	return authReply(true, "발급을 시작했습니다")
}

// sendClaudeAuthCode types the code the browser showed into setup-token.
func sendClaudeAuthCode(code string) json.RawMessage {
	code = strings.TrimSpace(code)
	if code == "" {
		return settingsFail("코드를 입력해 주세요.")
	}
	claudeAuthJob.mu.Lock()
	input := claudeAuthJob.input
	claudeAuthJob.mu.Unlock()
	if input == nil {
		return settingsFail("코드를 기다리는 발급 작업이 없습니다.")
	}
	if err := input(code + "\r"); err != nil {
		return settingsFail("코드를 전달하지 못했습니다: " + err.Error())
	}
	claudeAuthJob.set(func(s *authJobState) {
		s.Phase = "running"
		s.Message = "코드를 확인하는 중…"
	})
	return authReply(true, "")
}

// setupTokenTimeout bounds one issue attempt: time to sign in and approve.
const setupTokenTimeout = 10 * time.Minute

// scanSetupTokenOutput reads what setup-token has printed so far (ANSI already
// stripped): the token once it is there, the sign-in URL, and whether it is
// asking for a code to be pasted.
func scanSetupTokenOutput(out string) (token, url string, wantsCode bool) {
	// A terminal wraps long lines; a token has no spaces, so joining the
	// wrapped pieces back is safe for the token search only.
	joined := strings.NewReplacer("\r", "", "\n", "").Replace(out)
	token = oauthTokenRE.FindString(joined)
	// The URL is searched line by line: joined, it would run on into the next
	// line's text.
	if m := setupURLRE.FindString(out); m != "" {
		url = m
	}
	lower := strings.ToLower(out)
	wantsCode = strings.Contains(lower, "paste code") || strings.Contains(lower, "paste the code")
	return token, url, wantsCode
}

var setupURLRE = regexp.MustCompile(`https://[A-Za-z0-9.-]*(claude\.ai|anthropic\.com)/[^\s"'<>]*oauth[^\s"'<>]*`)
