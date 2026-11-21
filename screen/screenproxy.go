package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// screenBridgeTokenEnv is the per-window secret aglink-bridge (the remote half
// of aglink-vscode) exports next to AGLINK_SCREEN_PORT. The bridge refuses
// requests without it; the relay on the PC adds the PC's own screen token.
const screenBridgeTokenEnv = "AGLINK_BRIDGE_TOKEN"

func screenBridgeToken() string {
	return strings.TrimSpace(os.Getenv(screenBridgeTokenEnv))
}

// screenClientHost returns the host to reach the screen daemon on. In a
// Remote-SSH session SSH_CONNECTION is "<client-ip> <client-port> <server-ip>
// <server-port>", so the first field is the client PC — the machine running the
// VS Code window this worker is driven from. Under the VS Code bridge it is
// this server's loopback, whatever SSH_CONNECTION says. Empty when not in an
// SSH session (local use on the PC that has the screen).
func screenClientHost() string {
	if screenBridgeToken() != "" {
		return "127.0.0.1"
	}
	if f := strings.Fields(os.Getenv("SSH_CONNECTION")); len(f) > 0 {
		return f[0]
	}
	return ""
}

// screenRemoteRouted reports a session whose screen server is reached through a
// loopback port on this server that leads to some PC: the VS Code bridge, or a
// registration pinning SSH_CONNECTION's client to loopback (bridge or SSH
// reverse tunnel). Those ports are per window or per tunnel, and 48220 may be a
// different PC's tunnel, so the port must be named explicitly.
func screenRemoteRouted() bool {
	if screenBridgeToken() != "" {
		return true
	}
	f := strings.Fields(os.Getenv("SSH_CONNECTION"))
	if len(f) == 0 {
		return false
	}
	ip := net.ParseIP(f[0])
	return ip != nil && ip.IsLoopback()
}

// screenRemotePort is the port the client PC's screen server is reached on:
// AGLINK_SCREEN_PORT, else 48220 for a direct connection to the client PC. A
// remote-routed session gets no default.
func screenRemotePort() (string, error) {
	v := strings.TrimSpace(os.Getenv("AGLINK_SCREEN_PORT"))
	if v == "" {
		if screenRemoteRouted() {
			return "", fmt.Errorf("screen proxy: AGLINK_SCREEN_PORT is not set — this session reaches the screen through the VS Code aglink bridge (or an SSH tunnel), which needs this window's port. Start Claude Code from a Remote-SSH window with aglink-bridge 0.2+ (reload the window after it installs), or set AGLINK_SCREEN_PORT explicitly for a tunnel. Not falling back to 48220: on a shared server that port can lead to another PC")
		}
		return "48220", nil
	}
	if p, err := strconv.Atoi(v); err != nil || p <= 0 || p >= 65536 {
		return "", fmt.Errorf("screen proxy: AGLINK_SCREEN_PORT=%q is not a valid port", v)
	}
	return v, nil
}

// screenRemoteToken is the bearer token for the screen server: the bridge's
// per-window token under the VS Code bridge, else the shared secret of the
// client PC's screen server for a direct connection. Cross-platform
// (remoteToken in mcphttp_windows.go is Windows-only).
func screenRemoteToken() string {
	if t := screenBridgeToken(); t != "" {
		return t
	}
	return strings.TrimSpace(os.Getenv("AGLINK_SCREEN_REMOTE_TOKEN"))
}

// runScreenProxy serves the screen MCP tools over stdio by transparently
// forwarding to the screen HTTP server on the client PC. It is how a remote
// worker — where SSH_CONNECTION is set and there is no local Win32 screen —
// drives the screen on the PC it is driven from, with no configuration: it reads
// the client IP from SSH_CONNECTION itself, so no shared reverse-tunnel port and
// no per-session value are involved.
func runScreenProxy(host, token string) error {
	port, err := screenRemotePort()
	if err != nil {
		return err
	}
	base := fmt.Sprintf("http://%s:%s/mcp", host, port)
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
		if screenBridgeToken() != "" {
			return fmt.Errorf("screen proxy: cannot reach the VS Code SSH bridge at %s: %w. Reopen the agent from the current window or a new integrated terminal to refresh its port and token", base, err)
		}
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
