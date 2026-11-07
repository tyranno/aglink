package main

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Without turn_server.py and its models the mic falls back to the level and
// fixed pauses: VadProbs says "not installed" (the page stops asking) and
// TurnEnd -1 (no opinion).
func TestTurnWithoutModels(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	v := &VoiceService{}
	pcm := base64.StdEncoding.EncodeToString(make([]byte, 2048))
	if _, err := v.VadProbs("rec-1", pcm, true); !errors.Is(err, errNoTurnModel) {
		t.Fatalf("VadProbs without a model: %v, want %v", err, errNoTurnModel)
	}
	if p, err := v.TurnEnd(pcm); p != -1 || err != nil {
		t.Fatalf("TurnEnd without a model = %v, %v; want -1, nil", p, err)
	}
	// The server alone, with no model next to it, is not enough either.
	dir := filepath.Join(os.Getenv("AGLINK_HOME"), "openvoice")
	for _, f := range []string{ovPython(dir), filepath.Join(dir, "turn_server.py")} {
		_ = os.MkdirAll(filepath.Dir(f), 0o700)
		_ = os.WriteFile(f, nil, 0o600)
	}
	if vadInstalled(dir) || smartTurnInstalled(dir) {
		t.Fatal("installed without models")
	}
}
