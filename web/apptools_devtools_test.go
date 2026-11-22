package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAppGetStyles(t *testing.T) {
	f := &fakePage{results: map[string]string{
		"getStyles": `{"found":true,"tag":"button","text":"button.primary — box x=10 y=20 100x30 (viewport 800x600)\ndisplay: none"}`,
	}}
	r := run(t, f, "get_styles", map[string]any{"selector": "role=button", "properties": "display"})
	if r.Text != "button.primary — box x=10 y=20 100x30 (viewport 800x600)\ndisplay: none" {
		t.Fatalf("got %+v", r)
	}
	if f.calls[0] != `getStyles["role=button","display",null]` {
		t.Fatalf("call: %s", f.calls[0])
	}
	f.results["getStyles"] = `{"found":false,"hint":"similar elements: button \"Save\""}`
	if r := run(t, f, "get_styles", map[string]any{"selector": "#nope"}); !strings.HasPrefix(r.Error, "no element matched selector: #nope — similar") {
		t.Fatalf("miss: %+v", r)
	}
	if r := run(t, f, "get_styles", map[string]any{}); r.Error != "get_styles requires 'selector'" {
		t.Fatalf("no selector: %+v", r)
	}
}

func TestAppStorage(t *testing.T) {
	f := &fakePage{results: map[string]string{"storage": `{"ok":true,"text":"ok: set localStorage[\"k\"] (1 chars) on http://wails.localhost"}`}}
	r := run(t, f, "storage", map[string]any{"action": "set", "key": "k", "value": "v"})
	if r.Text != `ok: set localStorage["k"] (1 chars) on http://wails.localhost` {
		t.Fatalf("got %+v", r)
	}
	if f.calls[0] != `storage["set","local","k","v",20000]` {
		t.Fatalf("call: %s", f.calls[0])
	}
	f.results["storage"] = `{"ok":false,"error":"localStorage is not available on this page"}`
	if r := run(t, f, "storage", map[string]any{"area": "local"}); r.Error != "localStorage is not available on this page" {
		t.Fatalf("page error: %+v", r)
	}
	if r := run(t, f, "storage", map[string]any{"action": "delete"}); !strings.Contains(r.Error, "requires 'key'") {
		t.Fatalf("validation: %+v", r)
	}
}

func TestAppEmulate(t *testing.T) {
	f := &fakePage{}
	r := run(t, f, "emulate", map[string]any{"device": "pixel-8", "colorScheme": "dark"})
	if !strings.HasPrefix(r.Text, "ok: emulating viewport 412x915 @2.625x mobile+touch (pixel-8), pixel-8 user agent, prefers-color-scheme: dark in window 1") {
		t.Fatalf("got %+v", r)
	}
	if got := strings.Join(f.raws, " "); got != "Emulation.setDeviceMetricsOverride Emulation.setTouchEmulationEnabled Emulation.setUserAgentOverride Emulation.setEmulatedMedia" {
		t.Fatalf("cdp: %s", got)
	}
	// A later call keeps the colour scheme while adding reduced motion.
	f.raws = nil
	run(t, f, "emulate", map[string]any{"reducedMotion": "reduce"})
	b, _ := json.Marshal(f.rawParams["Emulation.setEmulatedMedia"])
	if string(b) != `{"features":[{"name":"prefers-color-scheme","value":"dark"},{"name":"prefers-reduced-motion","value":"reduce"}],"media":""}` {
		t.Fatalf("media merge: %s", b)
	}
	r = run(t, f, "emulate", map[string]any{"reset": "true"})
	if !strings.HasPrefix(r.Text, "ok: emulation cleared") {
		t.Fatalf("reset: %+v", r)
	}
	b, _ = json.Marshal(f.rawParams["Emulation.setEmulatedMedia"])
	if string(b) != `{"features":[],"media":""}` {
		t.Fatalf("reset media: %s", b)
	}
	if r := run(t, f, "emulate", map[string]any{"network": "4g"}); !strings.Contains(r.Error, "network must be") {
		t.Fatalf("bad: %+v", r)
	}
}

func TestAppSnapshotWithPageLine(t *testing.T) {
	f := &fakePage{results: map[string]string{"snapshot": `"[snapshot 1 lines, 0 refs]\n- text \"hi\""`}}
	r := run(t, f, "page_snapshot", map[string]any{"withPage": true, "tabId": 2})
	tab, _, body, ok := splitSnapPage(r.Text)
	if !ok || tab != "2" || body != "[snapshot 1 lines, 0 refs]\n- text \"hi\"" {
		t.Fatalf("got %q", r.Text)
	}
	if r := run(t, f, "page_snapshot", nil); strings.HasPrefix(r.Text, snapPageMarker) {
		t.Fatalf("no page line unless asked: %q", r.Text)
	}
}
