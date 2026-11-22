package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	EvalResult(ctx context.Context, expr string) (*evalResult, error)
	Raw(ctx context.Context, method string, params any) (json.RawMessage, error)
	Dialog() *dialogInfo
	HandleDialog(ctx context.Context, accept bool, promptText string) error
	Screenshot(ctx context.Context) (string, error)
	ConsoleText(max int) string
	NetworkIdle() (pending []string, count int, idleFor time.Duration)
	NetworkText(ctx context.Context, max int, filter string) string
}

// Mirrors of background.js constants.
const (
	defaultMaxChars       = 20000
	defaultQueryMax       = 200
	defaultWaitTimeoutMs  = 8000
	waitForElementPoll    = 150 * time.Millisecond
	aglinkIDAttr          = "data-aglink-id"
	navigateLoadPollDelay = 100 * time.Millisecond
)

// navigateLoadTimeout is how long navigate waits for an app window to load. A
// variable so tests can shorten it.
var navigateLoadTimeout = 10 * time.Second

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
	notFound := func() error { return missErr(sel, "") }

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
		// A host the user listed as trusted-despite-its-certificate loads
		// without the warning, as the extension does for Chrome.
		if insecureAllowed(urlHost(url)) {
			if _, err := p.Raw(ctx, "Security.setIgnoreCertificateErrors", map[string]any{"ignore": true}); err != nil {
				return "", fmt.Errorf("ignore certificate errors for %s: %w", urlHost(url), err)
			}
		}
		if _, err := p.Raw(ctx, "Page.navigate", map[string]any{"url": url}); err != nil {
			return "", err
		}
		title, href, loaded, err := waitLoadedState(ctx, p)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("ok: navigated — %s — %s%s", title, href, stillLoading(loaded)), nil

	case "proceed_insecure":
		// WebView2 and Electron put up their own error page for a bad
		// certificate. Ignoring certificate errors lasts as long as this CDP
		// connection, which the daemon keeps open for the window.
		if _, err := p.Raw(ctx, "Security.setIgnoreCertificateErrors", map[string]any{"ignore": true}); err != nil {
			return "", err
		}
		if _, err := p.Raw(ctx, "Page.reload", map[string]any{"ignoreCache": true}); err != nil {
			return "", err
		}
		title, href, err := waitLoaded(ctx, p)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("ok: certificate errors are ignored in this window while aglink-web stays attached; reloaded — %s — %s", title, href), nil

	case "screenshot":
		if str(params, "fullPage") == "true" {
			if sel != "" {
				return "", errors.New("screenshot: fullPage is not combinable with selector — capture the element on its own, or the full page, not both")
			}
			return appFullPageScreenshot(ctx, p)
		}
		if sel == "" {
			return p.Screenshot(ctx)
		}
		// rectOf's box: found, x, y, width, height, vw, vh, sx, sy (CSS px).
		var box map[string]any
		if err := callInto(ctx, p, "rectOf", []any{sel}, &box); err != nil {
			return "", err
		}
		if found, _ := box["found"].(bool); !found {
			hint, _ := box["hint"].(string)
			return "", missErr(sel, hint)
		}
		n := func(k string) float64 { v, _ := box[k].(float64); return v }
		// Clip to the part inside the viewport, then to page coordinates,
		// which is what Page.captureScreenshot's clip is in.
		x0, y0 := math.Max(0, n("x")), math.Max(0, n("y"))
		x1, y1 := math.Min(n("vw"), n("x")+n("width")), math.Min(n("vh"), n("y")+n("height"))
		if n("width") <= 0 || n("height") <= 0 {
			return "", fmt.Errorf("element matching %s is not visible (zero size)", sel)
		}
		if x1 <= x0 || y1 <= y0 {
			return "", errors.New("the element is outside the visible viewport")
		}
		shot, err := p.Raw(ctx, "Page.captureScreenshot", map[string]any{
			"format": "png",
			"clip":   map[string]any{"x": x0 + n("sx"), "y": y0 + n("sy"), "width": x1 - x0, "height": y1 - y0, "scale": 1},
		})
		if err != nil {
			return "", err
		}
		var img struct {
			Data string `json:"data"`
		}
		if err := json.Unmarshal(shot, &img); err != nil || img.Data == "" {
			return "", errors.New("screenshot: no image data")
		}
		return img.Data, nil

	case "cookies":
		return appCookies(ctx, p, params)

	case "print_pdf":
		raw, err := p.Raw(ctx, "Page.printToPDF", map[string]any{})
		if err != nil {
			return "", err
		}
		var pdf struct {
			Data string `json:"data"`
		}
		if err := json.Unmarshal(raw, &pdf); err != nil || pdf.Data == "" {
			return "", errors.New("print_pdf: no PDF data")
		}
		return pdf.Data, nil

	case "get_page_text":
		var r struct {
			Found   bool   `json:"found"`
			Text    string `json:"text"`
			Hint    string `json:"hint"`
			XFrames []struct {
				Src string `json:"src"`
			} `json:"xframes"`
		}
		// {"frames": true} asks the page for cross-origin iframes it cannot
		// read itself (xframes) when reading the whole page — mirrors
		// background.js's getPageText, which passes the same option so Chrome
		// profiles get this. Only meaningful without a selector (xframes is
		// about the whole document).
		opts := map[string]any{}
		if sel == "" {
			opts["frames"] = true
		}
		if err := callInto(ctx, p, "getPageText", []any{nullable(sel), opts}, &r); err != nil {
			return "", err
		}
		if sel != "" && !r.Found {
			return "", missErr(sel, r.Hint)
		}
		text := r.Text
		if sel == "" && len(r.XFrames) > 0 {
			srcs := make([]string, len(r.XFrames))
			for i, xf := range r.XFrames {
				srcs[i] = xf.Src
			}
			text += appCrossFrameText(ctx, p, srcs)
		}
		return formatPageText(text, params), nil

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
		if err := callInto(ctx, p, "click", []any{sel, button, actionOpts(params)}, &r); err != nil {
			return "", err
		}
		if err := r.err(sel); err != nil {
			return "", err
		}
		return r.noted(fmt.Sprintf("ok: %s-clicked <%s>%s", button, r.Tag, r.suffix())), nil

	case "hover", "double_click":
		if sel == "" {
			return "", fmt.Errorf("%s requires 'selector'", method)
		}
		fn, verb, args := "hover", "hovered", []any{sel}
		if method == "double_click" {
			fn, verb, args = "doubleClick", "double-clicked", []any{sel, actionOpts(params)}
		}
		var r tagText
		if err := callInto(ctx, p, fn, args, &r); err != nil {
			return "", err
		}
		if err := r.err(sel); err != nil {
			return "", err
		}
		return r.noted(fmt.Sprintf("ok: %s <%s>%s", verb, r.Tag, r.suffix())), nil

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
			Hint           string
		}
		raw, err := p.CallPage(ctx, "drag", []any{sel, target})
		if err != nil {
			return "", err
		}
		if string(raw) == "null" || json.Unmarshal(raw, &r) != nil {
			return "", errors.New("drag failed")
		}
		if !r.Found {
			return "", fmt.Errorf("no element matched %s selector: %s%s", r.Which, r.Sel, hintSuffix(r.Hint))
		}
		return fmt.Sprintf("ok: dragged <%s> onto <%s>", r.SrcTag, r.DstTag), nil

	case "get_html":
		var r struct {
			Found bool   `json:"found"`
			HTML  string `json:"html"`
			Hint  string `json:"hint"`
		}
		if err := callInto(ctx, p, "getHtml", []any{nullable(sel)}, &r); err != nil {
			return "", err
		}
		if !r.Found {
			return "", missErr(sel, r.Hint)
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
		max := intp(params, "max", defaultQueryMax)
		if fields := str(params, "fields"); fields != "" {
			var r extractResult
			if err := callInto(ctx, p, "extract", []any{sel, fields, max}, &r); err != nil {
				return "", err
			}
			return r.format(sel)
		}
		raw, err := p.CallPage(ctx, "queryAll", []any{sel, attrs, max})
		if err != nil {
			return "", err
		}
		// A selector matching only tables comes back as JSON rows.
		var tables struct {
			Lines []string `json:"lines"`
		}
		if json.Unmarshal(raw, &tables) == nil && tables.Lines != nil {
			return strings.Join(tables.Lines, "\n"), nil
		}
		var els []struct {
			Tag   string     `json:"tag"`
			Text  string     `json:"text"`
			Attrs [][]string `json:"attrs"`
		}
		if err := json.Unmarshal(raw, &els); err != nil {
			return "", fmt.Errorf("query_all: unexpected result: %w", err)
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
			Hint    string          `json:"hint"`
			Note    string          `json:"note"`
		}
		if err := callInto(ctx, p, "getAttribute", []any{sel, name}, &r); err != nil {
			return "", err
		}
		if !r.Found {
			return "", missErr(sel, r.Hint)
		}
		if !r.Present {
			return withNote(fmt.Sprintf("%s = (not present) on <%s>", name, r.Tag), r.Note), nil
		}
		return withNote(fmt.Sprintf("%s = %s", name, compactJSON(r.Value)), r.Note), nil

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
		gone := isTrue(fmt.Sprint(params["gone"]))
		idleMs := intp(params, "idleMs", 0)
		if sel == "" && idleMs <= 0 {
			return "", errors.New("wait_for_element requires 'selector' (or 'idleMs' to wait for the network to go quiet)")
		}
		timeoutMs := intp(params, "timeoutMs", defaultWaitTimeoutMs)
		deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
		for {
			elOk, tag := true, ""
			if sel != "" {
				var r struct {
					Found   bool   `json:"found"`
					Visible bool   `json:"visible"`
					Tag     string `json:"tag"`
				}
				if err := callInto(ctx, p, "waitForElement", []any{sel}, &r); err != nil {
					return "", err
				}
				shown := r.Found && r.Visible
				elOk = shown != gone
				tag = r.Tag
			}
			netOk := true
			var pending []string
			var count int
			var idle time.Duration
			if elOk && idleMs > 0 {
				pending, count, idle = p.NetworkIdle()
				netOk = count == 0 && idle >= time.Duration(idleMs)*time.Millisecond
			}
			if elOk && netOk {
				var parts []string
				if sel != "" {
					if gone {
						parts = append(parts, "no visible element matches "+sel)
					} else {
						parts = append(parts, fmt.Sprintf("found <%s> matching %s", tag, sel))
					}
				}
				if idleMs > 0 {
					parts = append(parts, fmt.Sprintf("network idle for %dms", idleMs))
				}
				return "ok: " + strings.Join(parts, "; "), nil
			}
			if !time.Now().Before(deadline) {
				switch {
				case !elOk && gone:
					return "", fmt.Errorf("timed out after %dms: an element matching %s is still visible", timeoutMs, sel)
				case !elOk:
					return "", fmt.Errorf("timed out after %dms waiting for a visible element matching %s", timeoutMs, sel)
				case count > 0:
					return "", fmt.Errorf("timed out after %dms waiting for the network to be idle for %dms — %d request(s) still pending: %s", timeoutMs, idleMs, count, strings.Join(pending, ", "))
				default:
					return "", fmt.Errorf("timed out after %dms waiting for the network to be idle for %dms — the last request ended %dms ago", timeoutMs, idleMs, idle.Milliseconds())
				}
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
		mode := strings.ToLower(str(params, "mode"))
		if mode == "" {
			mode = "set"
		}
		if mode != "set" && mode != "append" && mode != "keys" {
			return "", fmt.Errorf("type: unknown mode %s (want set, append or keys)", jsQuote(str(params, "mode")))
		}
		if mode == "keys" {
			return appTypeKeys(ctx, p, sel, fmt.Sprint(text))
		}
		var r tagText
		if err := callInto(ctx, p, "typeText", []any{sel, fmt.Sprint(text), mode}, &r); err != nil {
			return "", err
		}
		if err := r.err(sel); err != nil {
			return "", err
		}
		verb, via := "typed into", ""
		if mode == "append" {
			verb = "appended to"
		}
		if r.Via == "textContent" {
			via = " (the editor refused insertText; set its text directly)"
		}
		return r.noted(fmt.Sprintf("ok: %s <%s>%s", verb, r.Tag, via)), nil

	case "get_value":
		if sel == "" {
			return "", errors.New("get_value requires 'selector'")
		}
		var r struct {
			Found bool            `json:"found"`
			Value json.RawMessage `json:"value"`
			Hint  string          `json:"hint"`
			Note  string          `json:"note"`
		}
		if err := callInto(ctx, p, "getValue", []any{sel}, &r); err != nil {
			return "", err
		}
		if !r.Found {
			return "", missErr(sel, r.Hint)
		}
		return withNote(fmt.Sprintf("%s = %s", sel, compactJSON(r.Value)), r.Note), nil

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
			Found bool   `json:"found"`
			Hint  string `json:"hint"`
		}
		if err := callInto(ctx, p, "scroll", []any{nullable(sel), dx, dy}, &r); err != nil {
			return "", err
		}
		if !r.Found {
			return "", missErr(sel, r.Hint)
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
			Hint     string `json:"hint"`
			Note     string `json:"note"`
		}
		if err := callInto(ctx, p, "selectOption", []any{sel, v, l}, &r); err != nil {
			return "", err
		}
		if !r.Found {
			return "", missErr(sel, r.Hint)
		}
		if r.IsSelect != nil && !*r.IsSelect {
			return "", fmt.Errorf("element <%s> matched by %s is not a <select>", r.Tag, sel)
		}
		if !r.Matched {
			return "", fmt.Errorf("no <option> matching value=%s label=%s", jsAny(v), jsAny(l))
		}
		return withNote(fmt.Sprintf("ok: selected \"%s\"", r.Selected), r.Note), nil

	case "eval":
		expr := str(params, "expression")
		if expr == "" {
			return "", errors.New("eval requires 'expression'")
		}
		// Straight through Runtime.evaluate rather than the shared page
		// function: that one calls eval(), which a Content-Security-Policy
		// without 'unsafe-eval' — common in Electron apps — refuses, while
		// DevTools evaluation is exempt from it.
		r, err := p.EvalResult(ctx, expr)
		if err != nil {
			if strings.HasPrefix(err.Error(), "page error: ") {
				return "", fmt.Errorf("eval error: %s", strings.TrimPrefix(err.Error(), "page error: "))
			}
			return "", err
		}
		return formatEvalValue(r), nil

	case "page_snapshot":
		raw, err := p.CallPage(ctx, "snapshot", []any{intp(params, "maxLines", 200)})
		if err != nil {
			return "", err
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", errors.New("page_snapshot returned no result")
		}
		return s, nil

	case "upload_file":
		paths := toStrings(params["paths"])
		if sel == "" || len(paths) == 0 {
			return "", errors.New("upload_file requires 'selector' and 'paths'")
		}
		// Through the shared page scripts first, so the resolver understands
		// role=/text=/label= selectors here as everywhere else.
		if _, err := p.CallPage(ctx, "elementExists", []any{sel}); err != nil {
			return "", err
		}
		raw, err := p.Raw(ctx, "Runtime.evaluate", map[string]any{"expression": fileInputExpr(sel), "returnByValue": false})
		if err != nil {
			return "", err
		}
		var ev struct {
			Result struct {
				ObjectID string `json:"objectId"`
				Subtype  string `json:"subtype"`
			} `json:"result"`
		}
		_ = json.Unmarshal(raw, &ev)
		if ev.Result.ObjectID == "" || ev.Result.Subtype == "null" {
			return "", notFound()
		}
		chk, err := p.Raw(ctx, "Runtime.callFunctionOn", map[string]any{
			"objectId":            ev.Result.ObjectID,
			"functionDeclaration": `function(){ return [this.tagName === "INPUT" && this.type === "file", !!this.multiple, this.tagName.toLowerCase()]; }`,
			"returnByValue":       true,
		})
		if err != nil {
			return "", err
		}
		var c struct {
			Result struct {
				Value []any `json:"value"`
			} `json:"result"`
		}
		_ = json.Unmarshal(chk, &c)
		if len(c.Result.Value) < 3 || c.Result.Value[0] != true {
			tag := "?"
			if len(c.Result.Value) >= 3 {
				tag, _ = c.Result.Value[2].(string)
			}
			return "", fmt.Errorf("element <%s> matched by %s is not an <input type=\"file\"> — target the file input itself (often hidden), e.g. input[type=file]", tag, sel)
		}
		if len(paths) > 1 && c.Result.Value[1] != true {
			return "", fmt.Errorf("this file input takes one file, %d given", len(paths))
		}
		if _, err := p.Raw(ctx, "DOM.setFileInputFiles", map[string]any{"files": paths, "objectId": ev.Result.ObjectID}); err != nil {
			return "", err
		}
		return uploadedText(paths), nil

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

	case "get_console_logs":
		return p.ConsoleText(intp(params, "max", 50)), nil

	case "get_network_requests":
		return p.NetworkText(ctx, intp(params, "max", 50), str(params, "filter")), nil

	case "close_tab", "reload_extension":
		return "", fmt.Errorf("not supported for app profiles: %s", method)
	case "wait_download":
		return "", errors.New("wait_download is not supported for app profiles — Electron/Wails apps handle downloads their own way, not through a generic CDP hook; use eval to check the app's own download state/folder instead")
	}
	return "", fmt.Errorf("unknown method: %s", method)
}

