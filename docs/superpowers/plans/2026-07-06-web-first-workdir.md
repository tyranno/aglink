# 웹 우선 + 대화별 작업폴더 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 웹을 텔레그램과 완전히 독립시키고(기본 대상=웹 대화), 텔레그램·웹 모든 대화의 작업 디렉토리를 대화별로 두되 기본값을 서비스 홈(`~/teleclaude`)으로 한다.

**Architecture:** `Config.HomeDir`(기본 `~/teleclaude`, 없으면 생성)를 도입. `Conversation.WorkDir`(대화별 작업폴더, "" → 홈) 추가. 웹 대화는 프로젝트에서 분리해 `StoreData.WebConvs`(최상위 맵)로 이동. 텔레그램은 활성 프로젝트가 없으면 홈에서 동작(에러 제거). 웹 UI는 기본 대상을 웹 대화로 두고 "+"가 홈-기본 웹 대화를 생성·전환하며 대화별 "설정"으로 폴더를 바꾼다.

**Tech Stack:** Go 1.25, `github.com/coder/websocket`, go:embed, Vanilla JS (web/app.js ×2).

## Global Constraints

- 서비스 기본 홈 = `Config.HomeDir`, 기본값 `filepath.Join(userHome, "teleclaude")`, 없으면 `os.MkdirAll`로 생성. config yaml 키 `home_dir`. 나중에 설정 UI로 변경 가능하도록 config 필드로 둔다.
- 대화별 작업폴더 `Conversation.WorkDir`; `""`이면 홈을 쓴다. 텔레그램·웹 공통.
- 웹은 텔레그램과 독립: 웹 기본 대상은 웹 대화이며, 텔레그램 스트림은 사용자가 "📱 텔레그램 대화" 항목을 명시적으로 클릭했을 때만 대상이 된다. 웹 안내 문구는 절대 "텔레그램에서 하라"고 하지 않는다.
- 텔레그램은 활성 프로젝트가 없어도 홈에서 동작한다("프로젝트 없음" 차단 메시지 제거). `!project`/"이제 <프로젝트> 하자"로 등록된 폴더 전환은 유지(선택).
- 웹 대화는 `StoreData.WebConvs`(최상위)에 저장하며 프로젝트에 속하지 않는다. 프로젝트(`StoreData.Projects`)는 텔레그램 폴더 전환용으로 유지.
- 각 단계 `go build ./... && go vet ./... && go test ./...` 그린. Windows gofmt는 CRLF 오탐 → `tr -d '\r' < <file> | gofmt -d`로 확인. 에디터/gopls 진단은 STALE — 실제 `go build`/`go test`만 신뢰.
- 경로 입력은 서버에서 존재·디렉토리 검증(같은 PC). push 금지(로컬 커밋). 커밋 trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- 두 web/app.js(`Teleclaude/web/`, `../aglink-chat/web/`)에 동일 기능 반영, `node --check` 양쪽 통과.

---

## File Structure

- `types.go` — `Config.HomeDir`, `Conversation.WorkDir`, `StoreData.WebConvs`, `StoreRepo` 웹대화 메서드, 스키마 버전 3.
- `config_yaml.go` — `home_dir` 매핑, 기본값.
- `store.go` — 웹대화 CRUD, 홈 기본 처리, `HistorySnapshot` 웹 분기, 스키마 리셋.
- `manager.go` — 홈 해석 헬퍼, 텔레그램 홈 폴백, `webConvSink`, `HandleWebTarget` 웹 분기(WebConvs+WorkDir), 웹대화 생성/설정 진입점.
- `bot.go` — 웹대화 관리 디스패치(생성/이름/폴더).
- `webchat.go` / `chatcontrol.go` — 응답에 웹대화 목록, `web_new`/`web_setdir`/`web_rename` 제어 op, get_history 웹 분기, 경로검증.
- `Teleclaude/web/app.js`, `Teleclaude/web/index.html`, `../aglink-chat/web/app.js` — 기본대상=웹대화, "+" 생성·전환, 대화별 "설정", 웹 우선 안내.

---

## Task 1: Config `home_dir` + 홈 디렉토리 보장

**Files:**
- Modify: `types.go` (Config), `config_yaml.go`
- Create: `homedir.go` (helper), `homedir_test.go`

**Interfaces:**
- Produces:
  - `Config.HomeDir string` (yaml `home_dir`).
  - `func defaultHomeDir() string` — `filepath.Join(userHome, "teleclaude")` (userHome from `os.UserHomeDir()`; on error, `"teleclaude"`).
  - `func resolveHomeDir(cfg *Config) string` — returns `cfg.HomeDir` if non-empty else `defaultHomeDir()`, and `os.MkdirAll`s it (best-effort; logs on failure). Always returns the path.

