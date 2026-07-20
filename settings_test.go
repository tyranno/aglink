package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestGetSettings_CodexModelSelect_RealCLI is an integration test against
// whatever codex CLI is actually installed on the machine running the test
// (not a fake) — it exercises the exact request path a real aglink-desktop
// settings load takes: chatControlServer.handleInbound → buildSettings →
// codexModelOptionsFor → the real codexRunner.modelCatalog() → a real `codex
// debug models` subprocess. Skips (doesn't fail) when codex isn't installed,
// since that's expected on machines/CI without it. This runs against a
// throwaway in-test Manager/Bot, never the live teleclaude process.
func TestGetSettings_CodexModelSelect_RealCLI(t *testing.T) {
	codexPath, err := findCodex("")
	if err != nil || codexPath == "" {
		t.Skip("codex CLI not installed — skipping live model-catalog integration test")
	}
	cfgh := NewConfigHolder(&Config{})
	codex := NewCodexRunner(codexPath, cfgh)
	m := NewManager(nil, codex, NewFileStore(filepath.Join(t.TempDir(), "store.json")), cfgh)
	b := &Bot{manager: m, cfgh: cfgh}
	s := &chatControlServer{bot: b}
	ch := &remoteChatChannel{send: make(chan controlOut, 4), cancel: func() {}}

	s.handleInbound(ch, controlIn{Type: "get_settings", ReqID: "r1"})

	outs := drainControlOut(ch.send)
	if len(outs) != 1 || outs[0].Kind != "reply" {
		t.Fatalf("expected one reply, got %+v", outs)
	}
	var parsed struct {
		Sections []settingSection `json:"sections"`
	}
	if err := json.Unmarshal(outs[0].Data, &parsed); err != nil {
		t.Fatalf("reply not valid JSON: %v", err)
	}
	var field *settingField
	for i := range parsed.Sections {
		for j := range parsed.Sections[i].Fields {
			if parsed.Sections[i].Fields[j].Key == "backend.codex_model" {
				field = &parsed.Sections[i].Fields[j]
			}
		}
	}
	if field == nil {
		t.Fatal("backend.codex_model field missing from get_settings reply")
	}
	if field.Type != "select" {
		t.Fatalf("installed codex-cli supports `debug models` but backend.codex_model type = %q, want select (options=%v)", field.Type, field.Options)
	}
	if len(field.Options) < 2 { // at least the blank default + one real model
		t.Errorf("expected detected codex models in options, got %v", field.Options)
	}
	for _, o := range field.Options {
		if o == "codex-auto-review" {
			t.Errorf("hidden/internal model slug leaked into options: %v", field.Options)
		}
	}
	t.Logf("detected codex models: %v", field.Options)
}

func TestApplySettingsUpdate_WritesValidAndRejectsBad(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &Config{
		TelegramBotToken: "SECRET", AllowedUserIDs: []int64{1}, // required for validate()
		ManagerModel: "haiku", MaxWorkers: 3,
	}

	// Valid update → ok:true, file written, non-edited secret preserved.
	reply := applySettingsUpdate(cfgPath, cfg, []byte(`{"runtime.max_workers":9}`))
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(reply, &r)
	if !r.OK {
		t.Fatalf("valid update should succeed, got %s", reply)
	}
	written, _ := os.ReadFile(cfgPath)
	back, err := unmarshalConfigYAML(written)
	if err != nil {
		t.Fatalf("written config invalid: %v", err)
	}
	if back.MaxWorkers != 9 {
		t.Errorf("max_workers not persisted: %d", back.MaxWorkers)
	}
	if back.TelegramBotToken != "SECRET" {
		t.Errorf("secret not preserved through structured save: %q", back.TelegramBotToken)
	}

	// Invalid JSON → ok:false, no crash.
	bad := applySettingsUpdate(cfgPath, cfg, []byte(`not json`))
	_ = json.Unmarshal(bad, &r)
	if r.OK {
		t.Errorf("invalid body should fail")
	}
}

