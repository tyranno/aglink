package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Approvals: ask the user before an agent runs a tool they marked as risky.
//
// Workers run without per-action approval, so a page (or a document, a
// message) that talks the agent into "run this in the terminal" is acted on.
// approvals.tools in config.yaml names the aglink-web / aglink-screen tools
// that must ask first. The host passes the list and its /approve address to
// those MCP servers through the worker's MCP config; before such a tool runs
// they POST here, the host asks the user on every chat channel —
// "🔐 승인 요청 #k7 … !ok k7 / !no k7" — and holds the request until the user
// answers or the timeout refuses it.
//
// One "yes" can reach further than the one call (scopes, like Claude Code's
// permission prompts):
//   - once     — this call only.
//   - session  — this tool for the rest of the conversation the call came from
//     (the AGLINK_OWNER_LABEL the MCP server sends); in memory only, gone when
//     the conversation is deleted, after approvalSessionIdle unused, or on restart.
//   - target   — this tool on this one target, always: "launch_app:notepad",
//     "vscode_terminal_run:git status". Saved in approvals.allow.
//   - always   — this tool, always: dropped from approvals.tools.
//
// Every request and its outcome — including the ones the host allows by
// itself — is appended to ~/.aglink/approvals.log (approval_log.go).

const defaultApprovalTimeout = 2 * time.Minute

// Connected control clients (the desktop app, the web chat). An approval is
// pushed to all of them as {kind:"approval"} so they can show it with buttons —
// a host with no Telegram (a desktop-only PC) would otherwise ask nobody.
var controlClients = struct {
	sync.Mutex
	set map[*remoteChatChannel]bool
}{set: map[*remoteChatChannel]bool{}}

func addControlClient(c *remoteChatChannel) {
	controlClients.Lock()
	controlClients.set[c] = true
	controlClients.Unlock()
}

func removeControlClient(c *remoteChatChannel) {
	controlClients.Lock()
	delete(controlClients.set, c)
	controlClients.Unlock()
}

// broadcastControl pushes o to every connected control client and reports
// how many there were.
func broadcastControl(o controlOut) int {
	controlClients.Lock()
	defer controlClients.Unlock()
	for c := range controlClients.set {
		c.push(o)
	}
	return len(controlClients.set)
}

func approvalEvent(kind string, v any) controlOut {
	d, _ := json.Marshal(v)
	return controlOut{Kind: kind, Data: d}
}

// Approval scopes — how much one "yes" covers.
const (
	scopeOnce    = "once"
	scopeSession = "session"
	scopeTarget  = "target"
	scopeAlways  = "always"
)

// approvalInput is one /approve call as the MCP servers send it.
type approvalInput struct {
	Source  string `json:"source"`
	Tool    string `json:"tool"`
	Summary string `json:"summary"`
	// Target is the raw thing the call acts on (app name, command line,
	// command id, URL); the host normalizes it (normalizeApprovalTarget).
	Target string `json:"target,omitempty"`
	// Session is the conversation the call came from (AGLINK_OWNER_LABEL,
	// "chat:<id>/conv:<id>"), "" when unknown.
	Session string `json:"session,omitempty"`
}

type approvalRequest struct {
	ID      string
	Source  string // aglink-web | aglink-screen
	Tool    string
	Summary string
	Target  string // normalized, "" when the call has none
	Session string
	Created time.Time
	done    chan approvalAnswer
}

// approvalAnswer is the user's verdict: ok, how far it reaches, and the
// channel it came from (desktop | telegram | command).
type approvalAnswer struct {
	ok    bool
	scope string
	by    string
}

var approvals = struct {
	sync.Mutex
	pending map[string]*approvalRequest
}{pending: map[string]*approvalRequest{}}

// approvalURL is the endpoint the MCP servers call, or "" when the health
// server (which hosts it) is off.
func approvalURL() string {
	addr := healthAddr()
	if addr == "" {
		return ""
	}
	return "http://" + addr + "/approve"
}

// approvalEnv is what pluginWorkerArgs adds to the aglink-web/aglink-screen
// MCP entries when approvals are on. The conversation identity the session
// scope keys on is not here: the worker already carries AGLINK_OWNER_LABEL in
// its process environment, which its MCP servers inherit.
func approvalEnv(cfg *Config) map[string]string {
	if cfg == nil || len(cfg.ApprovalTools) == 0 {
		return nil
	}
	url := approvalURL()
	if url == "" {
		return nil
	}
	return map[string]string{
		"AGLINK_APPROVAL_URL":   url,
		"AGLINK_APPROVAL_TOOLS": strings.Join(cfg.ApprovalTools, ","),
	}
}

