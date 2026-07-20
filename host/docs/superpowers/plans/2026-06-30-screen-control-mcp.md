# M1 — Windows 화면제어 MCP (Go) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use `- [ ]`.

**Goal:** teleclaude.exe에 Go 네이티브 Windows 화면제어 MCP 서버를 내장하고, YAML `screen_control.enabled`로 켜면 claude 워커가 UIA(우선)/스크린샷(폴백)으로 Windows GUI를 조작하게 한다.

**Architecture:** teleclaude가 자신을 숨은 인자 `__mcp-screen`으로 spawn → stdio MCP 서버. Win32(syscall, CGO-free) + UIA(go-ole COM). 워커는 `--strict-mcp-config --mcp-config <self>`로 이 서버만 로드. 화면 판독은 claude 비전(OCR 없음).

**Tech Stack:** Go 1.25, `github.com/mark3labs/mcp-go`(MCP), `github.com/go-ole/go-ole`(UIA COM), `golang.org/x/sys/windows`(Win32). CGO 없음.

## Global Constraints
- CGO 없이 빌드. Windows 전용 코드는 `//go:build windows`, 비-Windows는 stub(`//go:build !windows`)로 전체 빌드 유지.
- 기존 테스트 전부 green 유지. `go build ./...`(win+linux), `go vet`, `gofmt` 클린.
- 설정은 Phase 0 YAML `screen_control.{enabled,presets_file}`. 사용자 대면 서브커맨드 추가 금지(`__mcp-screen`은 내부 전용).
- 커밋 말미: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`
- 플랫폼/COM API는 구현 중 라이브러리 문서를 확인해 정확히 작성(이 플랜은 구조·인터페이스·검증을 고정; Win32/COM 구체 코드는 구현자가 라이브러리에 맞춰 작성).

## File Structure
| 파일 | build tag | 책임 |
|------|-----------|------|
| `mcpscreen.go` | windows | MCP 서버(mcp-go): 도구 등록·핸들러, `RunMCPScreen()` 진입 |
| `mcpscreen_stub.go` | !windows | `RunMCPScreen()` no-op(에러 로그 후 종료) |
| `screen_capture_windows.go` | windows | `captureScreen()`→PNG bytes |
| `screen_input_windows.go` | windows | click/move/type/key/scroll (SendInput) |
| `screen_uia_windows.go` | windows | go-ole UIA: snapshot/find/invoke/setValue |
| `screen_apps_windows.go` | windows | launchApp(name), listWindows, focusWindow |
| `screen_presets.go` | (all) | 프리셋 JSON CRUD |
| `screen_presets_test.go` | (all) | 프리셋 테스트 |
| `screenargs.go` | (all) | 워커용 screen MCP args/system-prompt 조립(순수 함수, 테스트 가능) |
| `screenargs_test.go` | (all) | args 조립 테스트 |
| `main.go` | (all) | `__mcp-screen` 인자 분기 |
| `runner.go`/`manager.go` | (all) | enabled면 워커에 screen args 주입 |
| `confighot.go` | (all) | OnScreenControl 훅 본문(로그/상태) |

---

## Task 1: 프리셋 저장소 + 워커 args 조립 (크로스플랫폼, 순수 로직 먼저)

**Files:** Create `screen_presets.go`, `screen_presets_test.go`, `screenargs.go`, `screenargs_test.go`

**Interfaces:**
- Produces: `type Preset struct{ Name string; X,Y int }`; `type PresetStore` with `Load(path)`, `Save()`, `Set(name,x,y)`, `Get(name)(Preset,bool)`, `List()[]Preset`.
- Produces: `func screenWorkerArgs(self string) []string` → MCP/allowedTools/system-prompt args for the worker; `func screenSystemPrompt() string`.

- [ ] **Step 1: 프리셋 테스트 작성** — `screen_presets_test.go`: Set→Get→List 라운드트립, Save/Load(JSON 파일, temp), 미존재 Get=false.
- [ ] **Step 2: 실패 확인** `go test ./... -run TestPreset -v` → FAIL(undefined)
- [ ] **Step 3: `screen_presets.go` 구현** — JSON 파일(map[name]→{x,y}) CRUD, mutex 보호. presets_file 빈 값이면 `~/.teleclaude/presets.json`.
- [ ] **Step 4: 통과 확인** `go test ./... -run TestPreset -v` → PASS
- [ ] **Step 5: args 테스트 작성** — `screenargs_test.go`: `screenWorkerArgs("C:\\t\\teleclaude.exe")`가 `--strict-mcp-config`, `--mcp-config`(self __mcp-screen 가리키는 json or inline), `--allowedTools mcp__screen__*` 또는 개별, `--append-system-prompt`(UIA 우선 지침 포함) 를 포함하는지. `screenSystemPrompt()`에 "snapshot" "UIA" "screenshot" "preset" 키워드 포함 확인.
- [ ] **Step 6: 실패 확인** → FAIL
- [ ] **Step 7: `screenargs.go` 구현** — inline mcp-config JSON 문자열 생성: `{"mcpServers":{"screen":{"command":"<self>","args":["__mcp-screen"]}}}`을 임시 파일로 쓰거나 `--mcp-config`에 인라인 JSON 전달(claude는 인라인 JSON도 허용). allowedTools: `mcp__screen__*`. system prompt: §2 지침 문구.
- [ ] **Step 8: 통과 + vet/build(win&linux)** `GOOS=windows go build ./... && GOOS=linux go build ./... && go test ./... | tail -3`
- [ ] **Step 9: 커밋** `feat(screen): preset store + worker MCP args/system-prompt (cross-platform)`

---

## Task 2: MCP 서버 스켈레톤 + `__mcp-screen` 진입 + list_windows

**Files:** Create `mcpscreen.go`(win), `mcpscreen_stub.go`(!win), `screen_apps_windows.go`(win, listWindows/focusWindow part); Modify `main.go`, `go.mod`

**Interfaces:**
- Produces: `func RunMCPScreen() error` (win: starts stdio MCP server; !win: logs "unsupported" and returns). Tools registered: start with `list_windows`, `focus_window`.
- main.go: `case "__mcp-screen": if err := RunMCPScreen(); err != nil { log.Fatal(err) }`

- [ ] **Step 1: deps 추가** `go get github.com/mark3labs/mcp-go` (and go-ole in Task 5).
- [ ] **Step 2: stub 먼저** `mcpscreen_stub.go`(!windows): `func RunMCPScreen() error { return fmt.Errorf("screen control is Windows-only") }`. main.go에 `__mcp-screen` 분기 추가. `GOOS=linux go build ./...` 통과 확인.
- [ ] **Step 3: windows MCP 서버** `mcpscreen.go`(windows): mcp-go로 stdio 서버 생성, `list_windows`(EnumWindows→제목/핸들), `focus_window(title)`(SetForegroundWindow) 도구 등록. `screen_apps_windows.go`에 EnumWindows/focus 구현.
- [ ] **Step 4: 빌드(win)** `GOOS=windows go build -o teleclaude.exe .` → 성공
- [ ] **Step 5: 스모크(이 dev머신, Windows)** inline config로 claude가 서버에 붙어 도구 목록에 `mcp__screen__list_windows`가 보이고 호출 시 창 목록 반환:
  `claude -p "list windows via the list_windows tool" --strict-mcp-config --mcp-config <{screen→teleclaude.exe __mcp-screen}> --dangerously-skip-permissions` → 창 목록 출력 확인.
- [ ] **Step 6: 커밋** `feat(screen): Go MCP server skeleton (__mcp-screen) + list/focus windows`

---

## Task 3: screenshot (캡처→PNG, MCP 이미지)

**Files:** Create `screen_capture_windows.go`; Modify `mcpscreen.go`

- [ ] **Step 1: 캡처 구현** BitBlt+GetDIBits로 가상화면 캡처→`image.RGBA`→PNG `[]byte`. DPI 인지(SetProcessDpiAwarenessContext) 적용해 좌표 일치.
- [ ] **Step 2: `screenshot` 도구 등록** mcp-go의 이미지 결과(base64 PNG, mimetype image/png)로 반환. 옵션 scale.
- [ ] **Step 3: 빌드(win)** 성공
- [ ] **Step 4: 스모크** `claude -p "use screenshot tool then describe the screen in one sentence" ...` → 화면 묘사 반환(이전 Windows-MCP 검증과 동일 수준) 확인.
- [ ] **Step 5: 커밋** `feat(screen): screenshot tool (BitBlt→PNG, vision fallback)`

---

## Task 4: 입력(click/move/type/key/scroll) + 프리셋 도구

**Files:** Create `screen_input_windows.go`; Modify `mcpscreen.go`

- [ ] **Step 1: SendInput 구현** mouse move/click(left/right/double)/drag, keyboard type(유니코드)/key combo, scroll.
- [ ] **Step 2: 도구 등록** `click(x,y,button)`,`move`,`type(text)`,`key(combo)`,`scroll(dx,dy)`,`preset_save/preset_click/preset_list`(screen_presets.go 사용).
- [ ] **Step 3: 빌드(win)**
- [ ] **Step 4: 스모크** 메모장 실행 후 `type`으로 텍스트 입력되는지 수동 확인(또는 claude로). preset_save→preset_click 동작.
- [ ] **Step 5: 커밋** `feat(screen): input tools (SendInput) + coordinate presets`

---

## Task 5: UIA — snapshot / invoke / set_value (go-ole)

**Files:** Create `screen_uia_windows.go`; Modify `mcpscreen.go`, `go.mod`

- [ ] **Step 1: dep** `go get github.com/go-ole/go-ole`
- [ ] **Step 2: UIA 초기화** COM init + IUIAutomation 인스턴스(CUIAutomation CLSID). foreground 창 ElementFromHandle.
- [ ] **Step 3: snapshot** TreeWalker/FindAll로 요소 순회 → `name | controlType | automationId | clickable | rect` 텍스트(또는 JSON) 반환. 너무 크면 상위 N/깊이 제한.
- [ ] **Step 4: invoke/set_value** name(또는 automationId)로 FindFirst → InvokePattern.Invoke()(클릭) / ValuePattern.SetValue(text).
- [ ] **Step 5: 도구 등록** `snapshot`,`invoke(name)`,`set_value(name,text)`.
- [ ] **Step 6: 빌드(win)**
- [ ] **Step 7: 스모크(네이티브 앱)** 계산기/메모장에서 `snapshot`이 버튼 이름들 반환, `invoke("5")`/`invoke("Close")` 동작 확인. (Electron앱은 비노출 → screenshot 폴백 경로 확인.)
- [ ] **Step 8: 커밋** `feat(screen): UIA snapshot/invoke/set_value via go-ole (primary path)`

---

## Task 6: config 연동 (screen_control → 워커 주입) + launch_app

**Files:** Modify `runner.go` or `manager.go`(워커 args), `confighot.go`(OnScreenControl), `screen_apps_windows.go`(launchApp); add launch_app tool

- [ ] **Step 1: launch_app 구현** name으로 시작메뉴(`%APPDATA%`/`%ProgramData%` Start Menu의 *.lnk 매칭) → 타겟 실행; 폴백 Program Files 검색/`exec.Command("cmd","/c","start","",name)`. 도구 등록.
- [ ] **Step 2: 워커 주입** 워커 실행 시 `cfg.ScreenControl`이면 `screenWorkerArgs(selfExePath)`(Task1)를 claude args에 추가. selfExePath = os.Executable(). (claudeRunner.Run/Route 중 Run에만 적용; Route는 라우팅이라 불필요.)
- [ ] **Step 3: OnScreenControl 훅** confighot.go main 와이어링의 훅에서 enable/disable 로그 + (옵션) 텔레그램 "🖥 화면제어 ON/OFF" 알림.
- [ ] **Step 4: 안전 알림** screen_control 활성 워커 첫 사용 시 경고 1회(선택).
- [ ] **Step 5: 빌드(win&linux)+test** 전체 green.
- [ ] **Step 6: 통합 스모크** config에 `screen_control: {enabled: true}` → 텔레그램(또는 claude 직접)으로 "메모장 실행해서 'hi' 입력하고 스크린샷 보여줘" → launch→type→screenshot 흐름 동작.
- [ ] **Step 7: 커밋** `feat(screen): wire screen_control config → worker injection + launch_app`

---

## Self-Review
- spec §2 도구 전부 태스크로 매핑(snapshot/invoke/set_value=T5, screenshot=T3, click/type/key/scroll/preset=T4, launch_app/list/focus=T2&T6).
- UIA 우선/비전 폴백 = system prompt(T1) + 도구 우선순위 + 폴백 경로(T5 스모크).
- config 구동 = Phase0 YAML + T6 주입 + OnScreenControl 훅.
- 크로스플랫폼 빌드 = stub(!windows) 유지(T2).
- 플랫폼/COM 구체코드는 구현자가 라이브러리에 맞춰 작성(플랜은 구조·검증 고정) — 각 태스크 빌드+스모크 게이트로 검증.

## 다음(M2)
패킷 캡처 + 기능별 패킷 상관 + 자율 메뉴 스윕 리포트.
