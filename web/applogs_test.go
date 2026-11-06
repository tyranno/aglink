package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAppLogsFromCDPEvents(t *testing.T) {
	l := newAppLogs()
	ev := func(method, params string) { l.onEvent(method, json.RawMessage(params)) }

	ev("Runtime.consoleAPICalled", `{"type":"log","args":[{"type":"string","value":"saved"},{"type":"number","value":3}]}`)
	ev("Runtime.consoleAPICalled", `{"type":"warning","args":[{"type":"object","description":"Object"}]}`)
	ev("Runtime.exceptionThrown", `{"exceptionDetails":{"text":"Uncaught","exception":{"description":"TypeError: x is undefined"}}}`)
	if got := l.consoleText(50); got != "[log] saved 3\n[warn] Object\n[error] uncaught: TypeError: x is undefined" {
		t.Fatalf("console:\n%s", got)
	}
	if got := l.consoleText(1); got != "[error] uncaught: TypeError: x is undefined" {
		t.Fatalf("max: %q", got)
	}

	ev("Network.requestWillBeSent", `{"requestId":"1","type":"Fetch","timestamp":10.0,"request":{"url":"http://wails.localhost/api/save","method":"POST","postData":"{\"name\":\"kim\"}"}}`)
	ev("Network.responseReceived", `{"requestId":"1","type":"Fetch","response":{"status":200}}`)
	ev("Network.loadingFinished", `{"requestId":"1","timestamp":10.25}`)
	ev("Network.requestWillBeSent", `{"requestId":"2","type":"Script","timestamp":11.0,"request":{"url":"http://wails.localhost/app.js","method":"GET"}}`)
	ev("Network.loadingFailed", `{"requestId":"2","timestamp":11.1,"errorText":"net::ERR_FAILED"}`)
	ev("Network.requestWillBeSent", `{"requestId":"3","type":"Image","timestamp":12.0,"request":{"url":"data:image/png;base64,AAAA","method":"GET"}}`)

	raw := func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		if method != "Network.getResponseBody" {
			t.Fatalf("unexpected CDP call %s", method)
		}
		return json.RawMessage(`{"body":"{\"ok\":true}","base64Encoded":false}`), nil
	}
	got := networkText(context.Background(), l.networkEntries(50, ""), raw)
	want := []string{
		`0 | fetch | POST http://wails.localhost/api/save | -> 200 (250ms) | req="{\"name\":\"kim\"}" | resp="{\"ok\":true}"`,
		`1 | script | GET http://wails.localhost/app.js | ERROR: net::ERR_FAILED`,
	}
	if got != strings.Join(want, "\n") {
		t.Fatalf("network:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	if n := len(l.networkEntries(50, "/API/")); n != 1 {
		t.Fatalf("filter is case-insensitive on the URL: %d", n)
	}
}

func TestAppLogsEmptySaysWhenCaptureStarts(t *testing.T) {
	l := newAppLogs()
	if !strings.Contains(l.consoleText(50), "capture starts when aglink-web first attaches") {
		t.Fatal(l.consoleText(50))
	}
	if !strings.Contains(networkText(context.Background(), nil, nil), "capture starts") {
		t.Fatal("network empty text")
	}
}

func TestAppLogsAreBounded(t *testing.T) {
	l := newAppLogs()
	for i := 0; i < appConsoleKeep+50; i++ {
		l.addConsole("log", "x")
	}
	for i := 0; i < appNetworkKeep+50; i++ {
		l.onEvent("Network.requestWillBeSent", json.RawMessage(`{"requestId":"r`+strings.Repeat("1", i%7)+string(rune('a'+i%26))+`","type":"XHR","request":{"url":"http://a/","method":"GET"}}`))
	}
	if len(l.console) != appConsoleKeep || len(l.net) > appNetworkKeep {
		t.Fatalf("console=%d net=%d", len(l.console), len(l.net))
	}
}

func TestFixMojibake(t *testing.T) {
	orig := `{"title":"① 어떤 AI를 쓸까요 — 기본값"}`
	// What getResponseBody returns: the UTF-8 bytes read as Windows-1252.
	var garbled strings.Builder
	for _, b := range []byte(orig) {
		r := rune(b)
		for k, v := range cp1252High {
			if v == b {
				r = k
			}
		}
		garbled.WriteRune(r)
	}
	if got := fixMojibake(garbled.String()); got != orig {
		t.Fatalf("not restored:\n%s", got)
	}
	// Text that is already right is left alone.
	for _, s := range []string{orig, "plain ascii", "café", `{"ok":true}`} {
		if got := fixMojibake(s); got != s {
			t.Errorf("changed %q to %q", s, got)
		}
	}
}

func TestAppLogsTrackInflightAjax(t *testing.T) {
	l := newAppLogs()
	ev := func(method, params string) { l.onEvent(method, json.RawMessage(params)) }
	ev("Network.requestWillBeSent", `{"requestId":"1","type":"XHR","request":{"url":"http://x/api/a","method":"GET"}}`)
	ev("Network.requestWillBeSent", `{"requestId":"2","type":"Image","request":{"url":"http://x/a.png","method":"GET"}}`)
	urls, n, _ := l.networkIdle()
	if n != 1 || len(urls) != 1 || urls[0] != "http://x/api/a" {
		t.Fatalf("only fetch/XHR count as in flight: %v %d", urls, n)
	}
	ev("Network.loadingFinished", `{"requestId":"1"}`)
	ev("Network.loadingFinished", `{"requestId":"2"}`)
	if _, n, idle := l.networkIdle(); n != 0 || idle > time.Second {
		t.Fatalf("after finish: n=%d idle=%s", n, idle)
	}
}
