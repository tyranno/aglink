package main

import (
	"strings"
	"testing"
)

func TestScreenRemotePortAndToken(t *testing.T) {
	cases := []struct {
		name, ssh, bridge, port, remoteTok string
		host, wantPort, wantTok, errHas    string
	}{
		{name: "direct ssh defaults to 48220", ssh: "100.64.0.21 5000 100.64.0.1 22", remoteTok: "pc", host: "100.64.0.21", wantPort: "48220", wantTok: "pc"},
		{name: "loopback ssh without port fails", ssh: "127.0.0.1 0 127.0.0.1 22", host: "127.0.0.1", errHas: "not set"},
		{name: "bridge without port fails", ssh: "100.64.0.21 5000 100.64.0.1 22", bridge: "w", host: "127.0.0.1", errHas: "not set"},
		{name: "bridge with port sends bridge token", ssh: "100.64.0.21 5000 100.64.0.1 22", bridge: "w", port: "39477", remoteTok: "pc", host: "127.0.0.1", wantPort: "39477", wantTok: "w"},
		{name: "explicit tunnel port", ssh: "127.0.0.1 0 127.0.0.1 22", port: "48220", host: "127.0.0.1", wantPort: "48220"},
		{name: "bad port", bridge: "w", port: "x", host: "127.0.0.1", errHas: "not a valid port"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("SSH_CONNECTION", c.ssh)
			t.Setenv(screenBridgeTokenEnv, c.bridge)
			t.Setenv("AGLINK_SCREEN_PORT", c.port)
			t.Setenv("AGLINK_SCREEN_REMOTE_TOKEN", c.remoteTok)
			if h := screenClientHost(); h != c.host {
				t.Fatalf("screenClientHost() = %q, want %q", h, c.host)
			}
			p, err := screenRemotePort()
			if c.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), c.errHas) {
					t.Fatalf("err = %v, want containing %q", err, c.errHas)
				}
				return
			}
			if err != nil || p != c.wantPort {
				t.Fatalf("screenRemotePort() = %q, %v; want %q", p, err, c.wantPort)
			}
			if tok := screenRemoteToken(); tok != c.wantTok {
				t.Fatalf("screenRemoteToken() = %q, want %q", tok, c.wantTok)
			}
		})
	}
}
