# aglink-web 으로 Electron·Wails 앱 화면 다루기

**날짜:** 2026-09-22
**대상:** `aglink/web` (데몬 + 확장), 본보기로 `aglink/desktop`

## 문제

사용자가 만드는 윈도우 앱은 대부분 Electron 이나 Wails 위에 Svelte 로 UI 를
얹은 구조다. 그 UI 를 에이전트가 다루려면 지금은 aglink-screen 밖에 없다.
화면 캡처·접근성 트리로 읽으니 토큰이 많이 들고, 좌표 기반이라 무겁다.

aglink-web 은 같은 일을 텍스트로 싸게 하지만 **크롬 확장**으로 동작한다. 앱에
박힌 웹뷰는 크롬이 아니라 확장을 설치할 곳이 없다.

같은 이유로 `alert`/`confirm`/`prompt` 창도 못 다룬다. 확장의 모든 도구는 페이지
안에서 JS 를 돌리는데, 이 창들은 페이지 JS 를 멈춰 세운다. 도구는 창이 닫힐
때까지 대기하다 시간 초과가 나고, 창은 DOM 에 없으니 보이지도 않는다.

## 목표

- Electron·Wails 앱의 화면을 **일반 웹페이지처럼** aglink-web 도구로 다룬다.
- 도구 이름·인자는 그대로. `profile` 만 바꿔 대상을 고른다.
- 앱에서 `alert`/`confirm`/`prompt` 를 보고 답한다.
- 크롬과 앱이 **같은 코드로** 요소를 찾고 이벤트를 만든다.

## 비목표

- 크롬의 `alert`/`confirm`. 확장에 디버거 권한이 필요하고, "이미 떠 있는 창에
  나중에 붙어도 보이는가"를 먼저 시험해야 방식이 정해진다. **2단계**로 뺀다.
- 앱의 콘솔 로그·네트워크 요청 수집(`get_console_logs`, `get_network_requests`).
  CDP 로 가능하지만 1단계 범위 밖. 앱 프로필에서는 분명한 오류로 거절한다.
- 앱을 띄우거나 끄는 것. 이미 디버그 포트로 떠 있는 앱에 붙기만 한다.
- 페이지 JS 를 바꿔치는 방식(`window.alert` 덮어쓰기 등). 사용자가 명시적으로
  기각했다 — 페이지와 사람의 경험을 바꾸기 때문이다.

## 실측한 것

모두 이 PC 에서 aglink 데스크톱(Wails v3, WebView2)으로 확인했다.

### Wails 는 환경변수로 포트를 열 수 없다

`WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=…` 를 주고 띄우면
WebView2 자식 프로세스가 그 인자를 **받지 못한다.** Wails 의 WebView2 로더가
보안상 의도적으로 지운다.

```go
// github.com/wailsapp/wails/webview2 v1.0.24 — webviewloader/env_create.go
func preventEnvAndRegistryOverrides() {
	os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "")
	...
}
```

### 앱 옵션으로는 열린다

`application.WindowsOptions{ AdditionalBrowserArgs: []string{"--remote-debugging-port=9333"} }`
를 넣고 빌드하면 CDP 가 뜬다.

```
GET http://127.0.0.1:9333/json/list
  "title": "aglink", "type": "page", "url": "http://wails.localhost/",
  "webSocketDebuggerUrl": "ws://127.0.0.1:9333/devtools/page/…"
```

그러니 **Wails 앱은 앱마다 한 줄이 필요하다.** 환경변수가 있을 때만 켜지게 해서
평소 실행·배포판에는 포트가 안 열리게 한다. Electron 은 이런 차단이 없어
`--remote-debugging-port` 실행 인자로 된다.

### Svelte UI 가 텍스트로 읽힌다

CDP `Runtime.evaluate` 로 읽은 aglink 데스크톱 화면:

```
버튼  : 🌙 ⊞ ⚙ 🔌 « 대화 · 업무 관리 · 예약 · ↻ ＋ …
입력칸: "메시지 또는 !명령을 입력하세요"
본문  : 연결됨 | aglink | Claude | 대화 목록 | 텔레그램 채널 | 로컬 채널 | …
```

스크린샷 한 장(~1,800 비전 토큰) 대신 몇백 바이트다.

### 확장 도구 중 크롬 API 를 쓰는 건 스크린샷뿐이다