- [ ] **Step 1: 실패 테스트** — `homedir_test.go`

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveHomeDir_DefaultCreatesTeleclaudeUnderHome(t *testing.T) {
	cfg := &Config{} // no HomeDir → default
	got := resolveHomeDir(cfg)
	if !strings.HasSuffix(filepath.Clean(got), filepath.Join("", "teleclaude")) && filepath.Base(got) != "teleclaude" {
		t.Fatalf("default home should end in /teleclaude, got %q", got)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Errorf("resolveHomeDir must create the directory, stat err=%v", err)
	}
}

func TestResolveHomeDir_ExplicitOverride(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myhome")
	cfg := &Config{HomeDir: dir}
	got := resolveHomeDir(cfg)
	if got != dir {
		t.Errorf("explicit HomeDir should be used, got %q want %q", got, dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("explicit HomeDir must be created, err=%v", err)
	}
}
```

- [ ] **Step 2: 실패 확인** — `go test -run TestResolveHomeDir . -v` → 미정의.

- [ ] **Step 3: `types.go` Config 필드 추가** (기존 Config 구조체 안, 적절한 위치):

```go
	HomeDir string // 서비스 기본 작업 홈 (yaml home_dir); "" → <userHome>/teleclaude
```

- [ ] **Step 4: `homedir.go` 생성**

```go
package main

import (
	"log"
	"os"
	"path/filepath"
)

// defaultHomeDir is the service's default working home: <userHome>/teleclaude.
func defaultHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "teleclaude"
	}
	return filepath.Join(home, "teleclaude")
}

// resolveHomeDir returns the configured home dir (or the default) and ensures it
// exists. Used as the fallback working directory for any conversation whose
// WorkDir is unset.
func resolveHomeDir(cfg *Config) string {
	dir := cfg.HomeDir
	if dir == "" {
		dir = defaultHomeDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[home] could not create home dir %q: %v", dir, err)
	}
	return dir
}
```

- [ ] **Step 5: `config_yaml.go` — home_dir 매핑** — yaml 구조체에 필드 추가하고 양방향 매핑:

yaml 입력 구조체(예: 최상위 yamlConfig 등 기존 구조에 맞춰)에 `HomeDir string \`yaml:"home_dir"\`` 추가. `yamlToConfig`에서 `c.HomeDir = y.HomeDir`. `configToYAML`에서 `y.HomeDir = c.HomeDir`. 기본값은 비워두고(런타임 `resolveHomeDir`이 기본 처리) — 명시 설정만 저장.

- [ ] **Step 6: 통과 확인 + 커밋**

```bash
go test -run TestResolveHomeDir . -v && go build ./... && go vet ./... && go test ./...
git add types.go config_yaml.go homedir.go homedir_test.go
git commit -m "feat(config): home_dir (default ~/teleclaude) + resolveHomeDir"
```

---

## Task 2: `Conversation.WorkDir` + 최상위 `WebConvs` 저장

**Files:**
- Modify: `types.go` (Conversation, StoreData, StoreRepo, schema)
- Modify: `store.go`
- Test: `webconv_store_test.go`

**Interfaces:**
- Consumes: Task 1(`Config.HomeDir`).
- Produces:
  - `Conversation.WorkDir string` (json `workDir,omitempty`).
  - `StoreData.WebConvs map[string]*Conversation` (json `webConvs`).
  - `const storeSchemaVersion = 3` (bump — 기존 데이터 이미 초기화됨; 스키마 불일치 리셋 로직 재사용).
  - StoreRepo 추가: `NewWebConv(title string) (*Conversation, error)`, `GetWebConv(id string) (*Conversation, bool)`, `UpdateWebConv(c *Conversation) error`, `ListWebConvs() map[string]*Conversation`, `DeleteWebConv(id string) error`.
  - `HistorySnapshot`가 `tgt.Kind=="web"`일 때 `WebConvs[tgt.ID]`를 본다.

- [ ] **Step 1: 실패 테스트** — `webconv_store_test.go`

