# 대화 관리 재설계 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 텔레그램을 프로젝트 무관 단일 연속 대화로, 웹을 프로젝트별 토픽으로 완전히 분리하고, 라우팅·저장·웹 프로토콜·양쪽 UI를 새 구조로 재정리한다.

**Architecture:** `StoreData`에 전역 `TelegramConv` + `TelegramActiveProject`를 1급 필드로 추가하고 `Projects[].Conversations`는 웹 토픽 전용으로 좁힌다. `runWorker`의 대화 저장/이어쓰기 경로를 `convSink` 인터페이스로 추상화해 텔레그램(전역)과 웹 토픽(프로젝트) 두 구현이 각자 저장한다. `Manager.Handle`을 origin으로 분기하고, 웹 전송은 per-send target(telegram|web-topic)으로 라우팅한다.

**Tech Stack:** Go 1.25, `github.com/coder/websocket` + `wsjson`, go:embed, Vanilla JS (web/app.js × 2: 임베디드 `Teleclaude/web/`, 분리 `aglink-chat/web/`).

## Global Constraints

- 마이그레이션 없음: 기존 store.json은 스키마 버전 불일치 시 `store.json.bak`로 1회 백업 후 새 스키마로 초기화. 옛 대화 보존 안 함.
- 텔레그램 = 전역 단일 대화 1개(프로젝트 무관). 활성 프로젝트는 작업 디렉토리 포인터일 뿐.
- 웹 = 프로젝트별 토픽. 웹만 토픽 생성/이름변경/전환 가능. 텔레그램은 웹 토픽을 못 건드린다.
- 방향 비대칭: 웹→텔레그램 보기·이어가기 가능 / 텔레그램→웹 토픽 불가.
- 대화는 항상 **자기 backend**로 resume (커밋 5f9e2a0 원칙 유지). 미설치 backend면 포크 금지, 명확 안내.
- 각 태스크: `go build ./... && go vet ./... && go test ./...` 그린. JS 변경 시 `node --check` 양쪽. Windows에서 gofmt는 CRLF 오탐 가능 → 실제 확인은 `tr -d '\r' | gofmt -d`.
- push 금지(로컬 커밋만). 커밋 trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- 유틸 명령(`!project`,`!status`,`!backend`,`!task`,`!screen`,`!help`)은 텔레그램 유지. `!chat`만 웹 전용.

---

## File Structure

- `types.go` — `StoreData` 필드 추가, `StoreRepo` 인터페이스 확장, 새 프로토콜 타입(`Target`, `historyResponse`).
- `store.go` — 스키마 버전/백업/초기화, 텔레그램 대화 메서드.
- `manager.go` — `convSink` 추상화, `Handle` origin 분기, `handleTelegram`/`handleWeb`, `detectProjectSwitchIntent`, `routeProjectOnly`.
- `bot.go` — `handleChat` 웹 전용화 + `rename` 서브커맨드.
- `webchat.go` — 응답에 텔레그램 전역 항목, `inMsg`에 target, `/api/history`, `buildHistoryResponse`.
- `chatcontrol.go` — `controlIn`에 target + `get_history`, 웹 전송 target 라우팅.
- `Teleclaude/web/app.js`, `aglink-chat/web/app.js` — 텔레그램 고정 항목, currentTarget, 클릭 시 히스토리 로드.
- `Teleclaude/web/index.html` — (필요 시) 텔레그램 고정 항목 컨테이너. (현재는 JS가 topic-list에 렌더하므로 마크업 변경 최소.)

---

## Task 1: Store 스키마 — 전역 텔레그램 대화 + 백업/초기화

**Files:**
- Modify: `types.go` (StoreData, StoreRepo)
- Modify: `store.go` (Load 백업/초기화, 텔레그램 메서드)
- Test: `store_telegram_test.go` (Create)

**Interfaces:**
- Produces:
  - `StoreData.SchemaVersion int`, `StoreData.TelegramConv *Conversation`, `StoreData.TelegramActiveProject string`
  - `const storeSchemaVersion = 2`
  - `func (s *fileStore) TelegramConversation() *Conversation` — 없으면 생성(새 UUID, Backend=GetStoredBackend()||"claude", Title="텔레그램 대화").
  - `func (s *fileStore) UpdateTelegramConversation(c *Conversation) error`
  - `func (s *fileStore) TelegramActiveProject() string`
  - `func (s *fileStore) SetTelegramActiveProject(name string) error`
  - StoreRepo 인터페이스에 위 4개 메서드 추가.

- [ ] **Step 1: 실패 테스트 작성** — `store_telegram_test.go`

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStore_LegacyFile_BackedUpAndReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	// A legacy file with no schemaVersion and an old telegram conversation.
	legacy := `{"projects":{"myapp":{"path":"` + filepath.ToSlash(dir) + `","conversations":{"1":{"id":"1","title":"old","origin":"telegram"}}}},"active":{"project":"myapp","conversationId":"1"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	st := NewFileStore(path)
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	// Legacy schema (version 0) → backed up and reset to empty new schema.
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Errorf("expected store.json.bak backup, got %v", err)
	}
	if len(st.ListProjects()) != 0 {
		t.Errorf("legacy data must be discarded, got %d projects", len(st.ListProjects()))
	}
}

func TestStore_TelegramConversation_GetOrCreate(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	c1 := st.TelegramConversation()
	if c1 == nil || c1.SessionID == "" {
		t.Fatal("telegram conversation must be created with a session id")
	}
	c2 := st.TelegramConversation()
	if c2.ID != c1.ID || c2.SessionID != c1.SessionID {
		t.Errorf("TelegramConversation must be a singleton, got %q then %q", c1.SessionID, c2.SessionID)
	}
}

func TestStore_TelegramActiveProject_Persists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	st := NewFileStore(path)
	_ = st.Load()
	if err := st.SetTelegramActiveProject("myapp"); err != nil {
		t.Fatal(err)
	}
	// Reload from disk to confirm persistence + schema version written.
	st2 := NewFileStore(path)
	if err := st2.Load(); err != nil {
		t.Fatal(err)
	}
	if st2.TelegramActiveProject() != "myapp" {
		t.Errorf("telegram active project = %q, want myapp", st2.TelegramActiveProject())
	}
	b, _ := os.ReadFile(path)
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	if raw["schemaVersion"] == nil {
		t.Error("persisted store must carry schemaVersion")
	}
}
```

- [ ] **Step 2: 실패 확인** — `go test -run TestStore_ . -v` → 컴파일 실패(미정의 메서드/필드).

- [ ] **Step 3: `types.go` — StoreData 필드 + 인터페이스**

`StoreData` 교체:

```go
// StoreData is the root persisted to store.json (별도 저장소).
type StoreData struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Projects      map[string]*Project `json:"projects"`             // 웹 토픽 전용
	Active        ActiveRef           `json:"active"`               // 웹의 활성 토픽
	ActiveBackend string              `json:"activeBackend,omitempty"`

	// 전역 단일 텔레그램 대화 (프로젝트 무관).
	TelegramConv          *Conversation `json:"telegramConv,omitempty"`
	TelegramActiveProject string        `json:"telegramActiveProject,omitempty"`
}
```

