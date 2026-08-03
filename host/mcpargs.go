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
	if cfg.NotionControl && cfg.NotionToken != "" {
		// Official Notion-maintained MCP server, run via npx rather than a bundled
		// binary (unlike screen/web there's no OS-level control surface to
		// implement, so there's nothing custom to build). npx caches the package
		// after the first fetch, so steady-state startup is fast.
		servers["notion"] = mcpServerSpec{
			Command: "npx",
			Args:    []string{"-y", "@notionhq/notion-mcp-server"},
			Env:     map[string]string{"NOTION_TOKEN": cfg.NotionToken},
		}
		allowed = append(allowed, "mcp__notion__*")
	}
	if len(servers) == 0 {
		return nil
	}

	// When BOTH plugins are active the worker has two ways to touch a browser, and
	// the per-plugin prompts each only say "prefer me" — which in the field still
	// let a worker research the web by capturing ~3MB browser-window screenshots
	// dozens of times (huge vision cost, re-sent every --resume turn) instead of
	// reading get_page_text. Lead with one decisive arbitration rule so the browser
	// case is unambiguous. Prepended so it's the first thing the worker reads.
	if cfg.ScreenControl && screenBin != "" && cfg.WebControl && webBin != "" {
		prompts = append([]string{screenWebArbitrationPrompt()}, prompts...)
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
