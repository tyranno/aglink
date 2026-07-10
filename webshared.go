package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// This file holds the browser-agnostic pieces that used to live in webchat.go
// alongside the embedded web server. The embedded server was removed when
// aglink-chat became the primary frontend (Phase 2); everything here is still
// used by the control API (chatcontrol.go), aglink-chat relay, and main.go.

// loadOrCreateWebToken returns cfgToken if set, otherwise reads (or creates and
// persists) ~/.teleclaude/web_chat.token with 0600 perms. This token is now the
// shared browser token teleclaude hands to the aglink-chat child (so an
// already-connected browser keeps authenticating after the frontend swap).
func loadOrCreateWebToken(cfgToken string) (string, error) {
	return loadOrCreateToken(cfgToken, "web_chat.token")
}

// loadOrCreateToken returns cfgToken if set, otherwise reads (or creates and
// persists) ~/.teleclaude/<filename> with 0600 perms. Used for the web_chat,
// chat_control, and aglink_chat shared tokens.
func loadOrCreateToken(cfgToken, filename string) (string, error) {
	if cfgToken != "" {
		return cfgToken, nil
	}
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, filename)
	if b, rerr := os.ReadFile(p); rerr == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			return tok, nil
		}
	}
	buf := make([]byte, 24)
	if _, rerr := rand.Read(buf); rerr != nil {
		return "", rerr
	}
	tok := hex.EncodeToString(buf)
	if werr := os.WriteFile(p, []byte(tok), 0o600); werr != nil {
		return "", werr
	}
	return tok, nil
}

// tokenOK checks the request token (query ?token= or "Authorization: Bearer <t>")
// against want using a constant-time comparison. Used by the control-API server.
func tokenOK(r *http.Request, want string) bool {
	got := r.URL.Query().Get("token")
	if got == "" {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
	}
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// originOK guards against cross-site requests: allow when there is no Origin
// header (non-browser / same-origin) or when the Origin host is loopback.
func originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "127.0.0.1" || host == "localhost"
}

// wsFrame is the JSON envelope sent to browsers. The embedded server is gone,
// but the control-API channel (remoteChatChannel) still serializes these to
// aglink-chat, which forwards them to real browsers.
type wsFrame struct {
	Type    string `json:"type"` // "text" | "image" | "typing" | "done" | "user"
	Text    string `json:"text,omitempty"`
	Caption string `json:"caption,omitempty"`
	Data    string `json:"data,omitempty"` // base64 PNG for images
	// Target names the conversation this frame belongs to, so a client files it
	// under the right topic instead of whatever is on screen. A frame with no
	// target means the telegram stream — the same default an empty Target.Kind
	// carries everywhere else.
	Target *Target `json:"target,omitempty"`
}

type webConversationTopic struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Summary      string `json:"summary,omitempty"`
	Started      bool   `json:"started"`
	Backend      string `json:"backend,omitempty"`
	Active       bool   `json:"active"`
	LastActivity string `json:"lastActivity,omitempty"`
	// Channel is the conversation series' origin: "web" for chats explicitly
	// created from the web UI, "telegram" for everything else.
	Channel string `json:"channel"`
}

// conversationChannel maps a conversation's Origin to a display channel. Empty
// Origin (legacy/auto-created/telegram) is treated as "telegram"; only an
// explicit "web" origin counts as web.
func conversationChannel(c *Conversation) string {
	if c != nil && c.Origin == OriginWeb {
		return OriginWeb
	}
	return OriginTelegram
}

type webProjectTopics struct {
	Name          string                 `json:"name"`
	Path          string                 `json:"path"`
	Conversations []webConversationTopic `json:"conversations"`
}

type webTelegramEntry struct {
	Title   string `json:"title"`
	ID      string `json:"id"`
	Active  bool   `json:"active"`
	Backend string `json:"backend,omitempty"`
	Project string `json:"project,omitempty"` // current working-dir project
}

type webConversationsResponse struct {
	Active   ActiveRef          `json:"active"`
	Telegram *webTelegramEntry  `json:"telegram,omitempty"`
	Projects []webProjectTopics `json:"projects"`
	WebConvs []webWebConv       `json:"webConvs"`
}