// extractResult is page-actions.js P.extract's result; format mirrors
// background.js formatExtract.
type extractResult struct {
	Lines []string `json:"lines"`
	Total int      `json:"total"`
	Error string   `json:"error"`
}

func (r extractResult) format(sel string) (string, error) {
	if r.Error != "" {
		return "", errors.New(r.Error)
	}
	if len(r.Lines) == 0 {
		return fmt.Sprintf("(no elements matched selector: %s)", sel), nil
	}
	out := strings.Join(r.Lines, "\n")
	if r.Total > len(r.Lines) {
		out += fmt.Sprintf("\n… [%d more matched — raise max]", r.Total-len(r.Lines))
	}
	return out, nil
}

// waitLoaded polls until the window's document is complete (or the load
// timeout passes) and returns its title and URL.
func waitLoaded(ctx context.Context, p appPage) (title, href string, err error) {
	title, href, _, err = waitLoadedState(ctx, p)
	return title, href, err
}

// waitLoadedState is waitLoaded that also says whether the load finished
// (false: the timeout passed with the document still loading).
func waitLoadedState(ctx context.Context, p appPage) (title, href string, loaded bool, err error) {
	deadline := time.Now().Add(navigateLoadTimeout)
	for time.Now().Before(deadline) {
		raw, err := p.Eval(ctx, "document.readyState")
		if err == nil && string(raw) == `"complete"` {
			loaded = true
			break
		}
		time.Sleep(navigateLoadPollDelay)
	}
	raw, err := p.Eval(ctx, "[document.title, location.href]")
	if err != nil {
		return "", "", false, err
	}
	var tu [2]string
	_ = json.Unmarshal(raw, &tu)
	return tu[0], tu[1], loaded, nil
}

