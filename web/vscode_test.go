package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"net/http/httptest"

	"github.com/gorilla/websocket"
)

// dialFakeVSCode connects like the aglink-vscode extension from one window.
func dialFakeVSCode(t *testing.T, srv *httptest.Server, name, remote, folder, session string) *websocket.Conn {
	t.Helper()
	q := url.Values{"name": {name}, "remote": {remote}, "folder": {folder}, "session": {session}}
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/vscode?" + q.Encode()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("fake vscode dial: %v", err)
	}
	return conn
}

func vscodeDaemon(t *testing.T) (*Daemon, *httptest.Server) {
	t.Helper()
	d := newDaemon("")
	d.discover = func(context.Context) []appInfo { return nil }
	srv := httptest.NewServer(d.handler())
	t.Cleanup(srv.Close)
	return d, srv
}

func vscodeCount(d *Daemon) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.vscodes)
}

func TestVSCodeWindowRegistersAndLists(t *testing.T) {
	d, srv := vscodeDaemon(t)
	c := dialFakeVSCode(t, srv, "backend", "host1", "/home/u/backend", "s1")
	defer c.Close()
	go runFakeExtension(c, true)
	waitFor(t, func() bool { return vscodeCount(d) == 1 })

	res := d.call(listProfilesMethod, nil, "")
	if !strings.Contains(res.Text, "vscode:backend@host1 | SSH host1 | /home/u/backend | connected ") {
		t.Fatalf("window missing from list_profiles:\n%s", res.Text)
	}
}

func TestVSCodeLocalWindowLabel(t *testing.T) {
	d, srv := vscodeDaemon(t)
	c := dialFakeVSCode(t, srv, "aglink", "", `C:\p\aglink`, "s1")
	defer c.Close()
	go runFakeExtension(c, true)
	waitFor(t, func() bool { return vscodeCount(d) == 1 })
	if res := d.call(listProfilesMethod, nil, ""); !strings.Contains(res.Text, `vscode:aglink | local | C:\p\aglink |`) {
		t.Fatalf("local window line:\n%s", res.Text)
	}
}

func TestVSCodeSameFolderTwiceGetsDistinctNames(t *testing.T) {
	d, srv := vscodeDaemon(t)
	a := dialFakeVSCode(t, srv, "backend", "host1", "/p", "s1")
	defer a.Close()
	go runFakeExtension(a, true)
	b := dialFakeVSCode(t, srv, "backend", "host1", "/p", "s2")
	defer b.Close()
	go runFakeExtension(b, true)
	waitFor(t, func() bool { return vscodeCount(d) == 2 })
	res := d.call(listProfilesMethod, nil, "")
	if !strings.Contains(res.Text, "vscode:backend@host1 |") || !strings.Contains(res.Text, "vscode:backend@host1#2 |") {
		t.Fatalf("two windows on the same folder need distinct names:\n%s", res.Text)
	}
}

func TestVSCodeReconnectReplacesSameSession(t *testing.T) {
	d, srv := vscodeDaemon(t)
	a := dialFakeVSCode(t, srv, "backend", "host1", "/p", "s1")
	go runFakeExtension(a, true)
	waitFor(t, func() bool { return vscodeCount(d) == 1 })
	b := dialFakeVSCode(t, srv, "backend", "host1", "/p", "s1") // same window reconnecting
	defer b.Close()
	go runFakeExtension(b, true)
	waitFor(t, func() bool {
		res := d.call(listProfilesMethod, nil, "")
		return strings.Count(res.Text, "vscode:") == 1
	})
	a.Close()
}

// A VS Code window must never become the default target for web tools — that
// would send a Chrome command to an editor window.
func TestVSCodeIsNeverTheChromeDefault(t *testing.T) {
	d, srv := vscodeDaemon(t)
	c := dialFakeVSCode(t, srv, "backend", "host1", "/p", "s1")
	defer c.Close()
	go runFakeExtension(c, true)
	waitFor(t, func() bool { return vscodeCount(d) == 1 })
	res := d.call("get_page_text", nil, "")
	if res.OK || !strings.Contains(res.Error, "Chrome extension not connected") {
		t.Fatalf("a web tool with no profile must not land on a VS Code window: %+v", res)
	}
}

func TestResolveVSCode(t *testing.T) {
	d, srv := vscodeDaemon(t)
	for _, w := range [][2]string{{"backend", "s1"}, {"scam-sv", "s2"}, {"scam-tool", "s3"}} {
		c := dialFakeVSCode(t, srv, w[0], "host1", "/p", w[1])
		defer c.Close()
		go runFakeExtension(c, true)
	}
	waitFor(t, func() bool { return vscodeCount(d) == 3 })

	d.mu.Lock()
	defer d.mu.Unlock()
	if ec, err := d.resolveVSCode("vscode:back"); err != nil || !strings.HasPrefix(ec.account, "vscode:backend") {
		t.Errorf("unique prefix: %v %v", ec, err)
	}
	if _, err := d.resolveVSCode("vscode:scam"); err == nil || !strings.Contains(err.Error(), "scam-sv") || !strings.Contains(err.Error(), "scam-tool") {
		t.Errorf("ambiguous prefix must list both: %v", err)
	}
	if _, err := d.resolveVSCode("vscode:nothing"); err == nil || !strings.Contains(err.Error(), "vscode:backend@host1") {
		t.Errorf("unknown window should list what is connected: %v", err)
	}
	if _, err := d.resolveVSCode(""); err == nil {
		t.Error("no default VS Code window: an empty name must be refused")
	}
}

