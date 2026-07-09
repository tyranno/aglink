package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Design Ref: §2.2, §4.1, §6.3 — routing orchestration, clarify, fallback. Application layer.

type Manager struct {
	client       ClaudeClient
	backendName  string // "claude" | "codex"
	backendMu    sync.RWMutex
	claudeClient ClaudeClient // preserved for switching back to claude
	codexClient  ClaudeClient // nil if codex not available

	store        StoreRepo
	workerStatus WorkerStatusStore
	scheduler    *Scheduler
	cfgh         *ConfigHolder

	telegramMu sync.Mutex // serializes turns on the single global telegram conversation

	// durationsMu/turnDurations back the "recent average" turn-time estimate
	// shown when a new turn starts — see recordTurnDuration/estimateTurnDuration.
	durationsMu   sync.Mutex
	turnDurations map[string][]time.Duration // backend name → rolling window of completed-turn durations
}

func NewManager(claude ClaudeClient, codex ClaudeClient, store StoreRepo, cfgh *ConfigHolder) *Manager {
	m := &Manager{
		claudeClient: claude,
		codexClient:  codex,
		store:        store,
		workerStatus: NewMemoryWorkerStatusStore(),
		cfgh:         cfgh,
	}
	// Default to claude when available (backward-compatible); otherwise fall back
	// to codex so a codex-only install still boots with a valid active client
	// instead of a nil m.client.
	if claude != nil {
		m.client = claude
		m.backendName = "claude"
	} else if codex != nil {
		m.client = codex
		m.backendName = "codex"
	}
	return m
}

func (m *Manager) cfg() *Config { return m.cfgh.Get() }

func (m *Manager) SetScheduler(s *Scheduler) { m.scheduler = s }

// SetBackend switches the active AI backend and persists the choice. Returns an
// error if the requested backend is unavailable.
func (m *Manager) SetBackend(name string) error {
	return m.setBackend(name, true)
}

// setBackend switches the active backend. persist controls whether the choice is
// written to the store — startup restoration passes false so that falling back to
// an installed backend (when the preferred one is missing) never clobbers the
// user's saved preference.
func (m *Manager) setBackend(name string, persist bool) error {
	m.backendMu.Lock()
	defer m.backendMu.Unlock()
	switch name {
	case "claude":
		if m.claudeClient == nil {
			return fmt.Errorf("Claude가 설치되어 있지 않습니다")
		}
		m.client = m.claudeClient
		m.backendName = "claude"
		log.Printf("[manager] backend → claude (worker_model=%q)", m.cfg().WorkerModel)
	case "codex":
		if m.codexClient == nil {
			return fmt.Errorf("Codex가 설치되어 있지 않습니다")
		}
		m.client = m.codexClient
		m.backendName = "codex"
		log.Printf("[manager] backend → codex (codex_model=%q codex_manager_model=%q)", m.cfg().CodexModel, m.cfg().CodexManagerModel)
	default:
		return fmt.Errorf("알 수 없는 백엔드: %s (claude | codex)", name)
	}
	if persist {
		if err := m.store.SetStoredBackend(name); err != nil {
			log.Printf("[manager] backend persist failed: %v", err)
		}
	}
	return nil
}

// Backend returns the current backend name.
func (m *Manager) Backend() string {
	m.backendMu.RLock()
	defer m.backendMu.RUnlock()
	return m.backendName
}

// CodexAvailable reports whether codex is registered.
func (m *Manager) CodexAvailable() bool {
	return m.codexClient != nil
}

// detectBackendSwitchIntent checks if the message explicitly intends to switch the AI backend.
// Requires an explicit switch verb to avoid false positives when messages merely mention
// "codex" or "backend" in a non-switching context.
// Returns "codex" or "claude" if switching intent is detected, "" otherwise.
func detectBackendSwitchIntent(text string) string {
	lower := strings.ToLower(text)
	switchVerbs := []string{
		"전환", "바꿔", "변경", "switch", "써줘", "사용해줘", "사용해", "써", "바꿔줘", "전환해",
	}
	hasSwitchVerb := false
	for _, v := range switchVerbs {
		if strings.Contains(lower, v) {
			hasSwitchVerb = true
			break
		}
	}
	if !hasSwitchVerb {
		return ""
	}
	if strings.Contains(lower, "codex") {
		return "codex"
	}
	if strings.Contains(lower, "claude") {
		return "claude"
	}
	return ""
}

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

// projectNames returns the registered project names (unordered).
func projectNames(store StoreRepo) []string {
	projects := store.ListProjects()
	names := make([]string, 0, len(projects))
	for name := range projects {
		names = append(names, name)
	}
	return names
}

// Handle routes a free-text message to a worker. origin ("telegram"|"web") selects
// the channel: telegram is a single global conversation stream; web uses per-topic
// conversations (handleWeb, Task 5).
func (m *Manager) Handle(ctx context.Context, chatID int64, text, origin string, s MessageSender) {
	if origin == OriginWeb {
		m.handleWeb(ctx, chatID, text, s) // Task 5
		return
	}
	m.handleTelegram(ctx, chatID, text, s)
}

