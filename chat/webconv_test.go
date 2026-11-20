package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestBuildWebconvControlIn(t *testing.T) {
	m, ok := buildWebconvControlIn("setdir", webconvBody{ID: "c1", Path: "/tmp/x"})
	if !ok || m.Type != "web_setdir" || m.ID != "c1" || m.Path != "/tmp/x" || m.Origin != "web" {
		t.Errorf("setdir → %+v ok=%v", m, ok)
	}
	m, ok = buildWebconvControlIn("delete", webconvBody{ID: "c1"})
	if !ok || m.Type != "web_delete" || m.ID != "c1" {
		t.Errorf("delete → %+v ok=%v", m, ok)
	}
	m, ok = buildWebconvControlIn("rename", webconvBody{ID: "c1", Title: "t"})
	if !ok || m.Type != "web_rename" || m.Title != "t" {
		t.Errorf("rename → %+v ok=%v", m, ok)
	}
	if _, ok := buildWebconvControlIn("delete", webconvBody{}); ok {
		t.Error("missing id must be rejected")
	}
	if _, ok := buildWebconvControlIn("bogus", webconvBody{ID: "c1"}); ok {
		t.Error("unknown verb must be rejected")
	}
}

// fakeTeleclaude answers every control request with reply(m).
func fakeTeleclaude(t *testing.T, reply func(controlIn) any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		for {
			var m controlIn
			if err := wsjson.Read(r.Context(), c, &m); err != nil {
				return
			}
			data, _ := json.Marshal(reply(m))
			_ = wsjson.Write(r.Context(), c, controlOut{Kind: "reply", ReqID: m.ReqID, Data: data})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func connectedServer(t *testing.T, reply func(controlIn) any) *browserServer {
	t.Helper()
	srv := fakeTeleclaude(t, reply)
	hub := newBrowserHub()
	cc := newControlClient(strings.TrimPrefix(srv.URL, "http://"), "tok", hub)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go cc.run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for cc.getConn() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if cc.getConn() == nil {
		t.Fatal("control client never connected to the fake teleclaude")
	}
	return &browserServer{token: "secret", control: cc, hub: hub}
}

// The setdir / delete endpoints relay to teleclaude and hand its {ok,error}
// back to the browser verbatim — success and failure alike.
func TestWebconvHandler_RelaysAck(t *testing.T) {
	var seen []controlIn
	s := connectedServer(t, func(m controlIn) any {
		seen = append(seen, m)
		if m.Type == "web_setdir" && m.Path == "/bad" {
			return map[string]any{"ok": false, "error": "경로를 찾을 수 없습니다: /bad"}
		}
		return map[string]any{"ok": true}
	})

	cases := []struct {
		verb, method, body, wantType string
		wantOK                       bool
	}{
		{"setdir", http.MethodPut, `{"id":"c1","path":"/good"}`, "web_setdir", true},
		{"setdir", http.MethodPut, `{"id":"c1","path":"/bad"}`, "web_setdir", false},
		{"delete", http.MethodPost, `{"id":"c1"}`, "web_delete", true},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, "/api/webconv/"+tc.verb, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		s.webconvHandler(tc.verb, tc.method)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: status %d", tc.verb, tc.body, rec.Code)
		}
		var got struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: body %q: %v", tc.verb, rec.Body.String(), err)
		}
		if got.OK != tc.wantOK || (!tc.wantOK && got.Error == "") {
			t.Errorf("%s %s: got %+v, want ok=%v with error on failure", tc.verb, tc.body, got, tc.wantOK)
		}
		if last := seen[len(seen)-1]; last.Type != tc.wantType {
			t.Errorf("%s relayed as %q, want %q", tc.verb, last.Type, tc.wantType)
		}
	}
}

func TestWebconvHandler_Rejects(t *testing.T) {
	s := &browserServer{token: "secret", control: newControlClient("127.0.0.1:1", "x", newBrowserHub())}

	// Wrong method / no token → 401; missing id → 400; teleclaude unreachable → 502.
	check := func(name, method, body, auth string, want int) {
		req := httptest.NewRequest(method, "/api/webconv/delete", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		s.webconvHandler("delete", http.MethodPost)(rec, req)
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d", name, rec.Code, want)
		}
	}
	check("wrong method", http.MethodGet, `{"id":"c1"}`, "secret", http.StatusUnauthorized)
	check("no token", http.MethodPost, `{"id":"c1"}`, "", http.StatusUnauthorized)
	check("missing id", http.MethodPost, `{}`, "secret", http.StatusBadRequest)
	check("not connected", http.MethodPost, `{"id":"c1"}`, "secret", http.StatusBadGateway)
}
