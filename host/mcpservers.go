package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
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
	return buildMCPServerListOpts(cfg, screenBin, webBin, goonoBin, false)
}

// buildMCPServerListOpts is buildMCPServerList with the screen server's system
// prompt optionally replaced by the short pointer (screenSystemPromptBrief).
func buildMCPServerListOpts(cfg *Config, screenBin, webBin, goonoBin string, screenBrief bool) []MCPServerDef {
	if cfg == nil {
		return nil
	}
	var out []MCPServerDef

	if cfg.ScreenControl && screenBin != "" {
		sp := screenSystemPrompt()
		if screenBrief {
			sp = screenSystemPromptBrief()
		}
		d := MCPServerDef{Name: "screen", Command: screenBin, Args: []string{"mcp"}, SystemPrompt: sp}
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
		// Official Notion-maintained MCP server — there's no OS-level control
		// surface to implement, so nothing custom to build. Launched with node
		// straight from a global install when there is one, else via npx (see
		// notionMCPLaunch).
		cmd, args := notionMCPLaunch()
		out = append(out, MCPServerDef{
			Name:    "notion",
			Command: cmd,
			Args:    args,
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

// notionMCPPackage is the npm package of the official Notion MCP server.
const notionMCPPackage = "@notionhq/notion-mcp-server"

var (
	notionLaunchOnce sync.Once
	notionLaunchCmd  string
	notionLaunchArgs []string
)

// notionMCPLaunch returns the command that starts the Notion MCP server,
// resolved once per process. `npx -y <pkg>` on Windows goes npx.cmd → node and
// checks the npm registry on every start — i.e. every worker turn, since each
// turn spawns its own MCP servers. When the package is installed globally
// (`npm i -g @notionhq/notion-mcp-server`), run `node <its bin entry>` directly
// instead; otherwise keep npx. A package installed after aglink started is
// picked up on the next restart.
func notionMCPLaunch() (string, []string) {
	notionLaunchOnce.Do(func() {
		notionLaunchCmd, notionLaunchArgs = "npx", []string{"-y", notionMCPPackage}
		node, err := exec.LookPath("node")
		if err != nil {
			return
		}
		entry, ok := findGlobalPackageBin(npmGlobalRootCandidates(node), notionMCPPackage)
		if !ok {
			// Cheap guesses missed (custom npm prefix): ask npm once.
			if root := npmRootGlobal(); root != "" {
				entry, ok = findGlobalPackageBin([]string{root}, notionMCPPackage)
			}
		}
		if ok {
			notionLaunchCmd, notionLaunchArgs = node, []string{entry}
			log.Printf("[mcp] notion: 전역 설치 사용 (node %s)", entry)
		} else {
			log.Printf("[mcp] notion: 전역 설치 없음 — npx로 실행 (npm i -g %s 권장)", notionMCPPackage)
		}
	})
	return notionLaunchCmd, append([]string(nil), notionLaunchArgs...)
}

// npmGlobalRootCandidates lists likely global node_modules dirs without running
// npm: %APPDATA%\npm\node_modules (default Windows prefix), <node dir>\node_modules
// (Windows prefix = the node install dir) and <node dir>/../lib/node_modules (Unix).
func npmGlobalRootCandidates(nodePath string) []string {
	var dirs []string
	if appData := os.Getenv("APPDATA"); appData != "" {
		dirs = append(dirs, filepath.Join(appData, "npm", "node_modules"))
	}
	if nodePath != "" {
		d := filepath.Dir(nodePath)
		dirs = append(dirs, filepath.Join(d, "node_modules"), filepath.Join(d, "..", "lib", "node_modules"))
	}
	return dirs
}

// npmRootGlobal returns `npm root -g`, or "" if npm is unavailable.
func npmRootGlobal() string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "npm", "root", "-g").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// findGlobalPackageBin looks for pkg under each root and returns the absolute
// path of its executable entry, taken from package.json "bin": a string, or a
// map — the entry named after the package's last path segment, else the only /
// alphabetically first one. ok is false when no root has the package or the
// entry file is missing.
func findGlobalPackageBin(roots []string, pkg string) (string, bool) {
	for _, root := range roots {
		if root == "" {
			continue
		}
		dir := filepath.Join(root, filepath.FromSlash(pkg))
		data, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			continue
		}
		var meta struct {
			Bin json.RawMessage `json:"bin"`
		}
		if json.Unmarshal(data, &meta) != nil || len(meta.Bin) == 0 {
			continue
		}
		rel := ""
		var single string
		var many map[string]string
		if json.Unmarshal(meta.Bin, &single) == nil {
			rel = single
		} else if json.Unmarshal(meta.Bin, &many) == nil && len(many) > 0 {
			rel = many[pkg[strings.LastIndex(pkg, "/")+1:]]
			if rel == "" {
				keys := make([]string, 0, len(many))
				for k := range many {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				rel = many[keys[0]]
			}
		}
		if rel == "" {
			continue
		}
		entry := filepath.Join(dir, filepath.FromSlash(rel))
		if fi, err := os.Stat(entry); err == nil && !fi.IsDir() {
			return entry, true
		}
	}
	return "", false
}
