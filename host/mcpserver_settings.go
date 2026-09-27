package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// --- User-defined MCP servers (list CRUD over the control API) --------------
//
// cfg.MCPServers (see MCPServerDef in mcpservers.go) is a list with no length
// limit, but the structured settings form is scalar-only — it can render a
// fixed set of flat key/value rows and nothing else. So this registry gets its
// own pair of control verbs (get_mcp_servers / save_mcp_servers) carrying the
// list as structured JSON, and the UIs render a real add/remove list instead
// of a handful of "slot 1/2/3" text fields.
//
// Saves are whole-list replacements: add, delete, reorder and edit are all just
// "here is the new list". The host validates the list as a unit (names unique,
// none shadowing a built-in, usable as an MCP identifier) and writes config.yaml
// only when every entry is valid, so a rejected save never leaves the registry
// half-applied.

// builtinMCPServerNames are the names buildMCPServerList hands to aglink's own
// plugins. A user-defined server may not reuse one: both backends key their MCP
// wiring by name (claude's --mcp-config object, codex's -c mcp_servers.<name>.*),
// so a collision would silently shadow the built-in.
var builtinMCPServerNames = []string{"screen", "web", "goono", "notion"}

// mcpServersResponse is the get_mcp_servers reply. Reserved travels with the
// list so the UI can warn about a built-in name collision while typing instead
// of only on save.
type mcpServersResponse struct {
	Servers  []MCPServerDef `json:"servers"`
	Reserved []string       `json:"reserved"`
}

// mcpServersRequest is the save_mcp_servers payload. Servers is a pointer so
// that a payload which omits the field (or sends null) is rejected instead of
// read as "the new list is empty": because a save replaces the whole registry,
// that distinction is the difference between a malformed request and silently
// deleting every server the user registered.
type mcpServersRequest struct {
	Servers *[]MCPServerDef `json:"servers"`
}

// buildMCPServersResponse snapshots the user-defined registry for the UI. The
// slice is always non-nil so clients can iterate it without a null check.
func buildMCPServersResponse(cfg *Config) mcpServersResponse {
	servers := []MCPServerDef{}
	if cfg != nil {
		servers = append(servers, cfg.MCPServers...)
	}
	return mcpServersResponse{Servers: servers, Reserved: builtinMCPServerNames}
}

// validMCPServerNameChar reports whether r may appear in an MCP server name.
// The name becomes a JSON object key for claude and a bare TOML key path
// segment for codex (`-c mcp_servers.<name>.command=…`), so anything outside
// this set could break the generated config rather than just look odd.
func validMCPServerNameChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
}

// validMCPEnvKey reports whether k is usable as an environment variable name.
// Rejecting '=' and whitespace keeps a malformed key from corrupting the codex
// `-c mcp_servers.<name>.env.<key>=…` override it is spliced into.
func validMCPEnvKey(k string) bool {
	if k == "" {
		return false
	}
	return !strings.ContainsAny(k, "= \t\r\n")
}

// normalizeMCPServers trims and validates a whole user-defined server list,
// returning the cleaned list to store in cfg.MCPServers. It returns an error —
// naming the offending row so the UI can point at it — rather than dropping bad
// entries, so a typo is reported instead of silently losing a server.
func normalizeMCPServers(in []MCPServerDef) ([]MCPServerDef, error) {
	if len(in) == 0 {
		return nil, nil
	}
	reserved := map[string]bool{}
	for _, n := range builtinMCPServerNames {
		reserved[n] = true
	}
	seen := map[string]int{} // lowercased name → 1-based row that claimed it
	out := make([]MCPServerDef, 0, len(in))
	for i, d := range in {
		row := i + 1
		name := strings.TrimSpace(d.Name)
		if name == "" {
			return nil, fmt.Errorf("%d번째 MCP 서버: 이름을 입력하세요", row)
		}
		for _, r := range name {
			if !validMCPServerNameChar(r) {
				return nil, fmt.Errorf("MCP 서버 이름 %q: 영문·숫자·'-'·'_'만 사용할 수 있습니다", name)
			}
		}
		lower := strings.ToLower(name)
		if reserved[lower] {
			return nil, fmt.Errorf("MCP 서버 이름 %q은(는) aglink 내장 서버 이름이라 사용할 수 없습니다 (%s)", name, strings.Join(builtinMCPServerNames, "/"))
		}
		if prev, dup := seen[lower]; dup {
			return nil, fmt.Errorf("MCP 서버 이름 %q이(가) 중복됩니다 (%d번째와 %d번째)", name, prev, row)
		}
		seen[lower] = row

		cmd := strings.TrimSpace(d.Command)
		if cmd == "" {
			return nil, fmt.Errorf("MCP 서버 %q: 실행 명령을 입력하세요", name)
		}

		// Args arrive as one entry per UI line; blank lines are just formatting.
		var args []string
		for _, a := range d.Args {
			if a = strings.TrimSpace(a); a != "" {
				args = append(args, a)
			}
		}

		var env map[string]string
		if len(d.Env) > 0 {
			keys := make([]string, 0, len(d.Env))
			for k := range d.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys) // deterministic error for multiple bad keys
			for _, k := range keys {
				trimmed := strings.TrimSpace(k)
				if trimmed == "" {
					continue // an empty row the user never filled in
				}
				if !validMCPEnvKey(trimmed) {
					return nil, fmt.Errorf("MCP 서버 %q: 환경변수 이름 %q에 '=' 또는 공백을 쓸 수 없습니다", name, trimmed)
				}
				if env == nil {
					env = map[string]string{}
				}
				env[trimmed] = strings.TrimSpace(d.Env[k])
			}
		}

		out = append(out, MCPServerDef{
			Name:         name,
			Enabled:      d.Enabled,
			Command:      cmd,
			Args:         args,
			Env:          env,
			SystemPrompt: strings.TrimSpace(d.SystemPrompt),
		})
	}
	return out, nil
}

// applyMCPServersUpdate replaces the whole user-defined MCP registry from a
// save_mcp_servers payload ({"servers":[…]}) and persists config.yaml (fsnotify
// hot-reloads it, so the next worker turn picks the new list up via
// buildMCPServerList — no restart). Returns the control-reply JSON:
// {"ok":true} or {"ok":false,"error":…}. Everything else in the config —
// secrets, allowlists, comments-free structured fields — round-trips untouched
// through marshalConfigYAML, same as the structured settings save.
func applyMCPServersUpdate(cfgPath string, cfg *Config, body []byte) json.RawMessage {
	var req mcpServersRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return settingsFail("잘못된 요청: " + err.Error())
	}
	if req.Servers == nil {
		// Clearing the registry is an explicit []; a missing list is a broken
		// request, and answering it by wiping every server would be unrecoverable.
		return settingsFail("잘못된 요청: servers 목록이 없습니다")
	}
	servers, err := normalizeMCPServers(*req.Servers)
	if err != nil {
		return settingsFail(err.Error())
	}
	newCfg := *cfg // shallow copy; only MCPServers is replaced (not mutated in place)
	newCfg.MCPServers = servers
	return persistConfig(cfgPath, &newCfg)
}