// splitToolList reads "eval, vscode_terminal_run" into tool names.
func splitToolList(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == ';' }) {
		if f = strings.TrimSpace(f); f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// splitAllowList reads approvals.allow entries ("launch_app:notepad,
// vscode_terminal_run:git status") — split on commas/newlines only, since a
// target may hold a space.
func splitAllowList(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == ';' }) {
		if e := normalizeAllowEntry(f); e != "" && !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

// normalizeAllowEntry trims and lowercases "tool:target"; "" when it is not
// one.
func normalizeAllowEntry(s string) string {
	tool, target, ok := strings.Cut(strings.TrimSpace(s), ":")
	tool, target = strings.TrimSpace(tool), strings.Join(strings.Fields(strings.ToLower(target)), " ")
	if !ok || tool == "" || target == "" {
		return ""
	}
	return tool + ":" + target
}

func newApprovalID() string {
	const letters = "abcdefghjkmnpqrstuvwxyz23456789"
	for {
		b := make([]byte, 3)
		for i := range b {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
			b[i] = letters[n.Int64()]
		}
		id := string(b)
		if _, taken := approvals.pending[id]; !taken {
			return id
		}
	}
}

// handleApproveHTTP answers POST /approve {source, tool, summary, target,
// session} with {approved, reason} once the user decides, an earlier answer
// covers it, or the timeout passes.
func (b *Bot) handleApproveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in approvalInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil || in.Tool == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	in.Session = strings.TrimSpace(in.Session)
	in.Target = normalizeApprovalTarget(in.Tool, in.Target)
	cfg := b.cfg()
	timeout := defaultApprovalTimeout
	if cfg.ApprovalTimeoutSec > 0 {
		timeout = time.Duration(cfg.ApprovalTimeoutSec) * time.Second
	}
	approved, reason := true, ""
	auto := ""
	switch {
	case !stillGated(cfg, in.Tool):
		// The worker's MCP server got its tool list at start; the user has
		// since allowed these tools ("항상 허용" or the settings), so let it run.
		auto, reason = "auto-always", "설정에서 승인 없이 허용된 도구입니다"
	case in.Target != "" && targetAllowed(cfg, in.Tool, in.Target):
		auto, reason = "auto-target", "사용자가 이 대상은 항상 허용했습니다 ("+in.Tool+":"+in.Target+")"
	case sessionAllowed(in.Session, in.Tool):
		auto, reason = "auto-session", "사용자가 이 대화에서 이 도구를 허용했습니다"
	default:
		approved, reason = b.askApproval(in, timeout)
	}
	if auto != "" {
		log.Printf("[approval] %s %s %s %s", auto, in.Source, in.Tool, in.Target)
		logApproval(approvalLogEntry{Source: in.Source, Tool: in.Tool, Target: in.Target, Session: in.Session, Summary: in.Summary, Decision: auto, By: "host"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"approved": approved, "reason": reason})
}

// askApproval sends the request to the user and waits for an answer.
func (b *Bot) askApproval(in approvalInput, timeout time.Duration) (bool, string) {
	approvals.Lock()
	req := &approvalRequest{ID: newApprovalID(), Source: in.Source, Tool: in.Tool, Summary: redactSecrets(in.Summary),
		Target: in.Target, Session: in.Session, Created: time.Now(), done: make(chan approvalAnswer, 1)}
	approvals.pending[req.ID] = req
	approvals.Unlock()
	defer func() {
		approvals.Lock()
		delete(approvals.pending, req.ID)
		approvals.Unlock()
	}()
	entry := approvalLogEntry{ID: req.ID, Source: req.Source, Tool: req.Tool, Target: req.Target, Session: req.Session, Summary: req.Summary}

	head := fmt.Sprintf("🔐 승인 요청 #%s — %s · %s", req.ID, req.Source, req.Tool)
	if req.Target != "" {
		head += " → " + req.Target
	}
	if s := shortSession(req.Session); s != "" {
		head += " (" + s + ")"
	}
	opts := "!ok " + req.ID
	if req.Session != "" {
		opts += " / !ok " + req.ID + " session"
	}
	if req.Target != "" {
		opts += " / !ok " + req.ID + " target"
	}
	opts += " / !always " + req.ID + " / !no " + req.ID
	msg := fmt.Sprintf("%s\n%s\n\n버튼을 누르거나 %s\n(%d초 안에 답이 없으면 거부합니다)",
		head, truncate(req.Summary, 600), opts, int(timeout.Seconds()))
	sent := 0
	var asked []tgbotapi.Message
	if b.api != nil { // Telegram: the chat message itself is the prompt, with buttons to answer it
		for _, id := range b.cfg().AllowedUserIDs {
			m := tgbotapi.NewMessage(id, msg)
			m.ReplyMarkup = approvalKeyboard(req)
			if sm, err := b.api.Send(m); err == nil {
				asked = append(asked, sm)
				sent++
			} else {
				log.Printf("[approval] telegram send: %v", err)
			}
		}
	}
	verdict := "⌛ 시간 초과로 거부"
	// The buttons go once the request is answered, so an old one cannot be tapped.
	defer func() {
		for _, m := range asked {
			_, _ = b.api.Send(tgbotapi.NewEditMessageText(m.Chat.ID, m.MessageID, msg+"\n\n→ "+verdict))
		}
	}()
	// Desktop / web chat: a dialog with buttons.
	sent += broadcastControl(approvalEvent("approval", map[string]any{
		"id": req.ID, "source": req.Source, "tool": req.Tool, "summary": truncate(req.Summary, 600), "timeoutSec": int(timeout.Seconds()),
		"target": req.Target, "session": shortSession(req.Session), "hasSession": req.Session != "",
	}))
	defer broadcastControl(approvalEvent("approval_done", map[string]any{"id": req.ID}))
	log.Printf("[approval] #%s asked on %d channel(s): %s %s %s", req.ID, sent, req.Source, req.Tool, req.Target)
	if sent == 0 {
		entry.Decision, entry.By = "denied", "none"
		logApproval(entry)
		return false, "승인을 물어볼 채팅 채널이 없어 거부했습니다"
	}
	select {
	case a := <-req.done:
		entry.By = a.by
		if !a.ok {
			log.Printf("[approval] #%s denied (%s)", req.ID, a.by)
			verdict = "🚫 거부됨"
			entry.Decision = "denied"
			logApproval(entry)
			return false, "사용자가 거부했습니다"
		}
		entry.Decision, entry.Scope = "approved", a.scope
		verdict = b.applyApprovalScope(req, a.scope)
		log.Printf("[approval] #%s approved scope=%s (%s)", req.ID, a.scope, a.by)
		logApproval(entry)
		return true, "사용자가 승인했습니다"
	case <-time.After(timeout):
		log.Printf("[approval] #%s timed out", req.ID)
		entry.Decision = "timeout"
		logApproval(entry)
		return false, fmt.Sprintf("%d초 안에 승인되지 않아 거부했습니다", int(timeout.Seconds()))
	}
}

// applyApprovalScope makes an approval reach beyond this call and returns the
// verdict line shown under the Telegram prompt.
func (b *Bot) applyApprovalScope(req *approvalRequest, scope string) string {
	switch scope {
	case scopeSession:
		grantApprovalSession(req.Session, req.Tool)
		return "💬 이 대화에서 허용됨 (" + req.Tool + ")"
	case scopeTarget:
		entry := req.Tool + ":" + req.Target
		if err := allowApprovalTarget(b.cfg(), entry); err != nil {
			log.Printf("[approval] #%s target-allow save: %v", req.ID, err)
			return "✅ 승인됨 (대상 허용 저장 실패: " + err.Error() + ")"
		}
		return "🎯 이 대상은 항상 허용됨 (" + entry + ")"
	case scopeAlways:
		if err := allowToolsAlways(b.cfg(), req.Tool); err != nil {
			log.Printf("[approval] #%s always-allow save: %v", req.ID, err)
			return "✅ 승인됨 (항상 허용 저장 실패: " + err.Error() + ")"
		}
		return "♾️ 항상 허용됨 (다음부터 묻지 않음)"
	}
	return "✅ 승인됨"
}

// shortSession is the conversation part of an owner label, for prompts:
// "chat:1/conv:ab12cd34ef" → "대화 ab12cd34".
func shortSession(s string) string {
	if s == "" {
		return ""
	}
	id := s
	if i := strings.LastIndex(s, ":"); i >= 0 && i+1 < len(s) {
		id = s[i+1:]
	}
	if r := []rune(id); len(r) > 8 {
		id = string(r[:8])
	}
	return "대화 " + id
}

// approvalKeyboard is the buttons under a Telegram request: a tap on the phone
// instead of typing "!ok k7". 💬/🎯 only appear when the request has a
// conversation / a target for them to apply to.
func approvalKeyboard(req *approvalRequest) tgbotapi.InlineKeyboardMarkup {
	row1 := []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("✅ 이번만", "ap:ok:"+req.ID)}
	if req.Session != "" {
		row1 = append(row1, tgbotapi.NewInlineKeyboardButtonData("💬 이 대화", "ap:se:"+req.ID))
	}
	if req.Target != "" {
		row1 = append(row1, tgbotapi.NewInlineKeyboardButtonData("🎯 이 대상", "ap:tg:"+req.ID))
	}
	return tgbotapi.NewInlineKeyboardMarkup(row1, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("♾️ 항상", "ap:al:"+req.ID),
		tgbotapi.NewInlineKeyboardButtonData("❌ 거부", "ap:no:"+req.ID),
	))
}

