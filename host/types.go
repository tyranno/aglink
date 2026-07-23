package main

import (
	"context"
	"time"
)

// Design Ref: §3.1 — Domain types. Plan SC: 멀티프로젝트 × 프로젝트별 다중 대화.

// Config holds runtime settings loaded from %USERPROFILE%\.aglink\config.txt.
type Config struct {
	TelegramBotToken      string
	AllowedUserIDs        []int64
	ManagerModel          string   // default "haiku"
	WorkerModel           string   // "" = claude default
	ClaudePath            string   // "" = auto-detect
	ClaudeOauthToken      string   // CLAUDE_CODE_OAUTH_TOKEN injected into worker env ("" = use claude's own login)
	TimeoutMinutes        int      // default 10
	ManagerAlways         bool     // default true (route every text via manager)
	CodexPath             string   // "" = auto-detect
	CodexModel            string   // worker model (powerful) — "" = codex built-in default
	CodexManagerModel     string   // routing model (fast/cheap) — "" = same as CodexModel
	OpencodePath          string   // opencode CLI 경로 — "" = auto-detect
	OpencodeModel         string   // worker model 참조 "provider/model" (예 anthropic/claude, ollama/qwen2.5) — "" = opencode 기본
	OpencodeManagerModel  string   // routing model 참조 — "" = OpencodeModel와 동일
	OpencodeConfigPath    string   // opencode.json 경로. provider baseURL/apiKey는 opencode가 이 파일에서 관리 — "" = opencode 기본 탐색
	DefaultBackend        string   // "claude" | "codex" | "opencode" — "" = "claude"
	HomeDir               string   // 서비스 기본 작업 홈 (yaml home_dir); "" → <userHome>/aglink
	MaxWorkers            int      // max concurrent Worker goroutines, default 3
	RateLimitPerMin       int      // max user messages per minute, 0 = unlimited, default 20
	AllowScripts          bool     // permit --script in !task add/update, default false
	AllowedScriptCommands []string // whitelist of allowed script first-tokens; empty = any
	AllowedUsernames      []string // Telegram usernames (without @) allowed to use the bot
	ScreenControl         bool     // screen-control MCP 활성화 (Windows). 기본 false
	ScreenPresetsFile     string   // 좌표 프리셋 파일 경로. 빈 값이면 <data dir>/presets.json
	ScreenElevated        bool     // 관리자 권한으로 실행해 관리자 대상 앱도 제어 (Windows UIPI 우회). 기본 false
	ScreenKeepAwake       bool     // 화면 유휴 잠금/화면보호기 방지 (SetThreadExecutionState, Windows). 기본 false
	ScreenBinaryPath      string   // aglink-screen 실행파일 경로. 빈 값이면 aglink 실행파일과 같은 폴더에서 찾음
	WebControl            bool     // 브라우저 제어 MCP(aglink-web) 활성화. 기본 false
	WebBinaryPath         string   // aglink-web 실행파일 경로. 빈 값이면 aglink 실행파일과 같은 폴더에서 찾음
	ConversationTTLDays   int      // 이 기간(일) 동안 활동 없는 대화/히스토리 파일을 자동 정리. 0 = 비활성화, 기본 30
	WebChat               bool     // local web chat transport enabled
	WebChatAddr           string   // web chat bind address (localhost only), default 127.0.0.1:27271
	WebChatToken          string   // web chat auth token; empty → auto-generated + persisted
	WebChatOwnerChatID    int64    // chatID web actions run as; 0 → first AllowedUserIDs

	// chat_control: loopback control API a separate aglink-chat process connects to.
	// Off by default — enabling it never affects the embedded web_chat above.
	ChatControl            bool   // loopback control-API server for aglink-chat enabled
	ChatControlAddr        string // control-API bind address (loopback only), default 127.0.0.1:27270
	ChatControlToken       string // control-API auth token; empty → auto-generated + persisted
	ChatControlOwnerChatID int64  // chatID aglink-chat actions run as; 0 → first AllowedUserIDs

	// aglink_chat: aglink spawns aglink-chat.exe serve as a managed child so
	// it runs as the primary frontend. Phase 1 runs it on a parallel port (27272)
	// alongside the embedded web_chat server; requires ChatControl enabled.
	AglinkChat           bool   // spawn+supervise aglink-chat.exe serve
	AglinkChatAddr       string // aglink-chat browser bind address, default 127.0.0.1:27272
	AglinkChatBinaryPath string // aglink-chat.exe path; empty → srcDir then ../aglink-chat
	AglinkChatToken      string // aglink-chat browser auth token; empty → auto-generated + persisted

	// InteractiveClaude gates whether a persistent ConPTY-backed claude session
	// (interactiveClaudeRunner) is constructed at all. Off by default: this is
	// experimental (B안) and only conversations that opt in via "!interactive on"
	// ever use it, but the runner itself is only built/spawned when this is true,
	// so an unset config leaves behavior byte-for-byte identical to before.
	InteractiveClaude bool

	// ToolPaths is a generic registry of external tool executables keyed by tool
	// name (e.g. "ssh", "sshpass"). Empty/absent → resolve from PATH. Lets an
	// install place a tool in a non-PATH location (e.g. C:\cygwin\bin\sshpass.exe)
	// and point aglink at it without editing the system PATH. See resolveToolPath.
	ToolPaths map[string]string

	// VLLMServers are OpenAI-compatible local inference endpoints. The first is
	// primary; more are added later as GPU capacity grows (also the failover
	// order). When the opencode backend has no explicit opencode.json, these are
	// rendered into a generated one so opencode can target local vLLM with no
	// manual provider editing. See renderVLLMOpencodeConfig / resolveOpencodeConfigPath.
	VLLMServers []VLLMServer

	// Providers holds per-user creds (api key + optional model) for the built-in
	// free-remote catalog (Groq/Cerebras/Gemini/OpenRouter), keyed by catalog id.
	// Only ids with a non-empty key are rendered into the generated opencode.json
	// as OpenAI-compatible providers, so an empty map changes nothing. See
	// providers.go / renderOpencodeProviderConfig.
	Providers map[string]ProviderCred

	// CustomProviders are user-defined OpenAI-compatible backends added entirely
	// from the settings UI (id/base_url/default_model), merged into the effective
	// provider catalog alongside the built-ins and any providers.d drop-ins. This
	// lets a user register a brand-new opencode provider without hand-editing a
	// providers.d YAML file or rebuilding; the API key is then supplied the normal
	// way via Providers (the free-provider key row the settings section renders
	// automatically once the definition exists). See catalogProviders.
	CustomProviders []FreeProvider

	// SSHEnabled gates the !ssh remote-control command and its host registry. Off
	// by default: an unset config can never reach out over SSH.
	SSHEnabled bool
	// SSHHosts is the registry of named remotes !ssh may reach. !ssh takes a host
	// *name*, never a raw host:port, so only registered hosts are reachable.
	SSHHosts []SSHHost
}

