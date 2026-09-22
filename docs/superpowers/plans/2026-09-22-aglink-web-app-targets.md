# aglink-web 앱 대상(Electron·Wails) 구현 계획 — 1단계

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** aglink-web 데몬이 CDP 로 Electron·Wails 앱 창에 직접 붙어, 기존 도구를 `profile: "app:…"` 로 그대로 쓰고 앱의 alert/confirm/prompt 를 다룬다.

**Architecture:** `Daemon.call` 에서 `app:`/`cdp:` 프로필을 새 `appTarget` 으로 가른다. `appTarget` 은 포트별 CDP WebSocket 을 계속 열어 두고, 확장과 **같은** 페이지 JS(`aglink-inject.js` + 새 `page-actions.js`)를 `Runtime.evaluate` 로 주입해 부른다. 결과 꾸미기만 Go 로 옮긴다.

**Tech Stack:** Go (`web/` 모듈), `github.com/gorilla/websocket`(이미 의존), `mark3labs/mcp-go`, 확장은 순수 JS + `node --test`.

**Spec:** [`docs/superpowers/specs/2026-09-22-aglink-web-app-targets-design.md`](../specs/2026-09-22-aglink-web-app-targets-design.md)

## Global Constraints

- Go 명령은 `GOFLAGS=-mod=readonly` 를 앞에 붙인다(이 머신의 전역 GOFLAGS 가 `-mod=mod`).
- 확장 시험은 `node --test extension/` (web/ 에서). 외부 npm 의존 추가 금지.
- **데몬은 `127.0.0.1` 밖의 CDP 에 절대 붙지 않는다.**
- 공개 저장소 — 주소·계정·비밀값을 코드·시험·문서에 적지 않는다.
- 확장의 페이지 함수 본문은 **글자 그대로** 옮긴다. 기존 `background.test.js` 가 그대로 통과해야 한다.
- 크롬 경로(`d.exts`, `/ext`)의 동작은 바꾸지 않는다.
- 커밋은 내 파일만 `git add`, 메시지 끝에 `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
- 운영 중인 데몬 교체·확장 재로드는 **사용자 확인 뒤** (Task 8).

## File Structure

| 파일 | 책임 |
|---|---|
| `web/extension/page-actions.js` (신규) | 페이지 안 함수 17개를 `globalThis.__aglinkPage` 에 모음. 확장·데몬 공용 |
| `web/extension/background.js` (수정) | 각 도구의 인라인 `func` 를 `__aglinkPage[이름]` 호출로 교체, 주입 목록에 `page-actions.js` 추가 |
| `web/extension/background.test.js` (수정) | 주입 파일 목록 기대값 갱신 + `page-actions.js` 시험 |
| `web/pagejs.go` (신규) | 두 JS 파일 `go:embed` |
| `web/cdp.go` (신규) | 최소 CDP 클라이언트: 요청/응답 짝짓기, 이벤트 구독, 끊김 감지 |
| `web/cdpdiscover.go` (신규) | 포트 탐색, 대상 목록, 프로필 이름(`app:`/`cdp:`) 해석 |
| `web/apptarget.go` (신규) | 포트별 연결 관리, 주입 보장, 창(dialog) 상태, 경주 |
| `web/apptools.go` (신규) | 메서드→페이지 함수 호출 + 결과 꾸미기(background.js 후처리의 Go 판) |
| `web/daemon.go` (수정) | `call` 분기, `listProfiles` 에 앱 추가 |
| `web/command.go` (수정) | `dialog_status`, `handle_dialog` 도구 |
| `web/README.md`, `desktop/main.go`, `desktop/README.md` (수정) | 문서 + 본보기 한 줄 |

---

### Task 1: 페이지 함수를 `page-actions.js` 로 옮긴다

**Files:** Create `web/extension/page-actions.js` · Modify `web/extension/background.js`, `web/extension/background.test.js`

**Interfaces:**
- Produces: `globalThis.__aglinkPage` = `{ getPageText, elementExists, click, doubleClick, hover, drag, getHtml, queryAll, evalExpression, getAttribute, listElements, waitForElement, typeText, getValue, keyCombo, scroll, selectOption }`. 각 함수의 인자·반환은 **지금 background.js 인라인 func 과 동일**. `KEY_SPECS`, `MOD_PROPS`, `INTERACTIVE_SELECTOR`, `AGLINK_ID_ATTR` 은 page-actions.js 안에도 정의(인자 전달이 아니라 자체 참조). background.js 쪽 같은 상수는 후처리에서 쓰므로 남긴다.

규칙:
- 옮기는 17개: 위 목록. `getConsoleLogs`/`getNetworkRequests` 는 MAIN 세계의 캡처 버퍼를 읽으므로 **그대로 둔다.**
- `evalExpression` 은 확장에서 `world: "MAIN"` 이다. 이것만은 확장에서 `page-actions.js` 를 MAIN 세계에도 주입해야 하므로, **확장 쪽은 인라인으로 남기고** page-actions.js 에는 사본을 둔다(데몬용). 사본 위에 "background.js evalExpression 과 동일하게 유지" 주석.
- background.js 호출 모양:

```js
// before
func: (sel) => { ... },
args: [selector],
// after
func: (name, args) => globalThis.__aglinkPage[name](...args),
args: ["getPageText", [selector]],
```

`ensureHelpers` 는 `files: ["aglink-inject.js", "page-actions.js"]` 로. 선택자 없는 호출(`getPageText` 무선택자, `getHtml` 무선택자, `listElements`, `keyCombo`, `scroll`…)도 이제 page-actions 가 필요하므로 **해당 도구는 무조건 `ensureHelpers` 를 부른다.**

- [ ] **Step 1: 실패하는 시험** — `background.test.js` 에 추가:

```js
test("page-actions.js defines every shared page function", () => {
  const src = fs.readFileSync(path.join(__dirname, "page-actions.js"), "utf8");
  const sb = { globalThis: {} };
  sb.globalThis = sb;
  vm.createContext(sb);
  vm.runInContext(src, sb);
  const want = ["getPageText","elementExists","click","doubleClick","hover","drag","getHtml",
    "queryAll","evalExpression","getAttribute","listElements","waitForElement","typeText",
    "getValue","keyCombo","scroll","selectOption"];
  for (const n of want) assert.strictEqual(typeof sb.__aglinkPage[n], "function", n);
});

