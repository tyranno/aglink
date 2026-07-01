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
	on := workerBaseArgs(&Config{ScreenControl: true}, req, screenBin)
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
	off := workerBaseArgs(&Config{ScreenControl: false}, req, screenBin)
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
	noBin := workerBaseArgs(&Config{ScreenControl: true}, req, "")
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
