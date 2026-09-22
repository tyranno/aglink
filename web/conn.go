package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/gorilla/websocket"
)

// The Chrome extension and the VS Code extension talk to the daemon the same
// way: they connect in, the daemon sends a Request, they answer with a Reply.
// These two functions are that shared machinery, so a second kind of
// connection gets the keepalive, reply routing and failure handling the first
// one already had, instead of a copy that drifts.

// serveConn runs one connection until it drops: keepalive pings, routing each
// Reply to the call waiting on it, and on exit failing only the calls parked on
// THIS connection. unregister runs under d.mu and removes ec from whichever
// registry holds it. label names the kind in log lines.
func (d *Daemon) serveConn(ec *extConn, label string, unregister func()) {
	// Keepalive: push an application-level ping every pingInterval. Received WS
	// messages reset Chrome's MV3 service-worker idle timer (Chrome 116+), so
	// this keeps the extension's worker from being terminated; the peer answers
	// each ping, and that reply refreshes our read deadline below. If either
	// side dies, no replies arrive, the deadline fires, and we tear the stale
	// connection down instead of letting commands hang.
	done := make(chan struct{})
	go d.pingLoop(ec, done)

	conn := ec.conn
	_ = conn.SetReadDeadline(time.Now().Add(d.readTimeout))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("aglink-web: %s read ended: %v", label, err)
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(d.readTimeout))
		var rep Reply
		if err := json.Unmarshal(data, &rep); err != nil {
			log.Printf("aglink-web: bad reply frame: %v", err)
			continue
		}
		if rep.ID == 0 {
			continue // keepalive ack — nothing is waiting on id 0
		}
		d.mu.Lock()
		ch := d.pending[rep.ID]
		delete(d.pending, rep.ID)
		d.mu.Unlock()
		if ch != nil {
			ch <- rep // buffered cap 1, sole sender for this id — never blocks
		}
	}

	close(done)
	d.mu.Lock()
	ec.gone = true
	unregister()
	// Fail only the calls parked on THIS connection, instead of making them wait
	// out the full call timeout. Another connection's pending calls are still
	// perfectly answerable and must not be collateral damage.
	for id := range ec.waiting {
		if ch := d.pending[id]; ch != nil {
			ch <- Reply{ID: id, Error: label + " connection lost"}
			delete(d.pending, id)
		}
	}
	d.mu.Unlock()
	_ = conn.Close()
}

// roundTrip sends one request on ec and waits up to timeout for its reply.
// noun names the peer in the timeout message ("browser did not respond…").
func (d *Daemon) roundTrip(ec *extConn, method string, params map[string]any, timeout time.Duration, noun string) CallResult {
	d.mu.Lock()
	if ec.gone {
		d.mu.Unlock()
		return CallResult{Error: noun + " connection lost"}
	}
	d.nextID++
	id := d.nextID
	ch := make(chan Reply, 1)
	d.pending[id] = ch
	ec.waiting[id] = struct{}{}
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		delete(d.pending, id)
		delete(ec.waiting, id)
		d.mu.Unlock()
	}()

	data, err := json.Marshal(Request{ID: id, Method: method, Params: params})
	if err != nil {
		return CallResult{Error: fmt.Sprintf("marshal request: %v", err)}
	}
	ec.writeMu.Lock()
	err = ec.conn.WriteMessage(websocket.TextMessage, data)
	ec.writeMu.Unlock()
	if err != nil {
		return CallResult{Error: fmt.Sprintf("send to %s: %v", noun, err)}
	}
	log.Printf("aglink-web: → %s #%d %s", ec.account, id, method)

	select {
	case rep := <-ch:
		if !rep.OK {
			log.Printf("aglink-web: ← #%d error: %s", id, rep.Error)
			return CallResult{Error: rep.Error}
		}
		log.Printf("aglink-web: ← #%d ok (%d bytes)", id, len(rep.Text))
		return CallResult{OK: true, Text: rep.Text}
	case <-time.After(timeout):
		log.Printf("aglink-web: ✗ #%d %s timed out", id, method)
		return CallResult{Error: noun + " did not respond within timeout"}
	}
}
