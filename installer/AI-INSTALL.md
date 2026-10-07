# aglink-web · aglink-screen 설치 지침 (AI 에이전트용)

> 이 문서는 **사용자 PC 에서 설치를 대신 해 줄 AI 에이전트**(Claude Code 등)가 읽고
> 그대로 실행하도록 쓴 것이다. 사용자에게는 설치 결과와, 사람이 꼭 해야 하는
> 한 가지(크롬 확장 불러오기)만 알려 주면 된다.
>
> 사용자가 이렇게 말하면 시작한다: "이 폴더의 README.md 대로 aglink 설치해줘"

## 0. 무엇을 설치하나

이 폴더에 함께 있는 파일:

| 파일 | 설치되는 것 | 설치 위치 (관리자 권한 불필요) |
|---|---|---|
| `aglink-web-Setup.exe` | 브라우저·Electron/Wails 앱·VS Code 창을 **텍스트로** 읽고 조작하는 MCP 서버 + 크롬 확장 + VS Code 확장 | `%LOCALAPPDATA%\Programs\aglink-web` |
| `aglink-screen-Setup.exe` | 일반 윈도우 프로그램 화면 조작(창 목록·접근성 트리·클릭·캡처) MCP 서버 | `%LOCALAPPDATA%\Programs\aglink-screen` |

사용자가 따로 말하지 않으면 **둘 다** 설치한다. 웹·앱 작업만 한다고 하면 web 만 설치한다.
이미 설치돼 있어도 같은 절차로 다시 실행하면 새 버전으로 바뀐다(업데이트).

## 1. 사전 확인

PowerShell 에서 차례로 확인한다. 하나라도 걸리면 사용자에게 알리고 해결한 뒤 진행한다.

```powershell
# (a) Windows x64 인지
[Environment]::Is64BitOperatingSystem          # True 여야 함

# (b) Claude Code CLI 가 PATH 에 있는지 — 설치 프로그램이 이걸로 MCP 를 등록한다
where.exe claude                               # 경로가 나와야 함

# (c) 이미 등록된 aglink MCP 가 있는지
claude mcp list
```

- **(b) 에서 `claude` 가 없으면**: 설치는 되지만 MCP 등록이 빠진다. Claude Code 를 먼저
  설치하거나, 설치 후 §3 의 수동 등록 명령을 실행한다.
- **(c) 에서 `aglink-web` / `aglink-screen` 이 `http://127.0.0.1:...` 로 이미 등록돼 있으면
  멈추고 사용자에게 묻는다.** 개발자 PC 의 상시 데몬 등록일 가능성이 높고, 설치 프로그램은
  같은 이름의 등록을 지우고 자기 것으로 바꾼다. 이미 `...\Programs\aglink-web\aglink-web.exe mcp`
  처럼 이 설치 프로그램의 경로로 등록돼 있으면 업데이트이므로 그대로 진행한다.
- 크롬을 쓸 거라면 크롬이 설치돼 있고 Google 계정으로 로그인돼 있어야 한다(확장이
  로그인한 계정 이름으로 붙는다). VS Code 는 있으면 확장이 자동 설치되고, 없어도 된다.

## 2. 설치 (조용히)

이 README 가 있는 폴더에서 실행한다. `/S` 는 창 없이 설치한다.

```powershell
Start-Process .\aglink-web-Setup.exe    -ArgumentList '/S' -Wait
Start-Process .\aglink-screen-Setup.exe -ArgumentList '/S' -Wait
```

설치 프로그램이 하는 일 — **추가 설정은 필요 없다**:

- **aglink-web**: 이 설치 폴더에서 돌던 이전 버전만 종료 → 파일 설치 → Claude Code 에
  `aglink-web` 등록(사용자 범위, stdio) → VS Code 가 있으면 `aglink-vscode` 확장 설치 →
  데몬을 지금 시작하고 로그온 때마다 자동 시작(`HKCU\...\Run`).
- **aglink-screen**: 파일 설치 → Claude Code 에 `aglink-screen` 등록. 상시로 도는 것은
  없다(Claude 가 필요할 때 띄운다).

