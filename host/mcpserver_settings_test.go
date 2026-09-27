package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// decodeMCPReply unwraps the {ok,error} control reply the save verb returns.
func decodeMCPReply(t *testing.T, raw json.RawMessage) (bool, string) {
	t.Helper()
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("reply not JSON: %v (%s)", err, raw)
	}
	return r.OK, r.Error
}

// saveServers runs the save_mcp_servers path against a temp config.yaml and
// returns the reply plus the config as re-read from disk.
func saveServers(t *testing.T, cfg *Config, servers []MCPServerDef) (bool, string, *Config) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	body, err := json.Marshal(map[string]any{"servers": servers})
	if err != nil {
		t.Fatal(err)
	}
	ok, msg := decodeMCPReply(t, applyMCPServersUpdate(cfgPath, cfg, body))
	if !ok {
		return ok, msg, nil
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	saved, err := unmarshalConfigYAML(raw)
	if err != nil {
		t.Fatalf("written config does not parse: %v", err)
	}
	return ok, msg, saved
}

// baseCfg is a minimally valid config (the save path re-validates what it wrote).
func baseCfg() *Config {
	return &Config{TelegramBotToken: "t", AllowedUserIDs: []int64{1}}
}

// TestApplyMCPServersUpdate_ReplacesWholeList proves the list is not capped at
// the old three scalar slots: five servers save and read back in order.
func TestApplyMCPServersUpdate_ReplacesWholeList(t *testing.T) {
	in := []MCPServerDef{
		{Name: "one", Enabled: true, Command: "npx", Args: []string{"-y", "@acme/one"}},
		{Name: "two", Command: "two.exe"},
		{Name: "three", Enabled: true, Command: "npx", Env: map[string]string{"API_KEY": "secret"}},
		{Name: "four", Command: "four.exe", SystemPrompt: "Use four for X."},
		{Name: "five", Enabled: true, Command: "five.exe"},
	}
	ok, msg, saved := saveServers(t, baseCfg(), in)
	if !ok {
		t.Fatalf("save failed: %s", msg)
	}
	if len(saved.MCPServers) != 5 {
		t.Fatalf("expected 5 servers (no slot cap), got %d: %+v", len(saved.MCPServers), saved.MCPServers)
	}
	for i, want := range []string{"one", "two", "three", "four", "five"} {
		if saved.MCPServers[i].Name != want {
			t.Errorf("server %d = %q, want %q (order must be preserved)", i, saved.MCPServers[i].Name, want)
		}
	}
	if d := saved.MCPServers[0]; !d.Enabled || d.Command != "npx" || len(d.Args) != 2 || d.Args[1] != "@acme/one" {
		t.Errorf("first server round-tripped wrong: %+v", d)
	}
	if saved.MCPServers[2].Env["API_KEY"] != "secret" {
		t.Errorf("env lost: %+v", saved.MCPServers[2])
	}
	if saved.MCPServers[3].SystemPrompt != "Use four for X." {
		t.Errorf("system prompt lost: %+v", saved.MCPServers[3])
	}
}

// TestApplyMCPServersUpdate_DeleteAndEmpty covers the delete side of the CRUD:
// saving a shorter list drops the removed rows, and an empty list clears the
// registry entirely (the old slot form could only blank a trailing slot).
func TestApplyMCPServersUpdate_DeleteAndEmpty(t *testing.T) {
	cfg := baseCfg()
	cfg.MCPServers = []MCPServerDef{
		{Name: "keep", Enabled: true, Command: "keep.exe"},
		{Name: "drop", Enabled: true, Command: "drop.exe"},
	}
	ok, msg, saved := saveServers(t, cfg, []MCPServerDef{{Name: "keep", Enabled: true, Command: "keep.exe"}})
	if !ok {
		t.Fatalf("save failed: %s", msg)
	}
	if len(saved.MCPServers) != 1 || saved.MCPServers[0].Name != "keep" {
		t.Fatalf("delete did not take effect: %+v", saved.MCPServers)
	}
	// The live cfg must be untouched — only the file is rewritten (fsnotify reloads).
	if len(cfg.MCPServers) != 2 {
		t.Errorf("live cfg was mutated by a save: %+v", cfg.MCPServers)
	}

	ok, msg, saved = saveServers(t, cfg, []MCPServerDef{})
	if !ok {
		t.Fatalf("clearing the list failed: %s", msg)
	}
	if len(saved.MCPServers) != 0 {
		t.Errorf("expected an empty registry, got %+v", saved.MCPServers)
	}
}