// stillLoading is what navigate appends when the page had not finished
// loading by the timeout, as the extension does.
func stillLoading(loaded bool) string {
	if loaded {
		return ""
	}
	return fmt.Sprintf(" (still loading after %ds)", int(navigateLoadTimeout/time.Second))
}

// appCrossFrameText reads the text of an app window's cross-origin iframes —
// the ones P.getPageText (page-actions.js) could see but not read itself,
// listed by src in xframes. A content script can reach a cross-origin frame
// by frameId (background.js's crossFrameText); an app window is not a content
// script, so this resolves each src to a frameId via Page.getFrameTree
// instead, then reads it through a CDP isolated world (Page.createIsolatedWorld
// + Runtime.evaluate with that contextId) — DevTools reaching into the frame
// from outside, not page script reaching across origins. One level deep only
// (a cross-origin frame's own nested cross-origin frames are not followed);
// background.js goes two deep, but this covers the common case — an embedded
// login/payment widget — without the extra isolated-world bookkeeping nesting
// would need.
func appCrossFrameText(ctx context.Context, p appPage, srcs []string) string {
	if len(srcs) == 0 {
		return ""
	}
	raw, err := p.Raw(ctx, "Page.getFrameTree", nil)
	if err != nil {
		return "" // best effort: same-origin text already returned above
	}
	type frameNode struct {
		Frame struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"frame"`
		ChildFrames []frameNode `json:"childFrames"`
	}
	var tree struct {
		FrameTree frameNode `json:"frameTree"`
	}
	if err := json.Unmarshal(raw, &tree); err != nil {
		return ""
	}
	var all []frameNode
	var walk func(n frameNode)
	walk = func(n frameNode) {
		all = append(all, n)
		for _, c := range n.ChildFrames {
			walk(c)
		}
	}
	walk(tree.FrameTree)

	var out strings.Builder
	for _, src := range srcs[:min(len(srcs), 10)] {
		var frameID string
		for _, n := range all {
			if n.Frame.URL == src || strings.HasPrefix(n.Frame.URL, src) || strings.HasPrefix(src, n.Frame.URL) {
				frameID = n.Frame.ID
				break
			}
		}
		if frameID == "" {
			continue
		}
		worldRaw, err := p.Raw(ctx, "Page.createIsolatedWorld", map[string]any{
			"frameId": frameID, "worldName": "aglink-xframe", "grantUniveralAccess": true,
		})
		if err != nil {
			continue
		}
		var world struct {
			ExecutionContextID int `json:"executionContextId"`
		}
		if json.Unmarshal(worldRaw, &world) != nil || world.ExecutionContextID == 0 {
			continue
		}
		evalRaw, err := p.Raw(ctx, "Runtime.evaluate", map[string]any{
			"expression":    "document.body ? document.body.innerText : ''",
			"contextId":     world.ExecutionContextID,
			"returnByValue": true,
			"awaitPromise":  true,
		})
		if err != nil {
			continue
		}
		var r evalResult
		if json.Unmarshal(evalRaw, &r) != nil || r.ExceptionDetails != nil {
			continue
		}
		var text string
		_ = json.Unmarshal(r.Result.Value, &text)
		if strings.TrimSpace(text) != "" {
			fmt.Fprintf(&out, "\n\n[iframe %s]\n%s", src, text)
		}
	}
	return out.String()
}

// appCookie is one cookie as CDP's Network.getCookies/setCookie shape it.
// Mirrored in background.js's formatCookies (chrome.cookies uses the same
// field names already, so no translation is needed there).
type appCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Secure   bool    `json:"secure"`
	HTTPOnly bool    `json:"httpOnly"`
	Expires  float64 `json:"expires"` // Unix seconds; -1 means session cookie
}

// formatCookies renders a cookie list the same way for both profiles.
func formatCookies(url string, cookies []appCookie) string {
	if len(cookies) == 0 {
		return fmt.Sprintf("(no cookies visible to %s)", url)
	}
	var b strings.Builder
	for i, c := range cookies {
		if i > 0 {
			b.WriteByte('\n')
		}
		exp := "session"
		if c.Expires > 0 {
			exp = time.Unix(int64(c.Expires), 0).UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(&b, "%s=%s | domain=%s | path=%s | secure=%v | httpOnly=%v | expires=%s", c.Name, c.Value, c.Domain, c.Path, c.Secure, c.HTTPOnly, exp)
	}
	return b.String()
}

// appCookies is the "cookies" command for an app window, via CDP's Network
// domain (Network.getCookies/setCookie/deleteCookies — these work without
// Network.enable, unlike request/response inspection).
func appCookies(ctx context.Context, p appPage, params map[string]any) (string, error) {
	url := str(params, "url")
	if url == "" {
		return "", errors.New("cookies requires 'url'")
	}
	action := strings.ToLower(str(params, "action"))
	if action == "" {
		action = "get"
	}
	name := str(params, "name")
	switch action {
	case "get":
		raw, err := p.Raw(ctx, "Network.getCookies", map[string]any{"urls": []string{url}})
		if err != nil {
			return "", err
		}
		var r struct {
			Cookies []appCookie `json:"cookies"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return "", err
		}
		cookies := r.Cookies
		if name != "" {
			filtered := cookies[:0]
			for _, c := range r.Cookies {
				if c.Name == name {
					filtered = append(filtered, c)
				}
			}
			cookies = filtered
		}
		return formatCookies(url, cookies), nil

	case "set":
		if name == "" {
			return "", errors.New("cookies action=set requires 'name'")
		}
		value := str(params, "value")
		if value == "" {
			return "", errors.New("cookies action=set requires 'value'")
		}
		p2 := map[string]any{"url": url, "name": name, "value": value}
		if d := str(params, "domain"); d != "" {
			p2["domain"] = d
		}
		if pth := str(params, "path"); pth != "" {
			p2["path"] = pth
		}
		if str(params, "secure") == "true" {
			p2["secure"] = true
		}
		if str(params, "httpOnly") == "true" {
			p2["httpOnly"] = true
		}
		if exp := intp(params, "expires", 0); exp != 0 {
			p2["expires"] = exp
		}
		raw, err := p.Raw(ctx, "Network.setCookie", p2)
		if err != nil {
			return "", err
		}
		var r struct {
			Success bool `json:"success"`
		}
		_ = json.Unmarshal(raw, &r)
		if !r.Success {
			return "", fmt.Errorf("cookies: setting %q for %s was rejected (check domain/path/secure match the URL)", name, url)
		}
		return fmt.Sprintf("ok: set cookie %s for %s", name, url), nil

	case "delete":
		if name == "" {
			return "", errors.New("cookies action=delete requires 'name'")
		}
		if _, err := p.Raw(ctx, "Network.deleteCookies", map[string]any{"url": url, "name": name}); err != nil {
			return "", err
		}
		return fmt.Sprintf("ok: deleted cookie %s for %s", name, url), nil

	default:
		return "", fmt.Errorf(`cookies: unknown action %q (want "get", "set", or "delete")`, action)
	}
}

