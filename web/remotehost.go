package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
)

// daemonHostFrom turns a session's SSH_CONNECTION value into the host the stdio
// bridge should dial. In a Remote-SSH session SSH_CONNECTION is
// "<client-ip> <client-port> <server-ip> <server-port>", so the first field is
// the client PC — the machine running the VS Code window this worker is driven
// from. Dialing that PC's own aglink-web daemon is how a remote worker reaches
// "its own PC" with zero configuration, instead of whichever PC a shared
// reverse-tunnel port happened to land on. Locally SSH_CONNECTION is empty, so
// we fall back to loopback and nothing changes.
func daemonHostFrom(sshConnection string) string {
	if f := strings.Fields(sshConnection); len(f) > 0 {
		return f[0]
	}
	return "127.0.0.1"
}

// daemonHost reads the live SSH_CONNECTION of this process (inherited from the
// Claude worker that spawned this stdio bridge).
func daemonHost() string {
	return daemonHostFrom(os.Getenv("SSH_CONNECTION"))
}

// bindAddrFor chooses the daemon's listen address. Loopback by default (local
// use, unchanged). When an access token is configured the daemon also serves
// the network interfaces (0.0.0.0) so a remote worker on another PC can reach
// this PC's daemon directly — safe because the token gates /call and /mcp. No
// token means no network exposure, so a default install is unchanged.
func bindAddrFor(port int, exposed bool) string {
	if exposed {
		return fmt.Sprintf("0.0.0.0:%d", port)
	}
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// remoteIsLoopback reports whether an http.Request RemoteAddr is a loopback
// peer.
func remoteIsLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// loopbackOnly rejects any non-loopback peer. The extension-connection
// endpoints (/ext, /vscode) are for local extensions only; once a token lets
// the daemon bind the network interfaces, these must stay loopback so a
// networked peer cannot impersonate the browser or a VS Code window. The drive
// endpoints (/call, /mcp) carry the token instead and are reachable remotely.
func loopbackOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !remoteIsLoopback(r.RemoteAddr) {
			http.Error(w, `{"error":"aglink-web: this endpoint is loopback-only"}`, http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}
