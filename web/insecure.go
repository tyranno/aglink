package main

import (
	"bufio"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Certificate warnings: hosts the user has said to always proceed past.
//
// Internal sites (NAS, router, dev servers) often run on a self-signed or
// private-CA certificate, and every visit lands on the browser's warning page.
// Listing such a host here makes navigate continue past the warning by itself,
// so an agent working with that site never stops on it. Nothing is listed by
// default: proceeding is the user's decision, one host at a time.
//
//	~/.aglink/aglink-web-insecure-hosts   one pattern per line, # comments
//	AGLINK_WEB_INSECURE_HOSTS             same patterns, comma or space separated
//
// A pattern is a host ("nas.local"), a host with port ("10.0.0.5:8443", which
// then matches only that port), or "*.corp.example" for every subdomain of
// corp.example. A bare "*" is deliberately not supported.

const (
	insecureHostsFile = "aglink-web-insecure-hosts"
	insecureHostsEnv  = "AGLINK_WEB_INSECURE_HOSTS"
	proceedTimeout    = 60 * time.Second
)

// insecureHostPatterns reads both sources on every call: the file is tiny, a
// navigate is rare, and an edit takes effect without restarting the daemon.
func insecureHostPatterns() []string {
	var out []string
	for _, f := range strings.FieldsFunc(webGetenv(insecureHostsEnv), func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		out = append(out, f)
	}
	if dir, err := dataDir(); err == nil {
		if f, err := os.Open(filepath.Join(dir, insecureHostsFile)); err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if i := strings.Index(line, "#"); i >= 0 {
					line = strings.TrimSpace(line[:i])
				}
				if line != "" {
					out = append(out, line)
				}
			}
			f.Close()
		}
	}
	return out
}

// hostMatches reports whether hostport ("host" or "host:port", as in a URL)
// matches any pattern.
func hostMatches(hostport string, patterns []string) bool {
	hostport = strings.ToLower(strings.TrimSpace(hostport))
	if hostport == "" {
		return false
	}
	host := hostport
	if u, err := url.Parse("https://" + hostport); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		switch {
		case p == "" || p == "*":
			continue
		case strings.HasPrefix(p, "*."):
			if strings.HasSuffix(host, p[1:]) {
				return true
			}
		default:
			if _, _, err := net.SplitHostPort(p); err == nil {
				if hostport == p { // a port was given: only that port
					return true
				}
				continue
			}
			if host == strings.Trim(p, "[]") {
				return true
			}
		}
	}
	return false
}

// urlHost returns the host[:port] of a URL, or "" if it has none.
func urlHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Host
}

// insecureAllowed reports whether the user listed the host of rawURL.
func insecureAllowed(hostport string) bool {
	return hostMatches(hostport, insecureHostPatterns())
}

var (
	// The extension's navigate reply ends with this when the tab is left on
	// a warning page (see certWarning in background.js).
	proceedHintRE = regexp.MustCompile(`call proceed_insecure with tabId=(\d+)`)
	warnHostRE    = regexp.MustCompile(`, host ([^)\s]+)\)`)
	stuckTabRE    = regexp.MustCompile(`on tab (\d+)`)
	activatedRE   = regexp.MustCompile(`^ok: activated tab \d+ — (.*) — [^ ]*$`)
)

// certStuck is how the extension's proceed_insecure error begins when nothing
// inside the browser got past the warning (CERT_STUCK in background.js).
const certStuck = "certificate warning is still showing"

// bypassKeys is Chrome's keyboard bypass on its certificate warning page.
const bypassKeys = "thisisunsafe"

// typeKeys is typeIntoChrome, swapped out by tests.
var typeKeys = typeIntoChrome

// proceedChrome is proceed_insecure for a Chrome profile. The extension tries
// what it can inside the browser; current Chrome keeps extensions off its
// warning page entirely, so that usually ends in certStuck. The daemon then
// does what a person would: brings the tab to the front and types Chrome's
// bypass keys into it, and asks the extension whether the site came up.
func (d *Daemon) proceedChrome(ec *extConn, params map[string]any) CallResult {
	res := d.roundTrip(ec, "proceed_insecure", params, proceedTimeout, "browser")
	if res.OK || !strings.Contains(res.Error, certStuck) || !osKeysSupported {
		return res
	}
	tabID := intp(params, "tabId", 0)
	if tabID == 0 {
		if m := stuckTabRE.FindStringSubmatch(res.Error); m != nil {
			tabID, _ = strconv.Atoi(m[1])
		}
	}
	if tabID == 0 {
		return res
	}
	act := d.roundTrip(ec, "activate_tab", map[string]any{"tabId": tabID}, callTimeout, "browser")
	if !act.OK {
		res.Error += " | bringing the tab to the front to type the bypass failed: " + act.Error
		return res
	}
	title := ""
	if m := activatedRE.FindStringSubmatch(act.Text); m != nil {
		title = m[1]
	}
	time.Sleep(400 * time.Millisecond) // let the window come forward and focus the page
	if err := typeKeys(title, bypassKeys); err != nil {
		res.Error += " | typing the bypass into Chrome: " + err.Error()
		return res
	}
	return d.roundTrip(ec, "proceed_insecure", map[string]any{"tabId": tabID, "waitOnly": 1}, proceedTimeout, "browser")
}

// autoProceed follows a navigate that stopped on a certificate warning with
// proceed_insecure, but only for a host the user listed. The host judged is
// the one the warning names — after redirects, that is where the bad
// certificate actually is — falling back to the requested URL's.
func (d *Daemon) autoProceed(ec *extConn, params map[string]any, res CallResult) CallResult {
	m := proceedHintRE.FindStringSubmatch(res.Text)
	if m == nil {
		return res
	}
	host := urlHost(str(params, "url"))
	if h := warnHostRE.FindStringSubmatch(res.Text); h != nil {
		host = h[1]
	}
	if !insecureAllowed(host) {
		return res
	}
	tabID, _ := strconv.Atoi(m[1])
	pr := d.proceedChrome(ec, map[string]any{"tabId": tabID})
	if !pr.OK {
		res.Text += "\nauto-proceed (host is in " + insecureHostsFile + ") failed: " + pr.Error
		return res
	}
	return CallResult{OK: true, Text: pr.Text + "\n(auto-proceeded: " + host + " is in " + insecureHostsFile + ")"}
}
