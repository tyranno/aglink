# aglink-vscode — aglink-web 이 VS Code 창을 다루게 하는 확장

**날짜:** 2026-09-22
**대상:** 새 `aglink/vscode/` (VS Code 확장) + `aglink/web` (데몬)

## 문제

사용자는 VS Code 를 여러 개 띄워 두고 일한다. 대개 Remote-SSH 로 원격 리눅스에
붙은 창들이다. 한 창의 Claude 세션에서 **다른 창**이 지금 뭘 하는지 보거나 뭔가를
시키려면 지금은 aglink-screen 으로 그 창을 캡처하는 수밖에 없다. 무겁고, 토큰이
많이 들고, 화면에 안 보이는 건 못 본다.

CDP(디버그 포트)로 창을 텍스트로 읽는 길은 이미 만들었다(2026-09-22 앱 대상).
그러나 VS Code 에는 맞지 않는 점이 셋 있다.

- 포트는 VS Code 가 **시작할 때만** 열린다. `argv.json` 에 넣고 전체를 재시작해야
  하고, 그 순간 모든 창의 작업과 세션이 끊긴다.
- 편집기는 화면에 보이는 줄만 DOM 에 있고, 터미널은 캔버스에 그려져 글자가 DOM 에
  없다. 정작 보고 싶은 두 가지가 안 읽힌다.
- 선택자는 VS Code 업데이트마다 깨질 수 있다.

## 목표

- 이 세션에서 **다른 VS Code 창**의 작업 상태를 구조화된 형태로 읽는다: 열린
  파일, 파일 전체 내용, 문제 목록, 터미널 출력.
- 그 창에서 **일을 시킨다**: 파일 열기, 터미널 명령 실행 후 출력 받기, VS Code
  명령 실행.
- Remote-SSH 창이 1급 대상이다.
- VS Code 재시작 없이 설치만으로 동작한다.

## 비목표

- **다른 확장의 화면(웹뷰)**, 특히 Claude 채팅 패널. VS Code 는 확장끼리 서로의
  웹뷰를 들여다보지 못하게 막는다. Claude 대화는 `!attach`(세션 기록 파일 +
  `SendMessage`)가 이미 더 정확하게 한다.
- 편집(파일 내용 바꾸기). 이 1단계는 읽기와 "명령 실행"까지. 편집은 에이전트가
  원격 파일을 직접 고치는 게 보통이라 필요가 확인되면 붙인다.
- VS Code 마켓플레이스 배포. 로컬 `.vsix` 설치만.

## 실측한 것

이 PC 의 VS Code(1.135) 에서 확인:

- 확장 호스트의 Node 는 **24.18**, Electron 42. **`WebSocket` 이 기본 내장**이다.
  → 확장은 외부 npm 의존성 없이 순수 JS 로 만든다. 빌드 단계도 없다.
- `@vscode/vsce` 는 설치돼 있지 않다. `.vsix` 는 정해진 모양의 zip 이므로 작은
  PowerShell 스크립트로 직접 만든다(`.NET ZipArchive`).
- 이 세션이 VS Code 확장 호스트 안에서 돌아 `ELECTRON_RUN_AS_NODE=1` 을 물려받는다.
  여기서 `code` 를 부르면 그 변수를 지워야 한다(설치 스크립트가 처리).

## 구조

```
이 세션 ─MCP─→ aglink-web 데몬 ─WS /vscode─→ [aglink-vscode @ backend 창]
                                         └─→ [aglink-vscode @ scam-sv 창]
                                         └─→ [aglink-vscode @ aglink 창]  (이 창 자신)
```

크롬 확장과 같은 모양이다: 확장이 **데몬으로 접속**하고, 데몬이 요청을 보내고,
확장이 답한다. 프로토콜(`Request`/`Reply`)도 크롬 확장과 같다.

### 확장은 "UI 쪽"에서 돈다

`package.json` 의 `"extensionKind": ["ui"]`. Remote-SSH 창이라도 확장은 **이
윈도우 PC** 에서 돌아, 데몬(`127.0.0.1:48219`)에 터널 없이 붙는다. 원격 파일·터미널은
VS Code 가 원격으로 넘겨 준다.

### 창 하나 = 연결 하나 = 프로필 하나

VS Code 는 창마다 확장 호스트가 따로 뜨므로 확장도 창마다 하나씩 떠서 따로
접속한다. 접속할 때 창의 정체를 알린다:

| 항목 | 값 |
|---|---|
| `name` | 작업공간 이름 (`vscode.workspace.name`, 없으면 `(no folder)`) |
| `remote` | 원격 호스트 (예: `192.168.123.146-doowon`), 로컬 창이면 빈 값 |
| `folder` | 첫 작업 폴더 경로 |
| `session` | `vscode.env.sessionId` — 같은 폴더를 두 번 연 창을 구분 |

데몬은 이를 **`vscode:<이름>`** 프로필로 등록한다(원격이면 `vscode:<이름>@<호스트>`).
크롬 프로필 등록부와는 **따로** 둔다 — 섞으면 VS Code 창이 "가장 오래 연결된
크롬 프로필"로 뽑혀 기본 대상이 될 수 있다.

`list_profiles` 에 크롬·앱 다음 줄로 나온다:

```
vscode:backend@192-168-123-146-doowon | SSH 192.168.123.146-doowon | /home/doowon/project/llie/backend | connected 5m ago
```

이름은 앞부분만 대도 된다(`vscode:backend`). 겹치면 되묻는다 — 크롬 프로필·앱과
같은 규칙.

### 도구

