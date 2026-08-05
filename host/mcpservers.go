package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// MCPServerDef is a generic, config-driven MCP server definition. Adding a new
// stdio-based MCP server (an npx package, a prebuilt binary, anything with a
// static command/args/env) is a config.yaml `mcp_servers:` entry — no new
// Config field, no yamlConfig field, no per-server .go file, no rebuild. This
// is what closes the gap with Claude Desktop's "just add a server block"
// model; aglink's OS-control plugins (screen/web/goono) still need Go code
// for binary resolution/elevation, but everything downstream of "here's a
// command to run" flows through this one shape for both the claude and codex
// backends. See buildMCPServerList / pluginWorkerArgs / codexMCPServerArgs.
type MCPServerDef struct {
	Name         string            `yaml:"name" json:"name"`
	Enabled      bool              `yaml:"enabled" json:"enabled"`
	Command      string            `yaml:"command" json:"command"`
	Args         []string          `yaml:"args,omitempty" json:"args,omitempty"`
	Env          map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	SystemPrompt string            `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
}

// buildMCPServerList assembles every MCP server a worker turn should load:
// aglink's built-in plugins (screen/web/goono/notion — pre-resolved by the
// caller, since screen/web/goono need OS-specific binary lookup that lives
// elsewhere) followed by any enabled entries in cfg.MCPServers, in the order
// each was registered. Both the claude (--mcp-config) and codex
// (-c mcp_servers.*) paths build their args from this single list instead of
// each carrying its own per-server merge logic.
func buildMCPServerList(cfg *Config, screenBin, webBin, goonoBin string) []MCPServerDef {
	if cfg == nil {
		return nil
	}
	var out []MCPServerDef

	if cfg.ScreenControl && screenBin != "" {
		d := MCPServerDef{Name: "screen", Command: screenBin, Args: []string{"mcp"}, SystemPrompt: screenSystemPrompt()}
		// Pass the configurable full-screenshot cap to the screen MCP process (it
		// reads AGLINK_SCREENSHOT_MAX_EDGE at startup). 0 = leave its built-in
		// default (1280).
		if cfg.ScreenMaxScreenshotLongEdge > 0 {
			d.Env = map[string]string{"AGLINK_SCREENSHOT_MAX_EDGE": strconv.Itoa(cfg.ScreenMaxScreenshotLongEdge)}
		}
		out = append(out, d)
	}
	if cfg.WebControl && webBin != "" {
		out = append(out, MCPServerDef{Name: "web", Command: webBin, Args: []string{"mcp"}, SystemPrompt: webSystemPrompt()})
	}
	if cfg.GoonoControl && goonoBin != "" {
		out = append(out, MCPServerDef{Name: "goono", Command: goonoBin, Args: []string{}, SystemPrompt: goonoSystemPrompt()})
	}
	if cfg.NotionControl && cfg.NotionToken != "" {
		// Official Notion-maintained MCP server, run via npx rather than a bundled
		// binary — there's no OS-level control surface to implement, so nothing
		// custom to build. npx caches the package after the first fetch.
		out = append(out, MCPServerDef{
			Name:    "notion",
			Command: "npx",
			Args:    []string{"-y", "@notionhq/notion-mcp-server"},
			Env:     map[string]string{"NOTION_TOKEN": cfg.NotionToken},
		})
	}
	for _, d := range cfg.MCPServers {
		name := strings.TrimSpace(d.Name)
		cmd := strings.TrimSpace(d.Command)
		if !d.Enabled || name == "" || cmd == "" {
			continue
		}
		d.Name, d.Command = name, cmd
		if d.Args == nil {
			d.Args = []string{}
		}
		out = append(out, d)
	}
	return out
}

// codexMCPServerArgs is the codex analogue of pluginWorkerArgs: it builds
// `-c mcp_servers.<name>.*` TOML overrides for every server in
// buildMCPServerList, one shared loop instead of a hand-written
// codex<Name>Args function per server. Codex has no inline-JSON flag; values
// are produced with encoding/json so a Windows path's backslashes (and any
// env value) are escaped correctly inside the TOML string/array literal (JSON
// string/array syntax is a valid TOML basic-string/array literal here).
func codexMCPServerArgs(cfg *Config, screenBin, webBin, goonoBin string) []string {
	var args []string
	for _, d := range buildMCPServerList(cfg, screenBin, webBin, goonoBin) {
		cmdVal, err := json.Marshal(d.Command)
		if err != nil {
			continue
		}
		argsVal, err := json.Marshal(d.Args)
		if err != nil {
			continue
		}
		args = append(args,
			"-c", "mcp_servers."+d.Name+".command="+string(cmdVal),
			"-c", "mcp_servers."+d.Name+".args="+string(argsVal),
		)
		if len(d.Env) > 0 {
			keys := make([]string, 0, len(d.Env))
			for k := range d.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys) // deterministic arg order
			for _, k := range keys {
				valVal, err := json.Marshal(d.Env[k])
				if err != nil {
					continue
				}
				args = append(args, "-c", "mcp_servers."+d.Name+".env."+k+"="+string(valVal))
			}
		}
	}
	return args
}
