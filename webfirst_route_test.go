package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// A web send to a web conversation runs in that conv's WorkDir (or home) and
// persists to WebConvs, never touching projects or telegram.
func TestHandleWebTarget_WebConv_UsesWorkDir(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	home := t.TempDir()
	m := NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true, HomeDir: home}))
	c, _ := st.NewWebConv("web")
	wd := t.TempDir()
	c.WorkDir = wd
	_ = st.UpdateWebConv(c)

	f := &fakeSender{}
	m.HandleWebTarget(context.Background(), 1, "hi", Target{Kind: "web", ID: c.ID}, f)

	if fc.lastRun.WorkDir != wd {
		t.Errorf("web conv turn should run in its WorkDir %q, got %q", wd, fc.lastRun.WorkDir)
	}
	got, _ := st.GetWebConv(c.ID)
	if len(got.History) != 1 {
		t.Errorf("web conv should have the turn, got %d", len(got.History))
	}
}

// Telegram with no active project runs in the service home, not an error.
func TestHandleTelegram_NoProject_UsesHome(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	home := t.TempDir()
	m := NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true, HomeDir: home}))

	f := &fakeSender{}
	m.Handle(context.Background(), 1, "그냥 해줘", OriginTelegram, f)

	if fc.runCalls != 1 {
		t.Fatalf("telegram with no project should still run in home, runCalls=%d", fc.runCalls)
	}
	if fc.lastRun.WorkDir != home {
		t.Errorf("telegram no-project turn should run in home %q, got %q", home, fc.lastRun.WorkDir)
	}
}

// A web→telegram target with zero projects registered runs in the service
// home, not the old "no project" error — clicking the pinned telegram entry
// must always work.
func TestHandleWebTarget_TelegramNoProject_UsesHome(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	st := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	_ = st.Load()
	home := t.TempDir()
	m := NewManager(fc, nil, st, NewConfigHolder(&Config{ManagerAlways: true, HomeDir: home}))

	f := &fakeSender{}
	m.HandleWebTarget(context.Background(), 1, "그냥 해줘", Target{Kind: "telegram"}, f)

	if fc.runCalls != 1 {
		t.Fatalf("web->telegram target with no project should still run, runCalls=%d", fc.runCalls)
	}
	if fc.lastRun.WorkDir != home {
		t.Errorf("web->telegram no-project turn should run in home %q, got %q", home, fc.lastRun.WorkDir)
	}
	for _, msg := range f.sent {
		if strings.Contains(msg, "정해지지 않았습니다") {
			t.Errorf("must not send the old no-project error, got %q", msg)
		}
	}
}