`StoreRepo` 인터페이스에 추가:

```go
	TelegramConversation() *Conversation
	UpdateTelegramConversation(c *Conversation) error
	TelegramActiveProject() string
	SetTelegramActiveProject(name string) error
```

- [ ] **Step 4: `store.go` — 스키마 버전/백업/초기화 + 메서드**

`store.go` 상단에 상수 추가:

```go
const storeSchemaVersion = 2
```

`Load()` 교체(파싱 후 스키마 검사):

```go
func (s *fileStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.data = newEmptyStore()
			return nil
		}
		return err
	}
	var d StoreData
	if err := json.Unmarshal(b, &d); err != nil {
		return fmt.Errorf("store.json 파싱 실패: %w", err)
	}
	// Schema mismatch (or legacy file with no version) → back up once and reset.
	// Migration is intentionally not supported: old conversations are discarded.
	if d.SchemaVersion != storeSchemaVersion {
		if berr := os.Rename(s.path, s.path+".bak"); berr != nil {
			log.Printf("[store] legacy backup failed: %v (starting fresh anyway)", berr)
		} else {
			log.Printf("[store] legacy store.json (schema %d) backed up to %s.bak; starting fresh (schema %d)", d.SchemaVersion, s.path, storeSchemaVersion)
		}
		s.data = newEmptyStore()
		return s.saveLocked()
	}
	if d.Projects == nil {
		d.Projects = map[string]*Project{}
	}
	for _, p := range d.Projects {
		if p.Conversations == nil {
			p.Conversations = map[string]*Conversation{}
		}
	}
	s.data = d
	return nil
}

func newEmptyStore() StoreData {
	return StoreData{SchemaVersion: storeSchemaVersion, Projects: map[string]*Project{}}
}
```

`NewFileStore`의 초기 data도 새 스키마로:

```go
func NewFileStore(path string) *fileStore {
	return &fileStore{path: path, data: newEmptyStore()}
}
```

`saveLocked()`에서 항상 버전을 새겨 저장(방어적):

```go
func (s *fileStore) saveLocked() error {
	s.data.SchemaVersion = storeSchemaVersion
	b, err := json.MarshalIndent(s.data, "", "  ")
	// ... (이하 기존과 동일: tmp write + rename)
```

`store.go`에 `log` import가 없으면 추가.

텔레그램 메서드 추가(파일 끝 부근):

```go
// TelegramConversation returns the single global telegram conversation, creating
// it on first access. It is project-independent; the working directory for a
// telegram turn comes from TelegramActiveProject.
func (s *fileStore) TelegramConversation() *Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.TelegramConv == nil {
		backend := s.data.ActiveBackend
		if backend == "" {
			backend = "claude"
		}
		s.data.TelegramConv = &Conversation{
			ID:           "telegram",
			Title:        "텔레그램 대화",
			SessionID:    newUUID(),
			Started:      false,
			LastActivity: time.Now().UTC(),
			Backend:      backend,
			Origin:       OriginTelegram,
		}
		_ = s.saveLocked()
	}
	return s.data.TelegramConv
}

func (s *fileStore) UpdateTelegramConversation(c *Conversation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.TelegramConv = c
	return s.saveLocked()
}

func (s *fileStore) TelegramActiveProject() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.TelegramActiveProject
}

func (s *fileStore) SetTelegramActiveProject(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.TelegramActiveProject = name
	return s.saveLocked()
}
```

- [ ] **Step 5: 통과 확인** — `go test -run TestStore_ . -v` → PASS. 그다음 전체: `go build ./... && go vet ./... && go test ./...`

> 참고: `RemoveProject`가 `Active`를 정리하듯, 삭제된 프로젝트가 `TelegramActiveProject`면 함께 비우도록 `RemoveProject`에 `if s.data.TelegramActiveProject == name { s.data.TelegramActiveProject = "" }` 한 줄 추가.

- [ ] **Step 6: 커밋**

```bash
git add types.go store.go store_telegram_test.go
git commit -m "feat(store): global telegram conversation + schema-versioned reset (no migration)"
```

---

## Task 2: runWorker 저장 경로 추상화 (`convSink`)

`runWorker`가 대화를 어디에 저장/이어쓸지 몰라도 되게 `convSink`로 추상화한다. 기존 프로젝트 토픽 동작은 **불변**(기존 테스트 전부 그린 유지)이어야 한다.

**Files:**
- Modify: `manager.go` (`convSink` 정의, `runWorker`가 sink 사용)
- Test: `convsink_test.go` (Create)

**Interfaces:**
- Consumes: Task 1의 `UpdateTelegramConversation`.
- Produces:
  - `type convSink interface { project() string; save(*Conversation) error; setActive(*Conversation) error; makeContinuation(*Conversation) (*Conversation, error) }`
  - `func (m *Manager) projectSink(project string) convSink` — 기존 동작(UpdateConversation/SetActive/makeContinuation).
  - `func (m *Manager) telegramSink(project string) convSink` — 텔레그램 전역 저장.
  - `runWorker` 시그니처: `runWorker(ctx, chatID int64, text string, sink convSink, workDir string, c *Conversation, s MessageSender, client ClaudeClient, backend string)` (project 문자열 파라미터를 sink로 교체; workDir 유지).

- [ ] **Step 1: 실패 테스트 작성** — `convsink_test.go`

```go
package main

import (
	"context"
	"path/filepath"
	"testing"
)

// The project sink persists into Projects[project]; the telegram sink persists
// into the global TelegramConv. runWorker must route saves through the sink.
func TestConvSink_Telegram_PersistsGlobally(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "done", SessionID: "sess-x"}}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	dir := t.TempDir()
	_ = st.AddProject("myapp", dir)
	m := NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true}))

	tc := st.TelegramConversation()
	sink := m.telegramSink("myapp")
	f := &fakeSender{}
	m.runWorker(context.Background(), 1, "hi", sink, dir, tc, f, fc, "claude")

	got := st.TelegramConversation()
	if !got.Started || len(got.History) != 1 || got.History[0].Prompt != "hi" {
		t.Errorf("telegram conversation not persisted globally: %+v", got)
	}
	// Must NOT have leaked into any project's conversation map.
	p, _ := st.GetProject("myapp")
	if len(p.Conversations) != 0 {
		t.Errorf("telegram turn must not create a project topic, got %d", len(p.Conversations))
	}
}
```

- [ ] **Step 2: 실패 확인** — `go test -run TestConvSink . -v` → 컴파일 실패.

- [ ] **Step 3: `convSink` 정의 + 두 구현** (`manager.go`)

