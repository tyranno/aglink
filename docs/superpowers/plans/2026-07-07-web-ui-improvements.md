# 웹 채팅 UI 개선 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** teleclaude 웹 UI에 버전 표시·`!update` 버튼·config.yaml 편집·연결/aglink 연동 상태 관리 패널을 추가하고, 작성창을 플로팅 버튼→모달로 바꾸며 대화 관리(이름변경/삭제/폴더) UX를 다듬는다.

**Architecture:** 관리 기능은 Teleclaude 내장 웹 서버([webchat.go](../../../webchat.go))의 신규 `/api/*` 엔드포인트로만 노출하고, 공유 `app.js`는 `GET /api/capabilities` 감지로 aglink-chat에서 관리 UI를 자동 숨긴다. 업데이트는 기존 `!update` 명령 경로를 그대로 타서 신규 백엔드가 없다. config 편집은 원문 YAML + 비밀값 마스킹으로 고정 `cfgPath`에만 기록 후 fsnotify 핫리로드. 두 `web/app.js`는 동일 자매 파일로 유지한다.

**Tech Stack:** Go 1.25, `github.com/coder/websocket`, `gopkg.in/yaml.v3`, `github.com/fsnotify/fsnotify`, go:embed, Vanilla JS (`Teleclaude/web/`, `../aglink-chat/web/`).

설계 문서: [specs/2026-07-07-web-ui-improvements-design.md](../specs/2026-07-07-web-ui-improvements-design.md)

## Global Constraints

- 각 단계 `go build ./... && go vet ./... && go test ./...` 그린 (Teleclaude, aglink-chat 각자). Windows gofmt는 CRLF 오탐 → `tr -d '\r' < <file> | gofmt -d`로 확인. 에디터/gopls 진단은 STALE — 실제 `go build`/`go test`만 신뢰.
- 모든 신규 HTTP 엔드포인트는 `s.authOK(r)`(origin 루프백 + 상수시간 토큰) 통과 필수. 미인증은 401.
- config 기록은 서버가 보유한 고정 `cfgPath`에만. 클라이언트는 경로를 절대 지정하지 않는다(임의 파일 쓰기 금지).
- 비밀값(`bot_token`/`oauth_token`/`web_chat.token`/`chat_control.token`)은 마스킹 전 절대 브라우저로 전송 금지. 저장 시 센티넬(`●●●…`)이 그대로면 기존 비밀 유지.
- `!update` 웹 트리거는 기존 명령 경로(`handleCommand`)를 그대로 사용 — 새 특권 경로를 만들지 않는다. 웹 버튼은 확인 다이얼로그 1단계만 추가.
- 두 `web/app.js`에 동일 로직 반영, `node --check` 양쪽 통과. `index.html`/`style.css`도 자매 파일로 수렴.
- push 금지(로컬 커밋). 커밋 trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.

---

## File Structure

- `version.go` (신규, Teleclaude) — `buildVersion`/`buildTime` 패키지 변수, `versionInfo()` 헬퍼.
- `bot.go` — `handleUpdate` 빌드 커맨드에 `-ldflags` 주입, `webDelete` 메서드.
- `webchat.go` — `webServer`에 `cfgPath`/`holder` 필드, 신규 핸들러 `handleCapabilities`/`handleVersion`/`handleStatus`/`handleConfig`, `inMsg`에 `web_delete`, 라우팅 등록.
- `configsecret.go` (신규, Teleclaude) — `maskConfigSecrets`/`restoreConfigSecrets`.
- `chatcontrol.go` — `connCount atomic.Int64`, `controlIn`에 `web_delete` 처리.
- `main.go` — `webServer` 생성부에 `cfgPath`/`holder` 주입.
- `../aglink-chat/control.go` — `controlIn`에 `Target`/`ID`/`Title` 필드.
- `../aglink-chat/server.go` — `browserMsg` 확장, handleWS 릴레이(`web_new`/`web_setdir`/`web_rename`/`web_delete`), `/api/history` 핸들러.
- `Teleclaude/web/{index.html,style.css,app.js}` + `../aglink-chat/web/{index.html,style.css,app.js}` — 동일 자매 파일. 플로팅 모달 작성창, 사이드바 아이콘, 대화 ⋯ 메뉴, capability 기반 관리 UI(버전 배지/업데이트/ config 패널/연결 패널).

---

## Task 1: 버전 인프라 + webServer 배선 + capabilities/version 엔드포인트

**Files:**
- Create: `version.go`, `version_test.go`
- Modify: `webchat.go` (webServer 필드 + 핸들러 + 라우팅), `main.go:390`, `bot.go:751` (ldflags)

**Interfaces:**
- Produces:
  - `var buildVersion = "dev"`, `var buildTime = ""` (main 패키지 전역).
  - `func versionInfo() (version, builtAt string)` — `buildVersion`, `buildTime` 반환.
  - `webServer.cfgPath string`, `webServer.holder *ConfigHolder` 필드.
  - `GET /api/capabilities` → `{"admin":true,"version":string,"buildTime":string}`.
  - `GET /api/version` → `{"version":string,"buildTime":string,"backend":string}`.

- [ ] **Step 1: 실패 테스트** — `version_test.go`

```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestWebServer(t *testing.T) *webServer {
	t.Helper()
	return &webServer{addr: "127.0.0.1:0", token: "tok"}
}

func authGet(t *testing.T, s *webServer, h http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

func TestHandleCapabilities_AdminTrue(t *testing.T) {
	s := newTestWebServer(t)
	rr := authGet(t, s, s.handleCapabilities, "/api/capabilities")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var body struct {
		Admin   bool   `json:"admin"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !body.Admin {
		t.Errorf("admin = false, want true")
	}
}

