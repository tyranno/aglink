# Linux 서버 배포 운영 가이드 (host 모듈)

헤드리스 리눅스 서버(ARM64/x86-64)에 `host` 모듈을 systemd user 서비스로
올려 두고, 텔레그램 봇 + 내장 스케줄러로 상시 운용하기 위한 문서다.

> **호스트별 접속 정보·계정·토큰은 이 파일에 적지 않는다.**
> 이 저장소는 GitHub 공개 저장소다. 실제 호스트명/IP/봇 ID 같은 값은
> `docs/deploy/<호스트>.local.md` 에 적고, 그 패턴은 `.gitignore` 되어 있다.

---

## 1. 구성 개요

서버 한 대에 다음이 함께 돌아가는 구성을 전제로 한다.

| 구성요소 | 역할 |
|---|---|
| `aglink.service` | 텔레그램 봇 + 내장 스케줄러 + Claude 워커 |
| `claude` CLI | 워커가 매 턴 자식 프로세스로 실행 |
| (선택) 별도 게이트웨이/크론 | 다른 봇·다른 스케줄러가 공존할 수 있음 → §7 |

`screen` / `web` / `chat` 모듈은 헤드리스 서버에 배포하지 않는다. 형제
바이너리가 없으면 host 가 해당 기능을 조용히 건너뛴다.

---

## 2. 사전 준비

- 대상 서버에 `claude` CLI 설치 및 로그인 완료
- SSH 키 인증 설정 (`~/.ssh/config` 에 별칭 등록 권장)
- 빌드 머신에 Go (workspace 지원 버전)
- **디스크 여유 공간** — §6.1 이 이것 때문에 터진다

---

## 3. 배포

```bash
./scripts/deploy-linux.sh [SSH_HOST] [REMOTE_PATH]
#   SSH_HOST    기본: nanopi
#   REMOTE_PATH 기본: ~/bin/aglink
#   GOARCH      기본: arm64  (x86-64 서버는 GOARCH=amd64)
```

스크립트가 하는 일: `host/` 만 크로스컴파일 → 원격 디렉터리 생성 → scp →
chmod → `aglink.service` 가 등록돼 있으면 재시작.

빌드는 `host/` 안에서 수행하며 `GOFLAGS=-mod=readonly` 를 강제한다.
리포 전역에 `go env -w GOFLAGS=-mod=mod` 가 걸려 있으면 `go.work` 아래에서
`-mod may only be set to readonly or vendor when in workspace mode` 로 실패하기
때문이다. 전역 설정은 건드리지 않는다.

## 4. 서비스 등록 (대상 서버에서 1회)

```bash
bash scripts/install-linux-service.sh ~/bin/aglink
```

- systemd **user** 서비스 → 루트 권한 불필요
- `loginctl enable-linger` 로 로그아웃 후에도 유지
- 로그: `~/.local/state/aglink/aglink{,.error}.log`
- PATH 에 NVM node bin 을 자동 포함

---

## 5. 경로 규칙 — 두 가지 함정

둘 다 실제로 운영 장애를 냈다. 스크립트가 막아주지만, 수동 설치 시 반드시 지킬 것.

### 5.1 바이너리를 `~/aglink` 에 두지 말 것

`~/aglink` 는 `host/homedir.go` 의 `defaultHomeDir()` — **모든 대화의 기본 작업
디렉터리**다. 여기에 파일이 있으면 워커의 cwd 가 디렉터리가 아니게 되어 CLI 가
실행 단계에서 죽는다.

```
[home] could not create home dir "/home/<user>/aglink": not a directory
[worker] 작업 실패: fork/exec /usr/bin/claude: not a directory
```

→ `~/bin/aglink` 를 쓴다. `install-linux-service.sh` 는 `~/aglink` 에 있는
바이너리를 아예 거부한다.

작업 디렉터리를 명시하려면 설정의 `home_dir` 을 쓴다 (예: `home_dir: /home/<user>`).

### 5.2 `~/.aglink` 를 미리 만들지 말 것

