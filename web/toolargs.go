package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Argument parsing for the filters and settings of get_console_logs,
// get_network_requests, storage and emulate. The daemon checks them once
// before a call goes anywhere (prepareToolArgs), so a typo fails with the same
// message in Chrome and in an app window; the app path then matches with the
// parsed form below, and the extension with its own short copy of the same
// rules (background.js consoleLevelMatch / netStatusMatch).

// prepareToolArgs validates method's filter/setting args and returns params
// with them in canonical form (a copy when anything changed).
func prepareToolArgs(method string, params map[string]any) (map[string]any, error) {
	switch method {
	case "get_console_logs":
		levels, err := parseConsoleLevels(str(params, "level"))
		if err != nil || levels == nil {
			return params, err
		}
		return withParam(params, "level", strings.Join(sortedKeys(levels), ",")), nil
	case "get_network_requests":
		if _, err := parseNetQuery(params); err != nil {
			return params, err
		}
	case "storage":
		if _, err := parseStorageArgs(params); err != nil {
			return params, err
		}
	case "emulate":
		if _, err := buildEmulatePlan(params); err != nil {
			return params, err
		}
	}
	return params, nil
}

func withParam(params map[string]any, key string, v any) map[string]any {
	p := make(map[string]any, len(params)+1)
	for k, x := range params {
		p[k] = x
	}
	p[key] = v
	return p
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// splitList splits a comma (or space) separated argument into trimmed,
// lowercased, non-empty items.
func splitList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '|' }) {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// ---- get_console_logs ----------------------------------------------------------

// consoleLevels are the levels the captures record. Chrome's page capture
// files an uncaught exception as "uncaught" and a rejected promise as
// "unhandledrejection"; level=error takes them in too, as it does in an app
// window, where both are recorded as "error".
var consoleLevels = map[string]bool{"error": true, "warn": true, "info": true, "log": true, "debug": true, "uncaught": true, "unhandledrejection": true}

// parseConsoleLevels parses level= into a set; nil means every level.
func parseConsoleLevels(s string) (map[string]bool, error) {
	items := splitList(s)
	if len(items) == 0 {
		return nil, nil
	}
	set := map[string]bool{}
	for _, l := range items {
		if l == "warning" {
			l = "warn"
		}
		if !consoleLevels[l] {
			return nil, fmt.Errorf("get_console_logs: unknown level %q — use error, warn, info, log or debug (comma-separated, e.g. \"error,warn\")", l)
		}
		set[l] = true
		if l == "error" {
			set["uncaught"], set["unhandledrejection"] = true, true
		}
	}
	return set, nil
}

// consoleQuery is what get_console_logs selects.
type consoleQuery struct {
	Max    int
	Levels map[string]bool // nil: all
	Filter string          // lowercased substring of the message
}

func parseConsoleQuery(params map[string]any) (consoleQuery, error) {
	levels, err := parseConsoleLevels(str(params, "level"))
	if err != nil {
		return consoleQuery{}, err
	}
	return consoleQuery{Max: intp(params, "max", 50), Levels: levels, Filter: strings.ToLower(str(params, "filter"))}, nil
}

func (q consoleQuery) match(level, text string) bool {
	if q.Levels != nil && !q.Levels[level] {
		return false
	}
	return q.Filter == "" || strings.Contains(strings.ToLower(text), q.Filter)
}

// filtered says whether anything narrows the query, for the empty message.
func (q consoleQuery) filtered() bool { return q.Levels != nil || q.Filter != "" }

// ---- get_network_requests ------------------------------------------------------

// statusRange is one status= item: an inclusive range of HTTP status codes,
// or a transport failure / an unanswered request.
type statusRange struct {
	lo, hi  int
	failed  bool
	pending bool
}

// netQuery is what get_network_requests selects.
type netQuery struct {
	Max     int
	Filter  string // lowercased URL substring
	Methods map[string]bool
	Status  []statusRange
	ID      string // detail of one request instead of the list
}

// parseNetStatus parses status=: "200", "4xx", "400-499", "failed" (no
// response: blocked, offline, aborted), "pending", comma-separated.
func parseNetStatus(s string) ([]statusRange, error) {
	var out []statusRange
	bad := func(item string) error {
		return fmt.Errorf("get_network_requests: bad status %q — use a code (404), a class (4xx), a range (400-499), \"failed\" or \"pending\", comma-separated", item)
	}
	for _, it := range splitList(s) {
		switch {
		case it == "failed" || it == "error" || it == "failure":
			out = append(out, statusRange{failed: true})
		case it == "pending":
			out = append(out, statusRange{pending: true})
		case len(it) == 3 && it[0] >= '1' && it[0] <= '5' && it[1:] == "xx":
			c := int(it[0]-'0') * 100
			out = append(out, statusRange{lo: c, hi: c + 99})
		case strings.Contains(it, "-"):
			a, b, _ := strings.Cut(it, "-")
			lo, err1 := strconv.Atoi(a)
			hi, err2 := strconv.Atoi(b)
			if err1 != nil || err2 != nil || lo < 100 || hi > 599 || lo > hi {
				return nil, bad(it)
			}
			out = append(out, statusRange{lo: lo, hi: hi})
		default:
			n, err := strconv.Atoi(it)
			if err != nil || n < 100 || n > 599 {
				return nil, bad(it)
			}
			out = append(out, statusRange{lo: n, hi: n})
		}
	}
	return out, nil
}

func parseNetQuery(params map[string]any) (netQuery, error) {
	q := netQuery{Max: intp(params, "max", 50), Filter: strings.ToLower(str(params, "filter")), ID: strings.TrimSpace(str(params, "id"))}
	var err error
	if q.Status, err = parseNetStatus(str(params, "status")); err != nil {
		return q, err
	}
	for _, m := range splitList(str(params, "method")) {
		if q.Methods == nil {
			q.Methods = map[string]bool{}
		}
		q.Methods[strings.ToUpper(m)] = true
	}
	return q, nil
}

// match says whether a request passes the filters. status 0 with no error is
// a request still waiting for its response.
func (q netQuery) match(method, url string, status int, failed bool) bool {
	if q.Filter != "" && !strings.Contains(strings.ToLower(url), q.Filter) {
		return false
	}
	if q.Methods != nil && !q.Methods[strings.ToUpper(method)] {
		return false
	}
	if len(q.Status) == 0 {
		return true
	}
	for _, r := range q.Status {
		switch {
		case r.failed:
			if failed {
				return true
			}
		case r.pending:
			if !failed && status == 0 {
				return true
			}
		default:
			if !failed && status >= r.lo && status <= r.hi {
				return true
			}
		}
	}
	return false
}

func (q netQuery) filtered() bool { return q.Filter != "" || q.Methods != nil || len(q.Status) > 0 }

// ---- storage -------------------------------------------------------------------

type storageArgs struct {
	Action, Area, Key, Value string
}

func parseStorageArgs(params map[string]any) (storageArgs, error) {
	a := storageArgs{
		Action: strings.ToLower(strings.TrimSpace(str(params, "action"))),
		Area:   strings.ToLower(strings.TrimSpace(str(params, "area"))),
		Key:    str(params, "key"),
		Value:  str(params, "value"),
	}
	if a.Action == "" {
		a.Action = "get"
	}
	switch a.Area {
	case "", "local", "localstorage":
		a.Area = "local"
	case "session", "sessionstorage":
		a.Area = "session"
	default:
		return a, fmt.Errorf("storage: area must be \"local\" or \"session\", not %q", a.Area)
	}
	switch a.Action {
	case "get", "clear":
	case "set":
		if a.Key == "" {
			return a, errors.New("storage: action=set requires 'key' and 'value'")
		}
		if _, ok := params["value"]; !ok {
			return a, errors.New("storage: action=set requires 'value'")
		}
	case "delete", "remove":
		a.Action = "delete"
		if a.Key == "" {
			return a, errors.New("storage: action=delete requires 'key'")
		}
	default:
		return a, fmt.Errorf("storage: action must be get, set, delete or clear, not %q", a.Action)
	}
	return a, nil
}

// ---- emulate -------------------------------------------------------------------

// devicePreset is one emulate device=: CSS-pixel viewport, device pixel
// ratio, whether it is a phone/tablet (mobile viewport + touch) and the user
// agent it sends.
type devicePreset struct {
	width, height int
	scale         float64
	mobile        bool
	ua            string
}

const (
	uaIPhone  = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"
	uaIPad    = "Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"
	uaAndroid = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36"
	uaGalaxy  = "Mozilla/5.0 (Linux; Android 14; SM-S921B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36"
)

// devicePresets are emulate's device= names. Desktop presets keep the
// browser's own user agent.
var devicePresets = map[string]devicePreset{
	"iphone-se":         {375, 667, 2, true, uaIPhone},
	"iphone-15":         {393, 852, 3, true, uaIPhone},
	"iphone-15-pro-max": {430, 932, 3, true, uaIPhone},
	"pixel-8":           {412, 915, 2.625, true, uaAndroid},
	"galaxy-s24":        {360, 780, 3, true, uaGalaxy},
	"ipad":              {820, 1180, 2, true, uaIPad},
	"ipad-pro":          {1024, 1366, 2, true, uaIPad},
	"laptop":            {1366, 768, 1, false, ""},
	"desktop":           {1920, 1080, 1, false, ""},
}

func presetNames() string {
	names := make([]string, 0, len(devicePresets))
	for n := range devicePresets {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// netCondition is a Network.emulateNetworkConditions setting; the 3G figures
// are Chrome DevTools' own presets.
type netCondition struct {
	offline   bool
	latencyMs float64
	down, up  float64 // bytes/s; -1 = unthrottled
	label     string
}

var netConditions = map[string]netCondition{
	"offline": {true, 0, -1, -1, "offline"},
	"slow-3g": {false, 2000, 50000, 50000, "slow 3G (2000ms latency, 50 KB/s)"},
	"fast-3g": {false, 562.5, 180000, 84375, "fast 3G (563ms latency, 180 KB/s down)"},
	"none":    {false, 0, -1, -1, "no network throttling"},
}

// cdpCmd is one DevTools command an emulate call sends.
type cdpCmd struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

// emulatePlan is what an emulate call does: commands to send in order, the
// media features to change (merged by the executor with those already set,
// since Emulation.setEmulatedMedia replaces the whole list), and a summary.
type emulatePlan struct {
	Cmds    []cdpCmd
	Media   map[string]string // feature → value; "" removes it
	Reset   bool
	Summary []string
}

// resetCmds undo every setting emulate makes.
func resetCmds() []cdpCmd {
	return []cdpCmd{
		{"Emulation.clearDeviceMetricsOverride", map[string]any{}},
		{"Emulation.setTouchEmulationEnabled", map[string]any{"enabled": false}},
		{"Emulation.setUserAgentOverride", map[string]any{"userAgent": ""}},
		{"Emulation.clearGeolocationOverride", map[string]any{}},
		{"Network.emulateNetworkConditions", map[string]any{"offline": false, "latency": 0, "downloadThroughput": -1, "uploadThroughput": -1}},
	}
}

// buildEmulatePlan turns emulate's arguments into the commands that apply
// them. Viewport: device= preset and/or width+height (+scale, mobile);
// device="none" clears it.
func buildEmulatePlan(params map[string]any) (emulatePlan, error) {
	var p emulatePlan
	has := func(k string) bool { return strings.TrimSpace(str(params, k)) != "" }
	if isTrue(str(params, "reset")) {
		for _, k := range []string{"device", "width", "height", "scale", "mobile", "colorScheme", "reducedMotion", "network", "geolocation", "userAgent"} {
			if has(k) {
				return p, fmt.Errorf("emulate: reset=\"true\" clears everything and cannot be combined with %s — call emulate again afterwards for new settings", k)
			}
		}
		p.Reset = true
		p.Cmds = resetCmds()
		p.Media = map[string]string{"prefers-color-scheme": "", "prefers-reduced-motion": ""}
		p.Summary = []string{"reset"}
		return p, nil
	}

	// Viewport.
	device := strings.ToLower(strings.TrimSpace(str(params, "device")))
	width, height := intp(params, "width", 0), intp(params, "height", 0)
	var vp devicePreset
	vpSet := false
	switch device {
	case "":
	case "none", "default", "off":
		if width != 0 || height != 0 {
			return p, errors.New("emulate: device=\"none\" clears the viewport and cannot be combined with width/height")
		}
		p.Cmds = append(p.Cmds,
			cdpCmd{"Emulation.clearDeviceMetricsOverride", map[string]any{}},
			cdpCmd{"Emulation.setTouchEmulationEnabled", map[string]any{"enabled": false}})
		p.Summary = append(p.Summary, "default viewport")
	default:
		d, ok := devicePresets[device]
		if !ok {
			return p, fmt.Errorf("emulate: unknown device %q — use one of %s, or width/height", device, presetNames())
		}
		vp, vpSet = d, true
	}
	if width != 0 || height != 0 {
		if !vpSet && (width == 0 || height == 0) {
			return p, errors.New("emulate: give both width and height (CSS px), or a device preset")
		}
		if width < 0 || height < 0 || width > 10000 || height > 10000 {
			return p, errors.New("emulate: width and height must be between 1 and 10000 CSS px")
		}
		if !vpSet {
			vp = devicePreset{scale: 0}
		}
		if width != 0 {
			vp.width = width
		}
		if height != 0 {
			vp.height = height
		}
		vpSet = true
	}
	if s := strings.TrimSpace(str(params, "scale")); s != "" {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || f < 0.25 || f > 5 {
			return p, fmt.Errorf("emulate: scale must be a device pixel ratio between 0.25 and 5, not %q", s)
		}
		if !vpSet {
			return p, errors.New("emulate: scale needs a viewport — give a device or width/height too")
		}
		vp.scale = f
	}
	if m := strings.TrimSpace(str(params, "mobile")); m != "" {
		if !vpSet {
			return p, errors.New("emulate: mobile needs a viewport — give a device or width/height too")
		}
		vp.mobile = isTrue(m)
	}
	if vpSet {
		p.Cmds = append(p.Cmds,
			cdpCmd{"Emulation.setDeviceMetricsOverride", map[string]any{"width": vp.width, "height": vp.height, "deviceScaleFactor": vp.scale, "mobile": vp.mobile}},
			cdpCmd{"Emulation.setTouchEmulationEnabled", touchParams(vp.mobile)})
		s := fmt.Sprintf("viewport %dx%d", vp.width, vp.height)
		if vp.scale != 0 {
			s += " @" + strconv.FormatFloat(vp.scale, 'f', -1, 64) + "x"
		}
		if vp.mobile {
			s += " mobile+touch"
		}
		if device != "" {
			s += " (" + device + ")"
		}
		p.Summary = append(p.Summary, s)
	}

	// User agent: explicit, else the mobile preset's own.
	ua := strings.TrimSpace(str(params, "userAgent"))
	switch {
	case strings.EqualFold(ua, "none") || strings.EqualFold(ua, "default"):
		p.Cmds = append(p.Cmds, cdpCmd{"Emulation.setUserAgentOverride", map[string]any{"userAgent": ""}})
		p.Summary = append(p.Summary, "default user agent")
	case ua != "":
		p.Cmds = append(p.Cmds, cdpCmd{"Emulation.setUserAgentOverride", map[string]any{"userAgent": ua}})
		p.Summary = append(p.Summary, "user agent "+jsQuote(truncateRunes(ua, 40)))
	case vpSet && vp.ua != "":
		p.Cmds = append(p.Cmds, cdpCmd{"Emulation.setUserAgentOverride", map[string]any{"userAgent": vp.ua}})
		p.Summary = append(p.Summary, device+" user agent")
	}

	// Media features.
	if cs := strings.ToLower(strings.TrimSpace(str(params, "colorScheme"))); cs != "" {
		switch cs {
		case "light", "dark":
		case "none", "default", "no-preference":
			cs = ""
		default:
			return p, fmt.Errorf("emulate: colorScheme must be light, dark or none, not %q", cs)
		}
		p.setMedia("prefers-color-scheme", cs)
	}
	if rm := strings.ToLower(strings.TrimSpace(str(params, "reducedMotion"))); rm != "" {
		switch rm {
		case "reduce", "true", "yes", "on":
			rm = "reduce"
		case "no-preference":
		case "none", "default", "false", "no", "off":
			rm = ""
		default:
			return p, fmt.Errorf("emulate: reducedMotion must be reduce, no-preference or none, not %q", rm)
		}
		p.setMedia("prefers-reduced-motion", rm)
	}

	// Network.
	if n := strings.ToLower(strings.TrimSpace(str(params, "network"))); n != "" {
		switch n {
		case "online", "no-throttling", "off", "default":
			n = "none"
		case "slow3g", "3g":
			n = "slow-3g"
		case "fast3g":
			n = "fast-3g"
		}
		c, ok := netConditions[n]
		if !ok {
			return p, fmt.Errorf("emulate: network must be offline, slow-3g, fast-3g or none, not %q", n)
		}
		p.Cmds = append(p.Cmds,
			cdpCmd{"Network.enable", map[string]any{}},
			cdpCmd{"Network.emulateNetworkConditions", map[string]any{"offline": c.offline, "latency": c.latencyMs, "downloadThroughput": c.down, "uploadThroughput": c.up}})
		p.Summary = append(p.Summary, c.label)
	}

	// Geolocation.
	if g := strings.TrimSpace(str(params, "geolocation")); g != "" {
		if lg := strings.ToLower(g); lg == "none" || lg == "default" || lg == "off" {
			p.Cmds = append(p.Cmds, cdpCmd{"Emulation.clearGeolocationOverride", map[string]any{}})
			p.Summary = append(p.Summary, "real geolocation")
		} else {
			lat, lon, acc, err := parseGeo(g)
			if err != nil {
				return p, err
			}
			p.Cmds = append(p.Cmds, cdpCmd{"Emulation.setGeolocationOverride", map[string]any{"latitude": lat, "longitude": lon, "accuracy": acc}})
			p.Summary = append(p.Summary, fmt.Sprintf("geolocation %s,%s", strconv.FormatFloat(lat, 'f', -1, 64), strconv.FormatFloat(lon, 'f', -1, 64)))
		}
	}

	if len(p.Cmds) == 0 && len(p.Media) == 0 {
		return p, errors.New("emulate: nothing to emulate — give device, width/height, colorScheme, reducedMotion, network, geolocation or userAgent, or reset=\"true\"")
	}
	return p, nil
}

func touchParams(mobile bool) map[string]any {
	if mobile {
		return map[string]any{"enabled": true, "maxTouchPoints": 5}
	}
	return map[string]any{"enabled": false}
}

func (p *emulatePlan) setMedia(name, value string) {
	if p.Media == nil {
		p.Media = map[string]string{}
	}
	p.Media[name] = value
	if value == "" {
		p.Summary = append(p.Summary, "real "+name)
	} else {
		p.Summary = append(p.Summary, name+": "+value)
	}
}

// parseGeo parses "lat,lon" or "lat,lon,accuracy" (metres, default 50).
func parseGeo(s string) (lat, lon, acc float64, err error) {
	parts := strings.Split(s, ",")
	bad := fmt.Errorf("emulate: geolocation must be \"lat,lon\" (e.g. \"37.5665,126.9780\"), optionally \",accuracy\" in metres, or \"none\" — not %q", s)
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, 0, bad
	}
	vals := make([]float64, len(parts))
	for i, x := range parts {
		if vals[i], err = strconv.ParseFloat(strings.TrimSpace(x), 64); err != nil || math.IsNaN(vals[i]) {
			return 0, 0, 0, bad
		}
	}
	lat, lon, acc = vals[0], vals[1], 50
	if len(vals) == 3 {
		acc = vals[2]
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 || acc < 0 {
		return 0, 0, 0, bad
	}
	return lat, lon, acc, nil
}

// mediaFeatures merges a plan's media changes into the features already set
// and returns both the new state and Emulation.setEmulatedMedia's list.
func mediaFeatures(cur, delta map[string]string) (map[string]string, []map[string]string) {
	next := map[string]string{}
	for k, v := range cur {
		next[k] = v
	}
	for k, v := range delta {
		if v == "" {
			delete(next, k)
		} else {
			next[k] = v
		}
	}
	names := make([]string, 0, len(next))
	for k := range next {
		names = append(names, k)
	}
	sort.Strings(names)
	list := make([]map[string]string, 0, len(names))
	for _, k := range names {
		list = append(list, map[string]string{"name": k, "value": next[k]})
	}
	return next, list
}

// chromeEmulateParams hands the extension the plan instead of the raw
// arguments: it only sends the commands and merges the media features.
func chromeEmulateParams(params map[string]any) (map[string]any, error) {
	plan, err := buildEmulatePlan(params)
	if err != nil {
		return nil, err
	}
	p := map[string]any{"cmds": plan.Cmds, "media": plan.Media, "reset": plan.Reset, "summary": strings.Join(plan.Summary, ", ")}
	if t := intp(params, "tabId", 0); t != 0 {
		p["tabId"] = t
	}
	return p, nil
}
