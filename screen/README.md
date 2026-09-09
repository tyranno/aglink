# aglink-screen

[agentlink](https://github.com/tyranno/aglink-screen) 계열의 첫 플러그인 —
LLM 에이전트가 Windows 화면(UIA/Win32/GDI)을 직접 조작하게 해주는 독립 실행파일.

원래 [teleclaude](https://github.com/tyranno/teleclaude)에 `__mcp-screen`이라는
숨은 서브커맨드로 내장돼 있던 화면제어 기능을 별도 프로젝트로 분리한 것.
teleclaude 본체("대화 감독" — 라우팅/스케줄러/텔레그램)는 이 실행파일을 자식
프로세스로 호출만 하고, 실제 화면 조작 로직은 여기 전부 들어있다.

- **UIA 우선** — `snapshot`/`invoke`/`set_value`/`get_value`로 대부분의 네이티브 앱을 좌표 없이 조작
- **Win32 자식창 폴백** — `win_controls`/`click_control`, UIA가 비어도 정확한 좌표 확보
- **GDI 캡처** — `screenshot`/`capture_window`/`capture_region`, 비전 다운스케일을 피해 정확한 좌표 매핑
- **입력** — `click`/`double_click`/`triple_click`/`drag`/`move`/`type`/`key`(hold_ms 지원)/`scroll` (+ modifier 조합), `get_cursor_position`으로 현재 좌표 확인
- **가상 데스크톱 인식** — `focus_window`/`return_desktop`이 데스크톱 경계를 넘나듦
- **대기 프리미티브** — `wait_for_window`(창), `wait_for_control`(UIA 요소), 뜰 때까지 수동 폴링할 필요 없음
- **창 배치** — `move_window`(정확한 좌표/크기), `window_state`(최소화/최대화/복원), `get_window_rect`(현재 위치/크기/상태 확인), `close_window`(특정 창을 정확히 지정해서 닫기 — foreground에 의존하는 `key("alt+f4")`보다 안전)
- **좌표 프리셋** — `preset_save`/`preset_click`/`preset_list`
- **관리자 권한 대상 앱** — UIPI 감지 + 경고 (`screen_control.elevated`로 우회)

Windows 전용 (`GOOS=windows` 빌드 태그). 다른 OS에서는 스텁이 명확한 에러를 반환한다.

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
로컬에서 stdio로 띄운 `aglink-screen`과 이름으로 갈린다.

### 항시 켜 두기

역터널을 VS Code 연결이 물고 있으면 그 창을 닫는 순간 포트가 죽는다. 상시로
쓰려면 [`scripts/aglink-always-on.ps1`](../scripts/aglink-always-on.ps1) 을
로그온 작업으로 걸어 둔다 — 데몬 둘과 포트별 터널을 확인해 **없는 것만** 띄운다.

```powershell
# 확인만 (아무것도 바꾸지 않음)
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\aglink-always-on.ps1 `
  -SshHost user@host -CheckOnly

# 작업 스케줄러: 로그온 시 + 5분마다. 반드시 "로그온한 사용자로만 실행"으로.
# 서비스(session 0)로 돌리면 데스크톱이 없어 aglink-screen 이 못 쓴다.
```

건강하면 아무것도 하지 않고 로그도 남기지 않는다. 이미 도는 `serve` 프로세스는
**절대 재시작하지 않는다** — 다른 세션이 붙어 쓰는 데몬이라, 응답이 잠깐 늦었다고
두 번째를 띄우면 포트를 두고 서로 싸운다. 그런 경우엔 `STALLED` 로 적고 사람에게
넘긴다. 로그는 `~/.aglink/always-on.log`.

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
  있다는 뜻이다).

## 빌드

```powershell
go build -o aglink-screen.exe .
```

teleclaude와 같은 폴더에 두면(예: `..\Teleclaude\aglink-screen.exe`) 별도
설정 없이 바로 인식된다.

## 관리자 권한 대상 앱

대상 앱이 관리자(High integrity)로 떠 있으면 Windows UIPI가 일반 권한 프로세스의
합성 입력(클릭 등)을 무음 차단한다. `click_control`/`invoke` 결과에 UIPI 경고가
붙으면, teleclaude 쪽 `screen_control.elevated: true`로 전체 프로세스 체인
(teleclaude → claude worker → aglink-screen)을 관리자 권한으로 재기동해야 한다.
