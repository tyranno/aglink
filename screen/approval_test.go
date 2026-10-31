package main

import (
	"strings"
	"testing"
)

func TestScreenGatedHits(t *testing.T) {
	set := map[string]bool{"launch_app": true, "type": true}
	if h := screenGatedHits("launch_app", nil, set); strings.Join(h, ",") != "launch_app" {
		t.Fatalf("direct: %v", h)
	}
	if h := screenGatedHits("screenshot", nil, set); h != nil {
		t.Fatalf("not gated: %v", h)
	}
	steps := `[{"action":"click","x":1,"y":2},{"action":"type","text":"secret"}]`
	if h := screenGatedHits("run_sequence", map[string]any{"steps": steps}, set); strings.Join(h, ",") != "type" {
		t.Fatalf("sequence: %v", h)
	}
}

func TestScreenGateOffWithoutURL(t *testing.T) {
	t.Setenv("AGLINK_APPROVAL_URL", "")
	t.Setenv("AGLINK_APPROVAL_TOOLS", "launch_app")
	if url, set := screenGatedSet(); url != "" || set != nil {
		t.Fatalf("got %q %v", url, set)
	}
}
