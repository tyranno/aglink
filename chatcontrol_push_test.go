package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// A browser that stops reading must not wedge the worker that is talking to it.
// push drops the peer instead of blocking: the Hub fans out from a worker
// goroutine holding no lock, so a blocking push would stall that turn forever.
func TestRemoteChatChannel_PushNeverBlocksAndDropsSlowPeer(t *testing.T) {
	var cancelled atomic.Int32
	r := &remoteChatChannel{
		send:   make(chan controlOut, 2), // nobody drains it
		cancel: func() { cancelled.Add(1) },
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			r.Send(TelegramTarget(), 1, "x") //nolint:errcheck // always nil
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("push blocked on a full buffer: a worker turn would hang forever")
	}

	if cancelled.Load() == 0 {
		t.Error("a peer that never drains must be dropped (cancel), not silently backed up")
	}
	// closeOnce: the connection is torn down exactly once no matter how many
	// frames pile up behind it.
	if got := cancelled.Load(); got != 1 {
		t.Errorf("cancel called %d times, want exactly 1", got)
	}
}

// Frames that arrive after the peer was dropped must not panic; the Hub keeps
// fanning out until Unregister runs on the accept goroutine.
func TestRemoteChatChannel_PushAfterCloseIsSafe(t *testing.T) {
	r := &remoteChatChannel{send: make(chan controlOut, 1), cancel: func() {}}
	r.close()
	r.close() // idempotent

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("push after close panicked: %v", rec)
		}
	}()
	for i := 0; i < 5; i++ {
		r.Typing(TelegramTarget(), 1)
		r.Done(TelegramTarget(), 1)
	}
}

// The Hub must survive a dropped peer: a worker fanning out to a dead channel
// keeps delivering to the live ones.
func TestHub_SlowPeerDoesNotStallOtherChannels(t *testing.T) {
	h := NewHub()
	dead := &remoteChatChannel{send: make(chan controlOut, 1), cancel: func() {}}
	live := &recCh{}
	h.Register(7, dead)
	h.Register(7, live)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			_ = h.Send(TelegramTarget(), 7, "hi")
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a peer that stopped reading stalled the whole fan-out")
	}
	if len(live.texts) != 20 {
		t.Errorf("live channel got %d messages, want 20", len(live.texts))
	}
}
