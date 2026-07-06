package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// concurrencyClaude is a ClaudeClient whose Run tracks how many calls are
// in flight simultaneously, to detect whether two turns raced on the same
// telegram conversation instead of being serialized.
type concurrencyClaude struct {
	mu       sync.Mutex
	current  int
	maxSeen  int
	runCalls int
}

func (c *concurrencyClaude) Route(_ context.Context, _ RouteRequest) (RouteDecision, error) {
	return RouteDecision{}, nil
}

func (c *concurrencyClaude) Run(_ context.Context, _ RunRequest) (RunResult, error) {
	c.mu.Lock()
	c.runCalls++
	c.current++
	if c.current > c.maxSeen {
		c.maxSeen = c.current
	}
	c.mu.Unlock()

	time.Sleep(30 * time.Millisecond) // overlap window: exposes a race if unsynchronized

	c.mu.Lock()
	c.current--
	c.mu.Unlock()

	return RunResult{Text: "ok"}, nil
}

// TestHandleTelegram_SerializesTurns proves two concurrent telegram-origin
// turns never run their worker (ClaudeClient.Run) at the same time. Both
// handleTelegram (telegram origin) and HandleWebTarget's telegram branch
// (web→telegram) mutate the single shared telegram Conversation and issue
// --resume against the same CLI session, so they must be mutually exclusive.
// Without the telegramMu guard in handleTelegram, maxSeen would be 2.
func TestHandleTelegram_SerializesTurns(t *testing.T) {
	fc := &concurrencyClaude{}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	dir := t.TempDir()
	_ = st.AddProject("myapp", dir)
	_ = st.SetTelegramActiveProject("myapp")
	m := NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true}))

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			f := &fakeSender{}
			m.handleTelegram(context.Background(), 1, "hi", f)
		}()
	}
	wg.Wait()

	if fc.runCalls != 2 {
		t.Fatalf("expected both turns to run the worker, got %d calls", fc.runCalls)
	}
	if fc.maxSeen != 1 {
		t.Fatalf("expected serialized turns (max concurrency 1), got max concurrency %d", fc.maxSeen)
	}
}
