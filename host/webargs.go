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
		"ALWAYS use these to read or navigate web pages — they return the page's actual text/DOM directly. " +
		"NEVER read a web page by capturing the browser window with screen tools: a page screenshot costs 10–100× the " +
		"tokens of its text, is far less accurate, and (because captured images stay in the session) is re-billed every " +
		"following turn. Use list_tabs to find a tab, navigate to open or move a tab to a URL, and get_page_text to read " +
		"its content. Only fall back to screen control for a genuinely visual check the DOM text cannot answer " +
		"(e.g. how a chart/canvas/image looks) — not to read text."
}

// screenWebArbitrationPrompt is prepended (see pluginWorkerArgs) only when BOTH
// the screen and web plugins are active, so the worker gets one unambiguous rule
// for the case where either could touch a browser. Without it, a worker treats a
// browser like any desktop app and drives it by screenshot+click — seen live as a
// research turn capturing 50 full browser windows (~3MB each, re-sent every
// resume turn) instead of a handful of get_page_text reads.
func screenWebArbitrationPrompt() string {
	return "" +
		"TOOL CHOICE — browser vs desktop: the target of a task decides which toolset to use. " +
		"For anything in a web page/browser (reading content, following links, filling web forms, checking a site), " +
		"use the `web` tools (list_tabs/navigate/get_page_text/get_attribute) and do NOT capture or click the browser " +
		"window with the `screen` tools — a page screenshot costs 10–100× the tokens of its text and is re-billed every " +
		"turn it stays in the session. Use the `screen` tools for native Windows desktop apps, or for a genuinely visual " +
		"check no DOM text can answer. When in doubt about a browser, reach for `web` first."
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