// handleTelegram continues the single global telegram conversation. A project-
// switch intent only moves the working-directory pointer; the conversation and
// its history stay one continuous stream. The telegram channel no longer does LLM
// project/conversation routing, but one manager LLM call still serves natural-
// language scheduling (and, incidentally, a project hint).
func (m *Manager) handleTelegram(ctx context.Context, chatID int64, text string, s MessageSender) {
	// Backend auto-switch pre-check (unchanged behavior).
	if target := detectBackendSwitchIntent(text); target != "" && target != m.Backend() {
		if err := m.SetBackend(target); err != nil {
			_ = s.Send(chatID, "⚠️ 백엔드 전환 실패: "+err.Error())
		} else {
			_ = s.Send(chatID, "🔄 백엔드 전환: "+strings.ToUpper(target))
		}
	}

	// One manager LLM call: honor a schedule request; otherwise keep the project hint.
	m.backendMu.RLock()
	routeClient := m.client
	m.backendMu.RUnlock()
	hint := ""
	if dec, err := routeClient.Route(ctx, m.buildRouteRequest(text)); err == nil {
		if dec.Action == ActionSchedule {
			m.handleSchedule(chatID, dec, s)
			return
		}
		hint = dec.Project
	}

	// Working directory: the active project's path if set & valid, else the service
	// home. No project is required — telegram always works, defaulting to home. A
	// switch intent (keyword, then the LLM hint) only moves the working-dir pointer;
	// the conversation stays one continuous stream.
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
	// A stale active pointer (project since removed) must not reach runWorker, which
	// would error "프로젝트를 찾을 수 없습니다"; treat it as no project → run in home.
	if project != "" {
		if _, exists := m.store.GetProject(project); !exists {
			project = ""
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
	// The global telegram stream always follows the manager's live active backend
	// (set by !backend / detectBackendSwitchIntent), not a value stamped on the
	// conversation at creation time — otherwise a backend switch would report
	// success but silently keep routing turns through the old backend forever
	// (tc.Backend, once set, was never updated by SetBackend).
	backend := m.Backend()
	client := m.clientForBackend(backend)
	if client == nil {
		_ = s.Send(chatID, fmt.Sprintf("⚠️ 텔레그램 대화는 %s로 생성됐는데 %s가 설치되어 있지 않습니다. `!backend`로 전환하거나 설치 후 다시 시도해 주세요.",
			strings.ToUpper(backend), strings.ToUpper(backend)))
		return
	}
	m.telegramMu.Lock()
	defer m.telegramMu.Unlock()
	m.runWorker(ctx, chatID, text, m.telegramSink(project), workDir, tc, s, client, backend)
}

// compactPrompt asks the Worker to externalize the conversation's durable
// facts into .teleclaude/memory.md instead of relying on the CLI session's
// ever-growing --resume context to carry them. Mirrors the manual /compact
// command in devladpopov/teleclaude, which the project journal names as a
// gap worth closing (see .teleclaude/memory.md, 2026-07-09).
const compactPrompt = "지금까지의 대화에서 앞으로도 참고해야 할 핵심 결정사항/사실을 이 프로젝트의 .teleclaude/memory.md에 정리해서 저장해줘(파일이 없으면 새로 만들고, 이미 있는 내용과 중복되면 병합·정리). 저장한 뒤에는 무엇을 저장했는지 3~5줄로 요약해서 답해줘."

// CompactTelegramConversation runs one Worker turn asking Claude to persist the
// telegram stream's key decisions to .teleclaude/memory.md, then drops the
// local history mirror and the CLI SessionID so the next turn starts a fresh,
// cheap session instead of resuming an ever-growing one. The externalized
// memory.md is already read into every Worker prompt (buildContextPrompt), so
// nothing is actually lost — just moved out of the expensive live session.
// State is left untouched if the compaction turn itself fails, so a failure
// never discards history that wasn't actually saved anywhere durable.
func (m *Manager) CompactTelegramConversation(ctx context.Context, chatID int64, s MessageSender) {
	m.telegramMu.Lock()
	defer m.telegramMu.Unlock()

	tc := m.store.TelegramConversation()
	// SessionID is pre-generated by TelegramConversation() even before the first
	// turn ever runs, so it can't signal "nothing to compact" — Started (set once
	// a turn actually completes) is the real indicator, same as isNewConv elsewhere.
	if len(tc.History) == 0 && !tc.Started {
		_ = s.Send(chatID, "압축할 대화 내용이 없습니다.")
		return
	}

	project := m.store.TelegramActiveProject()
	workDir := resolveHomeDir(m.cfg())
	if project != "" {
		if p, ok := m.store.GetProject(project); ok {
			workDir = p.Path
		}
	}
	if tc.WorkDir != "" {
		workDir = tc.WorkDir
	}

	backend := m.Backend()
	client := m.clientForBackend(backend)
	if client == nil {
		_ = s.Send(chatID, "⚠️ 백엔드를 사용할 수 없습니다.")
		return
	}

	s.Typing(chatID)
	_ = s.Send(chatID, "🗜 대화 압축 중...")

	res, err := client.Run(ctx, RunRequest{
		Prompt:    compactPrompt,
		WorkDir:   workDir,
		SessionID: tc.SessionID,
		Resume:    tc.Started,
		Model:     m.workerModelForBackendName(backend),
	})
	if err != nil {
		_ = s.Send(chatID, "⚠️ 압축 실패: "+err.Error())
		return
	}

	tc.History = nil
	tc.SessionID = ""
	tc.Started = false
	tc.Summary = truncate(res.Text, 80)
	if uerr := m.store.UpdateTelegramConversation(tc); uerr != nil {
		log.Printf("[manager] compact: update telegram conv: %v", uerr)
	}

	_ = sendChunked(s, chatID, "✅ 압축 완료 — 다음 대화는 새 세션으로 시작합니다.\n\n"+res.Text)
}

// handleWeb handles a web send with no explicit target (legacy path): default to
// the telegram stream, the always-present conversation.
func (m *Manager) handleWeb(ctx context.Context, chatID int64, text string, s MessageSender) {
	m.HandleWebTarget(ctx, chatID, text, Target{Kind: "telegram"}, s)
}

// HandleWebTarget routes a web send to its explicit target: the global telegram
// stream or a specific web topic. Web never does LLM project routing.
func (m *Manager) HandleWebTarget(ctx context.Context, chatID int64, text string, tgt Target, s MessageSender) {
	if tgt.Kind != "web" {
		// Telegram stream: the default for kind "telegram", and for any unknown
		// or empty kind (e.g. a not-yet-updated web client). Working directory
		// resolution mirrors handleTelegram: the active project's path if set &
		// valid, else the service home. No project is required — this must
		// always run, never error, even with zero projects registered.
		project := m.store.TelegramActiveProject()
		// A stale active pointer (project since removed) must not reach
		// runWorker, which would error "프로젝트를 찾을 수 없습니다"; treat it as
		// no project → run in home.
		if project != "" {
			if _, exists := m.store.GetProject(project); !exists {
				project = ""
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
		// See handleTelegram: the global stream follows the live active backend,
		// not the conversation's creation-time stamp.
		backend := m.Backend()
		client := m.clientForBackend(backend)
		if client == nil {
			_ = s.Send(chatID, fmt.Sprintf("⚠️ 텔레그램 대화는 %s로 생성됐는데 %s가 설치되어 있지 않습니다.", strings.ToUpper(backend), strings.ToUpper(backend)))
			return
		}
		m.telegramMu.Lock()
		defer m.telegramMu.Unlock()
		m.runWorker(ctx, chatID, text, m.telegramSink(project), workDir, tc, s, client, backend)
		return
	}

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
}

func (m *Manager) buildRouteRequest(text string) RouteRequest {
	const maxConvsPerProject = 10 // keep routing prompt lean
	projects := m.store.ListProjects()
	summaries := make([]ProjectSummary, 0, len(projects))
	for name, p := range projects {
		ids := sortedConvIDsByActivity(p.Conversations)
		if len(ids) > maxConvsPerProject {
			ids = ids[:maxConvsPerProject]
		}
		convs := make([]ConversationSummary, 0, len(ids))
		for _, id := range ids {
			c := p.Conversations[id]
			convs = append(convs, ConversationSummary{ID: c.ID, Title: c.Title, Summary: c.Summary})
		}
		summaries = append(summaries, ProjectSummary{Name: name, Conversations: convs})
	}
	return RouteRequest{Message: text, Projects: summaries, Active: m.store.GetActive()}
}

// chainInfo walks to the chain root and returns the base title and next series number.
func (m *Manager) chainInfo(project string, c *Conversation) (string, int) {
	baseTitle := c.Title
	seriesNum := 2
	cur := c
	for cur.ParentID != "" {
		parent, ok := m.store.GetParent(project, cur.ID)
		if !ok {
			break
		}
		baseTitle = parent.Title
		seriesNum++
		cur = parent
	}
	return baseTitle, seriesNum
}

// makeContinuation creates a new continuation conversation linked to parent.
func (m *Manager) makeContinuation(project string, parent *Conversation) (*Conversation, error) {
	baseTitle, seriesNum := m.chainInfo(project, parent)
	// A continuation belongs to the same channel as its parent series.
	newC, err := m.store.NewConversation(project, fmt.Sprintf("%s (시리즈 %d)", baseTitle, seriesNum), parent.Origin)
	if err != nil {
		return nil, err
	}
	newC.ParentID = parent.ID
	newC.IsContinuation = true
	parent.ChildID = newC.ID
	if uerr := m.store.UpdateConversation(project, parent); uerr != nil {
		log.Printf("[manager] update parent childID: %v", uerr)
	}
	return newC, nil
}

// convSink abstracts where a worker turn persists its conversation: a project
// topic (web) or the global telegram conversation. This keeps runWorker unaware
// of the two storage locations.
type convSink interface {
	project() string                 // workdir/status/history-log scope
	label() string                   // channel/project label for display & history filing
	save(c *Conversation) error      // persist updated conversation
	setActive(c *Conversation) error // update the channel's active pointer
	makeContinuation(c *Conversation) (*Conversation, error)
}

type projectSink struct {
	m    *Manager
	proj string
}

func (p projectSink) project() string { return p.proj }
func (p projectSink) label() string   { return p.proj }
func (p projectSink) save(c *Conversation) error {
	return p.m.store.UpdateConversation(p.proj, c)
}
func (p projectSink) setActive(c *Conversation) error {
	return p.m.store.SetActive(p.proj, c.ID)
}
func (p projectSink) makeContinuation(c *Conversation) (*Conversation, error) {
	return p.m.makeContinuation(p.proj, c)
}

func (m *Manager) projectSink(project string) convSink { return projectSink{m: m, proj: project} }

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
func (t telegramSink) label() string   { return "telegram" }
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

// webConvSink persists a top-level, project-independent web conversation. It has
// no project scope (project()==""), so runWorker skips project lookup/memory and
// runs in the conversation's WorkDir (or the service home). Continuation is an
// in-place session reset, matching the telegram stream's single-record behavior.
type webConvSink struct {
	m *Manager
}

func (w webConvSink) project() string            { return "" }
func (w webConvSink) label() string              { return "web" }
func (w webConvSink) save(c *Conversation) error { return w.m.store.UpdateWebConv(c) }
func (w webConvSink) setActive(c *Conversation) error {
	return w.m.store.SetActive("", c.ID)
}
func (w webConvSink) makeContinuation(c *Conversation) (*Conversation, error) {
	c.SessionID = newUUID()
	c.Started = false
	return c, nil
}

func (m *Manager) newWebConvSink() convSink { return webConvSink{m: m} }

// isContextOverflow detects Claude CLI "Prompt is too long" context limit errors.
func isContextOverflow(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "prompt is too long") ||
		strings.Contains(lower, "context length exceeded") ||
		strings.Contains(lower, "context window")
}

// isSessionNotFound detects a lost/absent CLI session — e.g. `--resume <id>`
// after a bot restart or CLI update, where the session store no longer has that
// ID. The CLI exits non-zero with "No conversation found with session ID: ...".
func isSessionNotFound(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "no conversation found") ||
		strings.Contains(lower, "session not found")
}

// workerModelForBackend returns the right model string based on the active backend.
func (m *Manager) workerModelForBackend() string {
	return m.workerModelForBackendName(m.Backend())
}

// workerModelForBackendName returns the worker model for a specific backend,
// independent of the globally active backend — so a conversation resumed on its
// own backend uses that backend's model, not the currently selected one.
func (m *Manager) workerModelForBackendName(backend string) string {
	if backend == "codex" {
		return m.cfg().CodexModel
	}
	return m.cfg().WorkerModel
}

// clientForBackend returns the CLI client for a backend name, or nil if that
// backend is not installed. Used to resume a conversation on the backend it was
// created with, regardless of the current global backend selection.
func (m *Manager) clientForBackend(name string) ClaudeClient {
	m.backendMu.RLock()
	defer m.backendMu.RUnlock()
	switch name {
	case "codex":
		return m.codexClient
	case "claude":
		return m.claudeClient
	default:
		return nil
	}
}

// turnDurationSamples caps the rolling window of completed-turn durations kept
// per backend for the startup estimate — recent conditions, not an
// all-time average that would drift stale as the workload changes.
const turnDurationSamples = 20

// recordTurnDuration appends a completed turn's wall-clock duration to the
// backend's rolling window. Called once a turn actually finishes (success or
// session-recovered success) — see the call site in runWorker.
func (m *Manager) recordTurnDuration(backend string, d time.Duration) {
	m.durationsMu.Lock()
	defer m.durationsMu.Unlock()
	if m.turnDurations == nil {
		m.turnDurations = make(map[string][]time.Duration)
	}
	ds := append(m.turnDurations[backend], d)
	if len(ds) > turnDurationSamples {
		ds = ds[len(ds)-turnDurationSamples:]
	}
	m.turnDurations[backend] = ds
}

// estimateTurnDuration returns the average of the backend's recent completed
// turns. ok is false with fewer than 3 samples — too little data to show
// without it reading as a made-up number. This is deliberately NOT a real
// ETA: there is no way to know upfront how many tool calls an agentic turn
// will take. It's an honest "here's roughly what recent turns have looked
// like" instead of the user waiting in total silence with no sense of scale.
func (m *Manager) estimateTurnDuration(backend string) (avg time.Duration, samples int, ok bool) {
	m.durationsMu.Lock()
	defer m.durationsMu.Unlock()
	ds := m.turnDurations[backend]
	if len(ds) < 3 {
		return 0, len(ds), false
	}
	var total time.Duration
	for _, d := range ds {
		total += d
	}
	return total / time.Duration(len(ds)), len(ds), true
}

// formatMinSec renders a duration as "N분 M초" (or "M초" under a minute),
// matching runHeartbeat's own elapsed-time formatting below.
func formatMinSec(d time.Duration) string {
	secs := int(d.Seconds())
	mins := secs / 60
	secs %= 60
	if mins == 0 {
		return fmt.Sprintf("%d초", secs)
	}
	return fmt.Sprintf("%d분 %d초", mins, secs)
}

// runHeartbeat sends a periodic "still going" text message every 2 minutes
// while a Worker turn is in flight, and — critically — also refreshes the
// channel's live "typing" signal on each tick (s.Typing), not just once at
// turn start. Without this, the web UI's "작업 진행 중" indicator was a client-
// side timer with no server-side confirmation the worker was still alive: a
// hung/dead worker and a slow-but-fine one looked identical until the turn
// eventually returned (or timed out). label distinguishes the normal run
// ("작업 진행 중") from a post-session-loss retry ("세션 복구 진행 중").
//
// When timeoutMinutes is set, the last heartbeat before the deadline switches
// to an explicit warning instead of the routine message, so a slow turn gets
// a heads-up (and a chance to !cancel) before the hard timeout silently kills
// it — rather than the user only finding out after the fact.
func runHeartbeat(s MessageSender, chatID int64, label string, startTime time.Time, timeoutMinutes int, done <-chan struct{}) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	var deadline time.Time
	if timeoutMinutes > 0 {
		deadline = startTime.Add(time.Duration(timeoutMinutes) * time.Minute)
	}
	for {
		select {
		case <-ticker.C:
			s.Typing(chatID) // refresh the live-signal independent of the text bubble below
			elapsed := time.Since(startTime)
			mins, secs := int(elapsed.Minutes()), int(elapsed.Seconds())%60
			msg := fmt.Sprintf("⏳ %s... (%d분 %d초 경과)", label, mins, secs)
			if !deadline.IsZero() {
				if remaining := time.Until(deadline); remaining > 0 && remaining <= 2*time.Minute {
					msg = fmt.Sprintf("⏳ %s... (%d분 %d초 경과) — 곧 제한 시간(%d분)에 도달합니다. 계속 기다리는 중이며, 그만두려면 !cancel",
						label, mins, secs, timeoutMinutes)
				}
			}
			_ = s.Send(chatID, msg)
		case <-done:
			return
		}
	}
}

// runWorker executes the Worker turn for a resolved (project, conversation) and relays output.
// workDir overrides the project's path as the Claude CLI working directory (e.g. a git worktree).
// Pass "" to use the project's registered path.
// backend names the AI backend this turn runs on and MUST match `client`. It is
// passed explicitly (rather than read from the global active backend) so a
// conversation resumed on its own backend uses the right model, logging, and
// continuation tagging even when the global backend differs.
func (m *Manager) runWorker(ctx context.Context, chatID int64, text string, sink convSink, workDir string, c *Conversation, s MessageSender, client ClaudeClient, backend string) {
	// Signal turn completion on every exit path (success, error, timeout, or the
	// early "project not found" return) so channels with a live "working"
	// indicator (web chat) always get a matching Done for their Typing.
	defer s.Done(chatID)

	// project scopes workdir/status/history-log. For a top-level web conversation
	// (webConvSink) project()=="" — there is no registered project to look up, so
	// skip GetProject and fall back to the passed workDir / the service home.
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

	// Forward tool images (screen MCP screenshot/capture_window/capture_region) to
	// the chat. Only when screen control is on (images come only from those tools)
	// and the sender can send photos. Setting OnImage makes the worker stream NDJSON
	// so tool_result image blocks can be recovered — the final envelope drops them.
	var onImage func(png []byte, caption string)
	if m.cfg().ScreenControl {
		if ps, ok := s.(interface {
			SendPhoto(int64, []byte, string) error
		}); ok {
			onImage = func(png []byte, caption string) { _ = ps.SendPhoto(chatID, png, caption) }
		}
	}

	// Check if context is growing too large; auto-create continuation if needed.
	// Threshold: ~40k tokens (conservative estimate for claude-haiku).
	const contextThreshold = 40000
	parentSummary := ""
	workConv := c

	historyTokens := 0
	for _, turn := range c.History {
		historyTokens += estimateTokens(turn.Prompt)
		historyTokens += estimateTokens(turn.Response)
	}
	currentTokens := estimateTokens(text)
	totalTokens := historyTokens + currentTokens

	if totalTokens > contextThreshold {
		summary := c.Summary
		if summary == "" {
			summary = "이전 대화 내용을 참고해 주세요."
		}
		if newC, err := sink.makeContinuation(c); err != nil {
			log.Printf("[manager] auto-continuation failed: %v", err)
		} else {
			newC.Backend = backend
			parentSummary = summary
			workConv = newC
			// Internal context-length split: the conversation continues seamlessly
			// (summary carried forward), so don't expose the series boundary to the
			// user — just log it for debugging.
			log.Printf("[manager] context length → new series (conv %s → %s)", c.ID, newC.ID)
		}
	}

	s.Typing(chatID)
	isNewConv := !workConv.Started
	// Show the "📂 project · 💬 conversation" header only when a genuinely new topic
	// begins — never on a resume, and never for an internal continuation (a
	// context-length series split). This keeps Telegram feeling like one continuous
	// chat while the backend still manages conversations/series behind the scenes.
	if isNewConv && !workConv.IsContinuation {
		headerProject := project
		if headerProject == "" {
			// No project scope: label by the actual channel (telegram/web), not a
			// hardcoded "웹" — a telegram turn with no active project must show
			// "telegram", never "web".
			headerProject = sink.label()
		}
		_ = s.Send(chatID, routingHeader(headerProject, workConv.Title, isNewConv))
	}

	// Record Worker status as running
	_ = m.workerStatus.SetStatus(WorkerStatus{
		Project:        project,
		ConversationID: workConv.ID,
		Title:          workConv.Title,
		Status:         "running",
		StartTime:      time.Now(),
	})

	startTime := time.Now()
	// Give the user a sense of scale before the wait starts, not just silence
	// until the (up to 2-minute-away) first heartbeat tick — a turn that
	// finishes in 90s currently produces zero progress signal at all.
	if avg, samples, ok := m.estimateTurnDuration(backend); ok {
		_ = s.Send(chatID, fmt.Sprintf("⏳ 작업 시작 (최근 %d건 평균 %s)", samples, formatMinSec(avg)))
	}
	timeoutMinutes := m.cfg().TimeoutMinutes
	heartbeatDone := make(chan struct{})
	go runHeartbeat(s, chatID, "작업 진행 중", startTime, timeoutMinutes, heartbeatDone)

	// Pass history in the prompt as a restart-safe fallback.
	// When --resume is in play, the CLI session already carries full context server-side,
	// so only a short trailing reminder is needed (avoids re-sending the whole history
	// every turn, which was making prompts — and response times — grow with each message).
	// Without an existing session (fresh conversation), history is empty anyway.
	// If the session is ever lost (e.g. after restart or CLI update), the short reminder
	// is what's available; deeper recovery relies on parentSummary/.teleclaude/memory.md.
	const maxHistoryInPrompt = 3
	historyForPrompt := workConv.History
	if len(historyForPrompt) > maxHistoryInPrompt {
		historyForPrompt = historyForPrompt[len(historyForPrompt)-maxHistoryInPrompt:]
	}
	globalMemory := readGlobalMemory()
	projectMemory := ""
	if pPath != "" {
		projectMemory = readProjectMemory(pPath)
	}
	prompt := buildContextPrompt(text, parentSummary, globalMemory, projectMemory, historyForPrompt)

	workerModel := m.workerModelForBackendName(backend)
	log.Printf("[worker] ▶ backend=%s model=%q project=%s conv=%s resume=%v prompt=%d chars",
		backend, workerModel, project, workConv.ID, workConv.Started, len(prompt))

	res, err := client.Run(ctx, RunRequest{
		Prompt:    prompt,
		WorkDir:   workDir,
		SessionID: workConv.SessionID,
		Resume:    workConv.Started,
		Model:     workerModel,
		OnImage:   onImage,
	})
	close(heartbeatDone)
	elapsed := time.Since(startTime)

	if err != nil {
		if ctx.Err() != nil {
			log.Printf("[worker] ✗ backend=%s context cancelled/timeout after %s", backend, elapsed)
			_ = m.workerStatus.UpdateStatus(project, workConv.ID, "timeout", ctx.Err().Error())
			return
		}
		// If --resume hard-failed because the CLI session was lost (bot restart or
		// CLI update dropped the session store), retry once as a fresh session. The
		// prompt already carries the recent-history reminder, so the conversation
		// continues seamlessly instead of dead-ending on an error.
		if workConv.Started && isSessionNotFound(err.Error()) {
			// Session store was lost (bot restart / CLI update). Retry as a fresh
			// session — the prompt carries a recent-history reminder so the chat
			// continues seamlessly. This is internal recovery; don't tell the user.
			log.Printf("[worker] session lost (%v) — retrying once without --resume", err)
			// The retry is a full fresh turn (may take a while); keep a heartbeat
			// alive for it — the original one was already closed above.
			recoverDone := make(chan struct{})
			go runHeartbeat(s, chatID, "세션 복구 진행 중", startTime, timeoutMinutes, recoverDone)
			res, err = client.Run(ctx, RunRequest{
				Prompt:    prompt,
				WorkDir:   workDir,
				SessionID: workConv.SessionID,
				Resume:    false,
				Model:     workerModel,
				OnImage:   onImage,
			})
			close(recoverDone)
			elapsed = time.Since(startTime)
		}
		if err != nil {
			if ctx.Err() != nil {
				log.Printf("[worker] ✗ backend=%s context cancelled/timeout after %s", backend, elapsed)
				_ = m.workerStatus.UpdateStatus(project, workConv.ID, "timeout", ctx.Err().Error())
				return
			}
			log.Printf("[worker] ✗ backend=%s error after %s: %v", backend, elapsed, err)
			_ = s.Send(chatID, "⚠️ 작업 실패: "+err.Error())
			_ = m.workerStatus.UpdateStatus(project, workConv.ID, "failed", err.Error())
			return
		}
		log.Printf("[worker] ✅ (session-recovered) backend=%s elapsed=%s output=%d bytes session=%q",
			backend, elapsed, len(res.Text), res.SessionID)
	}

	log.Printf("[worker] ✅ backend=%s elapsed=%s output=%d bytes session=%q",
		backend, elapsed, len(res.Text), res.SessionID)
	m.recordTurnDuration(backend, elapsed)

	// Reactive: if Worker hit Claude's context limit, auto-create continuation and retry once.
	if res.IsError && isContextOverflow(res.Text) {
		overflowSummary := workConv.Summary
		if overflowSummary == "" {
			overflowSummary = "이전 대화 내용을 참고해 주세요."
		}
		if newC, cerr := sink.makeContinuation(workConv); cerr == nil {
			newC.Backend = backend
			workConv = newC
			// Reactive context-overflow split: continue seamlessly in a new series
			// (summary carried); keep it internal — logged below, not sent to the user.
			retryPrompt := buildContextPrompt(text, overflowSummary, globalMemory, projectMemory, nil)
			log.Printf("[worker] ▶ retry backend=%s model=%q conv=%s (context overflow)", backend, workerModel, workConv.ID)

			// Restart heartbeat for the retry turn — it may take another full TimeoutMinutes.
			retryDone := make(chan struct{})
			go func() {
				ticker := time.NewTicker(2 * time.Minute)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						e := time.Since(startTime)
						_ = s.Send(chatID, fmt.Sprintf("⏳ 재시작 진행 중... (%d분 %d초 경과)", int(e.Minutes()), int(e.Seconds())%60))
					case <-retryDone:
						return
					}
				}
			}()
			res, err = client.Run(ctx, RunRequest{
				Prompt:    retryPrompt,
				WorkDir:   workDir,
				SessionID: workConv.SessionID,
				Resume:    false,
				Model:     workerModel,
				OnImage:   onImage,
			})
			close(retryDone)
			elapsed = time.Since(startTime)
			if err != nil {
				_ = s.Send(chatID, "⚠️ 재시작 후 작업 실패: "+err.Error())
				_ = m.workerStatus.UpdateStatus(project, workConv.ID, "failed", err.Error())
				return
			}
		} else {
			log.Printf("[manager] reactive continuation failed: %v", cerr)
		}
	}

	_ = sendChunked(s, chatID, res.Text)

	// Persist conversation progress and history.
	wasStarted := workConv.Started
	workConv.Started = true
	workConv.LastActivity = time.Now().UTC()
	if res.Text != "" {
		workConv.Summary = truncate(res.Text, 80)
	}

	// Append this turn to conversation history. This cap is a storage/display
	// limit only — it does NOT affect LLM prompt size, which is separately
	// (and much more tightly) bounded by maxHistoryInPrompt below. Web chat's
	// /api/history reads this same array (store.go HistorySnapshot), so a small
	// cap here made old turns silently vanish from the web view (while still
	// visible in Telegram's own server-side scrollback) well before it mattered
	// for prompt size — kept generous so that doesn't happen in normal use.
	const maxHistoryTurns = 200
	workConv.History = append(workConv.History, ConversationTurn{
		Timestamp: time.Now().UTC(),
		Prompt:    text,
		Response:  res.Text,
	})
	if len(workConv.History) > maxHistoryTurns {
		dropped := len(workConv.History) - maxHistoryTurns
		workConv.History = workConv.History[dropped:]
		// The cap silently dropped visible history once before (see the comment
		// above) without so much as a log line, so a user report was the only way
		// to notice it happened at all. Logging it costs nothing and means the
		// next occurrence is visible in the server log instead of a surprise.
		log.Printf("[manager] conv %s history capped at %d turns, dropped %d oldest", workConv.ID, maxHistoryTurns, dropped)
	}
	if res.SessionID != "" && !wasStarted {
		workConv.SessionID = res.SessionID
	}

	if err := sink.save(workConv); err != nil {
		log.Printf("[manager] update conversation: %v", err)
	}
	if err := sink.setActive(workConv); err != nil {
		log.Printf("[manager] set active: %v", err)
	}

	// Append to date-based history log for !history command. When there's no
	// project scope (project==""), which would make WriteHistory write to the
	// history root, fall back to the sink's channel label ("telegram" or "web")
	// so those turns land in a dedicated per-channel folder instead — never
	// mislabeling a telegram-no-project turn as "web".
	if res.Text != "" {
		historyLabel := project
		if historyLabel == "" {
			historyLabel = sink.label()
		}
		if herr := WriteHistory(historyLabel, workConv.Title, text, res.Text); herr != nil {
			log.Printf("[manager] history write error: %v", herr)
		}
	}

	// Update Worker status to completed
	_ = m.workerStatus.UpdateStatus(project, workConv.ID, "completed", "")

	// Send completion notification with elapsed time
	completionMsg := formatCompletion(elapsed)
	_ = s.Send(chatID, completionMsg)
}