```go
// convSink abstracts where a worker turn persists its conversation: a project
// topic (web) or the global telegram conversation. This keeps runWorker unaware
// of the two storage locations.
type convSink interface {
	project() string                                   // workdir/status/history-log scope
	save(c *Conversation) error                        // persist updated conversation
	setActive(c *Conversation) error                   // update the channel's active pointer
	makeContinuation(c *Conversation) (*Conversation, error)
}

type projectSink struct {
	m       *Manager
	project string
}

func (p projectSink) project() string { return p.project }
func (p projectSink) save(c *Conversation) error {
	return p.m.store.UpdateConversation(p.project, c)
}
func (p projectSink) setActive(c *Conversation) error {
	return p.m.store.SetActive(p.project, c.ID)
}
func (p projectSink) makeContinuation(c *Conversation) (*Conversation, error) {
	return p.m.makeContinuation(p.project, c)
}

func (m *Manager) projectSink(project string) convSink { return projectSink{m: m, project: project} }

// telegramSink persists the single global telegram conversation. `proj` is only
// the working-directory/history scope (the active project), never the owner of
// the conversation record. Continuation is an in-place session reset: the same
// record continues with a fresh CLI session, so the telegram stream stays one
// conversation to the user.
type telegramSink struct {
	m    *Manager
	proj string
}

func (t telegramSink) project() string { return t.proj }
func (t telegramSink) save(c *Conversation) error {
	return t.m.store.UpdateTelegramConversation(c)
}
func (t telegramSink) setActive(c *Conversation) error { return nil } // telegram active is the stream itself
func (t telegramSink) makeContinuation(c *Conversation) (*Conversation, error) {
	// In-place continuation: keep the same telegram record but drop the CLI
	// session so the next turn starts fresh with the carried summary. History is
	// preserved (capped), so the user still sees a single continuous stream.
	c.SessionID = newUUID()
	c.Started = false
	return c, nil
}

func (m *Manager) telegramSink(project string) convSink { return telegramSink{m: m, proj: project} }
```

- [ ] **Step 4: `runWorker`가 sink 사용하도록 수정** (`manager.go`)

`runWorker` 시그니처에서 `project string` → `sink convSink`. 본문에서:
- `project := sink.project()`를 함수 첫 줄에 추가(이후 `project` 사용처 그대로 유지: `GetProject(project)`, workDir, workerStatus, WriteHistory, 로그).
- 저장/전환/이어쓰기 3곳 교체:
  - proactive 길이 분할: `m.makeContinuation(project, c)` → `sink.makeContinuation(c)`
  - reactive 오버플로: `m.makeContinuation(project, workConv)` → `sink.makeContinuation(workConv)`
  - 최종 저장: `m.store.UpdateConversation(project, workConv)` → `sink.save(workConv)`
  - 최종 활성화: `m.store.SetActive(project, workConv.ID)` → `sink.setActive(workConv)`
- 두 continuation 지점의 `newC.Backend = m.Backend()` / `newC.Backend = m.Backend()`는 이미 Task-없이 기존 코드가 `backend` 파라미터를 쓰도록 되어있지 않다면 `backend`로 유지(5f9e2a0에서 이미 `backend` 파라미터화됨).

함수 시작부 수정 예:

```go
func (m *Manager) runWorker(ctx context.Context, chatID int64, text string, sink convSink, workDir string, c *Conversation, s MessageSender, client ClaudeClient, backend string) {
	defer s.Done(chatID)
	project := sink.project()
	p, ok := m.store.GetProject(project)
	if !ok {
		_ = s.Send(chatID, "⚠️ 프로젝트를 찾을 수 없습니다: "+project)
		return
	}
	// ... 이하 기존 본문에서 UpdateConversation/SetActive/makeContinuation만 sink 경유로 교체
```

- [ ] **Step 5: 기존 호출부 8곳 전부 `sink`로 교체** — 모두 `m.projectSink(<project>)`로 감싼다. 예:
  - `m.runWorker(ctx, chatID, text, m.projectSink(active.Project), "", c, s, currentClient, currentBackend)`
  - 나머지 `dec.Project`, `only`, `projectName`(scheduled) 동일 패턴.

- [ ] **Step 6: 통과 확인** — `go build ./... && go vet ./... && go test ./...` (기존 매니저/스케줄러/오케스트레이션 테스트가 프로젝트 sink로 그대로 그린이어야 함) + 신규 `TestConvSink_Telegram_PersistsGlobally` PASS.

- [ ] **Step 7: 커밋**

```bash
git add manager.go convsink_test.go
git commit -m "refactor(manager): abstract worker persistence via convSink (project topic vs global telegram)"
```

---

## Task 3: `detectProjectSwitchIntent` + `routeProjectOnly`

텔레그램에서 "이제 프로젝트 B 하자"를 감지해 활성 프로젝트만 바꾼다. (i) 등록 프로젝트명 키워드 매칭 우선, (ii) 실패 시 경량 LLM 폴백.

**Files:**
- Modify: `manager.go`
- Test: `switch_intent_test.go` (Create)

**Interfaces:**
- Produces:
  - `func detectProjectSwitchIntent(text string, projectNames []string) (string, bool)` — 순수 함수. 텍스트에 등록 프로젝트명이 포함되면 그 이름 반환(가장 긴 매칭 우선, 대소문자 무시). 없으면 `("", false)`.
  - `func (m *Manager) routeProjectOnly(ctx context.Context, client ClaudeClient, text string) (string, bool)` — LLM `Route` 호출 후 `dec.Project`가 등록 프로젝트면 반환.

- [ ] **Step 1: 실패 테스트** — `switch_intent_test.go`

```go
package main

import "testing"

func TestDetectProjectSwitchIntent(t *testing.T) {
	names := []string{"myapp", "voice", "voice-server"}
	cases := []struct {
		text string
		want string
		ok   bool
	}{
		{"이제 voice-server 하자", "voice-server", true}, // 가장 긴 매칭 우선
		{"myapp 로그인 버그 보자", "myapp", true},
		{"그냥 계속 진행하자", "", false},
		{"VOICE 쪽 확인", "voice", true}, // 대소문자 무시
	}
	for _, c := range cases {
		got, ok := detectProjectSwitchIntent(c.text, names)
		if got != c.want || ok != c.ok {
			t.Errorf("detectProjectSwitchIntent(%q) = (%q,%v), want (%q,%v)", c.text, got, ok, c.want, c.ok)
		}
	}
}
```

- [ ] **Step 2: 실패 확인** — `go test -run TestDetectProjectSwitchIntent . -v` → FAIL(미정의).

- [ ] **Step 3: 구현** (`manager.go`)

```go
// detectProjectSwitchIntent returns a registered project name mentioned in text,
// preferring the longest match (so "voice-server" wins over "voice"). Case-
// insensitive. Deterministic; no LLM. Returns ("", false) when none match.
func detectProjectSwitchIntent(text string, projectNames []string) (string, bool) {
	lower := strings.ToLower(text)
	best := ""
	for _, name := range projectNames {
		if name == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(name)) && len(name) > len(best) {
			best = name
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

// routeProjectOnly asks the manager LLM which project the message targets, used
// only as a fallback when keyword matching fails. Returns the project name if the
// LLM names a registered project, else ("", false).
func (m *Manager) routeProjectOnly(ctx context.Context, client ClaudeClient, text string) (string, bool) {
	req := m.buildRouteRequest(text)
	dec, err := client.Route(ctx, req)
	if err != nil {
		log.Printf("[manager] routeProjectOnly error: %v", err)
		return "", false
	}
	if dec.Project == "" {
		return "", false
	}
	if _, ok := m.store.GetProject(dec.Project); !ok {
		return "", false
	}
	return dec.Project, true
}
```