// appFullPageScreenshot captures the entire scrollable page, not just the
// viewport: Page.getLayoutMetrics' cssContentSize is the full document size,
// and captureBeyondViewport only has an effect when paired with an explicit
// clip of that full size (passing captureBeyondViewport alone still crops to
// the viewport).
func appFullPageScreenshot(ctx context.Context, p appPage) (string, error) {
	raw, err := p.Raw(ctx, "Page.getLayoutMetrics", nil)
	if err != nil {
		return "", err
	}
	var metrics struct {
		CSSContentSize struct {
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
		} `json:"cssContentSize"`
	}
	if err := json.Unmarshal(raw, &metrics); err != nil {
		return "", err
	}
	if metrics.CSSContentSize.Width <= 0 || metrics.CSSContentSize.Height <= 0 {
		return "", errors.New("screenshot: could not measure the full page size")
	}
	shot, err := p.Raw(ctx, "Page.captureScreenshot", map[string]any{
		"format":              "png",
		"captureBeyondViewport": true,
		"clip": map[string]any{
			"x": 0, "y": 0,
			"width": metrics.CSSContentSize.Width, "height": metrics.CSSContentSize.Height,
			"scale": 1,
		},
	})
	if err != nil {
		return "", err
	}
	var img struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(shot, &img); err != nil || img.Data == "" {
		return "", errors.New("screenshot: no image data")
	}
	return img.Data, nil
}