// callbackScopes maps the Telegram button codes to scopes.
var callbackScopes = map[string]string{"ok": scopeOnce, "se": scopeSession, "tg": scopeTarget, "al": scopeAlways}

// scopeToast is the short confirmation for an approval with that scope.
var scopeToast = map[string]string{
	scopeOnce:    "✅ 이번만 승인했습니다",
	scopeSession: "💬 이 대화에서 허용했습니다",
	scopeTarget:  "🎯 이 대상을 항상 허용했습니다",
	scopeAlways:  "♾️ 항상 허용했습니다",
}

// handleCallback answers a button tap. The request's message loses its
// buttons when askApproval returns; the tap itself gets a short toast.
func (b *Bot) handleCallback(cq *tgbotapi.CallbackQuery) {
	toast := ""
	if rest, ok := strings.CutPrefix(cq.Data, "ap:"); ok {
		answer, id, _ := strings.Cut(rest, ":")
		scope, approve := callbackScopes[answer]
		req, msg := resolveApprovalScope(id, approve, scope, "telegram")
		switch {
		case req == nil:
			toast = msg
		case approve:
			toast = scopeToast[scope]
		default:
			toast = "🚫 거부했습니다"
		}
	}
	if _, err := b.api.Request(tgbotapi.NewCallback(cq.ID, toast)); err != nil {
		log.Printf("[approval] callback answer: %v", err)
	}
}

