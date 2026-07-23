package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// mcpServerSpec is one entry under mcpServers in an inline --mcp-config.
type mcpServerSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

// mcpConfig is the inline --mcp-config document shape.
type mcpConfig struct {
	McpServers map[string]mcpServerSpec `json:"mcpServers"`
}

// pluginWorkerArgs builds the claude CLI args that load whichever aglink-*
// plugin MCP servers are enabled and resolved (screenBin/webBin, each ""
// when its plugin is off or unresolved). The claude CLI accepts only one
// --mcp-config/--allowedTools/--append-system-prompt, so this merges every
// active plugin into one of each rather than the caller appending them
// separately (which would silently make the last one win). Returns nil when
// no plugin is active.
func pluginWorkerArgs(cfg *Config, screenBin, webBin string) []string {
	if cfg == nil {
		return nil
	}
	servers := map[string]mcpServerSpec{}
	var allowed []string
	var prompts []string

	if cfg.ScreenControl && screenBin != "" {
		spec := mcpServerSpec{Command: screenBin, Args: []string{"mcp"}}
		// Pass the configurable full-screenshot cap to the screen MCP process (it
		// reads AGLINK_SCREENSHOT_MAX_EDGE at startup). Scoped to this server's env,
		// not the whole worker environment. 0 = leave the screen binary's built-in
		// default (1280).
		if cfg.ScreenMaxScreenshotLongEdge > 0 {
			spec.Env = map[string]string{"AGLINK_SCREENSHOT_MAX_EDGE": strconv.Itoa(cfg.ScreenMaxScreenshotLongEdge)}
		}
		servers["screen"] = spec
		allowed = append(allowed, "mcp__screen__*")
		prompts = append(prompts, screenSystemPrompt())
	}
	if cfg.WebControl && webBin != "" {
		servers["web"] = mcpServerSpec{Command: webBin, Args: []string{"mcp"}}
		allowed = append(allowed, "mcp__web__*")
		prompts = append(prompts, webSystemPrompt())
	}
	if len(servers) == 0 {
		return nil
	}

	inline, err := json.Marshal(mcpConfig{McpServers: servers})
	if err != nil {
		// servers is a fixed, marshalable shape; this can't realistically fail.
		inline = []byte(`{"mcpServers":{}}`)
	}

	// Pass --mcp-config as a FILE PATH, not inline JSON. On Windows claude is a
	// .cmd shim (e.g. C:\Program Files\nodejs\claude.cmd); Go runs a .cmd via
	// cmd.exe, and an inline JSON arg — full of quotes and backslashes — gets
	// mangled by cmd.exe's command-line parsing. The worker then dies with a
	// spurious 'C:\Program' "not a recognized command" error instead of loading
	// the plugin. A temp-file path (no spaces, no quotes) survives cmd.exe intact,
	// and claude accepts `--mcp-config <file>` on every platform. The content is
	// deterministic for a given config, so one stable file is safe even with
	// concurrent workers (they all write/read identical bytes).
	mcpArg := string(inline)
	if dir, derr := dataDir(); derr == nil {
		p := filepath.Join(dir, "worker-mcp.json")
		if werr := os.WriteFile(p, inline, 0o600); werr == nil {
			mcpArg = p
		}
	}

	return []string{
		"--strict-mcp-config",
		"--mcp-config", mcpArg,
		"--allowedTools", strings.Join(allowed, ","),
		"--append-system-prompt", strings.Join(prompts, "\n\n"),
	}
}
