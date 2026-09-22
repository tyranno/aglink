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
- **Loopback only.** The daemon binds `127.0.0.1`; the `/call` and `/mcp`
  control endpoints are reachable only by local processes — the same trust
  boundary as `aglink-screen` (a local process could always spawn the binary
  directly). An SSH reverse tunnel deliberately widens that boundary to a
  second machine's loopback; see
  [원격 머신에서 쓰기](#원격-머신에서-쓰기-ssh-역터널--mcp) before setting one up
  on a shared box.

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

## 원격 머신에서 쓰기 (SSH 역터널 + `/mcp`)

윈도우에서 작업하지만 **VS Code Remote-SSH 로 리눅스 머신의 프로젝트를 열어**
그쪽 Claude 로 작업하는 경우 — 브라우저는 여전히 윈도우에 있다. 그 원격
Claude 가 이 브라우저를 쓰게 하려면, 리눅스에 바이너리를 깔 필요 없이
데몬이 직접 제공하는 MCP 엔드포인트에 붙이면 된다.

데몬은 `/call`·`/health` 와 나란히 **`/mcp` 로 streamable-HTTP MCP** 를
서빙한다. stdio 브리지와 **똑같은 툴 세트**(`command.go` 의 `commands` 표)이고,
툴 호출은 자기 자신에게 다시 HTTP 를 치지 않고 데몬 라우터로 바로 들어간다.

```
Ubuntu (VS Code Remote-SSH)              Windows
  Claude ──http──> 127.0.0.1:48219 ══SSH -R══> 127.0.0.1:48219 ──ws──> Chrome
                   (터널 입구)                   aglink-web serve
```

**1. 윈도우 `~/.ssh/config`** 에 그 호스트 항목에 한 줄 — VS Code 가 접속할
때마다 역터널이 자동으로 따라 올라온다:

```
Host my-linux-box
  HostName 192.168.0.10
  User me
  RemoteForward 48219 127.0.0.1:48219
```

> `ExitOnForwardFailure yes` 는 **넣지 말 것.** 같은 머신에 VS Code 창을 두 개
> 이상 붙이면 두 번째부터는 원격 48219 가 이미 잡혀 있어 포워딩이 실패하는데,
> 이 옵션이 있으면 실패가 곧 **접속 자체의 종료**가 된다. 없으면 경고만 남기고
> 붙으며, 먼저 뚫린 터널을 그대로 같이 쓴다.

**2. 리눅스 쪽 Claude 에 등록** (설치할 바이너리 없음):

```sh
claude mcp add --transport http -s user aglink-web http://127.0.0.1:48219/mcp
claude mcp list      # aglink-web: ... (HTTP) - ✔ Connected
```

이미 열려 있던 Claude 세션에는 반영되지 않는다 — MCP 서버는 세션 시작 시점에
로드되므로 **새 세션부터** 툴이 보인다.

### 프로필 지정이 달라지는 점

로컬 stdio 브리지는 호출자의 작업 디렉터리에서 `.aglink-web/config` 의 프로필
핀을 읽는다. `/mcp` 로 들어온 호출은 그 디렉터리가 **다른 머신에** 있으므로
그 핀이 적용될 수 없고, `profile` 을 명시하지 않으면 데몬의 기본 프로필로
간다. 다른 브라우저를 몰고 싶으면 `list_profiles` 로 확인한 뒤 호출마다
`profile` 을 넘길 것.

### 보안: 원격 머신을 공유한다면

역터널의 출구는 그 리눅스 머신의 loopback 이고, loopback 은 **그 머신의 모든
사용자가 공유**한다. 즉 그 박스에 계정이 있는 사람은 누구나
`127.0.0.1:48219` 를 찔러 당신의 윈도우 크롬을 — **로그인된 세션 그대로** —
조작할 수 있다. 1인 전용 머신이면 문제 없지만, 공용 머신이라면 이 터널을
상시로 두지 말 것.

## 앱 화면 다루기 (Electron · Wails)

Electron 이나 Wails 로 만든 윈도우 앱의 UI 도 **일반 웹페이지처럼** 같은 도구로
다룬다. 앱의 웹뷰는 크롬이 아니라 확장을 설치할 수 없으므로, 데몬이 앱의
DevTools 포트(CDP)에 **직접** 붙는다. 스크린샷 대신 텍스트로 읽으니 aglink-screen
보다 훨씬 가볍다.

### 앱 쪽 준비 — 디버그 포트 열기

**Wails** — `main.go` 의 Windows 옵션에 한 줄. 환경변수가 있을 때만 포트가 열리므로
평소 실행·배포판에는 영향이 없다.

```go
Windows: application.WindowsOptions{
	AdditionalBrowserArgs: devtoolsArgs(),
},

func devtoolsArgs() []string {
	if p := os.Getenv("AGLINK_WEBVIEW_DEBUG_PORT"); p != "" {
		return []string{"--remote-debugging-port=" + p}
	}
	return nil
}
```

```powershell
$env:AGLINK_WEBVIEW_DEBUG_PORT = '9333'; .\myapp.exe
```

> `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS` 환경변수로는 **안 된다.** Wails 의 WebView2
> 로더가 보안상 그 변수를 일부러 비운다. 반드시 앱 옵션으로 넣어야 한다.
> `aglink/desktop/main.go` 에 본보기가 있다.

**Electron** — 코드 수정 없이 실행 인자로:

```powershell
.\myapp.exe --remote-debugging-port=9222
```

### 부르기

```
list_profiles
  doowon.lab.02@gmail.com | connected 3h ago | default
  app:aglink | cdp:9333 | http://wails.localhost/ | 1 window

get_page_text   profile="app:aglink"
click           profile="app:aglink"  selector="text=업무 관리"
type            profile="cdp:9333"    selector="placeholder=메시지" text="안녕"
```

- `app:<이름>` — 창 제목에서 만든 이름. 앞부분만 대도 된다(겹치면 되묻는다).
- `cdp:<포트>` — 항상 통하는 정확한 이름. 제목이 바뀌어도 안 변한다.
- 창이 여럿이면 `list_tabs` 가 `1 | 제목 | url` 로 번호를 매기고, 다른 도구의
  `tabId` 가 그 번호다.
- 설정은 없다. 데몬이 `127.0.0.1:9222–9240` 을 짧게 두드려 찾는다. 범위는
  `AGLINK_WEB_CDP_PORTS` 로 바꾼다(`9333`, `9300-9310`, `9222,9333` 모두 가능).

선택자는 크롬과 **같은 엔진**이다(`extension/aglink-inject.js` +
`extension/page-actions.js` 를 데몬이 그대로 앱에 넣는다). 크롬에서 되는
`role=`/`text=`/`label=`/`placeholder=`/`testid=` 선택자가 앱에서도 그대로 된다.

### alert · confirm · prompt

이 창이 뜨면 페이지 JS 가 멈춰서, 그 창에 대한 다른 도구는 전부
`dialog open: confirm "…" — call handle_dialog to answer it` 로 **즉시** 돌아온다
(시간 초과까지 기다리지 않는다).

```
dialog_status   profile="app:aglink"                  → confirm: "삭제할까요?"
handle_dialog   profile="app:aglink"  accept="false"  → 취소
handle_dialog   profile="app:aglink"  prompt_text="홍길동"
```

크롬의 alert/confirm 은 아직 지원하지 않는다(확장에 디버거 권한이 필요해 따로
진행한다).

2026-09-22 aglink 데스크톱(Wails v3)으로 실측: 목록·본문 읽기·`text=`/`role=`
선택자 클릭·입력·값 읽기·스크린샷·`confirm()` 처리까지 전부 동작했고, 확인창이
떠 있는 동안 다른 도구는 100ms 안에 `dialog open` 으로 돌아왔다.

### 앱에서 안 되는 것

`get_console_logs`, `get_network_requests`, `close_tab`, `reload_extension` 은 앱
프로필에서 분명한 오류로 거절한다.

### 주의 — 디버그 포트는 앱 전체를 주는 문이다

포트에 닿는 누구든 그 앱을 통째로 조작할 수 있다. 데몬은 `127.0.0.1` 밖의 CDP 에는
절대 붙지 않고, Chromium 의 기본 바인딩도 루프백이다. 그래도 **개발용 실행에서만**
켤 것 — 위 Wails 코드가 환경변수 없이는 포트를 열지 않는 이유다.

## VS Code 창 다루기 (aglink-vscode 확장)

VS Code 창은 DOM 대신 **확장**으로 다룬다. 편집기는 보이는 줄만 DOM 에 있고
터미널은 캔버스에 그려져, 정작 보고 싶은 것이 CDP 로는 안 읽히기 때문이다.
[`../vscode`](../vscode/README.md) 의 확장을 윈도우 VS Code 에 한 번 설치하면
창마다(Remote-SSH 창 포함) 데몬의 `/vscode` 로 접속해 `list_profiles` 에
`vscode:<작업공간>@<호스트>` 로 나타난다.

```
vscode_workspace      profile="vscode:backend"   → 무엇을 하고 있는지
vscode_read           profile="vscode:backend"   path="src/main.go"   (저장 안 한 내용 포함)
vscode_problems       profile="vscode:backend"
vscode_terminal_run   profile="vscode:backend"   command="go test ./..."   → 출력 + 종료 코드
```

- 확장은 UI 확장이라 **윈도우에만 설치**한다. 원격에는 아무것도 깔지 않는다.
- 창을 고르는 기본값은 없다 — 에이전트 자신의 창도 연결돼 있어서다.
- `vscode_*` 도구에 크롬·앱 프로필을, DOM 도구에 `vscode:` 프로필을 주면 거절한다.
- 원격 창의 `vscode_terminal_run` 은 **원격에서** 실행된다. 역터널로 데몬에 닿는
  원격 세션도 이 도구를 쓸 수 있다 — aglink-screen 과 같은 수준의 권한이다.
- 다른 확장의 화면(Claude 채팅 패널 등)은 읽지 못한다. Claude 대화는 `!attach`.

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