```go
package main

import (
	"path/filepath"
	"testing"
)

func TestWebConv_CRUD(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	c, err := st.NewWebConv("첫 웹 대화")
	if err != nil || c.ID == "" || c.SessionID == "" {
		t.Fatalf("NewWebConv failed: %v %+v", err, c)
	}
	if c.Origin != OriginWeb {
		t.Errorf("web conv origin should be web, got %q", c.Origin)
	}
	c.WorkDir = "C:/tmp/x"
	if err := st.UpdateWebConv(c); err != nil {
		t.Fatal(err)
	}
	got, ok := st.GetWebConv(c.ID)
	if !ok || got.WorkDir != "C:/tmp/x" {
		t.Errorf("web conv workdir not persisted: %+v", got)
	}
	if len(st.ListWebConvs()) != 1 {
		t.Errorf("expected 1 web conv, got %d", len(st.ListWebConvs()))
	}
	if err := st.DeleteWebConv(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.GetWebConv(c.ID); ok {
		t.Error("web conv should be deleted")
	}
}

func TestHistorySnapshot_WebConv(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	c, _ := st.NewWebConv("t")
	c.History = []ConversationTurn{{Prompt: "q", Response: "a"}}
	_ = st.UpdateWebConv(c)
	turns := st.HistorySnapshot(Target{Kind: "web", ID: c.ID})
	if len(turns) != 1 || turns[0].Prompt != "q" {
		t.Errorf("web history snapshot wrong: %+v", turns)
	}
}
```

- [ ] **Step 2: 실패 확인** — `go test -run 'TestWebConv_CRUD|TestHistorySnapshot_WebConv' . -v` → 미정의.

- [ ] **Step 3: `types.go` 변경**

`Conversation`에 필드 추가:

```go
	WorkDir string `json:"workDir,omitempty"` // per-conversation working directory; "" → service home
```

`StoreData`에 필드 추가(기존 필드 옆):

```go
	WebConvs map[string]*Conversation `json:"webConvs,omitempty"` // top-level web conversations (project-independent)
```

`storeSchemaVersion`를 `3`으로 변경.

`StoreRepo` 인터페이스에 5개 메서드 추가:

```go
	NewWebConv(title string) (*Conversation, error)
	GetWebConv(id string) (*Conversation, bool)
	UpdateWebConv(c *Conversation) error
	ListWebConvs() map[string]*Conversation
	DeleteWebConv(id string) error
```

- [ ] **Step 4: `store.go` — WebConvs 구현**

`newEmptyStore()`에 `WebConvs: map[string]*Conversation{}` 포함. `Load()`의 nil-map 보정에 `if d.WebConvs == nil { d.WebConvs = map[string]*Conversation{} }` 추가.

메서드:

```go
func (s *fileStore) NewWebConv(title string) (*Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.WebConvs == nil {
		s.data.WebConvs = map[string]*Conversation{}
	}
	id := nextConvID(s.data.WebConvs)
	if title == "" {
		title = "웹 대화 " + id
	}
	backend := s.data.ActiveBackend
	if backend == "" {
		backend = "claude"
	}
	c := &Conversation{
		ID:           id,
		Title:        title,
		SessionID:    newUUID(),
		Started:      false,
		LastActivity: time.Now().UTC(),
		Backend:      backend,
		Origin:       OriginWeb,
	}
	s.data.WebConvs[id] = c
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *fileStore) GetWebConv(id string) (*Conversation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.data.WebConvs[id]
	return c, ok
}

func (s *fileStore) UpdateWebConv(c *Conversation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.WebConvs == nil {
		s.data.WebConvs = map[string]*Conversation{}
	}
	s.data.WebConvs[c.ID] = c
	return s.saveLocked()
}

func (s *fileStore) ListWebConvs() map[string]*Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]*Conversation, len(s.data.WebConvs))
	maps.Copy(out, s.data.WebConvs)
	return out
}

func (s *fileStore) DeleteWebConv(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.WebConvs, id)
	if s.data.Active.ConversationID == id && s.data.Active.Project == "" {
		s.data.Active = ActiveRef{}
	}
	return s.saveLocked()
}
```

`HistorySnapshot`(Task 1의 이전 재설계에서 추가됨) 웹 분기 수정 — `tgt.Kind=="web"`이고 `tgt.Project==""`이면 `s.data.WebConvs[tgt.ID]`를 보도록:

```go
func (s *fileStore) HistorySnapshot(tgt Target) []ConversationTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	var conv *Conversation
	switch {
	case tgt.Kind == "telegram":
		conv = s.data.TelegramConv
	case tgt.Kind == "web" && tgt.Project == "":
		conv = s.data.WebConvs[tgt.ID]
	default: // legacy project-scoped topic (kept for safety)
		if p, ok := s.data.Projects[tgt.Project]; ok {
			conv = p.Conversations[tgt.ID]
		}
	}
	if conv == nil {
		return nil
	}
	out := make([]ConversationTurn, len(conv.History))
	copy(out, conv.History)
	return out
}
```

- [ ] **Step 5: 통과 + 커밋**