func TestBuildSettings_HasSectionsAndValues(t *testing.T) {
	cfg := &Config{ManagerModel: "haiku", MaxWorkers: 5, DefaultBackend: "claude", CodexModel: "gpt-5.4"}
	sections := buildSettings(cfg, []string{"gpt-5.5", "gpt-5.4"})
	if len(sections) == 0 {
		t.Fatal("no sections")
	}
	find := func(key string) *settingField {
		for i := range sections {
			for j := range sections[i].Fields {
				if sections[i].Fields[j].Key == key {
					return &sections[i].Fields[j]
				}
			}
		}
		return nil
	}
	if f := find("models.manager"); f == nil || f.Value != "haiku" || f.Desc == "" {
		t.Errorf("models.manager field wrong: %+v", f)
	}
	if f := find("models.manager"); f == nil || f.Type != "select" || len(f.Options) == 0 || f.Options[0] != "" {
		t.Errorf("models.manager should be a select with a blank default option: %+v", f)
	}
	if f := find("runtime.max_workers"); f == nil || f.Value != 5 || f.Type != "int" {
		t.Errorf("runtime.max_workers field wrong: %+v", f)
	}
	if f := find("backend.default"); f == nil || f.Type != "select" || len(f.Options) == 0 {
		t.Errorf("backend.default should be a select with options: %+v", f)
	}
	if f := find("backend.codex_model"); f == nil || f.Type != "select" || f.Value != "gpt-5.4" {
		t.Errorf("backend.codex_model should be a select reflecting detected models: %+v", f)
	}
}

func TestBuildSettings_CodexModelFallsBackToStringWhenUndetected(t *testing.T) {
	cfg := &Config{CodexModel: "some-custom-model"}
	sections := buildSettings(cfg, nil)
	for _, s := range sections {
		for _, f := range s.Fields {
			if f.Key != "backend.codex_model" && f.Key != "backend.codex_manager_model" {
				continue
			}
			if f.Type != "string" {
				t.Errorf("%s should fall back to a free-text field when codex model detection failed, got type %q", f.Key, f.Type)
			}
			if f.Key == "backend.codex_model" && f.Value != "some-custom-model" {
				t.Errorf("existing custom model value should be preserved: %+v", f)
			}
		}
	}
}

func TestApplySettings_WhitelistAndTypes(t *testing.T) {
	cfg := &Config{ManagerModel: "old", MaxWorkers: 3, ManagerAlways: false}
	// JSON scalars: numbers are float64, bools are bool, strings are string.
	err := applySettings(cfg, map[string]any{
		"models.manager":        "haiku",
		"runtime.max_workers":   float64(7),
		"models.manager_always": true,
		"aglink_chat.addr":      "127.0.0.1:1717",
		"telegram.bot_token":    "HACK", // not whitelisted → ignored
		"unknown.key":           "x",    // ignored
	})
	if err != nil {
		t.Fatalf("applySettings: %v", err)
	}
	if cfg.ManagerModel != "haiku" {
		t.Errorf("ManagerModel = %q, want haiku", cfg.ManagerModel)
	}
	if cfg.MaxWorkers != 7 {
		t.Errorf("MaxWorkers = %d, want 7 (float64 coerced to int)", cfg.MaxWorkers)
	}
	if !cfg.ManagerAlways {
		t.Errorf("ManagerAlways = false, want true")
	}
	if cfg.AglinkChatAddr != "127.0.0.1:1717" {
		t.Errorf("AglinkChatAddr = %q", cfg.AglinkChatAddr)
	}
	if cfg.TelegramBotToken == "HACK" {
		t.Errorf("non-whitelisted key was applied — bot_token got clobbered")
	}
}

func TestApplyThenBuild_RoundTrip(t *testing.T) {
	cfg := &Config{}
	_ = applySettings(cfg, map[string]any{"runtime.timeout_minutes": float64(42), "backend.default": "CODEX"})
	if cfg.TimeoutMinutes != 42 {
		t.Errorf("timeout not applied: %d", cfg.TimeoutMinutes)
	}
	if cfg.DefaultBackend != "codex" {
		t.Errorf("backend.default should lowercase to codex, got %q", cfg.DefaultBackend)
	}
	// The rebuilt schema reflects the applied value.
	for _, s := range buildSettings(cfg, nil) {
		for _, f := range s.Fields {
			if f.Key == "runtime.timeout_minutes" && f.Value != 42 {
				t.Errorf("buildSettings did not reflect applied value: %+v", f)
			}
		}
	}
}
