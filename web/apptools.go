package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// This file runs the tool set against an Electron/Wails window. Each method is
// a translation of the handler with the same name in extension/background.js:
// the in-page half is the shared function in page-actions.js, and the result
// formatting below reproduces background.js's text exactly — same messages,
// same defaults, same shape — so a tool reads the same whichever kind of
// target it drove.

// appPage is what the tool set needs from a window. *appTarget implements it;
// tests substitute a scripted fake.
type appPage interface {
	CallPage(ctx context.Context, name string, args []any) (json.RawMessage, error)
	Eval(ctx context.Context, expr string) (json.RawMessage, error)
	Raw(ctx context.Context, method string, params any) (json.RawMessage, error)
	Dialog() *dialogInfo
	HandleDialog(ctx context.Context, accept bool, promptText string) error
	Screenshot(ctx context.Context) (string, error)
}

// Mirrors of background.js constants.
const (
	defaultMaxChars       = 20000
	defaultQueryMax       = 200
	defaultWaitTimeoutMs  = 8000
	waitForElementPoll    = 150 * time.Millisecond
	aglinkIDAttr          = "data-aglink-id"
	navigateLoadTimeout   = 10 * time.Second
	navigateLoadPollDelay = 100 * time.Millisecond
)

func appCall(ctx context.Context, p appPage, pages []cdpTarget, method string, params map[string]any) CallResult {
	if params == nil {
		params = map[string]any{}
	}
	text, err := appDispatch(ctx, p, pages, method, params)
	if err != nil {
		if errors.Is(err, errDialogOpen) {
			if d := p.Dialog(); d != nil {
				return CallResult{Error: fmt.Sprintf("dialog open: %s %s — call handle_dialog to answer it", d.Type, jsQuote(d.Message))}
			}
			return CallResult{Error: "dialog open — call dialog_status, then handle_dialog"}
		}
		return CallResult{Error: err.Error()}
	}
	return CallResult{OK: true, Text: text}
}