```bash
go test -run 'TestWebConv_CRUD|TestHistorySnapshot_WebConv' . -v && go build ./... && go vet ./... && go test ./...
git add types.go store.go webconv_store_test.go
git commit -m "feat(store): top-level web conversations + per-conversation WorkDir (schema 3)"
```

---

## Task 3: 매니저 — 텔레그램 홈 폴백 + 웹 라우팅을 WebConvs로

**Files:**
- Modify: `manager.go`
- Test: `webfirst_route_test.go`

**Interfaces:**
- Consumes: Task 1(`resolveHomeDir`), Task 2(WebConvs, `Conversation.WorkDir`).
- Produces:
  - `webConvSink` — `save`=`UpdateWebConv`, `setActive`=`SetActive("", c.ID)`, `makeContinuation`=in-place reset(텔레그램과 동일), `project()`=`""`.
  - `func (m *Manager) webConvSink() convSink`.
  - `handleTelegram`/`resolveTelegramProject`: 활성 프로젝트 없으면 홈(`resolveHomeDir(m.cfg())`) 작업디렉토리로 동작, 차단 메시지 제거.
  - `HandleWebTarget` 웹 분기: `GetWebConv(tgt.ID)`로 조회, workDir = `conv.WorkDir` 또는 홈, `webConvSink` 사용.

- [ ] **Step 1: 실패 테스트** — `webfirst_route_test.go`

```go
package main

import (
	"context"
	"path/filepath"
	"testing"
)

// A web send to a web conversation runs in that conv's WorkDir (or home) and
// persists to WebConvs, never touching projects or telegram.
func TestHandleWebTarget_WebConv_UsesWorkDir(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	home := t.TempDir()
	m := NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true, HomeDir: home}))
	c, _ := st.NewWebConv("web")
	wd := t.TempDir()
	c.WorkDir = wd
	_ = st.UpdateWebConv(c)

	f := &fakeSender{}
	m.HandleWebTarget(context.Background(), 1, "hi", Target{Kind: "web", ID: c.ID}, f)

	if fc.lastRun.WorkDir != wd {
		t.Errorf("web conv turn should run in its WorkDir %q, got %q", wd, fc.lastRun.WorkDir)
	}
	got, _ := st.GetWebConv(c.ID)
	if len(got.History) != 1 {
		t.Errorf("web conv should have the turn, got %d", len(got.History))
	}
}

// Telegram with no active project runs in the service home, not an error.
func TestHandleTelegram_NoProject_UsesHome(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	home := t.TempDir()
	m := NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true, HomeDir: home}))

	f := &fakeSender{}
	m.Handle(context.Background(), 1, "그냥 해줘", OriginTelegram, f)

	if fc.runCalls != 1 {
		t.Fatalf("telegram with no project should still run in home, runCalls=%d", fc.runCalls)
	}
	if fc.lastRun.WorkDir != home {
		t.Errorf("telegram no-project turn should run in home %q, got %q", home, fc.lastRun.WorkDir)
	}
}
```

- [ ] **Step 2: 실패 확인** — `go test -run 'TestHandleWebTarget_WebConv_UsesWorkDir|TestHandleTelegram_NoProject_UsesHome' . -v`.

- [ ] **Step 3: `webConvSink`** (`manager.go`)

```go
// webConvSink persists a top-level web conversation (project-independent).
type webConvSink struct {
	m *Manager
}

func (w webConvSink) project() string { return "" }
func (w webConvSink) save(c *Conversation) error { return w.m.store.UpdateWebConv(c) }
func (w webConvSink) setActive(c *Conversation) error { return w.m.store.SetActive("", c.ID) }
func (w webConvSink) makeContinuation(c *Conversation) (*Conversation, error) {
	c.SessionID = newUUID()
	c.Started = false
	return c, nil
}

func (m *Manager) newWebConvSink() convSink { return webConvSink{m: m} }
```

> **`project()==""` 부수효과 주의:** `runWorker`는 `sink.project()`를 `workerStatus.SetStatus`/`UpdateStatus`와 `WriteHistory(project, title, ...)`에도 쓴다. `project==""`이면: (a) workerStatus는 빈 project로 기록돼도 무방(조회 키만 달라짐); (b) `WriteHistory("", title, ...)`가 이상한 경로를 만들 수 있으니, `runWorker`에서 `project==""`일 때는 `WriteHistory` 호출을 건너뛰거나 `project`를 `"web"` 라벨로 대체한다(구현자가 `WriteHistory` 시그니처를 확인해 안전한 쪽 선택). 이 처리를 Step 3의 runWorker 수정에 포함할 것.