모두 `profile: "vscode:…"` 가 필요하다. 이 세션 자신의 창도 연결돼 있으므로
"하나뿐이면 그걸 쓴다"는 기본값은 두지 않는다 — 엉뚱한 창을 건드리는 게 바로 막고
싶은 실패다.

| 도구 | 하는 일 |
|---|---|
| `vscode_workspace` | 작업공간 요약: 폴더, 원격, 활성 파일·커서, 열린 파일 수, 터미널 수 |
| `vscode_editors` | 열린 탭 목록(경로, 저장 안 됨 표시, 활성) |
| `vscode_read` | 파일 내용. `path` 생략 시 활성 파일. **저장 안 된 편집 포함**. `startLine`/`endLine` |
| `vscode_open` | 파일 열기, 선택적으로 `line` 으로 이동 |
| `vscode_problems` | 오류·경고 목록(파일 지정 가능) |
| `vscode_terminals` | 터미널 목록(이름, 셸 통합 여부, 마지막 명령) |
| `vscode_terminal_run` | 터미널에서 명령 실행 → **종료까지 기다려 출력과 종료 코드** |
| `vscode_terminal_read` | 터미널에서 최근 실행된 명령들과 그 출력 |
| `vscode_command` | VS Code 명령을 이름(`workbench.action.files.save` 등)과 인자로 실행 |

크롬 DOM 도구에 `vscode:` 프로필을, `vscode_*` 도구에 크롬·앱 프로필을 주면 분명한
오류로 거절한다.

### 터미널 — 셸 통합 API

VS Code 의 셸 통합(`window.onDidStartTerminalShellExecution`,
`terminal.shellIntegration.executeCommand`, `execution.read()`)은 1.93 부터 안정
API 다.

- `vscode_terminal_run`: 지정한 터미널(없으면 새로 만든 `aglink` 터미널)에서
  `executeCommand` → `read()` 스트림을 끝까지 모으고, 종료 이벤트의 `exitCode` 와
  함께 돌려준다. 제한 시간(기본 60초, 최대 10분)을 넘기면 그때까지의 출력과
  "아직 실행 중"을 돌려준다.
- `vscode_terminal_read`: 확장이 켜진 뒤 **모든** 터미널에서 실행된 명령(사람이 친
  것 포함)의 출력을 터미널별로 최근 20개·각 64KB 까지 기억해 두고 보여 준다.
- 셸 통합이 안 켜진 터미널(`shellIntegration` 이 없음)은 `sendText` 로 실행만 하고
  "출력은 읽을 수 없음"을 분명히 알린다.
- 출력의 ANSI 제어 문자는 걷어 낸다.

### 안전

- 데몬의 `/vscode` 는 루프백에서만 받는다. 이 PC 의 다른 프로그램이 VS Code 인 척
  접속하는 것은 막지 못한다 — `/call` 이 이미 로컬 누구에게나 열려 있는 것과 같은
  수준이다.
- `vscode_terminal_run` 과 `vscode_command` 는 **그 창의 권한으로 무엇이든** 실행한다.
  로컬 창이면 이 윈도우에서, 원격 창이면 그 원격에서. 원격 세션이 SSH 역터널로
  데몬에 붙어 있으면 그 세션도 이 도구들을 쓸 수 있다 — aglink-screen 이 이미 주는
  권한(키보드·마우스 전체)과 같은 수준이다. README 에 명시한다.
- 확장 설정 `aglink.enabled`(기본 true)로 창별로 끌 수 있다.

## 설치

`vscode/install.ps1`:
1. `vscode/` 를 `.vsix` 로 묶는다.
2. `ELECTRON_RUN_AS_NODE`·`VSCODE_*` 를 지우고 `code --install-extension` 로 설치.

새 확장은 설치하면 **열려 있는 창들에서 바로 켜진다**(재시작 불필요) — 실물 점검에서
확인한다. 안 켜지면 창마다 "Developer: Reload Window" 가 필요하다고 문서에 적는다.

## 위험

- **UI 확장이 원격 창의 진단(문제 목록)을 볼 수 있는가.** 진단은 원격 확장 호스트의
  언어 서버가 만든다. VS Code 는 진단을 메인 쪽에 모아 모든 확장 호스트에 나눠 주는
  것으로 알고 있으나 실측으로 확인한다. 안 되면 `vscode_problems` 는 로컬 창 전용으로
  문서화한다.
- **셸 통합이 원격 bash 에서 켜지는가.** 기본 자동 주입이 켜져 있으면 된다. 실측.
- **git 확장 API 는 쓰지 않는다.** 원격 창에서 git 확장은 원격 확장 호스트에서 돌아
  UI 확장이 그 `exports` 를 부를 수 없다. 브랜치 같은 건 `vscode_terminal_run
  "git status"` 로 충분하다.

## 시험

- 확장(JS): `vscode` 모듈을 흉내 낸 가짜로 `node --test` — 연결·재연결, 핑 응답,
  메서드별 결과, 출력 버퍼(개수·크기 상한), ANSI 제거, 셸 통합 없는 터미널.
- 데몬(Go): `/vscode` 등록·끊김, 크롬 등록부와 분리(기본 프로필에 안 뽑힘),
  `vscode:` 이름 해석(앞부분/겹침/없음), 도구·프로필 짝 안 맞으면 거절,
  `list_profiles` 줄.
- 실물: 사용자 VS Code 에 설치 → 이 세션에서 `backend` 원격 창의 작업공간·열린
  파일·파일 내용·문제 목록·`vscode_terminal_run "pwd; ls"` → 출력 확인.