선택 옵션 — 사용자가 요청할 때만 `-ArgumentList` 에 덧붙인다(값은 다음 업데이트 때도 유지):

| 옵션 | 뜻 |
|---|---|
| `/STEPDELAY=200` | 연속 동작(run_steps / run_sequence) 단계 사이 기본 대기 ms. 화면 전환 애니메이션이 있는 앱이면 150~300 |
| `/INSECUREHOSTS=nas.local,10.0.0.5:8443` | (web) 인증서 경고를 자동으로 넘길 **사내** 호스트. 사용자가 이름을 댄 호스트만 넣는다 |
| `/CDPPORTS=9222-9240,9333` | (web) Electron/Wails 앱을 찾을 포트. 기본값과 같으므로 보통 불필요 |
| `/NOVSCODE` `/NOAUTOSTART` `/NOCLAUDE` | VS Code 확장 / 자동 시작 / Claude 등록을 건너뜀 |
| `/D=C:\경로` | 설치 위치 변경 — **맨 끝에, 따옴표 없이** |

예: `Start-Process .\aglink-web-Setup.exe -ArgumentList '/S /STEPDELAY=200' -Wait`

## 3. 설치 확인

```powershell
# 파일
Test-Path "$env:LOCALAPPDATA\Programs\aglink-web\aglink-web.exe"
Test-Path "$env:LOCALAPPDATA\Programs\aglink-screen\aglink-screen.exe"

# aglink-web 데몬 — "ok" 가 나와야 함 (설치 직후 몇 초 걸릴 수 있음)
(Invoke-WebRequest -UseBasicParsing http://127.0.0.1:48219/health).Content

# Claude Code 등록 — aglink-web, aglink-screen 이 보이고 Connected 여야 함
claude mcp list
```

등록이 빠졌으면(§1 (b) 의 경우 등) 직접 등록한다:

```powershell
claude mcp add --scope user aglink-web    -- "$env:LOCALAPPDATA\Programs\aglink-web\aglink-web.exe" mcp
claude mcp add --scope user aglink-screen -- "$env:LOCALAPPDATA\Programs\aglink-screen\aglink-screen.exe" mcp
```

데몬 health 가 실패하면 한 번 직접 띄우고 다시 확인한다:

```powershell
Start-Process wscript.exe -ArgumentList '//B','//Nologo',"$env:LOCALAPPDATA\Programs\aglink-web\start-web-daemon.vbs"
```

## 4. 도구 사용 권한 (권장)

Claude Code 는 MCP 도구를 처음 쓸 때마다 허용할지 묻는다. 매번 묻지 않게 하려면
`%USERPROFILE%\.claude\settings.json` 의 `permissions.allow` 배열에 **서버 단위 규칙**
두 줄을 넣는다. 서버 단위라서 나중에 새 버전에서 도구가 늘어도 다시 고칠 필요가 없다.

```json
"mcp__aglink-web",
"mcp__aglink-screen"
```

- 파일이 이미 있으면 **기존 내용을 지우지 말고** 배열에 두 항목만 추가한다(이미 있으면 생략).
  파일이 없으면 `{ "permissions": { "allow": [ "mcp__aglink-web", "mcp__aglink-screen" ] } }` 로 만든다.
- 이 단계는 사용자에게 "aglink 도구는 묻지 않고 쓰도록 허용해 두었다"고 알린다. 사용자가
  원치 않으면 건너뛴다.

## 5. 사람이 해야 하는 한 가지 — 크롬 확장 불러오기 (aglink-web, 처음 한 번)

크롬은 스토어 밖 확장을 스스로 설치하지 않으므로 이 단계는 자동화할 수 없다.
AI 는 준비만 해 주고, 사용자에게 아래 3단계를 안내한다.

```powershell
# 확장 폴더 경로를 클립보드에 복사하고, 크롬 확장 페이지를 연다
Set-Clipboard "$env:LOCALAPPDATA\Programs\aglink-web\chrome-extension"
Start-Process chrome 'chrome://extensions'
```

사용자에게 전할 안내:

