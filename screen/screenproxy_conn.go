package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// runScreenProxy serves the screen MCP tools over stdio by transparently
// forwarding to the screen HTTP server on the client PC. It is how a remote
// worker — where SSH_CONNECTION is set and there is no local Win32 screen —
// drives the screen on the PC it is driven from, with no configuration: it reads
// the client IP from SSH_CONNECTION itself, so no shared reverse-tunnel port and
// no per-session value are involved.
//
// The PC is not assumed to be there for the whole session: it may be asleep
// when the session starts, its screen server may restart, the VS Code window may
// be reconnecting. So the proxy never exits over it. It starts with the tools it
// saw last time (a cache file) when the PC does not answer, keeps trying in the
// background, reconnects on demand, and re-reads the tool list after a
// reconnect so tools added on the PC show up (tools/list_changed).
func runScreenProxy(host, token string) error {
	port, err := screenRemotePort()
	if err != nil {
		return err
	}
	p := newScreenProxy("http://"+net.JoinHostPort(host, port)+"/mcp", token)
	ctx := context.Background()

	ictx, cancel := context.WithTimeout(ctx, proxyStartupWait)
	c, err := p.connect(ictx)
	if err == nil {
		err = p.syncTools(ictx, c)
	}
	cancel()
	if err != nil {
		if !p.loadCachedTools() {
			p.setPlaceholder(err)
		}
		go p.reconnectLoop(ctx)
	}
	return serveStdio(p.srv)
}

const (
	// proxyStartupWait bounds the first connect, so the MCP client is not kept
	// waiting at session start while the PC is away.
	proxyStartupWait = 8 * time.Second
	// proxyRetryWindow is how long one tool call keeps retrying a PC that does
	// not answer before it reports the failure.
	proxyRetryWindow = 30 * time.Second
	// proxyShortRetry replaces it right after a call already waited it out.
	proxyShortRetry = 8 * time.Second
	// proxyCallTimeout bounds one call end to end, a little past the PC
	// relay's own 600s limit.
	proxyCallTimeout = 11 * time.Minute
	// proxyStatusTool stands in for the real tools while the PC has never been
	// reached and no tool list is cached.
	proxyStatusTool = "screen_connection_status"
)

type screenProxy struct {
	base string
	opts []transport.StreamableHTTPCOption
	srv  *server.MCPServer

	mu sync.Mutex // guards c
	c  *client.Client

	toolsMu   sync.Mutex
	toolsJSON string // tool list last registered, to skip no-op updates
	syncing   bool

	gaveMu   sync.Mutex // guards gaveUpAt
	gaveUpAt time.Time  // when a call last used up its retry window
}

