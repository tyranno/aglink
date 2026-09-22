# aglink-vscode 구현 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** VS Code 창마다 떠서 aglink-web 데몬에 접속하는 확장과, 그 창들을 `vscode:<이름>` 프로필과 `vscode_*` 도구로 다루는 데몬 쪽을 만든다.

**Architecture:** 크롬 확장과 같은 모양 — 확장이 `ws://127.0.0.1:<port>/vscode` 로 접속, 데몬이 `Request` 를 보내고 확장이 `Reply` 로 답한다. 데몬은 크롬 등록부와 **분리된** VS Code 등록부를 두고, 요청 대기·핑·끊김 처리는 크롬 쪽 코드를 공유하도록 뽑아낸다.

**Tech Stack:** 확장 — 순수 JS(CommonJS), 의존성 0, VS Code API ≥1.93, 시험은 `node --test` + 가짜 `vscode` 모듈. 데몬 — Go(`web/`).

**Spec:** [`docs/superpowers/specs/2026-09-22-aglink-vscode-extension-design.md`](../specs/2026-09-22-aglink-vscode-extension-design.md)

## Global Constraints

- Go 명령은 `GOFLAGS=-mod=readonly`. 확장 시험은 `node --test --test-force-exit`.
- 확장은 **npm 의존성 0**. `require("vscode")` 외에는 Node 내장만.
- 크롬 경로(`/ext`, `d.exts`, 기존 도구)의 동작은 바꾸지 않는다. 기존 Go 시험·확장 시험 전부 통과.
- VS Code 창은 크롬 기본 프로필 후보가 **되지 않는다.**
- `vscode_*` 도구는 `profile` 이 `vscode:` 가 아니면 거절. DOM 도구는 `vscode:` 프로필을 거절.
- 공개 저장소 — 주소·계정·비밀값을 코드·시험·문서에 적지 않는다(시험 이름은 `backend`, `host1` 등).
- 사용자 VS Code 에 설치·재로드는 **사용자 확인 뒤**(Task 7).
- 커밋은 내 파일만, 메시지 끝에 `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.

## File Structure

| 파일 | 책임 |
|---|---|
| `web/conn.go` (신규) | 연결 공용부: `serveConn`(읽기 루프·핑·정리), `roundTrip`(요청 보내고 답 기다리기). `handleExt` 와 `handleVSCode` 가 공유 |
| `web/daemon.go` (수정) | `handleExt`·`call` 이 공용부를 쓰게 정리, `vscodes` 등록부 필드 |
| `web/vscode.go` (신규) | `/vscode` 핸들러, 창 이름 짓기, `resolveVSCode`, 목록 줄, 도구·프로필 짝 검사 |
| `web/vscode_test.go` (신규) | 위 시험 |
| `web/command.go` (수정) | `vscode_*` 도구 9개 |
| `vscode/package.json`, `vscode/extension.js` (신규) | 확장 본체: 연결·재연결·핑·메서드 분기 |
| `vscode/methods.js` (신규) | 메서드 구현(`vscode` API 를 인자로 받아 시험 가능하게) |
| `vscode/terminals.js` (신규) | 셸 통합 실행·출력 버퍼·ANSI 제거 |
| `vscode/test/*.test.js` (신규) | 가짜 `vscode` 로 시험 |
| `vscode/install.ps1`, `vscode/README.md` (신규), `web/README.md` (수정) | 패키징·설치·문서 |

---

### Task 1: 연결 공용부를 뽑아낸다 (동작 변화 없음)

**Files:** Create `web/conn.go` · Modify `web/daemon.go`

**Interfaces — Produces:**
```go
// serveConn runs one extension-like connection until it drops: keepalive pings,
// reply routing to d.pending, and on exit failing only this connection's waiters
// and removing it from reg via unregister.
func (d *Daemon) serveConn(ec *extConn, label string, unregister func())
// roundTrip sends one request on ec and waits for its reply (callTimeout).
func (d *Daemon) roundTrip(ec *extConn, method string, params map[string]any, label string) CallResult
```

- [ ] `handleExt` 의 핑 시작~정리 부분을 `serveConn` 으로, `call` 의 id 발급~응답 대기 부분을 `roundTrip` 으로 옮긴다. 로그 문구는 `label`(크롬이면 계정) 을 넣어 지금과 같게.
- [ ] `GOFLAGS=-mod=readonly go test ./web/ -race` — 기존 시험 전부 PASS(이 태스크의 시험은 "안 깨짐").
- [ ] 커밋 `refactor(web): share the connection loop and round trip between connection kinds`

---

### Task 2: `/vscode` 엔드포인트와 등록부

**Files:** Create `web/vscode.go`, `web/vscode_test.go` · Modify `web/daemon.go`

**Interfaces — Produces:**
```go
type vscodeWindow struct{ Name, Remote, Folder, Session string }
func vscodeProfileName(w vscodeWindow) string   // "vscode:"+appSlug(Name) [+ "@"+appSlug(Remote)]
func isVSCodeProfile(name string) bool           // "vscode:" 접두
func (d *Daemon) handleVSCode(w http.ResponseWriter, r *http.Request)
func (d *Daemon) resolveVSCode(name string) (*extConn, error) // 정확 > 유일 접두; 겹침·없음 오류; 빈 이름은 오류(기본값 없음)
func (d *Daemon) vscodeProfileLines() []string
```
- `Daemon` 에 `vscodes map[string]*extConn`, `vscodeInfo map[string]vscodeWindow`(표시용). 같은 이름이 이미 있고 `session` 이 다르면 `#2`, `#3` 을 붙인다. 같은 `session` 이 다시 오면 교체(재접속).
- 루프백이 아닌 `RemoteAddr` 는 403.
- `listProfiles` 에 `vscodeProfileLines()` 를 앱 줄 뒤에 붙인다. 줄 모양: `vscode:<id> | SSH <remote> | <folder> | connected <d> ago` (로컬 창이면 `local`).

- [ ] **시험:** (a) 가짜 확장이 `/vscode?name=backend&remote=host1&folder=/p&session=s1` 로 붙으면 `list_profiles` 에 `vscode:backend@host1 | SSH host1 | /p |` 줄 (b) 같은 name·다른 session 두 개 → `…@host1` 과 `…@host1#2` (c) VS Code 창만 붙은 상태에서 크롬 도구 `call("get_page_text",…,"")` 는 "Chrome extension not connected" — VS Code 가 기본 프로필로 뽑히지 않음 (d) `resolveVSCode("vscode:back")` 유일 접두, `""` 오류, 겹침 오류에 두 이름 (e) 연결이 끊기면 등록부에서 사라짐.
- [ ] FAIL → 구현 → PASS(`-race`) → 커밋 `feat(web): accept VS Code windows on /vscode as vscode: profiles`

---

### Task 3: `vscode_*` 도구와 라우팅

**Files:** Modify `web/command.go`, `web/daemon.go`, `web/vscode.go`, `web/vscode_test.go`

- `command.go` 에 9개: `vscode_workspace`, `vscode_editors`, `vscode_read`(path, startLine, endLine), `vscode_open`(path 필수, line), `vscode_problems`(path), `vscode_terminals`, `vscode_terminal_run`(command 필수, terminal, timeoutSec), `vscode_terminal_read`(terminal, max), `vscode_command`(command 필수, args — JSON 배열 문자열). 설명은 한 줄 요약 + "profile 에 vscode:<name> 필수(list_profiles)".
- `call()` 분기 순서: list_profiles → `vscode_` 접두 메서드면 `callVSCode` → 앱 → 크롬. `callVSCode`: 프로필이 `vscode:` 아니면 `"<tool> needs a VS Code window: pass profile=\"vscode:<name>\" (see list_profiles)"`. 크롬/앱 경로는 `vscode:` 프로필이면 `"<tool> works on web pages; for a VS Code window use the vscode_* tools"`.
- 확장으로 보낼 때 메서드 이름은 `vscode_` 를 뗀 것(`workspace`, `read` …), `timeoutSec` 가 있으면 그 시간+5초로 대기(`roundTrip` 에 timeout 인자 추가).
- `serverInstructions` 에 VS Code 문단 추가.

- [ ] **시험:** (a) 가짜 확장에 `vscode_read` → 확장이 `method:"read"` 받고 답이 그대로 돌아옴 (b) 프로필 짝 안 맞는 두 방향 거절 문구 (c) `vscode_terminal_run timeoutSec=120` 이면 30초 기본을 넘어 기다림(가짜가 35초 뒤 답해도 성공 — 시험에서는 `callTimeout` 을 필드로 줄여 확인) (d) 안내문에 `vscode:` 포함.
- [ ] FAIL → 구현 → PASS → 커밋 `feat(web): add vscode_* tools routed to VS Code windows`

---

### Task 4: 확장 본체 — 연결·메서드(터미널 제외)

**Files:** Create `vscode/package.json`, `vscode/extension.js`, `vscode/methods.js`, `vscode/test/fakevscode.js`, `vscode/test/methods.test.js`, `vscode/test/extension.test.js`

**Interfaces:**
```js
// methods.js
function createMethods(vscode, terminals) -> { workspace(p), editors(p), read(p), open(p), problems(p), command(p), terminals(p), terminal_run(p), terminal_read(p) }
// each returns Promise<string> (the reply text) or throws Error (reply error)
// extension.js
function activate(context)   // starts connector unless aglink.enabled === false
function createConnector({ vscode, WebSocketImpl, readPortFile, methods, identity, log }) -> { start(), stop() }
```
- `package.json`: `name: "aglink-vscode"`, `publisher: "aglink"`, `engines.vscode: "^1.93.0"`, `main: "./extension.js"`, `activationEvents: ["onStartupFinished"]`, `extensionKind: ["ui"]`, 설정 `aglink.enabled`(bool, true), `aglink.port`(number, 0=포트 파일/48219).
- 포트: `~/.aglink/aglink-web.port` → 없으면 48219. `AGLINK_HOME` 이 있으면 그 폴더.
- 재연결: 1초에서 두 배씩 최대 30초. 핑(`{id:0,method:"ping"}`) → `{id:0,ok:true}`.
- 결과 문자열 형식:
  - `workspace`: `name: …` / `remote: …` / `folders: …` / `active: <path>:<line>` / `editors: N` / `terminals: N`
  - `editors`: 줄마다 `[active] [modified] <path>` (탭 그룹 순)
  - `read`: `<path> (lines a-b of N)` + 줄번호 붙인 본문(`   12| …`), 최대 2000줄·200KB, 넘치면 잘렸다고 표시. 경로는 작업 폴더 기준 상대 경로 또는 절대 경로 둘 다 받음(원격이면 원격 경로).
  - `open`: `ok: opened <path>[:line]`
  - `problems`: 줄마다 `<severity> <path>:<line>:<col> <message> [<source>]`, 없으면 `no problems`, 최대 200개
  - `command`: `ok: ran <id>` + 결과가 있으면 JSON(512자 절단)

- [ ] **시험(가짜 vscode):** 각 메서드 형식, `read` 가 저장 안 된 문서 내용을 읽음, 상대 경로를 원격 URI 로 바꿈(`vscode-remote://ssh-remote+host1/p/a.txt`), 잘림 표시, `problems` 상한, 알 수 없는 메서드 오류. 커넥터: 가짜 WebSocket 으로 접속 URL 에 name/remote/folder/session, 핑 응답, 요청→응답 id 짝, 끊기면 재연결 지연 증가, `aglink.enabled=false` 면 접속 안 함.
- [ ] FAIL → 구현 → PASS → 커밋 `feat(vscode): extension that connects each window to aglink-web`

---

### Task 5: 터미널

**Files:** Create `vscode/terminals.js`, `vscode/test/terminals.test.js` · Modify `vscode/methods.js`, `vscode/extension.js`

**Interfaces:**
```js
function createTerminals(vscode) -> {
  list() -> string,
  run({ command, terminal, timeoutSec }) -> Promise<string>,
  read({ terminal, max }) -> string,
  dispose()
}
function stripAnsi(s) -> string
```
- 켜질 때 `window.onDidStartTerminalShellExecution` 를 구독해 **모든** 실행의 `read()` 를 모아 터미널별 링버퍼(최근 20개, 각 64KB, 넘치면 앞을 버리고 `…(앞부분 생략)`)에 넣고, `onDidEndTerminalShellExecution` 에서 `exitCode` 기록.
- `run`: 터미널 이름이 주어지면 그걸, 없으면 이름이 `aglink` 인 터미널(없으면 생성). `shellIntegration` 이 없으면 생성 직후일 수 있으니 `onDidChangeTerminalShellIntegration` 을 최대 5초 기다림. 그래도 없으면 `sendText` 후 `ok: sent to <name> (no shell integration — output cannot be read)`. 있으면 `executeCommand` → 끝까지 수집 → `$ <cmd>\n<출력>\n[exit <code>]`, 시간 초과면 `[still running after Ns]`.
- `list`: `<name> | shell integration: yes/no | last: <cmd> [exit n]`.
- `read`: 지정 터미널(없으면 활성) 최근 `max`(기본 5)개 실행을 `$ cmd` / 출력 / `[exit n]` 로.

- [ ] **시험(가짜 셸 통합):** 실행 수집·종료 코드, 링버퍼 개수·크기 상한, ANSI 제거(`\x1b[31m`, OSC `\x1b]633;…\x07`), 셸 통합 없는 터미널 문구, 시간 초과 문구, 없는 터미널 이름 오류.
- [ ] FAIL → 구현 → PASS → 커밋 `feat(vscode): run commands in a window's terminal and read their output`

---

### Task 6: 패키징·설치·문서

**Files:** Create `vscode/install.ps1`, `vscode/README.md`, `vscode/.vscodeignore` · Modify `web/README.md`

- `install.ps1`: `vscode/` 의 배포 파일(`package.json`, `extension.js`, `methods.js`, `terminals.js`, `README.md`)을 `extension/` 아래에 넣고 `[Content_Types].xml`·`extension.vsixmanifest` 를 생성해 `.vsix` 로 zip(`System.IO.Compression`). `-InstallOnly`/`-PackageOnly` 스위치. 설치 전에 `ELECTRON_RUN_AS_NODE`·`VSCODE_*` 제거 후 `code --install-extension <vsix> --force`.
- README: 무엇을 하는지, 설치, 쓰는 법(`list_profiles` → `vscode:…`), 도구 표, 원격 창 동작, 권한 주의(터미널·명령 = 그 창 권한), 끄는 법(`aglink.enabled`).
- [ ] `install.ps1 -PackageOnly` 로 `.vsix` 생성 → 구조 확인(zip 목록에 manifest·package.json).
- [ ] 커밋 `feat(vscode): package and install script; document the VS Code integration`

---

### Task 7: 실물 점검 (사용자 확인 뒤)

- [ ] 데몬: 운영 데몬은 두고 격리 시험 데몬(`AGLINK_HOME`=임시, 포트 48299). 확장은 설정 `aglink.port` 로 48299 를 보게 해서 운영 데몬과 섞이지 않게 — **사용자 VS Code 설정을 바꾸는 일이라 확인 뒤.**
- [ ] 설치 → 창들이 `list_profiles` 에 뜨는지 → `backend` 원격 창: `vscode_workspace`, `vscode_editors`, `vscode_read`(열린 파일), `vscode_problems`, `vscode_terminal_run "pwd; ls | head"`, `vscode_terminal_read`.
- [ ] 결과(특히 설계서 위험 항목: 원격 진단, 원격 셸 통합, 재시작 없이 켜지는지)를 README 에 기록.
- [ ] 정리: 시험 데몬 종료, `aglink.port` 설정 원복 여부 사용자와 결정.

## Self-Review

- 설계 반영: 창별 연결·프로필(T2), 등록부 분리(T2 c), 도구 9개(T3·4·5), 짝 거절(T3), 셸 통합 실행·버퍼·셸 통합 없음(T5), UI 확장·의존성 0(T4), 설치 스크립트·ELECTRON 변수(T6), 위험 3항목 실측(T7), 안전 문구(T6 README), `aglink.enabled`(T4).
- 확장 쪽 결과 형식은 이 계획이 정본 — 데몬은 문자열을 그대로 전달만 한다.