// TestApplyMCPServersUpdate_Rejects covers server-side validation. Every case
// must fail with a message (so the UI can show it) and must NOT write the file.
func TestApplyMCPServersUpdate_Rejects(t *testing.T) {
	cases := []struct {
		name    string
		servers []MCPServerDef
		want    string // substring the error must mention
	}{
		{"blank name", []MCPServerDef{{Command: "x.exe"}}, "이름"},
		{"blank command", []MCPServerDef{{Name: "acme"}}, "실행 명령"},
		{"duplicate name", []MCPServerDef{
			{Name: "acme", Command: "a.exe"}, {Name: "ACME", Command: "b.exe"},
		}, "중복"},
		{"builtin name screen", []MCPServerDef{{Name: "screen", Command: "x.exe"}}, "내장"},
		{"builtin name notion", []MCPServerDef{{Name: "Notion", Command: "x.exe"}}, "내장"},
		{"bad name chars", []MCPServerDef{{Name: "my server", Command: "x.exe"}}, "영문"},
		{"bad env key", []MCPServerDef{{Name: "acme", Command: "x.exe", Env: map[string]string{"A B": "1"}}}, "환경변수"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := filepath.Join(t.TempDir(), "config.yaml")
			body, _ := json.Marshal(map[string]any{"servers": tc.servers})
			ok, msg := decodeMCPReply(t, applyMCPServersUpdate(cfgPath, baseCfg(), body))
			if ok {
				t.Fatalf("expected rejection for %s", tc.name)
			}
			if !strings.Contains(msg, tc.want) {
				t.Errorf("error %q should mention %q", msg, tc.want)
			}
			if _, err := os.Stat(cfgPath); err == nil {
				t.Errorf("a rejected save must not write config.yaml")
			}
		})
	}
}

// TestApplyMCPServersUpdate_BadBody guards the malformed-payload path.
func TestApplyMCPServersUpdate_BadBody(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	ok, msg := decodeMCPReply(t, applyMCPServersUpdate(cfgPath, baseCfg(), []byte("not json")))
	if ok || msg == "" {
		t.Fatalf("invalid body should fail with a message, got ok=%v msg=%q", ok, msg)
	}
}

// TestApplyMCPServersUpdate_MissingListIsRejected pins the guard that keeps a
// whole-list replace from becoming accidental data loss: a payload with no
// servers list (missing key, or null) must be refused, not treated as "delete
// everything". Only an explicit [] clears the registry.
func TestApplyMCPServersUpdate_MissingListIsRejected(t *testing.T) {
	for _, body := range []string{`{}`, `{"servers":null}`, `{"reserved":["screen"]}`} {
		cfg := baseCfg()
		cfg.MCPServers = []MCPServerDef{{Name: "keep", Enabled: true, Command: "keep.exe"}}
		cfgPath := filepath.Join(t.TempDir(), "config.yaml")
		ok, msg := decodeMCPReply(t, applyMCPServersUpdate(cfgPath, cfg, []byte(body)))
		if ok {
			t.Errorf("%s: a payload without a servers list must be rejected", body)
		}
		if !strings.Contains(msg, "servers") {
			t.Errorf("%s: error %q should name the missing field", body, msg)
		}
		if _, err := os.Stat(cfgPath); err == nil {
			t.Errorf("%s: rejected save must not write config.yaml", body)
		}
	}
}

// TestApplyMCPServersUpdate_PreservesOtherConfig pins that a registry save is
// surgical: secrets and unrelated settings survive the config rewrite.
func TestApplyMCPServersUpdate_PreservesOtherConfig(t *testing.T) {
	cfg := baseCfg()
	cfg.TelegramBotToken = "SECRET"
	cfg.MaxWorkers = 9
	cfg.ScreenControl = true
	_, msg, saved := saveServers(t, cfg, []MCPServerDef{{Name: "acme", Enabled: true, Command: "npx"}})
	if saved == nil {
		t.Fatalf("save failed: %s", msg)
	}
	if saved.TelegramBotToken != "SECRET" || saved.MaxWorkers != 9 || !saved.ScreenControl {
		t.Errorf("unrelated config lost through an MCP save: %+v", saved)
	}
}

// TestNormalizeMCPServers_TrimsAndDropsBlankArgs covers the cleanup the list UI
// relies on: whitespace is trimmed and blank arg lines (the UI's textarea has
// one arg per line) never reach the CLI.
func TestNormalizeMCPServers_TrimsAndDropsBlankArgs(t *testing.T) {
	got, err := normalizeMCPServers([]MCPServerDef{{
		Name: "  acme  ", Enabled: true, Command: "  npx  ",
		Args:         []string{" -y ", "", "   ", "@acme/mcp-server"},
		Env:          map[string]string{" API_KEY ": " secret ", "": "ignored"},
		SystemPrompt: "  Use acme for X.  ",
	}})
	if err != nil {
		t.Fatal(err)
	}
	d := got[0]
	if d.Name != "acme" || d.Command != "npx" || d.SystemPrompt != "Use acme for X." {
		t.Errorf("fields not trimmed: %+v", d)
	}
	if len(d.Args) != 2 || d.Args[0] != "-y" || d.Args[1] != "@acme/mcp-server" {
		t.Errorf("blank arg lines not dropped: %+v", d.Args)
	}
	if len(d.Env) != 1 || d.Env["API_KEY"] != "secret" {
		t.Errorf("env not trimmed / blank key not dropped: %+v", d.Env)
	}
}

