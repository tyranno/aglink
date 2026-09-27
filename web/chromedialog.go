package main

import (
	"strconv"
	"strings"
	"time"
)

// dialogNeedsKeys is how the extension's handle_dialog error begins when the
// debugger could not answer the dialog (DIALOG_NEEDS_KEYS in background.js).
const dialogNeedsKeys = "dialog needs the keyboard"

// pressKeys is chromeKeys, swapped out by tests.
var pressKeys = chromeKeys

// handleDialogChrome is handle_dialog for a Chrome profile. The extension
// answers through the debugger; if it cannot, the daemon answers the way a
// person does — brings the tab to the front and presses Enter (OK, after
// typing any prompt text) or Esc (Cancel) — then checks the dialog is gone
// before saying so.
func (d *Daemon) handleDialogChrome(ec *extConn, params map[string]any) CallResult {
	res := d.roundTrip(ec, "handle_dialog", params, callTimeout, "browser")
	if res.OK || !strings.Contains(res.Error, dialogNeedsKeys) || !osKeysSupported {
		return res
	}
	tabID := intp(params, "tabId", 0)
	if tabID == 0 {
		if m := stuckTabRE.FindStringSubmatch(res.Error); m != nil {
			tabID, _ = strconv.Atoi(m[1])
		}
	}
	if tabID == 0 {
		return res
	}
	act := d.roundTrip(ec, "activate_tab", map[string]any{"tabId": tabID}, callTimeout, "browser")
	if !act.OK {
		res.Error += " | bringing the tab to the front to answer it: " + act.Error
		return res
	}
	title := ""
	if m := activatedRE.FindStringSubmatch(act.Text); m != nil {
		title = m[1]
	}
	accept := true
	if a := strings.ToLower(str(params, "accept")); a == "false" || a == "no" || a == "0" {
		accept = false
	}
	text, key, verb := "", uint16(vkEscape), "dismissed"
	if accept {
		text, key, verb = str(params, "prompt_text"), vkReturn, "accepted"
	}
	time.Sleep(300 * time.Millisecond) // let the window come forward
	if err := pressKeys(title, text, key); err != nil {
		res.Error += " | answering it from the keyboard: " + err.Error()
		return res
	}
	time.Sleep(300 * time.Millisecond)
	st := d.roundTrip(ec, "dialog_status", map[string]any{"tabId": tabID}, callTimeout, "browser")
	if st.OK && st.Text == "no dialog open" {
		return CallResult{OK: true, Text: "ok: " + verb + " the dialog on tab " + strconv.Itoa(tabID) + " from the keyboard"}
	}
	res.Error += " | pressed the keys, but the dialog still reads: " + st.Text + st.Error
	return res
}
