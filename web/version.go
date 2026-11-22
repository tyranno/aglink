package main

// buildCommit is the git short hash this binary was built from, injected at
// build time via -ldflags (see installer/build-installer.ps1 and
// installer/build-product-setup.ps1). It is empty on a plain `go build` dev
// build.
//
// The staged chrome-extension/manifest.json's "version_name" is stamped with
// the same hash in the same build step, so the extension can tell the daemon
// which build it shipped with. The two must always travel together — they
// come from the same commit — so a mismatch means Chrome is still running a
// cached service worker from before the files on disk were last updated
// (chrome.runtime.reload() has not run since). See relayNM (nmhost.go) and
// Daemon.handleExt (daemon.go), which tell a stale extension to reload itself.
var buildCommit = ""
