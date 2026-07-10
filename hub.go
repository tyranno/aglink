package main

import (
	"log"
	"sync"
)

// ChannelSender is the full output surface a transport channel must provide.
// Extends the relay MessageSender (Send/Typing) with photo delivery so images
// fan out too. Telegram and each web connection implement this.
//
// Every method carries the Target the output belongs to (the telegram stream, or
// a specific web topic). Channels use it to label what they emit; the Hub uses it
// to decide who receives at all — see Hub.targets.
type ChannelSender interface {
	Send(tgt Target, chatID int64, text string) error
	SendPhoto(tgt Target, chatID int64, png []byte, caption string) error
	Typing(tgt Target, chatID int64)
	Done(tgt Target, chatID int64)
	// EchoUser mirrors a user's *input* text to the OTHER channel so both sides
	// see what was typed. origin is the channel the text came from; each channel
	// acts only when it is NOT that origin (Telegram relays web input as a bot
	// message; web shows Telegram input as a user bubble), so fanning to every
	// channel never double-echoes the origin channel.
	EchoUser(tgt Target, chatID int64, text, origin string)
}

// Hub fans outgoing messages to the channels a Target belongs to. Global channels
// (Telegram) are registered for every chatID; per-chat channels (web sessions)
// receive only their bound chatID. A per-channel error is logged and isolated so
// one dead channel never blocks the others. Hub itself satisfies ChannelSender.
type Hub struct {
	mu      sync.RWMutex
	global  []ChannelSender
	perChat map[int64][]ChannelSender
}

func NewHub() *Hub {
	return &Hub{perChat: make(map[int64][]ChannelSender)}
}

func (h *Hub) RegisterGlobal(ch ChannelSender) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.global = append(h.global, ch)
}

func (h *Hub) Register(chatID int64, ch ChannelSender) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.perChat[chatID] = append(h.perChat[chatID], ch)
}

func (h *Hub) Unregister(chatID int64, ch ChannelSender) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.perChat[chatID]
	for i, c := range list {
		if c == ch {
			h.perChat[chatID] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(h.perChat[chatID]) == 0 {
		delete(h.perChat, chatID)
	}
}

// targets returns the fan-out set for a Target: the per-chat channels bound to
// chatID (the web sessions), plus the global channels (Telegram) — but only when
// the output belongs to the telegram stream.
//
// Telegram and web are separate conversation channels, not two views of one
// stream. A web topic is invisible from Telegram, so its output must never reach
// a global channel; the browser, in contrast, renders the telegram stream as one
// of its conversations and does receive it (tagged, so it lands in the right
// place). An empty/unknown Kind means the telegram stream — see HandleWebTarget,
// which treats anything that is not "web" as the global stream.
//
// The result is copied so we don't hold the lock while calling into channels.
func (h *Hub) targets(tgt Target, chatID int64) []ChannelSender {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]ChannelSender, 0, len(h.global)+len(h.perChat[chatID]))
	if tgt.Kind != TargetWeb {
		out = append(out, h.global...)
	}
	out = append(out, h.perChat[chatID]...)
	return out
}

func (h *Hub) Send(tgt Target, chatID int64, text string) error {
	for _, ch := range h.targets(tgt, chatID) {
		if err := ch.Send(tgt, chatID, text); err != nil {
			log.Printf("[hub] channel send error: %v", err)
		}
	}
	return nil
}

func (h *Hub) SendPhoto(tgt Target, chatID int64, png []byte, caption string) error {
	for _, ch := range h.targets(tgt, chatID) {
		if err := ch.SendPhoto(tgt, chatID, png, caption); err != nil {
			log.Printf("[hub] channel photo error: %v", err)
		}
	}
	return nil
}

func (h *Hub) Typing(tgt Target, chatID int64) {
	for _, ch := range h.targets(tgt, chatID) {
		ch.Typing(tgt, chatID)
	}
}

func (h *Hub) Done(tgt Target, chatID int64) {
	for _, ch := range h.targets(tgt, chatID) {
		ch.Done(tgt, chatID)
	}
}

// EchoUser fans a user-input echo to the channels for tgt. Each channel no-ops
// when the origin is its own kind, so the origin channel is never double-echoed
// (the web tab already showed it locally; the Telegram client already shows the
// user's own message). Web-topic input never reaches Telegram at all, because
// targets() excludes the global channels for it.
func (h *Hub) EchoUser(tgt Target, chatID int64, text, origin string) {
	for _, ch := range h.targets(tgt, chatID) {
		ch.EchoUser(tgt, chatID, text, origin)
	}
}
