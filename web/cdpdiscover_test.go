package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeApp serves /json/version and /json/list like a WebView2/Electron page.
func fakeApp(t *testing.T, title, url string, delay time.Duration) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		switch r.URL.Path {
		case "/json/version":
			fmt.Fprint(w, `{"Browser":"Edg/153.0"}`)
		case "/json/list":
			fmt.Fprintf(w, `[
			 {"id":"DT","type":"other","title":"DevTools","url":"devtools://x","webSocketDebuggerUrl":"ws://127.0.0.1:1/devtools/page/DT"},
			 {"id":"P1","type":"page","title":%q,"url":%q,"webSocketDebuggerUrl":"ws://127.0.0.1:1/devtools/page/P1"}]`, title, url)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	_, portStr, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	p, _ := strconv.Atoi(portStr)
	return p
}

func deadPort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

func TestDiscoverApps(t *testing.T) {
	a := fakeApp(t, "aglink", "http://wails.localhost/", 0)
	b := fakeApp(t, "My Tool — Dev", "http://localhost:5173/", 0)
	slow := fakeApp(t, "slow", "http://x/", 800*time.Millisecond)
	apps := discoverApps(context.Background(), []int{deadPort(t), a, b, slow})
	if len(apps) != 2 {
		t.Fatalf("want 2 apps (dead and slow ports dropped), got %d: %+v", len(apps), apps)
	}
	for _, app := range apps {
		if len(app.Pages) != 1 || app.Pages[0].Type != "page" {
			t.Errorf("only page targets belong in Pages: %+v", app.Pages)
		}
	}
	byPort := map[int]appInfo{}
	for _, app := range apps {
		byPort[app.Port] = app
	}
	if byPort[a].Title != "aglink" || byPort[a].URL != "http://wails.localhost/" {
		t.Errorf("app a: %+v", byPort[a])
	}
}

func TestAppSlug(t *testing.T) {
	cases := map[string]string{
		"aglink":        "aglink",
		"My Tool — Dev": "my-tool-dev",
		"  SPC-UI  ":    "spc-ui",
		"":              "app",
		"휴가 관리":         "휴가-관리",
	}
	for in, want := range cases {
		if got := appSlug(in); got != want {
			t.Errorf("appSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsAppProfile(t *testing.T) {
	for _, s := range []string{"app:aglink", "cdp:9333", "APP:x"} {
		if !isAppProfile(s) {
			t.Errorf("%q should route to an app", s)
		}
	}
	for _, s := range []string{"", "doowon.lab.02@gmail.com", "apple@x.com"} {
		if isAppProfile(s) {
			t.Errorf("%q must stay a Chrome profile", s)
		}
	}
}

func TestResolveApp(t *testing.T) {
	apps := []appInfo{
		{Port: 9333, Title: "aglink"},
		{Port: 9334, Title: "SPC-UI"},
		{Port: 9335, Title: "SPC-Tool"},
	}
	if a, err := resolveApp(apps, "app:agl"); err != nil || a.Port != 9333 {
		t.Errorf("prefix: %+v %v", a, err)
	}
	if a, err := resolveApp(apps, "cdp:9334"); err != nil || a.Port != 9334 {
		t.Errorf("exact port: %+v %v", a, err)
	}
	if a, err := resolveApp(apps, "app:spc-ui"); err != nil || a.Port != 9334 {
		t.Errorf("exact slug wins over prefix: %+v %v", a, err)
	}
	if _, err := resolveApp(apps, "app:spc"); err == nil || !strings.Contains(err.Error(), "spc-ui") || !strings.Contains(err.Error(), "spc-tool") {
		t.Errorf("ambiguous prefix must name both: %v", err)
	}
	if _, err := resolveApp(apps, "app:nothing"); err == nil {
		t.Error("unknown app must be an error")
	}
	if _, err := resolveApp(apps, "cdp:1"); err == nil {
		t.Error("unknown port must be an error")
	}
	if _, err := resolveApp(nil, "app:x"); err == nil || !strings.Contains(err.Error(), "no app") {
		t.Errorf("no apps at all should say so: %v", err)
	}
}

func TestCDPPortsFromEnv(t *testing.T) {
	t.Setenv("AGLINK_WEB_CDP_PORTS", "")
	// 9222-9240 plus 9333, the port the Wails docs use.
	if p := cdpPorts(); len(p) != 20 || p[0] != 9222 || p[18] != 9240 || p[19] != 9333 {
		t.Errorf("default range: %v", p)
	}
	t.Setenv("AGLINK_WEB_CDP_PORTS", "9333")
	if p := cdpPorts(); len(p) != 1 || p[0] != 9333 {
		t.Errorf("single: %v", p)
	}
	t.Setenv("AGLINK_WEB_CDP_PORTS", "9300-9302, 9400")
	if p := cdpPorts(); len(p) != 4 || p[3] != 9400 {
		t.Errorf("range+list: %v", p)
	}
}

func TestAppPagesSkipsDevToolsAndKeepsAFixedOrder(t *testing.T) {
	// Chromium's most-recently-used-first order, with an open DevTools window.
	list := []cdpTarget{
		{ID: "C3", Type: "page", Title: "설정", URL: "http://wails.localhost/settings", WebSocketDebuggerURL: "ws://127.0.0.1:9333/devtools/page/C3"},
		{ID: "D9", Type: "page", Title: "DevTools", URL: "devtools://devtools/bundled/inspector.html", WebSocketDebuggerURL: "ws://127.0.0.1:9333/devtools/page/D9"},
		{ID: "A1", Type: "page", Title: "aglink", URL: "http://wails.localhost/", WebSocketDebuggerURL: "ws://127.0.0.1:9333/devtools/page/A1"},
		{ID: "S0", Type: "service_worker", URL: "http://wails.localhost/sw.js", WebSocketDebuggerURL: "ws://127.0.0.1:9333/devtools/page/S0"},
	}
	pages := appPages(list)
	if len(pages) != 2 || pages[0].ID != "A1" || pages[1].ID != "C3" {
		t.Fatalf("pages = %+v", pages)
	}
	// The user clicks the other window: Chromium reorders, the numbering must not.
	list[0], list[2] = list[2], list[0]
	again := appPages(list)
	if again[0].ID != "A1" || again[1].ID != "C3" {
		t.Fatalf("order changed with recency: %+v", again)
	}
}
