package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseConsoleLevels(t *testing.T) {
	got, err := parseConsoleLevels("Error, warning")
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(sortedKeys(got), ","); s != "error,uncaught,unhandledrejection,warn" {
		t.Fatalf("levels: %s", s)
	}
	if got, err := parseConsoleLevels(""); got != nil || err != nil {
		t.Fatalf("empty means all: %v %v", got, err)
	}
	if _, err := parseConsoleLevels("log,verbose"); err == nil || !strings.Contains(err.Error(), `unknown level "verbose"`) {
		t.Fatalf("bad level: %v", err)
	}
	q := consoleQuery{Levels: got, Filter: "boom"}
	if !q.match("uncaught", "Boom at x.js") || q.match("log", "boom") || q.match("error", "fine") {
		t.Fatal("match")
	}
}

func TestParseNetStatus(t *testing.T) {
	for _, bad := range []string{"6xx", "abc", "99", "500-400", "4x"} {
		if _, err := parseNetStatus(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	q, err := parseNetQuery(map[string]any{"status": "2xx,404,failed", "method": "get, post", "filter": "/API"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		method, url string
		status      int
		failed      bool
		want        bool
	}{
		{"GET", "https://x/api/a", 204, false, true},
		{"post", "https://x/api/a", 404, false, true},
		{"GET", "https://x/api/a", 500, false, false},
		{"GET", "https://x/api/a", 0, true, true},
		{"PUT", "https://x/api/a", 200, false, false},
		{"GET", "https://x/static/a", 200, false, false},
		{"GET", "https://x/api/a", 0, false, false}, // pending is not asked for
	} {
		if got := q.match(c.method, c.url, c.status, c.failed); got != c.want {
			t.Errorf("%+v: got %v", c, got)
		}
	}
}

func TestParseStorageArgs(t *testing.T) {
	if a, err := parseStorageArgs(map[string]any{}); err != nil || a.Action != "get" || a.Area != "local" {
		t.Fatalf("defaults: %+v %v", a, err)
	}
	if a, err := parseStorageArgs(map[string]any{"action": "remove", "key": "k", "area": "sessionStorage"}); err != nil || a.Action != "delete" || a.Area != "session" {
		t.Fatalf("aliases: %+v %v", a, err)
	}
	for _, c := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{"action": "set", "key": "k"}, "requires 'value'"},
		{map[string]any{"action": "set", "value": "v"}, "requires 'key'"},
		{map[string]any{"action": "delete"}, "requires 'key'"},
		{map[string]any{"action": "wipe"}, "action must be"},
		{map[string]any{"area": "cookie"}, "area must be"},
	} {
		if _, err := parseStorageArgs(c.params); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.params, err)
		}
	}
}

func TestDevicePresetsAreSane(t *testing.T) {
	for _, name := range []string{"iphone-15", "pixel-8", "ipad", "desktop"} {
		if _, ok := devicePresets[name]; !ok {
			t.Errorf("preset %s missing", name)
		}
	}
	for name, d := range devicePresets {
		if d.width <= 0 || d.height <= 0 || d.scale <= 0 {
			t.Errorf("%s: bad metrics %+v", name, d)
		}
		if d.mobile != (d.ua != "") {
			t.Errorf("%s: a mobile preset brings its user agent, a desktop one keeps the browser's", name)
		}
		if strings.ToLower(name) != name {
			t.Errorf("%s: names are matched lowercased", name)
		}
	}
}

func cmdMethods(p emulatePlan) string {
	var m []string
	for _, c := range p.Cmds {
		m = append(m, c.Method)
	}
	return strings.Join(m, " ")
}