func TestHandleCapabilities_Unauthorized(t *testing.T) {
	s := newTestWebServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/capabilities", nil) // no token
	rr := httptest.NewRecorder()
	s.handleCapabilities(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestHandleVersion_ReportsBuildVars(t *testing.T) {
	s := newTestWebServer(t)
	rr := authGet(t, s, s.handleVersion, "/api/version")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var body struct{ Version string `json:"version"` }
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Version == "" {
		t.Errorf("version empty, want at least %q", "dev")
	}
}
```

- [ ] **Step 2: 실패 확인** — `go test -run 'TestHandleCapabilities|TestHandleVersion' . -v` → 미정의(컴파일 실패).

- [ ] **Step 3: `version.go` 생성**

```go
package main

// buildVersion / buildTime are injected at build time via -ldflags
// (-X main.buildVersion=... -X main.buildTime=...) by handleUpdate's go build.
// A plain `go build` (dev workflow) leaves the defaults.
var (
	buildVersion = "dev"
	buildTime    = ""
)

// versionInfo returns the running binary's version and build timestamp.
func versionInfo() (version, builtAt string) {
	return buildVersion, buildTime
}
```

- [ ] **Step 4: `webchat.go` webServer 필드 추가** — 기존 struct(97-103행)에 두 필드 추가:

```go
type webServer struct {
	addr        string
	token       string
	ownerChatID int64
	hub         *Hub
	bot         *Bot
	cfgPath     string         // config.yaml 경로 (편집 대상, 고정)
	holder      *ConfigHolder  // 현재 설정 (비밀값 복원/상태 조회용)
}
```

- [ ] **Step 5: `webchat.go` 핸들러 추가** — 파일 하단(handleUpload 뒤)에:

```go
func (s *webServer) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	v, bt := versionInfo()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"admin": true, "version": v, "buildTime": bt,
	})
}

func (s *webServer) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	v, bt := versionInfo()
	backend := ""
	if s.bot != nil && s.bot.manager != nil {
		backend = s.bot.manager.Backend()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version": v, "buildTime": bt, "backend": backend,
	})
}
```

- [ ] **Step 6: 라우팅 등록** — `webchat.go` `Start()`의 mux 블록(552행 부근)에 추가:

```go
	mux.HandleFunc("/api/capabilities", s.handleCapabilities)
	mux.HandleFunc("/api/version", s.handleVersion)
```

- [ ] **Step 7: 테스트 통과 확인** — `go test -run 'TestHandleCapabilities|TestHandleVersion' . -v` → PASS.

- [ ] **Step 8: main.go webServer 생성부에 주입** — main.go:390을 다음으로 교체:

```go
			ws := &webServer{addr: addr, token: tok, ownerChatID: owner, hub: bot.Hub(), bot: bot, cfgPath: cfgPath, holder: holder}
```

- [ ] **Step 9: `handleUpdate` 빌드에 ldflags 주입** — `bot.go`의 build 커맨드(751행) 앞에 커밋/시각을 계산하고 커맨드를 교체:

```go
	// Version stamp for the new binary. Best-effort: git 부재/실패 시 ldflags 생략(dev 유지).
	ver := buildStampVersion(srcDir)
	buildArgs := []string{"build", "-o", newExe}
	if ver != "" {
		buildArgs = append(buildArgs, "-ldflags", "-X main.buildVersion="+ver+" -X main.buildTime="+time.Now().UTC().Format(time.RFC3339))
	}
	buildArgs = append(buildArgs, ".")
	buildCmd := exec.CommandContext(buildCtx, "go", buildArgs...)
