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
	if got := l.consoleText(consoleQuery{Max: 50}); got != "[log] saved 3\n[warn] Object\n[error] uncaught: TypeError: x is undefined" {
		t.Fatalf("console:\n%s", got)
	}
	if got := l.consoleText(consoleQuery{Max: 1}); got != "[error] uncaught: TypeError: x is undefined" {
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
	all, total := l.networkEntries(netQuery{Max: 50})
	got := networkText(context.Background(), all, total, netQuery{Max: 50}, raw)
	want := []string{
		`0 | fetch | POST http://wails.localhost/api/save | -> 200 (250ms) | id=1 | req="{\"name\":\"kim\"}" | resp="{\"ok\":true}"`,
		`1 | script | GET http://wails.localhost/app.js | ERROR: net::ERR_FAILED | id=2`,
	}
	if got != strings.Join(want, "\n") {
		t.Fatalf("network:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	upper, _ := parseNetQuery(map[string]any{"filter": "/API/"})
	if got, _ := l.networkEntries(upper); len(got) != 1 {
		t.Fatalf("filter is case-insensitive on the URL: %d", len(got))
	}
}

func TestAppLogsFilters(t *testing.T) {
	l := newAppLogs()
	ev := func(method, params string) { l.onEvent(method, json.RawMessage(params)) }
	ev("Runtime.consoleAPICalled", `{"type":"log","args":[{"type":"string","value":"Saved draft"}]}`)
	ev("Runtime.consoleAPICalled", `{"type":"warning","args":[{"type":"string","value":"slow render"}]}`)
	ev("Runtime.exceptionThrown", `{"exceptionDetails":{"text":"Uncaught","exception":{"description":"TypeError: draft is null"}}}`)

	q, err := parseConsoleQuery(map[string]any{"level": "error,warning"})
	if err != nil {
		t.Fatal(err)
	}
	if got := l.consoleText(q); got != "[warn] slow render\n[error] uncaught: TypeError: draft is null" {
		t.Fatalf("level: %q", got)
	}
	q, _ = parseConsoleQuery(map[string]any{"filter": "DRAFT"})
	if got := l.consoleText(q); got != "[log] Saved draft\n[error] uncaught: TypeError: draft is null" {
		t.Fatalf("filter: %q", got)
	}
	q, _ = parseConsoleQuery(map[string]any{"level": "debug"})
	if got := l.consoleText(q); got != "(no console messages match — 3 captured in all)" {
		t.Fatalf("none match: %q", got)
	}

	ev("Network.requestWillBeSent", `{"requestId":"a","type":"Fetch","timestamp":1,"request":{"url":"http://x/api/list","method":"GET","headers":{"Accept":"application/json"}}}`)
	ev("Network.responseReceived", `{"requestId":"a","type":"Fetch","response":{"status":200,"mimeType":"application/json","headers":{"content-type":"application/json"}}}`)
	ev("Network.loadingFinished", `{"requestId":"a","timestamp":1.1}`)
	ev("Network.requestWillBeSent", `{"requestId":"b","type":"Fetch","timestamp":2,"request":{"url":"http://x/api/save","method":"POST","postData":"`+strings.Repeat("x", 500)+`"}}`)
	ev("Network.responseReceived", `{"requestId":"b","type":"Fetch","response":{"status":422}}`)
	ev("Network.loadingFinished", `{"requestId":"b","timestamp":2.1}`)
	ev("Network.requestWillBeSent", `{"requestId":"c","type":"XHR","timestamp":3,"request":{"url":"http://x/api/ping","method":"GET"}}`)
	ev("Network.loadingFailed", `{"requestId":"c","timestamp":3.1,"errorText":"net::ERR_INTERNET_DISCONNECTED"}`)
	ev("Network.requestWillBeSent", `{"requestId":"d","type":"XHR","timestamp":4,"request":{"url":"http://x/api/slow","method":"GET"}}`)

	ids := func(params map[string]any) string {
		q, err := parseNetQuery(params)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := l.networkEntries(q)
		var out []string
		for _, e := range got {
			out = append(out, e.ID)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{"status": "4xx"}, "b"},
		{map[string]any{"status": "failed"}, "c"},
		{map[string]any{"status": "200,422"}, "a,b"},
		{map[string]any{"status": "400-599, failed"}, "b,c"},
		{map[string]any{"status": "pending"}, "d"},
		{map[string]any{"method": "post"}, "b"},
		{map[string]any{"method": "GET", "status": "2xx"}, "a"},
	} {
		if got := ids(c.params); got != c.want {
			t.Errorf("%v: got %q want %q", c.params, got, c.want)
		}
	}

	raw := func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		return json.RawMessage(`{"body":"{\"items\":[1,2,3]}","base64Encoded":false}`), nil
	}
	// The list keeps bodies short; the detail has them whole, with headers.
	entries, total := l.networkEntries(netQuery{Max: 50, Methods: map[string]bool{"POST": true}})
	list := networkText(context.Background(), entries, total, netQuery{}, raw)
	if !strings.Contains(list, `req="`+strings.Repeat("x", netListBodyMax)+`…"`) {
		t.Fatalf("list body not clipped: %s", list)
	}
	e, ok := l.networkEntry("a")
	if !ok {
		t.Fatal("entry a")
	}
	detail := networkDetail(context.Background(), e, raw)
	for _, want := range []string{
		"fetch | GET http://x/api/list",
		"status: -> 200 (100ms)",
		`request headers: {"Accept":"application/json"}`,
		`response headers: {"content-type":"application/json"}`,
		`response body: "{\"items\":[1,2,3]}"`,
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail lacks %q:\n%s", want, detail)
		}
	}
	if got := networkText(context.Background(), nil, 4, netQuery{Filter: "nothing"}, raw); got != "(no network requests match — 4 captured in all)" {
		t.Fatalf("none match: %q", got)
	}
}

func TestAppLogsEmptySaysWhenCaptureStarts(t *testing.T) {
	l := newAppLogs()
	if !strings.Contains(l.consoleText(consoleQuery{Max: 50}), "capture starts when aglink-web first attaches") {
		t.Fatal(l.consoleText(consoleQuery{Max: 50}))
	}
	if !strings.Contains(networkText(context.Background(), nil, 0, netQuery{}, nil), "capture starts") {
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
