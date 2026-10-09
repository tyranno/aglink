package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
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
func pluginWorkerArgs(cfg *Config, screenBin, webBin, goonoBin string) []string {
	return pluginWorkerArgsOpts(cfg, screenBin, webBin, goonoBin, false)
}

// pluginWorkerArgsOpts is pluginWorkerArgs with the screen guidance optionally
// condensed (screenBrief — see screenSystemPromptBrief / RunRequest.ScreenBrief).
// longWritePrompt: a single huge Write (15–26k tokens) streams nothing for
// minutes and loses everything if the turn fails midway. Writing in sections
// keeps progress visible to the turn watchdog and saved on disk.
const longWritePrompt = "긴 문서나 큰 파일(수백 줄 이상)을 새로 쓸 때는 한 번의 Write로 전부 쓰지 말고, 뼈대를 먼저 저장한 뒤 섹션별로 나눠 Edit로 채우세요. 중간에 실패해도 써둔 부분이 남습니다."

func pluginWorkerArgsOpts(cfg *Config, screenBin, webBin, goonoBin string, screenBrief bool) []string {
	if cfg == nil {
		return nil
	}
	list := buildMCPServerListOpts(cfg, screenBin, webBin, goonoBin, screenBrief)
	if len(list) == 0 {
		return nil
	}
	servers := map[string]mcpServerSpec{}
	var allowed []string
	var prompts []string
	hasScreen, hasWeb := false, false
	for _, d := range list {
		servers[d.Name] = mcpServerSpec{Command: d.Command, Args: d.Args, Env: d.Env}
		allowed = append(allowed, "mcp__"+d.Name+"__*")
		if d.SystemPrompt != "" {
			prompts = append(prompts, d.SystemPrompt)
		}
		switch d.Name {
		case "screen":
			hasScreen = true
		case "web":
			hasWeb = true
		}
	}

	// When BOTH plugins are active the worker has two ways to touch a browser, and
	// the per-plugin prompts each only say "prefer me" — which in the field still
	// let a worker research the web by capturing ~3MB browser-window screenshots
	// dozens of times (huge vision cost, re-sent every --resume turn) instead of
	// reading get_page_text. Lead with one decisive arbitration rule so the browser
	// case is unambiguous. Prepended so it's the first thing the worker reads.
	if hasScreen && hasWeb {
		prompts = append([]string{screenWebArbitrationPrompt()}, prompts...)
	}
	prompts = append(prompts, longWritePrompt)

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
	// deterministic for a given config and the file is named by its content hash,
	// so concurrent workers write/read identical bytes, and conversations whose
	// configs differ (e.g. ScreenBrief) never rewrite each other's file — a
	// persistent worker (runner_persistent.go) compares this file's content to
	// decide reuse, so a shared file would make them restart each other.
	mcpArg := string(inline)
	if dir, derr := dataDir(); derr == nil {
		sum := sha256.Sum256(inline)
		p := filepath.Join(dir, "worker-mcp-"+hex.EncodeToString(sum[:4])+".json")
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
