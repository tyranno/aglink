# MCP 서버 이름과 포트 규칙

aglink 의 MCP 서버는 여러 머신에서 여러 경로로 붙는다. 이름이 제각각이면 어느
도구가 어느 머신의 무엇을 움직이는지 헷갈리고, 권한 목록(`mcp__<이름>__도구`)도
엉킨다. 그래서 이름은 한 가지 질문으로만 정한다: **그 머신 자신의 것인가, SSH 로
건너간 윈도우 PC 의 것인가.**

| 이름 | 무엇을 움직이나 | 어디에 등록 |
|---|---|---|
| `aglink-web` | **그 머신 자신의** 브라우저 (+ 앱·VS Code 창) | 그 머신 |
| `aglink-screen` | **그 머신 자신의** 화면 | 윈도우 머신 |
| `aglink-web-remote` | SSH 역터널 너머 **윈도우 PC** 의 브라우저·앱·VS Code 창 | 원격 리눅스 |
| `aglink-screen-remote` | SSH 역터널 너머 **윈도우 PC** 의 화면 | 원격 리눅스 |

도구 이름이 곧 이 이름이다: `mcp__aglink-web-remote__click` 은 윈도우 PC 의 크롬을
누르고, 같은 원격에서 `mcp__aglink-web__click` 은 그 리눅스 머신의 크롬을 누른다.

## 포트

| 포트 | 무엇 | 어디서 도나 |
|---|---|---|
| 48219 | aglink-web 데몬 (`/mcp`, `/ext`, `/vscode`) | 윈도우 PC |
| 48220 | aglink-screen `serve` (`/mcp`) | 윈도우 PC |
| 48221 | 리눅스 머신 자신의 aglink-web 데몬 | 원격 리눅스 (48219 는 터널이 차지하므로) |

원격 리눅스에서 48219·48220 은 SSH `RemoteForward` 로 윈도우 PC 의 같은 포트에
이어진 **터널 입구**다. 원격의 루프백은 계정 구분이 없어서 터널 하나를 그 머신의 모든
계정이 같이 쓰고, MCP 등록만 계정마다 한다.

## 등록 명령

**윈도우 PC** (설치 프로그램이 대신 해 준다 — `installer/README.md`):

```sh
claude mcp add --scope user aglink-web    -- "<설치폴더>\aglink-web.exe" mcp
claude mcp add --scope user aglink-screen -- "<설치폴더>\aglink-screen.exe" mcp
```

항시 켜 둔 데몬에 HTTP 로 붙여 세션마다 프로세스가 뜨지 않게 할 수도 있다(개발 PC
설정):

```sh
claude mcp add --transport http --scope user aglink-web    http://127.0.0.1:48219/mcp
claude mcp add --transport http --scope user aglink-screen http://127.0.0.1:48220/mcp
```

**원격 리눅스** (계정마다 한 번):

```sh
claude mcp add --transport http --scope user aglink-web-remote    http://127.0.0.1:48219/mcp
claude mcp add --transport http --scope user aglink-screen-remote http://127.0.0.1:48220/mcp
# 그 리눅스 머신에도 aglink-web 데몬과 크롬 확장을 깔았다면:
claude mcp add --transport http --scope user aglink-web           http://127.0.0.1:48221/mcp
```

## 이름을 바꿀 때

MCP 이름을 바꾸면 도구 이름이 바뀐다. 권한 허용 목록(`~/.claude/settings.json`
의 `permissions.allow`)에 적힌 `mcp__<옛 이름>__…` 도 같이 고치고, 떠 있던 Claude
세션은 새로 열어야 반영된다.
