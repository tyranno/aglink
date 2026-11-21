package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Saved screen flows: a run_sequence steps array kept under a name so it can be
// replayed with flow_run instead of re-planning it. One JSON file next to
// presets.json (screen-flows.json — web's flows live under flows/, a different
// format), written atomically like the presets.

// Flow is one saved sequence.
type Flow struct {
	Name  string          `json:"-"`
	Steps json.RawMessage `json:"steps"`
	Saved string          `json:"saved"`
}

var flowNameRE = regexp.MustCompile(`^[\p{L}\p{N}_.-]{1,64}$`)

// validateFlowName rejects names that are empty, too long, or carry path or
// shell punctuation.
func validateFlowName(name string) error {
	if !flowNameRE.MatchString(name) {
		return fmt.Errorf("flow name %q: use letters, digits, - _ . (max 64)", name)
	}
	return nil
}

// normalizeFlowSteps checks that steps is a non-empty JSON array of objects
// each with an "action", and returns it compacted.
func normalizeFlowSteps(steps string) (json.RawMessage, int, error) {
	var arr []map[string]any
	if err := json.Unmarshal([]byte(steps), &arr); err != nil {
		return nil, 0, fmt.Errorf("steps must be a JSON array of step objects: %w", err)
	}
	if len(arr) == 0 {
		return nil, 0, fmt.Errorf("steps is empty")
	}
	for i, st := range arr {
		if a, _ := st["action"].(string); strings.TrimSpace(a) == "" {
			return nil, 0, fmt.Errorf("step %d has no \"action\"", i)
		}
	}
	b, err := json.Marshal(arr)
	if err != nil {
		return nil, 0, err
	}
	return b, len(arr), nil
}

// stableFlowWindows rewrites every step's hex-handle "window" ("0x1234") to a
// title that will still match after the app restarts: the part after the last
// " - " ("제목 없음 - 메모장" → "메모장"), or the whole title. A handle dies with
// its window, so a flow saved with one fails on the next run. Handles whose
// window is gone (empty title) are left as they are. Returns the steps (the
// input unchanged when nothing was rewritten) and "0x.. → title" notes.
func stableFlowWindows(steps string, titleOf func(uintptr) string) (string, []string) {
	var arr []map[string]any
	if err := json.Unmarshal([]byte(steps), &arr); err != nil {
		return steps, nil
	}
	var notes []string
	for _, st := range arr {
		w, _ := st["window"].(string)
		w = strings.TrimSpace(w)
		if !strings.HasPrefix(strings.ToLower(w), "0x") {
			continue
		}
		h, err := strconv.ParseUint(w[2:], 16, 64)
		if err != nil {
			continue
		}
		title := strings.TrimSpace(titleOf(uintptr(h)))
		if i := strings.LastIndex(title, " - "); i >= 0 && strings.TrimSpace(title[i+3:]) != "" {
			title = strings.TrimSpace(title[i+3:])
		}
		if title == "" {
			continue
		}
		st["window"] = title
		notes = append(notes, fmt.Sprintf("%s → %q", w, title))
	}
	if len(notes) == 0 {
		return steps, nil
	}
	b, err := json.Marshal(arr)
	if err != nil {
		return steps, nil
	}
	return string(b), notes
}

// FlowStore is a mutex-protected, JSON-file-backed map of named flows.
type FlowStore struct {
	path   string
	mu     sync.RWMutex
	m      map[string]Flow
	saveMu sync.Mutex
}

func NewFlowStore(path string) *FlowStore {
	return &FlowStore{path: path, m: map[string]Flow{}}
}

// Load reads the file; a missing file is an empty store.
func (s *FlowStore) Load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	m := map[string]Flow{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.m = m
	s.mu.Unlock()
	return nil
}

// save writes the store atomically (temp file + rename), serialized so an
// older snapshot can never be renamed over a newer one.
func (s *FlowStore) save() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if s.path == "" {
		return fmt.Errorf("flows file path unavailable")
	}
	s.mu.RLock()
	data, err := json.MarshalIndent(s.m, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".screen-flows-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

// Set validates and stores (or replaces) a flow, then persists. Returns the
// step count.
func (s *FlowStore) Set(name, steps string) (int, error) {
	name = strings.TrimSpace(name)
	if err := validateFlowName(name); err != nil {
		return 0, err
	}
	raw, n, err := normalizeFlowSteps(steps)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	prev, had := s.m[name]
	s.m[name] = Flow{Steps: raw, Saved: time.Now().Format(time.RFC3339)}
	s.mu.Unlock()
	if err := s.save(); err != nil {
		s.mu.Lock()
		if had {
			s.m[name] = prev
		} else {
			delete(s.m, name)
		}
		s.mu.Unlock()
		return 0, err
	}
	return n, nil
}

// Get returns a flow by name.
func (s *FlowStore) Get(name string) (Flow, error) {
	name = strings.TrimSpace(name)
	if err := validateFlowName(name); err != nil {
		return Flow{}, err
	}
	s.mu.RLock()
	f, ok := s.m[name]
	s.mu.RUnlock()
	if !ok {
		return Flow{}, fmt.Errorf("no flow named %q — flow_list shows the saved ones", name)
	}
	f.Name = name
	return f, nil
}

// List returns all flows sorted by name.
func (s *FlowStore) List() []Flow {
	s.mu.RLock()
	out := make([]Flow, 0, len(s.m))
	for name, f := range s.m {
		f.Name = name
		out = append(out, f)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// StepCount is the number of steps in the flow (0 if unreadable).
func (f Flow) StepCount() int {
	var arr []json.RawMessage
	if json.Unmarshal(f.Steps, &arr) != nil {
		return 0
	}
	return len(arr)
}

// defaultFlowsPath is <dataDir>/screen-flows.json, next to presets.json.
func defaultFlowsPath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "screen-flows.json"), nil
}
