package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// An app target is an Electron or Wails window that opened a Chrome DevTools
// Protocol port. It is named on the tool's 'profile' argument as either
//
//	app:<slug of its title>   e.g. app:aglink — a unique prefix is enough
//	cdp:<port>                e.g. cdp:9333   — exact, survives title changes
//
// Nothing is configured: the daemon finds apps by knocking on a small range of
// loopback ports.

type cdpTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type appInfo struct {
	Port  int
	Title string // the first page's title
	URL   string
	Pages []cdpTarget
}

func (a appInfo) slug() string { return appSlug(a.Title) }

// probeTimeout keeps discovery cheap: every port is knocked on concurrently, so
// a listing costs at most this long however many ports are in range.
const probeTimeout = 300 * time.Millisecond

// cdpPorts reads AGLINK_WEB_CDP_PORTS ("9222-9240", "9333", or a comma list of
// either), defaulting to 9222–9240.
func cdpPorts() []int {
	spec := strings.TrimSpace(os.Getenv("AGLINK_WEB_CDP_PORTS"))
	if spec == "" {
		spec = "9222-9240"
	}
	var out []int
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			a, err1 := strconv.Atoi(strings.TrimSpace(lo))
			b, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 == nil && err2 == nil && a <= b && b-a < 1000 {
				for p := a; p <= b; p++ {
					out = append(out, p)
				}
			}
			continue
		}
		if p, err := strconv.Atoi(part); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// discoverApps knocks on every port concurrently and returns the ones that
// answer as a CDP endpoint with at least one page, sorted by port.
func discoverApps(ctx context.Context, ports []int) []appInfo {
	client := &http.Client{Timeout: probeTimeout}
	var mu sync.Mutex
	var out []appInfo
	var wg sync.WaitGroup
	for _, p := range ports {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			base := fmt.Sprintf("http://127.0.0.1:%d", port)
			if !getOK(ctx, client, base+"/json/version") {
				return
			}
			var targets []cdpTarget
			if !getJSON(ctx, client, base+"/json/list", &targets) {
				return
			}
			app := appInfo{Port: port}
			for _, t := range targets {
				if t.Type == "page" && t.WebSocketDebuggerURL != "" {
					app.Pages = append(app.Pages, t)
				}
			}
			if len(app.Pages) == 0 {
				return
			}
			app.Title, app.URL = app.Pages[0].Title, app.Pages[0].URL
			mu.Lock()
			out = append(out, app)
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

func getOK(ctx context.Context, c *http.Client, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func getJSON(ctx context.Context, c *http.Client, url string, v any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(v) == nil
}

// appSlug turns a window title into a profile name: lowercase, every run of
// characters that is neither letter nor digit becomes one '-'. Letters include
// Hangul, so a Korean title stays readable.
func appSlug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "app"
	}
	return s
}

// isAppProfile reports whether a profile name addresses an app rather than a
// Chrome profile. Chrome profiles are account emails, so neither prefix can
// collide with one.
func isAppProfile(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(n, "app:") || strings.HasPrefix(n, "cdp:")
}

// resolveApp picks one app for a profile name, following the same rule as
// Chrome profiles: exact wins, a unique prefix is accepted, and an ambiguous
// one is refused rather than guessed — driving the wrong window is the exact
// failure the naming exists to prevent.
func resolveApp(apps []appInfo, name string) (appInfo, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if len(apps) == 0 {
		return appInfo{}, fmt.Errorf("no app with a DevTools port found on %s — launch it with a debugging port (see README: app targets)", portSpecLabel())
	}
	if rest, ok := strings.CutPrefix(n, "cdp:"); ok {
		port, err := strconv.Atoi(rest)
		if err == nil {
			for _, a := range apps {
				if a.Port == port {
					return a, nil
				}
			}
		}
		return appInfo{}, fmt.Errorf("no app on %s — found: %s", n, appNames(apps))
	}
	want := strings.TrimPrefix(n, "app:")
	var hits []appInfo
	for _, a := range apps {
		if a.slug() == want {
			return a, nil
		}
		if strings.HasPrefix(a.slug(), want) {
			hits = append(hits, a)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return appInfo{}, fmt.Errorf("no app matches %q — found: %s", name, appNames(apps))
	default:
		return appInfo{}, fmt.Errorf("app %q is ambiguous — matches: %s (use cdp:<port> to pick one)", name, appNames(hits))
	}
}

func appNames(apps []appInfo) string {
	names := make([]string, len(apps))
	for i, a := range apps {
		names[i] = fmt.Sprintf("app:%s (cdp:%d)", a.slug(), a.Port)
	}
	return strings.Join(names, ", ")
}

func portSpecLabel() string {
	if s := strings.TrimSpace(os.Getenv("AGLINK_WEB_CDP_PORTS")); s != "" {
		return "ports " + s
	}
	return "ports 9222-9240"
}
