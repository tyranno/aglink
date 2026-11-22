package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// fakeScreenServer serves one "echo" tool over streamable HTTP on addr.
func fakeScreenServer(t *testing.T, addr string) *http.Server {
	t.Helper()
	s := server.NewMCPServer("fake", "0.0.1", server.WithToolCapabilities(true))
	s.AddTool(mcp.NewTool("echo", mcp.WithString("text")), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("echo:" + req.GetString("text", "")), nil
	})
	hs := &http.Server{Handler: server.NewStreamableHTTPServer(s, server.WithStateLess(true))}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	go hs.Serve(ln)
	t.Cleanup(func() { hs.Close() })
	return hs
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func useTempCache(t *testing.T) {
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("LocalAppData", dir)
	} else {
		t.Setenv("XDG_CACHE_HOME", dir)
		t.Setenv("HOME", dir)
	}
}

func callEcho(t *testing.T, p *screenProxy) (*mcp.CallToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = "echo"
	req.Params.Arguments = map[string]any{"text": "hi"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return p.call(ctx, req)
}

func resultText(r *mcp.CallToolResult) string {
	if r == nil || len(r.Content) == 0 {
		return ""
	}
	if tc, ok := r.Content[0].(mcp.TextContent); ok {
		return tc.Text
	}
	return fmt.Sprint(r.Content[0])
}

// A call made while the PC's server is down waits for it to come up instead
// of failing, and a server restart mid-session is reconnected to.
func TestScreenProxyReconnects(t *testing.T) {
	t.Setenv(screenBridgeTokenEnv, "")
	useTempCache(t)
	addr := freeAddr(t)
	p := newScreenProxy("http://"+addr+"/mcp", "")

	started := make(chan *http.Server, 1)
	go func() {
		time.Sleep(1500 * time.Millisecond)
		started <- fakeScreenServer(t, addr)
	}()
	res, err := callEcho(t, p)
	if err != nil || res.IsError || resultText(res) != "echo:hi" {
		t.Fatalf("first call while the server was starting: res=%q err=%v", resultText(res), err)
	}

	// Restart the server: the call made while it is down must reach the new one.
	(<-started).Close()
	go func() {
		time.Sleep(1500 * time.Millisecond)
		fakeScreenServer(t, addr)
	}()
	res, err = callEcho(t, p)
	if err != nil || resultText(res) != "echo:hi" {
		t.Fatalf("second call: res=%q err=%v", resultText(res), err)
	}
}

// A session that starts while the PC is away gets the cached tool list, and
// without a cache the status placeholder.
func TestScreenProxyStartsWithoutPC(t *testing.T) {
	t.Setenv(screenBridgeTokenEnv, "")
	useTempCache(t)
	addr := freeAddr(t)
	fakeScreenServer(t, addr)
	up := newScreenProxy("http://"+addr+"/mcp", "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := up.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := up.syncTools(ctx, c); err != nil {
		t.Fatal(err)
	}

	down := newScreenProxy("http://"+freeAddr(t)+"/mcp", "")
	if !down.loadCachedTools() {
		t.Fatal("cached tool list not loaded for another route")
	}
	if down.srv.GetTool("echo") == nil {
		t.Fatal("cached tool echo not registered")
	}

	useTempCache(t) // empty cache
	none := newScreenProxy("http://"+freeAddr(t)+"/mcp", "")
	if none.loadCachedTools() {
		t.Fatal("loaded a cache that does not exist")
	}
	none.setPlaceholder(errors.New("down"))
	if none.srv.GetTool(proxyStatusTool) == nil {
		t.Fatal("placeholder tool not registered")
	}
}

func TestScreenProxyRetryable(t *testing.T) {
	cases := map[string]bool{
		"failed to send request: dial tcp 127.0.0.1:1: connect: connection refused":                         true,
		`request failed with status 502: {"error":"aglink-bridge: connection lost"}`:                        true,
		`request failed with status 502: {"error":"aglink relay: screen closed the connection mid-answer"}`: false,
		// After the connection was made the call may have run: never again.
		`failed to send request: Post "http://127.0.0.1:1/mcp": read tcp 127.0.0.1:2->127.0.0.1:1: connection reset by peer`:          false,
		`failed to send request: Post "http://127.0.0.1:1/mcp": context deadline exceeded`:                                            false,
		`request failed with status 502: {"error":"aglink-bridge: the link to the PC dropped mid-answer (the call may have run): x"}`: false,
		"unauthorized (401)":   false,
		"tool not found: echo": false,
	}
	for msg, want := range cases {
		if got := retryable(errors.New(msg)); got != want {
			t.Errorf("retryable(%q) = %v, want %v", msg, got, want)
		}
	}
}
