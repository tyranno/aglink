package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// recoverClient fails a --resume turn the way a CLI does when its session store
// lost the conversation, then succeeds on the fresh retry and hands back the new
// session id.
type recoverClient struct {
	calls []RunRequest
	newID string
}

func (c *recoverClient) Route(context.Context, RouteRequest) (RouteDecision, error) {
	return RouteDecision{}, errors.New("not used")
}

func (c *recoverClient) Run(_ context.Context, req RunRequest) (RunResult, error) {
	c.calls = append(c.calls, req)
	if req.Resume {
		return RunResult{}, errors.New("No conversation found with session ID: " + req.SessionID)
	}
	return RunResult{Text: "recovered", SessionID: c.newID}, nil
}

// recoveryFixture mirrors webTgtManager but accepts any ClaudeClient.
func recoveryFixture(t *testing.T, c ClaudeClient) (*Manager, *fileStore) {
	t.Helper()
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := st.Load(); err != nil {
		t.Fatalf("load store: %v", err)
	}
	return NewManager(c, nil, st, NewConfigHolder(&Config{ManagerAlways: true})), st
}

// A turn that recovers from a lost session must persist the session id the fresh
// run handed back. Without it, every later turn resumes the same dead id and
// pays the failed --resume plus a full retry, forever.
func TestRunWorker_PersistsSessionIDAfterRecovery(t *testing.T) {
	fc := &recoverClient{newID: "fresh-session-id"}
	m, st := recoveryFixture(t, fc)

	// Seed the telegram conversation as an established one whose CLI session is gone.
	tc := st.TelegramConversation()
	tc.Started = true
	tc.SessionID = "dead-session-id"
	if err := st.UpdateTelegramConversation(tc); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m.HandleWebTarget(context.Background(), 1, "계속 이어서", TelegramTarget(), &fakeSender{})

	if len(fc.calls) != 2 {
		t.Fatalf("expected a --resume attempt then a fresh retry, got %d call(s): %+v", len(fc.calls), fc.calls)
	}
	if !fc.calls[0].Resume || fc.calls[1].Resume {
		t.Fatalf("call shapes = resume:%v then resume:%v, want true then false", fc.calls[0].Resume, fc.calls[1].Resume)
	}

	got := st.TelegramConversation().SessionID
	if got != "fresh-session-id" {
		t.Errorf("stored session id = %q, want %q — the recovered session was dropped, "+
			"so the next turn would resume the dead one again", got, "fresh-session-id")
	}
}

// The guard exists to stop a *resumed* turn clobbering a good session id with an
// empty one. That must keep holding.
func TestRunWorker_ResumedTurnKeepsSessionID(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}} // returns no SessionID
	m, st := recoveryFixture(t, fc)

	tc := st.TelegramConversation()
	tc.Started = true
	tc.SessionID = "live-session-id"
	if err := st.UpdateTelegramConversation(tc); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m.HandleWebTarget(context.Background(), 1, "평범한 후속 메시지", TelegramTarget(), &fakeSender{})

	if got := st.TelegramConversation().SessionID; got != "live-session-id" {
		t.Errorf("stored session id = %q, want it untouched", got)
	}
}
