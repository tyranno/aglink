# aglink-screen

[agentlink](https://github.com/tyranno/aglink-screen) 계열의 첫 플러그인 —
LLM 에이전트가 Windows 화면(UIA/Win32/GDI)을 직접 조작하게 해주는 독립 실행파일.

원래 [teleclaude](https://github.com/tyranno/teleclaude)에 `__mcp-screen`이라는
숨은 서브커맨드로 내장돼 있던 화면제어 기능을 별도 프로젝트로 분리한 것.
teleclaude 본체("대화 감독" — 라우팅/스케줄러/텔레그램)는 이 실행파일을 자식
프로세스로 호출만 하고, 실제 화면 조작 로직은 여기 전부 들어있다.

- **UIA 우선** — `snapshot`/`invoke`/`set_value`/`get_value`/`get_text`/`set_state`로 대부분의 네이티브 앱을 좌표 없이 조작
  - `snapshot`: 문서 순서 + 포함 관계 들여쓰기, 상태 플래그(`[checked]`/`[unchecked]`/`[mixed]`, `[expanded]`/`[collapsed]`, `[selected]`, 슬라이더 `[value=N]`), 화면 사각형 `@x,y,w×h`. 트리 전체를 CacheRequest 한 번으로 읽어 예전보다 빠르다. `element_at`도 같은 정보
  - `set_state`: `checked`/`expanded`/`selected`/`value`(RangeValue)/`scroll_into_view`/`select_item`(콤보박스 펼침→항목 선택→접기). 현재 상태를 읽고 다를 때만 바꾸며 `before→after`를 보고(멱등)
  - 선택자: `invoke`/`set_value`/`get_value`/`get_text`/`wait_for_control`/`set_state`에 `control_type`(Button, Edit, CheckBox, ComboBox, ListItem, MenuItem, TreeItem, TabItem, Hyperlink, Text …) · `nth`(1부터) · `match`(`exact` 기본, `contains`/`prefix`는 대소문자 무시). 여러 개가 맞으면 첫 번째를 쓰고 `matched N; used 1st (use nth …)`라고 알려 주며, 하나도 없으면 가까운 이름 최대 5개를 제안
- **읽기 출력 상한(토큰 절약)** — 도구 결과는 같은 턴의 이후 API 왕복마다 재전송되므로, 읽기 도구는 기본 상한과 이어읽기(`offset`)를 둔다. 잘리면 응답 끝에 `…[잘림: 전체 N자 중 a–b 표시. 이어 읽으려면 offset=b …]` 형태로 다음 offset을 알려준다.
  - `get_text`: `offset`(음수면 끝에서부터 tail, 예: `-3000`) · `max_chars`(기본 8000, 최대 50000)
  - `snapshot`: `offset`(요소 인덱스) · `max_elements`(기본 150, 최대 1000; 구 이름 `max`도 허용) · `max_chars`(기본 10000, 최대 50000) · 요소별 내용 미리보기 120자
  - `win_controls`: `offset` · `max`(기본 200) · 라벨 80자
  - `get_value`: 2000자 (넘으면 `get_text` offset으로 이어 읽으라고 안내)
- **백그라운드 조작(포커스 안 뺏음)** — `snapshot`/`invoke`/`set_value`/`get_value`/`get_text`/`wait_for_control`에 `window`(창 제목 일부 또는 hwnd)를 주면 그 창을 앞으로 가져오지 않고 UIA로 조작한다. 사용자 입력 대기 없이 제어권(리스)만 잡고, 조작한 지점에 보라색 원(에이전트 커서)이 잠깐 뜬다(`AGLINK_SCREEN_AGENT_CURSOR=0`으로 끔). `click_control`은 기본이 백그라운드 — 버튼/체크박스/라디오는 `BM_CLICK`, 그 밖은 UIA invoke, 안 되면 예전처럼 포커스+실제 클릭(`background:false`로 강제). 결과에 쓴 경로가 나온다
- **Win32 자식창 폴백** — `win_controls`/`click_control`, UIA가 비어도 정확한 좌표 확보
- **번호 표시(Set-of-Mark)** — `show_numbers`/`click_number`, 음성 액세스의 "번호 표시"처럼 조작 가능한 요소에 번호를 붙이고 번호로 클릭 (아래 "번호 표시와 OCR")
- **내장 OCR** — `ocr`/`click_text`, UIA가 아무것도 안 주는 창(게임·원격 데스크톱·캔버스)의 글자를 Windows OCR로 읽고 글자로 클릭
- **GDI 캡처** — `screenshot`(`monitor`=1부터, 1=주 모니터)/`capture_window`/`capture_region`. 캡션에 `origin`·`size`·`scale`이 들어가 이미지 좌표를 `screen_x = origin_x + img_x/scale`로 되돌릴 수 있다
- **입력** — `click`/`double_click`/`triple_click`/`drag`(`hold_ms`·`steps`)/`move`/`type`/`key`(hold_ms 지원)/`scroll` (+ modifier 조합), `get_cursor_position`으로 현재 좌표 확인
- **가상 데스크톱 인식** — `focus_window`/`return_desktop`이 데스크톱 경계를 넘나듦
- **대기 프리미티브** — `wait_for_window`(창), `wait_for_control`(UIA 요소; `until`=`appear`(기본)/`disappear`/`enabled`/`value`(+`value` 정확히 또는 `contains`)), 수동 폴링할 필요 없음
- **클립보드** — `clipboard_get`(텍스트, `offset`/`max_chars`로 잘라 읽기) / `clipboard_set` (+ `key("ctrl+v")`로 긴·비ASCII 텍스트를 한 번에 붙여넣기)
- **창 배치** — `move_window`(정확한 좌표/크기), `window_state`(최소화/최대화/복원), `get_window_rect`(현재 위치/크기/상태 확인), `close_window`(특정 창을 정확히 지정해서 닫기 — foreground에 의존하는 `key("alt+f4")`보다 안전)
- **좌표 프리셋** — `preset_save`/`preset_click`/`preset_list`
- **관리자 권한 대상 앱** — UIPI 감지. 대상이 관리자 권한임이 확실하면 클릭/드래그/타이핑/키 입력은 보내기 전에 바로 실패(무음 무시 대신), 확실치 않으면 경고 (`screen_control.elevated`로 우회)
- **보안 경고 자동 통과** — `launch_app`(`elevated`/`unblock`) + `pass_security_prompts` (아래 "보안 경고 통과")

Windows 전용 (`GOOS=windows` 빌드 태그). 다른 OS에서는 스텁이 명확한 에러를 반환한다.

## 번호 표시와 OCR — `show_numbers` · `click_number` · `ocr` · `click_text`

창을 보고 조작하는 도구의 선호 순서(도구 설명에도 들어 있다):
`snapshot` + `invoke`/`set_value` → `win_controls`/`click_control` → `show_numbers` + `click_number`
→ `ocr` + `click_text` → `capture_window`/`screenshot`.

- `show_numbers(window?, seconds=10, capture=false, max=150)` — 창(기본: 포그라운드)의 보이는·활성
  조작 요소(버튼, 링크, 입력칸, 체크박스, 목록/트리/탭/메뉴 항목 …)를 UIA로 모아 읽는 순서대로
  1..N 번호를 매긴다. 각 요소 왼쪽 위에 보라색 번호 배지를 클릭 통과·최상위 오버레이로 띄우고
  (`seconds` 뒤 자동으로 사라짐, `0` = 화면 배지 없이 목록만·기존 배지 숨김), 응답은
  `N. [type] name — center(x,y)`. `max`(최대 300)를 넘으면 잘렸다고 알린다. `capture=true` 면
  같은 번호를 그려 넣은 창 캡처도 함께 준다(capture_window 처럼 1568px 상한). 입력·포커스 변경 없음.
- `click_number(n, window?, double?, right?, background=true)` — 최근 `show_numbers` 결과(창별로
  메모리에 보관; `window` 를 안 주면 마지막 호출)의 n번을 누른다. 단일 클릭은 그 요소의 UIA invoke 로
  백그라운드에서, 안 되거나 `double`/`right` 면 창을 앞으로 가져와 요소 중앙을 실제 마우스로.
  배지를 숨긴다. 창이 닫히거나 움직였거나 크기가 바뀌었을 때, 요소가 사라졌거나 움직였을 때
  (스크롤 등), 5분이 지났을 때는 누르지 않고 `show_numbers` 를 다시 부르라고 한다.
- `ocr(window? | x,y,width,height, lang?)` — 내장 `Windows.Media.Ocr` 엔진. 영역을 캡처해(작은 글자
  인식을 위해 2배 확대) 임시 PNG 로 쓰고, 실행파일에 내장된 PowerShell 5.1 스크립트로 인식한 뒤
  임시 파일은 지운다(20초 제한). 결과는 읽는 순서의 `text — center(x,y)` (화면 좌표).
  `lang` 기본은 자동 — 한국어 OCR 이 있으면 한국어, 없으면 사용자 프로필 언어. 원하는 언어가 없으면
  설치 방법(설정 > 시간 및 언어 > 언어 > 해당 언어 옵션의 OCR, 또는 관리자 PowerShell
  `Add-WindowsCapability -Online -Name "Language.OCR~~~ko-KR~0.0.1.0"`)을 담은 오류를 준다.
  화면 픽셀을 읽으므로 대상 창이 가려져 있으면 안 된다.
- `click_text(text, window?, nth=0, double?, x,y,width,height?, lang?)` — OCR 후 대소문자·공백을
  무시한 부분일치로 찾아 그 단어들의 중앙을 실제 마우스로 클릭한다. 단어 경계에 딱 맞는 일치가
  먼저, 같은 글자가 여럿이면 `nth`(0부터, 읽는 순서). `window` 를 주면 먼저 앞으로 가져온다.

수동 확인(실제 메모장을 띄웠다 닫는다):
`AGLINK_MANUAL_UI=1 go test -run TestManualNumbersAndOCR -v`

## 연속 동작 — `run_sequence`

좌표·컨트롤이 정해진 여러 동작을 한 호출로 실행한다(단계 사이 모델 왕복이 사라진다).
첫 실패에서 멈춘다.

```
run_sequence  step_delay_ms=150  steps=
  [{"action":"click_control","window":"설정","text":"네트워크"},
   {"action":"wait_for_control","name":"Wi-Fi","timeout_ms":3000},
   {"action":"click","x":420,"y":310,"wait_ms":400},
   {"action":"type","text":"hello"},
   {"action":"wait","ms":500}]
```

- 어느 단계든 `wait_ms` 로 그 단계 뒤에 쉰다(화면 전환·애니메이션). `wait{ms}` 는 그냥 기다린다.
  기다릴 대상이 있으면 `wait_for_control`/`wait_for_window` 가 정확하다.
- 단계 사이 기본 대기: `step_delay_ms`, 없으면 `AGLINK_SCREEN_STEP_DELAY_MS`
  (설치 프로그램 `/STEPDELAY=` 가 MCP 등록에 넣는다). 한 번의 대기는 60초로 제한.
- Electron/Wails 창과 웹 페이지는 aglink-web 의 `run_steps` 가 좌표 대신 선택자로 한다.
- `invoke`/`set_value`/`set_state`/`wait_for_control` 단계에 `window` 를 주면 백그라운드로, `click_control` 은
  `background:false` 로 예전 경로. `note{text}` 는 아무것도 안 하는 표시용 단계.
- `set_state`·`clipboard_get`·`clipboard_set` 단계도 있고, UIA 단계는 `control_type`/`nth`(1부터)/`match` 를
  받는다(`click_control` 의 `nth` 는 예전대로 0부터). `drag` 는 `hold_ms`/`steps`, `wait_for_control` 은
  `until`/`value`/`contains`.

## 녹화와 플로우 — `record_workflow` · `flow_*`

- `record_workflow(max_seconds=120, stop_key="f9", save_as?)` — 사용자가 직접 하는 조작을 녹화해
  `run_sequence` steps(JSON)와 요약을 돌려준다. 녹화 중에는 빨간 알림이 뜨고, 정지 키(기본 F9 —
  키 자체는 앱에도 전달된다)나 시간 제한으로 끝난다. 클릭은 가능하면 이름 기반
  (`click_control`/`invoke{window,name}`), 아니면 좌표 `click`; 연속 타이핑은 `type` 하나로,
  단축키·특수키는 `key`, 창 전환은 `wait_for_window`. **비밀번호 칸 입력은 기록하지 않고**
  `note` 단계로 표시한다. IME(한글) 조합 입력도 잡히지 않아 `note` 로 남는다 — 재생 전에 고칠 것.
- `run_sequence save_as=이름` — 모든 단계가 성공하면 그 steps 를 저장. `flow_save(name, steps)`,
  `flow_list`, `flow_run(name)`. 데이터 디렉터리의 `screen-flows.json`(presets.json 옆, 원자적 쓰기).
  이름은 문자·숫자·`- _ .` 64자까지. 승인 게이트는 `flow_run` 이 재생하는 단계까지 본다.

## 보안 경고 통과

무인(사람이 컴퓨터 앞에 없는) 자동 제어에서, 윈도우가 끼워 넣는 "실행하시겠습니까?"
류의 창에 막히지 않게 한다.

**`launch_app`** 옵션:
- `elevated=true` — 관리자 권한으로 실행. **aglink-screen 자신이 관리자 권한으로 떠 있으면
  자식이 그대로 물려받아 UAC 가 뜨지 않는다.** 일반 권한이면 `runas`(UAC 프롬프트)로 떨어진다.
  실행한 관리자 앱을 이어서 클릭하려면 aglink-screen 도 관리자여야 한다(UIPI).
- `unblock=true` — 실행 전에 파일의 "인터넷에서 받음" 표시(NTFS `Zone.Identifier`)를 지운다.
  PowerShell `Unblock-File` 과 같고, 이 파일을 믿는다는 선언이다. 대상이 구체 경로로
  풀릴 때 적용. 기본 꺼짐.

**`pass_security_prompts`** — 지금 화면에 떠 있는 보안·확인 창을 찾아 긍정 버튼을 누른다.
**아는 창에만** 손댄다:
- "파일 열기 - 보안 경고"(다운로드한 exe 실행 확인) → `실행`
- SmartScreen "Windows의 PC 보호" → `추가 정보` → `실행`(UI Automation 경유). *실제 창으로는 미검증.*
- 사용자가 `~/.aglink/aglink-screen-prompts` 에 등록한 앱 창. 한 줄에 `제목 | 버튼[ + 버튼…]`,
  `#` 주석:
  ```
  # 사내 인증 모듈이 띄우는 허용 창
  INISAFE | 허용 + 예
  ```
- **UAC 창은 누를 수 없다** — 윈도우가 보안 데스크톱에 띄워 합성 입력이 닿지 않는다.
  누르는 대신 "사람이 승인해야 함"으로 보고한다. 즉 UAC 를 아예 안 뜨게 하려면
  aglink-screen 데몬을 관리자 권한으로 띄우는 수밖에 없다(위 `elevated` 설명).
- `accept` 로 긍정 버튼 라벨을 더 보탤 수 있고, `max`(기본 5)로 연속 처리 개수를 정한다.
- 같은 창을 두 번 누르지 않는다(처리한 hwnd 는 건너뜀). `launch_app` 직후에 쓰면 좋다.

> **보안상 유의** — 데몬을 관리자 권한으로 상시 띄우면, `127.0.0.1:48220` 에 붙는 다른
> 프로그램이나 (SSH 역터널로 연결된) 원격 계정도 이 PC 에서 관리자 권한으로 프로그램을
> 실행할 수 있게 된다. 사실상 이 계정의 UAC 를 끄는 것과 비슷한 범위다. 무인 제어의
> 편의와 맞바꾸는 위험이므로, 필요한 PC 에서만 켠다.

## 실행 모드

```
aglink-screen              # 기본값. MCP stdio 서버로 기동 (아래 "mcp"와 동일)
aglink-screen mcp          # 명시적으로 같음
aglink-screen serve [--addr host:port]
                            # 같은 도구를 MCP streamable HTTP(/mcp)로 노출.
                            # 기본 127.0.0.1:48220. 아래 "원격에서 쓰기" 참고
aglink-screen cmd <sub> [args...] [--presets <path>]
                            # LLM 우회 fast-path. 결과를 JSON으로 stdout에 출력:
                            #   {"text": "...", "image": "<base64 PNG, 있으면>", "error": "..."}
```

`cmd`의 서브커맨드: `list` (창 목록) · `shot [창이름]` (스크린샷) ·
`region <x> <y> <w> <h> [창이름]` (영역 캡처) · `preset save <이름>` ·
`click <프리셋이름>`.

## teleclaude와 연결

teleclaude는 `screen_control.binary_path`(config.yaml)로 이 실행파일 경로를
찾는다. 값이 비어 있으면 teleclaude 실행파일과 **같은 폴더**에서
`aglink-screen(.exe)`를 찾는다 — 배포 시 두 실행파일을 나란히 두면 별도 설정
없이 동작한다.

```yaml
screen_control:
  enabled: true
  binary_path: ""   # 비우면 teleclaude exe와 같은 폴더에서 자동 탐색
  elevated: false
  keep_awake: false
```

teleclaude 쪽에서는 워커의 `--mcp-config`가 `aglink-screen mcp`를 가리키게
하고, `!screen` 텔레그램 명령은 `aglink-screen cmd ...`를 서브프로세스로
실행해 JSON 결과를 파싱한다.

### 제어권(Control Ownership)

대화(worker)마다 별도 aglink-screen 프로세스가 뜨고 **같은 물리 화면 하나**를
조작하므로, 두 대화가 동시에 입력을 합성하면 충돌한다. 이를 막기 위한
크로스-프로세스 제어권 조율(리스 + 세션-로컬 뮤텍스, fail-fast `SCREEN_BUSY`
응답, `control_status` 사전 확인 툴)의 **설계와 MCP 계약**은
[docs/control-ownership.md](docs/control-ownership.md)에 정리돼 있다 —
teleclaude 호출측 개발은 이 문서를 참조.

### 통합 배포 (`!update`)

teleclaude와 이 저장소를 **형제 디렉터리**(예: `..\teleclaude`, `..\aglink-screen`)로
나란히 clone해두면, teleclaude의 텔레그램 `!update` 명령이 teleclaude 자체를
빌드하기 전에 이 저장소도 함께 `go build`해서 teleclaude 실행파일 옆에
떨어뜨려준다 — 저장소 두 개를 각각 손으로 빌드/복사할 필요 없이 `!update`
한 번으로 둘 다 최신화된다. 형제 디렉터리가 없으면(화면제어가 필요 없는
헤드리스 배포 등) 조용히 건너뛴다 — 자세한 내용은
[teleclaude README의 "플러그인 확장" 절](https://github.com/tyranno/teleclaude#플러그인-확장-aglink-)
참고.

## 원격에서 쓰기 (SSH 역터널 + `/mcp`)

VS Code를 SSH 원격으로 열어 리눅스 머신에서 작업할 때, 그쪽 Claude 세션이 이
윈도우 호스트의 화면을 조작하게 하는 경로다. 화면 제어는 이 윈도우에서 돌아야
하므로 **원격에는 설치할 것이 없다** — MCP 등록 한 줄이면 된다.

**1. 윈도우에서 서버를 띄운다**

```powershell
aglink-screen serve                     # 127.0.0.1:48220 (loopback 전용)
```

**2. SSH 역터널로 그 포트를 원격에 넘긴다.** `~/.ssh/config`:

```
Host 192.168.123.146-doowon
  HostName 192.168.123.146
  User doowon
  RemoteForward 48220 127.0.0.1:48220
```

> **`ExitOnForwardFailure yes`를 넣지 말 것.** 같은 호스트에 VS Code 창을 여러 개
> 열면 두 번째 연결이 포트를 못 잡는데, 이 옵션이 있으면 그 연결 자체가 죽는다.
> 포워딩 실패는 무시하고 연결만 살려두는 편이 맞다 — 이미 선 터널을 다 같이 쓴다.

**3. 원격에서 MCP로 등록한다.**

```sh
claude mcp add --transport http --scope user \
  aglink-screen-remote http://127.0.0.1:48220/mcp
```

이름을 `aglink-screen-remote`로 두면 도구가 `mcp__aglink-screen-remote__*`로 떠서,
로컬에서 stdio로 띄운 `aglink-screen`과 이름으로 갈린다. aglink-web 도 같은 규칙이다
(`aglink-web-remote`) — [`docs/mcp-names.md`](../docs/mcp-names.md).

### 항시 켜 두기

역터널을 VS Code 연결이 물고 있으면 그 창을 닫는 순간 포트가 죽는다. 상시로
쓰려면 [`scripts/aglink-always-on.ps1`](../scripts/aglink-always-on.ps1) 을
로그온 작업으로 걸어 둔다 — aglink-web 과 포트별 터널을 확인해 **없는 것만** 띄운다.
aglink-screen 은 이 일반 권한 작업이 아니라 아래 별도 관리자 작업이 띄운다(터널은
그대로 48220 을 넘겨 준다 — 터널은 포트만 보고 어느 프로세스·권한인지 상관없다).

```powershell
# 확인만 (아무것도 바꾸지 않음)
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\aglink-always-on.ps1 `
  -SshHost user@host -CheckOnly

# 작업 스케줄러: 로그온 시 + 5분마다. 반드시 "로그온한 사용자로만 실행"으로.
# 서비스(session 0)로 돌리면 데스크톱이 없어 aglink-screen 이 못 쓴다.
#
# 작업의 실행 대상은 powershell.exe 가 아니라 run-hidden.vbs 로 둔다:
wscript.exe //B //Nologo scripts\run-hidden.vbs `
  "powershell.exe -NoProfile -ExecutionPolicy Bypass -File ""...\aglink-always-on.ps1"" -SshHost user@host"
```

`-WindowStyle Hidden` 만으로는 **5분마다 콘솔 창이 번쩍인다** — 작업 스케줄러가
창을 만든 뒤 숨기는 순서라 그 사이가 보인다. `run-hidden.vbs` 는 창을 애초에
만들지 않는다.

그 시임은 띄우자마자 끝나므로 작업 스케줄러는 실행이 끝난 줄 알고, 그 결과
`MultipleInstances=IgnoreNew` 가 무력해진다. 그래서 스크립트 자신이 뮤텍스로
중복 실행을 막는다(겹치면 두 번째가 조용히 물러난다).

### aglink 호스트(텔레그램 봇)는 별도 작업으로

호스트는 `screen_control.elevated` 가 켜져 있으면 **스스로 UAC 승격**을 한다.
승격되지 않은 작업이 그걸 띄우면 되살릴 때마다 UAC 창이 뜨고, 자리에 사람이
없으면 그 창 앞에서 멈춰 버린다 — 자동복구가 필요한 바로 그 상황에서 못 산다.
그래서 호스트는 `RunLevel Highest` 작업으로 따로 건다(관리자 PowerShell 1회):

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\install-host-task.ps1
```

호스트는 health 포트가 없어 "프로세스가 있는가"가 유일한 판정이다. 텔레그램
long-poll 은 **실패만 로그에 남기므로**, 2026-09-07 에 죽었을 때 11일 동안
아무도 몰랐다 — 브라우저·화면 서버는 멀쩡해서 겉보기에 이상이 없었다.

건강하면 아무것도 하지 않고 로그도 남기지 않는다. 이미 도는 `serve` 프로세스는
**절대 재시작하지 않는다** — 다른 세션이 붙어 쓰는 데몬이라, 응답이 잠깐 늦었다고
두 번째를 띄우면 포트를 두고 서로 싸운다. 그런 경우엔 `STALLED` 로 적고 사람에게
넘긴다. 로그는 `~/.aglink/always-on.log`.

### aglink-screen 을 관리자 권한으로 (무인 제어)

`launch_app` 이 실행하는 프로그램이 UAC 없이 뜨게 하려면 **데몬 자체가 관리자**여야
한다(자식이 권한을 물려받는다). UAC 창은 보안 데스크톱에 떠서 무엇으로도 누를 수
없으니, "눌러서 통과"가 아니라 "애초에 안 뜨게" 하는 유일한 길이다. 호스트와 같은
방식으로 `RunLevel Highest` 작업을 따로 건다(관리자 PowerShell 1회):

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\install-screen-task.ps1
```

이러면 `aglink-always-on.ps1` 의 일반 권한 작업은 aglink-screen 을 더 이상 띄우지
않고, 이 관리자 작업이 `-Screen_` 로 띄운다. 등록 직후에는 로그오프/로그온하거나
관리자 권한으로 `aglink-screen serve` 를 한 번 띄워 바로 적용한다.

> **보안 유의** — 관리자 데몬이 `127.0.0.1:48220` 에 떠 있으면, 그 포트에 닿는 다른
> 프로그램이나 (SSH 역터널로 연결된) 원격 계정도 이 PC 에서 프로그램을 관리자로
> 실행할 수 있다. 이 계정 UAC 를 끄는 것에 가까운 범위다. 감수할 PC 에서만 건다.
> 되돌리려면 이 작업만 지우면 다시 일반 권한으로 돈다.

### 알아둘 것 셋

- **제어 잠금이 거칠어진다.** `screen_lease_windows.go`는 "대화마다 프로세스 하나"를
  전제로 **PID**로 주인을 가리는데, 원격 세션은 전부 이 서버 프로세스 하나를
  공유한다. 그래서 *로컬 aglink-screen ↔ 원격 서버*는 여전히 서로 막지만,
  **원격 세션끼리는 서로 막지 못한다.** 한 사람이 쓰는 데스크톱을 전제로 한
  선택이다 — 둘을 갈라야 할 일이 생기면 lease에 세션 id를 넣어야 한다.
- **가상 데스크톱 복귀가 유휴 타이머로 바뀐다.** stdio는 파이프가 닫히는 것(=턴
  종료)을 신호로 원래 데스크톱으로 되돌리는데, 상주 서버에는 그 경계가 없다.
  대신 마지막 요청 이후 조용하면 되돌린다(기본 60초,
  `AGLINK_SCREEN_REMOTE_IDLE_MS`).
- **이 포트는 키보드·마우스·화면 전체를 준다.** 브라우저만 다루는 aglink-web보다
  위험이 크다. `AGLINK_SCREEN_REMOTE_TOKEN`을 설정하면
  `Authorization: Bearer <토큰>`을 요구하고, 없으면 기동할 때 경고만 찍고 연다
  (loopback 전용이라 여기 닿는다는 건 이미 이 머신 또는 그 터널에 접근권이
  있다는 뜻이다). 토큰이 있어도 loopback 요청은 기본적으로 면제된다.
  `serve --require-token` 이면 loopback 도 토큰을 내야 한다 — aglink-vscode 의
  relay 가 Remote-SSH 창 때문에 띄우는 서버는 이렇게 뜬다(토큰은 VS Code 의
  `AGLINK_SCREEN_REMOTE_TOKEN`, 없으면 `~/.aglink/aglink-screen-relay.token`). relay 는 띄우기 전에
  `aglink-screen features` 에 `require-token` 이 있는지 보고, 없는 구버전은 이 플래그를
  무시한 채 토큰 없이 열리므로 아예 띄우지 않는다(업데이트하라는 오류).

## 빌드

```powershell
go build -o aglink-screen.exe .
```

teleclaude와 같은 폴더에 두면(예: `..\Teleclaude\aglink-screen.exe`) 별도
설정 없이 바로 인식된다.

## 관리자 권한 대상 앱

대상 앱이 관리자(High integrity)로 떠 있으면 Windows UIPI가 일반 권한 프로세스의
합성 입력(클릭 등)을 무음 차단한다. 대상 프로세스가 관리자 권한임을 확인할 수 있으면
`click`/`double_click`/`triple_click`/`drag`/`type`/`key` 는 입력을 보내지 않고 바로 오류를 낸다
(미디어 키·Win 조합 제외; 확인할 수 없으면 예전처럼 경고만). `click_control`/`invoke` 결과에 UIPI 경고가
붙으면, teleclaude 쪽 `screen_control.elevated: true`로 전체 프로세스 체인
(teleclaude → claude worker → aglink-screen)을 관리자 권한으로 재기동해야 한다.
