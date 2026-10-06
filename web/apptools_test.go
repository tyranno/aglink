package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakePage scripts what each shared page function returns, so the Go
// formatting can be checked against the same strings background.js produces.
type fakePage struct {
	results map[string]string // page function name -> JSON it returns
	calls   []string          // "name(argsJSON)"
	evals   []string
	raws    []string
	dialog  *dialogInfo
	handled *struct {
		accept bool
		text   string
	}
	evalErr error
	eval    string // the Runtime.evaluate result EvalResult returns, as JSON
}

func (f *fakePage) CallPage(ctx context.Context, name string, args []any) (json.RawMessage, error) {
	a, _ := json.Marshal(args)
	f.calls = append(f.calls, name+string(a))
	if f.evalErr != nil {
		return nil, f.evalErr
	}
	r, ok := f.results[name]
	if !ok {
		return json.RawMessage("null"), nil
	}
	return json.RawMessage(r), nil
}
func (f *fakePage) Eval(ctx context.Context, expr string) (json.RawMessage, error) {
	f.evals = append(f.evals, expr)
	if strings.Contains(expr, "readyState") {
		return json.RawMessage(`"complete"`), nil
	}
	return json.RawMessage(`["제목","http://wails.localhost/next"]`), nil
}

// EvalResult answers the eval tool from f.eval (an evalResult as JSON), or
// fails with f.evalErr.
func (f *fakePage) EvalResult(ctx context.Context, expr string) (*evalResult, error) {
	f.evals = append(f.evals, expr)
	if f.evalErr != nil {
		return nil, f.evalErr
	}
	var r evalResult
	_ = json.Unmarshal([]byte(f.eval), &r)
	return &r, nil
}
func (f *fakePage) Raw(ctx context.Context, method string, params any) (json.RawMessage, error) {
	f.raws = append(f.raws, method)
	return json.RawMessage(`{}`), nil
}
func (f *fakePage) Dialog() *dialogInfo { return f.dialog }
func (f *fakePage) HandleDialog(ctx context.Context, accept bool, text string) error {
	f.handled = &struct {
		accept bool
		text   string
	}{accept, text}
	return nil
}
func (f *fakePage) Screenshot(ctx context.Context) (string, error) { return "iVBORw0KGgo=", nil }

var testPages = []cdpTarget{
	{ID: "P1", Type: "page", Title: "aglink", URL: "http://wails.localhost/"},
	{ID: "P2", Type: "page", Title: "설정", URL: "http://wails.localhost/settings"},
}

func run(t *testing.T, f *fakePage, method string, params map[string]any) CallResult {
	t.Helper()
	return appCall(context.Background(), f, testPages, method, params)
}

func TestAppGetPageText(t *testing.T) {
	f := &fakePage{results: map[string]string{"getPageText": `{"found":true,"text":"안녕하세요 세계"}`}}
	r := run(t, f, "get_page_text", map[string]any{})
	if r.Text != "안녕하세요 세계\n[cursor:8]" {
		t.Fatalf("got %q", r.Text)
	}
	if f.calls[0] != `getPageText[null]` {
		t.Fatalf("selector must pass through as null: %s", f.calls[0])
	}
	// Truncation counts UTF-16 units like the extension, so cursors agree.
	r = run(t, f, "get_page_text", map[string]any{"maxChars": 3})
	if r.Text != "안녕하\n[cursor:8 (0..3 of 8)]" {
		t.Fatalf("truncated: %q", r.Text)
	}
	r = run(t, f, "get_page_text", map[string]any{"cursor": float64(6)})
	if r.Text != "세계\n[cursor:8 new:2]" {
		t.Fatalf("cursor (as float64 from /call): %q", r.Text)
	}
	r = run(t, f, "get_page_text", map[string]any{"offset": -2})
	if r.Text != "세계\n[cursor:8 (6..8 of 8)]" {
		t.Fatalf("tail: %q", r.Text)
	}
	f.results["getPageText"] = `{"found":false}`
	r = run(t, f, "get_page_text", map[string]any{"selector": "#nope"})
	if r.Error != "no element matched selector: #nope" {
		t.Fatalf("missing: %+v", r)
	}
}

func TestAppClickAndFriends(t *testing.T) {
	f := &fakePage{results: map[string]string{
		"click":       `{"found":true,"tag":"button","text":"저장 <지금>"}`,
		"hover":       `{"found":true,"tag":"a","text":""}`,
		"doubleClick": `{"found":true,"tag":"td","text":"x"}`,
		"drag":        `{"found":true,"srcTag":"li","dstTag":"ul"}`,
	}}
	if r := run(t, f, "click", map[string]any{"selector": "text=저장"}); r.Text != `ok: left-clicked <button> "저장 <지금>"` {
		t.Fatalf("click: %q (JSON must not escape < >)", r.Text)
	}
	if f.calls[0] != `click["text=저장","left"]` {
		t.Fatalf("default button: %s", f.calls[0])
	}
	if r := run(t, f, "click", map[string]any{"selector": "x", "button": "sideways"}); !strings.Contains(r.Error, `unknown button "sideways"`) {
		t.Fatalf("bad button: %+v", r)
	}
	if r := run(t, f, "click", map[string]any{}); r.Error != "click requires 'selector'" {
		t.Fatalf("no selector: %+v", r)
	}
	if r := run(t, f, "hover", map[string]any{"selector": "a"}); r.Text != "ok: hovered <a>" {
		t.Fatalf("hover: %q", r.Text)
	}
	if r := run(t, f, "double_click", map[string]any{"selector": "td"}); r.Text != `ok: double-clicked <td> "x"` {
		t.Fatalf("dblclick: %q", r.Text)
	}
	if r := run(t, f, "drag", map[string]any{"selector": "li", "target": "ul"}); r.Text != "ok: dragged <li> onto <ul>" {
		t.Fatalf("drag: %q", r.Text)
	}
}