- [ ] **Step 4: 통과 확인** — `go test -run TestDetectProjectSwitchIntent . -v` → PASS.

- [ ] **Step 5: 커밋**

```bash
git add manager.go switch_intent_test.go
git commit -m "feat(manager): project-switch intent detection (keyword + LLM fallback)"
```

---

## Task 4: `Manager.Handle` origin 분기 + 텔레그램 턴

**Files:**
- Modify: `manager.go`
- Test: `handle_split_test.go` (Create)

**Interfaces:**
- Consumes: Task 1(텔레그램 store 메서드), Task 2(sink), Task 3(switch intent).
- Produces:
  - `func (m *Manager) handleTelegram(ctx, chatID int64, text string, s MessageSender)`
  - `func (m *Manager) resolveTelegramProject(chatID int64, text string, s MessageSender) (string, bool)` — 활성 프로젝트 결정(전환 의도 반영, 없으면 1개 자동/다수 질문/0개 안내).
  - `Handle`은 `origin`으로 분기: telegram → `handleTelegram`, web → `handleWeb`(Task 5).

- [ ] **Step 1: 실패 테스트** — `handle_split_test.go`

```go
package main

import (
	"context"
	"path/filepath"
	"testing"
)

func tgManager(t *testing.T, fc *fakeClaude) (*Manager, *fileStore, string) {
	t.Helper()
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	dir := t.TempDir()
	_ = st.AddProject("myapp", dir)
	_ = st.SetTelegramActiveProject("myapp")
	return NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true})), st, dir
}

// A plain telegram message always continues the single global telegram conversation.
func TestHandle_Telegram_AlwaysGlobalConversation(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m, st, _ := tgManager(t, fc)
	f := &fakeSender{}

	m.Handle(context.Background(), 1, "로그인 고쳐줘", OriginTelegram, f)

	if fc.runCalls != 1 {
		t.Fatalf("expected one worker run, got %d", fc.runCalls)
	}
	tc := st.TelegramConversation()
	if len(tc.History) != 1 {
		t.Fatalf("telegram conversation should have the turn, got %d", len(tc.History))
	}
	// No project topic created.
	p, _ := st.GetProject("myapp")
	if len(p.Conversations) != 0 {
		t.Errorf("telegram must not create a project topic, got %d", len(p.Conversations))
	}
}

// "이제 <project> 하자" switches TelegramActiveProject but keeps the same conversation.
func TestHandle_Telegram_ProjectSwitch_KeepsConversation(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m, st, _ := tgManager(t, fc)
	_ = st.AddProject("voice", t.TempDir())
	f := &fakeSender{}

	m.Handle(context.Background(), 1, "이제 voice 하자", OriginTelegram, f)

	if st.TelegramActiveProject() != "voice" {
		t.Errorf("active project should switch to voice, got %q", st.TelegramActiveProject())
	}
	if st.TelegramConversation().ID != "telegram" {
		t.Errorf("switch must not fork the telegram conversation")
	}
}
```

- [ ] **Step 2: 실패 확인** — `go test -run TestHandle_Telegram . -v` → 컴파일/동작 실패.

- [ ] **Step 3: `Handle` 분기 + `handleTelegram` 구현** (`manager.go`)

`Handle` 최상단(백엔드 자동전환 프리체크 유지)에서 origin 분기:

```go
func (m *Manager) Handle(ctx context.Context, chatID int64, text, origin string, s MessageSender) {
	if origin == OriginWeb {
		m.handleWeb(ctx, chatID, text, s) // Task 5
		return
	}
	m.handleTelegram(ctx, chatID, text, s)
}
```

```go
// handleTelegram continues the single global telegram conversation. A project-
// switch intent only moves the working-directory pointer; the conversation and
// its history stay one continuous stream.
func (m *Manager) handleTelegram(ctx context.Context, chatID int64, text string, s MessageSender) {
	// Backend auto-switch pre-check (unchanged behavior).
	if target := detectBackendSwitchIntent(text); target != "" && target != m.Backend() {
		if err := m.SetBackend(target); err != nil {
			_ = s.Send(chatID, "⚠️ 백엔드 전환 실패: "+err.Error())
		} else {
			_ = s.Send(chatID, "🔄 백엔드 전환: "+strings.ToUpper(target))
		}
	}

	project, ok := m.resolveTelegramProject(chatID, text, s)
	if !ok {
		return // resolveTelegramProject already messaged the user
	}

	p, _ := m.store.GetProject(project)
	tc := m.store.TelegramConversation()

	convBackend := tc.Backend
	if convBackend == "" {
		convBackend = "claude"
	}
	client := m.clientForBackend(convBackend)
	if client == nil {
		_ = s.Send(chatID, fmt.Sprintf("⚠️ 텔레그램 대화는 %s로 생성됐는데 %s가 설치되어 있지 않습니다. `!backend`로 전환하거나 설치 후 다시 시도해 주세요.",
			strings.ToUpper(convBackend), strings.ToUpper(convBackend)))
		return
	}
	m.runWorker(ctx, chatID, text, m.telegramSink(project), p.Path, tc, s, client, convBackend)
}

// resolveTelegramProject returns the working-directory project for this turn.
// A project-switch intent updates the stored pointer; otherwise the current
// pointer is used. Falls back to the single project, or asks when ambiguous.
func (m *Manager) resolveTelegramProject(chatID int64, text string, s MessageSender) (string, bool) {
	names := make([]string, 0)
	for name := range m.store.ListProjects() {
		names = append(names, name)
	}
	if len(names) == 0 {
		_ = s.Send(chatID, "등록된 프로젝트가 없습니다. 먼저 등록하세요:\n!project add <이름> <경로>")
		return "", false
	}

	// Explicit switch intent (keyword, then LLM fallback).
	switched, ok := detectProjectSwitchIntent(text, names)
	if !ok && len(names) > 1 {
		m.backendMu.RLock()
		client := m.client
		m.backendMu.RUnlock()
		switched, ok = m.routeProjectOnly(context.Background(), client, text)
	}
	if ok {
		if switched != m.store.TelegramActiveProject() {
			_ = m.store.SetTelegramActiveProject(switched)
			_ = s.Send(chatID, "📂 이제 "+switched+"에서 진행합니다.")
		}
		return switched, true
	}

	// No switch: use current pointer if still valid.
	cur := m.store.TelegramActiveProject()
	if _, exists := m.store.GetProject(cur); exists {
		return cur, true
	}
	// Pointer empty/stale: 1 project → auto, else ask.
	if len(names) == 1 {
		_ = m.store.SetTelegramActiveProject(names[0])
		return names[0], true
	}
	_ = s.Send(chatID, "🤔 어느 프로젝트에서 할지 알려주세요. 예: \"이제 <프로젝트명> 하자\" (!project list 로 목록 확인)")
	return "", false
}
```

