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
	return server.ServeStdio(newMCPServer(callDaemon))
}

// newMCPServer builds the "web" MCP server, registering every entry of the
// `commands` table as a tool that routes through dispatch. Two transports share
// it: the stdio bridge (dispatch = callDaemon) and the daemon's own /mcp
// endpoint (dispatch = the daemon's router), so a remote client over /mcp sees
// exactly the same tool set as a locally spawned bridge.
func newMCPServer(dispatch dispatchFunc) *server.MCPServer {
	s := server.NewMCPServer(
		"web",
		"0.1.0",
		server.WithToolCapabilities(true),
	)

	for _, c := range commands {
		s.AddTool(c.mcpTool(), c.mcpHandler(dispatch))
	}

	return s
}

// toolResult converts a daemon CallResult into an MCP tool result, surfacing
// the extension's error text as an MCP error.
func toolResult(r CallResult) *mcp.CallToolResult {
	if r.Error != "" {
		return mcp.NewToolResultError(r.Error)
	}
	return mcp.NewToolResultText(r.Text)
}