func appDispatch(ctx context.Context, p appPage, pages []cdpTarget, method string, params map[string]any) (string, error) {
	sel := str(params, "selector")
	notFound := func() error { return fmt.Errorf("no element matched selector: %s", sel) }

	switch method {
	case "list_tabs":
		var b strings.Builder
		for i, t := range pages {
			if i > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "%d | %s | %s", i+1, t.Title, t.URL)
		}
		if b.Len() == 0 {
			return "(no open windows)", nil
		}
		return b.String(), nil

	case "activate_tab":
		if _, err := p.Raw(ctx, "Page.bringToFront", nil); err != nil {
			return "", err
		}
		return fmt.Sprintf("ok: activated window %d", intp(params, "tabId", 1)), nil

	case "navigate":
		url := str(params, "url")
		if url == "" {
			return "", errors.New("navigate requires 'url'")
		}
		if _, err := p.Raw(ctx, "Page.navigate", map[string]any{"url": url}); err != nil {
			return "", err
		}
		deadline := time.Now().Add(navigateLoadTimeout)
		for time.Now().Before(deadline) {
			raw, err := p.Eval(ctx, "document.readyState")
			if err == nil && string(raw) == `"complete"` {
				break
			}
			time.Sleep(navigateLoadPollDelay)
		}
		raw, err := p.Eval(ctx, "[document.title, location.href]")
		if err != nil {
			return "", err
		}
		var tu [2]string
		_ = json.Unmarshal(raw, &tu)
		return fmt.Sprintf("ok: navigated — %s — %s", tu[0], tu[1]), nil

	case "screenshot":
		return p.Screenshot(ctx)

	case "get_page_text":
		var r struct {
			Found bool   `json:"found"`
			Text  string `json:"text"`
		}
		if err := callInto(ctx, p, "getPageText", []any{nullable(sel)}, &r); err != nil {
			return "", err
		}
		if sel != "" && !r.Found {
			return "", notFound()
		}
		return formatPageText(r.Text, params), nil

	case "element_exists":
		if sel == "" {
			return "", errors.New("element_exists requires 'selector'")
		}
		raw, err := p.CallPage(ctx, "elementExists", []any{sel})
		if err != nil {
			return "", err
		}
		if string(raw) == "null" || len(raw) == 0 {
			return `{"exists":false,"visible":false,"count":0}`, nil
		}
		// Compact the page's own bytes rather than decoding into a map, which
		// would reorder the keys and stop matching the extension's output.
		var b bytes.Buffer
		if json.Compact(&b, raw) != nil {
			return string(raw), nil
		}
		return b.String(), nil

	case "click":
		if sel == "" {
			return "", errors.New("click requires 'selector'")
		}
		button := strings.ToLower(str(params, "button"))
		if button == "" {
			button = "left"
		}
		if button != "left" && button != "right" && button != "middle" {
			return "", fmt.Errorf("click: unknown button %s (want left/right/middle)", jsQuote(str(params, "button")))
		}
		var r tagText
		if err := callInto(ctx, p, "click", []any{sel, button}, &r); err != nil {
			return "", err
		}
		if !r.Found {
			return "", notFound()
		}
		return fmt.Sprintf("ok: %s-clicked <%s>%s", button, r.Tag, r.suffix()), nil

	case "hover", "double_click":
		if sel == "" {
			return "", fmt.Errorf("%s requires 'selector'", method)
		}
		fn, verb := "hover", "hovered"
		if method == "double_click" {
			fn, verb = "doubleClick", "double-clicked"
		}
		var r tagText
		if err := callInto(ctx, p, fn, []any{sel}, &r); err != nil {
			return "", err
		}
		if !r.Found {
			return "", notFound()
		}
		return fmt.Sprintf("ok: %s <%s>%s", verb, r.Tag, r.suffix()), nil

	case "drag":
		target := str(params, "target")
		if sel == "" {
			return "", errors.New("drag requires 'selector' (the source)")
		}
		if target == "" {
			return "", errors.New("drag requires 'target' (the destination)")
		}
		var r struct {
			Found          bool
			Which, Sel     string
			SrcTag, DstTag string
		}
		raw, err := p.CallPage(ctx, "drag", []any{sel, target})
		if err != nil {
			return "", err
		}
		if string(raw) == "null" || json.Unmarshal(raw, &r) != nil {
			return "", errors.New("drag failed")
		}
		if !r.Found {
			return "", fmt.Errorf("no element matched %s selector: %s", r.Which, r.Sel)
		}
		return fmt.Sprintf("ok: dragged <%s> onto <%s>", r.SrcTag, r.DstTag), nil

	case "get_html":
		var r struct {
			Found bool   `json:"found"`
			HTML  string `json:"html"`
		}
		if err := callInto(ctx, p, "getHtml", []any{nullable(sel)}, &r); err != nil {
			return "", err
		}
		if !r.Found {
			return "", notFound()
		}
		maxChars := intp(params, "maxChars", defaultMaxChars)
		html := r.HTML
		if jsLen(html) > maxChars {
			html = jsSlice(html, 0, maxChars) + fmt.Sprintf("\n… [truncated at %d chars]", maxChars)
		}
		return html, nil

	case "query_all":
		if sel == "" {
			return "", errors.New("query_all requires 'selector'")
		}
		attrs := []string{}
		for _, a := range strings.Split(str(params, "attrs"), ",") {
			if a = strings.TrimSpace(a); a != "" {
				attrs = append(attrs, a)
			}
		}
		var els []struct {
			Tag   string     `json:"tag"`
			Text  string     `json:"text"`
			Attrs [][]string `json:"attrs"`
		}
		if err := callInto(ctx, p, "queryAll", []any{sel, attrs, intp(params, "max", defaultQueryMax)}, &els); err != nil {
			return "", err
		}
		if len(els) == 0 {
			return fmt.Sprintf("(no elements matched selector: %s)", sel), nil
		}
		lines := make([]string, len(els))
		for i, e := range els {
			var as []string
			for _, kv := range e.Attrs {
				if len(kv) == 2 {
					as = append(as, kv[0]+"="+jsQuote(kv[1]))
				}
			}
			line := fmt.Sprintf("%d | %s | %s", i, e.Tag, jsQuote(e.Text))
			if len(as) > 0 {
				line += " | " + strings.Join(as, " ")
			}
			lines[i] = line
		}
		return strings.Join(lines, "\n"), nil

	case "get_attribute":
		name := str(params, "name")
		if sel == "" {
			return "", errors.New("get_attribute requires 'selector'")
		}
		if name == "" {
			return "", errors.New("get_attribute requires 'name' (an attribute name, or 'text' for textContent)")
		}
		var r struct {
			Found   bool            `json:"found"`
			Tag     string          `json:"tag"`
			Present bool            `json:"present"`
			Value   json.RawMessage `json:"value"`
		}
		if err := callInto(ctx, p, "getAttribute", []any{sel, name}, &r); err != nil {
			return "", err
		}
		if !r.Found {
			return "", notFound()
		}
		if !r.Present {
			return fmt.Sprintf("%s = (not present) on <%s>", name, r.Tag), nil
		}
		return fmt.Sprintf("%s = %s", name, compactJSON(r.Value)), nil

	case "list_elements":
		var els []struct {
			Idx      int    `json:"idx"`
			Tag      string `json:"tag"`
			Role     string `json:"role"`
			Type     string `json:"type"`
			Label    string `json:"label"`
			X        int    `json:"x"`
			Y        int    `json:"y"`
			Disabled bool   `json:"disabled"`
		}
		if err := callInto(ctx, p, "listElementsDefault", []any{intp(params, "max", defaultQueryMax)}, &els); err != nil {
			return "", err
		}
		if len(els) == 0 {
			return "(no visible interactive elements found)", nil
		}
		lines := make([]string, len(els))
		for i, e := range els {
			kind := e.Tag
			if e.Role != "" {
				kind = fmt.Sprintf("%s[%s]", e.Tag, e.Role)
			}
			typeStr, disabledStr := "", ""
			if e.Type != "" {
				typeStr = " type=" + e.Type
			}
			if e.Disabled {
				disabledStr = " [disabled]"
			}
			lines[i] = fmt.Sprintf("%d | %s%s | \"%s\" | selector=[%s=\"%d\"] | viewport(%d,%d)%s",
				e.Idx, kind, typeStr, e.Label, aglinkIDAttr, e.Idx, e.X, e.Y, disabledStr)
		}
		return strings.Join(lines, "\n"), nil

	case "wait_for_element":
		if sel == "" {
			return "", errors.New("wait_for_element requires 'selector'")
		}
		timeoutMs := intp(params, "timeoutMs", defaultWaitTimeoutMs)
		deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
		for {
			var r struct {
				Found   bool   `json:"found"`
				Visible bool   `json:"visible"`
				Tag     string `json:"tag"`
			}
			if err := callInto(ctx, p, "waitForElement", []any{sel}, &r); err != nil {
				return "", err
			}
			if r.Found && r.Visible {
				return fmt.Sprintf("ok: found <%s> matching %s", r.Tag, sel), nil
			}
			if !time.Now().Before(deadline) {
				return "", fmt.Errorf("timed out after %dms waiting for a visible element matching %s", timeoutMs, sel)
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(waitForElementPoll):
			}
		}

	case "type":
		text, hasText := params["text"]
		if sel == "" {
			return "", errors.New("type requires 'selector'")
		}
		if !hasText || text == nil {
			return "", errors.New("type requires 'text'")
		}
		var r tagText
		if err := callInto(ctx, p, "typeText", []any{sel, fmt.Sprint(text)}, &r); err != nil {
			return "", err
		}
		if !r.Found {
			return "", notFound()
		}
		return fmt.Sprintf("ok: typed into <%s>", r.Tag), nil

	case "get_value":
		if sel == "" {
			return "", errors.New("get_value requires 'selector'")
		}
		var r struct {
			Found bool            `json:"found"`
			Value json.RawMessage `json:"value"`
		}
		if err := callInto(ctx, p, "getValue", []any{sel}, &r); err != nil {
			return "", err
		}
		if !r.Found {
			return "", notFound()
		}
		return fmt.Sprintf("%s = %s", sel, compactJSON(r.Value)), nil

	case "key":
		combo := str(params, "combo")
		if combo == "" {
			return "", errors.New("key requires 'combo'")
		}
		var r struct {
			OK    bool   `json:"ok"`
			Tag   string `json:"tag"`
			Error string `json:"error"`
		}
		if err := callInto(ctx, p, "keyComboDefault", []any{combo}, &r); err != nil {
			return "", err
		}
		if !r.OK {
			if r.Error != "" {
				return "", errors.New(r.Error)
			}
			return "", errors.New("key failed")
		}
		return fmt.Sprintf("ok: pressed \"%s\" on <%s>", combo, r.Tag), nil

	case "scroll":
		dx, dy := intp(params, "dx", 0), intp(params, "dy", 0)
		if dx == 0 && dy == 0 {
			return "", errors.New("scroll requires a non-zero dx or dy")
		}
		var r struct {
			Found bool `json:"found"`
		}
		if err := callInto(ctx, p, "scroll", []any{nullable(sel), dx, dy}, &r); err != nil {
			return "", err
		}
		if !r.Found {
			return "", notFound()
		}
		on := ""
		if sel != "" {
			on = " on " + sel
		}
		return fmt.Sprintf("ok: scrolled dx=%d dy=%d%s", dx, dy, on), nil

	case "select_option":
		value, hasValue := params["value"]
		label, hasLabel := params["label"]
		if sel == "" {
			return "", errors.New("select_option requires 'selector'")
		}
		if !hasValue && !hasLabel {
			return "", errors.New("select_option requires 'value' or 'label'")
		}
		var v, l any
		if hasValue {
			v = value
		}
		if hasLabel {
			l = label
		}
		var r struct {
			Found    bool   `json:"found"`
			IsSelect *bool  `json:"isSelect"`
			Tag      string `json:"tag"`
			Matched  bool   `json:"matched"`
			Selected string `json:"selected"`
		}
		if err := callInto(ctx, p, "selectOption", []any{sel, v, l}, &r); err != nil {
			return "", err
		}
		if !r.Found {
			return "", notFound()
		}
		if r.IsSelect != nil && !*r.IsSelect {
			return "", fmt.Errorf("element <%s> matched by %s is not a <select>", r.Tag, sel)
		}
		if !r.Matched {
			return "", fmt.Errorf("no <option> matching value=%s label=%s", jsAny(v), jsAny(l))
		}
		return fmt.Sprintf("ok: selected \"%s\"", r.Selected), nil

	case "eval":
		expr := str(params, "expression")
		if expr == "" {
			return "", errors.New("eval requires 'expression'")
		}
		raw, err := p.CallPage(ctx, "evalExpression", []any{expr})
		if err != nil {
			return "", err
		}
		var r struct {
			OK    bool   `json:"ok"`
			JSON  string `json:"json"`
			Error string `json:"error"`
		}
		if string(raw) == "null" || json.Unmarshal(raw, &r) != nil {
			return "", errors.New("eval returned no result (the page may block script injection)")
		}
		if !r.OK {
			return "", fmt.Errorf("eval error: %s", r.Error)
		}
		return r.JSON, nil

	case "dialog_status":
		d := p.Dialog()
		if d == nil {
			return "no dialog open", nil
		}
		s := fmt.Sprintf("%s: %s", d.Type, jsQuote(d.Message))
		if d.Type == "prompt" && d.DefaultPrompt != "" {
			s += fmt.Sprintf(" (default %s)", jsQuote(d.DefaultPrompt))
		}
		return s, nil

	case "handle_dialog":
		d := p.Dialog()
		if d == nil {
			return "", errors.New("no dialog open")
		}
		accept := true
		if a := strings.ToLower(str(params, "accept")); a == "false" || a == "no" || a == "0" {
			accept = false
		}
		if err := p.HandleDialog(ctx, accept, str(params, "prompt_text")); err != nil {
			return "", err
		}
		verb := "accepted"
		if !accept {
			verb = "dismissed"
		}
		return fmt.Sprintf("ok: %s %s %s", verb, d.Type, jsQuote(d.Message)), nil

	case "close_tab", "reload_extension", "get_console_logs", "get_network_requests":
		return "", fmt.Errorf("not supported for app profiles: %s", method)
	}
	return "", fmt.Errorf("unknown method: %s", method)
}