func TestBuildEmulatePlan(t *testing.T) {
	p, err := buildEmulatePlan(map[string]any{"device": "iPhone-15", "colorScheme": "dark", "network": "slow-3g", "geolocation": "37.5665, 126.978"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cmdMethods(p); got != "Emulation.setDeviceMetricsOverride Emulation.setTouchEmulationEnabled Emulation.setUserAgentOverride Network.enable Network.emulateNetworkConditions Emulation.setGeolocationOverride" {
		t.Fatalf("cmds: %s", got)
	}
	m := p.Cmds[0].Params
	if m["width"] != 393 || m["height"] != 852 || m["deviceScaleFactor"] != 3.0 || m["mobile"] != true {
		t.Fatalf("metrics: %v", m)
	}
	if ua, _ := p.Cmds[2].Params["userAgent"].(string); !strings.Contains(ua, "iPhone") {
		t.Fatalf("preset UA: %q", ua)
	}
	if p.Media["prefers-color-scheme"] != "dark" {
		t.Fatalf("media: %v", p.Media)
	}
	if s := strings.Join(p.Summary, ", "); s != "viewport 393x852 @3x mobile+touch (iphone-15), iphone-15 user agent, prefers-color-scheme: dark, slow 3G (2000ms latency, 50 KB/s), geolocation 37.5665,126.978" {
		t.Fatalf("summary: %s", s)
	}

	// Explicit size over a preset, own user agent, no touch on desktop.
	p, err = buildEmulatePlan(map[string]any{"device": "desktop", "width": 1280, "userAgent": "Bot/1"})
	if err != nil {
		t.Fatal(err)
	}
	if m := p.Cmds[0].Params; m["width"] != 1280 || m["height"] != 1080 || m["mobile"] != false {
		t.Fatalf("override: %v", m)
	}
	if p.Cmds[1].Params["enabled"] != false || p.Cmds[2].Params["userAgent"] != "Bot/1" {
		t.Fatalf("touch/ua: %v %v", p.Cmds[1].Params, p.Cmds[2].Params)
	}

	// Width/height alone, with a pixel ratio given as a string.
	p, err = buildEmulatePlan(map[string]any{"width": float64(500), "height": "700", "scale": "2.5", "mobile": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if m := p.Cmds[0].Params; m["width"] != 500 || m["height"] != 700 || m["deviceScaleFactor"] != 2.5 || m["mobile"] != true {
		t.Fatalf("explicit: %v", m)
	}

	// Undoing single settings.
	p, err = buildEmulatePlan(map[string]any{"device": "none", "reducedMotion": "none", "network": "online", "geolocation": "none", "userAgent": "none"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cmdMethods(p); got != "Emulation.clearDeviceMetricsOverride Emulation.setTouchEmulationEnabled Emulation.setUserAgentOverride Network.enable Network.emulateNetworkConditions Emulation.clearGeolocationOverride" {
		t.Fatalf("undo cmds: %s", got)
	}
	if v, ok := p.Media["prefers-reduced-motion"]; !ok || v != "" {
		t.Fatalf("media undo: %v", p.Media)
	}

	p, err = buildEmulatePlan(map[string]any{"reset": "true"})
	if err != nil || !p.Reset || len(p.Cmds) != len(resetCmds()) {
		t.Fatalf("reset: %+v %v", p, err)
	}
}

func TestBuildEmulatePlanRejects(t *testing.T) {
	for _, c := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{}, "nothing to emulate"},
		{map[string]any{"device": "nokia-3310"}, `unknown device "nokia-3310"`},
		{map[string]any{"width": 300}, "give both width and height"},
		{map[string]any{"width": 300, "height": 20000}, "between 1 and 10000"},
		{map[string]any{"scale": "2"}, "scale needs a viewport"},
		{map[string]any{"device": "ipad", "scale": "9"}, "scale must be"},
		{map[string]any{"mobile": "true"}, "mobile needs a viewport"},
		{map[string]any{"device": "none", "width": 300, "height": 300}, "cannot be combined with width/height"},
		{map[string]any{"colorScheme": "sepia"}, "colorScheme must be"},
		{map[string]any{"reducedMotion": "less"}, "reducedMotion must be"},
		{map[string]any{"network": "5g"}, "network must be"},
		{map[string]any{"geolocation": "37.5"}, "geolocation must be"},
		{map[string]any{"geolocation": "91,0"}, "geolocation must be"},
		{map[string]any{"reset": "true", "device": "ipad"}, "cannot be combined with device"},
	} {
		if _, err := buildEmulatePlan(c.params); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.params, err)
		}
	}
}

func TestMediaFeaturesMerge(t *testing.T) {
	cur, list := mediaFeatures(nil, map[string]string{"prefers-color-scheme": "dark"})
	cur, list = mediaFeatures(cur, map[string]string{"prefers-reduced-motion": "reduce"})
	if b, _ := json.Marshal(list); string(b) != `[{"name":"prefers-color-scheme","value":"dark"},{"name":"prefers-reduced-motion","value":"reduce"}]` {
		t.Fatalf("merged: %s", b)
	}
	_, list = mediaFeatures(cur, map[string]string{"prefers-color-scheme": ""})
	if b, _ := json.Marshal(list); string(b) != `[{"name":"prefers-reduced-motion","value":"reduce"}]` {
		t.Fatalf("removed: %s", b)
	}
}

func TestPrepareToolArgs(t *testing.T) {
	p, err := prepareToolArgs("get_console_logs", map[string]any{"level": "warning", "max": 5})
	if err != nil || p["level"] != "warn" || p["max"] != 5 {
		t.Fatalf("console: %v %v", p, err)
	}
	if _, err := prepareToolArgs("get_network_requests", map[string]any{"status": "7xx"}); err == nil {
		t.Fatal("bad status must fail before the call goes out")
	}
	if _, err := prepareToolArgs("emulate", map[string]any{"device": "?"}); err == nil {
		t.Fatal("bad device must fail before the call goes out")
	}
	cp, err := chromeEmulateParams(map[string]any{"device": "pixel-8", "tabId": 4})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(cp)
	if !strings.Contains(string(b), `"method":"Emulation.setDeviceMetricsOverride"`) || cp["tabId"] != 4 || cp["reset"] != false {
		t.Fatalf("chrome params: %s", b)
	}
}