func TestVSCodeDisconnectRemovesWindow(t *testing.T) {
	d, srv := vscodeDaemon(t)
	c := dialFakeVSCode(t, srv, "backend", "host1", "/p", "s1")
	go runFakeExtension(c, true)
	waitFor(t, func() bool { return vscodeCount(d) == 1 })
	c.Close()
	waitFor(t, func() bool { return vscodeCount(d) == 0 })
}

func TestVSCodeProfileName(t *testing.T) {
	cases := []struct {
		w    vscodeWindow
		want string
	}{
		{vscodeWindow{Name: "backend", Remote: "192.168.0.9-doowon"}, "vscode:backend@192-168-0-9-doowon"},
		{vscodeWindow{Name: "aglink"}, "vscode:aglink"},
		{vscodeWindow{Name: ""}, "vscode:no-folder"},
		// VS Code titles a remote workspace "backend [SSH: host]"; the host is
		// already after the @, so the bracket must not be repeated in the name.
		{vscodeWindow{Name: "backend [SSH: 192.168.0.9-doowon]", Remote: "192.168.0.9-doowon"}, "vscode:backend@192-168-0-9-doowon"},
	}
	for _, c := range cases {
		if got := vscodeProfileName(c.w); got != c.want {
			t.Errorf("%+v → %q, want %q", c.w, got, c.want)
		}
	}
}

// recordingVSCode answers like the extension and remembers what it was asked.
func recordingVSCode(conn *websocket.Conn, got chan<- Request) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req Request
		if json.Unmarshal(data, &req) != nil {
			continue
		}
		if req.Method == pingMethod {
			b, _ := json.Marshal(Reply{ID: 0, OK: true})
			_ = conn.WriteMessage(websocket.TextMessage, b)
			continue
		}
		got <- req
		b, _ := json.Marshal(Reply{ID: req.ID, OK: true, Text: "answer:" + req.Method})
		_ = conn.WriteMessage(websocket.TextMessage, b)
	}
}

func TestVSCodeToolReachesTheWindow(t *testing.T) {
	d, srv := vscodeDaemon(t)
	c := dialFakeVSCode(t, srv, "backend", "host1", "/p", "s1")
	defer c.Close()
	got := make(chan Request, 4)
	go recordingVSCode(c, got)
	waitFor(t, func() bool { return vscodeCount(d) == 1 })

	res := d.call("vscode_read", map[string]any{"path": "src/a.go", "startLine": 10}, "vscode:backend")
	if !res.OK || res.Text != "answer:read" {
		t.Fatalf("round trip: %+v", res)
	}
	req := <-got
	if req.Method != "read" || req.Params["path"] != "src/a.go" {
		t.Fatalf("the extension must get the bare method and the params: %+v", req)
	}
}

func TestVSCodeToolAndProfileMustMatch(t *testing.T) {
	d, srv := vscodeDaemon(t)
	c := dialFakeVSCode(t, srv, "backend", "host1", "/p", "s1")
	defer c.Close()
	go runFakeExtension(c, true)
	waitFor(t, func() bool { return vscodeCount(d) == 1 })

	if res := d.call("vscode_workspace", nil, ""); res.OK || !strings.Contains(res.Error, `profile="vscode:<name>"`) {
		t.Errorf("a vscode tool without a vscode profile: %+v", res)
	}
	if res := d.call("vscode_workspace", nil, "app:aglink"); res.OK || !strings.Contains(res.Error, "needs a VS Code window") {
		t.Errorf("a vscode tool on an app profile: %+v", res)
	}
	if res := d.call("click", map[string]any{"selector": "x"}, "vscode:backend"); res.OK || !strings.Contains(res.Error, "use the vscode_* tools") {
		t.Errorf("a web tool on a vscode profile: %+v", res)
	}
}

func TestVSCodeTimeoutFollowsTheCommand(t *testing.T) {
	if got := vscodeTimeout(nil); got != callTimeout {
		t.Errorf("default: %v", got)
	}
	if got := vscodeTimeout(map[string]any{"timeoutSec": 120}); got != 125*time.Second {
		t.Errorf("a long command must be waited for: %v", got)
	}
	if got := vscodeTimeout(map[string]any{"timeoutSec": float64(99999)}); got != 605*time.Second {
		t.Errorf("capped at 10 minutes: %v", got)
	}
}

func TestInstructionsMentionVSCode(t *testing.T) {
	for _, want := range []string{"vscode:", "vscode_terminal_run", "aglink-vscode"} {
		if !strings.Contains(serverInstructions, want) {
			t.Errorf("server instructions miss %q", want)
		}
	}
}
