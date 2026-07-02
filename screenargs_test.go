package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestScreenSystemPrompt(t *testing.T) {
	p := screenSystemPrompt()
	for _, kw := range []string{"snapshot", "UIA", "screenshot", "preset"} {
		if !strings.Contains(p, kw) {
			t.Errorf("screenSystemPrompt() missing keyword %q", kw)
		}
	}
}

func TestScreenWorkerArgs(t *testing.T) {
	const screenBin = "C:\\t\\aglink-screen.exe"
	args := screenWorkerArgs(screenBin)

	for _, want := range []string{
		"--strict-mcp-config",
		"--mcp-config",
		"--allowedTools",
		"mcp__screen__*",
		"--append-system-prompt",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("screenWorkerArgs missing %q in %v", want, args)
		}
	}

	// Locate the inline JSON arg (the value after --mcp-config).
	idx := slices.Index(args, "--mcp-config")
	if idx < 0 || idx+1 >= len(args) {
		t.Fatalf("--mcp-config has no value: %v", args)
	}
	inline := args[idx+1]

	if !strings.Contains(inline, "screen") {
		t.Errorf("inline JSON missing server key screen: %s", inline)
	}
	// The exe path appears JSON-escaped (backslashes doubled) inside the inline
	// string, so check via encoding/json to mirror how it was produced.
	escaped, _ := json.Marshal(screenBin)
	// strip surrounding quotes that Marshal adds around the string
	escapedInner := strings.Trim(string(escaped), `"`)
	if !strings.Contains(inline, escapedInner) {
		t.Errorf("inline JSON missing (escaped) binary path %q: %s", escapedInner, inline)
	}

	// Must parse back as valid JSON with the expected shape.
	var parsed struct {
		McpServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(inline), &parsed); err != nil {
		t.Fatalf("inline JSON does not parse: %v\n%s", err, inline)
	}
	srv, ok := parsed.McpServers["screen"]
	if !ok {
		t.Fatalf("parsed JSON has no screen server: %s", inline)
	}
	if srv.Command != screenBin {
		t.Errorf("screen.command = %q, want %q", srv.Command, screenBin)
	}
	if !slices.Contains(srv.Args, "mcp") {
		t.Errorf("screen.args missing mcp: %v", srv.Args)
	}
}

func TestResolveScreenBinaryPath(t *testing.T) {
	// Explicit override wins regardless of selfExe.
	if got := resolveScreenBinaryPath(&Config{ScreenBinaryPath: "C:\\custom\\aglink-screen.exe"}, "C:\\t\\teleclaude.exe"); got != "C:\\custom\\aglink-screen.exe" {
		t.Errorf("override: got %q", got)
	}
	// No override, no selfExe → unresolved.
	if got := resolveScreenBinaryPath(&Config{}, ""); got != "" {
		t.Errorf("no selfExe: got %q, want \"\"", got)
	}
	// No override → aglink-screen next to selfExe, but only when it exists.
	dir := t.TempDir()
	self := filepath.Join(dir, "teleclaude"+exeSuffix)
	agl := filepath.Join(dir, "aglink-screen"+exeSuffix)
	if got := resolveScreenBinaryPath(&Config{}, self); got != "" {
		t.Errorf("default (binary absent): got %q, want \"\"", got)
	}
	if err := os.WriteFile(agl, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveScreenBinaryPath(&Config{}, self); got != agl {
		t.Errorf("default (binary present): got %q, want %q", got, agl)
	}
}
