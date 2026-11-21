package main

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// The Chrome extension's connection comes and goes: the MV3 worker restarts,
// Chrome starts after the agent, the daemon restarts. A call that lands in such
// a gap waits for the extension to come back instead of failing at once.

// notConnectedError is a resolve failure that a reconnect can cure, as opposed
// to an ambiguous profile name, which waiting never fixes.
type notConnectedError string

func (e notConnectedError) Error() string { return string(e) }

// reconnectBudget is how long a call may wait for the extension: the caller's
// AGLINK_WEB_RECONNECT_WAIT_MS when set, else the daemon's default.
func (d *Daemon) reconnectBudget() time.Duration {
	if v := strings.TrimSpace(webGetenv(reconnectWaitEnv)); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms >= 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return d.reconnectWait
}

// Hints appended when a wait for a connection runs out: by then a reconnect on
// its own is clearly not coming, so say what to look at.
const (
	chromeWaitHint = "check that Chrome is running and the aglink-web extension is enabled (chrome://extensions)"
	vscodeWaitHint = "check that the VS Code window is open and the aglink-vscode extension is enabled"
)

// waitConn runs pick (under d.mu) until it yields a connection, waking on every
// registration (d.connected — no polling), for at most budget. Only a
// notConnectedError is waited on; the last one is returned with how long it
// waited and hint.
func (d *Daemon) waitConn(pick func() (*extConn, error), budget time.Duration, hint string) (*extConn, error) {
	deadline := time.Now().Add(budget)
	d.mu.Lock()
	for {
		ec, err := pick()
		var nc notConnectedError
		if err == nil || !errors.As(err, &nc) {
			d.mu.Unlock()
			return ec, err
		}
		left := time.Until(deadline)
		if left <= 0 {
			d.mu.Unlock()
			if budget > 0 {
				return nil, fmt.Errorf("%v (waited %gs for it to reconnect, still not connected — %s)", err, budget.Seconds(), hint)
			}
			return nil, err
		}
		ch := d.connected
		d.mu.Unlock()
		t := time.NewTimer(left)
		select {
		case <-ch:
		case <-t.C:
		}
		t.Stop()
		d.mu.Lock()
	}
}

// maxResends bounds how many times one call follows a flapping connection.
const maxResends = 3

// sendChrome sends one command to ec. If the connection drops before the
// request was written, it waits for the same account to reconnect and sends it
// there. If it drops after, only a read-only method is sent again, once: the
// rest may already have acted on the page. Returns the connection that answered.
func (d *Daemon) sendChrome(ec *extConn, method string, params map[string]any, budget time.Duration) (*extConn, CallResult) {
	resentAfterDrop := false
	deadline := time.Now().Add(budget) // one shared budget across resends, not one per attempt
	for attempt := 0; ; attempt++ {
		res, out := d.roundTripOutcome(ec, method, params, callTimeout, "browser")
		if out == tripDone {
			return ec, res
		}
		if out == tripLostAfterSend && (resentAfterDrop || !retryAfterDropMethods[method]) {
			res.Error += " after the command was sent — it may or may not have run; check the page before repeating it"
			return ec, res
		}
		left := time.Until(deadline)
		if attempt >= maxResends || budget <= 0 || left <= 0 {
			return ec, res
		}
		if out == tripLostAfterSend {
			resentAfterDrop = true
		}
		old := ec
		next, err := d.waitConn(func() (*extConn, error) {
			if c := d.exts[old.account]; c != nil && c != old && !c.gone {
				return c, nil
			}
			return nil, notConnectedError(fmt.Sprintf("%s: %s did not reconnect", res.Error, old.account))
		}, left, chromeWaitHint)
		if err != nil {
			return ec, CallResult{Error: err.Error()}
		}
		log.Printf("aglink-web: %s reconnected; sending %s again", old.account, method)
		ec = next
	}
}
