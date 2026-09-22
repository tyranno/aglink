package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// VS Code windows connect on /vscode, one connection per window, from the
// aglink-vscode extension (see ../vscode). They are addressed as
//
//	vscode:<workspace>[@<remote host>]   e.g. vscode:backend@192-168-123-146-doowon
//
// and are kept in their own registry, apart from Chrome profiles: a window must
// never be picked as the default target for a web tool.

// vscodeWindow is what a window says about itself when it connects.
type vscodeWindow struct {
	Name    string // workspace name
	Remote  string // remote host (e.g. an SSH alias); "" for a local window
	Folder  string // first workspace folder path
	Session string // vscode.env.sessionId — distinguishes two windows on one folder
}

func vscodeProfileName(w vscodeWindow) string {
	name := "no-folder"
	if strings.TrimSpace(w.Name) != "" {
		name = appSlug(w.Name)
	}
	if strings.TrimSpace(w.Remote) == "" {
		return "vscode:" + name
	}
	return "vscode:" + name + "@" + appSlug(w.Remote)
}

func isVSCodeProfile(name string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "vscode:")
}

// handleVSCode accepts one window's connection and serves it until it drops.
// Loopback only: the extension runs on this machine (it is a UI extension, so
// it stays here even for a Remote-SSH window).
func (d *Daemon) handleVSCode(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	win := vscodeWindow{
		Name:    q.Get("name"),
		Remote:  q.Get("remote"),
		Folder:  q.Get("folder"),
		Session: q.Get("session"),
	}
	conn, err := d.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	d.mu.Lock()
	key := d.vscodeKeyLocked(win)
	if old := d.vscodes[key]; old != nil {
		_ = old.conn.Close() // the same window reconnecting — newest wins
	}
	ec := &extConn{conn: conn, account: key, since: time.Now(), waiting: make(map[uint64]struct{})}
	d.vscodes[key] = ec
	d.vscodeInfo[key] = win
	n := len(d.vscodes)
	d.mu.Unlock()
	log.Printf("aglink-web: VS Code window registered as %s (%d window(s))", key, n)

	d.serveConn(ec, "VS Code window", func() {
		if d.vscodes[key] == ec {
			delete(d.vscodes, key)
			delete(d.vscodeInfo, key)
		}
	})
	log.Printf("aglink-web: VS Code window disconnected (%s)", key)
}

// vscodeKeyLocked picks the registry key for a window: its profile name, or
// the key it already holds if this is the same window (same session)
// reconnecting, or the name with #2, #3… when another window already has it.
// Caller holds d.mu.
func (d *Daemon) vscodeKeyLocked(win vscodeWindow) string {
	base := vscodeProfileName(win)
	for key, info := range d.vscodeInfo {
		if win.Session != "" && info.Session == win.Session {
			return key
		}
	}
	if _, taken := d.vscodes[base]; !taken {
		return base
	}
	for i := 2; ; i++ {
		k := fmt.Sprintf("%s#%d", base, i)
		if _, taken := d.vscodes[k]; !taken {
			return k
		}
	}
}

// resolveVSCode picks a window by name: exact, else a unique prefix. There is
// no default — the calling session's own window is connected too, and landing
// on the wrong window is exactly what naming it prevents. Caller holds d.mu.
func (d *Daemon) resolveVSCode(name string) (*extConn, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if len(d.vscodes) == 0 {
		return nil, fmt.Errorf("no VS Code window connected — install the aglink-vscode extension (see vscode/README.md)")
	}
	if n == "" || n == "vscode:" {
		return nil, fmt.Errorf("name the VS Code window: pass profile=\"vscode:<name>\" — connected: %s", strings.Join(d.vscodeKeysLocked(), ", "))
	}
	if ec := d.vscodes[n]; ec != nil {
		return ec, nil
	}
	var hits []string
	for key := range d.vscodes {
		if strings.HasPrefix(key, n) {
			hits = append(hits, key)
		}
	}
	sort.Strings(hits)
	switch len(hits) {
	case 1:
		return d.vscodes[hits[0]], nil
	case 0:
		return nil, fmt.Errorf("no VS Code window matches %q — connected: %s", name, strings.Join(d.vscodeKeysLocked(), ", "))
	default:
		return nil, fmt.Errorf("VS Code window %q is ambiguous — matches: %s", name, strings.Join(hits, ", "))
	}
}

func (d *Daemon) vscodeKeysLocked() []string {
	keys := make([]string, 0, len(d.vscodes))
	for k := range d.vscodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// vscodeProfileLines renders the connected windows for list_profiles.
func (d *Daemon) vscodeProfileLines() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var lines []string
	for _, key := range d.vscodeKeysLocked() {
		info := d.vscodeInfo[key]
		where := "local"
		if info.Remote != "" {
			where = "SSH " + info.Remote
		}
		lines = append(lines, fmt.Sprintf("%s | %s | %s | connected %s ago",
			key, where, info.Folder, time.Since(d.vscodes[key].since).Round(time.Second)))
	}
	return lines
}

// maxVSCodeWait caps how long a single tool call may keep a caller waiting,
// however long a terminal command was allowed to run.
const maxVSCodeWait = 10 * time.Minute

// vscodeTimeout is the daemon's wait for a window's answer: the default, or a
// terminal command's own timeoutSec plus a margin for the answer to travel.
func vscodeTimeout(params map[string]any) time.Duration {
	sec := intp(params, "timeoutSec", 0)
	if sec <= 0 {
		return callTimeout
	}
	d := time.Duration(sec) * time.Second
	if d > maxVSCodeWait {
		d = maxVSCodeWait
	}
	return d + 5*time.Second
}

// callVSCode runs one vscode_* tool in the named window. The extension gets
// the method without its "vscode_" prefix — "read", "terminal_run" — since the
// namespace only matters on the MCP side.
func (d *Daemon) callVSCode(method string, params map[string]any, profile string) CallResult {
	if !isVSCodeProfile(profile) {
		return CallResult{Error: fmt.Sprintf("%s needs a VS Code window: pass profile=\"vscode:<name>\" (see list_profiles)", method)}
	}
	d.mu.Lock()
	ec, err := d.resolveVSCode(profile)
	d.mu.Unlock()
	if err != nil {
		return CallResult{Error: err.Error()}
	}
	return d.roundTrip(ec, strings.TrimPrefix(method, "vscode_"), params, vscodeTimeout(params), "VS Code window")
}
