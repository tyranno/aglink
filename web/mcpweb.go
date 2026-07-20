package main

import (
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RunMCPWeb runs the "web" MCP server over stdio (blocking). teleclaude spawns
// this binary's default subcommand and points its worker's --mcp-config here,
// exactly as it does for aglink-screen. Every tool is a thin forwarder: it
// builds params and calls callDaemon, which ensures the persistent daemon is up
// and relays the command to the Chrome extension.
//
// The tool set is defined once in command.go (the `commands` table) and shared
// with the `cmd` fast-path, so this function just registers each command.
// web_search is still pending — it's a different shape (query-in, results-out
// via a search engine) rather than a direct browser action, so it needs its own
// design pass.
func RunMCPWeb() error {
	s := server.NewMCPServer(
		"web",
		"0.1.0",
		server.WithToolCapabilities(true),
	)

	for _, c := range commands {
		s.AddTool(c.mcpTool(), c.mcpHandler())
	}

	return server.ServeStdio(s)
}

// toolResult converts a daemon CallResult into an MCP tool result, surfacing
// the extension's error text as an MCP error.
func toolResult(r CallResult) *mcp.CallToolResult {
	if r.Error != "" {
		return mcp.NewToolResultError(r.Error)
	}
	return mcp.NewToolResultText(r.Text)
}