// ConversationTurn represents one exchange in a conversation.
type ConversationTurn struct {
	Timestamp time.Time `json:"timestamp"`
	Prompt    string    `json:"prompt"`   // user input
	Response  string    `json:"response"` // claude output
	// Images are refs ("<convID>/<name>.png") to tool screenshots captured during
	// this turn, saved on disk (not inline) so store.json stays small; the history
	// API reloads them so images survive a restart. Missing refs (pruned) are skipped.
	Images []string `json:"images,omitempty"`
}

// Conversation is one topic within a project; maps 1:1 to a claude session.
// Design Ref: §3.1 — SessionID is a UUID we generate (--session-id first turn, --resume after).
// ParentID chains conversations when context grows too large (auto-continuation).
// Conversation origin channels. Empty string is treated as OriginTelegram for
// backward compatibility with conversations created before this field existed.
const (
	OriginTelegram = "telegram"
	OriginWeb      = "web"
)

type Conversation struct {
	ID             string             `json:"id"`
	Title          string             `json:"title"`
	Summary        string             `json:"summary"`
	SessionID      string             `json:"sessionId"` // UUID assigned at creation
	Started        bool               `json:"started"`   // false until first worker turn completes
	LastActivity   time.Time          `json:"lastActivity"`
	History        []ConversationTurn `json:"history"`                  // conversation turns for context preservation
	ParentID       string             `json:"parentId,omitempty"`       // ID of previous conversation in chain
	ChildID        string             `json:"childId,omitempty"`        // ID of next conversation in chain
	IsContinuation bool               `json:"isContinuation,omitempty"` // auto-generated continuation
	Backend        string             `json:"backend,omitempty"`        // "claude"|"codex"|"" (""=claude)
	Origin         string             `json:"origin,omitempty"`         // "telegram"|"web"|"" (""=telegram, back-compat)
	WorkDir        string             `json:"workDir,omitempty"`        // per-conversation working directory; "" → service home
	Interactive    bool               `json:"interactive,omitempty"`    // per-conversation interactive override; only meaningful when InteractiveSet — see IsInteractive
	InteractiveSet bool               `json:"interactiveSet,omitempty"` // true once "!interactive on|off" set Interactive explicitly; unset → follow the global default (on for claude when interactive_claude.enabled)

	// CodexContextTokens is the input-token size codex reported on this
	// conversation's last worker turn (usage.input_tokens). Codex `exec resume`
	// re-sends the whole server-side rollout each turn, so this grows unbounded on
	// a long conversation; when it crosses codexContextResetTokens the manager
	// starts a fresh codex thread instead of resuming. Only meaningful for the
	// codex backend; 0 = unknown / not yet observed. See runWorker.
	CodexContextTokens int `json:"codexContextTokens,omitempty"`
}

