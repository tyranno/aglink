package main

import (
	"encoding/json"
	"strings"
)

// mcpServerSpec is one entry under mcpServers in an inline --mcp-config.
type mcpServerSpec struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
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
		servers["screen"] = mcpServerSpec{Command: screenBin, Args: []string{"mcp"}}
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

	return []string{
		"--strict-mcp-config",
		"--mcp-config", string(inline),
		"--allowedTools", strings.Join(allowed, ","),
		"--append-system-prompt", strings.Join(prompts, "\n\n"),
	}
}