func newScreenProxy(base, token string) *screenProxy {
	p := &screenProxy{base: base}
	if token != "" {
		p.opts = append(p.opts, transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
	}
	p.srv = server.NewMCPServer("screen", "0.1.0", server.WithToolCapabilities(true))
	return p
}

// connect returns the live client, making and initializing a new one if there
// is none. Concurrent callers share one attempt (the mutex).
func (p *screenProxy) connect(ctx context.Context) (*client.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.c != nil {
		return p.c, nil
	}
	c, err := client.NewStreamableHttpClient(p.base, p.opts...)
	if err != nil {
		return nil, fmt.Errorf("screen proxy: new client for %s: %w", p.base, err)
	}
	// The client outlives the call that made it: started with that call's
	// context it would be cancelled the moment the call returned, and every
	// later call would fail on a dead client.
	if err := c.Start(context.WithoutCancel(ctx)); err != nil {
		return nil, fmt.Errorf("screen proxy: start: %w", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "aglink-screen-proxy", Version: "0.2.0"}
	ictx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := c.Initialize(ictx, initReq); err != nil {
		c.Close()
		return nil, p.explain(err)
	}
	p.c = c
	return c, nil
}

// drop forgets c after a failure, so the next call makes a fresh client. It
// does not Close c: other calls may still be running on it (a tool list
// refresh failing must not cancel a click in flight), and against the
// stateless screen server a client holds no session, only idle HTTP
// connections that are let go with it.
func (p *screenProxy) drop(c *client.Client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.c == c && c != nil {
		p.c = nil
	}
}

func isUnauthorized(err error) bool {
	return errors.Is(err, transport.ErrUnauthorized) || strings.Contains(err.Error(), "status 401")
}

// tidyErr drops the doubled prefix the MCP client library puts on transport
// failures ("failed to send request: failed to send request: ...").
func tidyErr(err error) string {
	s := err.Error()
	const dup = "failed to send request: failed to send request: "
	return strings.Replace(s, dup, "failed to send request: ", 1)
}

// tidied shows an error through tidyErr but still unwraps to it.
type tidied struct{ err error }

func (t tidied) Error() string { return tidyErr(t.err) }
func (t tidied) Unwrap() error { return t.err }

// explain wraps a failure to reach the PC with what to do about it.
func (p *screenProxy) explain(err error) error {
	if strings.HasPrefix(err.Error(), "screen proxy:") {
		return err
	}
	err = tidied{err}
	bridged := screenBridgeToken() != ""
	switch {
	case isUnauthorized(err) && bridged:
		return fmt.Errorf("screen proxy: the VS Code bridge at %s refused this session's token (401) — the window was reloaded with a new token. Start the agent again from this window or a new integrated terminal: %w", p.base, err)
	case isUnauthorized(err):
		return fmt.Errorf("screen proxy: the screen server at %s refused the token (401) — AGLINK_SCREEN_REMOTE_TOKEN must match the PC's: %w", p.base, err)
	case bridged:
		return fmt.Errorf("screen proxy: cannot reach the PC through the VS Code bridge at %s (window closed or reconnecting, PC asleep?): %w", p.base, err)
	}
	return fmt.Errorf("screen proxy: cannot reach the screen server at %s (is the PC on and aglink-screen serve running there?): %w", p.base, err)
}

// retryable reports a failure where the request never reached the PC's screen
// server, so sending it again cannot repeat an action: the connection could not
// be made, or the bridge / relay said the PC side is not reachable. Anything
// after the connection was made (a reset, a timeout, "mid-answer") is not
// retried: the request was on its way and the action may have run.
func retryable(err error) bool {
	if err == nil || isUnauthorized(err) {
		return false
	}
	s := err.Error()
	// mid-answer: the call may have run. 413: the bridge refused the body
	// outright, and sending it again cannot help.
	if strings.Contains(s, "mid-answer") || strings.Contains(s, "status 413") || strings.Contains(s, "over the") && strings.Contains(s, "-byte limit") {
		return false
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	for _, m := range []string{"connection refused", "no route to host", "network is unreachable",
		"status 502", "status 503", "status 504", "the PC side is not reachable", "not reachable", "could not be started"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// call forwards one tool call, reconnecting and retrying for proxyRetryWindow
// while the PC does not answer.
func (p *screenProxy) call(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Bounded even when the MCP client never cancels: a link that died
	// silently would otherwise hold the call (and this process) for ever.
	ctx, cancel := context.WithTimeout(ctx, proxyCallTimeout)
	defer cancel()
	window := proxyRetryWindow
	if p.recentlyGaveUp() {
		// The last call already waited out the whole window (a bridge port
		// that moved, a PC that is off): don't make every call wait 30s again.
		window = proxyShortRetry
	}
	deadline := time.Now().Add(window)
	wait := 500 * time.Millisecond
	reconnected := false
	for {
		c, err := p.connect(ctx)
		if err == nil {
			var res *mcp.CallToolResult
			res, err = c.CallTool(ctx, req)
			if err == nil {
				if reconnected {
					// A new connection may lead to a newer screen server.
					p.syncLater()
				}
				p.setGaveUp(false)
				return res, nil
			}
			if !retryable(err) {
				if isUnauthorized(err) {
					return nil, p.explain(err)
				}
				// The request was on its way when the link broke: it may have
				// run on the PC, so say so instead of "cannot reach".
				p.drop(c)
				return mcp.NewToolResultError(fmt.Sprintf("screen proxy: the connection to the PC broke while %s was in progress, so it may or may not have run there. Check the screen (snapshot/list_windows) before repeating it: %v", req.Params.Name, tidyErr(err))), nil
			}
			p.drop(c)
		} else if !retryable(err) {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if time.Now().Add(wait).After(deadline) {
			p.setGaveUp(true)
			return mcp.NewToolResultError(fmt.Sprintf("%v (retried for %s)", p.explain(err), window)), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		// Capped low: the PC coming back should be noticed within a second or
		// two, and a refused dial costs nothing.
		if wait < 1500*time.Millisecond {
			wait *= 2
		}
		reconnected = true
	}
}

// syncLater re-reads the tool list in the background, one at a time.
func (p *screenProxy) syncLater() {
	p.toolsMu.Lock()
	if p.syncing {
		p.toolsMu.Unlock()
		return
	}
	p.syncing = true
	p.toolsMu.Unlock()
	go func() {
		defer func() { p.toolsMu.Lock(); p.syncing = false; p.toolsMu.Unlock() }()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if c, err := p.connect(ctx); err == nil {
			_ = p.syncTools(ctx, c)
		}
	}()
}

// reconnectLoop keeps trying the PC until it answers, then registers its
// tools. Used when the session started while the PC was away.
func (p *screenProxy) reconnectLoop(ctx context.Context) {
	for wait := 2 * time.Second; ; {
		time.Sleep(wait)
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		c, err := p.connect(cctx)
		if err == nil {
			err = p.syncTools(cctx, c)
		}
		cancel()
		if err == nil {
			return
		}
		if wait < 30*time.Second {
			wait *= 2
		}
	}
}

// syncTools lists the PC's tools and registers them, replacing what is there
// (the placeholder, a stale cache) and notifying the MCP client when the list
// actually changed. A successful list is cached for the next session.
func (p *screenProxy) syncTools(ctx context.Context, c *client.Client) error {
	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		p.drop(c)
		return fmt.Errorf("screen proxy: list tools: %w", err)
	}
	if len(res.Tools) == 0 {
		return fmt.Errorf("screen proxy: the PC's screen server lists no tools")
	}
	if b, err := json.Marshal(res); err == nil {
		p.saveCache(b)
	}
	p.register(res.Tools)
	return nil
}

// register replaces the served tools with tools, each forwarded through call.
func (p *screenProxy) register(tools []mcp.Tool) {
	b, _ := json.Marshal(tools)
	p.toolsMu.Lock()
	defer p.toolsMu.Unlock()
	if string(b) == p.toolsJSON {
		return
	}
	p.toolsJSON = string(b)
	entries := make([]server.ServerTool, 0, len(tools))
	for _, t := range tools {
		name := t.Name
		entries = append(entries, server.ServerTool{Tool: t, Handler: func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			req.Params.Name = name
			return p.call(ctx, req)
		}})
	}
	p.srv.SetTools(entries...)
}

// setPlaceholder registers a single status tool while there is nothing better:
// calling it tries the PC again and, once it answers, brings in the real tools.
func (p *screenProxy) setPlaceholder(cause error) {
	tool := mcp.NewTool(proxyStatusTool,
		mcp.WithDescription("The PC's screen tools are not loaded yet: the PC (its aglink-screen server) could not be reached when this session started, and it is being retried in the background. Call this to try again now; once the PC answers, the screen tools appear. Startup error: "+cause.Error()))
	p.toolsMu.Lock()
	p.toolsJSON = ""
	p.toolsMu.Unlock()
	p.srv.SetTools(server.ServerTool{Tool: tool, Handler: func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		c, err := p.connect(cctx)
		if err == nil {
			err = p.syncTools(cctx, c)
		}
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("Connected to the PC — the screen tools are loaded now (the tool list was refreshed)."), nil
	}})
}

// cachePath is where the last tool list seen is kept, per PC route (the base
// URL), so a session started while the PC is away still has its tools.
func (p *screenProxy) cachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	h := fnv.New32a()
	h.Write([]byte(p.base))
	return filepath.Join(dir, "aglink", fmt.Sprintf("screen-proxy-tools-%08x.json", h.Sum32()))
}

func (p *screenProxy) saveCache(b []byte) {
	path := p.cachePath()
	if path == "" || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	// A temp file of its own: sessions of one window share the cache path.
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil && cerr == nil && os.Rename(f.Name(), path) == nil {
		return
	}
	_ = os.Remove(f.Name())
}

// recentlyGaveUp reports a call that used up its whole retry window within
// the last minute, with no success since.
func (p *screenProxy) recentlyGaveUp() bool {
	p.gaveMu.Lock()
	defer p.gaveMu.Unlock()
	return !p.gaveUpAt.IsZero() && time.Since(p.gaveUpAt) < time.Minute
}

func (p *screenProxy) setGaveUp(v bool) {
	p.gaveMu.Lock()
	defer p.gaveMu.Unlock()
	if v {
		p.gaveUpAt = time.Now()
	} else {
		p.gaveUpAt = time.Time{}
	}
}

// loadCachedTools registers the cached tool list, falling back to the newest
// cache of any route when this one has none (bridge ports differ per window).
func (p *screenProxy) loadCachedTools() bool {
	own := p.cachePath()
	if own == "" {
		return false
	}
	paths := []string{own}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(own), "screen-proxy-tools-*.json")); len(m) > 0 {
		sort.Slice(m, func(i, j int) bool { return modTime(m[i]).After(modTime(m[j])) })
		paths = append(paths, m...)
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var res mcp.ListToolsResult
		if json.Unmarshal(b, &res) != nil || len(res.Tools) == 0 {
			continue
		}
		p.register(res.Tools)
		return true
	}
	return false
}

func modTime(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}
