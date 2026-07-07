package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// buildVersion / buildTime are injected at build time via -ldflags
// (-X main.buildVersion=... -X main.buildTime=...) by handleUpdate's go build.
// A plain `go build` (dev workflow) leaves the defaults.
var (
	buildVersion = "dev"
	buildTime    = ""
)

// versionInfo returns the running binary's version and build timestamp.
func versionInfo() (version, builtAt string) {
	return buildVersion, buildTime
}

// buildStampVersion returns a short git commit (e.g. "a1b2c3d") for the source in
// srcDir, or "" when git is unavailable / not a repo — the build then omits ldflags
// and the binary keeps buildVersion="dev".
func buildStampVersion(srcDir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", srcDir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