// tagText is the {found, tag, text} shape several page functions return.
type tagText struct {
	Found bool   `json:"found"`
	Tag   string `json:"tag"`
	Text  string `json:"text"`
}

func (r tagText) suffix() string {
	if r.Text == "" {
		return ""
	}
	return " " + jsQuote(r.Text)
}

// callInto runs a page function and decodes its result. A null result reads
// as the zero value, which every caller treats as "not found" — the same as
// background.js's `!r || !r.found`.
func callInto(ctx context.Context, p appPage, name string, args []any, v any) error {
	raw, err := p.CallPage(ctx, name, args)
	if err != nil {
		return err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// formatPageText is background.js getPageText's windowing, cursor and tail
// logic. Lengths and offsets are UTF-16 code units, as in JavaScript, so a
// cursor means the same thing on either kind of target.
func formatPageText(full string, params map[string]any) string {
	maxChars := intp(params, "maxChars", defaultMaxChars)
	total := jsLen(full)
	cursor := intp(params, "cursor", 0)
	offset := intp(params, "offset", 0)

	if cursor > 0 {
		if cursor <= total {
			delta := jsSlice(full, cursor, total)
			n := jsLen(delta)
			note := fmt.Sprintf("new:%d", n)
			if n > maxChars {
				delta = jsSlice(delta, n-maxChars, n)
				note += fmt.Sprintf(" truncated-to-last:%d", maxChars)
			}
			body := delta
			if body == "" {
				body = "(no new text since cursor)"
			}
			return fmt.Sprintf("%s\n[cursor:%d %s]", body, total, note)
		}
		t := full
		if total > maxChars {
			t = jsSlice(full, total-maxChars, total)
		}
		return fmt.Sprintf("%s\n[cursor:%d reset (content shrank; showing last %d)]", t, total, jsLen(t))
	}

	var text, span string
	if offset < 0 {
		n := -offset
		if n > maxChars {
			n = maxChars
		}
		start := total - n
		if start < 0 {
			start = 0
		}
		text = jsSlice(full, start, total)
		span = fmt.Sprintf("%d..%d", start, total)
	} else {
		start := offset
		if start > total {
			start = total
		}
		end := start + maxChars
		if end > total {
			end = total
		}
		text = jsSlice(full, start, end)
		span = fmt.Sprintf("%d..%d", start, start+jsLen(text))
	}
	trunc := ""
	if jsLen(text) < total {
		trunc = fmt.Sprintf(" (%s of %d)", span, total)
	}
	return fmt.Sprintf("%s\n[cursor:%d%s]", text, total, trunc)
}

// ---- small helpers ----------------------------------------------------------

func jsLen(s string) int { return len(utf16.Encode([]rune(s))) }

// jsSlice is String.prototype.slice over UTF-16 units, clamped.
func jsSlice(s string, start, end int) string {
	u := utf16.Encode([]rune(s))
	if start < 0 {
		start = 0
	}
	if end > len(u) {
		end = len(u)
	}
	if start >= end {
		return ""
	}
	return string(utf16.Decode(u[start:end]))
}

// jsQuote is JSON.stringify for a string: unlike encoding/json's default it
// leaves < > & alone, so a button labelled "저장 <지금>" reads the same as it
// does from the extension.
func jsQuote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(b.String(), "\n")
}

// jsAny is JSON.stringify for a value that may be absent (null).
func jsAny(v any) string {
	if v == nil {
		return "null"
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimRight(b.String(), "\n")
}

// compactJSON re-renders a page-provided JSON value without HTML escaping.
func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	return jsAny(v)
}

func str(params map[string]any, key string) string {
	v, ok := params[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// intp reads an integer param that may arrive as int (MCP) or float64 (JSON
// over /call) or a numeric string, falling back to def when absent or zero —
// background.js's `params.x || def`.
func intp(params map[string]any, key string, def int) int {
	var n int
	switch v := params[key].(type) {
	case int:
		n = v
	case int64:
		n = int(v)
	case float64:
		n = int(v)
	case string:
		n, _ = strconv.Atoi(v)
	}
	if n == 0 {
		return def
	}
	return n
}

// nullable turns an empty selector into JSON null, as background.js passes
// `params.selector || null`.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