`click`/`type`/`key`/`hover`/`drag`/`scroll`/`select_option` 은 전부 페이지 안에서
`el.click()`·`dispatchEvent(new MouseEvent…)` 로 이벤트를 만든다.
`chrome.tabs.captureVisibleTab` 을 쓰는 `screenshot` 만 예외다. 그러니 페이지 쪽
로직은 CDP 로 그대로 옮겨 돌릴 수 있다.

## 구조

```
MCP 도구 호출 ─→ Daemon.call(method, params, profile)
                  │
                  ├─ profile = "" / 이메일      → 크롬 확장 (지금 그대로)
                  │
                  └─ profile = "app:…"/"cdp:…" → appTarget (신규)
                                                    │  CDP WebSocket 을 데몬이 직접 유지
                                                    ├─ 페이지 쪽 JS 주입 후 Runtime.evaluate
                                                    ├─ screenshot  → Page.captureScreenshot
                                                    └─ 창 이벤트   → Page.javascriptDialog*
```

`Daemon.call` 한 곳으로 모든 호출이 모이므로, 거기서 프로필 이름만 보고
갈라 준다. 크롬 경로는 한 줄도 바뀌지 않는다.

### 앱 찾기 — 설정 없이

`list_profiles` 를 부르면 데몬이 `127.0.0.1` 의 포트 범위(기본 9222–9240,
`AGLINK_WEB_CDP_PORTS` 로 변경)에 `/json/version` 을 짧게(300ms) 동시에 두드려
떠 있는 앱을 찾는다. 찾은 앱은 크롬 프로필 아래에 이렇게 나온다:

```
doowon.lab.02@gmail.com | connected 3h ago | default
app:aglink | cdp:9333 | http://wails.localhost/ | 1 window
```

- `app:<제목>` — 창 제목을 소문자·공백은 `-` 로 바꾼 이름. 앞부분만 대도 된다.
  겹치면 되묻는다(크롬 프로필 고르기와 같은 규칙).
- `cdp:<포트>` — 항상 통하는 정확한 이름. 제목이 바뀌어도 안 변한다.

**루프백만 본다.** 데몬은 `127.0.0.1` 밖의 CDP 에는 절대 붙지 않는다. 디버그
포트는 그 앱을 통째로 조작하는 문이다.

### 창(탭)

앱 창이 여럿이면 CDP 페이지 대상도 여럿이다. `list_tabs` 는 이를 `1 | 제목 |
url` 로 번호 매겨 보여 주고, 다른 도구의 `tabId` 는 그 번호다. 생략하면 첫
페이지 대상. (크롬 탭 ID 가 정수라 도구 인자 형식이 그대로 맞는다.)

### 페이지 쪽 로직 공유

확장의 각 도구는 "페이지 안 함수 + 서비스워커 쪽 후처리"로 나뉘어 있다. 페이지
안 함수를 새 파일 `extension/page-actions.js` 로 옮겨
`globalThis.__aglinkPage = { getPageText(…), click(…), … }` 로 모은다.

- **확장:** 기존대로 `aglink-inject.js`(요소 탐색기) 와 함께 `page-actions.js` 를
  주입하고, `executeScript` 의 `func` 는 `__aglinkPage[이름](…)` 을 부르기만 한다.
  후처리는 그대로 `background.js` 에 남는다.
- **데몬:** 두 파일을 `go:embed` 로 품고 `Runtime.evaluate` 로 주입한 뒤 같은
  함수를 부른다. 후처리는 Go 로 옮긴다.

이렇게 **선택자 해석·이벤트 합성·프레임워크 입력값 처리**처럼 미묘한 버그가
사는 부분은 한 벌만 존재한다. 두 벌이 되는 것은 결과 문자열 꾸미기뿐인데, 작고
시험으로 고정할 수 있다.

앱에서는 확장처럼 격리 세계가 아니라 **페이지 기본 세계**에서 돈다.
`globalThis.__aglink`·`__aglinkPage` 두 전역이 앱 페이지에 생긴다. 대상이 사용자
자신의 개발용 앱이라 받아들인다.

### alert / confirm / prompt

데몬은 앱마다 CDP 연결을 **계속 열어 두고** `Page.enable` 로
`javascriptDialogOpening`/`Closed` 를 받는다. 떠 있는 창은 대상별로 기억한다.

- 도구 실행 중에 창이 뜨면 `Runtime.evaluate` 는 창이 닫힐 때까지 돌아오지
  않는다. 그래서 응답과 창 이벤트를 **경주**시켜, 창이 먼저 오면 즉시
  `dialog open: confirm "…" — use handle_dialog` 로 돌려준다. 시간 초과까지
  기다리지 않는다.
