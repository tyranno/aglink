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

// flow_run replays stored steps; a gated action inside must still be caught.
func TestScreenGatedHitsFlowRun(t *testing.T) {
	orig := gatedFlowSteps
	t.Cleanup(func() { gatedFlowSteps = orig })
	gatedFlowSteps = func(name string) string {
		if name == "login" {
			return `[{"action":"click_control","window":"A","text":"OK"},{"action":"type","text":"x"}]`
		}
		return ""
	}
	set := map[string]bool{"type": true}
	if h := screenGatedHits("flow_run", map[string]any{"name": " login "}, set); strings.Join(h, ",") != "type" {
		t.Fatalf("flow_run: %v", h)
	}
	if h := screenGatedHits("flow_run", map[string]any{"name": "other"}, set); h != nil {
		t.Fatalf("unknown flow: %v", h)
	}
}

func TestScreenGateOffWithoutURL(t *testing.T) {
	t.Setenv("AGLINK_APPROVAL_URL", "")
	t.Setenv("AGLINK_APPROVAL_TOOLS", "launch_app")
	if url, set := screenGatedSet(); url != "" || set != nil {
		t.Fatalf("got %q %v", url, set)
	}
}

func TestScreenApprovalTarget(t *testing.T) {
	args := map[string]any{"name": " notepad "}
	if got := screenApprovalTarget("launch_app", args, []string{"launch_app"}); got != "notepad" {
		t.Fatalf("launch_app: %q", got)
	}
	if got := screenApprovalTarget("run_sequence", args, []string{"launch_app"}); got != "" {
		t.Fatalf("batch: %q", got)
	}
	if got := screenApprovalTarget("type", map[string]any{"text": "x"}, []string{"type"}); got != "" {
		t.Fatalf("no target tool: %q", got)
	}
}
