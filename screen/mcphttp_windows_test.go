//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// postMCP sends one JSON-RPC request to /mcp and returns the decoded reply.
// The streamable-HTTP transport may answer as plain JSON or as a one-event SSE
// stream, so accept both rather than pinning the test to whichever the library
// currently picks.
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

// serveTestMCP starts the same handler stack RunScreenRemote builds, minus the
// listener, so the test exercises the real token gate and the real MCP server
// rather than a stand-in.
func serveTestMCP(t *testing.T, token string, watch *idleWatch) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newRemoteMux(token, watch))
	t.Cleanup(srv.Close)
	return srv
}

// TestRemoteMCPListsEveryScreenTool is the regression guard for splitting the
// tool registration out of RunMCPScreen: if a tool is ever added to the stdio
// path but not to newScreenMCPServer, a remote session silently loses it. The
// test asserts the list is non-empty and contains the tools a caller must have
// to do anything at all — enumerate, capture, and click.
func TestRemoteMCPListsEveryScreenTool(t *testing.T) {
	srv := serveTestMCP(t, "", nil)

	postMCP(t, srv.URL, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "0"},
		},
	})

	reply := postMCP(t, srv.URL, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{},
	})
	result, ok := reply["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list has no result: %v", reply)
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools/list returned no tools: %v", result)
	}

	got := map[string]bool{}
	for _, raw := range tools {
		if m, ok := raw.(map[string]any); ok {
			if name, ok := m["name"].(string); ok {
				got[name] = true
			}
		}
	}
	for _, want := range []string{"list_windows", "screenshot", "click", "capture_window", "return_desktop"} {
		if !got[want] {
			t.Errorf("tool %q missing over HTTP (have %d tools)", want, len(tools))
		}
	}
}

// TestRemoteMCPTokenGate — with a token configured, an unauthenticated request
// must not reach the tools. This endpoint drives the keyboard and mouse, so a
// gate that silently passed everything would be worse than no gate at all.
func TestRemoteMCPTokenGate(t *testing.T) {
	srv := serveTestMCP(t, "s3cret", nil)

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/mcp", body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token request: got %d, want 401", resp.StatusCode)
	}
}

// TestRemoteMCPTokenAccepted — the same request with the right bearer token
// gets through, so the gate is not simply refusing everything.
func TestRemoteMCPTokenAccepted(t *testing.T) {
	srv := serveTestMCP(t, "s3cret", nil)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer s3cret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bearer request: got %d, want 200", resp.StatusCode)
	}
}

// TestIdlePokeDefersReturn — a request must push the idle deadline out. Without
// this, an active session that keeps working past the idle window would be
// yanked back to the user's original virtual desktop mid-task.
func TestIdlePokeDefersReturn(t *testing.T) {
	fired := make(chan struct{}, 1)
	w := &idleWatch{after: 60 * time.Millisecond}
	w.timer = time.AfterFunc(w.after, func() { fired <- struct{}{} })

	// Poke twice across the original deadline; it must not have fired.
	time.Sleep(40 * time.Millisecond)
	w.poke()
	time.Sleep(40 * time.Millisecond)
	w.poke()

	select {
	case <-fired:
		t.Fatal("idle timer fired while requests were still arriving")
	default:
	}

	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("idle timer never fired after the pokes stopped")
	}
}
