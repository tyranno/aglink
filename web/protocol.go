package main

// Wire protocol shared by all three hops of aglink-web:
//
//	worker → mcp bridge → daemon → chrome extension → back
//
// Everything is deliberately text-result-based for the v1 scaffold: each
// browser method returns a single human-readable string (formatted tab list,
// page text, "ok: ..." status). That keeps the daemon a near-pure relay and
// lets the MCP bridge pass results straight to NewToolResultText.

// Request is what the daemon pushes to the extension over the WS /ext channel.
type Request struct {
	ID     uint64         `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

// Reply is what the extension sends back for a given request ID.
type Reply struct {
	ID    uint64 `json:"id"`
	OK    bool   `json:"ok"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// CallResult is the JSON shape of the daemon's HTTP POST /call boundary, which
// the MCP bridge and the `cmd` fast-path both consume. It mirrors Reply minus
// the correlation ID (the HTTP request/response pairing replaces it).
type CallResult struct {
	OK    bool   `json:"ok"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// callRequest is the POST /call request body.
type callRequest struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}
