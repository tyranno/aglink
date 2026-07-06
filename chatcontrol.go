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
}

// controlIn is a request from aglink-chat.
type controlIn struct {
	Type    string  `json:"type"` // send_text | handle_command | list_conversations | upload_attachment
	ReqID   string  `json:"reqID,omitempty"`
	ChatID  int64   `json:"chatID,omitempty"`
	Text    string  `json:"text,omitempty"`
	Origin  string  `json:"origin,omitempty"`
	Path    string  `json:"path,omitempty"`
	Caption string  `json:"caption,omitempty"`
	Target  *Target `json:"target,omitempty"`
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

func (r *remoteChatChannel) Send(_ int64, text string) error {
	r.push(controlOut{Kind: "frame", Frame: &wsFrame{Type: "text", Text: text}})
	return nil
}
func (r *remoteChatChannel) SendPhoto(_ int64, png []byte, caption string) error {
	r.push(controlOut{Kind: "frame", Frame: &wsFrame{Type: "image", Caption: caption, Data: base64.StdEncoding.EncodeToString(png)}})
	return nil
}
func (r *remoteChatChannel) Typing(_ int64) {
	r.push(controlOut{Kind: "frame", Frame: &wsFrame{Type: "typing"}})
}
func (r *remoteChatChannel) Done(_ int64) {
	r.push(controlOut{Kind: "frame", Frame: &wsFrame{Type: "done"}})
}
func (r *remoteChatChannel) EchoUser(_ int64, text, origin string) {
	// Same rule as webChannel: mirror Telegram input as a user bubble; web-origin
	// was already rendered locally by the sending browser.
	if origin == OriginTelegram {
		r.push(controlOut{Kind: "frame", Frame: &wsFrame{Type: "user", Text: text}})
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
	ctx, cancel := context.WithCancel(context.Background())
	ch := &remoteChatChannel{send: make(chan controlOut, 64), cancel: cancel}
	s.hub.Register(s.ownerChatID, ch)
	defer s.hub.Unregister(s.ownerChatID, ch)
	defer c.Close(websocket.StatusNormalClosure, "")
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
	switch m.Type {
	case "send_text":
		text := strings.TrimSpace(m.Text)
		if text == "" {
			return
		}
		if strings.HasPrefix(text, "!") {
			go s.bot.handleCommand(chatID, text, origin)
			return
		}
		if s.bot.rateLimiter != nil && !s.bot.rateLimiter.Allow(chatID) {
			_ = s.bot.Send(chatID, "⚠️ 요청이 너무 많습니다. 잠시 후 다시 시도해 주세요.")
			return
		}
		go s.bot.dispatchTargeted(chatID, text, m.Target)
	case "handle_command":
		go s.bot.handleCommand(chatID, m.Text, origin)
	case "list_conversations":
		data, err := json.Marshal(buildConversationsResponse(s.bot.store))
		if err != nil {
			log.Printf("[chatcontrol] list_conversations marshal: %v", err)
			return
		}
		ch.push(controlOut{Kind: "reply", ReqID: m.ReqID, Data: data})
	case "upload_attachment":
		go s.bot.ingestAttachment(chatID, m.Path, m.Caption, origin)
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
