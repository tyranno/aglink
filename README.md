# aglink-web

Standalone [agentlink](https://github.com/tyranno) plugin that lets teleclaude
workers (Claude / Codex) drive the user's **real Chrome browser** — list tabs,
navigate, and read page text — as a sibling to
[`aglink-screen`](../aglink-screen) (Windows screen control).

Status: **v1 scaffold.** Minimal tool set (`list_tabs`, `navigate`,
`get_page_text`) proving the architecture end-to-end. `click`, `screenshot`,
and `web_search` come after the scaffold is validated.

## Why this architecture (and not Native Messaging)

Chrome Native Messaging starts a native host process *when the extension opens a
port* — Chrome owns the lifecycle. That fights teleclaude's model, where each
worker **spawns its own stdio MCP server on demand** (exactly how `aglink-screen`
is wired: teleclaude points the worker's `--mcp-config` at the binary).

Instead, the extension **dials out** to a persistent local daemon over a
localhost WebSocket. No Native Messaging, no registry host manifest. teleclaude's
spawn-a-stdio-server model stays unchanged: it spawns a thin **bridge** that
forwards tool calls to the daemon.

```
Chrome (always running)
  └─ Extension (MV3 service worker, installed once)
        │  ws://127.0.0.1:48219/ext   (extension dials OUT — Origin-checked)
        ▼
  aglink-web serve   ← persistent daemon; owns the live extension socket,
        ▲              routes commands, awaits replies
        │  HTTP POST /call  (localhost)
  aglink-web mcp     ← thin stdio MCP server; teleclaude SPAWNS this per worker.
        ▲              auto-starts the daemon if it isn't already running.
        │  stdio (MCP)
   teleclaude worker
```

One Go binary, three subcommands (mirrors `aglink-screen`):

| Command | Role |
|---|---|
| `aglink-web` / `aglink-web mcp` | stdio MCP server teleclaude spawns per worker (default). Thin forwarder. |
| `aglink-web serve` | the persistent daemon the extension connects to. Auto-spawned by the bridge if not already up. |
| `aglink-web cmd <sub>` | no-LLM fast-path; prints `{"text","error"}` JSON. `list_tabs` / `navigate <url> [tabId]` / `get_page_text [tabId] [maxChars]`. |

## Security

- **WS handshake Origin check.** The daemon only upgrades connections whose
  `Origin` is `chrome-extension://…` — arbitrary web pages hitting
  `ws://127.0.0.1:48219` are rejected. Pin the exact extension ID by setting
  `AGLINK_WEB_EXT_ID` (find it at `chrome://extensions`); unset accepts any
  extension (fine for local dev, logged as a warning).
- **Loopback only.** The daemon binds `127.0.0.1`; the `/call` control endpoint
  is reachable only by local processes — the same trust boundary as
  `aglink-screen` (a local process could always spawn the binary directly).

## Install & run

### 1. Build

```sh
go build -o aglink-web.exe .
```

### 2. Load the extension (once)

1. Open `chrome://extensions`, enable **Developer mode**.
2. **Load unpacked** → select the `extension/` directory.
3. Note the extension **ID** shown on the card. To pin it, set
   `AGLINK_WEB_EXT_ID=<that-id>` in the daemon's environment.

The extension auto-connects to `ws://127.0.0.1:48219/ext` and reconnects with
backoff (a keepalive alarm revives the MV3 service worker if it sleeps).

### 3. Wire teleclaude

Point the worker's `--mcp-config` at the built binary (default `mcp`
subcommand), same as `aglink-screen`. The first tool call auto-starts the daemon
if it isn't running.

### Try it without teleclaude

```sh
./aglink-web.exe serve         # terminal 1: start the daemon (or let the bridge do it)
./aglink-web.exe cmd list_tabs # terminal 2: should print the open tabs
./aglink-web.exe cmd navigate https://example.com
./aglink-web.exe cmd get_page_text
```

## Config

| Env var | Default | Meaning |
|---|---|---|
| `AGLINK_WEB_PORT` | `48219` | Daemon/bridge port. If you change it, also update `PORT` in `extension/background.js`. |
| `AGLINK_WEB_EXT_ID` | *(unset)* | Pin the accepted extension ID. Unset = accept any `chrome-extension://` origin. |

The daemon writes its live port to `~/.teleclaude/aglink-web.port` so the bridge
finds it; a stale/corrupt file falls back to the default.

## Layout

```
main.go        subcommand dispatch (mcp | serve | cmd)
mcpweb.go      MCP tool definitions (forward to daemon)
daemon.go      `serve`: WS /ext + HTTP /call + /health, request router
client.go      bridge → daemon: ensureDaemon (auto-spawn) + call
protocol.go    shared JSON wire types
datadir.go     ~/.teleclaude, port file
cmd.go         `cmd` fast-path
proc_windows.go / proc_other.go   detached daemon spawn per OS
extension/     MV3 extension (manifest.json + background.js)
```

## Development

```sh
go build ./... && go vet ./... && go test ./...
```
