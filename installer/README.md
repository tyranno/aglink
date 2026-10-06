# installer — 제품별 설치 프로그램

aglink 의 세 제품은 **각각 따로** 설치·배포할 수 있다.

| 설치 파일 | 무엇 | 권한 | 설치 위치 |
|---|---|---|---|
| `aglink-web-Setup.exe` | aglink-web 데몬 + 크롬 확장 + VS Code 확장(aglink-vscode) | 사용자 | `%LOCALAPPDATA%\Programs\aglink-web` |
| `aglink-screen-Setup.exe` | aglink-screen (화면 제어 MCP) | 사용자 | `%LOCALAPPDATA%\Programs\aglink-screen` |
| `aglink-Setup.exe` | 전체 묶음 — 텔레그램 호스트, 채팅, 데스크톱 앱 포함 | 관리자 | `Program Files\aglink` |

팀원에게는 보통 **aglink-web**(과 필요하면 aglink-screen)만 주면 된다. 관리자 권한이
필요 없고, 새 버전은 설치 파일을 다시 실행하면 덮어쓴다.

## 만들기

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File installer\build-product-setup.ps1 -Product web
powershell -NoProfile -ExecutionPolicy Bypass -File installer\build-product-setup.ps1 -Product screen
powershell -NoProfile -ExecutionPolicy Bypass -File installer\build-product-setup.ps1 -Product all   # 셋 다
```

- 필요한 것: Go, [NSIS](https://nsis.sourceforge.io/) (`C:\Program Files (x86)\NSIS`).
- 결과는 저장소 루트에 생긴다(`*.exe` 는 gitignore). 준비물은 `%TEMP%` 에 모아서
  저장소에 아무것도 남기지 않는다.
- 버전은 `1.0.<커밋 수>` — 늘기만 하므로 나중 빌드가 항상 업그레이드로 설치된다.
- `-Product aglink` 는 기존 `build-installer.ps1`(전체 묶음)을 부른다.

## 설치할 때 일어나는 일

**aglink-web**
1. 이 설치 폴더에서 돌고 있는 `aglink-web.exe` 만 끝낸다(업데이트용). 다른 곳에서
   도는 개발용 빌드 같은 건 건드리지 않는다.
2. 파일 설치. 크롬 확장 폴더는 통째로 바꾼다 — 폴더 이름이 그대로라 크롬에서
   새로고침만 하면 새 코드가 적용된다.
3. Claude Code 에 MCP 서버 `aglink-web` 등록(사용자 범위, stdio — 데몬이 꺼져 있으면
   알아서 띄운다). `claude` 가 없으면 등록 명령을 안내만 한다.
4. VS Code 가 있으면 `aglink-vscode` 확장 설치.
5. 데몬을 지금 시작하고 로그온 때마다 시작(`HKCU\...\Run` → `start-web-daemon.vbs`,
   콘솔 창이 번쩍이지 않게 wscript 로).
6. 마지막 화면에서 사용 안내(`guide.html`)를 연다. **크롬 확장만은 직접** 불러와야
   한다 — 크롬은 스토어 밖 확장을 스스로 설치하지 않는다.

**aglink-screen** — 파일 설치 + Claude Code 에 `aglink-screen` 등록. Claude 가 필요할
때 띄우므로 상시로 도는 것은 없다.

제거는 위를 전부 되돌린다(설정 → 앱). 사용자 설정 `%USERPROFILE%\.aglink` 는 남긴다.

등록 이름은 `aglink-web` / `aglink-screen` — **그 머신 자신의 것**이라는 뜻이다. 원격
리눅스의 Claude 가 이 PC 를 쓸 때는 `aglink-web-remote` / `aglink-screen-remote` 로
등록한다. 규칙과 포트는 [`docs/mcp-names.md`](../docs/mcp-names.md).

## 명령줄 옵션

조용히 설치하거나 일부를 건너뛸 때:

```
aglink-web-Setup.exe /S                               조용히 설치
aglink-web-Setup.exe /S /D=C:\Tools\aglink-web        설치 위치 지정 (/D 는 맨 끝, 따옴표 없이)
/NOCLAUDE      Claude Code 에 등록하지 않음
/NOVSCODE      VS Code 확장을 설치하지 않음 (web)
/NOAUTOSTART   자동 시작·지금 시작 안 함 (web)
uninstall.exe /S                                      조용히 제거
```

MCP 등록에 환경변수로 함께 넣는 설정(`claude mcp add … -e KEY=VALUE`):

```
/STEPDELAY=<ms>        배치 단계 사이 기본 대기 — web: AGLINK_WEB_STEP_DELAY_MS (run_steps),
                       screen: AGLINK_SCREEN_STEP_DELAY_MS (run_sequence)
/CDPPORTS=<spec>       Electron/Wails 앱 탐색 포트 (web: AGLINK_WEB_CDP_PORTS)
/INSECUREHOSTS=<list>  인증서 경고 자동 통과 호스트 (web: AGLINK_WEB_INSECURE_HOSTS)
```

- 준 값은 `HKCU\Software\aglink\mcp-env\<제품>` 에 기억되어, 다음 업데이트 때 옵션을
  다시 주지 않아도 유지된다. 빈 값(`/STEPDELAY=`)이면 지운다. 제거하면 함께 지운다.
- aglink-web 데몬은 보통 로그온 때 따로 떠 있어서 MCP 등록의 환경변수를 직접 받지
  못한다. 그래서 브리지가 호출마다 이 값들을 데몬에 실어 보낸다(`web/settings.go`) —
  등록을 바꾸면 데몬을 재시작하지 않아도 다음 호출부터 적용된다.

`/NOCLAUDE /NOVSCODE /NOAUTOSTART` 로 임시 폴더에 설치했다가 지우면, 개발 PC 의 설정을
건드리지 않고 설치 프로그램 자체를 시험할 수 있다.

## 파일

| 파일 | 역할 |
|---|---|
| `build-product-setup.ps1` | 제품별 빌드 |
| `common.nsh` | 공용: 옵션, 설치 폴더의 프로세스만 끝내기, Claude 등록/해제, 앱 목록 등록 |
| `aglink-web-setup.nsi`, `aglink-screen-setup.nsi` | 제품별 설치 스크립트 |
| `start-web-daemon.vbs` | 창 없이 데몬 시작(로그온 자동 시작용) |
| `web-guide.html`, `screen-guide.html` | 설치 후 여는 사용 안내 |
| `aglink-setup.nsi`, `build-installer.ps1` | 전체 묶음(기존) |
