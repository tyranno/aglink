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

// fakeForkHost answers every web_fork with reply and records what it got.
func fakeForkHost(t *testing.T, reply string, got chan<- controlIn) *ControlService {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		for {
			var m controlIn
			if err := wsjson.Read(r.Context(), conn, &m); err != nil {
				return
			}
			got <- m
			_ = wsjson.Write(r.Context(), conn, controlOut{Kind: "reply", ReqID: m.ReqID, Data: json.RawMessage(reply)})
		}
	}))
	t.Cleanup(server.Close)
	svc := &ControlService{addr: strings.TrimPrefix(server.URL, "http://"), pending: map[string]chan json.RawMessage{}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = svc.connectOnce(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for svc.getConn() == nil {
		if time.Now().After(deadline) {
			t.Fatal("fake host never connected")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return svc
}

func TestWebFork_SendsTargetAndPointAndReturnsNewID(t *testing.T) {
	got := make(chan controlIn, 1)
	svc := fakeForkHost(t, `{"ok":true,"id":"12","title":"a (갈래)"}`, got)
	out, err := svc.WebFork("web", "3", `{"role":"assistant","text":"답","at":5,"occurrence":1}`)
	if err != nil {
		t.Fatalf("WebFork: %v", err)
	}
	if !strings.Contains(out, `"id":"12"`) {
		t.Errorf("reply = %s", out)
	}
	m := <-got
	if m.Type != "web_fork" || !strings.Contains(string(m.Target), `"id":"3"`) || !strings.Contains(string(m.Payload), `"occurrence":1`) {
		t.Errorf("sent %+v target=%s payload=%s", m, m.Target, m.Payload)
	}
}

func TestWebFork_RefusedForkIsAnError(t *testing.T) {
	got := make(chan controlIn, 1)
	svc := fakeForkHost(t, `{"ok":false,"error":"갈라낼 메시지를 대화 기록에서 찾지 못했습니다"}`, got)
	if _, err := svc.WebFork("web", "3", `{"role":"user","text":"x"}`); err == nil || !strings.Contains(err.Error(), "찾지 못했습니다") {
		t.Errorf("err = %v, want the host's refusal", err)
	}
	if _, err := svc.WebFork("web", "3", `not json`); err == nil {
		t.Error("an invalid fork point must be rejected before sending")
	}
}