> `runWorker`의 `GetProject(project)` 조회는 `project()==""`일 때 실패한다. 그래서 웹 대화 경로는 프로젝트 조회를 우회해야 한다. **간단한 해법**: `runWorker` 초입의 `p, ok := m.store.GetProject(project); if !ok { ... return }`를 `project == ""`일 때는 건너뛰고 `workDir`를 그대로 쓰도록 수정한다:

`manager.go` runWorker 초입 수정:

```go
	project := sink.project()
	var pPath string
	if project != "" {
		p, ok := m.store.GetProject(project)
		if !ok {
			_ = s.Send(chatID, "⚠️ 프로젝트를 찾을 수 없습니다: "+project)
			return
		}
		pPath = p.Path
	}
	if workDir == "" {
		workDir = pPath
	}
	if workDir == "" {
		workDir = resolveHomeDir(m.cfg())
	}
```

(이후 `p` 사용처가 있으면 — 예: `readProjectMemory(p.Path)` — `pPath`로 대체하고, `project==""`이면 프로젝트 메모리는 건너뛴다.)

- [ ] **Step 4: `HandleWebTarget` 웹 분기 교체** (`manager.go`) — 기존 `GetConversation(tgt.Project, tgt.ID)` 대신 WebConvs:

```go
	// Web conversation target (top-level, project-independent).
	c, ok := m.store.GetWebConv(tgt.ID)
	if !ok {
		_ = s.Send(chatID, "웹 대화를 찾을 수 없습니다. 새 대화를 만들어 주세요.")
		return
	}
	backend := c.Backend
	if backend == "" {
		backend = "claude"
	}
	client := m.clientForBackend(backend)
	if client == nil {
		_ = s.Send(chatID, fmt.Sprintf("⚠️ 이 대화는 %s로 만들어졌는데 %s가 설치되어 있지 않습니다.", strings.ToUpper(backend), strings.ToUpper(backend)))
		return
	}
	workDir := c.WorkDir
	if workDir == "" {
		workDir = resolveHomeDir(m.cfg())
	}
	_ = m.store.SetActive("", c.ID)
	m.runWorker(ctx, chatID, text, m.newWebConvSink(), workDir, c, s, client, backend)
	return
```

- [ ] **Step 5: 텔레그램 홈 폴백** (`manager.go`) — `resolveTelegramProject`가 프로젝트 없을 때 차단하지 말고 홈을 쓰도록. `handleTelegram`가 프로젝트 경로 대신 홈 폴백을 쓰게:

`handleTelegram`의 프로젝트 결정부를 다음으로 교체(스케줄 처리 이후):

```go
	// Working directory: active project's path if set & valid, else service home.
	// No project is required — telegram always works, defaulting to home.
	project := m.store.TelegramActiveProject()
	if sw, ok := detectProjectSwitchIntent(text, projectNames(m.store)); ok {
		if sw != project {
			_ = m.store.SetTelegramActiveProject(sw)
			_ = s.Send(chatID, "📂 이제 "+sw+"에서 진행합니다.")
		}
		project = sw
	} else if hint != "" {
		if _, exists := m.store.GetProject(hint); exists && hint != project {
			_ = m.store.SetTelegramActiveProject(hint)
			_ = s.Send(chatID, "📂 이제 "+hint+"에서 진행합니다.")
			project = hint
		}
	}
	workDir := resolveHomeDir(m.cfg())
	if project != "" {
		if p, ok := m.store.GetProject(project); ok {
			workDir = p.Path
		}
	}
	tc := m.store.TelegramConversation()
	if tc.WorkDir != "" {
		workDir = tc.WorkDir
	}
	// ... backend selection unchanged ...
	m.telegramMu.Lock()
	defer m.telegramMu.Unlock()
	m.runWorker(ctx, chatID, text, m.telegramSink(project), workDir, tc, s, client, backend)
```

기존 `resolveTelegramProject`가 다른 곳에서 안 쓰이면 제거하고, 위 인라인 로직으로 대체(또는 `resolveTelegramProject`를 홈-폴백 반환하도록 리팩터). `telegramSink.project()`가 `""`여도 runWorker(Step 3 수정)로 홈에서 동작한다. `projectNames`가 0개여도 에러 없이 홈 사용.

- [ ] **Step 6: 통과 + 커밋**

```bash
go test -run 'TestHandleWebTarget_WebConv_UsesWorkDir|TestHandleTelegram_NoProject_UsesHome' . -v && go build ./... && go vet ./... && go test ./...
git add manager.go webfirst_route_test.go
git commit -m "feat(manager): web conversations run in per-conv WorkDir; telegram defaults to home"
```

---

