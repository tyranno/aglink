package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestClientPortRemoteRoutedNeedsExplicitPort(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	cases := []struct {
		name, ssh, token, port string
		want                   int
		errHas                 string
	}{
		{name: "local uses port file/default", want: defaultPort},
		{name: "direct ssh uses default", ssh: "100.64.0.21 5000 100.64.0.1 22", want: defaultPort},
		{name: "loopback ssh without port fails", ssh: "127.0.0.1 0 127.0.0.1 22", errHas: "not set"},
		{name: "bridge token without port fails", token: "t", errHas: "not set"},
		{name: "bridge token with port", token: "t", port: "39477", want: 39477},
		{name: "loopback ssh with explicit tunnel port", ssh: "127.0.0.1 0 127.0.0.1 22", port: "48219", want: 48219},
		{name: "bad port", token: "t", port: "nope", errHas: "not a valid port"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("SSH_CONNECTION", c.ssh)
			t.Setenv(bridgeTokenEnv, c.token)
			t.Setenv(portEnv, c.port)
			got, err := clientPort()
			if c.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), c.errHas) {
					t.Fatalf("err = %v, want containing %q", err, c.errHas)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("clientPort() = %d, %v; want %d", got, err, c.want)
			}
		})
	}
}

func TestBridgeTokenWinsHostAndHeader(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	t.Setenv("SSH_CONNECTION", "100.64.0.21 5000 100.64.0.1 22")
	t.Setenv(bridgeTokenEnv, "window-secret")
	if h := daemonHost(); h != "127.0.0.1" {
		t.Fatalf("daemonHost() = %q under the bridge, want loopback", h)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/health", nil)
	addTokenHeader(req)
	if got := req.Header.Get("Authorization"); got != "Bearer window-secret" {
		t.Fatalf("Authorization = %q, want the bridge token", got)
	}
}
