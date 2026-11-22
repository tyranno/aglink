package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Console and network capture for app windows (get_console_logs,
// get_network_requests).
//
// In Chrome these come from scripts the extension injects into every page. An
// app window has no extension, but the daemon keeps a DevTools connection to
// it open anyway (for dialogs), so it enables the Runtime and Network domains
// on that connection and keeps the most recent entries in memory. Capture
// starts when aglink-web first attaches to the window — say so if nothing is
// there yet.

const (
	appConsoleKeep = 300
	appNetworkKeep = 300
	// netListBodyMax keeps the list short: a body preview per line, the
	// whole of it (up to netDetailBodyMax) with id=.
	netListBodyMax   = 200
	netDetailBodyMax = 20000
)

type appConsoleEntry struct {
	Level string
	Text  string
}

type appNetEntry struct {
	ID       string
	Type     string // XHR, Fetch, Document, Script, …
	Method   string
	URL      string
	PostData string
	ReqHdrs  json.RawMessage
	RespHdrs json.RawMessage
	Mime     string
	Status   int
	Error    string
	start    float64 // CDP monotonic seconds
	end      float64
}

type appLogs struct {
	mu      sync.Mutex
	console []appConsoleEntry
	net     []*appNetEntry
	byID    map[string]*appNetEntry
	// In-flight fetch/XHR (the same requests the extension counts in Chrome)
	// and when one last started or ended, for wait_for_element's idleMs.
	inflight map[string]string
	lastNet  time.Time
}

func newAppLogs() *appLogs {
	return &appLogs{byID: map[string]*appNetEntry{}, inflight: map[string]string{}, lastNet: time.Now()}
}

// isAjax says whether a CDP resource type is a page's own fetch/XHR.
func isAjax(typ string) bool { return typ == "XHR" || typ == "Fetch" }

// networkIdle returns up to three in-flight URLs, how many there are, and how
// long the window has been without a fetch/XHR starting or ending.
func (l *appLogs) networkIdle() ([]string, int, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var urls []string
	for _, u := range l.inflight {
		if len(urls) == 3 {
			break
		}
		urls = append(urls, u)
	}
	return urls, len(l.inflight), time.Since(l.lastNet)
}

func (l *appLogs) addConsole(level, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.console = append(l.console, appConsoleEntry{Level: level, Text: text})
	if len(l.console) > appConsoleKeep {
		l.console = l.console[len(l.console)-appConsoleKeep:]
	}
}

// onEvent feeds the capture from the window's CDP events.
func (l *appLogs) onEvent(method string, params json.RawMessage) {
	switch method {
	case "Runtime.consoleAPICalled":
		var p struct {
			Type string `json:"type"`
			Args []struct {
				Type        string          `json:"type"`
				Value       json.RawMessage `json:"value"`
				Description string          `json:"description"`
			} `json:"args"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		parts := make([]string, 0, len(p.Args))
		for _, a := range p.Args {
			var s string
			if json.Unmarshal(a.Value, &s) == nil {
				parts = append(parts, s)
			} else if len(a.Value) > 0 {
				parts = append(parts, string(a.Value))
			} else {
				parts = append(parts, a.Description)
			}
		}
		level := p.Type
		if level == "warning" {
			level = "warn"
		}
		l.addConsole(level, strings.Join(parts, " "))
	case "Runtime.exceptionThrown":
		var p struct {
			ExceptionDetails struct {
				Text      string `json:"text"`
				Exception *struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		msg := p.ExceptionDetails.Text
		if p.ExceptionDetails.Exception != nil && p.ExceptionDetails.Exception.Description != "" {
			msg = p.ExceptionDetails.Exception.Description
		}
		l.addConsole("error", "uncaught: "+msg)
	case "Network.requestWillBeSent":
		var p struct {
			RequestID string  `json:"requestId"`
			Type      string  `json:"type"`
			Timestamp float64 `json:"timestamp"`
			Request   struct {
				URL      string          `json:"url"`
				Method   string          `json:"method"`
				PostData string          `json:"postData"`
				Headers  json.RawMessage `json:"headers"`
			} `json:"request"`
		}
		if json.Unmarshal(params, &p) != nil || strings.HasPrefix(p.Request.URL, "data:") {
			return
		}
		e := &appNetEntry{ID: p.RequestID, Type: p.Type, Method: p.Request.Method, URL: p.Request.URL, PostData: p.Request.PostData, ReqHdrs: p.Request.Headers, start: p.Timestamp}
		l.mu.Lock()
		if isAjax(p.Type) {
			l.inflight[p.RequestID] = truncateRunes(p.Request.URL, 200)
			l.lastNet = time.Now()
		}
		l.net = append(l.net, e)
		l.byID[p.RequestID] = e
		if len(l.net) > appNetworkKeep {
			drop := l.net[0]
			delete(l.byID, drop.ID)
			l.net = l.net[1:]
		}
		l.mu.Unlock()
	case "Network.responseReceived":
		var p struct {
			RequestID string `json:"requestId"`
			Type      string `json:"type"`
			Response  struct {
				Status   int             `json:"status"`
				Headers  json.RawMessage `json:"headers"`
				MimeType string          `json:"mimeType"`
			} `json:"response"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		l.mu.Lock()
		if e := l.byID[p.RequestID]; e != nil {
			e.Status = p.Response.Status
			e.RespHdrs = p.Response.Headers
			e.Mime = p.Response.MimeType
			if p.Type != "" {
				e.Type = p.Type
			}
		}
		l.mu.Unlock()
	case "Network.loadingFinished", "Network.loadingFailed":
		var p struct {
			RequestID string  `json:"requestId"`
			Timestamp float64 `json:"timestamp"`
			ErrorText string  `json:"errorText"`
			Canceled  bool    `json:"canceled"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		l.mu.Lock()
		if _, ok := l.inflight[p.RequestID]; ok {
			delete(l.inflight, p.RequestID)
			l.lastNet = time.Now()
		}
		if e := l.byID[p.RequestID]; e != nil {
			e.end = p.Timestamp
			if method == "Network.loadingFailed" {
				e.Error = p.ErrorText
				if p.Canceled {
					e.Error = "canceled"
				}
			}
		}
		l.mu.Unlock()
	}
}

// consoleText renders the last q.Max console entries matching q as the
// extension does.
func (l *appLogs) consoleText(q consoleQuery) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var entries []appConsoleEntry
	for _, e := range l.console {
		if q.match(e.Level, e.Text) {
			entries = append(entries, e)
		}
	}
	if len(entries) > q.Max {
		entries = entries[len(entries)-q.Max:]
	}
	if len(entries) == 0 {
		if q.filtered() && len(l.console) > 0 {
			return fmt.Sprintf("(no console messages match — %d captured in all)", len(l.console))
		}
		return "(no console messages captured — capture starts when aglink-web first attaches to the window; reproduce the action now)"
	}
	lines := make([]string, len(entries))
	for i, e := range entries {
		lines[i] = fmt.Sprintf("[%s] %s", e.Level, e.Text)
	}
	return strings.Join(lines, "\n")
}

// networkEntries returns copies of the last q.Max requests matching q, and
// how many are captured in all.
func (l *appLogs) networkEntries(q netQuery) ([]appNetEntry, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []appNetEntry
	for _, e := range l.net {
		if q.match(e.Method, e.URL, e.Status, e.Error != "") {
			out = append(out, *e)
		}
	}
	if len(out) > q.Max {
		out = out[len(out)-q.Max:]
	}
	return out, len(l.net)
}

// networkEntry returns a copy of the request with CDP id id.
func (l *appLogs) networkEntry(id string) (appNetEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.byID[id]; e != nil {
		return *e, true
	}
	return appNetEntry{}, false
}

// responseBody fetches a finished request's body over raw; "" when there is
// none to show (evicted, a redirect, a preflight).
func responseBody(ctx context.Context, e appNetEntry, raw func(context.Context, string, any) (json.RawMessage, error)) string {
	if e.end == 0 || e.Error != "" || raw == nil {
		return ""
	}
	bctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	r, err := raw(bctx, "Network.getResponseBody", map[string]any{"requestId": e.ID})
	if err != nil {
		return ""
	}
	var body struct {
		Body          string `json:"body"`
		Base64Encoded bool   `json:"base64Encoded"`
	}
	if json.Unmarshal(r, &body) != nil || body.Body == "" {
		return ""
	}
	if body.Base64Encoded {
		return fmt.Sprintf("(binary, %d bytes)", len(body.Body)*3/4)
	}
	return fixMojibake(body.Body)
}

// netStatusText is a request's "-> 200 (12ms)" / "ERROR: …" / "-> (pending)".
func netStatusText(e appNetEntry) string {
	switch {
	case e.Error != "":
		return "ERROR: " + e.Error
	case e.Status == 0:
		return "-> (pending)"
	}
	ms := ""
	if e.end > 0 && e.start > 0 {
		ms = fmt.Sprintf(" (%dms)", int((e.end-e.start)*1000))
	}
	return fmt.Sprintf("-> %d%s", e.Status, ms)
}

// networkDetail is get_network_requests id=: one request in full — headers
// both ways and the bodies up to netDetailBodyMax.
func networkDetail(ctx context.Context, e appNetEntry, raw func(context.Context, string, any) (json.RawMessage, error)) string {
	lines := []string{
		fmt.Sprintf("%s | %s %s", strings.ToLower(e.Type), e.Method, e.URL),
		"status: " + netStatusText(e),
	}
	if len(e.ReqHdrs) > 0 && string(e.ReqHdrs) != "null" {
		lines = append(lines, "request headers: "+compactJSON(e.ReqHdrs))
	}
	if e.PostData != "" {
		lines = append(lines, "request body: "+jsQuote(truncateRunes(e.PostData, netDetailBodyMax)))
	}
	if len(e.RespHdrs) > 0 && string(e.RespHdrs) != "null" {
		lines = append(lines, "response headers: "+compactJSON(e.RespHdrs))
	}
	if e.Mime != "" {
		lines = append(lines, "mime: "+e.Mime)
	}
	if b := responseBody(ctx, e, raw); strings.HasPrefix(b, "(binary,") {
		lines = append(lines, "response body: "+b)
	} else if b != "" {
		lines = append(lines, "response body: "+jsQuote(truncateRunes(b, netDetailBodyMax)))
	}
	return strings.Join(lines, "\n")
}

// networkText renders requests like the extension's get_network_requests,
// fetching response bodies for XHR/fetch calls (the API traffic the tool is
// for) over raw. Bodies are previews of netListBodyMax; a line's id= is what
// get_network_requests id= takes for the whole request. total is how many
// requests are captured in all, for the reply when the filters match none.
func networkText(ctx context.Context, entries []appNetEntry, total int, q netQuery, raw func(context.Context, string, any) (json.RawMessage, error)) string {
	if len(entries) == 0 {
		if q.filtered() && total > 0 {
			return fmt.Sprintf("(no network requests match — %d captured in all)", total)
		}
		return "(no network requests captured — capture starts when aglink-web first attaches to the window; reproduce the action now)"
	}
	lines := make([]string, len(entries))
	for i, e := range entries {
		parts := []string{fmt.Sprintf("%d | %s | %s %s", i, strings.ToLower(e.Type), e.Method, e.URL), netStatusText(e), "id=" + e.ID}
		if e.PostData != "" {
			parts = append(parts, "req="+jsQuote(truncateRunes(e.PostData, netListBodyMax)))
		}
		if isAjax(e.Type) {
			if b := responseBody(ctx, e, raw); b != "" && !strings.HasPrefix(b, "(binary,") {
				parts = append(parts, "resp="+jsQuote(truncateRunes(b, netListBodyMax)))
			}
		}
		lines[i] = strings.Join(parts, " | ")
	}
	return strings.Join(lines, "\n")
}

// cp1252High maps Windows-1252's 0x80–0x9F bytes to the characters they decode
// to; the rest of 0x00–0xFF decode to the same code point.
var cp1252High = map[rune]byte{
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84, '…': 0x85, '†': 0x86, '‡': 0x87,
	'ˆ': 0x88, '‰': 0x89, 'Š': 0x8A, '‹': 0x8B, 'Œ': 0x8C, 'Ž': 0x8E,
	'‘': 0x91, '’': 0x92, '“': 0x93, '”': 0x94, '•': 0x95, '–': 0x96, '—': 0x97,
	'˜': 0x98, '™': 0x99, 'š': 0x9A, '›': 0x9B, 'œ': 0x9C, 'ž': 0x9E, 'Ÿ': 0x9F,
}

// fixMojibake undoes a UTF-8 body that was decoded as Windows-1252: a WebView2
// custom-scheme response (Wails' runtime calls) carries no charset, so
// Network.getResponseBody hands back Korean JSON as "ì–´ë–¤". When every
// character maps back to one byte and those bytes are valid UTF-8 with at
// least one multi-byte sequence, the original text is restored; anything else
// is returned untouched.
func fixMojibake(s string) string {
	buf := make([]byte, 0, len(s))
	high := false
	for _, r := range s {
		switch {
		case r < 0x80:
			buf = append(buf, byte(r))
		case r <= 0xFF:
			buf = append(buf, byte(r))
			high = true
		default:
			b, ok := cp1252High[r]
			if !ok {
				return s
			}
			buf = append(buf, b)
			high = true
		}
	}
	if !high || !utf8.Valid(buf) {
		return s
	}
	return string(buf)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