## Task 4: 웹대화 관리 op + 응답 + 엔드포인트 + 경로검증

**Files:**
- Modify: `bot.go`, `webchat.go`, `chatcontrol.go`, `types.go`(응답 구조)
- Test: `webconv_api_test.go`

**Interfaces:**
- Consumes: Task 2(WebConvs), Task 3(라우팅).
- Produces:
  - Bot 메서드: `webNew(chatID int64, title string, s ...)` → `NewWebConv`, 알림. `webSetDir(chatID, id, path string)` → 경로검증 후 `WorkDir` 갱신. `webRename(chatID, id, title string)`.
  - 제어/WS op: `web_new {title}`, `web_setdir {id,path}`, `web_rename {id,title}` (webchat inMsg + chatcontrol controlIn).
  - 응답: `webConversationsResponse`에 `WebConvs []webWebConv` 추가(각 `{id,title,workDir,active,backend}`). `buildConversationsResponse`가 `ListWebConvs()`로 채움. (기존 `Projects`는 텔레그램 폴더 참고용으로 유지하되 웹 UI는 WebConvs를 씀.)
  - `validateDir(path string) error` — `os.Stat` + `IsDir`.

- [ ] **Step 1: 실패 테스트** — `webconv_api_test.go`

```go
package main

import (
	"path/filepath"
	"testing"
)

func TestBuildConversationsResponse_ListsWebConvs(t *testing.T) {
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	c, _ := st.NewWebConv("웹1")
	_ = c
	resp := buildConversationsResponse(st)
	if len(resp.WebConvs) != 1 || resp.WebConvs[0].Title != "웹1" {
		t.Errorf("response should list web convs, got %+v", resp.WebConvs)
	}
}

func TestValidateDir(t *testing.T) {
	dir := t.TempDir()
	if err := validateDir(dir); err != nil {
		t.Errorf("existing dir should validate: %v", err)
	}
	if err := validateDir(filepath.Join(dir, "nope")); err == nil {
		t.Error("missing dir should fail validation")
	}
}
```

- [ ] **Step 2: 실패 확인** — `go test -run 'TestBuildConversationsResponse_ListsWebConvs|TestValidateDir' . -v`.

- [ ] **Step 3: 응답 구조** (`webchat.go` 또는 types.go, 기존 `webConversationsResponse` 옆)

```go
type webWebConv struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	WorkDir string `json:"workDir,omitempty"`
	Active  bool   `json:"active"`
	Backend string `json:"backend,omitempty"`
}
```

`webConversationsResponse`에 `WebConvs []webWebConv \`json:"webConvs"\`` 추가. `buildConversationsResponse`에서:

```go
	active := store.GetActive()
	webConvs := store.ListWebConvs()
	ids := make([]string, 0, len(webConvs))
	for id := range webConvs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return webConvs[ids[i]].LastActivity.After(webConvs[ids[j]].LastActivity) })
	resp.WebConvs = make([]webWebConv, 0, len(ids))
	for _, id := range ids {
		c := webConvs[id]
		resp.WebConvs = append(resp.WebConvs, webWebConv{
			ID: c.ID, Title: c.Title, WorkDir: c.WorkDir,
			Active:  active.Project == "" && active.ConversationID == c.ID,
			Backend: c.Backend,
		})
	}
```

- [ ] **Step 4: `validateDir` + Bot 메서드** (`bot.go`)

```go
func validateDir(path string) error {
	if path == "" {
		return fmt.Errorf("경로가 비어 있습니다")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("경로를 찾을 수 없습니다: %s", path)
	}
	if !fi.IsDir() {
		return fmt.Errorf("디렉토리가 아닙니다: %s", path)
	}
	return nil
}

func (b *Bot) webNew(chatID int64, title string) {
	c, err := b.store.NewWebConv(title)
	if err != nil {
		_ = b.Send(chatID, "⚠️ 새 웹 대화 생성 실패: "+err.Error())
		return
	}
	_ = b.store.SetActive("", c.ID)
	_ = b.Send(chatID, "🆕 새 웹 대화: "+c.Title)
}

func (b *Bot) webSetDir(chatID int64, id, path string) {
	c, ok := b.store.GetWebConv(id)
	if !ok {
		_ = b.Send(chatID, "웹 대화를 찾을 수 없습니다.")
		return
	}
	if err := validateDir(path); err != nil {
		_ = b.Send(chatID, "⚠️ "+err.Error())
		return
	}
	c.WorkDir = path
	if err := b.store.UpdateWebConv(c); err != nil {
		_ = b.Send(chatID, "⚠️ 설정 실패: "+err.Error())
		return
	}
	_ = b.Send(chatID, "📁 작업 폴더 설정: "+path)
}

func (b *Bot) webRename(chatID int64, id, title string) {
	c, ok := b.store.GetWebConv(id)
	if !ok || title == "" {
		_ = b.Send(chatID, "이름 변경 실패: 대화가 없거나 제목이 비었습니다.")
		return
	}
	c.Title = title
	if err := b.store.UpdateWebConv(c); err != nil {
		_ = b.Send(chatID, "⚠️ 이름 변경 실패: "+err.Error())
		return
	}
	_ = b.Send(chatID, "✏️ 이름 변경: "+title)
}
```

