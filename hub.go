package main

import (
	"log"
	"sync"
)

// ChannelSender is the full output surface a transport channel must provide.
// Extends the relay MessageSender (Send/Typing) with photo delivery so images
// fan out too. Telegram and each web connection implement this.
type ChannelSender interface {
	Send(chatID int64, text string) error
	SendPhoto(chatID int64, png []byte, caption string) error
	Typing(chatID int64)
	Done(chatID int64)
}

// Hub fans outgoing messages to every registered channel. Global channels
// (Telegram) receive traffic for every chatID; per-chat channels (web sessions)
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

// targets returns the fan-out set for chatID: all global channels plus any
// per-chat channels bound to that chatID (copied so we don't hold the lock
// while calling into channels).
func (h *Hub) targets(chatID int64) []ChannelSender {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]ChannelSender, 0, len(h.global)+len(h.perChat[chatID]))
	out = append(out, h.global...)
	out = append(out, h.perChat[chatID]...)
	return out
}

func (h *Hub) Send(chatID int64, text string) error {
	for _, ch := range h.targets(chatID) {
		if err := ch.Send(chatID, text); err != nil {
			log.Printf("[hub] channel send error: %v", err)
		}
	}
	return nil
}

func (h *Hub) SendPhoto(chatID int64, png []byte, caption string) error {
	for _, ch := range h.targets(chatID) {
		if err := ch.SendPhoto(chatID, png, caption); err != nil {
			log.Printf("[hub] channel photo error: %v", err)
		}
	}
	return nil
}

func (h *Hub) Typing(chatID int64) {
	for _, ch := range h.targets(chatID) {
		ch.Typing(chatID)
	}
}

func (h *Hub) Done(chatID int64) {
	for _, ch := range h.targets(chatID) {
		ch.Done(chatID)
	}
}
