package main

import (
	"slices"
	"strings"
	"testing"
)

// When both the screen and web plugins are active, the worker must be told, up
// front, to read browsers with the web tools rather than by capturing the browser
// window as a screenshot — the failure that ran a research turn as 50 full-window
// captures (~3MB each, re-billed every resume turn). These tests pin that the
// arbitration guidance is present exactly when both plugins are, and absent
// otherwise (nothing to arbitrate).

func appendSystemPrompt(args []string) string {
	i := slices.Index(args, "--append-system-prompt")
	if i < 0 || i+1 >= len(args) {
		return ""
	}
	return args[i+1]
}

func TestArbitrationPresentWhenBothPluginsActive(t *testing.T) {
	cfg := &Config{ScreenControl: true, WebControl: true}
	args := pluginWorkerArgs(cfg, "screen.exe", "web.exe", "")
	sp := appendSystemPrompt(args)
	if sp == "" {
		t.Fatal("expected an --append-system-prompt with both plugins active")
	}
	if !strings.Contains(sp, "TOOL CHOICE") {
		t.Errorf("both plugins active: arbitration rule missing from system prompt:\n%s", sp)
	}
	// Both per-plugin prompts must still be there.
	if !strings.Contains(sp, "web` MCP tools") && !strings.Contains(sp, "web` tools") {
		t.Error("web guidance missing")
	}
	if !strings.Contains(sp, "screen` MCP tools") {
		t.Error("screen guidance missing")
	}
}

func TestNoArbitrationWithOnlyOnePlugin(t *testing.T) {
	// Screen only.
	sp := appendSystemPrompt(pluginWorkerArgs(&Config{ScreenControl: true}, "screen.exe", "", ""))
	if strings.Contains(sp, "TOOL CHOICE") {
		t.Error("screen-only: should not inject browser-vs-desktop arbitration")
	}
	// Web only.
	sp = appendSystemPrompt(pluginWorkerArgs(&Config{WebControl: true}, "", "web.exe", ""))
	if strings.Contains(sp, "TOOL CHOICE") {
		t.Error("web-only: should not inject browser-vs-desktop arbitration")
	}
}

func TestNoPluginArgsWhenAllOff(t *testing.T) {
	if args := pluginWorkerArgs(&Config{}, "", "", ""); args != nil {
		t.Errorf("no plugins active should yield nil args, got %v", args)
	}
}
