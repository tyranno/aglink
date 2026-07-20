package main

import "testing"

func TestApplyReload_RateLimitChanged(t *testing.T) {
	old := &Config{RateLimitPerMin: 20, TelegramBotToken: "t"}
	nw := &Config{RateLimitPerMin: 5, TelegramBotToken: "t"}
	var gotLimit = -999
	applyReload(old, nw, ReloadHooks{OnRateLimit: func(n int) { gotLimit = n }})
	if gotLimit != 5 {
		t.Errorf("OnRateLimit got %d, want 5", gotLimit)
	}
}

func TestApplyReload_TokenChanged(t *testing.T) {
	old := &Config{TelegramBotToken: "A"}
	nw := &Config{TelegramBotToken: "B"}
	called := false
	applyReload(old, nw, ReloadHooks{OnTokenChanged: func() { called = true }})
	if !called {
		t.Error("OnTokenChanged should fire on token change")
	}
}

func TestApplyReload_ScreenControlToggle(t *testing.T) {
	old := &Config{ScreenControl: false}
	nw := &Config{ScreenControl: true}
	var got *bool
	applyReload(old, nw, ReloadHooks{OnScreenControl: func(b bool) { got = &b }})
	if got == nil || *got != true {
		t.Error("OnScreenControl should fire true")
	}
}

func TestApplyReload_NoChange_NoHooks(t *testing.T) {
	c := &Config{TelegramBotToken: "t", RateLimitPerMin: 20}
	applyReload(c, &Config{TelegramBotToken: "t", RateLimitPerMin: 20}, ReloadHooks{
		OnRateLimit:    func(int) { t.Error("rate hook should not fire") },
		OnTokenChanged: func() { t.Error("token hook should not fire") },
	})
}

// Saving "기본 백엔드" through the desktop/web settings form writes
// backend.default to config.txt; the running Manager must switch its active
// backend immediately, not just on the next cold start.
func TestApplyReload_DefaultBackendChanged(t *testing.T) {
	old := &Config{DefaultBackend: "claude"}
	nw := &Config{DefaultBackend: "codex"}
	got := ""
	applyReload(old, nw, ReloadHooks{OnDefaultBackend: func(name string) { got = name }})
	if got != "codex" {
		t.Errorf("OnDefaultBackend got %q, want %q", got, "codex")
	}
}

func TestApplyReload_DefaultBackendUnchanged_NoHook(t *testing.T) {
	old := &Config{DefaultBackend: "claude"}
	nw := &Config{DefaultBackend: "claude"}
	applyReload(old, nw, ReloadHooks{
		OnDefaultBackend: func(string) { t.Error("backend hook should not fire when unchanged") },
	})
}