- 새 도구 둘:

| 도구 | 하는 일 |
|---|---|
| `dialog_status` | 떠 있는 창의 종류(alert/confirm/prompt/beforeunload)·메시지·prompt 기본값. 없으면 없다고 |
| `handle_dialog` | `accept`(기본 true) 와 `prompt_text` 로 `Page.handleJavaScriptDialog` |

크롬 프로필에서 두 도구를 부르면 "2단계 전까지 크롬은 미지원"으로 분명히 거절한다.

### 도구별 지원

| 도구 | 앱에서 |
|---|---|
| `get_page_text` `element_exists` `click` `double_click` `hover` `drag` `get_html` `query_all` `eval` `get_attribute` `list_elements` `wait_for_element` `type` `get_value` `key` `scroll` `select_option` | 공유 페이지 로직 |
| `screenshot` | `Page.captureScreenshot` |
| `navigate` | `Page.navigate` 후 로드 대기 |
| `list_tabs` | CDP 페이지 대상 |
| `activate_tab` | `Target.activateTarget` |
| `dialog_status` `handle_dialog` | 신규 |
| `close_tab` `reload_extension` `get_console_logs` `get_network_requests` | 거절(분명한 오류) |

## 앱 쪽 한 줄

**Wails** (`main.go` 의 Windows 옵션):

```go
Windows: application.WindowsOptions{
	AdditionalBrowserArgs: devtoolsArgs(),
},

// devtoolsArgs opens a Chrome DevTools Protocol port for aglink-web when
// AGLINK_WEBVIEW_DEBUG_PORT is set, and nothing otherwise — a normal launch or
// a shipped build never exposes one.
func devtoolsArgs() []string {
	if p := os.Getenv("AGLINK_WEBVIEW_DEBUG_PORT"); p != "" {
		return []string{"--remote-debugging-port=" + p}
	}
	return nil
}
```

Wails 가 지우는 것은 `WEBVIEW2_*` 환경변수뿐이라 앱이 자기 환경변수를 읽는 데는
문제가 없다.

**Electron:** 실행 인자 `--remote-debugging-port=9222`. 코드로 하려면
`app.commandLine.appendSwitch('remote-debugging-port', port)` 를 같은 환경변수
조건으로.

`aglink/desktop` 에 위 Wails 한 줄을 넣어 본보기로 둔다.

## 위험과 처리

**디버그 포트는 앱 전체를 주는 문이다.** 앱 쪽은 환경변수가 있을 때만 열고,
데몬 쪽은 루프백 밖에 붙지 않는다. Chromium 의 `--remote-debugging-port` 기본
바인딩도 루프백이다.

**확장 리팩터가 크롬 쪽을 깰 수 있다.** 확장은 지금도 다른 작업에서 쓰이는
중이다. 페이지 함수 본문은 글자 그대로 옮기고, 기존 `background.test.js`
(687줄)가 그대로 통과해야 한다. 확장 재로드는 사용자 확인 뒤에 한다.

**앱이 다시 로드되면 주입한 전역이 사라진다.** 매 호출 전에 존재를 확인하고
없으면 다시 주입한다(확장이 매번 `ensureHelpers` 하는 것과 같다).

**포트를 두드리는 비용.** 19개 포트에 300ms 동시 요청 — `list_profiles` 한 번에
최대 0.3초. 도구 호출마다가 아니라 목록을 볼 때와, 이름을 처음 해석할 때만 한다.

## 시험

- 프로필 이름 해석: `app:` 앞부분 / 겹침 / 없음 / `cdp:포트` / 크롬 이메일과 공존.
- 포트 탐색: 가짜 CDP HTTP 서버(httptest)로 `/json/version`·`/json/list` 응답.
- CDP 클라이언트: 가짜 WebSocket 서버로 요청/응답 짝짓기, 이벤트 분기, 연결 끊김.
- 창 경주: evaluate 응답 전에 `javascriptDialogOpening` 이 오면 즉시 반환.
- 확장: 기존 시험 전부 통과 + 주입 파일 목록에 `page-actions.js` 포함.
- 결과 문자열: 앱 경로와 확장 경로가 같은 입력에 같은 문자열을 내는지.
- 실물: aglink 데스크톱을 디버그 포트로 띄워 목록·읽기·클릭·입력·스크린샷, 그리고
  페이지에서 `confirm()` 을 띄워 `dialog_status`→`handle_dialog`.
