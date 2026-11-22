package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
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
			return "", withBridgeError(fmt.Errorf("screen proxy: AGLINK_SCREEN_PORT is not set — this session reaches the screen through the VS Code aglink bridge (or an SSH tunnel), which needs this window's port. Start Claude Code from a Remote-SSH window with aglink-bridge 0.2+ (reload the window after it installs), or set AGLINK_SCREEN_PORT explicitly for a tunnel. Not falling back to 48220: on a shared server that port can lead to another PC"))
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

// withBridgeError adds the reason aglink-bridge gave for not opening a port
// (AGLINK_BRIDGE_ERROR), which is the real cause behind "port is not set".
func withBridgeError(err error) error {
	if v := strings.TrimSpace(os.Getenv("AGLINK_BRIDGE_ERROR")); v != "" {
		return fmt.Errorf("%w (aglink-bridge could not open its port: %s — reload the VS Code window)", err, v)
	}
	return err
}
