package main

import (
	"fmt"
	"sort"
	"strings"
)

// --- User-defined MCP servers (scalar-slot editing) ------------------------
//
// The structured settings form is scalar-only, so cfg.MCPServers (see
// MCPServerDef in mcpservers.go) is edited as fixed "slots"
// (mcp_server.1.*, mcp_server.2.*, …) exactly like vLLM's primary/secondary
// servers and the custom-provider slots. Args/Env don't have a native scalar
// UI type, so they round-trip through a comma-separated string
// ("-y, @pkg/name" / "KEY=VALUE, FOO=bar").

// mcpServerSlots is how many user-defined MCP server slots the scalar
// settings form exposes. A small fixed count keeps the form manageable;
// config.yaml's mcp_servers list itself has no such limit (raw editor covers
// anything beyond this).
const mcpServerSlots = 3

// mcpServerAt returns the i-th user-defined MCP server, or a zero
// MCPServerDef when the slot doesn't exist, so the scalar settings UI can
// read a slot without index checks.
func mcpServerAt(cfg *Config, i int) MCPServerDef {
	if cfg == nil || i < 0 || i >= len(cfg.MCPServers) {
		return MCPServerDef{}
	}
	return cfg.MCPServers[i]
}

// setMCPServerField sets one field of the i-th server, growing the slice with
// empty slots as needed (the UI may fill slot 2 before slot 1). Callers
// normalize afterward (normalizeMCPServers) to drop the trailing empties this
// can create.
func setMCPServerField(cfg *Config, i int, field string, v any) {
	for len(cfg.MCPServers) <= i {
		cfg.MCPServers = append(cfg.MCPServers, MCPServerDef{})
	}
	switch field {
	case "enabled":
		cfg.MCPServers[i].Enabled = asBool(v)
	case "name":
		cfg.MCPServers[i].Name = strings.TrimSpace(asString(v))
	case "command":
		cfg.MCPServers[i].Command = strings.TrimSpace(asString(v))
	case "args":
		cfg.MCPServers[i].Args = parseMCPArgsString(asString(v))
	case "env":
		cfg.MCPServers[i].Env = parseMCPEnvString(asString(v))
	case "system_prompt":
		cfg.MCPServers[i].SystemPrompt = asString(v)
	}
}

// mcpServerEmpty reports whether a slot carries no usable definition (an
// all-blank slot the scalar UI created and the user never filled in).
func mcpServerEmpty(d MCPServerDef) bool {
	return !d.Enabled && strings.TrimSpace(d.Name) == "" && strings.TrimSpace(d.Command) == "" &&
		len(d.Args) == 0 && len(d.Env) == 0 && strings.TrimSpace(d.SystemPrompt) == ""
}

// normalizeMCPServers trims trailing all-blank slots so a cleared slot
// doesn't linger. Only trailing blanks are dropped — a blank slot between two
// filled ones is kept so slot indices stay stable within one save.
func normalizeMCPServers(cfg *Config) {
	for len(cfg.MCPServers) > 0 && mcpServerEmpty(cfg.MCPServers[len(cfg.MCPServers)-1]) {
		cfg.MCPServers = cfg.MCPServers[:len(cfg.MCPServers)-1]
	}
}

// joinMCPArgsString / parseMCPArgsString round-trip an args slice through the
// scalar string field ("-y, @pkg/name" <-> []string{"-y", "@pkg/name"}).
func joinMCPArgsString(args []string) string {
	return strings.Join(args, ", ")
}

func parseMCPArgsString(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// joinMCPEnvString / parseMCPEnvString round-trip an env map through the
// scalar string field ("KEY=VALUE, FOO=bar" <-> map[string]string).
func joinMCPEnvString(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic display order
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+env[k])
	}
	return strings.Join(parts, ", ")
}

func parseMCPEnvString(s string) map[string]string {
	var out map[string]string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		eq := strings.Index(p, "=")
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(p[:eq])
		if k == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = strings.TrimSpace(p[eq+1:])
	}
	return out
}

// mcpServerFields renders the "user-defined MCP servers" slots for the
// structured settings form. Each slot maps 1:1 onto an MCPServerDef; saving a
// slot with a name+command grows cfg.MCPServers, which buildMCPServerList
// then picks up on the worker's next turn — no Go code or rebuild needed.
func mcpServerFields(cfg *Config) []settingField {
	fields := make([]settingField, 0, mcpServerSlots*6)
	for i := 0; i < mcpServerSlots; i++ {
		d := mcpServerAt(cfg, i)
		n := i + 1
		prefix := fmt.Sprintf("mcp_server.%d.", n)
		fields = append(fields,
			settingField{Key: prefix + "enabled", Label: fmt.Sprintf("MCP %d — 사용", n),
				Desc: "이 슬롯을 켜야 실제로 연결됩니다.", Type: "bool", Value: d.Enabled},
			settingField{Key: prefix + "name", Label: fmt.Sprintf("MCP %d — 이름", n),
				Desc: "영문/숫자 식별자. 다른 슬롯이나 screen/web/goono/notion과 겹치면 안 됩니다.", Type: "string", Value: d.Name},
			settingField{Key: prefix + "command", Label: fmt.Sprintf("MCP %d — 실행 명령", n),
				Desc: "예: npx, 또는 MCP 서버 실행파일의 전체 경로.", Type: "string", Value: d.Command},
			settingField{Key: prefix + "args", Label: fmt.Sprintf("MCP %d — 인자", n),
				Desc: "쉼표로 구분해서 순서대로 적습니다. 예: -y, @notionhq/notion-mcp-server", Type: "string", Value: joinMCPArgsString(d.Args)},
			settingField{Key: prefix + "env", Label: fmt.Sprintf("MCP %d — 환경변수(선택)", n),
				Desc: "KEY=VALUE 쌍을 쉼표로 구분. 예: API_KEY=secret, FOO=bar", Type: "string", Value: joinMCPEnvString(d.Env)},
			settingField{Key: prefix + "system_prompt", Label: fmt.Sprintf("MCP %d — 안내 문구(선택)", n),
				Desc: "AI에게 이 MCP를 언제·어떻게 쓸지 알려주는 짧은 문구. 비워도 됩니다.", Type: "string", Value: d.SystemPrompt},
		)
	}
	return fields
}

// applyMCPServerSetting routes a dynamic "mcp_server.<n>.<field>" settings key
// to the matching slot (1-based n in the UI → 0-based slice index). Unknown
// fields are ignored (the raw editor covers anything the form can't).
func applyMCPServerSetting(cfg *Config, key string, v any) {
	rest := strings.TrimPrefix(key, "mcp_server.")
	dot := strings.Index(rest, ".")
	if dot <= 0 {
		return
	}
	n := atoiOr(rest[:dot], 0)
	if n < 1 {
		return
	}
	switch field := rest[dot+1:]; field {
	case "enabled", "name", "command", "args", "env", "system_prompt":
		setMCPServerField(cfg, n-1, field, v)
	}
}
