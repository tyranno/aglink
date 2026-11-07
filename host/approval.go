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

type approvalRequest struct {
	ID      string
	Source  string // aglink-web | aglink-screen
	Tool    string
	Summary string
	Created time.Time
	done    chan bool
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
// MCP entries when approvals are on.
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

// handleApproveHTTP answers POST /approve {source, tool, summary} with
// {approved, reason} once the user decides or the timeout passes.
func (b *Bot) handleApproveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Source  string `json:"source"`
		Tool    string `json:"tool"`
		Summary string `json:"summary"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil || in.Tool == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cfg := b.cfg()
	timeout := defaultApprovalTimeout
	if cfg.ApprovalTimeoutSec > 0 {
		timeout = time.Duration(cfg.ApprovalTimeoutSec) * time.Second
	}
	approved, reason := b.askApproval(in.Source, in.Tool, in.Summary, timeout)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"approved": approved, "reason": reason})
}

// askApproval sends the request to the user and waits for !ok / !no.
func (b *Bot) askApproval(source, tool, summary string, timeout time.Duration) (bool, string) {
	approvals.Lock()
	req := &approvalRequest{ID: newApprovalID(), Source: source, Tool: tool, Summary: redactSecrets(summary), Created: time.Now(), done: make(chan bool, 1)}
	approvals.pending[req.ID] = req
	approvals.Unlock()
	defer func() {
		approvals.Lock()
		delete(approvals.pending, req.ID)
		approvals.Unlock()
	}()

	msg := fmt.Sprintf("🔐 승인 요청 #%s — %s · %s\n%s\n\n버튼을 누르거나 !ok %s / !no %s\n(%d초 안에 답이 없으면 거부합니다)",
		req.ID, source, tool, truncate(req.Summary, 600), req.ID, req.ID, int(timeout.Seconds()))
	sent := 0
	var asked []tgbotapi.Message
	if b.api != nil { // Telegram: the chat message itself is the prompt, with buttons to answer it
		for _, id := range b.cfg().AllowedUserIDs {
			m := tgbotapi.NewMessage(id, msg)
			m.ReplyMarkup = approvalKeyboard(req.ID)
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
		"id": req.ID, "source": source, "tool": tool, "summary": truncate(req.Summary, 600), "timeoutSec": int(timeout.Seconds()),
	}))
	defer broadcastControl(approvalEvent("approval_done", map[string]any{"id": req.ID}))
	log.Printf("[approval] #%s asked on %d channel(s): %s %s", req.ID, sent, source, tool)
	if sent == 0 {
		return false, "승인을 물어볼 채팅 채널이 없어 거부했습니다"
	}
	select {
	case ok := <-req.done:
		log.Printf("[approval] #%s %v", req.ID, map[bool]string{true: "approved", false: "denied"}[ok])
		if ok {
			verdict = "✅ 승인됨"
			return true, "사용자가 승인했습니다"
		}
		verdict = "🚫 거부됨"
		return false, "사용자가 거부했습니다"
	case <-time.After(timeout):
		log.Printf("[approval] #%s timed out", req.ID)
		return false, fmt.Sprintf("%d초 안에 승인되지 않아 거부했습니다", int(timeout.Seconds()))
	}
}

// approvalKeyboard is the 승인/거부 buttons under a Telegram request: a tap on
// the phone instead of typing "!ok k7".
func approvalKeyboard(id string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✅ 승인", "ap:ok:"+id),
		tgbotapi.NewInlineKeyboardButtonData("🚫 거부", "ap:no:"+id),
	))
}

// handleCallback answers a button tap. The request's message loses its
// buttons when askApproval returns; the tap itself gets a short toast.
func (b *Bot) handleCallback(cq *tgbotapi.CallbackQuery) {
	toast := ""
	if rest, ok := strings.CutPrefix(cq.Data, "ap:"); ok {
		answer, id, _ := strings.Cut(rest, ":")
		req, msg := resolveApproval(id, answer == "ok")
		switch {
		case req == nil:
			toast = msg
		case answer == "ok":
			toast = "✅ 승인했습니다"
		default:
			toast = "🚫 거부했습니다"
		}
	}
	if _, err := b.api.Request(tgbotapi.NewCallback(cq.ID, toast)); err != nil {
		log.Printf("[approval] callback answer: %v", err)
	}
}

// resolveApproval answers a pending request. With id "" it picks the only
// pending one, so a quick "!ok" works when there is just one.
func resolveApproval(id string, ok bool) (*approvalRequest, string) {
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
	select {
	case req.done <- ok:
	default:
	}
	return req, ""
}

// handleApprovalCommand is !ok / !no [id].
func (b *Bot) handleApprovalCommand(reply replySender, chatID int64, fields []string, ok bool) {
	id := ""
	if len(fields) > 1 {
		id = fields[1]
	}
	req, msg := resolveApproval(id, ok)
	if req == nil {
		_ = reply.Send(chatID, msg)
		return
	}
	verb := "✅ 승인"
	if !ok {
		verb = "🚫 거부"
	}
	_ = reply.Send(chatID, fmt.Sprintf("%s: #%s %s · %s", verb, req.ID, req.Source, req.Tool))
}
