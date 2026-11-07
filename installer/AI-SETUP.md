# aglink 전체 설치·설정 지침 (AI 에이전트용)

> 이 문서는 **사용자 PC 에서 설치와 설정을 대신 해 줄 AI 에이전트**(Claude Code 등)가 읽고
> 그대로 실행하도록 쓴 것이다. 사람이 꼭 직접 해야 하는 일(관리자 권한 승인, 텔레그램 봇 만들기,
> 크롬 확장 불러오기)만 사용자에게 안내하고, 나머지는 AI 가 처리한다.
>
> 사용자가 이렇게 말하면 시작한다:
> - "https://github.com/tyranno/aglink/releases/latest 의 README 대로 aglink 설치해줘"
> - 또는 설치 후 "C:\Program Files\aglink\AI-SETUP.md 대로 aglink 설정해줘"
>
> Claude Code 에 브라우저·화면 제어 도구만 붙이려는 경우(aglink 본체 없이)는 이 문서가 아니라
> 릴리스의 `aglink-web-Setup.exe` / `aglink-screen-Setup.exe` 와 `AI-INSTALL.md` 를 따른다.

## 0. 무엇이 설치되나

`aglink-Setup.exe` 하나로 아래가 `C:\Program Files\aglink` 에 설치된다(관리자 권한 필요).

