package main

import (
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// A call through the VS Code bridge made while nothing answers on the bridge
// port waits for it instead of failing, and a bridge 502 saying the PC side is
// not reachable is retried.
func TestCallDaemonRemoteRetries(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	_, port, _ := net.SplitHostPort(addr)
	t.Setenv(bridgeTokenEnv, "tok")
	t.Setenv(portEnv, port)

	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/call", func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(`{"error":"aglink-bridge: Canceled"}`))
			return
		}
		w.Write([]byte(`{"ok":true,"text":"done"}`))
	})
	go func() {
		time.Sleep(1200 * time.Millisecond)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			t.Error(err)
			return
		}
		srv := &http.Server{Handler: mux}
		t.Cleanup(func() { srv.Close() })
		srv.Serve(l)
	}()

	out := callDaemon("noop", nil, "x")
	if !out.OK || out.Text != "done" {
		t.Fatalf("callDaemon = %+v, want ok/done", out)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("/call hit %d times, want 2 (502 retried once)", n)
	}
}

func TestRelayNotReached(t *testing.T) {
	cases := map[string]bool{
		"aglink-bridge: Canceled": true,
		"aglink relay: web (127.0.0.1:48219) is not reachable on this PC: connect ECONNREFUSED": true,
		"aglink relay: web is not running on this PC and could not be started: x":               true,
		"aglink relay: web (127.0.0.1:48219) closed the connection mid-answer":                  false,
		"no such tab": false,
	}
	for msg, want := range cases {
		if got := relayNotReached(msg); got != want {
			t.Errorf("relayNotReached(%q) = %v, want %v", msg, got, want)
		}
	}
}

// A 4xx from /health (wrong or old token) is reported at once, not retried
// for the whole window; a missing port is the same.
func TestPermanentSetupError(t *testing.T) {
	if !permanentSetupError(permanentError{errors.New("aglink-web: /health answered 401")}) {
		t.Error("4xx probe should be permanent")
	}
	if !permanentSetupError(errors.New("AGLINK_WEB_PORT is not set")) {
		t.Error("missing port should be permanent")
	}
	if permanentSetupError(errors.New("aglink-web: nothing answers at http://127.0.0.1:1")) {
		t.Error("nothing answering should be retried")
	}
}