// Project is a registered directory holding multiple conversations.
type Project struct {
	Path          string                   `json:"path"`
	Conversations map[string]*Conversation `json:"conversations"`
}

// ActiveRef points at the current project/conversation (fallback + manual switching).
type ActiveRef struct {
	Project        string `json:"project"`
	ConversationID string `json:"conversationId"`
}

// StoreData is the root persisted to store.json (별도 저장소).
type StoreData struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Projects      map[string]*Project `json:"projects"` // 웹 토픽 전용
	Active        ActiveRef           `json:"active"`   // 웹의 활성 토픽
	ActiveBackend string              `json:"activeBackend,omitempty"`

	// 전역 단일 텔레그램 대화 (프로젝트 무관).
	TelegramConv          *Conversation `json:"telegramConv,omitempty"`
	TelegramActiveProject string        `json:"telegramActiveProject,omitempty"`

	WebConvs map[string]*Conversation `json:"webConvs,omitempty"` // top-level web conversations (project-independent)
}

// --- Manager routing I/O (Design §3.2) ---

type ConversationSummary struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

type ProjectSummary struct {
	Name          string                `json:"name"`
	Conversations []ConversationSummary `json:"conversations"`
}

type RouteRequest struct {
	Message  string
	Projects []ProjectSummary
	Active   ActiveRef
}

// RouteDecision is the structured output the Manager (claude) must return.
type RouteDecision struct {
	Project        string  `json:"project"`
	ConversationID string  `json:"conversationId"`
	Action         string  `json:"action"` // "resume" | "new" | "clarify" | "status" | "schedule"
	NewTitle       string  `json:"newTitle"`
	Clarify        string  `json:"clarify"`
	Confidence     float64 `json:"confidence"`

	// Schedule fields — only set when action == "schedule"
	ScheduleType     string `json:"scheduleType,omitempty"`     // "remind" | "cron"
	ScheduleInterval string `json:"scheduleInterval,omitempty"` // "30m", "2h", "hourly", "daily" …
	ScheduleTask     string `json:"scheduleTask,omitempty"`     // message or Claude prompt
	ScheduleIsTask   bool   `json:"scheduleIsTask,omitempty"`   // true → dispatch through Worker
}

