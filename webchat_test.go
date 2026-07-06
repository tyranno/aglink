package main

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// TestWebServerStartFailSoft asserts Start() does not panic and returns
// promptly when net.Listen fails (malformed addr here), leaving the bot
// alive. A timeout means Start wrongly blocked past the listen-error path.
func TestWebServerStartFailSoft(t *testing.T) {
	s := &webServer{addr: "127.0.0.1:999999", token: "secret", ownerChatID: 7, hub: NewHub(), bot: &Bot{}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Start()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start() blocked past a listen error instead of returning promptly")
	}
}

// TestResolveWebOwner covers the three owner-resolution outcomes used at
// web-chat startup: explicit config wins, else fall back to the first
// allowed user, else web chat is disabled (ok=false).
func TestResolveWebOwner(t *testing.T) {
	if got, ok := resolveWebOwner(42, []int64{1, 2}); got != 42 || !ok {
		t.Errorf("explicit owner: got (%d, %v), want (42, true)", got, ok)
	}
	if got, ok := resolveWebOwner(0, []int64{5, 6}); got != 5 || !ok {
		t.Errorf("fallback to allowed[0]: got (%d, %v), want (5, true)", got, ok)
	}
	if got, ok := resolveWebOwner(0, nil); got != 0 || ok {
		t.Errorf("no owner: got (%d, %v), want (0, false)", got, ok)
	}
}

func TestTokenOK(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ws?token=secret", nil)
	if !tokenOK(r, "secret") {
		t.Error("query token should match")
	}
	if tokenOK(r, "other") {
		t.Error("wrong token must fail")
	}
	r2 := httptest.NewRequest(http.MethodGet, "/api/upload", nil)
	r2.Header.Set("Authorization", "Bearer secret")
	if !tokenOK(r2, "secret") {
		t.Error("bearer token should match")
	}
	r3 := httptest.NewRequest(http.MethodGet, "/ws", nil)
	if tokenOK(r3, "secret") {
		t.Error("missing token must fail")
	}
}

func TestLoadOrCreateWebToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	tok1, err := loadOrCreateWebToken("")
	if err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}
	if tok1 == "" {
		t.Fatal("first call: expected non-empty token")
	}

	tok2, err := loadOrCreateWebToken("")
	if err != nil {
		t.Fatalf("second call: unexpected error: %v", err)
	}
	if tok2 != tok1 {
		t.Errorf("token not persisted across calls: first=%q second=%q", tok1, tok2)
	}

	if got, err := loadOrCreateWebToken("explicit"); err != nil || got != "explicit" {
		t.Errorf("loadOrCreateWebToken(%q) = (%q, %v), want (%q, nil)", "explicit", got, err, "explicit")
	}
}

func TestOriginOK(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		{"", true},
		{"http://127.0.0.1:1717", true},
		{"http://localhost:1717", true},
		{"http://localhost", true},
		{"http://evil.com", false},
		{"https://example.org:1717", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := originOK(r); got != c.want {
			t.Errorf("originOK(%q)=%v, want %v", c.origin, got, c.want)
		}
	}
}

// Non-command text with no explicit target now routes through dispatchTargeted
// (default: telegram stream) instead of the LLM-routed dispatchText/dispatchHook
// path (Task 5), so routing is observed via the telegram conversation's history
// rather than dispatchHook.
func TestWebInjectRouting(t *testing.T) {
	var gotCmd string
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m, st, _ := webTgtManager(t, fc)
	b := &Bot{manager: m, store: st, cfgh: NewConfigHolder(&Config{MaxWorkers: 3, TimeoutMinutes: 1}), cancels: make(map[int]context.CancelFunc)}
	b.out = NewHub()
	b.commandHook = func(_ int64, text string) { gotCmd = text }
	s := &webServer{ownerChatID: 7, bot: b, hub: b.out}

	s.inject("!help", nil)
	s.inject("hello world", nil)

	if gotCmd != "!help" {
		t.Errorf("command not routed to handleCommand, got %q", gotCmd)
	}
	deadline := time.Now().Add(2 * time.Second) // dispatchTargeted now runs the actual send via the queue's goroutine
	for len(st.TelegramConversation().History) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(st.TelegramConversation().History) != 1 {
		t.Errorf("text not routed to telegram target via dispatchTargeted, history len=%d", len(st.TelegramConversation().History))
	}
}

