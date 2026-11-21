package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestNMFramingRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	msgs := []string{`{"type":"hello"}`, `{}`, strings.Repeat("x", 70000)}
	for _, m := range msgs {
		if err := writeNMMessage(&buf, []byte(m)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if got := buf.Bytes()[:4]; !bytes.Equal(got, []byte{16, 0, 0, 0}) {
		t.Fatalf("length prefix must be 32-bit little-endian, got % x", got)
	}
	for _, want := range msgs {
		got, err := readNMMessage(&buf)
		if err != nil || string(got) != want {
			t.Fatalf("read: %v, got %d bytes want %d", err, len(got), len(want))
		}
	}
	if _, err := readNMMessage(&buf); !errors.Is(err, io.EOF) {
		t.Fatalf("empty stream should be EOF: %v", err)
	}
}

func TestNMWriteRefusesOverOneMB(t *testing.T) {
	var buf bytes.Buffer
	if err := writeNMMessage(&buf, make([]byte, nmMaxToChrome)); err != nil {
		t.Fatalf("exactly 1 MB is allowed: %v", err)
	}
	buf.Reset()
	if err := writeNMMessage(&buf, make([]byte, nmMaxToChrome+1)); !errors.Is(err, errNMTooLarge) {
		t.Fatalf("over 1 MB must be refused: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatal("a refused message must write nothing, not a partial frame")
	}
}

func TestNMReadRefusesHugeLength(t *testing.T) {
	r := bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff})
	if _, err := readNMMessage(r); err == nil {
		t.Fatal("a 4 GB length must be refused before allocating")
	}
}

// TestNMRelayEndToEnd plays Chrome on the host's stdin/stdout: hello, then a
// daemon call travels daemon → host → "extension" and its reply comes back.
func TestNMRelayEndToEnd(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	dial := func(account, boot string) (*websocket.Conn, error) {
		u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ext?" + url.Values{"account": {account}, "boot": {boot}}.Encode()
		hdr := http.Header{}
		hdr.Set("Origin", "chrome-extension://fake")
		c, _, err := websocket.DefaultDialer.Dial(u, hdr)
		return c, err
	}

	toHostR, toHostW := io.Pipe()
	fromHostR, fromHostW := io.Pipe()
	relayDone := make(chan error, 1)
	go func() { relayDone <- relayNM(toHostR, fromHostW, dial) }()

	send := func(v any) {
		b, _ := json.Marshal(v)
		if err := writeNMMessage(toHostW, b); err != nil {
			t.Errorf("send: %v", err)
		}
	}
	recv := func() map[string]any {
		b, err := readNMMessage(fromHostR)
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}

	send(nmHello{Type: "hello", Account: "nm@example.com", Boot: "b1"})
	if m := recv(); m["type"] != "ready" {
		t.Fatalf("want ready, got %v", m)
	}
	waitForProfile(t, d, "nm@example.com")

	res := make(chan CallResult, 1)
	go func() { res <- d.call("list_tabs", nil, "") }()
	var req map[string]any
	for {
		req = recv()
		if req["method"] != pingMethod {
			break
		}
	}
	if req["method"] != "list_tabs" {
		t.Fatalf("relayed request: %v", req)
	}
	send(Reply{ID: uint64(req["id"].(float64)), OK: true, Text: "1 | tab"})
	select {
	case r := <-res:
		if !r.OK || r.Text != "1 | tab" {
			t.Fatalf("reply through relay: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reply did not come back through the relay")
	}

	// A request Chrome could not accept is answered by the host itself.
	big := d.call("eval", map[string]any{"expression": strings.Repeat("1", nmMaxToChrome)}, "")
	if big.OK || !strings.Contains(big.Error, "too large for the native messaging") {
		t.Fatalf("oversized request: %+v", big)
	}

	// Chrome closing the port ends the relay cleanly and unregisters.
	toHostW.Close()
	select {
	case err := <-relayDone:
		if err != nil {
			t.Fatalf("relay should end cleanly on EOF: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not end when Chrome closed the port")
	}
	waitFor(t, func() bool { return profileCount(d) == 0 })
}

func TestNMRelayRejectsBadHello(t *testing.T) {
	var in, out bytes.Buffer
	_ = writeNMMessage(&in, []byte(`{"type":"hello"}`))
	err := relayNM(&in, &out, func(string, string) (*websocket.Conn, error) {
		t.Fatal("must not dial without an account")
		return nil, nil
	})
	if err == nil {
		t.Fatal("hello without account must fail")
	}
	b, _ := readNMMessage(&out)
	if !strings.Contains(string(b), `"type":"error"`) {
		t.Fatalf("the extension should be told why: %s", b)
	}
}