// webActiveWorker is one still-running worker turn. Clients poll these to
// reconcile a working indicator: a conversation missing from the list is idle,
// whatever frames did or didn't arrive.
type webActiveWorker struct {
	Project        string `json:"project,omitempty"`
	ConversationID string `json:"conversationId"`
	Title          string `json:"title,omitempty"`
	StartedAt      string `json:"startedAt,omitempty"` // RFC3339
}

type webActiveWorkersResponse struct {
	Workers []webActiveWorker `json:"workers"`
}

// buildActiveWorkersResponse projects the manager's running workers onto the
// wire shape. Conversation IDs match the client's target ids — the telegram
// stream's conversation is literally "telegram" — so a client can key on them.
func buildActiveWorkersResponse(workers []WorkerStatus) webActiveWorkersResponse {
	out := webActiveWorkersResponse{Workers: make([]webActiveWorker, 0, len(workers))}
	for _, w := range workers {
		aw := webActiveWorker{
			Project:        w.Project,
			ConversationID: w.ConversationID,
			Title:          w.Title,
		}
		if !w.StartTime.IsZero() {
			aw.StartedAt = w.StartTime.UTC().Format(time.RFC3339)
		}
		out.Workers = append(out.Workers, aw)
	}
	return out
}

// webWebConv is a top-level web conversation entry (store.WebConvs), distinct
// from webProjectTopics/webConversationTopic which describe project-scoped
// (Telegram-origin) conversation trees.
type webWebConv struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	WorkDir string `json:"workDir,omitempty"`
	Active  bool   `json:"active"`
	Backend string `json:"backend,omitempty"`
}

type webConversationGroup struct {
	root     *Conversation
	selected *Conversation
	active   bool
	started  bool
	last     time.Time
}

// buildConversationsResponse assembles the /api/conversations payload (project +
// grouped-topic list with per-topic channel tags). Used by the control-API
// list_conversations request.
func buildConversationsResponse(store StoreRepo) webConversationsResponse {
	active := store.GetActive()
	projects := store.ListProjects()
	names := make([]string, 0, len(projects))
	for name := range projects {
		names = append(names, name)
	}
	sort.Strings(names)

	resp := webConversationsResponse{Active: active, Projects: make([]webProjectTopics, 0, len(names))}
	for _, name := range names {
		p := projects[name]
		item := webProjectTopics{Name: name, Path: p.Path}
		item.Conversations = webTopicsForProject(name, p.Conversations, active)
		resp.Projects = append(resp.Projects, item)
	}
	if tc := store.TelegramConversation(); tc != nil {
		resp.Telegram = &webTelegramEntry{
			Title:   tc.Title,
			ID:      tc.ID,
			Backend: tc.Backend,
			Project: store.TelegramActiveProject(),
		}
	}

	webConvs := store.ListWebConvs()
	ids := make([]string, 0, len(webConvs))
	for id := range webConvs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return webConvs[ids[i]].LastActivity.After(webConvs[ids[j]].LastActivity) })
	resp.WebConvs = make([]webWebConv, 0, len(ids))
	for _, id := range ids {
		c := webConvs[id]
		resp.WebConvs = append(resp.WebConvs, webWebConv{
			ID: c.ID, Title: c.Title, WorkDir: c.WorkDir,
			Active:  active.Project == "" && active.ConversationID == c.ID,
			Backend: c.Backend,
		})
	}
	return resp
}

type historyTurn struct {
	Role string `json:"role"` // "user" | "assistant"
	Text string `json:"text"`
}
type historyResponse struct {
	Turns []historyTurn `json:"turns"`
}

// buildHistoryResponse expands a conversation's stored turns into a flat
// user/assistant sequence for the web log. Used by the control-API get_history
// request.
func buildHistoryResponse(store StoreRepo, tgt Target) historyResponse {
	resp := historyResponse{Turns: []historyTurn{}}
	// Take a copy of the history under the store lock instead of reaching into
	// a live *Conversation — a worker may concurrently be appending to (and
	// reslicing) the same conversation's History slice.
	turns := store.HistorySnapshot(tgt)
	for _, turn := range turns {
		if turn.Prompt != "" {
			resp.Turns = append(resp.Turns, historyTurn{Role: "user", Text: turn.Prompt})
		}
		if turn.Response != "" {
			resp.Turns = append(resp.Turns, historyTurn{Role: "assistant", Text: turn.Response})
		}
	}
	return resp
}