```

그리고 `bot.go` 하단(또는 `version.go`)에 헬퍼 추가:

```go
// buildStampVersion returns a short git commit (e.g. "a1b2c3d") for the source in
// srcDir, or "" when git is unavailable / not a repo — the build then omits ldflags
// and the binary keeps buildVersion="dev".
func buildStampVersion(srcDir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", srcDir, "rev-parse", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
```

> 참고: `bot.go`는 이미 `context`/`exec`/`strings`/`time`/`filepath` import 보유. `version.go`에 헬퍼를 둘 경우 해당 import 추가.

- [ ] **Step 10: 전체 빌드/벳/테스트** — `go build ./... && go vet ./... && go test ./...` → 그린.

- [ ] **Step 11: 커밋**

```bash
git add version.go version_test.go webchat.go main.go bot.go
git commit -m "feat(web): build-version stamping + /api/capabilities + /api/version"
```

---

## Task 2: config.yaml 조회/편집 (비밀 마스킹 + 핫리로드)

**Files:**
- Create: `configsecret.go`, `configsecret_test.go`
- Modify: `webchat.go` (handleConfig + 라우팅)

**Interfaces:**
- Consumes: `webServer.cfgPath`, `webServer.holder` (Task 1).
- Produces:
  - `func maskConfigSecrets(raw []byte, c *Config) []byte` — raw YAML의 비밀값 정확값을 `configSecretSentinel`로 치환.
  - `func restoreConfigSecrets(edited []byte, c *Config) []byte` — 센티넬을 기존 비밀값으로 역치환.
  - `const configSecretSentinel = "●●●●●●●● (unchanged)"`.
  - `GET /api/config` → `text/plain` (마스킹된 원문). `PUT /api/config` → 본문 원문 검증·복원·기록, 성공 204 / 실패 400.

- [ ] **Step 1: 실패 테스트** — `configsecret_test.go`

```go
package main

import (
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
```

- [ ] **Step 2: 실패 확인** — `go test -run 'TestMask|TestRestore' . -v` → 미정의.

- [ ] **Step 3: `configsecret.go` 생성**

```go
package main

import "strings"

// configSecretSentinel replaces every non-empty secret value when config.yaml is
// sent to the browser, and is swapped back to the stored secret on save. Exact
// string replacement (not YAML-structural) so comments/formatting survive.
const configSecretSentinel = "●●●●●●●● (unchanged)"

// configSecrets returns the current non-empty secret values (longest first so a
// secret that is a substring of another is replaced correctly).
func configSecrets(c *Config) []string {
	if c == nil {
		return nil
	}
	cand := []string{c.TelegramBotToken, c.ClaudeOauthToken, c.WebChatToken, c.ChatControlToken}
	out := make([]string, 0, len(cand))
	seen := map[string]bool{}
	for _, s := range cand {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	// longest first
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if len(out[j]) > len(out[i]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func maskConfigSecrets(raw []byte, c *Config) []byte {
	s := string(raw)
	for _, secret := range configSecrets(c) {
		s = strings.ReplaceAll(s, secret, configSecretSentinel)
	}
	return []byte(s)
}

func restoreConfigSecrets(edited []byte, c *Config) []byte {
	s := string(edited)
	secrets := configSecrets(c)
	// Restore in a fixed order. Only one sentinel value exists, so replace each
	// sentinel occurrence with the secret whose field it belongs to. Because we
	// masked longest-first, restoring pairs 1:1 by the field order used at mask
	// time is only safe when secrets are distinct; here every sentinel maps back
	// to the SAME literal, so we restore per-line by matching the yaml key.
	if !strings.Contains(s, configSecretSentinel) {
		return []byte(s)
	}
	// Per-key restore keeps correctness when multiple secrets share the sentinel.
	byKey := map[string]string{
		"bot_token":   c.TelegramBotToken,
		"oauth_token": c.ClaudeOauthToken,
		"token":       "", // ambiguous (web_chat/chat_control) — resolved below
	}
	_ = secrets
	lines := strings.Split(s, "\n")
	section := ""
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "web_chat:" {
			section = "web_chat"
		} else if trimmed == "chat_control:" {
			section = "chat_control"
		} else if trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(ln, " ") {
			section = "" // left the indented block
		}
		if !strings.Contains(ln, configSecretSentinel) {
			continue
		}
		key := ""
		if idx := strings.Index(trimmed, ":"); idx > 0 {
			key = strings.TrimSpace(trimmed[:idx])
		}
		repl := byKey[key]
		if key == "token" {
			if section == "chat_control" {
				repl = c.ChatControlToken
			} else {
				repl = c.WebChatToken
			}
		}
		if repl != "" {
			lines[i] = strings.ReplaceAll(ln, configSecretSentinel, repl)
		}
	}
	return []byte(strings.Join(lines, "\n"))
}
```

> 참고: 위 `TestMaskAndRestore_RoundTrip`은 web_chat/chat_control 섹션 헤더가 있는 raw를 사용하므로 per-key 복원이 정확히 동작한다. 실제 config.yaml도 `marshalConfigYAML`이 항상 섹션 헤더를 포함한다.

- [ ] **Step 4: 테스트 통과** — `go test -run 'TestMask|TestRestore' . -v` → PASS.

- [ ] **Step 5: handleConfig 테스트 추가** — `configsecret_test.go`에:

```go
import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
)

func TestHandleConfig_GetMasksAndPutValidates(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &Config{TelegramBotToken: "S3CRET", WebChatAddr: "127.0.0.1:1717", ChatControlAddr: "127.0.0.1:17170"}
	raw, err := marshalConfigYAML(cfg)
	if err != nil { t.Fatalf("marshal: %v", err) }
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil { t.Fatalf("write: %v", err) }

	s := &webServer{token: "tok", cfgPath: cfgPath, holder: NewConfigHolder(cfg)}

	// GET masks the secret
	getReq := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	getReq.Header.Set("Authorization", "Bearer tok")
	getRR := httptest.NewRecorder()
	s.handleConfig(getRR, getReq)
	if getRR.Code != http.StatusOK { t.Fatalf("GET status %d", getRR.Code) }
	if strings.Contains(getRR.Body.String(), "S3CRET") { t.Errorf("GET leaked secret") }

	// PUT invalid YAML → 400, file unchanged
	badReq := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader("::: not yaml :::"))
	badReq.Header.Set("Authorization", "Bearer tok")
	badRR := httptest.NewRecorder()
	s.handleConfig(badRR, badReq)
	if badRR.Code != http.StatusBadRequest { t.Errorf("bad PUT status = %d, want 400", badRR.Code) }
	after, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(after), "not yaml") { t.Errorf("invalid config was written") }
}
```

- [ ] **Step 6: `webchat.go` handleConfig 구현** — 하단에 추가:

```go
func (s *webServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.cfgPath == "" || s.holder == nil {
		http.Error(w, "config editing unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		raw, err := os.ReadFile(s.cfgPath)
		if err != nil {
			http.Error(w, "read failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(maskConfigSecrets(raw, s.holder.Get()))
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		restored := restoreConfigSecrets(body, s.holder.Get())
		if _, verr := unmarshalConfigYAML(restored); verr != nil {
			http.Error(w, verr.Error(), http.StatusBadRequest)
			return
		}
		if werr := os.WriteFile(s.cfgPath, restored, 0o600); werr != nil {
			http.Error(w, "write failed: "+werr.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent) // fsnotify(confighot.go)가 핫리로드
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
```

- [ ] **Step 7: 라우팅 등록** — `Start()` mux에 `mux.HandleFunc("/api/config", s.handleConfig)`.

- [ ] **Step 8: 테스트 통과** — `go test -run 'TestHandleConfig' . -v` → PASS.

- [ ] **Step 9: 전체** — `go build ./... && go vet ./... && go test ./...` → 그린.

- [ ] **Step 10: 커밋**

```bash
git add configsecret.go configsecret_test.go webchat.go
git commit -m "feat(web): config.yaml view/edit with secret masking + validated write"
```

---

## Task 3: 연결/aglink 연동 상태 (/api/status + 연결 카운터)

**Files:**
- Modify: `chatcontrol.go` (connCount), `webchat.go` (handleStatus + 라우팅)
- Test: `chatcontrol_test.go` 또는 신규 `status_test.go`

**Interfaces:**
- Consumes: `webServer.holder`.
- Produces:
  - `chatControlServer.connCount atomic.Int64` + register/unregister시 증감.
  - `GET /api/status` → `{"webChatAddr","chatControlEnabled","chatControlAddr","aglinkConnected":bool,"aglinkClients":int}`.
  - `webServer.control *chatControlServer` (nil 가능) — status에서 카운터 읽기용.

- [ ] **Step 1: 실패 테스트** — `status_test.go`

```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleStatus_ReportsConfigAndConnections(t *testing.T) {
	cfg := &Config{WebChatAddr: "127.0.0.1:1717", ChatControl: true, ChatControlAddr: "127.0.0.1:17170"}
	cs := &chatControlServer{}
	cs.connCount.Add(1) // one aglink-chat connected
	s := &webServer{token: "tok", holder: NewConfigHolder(cfg), control: cs}

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	s.handleStatus(rr, req)
	if rr.Code != http.StatusOK { t.Fatalf("status %d", rr.Code) }

	var body struct {
		WebChatAddr    string `json:"webChatAddr"`
		AglinkClients  int    `json:"aglinkClients"`
		AglinkConnected bool  `json:"aglinkConnected"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil { t.Fatalf("unmarshal: %v", err) }
	if body.WebChatAddr != "127.0.0.1:1717" { t.Errorf("addr = %q", body.WebChatAddr) }
	if body.AglinkClients != 1 || !body.AglinkConnected { t.Errorf("clients=%d connected=%v", body.AglinkClients, body.AglinkConnected) }
}
```

- [ ] **Step 2: 실패 확인** — `go test -run TestHandleStatus . -v` → 미정의.

- [ ] **Step 3: `chatcontrol.go` 카운터 추가** — struct에 필드, handleControl에 증감. import에 `sync/atomic` 추가:

```go
type chatControlServer struct {
	addr        string
	token       string
	ownerChatID int64
	hub         *Hub
	bot         *Bot
	connCount   atomic.Int64
}
```

`handleControl`에서 Accept 성공 직후(로그 "connected" 부근):

```go
	s.connCount.Add(1)
	defer s.connCount.Add(-1)
	log.Printf("[chatcontrol] aglink-chat connected")
```

- [ ] **Step 4: `webchat.go` webServer.control 필드 + handleStatus** — struct에 `control *chatControlServer` 추가, 핸들러:

```go
func (s *webServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	cfg := (*Config)(nil)
	if s.holder != nil {
		cfg = s.holder.Get()
	}
	clients := 0
	if s.control != nil {
		clients = int(s.control.connCount.Load())
	}
	resp := map[string]any{"aglinkClients": clients, "aglinkConnected": clients > 0}
	if cfg != nil {
		resp["webChatAddr"] = cfg.WebChatAddr
		resp["chatControlEnabled"] = cfg.ChatControl
		resp["chatControlAddr"] = cfg.ChatControlAddr
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(resp)
}
```

- [ ] **Step 5: 라우팅 + main.go 배선** — `Start()` mux에 `mux.HandleFunc("/api/status", s.handleStatus)`. main.go에서 webServer가 chatControlServer를 참조하려면 chat_control 블록을 web_chat 블록보다 먼저 만들거나, 공유 포인터를 준비해야 한다. **단순화:** main.go에서 `cs`를 web_chat 블록보다 위에서 생성(활성 시)하고, `ws.control = cs`로 연결. chat_control 비활성 시 `ws.control = nil`.

  main.go 수정: chat_control 서버 생성 블록(398-412)을 web_chat 블록(379-393) **앞으로** 이동하고, `cs`를 변수로 보관 후 web_chat 생성 시 주입:

```go
	var chatCtl *chatControlServer
	if cfg.ChatControl {
		// ... 기존 토큰/owner 처리 ...
		chatCtl = &chatControlServer{addr: addr, token: tok, ownerChatID: owner, hub: bot.Hub(), bot: bot}
		go chatCtl.Start()
	}
	if cfg.WebChat {
		// ... 기존 처리 ...
		ws := &webServer{addr: addr, token: tok, ownerChatID: owner, hub: bot.Hub(), bot: bot, cfgPath: cfgPath, holder: holder, control: chatCtl}
		go ws.Start()
	}
```

- [ ] **Step 6: 테스트 통과** — `go test -run TestHandleStatus . -v` → PASS.

- [ ] **Step 7: 전체** — `go build ./... && go vet ./... && go test ./...` → 그린.

- [ ] **Step 8: 커밋**

```bash
git add chatcontrol.go webchat.go main.go status_test.go
git commit -m "feat(web): /api/status with aglink-chat connection counter"
```

---

## Task 4: 대화 삭제 (web_delete) — Teleclaude 백엔드

**Files:**
- Modify: `bot.go` (webDelete), `webchat.go` (inMsg + WS 라우팅), `chatcontrol.go` (controlIn 처리)
- Test: `webchat_test.go` 또는 신규 `webdelete_test.go`

**Interfaces:**
- Consumes: `store.DeleteWebConv(id)` (기존).
- Produces:
  - `func (b *Bot) webDelete(chatID int64, id string)` — 대화 삭제 + 알림.
  - `inMsg.Type == "web_delete"` (webchat.go WS) → `b.webDelete`.
  - `controlIn.Type == "web_delete"` (chatcontrol.go) → `b.webDelete`.

- [ ] **Step 1: 실패 테스트** — `webdelete_test.go` (기존 store 테스트 패턴 참고)

```go
package main

import "testing"

func TestWebDelete_RemovesConversation(t *testing.T) {
	store := newTestStore(t) // 기존 헬퍼 (store_test.go 참고; 없으면 아래 주석 참고)
	c, err := store.NewWebConv("temp")
	if err != nil { t.Fatalf("new: %v", err) }
	b := &Bot{store: store, sendHook: func(int64, string) {}} // sendHook로 Send 무력화

	b.webDelete(1, c.ID)

	if _, ok := store.GetWebConv(c.ID); ok {
		t.Errorf("conversation %s still present after webDelete", c.ID)
	}
}
```

> `newTestStore`/`sendHook`가 없으면 기존 `store_test.go`/`bot_test.go`의 테스트 스토어·Send 무력화 관용구를 그대로 사용한다. (Bot의 Send는 `b.commandHook`/채널 주입 패턴을 따름 — 기존 테스트 확인 후 맞춘다.)

- [ ] **Step 2: 실패 확인** — `go test -run TestWebDelete . -v` → 미정의.

- [ ] **Step 3: `bot.go` webDelete 추가** — webRename(1898행) 뒤:

```go
// webDelete removes a top-level web conversation. store.DeleteWebConv already
// clears the active pointer when it referenced the deleted conversation.
func (b *Bot) webDelete(chatID int64, id string) {
	if _, ok := b.store.GetWebConv(id); !ok {
		_ = b.Send(chatID, "웹 대화를 찾을 수 없습니다.")
		return
	}
	if err := b.store.DeleteWebConv(id); err != nil {
		_ = b.Send(chatID, "⚠️ 삭제 실패: "+err.Error())
		return
	}
	_ = b.Send(chatID, "🗑️ 대화가 삭제되었습니다.")
}
```

- [ ] **Step 4: `webchat.go` WS 라우팅** — `inMsg.Type` 주석에 `web_delete` 추가(114행 주석), reader switch(290-299행)에:

```go
		case "web_delete":
			go s.bot.webDelete(s.ownerChatID, m.ID)
```

- [ ] **Step 5: `chatcontrol.go` 라우팅** — `controlIn.Type` 주석 갱신, `handleInbound` switch(198-203행 부근)에:

```go
	case "web_delete":
		go s.bot.webDelete(chatID, m.ID)
```

- [ ] **Step 6: 테스트 통과** — `go test -run TestWebDelete . -v` → PASS.

- [ ] **Step 7: 전체** — `go build ./... && go vet ./... && go test ./...` → 그린.

- [ ] **Step 8: 커밋**

```bash
git add bot.go webchat.go chatcontrol.go webdelete_test.go
git commit -m "feat(web): web_delete conversation op (embedded + control API)"
```

---

## Task 5: aglink-chat 릴레이 파리티 (web_* + /api/history)

**Files:**
- Modify: `../aglink-chat/control.go` (controlIn 필드), `../aglink-chat/server.go` (browserMsg + handleWS + handleHistory)

**Interfaces:**
- Produces (aglink-chat):
  - `controlIn`에 `Path`(기존), `ID string`, `Title string`, `Target json.RawMessage` 필드.
  - `browserMsg`에 `ID`/`Title`/`Path`/`Target` 필드.
  - `server.go` handleWS: `web_new`/`web_setdir`/`web_rename`/`web_delete`를 control API로 릴레이.
  - `GET /api/history` → control `get_history` 요청 결과 프록시.

- [ ] **Step 1: aglink-chat controlIn 확장** — `../aglink-chat/control.go` controlIn(35-44행)에 필드 추가:

```go
type controlIn struct {
	Type    string          `json:"type"`
	ReqID   string          `json:"reqID,omitempty"`
	ChatID  int64           `json:"chatID,omitempty"`
	Text    string          `json:"text,omitempty"`
	Origin  string          `json:"origin,omitempty"`
	Path    string          `json:"path,omitempty"`
	Caption string          `json:"caption,omitempty"`
	ID      string          `json:"id,omitempty"`
	Title   string          `json:"title,omitempty"`
	Target  json.RawMessage `json:"target,omitempty"`
}
```

- [ ] **Step 2: aglink-chat browserMsg 확장 + handleWS 릴레이** — `../aglink-chat/server.go` browserMsg(78-81행) 및 handleWS reader(145-154행):

```go
type browserMsg struct {
	Type   string          `json:"type"`
	Text   string          `json:"text"`
	ID     string          `json:"id,omitempty"`
	Title  string          `json:"title,omitempty"`
	Path   string          `json:"path,omitempty"`
	Target json.RawMessage `json:"target,omitempty"`
}
```

handleWS reader loop 교체:

```go
	for {
		var m browserMsg
		if rerr := wsjson.Read(ctx, c, &m); rerr != nil {
			break
		}
		switch m.Type {
		case "send":
			s.forwardSend(m.Text) // 기존 (target은 send_text에 미전달 — 아래 참고)
		case "web_new":
			_ = s.control.send(controlIn{Type: "web_new", Title: m.Title, Origin: "web"})
		case "web_setdir":
			_ = s.control.send(controlIn{Type: "web_setdir", ID: m.ID, Path: m.Path, Origin: "web"})
		case "web_rename":
			_ = s.control.send(controlIn{Type: "web_rename", ID: m.ID, Title: m.Title, Origin: "web"})
		case "web_delete":
			_ = s.control.send(controlIn{Type: "web_delete", ID: m.ID, Origin: "web"})
		}
	}
```

> 참고(설계 노트): 현 aglink-chat `forwardSend`는 `target`을 control로 전달하지 않아 웹 대화 타겟팅이 telegram 기본으로 동작한다. 이는 기존 동작이며 본 계획의 파리티 목표(web_* 릴레이 + history)에는 영향 없다. target 전달까지 맞추려면 `forwardSend(text, target)`로 확장하고 controlIn.Target을 채운다 — 선택 개선(YAGNI: 이번 커밋 범위 밖, 별도 노트만).

- [ ] **Step 3: aglink-chat /api/history 프록시** — server.go에 핸들러 추가 + 라우팅:

```go
func (s *browserServer) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "telegram"
	}
	tgt, _ := json.Marshal(map[string]string{
		"kind": kind, "project": r.URL.Query().Get("project"), "id": r.URL.Query().Get("id"),
	})
	data, err := s.control.request(controlIn{Type: "get_history", Target: json.RawMessage(tgt)})
	if err != nil {
		http.Error(w, "control API error", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(data)
}
```

`Start()` mux(216-220행)에 `mux.HandleFunc("/api/history", s.handleHistory)` 추가. server.go에 `encoding/json` import 확인/추가.

> 참고: control API의 `controlIn.Target`은 `*Target`(구조체)인데 aglink-chat은 `json.RawMessage`로 보낸다. Teleclaude `wsjson.Read`가 controlIn을 언마샬할 때 `Target *Target`로 파싱되므로 RawMessage(JSON object)가 그대로 구조체로 언마샬된다 — 호환됨. (kind/project/id 필드명 일치.)

- [ ] **Step 4: 빌드** — aglink-chat에서 `go build ./... && go vet ./...` → 그린. (aglink-chat 테스트가 있으면 `go test ./...`.)

- [ ] **Step 5: 커밋 (aglink-chat 저장소)**

```bash
cd ../aglink-chat
git add control.go server.go
git commit -m "feat: relay web_new/setdir/rename/delete + /api/history to control API"
```

---

## Task 6: 공유 UI 골격 — 플로팅 모달 작성창 + 사이드바 아이콘 (index.html/style.css/app.js ×2)

**Files:**
- Modify: `Teleclaude/web/index.html`, `Teleclaude/web/style.css`, `Teleclaude/web/app.js` + `../aglink-chat/web/{index.html,style.css,app.js}` (동일 자매)

**Interfaces:**
- Produces (app.js):
  - `openComposer()` / `closeComposer()` — 모달 표시/숨김, 열릴 때 `input` 포커스.
  - 플로팅 버튼 `#compose-fab` 클릭 → openComposer. 모달 배경/Esc → closeComposer. 전송 성공 시 closeComposer.

- [ ] **Step 1: index.html 구조 변경 (두 파일 동일)** — `#composer`를 모달 컨테이너로 감싸고 플로팅 버튼 추가. `<section id="chat-panel">` 내부를 다음 구조로:

```html
    <section id="chat-panel">
      <div id="current-topic" class="current-topic" title="현재 보고 있는 대화">
        <span id="current-project" class="current-project"></span>
        <span id="current-title" class="current-title empty">대화 미선택</span>
        <span id="current-id" class="current-id"></span>
      </div>
      <main id="log"></main>
      <div id="working" hidden>
        <span class="dot"></span><span class="dot"></span><span class="dot"></span>
        <span id="working-label">작업 진행 중…</span>
      </div>
      <button type="button" id="compose-fab" class="fab" title="메시지 작성" aria-label="메시지 작성">✎</button>
      <div id="composer-overlay" class="overlay" hidden>
        <form id="composer" class="composer-modal">
          <div class="composer-head">
            <span>메시지 작성</span>
            <button type="button" id="composer-close" class="icon-button" title="닫기" aria-label="닫기">✕</button>
          </div>
          <div class="composer-row">
            <button type="button" id="attach-btn" class="icon-button" title="파일 첨부" aria-label="파일 첨부">📎</button>
            <input id="file" type="file" hidden />
            <span id="file-name" hidden></span>
            <textarea id="input" rows="3" autocomplete="off" spellcheck="false" placeholder="메시지 또는 !명령…"></textarea>
            <button type="submit" id="send-btn" class="icon-button" title="전송" aria-label="전송">➤</button>
          </div>
        </form>
      </div>
    </section>
```

> 사이드바 `#topics-head` 아이콘 버튼(＋/↻)은 유지. (Task 8에서 관리 컨트롤이 헤더에 추가됨.)

- [ ] **Step 2: style.css 추가 (두 파일 동일)** — 기존 `#composer` 규칙을 모달용으로 교체/추가:

```css
.fab { position: absolute; right: 18px; bottom: 18px; width: 52px; height: 52px; border-radius: 50%; font-size: 22px; background: #2563eb; color: #fff; box-shadow: 0 4px 12px rgba(37,99,235,0.4); border: 0; cursor: pointer; z-index: 5; }
.fab:hover { background: #1d4ed8; }
.overlay { position: fixed; inset: 0; background: rgba(15,23,42,0.35); display: flex; align-items: flex-end; justify-content: center; z-index: 20; padding: 16px; }
.overlay[hidden] { display: none; }
.composer-modal { width: min(720px, 100%); background: #fff; border-radius: 12px; box-shadow: 0 10px 40px rgba(15,23,42,0.3); padding: 12px; display: flex; flex-direction: column; gap: 8px; }
.composer-head { display: flex; align-items: center; justify-content: space-between; font-weight: 700; color: #334155; }
.composer-row { display: flex; align-items: flex-end; gap: 6px; }
.composer-row #input { flex: 1; min-height: calc(var(--input-line-height) * 3 + 16px); max-height: calc(var(--input-line-height) * 12 + 16px); line-height: var(--input-line-height); padding: 7px 8px; border: 1px solid #cbd5e1; border-radius: 6px; resize: none; overflow-y: auto; font: inherit; background: #fff; }
@media (max-width: 760px) { .overlay { align-items: stretch; } .composer-modal { align-self: flex-end; } }
```

`#chat-panel { position: relative; }` 추가(FAB 앵커). 기존 `#composer { ... border-top ... }` 규칙은 제거(모달로 대체).

- [ ] **Step 3: app.js — 모달 제어 추가 (두 파일 동일)** — 엘리먼트 조회부에 추가:

```js
  const composerFab = document.getElementById("compose-fab");
  const composerOverlay = document.getElementById("composer-overlay");
  const composerClose = document.getElementById("composer-close");

  function openComposer() {
    if (!composerOverlay) return;
    composerOverlay.hidden = false;
    if (input) { input.focus(); resizeInput(); }
  }
  function closeComposer() {
    if (!composerOverlay) return;
    composerOverlay.hidden = true;
  }
  if (composerFab) composerFab.addEventListener("click", openComposer);
  if (composerClose) composerClose.addEventListener("click", closeComposer);
  if (composerOverlay) composerOverlay.addEventListener("click", (e) => {
    if (e.target === composerOverlay) closeComposer();
  });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeComposer(); });
```

- [ ] **Step 4: app.js — 전송 성공 시 모달 닫기** — form submit 핸들러에서 업로드/텍스트 전송 성공 경로 끝에 `closeComposer();` 추가(파일 업로드 return 직전, 텍스트 sendText 성공 블록 내).

- [ ] **Step 5: node --check 양쪽** — `node --check Teleclaude/web/app.js && node --check ../aglink-chat/web/app.js` → 오류 없음.

- [ ] **Step 6: 커밋 (양 저장소)** — 두 저장소 각각:

```bash
# Teleclaude
git add web/index.html web/style.css web/app.js
git commit -m "feat(web): floating button + modal composer, sidebar polish"
# aglink-chat
cd ../aglink-chat && git add web/index.html web/style.css web/app.js
git commit -m "feat(web): floating button + modal composer, sidebar polish"
```

---

## Task 7: 대화 관리 ⋯ 메뉴 (이름변경/폴더/삭제) — app.js ×2

**Files:**
- Modify: `Teleclaude/web/app.js`, `Teleclaude/web/style.css` + aglink-chat 동일

**Interfaces:**
- Consumes: WS `web_rename`/`web_setdir`/`web_delete` (Task 4/5).
- Produces: `makeWebConvButton`의 ⚙ 단일 아이콘을 `⋯` 메뉴로 교체 (rename/workdir/delete 액션).

- [ ] **Step 1: app.js — ⋯ 메뉴 구현 (두 파일 동일)** — `makeWebConvButton`의 gear 블록을 다음으로 교체:

```js
    const menu = document.createElement("span");
    menu.textContent = "⋯";
    menu.dataset.gear = "1";
    menu.className = "topic-menu";
    menu.title = "대화 관리";
    menu.addEventListener("click", (ev) => {
      ev.stopPropagation();
      const action = window.prompt(
        "대화 관리 — 번호 입력:\n1) 이름 변경\n2) 작업 폴더 변경\n3) 삭제", "1");
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      if (action === "1") {
        const title = window.prompt("새 이름:", wc.title || "");
        if (title) { ws.send(JSON.stringify({ type: "web_rename", id: wc.id, title })); window.setTimeout(loadConversations, 400); }
      } else if (action === "2") {
        const path = window.prompt("이 대화의 작업 폴더 경로:", wc.workDir || "");
        if (path) { ws.send(JSON.stringify({ type: "web_setdir", id: wc.id, path })); window.setTimeout(loadConversations, 400); }
      } else if (action === "3") {
        if (window.confirm("이 대화를 삭제할까요? 되돌릴 수 없습니다.")) {
          if (currentTarget && currentTarget.kind === "web" && currentTarget.id === wc.id) { currentTarget = null; log.replaceChildren(); }
          ws.send(JSON.stringify({ type: "web_delete", id: wc.id }));
          window.setTimeout(loadConversations, 400);
        }
      }
    });
    button.appendChild(menu);
```

> 설계 노트: 3-액션 메뉴는 `prompt` 기반(현재 코드 관용구와 일관). 팝오버 UI는 YAGNI로 보류. 삭제된 대화가 현재 타겟이면 타겟 해제 + 로그 클리어.

- [ ] **Step 2: style.css — .topic-menu (두 파일 동일)**

```css
.topic-menu { margin-left: 6px; cursor: pointer; color: #64748b; font-weight: 700; padding: 0 4px; border-radius: 4px; }
.topic-menu:hover { background: #cbd5e1; color: #0f172a; }
```

- [ ] **Step 3: node --check 양쪽** → 오류 없음.

- [ ] **Step 4: 커밋 (양 저장소)** — 각각 `feat(web): conversation ⋯ menu (rename/workdir/delete)`.

---

## Task 8: 관리 UI (capability 감지 + 버전 배지 + 업데이트 + config/연결 패널) — app.js ×2

**Files:**
- Modify: `Teleclaude/web/index.html`, `Teleclaude/web/style.css`, `Teleclaude/web/app.js` + aglink-chat 동일

**Interfaces:**
- Consumes: `/api/capabilities`, `/api/version`, `/api/status`, `/api/config` (Teleclaude 전용; aglink-chat은 404 → 숨김).
- Produces: `bootstrapCapabilities()` — admin이면 헤더 관리 컨트롤/패널 표시. 아니면 계속 숨김.

- [ ] **Step 1: index.html — 관리 마크업 (두 파일 동일, 기본 hidden)** — `<header>`에 우측 관리 컨트롤 추가, `<body>` 끝(script 앞)에 패널:

```html
  <header>
    <button id="toggle-sidebar" class="icon-button" type="button" title="대화 목록 표시/숨김" aria-label="대화 목록 표시/숨김" aria-pressed="false">☰</button>
    <span id="status" class="off">연결 끊김</span> teleclaude web chat
    <span id="admin-controls" hidden>
      <span id="version-badge" class="version-badge" title="실행 중인 버전"></span>
      <button id="btn-config" class="icon-button" type="button" title="설정 파일 편집" aria-label="설정 파일 편집">⚙</button>
      <button id="btn-connections" class="icon-button" type="button" title="연결/연동 상태" aria-label="연결 상태">🔌</button>
      <button id="btn-update" class="icon-button" type="button" title="새 버전 빌드 & 재시작" aria-label="업데이트">⟳</button>
    </span>
  </header>
  ...
  <div id="config-overlay" class="overlay" hidden>
    <div class="panel">
      <div class="panel-head"><span>config.yaml</span><button id="config-close" class="icon-button">✕</button></div>
      <textarea id="config-text" spellcheck="false"></textarea>
      <div class="panel-foot"><span id="config-msg"></span><button id="config-save">저장 (핫리로드)</button></div>
    </div>
  </div>
  <div id="conn-overlay" class="overlay" hidden>
    <div class="panel">
      <div class="panel-head"><span>연결 / aglink 연동</span><button id="conn-close" class="icon-button">✕</button></div>
      <div id="conn-body" class="conn-body"></div>
    </div>
  </div>
```

`#admin-controls`는 `margin-left:auto`로 우측 정렬(아래 CSS).

- [ ] **Step 2: style.css — 관리 스타일 (두 파일 동일)**

```css
#admin-controls { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; }
#admin-controls[hidden] { display: none; }
.version-badge { font-size: 11px; padding: 2px 8px; border-radius: 999px; background: rgba(255,255,255,0.18); color: #e2e8f0; font-weight: 600; }
.panel { width: min(820px, 100%); max-height: 82vh; background: #fff; border-radius: 12px; box-shadow: 0 10px 40px rgba(15,23,42,0.3); display: flex; flex-direction: column; gap: 8px; padding: 12px; }
.panel-head, .panel-foot { display: flex; align-items: center; justify-content: space-between; font-weight: 700; color: #334155; }
.panel-foot { font-weight: 400; }
#config-text { width: 100%; flex: 1; min-height: 320px; font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 12px; border: 1px solid #cbd5e1; border-radius: 6px; padding: 8px; resize: vertical; }
#config-msg { font-size: 12px; color: #b45309; }
.conn-body { font-size: 13px; display: flex; flex-direction: column; gap: 6px; }
.conn-row { display: flex; justify-content: space-between; gap: 12px; }
.conn-row .k { color: #475569; }
.conn-ok { color: #16a34a; font-weight: 700; }
.conn-off { color: #dc2626; font-weight: 700; }
.overlay .panel { align-self: center; }
.overlay { align-items: center; }
```

> 주의: Task 6에서 `.overlay`를 `align-items: flex-end`로 뒀다면, 관리 패널 오버레이는 중앙정렬이 낫다. 위 `.overlay { align-items: center; }`가 뒤에 와서 전역 중앙정렬이 되며, 작성창 모달도 중앙/하단 어디든 무방하다. (모바일 미디어쿼리는 유지.)

- [ ] **Step 3: app.js — bootstrapCapabilities + 패널 로직 (두 파일 동일)** — IIFE 내에 추가하고 초기화부에서 호출:

```js
  const adminControls = document.getElementById("admin-controls");
  const versionBadge = document.getElementById("version-badge");
  const btnConfig = document.getElementById("btn-config");
  const btnConnections = document.getElementById("btn-connections");
  const btnUpdate = document.getElementById("btn-update");
  const configOverlay = document.getElementById("config-overlay");
  const configText = document.getElementById("config-text");
  const configMsg = document.getElementById("config-msg");
  const connOverlay = document.getElementById("conn-overlay");
  const connBody = document.getElementById("conn-body");
  const authHeaders = { Authorization: "Bearer " + token };

  async function bootstrapCapabilities() {
    try {
      const resp = await fetch("/api/capabilities", { headers: authHeaders });
      if (!resp.ok) return; // aglink-chat: 404 → 관리 UI 숨김 유지
      const cap = await resp.json();
      if (!cap.admin) return;
      if (adminControls) adminControls.hidden = false;
      if (versionBadge) versionBadge.textContent = "v " + (cap.version || "?");
    } catch (e) { /* 관리 UI 숨김 유지 */ }
  }

  // Update
  if (btnUpdate) btnUpdate.addEventListener("click", () => {
    if (!window.confirm("새 버전을 빌드하고 재시작할까요? 진행 중 작업이 없어야 합니다.")) return;
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "send", text: "!update", target: currentTarget || { kind: "telegram" } }));
      add("system", "업데이트를 요청했습니다. 빌드/재시작 로그가 곧 표시됩니다.");
    }
  });

  // Config panel
  async function openConfig() {
    if (!configOverlay) return;
    if (configMsg) configMsg.textContent = "";
    try {
      const resp = await fetch("/api/config", { headers: authHeaders });
      configText.value = resp.ok ? await resp.text() : "(불러오기 실패: " + resp.status + ")";
    } catch (e) { configText.value = "(불러오기 오류)"; }
    configOverlay.hidden = false;
  }
  async function saveConfig() {
    if (configMsg) configMsg.textContent = "저장 중…";
    try {
      const resp = await fetch("/api/config", { method: "PUT", headers: authHeaders, body: configText.value });
      if (resp.status === 204) { if (configMsg) configMsg.textContent = "저장됨 — 핫리로드 적용"; }
      else { const t = await resp.text(); if (configMsg) configMsg.textContent = "실패: " + t; }
    } catch (e) { if (configMsg) configMsg.textContent = "오류: " + e; }
  }
  if (btnConfig) btnConfig.addEventListener("click", openConfig);
  document.getElementById("config-close")?.addEventListener("click", () => { configOverlay.hidden = true; });
  document.getElementById("config-save")?.addEventListener("click", saveConfig);

  // Connections panel
  async function openConnections() {
    if (!connOverlay || !connBody) return;
    connBody.replaceChildren();
    let data = {};
    try { const resp = await fetch("/api/status", { headers: authHeaders }); if (resp.ok) data = await resp.json(); } catch (e) {}
    const rows = [
      ["웹 채팅 주소", data.webChatAddr || "(미설정)"],
      ["제어 API 사용", data.chatControlEnabled ? "켜짐" : "꺼짐"],
      ["제어 API 주소", data.chatControlAddr || "(미설정)"],
    ];
    for (const [k, v] of rows) {
      const row = document.createElement("div"); row.className = "conn-row";
      const kk = document.createElement("span"); kk.className = "k"; kk.textContent = k;
      const vv = document.createElement("span"); vv.textContent = v;
      row.append(kk, vv); connBody.appendChild(row);
    }
    const arow = document.createElement("div"); arow.className = "conn-row";
    const ak = document.createElement("span"); ak.className = "k"; ak.textContent = "aglink-chat 연동";
    const av = document.createElement("span");
    av.className = data.aglinkConnected ? "conn-ok" : "conn-off";
    av.textContent = data.aglinkConnected ? ("연결됨 (" + (data.aglinkClients || 0) + ")") : "연결 안 됨";
    arow.append(ak, av); connBody.appendChild(arow);
    const note = document.createElement("div"); note.className = "topic-summary";
    note.textContent = "주소/포트 변경은 ⚙ 설정에서 config.yaml을 편집한 뒤 재시작하세요.";
    connBody.appendChild(note);
    connOverlay.hidden = false;
  }
  if (btnConnections) btnConnections.addEventListener("click", openConnections);
  document.getElementById("conn-close")?.addEventListener("click", () => { connOverlay.hidden = true; });
```

초기화부(파일 끝 `loadConversations(); connect();`)에 `bootstrapCapabilities();` 추가.

> 주의: optional chaining(`?.`)은 Node 14+/모든 현대 브라우저 지원 — `node --check` 통과. 기존 코드 스타일과 일관되게 유지.

- [ ] **Step 4: node --check 양쪽** → 오류 없음.

- [ ] **Step 5: 수동 확인 (Teleclaude 로컬 실행 가능 시)** — 헤더에 버전 배지/⚙/🔌/⟳ 노출, ⚙→config 로드/저장, 🔌→상태 표시. aglink-chat에서는 `#admin-controls` 숨김 유지.

- [ ] **Step 6: 커밋 (양 저장소)** — 각각 `feat(web): admin panel (version, update, config edit, connections)`.

---

## Task 9: 자매 파일 수렴 + 전체 검증 + 배포 준비

**Files:**
- Verify: `Teleclaude/web/*` == `../aglink-chat/web/*` (동일 자매), 양 저장소 빌드.

- [ ] **Step 1: 자매 파일 동일성 확인** — `diff Teleclaude/web/app.js ../aglink-chat/web/app.js` → 차이 없음(줄바꿈 제외). index.html/style.css도 동일. 차이가 있으면 정본(Teleclaude)을 aglink-chat로 복사.
- [ ] **Step 2: Teleclaude 전체** — `go build ./... && go vet ./... && go test ./...` → 그린. gofmt: `for f in *.go; do tr -d '\r' < "$f" | gofmt -l 2>/dev/null; done` 빈 출력.
- [ ] **Step 3: aglink-chat 전체** — `go build ./... && go vet ./...` (+ 테스트 있으면) → 그린.
- [ ] **Step 4: node --check** — 두 app.js 통과.
- [ ] **Step 5: 최종 커밋 정리** — 두 저장소 `git status` clean, 미커밋분 커밋.
- [ ] **Step 6: 배포** — 사용자 확인 후 웹 UI ⟳ 버튼 또는 텔레그램 `!update`로 배포. (배포는 사용자가 브라우저에서 확인.)

---

## Self-Review (스펙 대비 커버리지)

- 1 레이아웃/모달 작성창 → Task 6, 7. ✓
- 2 버전 표시 → Task 1(+Task 8 배지). ✓
- 3 업데이트 버튼 → Task 8(백엔드 없음, `!update` 재사용). ✓
- 4 aglink 연동 상태 → Task 3(/api/status) + Task 8(연결 패널). ✓
- 5 config.yaml 편집 → Task 2 + Task 8. ✓
- 6 연결 관리 → Task 3 + Task 8(재시작 안내). ✓
- 7 대화 관리 → Task 4(web_delete) + Task 5(aglink 릴레이) + Task 7(⋯ 메뉴). ✓
- 보안(토큰/고정경로/비밀마스킹/명령경로 재사용) → 각 Go 태스크 + Global Constraints. ✓