- [ ] **Step 5: 제어/WS op 배선**

`webchat.go` `inMsg`에 필드 추가: `ID string \`json:"id,omitempty"\``, `Path string \`json:"path,omitempty"\``, `Title string \`json:"title,omitempty"\``. `handleWS` 리더에서 `m.Type` 분기 추가:

```go
	switch m.Type {
	case "send":
		go s.inject(m.Text, m.Target)
	case "web_new":
		go s.bot.webNew(s.ownerChatID, m.Title)
	case "web_setdir":
		go s.bot.webSetDir(s.ownerChatID, m.ID, m.Path)
	case "web_rename":
		go s.bot.webRename(s.ownerChatID, m.ID, m.Title)
	}
```

`chatcontrol.go` `controlIn`에 동일 필드(`ID`,`Path`,`Title`) 추가, `handleInbound` switch에 `web_new`/`web_setdir`/`web_rename` case를 동일하게 추가(`go s.bot.webNew(chatID, m.Title)` 등).

- [ ] **Step 6: 통과 + 커밋**

```bash
go test -run 'TestBuildConversationsResponse_ListsWebConvs|TestValidateDir' . -v && go build ./... && go vet ./... && go test ./...
git add bot.go webchat.go chatcontrol.go types.go webconv_api_test.go
git commit -m "feat(web): web conversation ops (new/setdir/rename) + list in response"
```

---

## Task 5: 웹 UI (양쪽) — 기본대상=웹대화, "+" 생성·전환, 대화별 설정

**Files:**
- Modify: `Teleclaude/web/app.js`, `Teleclaude/web/index.html`, `../aglink-chat/web/app.js` (+ `../aglink-chat/web/index.html` 동일 시)
- Test: `node --check` 양쪽

**Interfaces:**
- Consumes: Task 4(`data.webConvs`, `web_new`/`web_setdir`/`web_rename`, `target {kind:web,id}`), Task 3(라우팅).

- [ ] **Step 1: 기본 대상/렌더 변경** — 두 app.js 공통:
  - `currentTarget` 기본을 `null`로 두고, `renderConversations`가 웹 대화 목록을 최상위 아래에 렌더. 활성 웹 대화가 있으면 그걸 기본 대상으로, 없으면 대상 없음(전송 시 안내).
  - 텔레그램 고정 항목은 유지하되 기본 대상 아님.
  - 웹 대화 목록을 `data.webConvs`로 렌더(각 항목 클릭 → `selectTarget({kind:"web", id})`; 각 항목에 ⚙ 설정 버튼 → 경로 prompt → `ws.send({type:"web_setdir", id, path})`).

```js
  function makeWebConvButton(wc) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "topic";
    if (currentTarget && currentTarget.kind === "web" && currentTarget.id === wc.id) button.classList.add("active");
    button.dataset.id = wc.id;
    const title = document.createElement("span");
    title.className = "topic-title";
    title.textContent = wc.title || wc.id;
    button.appendChild(title);
    if (wc.workDir) {
      const sub = document.createElement("span");
      sub.className = "topic-summary";
      sub.textContent = "📁 " + wc.workDir;
      button.appendChild(sub);
    }
    button.addEventListener("click", (e) => {
      if (e.target && e.target.dataset && e.target.dataset.gear) return;
      selectTarget({ kind: "web", id: wc.id });
    });
    const gear = document.createElement("span");
    gear.textContent = "⚙";
    gear.dataset.gear = "1";
    gear.style.cursor = "pointer";
    gear.style.marginLeft = "6px";
    gear.title = "작업 폴더 설정";
    gear.addEventListener("click", (ev) => {
      ev.stopPropagation();
      const path = prompt("이 대화의 작업 폴더 경로:", wc.workDir || "");
      if (path && ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "web_setdir", id: wc.id, path: path }));
        window.setTimeout(loadConversations, 400);
      }
    });
    button.appendChild(gear);
    return button;
  }
```

`renderConversations`에서 텔레그램 항목 렌더 직후, 웹 대화들을 렌더:

