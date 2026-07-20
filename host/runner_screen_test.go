package main

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// mcpConfigContent returns the effective --mcp-config document: the arg value is
// a temp-file path (the normal case — see pluginWorkerArgs), so read it; if the
// file doesn't exist (write failed → inline fallback) treat the value as the
// JSON itself.
func mcpConfigContent(t *testing.T, args []string) string {
	t.Helper()
	i := slices.Index(args, "--mcp-config")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("no --mcp-config value in %v", args)
	}
	v := args[i+1]
	if b, err := os.ReadFile(v); err == nil {
		return string(b)
	}
	return v
}

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
	// The mcp-config document (written to a temp file, its path passed to claude)
	// must reference the resolved aglink-screen binary.
	if c := mcpConfigContent(t, on); !strings.Contains(c, "aglink-screen") {
		t.Errorf("ScreenControl=true: mcp-config missing aglink-screen path: %s", c)
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
	if c := mcpConfigContent(t, both); !strings.Contains(c, "aglink-screen") || !strings.Contains(c, "aglink-web") {
		t.Errorf("both enabled: mcp-config missing one of the binary paths: %s", c)
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
