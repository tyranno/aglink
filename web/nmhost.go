package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

// Native messaging transport. Chrome starts `aglink-web nm-host <origin>` when
// the extension calls chrome.runtime.connectNative("com.aglink.web") and keeps
// it for as long as the port is open — which also keeps the MV3 service worker
// alive. The host is a pipe: it dials the daemon's /ext endpoint exactly as the
// extension's own WebSocket would, then copies frames both ways. The daemon
// cannot tell the two transports apart.

const nmHostName = "com.aglink.web"

// nmMaxToChrome is Chrome's limit on one message from a native host.
const nmMaxToChrome = 1 << 20

// nmMaxFromChrome bounds what we will allocate for one message from Chrome
// (Chrome allows up to 64 MiB; a screenshot reply is a few MB).
const nmMaxFromChrome = 64 << 20

// readNMMessage reads one Chrome native-messaging frame: a 32-bit length in
// native byte order (little-endian on every platform Chrome runs this on),
// then that many bytes of UTF-8 JSON.
func readNMMessage(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n > nmMaxFromChrome {
		return nil, fmt.Errorf("native message of %d bytes exceeds %d", n, nmMaxFromChrome)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

var errNMTooLarge = errors.New("message exceeds Chrome's 1 MB native messaging limit")

// writeNMMessage writes one frame to Chrome, refusing what Chrome would reject.
func writeNMMessage(w io.Writer, msg []byte) error {
	if len(msg) > nmMaxToChrome {
		return errNMTooLarge
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(msg)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(msg)
	return err
}

// nmHello is the extension's first message: who it is, so the host can open
// the same /ext handshake the WebSocket transport would.
type nmHello struct {
	Type    string `json:"type"`
	Account string `json:"account"`
	Boot    string `json:"boot"`
	Ext     string `json:"ext"` // extension's build commit (manifest.json version_name)
}

// nmDialer opens the daemon connection for an extension that said hello.
type nmDialer func(account, boot string) (*websocket.Conn, error)

// relayNM runs one native-messaging session: hello, dial, then copy until
// either side closes. Control messages to the extension are {"type":"ready"}
// once the daemon is reached and {"type":"error","error":…} if it is not.
func relayNM(in io.Reader, out io.Writer, dial nmDialer) error {
	first, err := readNMMessage(in)
	if err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	var hello nmHello
	if err := json.Unmarshal(first, &hello); err != nil || hello.Type != "hello" || strings.TrimSpace(hello.Account) == "" {
		msg, _ := json.Marshal(map[string]string{"type": "error", "error": "expected hello with account"})
		_ = writeNMMessage(out, msg)
		return fmt.Errorf("bad hello: %q", first)
	}
	if buildCommit != "" && hello.Ext != "" && hello.Ext != buildCommit {
		msg, _ := json.Marshal(map[string]string{"type": "reload"})
		_ = writeNMMessage(out, msg)
		return fmt.Errorf("extension build %q != daemon build %q; told it to reload", hello.Ext, buildCommit)
	}
	conn, err := dial(hello.Account, hello.Boot)
	if err != nil {
		msg, _ := json.Marshal(map[string]string{"type": "error", "error": err.Error()})
		_ = writeNMMessage(out, msg)
		return err
	}
	defer conn.Close()
	ready, _ := json.Marshal(map[string]string{"type": "ready"})
	if err := writeNMMessage(out, ready); err != nil {
		return err
	}

	var wsMu sync.Mutex // the oversize reply below and the stdin pump both write
	wsWrite := func(b []byte) error {
		wsMu.Lock()
		defer wsMu.Unlock()
		return conn.WriteMessage(websocket.TextMessage, b)
	}

	daemonDone := make(chan error, 1)
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				daemonDone <- fmt.Errorf("daemon connection: %w", err)
				return
			}
			if err := writeNMMessage(out, data); err != nil {
				if !errors.Is(err, errNMTooLarge) {
					daemonDone <- fmt.Errorf("write to Chrome: %w", err)
					return
				}
				// Answer for the extension: it can never receive this request.
				var req Request
				_ = json.Unmarshal(data, &req)
				rep, _ := json.Marshal(Reply{ID: req.ID, Error: fmt.Sprintf("request of %d bytes is too large for the native messaging transport (1 MB)", len(data))})
				_ = wsWrite(rep)
			}
		}
	}()

	chromeDone := make(chan error, 1)
	go func() {
		for {
			msg, err := readNMMessage(in)
			if err != nil {
				chromeDone <- err
				return
			}
			if err := wsWrite(msg); err != nil {
				chromeDone <- err
				return
			}
		}
	}()

	select {
	case err := <-daemonDone:
		return err
	case err := <-chromeDone:
		if errors.Is(err, io.EOF) {
			return nil // Chrome closed the port
		}
		return err
	}
}

// runNMHost is the `nm-host` subcommand Chrome launches. Chrome passes the
// caller's origin (chrome-extension://<id>/) as the first argument, and on
// Windows a --parent-window flag after it.
func runNMHost(args []string) {
	log.SetOutput(os.Stderr) // stdout is the Chrome channel
	origin := ""
	for _, a := range args {
		if strings.HasPrefix(a, "chrome-extension://") {
			origin = strings.TrimSuffix(a, "/")
		}
	}
	if origin == "" {
		fmt.Fprintln(os.Stderr, "nm-host is started by Chrome (native messaging host "+nmHostName+")")
		os.Exit(2)
	}
	dial := func(account, boot string) (*websocket.Conn, error) {
		port, err := ensureDaemon()
		if err != nil {
			return nil, err
		}
		q := url.Values{"account": {account}, "boot": {boot}}
		u := fmt.Sprintf("ws://127.0.0.1:%d/ext?%s", port, q.Encode())
		hdr := http.Header{}
		hdr.Set("Origin", origin)
		conn, _, err := websocket.DefaultDialer.Dial(u, hdr)
		return conn, err
	}
	if err := relayNM(os.Stdin, os.Stdout, dial); err != nil {
		log.Printf("aglink-web nm-host: %v", err)
		os.Exit(1)
	}
}
