package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// postMCP sends one JSON-RPC request to the daemon's /mcp endpoint and returns
// the decoded reply. The streamable-HTTP transport may answer either as plain
// JSON or as a one-event SSE stream, so accept both rather than pinning the
// test to whichever the library currently picks.
func postMCP(t *testing.T, srvURL string, req map[string]any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequest(http.MethodPost, srvURL+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /mcp status: got %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	payload := strings.TrimSpace(string(raw))
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		payload = ""
		for _, line := range strings.Split(string(raw), "\n") {
			if after, ok := strings.CutPrefix(strings.TrimSpace(line), "data:"); ok {
				payload = strings.TrimSpace(after)
				break
			}
		}
		if payload == "" {
			t.Fatalf("no data event in SSE reply: %q", raw)
		}
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatalf("decode %q: %v", payload, err)
	}
	return out
}

// TestMCPEndpointRoundTrip is the remote path in miniature: a client that never
// spawns this binary speaks MCP straight to the daemon over HTTP and still
// reaches the Chrome extension. That is what an SSH reverse tunnel carries, so
// a break here breaks every remote session.
func TestMCPEndpointRoundTrip(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	conn := dialFakeExtension(t, srv, "a@b.com")
	defer conn.Close()
	go runFakeExtension(conn, true /* replyToPings */)
	waitForProfile(t, d, "a@b.com")

	init := postMCP(t, srv.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "0"},
		},
	})
	if init["error"] != nil {
		t.Fatalf("initialize failed: %v", init["error"])
	}

	// The tool set must be the same table the stdio bridge registers; a remote
	// client silently getting fewer tools would be the subtle failure here.
	list := postMCP(t, srv.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	names := map[string]bool{}
	result, _ := list["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	for _, tool := range tools {
		if m, ok := tool.(map[string]any); ok {
			if n, ok := m["name"].(string); ok {
				names[n] = true
			}
		}
	}
	if len(names) != len(commands) {
		t.Fatalf("tools/list: got %d tools, want %d (the commands table)", len(names), len(commands))
	}
	for _, c := range commands {
		if !names[c.name] {
			t.Fatalf("tools/list is missing %q", c.name)
		}
	}

	// The call must travel all the way out to the extension, not stop at the
	// MCP layer: the fake extension echoes "pong:<method>".
	call := postMCP(t, srv.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params":  map[string]any{"name": "list_tabs", "arguments": map[string]any{}},
	})
	if call["error"] != nil {
		t.Fatalf("tools/call failed: %v", call["error"])
	}
	text, _ := json.Marshal(call["result"])
	if !strings.Contains(string(text), "pong:list_tabs") {
		t.Fatalf("tools/call did not reach the extension: %s", text)
	}
}

// TestMCPEndpointReportsNoExtension checks the honest-failure path: with no
// browser connected, a remote caller gets an error result rather than a hang.
func TestMCPEndpointReportsNoExtension(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()

	call := postMCP(t, srv.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": "list_tabs", "arguments": map[string]any{}},
	})
	text, _ := json.Marshal(call)
	if !strings.Contains(string(text), "not connected") {
		t.Fatalf("expected a 'not connected' result, got: %s", text)
	}
}