> 주의: `resolveTelegramProject`의 LLM 폴백은 `ManagerAlways`와 무관하게 프로젝트 전환 판단에만 쓰인다. 테스트 `TestHandle_Telegram_ProjectSwitch_KeepsConversation`는 키워드 매칭("voice")으로 처리되어 LLM 호출 없이 통과한다.

- [ ] **Step 4: 통과 확인** — `go test -run TestHandle_Telegram . -v` → PASS. (이 시점엔 `handleWeb`가 아직 없으면 컴파일 위해 Step 3에서 `handleWeb` 임시 스텁을 두거나 Task 5와 함께 진행. 실무상 Task 4·5를 연속 커밋 권장 — 아래 Step 5에서 스텁 명시.)

- [ ] **Step 5: `handleWeb` 임시 스텁**(Task 5에서 교체) — 컴파일용 최소 스텁을 manager.go에 추가:

```go
func (m *Manager) handleWeb(ctx context.Context, chatID int64, text string, s MessageSender) {
	// Replaced in Task 5. Temporary: route to the active web topic if any.
	active := m.store.GetActive()
	if active.Project == "" {
		_ = s.Send(chatID, "웹 토픽을 먼저 선택하거나 생성하세요.")
		return
	}
	c, ok := m.store.GetConversation(active.Project, active.ConversationID)
	if !ok {
		_ = s.Send(chatID, "웹 토픽을 찾을 수 없습니다.")
		return
	}
	m.backendMu.RLock()
	client, backend := m.client, m.backendName
	m.backendMu.RUnlock()
	m.runWorker(ctx, chatID, text, m.projectSink(active.Project), "", c, s, client, backend)
}
```

- [ ] **Step 6: 커밋**

```bash
git add manager.go handle_split_test.go
git commit -m "feat(manager): split Handle by origin; telegram = single global conversation"
```

---

## Task 5: 웹 전송 target 라우팅 (telegram vs web-topic)

웹 전송이 무엇을 대상으로 하는지 per-send target으로 명시한다. 텔레그램 항목이면 전역 텔레그램 대화를, 웹 토픽이면 해당 토픽을 이어간다.

**Files:**
- Modify: `types.go` (Target 타입), `webchat.go` (inMsg.Target, inject), `chatcontrol.go` (controlIn.Target, send_text 라우팅), `manager.go` (`handleWeb` 교체 + `HandleWebTarget`)
- Test: `handle_web_test.go` (Create)

**Interfaces:**
- Produces:
  - `type Target struct { Kind string; Project string; ID string }` — Kind: `"telegram"` | `"web"`.
  - `func (m *Manager) HandleWebTarget(ctx, chatID int64, text string, tgt Target, s MessageSender)` — target 라우팅.
  - `inMsg.Target *Target`, `controlIn.Target *Target`.
- Consumes: Task 4 텔레그램 경로(`telegramSink`, `resolveTelegramProject`), Task 2 sink.

- [ ] **Step 1: 실패 테스트** — `handle_web_test.go`

```go
package main

import (
	"context"
	"path/filepath"
	"testing"
)

func webTgtManager(t *testing.T, fc *fakeClaude) (*Manager, *fileStore, string) {
	t.Helper()
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	dir := t.TempDir()
	_ = st.AddProject("myapp", dir)
	_ = st.SetTelegramActiveProject("myapp")
	return NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true})), st, dir
}

// A web send targeting telegram continues the global telegram conversation.
func TestHandleWebTarget_Telegram(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m, st, _ := webTgtManager(t, fc)
	f := &fakeSender{}

	m.HandleWebTarget(context.Background(), 1, "여기 텔레그램 스트림에 이어서", Target{Kind: "telegram"}, f)

	if len(st.TelegramConversation().History) != 1 {
		t.Errorf("web→telegram target should append to telegram conversation")
	}
}

// A web send targeting a web topic continues that topic only.
func TestHandleWebTarget_WebTopic(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m, st, _ := webTgtManager(t, fc)
	c, _ := st.NewConversation("myapp", "웹 토픽", OriginWeb)
	f := &fakeSender{}

	m.HandleWebTarget(context.Background(), 1, "이 토픽 이어서", Target{Kind: "web", Project: "myapp", ID: c.ID}, f)

	got, _ := st.GetConversation("myapp", c.ID)
	if len(got.History) != 1 {
		t.Errorf("web topic target should append to that topic, got %d", len(got.History))
	}
	if len(st.TelegramConversation().History) != 0 {
		t.Errorf("web topic target must not touch telegram conversation")
	}
}
```

- [ ] **Step 2: 실패 확인** — `go test -run TestHandleWebTarget . -v` → 실패.

- [ ] **Step 3: `Target` 타입** (`types.go`)

```go
// Target identifies what a web send continues: the global telegram stream, or a
// specific web topic. Kind is "telegram" or "web".
type Target struct {
	Kind    string `json:"kind"`
	Project string `json:"project,omitempty"`
	ID      string `json:"id,omitempty"`
}
```

- [ ] **Step 4: `HandleWebTarget` + `handleWeb` 교체** (`manager.go`)

```go
// HandleWebTarget routes a web send to its explicit target: the global telegram
// stream or a specific web topic. Web never does LLM project routing.
func (m *Manager) HandleWebTarget(ctx context.Context, chatID int64, text string, tgt Target, s MessageSender) {
	if tgt.Kind == "telegram" {
		project := m.store.TelegramActiveProject()
		p, ok := m.store.GetProject(project)
		if !ok {
			// Fall back to a single project, else ask.
			names := projectNames(m.store)
			if len(names) == 1 {
				project = names[0]
				_ = m.store.SetTelegramActiveProject(project)
				p, _ = m.store.GetProject(project)
			} else {
				_ = s.Send(chatID, "🤔 텔레그램 대화의 작업 프로젝트가 정해지지 않았습니다. 텔레그램에서 \"이제 <프로젝트명> 하자\"로 먼저 지정해 주세요.")
				return
			}
		}
		tc := m.store.TelegramConversation()
		backend := tc.Backend
		if backend == "" {
			backend = "claude"
		}
		client := m.clientForBackend(backend)
		if client == nil {
			_ = s.Send(chatID, fmt.Sprintf("⚠️ 텔레그램 대화는 %s로 생성됐는데 %s가 설치되어 있지 않습니다.", strings.ToUpper(backend), strings.ToUpper(backend)))
			return
		}
		m.runWorker(ctx, chatID, text, m.telegramSink(project), p.Path, tc, s, client, backend)
		return
	}

	// Web topic target.
	c, ok := m.store.GetConversation(tgt.Project, tgt.ID)
	if !ok {
		_ = s.Send(chatID, "웹 토픽을 찾을 수 없습니다: "+tgt.Project+"/"+tgt.ID)
		return
	}
	backend := c.Backend
	if backend == "" {
		backend = "claude"
	}
	client := m.clientForBackend(backend)
	if client == nil {
		_ = s.Send(chatID, fmt.Sprintf("⚠️ 이 토픽은 %s로 만들어졌는데 %s가 설치되어 있지 않습니다.", strings.ToUpper(backend), strings.ToUpper(backend)))
		return
	}
	_ = m.store.SetActive(tgt.Project, c.ID)
	m.runWorker(ctx, chatID, text, m.projectSink(tgt.Project), "", c, s, client, backend)
}

// projectNames returns registered project names (helper).
func projectNames(store StoreRepo) []string {
	out := make([]string, 0)
	for name := range store.ListProjects() {
		out = append(out, name)
	}
	return out
}
```