// resolveApproval answers a pending request once. With id "" it picks the
// only pending one, so a quick "!ok" works when there is just one.
func resolveApproval(id string, ok bool) (*approvalRequest, string) {
	return resolveApprovalScope(id, ok, scopeOnce, "command")
}

// resolveApprovalAlways is resolveApproval that, with always, also stops
// asking for the request's tools once askApproval sees the answer.
func resolveApprovalAlways(id string, ok, always bool) (*approvalRequest, string) {
	scope := scopeOnce
	if always {
		scope = scopeAlways
	}
	return resolveApprovalScope(id, ok, scope, "desktop")
}

// resolveApprovalScope answers a pending request: approve (ok) with a scope,
// or refuse. by names the channel for the log. A scope the request cannot take
// (session with no conversation, target with no target) is refused with a
// message and leaves the request pending.
func resolveApprovalScope(id string, ok bool, scope, by string) (*approvalRequest, string) {
	if scope == "" {
		scope = scopeOnce
	}
	approvals.Lock()
	defer approvals.Unlock()
	if id == "" {
		switch len(approvals.pending) {
		case 0:
			return nil, "대기 중인 승인 요청이 없습니다."
		case 1:
			for k := range approvals.pending {
				id = k
			}
		default:
			ids := make([]string, 0, len(approvals.pending))
			for k, r := range approvals.pending {
				ids = append(ids, "#"+k+" "+r.Tool)
			}
			sort.Strings(ids)
			return nil, "승인 요청이 여러 개입니다 — 번호를 붙여 주세요: " + strings.Join(ids, ", ")
		}
	}
	req := approvals.pending[strings.TrimPrefix(strings.ToLower(id), "#")]
	if req == nil {
		return nil, fmt.Sprintf("#%s 승인 요청이 없습니다 (이미 처리됐거나 시간이 지났습니다).", id)
	}
	if ok && scope == scopeSession && req.Session == "" {
		return nil, fmt.Sprintf("#%s 는 어느 대화에서 온 요청인지 몰라 '이 대화에서 허용'을 쓸 수 없습니다.", req.ID)
	}
	if ok && scope == scopeTarget && req.Target == "" {
		return nil, fmt.Sprintf("#%s 는 대상(앱·명령)을 알 수 없어 '이 대상만 허용'을 쓸 수 없습니다.", req.ID)
	}
	if !ok {
		scope = ""
	}
	select {
	case req.done <- approvalAnswer{ok: ok, scope: scope, by: by}:
	default:
	}
	return req, ""
}

