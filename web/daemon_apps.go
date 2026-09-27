package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// appState is the daemon's side of app targets: which windows it is attached
// to, and a short-lived cache of what discovery last found.
type appState struct {
	mu       sync.Mutex
	conns    map[string]*appTarget // CDP target id → live connection
	cache    []appInfo
	cachedAt time.Time
}

// appCacheTTL keeps back-to-back tool calls from re-knocking on every port.
// A miss re-discovers once regardless, so an app launched a moment ago is not
// hidden by a stale cache.
const appCacheTTL = 5 * time.Second

func defaultDiscover(ctx context.Context) []appInfo { return discoverApps(ctx, cdpPorts()) }

func (d *Daemon) appList(ctx context.Context, fresh bool) []appInfo {
	d.apps.mu.Lock()
	if !fresh && d.apps.cache != nil && time.Since(d.apps.cachedAt) < appCacheTTL {
		out := d.apps.cache
		d.apps.mu.Unlock()
		return out
	}
	d.apps.mu.Unlock()

	found := d.discover(ctx)
	d.apps.mu.Lock()
	d.apps.cache, d.apps.cachedAt = found, time.Now()
	d.apps.mu.Unlock()
	return found
}

// callApp runs one tool against an Electron/Wails window.
func (d *Daemon) callApp(method string, params map[string]any, profile string) CallResult {
	timeout := callTimeout
	if method == "wait_for_element" {
		if ms := intp(params, "timeoutMs", defaultWaitTimeoutMs); time.Duration(ms)*time.Millisecond+5*time.Second > timeout {
			timeout = time.Duration(ms)*time.Millisecond + 5*time.Second
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	app, err := resolveApp(d.appList(ctx, false), profile)
	if err != nil {
		// The cache may predate the app; look once more before giving up.
		if app, err = resolveApp(d.appList(ctx, true), profile); err != nil {
			return CallResult{Error: err.Error()}
		}
	}

	page := app.Pages[0]
	if n := intp(params, "tabId", 0); n != 0 {
		if n < 1 || n > len(app.Pages) {
			return CallResult{Error: fmt.Sprintf("no window %d in app:%s — it has %d (see list_tabs)", n, app.slug(), len(app.Pages))}
		}
		page = app.Pages[n-1]
	}

	target, err := d.attach(page, app.Port)
	if err != nil {
		return CallResult{Error: fmt.Sprintf("attach to app:%s: %v", app.slug(), err)}
	}
	log.Printf("aglink-web: → app:%s (cdp:%d) %s", app.slug(), app.Port, method)
	res := appCall(ctx, target, app.Pages, method, params)
	if res.Error != "" {
		log.Printf("aglink-web: ← app:%s error: %s", app.slug(), res.Error)
	}
	return res
}

// attach returns the live connection to a window, opening it on first use and
// reopening it if the window went away (an app restart gives the same port a
// fresh target).
func (d *Daemon) attach(page cdpTarget, port int) (*appTarget, error) {
	d.apps.mu.Lock()
	defer d.apps.mu.Unlock()
	if a := d.apps.conns[page.ID]; a != nil {
		select {
		case <-a.Done():
			delete(d.apps.conns, page.ID)
		default:
			return a, nil
		}
	}
	a, err := openAppTarget(page, port)
	if err != nil {
		return nil, err
	}
	d.apps.conns[page.ID] = a
	return a, nil
}

// appProfileLines renders the discovered apps for list_profiles.
func (d *Daemon) appProfileLines() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*probeTimeout)
	defer cancel()
	var lines []string
	for _, a := range d.appList(ctx, true) {
		windows := "1 window"
		if len(a.Pages) != 1 {
			windows = fmt.Sprintf("%d windows", len(a.Pages))
		}
		lines = append(lines, fmt.Sprintf("app:%s | cdp:%d | %s | %s", a.slug(), a.Port, a.URL, windows))
	}
	return lines
}

func (d *Daemon) closeApps() {
	d.apps.mu.Lock()
	defer d.apps.mu.Unlock()
	for id, a := range d.apps.conns {
		a.Close()
		delete(d.apps.conns, id)
	}
}
