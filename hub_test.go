package main

import (
	"errors"
	"sync"
	"testing"
)

// recCh is a ChannelSender test double recording calls; optional forced error.
type recCh struct {
	mu      sync.Mutex
	texts   []string
	photos  int
	typings int
	sendErr error
}

func (r *recCh) Send(_ int64, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	return r.sendErr
}
func (r *recCh) SendPhoto(_ int64, _ []byte, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.photos++
	return nil
}
func (r *recCh) Typing(_ int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.typings++
}

func TestHubFanOut_GlobalPlusPerChat(t *testing.T) {
	h := NewHub()
	g := &recCh{}     // telegram-like global
	w := &recCh{}     // web, bound to chat 7
	other := &recCh{} // web bound to a different chat
	h.RegisterGlobal(g)
	h.Register(7, w)
	h.Register(99, other)

	_ = h.Send(7, "hi")
	_ = h.SendPhoto(7, []byte("png"), "cap")
	h.Typing(7)

	if len(g.texts) != 1 || g.texts[0] != "hi" {
		t.Errorf("global should get the text, got %v", g.texts)
	}
	if len(w.texts) != 1 || g.photos != 1 || w.photos != 1 || g.typings != 1 || w.typings != 1 {
		t.Errorf("chat-7 channels should receive; g=%+v w=%+v", g, w)
	}
	if len(other.texts) != 0 || other.photos != 0 || other.typings != 0 {
		t.Errorf("chat-99 channel must NOT receive chat-7 traffic, got %+v", other)
	}
}

func TestHubUnregister(t *testing.T) {
	h := NewHub()
	w := &recCh{}
	h.Register(7, w)
	h.Unregister(7, w)
	_ = h.Send(7, "hi")
	if len(w.texts) != 0 {
		t.Errorf("unregistered channel must not receive, got %v", w.texts)
	}
}

func TestHubErrorIsolation(t *testing.T) {
	h := NewHub()
	bad := &recCh{sendErr: errors.New("boom")}
	good := &recCh{}
	h.RegisterGlobal(bad)
	h.Register(7, good)
	if err := h.Send(7, "hi"); err != nil {
		t.Errorf("Hub.Send should swallow per-channel errors, got %v", err)
	}
	if len(good.texts) != 1 {
		t.Errorf("a failing channel must not block others, got %v", good.texts)
	}
}