func webTopicsForProject(project string, convs map[string]*Conversation, active ActiveRef) []webConversationTopic {
	groups := map[string]*webConversationGroup{}
	for _, id := range sortedConvIDsByActivity(convs) {
		c := convs[id]
		rootID := webConversationRootID(convs, c)
		root := convs[rootID]
		if root == nil {
			root = c
		}
		g := groups[rootID]
		if g == nil {
			g = &webConversationGroup{root: root, selected: c, last: c.LastActivity}
			groups[rootID] = g
		}
		if c.LastActivity.After(g.last) {
			g.last = c.LastActivity
		}
		g.started = g.started || c.Started

		isActive := project == active.Project && c.ID == active.ConversationID
		if isActive {
			g.active = true
			g.selected = c
			continue
		}
		if !g.active && c.LastActivity.After(g.selected.LastActivity) {
			g.selected = c
		}
	}

	ordered := make([]*webConversationGroup, 0, len(groups))
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].last.After(ordered[j].last)
	})

	topics := make([]webConversationTopic, 0, len(ordered))
	for _, g := range ordered {
		c := g.selected
		if c == nil {
			continue
		}
		title := c.Title
		if g.root != nil && g.root.Title != "" {
			title = g.root.Title
		}
		lastActivity := ""
		if !g.last.IsZero() {
			lastActivity = g.last.UTC().Format(time.RFC3339)
		}
		topics = append(topics, webConversationTopic{
			ID:           c.ID,
			Title:        title,
			Summary:      c.Summary,
			Started:      g.started,
			Backend:      c.Backend,
			Active:       g.active,
			LastActivity: lastActivity,
			Channel:      conversationChannel(g.root),
		})
	}
	return topics
}

func webConversationRootID(convs map[string]*Conversation, c *Conversation) string {
	if c == nil {
		return ""
	}
	rootID := c.ID
	seen := map[string]bool{}
	cur := c
	for cur != nil && cur.ParentID != "" {
		if seen[cur.ID] {
			break
		}
		seen[cur.ID] = true
		parent, ok := convs[cur.ParentID]
		if !ok {
			break
		}
		rootID = parent.ID
		cur = parent
	}
	return rootID
}

// resolveWebOwner picks the chatID web/control actions run as: the explicit
// config value, else the first allowed user ID; ok=false when neither is set.
func resolveWebOwner(ownerCfg int64, allowed []int64) (int64, bool) {
	if ownerCfg != 0 {
		return ownerCfg, true
	}
	if len(allowed) > 0 {
		return allowed[0], true
	}
	return 0, false
}

// versionPayload builds the running-vs-latest-source version report. "latest" is
// computed live from the binary's own source dir (git commit count of HEAD).
// Used by the control-API get_version relay. backend, when non-empty, is the
// active runner backend.
func versionPayload(backend string) map[string]any {
	p := map[string]any{
		"version":     runningVersion(),
		"commit":      buildCommit,
		"buildTime":   buildTime,
		"commitCount": atoiOr(buildCommitCount, 0),
	}
	if backend != "" {
		p["backend"] = backend
	}
	if exe, err := os.Executable(); err == nil {
		srcDir := filepath.Dir(exe)
		if latest := gitCommitCount(srcDir); latest != "" {
			p["latestCommitCount"] = atoiOr(latest, 0)
			p["latestVersion"] = formatVersion(latest)
			p["latestCommit"] = gitShortCommit(srcDir)
			if buildCommitCount != "" {
				p["updateAvailable"] = atoiOr(latest, 0) > atoiOr(buildCommitCount, 0)
			}
		}
	}
	return p
}

// readMaskedConfig reads cfgPath and masks its secret values for display. Used
// by the control-API get_config relay.
func readMaskedConfig(cfgPath string, cfg *Config) ([]byte, error) {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	return maskConfigSecrets(raw, cfg), nil
}

// writeValidatedConfig restores masked secrets in body, validates it, and — only
// if valid — writes it to cfgPath (the fixed path; never client-supplied). A
// non-nil error means nothing was written. Used by the control-API set_config
// relay.
func writeValidatedConfig(cfgPath string, cfg *Config, body []byte) error {
	restored := restoreConfigSecrets(body, cfg)
	if _, verr := unmarshalConfigYAML(restored); verr != nil {
		return verr
	}
	return os.WriteFile(cfgPath, restored, 0o600)
}
