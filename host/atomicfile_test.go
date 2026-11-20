package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failRename swaps renameFile for one that always fails, for the test's life.
func failRename(t *testing.T) {
	t.Helper()
	orig := renameFile
	renameFile = func(string, string) error { return errors.New("simulated rename failure") }
	t.Cleanup(func() { renameFile = orig })
}

// noTempLeft fails if a writeFileAtomic temp file was left beside path.
func noTempLeft(t *testing.T, path string) {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "."+filepath.Base(path)+".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestWriteFileAtomic_ReplacesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Twice: replacing an existing target must work on Windows too.
	for _, want := range []string{"new-1", "new-2"} {
		if err := writeFileAtomic(path, []byte(want)); err != nil {
			t.Fatalf("writeFileAtomic: %v", err)
		}
		if got, _ := os.ReadFile(path); string(got) != want {
			t.Fatalf("content = %q, want %q", got, want)
		}
	}
	noTempLeft(t, path)
}

func TestWriteFileAtomic_RenameFailureKeepsOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	failRename(t)
	if err := writeFileAtomic(path, []byte("replacement")); err == nil {
		t.Fatal("want error when the rename fails")
	}
	if got, _ := os.ReadFile(path); string(got) != "original" {
		t.Fatalf("original clobbered: %q", got)
	}
	noTempLeft(t, path)
}

// The structured settings save: a failed replacement is "not saved" and the
// config on disk is byte-for-byte the old one.
func TestPersistConfig_ReplaceFailureKeepsConfigIntact(t *testing.T) {
	path := writeTestConfig(t, t.TempDir(), "sonnet")
	before, _ := os.ReadFile(path)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	failRename(t)
	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(applySettingsUpdate(path, cfg, []byte(`{"models.worker":"opus"}`)), &got)
	if got.OK || !strings.Contains(got.Error, "simulated rename failure") {
		t.Fatalf("reply = %+v, want not-saved with the rename error", got)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatalf("config changed despite the failed save:\n%s", after)
	}
	noTempLeft(t, path)

	// Same for the raw YAML editor path.
	if reply := setConfigReply(path, cfg, before); bytes.Contains(reply, []byte(`"ok":true`)) {
		t.Fatalf("set_config reply = %s, want ok:false", reply)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("config changed despite the failed raw save")
	}
}

type saveReply struct {
	OK         bool   `json:"ok"`
	Error      string `json:"error"`
	Applied    *bool  `json:"applied"`
	ApplyError string `json:"applyError"`
}

// A save that reaches disk but whose live apply fails must say so instead of
// a plain ok — for both the structured form and the raw editor.
func TestConfigSave_ApplyFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, "sonnet")
	cfg, _ := LoadConfig(path)

	// An applier whose file can't be loaded makes Apply fail (created before
	// the bad file exists so it isn't seeded as the "already applied" bytes).
	badPath := filepath.Join(dir, "broken.yaml")
	SetLiveConfigApplier(NewConfigApplier(badPath, NewConfigHolder(cfg), ReloadHooks{}))
	t.Cleanup(func() { SetLiveConfigApplier(nil) })
	if err := os.WriteFile(badPath, []byte("::: not yaml :::"), 0o600); err != nil {
		t.Fatal(err)
	}

	check := func(name string, raw json.RawMessage) {
		t.Helper()
		var r saveReply
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !r.OK || r.Applied == nil || *r.Applied || r.ApplyError == "" {
			t.Fatalf("%s reply = %s, want ok:true applied:false with applyError", name, raw)
		}
	}
	check("set_settings", applySettingsUpdate(path, cfg, []byte(`{"models.worker":"opus"}`)))
	if got, _ := LoadConfig(path); got.WorkerModel != "opus" {
		t.Fatalf("file not saved: worker = %q", got.WorkerModel)
	}
	raw, _ := marshalConfigYAML(&Config{TelegramBotToken: "t", AllowedUserIDs: []int64{1}, WorkerModel: "haiku"})
	check("set_config", setConfigReply(path, cfg, raw))

	// And with a working applier the reply says applied:true.
	SetLiveConfigApplier(nil)
	var r saveReply
	_ = json.Unmarshal(applySettingsUpdate(path, cfg, []byte(`{"models.worker":"sonnet"}`)), &r)
	if !r.OK || r.Applied == nil || !*r.Applied || r.ApplyError != "" {
		t.Fatalf("reply = %+v, want ok:true applied:true", r)
	}
}
