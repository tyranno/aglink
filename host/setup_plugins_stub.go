//go:build !windows

package main

import "bufio"

// aglink-* plugins (screen/browser/web-chat control) are Windows-only, so
// there is nothing to offer to install on other platforms.
func ensureAglinkPlugins(in *bufio.Reader, srcDir string) {}
