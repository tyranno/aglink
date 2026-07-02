package main

import (
	"slices"
	"strings"
	"testing"
)

// TestWorkerBaseArgs_ScreenControlInjection verifies that the worker arg builder
// injects the screen MCP args only when cfg.ScreenControl is enabled (and a
// resolved aglink-screen binary path is available). This exercises the
// config-gated injection without actually exec'ing claude.
func TestWorkerBaseArgs_ScreenControlInjection(t *testing.T) {
	const screenBin = "C:\\t\\aglink-screen.exe"
	req := RunRequest{Prompt: "hi", SessionID: "11111111-1111-1111-1111-111111111111"}

	// ScreenControl ON → screen MCP args present.
	on := workerBaseArgs(&Config{ScreenControl: true}, req, screenBin, "")
	if !slices.Contains(on, "--mcp-config") {
		t.Errorf("ScreenControl=true: missing --mcp-config in %v", on)
	}
	if !slices.Contains(on, "mcp__screen__*") {
		t.Errorf("ScreenControl=true: missing allowedTools mcp__screen__* in %v", on)
	}
	if !slices.Contains(on, "--append-system-prompt") {
		t.Errorf("ScreenControl=true: missing --append-system-prompt in %v", on)
	}
	// The inline mcp-config must reference the resolved aglink-screen binary.
	joined := strings.Join(on, " ")
	if !strings.Contains(joined, "aglink-screen") {
		t.Errorf("ScreenControl=true: inline config missing aglink-screen path in %v", on)
	}

	// ScreenControl OFF → no screen MCP args.
	off := workerBaseArgs(&Config{ScreenControl: false}, req, screenBin, "")
	if slices.Contains(off, "--mcp-config") {
		t.Errorf("ScreenControl=false: unexpected --mcp-config in %v", off)
	}
	if slices.Contains(off, "mcp__screen__*") {
		t.Errorf("ScreenControl=false: unexpected mcp__screen__* in %v", off)
	}
	if strings.Contains(strings.Join(off, " "), "mcp__screen__") {
		t.Errorf("ScreenControl=false: unexpected mcp__screen__ token in %v", off)
	}

	// Even with ScreenControl on, an empty resolved binary path skips injection
	// (we don't know where the screen MCP server binary is).
	noBin := workerBaseArgs(&Config{ScreenControl: true}, req, "", "")
	if slices.Contains(noBin, "--mcp-config") {
		t.Errorf("empty screenBin: unexpected --mcp-config in %v", noBin)
	}

	// Base args are always present regardless of screen control.
	for _, base := range []string{"-p", "--output-format", "json", "--dangerously-skip-permissions"} {
		if !slices.Contains(off, base) {
			t.Errorf("base arg %q missing in %v", base, off)
		}
	}
}

// TestWorkerBaseArgs_WebControlInjection mirrors the screen-control test for
// aglink-web, and additionally verifies that when both plugins are enabled
// they are merged into a single --mcp-config/--allowedTools pair rather than
// one silently overriding the other.
func TestWorkerBaseArgs_WebControlInjection(t *testing.T) {
	const screenBin = "C:\\t\\aglink-screen.exe"
	const webBin = "C:\\t\\aglink-web.exe"
	req := RunRequest{Prompt: "hi", SessionID: "11111111-1111-1111-1111-111111111111"}

	// WebControl ON (screen off) → web MCP args present, screen absent.
	webOnly := workerBaseArgs(&Config{WebControl: true}, req, "", webBin)
	if !slices.Contains(webOnly, "mcp__web__*") {
		t.Errorf("WebControl=true: missing allowedTools mcp__web__* in %v", webOnly)
	}
	if strings.Contains(strings.Join(webOnly, " "), "mcp__screen__") {
		t.Errorf("WebControl=true, ScreenControl=false: unexpected mcp__screen__ token in %v", webOnly)
	}

	// WebControl OFF → no web MCP args even with a resolved binary.
	off := workerBaseArgs(&Config{WebControl: false}, req, "", webBin)
	if strings.Contains(strings.Join(off, " "), "mcp__web__") {
		t.Errorf("WebControl=false: unexpected mcp__web__ token in %v", off)
	}

	// Both enabled → exactly one --mcp-config/--allowedTools pair covering both
	// server keys (not two competing flags where the last one wins).
	both := workerBaseArgs(&Config{ScreenControl: true, WebControl: true}, req, screenBin, webBin)
	if n := slices.Index(both, "--mcp-config"); n < 0 || slices.Index(both[n+2:], "--mcp-config") >= 0 {
		t.Errorf("both enabled: expected exactly one --mcp-config in %v", both)
	}
	joined := strings.Join(both, " ")
	if !strings.Contains(joined, "aglink-screen") || !strings.Contains(joined, "aglink-web") {
		t.Errorf("both enabled: inline config missing one of the binary paths in %v", both)
	}
	idx := slices.Index(both, "--allowedTools")
	if idx < 0 || idx+1 >= len(both) {
		t.Fatalf("both enabled: --allowedTools has no value: %v", both)
	}
	allowed := both[idx+1]
	if !strings.Contains(allowed, "mcp__screen__*") || !strings.Contains(allowed, "mcp__web__*") {
		t.Errorf("both enabled: --allowedTools missing a plugin: %q", allowed)
	}
}