// describeActive returns a human-readable active pointer (used by !status-like replies).
func (m *Manager) describeActive() string {
	a := m.store.GetActive()
	if a.Project == "" {
		return "활성 대화 없음"
	}
	if c, ok := m.store.GetConversation(a.Project, a.ConversationID); ok {
		return fmt.Sprintf("📂 %s · 💬 %s", a.Project, c.Title)
	}
	return "활성 대화 없음"
}

// GetWorkerStatus returns status of a specific Worker or empty if not found.
func (m *Manager) GetWorkerStatus(project, convID string) (WorkerStatus, bool) {
	return m.workerStatus.GetStatus(project, convID)
}

// DescribeActiveWorkers returns a human-readable status of all running Workers.
func (m *Manager) DescribeActiveWorkers() string {
	active := m.workerStatus.ListActive()
	if len(active) == 0 {
		return "실행 중인 작업 없음"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🔄 실행 중인 작업 (%d개):\n\n", len(active))
	for i, ws := range active {
		elapsed := time.Since(ws.StartTime)
		mins := int(elapsed.Minutes())
		secs := int(elapsed.Seconds()) % 60

		var elapsedStr string
		if mins > 0 {
			elapsedStr = fmt.Sprintf("%d분 %d초", mins, secs)
		} else {
			elapsedStr = fmt.Sprintf("%d초", secs)
		}

		fmt.Fprintf(&sb, "%d) 📂 %s · 💬 %s\n", i+1, ws.Project, ws.Title)
		fmt.Fprintf(&sb, "   ⏱️ %s 경과\n", elapsedStr)
	}
	return sb.String()
}

// formatCompletion formats the work completion notification with elapsed time.
func formatCompletion(elapsed time.Duration) string {
	secs := int(elapsed.Seconds())
	mins := secs / 60
	secs = secs % 60

	var duration string
	if mins > 0 {
		duration = fmt.Sprintf("%d분 %d초", mins, secs)
	} else {
		duration = fmt.Sprintf("%d초", secs)
	}

	return fmt.Sprintf("✅ 작업 완료 (%s)", duration)
}

// readProjectMemory reads .teleclaude/memory.md from the project directory.
// Worker Claude can freely update this file to persist project-level knowledge.
func readProjectMemory(projectPath string) string {
	b, err := os.ReadFile(filepath.Join(projectPath, ".teleclaude", "memory.md"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readGlobalMemory reads ~/.teleclaude/global-memory.md for cross-project long-term memory.
func readGlobalMemory() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, ".teleclaude", "global-memory.md"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// buildContextPrompt assembles the Worker prompt from available context layers.
// Layer order: global memory → project memory → parent summary → recent history → current request.
// Only adds sections when content exists — no empty headers.
func buildContextPrompt(currentPrompt, parentSummary, globalMemory, projectMemory string, history []ConversationTurn) string {
	hasContext := globalMemory != "" || projectMemory != "" || parentSummary != "" || len(history) > 0
	if !hasContext {
		return currentPrompt
	}

	var sb strings.Builder

	if globalMemory != "" {
		sb.WriteString("## 장기 기억 (글로벌)\n\n")
		sb.WriteString(globalMemory)
		sb.WriteString("\n\n---\n\n")
	}

	if projectMemory != "" {
		sb.WriteString("## 프로젝트 메모리\n\n")
		sb.WriteString(projectMemory)
		sb.WriteString("\n\n---\n\n")
	}

	if parentSummary != "" {
		sb.WriteString("## 이전 대화 요약\n\n")
		sb.WriteString(parentSummary)
		sb.WriteString("\n\n---\n\n")
	}

	if len(history) > 0 {
		sb.WriteString("## 최근 대화 기록\n\n")
		for i, turn := range history {
			// Truncate response to 300 chars — enough for context, avoids token bloat
			// when --resume also carries the full session.
			fmt.Fprintf(&sb, "**Turn %d** (%s)\n**요청:** %s\n**응답:** %s\n\n",
				i+1, turn.Timestamp.Format("2006-01-02 15:04"), turn.Prompt, truncate(turn.Response, 300))
		}
		sb.WriteString("---\n\n")
	}

	sb.WriteString("## 현재 요청\n\n")
	sb.WriteString(currentPrompt)
	sb.WriteString("\n\n> 중요한 결정/해결책은 .teleclaude/memory.md에 기록해두세요.")
	return sb.String()
}

// handleSchedule registers a reminder or cron job decoded from the Manager's routing decision.
func (m *Manager) handleSchedule(chatID int64, dec RouteDecision, s MessageSender) {
	if m.scheduler == nil {
		_ = s.Send(chatID, "⚠️ 스케줄러가 초기화되지 않았습니다.")
		return
	}
	if dec.ScheduleTask == "" {
		_ = s.Send(chatID, "🤔 어떤 내용을 언제 알림/실행할지 좀 더 구체적으로 말씀해주세요.")
		return
	}

	// If LLM returned a 5-field cron expression (or @-shorthand), use AddTask directly.
	if isCronExpr(dec.ScheduleInterval) {
		kind := "알림"
		if dec.ScheduleIsTask {
			kind = "Claude 작업"
		}
		t := &Task{
			ID:        newTaskID(),
			ChatID:    chatID,
			Prompt:    dec.ScheduleTask,
			CronExpr:  dec.ScheduleInterval,
			Status:    "pending",
			IsTask:    dec.ScheduleIsTask,
			Label:     truncate(dec.ScheduleTask, 30),
			CreatedAt: time.Now(),
		}
		if err := m.scheduler.AddTask(t); err != nil {
			_ = s.Send(chatID, "⚠️ 작업 등록 실패: "+err.Error())
			return
		}
		_ = s.Send(chatID, fmt.Sprintf("✅ 예약 등록 [%s] %s (%s)\n  %s", t.ID, dec.ScheduleInterval, kind, dec.ScheduleTask))
		return
	}

	dur, label, err := ParseSchedule(dec.ScheduleInterval)
	if err != nil {
		_ = s.Send(chatID, fmt.Sprintf("🤔 시간을 파악하지 못했어요 (%q). 예) 30분 후에, 매시간, 매일", dec.ScheduleInterval))
		return
	}

	switch dec.ScheduleType {
	case "remind":
		r, err := m.scheduler.AddReminder(chatID, dec.ScheduleTask, timeNow().Add(dur))
		if err != nil {
			_ = s.Send(chatID, "⚠️ 알림 등록 실패: "+err.Error())
			return
		}
		_ = s.Send(chatID, fmt.Sprintf("✅ 알림 등록 [%s] — %s 후\n  %s", r.ID, label, dec.ScheduleTask))
	case "cron":
		c, err := m.scheduler.AddCron(chatID, label, dur, dec.ScheduleTask, dec.ScheduleIsTask)
		if err != nil {
			_ = s.Send(chatID, "⚠️ 크론 등록 실패: "+err.Error())
			return
		}
		kind := "알림"
		if dec.ScheduleIsTask {
			kind = "Claude 작업"
		}
		_ = s.Send(chatID, fmt.Sprintf("✅ 반복 등록 [%s] %s (%s)\n  %s", c.ID, label, kind, dec.ScheduleTask))
	default:
		_ = s.Send(chatID, "🤔 알림(일회성)인지 반복인지 명확하지 않아요. 예) 30분 후에 알림 / 매시간 서버 확인")
	}
}

// isCronExpr returns true if s looks like a 5-field cron expression or @-shorthand.
func isCronExpr(s string) bool {
	if strings.HasPrefix(s, "@") {
		return true
	}
	return len(strings.Fields(s)) == 5
}

// HandleScheduledTask executes a pre-scheduled task in a fresh conversation,
// bypassing the Manager LLM routing so the task prompt is not misinterpreted
// as a routing request and never leaks into a prior conversation's context.
func (m *Manager) HandleScheduledTask(ctx context.Context, chatID int64, text string, s MessageSender) {
	m.backendMu.RLock()
	currentBackend := m.backendName
	currentClient := m.client
	m.backendMu.RUnlock()

	projects := m.store.ListProjects()
	if len(projects) == 0 {
		_ = s.Send(chatID, "⚠️ 예약 작업 실행 실패: 등록된 프로젝트가 없습니다. !project add <이름> <경로>")
		return
	}

	// Prefer the active project; fall back to alphabetically first to ensure determinism.
	projectName := m.store.GetActive().Project
	if _, ok := m.store.GetProject(projectName); !ok {
		names := make([]string, 0, len(projects))
		for name := range projects {
			names = append(names, name)
		}
		sort.Strings(names)
		projectName = names[0]
	}

	c, err := m.store.NewConversation(projectName, "📅 "+truncate(text, 28), OriginTelegram)
	if err != nil {
		_ = s.Send(chatID, "⚠️ 예약 작업 대화 생성 실패: "+err.Error())
		return
	}
	c.Backend = currentBackend
	_ = m.store.UpdateConversation(projectName, c)

	// Create a git worktree for isolation: parallel scheduled tasks on the same project
	// work in separate directories, preventing file-level conflicts.
	p, _ := m.store.GetProject(projectName)
	workDir := p.Path
	wtID := newTaskID()
	if wtPath, err := CreateWorktree(p.Path, wtID); err != nil {
		log.Printf("[manager] worktree create failed, falling back to project dir: %v", err)
	} else if wtPath != "" {
		workDir = wtPath
		defer RemoveWorktree(p.Path, wtPath)
		log.Printf("[manager] worktree created: %s", wtPath)
	}

	log.Printf("[manager] scheduled task → project=%s conv=%s workDir=%s", projectName, c.ID, workDir)
	m.runWorker(ctx, chatID, text, m.projectSink(projectName), workDir, c, s, currentClient, currentBackend)
}

// timeNow is a replaceable clock for testing.
var timeNow = time.Now
