package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// screenClientHost returns the host to reach the screen daemon on. In a
// Remote-SSH session SSH_CONNECTION is "<client-ip> <client-port> <server-ip>
// <server-port>", so the first field is the client PC — the machine running the
// VS Code window this worker is driven from. Empty when not in an SSH session
// (local use on the PC that has the screen).
func screenClientHost() string {
	if f := strings.Fields(os.Getenv("SSH_CONNECTION")); len(f) > 0 {
		return f[0]
	}
	return ""
}

// screenRemotePort is the port the client PC's screen server listens on.
func screenRemotePort() string {
	if p := strings.TrimSpace(os.Getenv("AGLINK_SCREEN_PORT")); p != "" {
		return p
	}
	return "48220"
}

// screenRemoteToken is the shared secret for the client PC's screen server.
// Cross-platform (remoteToken in mcphttp_windows.go is Windows-only).
func screenRemoteToken() string {
	return strings.TrimSpace(os.Getenv("AGLINK_SCREEN_REMOTE_TOKEN"))
}

// runScreenProxy serves the screen MCP tools over stdio by transparently
// forwarding to the screen HTTP server on the client PC. It is how a remote
// worker — where SSH_CONNECTION is set and there is no local Win32 screen —
// drives the screen on the PC it is driven from, with no configuration: it reads
// the client IP from SSH_CONNECTION itself, so no shared reverse-tunnel port and
// no per-session value are involved.
func runScreenProxy(host, token string) error {
	base := fmt.Sprintf("http://%s:%s/mcp", host, screenRemotePort())
	ctx := context.Background()

	var opts []transport.StreamableHTTPCOption
	if token != "" {
		opts = append(opts, transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
	}
	c, err := client.NewStreamableHttpClient(base, opts...)
	if err != nil {
		return fmt.Errorf("screen proxy: new client for %s: %w", base, err)
	}
	if err := c.Start(ctx); err != nil {
		return fmt.Errorf("screen proxy: start: %w", err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "aglink-screen-proxy", Version: "0.1.0"}
	ictx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := c.Initialize(ictx, initReq); err != nil {
		return fmt.Errorf("screen proxy: cannot reach the screen server at %s (is aglink-screen serve running on the client, token matching?): %w", base, err)
	}

	tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return fmt.Errorf("screen proxy: list tools: %w", err)
	}

	s := server.NewMCPServer("screen", "0.1.0", server.WithToolCapabilities(true))
	for _, t := range tools.Tools {
		tool := t
		s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			req.Params.Name = tool.Name
			return c.CallTool(ctx, req)
		})
	}
	return server.ServeStdio(s)
}