func TestWebInjectRateLimited(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m, st, _ := webTgtManager(t, fc)
	b := &Bot{manager: m, store: st, cfgh: NewConfigHolder(&Config{MaxWorkers: 3, TimeoutMinutes: 1}), cancels: make(map[int]context.CancelFunc)}
	b.out = NewHub()
	b.rateLimiter = NewRateLimiter(1) // allow 1 per minute
	s := &webServer{ownerChatID: 7, bot: b, hub: b.out}

	s.inject("hi", nil)
	s.inject("hi again", nil)

	deadline := time.Now().Add(2 * time.Second)
	for len(st.TelegramConversation().History) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(st.TelegramConversation().History) != 1 {
		t.Errorf("history len = %d, want 1 (second call should have been rate-limited)", len(st.TelegramConversation().History))
	}
}

func TestHandleUpload_Ingests(t *testing.T) {
	var gotText string
	b := &Bot{}
	b.out = NewHub()
	b.dispatchHook = func(_ int64, text string) { gotText = text }
	s := &webServer{ownerChatID: 7, token: "secret", bot: b, hub: b.out}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("caption", "이거 봐줘")
	fw, _ := mw.CreateFormFile("file", "note.txt")
	_, _ = fw.Write([]byte("hello"))
	_ = mw.Close()

	r := httptest.NewRequest(http.MethodPost, "/api/upload?token=secret", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.handleUpload(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	// The ingest prompt must contain the caption and the saved path (ends in .txt).
	if !strings.Contains(gotText, "이거 봐줘") || !strings.Contains(gotText, ".txt]") {
		t.Errorf("ingest prompt = %q", gotText)
	}
}

func TestHandleConversationsListsActiveTopics(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.json")
	st := NewFileStore(storePath)
	if err := st.Load(); err != nil {
		t.Fatalf("load store: %v", err)
	}
	projectDir := filepath.Join(dir, "alpha")
	if err := os.Mkdir(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := st.AddProject("alpha", projectDir); err != nil {
		t.Fatalf("add project: %v", err)
	}

	oldConv, err := st.NewConversation("alpha", "오래된 주제", "")
	if err != nil {
		t.Fatalf("new old conversation: %v", err)
	}
	oldConv.Summary = "예전 작업 요약"
	oldConv.LastActivity = time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	oldConv.Started = true
	if err := st.UpdateConversation("alpha", oldConv); err != nil {
		t.Fatalf("update old conversation: %v", err)
	}

	activeConv, err := st.NewConversation("alpha", "현재 주제", "")
	if err != nil {
		t.Fatalf("new active conversation: %v", err)
	}
	activeConv.Summary = "현재 작업 요약"
	activeConv.LastActivity = time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	activeConv.Started = true
	activeConv.Backend = "codex"
	if err := st.UpdateConversation("alpha", activeConv); err != nil {
		t.Fatalf("update active conversation: %v", err)
	}
	if err := st.SetActive("alpha", activeConv.ID); err != nil {
		t.Fatalf("set active: %v", err)
	}

	b := &Bot{store: st}
	s := &webServer{token: "secret", bot: b}
	r := httptest.NewRequest(http.MethodGet, "/api/conversations?token=secret", nil)
	w := httptest.NewRecorder()
	s.handleConversations(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}

	var got struct {
		Active struct {
			Project        string `json:"project"`
			ConversationID string `json:"conversationId"`
		} `json:"active"`
		Projects []struct {
			Name          string `json:"name"`
			Path          string `json:"path"`
			Conversations []struct {
				ID           string `json:"id"`
				Title        string `json:"title"`
				Summary      string `json:"summary"`
				Started      bool   `json:"started"`
				Backend      string `json:"backend"`
				Active       bool   `json:"active"`
				LastActivity string `json:"lastActivity"`
			} `json:"conversations"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("json decode: %v\nbody=%s", err, w.Body.String())
	}
	if got.Active.Project != "alpha" || got.Active.ConversationID != activeConv.ID {
		t.Fatalf("active = %+v, want alpha/%s", got.Active, activeConv.ID)
	}
	if len(got.Projects) != 1 {
		t.Fatalf("projects len = %d, want 1", len(got.Projects))
	}
	if got.Projects[0].Name != "alpha" || got.Projects[0].Path != projectDir {
		t.Fatalf("project = %+v, want alpha/%s", got.Projects[0], projectDir)
	}
	if len(got.Projects[0].Conversations) != 2 {
		t.Fatalf("conversation len = %d, want 2", len(got.Projects[0].Conversations))
	}
	first := got.Projects[0].Conversations[0]
	if first.ID != activeConv.ID || first.Title != "현재 주제" || !first.Active || first.Summary != "현재 작업 요약" || first.Backend != "codex" {
		t.Fatalf("first conversation = %+v, want active conversation first", first)
	}
	if first.LastActivity == "" {
		t.Fatal("lastActivity should be populated")
	}
}

func TestHandleConversationsGroupsContinuationChains(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.json")
	st := NewFileStore(storePath)
	if err := st.Load(); err != nil {
		t.Fatalf("load store: %v", err)
	}
	projectDir := filepath.Join(dir, "alpha")
	if err := os.Mkdir(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := st.AddProject("alpha", projectDir); err != nil {
		t.Fatalf("add project: %v", err)
	}

	root, err := st.NewConversation("alpha", "긴 작업", "")
	if err != nil {
		t.Fatalf("new root conversation: %v", err)
	}
	root.Summary = "첫 구간"
	root.LastActivity = time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	root.Started = true

	cont, err := st.NewConversation("alpha", "긴 작업 (시리즈 2)", "")
	if err != nil {
		t.Fatalf("new continuation conversation: %v", err)
	}
	cont.ParentID = root.ID
	cont.IsContinuation = true
	cont.Summary = "이어진 구간"
	cont.LastActivity = time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	cont.Started = true
	cont.Backend = "claude"
	root.ChildID = cont.ID
	if err := st.UpdateConversation("alpha", root); err != nil {
		t.Fatalf("update root conversation: %v", err)
	}
	if err := st.UpdateConversation("alpha", cont); err != nil {
		t.Fatalf("update continuation conversation: %v", err)
	}

	other, err := st.NewConversation("alpha", "다른 작업", "")
	if err != nil {
		t.Fatalf("new other conversation: %v", err)
	}
	other.Summary = "별도 주제"
	other.LastActivity = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	other.Started = true
	if err := st.UpdateConversation("alpha", other); err != nil {
		t.Fatalf("update other conversation: %v", err)
	}
	if err := st.SetActive("alpha", cont.ID); err != nil {
		t.Fatalf("set active: %v", err)
	}

	b := &Bot{store: st}
	s := &webServer{token: "secret", bot: b}
	r := httptest.NewRequest(http.MethodGet, "/api/conversations?token=secret", nil)
	w := httptest.NewRecorder()
	s.handleConversations(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var got struct {
		Projects []struct {
			Conversations []struct {
				ID      string `json:"id"`
				Title   string `json:"title"`
				Summary string `json:"summary"`
				Backend string `json:"backend"`
				Active  bool   `json:"active"`
			} `json:"conversations"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("json decode: %v\nbody=%s", err, w.Body.String())
	}
	if len(got.Projects) != 1 {
		t.Fatalf("projects len = %d, want 1", len(got.Projects))
	}
	convs := got.Projects[0].Conversations
	if len(convs) != 2 {
		t.Fatalf("conversation len = %d, want 2 grouped topics; body=%s", len(convs), w.Body.String())
	}
	first := convs[0]
	if first.ID != cont.ID || first.Title != "긴 작업" || first.Summary != "이어진 구간" || first.Backend != "claude" || !first.Active {
		t.Fatalf("grouped continuation topic = %+v, want active latest child under root title", first)
	}
	for _, conv := range convs {
		if conv.Title == "긴 작업 (시리즈 2)" {
			t.Fatalf("continuation child should not be listed as a separate topic: %+v", convs)
		}
	}
}

func TestWebUIUsesAutosizingTextarea(t *testing.T) {
	html, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	app, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	css, err := webFS.ReadFile("web/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}

	htmlText := string(html)
	if !strings.Contains(htmlText, `<textarea id="input"`) {
		t.Fatalf("composer should use an autosizing textarea, html=%s", htmlText)
	}
	if strings.Contains(htmlText, `<input id="input" type="text"`) {
		t.Fatal("composer must not use a single-line text input")
	}
	if !strings.Contains(htmlText, `rows="1"`) {
		t.Fatal("textarea should start at one row")
	}

	appText := string(app)
	if !strings.Contains(appText, "resizeInput") || !strings.Contains(appText, "input.scrollHeight") {
		t.Fatal("app.js should resize the textarea from its scrollHeight")
	}

	cssText := string(css)
	if !strings.Contains(cssText, "--input-line-height") || !strings.Contains(cssText, "max-height: calc(var(--input-line-height) * 10") {
		t.Fatal("style.css should cap the composer textarea at about 10 lines")
	}
}

func TestWebUIHasConversationTopicList(t *testing.T) {
	html, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	app, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	css, err := webFS.ReadFile("web/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}

	htmlText := string(html)
	if !strings.Contains(htmlText, `id="topics"`) || !strings.Contains(htmlText, `id="topic-list"`) {
		t.Fatalf("index should include a conversation topic list sidebar, html=%s", htmlText)
	}

	appText := string(app)
	if !strings.Contains(appText, "loadConversations") || !strings.Contains(appText, "/api/conversations") {
		t.Fatal("app.js should load and render conversations from /api/conversations")
	}
	// Web-first UI: the list is the top-level web conversations plus a pinned
	// telegram entry (project-group rendering was removed).
	if !strings.Contains(appText, "makeWebConvButton") || !strings.Contains(appText, "data.webConvs") {
		t.Fatal("app.js should render top-level web conversations (web-first)")
	}
	if !strings.Contains(appText, "makeTelegramButton") {
		t.Fatal("app.js should render the pinned telegram entry")
	}
	if !strings.Contains(appText, "selectTarget") {
		t.Fatal("app.js should switch conversations via selectTarget target routing")
	}

	cssText := string(css)
	if !strings.Contains(cssText, "#topics") || !strings.Contains(cssText, "#topic-list") {
		t.Fatal("style.css should style the conversation topic list")
	}
}

// TestOriginOK_Rejects checks originOK against spoofed/hostile Origin headers
// (subdomain confusables, the sandboxed "null" origin, and a bare foreign
// host) as well as the loopback origins that must still be allowed.
func TestOriginOK_Rejects(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		{"null", false},
		{"http://127.0.0.1.evil.com", false},
		{"http://localhost.evil.com", false},
		{"http://evil.com", false},
		{"http://127.0.0.1:1717", true},
		{"http://localhost:1717", true},
		{"", true},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := originOK(r); got != c.want {
			t.Errorf("originOK(%q) = %v, want %v", c.origin, got, c.want)
		}
	}
}

// TestWS_Unauthorized asserts the WS handshake is rejected for a wrong or
// missing token, and that no per-chat channel is left registered in the Hub
// as a result of the rejected attempts.
func TestWS_Unauthorized(t *testing.T) {
	b := &Bot{}
	hub := NewHub()
	b.out = hub
	s := &webServer{token: "secret", ownerChatID: 7, hub: hub, bot: b}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")

	dialAndExpectReject := func(name, url string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, resp, err := websocket.Dial(ctx, url, nil)
		if err == nil {
			c.Close(websocket.StatusNormalClosure, "")
			t.Errorf("%s: expected dial to be rejected, got success", name)
			return
		}
		if resp != nil && resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: response status = %d, want %d", name, resp.StatusCode, http.StatusUnauthorized)
		}
	}

	dialAndExpectReject("wrong token", "ws://"+host+"/ws?token=wrong")
	dialAndExpectReject("no token", "ws://"+host+"/ws")

	if n := len(hub.perChat[7]); n != 0 {
		t.Errorf("hub.perChat[7] has %d channel(s) after rejected handshakes, want 0", n)
	}
}

// TestUpload_Unauthorized asserts /api/upload rejects wrong/missing tokens
// and non-POST methods with 401, and never reaches ingestAttachment
// (dispatchHook) for any rejected request.
func TestUpload_Unauthorized(t *testing.T) {
	var dispatchCount int
	b := &Bot{}
	b.out = NewHub()
	b.dispatchHook = func(_ int64, _ string) { dispatchCount++ }
	s := &webServer{ownerChatID: 7, token: "secret", bot: b, hub: b.out}

	newUploadBody := func() (*bytes.Buffer, string) {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", "note.txt")
		_, _ = fw.Write([]byte("hello"))
		_ = mw.Close()
		return &body, mw.FormDataContentType()
	}

	// Wrong token, POST.
	body, ct := newUploadBody()
	r := httptest.NewRequest(http.MethodPost, "/api/upload?token=wrong", body)
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	s.handleUpload(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", w.Code)
	}

	// No token, POST.
	body, ct = newUploadBody()
	r = httptest.NewRequest(http.MethodPost, "/api/upload", body)
	r.Header.Set("Content-Type", ct)
	w = httptest.NewRecorder()
	s.handleUpload(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", w.Code)
	}

	// Valid token, wrong method (GET): method guard must reject before auth
	// would otherwise allow it through.
	r = httptest.NewRequest(http.MethodGet, "/api/upload?token=secret", nil)
	w = httptest.NewRecorder()
	s.handleUpload(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong method: status = %d, want 401", w.Code)
	}

	if dispatchCount != 0 {
		t.Errorf("dispatchHook called %d time(s) on rejected requests, want 0", dispatchCount)
	}
}

// TestWSRoundTrip exercises the full browser <-> server pipeline over a real
// WebSocket connection: an inbound {"type":"send"} frame with no explicit
// target must reach the Manager via dispatchTargeted's telegram default (Task
// 5), and a Hub broadcast to the registered chatID must arrive back at the
// client as a {"type":"text"} frame.
func TestWSRoundTrip(t *testing.T) {
	fc := &fakeClaude{runRes: RunResult{Text: "ok"}}
	m, st, _ := webTgtManager(t, fc)
	b := &Bot{manager: m, store: st, cfgh: NewConfigHolder(&Config{MaxWorkers: 3, TimeoutMinutes: 1}), cancels: make(map[int]context.CancelFunc)}
	hub := NewHub()
	b.out = hub
	s := &webServer{token: "secret", ownerChatID: 7, hub: hub, bot: b}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	host := strings.TrimPrefix(srv.URL, "http://")
	c, _, err := websocket.Dial(ctx, "ws://"+host+"/ws?token=secret", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	if werr := wsjson.Write(ctx, c, inMsg{Type: "send", Text: "hello"}); werr != nil {
		t.Fatalf("write: %v", werr)
	}

	deadline := time.Now().Add(3 * time.Second)
	for len(st.TelegramConversation().History) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(st.TelegramConversation().History) != 1 {
		t.Fatalf("timeout waiting for inbound frame to reach the manager, history len=%d", len(st.TelegramConversation().History))
	}

	// hub.Register happens synchronously in handleWS before the reader loop, so
	// the channel is guaranteed registered well before this broadcast. The real
	// worker run above also emits its own frames (typing/response/completion)
	// to the same channel, so scan past those for the explicit "reply" frame.
	_ = s.hub.Send(7, "reply")

	found := false
	for !found {
		var frame wsFrame
		if rerr := wsjson.Read(ctx, c, &frame); rerr != nil {
			t.Fatalf("read: %v (waiting for reply frame)", rerr)
		}
		if frame.Type == "text" && frame.Text == "reply" {
			found = true
		}
	}
}

// TestWebChannelBackpressure proves a slow/dead client is dropped rather than
// blocking the Hub: once the small send buffer is full, further pushes must
// not block, and the overflow must close the channel (via cancel) exactly
// once even across multiple overflow pushes.
func TestWebChannelBackpressure(t *testing.T) {
	send := make(chan wsFrame, 1) // small buffer, deliberately never drained
	var cancelCalls int32
	done := make(chan struct{})
	ch := &webChannel{
		send: send,
		cancel: func() {
			atomic.AddInt32(&cancelCalls, 1)
			close(done)
		},
	}

	// Fill the one-slot buffer; no reader goroutine drains it.
	ch.push(wsFrame{Type: "text", Text: "1"})

	// Further pushes/Sends must not block even though the buffer stays full
	// and the overflow path is hit repeatedly.
	overflowDone := make(chan struct{})
	go func() {
		ch.push(wsFrame{Type: "text", Text: "2"}) // overflow #1 -> triggers close()
		_ = ch.Send(7, "overflow-3")              // overflow #2 -> close() again, but closeOnce guards
		close(overflowDone)
	}()

	select {
	case <-overflowDone:
	case <-time.After(2 * time.Second):
		t.Fatal("push/Send blocked on a full buffer; overflow must be non-blocking")
	}

	select {
	case <-done:
	default:
		t.Fatal("overflow push should have triggered close()/cancel")
	}

	if got := atomic.LoadInt32(&cancelCalls); got != 1 {
		t.Errorf("cancel must be invoked exactly once via closeOnce even across multiple overflow pushes, got %d", got)
	}
}

// TestWSFrameJSONTags locks the wire protocol between the server and app.js:
// field names and omitempty behavior must not drift silently.
func TestWSFrameJSONTags(t *testing.T) {
	imgB, err := json.Marshal(wsFrame{Type: "image", Caption: "c", Data: "ZGF0YQ=="})
	if err != nil {
		t.Fatal(err)
	}
	var imgKeys map[string]json.RawMessage
	if err := json.Unmarshal(imgB, &imgKeys); err != nil {
		t.Fatal(err)
	}
	wantImgKeys := map[string]bool{"type": true, "caption": true, "data": true}
	if len(imgKeys) != len(wantImgKeys) {
		t.Fatalf("image frame keys = %v, want exactly %v", imgKeys, wantImgKeys)
	}
	for k := range imgKeys {
		if !wantImgKeys[k] {
			t.Errorf("unexpected key %q in image frame JSON: %s", k, imgB)
		}
	}
	if _, ok := imgKeys["text"]; ok {
		t.Errorf("empty text must be omitted via omitempty, got: %s", imgB)
	}

	textB, err := json.Marshal(wsFrame{Type: "text", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	var textKeys map[string]json.RawMessage
	if err := json.Unmarshal(textB, &textKeys); err != nil {
		t.Fatal(err)
	}
	wantTextKeys := map[string]bool{"type": true, "text": true}
	if len(textKeys) != len(wantTextKeys) {
		t.Fatalf("text frame keys = %v, want exactly %v", textKeys, wantTextKeys)
	}
	for k := range textKeys {
		if !wantTextKeys[k] {
			t.Errorf("unexpected key %q in text frame JSON: %s", k, textB)
		}
	}
}
