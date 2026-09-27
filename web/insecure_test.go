package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestHostMatches(t *testing.T) {
	pats := []string{"nas.local", "10.0.0.5:8443", "*.corp.example", "*", "[::1]"}
	cases := map[string]bool{
		"nas.local":        true,
		"NAS.local:5001":   true, // no port in the pattern: any port
		"10.0.0.5:8443":    true,
		"10.0.0.5":         false, // pattern pins the port
		"10.0.0.5:443":     false,
		"git.corp.example": true,
		"a.b.corp.example": true,
		"corp.example":     false, // *. means subdomains only
		"evilcorp.example": false,
		"example.com":      false, // bare * is ignored
		"[::1]:8080":       true,
		"":                 false,
	}
	for host, want := range cases {
		if got := hostMatches(host, pats); got != want {
			t.Errorf("hostMatches(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestInsecureHostPatternsReadsFileAndEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGLINK_HOME", home)
	t.Setenv(insecureHostsEnv, "a.local, b.local")
	os.WriteFile(filepath.Join(home, insecureHostsFile), []byte("# internal\nnas.local  # the NAS\n\n*.lab\n"), 0o600)
	got := strings.Join(insecureHostPatterns(), " ")
	if got != "a.local b.local nas.local *.lab" {
		t.Fatalf("patterns = %q", got)
	}
}

// runCertExtension answers navigate with a certificate warning for tab 7 and
// records every other method it is asked for.
func runCertExtension(conn *websocket.Conn, seen chan<- string) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req Request
		if json.Unmarshal(data, &req) != nil || req.Method == pingMethod {
			continue
		}
		reply := Reply{ID: req.ID, OK: true}
		switch req.Method {
		case "navigate":
			reply.Text = "ok: navigated tab 7 — Privacy error — https://nas.local/\n" +
				"warning: certificate warning on tab 7 (net::ERR_CERT_AUTHORITY_INVALID, host nas.local) — Chrome is showing its warning instead of the page. To continue past it, call proceed_insecure with tabId=7"
		case "proceed_insecure":
			p, _ := json.Marshal(req.Params)
			seen <- "proceed_insecure:" + string(p)
			reply.Text = "ok: continued past the certificate warning for nas.local via proceed link — NAS — https://nas.local/"
		}
		b, _ := json.Marshal(reply)
		conn.WriteMessage(websocket.TextMessage, b)
	}
}

func TestNavigateAutoProceedsOnlyListedHosts(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	conn := dialFakeExtension(t, srv, "a@b.com")
	defer conn.Close()
	seen := make(chan string, 4)
	go runCertExtension(conn, seen)
	waitForProfile(t, d, "a@b.com")

	// Not listed: the warning comes back untouched, nothing is proceeded.
	t.Setenv(insecureHostsEnv, "other.local")
	res := d.call("navigate", map[string]any{"url": "https://nas.local/"}, "")
	if !res.OK || !strings.Contains(res.Text, "call proceed_insecure with tabId=7") {
		t.Fatalf("unlisted host: %+v", res)
	}
	select {
	case s := <-seen:
		t.Fatalf("proceeded an unlisted host: %s", s)
	case <-time.After(50 * time.Millisecond):
	}

	// Listed: proceed_insecure follows for the tab the warning names.
	t.Setenv(insecureHostsEnv, "nas.local")
	res = d.call("navigate", map[string]any{"url": "https://nas.local/"}, "")
	if !res.OK || !strings.Contains(res.Text, "continued past the certificate warning") || !strings.Contains(res.Text, "auto-proceeded: nas.local") {
		t.Fatalf("listed host: %+v", res)
	}
	if s := <-seen; !strings.Contains(s, `"tabId":7`) {
		t.Fatalf("proceed_insecure params: %s", s)
	}
}

func TestAppProceedInsecure(t *testing.T) {
	f := &fakePage{}
	r := run(t, f, "proceed_insecure", nil)
	if !r.OK || !strings.Contains(r.Text, "certificate errors are ignored") {
		t.Fatalf("got %+v", r)
	}
	if strings.Join(f.raws, ",") != "Security.setIgnoreCertificateErrors,Page.reload" {
		t.Fatalf("raws = %v", f.raws)
	}
}