// appTypeKeys is type mode "keys" in an app window: focus the element (caret at
// the end), then type through Input.insertText — Enter between lines — so
// the page sees real input. Mirrors background.js typeKeys.
func appTypeKeys(ctx context.Context, p appPage, sel, text string) (string, error) {
	var r tagText
	if err := callInto(ctx, p, "focusForKeys", []any{sel}, &r); err != nil {
		return "", err
	}
	if err := r.err(sel); err != nil {
		return "", err
	}
	enter := map[string]any{"key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13, "nativeVirtualKeyCode": 13}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		if i > 0 {
			down := map[string]any{"type": "keyDown", "text": "\r", "unmodifiedText": "\r"}
			up := map[string]any{"type": "keyUp"}
			for k, v := range enter {
				down[k], up[k] = v, v
			}
			if _, err := p.Raw(ctx, "Input.dispatchKeyEvent", down); err != nil {
				return "", err
			}
			if _, err := p.Raw(ctx, "Input.dispatchKeyEvent", up); err != nil {
				return "", err
			}
		}
		if line != "" {
			if _, err := p.Raw(ctx, "Input.insertText", map[string]any{"text": line}); err != nil {
				return "", err
			}
		}
	}
	return r.noted(fmt.Sprintf("ok: typed %d chars as keystrokes into <%s>", jsLen(text), r.Tag)), nil
}

