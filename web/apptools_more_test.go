package main

import (
	"strings"
	"testing"
	"time"
)

// The page explains a miss and a pick among several, and the reply says so.
func TestAppHintsAndNotes(t *testing.T) {
	f := &fakePage{results: map[string]string{
		"click":    `{"found":false,"hint":"similar elements: button \"Save draft\""}`,
		"getValue": `{"found":true,"value":"x","note":"matched 2 elements, used the first: 1) input#a 2) input#b"}`,
		"getHtml":  `{"found":false,"hint":"not a valid CSS selector (bad)"}`,
	}}
	r := run(t, f, "click", map[string]any{"selector": "text=Save"})
	if r.Error != `no element matched selector: text=Save — similar elements: button "Save draft"` {
		t.Fatalf("hint: %+v", r)
	}
	r = run(t, f, "get_value", map[string]any{"selector": "input"})
	if r.Text != "input = \"x\"\nnote: matched 2 elements, used the first: 1) input#a 2) input#b" {
		t.Fatalf("note: %q", r.Text)
	}
	r = run(t, f, "get_html", map[string]any{"selector": "a[[["})
	if !strings.HasSuffix(r.Error, "— not a valid CSS selector (bad)") {
		t.Fatalf("html hint: %+v", r)
	}
}

// click passes its wait options to the page and reports why it could not
// click, in the page's own words.
func TestAppClickActionability(t *testing.T) {
	f := &fakePage{results: map[string]string{
		"click": `{"found":true,"tag":"button","blocked":"cannot click button \"Pay\" matched by #pay: it is disabled (waited 500ms) — pass force=\"true\" to click it anyway"}`,
	}}
	r := run(t, f, "click", map[string]any{"selector": "#pay", "timeout": 500})
	if !strings.Contains(r.Error, "it is disabled (waited 500ms)") {
		t.Fatalf("blocked: %+v", r)
	}
	if f.calls[0] != `click["#pay","left",{"timeout":500}]` {
		t.Fatalf("options: %s", f.calls[0])
	}
	f.results["click"] = `{"found":true,"tag":"button","text":"Pay"}`
	run(t, f, "click", map[string]any{"selector": "#pay", "force": "true"})
	if f.calls[1] != `click["#pay","left",{"force":true}]` {
		t.Fatalf("force: %s", f.calls[1])
	}
	f.results["doubleClick"] = `{"found":true,"tag":"td","blocked":"cannot double-click td matched by td: it is covered by div.modal (waited 3000ms) — pass force=\"true\" to double-click it anyway"}`
	if r := run(t, f, "double_click", map[string]any{"selector": "td"}); !strings.Contains(r.Error, "covered by div.modal") {
		t.Fatalf("double_click blocked: %+v", r)
	}
}

func TestAppTypeModes(t *testing.T) {
	f := &fakePage{results: map[string]string{
		"typeText":     `{"found":true,"tag":"div","via":"insertText"}`,
		"focusForKeys": `{"found":true,"tag":"textarea","focused":true}`,
	}}
	if r := run(t, f, "type", map[string]any{"selector": "#ed", "text": "더", "mode": "append"}); r.Text != "ok: appended to <div>" {
		t.Fatalf("append: %+v", r)
	}
	if f.calls[0] != `typeText["#ed","더","append"]` {
		t.Fatalf("append args: %s", f.calls[0])
	}
	run(t, f, "type", map[string]any{"selector": "#ed", "text": "x"})
	if f.calls[1] != `typeText["#ed","x","set"]` {
		t.Fatalf("default mode is set: %s", f.calls[1])
	}
	r := run(t, f, "type", map[string]any{"selector": "#msg", "text": "안녕\n둘째 줄", "mode": "keys"})
	if r.Text != "ok: typed 7 chars as keystrokes into <textarea>" {
		t.Fatalf("keys: %+v", r)
	}
	if got := strings.Join(f.raws, ","); got != "Input.insertText,Input.dispatchKeyEvent,Input.dispatchKeyEvent,Input.insertText" {
		t.Fatalf("keys must insert each line with Enter between: %s", got)
	}
	if p := f.rawParams["Input.insertText"].(map[string]any); p["text"] != "둘째 줄" {
		t.Fatalf("last line: %v", p)
	}
	if r := run(t, f, "type", map[string]any{"selector": "#x", "text": "a", "mode": "paste"}); !strings.Contains(r.Error, `unknown mode "paste"`) {
		t.Fatalf("bad mode: %+v", r)
	}
	f.results["focusForKeys"] = `{"found":false}`
	if r := run(t, f, "type", map[string]any{"selector": "#gone", "text": "a", "mode": "keys"}); r.Error != "no element matched selector: #gone" {
		t.Fatalf("keys miss: %+v", r)
	}
}

// navigate says when the page had not finished loading, instead of passing
// it off as done.
func TestAppNavigateStillLoading(t *testing.T) {
	old := navigateLoadTimeout
	navigateLoadTimeout = 50 * time.Millisecond
	defer func() { navigateLoadTimeout = old }()
	f := &fakePage{loading: true}
	r := run(t, f, "navigate", map[string]any{"url": "http://wails.localhost/next"})
	if !strings.HasPrefix(r.Text, "ok: navigated — ") || !strings.HasSuffix(r.Text, "(still loading after 0s)") {
		t.Fatalf("got %+v", r)
	}
	f.loading = false
	if r := run(t, f, "navigate", map[string]any{"url": "http://wails.localhost/next"}); strings.Contains(r.Text, "still loading") {
		t.Fatalf("a loaded page is not 'still loading': %q", r.Text)
	}
}
