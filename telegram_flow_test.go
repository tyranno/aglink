package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Route error + exactly one project + no active conversation: just run there
// instead of asking the user to pick (one-chat feel).
func TestManager_RouteError_SingleProject_AutoRuns(t *testing.T) {
	fc := &fakeClaude{routeErr: errors.New("boom"), runRes: RunResult{Text: "ok"}}
	m, _, _ := mgrFixture(t, fc) // mgrFixture registers exactly one project
	f := &fakeSender{}
	m.Handle(context.Background(), 1, "뭔가 해줘", "", f)

	if fc.runCalls != 1 {
		t.Errorf("single project should auto-run on route error, got %d run calls", fc.runCalls)
	}
	for _, msg := range f.sent {
		if strings.Contains(msg, "!project list") || strings.Contains(msg, "!chat use") {
			t.Errorf("single project must not ask the user to pick, got: %q", msg)
		}
	}
}

// Resuming an existing (already-started) conversation must NOT send the
// "📂 project · 💬 conversation (이어가기)" routing header — it should feel like
// one continuous chat.
func TestManager_Resume_NoRoutingHeader(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "이어서 처리"}}
	m, st, _ := mgrFixture(t, fc)
	c, _ := st.NewConversation("myapp", "진행 중 대화", "")
	c.Started = true
	_ = st.UpdateConversation("myapp", c)
	fc.decision = RouteDecision{Action: ActionResume, Project: "myapp", ConversationID: c.ID}

	f := &fakeSender{}
	m.Handle(context.Background(), 1, "계속하자", "", f)

	for _, msg := range f.sent {
		if strings.Contains(msg, "📂") || strings.Contains(msg, "이어가기") {
			t.Errorf("resume must not send a routing header, got: %q", msg)
		}
	}
}

// A genuinely new (non-continuation) conversation still shows the header so the
// user gets a signal that a new topic started.
func TestManager_NewConv_SendsHeader(t *testing.T) {
	fc := &fakeClaude{
		decision: RouteDecision{Action: ActionNew, Project: "myapp", NewTitle: "새 기능"},
		runRes:   RunResult{Text: "완료"},
	}
	m, _, _ := mgrFixture(t, fc)
	f := &fakeSender{}
	m.Handle(context.Background(), 1, "새 기능 만들어줘", "", f)

	found := false
	for _, msg := range f.sent {
		if strings.Contains(msg, "📂") && strings.Contains(msg, "새 대화") {
			found = true
		}
	}
	if !found {
		t.Errorf("new conversation should send a routing header, got: %v", f.sent)
	}
}
