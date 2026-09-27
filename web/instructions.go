package main

// serverInstructions is what an MCP client shows a model on connecting, ahead
// of any single tool's description. It carries the one fact no individual tool
// can convey: the same tools reach Electron/Wails app windows, and doing so is
// far cheaper than driving them through screen capture.
const serverInstructions = `aglink-web drives web UIs as text — reading, clicking and typing through the DOM rather than screenshots.

Two kinds of target, chosen with the 'profile' argument on any tool:
- Chrome: the user's own browser through the aglink-web extension. Omit 'profile' for the default, or give an account email / unique prefix.
- App windows: Electron or Wails desktop apps (including the user's own Svelte-based apps) that have a DevTools port open. Address them as app:<name> (from the window title; a unique prefix works) or cdp:<port>. Call list_profiles to see what is available.

Prefer this over screen-capture tools (aglink-screen) for any Electron/Wails window: it reads the real DOM as text, uses the same selectors as Chrome (CSS, or role=/text=/label=/placeholder=/testid=), and costs a fraction of the tokens.

An app shows up only if it was launched with a debugging port: Electron with --remote-debugging-port=9222; a Wails app needs AdditionalBrowserArgs set from AGLINK_WEBVIEW_DEBUG_PORT (the WEBVIEW2_* environment variable does not work for Wails). Ports 9222-9240 on 127.0.0.1 are searched.

VS Code windows: with the aglink-vscode extension installed, every open VS Code window (including Remote-SSH windows) appears in list_profiles as vscode:<workspace>@<host>. Use the vscode_* tools on it — vscode_workspace to see what it is doing, vscode_read / vscode_problems / vscode_terminal_read to inspect, vscode_open / vscode_terminal_run / vscode_command to act. This reads another window's files, errors and terminal output as text, which screen capture cannot. vscode_terminal_run in a Remote-SSH window runs on the remote machine. The web-page tools do not work on vscode: profiles, and the Claude chat panel of another window is not readable this way.

JavaScript dialogs (alert/confirm/prompt) freeze the page, in Chrome and in app windows alike: other tools on that tab then fail fast with "dialog open: …" saying what it asks; answer with handle_dialog (dialog_status reads it again). get_console_logs, get_network_requests, close_tab and reload_extension work on Chrome only.

Certificate warnings ("Your connection is not private", NET::ERR_CERT_…): navigate reports when a tab or window is left on one, and other tools say so instead of failing opaquely. Call proceed_insecure to continue past it when the user means to reach that site (typically an internal server with a self-signed certificate). If it cannot, the error names the fallbacks: typing "thisisunsafe" on the warning with aglink-screen, or the user trusting the certificate with 'aglink-web trust-cert <url>'. Hosts listed in ~/.aglink/aglink-web-insecure-hosts are proceeded automatically.`