| 파일 | 역할 |
|---|---|
| `aglink.exe` | 본체(호스트). 텔레그램·웹·데스크톱 대화를 받아 claude / codex / opencode CLI 를 돌린다 |
| `aglink-desktop.exe` | 데스크톱 앱. 대화·업무관리·예약·회의 모드·음성 대화 |
| `aglink-chat.exe` | 브라우저 채팅 화면 (http://127.0.0.1:27271) |
| `aglink-screen.exe` | 윈도우 프로그램 화면 제어 도구 (본체가 필요할 때 띄움) |
| `aglink-web.exe` | 크롬·앱 창을 텍스트로 읽고 조작하는 도구 (본체가 필요할 때 띄움) |
| `chrome-extension\` | aglink-web 이 크롬을 조작할 때 쓰는 확장 (§6 에서 사람이 한 번 불러옴) |
| `AI-SETUP.md` | 이 문서 |

설치 프로그램은 로그온 때 관리자 권한으로 자동 시작하는 작업(작업 스케줄러 `aglink`)을 등록한다.
**하지만 설정 파일이 없으면 본체는 시작하자마자 종료된다.** 그래서 설치 뒤 §3 의 설정 파일을
꼭 만들어야 한다.

사용자 데이터는 모두 `%USERPROFILE%\.aglink` 에 있다(설정, 대화 기록, 로그). 재설치·제거해도 남는다.

## 1. 사전 확인

PowerShell 에서 확인한다. 걸리는 것이 있으면 사용자에게 알리고 해결한 뒤 진행한다.

```powershell
[Environment]::Is64BitOperatingSystem      # True 여야 함
where.exe claude; where.exe codex; where.exe opencode   # 적어도 하나는 경로가 나와야 함
claude -p "hi" --max-turns 1               # claude 를 쓸 거면 로그인돼 있어 답이 나와야 함
Test-Path "$env:USERPROFILE\.aglink\config.yaml"        # True 면 이미 설정돼 있음 → 업데이트
```

- aglink 는 **이미 설치되고 로그인된** AI CLI 를 그대로 쓴다. 하나도 없으면 먼저 설치하도록
  안내한다(claude: https://claude.com/claude-code). 로그인이 안 돼 있으면 사용자에게 터미널에서
  `claude` 를 한 번 실행해 로그인하도록 한다(브라우저 승인이 필요해 AI 가 대신 못 한다).
- `config.yaml` 이 이미 있으면 **업데이트**다. §2 만 하고 §3 은 건너뛴 뒤 §4 로 확인한다.
  기존 설정 파일을 덮어쓰지 않는다.

## 2. 설치

### 2-1. 설치 파일 받기 (아직 없을 때)

```powershell
$dir = Join-Path $env:TEMP 'aglink-setup'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
Invoke-WebRequest -UseBasicParsing 'https://github.com/tyranno/aglink/releases/latest/download/aglink-Setup.exe' -OutFile "$dir\aglink-Setup.exe"
Unblock-File "$dir\aglink-Setup.exe"
```

### 2-2. 조용히 설치

```powershell
Start-Process "$dir\aglink-Setup.exe" -ArgumentList '/S' -Verb RunAs -Wait
```

- 관리자 권한 확인(UAC) 창이 뜬다. **AI 는 이 창을 누를 수 없으니** 사용자에게 "예"를 눌러
  달라고 미리 말한다.
- 설치 프로그램은 이전 버전을 끄고, 파일을 바꾸고, 자동 시작 작업을 등록하고, 바로 시작한다.
  업데이트라면 여기서 끝이고 §4 로 간다.

## 3. 설정 파일 만들기 (처음 설치일 때)

설정 파일이 없으면 aglink 가 처음 시작할 때 **웹 채팅 전용 기본 설정을 스스로 만든다**(텔레그램 없음,
화면 제어 끔, 설치된 CLI 중 하나를 기본 백엔드로). 그래서 설치만 해도 웹·데스크톱 앱으로 바로 쓸 수
있고, 텔레그램이나 화면 제어가 필요할 때만 아래처럼 설정을 고친다.

`aglink.exe setup` 이라는 대화형 마법사도 있지만 진짜 터미널 입력이 필요해서 AI 가 돌릴 수 없다.
AI 는 아래처럼 **`%USERPROFILE%\.aglink\config.yaml` 을 직접 쓴다**. 먼저 사용자에게 두 가지를 묻는다.

1. **텔레그램으로도 쓸지** (휴대폰에서 대화). 아니면 웹·데스크톱 앱만 쓴다.
2. **화면 제어를 켤지** (AI 가 윈도우 프로그램을 직접 클릭·입력). 기본은 끔. 켜면 aglink 의
   AI 가 이 PC 화면을 조작할 수 있다는 점을 설명하고 동의를 받는다.

### 3-1. 텔레그램을 쓸 때만: 봇 토큰과 사용자 ID

사용자에게 안내한다:

1. 텔레그램에서 `@BotFather` 를 열고 `/newbot` → 봇 이름과 아이디(끝이 `bot`)를 정한다.
2. BotFather 가 준 토큰(`123456789:AA...` 모양)을 AI 에게 알려 준다.
3. 방금 만든 봇과의 대화창을 열고 아무 말이나 한 번 보낸다.

그다음 AI 가 사용자 ID 를 읽는다:

```powershell
$token = '<봇 토큰>'
(Invoke-RestMethod "https://api.telegram.org/bot$token/getUpdates").result |
    ForEach-Object { $_.message.from } | Select-Object id, username -Unique
```

나온 `id`(숫자)가 `allowed_user_ids` 에 들어간다. 이 ID 의 사람만 봇을 쓸 수 있다.
결과가 비어 있으면 사용자가 봇에게 메시지를 보냈는지 다시 확인한다.

### 3-2. config.yaml 쓰기

아래 내용으로 만든다. `<...>` 만 바꾼다.

- 텔레그램을 안 쓰면 `bot_token` 을 `""` 로 두고 `allowed_user_ids` 는 `[1]` 로 둔다(웹 전용일 때
  쓰는 자리표시자 값이다).
- `backend.default` 는 §1 에서 설치·로그인이 확인된 것: `claude`, `codex`, `opencode` 중 하나.
- `screen_control.enabled` 는 §3 질문 2 의 답.

```powershell
$cfg = @'
home_dir: ""
telegram:
  bot_token: "<봇 토큰 또는 빈 문자열>"
  allowed_user_ids: [<사용자 ID 또는 1>]
models:
  manager: haiku
  manager_always: true
backend:
  default: claude
runtime:
  timeout_minutes: 10
  max_workers: 3
  rate_limit_per_min: 20
  persistent_worker: true
screen_control:
  enabled: false
web_control:
  enabled: true
aglink_chat:
  enabled: true
chat_control:
  enabled: true
'@
$path = "$env:USERPROFILE\.aglink\config.yaml"
New-Item -ItemType Directory -Force -Path (Split-Path $path) | Out-Null
[IO.File]::WriteAllText($path, $cfg, (New-Object Text.UTF8Encoding $false))
```

- `home_dir` 가 비어 있으면 AI 작업 폴더로 `%USERPROFILE%\aglink` 를 쓴다. 사용자가 원하는 작업
  폴더가 있으면 그 경로를 넣는다(YAML 이라 `\` 는 `/` 로 쓰거나 작은따옴표로 감싼다).
- 이 파일에는 봇 토큰이 들어가므로 다른 곳에 올리거나 보여 주지 않는다.
- 설정은 실행 중에도 바로 반영되지만, 봇 토큰을 바꾸면 다시 시작해야 한다.

### 3-3. 시작

```powershell
& "C:\Program Files\aglink\aglink.exe" start
```

`start` 는 자동 시작 작업을 지금 실행한다. 작업이 없다는 오류가 나면 관리자 PowerShell 에서
`& "C:\Program Files\aglink\aglink.exe" install` 을 한 번 실행하게 한다(UAC 승인 필요).

## 4. 동작 확인

```powershell
Start-Sleep 5
Invoke-RestMethod http://127.0.0.1:27269/health      # ok 가 True 여야 함. problems 에 원인이 나온다
(Invoke-WebRequest -UseBasicParsing http://127.0.0.1:27271/).StatusCode   # 200 이면 웹 채팅 화면 정상
& "C:\Program Files\aglink\aglink.exe" version
```

- health 가 응답하지 않으면 로그 끝부분을 본다: `Get-Content "$env:USERPROFILE\.aglink\aglink.log" -Tail 40`.
  설정 오류는 대개 여기 한 줄로 나온다(예: 토큰 없음, 백엔드 이름 오타).
- 텔레그램을 설정했다면 사용자에게 봇에게 "안녕" 을 보내 답이 오는지 확인하게 한다.
- 데스크톱 앱을 연다: `Start-Process "C:\Program Files\aglink\aglink-desktop.exe"`
  (바탕화면과 시작 메뉴의 `aglink` 바로가기도 같다). 대화 탭에서 한 문장 보내 답이 오면 성공이다.

## 5. 도구 등록 — 할 일 없음

aglink 의 AI 는 화면·브라우저 도구를 aglink 가 직접 넘겨주고 미리 허용해 두므로 `claude mcp add`
같은 등록이나 권한 설정은 **필요 없다.** 나머지 설정(모델, 프로젝트 등)은 데스크톱 앱이나
웹 화면의 설정(`http://127.0.0.1:27271/setting`)에서 바꾼다.

(aglink 밖의 일반 Claude Code 세션에서도 이 도구를 쓰고 싶다면 릴리스의 `aglink-web-Setup.exe`,
`aglink-screen-Setup.exe` 를 `AI-INSTALL.md` 대로 따로 설치한다.)

## 6. 사람이 해야 하는 일 — 크롬 확장 불러오기 (브라우저 제어를 쓸 때, 처음 한 번)

크롬은 스토어 밖 확장을 스스로 설치하지 않으므로 자동화할 수 없다. AI 는 준비만 하고 안내한다.

```powershell
Set-Clipboard "C:\Program Files\aglink\chrome-extension"
Start-Process chrome 'chrome://extensions'
```

사용자에게 전할 안내:

1. 열린 `chrome://extensions` 페이지 오른쪽 위 **개발자 모드**를 켠다.
2. **압축해제된 확장 프로그램을 로드합니다**를 누른다.
3. 폴더 선택 창 주소칸에 붙여넣기(Ctrl+V) 하고 **폴더 선택**.

크롬은 Google 계정으로 로그인돼 있어야 한다. **업데이트였다면** 이 단계 대신
`chrome://extensions` 의 aglink-web 카드에서 새로고침(↻)을 한 번 누르게 한다.

## 7. 음성 기능 (선택)

### 7-1. 데스크톱 앱의 음성 대화·회의 모드 — 따로 설치할 것 없음

데스크톱 앱은 음성 인식(whisper)을 **처음 쓸 때 스스로 내려받는다**(약 500MB, `%USERPROFILE%\.aglink\whisper`).
사용자에게 안내할 것:

- 입력줄의 마이크 버튼을 누르면 처음 한 번 내려받기가 진행된다. 인터넷이 필요하다.
- 호출어는 **"아글링크"** 다. 부르면 대화가 시작되고, 3분 동안 조용하면 다시 호출어를 기다린다.
  호출어 모드·대화 유지 시간·목소리 등은 데스크톱 앱의 음성 설정에서 바꾼다.
- 회의 모드는 오른쪽 위 **🎙 회의** 버튼으로 시작·종료한다. 회의록은
  `문서\aglink 회의` 폴더에 저장되고, 끝나면 요약·결정사항·할 일 정리가 함께 만들어진다.
  내 목소리를 등록해 두면 "나" 와 상대를 구분한다.
- 스피커폰을 쓰면 윈도우 소리 설정에서 **입력과 출력을 모두 같은 스피커폰**으로 맞춰야 에코 제거가 된다.
- 회의를 녹음·받아 적을 때는 참석자에게 미리 알리도록 권한다.

GPU 서버나 다른 PC 의 whisper 서버를 쓰려면(선택) 사용자 환경 변수 `AGLINK_WHISPER_URL` 을
`http://<서버>:8178` 처럼 넣고 데스크톱 앱을 다시 연다. 서버가 응답하지 않으면 자동으로 이 PC 의
whisper 를 쓴다.

```powershell
[Environment]::SetEnvironmentVariable('AGLINK_WHISPER_URL', 'http://<서버>:8178', 'User')
```

### 7-2. VS Code 에서 음성으로 Claude 와 대화 — aglink-voice

VS Code 안에서 음성을 쓰려는 경우에만 설치한다. VS Code 가 설치돼 있어야 하고, 관리자 권한은
필요 없다. 파일이 커서(기본판 약 870MB) 받는 데 시간이 걸린다고 미리 알린다.

```powershell
$v = '0.5.0'
$base = "https://github.com/tyranno/aglink-voice/releases/download/v$v"
Invoke-WebRequest -UseBasicParsing "$base/aglink-voice-setup-$v.exe" -OutFile "$dir\aglink-voice-setup.exe"
Unblock-File "$dir\aglink-voice-setup.exe"
Start-Process "$dir\aglink-voice-setup.exe" -ArgumentList '/S' -Wait
code --list-extensions | Select-String aglink-voice      # 설치 확인
```

- 최신 버전 번호는 https://github.com/tyranno/aglink-voice/releases/latest 에서 확인한다.
- NVIDIA 그래픽카드가 있으면 기본판을 설치한 **뒤에** `aglink-voice-gpu-<버전>.exe` 를 같은 방법으로
  추가 설치하면 인식이 빨라진다. 그래픽 드라이버가 없으면 설치 프로그램이 경고한다.
- 사용자에게 안내: VS Code 를 다시 열고, 아래 상태 표시줄의 **음성** 버튼 → 처음에 목소리 등록(15초)
  → 이후 `Ctrl+Alt+V` 로 말하기. 데스크톱 앱이 켜져 있으면 자동으로 데스크톱 앱의 음성을 함께 쓴다.

## 8. 마무리 보고

사용자에게 알릴 것:

- 설치한 버전(§4 `version`), health 결과, 텔레그램·웹·데스크톱 중 무엇을 켰는지.
- 화면 제어를 켰는지, 크롬 확장 단계(§6)가 남았는지, 음성(§7) 상태.
- 쓰는 곳: 데스크톱 앱(바탕화면 `aglink`), 웹 채팅 http://127.0.0.1:27271, 텔레그램 봇.
- 로그온하면 자동으로 시작된다는 것.

## 문제가 생기면

| 증상 | 확인 · 조치 |
|---|---|
| health 가 응답 없음 | `aglink.log` 끝부분 확인. 설정 파일 오류면 고친 뒤 `aglink.exe start` |
| `problems` 에 텔레그램 오류 | 봇 토큰 오타, 또는 같은 토큰으로 다른 PC 의 aglink 가 돌고 있음 |
| 텔레그램 봇이 답을 안 함 | `allowed_user_ids` 에 보낸 사람 ID 가 있는지(§3-1) |
| AI 답이 안 오고 오류 | 백엔드 CLI 로그인 확인: 터미널에서 `claude` 를 실행해 다시 로그인 |
| 브라우저 제어가 안 됨 | §6 확장을 불러왔는지, 크롬 로그인, `http://127.0.0.1:48219/health` 가 ok 인지 |
| 화면 클릭이 안 먹음 | 대상 프로그램이 관리자 권한이면 `screen_control.elevated: true` 로 바꿈 |
| 음성 내려받기 실패 | 인터넷·방화벽 확인 후 마이크 버튼을 다시 누름. 로그: `%USERPROFILE%\.aglink\aglink-desktop.log` |
| 마이크는 되는데 소리가 안 남 | 윈도우가 출력 장치를 바꿨을 수 있음. 작업 표시줄 스피커에서 장치 선택 |

## 제거

설정 → 앱 → aglink, 또는 `& "C:\Program Files\aglink\uninstall.exe" /S` (관리자 승인 필요).
자동 시작 작업과 바로가기까지 지운다. `%USERPROFILE%\.aglink`(설정·대화 기록)는 남는다.
크롬 확장은 `chrome://extensions` 에서, aglink-voice 는 설정 → 앱에서 따로 지운다.
