package main

// This file assembles the worker guidance and binary resolution for the web
// MCP server — the standalone "aglink-web" binary (see
// https://github.com/tyranno/aglink-web), a sibling of aglink-screen that lets
// a worker drive the user's real Chrome browser (list_tabs/navigate/
// get_page_text) via a local WS daemon + MV3 extension, instead of reading the
// page through a screenshot. See mcpargs.go for how this is merged with
// aglink-screen into the claude CLI's single --mcp-config/--allowedTools.

// webSystemPrompt returns the worker guidance for the web MCP tools: prefer
// them over screen_control for reading/navigating web pages, since they read
// the DOM directly instead of a screenshot.
func webSystemPrompt() string {
	return "" +
		"You can also drive the user's real Chrome browser via the `web` MCP tools (list_tabs, navigate, get_page_text). " +
		"Prefer these over screen control for reading or navigating web pages — they read the page's actual text/DOM " +
		"directly instead of a screenshot, so they are cheaper and exact. Use list_tabs to find a tab, navigate to open " +
		"or move a tab to a URL, and get_page_text to read its content. Fall back to screen control only for things the " +
		"web tools can't do (e.g. clicking, visual layout)."
}

// resolveWebBinaryPath locates the aglink-web executable that provides the web
// MCP server, mirroring resolveScreenBinaryPath. See resolveAglinkBinary for
// the shared lookup order. Returns "" when unresolved — the worker then simply
// runs without web tools.
func resolveWebBinaryPath(cfg *Config, selfExe string) string {
	var configured string
	if cfg != nil {
		configured = cfg.WebBinaryPath
	}
	return resolveAglinkBinary("aglink-web", configured, selfExe)
}