Task 4의 임시 `handleWeb` 스텁을 target 없는 기본(텔레그램 대상)으로 위임하도록 교체:

```go
// handleWeb handles a web send with no explicit target (legacy path): default to
// the telegram stream, the always-present conversation.
func (m *Manager) handleWeb(ctx context.Context, chatID int64, text string, s MessageSender) {
	m.HandleWebTarget(ctx, chatID, text, Target{Kind: "telegram"}, s)
}
```

- [ ] **Step 5: 프로토콜 배선** — `webchat.go`/`chatcontrol.go`

`webchat.go` `inMsg`에 target 추가 + `inject`가 target 전달:

```go
type inMsg struct {
	Type   string  `json:"type"`
	Text   string  `json:"text"`
	Target *Target `json:"target,omitempty"`
}
```

`webServer`가 target을 매니저에 넘기려면 `inject`에 target 인자를 추가하고 `handleWS`에서 `go s.inject(m.Text, m.Target)`. `inject` 내부에서 비명령 텍스트는 `s.bot.dispatchTargeted(s.ownerChatID, text, m.Target)` 호출(신규). `bot.go`에 얇은 헬퍼:

```go
// dispatchTargeted enqueues a web message with an explicit target.
func (b *Bot) dispatchTargeted(chatID int64, text string, tgt *Target) {
	t := Target{Kind: "telegram"}
	if tgt != nil {
		t = *tgt
	}
	b.manager.HandleWebTarget(context.Background(), chatID, text, t, b)
}
```

(주의: 기존 `dispatchText`는 rate-limit/큐를 태운다. 웹 target 경로도 동일 rate-limit을 적용하려면 `inject`의 기존 rate-limit 체크를 유지한 뒤 `dispatchTargeted` 호출. 큐잉이 필요하면 기존 `queuedMsg`에 `target *Target`를 추가하고 워커 실행부에서 `HandleWebTarget` 분기. 최소 구현은 `inject`가 rate-limit 후 `dispatchTargeted` 직접 호출.)

`chatcontrol.go` `controlIn`에 `Target *Target` 추가, `send_text` 케이스에서 비명령 텍스트를 `go s.bot.dispatchTargeted(chatID, text, m.Target)`로.

- [ ] **Step 6: 통과 확인** — `go test -run TestHandleWebTarget . -v` → PASS. 전체 빌드/vet/test 그린.

- [ ] **Step 7: 커밋**

```bash
git add types.go manager.go webchat.go chatcontrol.go bot.go handle_web_test.go
git commit -m "feat: route web sends by explicit target (telegram stream vs web topic)"
```

---

## Task 6: `!chat` 웹 전용화 + `rename`

**Files:**
- Modify: `bot.go` (`handleChat`)
- Test: `chat_cmd_test.go` (Create)

**Interfaces:**
- Consumes: 기존 store 토픽 메서드.
- Produces: `handleChat`이 origin==telegram이면 안내 후 종료; `!chat rename <새 제목>` 추가(웹 활성 토픽 이름 변경).

- [ ] **Step 1: 실패 테스트** — `chat_cmd_test.go`

```go
package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleChat_TelegramRejected(t *testing.T) {
	b, _ := chatCmdBot(t)
	f := b.sender.(*fakeSender)
	b.handleChat(1, "!chat new x", []string{"!chat", "new", "x"}, OriginTelegram)
	joined := strings.Join(f.sent, "\n")
	if !strings.Contains(joined, "웹") {
		t.Errorf("telegram !chat should be rejected with web guidance, got %v", f.sent)
	}
}

func TestHandleChat_WebRename(t *testing.T) {
	b, st := chatCmdBot(t)
	c, _ := st.NewConversation("myapp", "old", OriginWeb)
	_ = st.SetActive("myapp", c.ID)
	b.handleChat(1, "!chat rename 새 제목", []string{"!chat", "rename", "새", "제목"}, OriginWeb)
	got, _ := st.GetConversation("myapp", c.ID)
	if got.Title != "새 제목" {
		t.Errorf("rename should update title, got %q", got.Title)
	}
}
```

`chatCmdBot` 헬퍼는 기존 bot 테스트 패턴을 따른다(예: `bot_test.go`의 생성 방식 재사용). 최소:

```go
func chatCmdBot(t *testing.T) (*Bot, *fileStore) {
	t.Helper()
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	_ = st.AddProject("myapp", t.TempDir())
	b := &Bot{store: st, sender: &fakeSender{}}
	return b, st
}
```

> `Bot` 구성 필드는 실제 구조체에 맞춘다(예: 메시지 전송이 `b.Send`를 타면 `fakeSender`를 물리는 기존 테스트 헬퍼 사용). 이 스텁이 실제와 다르면 `bot_test.go`의 헬퍼를 그대로 재사용할 것.

- [ ] **Step 2: 실패 확인** — `go test -run TestHandleChat . -v` → 실패.

- [ ] **Step 3: `handleChat` 수정** (`bot.go`)

함수 진입부에 origin 가드 추가:

```go
func (b *Bot) handleChat(chatID int64, text string, fields []string, origin string) {
	if origin != OriginWeb {
		_ = b.Send(chatID, "ℹ️ 텔레그램에서는 대화 주제를 관리하지 않습니다. 대화 주제는 웹에서 관리하세요. (텔레그램은 \"이제 <프로젝트명> 하자\"로 작업 대상만 전환)")
		return
	}
	if len(fields) < 2 {
		_ = b.Send(chatID, "사용법: !chat new [제목] | !chat list | !chat use <id> | !chat use <프로젝트> <id> | !chat rename <새 제목>")
		return
	}
	active := b.store.GetActive()
	switch fields[1] {
	// ... 기존 new/list/use 유지 ...
	case "rename":
		if active.Project == "" || active.ConversationID == "" {
			_ = b.Send(chatID, "이름을 바꿀 활성 웹 토픽이 없습니다. 먼저 토픽을 선택하세요.")
			return
		}
		newTitle := ""
		if parts := strings.SplitN(text, " ", 3); len(parts) == 3 {
			newTitle = strings.TrimSpace(parts[2])
		}
		if newTitle == "" {
			_ = b.Send(chatID, "사용법: !chat rename <새 제목>")
			return
		}
		c, ok := b.store.GetConversation(active.Project, active.ConversationID)
		if !ok {
			_ = b.Send(chatID, "활성 토픽을 찾을 수 없습니다.")
			return
		}
		c.Title = newTitle
		if err := b.store.UpdateConversation(active.Project, c); err != nil {
			_ = b.Send(chatID, "⚠️ 이름 변경 실패: "+err.Error())
			return
		}
		_ = b.Send(chatID, "✏️ 대화 이름을 변경했습니다: "+newTitle)
	default:
		_ = b.Send(chatID, "사용법: !chat new [제목] | !chat list | !chat use <id> | !chat use <프로젝트> <id> | !chat rename <새 제목>")
	}
}
```

