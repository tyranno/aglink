package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// aglink-web trust-cert <https-url | host[:port]> [--yes]
//
// The permanent fix for an internal site's certificate warning: fetch the
// certificate the site presents and add it to the current user's trusted root
// store, so Chrome (which uses the Windows store for locally added roots) no
// longer warns at all. proceed_insecure only gets past the warning for one
// browser session; this ends it.
//
// Deliberately a command the user runs, never an MCP tool: adding a trusted root
// lets that certificate vouch for any site, so an agent must not do it on its
// own. Windows also asks for confirmation itself before adding to the user's
// root store.
//
// It refuses nothing the user asked for, but says plainly when trusting cannot
// help: a certificate for another name, or an expired one, still warns however
// it is trusted.
func runTrustCert(args []string) {
	var target string
	yes := false
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			yes = true
		default:
			if target == "" {
				target = a
			}
		}
	}
	if target == "" {
		fmt.Fprintln(os.Stderr, "usage: aglink-web trust-cert <https-url | host[:port]> [--yes]")
		os.Exit(2)
	}
	if err := trustCert(target, yes, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "trust-cert:", err)
		os.Exit(1)
	}
}

// certTarget turns "https://nas.local:5001/x", "nas.local:5001" or "nas.local"
// into the host to verify against and the address to dial.
func certTarget(target string) (host, addr string, err error) {
	t := strings.TrimSpace(target)
	if !strings.Contains(t, "://") {
		t = "https://" + t
	}
	u, err := url.Parse(t)
	if err != nil || u.Hostname() == "" {
		return "", "", fmt.Errorf("not a URL or host: %q", target)
	}
	if u.Scheme != "https" && u.Scheme != "wss" {
		return "", "", fmt.Errorf("%s has no certificate to trust (scheme %s)", target, u.Scheme)
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return u.Hostname(), net.JoinHostPort(u.Hostname(), port), nil
}

// certAnchor picks the certificate to trust: the self-signed root if the
// server sent one (the usual private-CA or self-signed setup), otherwise the
// topmost certificate it did send.
func certAnchor(chain []*x509.Certificate) (anchor *x509.Certificate, selfSigned bool) {
	top := chain[len(chain)-1]
	// CheckSignature, not CheckSignatureFrom: the latter insists the signer be a
	// CA, and a plain self-signed server certificate usually is not one.
	selfSigned = bytes.Equal(top.RawSubject, top.RawIssuer) &&
		top.CheckSignature(top.SignatureAlgorithm, top.RawTBSCertificate, top.Signature) == nil
	return top, selfSigned
}

// certProblems lists what trusting cannot fix.
func certProblems(leaf *x509.Certificate, host string, now time.Time) []string {
	var out []string
	if err := leaf.VerifyHostname(host); err != nil {
		out = append(out, fmt.Sprintf("the certificate is not issued for %s (%v) - the warning will stay even once it is trusted; fix the server's certificate, or use proceed_insecure / the insecure-hosts list", host, err))
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		out = append(out, fmt.Sprintf("the certificate is outside its validity period (%s - %s) - the warning will stay even once it is trusted", leaf.NotBefore.Format("2006-01-02"), leaf.NotAfter.Format("2006-01-02")))
	}
	return out
}

func trustCert(target string, yes bool, in io.Reader, out io.Writer) error {
	host, addr, err := certTarget(target)
	if err != nil {
		return err
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true, // we are here precisely because it does not verify; we only read it
	})
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}
	chain := conn.ConnectionState().PeerCertificates
	conn.Close()
	if len(chain) == 0 {
		return fmt.Errorf("%s presented no certificate", addr)
	}
	leaf := chain[0]

	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter}); err == nil {
		fmt.Fprintf(out, "%s is already trusted by this machine - nothing to do.\n", addr)
		return nil
	}

	anchor, selfSigned := certAnchor(chain)
	sum := sha256.Sum256(anchor.Raw)
	fmt.Fprintf(out, "Site:        %s\n", addr)
	fmt.Fprintf(out, "Certificate: %s\n", leaf.Subject)
	fmt.Fprintf(out, "  names:     %s\n", strings.Join(leaf.DNSNames, ", "))
	fmt.Fprintf(out, "  valid:     %s - %s\n", leaf.NotBefore.Format("2006-01-02"), leaf.NotAfter.Format("2006-01-02"))
	fmt.Fprintf(out, "To trust:    %s\n", anchor.Subject)
	fmt.Fprintf(out, "  SHA-256:   %s\n", strings.ToUpper(hex.EncodeToString(sum[:])))
	if !selfSigned {
		fmt.Fprintln(out, "  note:      the server did not send its root certificate; trusting the topmost one it sent.")
	}
	for _, p := range certProblems(leaf, host, time.Now()) {
		fmt.Fprintln(out, "WARNING:    "+p)
	}
	fmt.Fprintln(out, "Trusting this lets it vouch for any site, not just this one. Compare the SHA-256 with what the site's admin gives you if you can.")

	if !yes {
		fmt.Fprint(out, "Add it to the current user's trusted root certificates? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(out, "Not added.")
			return nil
		}
	}

	dir, err := os.MkdirTemp("", "aglink-trust-cert-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	pemPath := filepath.Join(dir, "cert.crt")
	if err := os.WriteFile(pemPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: anchor.Raw}), 0o600); err != nil {
		return err
	}

	if runtime.GOOS != "windows" {
		keep := filepath.Join(os.TempDir(), "aglink-"+strings.ReplaceAll(host, ":", "_")+".crt")
		_ = os.WriteFile(keep, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: anchor.Raw}), 0o644)
		return fmt.Errorf("adding a trusted root is automated on Windows only; the certificate is saved at %s - add it with your system's tool (e.g. update-ca-certificates, or Keychain Access on macOS)", keep)
	}

	fmt.Fprintln(out, "Adding... Windows will ask you to confirm.")
	cmd := exec.Command("certutil", "-user", "-addstore", "Root", pemPath)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("certutil failed (declined in the Windows prompt?): %w", err)
	}
	fmt.Fprintf(out, "Done. Reload the page in Chrome (restart Chrome if it still warns).\nTo undo: certutil -user -delstore Root %s\n", anchor.SerialNumber.Text(16))
	return nil
}
