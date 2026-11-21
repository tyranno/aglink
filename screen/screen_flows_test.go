package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateFlowName(t *testing.T) {
	for _, ok := range []string{"login", "staging.login", "a_b-c", "설정열기", strings.Repeat("x", 64)} {
		if err := validateFlowName(ok); err != nil {
			t.Errorf("%q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " ", "../x", "a/b", `a\b`, "a b", "x:y", strings.Repeat("x", 65)} {
		if err := validateFlowName(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestFlowStoreSaveListLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "screen-flows.json")
	s := NewFlowStore(path)
	if err := s.Load(); err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	n, err := s.Set("open-settings", `[{"action":"invoke","window":"Settings","name":"Network"},
		{"action":"wait","ms":100}]`)
	if err != nil || n != 2 {
		t.Fatalf("Set = %d, %v", n, err)
	}
	if _, err := s.Set("b", `[{"action":"key","combo":"enter"}]`); err != nil {
		t.Fatal(err)
	}

	r := NewFlowStore(path)
	if err := r.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	list := r.List()
	if len(list) != 2 || list[0].Name != "b" || list[1].Name != "open-settings" {
		t.Fatalf("List = %+v", list)
	}
	f, err := r.Get("open-settings")
	if err != nil {
		t.Fatal(err)
	}
	if f.StepCount() != 2 || !strings.Contains(string(f.Steps), `"Settings"`) || f.Saved == "" {
		t.Errorf("Get = %+v (%s)", f, f.Steps)
	}
	if _, err := r.Get("nope"); err == nil {
		t.Error("Get(missing) should error")
	}
	if _, err := r.Get("../evil"); err == nil {
		t.Error("Get with an invalid name should error")
	}
}

func TestFlowStoreRejectsBadSteps(t *testing.T) {
	s := NewFlowStore(filepath.Join(t.TempDir(), "f.json"))
	for _, steps := range []string{``, `{}`, `[]`, `not json`, `[{"x":1}]`, `[{"action":""}]`} {
		if _, err := s.Set("x", steps); err == nil {
			t.Errorf("steps %q should be rejected", steps)
		}
	}
	if _, err := s.Set("bad name", `[{"action":"wait","ms":1}]`); err == nil {
		t.Error("invalid name should be rejected")
	}
	if len(s.List()) != 0 {
		t.Error("nothing should have been stored")
	}
}

// The write goes through a temp file renamed into place: no temp files are
// left behind and the file is always complete JSON.
func TestFlowStoreAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "screen-flows.json")
	s := NewFlowStore(path)
	for i := 0; i < 5; i++ {
		if _, err := s.Set("f", `[{"action":"wait","ms":1}]`); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "screen-flows.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("dir should hold only the flows file, has %v", names)
	}
	if err := NewFlowStore(path).Load(); err != nil {
		t.Fatalf("written file is not valid JSON: %v", err)
	}
}

// A failed write must not leave the in-memory store claiming the flow exists.
func TestFlowStoreSetRollsBackOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewFlowStore(filepath.Join(blocker, "screen-flows.json")) // parent is a file
	if _, err := s.Set("f", `[{"action":"wait","ms":1}]`); err == nil {
		t.Fatal("expected a write error")
	}
	if len(s.List()) != 0 {
		t.Error("flow should be rolled back after a failed save")
	}
}
