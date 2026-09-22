package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// errDialogOpen means an alert/confirm/prompt is up in the app. While one is
// open the page's JavaScript is stopped, so nothing else can run until it is
// answered with handle_dialog.
var errDialogOpen = errors.New("dialog open")

// dialogInfo is the JavaScript dialog currently showing in a window.
type dialogInfo struct {
	Type          string `json:"type"` // alert | confirm | prompt | beforeunload
	Message       string `json:"message"`
	DefaultPrompt string `json:"defaultPrompt"`
	URL           string `json:"url"`
}

// appTarget is one Electron/Wails window the daemon is attached to over CDP.
// The connection stays open between tool calls, because a dialog is only ever
// announced as an event — a connection opened after the dialog appeared would
// never hear about it.
type appTarget struct {
	port   int
	target cdpTarget
	conn   *cdpConn

	mu     sync.Mutex
	dialog *dialogInfo
	// opened is closed the moment a dialog appears, waking every evaluation
	// that is waiting on the (now frozen) page. A fresh one replaces it when
	// the dialog closes.
	opened chan struct{}
}

func openAppTarget(t cdpTarget, port int) (*appTarget, error) {
	conn, err := dialCDP(t.WebSocketDebuggerURL)
	if err != nil {
		return nil, err
	}
	a := &appTarget{port: port, target: t, conn: conn, opened: make(chan struct{})}
	conn.OnEvent(a.onEvent)
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if _, err := conn.Call(ctx, "Page.enable", nil); err != nil {
		conn.Close()
		return nil, fmt.Errorf("enable page events: %w", err)
	}
	return a, nil
}

func (a *appTarget) onEvent(method string, params json.RawMessage) {
	switch method {
	case "Page.javascriptDialogOpening":
		var d dialogInfo
		_ = json.Unmarshal(params, &d)
		a.mu.Lock()
		if a.dialog == nil {
			close(a.opened)
		}
		a.dialog = &d
		a.mu.Unlock()
	case "Page.javascriptDialogClosed":
		a.mu.Lock()
		if a.dialog != nil {
			a.dialog = nil
			a.opened = make(chan struct{})
		}
		a.mu.Unlock()
	}
}

// Dialog returns the dialog showing now, or nil.
func (a *appTarget) Dialog() *dialogInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dialog == nil {
		return nil
	}
	d := *a.dialog
	return &d
}

// HandleDialog answers the open dialog. promptText is only used by prompt().
func (a *appTarget) HandleDialog(ctx context.Context, accept bool, promptText string) error {
	params := map[string]any{"accept": accept}
	if promptText != "" {
		params["promptText"] = promptText
	}
	_, err := a.conn.Call(ctx, "Page.handleJavaScriptDialog", params)
	return err
}

// Raw sends any CDP method on this window's connection.
func (a *appTarget) Raw(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return a.conn.Call(ctx, method, params)
}

type evalResult struct {
	Result struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	} `json:"result"`
	ExceptionDetails *struct {
		Text      string `json:"text"`
		Exception *struct {
			Description string `json:"description"`
		} `json:"exception"`
	} `json:"exceptionDetails"`
}

// Eval runs expr in the page and returns its JSON value. If a dialog appears
// before the page answers, it returns errDialogOpen at once instead of waiting
// out the call — the page cannot answer until the dialog is gone.
func (a *appTarget) Eval(ctx context.Context, expr string) (json.RawMessage, error) {
	a.mu.Lock()
	if a.dialog != nil {
		a.mu.Unlock()
		return nil, errDialogOpen
	}
	opened := a.opened
	a.mu.Unlock()

	type out struct {
		raw json.RawMessage
		err error
	}
	ch := make(chan out, 1) // buffered: the call may finish after we stopped listening
	go func() {
		raw, err := a.conn.Call(ctx, "Runtime.evaluate", map[string]any{
			"expression":    expr,
			"returnByValue": true,
			"awaitPromise":  true,
		})
		ch <- out{raw, err}
	}()

	select {
	case o := <-ch:
		if o.err != nil {
			return nil, o.err
		}
		var r evalResult
		if err := json.Unmarshal(o.raw, &r); err != nil {
			return nil, fmt.Errorf("bad evaluate result: %w", err)
		}
		if r.ExceptionDetails != nil {
			msg := r.ExceptionDetails.Text
			if r.ExceptionDetails.Exception != nil && r.ExceptionDetails.Exception.Description != "" {
				msg = r.ExceptionDetails.Exception.Description
			}
			return nil, fmt.Errorf("page error: %s", msg)
		}
		if len(r.Result.Value) == 0 {
			return json.RawMessage("null"), nil
		}
		return r.Result.Value, nil
	case <-opened:
		return nil, errDialogOpen
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// needInjectMarker is what the call-through expression returns when the page
// does not have the shared scripts yet — first use, or the app reloaded since.
const needInjectMarker = "__aglinkNeedInject"

// CallPage runs globalThis.__aglinkPage[name](...args) in the page. The check
// for the shared scripts is folded into the same round trip, so an already
// prepared page costs one evaluation; only a fresh page pays for injection.
func (a *appTarget) CallPage(ctx context.Context, name string, args []any) (json.RawMessage, error) {
	nameJSON, _ := json.Marshal(name)
	if args == nil {
		args = []any{}
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode args: %w", err)
	}
	expr := fmt.Sprintf(`(typeof globalThis.__aglinkPage === "undefined") ? {%q: true} : globalThis.__aglinkPage[%s](...%s)`,
		needInjectMarker, nameJSON, argsJSON)

	raw, err := a.Eval(ctx, expr)
	if err != nil || !isNeedInject(raw) {
		return raw, err
	}
	for _, script := range []string{injectJS, pageActionsJS} {
		if _, err := a.Eval(ctx, script); err != nil {
			return nil, fmt.Errorf("inject page scripts: %w", err)
		}
	}
	raw, err = a.Eval(ctx, expr)
	if err == nil && isNeedInject(raw) {
		return nil, fmt.Errorf("page scripts did not take effect in this window")
	}
	return raw, err
}

func isNeedInject(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	_, ok := m[needInjectMarker]
	return ok
}

// Screenshot captures the window's viewport as base64 PNG.
func (a *appTarget) Screenshot(ctx context.Context) (string, error) {
	raw, err := a.Raw(ctx, "Page.captureScreenshot", map[string]any{"format": "png"})
	if err != nil {
		return "", err
	}
	var r struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", err
	}
	return r.Data, nil
}

func (a *appTarget) Done() <-chan struct{} { return a.conn.Done() }

func (a *appTarget) Close() error { return a.conn.Close() }
