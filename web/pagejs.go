package main

import _ "embed"

// The extension's in-page scripts, embedded so the daemon runs the very same
// code inside an Electron/Wails window as the extension runs inside a Chrome
// tab. See extension/page-actions.js for why the page-side halves are shared.

//go:embed extension/aglink-inject.js
var injectJS string

//go:embed extension/page-actions.js
var pageActionsJS string