test("shared page functions run through __aglinkPage with the original args", async () => {
  const calls = [];
  const sb = loadBackground({ scripting: { executeScript: async (o) => { calls.push(o); return [{ result: { found: true, text: "hi" } }]; } },
                              tabs: { query: async () => [{ id: 5 }] } });
  await sb.getPageText({ selector: "#x" });
  const run = calls.find((c) => c.func && c.args);
  assert.deepStrictEqual(run.args, ["getPageText", ["#x"]]);
  const inject = calls.find((c) => c.files);
  assert.deepStrictEqual(inject.files, ["aglink-inject.js", "page-actions.js"]);
});
```

- [ ] **Step 2:** `cd web && node --test extension/` → 새 시험 2개 FAIL (파일 없음)
- [ ] **Step 3:** `page-actions.js` 작성 — 17개 함수 본문을 background.js 에서 글자 그대로 복사, 상수 4개 복사, `(function(){ const P = {...}; globalThis.__aglinkPage = P; })();`. background.js 의 각 호출을 위 모양으로 교체.
- [ ] **Step 4:** `node --test extension/` → 기존 시험 포함 전부 PASS. 기존 "selector commands inject the shared resolver helper first" 가 파일 목록을 검사해 실패하면 기대값을 `["aglink-inject.js","page-actions.js"]` 로 갱신.
- [ ] **Step 5:** 커밋 `refactor(web/ext): move page-side functions into a shared page-actions.js`

---

### Task 2: 최소 CDP 클라이언트

**Files:** Create `web/cdp.go`, `web/cdp_test.go`

**Interfaces:**
- Produces:
```go
type cdpConn struct{ /* ws, writeMu, nextID, pending map[int64]chan cdpMsg, subs []func(cdpMsg), done chan struct{} */ }
type cdpMsg struct {
	ID     int64           `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct{ Code int; Message string } `json:"error,omitempty"`
}
func dialCDP(wsURL string) (*cdpConn, error)          // 127.0.0.1 아닌 호스트면 오류
func (c *cdpConn) Call(ctx context.Context, method string, params any) (json.RawMessage, error)
func (c *cdpConn) OnEvent(fn func(method string, params json.RawMessage))
func (c *cdpConn) Done() <-chan struct{}
func (c *cdpConn) Close() error
```

- [ ] **Step 1: 시험** — httptest + gorilla upgrader 로 가짜 CDP 서버. (a) `Call` 이 id 로 짝지어진 result 를 받음 (b) 응답이 뒤섞여 와도 각자 제 것 (c) `error` 필드 → Go error (d) id 없는 메시지는 `OnEvent` 로 (e) 서버가 끊으면 대기 중 `Call` 이 즉시 오류, `Done()` 닫힘 (f) `dialCDP("ws://10.0.0.5:9222/…")` 는 연결 시도 없이 오류.
- [ ] **Step 2:** FAIL 확인 · **Step 3:** 구현(읽기 goroutine 하나, 쓰기 mutex) · **Step 4:** `GOFLAGS=-mod=readonly go test ./ -run CDP -race` PASS · **Step 5:** 커밋 `feat(web): minimal CDP client over websocket`

---

### Task 3: 앱 찾기와 이름 해석

**Files:** Create `web/cdpdiscover.go`, `web/cdpdiscover_test.go`

**Interfaces:**
- Produces:
```go
type appInfo struct {
	Port    int
	Title   string // 첫 page 대상의 제목
	URL     string
	Pages   []cdpTarget
}
type cdpTarget struct{ ID, Type, Title, URL, WebSocketDebuggerURL string }
func cdpPorts() []int                                  // AGLINK_WEB_CDP_PORTS "9222-9240" 또는 "9222,9333"; 기본 9222..9240
func discoverApps(ctx context.Context, ports []int) []appInfo   // 동시 300ms 탐색, 포트 순 정렬
func appSlug(title string) string                      // 소문자, 공백·연속 비영숫자 → "-", 양끝 "-" 제거, 빈 값이면 "app"
func isAppProfile(name string) bool                    // "app:" 또는 "cdp:" 접두
func resolveApp(apps []appInfo, name string) (appInfo, error)  // cdp:포트 정확 / app:접두 유일 / 겹침·없음 오류
```

- [ ] **Step 1: 시험** — httptest 서버 두 개로 `/json/version`(200) + `/json/list`(page 1개, devtools 대상 1개 섞음) 흉내. 포트는 서버 주소에서 뽑아 `discoverApps` 에 전달. 검사: 두 앱 발견, `Pages` 에 `type=="page"` 만, 죽은 포트는 빠짐, 300ms 넘는 서버는 빠짐. `appSlug("aglink")=="aglink"`, `appSlug("My Tool — Dev")=="my-tool-dev"`, `appSlug("")=="app"`. `resolveApp`: `app:agl`→aglink, `cdp:9333` 정확, `app:x` 없음 오류, 두 앱 제목이 같은 접두면 겹침 오류(메시지에 두 이름).
- [ ] **Step 2~4:** FAIL → 구현 → `go test -run 'Discover|Slug|ResolveApp'` PASS
- [ ] **Step 5:** 커밋 `feat(web): discover CDP apps on loopback and resolve app:/cdp: names`

---

### Task 4: 앱 연결 관리 · 주입 · 창 상태 · 경주

**Files:** Create `web/pagejs.go`, `web/apptarget.go`, `web/apptarget_test.go`

**Interfaces:**
- Consumes: Task 2 `cdpConn`, Task 3 `cdpTarget`
- Produces:
```go
//go:embed extension/aglink-inject.js
var injectJS string
//go:embed extension/page-actions.js
var pageActionsJS string

type dialogInfo struct{ Type, Message, DefaultPrompt, URL string } // Type: alert|confirm|prompt|beforeunload
type appTarget struct{ /* port, target cdpTarget, conn *cdpConn, mu, dialog *dialogInfo */ }
func openAppTarget(t cdpTarget, port int) (*appTarget, error)  // dial + Page.enable + Runtime.enable + dialog 이벤트 구독
func (a *appTarget) Dialog() *dialogInfo
func (a *appTarget) HandleDialog(ctx context.Context, accept bool, promptText string) error
func (a *appTarget) Eval(ctx context.Context, expr string) (json.RawMessage, error) // returnByValue, awaitPromise; 창이 먼저 뜨면 errDialogOpen
func (a *appTarget) CallPage(ctx context.Context, name string, args []any) (json.RawMessage, error) // 주입 보장 후 __aglinkPage[name](...args)
var errDialogOpen = errors.New("dialog open")
```
- `CallPage` 는 먼저 `typeof globalThis.__aglinkPage === 'function'||'object'` 를 확인하고 없으면 `injectJS` 와 `pageActionsJS` 를 차례로 evaluate. 앱이 다시 로드되면 자연히 재주입된다.
- `Eval` 경주: `Runtime.evaluate` 를 goroutine 으로 보내고 `select` 로 (결과 | `dialogOpened` 채널 | ctx). 창이 이기면 `errDialogOpen` (evaluate 는 창이 닫히면 뒤늦게 끝나고 버려짐).
- `Runtime.evaluate` 결과에 `exceptionDetails` 가 있으면 그 `text`+`exception.description` 으로 오류.

- [ ] **Step 1: 시험** — 가짜 CDP 서버(Task 2 시험 도우미 재사용)가 스크립트된 응답을 보냄. (a) `CallPage` 가 처음엔 주입 evaluate 2번 + 호출 1번, 두 번째엔 호출만 (b) evaluate 응답 대신 `Page.javascriptDialogOpening{type:"confirm",message:"지울까요?"}` 를 보내면 `errDialogOpen` 이 즉시(100ms 안) 오고 `Dialog()` 가 그 내용 (c) `HandleDialog(true,"")` 가 `Page.handleJavaScriptDialog{accept:true}` 를 보내고 `javascriptDialogClosed` 뒤 `Dialog()==nil` (d) exceptionDetails → 오류 문자열에 설명 포함.
- [ ] **Step 2~4:** FAIL → 구현 → `go test -run AppTarget -race` PASS
- [ ] **Step 5:** 커밋 `feat(web): hold a CDP connection per app, inject the shared page JS, track dialogs`

---

### Task 5: 앱에서의 도구 동작과 결과 꾸미기

**Files:** Create `web/apptools.go`, `web/apptools_test.go`

**Interfaces:**
- Consumes: Task 4 `appTarget`
- Produces: `func appCall(ctx context.Context, a *appTarget, pages []cdpTarget, method string, params map[string]any) (CallResult)`

규칙: 각 메서드는 background.js 의 같은 이름 핸들러를 **그대로 번역**한다 — 인자 검증 문구, 기본값(`DEFAULT_MAX_CHARS` 등), 결과 문자열. 페이지 함수 호출은 `CallPage` 로. 번역 대상과 background.js 위치:

| method | background.js 함수 | 페이지 함수 |
|---|---|---|
| get_page_text | getPageText (cursor/offset 포함) | getPageText |
| element_exists | elementExists | elementExists |
| click / double_click / hover / drag | click / doubleClick / hover / drag | 같은 이름 |
| get_html / query_all / get_attribute / get_value | getHtml / queryAll / getAttribute / getValue | 같은 이름 |
| list_elements | listElements | listElements |
| wait_for_element | waitForElement (폴링 루프는 Go 로) | waitForElement |
| type / key / scroll / select_option | typeText / keyCombo / scroll / selectOption | 같은 이름 |
| eval | evalExpression | evalExpression |
| screenshot | — | `Page.captureScreenshot{format:"png"}` → `data` 그대로(base64) |
| navigate | — | `Page.navigate{url}` 후 `document.readyState==='complete'` 폴링(10s) → `ok: navigated — <title> — <url>` |
| list_tabs | — | `pages` 를 `N | title | url` (N=1부터) |
| activate_tab | — | `Target.activateTarget{targetId}` → `ok: activated N` |
| dialog_status | — | `Dialog()` → `confirm: "…"` / `no dialog open` |
| handle_dialog | — | `accept`(기본 true), `prompt_text` |
| close_tab, reload_extension, get_console_logs, get_network_requests | — | `not supported for app profiles: <method>` |

어떤 호출이든 `errDialogOpen` 이면 `dialog open: <type> "<message>" — call handle_dialog to answer it` 오류.

- [ ] **Step 1: 시험** — 가짜 `appTarget`(인터페이스로 추상화: `CallPage`/`Eval`/`Dialog`/`HandleDialog`/`Screenshot`)로 각 행의 결과 문자열을 background.test.js 의 기대값과 **같은 입력·같은 문자열**로 검사. 최소: get_page_text(선택자 없음/있음/미발견/잘림/cursor), click(기본 left 보고), query_all 한 줄 형식, list_elements 행 형식, get_attribute 없음, list_tabs, dialog_status 두 경우, 미지원 메서드, errDialogOpen 문구.
- [ ] **Step 2~4:** FAIL → 구현 → PASS
- [ ] **Step 5:** 커밋 `feat(web): run the tool set against an app target`

---

### Task 6: 데몬 분기와 새 도구

**Files:** Modify `web/daemon.go`, `web/command.go`, `web/daemon_test.go`

**Interfaces:**
- Consumes: Task 3·4·5
- Produces: `Daemon.apps map[int]*appTarget` (포트→연결, 끊기면 제거) · `func (d *Daemon) callApp(method string, params map[string]any, profile string) CallResult`

변경:
- `call()` 맨 앞: `if isAppProfile(profile) { return d.callApp(...) }`.
- `callApp`: `discoverApps`(캐시 5초) → `resolveApp` → 포트의 `appTarget` 재사용 또는 `openAppTarget` (tabId 가 있으면 그 번호의 page) → `appCall`.
- `listProfiles`: 크롬 줄 뒤에 앱 줄 `app:<slug> | cdp:<port> | <url> | <n> window(s)`. 크롬이 하나도 없어도 앱만 보여 줄 수 있게 "no Chrome profiles" 문구는 앱도 없을 때만.
- `dialog_status`/`handle_dialog` 를 `command.go` 에 추가(`handle_dialog` 인자: `accept` string "true"/"false" 기본 true, `prompt_text`). 크롬 프로필로 오면 `d.call` 이 확장에 보내기 전에 `dialog handling is only available for app profiles in this version` 로 거절.

- [ ] **Step 1: 시험** — (a) `call("get_page_text", …, "app:x")` 가 확장 쪽 `pending` 을 건드리지 않음(가짜 CDP 로 응답) (b) 크롬 프로필 호출은 기존 시험 전부 그대로 통과 (c) `listProfiles` 에 앱 줄 (d) 크롬 프로필의 `dialog_status` 거절 문구.
- [ ] **Step 2~4:** FAIL → 구현 → `go test ./ -race` 전체 PASS
- [ ] **Step 5:** 커밋 `feat(web): route app:/cdp: profiles to CDP and add dialog tools`

---

### Task 7: 앱 쪽 한 줄과 문서

**Files:** Modify `desktop/main.go`, `desktop/README.md`, `web/README.md`

- [ ] **Step 1:** `desktop/main.go` Windows 옵션에 `AdditionalBrowserArgs: devtoolsArgs(),` 와 설계서의 `devtoolsArgs()` 함수(`os` import). `cd desktop && GOWORK=off go build ./...` 로 컴파일 확인(배포 빌드는 하지 않음).
- [ ] **Step 2:** `web/README.md` 에 "앱 화면 다루기" 절 — Wails 한 줄, Electron 인자, `list_profiles` 에 나오는 모양, `app:`/`cdp:` 이름, `AGLINK_WEB_CDP_PORTS`, 창 도구, 미지원 도구, 디버그 포트 주의. `desktop/README.md` 에 `AGLINK_WEBVIEW_DEBUG_PORT` 한 문단.
- [ ] **Step 3:** 커밋 `feat(desktop): opt-in DevTools port for aglink-web; document app targets`

---

### Task 8: 실물 점검

- [ ] **Step 1:** HEAD 기준 워크트리에서 `aglink-web.exe` 빌드.
- [ ] **Step 2:** aglink 데스크톱을 `AGLINK_WEBVIEW_DEBUG_PORT=9333` 로 **새로 빌드한 사본**으로 띄움(운영 중인 것 무관).
- [ ] **Step 3:** 데몬은 운영 중이라 교체 전 사용자 확인. 확인 전에는 **다른 포트로 두 번째 데몬**(`AGLINK_WEB_PORT` 류 설정이 있으면 사용, 없으면 `aglink-web serve` 를 임시 포트로)을 띄워 거기서 점검. — 설정 방법은 `datadir.go` 의 `configuredPort()` 확인 후 결정.
- [ ] **Step 4:** `list_profiles` → `app:aglink` 보임 · `get_page_text` · `list_elements` · `click`(사이드바 탭) · `type`(입력칸) · `screenshot` · `eval` 로 `setTimeout(()=>confirm('시험'),0)` → `dialog_status` → `handle_dialog accept=false`.
- [ ] **Step 5:** 크롬 쪽 회귀: 확장 재로드(사용자 확인 뒤) 후 `get_page_text`·`click`·`type` 이 그대로 되는지.
- [ ] **Step 6:** 시험 인스턴스 정리(내가 띄운 PID 만), 결과를 README 에 한 줄, 커밋·푸시는 사용자 요청 시.

---

## Self-Review

- **설계 반영:** 앱 프로필 분기(T6), 설정 없는 탐색(T3), 창 번호(T5 list_tabs/T6), 페이지 로직 공유(T1·T4), 창 도구·경주(T4·T5·T6), 도구별 지원표(T5), 루프백 제한(T2), 앱 한 줄·문서(T7), 실물(T8). 크롬 alert 는 비목표로 제외.
- **알려진 차이:** `evalExpression` 은 확장에서 MAIN 세계라 인라인으로 남기고 page-actions 에 사본 — 설계서의 "한 벌" 원칙의 유일한 예외. 사본에 동기화 주석.
- **열린 확인:** T8 Step 3 의 임시 포트 방식은 `datadir.go` 를 보고 정한다.
