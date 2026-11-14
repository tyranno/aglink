package main

import "testing"

// daemonHostFrom turns the session's SSH_CONNECTION into the host the stdio
// bridge should dial: in a Remote-SSH session the first field is the client
// PC's IP (so a remote worker reaches the PC it is driven from with zero
// config); locally SSH_CONNECTION is empty and we use loopback.
func TestDaemonHostFrom(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"remote lan", "192.168.11.2 42249 192.168.11.25 22", "192.168.11.2"},
		{"remote tailscale", "100.64.0.14 55000 100.64.0.1 22", "100.64.0.14"},
		{"local empty", "", "127.0.0.1"},
		{"local blanks", "   ", "127.0.0.1"},
	}
	for _, c := range cases {
		if got := daemonHostFrom(c.in); got != c.want {
			t.Errorf("%s: daemonHostFrom(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestBindAddrFor(t *testing.T) {
	if got := bindAddrFor(48219, false); got != "127.0.0.1:48219" {
		t.Errorf("no token: bindAddrFor(..,false) = %q, want loopback", got)
	}
	if got := bindAddrFor(48219, true); got != "0.0.0.0:48219" {
		t.Errorf("token: bindAddrFor(..,true) = %q, want 0.0.0.0", got)
	}
}