```js
    if (data && Array.isArray(data.webConvs)) {
      for (const wc of data.webConvs) topicList.appendChild(makeWebConvButton(wc));
    }
```

(기존 `projects` 그룹 렌더는 텔레그램 폴더 참고용으로 남겨도 되고, 웹 우선이면 접이식 또는 제거 — 최소 변경으로 남겨두되 웹 대화 목록을 위에 둔다.)

- [ ] **Step 2: "+" 새 웹 대화 생성·전환** — 기존 `newChat` 핸들러 교체:

```js
  if (newChat) newChat.addEventListener("click", () => {
    const title = prompt("새 대화 제목 (선택):", "") || "";
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify({ type: "web_new", title: title }));
    // After creation, refresh and select the newest web conv.
    window.setTimeout(async () => {
      await loadConversations();
      // pick the most-recent web conv as the new target
      try {
        const resp = await fetch("/api/conversations", { headers: { Authorization: "Bearer " + token } });
        const data = await resp.json();
        if (data.webConvs && data.webConvs.length) selectTarget({ kind: "web", id: data.webConvs[0].id });
      } catch (e) {}
    }, 400);
  });
```

- [ ] **Step 3: 기본 대상/전송/초기화** — `sendText`가 `currentTarget` 없으면 안내:

```js
  function sendText(text, echo) {
    if (!text) return false;
    if (!currentTarget) {
      add("system", "먼저 대화를 선택하거나 ＋로 새 대화를 만들어 주세요.");
      return false;
    }
    if (!ws || ws.readyState !== WebSocket.OPEN) return false;
    if (echo) add("user", text);
    ws.send(JSON.stringify({ type: "send", text, target: currentTarget }));
    return true;
  }
```

`ws.onopen`은 `loadConversations()`만 호출(자동으로 텔레그램 선택하지 않음). `loadConversations` 성공 후, 활성 웹 대화가 있으면 그걸 `selectTarget`, 없으면 대상 없음 유지:

```js
  // inside loadConversations after renderConversations(data):
  if (!currentTarget) {
    const act = (data.webConvs || []).find((w) => w.active);
    if (act) selectTarget({ kind: "web", id: act.id });
  }
```

- [ ] **Step 4: `selectTarget` 웹 쿼리** — 웹 타깃 히스토리 쿼리를 `kind=web&id=` 로(프로젝트 없음):

```js
      const qs = tgt.kind === "telegram"
        ? "kind=telegram"
        : "kind=web&id=" + encodeURIComponent(tgt.id);
```

- [ ] **Step 5: 검증** — `node --check Teleclaude/web/app.js` + `node --check ../aglink-chat/web/app.js` 통과. 두 파일의 Task 5 로직 동일 유지.

- [ ] **Step 6: 커밋 (두 레포)**

```bash
# teleclaude
git add web/app.js web/index.html
git commit -m "feat(web): web-first — default target is a web conversation, + creates & selects, per-conv workdir settings"
# aglink-chat
cd ../aglink-chat && git add web/app.js web/index.html && git commit -m "feat(web): web-first — default target is a web conversation, + creates & selects, per-conv workdir settings"
```

---

## 통합 검증 (모든 태스크 후)

- [ ] teleclaude `go build/vet/test ./...` 그린; aglink-chat `go build/vet/test ./...` + `node --check`.
- [ ] 재빌드·안전 재시작(활성 워커 없을 때) 후 라이브 스모크:
  - 웹만: `＋` → 제목 입력 → 새 웹 대화 생성·자동 선택 → 메시지 전송하면 홈(`~/teleclaude`)에서 동작(텔레그램 안 건드림). ⚙로 폴더 변경 → 그 폴더에서 동작.
  - 웹에서 "📱 텔레그램 대화" 클릭해야만 텔레그램 스트림 대상.
  - 텔레그램: 프로젝트 없이 메시지 → 홈에서 동작, "프로젝트 없음" 에러 안 뜸. "이제 <프로젝트> 하자"로 폴더 전환.
- [ ] 최종 whole-branch 리뷰.

## YAGNI / 후속

- OS 네이티브 폴더 선택창은 불가(브라우저 보안) — 경로 텍스트 입력 + 서버 검증으로 처리.
- `home_dir` 변경 UI는 이번 범위 밖(config 필드로만; 나중에 설정 화면).
- 기존 `Projects` 기반 `!chat new/list/use`(웹 토픽)는 웹이 WebConvs로 이동하면서 웹에서 미사용 — 텔레그램 폴더 전환용 `!project`만 유지. 필요 시 별도 정리 태스크.