1. 열린 `chrome://extensions` 페이지 오른쪽 위 **개발자 모드**를 켠다.
2. **압축해제된 확장 프로그램을 로드합니다**를 누른다.
3. 폴더 선택 창의 주소칸에 붙여넣기(Ctrl+V — 경로가 복사돼 있다) 하고 **폴더 선택**.

**업데이트였다면** 이 단계 대신: `chrome://extensions` 에서 aglink-web 카드의 새로고침(↻)을
한 번 누르게 한다. (새로고침하지 않으면 데몬은 새 버전인데 확장은 옛 코드라, 새 기능이
`unknown method` 로 실패한다.)

## 6. 마무리 — 새 세션에서 동작 확인

MCP 도구는 **Claude Code 세션이 시작될 때** 붙는다. 지금 설치를 진행한 세션에는 aglink
도구가 보이지 않는 것이 정상이다. 사용자에게 Claude Code 세션을 새로 열라고 안내한다
(이미 열려 있던 다른 세션은 `/mcp` 에서 다시 연결하거나 새로 연다).

새 세션에서의 확인 방법(사용자에게 알려 줄 것):

- "aglink-web 의 list_profiles 해 봐" → 크롬 계정 이메일이 `| default` 와 함께 나오면 크롬
  연결 성공. VS Code 창은 `vscode:<폴더>@<호스트>` 로 나온다.
- "aglink-screen 으로 열린 창 목록 보여줘" → 창 목록이 나오면 성공.

사용자에게 최종 보고할 내용: 설치한 제품과 버전(설정 → 앱 에 표시), §3 확인 결과, 권한
허용 여부(§4), 크롬 확장 단계(§5)가 끝났는지.

## 문제가 생기면

| 증상 | 확인 · 조치 |
|---|---|
| 새 세션에 aglink 도구가 없음 | `claude mcp list` 에 등록이 있는지. 없으면 §3 수동 등록 |
| `list_profiles` 에 크롬이 없음 | §5 를 했는지, 크롬에 로그인돼 있는지, `http://127.0.0.1:48219/health` 가 ok 인지 |
| 새 도구가 `unknown method` | 크롬 확장 새로고침(§5 업데이트) |
| VS Code 창이 안 보임 | VS Code 에서 Ctrl+Shift+P → "Developer: Reload Window". 그래도 안 되면 `code --install-extension "$env:LOCALAPPDATA\Programs\aglink-web\aglink-vscode.vsix" --force` |
| Electron/Wails 앱이 안 보임 | 앱이 디버그 포트로 떠 있어야 한다. Electron 은 `앱.exe --remote-debugging-port=9222` |
| aglink-screen 클릭이 안 먹고 경고가 붙음 | 대상 앱이 관리자 권한이면 윈도우가 막는다(UIPI) — Claude Code 를 관리자 권한으로 실행 |

## Claude Code 가 아닌 AI 툴에서 쓰려면

설치 프로그램은 Claude Code 에만 자동 등록한다. 다른 MCP 클라이언트(Cursor, Codex 등)는
그 툴의 MCP 설정에 stdio 서버로 직접 추가한다:

```json
{
  "mcpServers": {
    "aglink-web":    { "command": "%LOCALAPPDATA%\\Programs\\aglink-web\\aglink-web.exe",       "args": ["mcp"] },
    "aglink-screen": { "command": "%LOCALAPPDATA%\\Programs\\aglink-screen\\aglink-screen.exe", "args": ["mcp"] }
  }
}
```

`%LOCALAPPDATA%` 를 펼쳐 주지 않는 툴이면 실제 경로(`C:\Users\<사용자>\AppData\Local\...`)로 바꿔 적는다.

## 제거

설정 → 앱 → aglink-web / aglink-screen, 또는 조용히:

```powershell
& "$env:LOCALAPPDATA\Programs\aglink-web\uninstall.exe" /S
& "$env:LOCALAPPDATA\Programs\aglink-screen\uninstall.exe" /S
```

제거는 자동 시작·Claude 등록(이 설치가 만든 것만)·VS Code 확장까지 되돌린다. 사용자 설정
`%USERPROFILE%\.aglink` 와 §4 의 권한 규칙은 남는다. 크롬 확장은 `chrome://extensions` 에서
직접 삭제한다.
