package main

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RunMCPWeb runs the "web" MCP server over stdio (blocking). teleclaude spawns
// this binary's default subcommand and points its worker's --mcp-config here,
// exactly as it does for aglink-screen. Every tool is a thin forwarder: it
// builds params and calls callDaemon, which ensures the persistent daemon is up
// and relays the command to the Chrome extension.
//
// v1 tool set is intentionally minimal (list_tabs / navigate / get_page_text);
// click / screenshot / web_search follow once the scaffold is proven.
func RunMCPWeb() error {
	s := server.NewMCPServer(
		"web",
		"0.1.0",
		server.WithToolCapabilities(true),
	)

	// list_tabs — enumerate open tabs in the user's real Chrome.
	s.AddTool(
		mcp.NewTool("list_tabs",
			mcp.WithDescription("List the open tabs in the user's Chrome browser as 'tabId | [active] title | url' lines. Use a tabId with navigate or get_page_text to target a specific tab."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return toolResult(callDaemon("list_tabs", nil)), nil
		},
	)

	// navigate — point a tab at a URL (new tab if none given).
	s.AddTool(
		mcp.NewTool("navigate",
			mcp.WithDescription("Navigate a Chrome tab to a URL and wait for it to finish loading. If 'tabId' is given, that tab is used; otherwise a new tab is opened. Returns the final tab's id, title, and url."),
			mcp.WithString("url", mcp.Description("The URL to open (include scheme, e.g. https://…)."), mcp.Required()),
			mcp.WithNumber("tabId", mcp.Description("Optional existing tab id to navigate (from list_tabs). Omit to open a new tab.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			url, err := req.RequireString("url")
			if err != nil {
				return mcp.NewToolResultError("missing required argument 'url'"), nil
			}
			params := map[string]any{"url": url}
			if tabID := req.GetInt("tabId", 0); tabID != 0 {
				params["tabId"] = tabID
			}
			return toolResult(callDaemon("navigate", params)), nil
		},
	)

	// get_page_text — extract the visible text of a tab.
	s.AddTool(
		mcp.NewTool("get_page_text",
			mcp.WithDescription("Extract the visible text (document.body.innerText) of a Chrome tab so it can be read/summarized. If 'tabId' is omitted, the active tab of the focused window is used. Long pages are truncated."),
			mcp.WithNumber("tabId", mcp.Description("Optional tab id (from list_tabs). Omit for the active tab.")),
			mcp.WithNumber("maxChars", mcp.Description("Optional cap on returned characters (default 20000).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			params := map[string]any{}
			if tabID := req.GetInt("tabId", 0); tabID != 0 {
				params["tabId"] = tabID
			}
			if maxChars := req.GetInt("maxChars", 0); maxChars != 0 {
				params["maxChars"] = maxChars
			}
			return toolResult(callDaemon("get_page_text", params)), nil
		},
	)

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