func TestAppNavigateIgnoresCertErrorsForListedHost(t *testing.T) {
	t.Setenv("AGLINK_HOME", t.TempDir())
	t.Setenv(insecureHostsEnv, "nas.local")
	f := &fakePage{}
	run(t, f, "navigate", map[string]any{"url": "https://nas.local:5001/"})
	if strings.Join(f.raws, ",") != "Security.setIgnoreCertificateErrors,Page.navigate" {
		t.Fatalf("listed host raws = %v", f.raws)
	}
	f = &fakePage{}
	run(t, f, "navigate", map[string]any{"url": "https://elsewhere.example/"})
	if strings.Join(f.raws, ",") != "Page.navigate" {
		t.Fatalf("unlisted host raws = %v", f.raws)
	}
}

func TestCertTarget(t *testing.T) {
	cases := map[string]string{
		"https://nas.local:5001/x?y": "nas.local nas.local:5001",
		"nas.local":                  "nas.local nas.local:443",
		"10.0.0.5:8443":              "10.0.0.5 10.0.0.5:8443",
	}
	for in, want := range cases {
		h, a, err := certTarget(in)
		if err != nil || h+" "+a != want {
			t.Errorf("certTarget(%q) = %q %q %v, want %q", in, h, a, err, want)
		}
	}
	if _, _, err := certTarget("http://nas.local"); err == nil {
		t.Error("http:// has no certificate and must be refused")
	}
}

func TestTrustCertShowsTheCertificateAndHonoursNo(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	var out bytes.Buffer
	if err := trustCert(srv.URL, false, strings.NewReader("n\n"), &out); err != nil {
		t.Fatalf("trustCert: %v", err)
	}
	s := out.String()
	for _, want := range []string{"SHA-256:", "example.com", "Not added."} {
		if !strings.Contains(s, want) {
			t.Fatalf("output lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "WARNING") {
		t.Fatalf("a valid-for-the-name certificate must not warn:\n%s", s)
	}
}

func TestCertProblems(t *testing.T) {
	leaf := httptest.NewTLSServer(nil)
	defer leaf.Close()
	c := leaf.Certificate()
	if p := certProblems(c, "nas.local", time.Now()); len(p) != 1 || !strings.Contains(p[0], "not issued for nas.local") {
		t.Fatalf("name mismatch: %v", p)
	}
	if p := certProblems(c, "127.0.0.1", c.NotAfter.Add(time.Hour)); len(p) != 1 || !strings.Contains(p[0], "validity") {
		t.Fatalf("expired: %v", p)
	}
	if _, self := certAnchor([]*x509.Certificate{c}); !self {
		t.Fatal("httptest's certificate is self-signed")
	}
}

// A plain self-signed server certificate (not a CA) is what most NAS and
// router admin pages present; it must still be recognised as its own root.
func TestCertAnchorSelfSignedLeaf(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "nas.local"},
		DNSNames:     []string{"nas.local"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	if _, self := certAnchor([]*x509.Certificate{c}); !self {
		t.Fatal("a non-CA self-signed certificate is still self-signed")
	}
}

// runStuckExtension behaves like current Chrome: proceed_insecure cannot get
// past the warning in the browser, until the daemon has "typed" the bypass.
func runStuckExtension(conn *websocket.Conn, typed *bool, seen chan<- string) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req Request
		if json.Unmarshal(data, &req) != nil || req.Method == pingMethod {
			continue
		}
		p, _ := json.Marshal(req.Params)
		seen <- req.Method + string(p)
		reply := Reply{ID: req.ID}
		switch {
		case req.Method == "activate_tab":
			reply.OK, reply.Text = true, "ok: activated tab 7 — 개인 정보 보호 오류 — https://nas.local/"
		case req.Method == "proceed_insecure" && req.Params["waitOnly"] != nil && *typed:
			reply.OK, reply.Text = true, "ok: continued past the certificate warning for nas.local via thisisunsafe typed into the Chrome window — NAS — https://nas.local/"
		case req.Method == "proceed_insecure":
			reply.Error = "certificate warning is still showing on tab 7 (the browser's debugger cannot reach its warning page: Cannot attach to this target.). Other ways past it: …"
		}
		b, _ := json.Marshal(reply)
		conn.WriteMessage(websocket.TextMessage, b)
	}
}