// TestBuildMCPServersResponse_ExposesReservedNames proves the UI gets both the
// current list and the built-in names it must refuse, without a null slice.
func TestBuildMCPServersResponse_ExposesReservedNames(t *testing.T) {
	resp := buildMCPServersResponse(&Config{MCPServers: []MCPServerDef{{Name: "acme", Command: "npx"}}})
	if len(resp.Servers) != 1 || resp.Servers[0].Name != "acme" {
		t.Errorf("servers not surfaced: %+v", resp.Servers)
	}
	for _, want := range []string{"screen", "web", "goono", "notion"} {
		found := false
		for _, r := range resp.Reserved {
			if r == want {
				found = true
			}
		}
		if !found {
			t.Errorf("reserved list missing %q: %+v", want, resp.Reserved)
		}
	}
	// A nil config must still marshal as an empty array, not null.
	raw, err := json.Marshal(buildMCPServersResponse(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"servers":[]`) {
		t.Errorf("empty registry should marshal as [], got %s", raw)
	}
}

// TestSavedMCPServersReachTheBackends is the end-to-end point of the feature:
// a UI-saved, enabled entry shows up in the list both backends build their args
// from, and a disabled one doesn't.
func TestSavedMCPServersReachTheBackends(t *testing.T) {
	_, msg, saved := saveServers(t, baseCfg(), []MCPServerDef{
		{Name: "acme", Enabled: true, Command: "npx", Args: []string{"-y", "@acme/mcp-server"}},
		{Name: "off", Enabled: false, Command: "off.exe"},
	})
	if saved == nil {
		t.Fatalf("save failed: %s", msg)
	}
	list := buildMCPServerList(saved, "", "", "")
	if len(list) != 1 || list[0].Name != "acme" {
		t.Fatalf("only enabled servers should be loaded: %+v", list)
	}
	joined := strings.Join(codexMCPServerArgs(saved, "", "", ""), " ")
	if !strings.Contains(joined, "mcp_servers.acme.command=") {
		t.Errorf("codex args missing the saved server: %s", joined)
	}
	if strings.Contains(joined, "mcp_servers.off.") {
		t.Errorf("disabled server leaked into codex args: %s", joined)
	}
}

// TestMCPServers_RoundTrip ensures mcp_servers survives a YAML marshal/
// unmarshal cycle (so a UI-added server persists across restarts).
func TestMCPServers_RoundTrip(t *testing.T) {
	cfg := &Config{
		TelegramBotToken: "t",
		AllowedUserIDs:   []int64{1},
		MCPServers: []MCPServerDef{
			{Name: "acme", Enabled: true, Command: "npx", Args: []string{"-y", "@acme/mcp-server"}, Env: map[string]string{"API_KEY": "secret"}},
		},
	}
	raw, err := marshalConfigYAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := unmarshalConfigYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MCPServers) != 1 || got.MCPServers[0].Name != "acme" ||
		got.MCPServers[0].Command != "npx" || len(got.MCPServers[0].Args) != 2 ||
		got.MCPServers[0].Env["API_KEY"] != "secret" {
		t.Errorf("mcp_servers round-trip lost data: %+v", got.MCPServers)
	}
}

// TestApplySettings_IgnoresLegacyMCPSlotKeys pins that the retired scalar-slot
// keys are no longer a write path into the registry — a stale client (or a
// crafted request) can't grow cfg.MCPServers through set_settings any more.
func TestApplySettings_IgnoresLegacyMCPSlotKeys(t *testing.T) {
	cfg := &Config{}
	if err := applySettings(cfg, map[string]any{
		"mcp_server.1.enabled": true,
		"mcp_server.1.name":    "acme",
		"mcp_server.1.command": "npx",
	}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPServers) != 0 {
		t.Errorf("legacy slot keys should be ignored, got %+v", cfg.MCPServers)
	}
}

// TestBuildSettings_HasNoMCPServerSlots guards the removal: the scalar form must
// not carry mcp_server.* rows any more, or two editors would fight over the
// same list.
func TestBuildSettings_HasNoMCPServerSlots(t *testing.T) {
	for _, s := range buildSettings(&Config{MCPServers: []MCPServerDef{{Name: "acme", Command: "npx"}}}, nil) {
		for _, f := range s.Fields {
			if strings.HasPrefix(f.Key, "mcp_server.") {
				t.Errorf("scalar settings form still exposes %q — the list UI owns this now", f.Key)
			}
		}
	}
}
