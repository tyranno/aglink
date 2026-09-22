package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// cdpMsg is one Chrome DevTools Protocol frame. A response carries ID and
// Result/Error; an event carries Method and Params and no ID.
type cdpMsg struct {
	ID     int64           `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *cdpError       `json:"error,omitempty"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// cdpConn is a minimal CDP client over one page's WebSocket: request/response
// correlation by id, and a fan-out for events. It is all this package needs to
// drive an Electron or Wails window, so it stays this small instead of pulling
// in a full CDP library.
type cdpConn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan cdpMsg
	subs    []func(method string, params json.RawMessage)

	done    chan struct{}
	closeMu sync.Once
	err     error // why the connection ended; set before done closes
}

var errCDPClosed = errors.New("CDP connection closed")

// dialCDP connects to a page's webSocketDebuggerUrl. Only loopback is
// accepted: a debugging port hands over the entire app, and nothing on another
// machine should ever be driven through here.
func dialCDP(wsURL string) (*cdpConn, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, fmt.Errorf("bad CDP url: %w", err)
	}
	if u.Scheme != "ws" {
		return nil, fmt.Errorf("CDP url must be ws://, got %q", u.Scheme)
	}
	if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
		if u.Hostname() != "localhost" {
			return nil, fmt.Errorf("refusing non-loopback CDP endpoint %q", u.Host)
		}
	}
	d := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	ws, _, err := d.Dial(wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("connect CDP: %w", err)
	}
	// Page payloads (outerHTML, screenshots) routinely exceed the default.
	ws.SetReadLimit(64 << 20)
	c := &cdpConn{ws: ws, pending: make(map[int64]chan cdpMsg), done: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

func (c *cdpConn) readLoop() {
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			c.shutdown(err)
			return
		}
		var m cdpMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.ID != 0 {
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		if m.Method != "" {
			c.mu.Lock()
			subs := append([]func(string, json.RawMessage){}, c.subs...)
			c.mu.Unlock()
			for _, fn := range subs {
				fn(m.Method, m.Params)
			}
		}
	}
}

// shutdown fails every waiting caller at once. A dead window must not make
// its callers sit out their timeouts.
func (c *cdpConn) shutdown(err error) {
	c.closeMu.Do(func() {
		c.mu.Lock()
		c.err = err
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
		close(c.done)
		_ = c.ws.Close()
	})
}

// Call sends one method and waits for its result.
func (c *cdpConn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return nil, errCDPClosed
	default:
	}
	c.nextID++
	id := c.nextID
	ch := make(chan cdpMsg, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	frame := map[string]any{"id": id, "method": method}
	if params != nil {
		frame["params"] = params
	}
	data, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	c.writeMu.Lock()
	err = c.ws.WriteMessage(websocket.TextMessage, data)
	c.writeMu.Unlock()
	if err != nil {
		c.shutdown(err)
		return nil, fmt.Errorf("send %s: %w", method, err)
	}

	select {
	case m, ok := <-ch:
		if !ok {
			return nil, errCDPClosed
		}
		if m.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, m.Error.Message)
		}
		return m.Result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// OnEvent registers fn for every event on this connection. fn runs on the read
// goroutine, so it must not block.
func (c *cdpConn) OnEvent(fn func(method string, params json.RawMessage)) {
	c.mu.Lock()
	c.subs = append(c.subs, fn)
	c.mu.Unlock()
}

// Done closes when the connection ends for any reason.
func (c *cdpConn) Done() <-chan struct{} { return c.done }

func (c *cdpConn) Close() error {
	c.shutdown(errCDPClosed)
	return nil
}