func TestAppQueryAllAndListElements(t *testing.T) {
	f := &fakePage{results: map[string]string{
		"queryAll":            `[{"tag":"a","text":"홈","attrs":[["href","/"]]},{"tag":"a","text":"설정","attrs":[]}]`,
		"listElementsDefault": `[{"idx":0,"tag":"button","role":"","type":"","label":"저장","x":10,"y":20,"disabled":false},{"idx":1,"tag":"input","role":"","type":"text","label":"이름","x":5,"y":6,"disabled":true}]`,
	}}
	r := run(t, f, "query_all", map[string]any{"selector": "a", "attrs": "href, class"})
	want := "0 | a | \"홈\" | href=\"/\"\n1 | a | \"설정\""
	if r.Text != want {
		t.Fatalf("query_all:\n%q\nwant\n%q", r.Text, want)
	}
	if f.calls[0] != `queryAll["a",["href","class"],200]` {
		t.Fatalf("attrs split/trim and default max: %s", f.calls[0])
	}
	r = run(t, f, "list_elements", map[string]any{})
	want = "0 | button | \"저장\" | selector=[data-aglink-id=\"0\"] | viewport(10,20)\n" +
		"1 | input type=text | \"이름\" | selector=[data-aglink-id=\"1\"] | viewport(5,6) [disabled]"
	if r.Text != want {
		t.Fatalf("list_elements:\n%q\nwant\n%q", r.Text, want)
	}
	f.results["queryAll"] = `[]`
	if r := run(t, f, "query_all", map[string]any{"selector": ".x"}); r.Text != "(no elements matched selector: .x)" {
		t.Fatalf("empty: %q", r.Text)
	}
}

func TestAppAttributeValueHtml(t *testing.T) {
	f := &fakePage{results: map[string]string{
		"getAttribute": `{"found":true,"tag":"div","present":false,"value":null}`,
		"getValue":     `{"found":true,"value":"홍길동"}`,
		"getHtml":      `{"found":true,"html":"<p>안녕</p>"}`,
	}}
	if r := run(t, f, "get_attribute", map[string]any{"selector": "div", "name": "aria-expanded"}); r.Text != "aria-expanded = (not present) on <div>" {
		t.Fatalf("attr absent: %q", r.Text)
	}
	if r := run(t, f, "get_value", map[string]any{"selector": "#name"}); r.Text != `#name = "홍길동"` {
		t.Fatalf("value: %q", r.Text)
	}
	if r := run(t, f, "get_html", map[string]any{"maxChars": 4}); r.Text != "<p>안\n… [truncated at 4 chars]" {
		t.Fatalf("html truncation: %q", r.Text)
	}
}

func TestAppTypeKeyScrollSelectEval(t *testing.T) {
	f := &fakePage{results: map[string]string{
		"typeText":        `{"found":true,"tag":"input"}`,
		"keyComboDefault": `{"ok":true,"tag":"body"}`,
		"scroll":          `{"found":true}`,
		"selectOption":    `{"found":true,"isSelect":true,"matched":true,"selected":"서울"}`,
	}}
	if r := run(t, f, "type", map[string]any{"selector": "input", "text": ""}); r.Text != "ok: typed into <input>" {
		t.Fatalf("type (empty text is valid): %+v", r)
	}
	if r := run(t, f, "key", map[string]any{"combo": "ctrl+s"}); r.Text != `ok: pressed "ctrl+s" on <body>` {
		t.Fatalf("key: %q", r.Text)
	}
	if f.calls[len(f.calls)-1] != `keyComboDefault["ctrl+s"]` {
		t.Fatalf("key must use the page's own key table, got %s", f.calls[len(f.calls)-1])
	}
	if r := run(t, f, "scroll", map[string]any{"dy": 300}); r.Text != "ok: scrolled dx=0 dy=300" {
		t.Fatalf("scroll: %q", r.Text)
	}
	if r := run(t, f, "scroll", map[string]any{}); r.Error != "scroll requires a non-zero dx or dy" {
		t.Fatalf("scroll zero: %+v", r)
	}
	if r := run(t, f, "select_option", map[string]any{"selector": "select", "label": "서울"}); r.Text != `ok: selected "서울"` {
		t.Fatalf("select: %q", r.Text)
	}
	if !strings.HasSuffix(f.calls[len(f.calls)-1], `["select",null,"서울"]`) {
		t.Fatalf("absent value must pass as null: %s", f.calls[len(f.calls)-1])
	}
	f.eval = `{"result":{"type":"number","value":42}}`
	if r := run(t, f, "eval", map[string]any{"expression": "6*7"}); r.Text != "42" {
		t.Fatalf("eval: %q", r.Text)
	}
	if f.evals[len(f.evals)-1] != "6*7" {
		t.Fatalf("eval must hand the expression to Runtime.evaluate as is (no page eval(), which CSP blocks): %q", f.evals[len(f.evals)-1])
	}
	for _, c := range calls(f) {
		if strings.HasPrefix(c, "evalExpression") {
			t.Fatal("eval must not go through the page's own eval()")
		}
	}
	f.evalErr = errors.New("page error: ReferenceError: nope is not defined")
	if r := run(t, f, "eval", map[string]any{"expression": "nope"}); r.Error != "eval error: ReferenceError: nope is not defined" {
		t.Fatalf("eval error: %+v", r)
	}
}

