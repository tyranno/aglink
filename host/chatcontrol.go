package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// chatControlServer is a loopback-only WebSocket endpoint that a separate
// aglink-chat process connects to as a client. Each connection registers a
// remoteChatChannel with the Hub — the same role telegramChannel/webChannel play,
// but over a socket — and feeds inbound control requests into the Bot. It is off
// unless chat_control.enabled, and never affects the embedded web_chat server.
type chatControlServer struct {
	addr        string
	token       string
	ownerChatID int64
	hub         *Hub
	bot         *Bot
	cfgPath     string       // config.yaml path (get_config/set_config relay target)
	connCount   atomic.Int64 // number of currently connected aglink-chat clients
}

// controlIn is a request from aglink-chat.
type controlIn struct {
	Type    string  `json:"type"` // send_text | handle_command | list_conversations | get_active_workers | get_history | upload_attachment | web_new | web_setdir | web_rename | web_delete | set_channel_backend | get_version | get_aux | get_config | set_config | get_settings | set_settings | playbook_list | playbook_save | playbook_delete | pbgroup_save | pbgroup_delete | playbook_run
	ReqID   string  `json:"reqID,omitempty"`
	ChatID  int64   `json:"chatID,omitempty"`
	Text    string  `json:"text,omitempty"`
	Origin  string  `json:"origin,omitempty"`
	Path    string  `json:"path,omitempty"`
	Caption string  `json:"caption,omitempty"`
	Target  *Target `json:"target,omitempty"`
	ID      string  `json:"id,omitempty"`
	Title   string  `json:"title,omitempty"`
	Backend string  `json:"backend,omitempty"`
	Body    string  `json:"body,omitempty"`    // set_config: edited config.yaml text
	Payload json.RawMessage `json:"payload,omitempty"` // playbook_save/pbgroup_save: the Playbook/PlaybookGroup JSON
}

