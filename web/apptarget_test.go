package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// pageSim answers Runtime.evaluate like a page that starts without the shared
// scripts and gains them once both inject evaluations have run.
type pageSim struct {
	injected   atomic.Int32                                   // number of inject scripts evaluated
	calls      atomic.Int32                                   // number of __aglinkPage calls that actually ran
	onEvaluate func(f *fakeCDP, req cdpMsg, expr string) bool // return true if handled
}

func (s *pageSim) handle(f *fakeCDP, req cdpMsg) {
	switch req.Method {
	case "Page.enable", "Page.handleJavaScriptDialog":
		f.reply(req.ID, map[string]any{})
		if req.Method == "Page.handleJavaScriptDialog" {
			f.send(map[string]any{"method": "Page.javascriptDialogClosed", "params": map[string]any{"result": true}})
		}
	case "Runtime.evaluate":
		var p struct{ Expression string }
		_ = json.Unmarshal(req.Params, &p)
		if s.onEvaluate != nil && s.onEvaluate(f, req, p.Expression) {
			return
		}
		switch {
		case p.Expression == injectJS || p.Expression == pageActionsJS:
			s.injected.Add(1)
			f.reply(req.ID, map[string]any{"result": map[string]any{"type": "undefined"}})
		case strings.Contains(p.Expression, "__aglinkPage"):
			if s.injected.Load() < 2 {
				f.reply(req.ID, map[string]any{"result": map[string]any{"type": "object", "value": map[string]any{"__aglinkNeedInject": true}}})
				return
			}
			s.calls.Add(1)
			f.reply(req.ID, map[string]any{"result": map[string]any{"type": "object", "value": map[string]any{"found": true, "text": "hi"}}})
		default:
			f.reply(req.ID, map[string]any{"result": map[string]any{"type": "number", "value": 2}})
		}
	default:
		f.reply(req.ID, map[string]any{})
	}
}

func openSim(t *testing.T, s *pageSim) (*appTarget, *fakeCDP) {
	t.Helper()
	f := newFakeCDP(t, s.handle)
	a, err := openAppTarget(cdpTarget{ID: "P1", Type: "page", Title: "aglink", WebSocketDebuggerURL: f.url()}, 9333)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a, f
}

func TestAppTargetInjectsOnceThenCallsDirectly(t *testing.T) {
	s := &pageSim{}
	a, _ := openSim(t, s)
	ctx := context.Background()

	res, err := a.CallPage(ctx, "getPageText", []any{nil})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res), `"hi"`) {
		t.Fatalf("result not passed back: %s", res)
	}
	if s.injected.Load() != 2 || s.calls.Load() != 1 {
		t.Fatalf("first call: injected=%d calls=%d, want 2 and 1", s.injected.Load(), s.calls.Load())
	}
	if _, err := a.CallPage(ctx, "getPageText", []any{nil}); err != nil {
		t.Fatal(err)
	}
	if s.injected.Load() != 2 {
		t.Fatalf("an already-injected page must not be injected again (injected=%d)", s.injected.Load())
	}
}

func TestAppTargetDialogWinsTheRace(t *testing.T) {
	s := &pageSim{onEvaluate: func(f *fakeCDP, req cdpMsg, expr string) bool {
		if strings.Contains(expr, "confirm(") {
			// The page is now blocked: no reply, only the dialog event.
			f.send(map[string]any{"method": "Page.javascriptDialogOpening", "params": map[string]any{
				"type": "confirm", "message": "지울까요?", "url": "http://wails.localhost/"}})
			return true
		}
		return false
	}}
	a, _ := openSim(t, s)
	start := time.Now()
	_, err := a.Eval(context.Background(), "confirm('지울까요?')")
	if !errors.Is(err, errDialogOpen) {
		t.Fatalf("want errDialogOpen, got %v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("took %v — a dialog must end the wait at once, not at the timeout", time.Since(start))
	}
	d := a.Dialog()
	if d == nil || d.Type != "confirm" || d.Message != "지울까요?" {
		t.Fatalf("dialog not recorded: %+v", d)
	}
	// While it is open, any further evaluation fails fast too.
	if _, err := a.Eval(context.Background(), "1+1"); !errors.Is(err, errDialogOpen) {
		t.Fatalf("an open dialog must short-circuit the next call, got %v", err)
	}
}

func TestAppTargetHandleDialogClearsIt(t *testing.T) {
	s := &pageSim{}
	a, f := openSim(t, s)
	f.send(map[string]any{"method": "Page.javascriptDialogOpening", "params": map[string]any{"type": "prompt", "message": "이름?", "defaultPrompt": "홍길동"}})
	deadline := time.Now().Add(time.Second)
	for a.Dialog() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if d := a.Dialog(); d == nil || d.DefaultPrompt != "홍길동" {
		t.Fatalf("prompt not recorded: %+v", d)
	}
	if err := a.HandleDialog(context.Background(), true, "김철수"); err != nil {
		t.Fatal(err)
	}
	var sent cdpMsg
	for _, r := range f.requests() {
		if r.Method == "Page.handleJavaScriptDialog" {
			sent = r
		}
	}
	if !strings.Contains(string(sent.Params), `"accept":true`) || !strings.Contains(string(sent.Params), "김철수") {
		t.Fatalf("wrong handleJavaScriptDialog params: %s", sent.Params)
	}
	deadline = time.Now().Add(time.Second)
	for a.Dialog() != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.Dialog() != nil {
		t.Fatal("dialog must clear once the page reports it closed")
	}
	if _, err := a.Eval(context.Background(), "1+1"); err != nil {
		t.Fatalf("calls must work again after the dialog is handled: %v", err)
	}
}

func TestAppTargetExceptionBecomesError(t *testing.T) {
	s := &pageSim{onEvaluate: func(f *fakeCDP, req cdpMsg, expr string) bool {
		f.reply(req.ID, map[string]any{
			"result":           map[string]any{"type": "object", "subtype": "error"},
			"exceptionDetails": map[string]any{"text": "Uncaught", "exception": map[string]any{"description": "ReferenceError: nope is not defined"}},
		})
		return true
	}}
	a, _ := openSim(t, s)
	_, err := a.Eval(context.Background(), "nope")
	if err == nil || !strings.Contains(err.Error(), "ReferenceError: nope is not defined") {
		t.Fatalf("want the page's exception, got %v", err)
	}
}
