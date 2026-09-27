package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

// A startup-only field change (interactive_claude here) fires OnNeedRestart with
// the field named, so main.go can schedule a self-restart to apply it.
func TestApplyReload_NeedRestartOnStartupOnlyField(t *testing.T) {
	old := &Config{InteractiveClaude: false}
	nw := &Config{InteractiveClaude: true}
	got := ""
	applyReload(old, nw, ReloadHooks{OnNeedRestart: func(reason string) { got = reason }})
	if got == "" {
		t.Fatal("OnNeedRestart should fire when interactive_claude changes")
	}
	if !strings.Contains(got, "interactive_claude") {
		t.Errorf("reason %q should name interactive_claude", got)
	}
}

// A pure value-field change (worker model here) hot-applies via holder.Get() and
// must NOT trigger a restart.
func TestApplyReload_NoRestartOnHotField(t *testing.T) {
	old := &Config{WorkerModel: "opus", InteractiveClaude: true, TelegramBotToken: "t"}
	nw := &Config{WorkerModel: "haiku", InteractiveClaude: true, TelegramBotToken: "t"}
	applyReload(old, nw, ReloadHooks{OnNeedRestart: func(string) {
		t.Error("OnNeedRestart must not fire for a value-only (hot-applied) field")
	}})
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

// writeTestConfig writes a minimal-but-valid config.yaml with the given worker
// model and returns its path.
func writeTestConfig(t *testing.T, dir, workerModel string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	cfg := &Config{
		TelegramBotToken: "t",
		AllowedUserIDs:   []int64{1},
		WorkerModel:      workerModel,
	}
	raw, err := marshalConfigYAML(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// A file change is picked up synchronously by Apply — this is what lets a
// settings save reply only after the new config is live, so the UI's immediate
// refetch can't render the pre-save values.
func TestConfigApplier_AppliesFileChange(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, "sonnet")
	start, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	holder := NewConfigHolder(start)
	applier := NewConfigApplier(path, holder, ReloadHooks{})

	writeTestConfig(t, dir, "opus")
	if err := applier.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := holder.Get().WorkerModel; got != "opus" {
		t.Fatalf("worker model = %q, want %q (config change not applied)", got, "opus")
	}
}

// Applying the same file twice must not re-fire hooks: a save applies
// synchronously and the watcher's debounced event for that same write arrives
// afterwards, which would otherwise notify every user a second time.
func TestConfigApplier_UnchangedFileIsNoop(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, "sonnet")
	start, _ := LoadConfig(path)
	holder := NewConfigHolder(start)
	notes := make(chan string, 4)
	applier := NewConfigApplier(path, holder, ReloadHooks{Notify: func(m string) { notes <- m }})

	writeTestConfig(t, dir, "opus")
	if err := applier.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	select {
	case <-notes:
	case <-time.After(2 * time.Second):
		t.Fatal("first Apply should notify about the reload")
	}
	if err := applier.Apply(); err != nil { // same bytes — the watcher echo
		t.Fatalf("second Apply: %v", err)
	}
	select {
	case m := <-notes:
		t.Fatalf("unchanged file must not notify again, got %q", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// A broken config keeps the running one instead of dropping the process to a
// half-parsed state.
func TestConfigApplier_InvalidFileKeepsOldConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, "sonnet")
	start, _ := LoadConfig(path)
	holder := NewConfigHolder(start)
	applier := NewConfigApplier(path, holder, ReloadHooks{})

	if err := os.WriteFile(path, []byte("::: not yaml :::"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := applier.Apply(); err == nil {
		t.Fatal("Apply should fail on an unparseable config")
	}
	if got := holder.Get().WorkerModel; got != "sonnet" {
		t.Fatalf("worker model = %q, want the previous %q", got, "sonnet")
	}
}

// Regression: saving the settings form used to reply before the config was
// live, so the UI's immediate get_settings refetch rendered the *old* model
// (pick opus, form comes back showing sonnet). persistConfig must hot-apply
// before replying.
func TestApplySettingsUpdate_ConfigIsLiveWhenReplySent(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, "sonnet")
	start, _ := LoadConfig(path)
	holder := NewConfigHolder(start)
	SetLiveConfigApplier(NewConfigApplier(path, holder, ReloadHooks{}))
	t.Cleanup(func() { SetLiveConfigApplier(nil) })

	reply := applySettingsUpdate(path, holder.Get(), []byte(`{"models.worker":"opus"}`))
	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(reply, &got); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	if !got.OK {
		t.Fatalf("save failed: %s", got.Error)
	}
	if m := holder.Get().WorkerModel; m != "opus" {
		t.Fatalf("live worker model = %q right after the save reply, want %q", m, "opus")
	}
	// And the settings the UI refetches must show the saved value.
	for _, sec := range buildSettings(holder.Get(), nil) {
		for _, f := range sec.Fields {
			if f.Key == "models.worker" && f.Value != "opus" {
				t.Fatalf("get_settings would render models.worker=%v, want opus", f.Value)
			}
		}
	}
}