// formatEvalValue renders an evaluation the way the extension's eval does:
// "undefined", JSON.stringify(value, null, 2), or — for what JSON cannot
// carry, like a function — its string form.
func formatEvalValue(r *evalResult) string {
	if r.Result.Type == "undefined" {
		return "undefined"
	}
	if len(r.Result.Value) > 0 {
		var b bytes.Buffer
		if json.Indent(&b, r.Result.Value, "", "  ") == nil {
			return b.String()
		}
		return string(r.Result.Value)
	}
	return r.Result.Description
}

// tagText is the {found, tag, text} shape several page functions return,
// with what the page adds: a hint when nothing matched, a note when several
// did, and why a click could not happen (page-actions.js actionable).
type tagText struct {
	Found   bool   `json:"found"`
	Tag     string `json:"tag"`
	Text    string `json:"text"`
	Hint    string `json:"hint"`
	Note    string `json:"note"`
	Blocked string `json:"blocked"`
	Via     string `json:"via"`
}

// err is the error a not-found or blocked result means, or nil.
func (r tagText) err(sel string) error {
	if !r.Found {
		return missErr(sel, r.Hint)
	}
	if r.Blocked != "" {
		return errors.New(r.Blocked)
	}
	return nil
}

func (r tagText) noted(text string) string { return withNote(text, r.Note) }

// missErr is "no element matched selector", with the page's hint at what
// nearly matched — background.js notFound.
func missErr(sel, hint string) error {
	return fmt.Errorf("no element matched selector: %s%s", sel, hintSuffix(hint))
}

func hintSuffix(hint string) string {
	if hint == "" {
		return ""
	}
	return " — " + hint
}

// withNote appends a page result's ambiguity note — background.js withNote.
func withNote(text, note string) string {
	if note == "" {
		return text
	}
	return text + "\nnote: " + note
}

// actionOpts is the {timeout, force} click and double-click take — see
// background.js actionOpts.
func actionOpts(params map[string]any) map[string]any {
	o := map[string]any{}
	if t := intp(params, "timeout", 0); t > 0 {
		o["timeout"] = t
	}
	switch v := params["force"].(type) {
	case bool:
		if v {
			o["force"] = true
		}
	case string:
		if isTrue(v) {
			o["force"] = true
		}
	}
	return o
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