func TestProceedTypesTheBypassWhenTheBrowserCannot(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	conn := dialFakeExtension(t, srv, "a@b.com")
	defer conn.Close()
	typed := false
	seen := make(chan string, 8)
	go runStuckExtension(conn, &typed, seen)
	waitForProfile(t, d, "a@b.com")

	var gotTitle, gotKeys string
	orig := typeKeys
	defer func() { typeKeys = orig }()
	typeKeys = func(title, keys string) error {
		gotTitle, gotKeys, typed = title, keys, true
		return nil
	}

	res := d.call("proceed_insecure", nil, "") // no tabId: taken from the extension's error
	if !res.OK || !strings.Contains(res.Text, "via thisisunsafe typed into the Chrome window") {
		t.Fatalf("got %+v", res)
	}
	if gotTitle != "개인 정보 보호 오류" || gotKeys != "thisisunsafe" {
		t.Fatalf("typed %q into %q", gotKeys, gotTitle)
	}
	var calls []string
	for len(seen) > 0 {
		calls = append(calls, <-seen)
	}
	want := []string{`proceed_insecurenull`, `activate_tab{"tabId":7}`, `proceed_insecure{"tabId":7,"waitOnly":1}`}
	if strings.Join(calls, " ") != strings.Join(want, " ") {
		t.Fatalf("calls = %v", calls)
	}
}

func TestProceedDoesNotClaimSuccessWhenTypingIsRefused(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	conn := dialFakeExtension(t, srv, "a@b.com")
	defer conn.Close()
	typed := false
	go runStuckExtension(conn, &typed, make(chan string, 8))
	waitForProfile(t, d, "a@b.com")

	orig := typeKeys
	defer func() { typeKeys = orig }()
	typeKeys = func(title, keys string) error {
		return errors.New(`the Chrome window in front shows "DESKTOP-doowon - Chrome", not the warning tab — nothing was typed`)
	}
	res := d.call("proceed_insecure", map[string]any{"tabId": 7}, "")
	if res.OK || !strings.Contains(res.Error, "nothing was typed") || !strings.Contains(res.Error, "Other ways past it") {
		t.Fatalf("got %+v", res)
	}
}

// runDialogExtension behaves like Chrome with a dialog its debugger cannot
// answer: handle_dialog asks for the keyboard, and dialog_status reports the
// dialog gone once the daemon has pressed a key.
func runDialogExtension(conn *websocket.Conn, pressed *bool) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req Request
		if json.Unmarshal(data, &req) != nil || req.Method == pingMethod {
			continue
		}
		reply := Reply{ID: req.ID}
		switch req.Method {
		case "handle_dialog":
			reply.Error = `dialog needs the keyboard on tab 7 (a dialog (its text is not readable from the browser)): {"code":-32602,"message":"No dialog is showing"}`
		case "activate_tab":
			reply.OK, reply.Text = true, "ok: activated tab 7 — 게시판 — https://intra.example/"
		case "dialog_status":
			reply.OK, reply.Text = true, "a dialog (its text is not readable from the browser) (tab 7)"
			if *pressed {
				reply.Text = "no dialog open"
			}
		}
		b, _ := json.Marshal(reply)
		conn.WriteMessage(websocket.TextMessage, b)
	}
}

func TestHandleDialogFromTheKeyboard(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	conn := dialFakeExtension(t, srv, "a@b.com")
	defer conn.Close()
	pressed := false
	go runDialogExtension(conn, &pressed)
	waitForProfile(t, d, "a@b.com")

	type press struct {
		title, text string
		vk          uint16
	}
	var got []press
	orig := pressKeys
	defer func() { pressKeys = orig }()
	pressKeys = func(title, text string, vk uint16) error {
		got = append(got, press{title, text, vk})
		pressed = true
		return nil
	}

	res := d.call("handle_dialog", map[string]any{"prompt_text": "Kim"}, "")
	if !res.OK || res.Text != "ok: accepted the dialog on tab 7 from the keyboard" {
		t.Fatalf("accept: %+v", res)
	}
	pressed = false
	res = d.call("handle_dialog", map[string]any{"tabId": 7, "accept": "false", "prompt_text": "ignored"}, "")
	if !res.OK || !strings.Contains(res.Text, "dismissed") {
		t.Fatalf("dismiss: %+v", res)
	}
	want := []press{{"게시판", "Kim", vkReturn}, {"게시판", "", vkEscape}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("pressed %+v, want %+v", got, want)
	}
}

func TestHandleDialogKeyboardRefusedIsAnError(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	conn := dialFakeExtension(t, srv, "a@b.com")
	defer conn.Close()
	pressed := false
	go runDialogExtension(conn, &pressed)
	waitForProfile(t, d, "a@b.com")

	orig := pressKeys
	defer func() { pressKeys = orig }()
	pressKeys = func(string, string, uint16) error {
		return errors.New("the window in front is code.exe, not Chrome — nothing was typed")
	}
	res := d.call("handle_dialog", map[string]any{"tabId": 7}, "")
	if res.OK || !strings.Contains(res.Error, "nothing was typed") {
		t.Fatalf("got %+v", res)
	}
}
