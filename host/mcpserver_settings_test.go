package main

import "testing"

// TestApplyMCPServerSetting_SlotEditAndNormalize covers the scalar-slot apply
// path: filling slot 1 defines a server (with comma-separated args/env), a
// trailing all-blank slot 2 is trimmed by normalizeMCPServers.
func TestApplyMCPServerSetting_SlotEditAndNormalize(t *testing.T) {
	cfg := &Config{}
	err := applySettings(cfg, map[string]any{
		"mcp_server.1.enabled":       true,
		"mcp_server.1.name":          "acme",
		"mcp_server.1.command":       "npx",
		"mcp_server.1.args":          "-y, @acme/mcp-server",
		"mcp_server.1.env":           "API_KEY=secret, FOO=bar",
		"mcp_server.1.system_prompt": "Use acme for X.",
		"mcp_server.2.name":          "", // touched-but-blank slot 2
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("expected trailing blank slot trimmed, got %+v", cfg.MCPServers)
	}
	d := cfg.MCPServers[0]
	if !d.Enabled || d.Name != "acme" || d.Command != "npx" {
		t.Errorf("slot 1 scalar fields not applied correctly: %+v", d)
	}
	if len(d.Args) != 2 || d.Args[0] != "-y" || d.Args[1] != "@acme/mcp-server" {
		t.Errorf("args not parsed correctly: %+v", d.Args)
	}
	if d.Env["API_KEY"] != "secret" || d.Env["FOO"] != "bar" {
		t.Errorf("env not parsed correctly: %+v", d.Env)
	}
	if d.SystemPrompt != "Use acme for X." {
		t.Errorf("system_prompt not applied: %q", d.SystemPrompt)
	}
}

// TestMCPServerFields_RoundTripsIntoSettingsForm proves an existing
// cfg.MCPServers entry reads back into the scalar form (args/env re-joined
// into the same comma-separated display format the apply path parses).
func TestMCPServerFields_RoundTripsIntoSettingsForm(t *testing.T) {
	cfg := &Config{MCPServers: []MCPServerDef{
		{Name: "acme", Enabled: true, Command: "npx", Args: []string{"-y", "@acme/mcp-server"}, Env: map[string]string{"API_KEY": "secret"}},
	}}
	fields := mcpServerFields(cfg)
	got := map[string]any{}
	for _, f := range fields {
		got[f.Key] = f.Value
	}
	if got["mcp_server.1.name"] != "acme" || got["mcp_server.1.command"] != "npx" {
		t.Errorf("slot 1 scalar fields not surfaced: %+v", got)
	}
	if got["mcp_server.1.args"] != "-y, @acme/mcp-server" {
		t.Errorf("args not joined correctly: %q", got["mcp_server.1.args"])
	}
	if got["mcp_server.1.env"] != "API_KEY=secret" {
		t.Errorf("env not joined correctly: %q", got["mcp_server.1.env"])
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