// controlOut is a message to aglink-chat: either a Hub-driven browser frame
// (kind="frame", Frame is the same wsFrame the browser already understands) or a
// reply to a request (kind="reply", Data is the JSON payload).
type controlOut struct {
	Kind  string          `json:"kind"`
	Frame *wsFrame        `json:"frame,omitempty"`
	ReqID string          `json:"reqID,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// remoteChatChannel is a ChannelSender backed by the control-API socket. It
// mirrors webChannel but serializes frames to aglink-chat (which forwards them to
// real browsers) instead of a browser socket directly. Frames and request replies
// share one buffered channel drained by a single writer, since a WebSocket allows
// only one concurrent writer; a full buffer closes the connection (drop the slow
// peer rather than block the Hub).
type remoteChatChannel struct {
	send      chan controlOut
	closeOnce sync.Once
	cancel    context.CancelFunc
}

func (r *remoteChatChannel) push(o controlOut) {
	select {
	case r.send <- o:
	default:
		r.close()
	}
}
func (r *remoteChatChannel) close() { r.closeOnce.Do(func() { r.cancel() }) }

// Every frame carries its Target so the browser files it under the conversation
// it belongs to. Without the tag the browser appends whatever arrives to
// whichever conversation is open, mixing the telegram stream into web topics.
// The web channel receives both kinds — it renders the telegram stream as one of
// its conversations — so this tag is the only thing separating them.

func (r *remoteChatChannel) Send(tgt Target, _ int64, text string) error {
	r.push(controlOut{Kind: "frame", Frame: &wsFrame{Type: "text", Text: text, Target: &tgt}})
	return nil
}
func (r *remoteChatChannel) SendPhoto(tgt Target, _ int64, png []byte, caption string) error {
	r.push(controlOut{Kind: "frame", Frame: &wsFrame{Type: "image", Caption: caption, Data: base64.StdEncoding.EncodeToString(png), Target: &tgt}})
	return nil
}
func (r *remoteChatChannel) Typing(tgt Target, _ int64) {
	r.push(controlOut{Kind: "frame", Frame: &wsFrame{Type: "typing", Target: &tgt}})
}
func (r *remoteChatChannel) Done(tgt Target, _ int64) {
	r.push(controlOut{Kind: "frame", Frame: &wsFrame{Type: "done", Target: &tgt}})
}
func (r *remoteChatChannel) Progress(tgt Target, _ int64, text string) {
	r.push(controlOut{Kind: "frame", Frame: &wsFrame{Type: "progress", Text: text, Target: &tgt}})
}
func (r *remoteChatChannel) EchoUser(tgt Target, _ int64, text, origin string) {
	// Same rule as webChannel: mirror Telegram input as a user bubble; web-origin
	// was already rendered locally by the sending browser.
	if origin == OriginTelegram {
		r.push(controlOut{Kind: "frame", Frame: &wsFrame{Type: "user", Text: text, Target: &tgt}})
	}
}

func (s *chatControlServer) authOK(r *http.Request) bool { return originOK(r) && tokenOK(r, s.token) }

func (s *chatControlServer) handleControl(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"127.0.0.1:*", "localhost:*"},
	})
	if err != nil {
		return
	}
	// coder/websocket defaults to a 32KiB read limit; a large set_config body
	// (edited config.yaml) could exceed it, dropping the connection the same
	// way an oversized get_history reply does on aglink-chat's side (see the
	// matching SetReadLimit in aglink-chat's controlClient.connectOnce).
	c.SetReadLimit(8 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	ch := &remoteChatChannel{send: make(chan controlOut, 64), cancel: cancel}
	s.hub.Register(s.ownerChatID, ch)
	defer s.hub.Unregister(s.ownerChatID, ch)
	defer c.Close(websocket.StatusNormalClosure, "")
	s.connCount.Add(1)
	defer s.connCount.Add(-1)
	log.Printf("[chatcontrol] aglink-chat connected")

	// Single writer goroutine (a WebSocket allows only one concurrent writer).
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case o := <-ch.send:
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				werr := wsjson.Write(wctx, c, o)
				wcancel()
				if werr != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Reader loop (blocks until disconnect).
	for {
		var m controlIn
		if rerr := wsjson.Read(ctx, c, &m); rerr != nil {
			break
		}
		s.handleInbound(ch, m)
	}
	cancel()
	log.Printf("[chatcontrol] aglink-chat disconnected")
}

// handleInbound routes a control request into the Bot — the same entry points the
// embedded web server uses (dispatchTargeted/handleCommand/ingestAttachment), so
// all the recently added behavior (origin tagging, cross-channel echo, working
// indicator) applies identically.
func (s *chatControlServer) handleInbound(ch *remoteChatChannel, m controlIn) {
	chatID := m.ChatID
	if chatID == 0 {
		chatID = s.ownerChatID
	}
	origin := m.Origin
	if origin == "" {
		origin = OriginWeb
	}
	// The conversation this request came from. Every reply — command output,
	// errors, rate-limit notices — goes back to it and nowhere else. A request
	// with no target is the telegram stream, the always-present default.
	tgt := TelegramTarget()
	if m.Target != nil {
		tgt = *m.Target
	}

	switch m.Type {
	case "send_text":
		text := strings.TrimSpace(m.Text)
		if text == "" {
			return
		}
		if strings.HasPrefix(text, "!") {
			go s.bot.handleCommand(chatID, text, origin, tgt)
			return
		}
		if s.bot.rateLimiter != nil && !s.bot.rateLimiter.Allow(chatID) {
			_ = s.bot.ReplyTo(tgt).Send(chatID, "⚠️ 요청이 너무 많습니다. 잠시 후 다시 시도해 주세요.")
			return
		}
		go s.bot.dispatchTargeted(chatID, text, m.Target)
	case "handle_command":
		go s.bot.handleCommand(chatID, m.Text, origin, tgt)
	case "list_conversations":
		resp := buildConversationsResponse(s.bot.store)
		if s.bot != nil && s.bot.manager != nil {
			resp.BackendModels = s.bot.manager.BackendModels()
		}
		data, err := json.Marshal(resp)
		if err != nil {
			log.Printf("[chatcontrol] list_conversations marshal: %v", err)
			return
		}
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: data})
	// The authoritative answer to "is this conversation still working?". A client
	// polls it to correct an indicator that a dropped Done frame would otherwise
	// leave spinning; the pushed frames stay the fast path.
	case "get_active_workers":
		var workers []WorkerStatus
		if s.bot != nil && s.bot.manager != nil {
			workers = s.bot.manager.ActiveWorkers()
		}
		data, err := json.Marshal(buildActiveWorkersResponse(workers, s.bot.cfg().TimeoutMinutes))
		if err != nil {
			log.Printf("[chatcontrol] get_active_workers marshal: %v", err)
			return
		}
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: data})
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
	case "upload_attachment":
		go s.bot.ingestAttachmentTargeted(chatID, m.Path, m.Caption, origin, m.Target)
	// Web-conversation management is browser-only. Addressing the reply to a web
	// target keeps these confirmations out of Telegram by construction (see
	// Hub.targets), whatever the requester sent.
	case "web_new":
		go s.bot.webNew(s.bot.ReplyTo(AsWebTarget(tgt)), chatID, m.Title)
	case "web_setdir":
		go s.bot.webSetDir(s.bot.ReplyTo(WebTarget(m.ID)), chatID, m.ID, m.Path)
	case "web_rename":
		// Synchronous + acknowledged: a rename must be durable before the client
		// refreshes, or a fast page reload re-fetches /api/conversations while the
		// write is still in flight and shows the old title. webRename is a map
		// write + one file save — short enough to run inline on the read loop.
		err := s.bot.webRename(s.bot.ReplyTo(WebTarget(m.ID)), chatID, m.ID, m.Title)
		result := map[string]any{"ok": err == nil}
		if err != nil {
			result["error"] = err.Error()
		}
		data, merr := json.Marshal(result)
		if merr != nil {
			log.Printf("[chatcontrol] web_rename marshal: %v", merr)
			return
		}
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: data})
	case "web_delete":
		go s.bot.webDelete(s.bot.ReplyTo(WebTarget(m.ID)), chatID, m.ID)
	case "set_channel_backend":
		out := map[string]any{"ok": true}
		if err := s.bot.setChannelBackend(tgt, m.Backend); err != nil {
			out["ok"] = false
			out["error"] = err.Error()
		}
		data, _ := json.Marshal(out)
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: data})
	case "get_version":
		backend := ""
		if s.bot != nil && s.bot.manager != nil {
			backend = s.bot.manager.Backend()
		}
		data, _ := json.Marshal(versionPayload(backend))
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: data})
	case "get_aux":
		cfg := s.bot.cfg()
		feats := buildAuxFeatures(int(s.connCount.Load()), cfg.ChatControl, cfg.ChatControlAddr)
		data, _ := json.Marshal(map[string]any{"features": feats})
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: data})
	case "get_config":
		out := map[string]any{}
		if masked, err := readMaskedConfig(s.cfgPath, s.bot.cfg()); err != nil {
			out["error"] = err.Error()
		} else {
			out["config"] = string(masked)
		}
		data, _ := json.Marshal(out)
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: data})
	case "set_config":
		out := map[string]any{"ok": true}
		if err := writeValidatedConfig(s.cfgPath, s.bot.cfg(), []byte(m.Body)); err != nil {
			out["ok"] = false
			out["error"] = err.Error()
		}
		data, _ := json.Marshal(out)
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: data})
	case "get_settings":
		var manager *Manager
		if s.bot != nil {
			manager = s.bot.manager
		}
		data, _ := json.Marshal(map[string]any{"sections": buildSettings(s.bot.cfg(), codexModelOptionsFor(manager))})
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: data})
	case "set_settings":
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: applySettingsUpdate(s.cfgPath, s.bot.cfg(), []byte(m.Body))})
	case "playbook_list":
		data, err := json.Marshal(buildPlaybooksResponse(s.bot.playbooks))
		if err != nil {
			log.Printf("[chatcontrol] playbook_list marshal: %v", err)
			return
		}
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: data})
	case "playbook_save":
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: s.savePlaybook(m.Payload)})
	case "playbook_delete":
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: mutateResult(deletePlaybook(s.bot.playbooks, m.ID))})
	case "pbgroup_save":
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: s.savePlaybookGroup(m.Payload)})
	case "pbgroup_delete":
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: mutateResult(deletePlaybookGroup(s.bot.playbooks, m.ID))})
	case "playbook_run":
		// Compose the routine into a prompt, spin up a fresh web conversation, and
		// dispatch it — one click repeats the whole routine. The reply carries the
		// new conversation id so the client can switch to it.
		out := map[string]any{"ok": true}
		if convID, err := s.bot.runPlaybook(chatID, m.ID); err != nil {
			out["ok"] = false
			out["error"] = err.Error()
		} else {
			out["conversationId"] = convID
		}
		data, _ := json.Marshal(out)
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: data})
	default:
		log.Printf("[chatcontrol] unknown control message type %q", m.Type)
	}
}

func (s *chatControlServer) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc("/control", s.handleControl)
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		log.Printf("[chatcontrol] listen %s failed: %v — chat control disabled", s.addr, err)
		return
	}
	log.Printf("[chatcontrol] control API on ws://%s/control", s.addr)
	srv := &http.Server{Handler: mux}
	if serr := srv.Serve(ln); serr != nil {
		log.Printf("[chatcontrol] server stopped: %v", serr)
	}
}