- [ ] **Step 4: 통과 확인** — `go test -run TestHandleChat . -v` → PASS. 전체 그린.

- [ ] **Step 5: 커밋**

```bash
git add bot.go chat_cmd_test.go
git commit -m "feat(bot): !chat is web-only + add !chat rename"
```

---

## Task 7: 웹 응답에 텔레그램 전역 항목 + `get_history`

**Files:**
- Modify: `types.go` (응답/히스토리 타입), `webchat.go` (응답 확장, `/api/history`, `buildHistoryResponse`), `chatcontrol.go` (`get_history`)
- Test: `history_api_test.go` (Create)

**Interfaces:**
- Produces:
  - `webConversationsResponse.Telegram *webTelegramEntry` where `type webTelegramEntry struct { Title string; ID string; Active bool; Backend string; Project string }`.
  - `type historyTurn struct { Role string; Text string }` and `type historyResponse struct { Turns []historyTurn }`.
  - `func buildHistoryResponse(store StoreRepo, tgt Target) historyResponse` — 텔레그램/웹 토픽 히스토리를 role/text 배열로.
  - webchat: `GET /api/history?kind=telegram` 또는 `?kind=web&project=..&id=..`.
  - chatcontrol: `controlIn.Type == "get_history"`(target 포함) → reply(historyResponse).

- [ ] **Step 1: 실패 테스트** — `history_api_test.go`

```go
package main

import (
	"path/filepath"
	"testing"
	"time"
)

func histStore(t *testing.T) *fileStore {
	t.Helper()
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	_ = st.AddProject("myapp", t.TempDir())
	return st
}

func TestBuildHistoryResponse_Telegram(t *testing.T) {
	st := histStore(t)
	tc := st.TelegramConversation()
	tc.History = []ConversationTurn{{Timestamp: time.Now(), Prompt: "안녕", Response: "네"}}
	_ = st.UpdateTelegramConversation(tc)

	resp := buildHistoryResponse(st, Target{Kind: "telegram"})
	if len(resp.Turns) != 2 || resp.Turns[0].Role != "user" || resp.Turns[0].Text != "안녕" || resp.Turns[1].Role != "assistant" || resp.Turns[1].Text != "네" {
		t.Errorf("telegram history should expand to user/assistant turns, got %+v", resp.Turns)
	}
}

func TestBuildHistoryResponse_WebTopic(t *testing.T) {
	st := histStore(t)
	c, _ := st.NewConversation("myapp", "t", OriginWeb)
	c.History = []ConversationTurn{{Prompt: "q", Response: "a"}}
	_ = st.UpdateConversation("myapp", c)

	resp := buildHistoryResponse(st, Target{Kind: "web", Project: "myapp", ID: c.ID})
	if len(resp.Turns) != 2 || resp.Turns[1].Text != "a" {
		t.Errorf("web topic history wrong: %+v", resp.Turns)
	}
}

func TestBuildConversationsResponse_IncludesTelegram(t *testing.T) {
	st := histStore(t)
	_ = st.TelegramConversation()
	resp := buildConversationsResponse(st)
	if resp.Telegram == nil || resp.Telegram.ID != "telegram" {
		t.Errorf("conversations response must include the telegram global entry, got %+v", resp.Telegram)
	}
}
```

- [ ] **Step 2: 실패 확인** — `go test -run 'TestBuildHistoryResponse|TestBuildConversationsResponse_IncludesTelegram' . -v` → 실패.

- [ ] **Step 3: 응답 타입 확장** (`webchat.go`의 `webConversationsResponse` 정의부)

```go
type webTelegramEntry struct {
	Title   string `json:"title"`
	ID      string `json:"id"`
	Active  bool   `json:"active"`
	Backend string `json:"backend,omitempty"`
	Project string `json:"project,omitempty"` // current working-dir project
}

type webConversationsResponse struct {
	Active   ActiveRef          `json:"active"`
	Telegram *webTelegramEntry  `json:"telegram,omitempty"`
	Projects []webProjectTopics `json:"projects"`
}
```

`buildConversationsResponse`에 텔레그램 항목 채우기(끝부분):

```go
	if tc := store.TelegramConversation(); tc != nil {
		resp.Telegram = &webTelegramEntry{
			Title:   tc.Title,
			ID:      tc.ID,
			Backend: tc.Backend,
			Project: store.TelegramActiveProject(),
		}
	}
```

> `buildConversationsResponse(store StoreRepo)` 시그니처는 유지. `StoreRepo`에 `TelegramConversation()`/`TelegramActiveProject()`가 Task 1에서 추가됐으므로 호출 가능.

- [ ] **Step 4: `buildHistoryResponse`** (`webchat.go`)

```go
type historyTurn struct {
	Role string `json:"role"` // "user" | "assistant"
	Text string `json:"text"`
}
type historyResponse struct {
	Turns []historyTurn `json:"turns"`
}

// buildHistoryResponse expands a conversation's stored turns into a flat
// user/assistant sequence for the web log. Shared by /api/history and the
// chat-control get_history request.
func buildHistoryResponse(store StoreRepo, tgt Target) historyResponse {
	var conv *Conversation
	if tgt.Kind == "telegram" {
		conv = store.TelegramConversation()
	} else if c, ok := store.GetConversation(tgt.Project, tgt.ID); ok {
		conv = c
	}
	resp := historyResponse{Turns: []historyTurn{}}
	if conv == nil {
		return resp
	}
	for _, turn := range conv.History {
		if turn.Prompt != "" {
			resp.Turns = append(resp.Turns, historyTurn{Role: "user", Text: turn.Prompt})
		}
		if turn.Response != "" {
			resp.Turns = append(resp.Turns, historyTurn{Role: "assistant", Text: turn.Response})
		}
	}
	return resp
}
```

- [ ] **Step 5: 엔드포인트 배선**

`webchat.go` `Start()` mux에 `/api/history` 추가하고 핸들러:

```go
func (s *webServer) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	tgt := Target{Kind: r.URL.Query().Get("kind"), Project: r.URL.Query().Get("project"), ID: r.URL.Query().Get("id")}
	if tgt.Kind == "" {
		tgt.Kind = "telegram"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(buildHistoryResponse(s.bot.store, tgt))
}
```

`chatcontrol.go` `handleInbound`에 케이스 추가:

```go
	case "get_history":
		tgt := Target{Kind: "telegram"}
		if m.Target != nil {
			tgt = *m.Target
		}
		data, err := json.Marshal(buildHistoryResponse(s.bot.store, tgt))
		if err != nil {
			log.Printf("[chatcontrol] get_history marshal: %v", err)
			return
		}
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: data})
```

- [ ] **Step 6: 통과 확인** — 해당 테스트 PASS + 전체 그린.

- [ ] **Step 7: 커밋**

```bash
git add types.go webchat.go chatcontrol.go history_api_test.go
git commit -m "feat(web): telegram global entry in list + get_history (control + /api/history)"
```

---

## Task 8: 양쪽 web/app.js — 텔레그램 고정 항목 + 클릭 시 히스토리

