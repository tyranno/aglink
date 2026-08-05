package main

// This file assembles the worker guidance and binary resolution for the goono
// MCP server — the standalone "goono-mcp" binary (see
// https://github.com/tyranno/goono-mcp), which lets a worker search/upload
// documents in the 구노(goono) workspace. See mcpargs.go for how this is
// merged with any other enabled aglink-* plugin into the claude CLI's single
// --mcp-config/--allowedTools.

// goonoSystemPrompt returns the worker guidance for the goono MCP tools.
func goonoSystemPrompt() string {
	return "" +
		"You can search and upload documents in the 구노(goono) workspace via the `goono` MCP tools " +
		"(list_projects, search_files, upload_document). Use list_projects/search_files to find existing " +
		"documents before creating new ones, and upload_document to save a note/file to a project."
}

// resolveGoonoBinaryPath locates the goono-mcp executable that provides the
// goono MCP server, mirroring resolveScreenBinaryPath/resolveWebBinaryPath.
// See resolveAglinkBinary for the shared lookup order. Returns "" when
// unresolved — the worker then simply runs without goono tools.
func resolveGoonoBinaryPath(cfg *Config, selfExe string) string {
	var configured string
	if cfg != nil {
		configured = cfg.GoonoBinaryPath
	}
	return resolveAglinkBinary("goono-mcp", configured, selfExe)
}