// Task is a unified scheduled item replacing Reminder and CronJob.
// CronExpr != "" → recurring (robfig/cron/v3 syntax, e.g. "0 9 * * 1-5").
// CronExpr == "" → one-shot (FireAt used).
// Status: "pending" | "paused" | "cancelled"
type Task struct {
	ID        string    `json:"id"`
	ChatID    int64     `json:"chatId"`
	Project   string    `json:"project,omitempty"` // project active at creation time; fixed at fire time, never re-resolved
	Prompt    string    `json:"prompt"`
	Script    string    `json:"script,omitempty"`   // bash pre-check; empty = skip
	CronExpr  string    `json:"cronExpr,omitempty"` // standard 5-field cron
	FireAt    time.Time `json:"fireAt,omitempty"`   // one-shot: when to fire
	Status    string    `json:"status"`
	IsTask    bool      `json:"isTask"` // true = Claude Worker, false = notify
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"createdAt"`
	LastFired time.Time `json:"lastFired,omitempty"`
	DependsOn []string  `json:"dependsOn,omitempty"` // task IDs that must complete before this fires
}

// Target kinds. An empty or unrecognized Kind means the telegram stream: it is
// the always-present default, so a not-yet-updated client can never accidentally
// address a web topic.
const (
	TargetTelegram = "telegram"
	TargetWeb      = "web"
)

// Target identifies a conversation channel: the global telegram stream, or a
// specific web topic. It addresses both inbound sends and outbound frames, so
// each channel's output stays in its own conversation.
type Target struct {
	Kind    string `json:"kind"`
	Project string `json:"project,omitempty"`
	ID      string `json:"id,omitempty"`
}

// TelegramTarget is the global telegram stream.
func TelegramTarget() Target { return Target{Kind: TargetTelegram} }

// WebTarget names a specific web topic. Anything addressed to a web target is
// withheld from Telegram by Hub.targets, so web-only operations can never leak.
func WebTarget(id string) Target { return Target{Kind: TargetWeb, ID: id} }

// AsWebTarget coerces t to a web target, so a reply to a web-only operation can
// never be addressed to the telegram stream even if the requester sent no target.
func AsWebTarget(t Target) Target {
	if t.IsWeb() {
		return t
	}
	return Target{Kind: TargetWeb}
}

// IsWeb reports whether t addresses a web topic rather than the telegram stream.
func (t Target) IsWeb() bool { return t.Kind == TargetWeb }

// SameConversation reports whether two targets name the same conversation. Used
// by clients to decide whether an incoming frame belongs to what's on screen.
func (t Target) SameConversation(o Target) bool {
	if t.IsWeb() != o.IsWeb() {
		return false
	}
	if t.IsWeb() {
		return t.ID == o.ID
	}
	return true // all non-web targets are the single telegram stream
}

// Action constants.
const (
	ActionResume   = "resume"
	ActionNew      = "new"
	ActionClarify  = "clarify"
	ActionStatus   = "status"
	ActionSchedule = "schedule"
)

// --- Worker run I/O (Design §3.3) ---

type RunRequest struct {
	Prompt    string
	WorkDir   string
	SessionID string // UUID
	Resume    bool   // true → --resume, false → --session-id
	Model     string

	// OwnerLabel identifies the conversation driving this turn. It is exported to
	// the worker subprocess as AGLINK_OWNER_LABEL so the aglink-screen control
	// lease can name which channel currently holds the screen in its SCREEN_BUSY /
	// control_status messages (see aglink-screen docs/control-ownership.md §5).
	// Empty means "no label" — the lease then identifies the owner by PID only.
	OwnerLabel string

	// OnProgress, when non-nil, requests realtime NDJSON streaming from the
	// backend and is called with a short human-readable line for each tool-use
	// event as it happens (e.g. "🔧 Bash: go test ./..."). Optional — nil means
	// the backend runs its normal single-envelope turn. Currently only honored
	// by claudeRunner; codexRunner ignores it (codex already streams JSONL events
	// via logCodexEvent).
	OnProgress func(string)

	// OnImage, when non-nil, is called with each image (PNG bytes + caption) that a
	// tool returns during the turn — e.g. a screen MCP screenshot/capture_window/
	// capture_region result. Like OnProgress it requires NDJSON streaming; the
	// image blocks live in tool_result content that the final result envelope drops.
	// Wired to Telegram (bot.SendPhoto) so conversational captures actually arrive.
	OnImage func(png []byte, caption string)
}

type RunResult struct {
	Text      string
	IsError   bool
	SessionID string // non-empty only on first codex turn (thread_id from JSONL)
	// InputTokens is the total prompt tokens the backend processed this turn
	// (codex usage.input_tokens from the final turn.completed). 0 when unknown.
	// The manager tracks it per codex conversation to auto-reset a resumed thread
	// once its rollout has ballooned — see codexContextResetTokens.
	InputTokens int

	// Cache/cost telemetry for the turn, populated by claudeRunner from the result
	// envelope's usage (0/0.0 for backends that don't report it). CacheReadTokens is
	// the prompt-cache hit — high on a resumed claude session — and CostUSD is the
	// CLI's own billed cost. Surfaced to the user as a compact per-turn footer; not
	// persisted into history.
	CacheReadTokens     int
	CacheCreationTokens int
	OutputTokens        int
	CostUSD             float64
}

// --- Interfaces (Design §4.1, Option C boundaries) ---

// ClaudeClient abstracts the local `claude` CLI for both Manager routing and Worker execution.
type ClaudeClient interface {
	Route(ctx context.Context, req RouteRequest) (RouteDecision, error)
	Run(ctx context.Context, req RunRequest) (RunResult, error)
}

// backendReadiness is an optional interface a ClaudeClient may implement to gate
// a worker turn on CLI capability and surface a one-time heads-up before the
// first turn. The manager consults it (via a type assertion) right before
// running a turn. Only codexRunner implements it — claudeRunner does not, so
// claude-backed turns skip the check entirely.
type backendReadiness interface {
	// CheckReadiness reports whether a worker turn may proceed. ok=false means
	// block the turn and show msg to the user. ok=true with a non-empty msg is a
	// one-time informational notice to show before the turn.
	CheckReadiness() (ok bool, msg string)
}

// --- Worker status tracking (real-time monitoring) ---

// WorkerStatus tracks the state of a running or completed Worker task.
type WorkerStatus struct {
	Project        string    // project name
	ConversationID string    // conversation ID
	Title          string    // conversation title for display
	Status         string    // "running" | "completed" | "failed" | "timeout"
	StartTime      time.Time // when the worker started
	EndTime        time.Time // when the worker finished (zero if still running)
	Error          string    // error message if failed
}

// WorkerStatusStore tracks all active and recent Workers.
type WorkerStatusStore interface {
	GetStatus(project, convID string) (WorkerStatus, bool)
	SetStatus(status WorkerStatus) error
	ListActive() []WorkerStatus          // return workers that are still running
	ListRecent(limit int) []WorkerStatus // return last N completed workers
	UpdateStatus(project, convID string, newStatus, errorMsg string) error
}

// StoreRepo abstracts the conversation store (JSON for MVP, SQLite later).
type StoreRepo interface {
	Load() error
	Save() error
	ListProjects() map[string]*Project
	AddProject(name, path string) error
	RemoveProject(name string) error
	GetProject(name string) (*Project, bool)
	NewConversation(project, title, origin string) (*Conversation, error)
	GetConversation(project, convID string) (*Conversation, bool)
	UpdateConversation(project string, c *Conversation) error
	SetActive(project, convID string) error
	GetActive() ActiveRef
	GetParent(project, convID string) (*Conversation, bool)
	GetStoredBackend() string
	SetStoredBackend(name string) error
	TelegramConversation() *Conversation
	UpdateTelegramConversation(c *Conversation) error
	TelegramActiveProject() string
	SetTelegramActiveProject(name string) error
	// HistorySnapshot returns a copy of the target conversation's turns taken
	// under the store lock, so callers can read history without racing a
	// worker that is appending to the live conversation.
	HistorySnapshot(tgt Target) []ConversationTurn
	NewWebConv(title string) (*Conversation, error)
	GetWebConv(id string) (*Conversation, bool)
	UpdateWebConv(c *Conversation) error
	ListWebConvs() map[string]*Conversation
	DeleteWebConv(id string) error
}
