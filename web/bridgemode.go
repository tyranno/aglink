package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// bridgeTokenEnv is the per-window secret aglink-bridge (the remote half of
// aglink-vscode) exports next to AGLINK_WEB_PORT. The bridge refuses requests
// without it, so it is sent in place of the PC's aglink-web token, which never
// leaves the PC (the relay adds that one).
const bridgeTokenEnv = "AGLINK_BRIDGE_TOKEN"

func bridgeToken() string {
	return strings.TrimSpace(os.Getenv(bridgeTokenEnv))
}

// remoteRouted reports a session whose daemon is reached through a loopback
// port on this server that leads to some PC: the VS Code bridge (its token is
// set), or a registration that pins SSH_CONNECTION's client to loopback for the
// bridge or an SSH reverse tunnel. Such ports are per window or per tunnel, and
// the defaults (48219, the port file) may belong to a different PC's tunnel, so
// the port must be named explicitly.
func remoteRouted() bool {
	return bridgeToken() != "" || sshClientIsLoopback(os.Getenv("SSH_CONNECTION"))
}

func sshClientIsLoopback(sshConnection string) bool {
	f := strings.Fields(sshConnection)
	if len(f) == 0 {
		return false
	}
	ip := net.ParseIP(f[0])
	return ip != nil && ip.IsLoopback()
}

// clientPort is the port callers dial. Locally (and for a direct connection to
// the client PC) it is the running daemon's port, as before. In a remote-routed
// session it is AGLINK_WEB_PORT and nothing else.
func clientPort() (int, error) {
	if !remoteRouted() {
		return readPort(), nil
	}
	v := strings.TrimSpace(os.Getenv(portEnv))
	if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
		return p, nil
	}
	if v == "" {
		return 0, fmt.Errorf("aglink-web: %s is not set — this session reaches the browser through the VS Code aglink bridge (or an SSH tunnel), which needs this window's port. Start Claude Code from a Remote-SSH window with aglink-bridge 0.2+ (reload the window after it installs), or set %s explicitly for a tunnel. Not falling back to %d: on a shared server that port can lead to another PC", portEnv, portEnv, defaultPort)
	}
	return 0, fmt.Errorf("aglink-web: %s=%q is not a valid port", portEnv, v)
}
