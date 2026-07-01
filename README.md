# aglink-screen

[agentlink](https://github.com/tyranno/aglink-screen) 계열의 첫 플러그인 —
LLM 에이전트가 Windows 화면(UIA/Win32/GDI)을 직접 조작하게 해주는 독립 실행파일.

원래 [teleclaude](https://github.com/tyranno/teleclaude)에 `__mcp-screen`이라는
숨은 서브커맨드로 내장돼 있던 화면제어 기능을 별도 프로젝트로 분리한 것.
teleclaude 본체("대화 감독" — 라우팅/스케줄러/텔레그램)는 이 실행파일을 자식
프로세스로 호출만 하고, 실제 화면 조작 로직은 여기 전부 들어있다.

- **UIA 우선** — `snapshot`/`invoke`/`set_value`로 대부분의 네이티브 앱을 좌표 없이 조작
- **Win32 자식창 폴백** — `win_controls`/`click_control`, UIA가 비어도 정확한 좌표 확보
- **GDI 캡처** — `screenshot`/`capture_window`/`capture_region`, 비전 다운스케일을 피해 정확한 좌표 매핑
- **입력** — `click`/`double_click`/`drag`/`move`/`type`/`key`/`scroll` (+ modifier 조합)
- **가상 데스크톱 인식** — `focus_window`/`return_desktop`이 데스크톱 경계를 넘나듦
- **좌표 프리셋** — `preset_save`/`preset_click`/`preset_list`
- **관리자 권한 대상 앱** — UIPI 감지 + 경고 (`screen_control.elevated`로 우회)

Windows 전용 (`GOOS=windows` 빌드 태그). 다른 OS에서는 스텁이 명확한 에러를 반환한다.

## 실행 모드

```
aglink-screen              # 기본값. MCP stdio 서버로 기동 (아래 "mcp"와 동일)
aglink-screen mcp          # 명시적으로 같음
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
