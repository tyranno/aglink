package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// webDownloadsDir is where aglink-web saves files it produces on the user's
// behalf (print_pdf) or waits for (wait_download) — <dataDir>/web-downloads,
// kept apart from whatever folder the browser itself downloads into so a
// caller always gets back one predictable, writable location regardless of
// the user's own Chrome download settings.
func webDownloadsDir() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "web-downloads")
	if err := os.MkdirAll(out, 0o700); err != nil {
		return "", err
	}
	return out, nil
}

// savePDFResult turns print_pdf's raw base64 PDF (what both apptools.go and
// background.js hand back, the same shape screenshot uses for images) into a
// saved file and a path — what an agent calling "print this page to PDF"
// actually wants, not an opaque blob it cannot otherwise do anything with.
func savePDFResult(res CallResult) CallResult {
	data, err := base64.StdEncoding.DecodeString(res.Text)
	if err != nil {
		return CallResult{Error: fmt.Sprintf("print_pdf: the page returned data that wasn't valid PDF data (%v)", err)}
	}
	dir, err := webDownloadsDir()
	if err != nil {
		return CallResult{Error: fmt.Sprintf("print_pdf: could not prepare the downloads folder: %v", err)}
	}
	path := filepath.Join(dir, fmt.Sprintf("page-%s.pdf", time.Now().Format("20060102-150405.000")))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return CallResult{Error: fmt.Sprintf("print_pdf: could not save the PDF: %v", err)}
	}
	return CallResult{OK: true, Text: fmt.Sprintf("ok: saved PDF to %s (%d bytes)", path, len(data))}
}