`host/config.go` 의 `dataDir()` 은 **`~/.aglink` 가 없을 때만** pre-rename
`~/.teleclaude` 를 복사 이전한다. 로그 디렉터리 만들자고 `~/.aglink` 를 먼저
생성하면 마이그레이션이 통째로 스킵되고 **빈 데이터 디렉터리로 기동**한다 —
대화·스케줄이 전부 사라진 것처럼 보인다.

그래서 서비스 로그는 데이터 디렉터리가 아니라 `~/.local/state/aglink` 에 쓴다.

### 5.3 데이터 디렉터리 결정 순서

1. `AGLINK_HOME` 환경변수가 있으면 그 경로
2. `~/.aglink` 가 있으면 그것
3. 없고 `~/.teleclaude` 가 있으면 → **복사 이전** 후 `~/.aglink`
4. 둘 다 없으면 `~/.aglink` 신규 생성

이전은 `.aglink.migrating-<pid>` 임시 디렉터리에 복사 → 설정 정규화 →
`rename` 으로 원자적 활성화한다. **legacy 디렉터리는 지우지 않으므로 롤백
가능**하다. 복사 제외 대상은 런타임 찌꺼기뿐이다: `*.pid`, `*.log`,
`screen-control.lock`, `aglink-web.port`.

---

## 6. 장애 대응

### 6.1 스케줄 작업이 전부 "인증 만료"로 실패

증상 — 응답 본문이 그대로 이 문자열:

```
Failed to authenticate. API Error: 401 OAuth access token has expired. Re-authenticate to continue.
```

원인 — 워커는 `~/.claude/.credentials.json` 의 OAuth access token(수명 약 8시간)에
의존한다. 갱신은 **파일 쓰기**를 수반하므로 **디스크가 가득 차면 갱신이 실패**하고
401 로 떨어진다. 사람이 CLI 를 직접 쓸 때만 살아나는 것처럼 보여서 원인이
가려진다.

확인:
```bash
df -h /                                    # 디스크 여유
python3 -c "import json,os,datetime;d=json.load(open(os.path.expanduser('~/.claude/.credentials.json')));e=d['claudeAiOauth']['expiresAt'];print(datetime.datetime.fromtimestamp(e/1000))"
```

조치:
1. 디스크 확보
2. 근본 대책 — `claude setup-token` 으로 장수명 토큰을 발급해 설정의
   `claude.oauth_token` 에 넣는다. 워커 실행 시 `CLAUDE_CODE_OAUTH_TOKEN` 으로
   주입되어 **만료된 `~/.claude/.credentials.json` 을 override** 한다.

참고 — CLI 는 이 실패에서 **exit 0** 으로 끝나고 401 을 결과 본문에 담는다.
`isAuthFailure` 가 이를 잡아 실패 턴으로 처리한다(아래 6.2 의 연쇄를 끊는다).

### 6.2 `No conversation found with session ID: <uuid>`

6.1 의 2차 피해다. 401 턴은 CLI 세션을 만들지 못했는데도 대화가 "시작됨"으로
기록되면, 이후 매 턴이 존재하지 않는 세션을 `--resume` 하다 죽는다.

관련 감지 함수 (`host/manager.go`):

| 함수 | 잡는 상황 | CLI 종료코드 |
|---|---|---|
| `isAuthFailure` | 인증 만료 — 답변 대신 401 본문 | 0 |
| `isSessionNotFound` | resume 대상 세션 소실 | non-zero |
| `isSessionAlreadyInUse` | fresh 세션 id 가 이미 존재 | non-zero |

이미 깨진 대화는 새 대화를 시작하는 게 빠르다.

### 6.3 스케줄이 발화했는데 아무 일도 안 일어남

`HandleScheduledTask` 는 **등록된 프로젝트가 없으면 로그를 남기지 않고 즉시
반환**한다. 텔레그램에만 이렇게 뜬다:

```
⏰ 예약 작업 실행 중: ...
⚠️ 예약 작업 실행 실패: 등록된 프로젝트가 없습니다. !project add <이름> <경로>
```

`tasks.json` 의 `lastFired` 는 갱신되므로 "발화는 했다"만 남는다. 프로젝트를
등록하면 해결된다.

### 6.4 스케줄 변경이 반영되지 않음

