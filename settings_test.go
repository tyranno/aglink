package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
	cfg := &Config{ManagerModel: "haiku", MaxWorkers: 5, DefaultBackend: "claude"}
	sections := buildSettings(cfg)
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
	if f := find("runtime.max_workers"); f == nil || f.Value != 5 || f.Type != "int" {
		t.Errorf("runtime.max_workers field wrong: %+v", f)
	}
	if f := find("backend.default"); f == nil || f.Type != "select" || len(f.Options) == 0 {
		t.Errorf("backend.default should be a select with options: %+v", f)
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
	for _, s := range buildSettings(cfg) {
		for _, f := range s.Fields {
			if f.Key == "runtime.timeout_minutes" && f.Value != 42 {
				t.Errorf("buildSettings did not reflect applied value: %+v", f)
			}
		}
	}
}