// approvalScopeWords are the scope words !ok / !approve accept after the id.
var approvalScopeWords = map[string]string{
	"once": scopeOnce, "이번만": scopeOnce,
	"session": scopeSession, "conv": scopeSession, "대화": scopeSession,
	"target": scopeTarget, "대상": scopeTarget,
	"always": scopeAlways, "항상": scopeAlways,
}

// handleApprovalCommand is !ok / !approve [id] [once|session|target|always],
// !no / !deny [id], and !always [id] (= !ok id always).
func (b *Bot) handleApprovalCommand(reply replySender, chatID int64, fields []string, ok bool) {
	id, scope := "", scopeOnce
	if ok && len(fields) > 0 && strings.EqualFold(fields[0], "!always") {
		scope = scopeAlways
	}
	for _, f := range fields[min(1, len(fields)):] {
		if s, isScope := approvalScopeWords[strings.ToLower(f)]; isScope {
			scope = s
		} else if id == "" {
			id = f
		}
	}
	req, msg := resolveApprovalScope(id, ok, scope, "command")
	if req == nil {
		_ = reply.Send(chatID, msg)
		return
	}
	verb := map[string]string{scopeOnce: "✅ 승인", scopeSession: "💬 이 대화에서 허용", scopeTarget: "🎯 이 대상 항상 허용", scopeAlways: "♾️ 항상 허용"}[scope]
	if !ok {
		verb = "🚫 거부"
	}
	what := req.Tool
	if req.Target != "" {
		what += " → " + req.Target
	}
	_ = reply.Send(chatID, fmt.Sprintf("%s: #%s %s · %s", verb, req.ID, req.Source, what))
}

// approvalConfigPath is config.yaml, where "항상 허용" saves approvals.tools
// and "이 대상만 허용" approvals.allow.
var approvalConfigPath string

// stillGated reports whether any of tools ("launch_app", or "a,b" for a
// run_sequence) is still in approvals.tools.
func stillGated(cfg *Config, tools string) bool {
	if cfg == nil {
		return true
	}
	gated := map[string]bool{}
	for _, t := range cfg.ApprovalTools {
		gated[t] = true
	}
	for _, t := range splitToolList(tools) {
		if gated[t] {
			return true
		}
	}
	return false
}

// targetAllowed reports whether approvals.allow holds tool:target. Only a
// single-tool call has a target (normalizeApprovalTarget).
func targetAllowed(cfg *Config, tool, target string) bool {
	if cfg == nil || target == "" {
		return false
	}
	want := normalizeAllowEntry(tool + ":" + target)
	for _, e := range cfg.ApprovalAllow {
		if normalizeAllowEntry(e) == want {
			return true
		}
	}
	return false
}

// updateApprovalConfig applies change to a copy of cfg and saves config.yaml
// (which hot-reloads the live config). change reports whether it changed
// anything; nothing is written when it did not.
func updateApprovalConfig(cfg *Config, change func(*Config) bool) error {
	if cfg == nil || approvalConfigPath == "" {
		return fmt.Errorf("설정 파일 경로를 모릅니다")
	}
	newCfg := *cfg
	newCfg.ApprovalTools = append([]string(nil), cfg.ApprovalTools...)
	newCfg.ApprovalAllow = append([]string(nil), cfg.ApprovalAllow...)
	if !change(&newCfg) {
		return nil
	}
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(persistConfig(approvalConfigPath, &newCfg), &r); err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("%s", r.Error)
	}
	return nil
}

