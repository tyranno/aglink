# aglink-web

Standalone [agentlink](https://github.com/tyranno) plugin that lets teleclaude
workers (Claude / Codex) drive the user's **real Chrome browser** — list tabs,
navigate, read page text, click, type, screenshot, and close tabs — as a
sibling to [`aglink-screen`](../aglink-screen) (Windows screen control).

Status: scaffold validated end-to-end (live in teleclaude workers as
`mcp__web__*`); tool set now covers `list_tabs`, `navigate`, `get_page_text`,
`click`, `list_elements`, `screenshot`, `type`, `get_value`, `key`, `scroll`,
`select_option`, `wait_for_element`, `activate_tab`, `get_console_logs`,
`close_tab`. `web_search` is still pending — the
plan is to do it the same way a human would (navigate to a search engine in
the real browser and read/screenshot the results), not call a search API, so
it needs its own design pass rather than a one-line addition like the others.

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
| `aglink-web cmd <sub>` | no-LLM fast-path; prints `{"text","error"}` JSON. `list_tabs` / `navigate <url> [tabId]` / `get_page_text [tabId] [maxChars]` / `click <selector> [button] [tabId]` / `list_elements [tabId] [max]` / `screenshot [tabId]` (base64 PNG in `text`) / `type <selector> <text> [tabId]` / `get_value <selector> [tabId]` / `key <combo> [tabId]` / `scroll [selector] [dx] [dy] [tabId]` / `select_option <selector> [value] [label] [tabId]` / `wait_for_element <selector> [tabId] [timeoutMs]` / `activate_tab <tabId>` / `get_console_logs [tabId] [max]` / `close_tab [tabId]`. |

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

## teleclaude와 연결

teleclaude는 `web_control.binary_path`(config.yaml)로 이 실행파일 경로를
찾는다. 값이 비어 있으면 teleclaude 실행파일과 **같은 폴더**에서
`aglink-web(.exe)`를 찾는다 — `aglink-screen`과 완전히 동일한 자동탐색 관례라,
배포 시 세 실행파일(teleclaude, aglink-screen, aglink-web)을 나란히 두면
별도 설정 없이 다 같이 동작한다.

```yaml
web_control:
  enabled: true
  binary_path: ""   # 비우면 teleclaude exe와 같은 폴더에서 자동 탐색
```

teleclaude 쪽은 워커 실행 시 `aglink-screen`·`aglink-web` 등 활성화된
플러그인 전부를 **하나의** `--mcp-config`/`--allowedTools`로 병합해서
넘긴다(Claude CLI가 이 플래그들을 1회씩만 받기 때문 — 따로따로 넘기면
나중 것이 앞 것을 덮어씀). 그래서 `screen_control`과 `web_control`을 동시에
켜도 두 플러그인 다 정상적으로 워커에 노출된다.

### 통합 배포 (`!update`)

teleclaude와 이 저장소를 **형제 디렉터리**(예: `..\teleclaude`, `..\aglink-web`)로
나란히 clone해두면, teleclaude의 텔레그램 `!update` 명령이 teleclaude 자체를
빌드하기 전에 이 저장소도 함께 `go build`해서 teleclaude 실행파일 옆에
떨어뜨려준다. 빌드 후에는 상시 데몬(`aglink-web serve`)이 낡은 바이너리를
계속 메모리에서 서빙하지 않도록 자동으로 재시작까지 해준다(활성 대화 중인
`mcp` 브리지 자식 프로세스는 건드리지 않고, `serve` 데몬만 정밀 종료). 형제
디렉터리가 없으면 조용히 건너뛴다 — 자세한 내용은
[teleclaude README의 "플러그인 확장" 절](https://github.com/tyranno/teleclaude#플러그인-확장-aglink-)
참고.

> ⚠️ 단, Chrome 확장(`extension/background.js`/`manifest.json`)이 바뀐 경우 `!update`가
> 새 바이너리는 배포해주지만 **Chrome에 로드된 확장 자체는 자동으로 리로드되지
> 않는다** — `chrome://extensions` 페이지 자체는 `chrome://` 스킴이라 확장 코드 주입이
> 막혀 있어서 그 화면만큼은 자동화가 안 된다. 대신 `aglink-web cmd reload_extension`
> (또는 MCP `reload_extension`)으로 확장 자체(`chrome.runtime.reload()`)를 재시작할
> 수 있다 — `chrome://extensions`를 열 필요 없이 한 번의 호출로 반영됨. (최초 1회,
> 이 기능이 아직 없는 옛 버전이 로드돼 있을 때만 수동 리로드가 필요하다.)

### Try it without teleclaude

```sh
./aglink-web.exe serve         # terminal 1: start the daemon (or let the bridge do it)
./aglink-web.exe cmd list_tabs # terminal 2: should print the open tabs
./aglink-web.exe cmd navigate https://example.com
./aglink-web.exe cmd get_page_text
./aglink-web.exe cmd click "button.submit"
./aglink-web.exe cmd click "#file-row" right   # trigger a page's own JS context menu
./aglink-web.exe cmd list_elements   # visible interactive elements + ready-to-use selectors
./aglink-web.exe cmd screenshot   # prints base64 PNG — e.g. pipe through `base64 -d > shot.png`
./aglink-web.exe cmd type "input#q" "hello world"
./aglink-web.exe cmd key "enter"      # scoped to the focused element in the page, not the OS
./aglink-web.exe cmd scroll "" 0 400   # scroll the page down 400px ("" = whole page, not an element)
./aglink-web.exe cmd select_option "select#country" KR
./aglink-web.exe cmd wait_for_element ".results-loaded"
./aglink-web.exe cmd close_tab
```

## Config

| Env var | Default | Meaning |
|---|---|---|
| `AGLINK_WEB_PORT` | `48219` | Daemon/bridge port. If you change it, also set the matching port in the extension's options page (see below) — the extension can't read env vars. |
| `AGLINK_WEB_EXT_ID` | *(unset)* | Pin the accepted extension ID. Unset = accept any `chrome-extension://` origin. |

The daemon writes its live port to `~/.teleclaude/aglink-web.port` so the **bridge**
finds it automatically; a stale/corrupt file falls back to the default. The
**extension**, being a browser process, can't read env vars or that file, so it
keeps its own copy of the port in `chrome.storage.local` (default `48219`).

**If you override `AGLINK_WEB_PORT`**, set the same port once in the extension:
`chrome://extensions` → aglink-web → **Details** → **Extension options** → enter
the port → **Save**. The extension reconnects on the new port immediately (no
reload needed). Leave the field blank to fall back to the default.

## Layout

```
main.go        subcommand dispatch (mcp | serve | cmd)
command.go     single source of truth for the browser command set (shared by mcp + cmd)
mcpweb.go      MCP server: registers each command as a tool (forwards to daemon)
cmd.go         `cmd` fast-path: dispatches a subcommand from the command table
daemon.go      `serve`: WS /ext + HTTP /call + /health, request router
client.go      bridge → daemon: ensureDaemon (auto-spawn) + call
protocol.go    shared JSON wire types
datadir.go     ~/.teleclaude, port file
proc_windows.go / proc_other.go   detached daemon spawn per OS
extension/     MV3 extension:
                 manifest.json      permissions + options_ui + content_scripts
                 background.js      service worker (WS client + command handlers)
                 console-capture.js MAIN-world content script buffering console output
                                    (read by get_console_logs) — purely observational,
                                    never changes page behavior
                 options.html/.js   set the daemon port (chrome.storage.local)
                 background.test.js node:test unit tests (chrome.* mocked via vm)
```

Adding a browser command is a three-file change: add an entry to `commands` in
`command.go` (covers both the MCP tool and the `cmd` fast-path) and a matching
handler in `extension/background.js` (the JS half can't share the Go table). Keep
the method name and param names identical across the two.

## Development

```sh
go build ./... && go vet ./... && go test ./...   # Go: bridge, daemon, protocol, port
node --test                                       # extension JS unit tests
```