func calls(f *fakePage) []string { return f.calls }

// formatEvalValue must print what the extension's eval prints for the same value.
func TestFormatEvalValueMatchesTheExtension(t *testing.T) {
	cases := map[string]string{
		`{"result":{"type":"undefined"}}`:                                          "undefined",
		`{"result":{"type":"string","value":"hi"}}`:                                `"hi"`,
		`{"result":{"type":"object","value":{"a":1,"b":[2,3]}}}`:                   "{\n  \"a\": 1,\n  \"b\": [\n    2,\n    3\n  ]\n}",
		`{"result":{"type":"object","subtype":"null","value":null}}`:               "null",
		`{"result":{"type":"function","description":"function f() { return 1 }"}}`: "function f() { return 1 }",
	}
	for in, want := range cases {
		var r evalResult
		if err := json.Unmarshal([]byte(in), &r); err != nil {
			t.Fatal(err)
		}
		if got := formatEvalValue(&r); got != want {
			t.Errorf("%s:\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestAppTabsNavigateScreenshot(t *testing.T) {
	f := &fakePage{}
	if r := run(t, f, "list_tabs", nil); r.Text != "1 | aglink | http://wails.localhost/\n2 | 설정 | http://wails.localhost/settings" {
		t.Fatalf("list_tabs: %q", r.Text)
	}
	r := run(t, f, "navigate", map[string]any{"url": "http://wails.localhost/next"})
	if r.Text != "ok: navigated — 제목 — http://wails.localhost/next" || f.raws[0] != "Page.navigate" {
		t.Fatalf("navigate: %+v raws=%v", r, f.raws)
	}
	if r := run(t, f, "screenshot", nil); r.Text != "iVBORw0KGgo=" {
		t.Fatalf("screenshot: %+v", r)
	}
	if r := run(t, f, "activate_tab", map[string]any{"tabId": 1}); !r.OK || f.raws[len(f.raws)-1] != "Page.bringToFront" {
		t.Fatalf("activate_tab: %+v", r)
	}
}

func TestAppDialogTools(t *testing.T) {
	f := &fakePage{}
	if r := run(t, f, "dialog_status", nil); r.Text != "no dialog open" {
		t.Fatalf("none: %q", r.Text)
	}
	f.dialog = &dialogInfo{Type: "prompt", Message: "이름?", DefaultPrompt: "홍길동"}
	if r := run(t, f, "dialog_status", nil); r.Text != `prompt: "이름?" (default "홍길동")` {
		t.Fatalf("prompt: %q", r.Text)
	}
	if r := run(t, f, "handle_dialog", map[string]any{"accept": "false"}); !r.OK || f.handled.accept {
		t.Fatalf("dismiss: %+v %+v", r, f.handled)
	}
	if r := run(t, f, "handle_dialog", map[string]any{"prompt_text": "김철수"}); !f.handled.accept || f.handled.text != "김철수" {
		t.Fatalf("accept by default with text: %+v %+v", r, f.handled)
	}
	f.dialog = nil
	if r := run(t, f, "handle_dialog", nil); r.Error != "no dialog open" {
		t.Fatalf("nothing to handle: %+v", r)
	}
}

func TestAppDialogOpenErrorIsExplicit(t *testing.T) {
	f := &fakePage{evalErr: errDialogOpen, dialog: &dialogInfo{Type: "confirm", Message: "지울까요?"}}
	r := run(t, f, "click", map[string]any{"selector": "button"})
	if r.Error != `dialog open: confirm "지울까요?" — call handle_dialog to answer it` {
		t.Fatalf("got %+v", r)
	}
}

func TestAppUnsupportedMethods(t *testing.T) {
	for _, m := range []string{"close_tab", "reload_extension", "get_console_logs", "get_network_requests"} {
		if r := run(t, &fakePage{}, m, nil); r.Error != "not supported for app profiles: "+m {
			t.Errorf("%s: %+v", m, r)
		}
	}
	if r := run(t, &fakePage{evalErr: errors.New("boom")}, "get_value", map[string]any{"selector": "x"}); r.Error != "boom" {
		t.Errorf("transport errors pass through: %+v", r)
	}
}