// allowToolsAlways takes tools out of approvals.tools and saves config.yaml,
// so the next call runs without asking — in this worker and every later one.
func allowToolsAlways(cfg *Config, tools string) error {
	drop := map[string]bool{}
	for _, t := range splitToolList(tools) {
		drop[t] = true
	}
	return updateApprovalConfig(cfg, func(c *Config) bool {
		var keep []string
		for _, t := range c.ApprovalTools {
			if !drop[t] {
				keep = append(keep, t)
			}
		}
		if len(keep) == len(c.ApprovalTools) {
			return false
		}
		c.ApprovalTools = keep
		log.Printf("[approval] always allow %s — approvals.tools now %v", tools, keep)
		return true
	})
}

// allowApprovalTarget adds "tool:target" to approvals.allow and saves.
func allowApprovalTarget(cfg *Config, entry string) error {
	e := normalizeAllowEntry(entry)
	if e == "" {
		return fmt.Errorf("잘못된 항목: %q", entry)
	}
	return updateApprovalConfig(cfg, func(c *Config) bool {
		for _, x := range c.ApprovalAllow {
			if normalizeAllowEntry(x) == e {
				return false
			}
		}
		c.ApprovalAllow = append(c.ApprovalAllow, e)
		log.Printf("[approval] target allow %s", e)
		return true
	})
}

// revokeApprovalAllow removes entry from approvals.allow; found reports
// whether it was there.
func revokeApprovalAllow(cfg *Config, entry string) (found bool, err error) {
	e := normalizeAllowEntry(entry)
	err = updateApprovalConfig(cfg, func(c *Config) bool {
		var keep []string
		for _, x := range c.ApprovalAllow {
			if normalizeAllowEntry(x) == e {
				found = true
				continue
			}
			keep = append(keep, x)
		}
		c.ApprovalAllow = keep
		return found
	})
	return found, err
}

// Session grants: "이 대화에서 허용" — tools a conversation may use without
// asking again, in memory only.
const approvalSessionIdle = 12 * time.Hour

type approvalSessionGrant struct {
	tools map[string]bool
	last  time.Time
}

var approvalSessions = struct {
	sync.Mutex
	m map[string]*approvalSessionGrant
}{m: map[string]*approvalSessionGrant{}}

// grantApprovalSession lets session use tools without asking.
func grantApprovalSession(session, tools string) {
	if session == "" {
		return
	}
	approvalSessions.Lock()
	defer approvalSessions.Unlock()
	g := approvalSessions.m[session]
	if g == nil {
		g = &approvalSessionGrant{tools: map[string]bool{}}
		approvalSessions.m[session] = g
	}
	for _, t := range splitToolList(tools) {
		g.tools[t] = true
	}
	g.last = time.Now()
}

// sessionAllowed reports whether session was granted every one of tools. A
// grant unused for approvalSessionIdle lapses.
func sessionAllowed(session, tools string) bool {
	if session == "" {
		return false
	}
	approvalSessions.Lock()
	defer approvalSessions.Unlock()
	g := approvalSessions.m[session]
	if g == nil {
		return false
	}
	if time.Since(g.last) > approvalSessionIdle {
		delete(approvalSessions.m, session)
		return false
	}
	list := splitToolList(tools)
	if len(list) == 0 {
		return false
	}
	for _, t := range list {
		if !g.tools[t] {
			return false
		}
	}
	g.last = time.Now()
	return true
}

// clearApprovalSessions drops the grants of every session whose owner label
// names convID ("…/conv:<convID>"), or of all sessions when convID is "".
// Returns how many were dropped.
func clearApprovalSessions(convID string) int {
	approvalSessions.Lock()
	defer approvalSessions.Unlock()
	n := 0
	for k := range approvalSessions.m {
		if convID == "" || k == convID || strings.HasSuffix(k, "/conv:"+convID) {
			delete(approvalSessions.m, k)
			n++
		}
	}
	return n
}

// listApprovalSessions is "대화 ab12cd34: eval, launch_app" per live grant.
func listApprovalSessions() []string {
	approvalSessions.Lock()
	defer approvalSessions.Unlock()
	var out []string
	for k, g := range approvalSessions.m {
		if time.Since(g.last) > approvalSessionIdle {
			continue
		}
		tools := make([]string, 0, len(g.tools))
		for t := range g.tools {
			tools = append(tools, t)
		}
		sort.Strings(tools)
		out = append(out, shortSession(k)+": "+strings.Join(tools, ", "))
	}
	sort.Strings(out)
	return out
}