스케줄러는 기동 시 `tasks.json` 을 **1회만** 읽는다(파일 감시 없음). 파일을 직접
편집했다면 서비스를 재시작해야 한다. 서비스가 떠 있는 상태에서 편집하면 다음
발화 때 서비스가 덮어쓴다 — **반드시 정지 후 편집**.

---

## 7. 같은 서버에 다른 봇/크론이 공존할 때

한 대에 봇이 둘 이상, 스케줄러가 둘 이상 도는 경우가 흔하다. 증상 보고를 받으면
**어느 쪽 크론인지부터 가른다.**

- aglink 스케줄러 → 로그에 `[manager] scheduled task` / `[worker]` 가 남는다
- 시스템 `crontab` → `crontab -l`, 각 스크립트의 로그 파일을 본다

`crontab` 항목의 시각 주석이 UTC 기준으로 적혀 있는데 서버 TZ 가 KST 면 실제
발화가 9시간 어긋난다. `date` 로 서버 TZ 를 먼저 확인할 것.

### 게이트웨이형 클라이언트의 "인증 실패처럼 보이는" 오류

별도 게이트웨이(WebSocket)를 거쳐 메시지를 보내는 구성이라면, 게이트웨이가
죽었을 때 클라이언트가 이렇게 보고할 수 있다:

```
GatewayTransportError: gateway closed (1006 abnormal closure (no close frame))
```

1006 은 인증 거부처럼 읽히지만 실제로는 **ECONNREFUSED 가 WebSocket close
이벤트로 흘러나온 것**이다. 인증을 뒤지기 전에 포트부터 확인한다:

```bash
ss -ltn | grep <port>
systemctl --user status <gateway>.service
```

systemd 유닛에 `RestartPreventExitStatus=` 가 걸려 있고 유닛이 `failed` 상태면
`Restart=always` 여도 영영 안 뜬다. 설정을 고친 뒤에도 `reset-failed` 가 필요하다:

```bash
systemctl --user reset-failed <gateway>.service
systemctl --user start <gateway>.service
```

---

## 8. 배포 후 검증 레시피

바이너리를 바꿨으면 스케줄 경로까지 실제로 한 번 돌려본다. 대화형 메시지 없이
검증할 수 있다.

1. 서비스 정지 (`tasks.json` 을 서비스가 덮어쓰지 않게)
2. `~/.aglink/tasks.json` 에 몇 분 뒤 발화하는 임시 작업을 추가
   — `isTask: true`, `cronExpr: "<분> <시> * * *"`, `status: "pending"`,
   `chatId` 는 본인 텔레그램 ID
3. 서비스 기동 → 해당 시각까지 대기
4. 로그에서 다음 3줄을 확인

```
[manager] scheduled task → project=<프로젝트> conv=<n> workDir=<경로>
[worker] ▶ backend=claude model=... resume=false
[worker] ✅ elapsed=... output=... bytes
```

5. `~/.aglink/history/<프로젝트>/<날짜>.md` 에서 **응답 본문**을 확인한다.
   6.1 의 401 문구는 약 99바이트라 정상 짧은 답변과 길이가 비슷하니,
   길이만 보지 말고 내용을 볼 것.
6. 임시 작업 제거 → 서비스 재시작

---

## 9. 상태 점검 명령 모음

```bash
systemctl --user status aglink.service --no-pager -l
tail -f ~/.local/state/aglink/aglink.error.log

# 스케줄 목록
python3 -c "import json,os;[print(t['label'],'|',t.get('cronExpr'),'|',t['status']) for t in json.load(open(os.path.expanduser('~/.aglink/tasks.json')))]"

# 등록된 프로젝트
python3 -c "import json,os;d=json.load(open(os.path.expanduser('~/.aglink/store.json')));print(d.get('schemaVersion'),list((d.get('projects') or {}).keys()))"
```

---

## 10. 롤백

배포 스크립트는 이전 바이너리를 자동 보존하지 않는다. 교체 전에 직접 남길 것:

```bash
ssh <host> 'cp -p ~/bin/aglink ~/bin/aglink.$(date +%Y%m%d)'
```

데이터는 `~/.teleclaude` 에서 이전해 온 경우 legacy 디렉터리가 그대로 남아 있다.
되돌리려면 `~/.aglink` 를 치우고 구 바이너리로 기동하면 된다.
