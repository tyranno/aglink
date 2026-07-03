# aglink-chat

Browser chat UI for [teleclaude](https://github.com/tyranno/teleclaude), split out into its own
service so UI/feature iteration doesn't require rebuilding or restarting teleclaude itself.

## Why a separate project

teleclaude's web chat (`web/`, `webchat.go`) currently lives inside the teleclaude binary,
embedded via `go:embed`. Every UI tweak — font size, a new button, a layout change — requires
rebuilding and restarting the whole teleclaude process, which also interrupts any in-flight
Telegram conversation. As the web UI grows, that coupling gets worse.

`aglink-chat` moves the browser-facing HTTP/WebSocket server and the `web/` UI into an
independently deployable process. teleclaude keeps owning all state and business logic
(`Bot`/`Manager`/`Hub`/`store`); `aglink-chat` becomes a thin, replaceable front door.

## Architecture

Unlike `aglink-screen`/`aglink-web` — which are MCP tool plugins a teleclaude **worker** spawns
per-turn over stdio — `aglink-chat` is a **long-running peer service**. It needs live, bidirectional
communication with teleclaude's `Hub`, not a one-shot tool call. The two talk over a small
**local-only control API**:

```
Browser  <--HTTP/WS-->  aglink-chat serve  <--control API (local WS)-->  teleclaude
(public, e.g. :1717)                        (loopback only, e.g. :17170)
```

- **teleclaude side**: exposes a loopback-only WebSocket control endpoint. Authenticates
  `aglink-chat` with the same shared-token mechanism the current `web_chat` config uses.
  Registers a new `ChannelSender` implementation (a "remote chat channel") with `Hub`, backed by
  this connection — the same role `telegramChannel`/`webChannel` play today, just over a socket
  instead of in-process calls.
- **aglink-chat side**: connects out to teleclaude's control API as a client (with reconnect/backoff,
  same pattern as `aglink-web`'s Chrome-extension keepalive), and serves the actual public-facing
  HTTP/WS + static `web/` UI to real browsers. Re-implements `/api/conversations` and `/api/upload`
  against the control API instead of calling into teleclaude's Go internals directly.

### Control-API protocol (draft — finalize during implementation)

Outbound (teleclaude → aglink-chat), one frame per event — mirrors today's `wsFrame`:
`{"type":"text"|"image"|"typing"|"done"|"user", ...}` (same fields as teleclaude's current
`web/app.js` already expects, so the browser-side rendering logic can move over unchanged).

Inbound (aglink-chat → teleclaude), request/response:
- `send_text {chatID, text, origin}` — equivalent of today's `dispatchText`.
- `handle_command {chatID, text, origin}` — equivalent of today's `handleCommand`.
- `list_conversations {}` → today's `/api/conversations` payload.
- `upload_attachment {chatID, path, caption}` — aglink-chat saves the multipart upload to disk
  itself and hands teleclaude the path (avoids streaming file bytes over the control API).

## Migration plan

1. **Scaffold** (this commit) — repo, `go.mod`, `aglink-chat serve` skeleton, this design doc.
2. **teleclaude: control-API server** — new file (e.g. `chatcontrol.go`), loopback WS endpoint,
   `remoteChatChannel` implementing `ChannelSender`, registered with `Hub` alongside
   `telegramChannel`. Config: `chat_control.enabled` / `chat_control.addr` (mirrors
   `screen_control`/`web_control` sections in `config.yaml`).
3. **aglink-chat: client + browser server** — connect to teleclaude's control API, port
   `web/index.html`/`app.js`/`style.css` over verbatim, reimplement `/api/conversations` and
   `/api/upload` against the control API.
4. **Cutover** — verify aglink-chat reaches full feature parity with teleclaude's embedded web
   chat (conversation list, origin-based web/telegram split, working indicator, cross-channel
   input echo — all recently added, see teleclaude's git history), then teleclaude drops
   `webchat.go`/`web/`. Wire `aglink-chat` into teleclaude's `!update` integrated-deploy
   (`pluginupdate.go`, same pattern as `aglink-screen`/`aglink-web`).

## teleclaude와 연결

(To be filled in once step 2 lands — will document `chat_control.enabled`/`chat_control.addr` in
`config.yaml`, the sibling-directory layout, and `!update` integrated deploy, mirroring
[aglink-web's "teleclaude와 연결" section](https://github.com/tyranno/aglink-web#teleclaude와-연결).)
