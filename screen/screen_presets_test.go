package main

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestPresetSetGetList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "presets.json")
	s := NewPresetStore(path)
	if err := s.Load(); err != nil {
		t.Fatalf("Load on missing file should be ok: %v", err)
	}

	if err := s.Set("settings", 100, 200); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set("close", 10, 20); err != nil {
		t.Fatalf("Set: %v", err)
	}

	p, ok := s.Get("settings")
	if !ok {
		t.Fatalf("Get(settings) not found")
	}
	if p.Name != "settings" || p.X != 100 || p.Y != 200 {
		t.Fatalf("Get(settings) = %+v, want {settings 100 200}", p)
	}

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("List() len = %d, want 2", len(list))
	}
}

func TestPresetGetMissing(t *testing.T) {
	dir := t.TempDir()
	s := NewPresetStore(filepath.Join(dir, "presets.json"))
	if _, ok := s.Get("nope"); ok {
		t.Fatalf("Get(missing) should be false")
	}
}

func TestPresetSaveReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "presets.json")

	s := NewPresetStore(path)
	if err := s.Set("a", 1, 2); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set("b", 3, 4); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	fresh := NewPresetStore(path)
	if err := fresh.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	a, ok := fresh.Get("a")
	if !ok || a.X != 1 || a.Y != 2 {
		t.Fatalf("reloaded a = %+v, ok=%v", a, ok)
	}
	b, ok := fresh.Get("b")
	if !ok || b.X != 3 || b.Y != 4 {
		t.Fatalf("reloaded b = %+v, ok=%v", b, ok)
	}
	if len(fresh.List()) != 2 {
		t.Fatalf("reloaded List() len = %d, want 2", len(fresh.List()))
	}
}

// TestPresetConcurrentSetNoLoss guards the save-serialization fix: many
// overlapping Set calls (each of which persists) must never drop a preset by
// letting a stale snapshot win the temp-file rename race. Run with -race.
func TestPresetConcurrentSetNoLoss(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "presets.json")
	s := NewPresetStore(path)
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			if err := s.Set(fmt.Sprintf("p%02d", i), i, i*2); err != nil {
				t.Errorf("Set(p%02d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// The in-memory map must hold every preset...
	if got := len(s.List()); got != n {
		t.Fatalf("in-memory List() len = %d, want %d", got, n)
	}
	// ...and so must the last snapshot that actually reached disk: reload a fresh
	// store from the file and confirm nothing was clobbered by an overlapping save.
	fresh := NewPresetStore(path)
	if err := fresh.Load(); err != nil {
		t.Fatalf("reload Load: %v", err)
	}
	if got := len(fresh.List()); got != n {
		t.Fatalf("reloaded List() len = %d, want %d (a save dropped presets)", got, n)
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("p%02d", i)
		p, ok := fresh.Get(name)
		if !ok || p.X != i || p.Y != i*2 {
			t.Fatalf("reloaded %s = %+v ok=%v, want {%s %d %d}", name, p, ok, name, i, i*2)
		}
	}
}