**Files:**
- Modify: `Teleclaude/web/app.js`, `aglink-chat/web/app.js` (동일 변경)
- Test: `node --check` 양쪽 + 수동 스모크(선택)

**Interfaces:**
- Consumes: Task 5(send target), Task 7(응답 telegram 필드, get_history/api/history).
- Produces: 없음(프런트).

- [ ] **Step 1: 텔레그램 고정 항목 렌더 + currentTarget 상태**

`aglink-chat/web/app.js`와 `Teleclaude/web/app.js` 둘 다 동일하게:

`renderConversations(data)` 시작부에서 topic-list 최상단에 텔레그램 항목을 렌더:

```js
  let currentTarget = { kind: "telegram" }; // module-level state near `let ws`

  function makeTelegramButton(tg) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "topic telegram-topic";
    if (currentTarget.kind === "telegram") button.classList.add("active");
    const title = document.createElement("span");
    title.className = "topic-title";
    title.textContent = "📱 " + (tg.title || "텔레그램 대화");
    button.appendChild(title);
    if (tg.project) {
      const sub = document.createElement("span");
      sub.className = "topic-summary";
      sub.textContent = "작업: " + tg.project;
      button.appendChild(sub);
    }
    button.addEventListener("click", () => selectTarget({ kind: "telegram" }));
    return button;
  }
```

`renderConversations` 내 `topicList.replaceChildren();` 직후:

```js
    if (data && data.telegram) {
      topicList.appendChild(makeTelegramButton(data.telegram));
    }
```

- [ ] **Step 2: 웹 토픽 클릭을 `selectTarget`로 통일**

`makeConvButton`의 클릭 핸들러를 target 방식으로 교체(더는 `!chat use`만 보내지 않고 히스토리를 로드):

```js
    button.addEventListener("click", () => {
      if (!conv.id) return;
      // Keep the shared active pointer in sync for the sidebar highlight.
      sendText("!chat use " + project.name + " " + conv.id, false);
      selectTarget({ kind: "web", project: project.name, id: conv.id });
    });
```

- [ ] **Step 3: `selectTarget` — 히스토리 로드 후 실시간**

```js
  async function selectTarget(tgt) {
    currentTarget = tgt;
    // Load and render stored history, then live frames append after it.
    try {
      const qs = tgt.kind === "telegram"
        ? "kind=telegram"
        : "kind=web&project=" + encodeURIComponent(tgt.project) + "&id=" + encodeURIComponent(tgt.id);
      const resp = await fetch("/api/history?" + qs, { headers: { Authorization: "Bearer " + token } });
      if (resp.ok) {
        const data = await resp.json();
        log.replaceChildren();
        for (const turn of (data.turns || [])) {
          add(turn.role === "user" ? "user" : "assistant", turn.text);
        }
      }
    } catch (e) { /* keep whatever is shown; live continues */ }
    loadConversations(); // refresh highlight
  }
```

- [ ] **Step 4: 전송 시 target 포함**

`sendText`가 target을 실어 보내도록(웹 UI가 보내는 send에 currentTarget 부착):

```js
  function sendText(text, echo) {
    if (!text || !ws || ws.readyState !== WebSocket.OPEN) return false;
    if (echo) add("user", text);
    ws.send(JSON.stringify({ type: "send", text, target: currentTarget }));
    return true;
  }
```

(주의: `!chat use ...`를 보낼 때도 target이 붙지만 서버는 command면 target을 무시하므로 무해. command는 `handleCommand`로 가고 target은 send_text에서만 쓰인다.)

- [ ] **Step 5: 초기 타깃**

페이지 로드시 기본 타깃을 텔레그램으로 두고, 첫 `connect().onopen`에서 `selectTarget(currentTarget)`로 히스토리 표시:

`ws.onopen`의 `loadConversations();`를 `selectTarget(currentTarget);`로 교체(내부에서 loadConversations도 호출됨).

- [ ] **Step 6: (선택) 스타일** — `.telegram-topic.active`가 일반 topic과 구분되도록 `web/style.css` 양쪽에 한 줄(선택):

```css
.telegram-topic { border-bottom: 1px solid #cbd5e1; }
```

- [ ] **Step 7: 검증** — `node --check Teleclaude/web/app.js` 와 `node --check ../aglink-chat/web/app.js` 각각 통과. 두 파일 내용이 동일한지 `diff` 확인(마이그레이션 중 두 UI 동일 유지).

- [ ] **Step 8: 커밋** (두 레포 별도 커밋)

```bash
# teleclaude
git add web/app.js web/style.css
git commit -m "feat(web): pinned telegram entry + load history on conversation select"
# aglink-chat (별도 레포)
cd ../aglink-chat && git add web/app.js web/style.css && git commit -m "feat(web): pinned telegram entry + load history on conversation select"
```

---

## 통합 검증 (모든 태스크 후)

- [ ] `cd Teleclaude && go build ./... && go vet ./... && go test ./...` 그린.
- [ ] `cd ../aglink-chat && go build ./... && go vet ./... && go test ./...` 그린, `node --check web/app.js`.
- [ ] 안전 재시작(활성 워커 자연종료 확인 후) → 라이브 스모크:
  - 텔레그램: 일반 메시지 이어짐(단일 스트림). "이제 <B> 하자" → 작업 프로젝트만 전환, 같은 대화 지속.
  - 텔레그램: `!chat new` → "웹에서 관리" 안내.
  - 웹: 최상단 "📱 텔레그램 대화" 클릭 → 과거 히스토리 표시 + 이어서 전송 시 텔레그램 스트림에 반영.
  - 웹: 프로젝트 토픽 클릭 → 그 토픽 히스토리 표시 + 그 토픽만 이어짐. `#current-topic` 편집 아이콘/`!chat rename`으로 이름 변경.
- [ ] 최종 전체 리뷰(subagent-driven-development의 최종 whole-branch 리뷰).

## 미해결/후속 (YAGNI 경계)

- 텔레그램 continuation을 in-place 세션 리셋으로 처리(체인 레코드 미보존). 과거 전체 turn 보존이 필요해지면 별도 텔레그램 체인 저장을 후속으로.
- `#current-topic` 편집 아이콘(rename UI)은 Task 8 범위 밖(별도 소규모 작업). `!chat rename`은 Task 6에서 이미 동작.
- **크로스채널 라이브 프레임 필터링(중요, 이번 범위 밖)**: 현재 Hub는 한 owner의 모든 채널(텔레그램 + 모든 웹 브라우저)에 라이브 프레임(Send/Typing/Done)을 무차별 팬아웃한다. 새 모델에선 웹 토픽을 보는 브라우저가 텔레그램/다른 토픽의 라이브 출력을 같이 보게 된다(과거 "크로스채널 미러링"은 의도된 동작이었음). **히스토리 표시(Task 7/8)로 핵심 문제는 해결**되지만, 라이브 출력을 currentTarget에 맞춰 거르려면 프레임에 target/source 태그를 실어 브라우저가 필터링하는 별도 작업이 필요하다. 사용자 피드백(포크·히스토리 미표시)의 범위 밖이라 후속으로 분리. 필요 시 별도 spec.