// normalizeApprovalTarget derives the short target an "이 대상만 허용" entry
// keys on, from the raw target an MCP server sent: an app name for
// launch_app, a command prefix for vscode_terminal_run, a command id for
// vscode_command, a site host for a URL. "" when there is none — a batch of
// several tools, or a command line that chains commands (an allow for "git
// status" must not let "git status && curl … | sh" through).
func normalizeApprovalTarget(tool, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(tool, ",") {
		return ""
	}
	var t string
	switch tool {
	case "launch_app":
		t = strings.ToLower(trimExeName(raw))
		if strings.ContainsAny(t, ",:;\"") {
			t = ""
		}
	case "vscode_terminal_run":
		t = commandPrefixTarget(raw)
	case "vscode_command":
		if !strings.ContainsAny(raw, " \t,") {
			t = strings.ToLower(raw)
		}
	default:
		t = urlHostTarget(raw)
	}
	if len(t) > 80 {
		return ""
	}
	return t
}

// trimExeName is the base name of a program path without its extension:
// `C:\Windows\notepad.exe` → "notepad".
func trimExeName(s string) string {
	s = strings.Trim(strings.TrimSpace(s), `"'`)
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	lower := strings.ToLower(s)
	for _, ext := range []string{".exe", ".lnk", ".cmd", ".bat", ".ps1", ".com"} {
		if strings.HasSuffix(lower, ext) {
			return s[:len(s)-len(ext)]
		}
	}
	return s
}

// Programs whose second word picks what they do ("git status", "go test").
var subcommandPrograms = map[string]bool{
	"git": true, "go": true, "npm": true, "pnpm": true, "yarn": true, "npx": true, "cargo": true,
	"docker": true, "kubectl": true, "dotnet": true, "task": true, "make": true, "pip": true,
	"gh": true, "wails3": true, "flutter": true, "adb": true, "gradle": true, "gradlew": true, "systemctl": true,
	// Interpreters: the script is the target ("python manage.py"); a flag
	// ("python -c …") leaves no target.
	"python": true, "python3": true, "py": true, "node": true, "deno": true, "bun": true,
}

// Programs that run arbitrary other commands: no target, so no one-click
// allow that would cover everything.
var wrapperPrograms = map[string]bool{
	"cmd": true, "powershell": true, "pwsh": true, "bash": true, "sh": true, "zsh": true, "fish": true,
	"wsl": true, "sudo": true, "su": true, "env": true, "xargs": true, "nohup": true, "start": true,
	"call": true, "iex": true, "invoke-expression": true, "eval": true, "exec": true, "ssh": true, "time": true,
	"perl": true, "ruby": true,
}

var interpreterPrograms = map[string]bool{"python": true, "python3": true, "py": true, "node": true, "deno": true, "bun": true}

// commandPrefixTarget is the first word of a single, plain command — or the
// first two for subcommandPrograms. "" for chained, piped, redirected or
// substituted command lines.
func commandPrefixTarget(cmd string) string {
	if strings.ContainsAny(cmd, "&|;`$<>(){}\n\r\"'") {
		return ""
	}
	words := strings.Fields(cmd)
	if len(words) == 0 {
		return ""
	}
	first := strings.ToLower(trimExeName(words[0]))
	if first == "" || strings.HasPrefix(first, "-") || wrapperPrograms[first] || strings.ContainsAny(first, ",:") {
		return ""
	}
	if subcommandPrograms[first] && len(words) > 1 {
		second := strings.ToLower(words[1])
		if strings.HasPrefix(second, "-") || strings.ContainsAny(second, ",:") {
			if interpreterPrograms[first] {
				return ""
			}
			return first
		}
		return first + " " + second
	}
	if interpreterPrograms[first] {
		return "" // a bare REPL, or nothing to key on
	}
	return first
}

// urlHostTarget is the host of an http(s) URL, without "www." and port.
func urlHostTarget(raw string) string {
	rest, ok := strings.CutPrefix(strings.ToLower(raw), "https://")
	if !ok {
		if rest, ok = strings.CutPrefix(strings.ToLower(raw), "http://"); !ok {
			return ""
		}
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	if i := strings.LastIndex(rest, ":"); i >= 0 && !strings.Contains(rest, "]") {
		rest = rest[:i]
	}
	rest = strings.TrimPrefix(rest, "www.")
	if rest == "" || strings.ContainsAny(rest, " ,;\"'") {
		return ""
	}
	return rest
}
