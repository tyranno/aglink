package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Optional access token for the daemon's call endpoints.
//
// The daemon listens on loopback, which keeps web pages and other machines out
// — until an SSH reverse tunnel (RemoteForward 48219) carries the port to a
// remote box. Then every user and process there can reach /mcp, and /call too,
// since the tunnel forwards the whole port: they can drive this PC's Chrome,
// app windows and VS Code.
//
// `aglink-web token` creates <dataDir>/aglink-web.token. While that file
// exists, /call and /mcp answer only requests carrying
// "Authorization: Bearer <token>". Local callers (the stdio bridge, `cmd`) read
// the same file and add the header themselves, so nothing changes for them; a
// remote session is registered once with the header. Without the file nothing
// is required — so turning it on is a deliberate step and no existing remote
// registration breaks unannounced.

const tokenFileName = "aglink-web.token"

func tokenFilePath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, tokenFileName), nil
}

var tokenCache struct {
	sync.Mutex
	value   string
	readAt  time.Time
	present bool
}

// accessToken returns the configured token, or "" when none is set. Re-read
// every few seconds so creating or removing the file takes effect without
// restarting the daemon.
func accessToken() string {
	tokenCache.Lock()
	defer tokenCache.Unlock()
	if time.Since(tokenCache.readAt) < 3*time.Second {
		return tokenCache.value
	}
	tokenCache.readAt = time.Now()
	tokenCache.value = ""
	p, err := tokenFilePath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	tokenCache.value = strings.TrimSpace(string(b))
	return tokenCache.value
}

func forgetTokenCache() {
	tokenCache.Lock()
	tokenCache.readAt = time.Time{}
	tokenCache.Unlock()
}

// requireToken guards h with the access token, when one is configured.
func requireToken(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := accessToken()
		if want != "" {
			got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"aglink-web: access token required (Authorization: Bearer …) — see 'aglink-web token'"}`))
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

// addTokenHeader is what local callers use to pass the token along.
func addTokenHeader(req *http.Request) {
	if t := accessToken(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
}

// runTokenCmd: aglink-web token [--off]
func runTokenCmd(args []string) {
	p, err := tokenFilePath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "token:", err)
		os.Exit(1)
	}
	if len(args) > 0 && (args[0] == "--off" || args[0] == "off") {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "token:", err)
			os.Exit(1)
		}
		fmt.Println("Access token removed — /call and /mcp are open to loopback again.")
		return
	}
	tok := ""
	if b, err := os.ReadFile(p); err == nil {
		tok = strings.TrimSpace(string(b))
	}
	if tok == "" {
		buf := make([]byte, 24)
		if _, err := rand.Read(buf); err != nil {
			fmt.Fprintln(os.Stderr, "token:", err)
			os.Exit(1)
		}
		tok = hex.EncodeToString(buf)
		if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "token:", err)
			os.Exit(1)
		}
		fmt.Println("Access token created at", p)
	} else {
		fmt.Println("Access token already set at", p)
	}
	fmt.Println("From now on /call and /mcp require it. Local use (Claude on this PC, aglink-web cmd) picks it up by itself.")
	fmt.Println("On a remote machine reaching this PC through the SSH tunnel, register the server with the header:")
	fmt.Printf("  claude mcp add --transport http --scope user aglink-web-remote http://127.0.0.1:%d/mcp --header \"Authorization: Bearer %s\"\n", defaultPort, tok)
	fmt.Println("To turn it off: aglink-web token --off")
}
