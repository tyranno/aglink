# VS Code Claude 세션 붙기 구현 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 텔레그램/웹 대화에서 원격 리눅스의 VS Code Claude 세션 목록을 보고, 하나에 붙어 평문으로 주고받는다.

**Architecture:** 읽기는 전부 SSH 셸(프로세스 목록·기록파일 `tail -c`)로 하고 LLM 을 태우지 않는다. 쓰기와 세션 이름/busy 판정만 원격에서 일회용 `claude -p` 를 띄워 `SendMessage`/`ListAgents` 를 부른다. 새 SSH 코드는 만들지 않고 기존 `runSSH` 를 테스트 이음매를 통해 쓴다.

**Tech Stack:** Go (`host/` 모듈), 기존 `golang.org/x/crypto/ssh` 래퍼(`host/ssh.go`), 표준 `encoding/json`·`testing`.

**Spec:** [`docs/superpowers/specs/2026-09-18-vscode-claude-session-attach-design.md`](../specs/2026-09-18-vscode-claude-session-attach-design.md)

## Global Constraints

- **빌드·시험은 반드시** `GOFLAGS=-mod=readonly` 를 앞에 붙인다. 이 머신의 전역 `GOFLAGS` 에 `-mod=mod` 가 박혀 있어 `go.work` 아래에서 거부된다. 예: `GOFLAGS=-mod=readonly go test ./host/...`
- **이 저장소는 공개 GitHub 저장소다.** 호스트 주소·계정명·봇 식별자·비밀값을 코드·시험·문서 어디에도 적지 않는다. 시험에 쓰는 이름은 `dev`, `u1`, `proj-a` 같은 무의미한 문자열로 한다.
- **기록 파일(.jsonl)은 30 MB 를 넘는다.** 원격에서 읽을 때는 **반드시 `tail -c`** 로만 읽는다. `cat`·`head`·전체 읽기 금지.
- **읽기 경로에 LLM 을 태우지 않는다.** `claude -p` 는 `ListAgents`(이름·busy)와 `SendMessage`(보내기) 두 곳에서만 쓴다.
- **밖으로 나가는 세션 내용은 110 룬에서 자른다.** 바이트가 아니라 룬 기준 — 한글이 깨지면 안 된다.
- **평문 가로채기 지점은 둘이다.** `host/bot.go`(텔레그램)와 `host/chatcontrol.go`(웹). 한쪽만 고치면 웹에서 친 말이 세션이 아니라 워커로 샌다.
- **새 SSH 코드를 쓰지 않는다.** 전부 `runSSH(ctx, cfg, hostName, remoteCmd) (string, error)` 를 통한다.
- 기존 코드의 주석과 사용자 메시지는 한국어다. 새 코드도 같게 맞춘다.
- 커밋 메시지 끝에 `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>` 를 붙인다.
- 커밋 직전에 `git status` 를 다시 보고 **내가 만든 파일만** `git add` 한다. 이 워킹트리는 다른 세션이 동시에 만진다.

## File Structure

| 파일 | 책임 |
|---|---|
| `host/claudesession.go` (신규) | 자료형, 원격 출력 파싱, 원격 명령 문자열 조립, 두 출처 병합. 순수 함수 — 네트워크 없음. |
| `host/claudesession_test.go` (신규) | 위 파싱·조립 시험. |
| `host/claudesession_cmd.go` (신규) | 붙기 상태, `!sessions`/`!attach`/`!detach` 처리, 평문 라우팅, 턴 끝 감시. 네트워크는 `runSSHFn` 이음매로만. |
| `host/claudesession_cmd_test.go` (신규) | 가짜 `runSSHFn` 으로 핸들러·라우팅 시험. |
| `host/ssh.go` (수정) | `SSHHost` 에 `ClaudeSessions`/`ClaudeBin` 두 칸 추가, `var runSSHFn = runSSH` 이음매 추가. |
| `host/bot.go` (수정) | `Bot` 에 `attach` 추가, `handleCommand` 분기 셋, 평문 가로채기, `!status` 한 줄, `helpText()`. |
| `host/chatcontrol.go` (수정) | 웹 평문 가로채기 한 줄. |
| `host/README.md`, `host/config.example.txt` (수정) | 설정·사용법 문서. |

---

### Task 1: `ListAgents` 출력 파싱

원격 `claude -p` 가 뱉는 세션 목록을 자료형으로 바꾼다. 감싸는 LLM 이 앞뒤에 군말이나 코드펜스를 붙이므로 **알아볼 수 없는 줄은 전부 조용히 버린다.**

**Files:**
- Create: `host/claudesession.go`
- Test: `host/claudesession_test.go`

**Interfaces:**
- Consumes: 없음
- Produces: `type PeerSession struct { Name, Ref, Kind, Started string; Busy bool }` · `func parsePeerSessions(out string) []PeerSession`

- [ ] **Step 1: 실패하는 시험을 쓴다**

`host/claudesession_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

// 실제 원격에서 관측한 출력. 감싸는 LLM 이 코드펜스와 군말을 붙이므로 그대로
// 넣어 둔다 — 파서가 그것들을 버리는지가 이 시험의 핵심이다.
const sampleListAgents = "Raw output above, verbatim:\n" +
	"\n" +
	"```\n" +
	"Peer sessions (2):\n" +
	"  proj-a-cf [875607]  ·  interactive  ·  idle  ·  started 1h ago\n" +
	"  proj-b-4f [2265ec]  ·  interactive  ·  busy  ·  tmux sv:@0.%0  ·  started 1h ago\n" +
	"```\n"

func TestParsePeerSessions(t *testing.T) {
	got := parsePeerSessions(sampleListAgents)
	if len(got) != 2 {
		t.Fatalf("세션 2개를 기대했으나 %d개: %+v", len(got), got)
	}
	if got[0].Name != "proj-a-cf" || got[0].Ref != "875607" {
		t.Errorf("첫 세션 이름/참조가 어긋남: %+v", got[0])
	}
	if got[0].Kind != "interactive" || got[0].Busy {
		t.Errorf("첫 세션은 interactive·idle 이어야 함: %+v", got[0])
	}
	if got[0].Started != "1h ago" {
		t.Errorf("첫 세션 가동시간이 어긋남: %q", got[0].Started)
	}
	if !got[1].Busy {
		t.Errorf("둘째 세션은 busy 여야 함: %+v", got[1])
	}
}

func TestParsePeerSessions_Empty(t *testing.T) {
	for _, in := range []string{"", "Peer sessions (0):", "no sessions found", "오류가 났습니다"} {
		if got := parsePeerSessions(in); len(got) != 0 {
			t.Errorf("%q → 빈 목록이어야 하는데 %+v", in, got)
		}
	}
}

func TestParsePeerSessions_IgnoresJunkLines(t *testing.T) {
	in := "잡담 한 줄\n" +
		"  good-x [aaa111]  ·  interactive  ·  idle  ·  started 3m ago\n" +
		"  [빠진이름] 대괄호는 있지만 앞에 이름이 없다\n"
	got := parsePeerSessions(in)
	if len(got) != 1 || got[0].Name != "good-x" {
		t.Fatalf("알아볼 수 있는 한 줄만 남아야 함: %+v", got)
	}
}
```

- [ ] **Step 2: 시험이 실패하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run TestParsePeerSessions -v`
Expected: FAIL — `undefined: parsePeerSessions`

- [ ] **Step 3: 최소 구현을 쓴다**

`host/claudesession.go`:

```go
package main

import "strings"

// PeerSession is one Claude session the remote's own `claude -p` + ListAgents
// reported. It carries the two things the shell probe cannot know: the
// session's addressable NAME (what SendMessage's `to:` takes) and whether it
// is busy right now.
type PeerSession struct {
	Name    string // addressable name, e.g. "proj-a-cf"
	Ref     string // short ref shown in brackets
	Kind    string // "interactive" for a VS Code or terminal session
	Started string // human text, e.g. "1h ago"
	Busy    bool
}

// parsePeerSessions extracts sessions from ListAgents output. That output
// arrives wrapped in whatever the relaying `claude -p` chose to print around
// it — code fences, a preamble, a closing sentence — so anything that does not
// look like a session row is dropped without complaint. A row looks like:
//
//	proj-a-cf [875607]  ·  interactive  ·  idle  ·  started 1h ago
func parsePeerSessions(out string) []PeerSession {
	var sessions []PeerSession
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		open := strings.Index(line, "[")
		shut := strings.Index(line, "]")
		if open <= 0 || shut <= open {
			continue
		}
		name := strings.TrimSpace(line[:open])
		if name == "" || strings.ContainsAny(name, " \t") {
			continue
		}
		s := PeerSession{Name: name, Ref: strings.TrimSpace(line[open+1 : shut])}
		for _, f := range strings.Split(line[shut+1:], "·") {
			f = strings.TrimSpace(f)
			switch {
			case f == "busy":
				s.Busy = true
			case f == "idle":
				s.Busy = false
			case strings.HasPrefix(f, "started "):
				s.Started = strings.TrimSpace(strings.TrimPrefix(f, "started "))
			case f == "interactive" || f == "background":
				s.Kind = f
			}
		}
		sessions = append(sessions, s)
	}
	return sessions
}
```

- [ ] **Step 4: 시험이 통과하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run TestParsePeerSessions -v`
Expected: PASS (3개)

- [ ] **Step 5: 커밋한다**

```bash
git status --short
git add host/claudesession.go host/claudesession_test.go
git commit -m "feat(host): parse the remote's ListAgents output into sessions"
```

---

### Task 2: 셸 탐침 — pid·작업디렉터리·기록파일·시작시각

`ListAgents` 는 기록 파일이 어디 있는지 알려주지 않는다. 그건 셸로 직접 구한다 — 토큰이 들지 않고 결과가 결정적이다.

**Files:**
- Modify: `host/claudesession.go`
- Test: `host/claudesession_test.go`

**Interfaces:**
- Consumes: 없음
- Produces: `type remoteSession struct { PID int; Cwd, Transcript string; StartedAt time.Time }` · `func probeCmd() string` · `func parseProbe(out string) []remoteSession`

- [ ] **Step 1: 실패하는 시험을 쓴다**

`host/claudesession_test.go` 에 덧붙인다:

```go
func TestParseProbe(t *testing.T) {
	// 실제 원격 탐침 출력(경로는 일반화). 칸은 pid|cwd|기록파일|시작epoch.
	in := "10957|/home/u1/project/proj-a|/home/u1/.claude/projects/-home-u1-project-proj-a/aaa.jsonl|1789700022\n" +
		"11863|/home/u1/project/deep/proj-b|/home/u1/.claude/projects/-home-u1-project-deep-proj-b/bbb.jsonl|1789700105\n"
	got := parseProbe(in)
	if len(got) != 2 {
		t.Fatalf("2개를 기대했으나 %d개: %+v", len(got), got)
	}
	if got[0].PID != 10957 || got[0].Cwd != "/home/u1/project/proj-a" {
		t.Errorf("첫 항목이 어긋남: %+v", got[0])
	}
	if !strings.HasSuffix(got[0].Transcript, "aaa.jsonl") {
		t.Errorf("기록 파일 경로가 어긋남: %q", got[0].Transcript)
	}
	if got[0].StartedAt.Unix() != 1789700022 {
		t.Errorf("시작시각이 어긋남: %v", got[0].StartedAt)
	}
}

func TestParseProbe_SkipsBadLines(t *testing.T) {
	in := "\n" +
		"bash: 줄 1: 무슨 오류\n" +
		"notanumber|/a|/b|1\n" +
		"777|/home/u1/p|/home/u1/t.jsonl|\n" + // 시작시각 없음 — 받아들이되 zero
		"888|/home/u1/q\n" // 칸이 모자람 — 버린다
	got := parseProbe(in)
	if len(got) != 1 || got[0].PID != 777 {
		t.Fatalf("성한 줄 하나만 남아야 함: %+v", got)
	}
	if !got[0].StartedAt.IsZero() {
		t.Errorf("시작시각이 비면 zero time 이어야 함: %v", got[0].StartedAt)
	}
}

func TestProbeCmd_NeverReadsTheTranscript(t *testing.T) {
	cmd := probeCmd()
	// 기록 파일은 여기서 읽지 않는다 — 경로만 찾는다. 30MB 파일을 건드리면 버그다.
	for _, forbidden := range []string{"cat ", "head -c", "grep "} {
		if strings.Contains(cmd, forbidden) {
			t.Errorf("탐침이 기록 파일을 읽으려 한다: %q 가 들어 있음", forbidden)
		}
	}
	if !strings.Contains(cmd, "cc-socks") {
		t.Error("탐침이 세션 소켓 디렉터리를 보지 않는다")
	}
}
```

- [ ] **Step 2: 시험이 실패하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run 'TestParseProbe|TestProbeCmd' -v`
Expected: FAIL — `undefined: parseProbe`

- [ ] **Step 3: 최소 구현을 쓴다**

`host/claudesession.go` 의 import 를 `import ("fmt"; "strconv"; "strings"; "time")` 로 바꾸고 덧붙인다:

```go
// remoteSession is one live Claude process the shell probe found. Everything
// here is measured, not inferred: the pid owns the socket, the cwd comes from
// /proc, and the transcript is the newest .jsonl in the project directory that
// the cwd encodes.
type remoteSession struct {
	PID        int
	Cwd        string
	Transcript string
	StartedAt  time.Time
}

// probeCmd is the remote shell that lists live sessions. It costs no tokens and
// its output is deterministic, which is why the whole read path avoids an LLM.
// It only ever stats the transcript — reading a 30 MB file here would be a bug,
// and a test guards against it.
//
// Session sockets are 0600 under /run/user/<uid>, so this sees exactly the
// sessions the SSH account itself started. That boundary is intentional.
func probeCmd() string {
	return `for s in /run/user/$(id -u)/cc-socks/*.sock; do ` +
		`p=${s##*/}; p=${p%.sock}; ` +
		`[ -d /proc/$p ] || continue; ` +
		`cwd=$(readlink /proc/$p/cwd 2>/dev/null) || continue; ` +
		`[ -n "$cwd" ] || continue; ` +
		`enc=$(printf %s "$cwd" | sed "s#[/_.]#-#g"); ` +
		`f=$(ls -t "$HOME/.claude/projects/$enc"/*.jsonl 2>/dev/null | head -1); ` +
		`st=$(stat -c %Y /proc/$p 2>/dev/null); ` +
		`echo "$p|$cwd|$f|$st"; done`
}
// parseProbe turns probeCmd's output into sessions, dropping any line the shell
// or a login banner may have mixed in.
func parseProbe(out string) []remoteSession {
	var sessions []remoteSession
	for _, raw := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(raw), "|")
		if len(parts) < 4 {
			continue
		}
		pid, err := strconv.Atoi(parts[0])
		if err != nil || pid <= 0 {
			continue
		}
		s := remoteSession{PID: pid, Cwd: parts[1], Transcript: parts[2]}
		if secs, err := strconv.ParseInt(parts[3], 10, 64); err == nil && secs > 0 {
			s.StartedAt = time.Unix(secs, 0)
		}
		sessions = append(sessions, s)
	}
	return sessions
}
```

> `ls -t` 는 **최신이 먼저** 나오므로 `head -1` 이 맞다. 시험의 금지 목록이 `head -c` 만 막고 `head -1` 은 놔두는 것은 그래서다 — 막으려는 것은 "기록 파일을 읽는 것"이지 `head` 자체가 아니다.

- [ ] **Step 4: 시험이 통과하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run 'TestParseProbe|TestProbeCmd' -v`
Expected: PASS (3개)

- [ ] **Step 5: 커밋한다**

```bash
git status --short
git add host/claudesession.go host/claudesession_test.go
git commit -m "feat(host): probe live claude sessions over ssh without spending tokens"
```

---

### Task 3: 두 출처를 합친다

셸 탐침(기록파일·작업디렉터리)과 `ListAgents`(이름·busy)를 **작업디렉터리 이름**으로 잇는다. 세션 이름은 작업디렉터리 basename 에 짧은 꼬리를 붙여 만들어진다 — `…/proj-a` ↔ `proj-a-cf` 가 실측으로 확인됐다. 사용자가 `claude -n` 으로 직접 이름을 붙였으면 이 연결이 깨지는데, 그때는 **양쪽 다 따로 남긴다.**

**Files:**
- Modify: `host/claudesession.go`
- Test: `host/claudesession_test.go`

**Interfaces:**
- Consumes: Task 1 `PeerSession`, Task 2 `remoteSession`
- Produces: `type SessionInfo struct { Host, Name, Cwd, Transcript, Started, Last string; PID int; Busy, Addressable bool }` · `func mergeSessions(host string, probe []remoteSession, peers []PeerSession) []SessionInfo`

- [ ] **Step 1: 실패하는 시험을 쓴다**

```go
func TestMergeSessions_JoinsByBasename(t *testing.T) {
	probe := []remoteSession{
		{PID: 1, Cwd: "/home/u1/project/proj-a", Transcript: "/t/a.jsonl"},
		{PID: 2, Cwd: "/home/u1/deep/proj-b", Transcript: "/t/b.jsonl"},
	}
	peers := []PeerSession{
		{Name: "proj-b-4f", Busy: true, Started: "1h ago"},
		{Name: "proj-a-cf", Busy: false, Started: "2h ago"},
	}
	got := mergeSessions("dev", probe, peers)
	if len(got) != 2 {
		t.Fatalf("2개를 기대했으나 %d개: %+v", len(got), got)
	}
	if got[0].Name != "proj-a-cf" || got[0].Transcript != "/t/a.jsonl" {
		t.Errorf("proj-a 가 제 이름/기록과 이어지지 않음: %+v", got[0])
	}
	if !got[0].Addressable {
		t.Error("이름이 있으면 붙을 수 있어야 함")
	}
	if !got[1].Busy || got[1].Name != "proj-b-4f" {
		t.Errorf("proj-b 가 어긋남: %+v", got[1])
	}
	if got[0].Host != "dev" {
		t.Errorf("등록 호스트 이름이 실려야 함: %q", got[0].Host)
	}
}

func TestMergeSessions_UnnamedStillListed(t *testing.T) {
	// ListAgents 가 못 왔거나 사용자가 -n 으로 딴 이름을 붙인 경우.
	probe := []remoteSession{{PID: 9, Cwd: "/home/u1/project/lonely", Transcript: "/t/l.jsonl"}}
	got := mergeSessions("dev", probe, nil)
	if len(got) != 1 {
		t.Fatalf("이름이 없어도 목록에는 남아야 함: %+v", got)
	}
	if got[0].Addressable {
		t.Error("이름이 없으면 붙을 수 없다고 표시해야 함")
	}
	if got[0].Name != "lonely" {
		t.Errorf("이름이 없으면 작업디렉터리 이름을 보여야 함: %q", got[0].Name)
	}
}

func TestMergeSessions_PeerWithoutProbeIsKept(t *testing.T) {
	// 탐침이 못 본 세션도 ListAgents 에는 뜰 수 있다. 붙기는 이름만 있으면 된다.
	got := mergeSessions("dev", nil, []PeerSession{{Name: "ghost-11", Busy: true}})
	if len(got) != 1 || got[0].Transcript != "" || !got[0].Addressable {
		t.Fatalf("탐침에 없는 세션도 붙을 수 있게 남아야 함: %+v", got)
	}
}
```

- [ ] **Step 2: 시험이 실패하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run TestMergeSessions -v`
Expected: FAIL — `undefined: mergeSessions`

- [ ] **Step 3: 최소 구현을 쓴다**

`host/claudesession.go` 의 import 에 `path` 와 `sort` 를 더하고 덧붙인다:

```go
// SessionInfo is one row of what !sessions shows: the shell probe's facts and
// the LLM-sourced name/busy joined together.
//
// Addressable says whether SendMessage can reach it. A session the probe found
// but ListAgents did not name cannot be messaged — it is still listed, because
// hiding a session the user can see in their own editor is worse than showing
// one they cannot talk to.
type SessionInfo struct {
	Host        string // ssh.hosts registry name it was found on
	Name        string // addressable name, or the cwd's basename as a label
	Cwd         string
	Transcript  string
	Started     string
	Last        string // one line from the transcript tail; filled in by Task 5
	PID         int
	Busy        bool
	Addressable bool
}

// mergeSessions joins the shell probe to ListAgents by the session name's
// prefix. Names are generated from the working directory's basename with a
// short suffix ("…/proj-a" → "proj-a-cf"), which is what makes the join
// possible. A custom name set with `claude -n` breaks the join; both sides then
// survive as separate rows rather than one of them vanishing.
func mergeSessions(host string, probe []remoteSession, peers []PeerSession) []SessionInfo {
	used := make(map[int]bool, len(peers))
	out := make([]SessionInfo, 0, len(probe)+len(peers))

	for _, p := range probe {
		base := path.Base(p.Cwd)
		info := SessionInfo{
			Host: host, Name: base, Cwd: p.Cwd,
			Transcript: p.Transcript, PID: p.PID,
		}
		if !p.StartedAt.IsZero() {
			info.Started = humanSince(p.StartedAt)
		}
		for i, peer := range peers {
			if used[i] || !strings.HasPrefix(peer.Name, base+"-") {
				continue
			}
			used[i] = true
			info.Name = peer.Name
			info.Busy = peer.Busy
			info.Addressable = true
			if peer.Started != "" {
				info.Started = peer.Started
			}
			break
		}
		out = append(out, info)
	}

	for i, peer := range peers {
		if used[i] {
			continue
		}
		out = append(out, SessionInfo{
			Host: host, Name: peer.Name, Started: peer.Started,
			Busy: peer.Busy, Addressable: true,
		})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// humanSince renders an uptime the way the session list shows it.
func humanSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "방금"
	case d < time.Hour:
		return fmt.Sprintf("%d분째", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d시간째", int(d.Hours()))
	}
}
```

- [ ] **Step 4: 시험이 통과하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run TestMergeSessions -v`
Expected: PASS (3개)

- [ ] **Step 5: 커밋한다**

```bash
git status --short
git add host/claudesession.go host/claudesession_test.go
git commit -m "feat(host): join the shell probe to ListAgents by session name"
```

---

### Task 4: 기록 꼬리 읽기와 룬 단위 자르기

30 MB 짜리 기록 파일의 **끝 조각**만 받아, 마지막 어시스턴트 한 줄과 마지막 기록 시각을 뽑는다. 첫 줄은 중간에서 잘려 있으므로 버린다.

**Files:**
- Modify: `host/claudesession.go`
- Test: `host/claudesession_test.go`

**Interfaces:**
- Consumes: 없음
- Produces: `func tailCmd(transcript string, n int) string` · `func tailSummary(chunk []byte) (line string, last time.Time)` · `func truncRunes(s string, n int) string`

- [ ] **Step 1: 실패하는 시험을 쓴다**

```go
func TestTruncRunes_CutsOnRuneBoundary(t *testing.T) {
	s := strings.Repeat("가", 200)
	got := truncRunes(s, 110)
	if n := len([]rune(got)); n != 111 { // 110 + 말줄임표
		t.Fatalf("110 룬 + … 이어야 하는데 %d 룬: %q", n, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("잘렸으면 말줄임표가 붙어야 함: %q", got)
	}
	for _, r := range got {
		if r == 0xFFFD {
			t.Fatal("한글이 깨졌다")
		}
	}
	if short := truncRunes("짧다", 110); short != "짧다" {
		t.Errorf("한도 안이면 그대로여야 함: %q", short)
	}
}

func TestTailSummary(t *testing.T) {
	chunk := []byte(
		`{"type":"assis` + "\n" + // 잘린 첫 줄 — 버려야 한다
			`{"type":"user","timestamp":"2026-09-18T03:55:17.000Z","message":{"content":"뭐 하는 중?"}}` + "\n" +
			`{"type":"assistant","timestamp":"2026-09-18T03:55:43.000Z","message":{"content":[{"type":"text","text":"2단계도\n초록입니다."}]}}` + "\n" +
			`{"type":"assistant","timestamp":"2026-09-18T03:56:46.000Z","message":{"content":[{"type":"thinking","thinking":"속내"},{"type":"text","text":"담기를 지시했습니다."}]}}` + "\n")
	line, last := tailSummary(chunk)
	if line != "담기를 지시했습니다." {
		t.Errorf("마지막 어시스턴트 글이 어긋남: %q", line)
	}
	if last.UTC().Format("15:04:05") != "03:56:46" {
		t.Errorf("마지막 기록 시각이 어긋남: %v", last)
	}
}

func TestTailSummary_CollapsesNewlines(t *testing.T) {
	chunk := []byte("버림\n" +
		`{"type":"assistant","timestamp":"2026-09-18T03:55:43.000Z","message":{"content":[{"type":"text","text":"첫 줄\n\n둘째 줄"}]}}` + "\n")
	line, _ := tailSummary(chunk)
	if strings.Contains(line, "\n") {
		t.Errorf("여러 줄이 한 줄로 접혀야 함: %q", line)
	}
	if line != "첫 줄 둘째 줄" {
		t.Errorf("접힌 결과가 어긋남: %q", line)
	}
}

func TestTailSummary_NoAssistant(t *testing.T) {
	line, last := tailSummary([]byte("버림\n" +
		`{"type":"user","timestamp":"2026-09-18T03:55:17.000Z","message":{"content":"안녕"}}` + "\n"))
	if line != "" {
		t.Errorf("어시스턴트 글이 없으면 빈 문자열이어야 함: %q", line)
	}
	if last.IsZero() {
		t.Error("어시스턴트 글이 없어도 마지막 기록 시각은 나와야 함")
	}
}

func TestTailSummary_Empty(t *testing.T) {
	line, last := tailSummary(nil)
	if line != "" || !last.IsZero() {
		t.Errorf("빈 입력은 빈 결과여야 함: %q %v", line, last)
	}
}

func TestTailCmd_UsesTailC(t *testing.T) {
	cmd := tailCmd("/t/a.jsonl", 200000)
	if !strings.Contains(cmd, "tail -c 200000") {
		t.Errorf("tail -c 로 읽어야 함: %q", cmd)
	}
	if strings.Contains(cmd, "cat ") {
		t.Errorf("전체를 읽으려 한다: %q", cmd)
	}
}
```

- [ ] **Step 2: 시험이 실패하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run 'TestTruncRunes|TestTailSummary|TestTailCmd' -v`
Expected: FAIL — `undefined: truncRunes`

- [ ] **Step 3: 최소 구현을 쓴다**

`host/claudesession.go` 의 import 에 `encoding/json` 을 더하고 덧붙인다:

```go
// maxLastLine caps, in RUNES, how much of a session's own words leave the
// machine. A session's transcript can hold anything that was on screen — this
// morning it held a file of credentials — so the list shows a glance, not the
// content. Bytes would split a Korean character in half; runes do not.
const maxLastLine = 110

// truncRunes cuts s to n runes, appending an ellipsis when it had to cut.
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// tailCmd reads the LAST n bytes of a transcript. Transcripts run past 30 MB;
// anything that reads one whole is a bug, not a slow path.
func tailCmd(transcript string, n int) string {
	return fmt.Sprintf("tail -c %d %s 2>/dev/null", n, shellQuote(transcript))
}

// transcriptRec is the slice of a transcript record this code needs. The file
// holds far more per line; decoding only these fields keeps the parse cheap and
// stops a schema change elsewhere from breaking the read.
type transcriptRec struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// tailSummary reads a chunk taken from the END of a transcript and returns the
// last assistant text as a single line, plus the timestamp of the last record
// of any kind (which is how "has it gone quiet?" gets answered later).
//
// The first line is almost always cut mid-record, so it is dropped.
func tailSummary(chunk []byte) (string, time.Time) {
	lines := strings.Split(string(chunk), "\n")
	if len(lines) > 0 {
		lines = lines[1:]
	}
	var line string
	var last time.Time
	for _, raw := range lines {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var rec transcriptRec
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			continue
		}
		if ts, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
			last = ts
		}
		if rec.Type != "assistant" {
			continue
		}
		if text := recordText(rec.Message.Content); text != "" {
			line = text
		}
	}
	return collapseSpace(line), last
}

// recordText pulls the visible text out of a record's content, which is either
// a bare string or a list of blocks of which only "text" is shown to a reader.
func recordText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, " ")
}

// collapseSpace folds every run of whitespace into one space so a paragraph
// fits on the single line the list gives it.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
```

`shellQuote` 는 Task 5 에서 만든다. 이 단계에서는 `host/claudesession.go` 맨 아래에 임시로 다음을 넣어 컴파일을 통과시키고, Task 5 에서 제자리로 옮긴다:

```go
// shellQuote wraps s in single quotes so the remote shell takes it literally.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
```

- [ ] **Step 4: 시험이 통과하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run 'TestTruncRunes|TestTailSummary|TestTailCmd' -v`
Expected: PASS (6개)

- [ ] **Step 5: 커밋한다**

```bash
git status --short
git add host/claudesession.go host/claudesession_test.go
git commit -m "feat(host): read the tail of a session transcript, capped in runes"
```

---

### Task 5: 설정 스위치와 원격 명령 조립

어느 등록 호스트가 Claude 세션을 갖고 있는지 켜 주고, `ListAgents`/`SendMessage` 를 띄우는 원격 명령을 만든다. **보낼 본문은 base64 로 실어 보낸다** — 따옴표·줄바꿈·한글이 셸을 통과하며 다치지 않는 유일하게 확실한 방법이다.

**Files:**
- Modify: `host/ssh.go`, `host/config.example.txt`
- Modify: `host/claudesession.go`
- Test: `host/claudesession_test.go`

**Interfaces:**
- Consumes: 없음
- Produces: `SSHHost.ClaudeSessions bool` · `SSHHost.ClaudeBin string` · `func claudeBinOf(h SSHHost) string` · `func claudeListCmd(bin string) string` · `func claudeSendCmd(bin, target, text string) string`

- [ ] **Step 1: 실패하는 시험을 쓴다**

```go
func TestClaudeBinOf(t *testing.T) {
	if got := claudeBinOf(SSHHost{}); got != "claude" {
		t.Errorf("기본값은 claude 여야 함: %q", got)
	}
	if got := claudeBinOf(SSHHost{ClaudeBin: "/opt/claude"}); got != "/opt/claude" {
		t.Errorf("설정값이 이겨야 함: %q", got)
	}
}

func TestClaudeListCmd(t *testing.T) {
	cmd := claudeListCmd("claude")
	for _, want := range []string{"--safe-mode", "ListAgents", "-p "} {
		if !strings.Contains(cmd, want) {
			t.Errorf("%q 가 빠졌다: %s", want, cmd)
		}
	}
	// 읽기만 하는 호출이 쓰기 도구를 들고 있으면 안 된다.
	if strings.Contains(cmd, "SendMessage") {
		t.Errorf("목록 호출에 SendMessage 가 들어 있다: %s", cmd)
	}
}

func TestClaudeSendCmd_CarriesTextAsBase64(t *testing.T) {
	text := "따옴표 ' 와 \"둘\" 그리고\n줄바꿈이 든 한글"
	cmd := claudeSendCmd("claude", "proj-a-cf", text)

	// 본문은 셸에 날것으로 나타나면 안 된다 — base64 로만 실린다.
	if strings.Contains(cmd, "줄바꿈이 든 한글") {
		t.Errorf("본문이 셸 명령에 날것으로 들어갔다: %s", cmd)
	}
	enc := base64.StdEncoding.EncodeToString([]byte(text))
	if !strings.Contains(cmd, enc) {
		t.Errorf("base64 로 실린 본문을 찾을 수 없다: %s", cmd)
	}
	if !strings.Contains(cmd, "base64 -d") {
		t.Errorf("원격에서 되돌리는 부분이 없다: %s", cmd)
	}
	if !strings.Contains(cmd, "SendMessage") || !strings.Contains(cmd, "proj-a-cf") {
		t.Errorf("보내기 지시가 불완전하다: %s", cmd)
	}
	// 임시 파일은 반드시 지운다.
	if !strings.Contains(cmd, "rm -f") {
		t.Errorf("임시 파일을 지우지 않는다: %s", cmd)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Errorf("작은따옴표 탈출이 어긋남: %s", got)
	}
	if got := shellQuote("/t/a b.jsonl"); got != "'/t/a b.jsonl'" {
		t.Errorf("공백 있는 경로가 어긋남: %s", got)
	}
}
```

시험 파일 import 에 `"encoding/base64"` 를 추가한다.

- [ ] **Step 2: 시험이 실패하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run 'TestClaude|TestShellQuote' -v`
Expected: FAIL — `undefined: claudeBinOf`

- [ ] **Step 3: 최소 구현을 쓴다**

`host/ssh.go` 의 `SSHHost` 에 두 칸을 더한다:

```go
type SSHHost struct {
	Name     string `yaml:"name" json:"name"`         // registry key used by !ssh <name>
	Host     string `yaml:"host" json:"host"`         // hostname or IP
	Port     int    `yaml:"port" json:"port"`         // 0 → 22
	User     string `yaml:"user" json:"user"`         // login user
	Password string `yaml:"password" json:"password"` // used when KeyFile is empty
	KeyFile  string `yaml:"key_file" json:"key_file"` // private key path; preferred over Password

	// ClaudeSessions opts this host into !sessions. It is off by default
	// because listing costs a `claude -p` run on the remote account, which is
	// pointless on a host that runs no interactive sessions.
	ClaudeSessions bool `yaml:"claude_sessions,omitempty" json:"claude_sessions,omitempty"`
	// ClaudeBin overrides how the claude CLI is invoked there. Empty means
	// "claude", resolved through a login shell — a non-login SSH command does
	// not have ~/.local/bin on PATH.
	ClaudeBin string `yaml:"claude_bin,omitempty" json:"claude_bin,omitempty"`
}
```

`host/claudesession.go` 에서 Task 4 에 임시로 둔 `shellQuote` 를 그대로 두고(이제 제자리다) 덧붙인다 (import 에 `encoding/base64` 추가):

```go
// claudeBinOf resolves how to invoke the CLI on a host.
func claudeBinOf(h SSHHost) string {
	if b := strings.TrimSpace(h.ClaudeBin); b != "" {
		return b
	}
	return "claude"
}

// loginShell wraps a command so it runs with the account's own PATH. An SSH
// command runs a non-login shell, where ~/.local/bin — where the CLI usually
// lives — is not on PATH.
func loginShell(cmd string) string {
	return "bash -lc " + shellQuote(cmd)
}

// claudeListCmd asks the remote for its session list. --safe-mode skips the
// account's plugins and hooks so the run stays minimal, and the tool allowance
// is read-only on purpose: a listing must not be able to send anything.
func claudeListCmd(bin string) string {
	const prompt = "Call ListAgents once and print its raw output verbatim. Do nothing else."
	return loginShell(fmt.Sprintf(
		"cd /tmp && %s -p --safe-mode --allowedTools ListAgents --permission-mode acceptEdits %s",
		bin, shellQuote(prompt)))
}

// claudeSendCmd delivers text into a running session.
//
// The text travels as base64 and is decoded into a temp file on the remote,
// then read back by the CLI. Two reasons, both load-bearing: base64 is ASCII,
// so no quote, newline or Korean character can be mangled on the way through
// the shell; and handing the CLI a FILE rather than an inline prompt is what
// keeps it from paraphrasing the user's words, since the instruction is "send
// this file's contents", not "send this message".
func claudeSendCmd(bin, target, text string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(text))
	prompt := fmt.Sprintf(
		"Read the file $F. Send its exact contents to the peer session named %s "+
			"using SendMessage. Copy the text verbatim: do not translate it, "+
			"summarise it, reword it or add anything. Then print the tool result "+
			"and nothing else.", target)
	inner := fmt.Sprintf(
		"cd /tmp && F=$(mktemp) && printf %%s %s | base64 -d > \"$F\" && "+
			"%s -p --safe-mode --allowedTools Read,SendMessage --permission-mode acceptEdits %s; "+
			"rm -f \"$F\"",
		shellQuote(enc), bin, shellQuote(prompt))
	return loginShell(inner)
}
```

`host/config.example.txt` 의 ssh 절에 주석으로 두 칸을 적어 둔다:

```yaml
ssh:
  enabled: true
  hosts:
    - name: dev
      host: 10.0.0.2
      user: someone
      key_file: ~/.ssh/id_ed25519
      claude_sessions: true   # !sessions 가 이 호스트를 뒤진다
      # claude_bin: /opt/claude   # PATH 에 없을 때만
```

- [ ] **Step 4: 시험이 통과하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run 'TestClaude|TestShellQuote' -v`
Expected: PASS (4개)

그리고 설정 왕복이 깨지지 않았는지: `GOFLAGS=-mod=readonly go test ./host/ -run 'TestConfig|TestYaml' -v` → PASS

- [ ] **Step 5: 커밋한다**

```bash
git status --short
git add host/ssh.go host/claudesession.go host/claudesession_test.go host/config.example.txt
git commit -m "feat(host): build the remote list/send commands, text carried as base64"
```

---

### Task 6: 붙기 상태

대화마다 세션 하나. **메모리에만** 둔다 — 원격 세션은 호스트보다 먼저 죽는 쪽이라, 살아 있지도 않은 이름에 붙어 있다고 적힌 상태가 더 나쁘다.

**Files:**
- Create: `host/claudesession_cmd.go`
- Test: `host/claudesession_cmd_test.go`

**Interfaces:**
- Consumes: Task 3 `SessionInfo`
- Produces: `type attachState struct{…}` · `func newAttachState() *attachState` · `(*attachState) Attach(lane string, s SessionInfo)` · `Detach(lane string) bool` · `Current(lane string) (SessionInfo, bool)` · `Remember(lane string, list []SessionInfo)` · `Recall(lane string, n int) (SessionInfo, bool)`

- [ ] **Step 1: 실패하는 시험을 쓴다**

`host/claudesession_cmd_test.go`:

```go
package main

import (
	"strings"
	"sync"
	"testing"
)

func TestAttachState_AttachDetach(t *testing.T) {
	st := newAttachState()
	if _, ok := st.Current("telegram"); ok {
		t.Fatal("처음에는 붙은 것이 없어야 함")
	}
	st.Attach("telegram", SessionInfo{Host: "dev", Name: "proj-a-cf"})
	got, ok := st.Current("telegram")
	if !ok || got.Name != "proj-a-cf" {
		t.Fatalf("붙은 세션이 어긋남: %+v %v", got, ok)
	}
	if _, ok := st.Current("web:7"); ok {
		t.Error("다른 대화까지 붙으면 안 됨")
	}
	if !st.Detach("telegram") {
		t.Error("붙어 있었으면 Detach 가 true 여야 함")
	}
	if st.Detach("telegram") {
		t.Error("이미 풀린 것을 또 풀면 false 여야 함")
	}
}

func TestAttachState_RecallByNumber(t *testing.T) {
	st := newAttachState()
	st.Remember("telegram", []SessionInfo{
		{Name: "proj-a-cf", Addressable: true},
		{Name: "proj-b-4f", Addressable: true},
	})
	got, ok := st.Recall("telegram", 2)
	if !ok || got.Name != "proj-b-4f" {
		t.Fatalf("2번이 어긋남: %+v %v", got, ok)
	}
	if _, ok := st.Recall("telegram", 0); ok {
		t.Error("0번은 없어야 함")
	}
	if _, ok := st.Recall("telegram", 3); ok {
		t.Error("범위 밖은 없어야 함")
	}
	if _, ok := st.Recall("web:7", 1); ok {
		t.Error("목록을 본 적 없는 대화에서는 번호가 듣지 않아야 함")
	}
}

func TestAttachState_ConcurrentUse(t *testing.T) {
	st := newAttachState()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st.Attach("telegram", SessionInfo{Name: "x"})
			st.Remember("telegram", []SessionInfo{{Name: "y"}})
			st.Current("telegram")
			st.Recall("telegram", 1)
			st.Detach("telegram")
		}(i)
	}
	wg.Wait() // -race 에서 걸리지 않으면 통과
}
```

- [ ] **Step 2: 시험이 실패하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -race -run TestAttachState -v`
Expected: FAIL — `undefined: newAttachState`

- [ ] **Step 3: 최소 구현을 쓴다**

`host/claudesession_cmd.go`:

```go
package main

import "sync"

// attachState remembers, per conversation lane, which remote Claude session
// that conversation is talking to — and the last list it was shown, so "2" can
// mean something.
//
// It lives in memory only. A remote session dies well before this host does,
// and a persisted binding to a session that is no longer there is worse than
// no binding: the user would keep typing into nothing. A restart forgets, and
// !status always shows the truth.
type attachState struct {
	mu       sync.Mutex
	attached map[string]SessionInfo   // laneKey → session
	listed   map[string][]SessionInfo // laneKey → the last list shown there
}

func newAttachState() *attachState {
	return &attachState{
		attached: make(map[string]SessionInfo),
		listed:   make(map[string][]SessionInfo),
	}
}

// Attach binds a lane to a session, replacing whatever it had.
func (a *attachState) Attach(lane string, s SessionInfo) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.attached[lane] = s
}

// Detach clears a lane's binding and reports whether there was one.
func (a *attachState) Detach(lane string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, had := a.attached[lane]
	delete(a.attached, lane)
	return had
}

// Current returns the lane's session, if it has one.
func (a *attachState) Current(lane string) (SessionInfo, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.attached[lane]
	return s, ok
}

// Remember stores the list a lane was just shown so its numbering stays valid.
func (a *attachState) Remember(lane string, list []SessionInfo) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.listed[lane] = append([]SessionInfo(nil), list...)
}

// Recall resolves a 1-based number from the lane's last list.
func (a *attachState) Recall(lane string, n int) (SessionInfo, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	list := a.listed[lane]
	if n < 1 || n > len(list) {
		return SessionInfo{}, false
	}
	return list[n-1], true
}
```

- [ ] **Step 4: 시험이 통과하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -race -run TestAttachState -v`
Expected: PASS (3개)

- [ ] **Step 5: 커밋한다**

```bash
git status --short
git add host/claudesession_cmd.go host/claudesession_cmd_test.go
git commit -m "feat(host): keep the attached session per conversation, in memory only"
```

---

### Task 7: `!sessions` — 원격을 훑어 목록을 만든다

이제 실제로 원격에 나간다. 네트워크는 `runSSHFn` 이음매 하나로만 건드려 시험에서 가짜로 갈아끼운다.

**Files:**
- Modify: `host/ssh.go` (이음매), `host/claudesession_cmd.go`
- Test: `host/claudesession_cmd_test.go`

**Interfaces:**
- Consumes: Task 1~5 전부, Task 6 `attachState`
- Produces: `var runSSHFn = runSSH` · `func collectSessions(ctx context.Context, cfg *Config) ([]SessionInfo, []string)` · `func formatSessionList(list []SessionInfo) string`

- [ ] **Step 1: 실패하는 시험을 쓴다**

```go
// fakeSSH swaps the one seam every remote call goes through.
func fakeSSH(t *testing.T, fn func(host, cmd string) (string, error)) {
	t.Helper()
	orig := runSSHFn
	runSSHFn = func(ctx context.Context, cfg *Config, host, cmd string) (string, error) {
		return fn(host, cmd)
	}
	t.Cleanup(func() { runSSHFn = orig })
}

func testCfgWithHost() *Config {
	return &Config{
		SSHEnabled: true,
		SSHHosts: []SSHHost{
			{Name: "dev", Host: "h", User: "u", Password: "p", ClaudeSessions: true},
			{Name: "other", Host: "h2", User: "u", Password: "p"}, // 옵트인 안 함
		},
	}
}

func TestCollectSessions(t *testing.T) {
	var sawOther bool
	fakeSSH(t, func(host, cmd string) (string, error) {
		if host != "dev" {
			sawOther = true
		}
		switch {
		case strings.Contains(cmd, "cc-socks"):
			return "10957|/home/u1/project/proj-a|/t/a.jsonl|1789700022\n", nil
		case strings.Contains(cmd, "ListAgents"):
			return "  proj-a-cf [875607]  ·  interactive  ·  idle  ·  started 1h ago\n", nil
		case strings.Contains(cmd, "tail -c"):
			return "버림\n" +
				`{"type":"assistant","timestamp":"2026-09-18T03:56:46.000Z","message":{"content":[{"type":"text","text":"담기를 지시했습니다."}]}}` + "\n", nil
		}
		return "", nil
	})

	list, errs := collectSessions(context.Background(), testCfgWithHost())
	if len(errs) != 0 {
		t.Fatalf("오류가 없어야 함: %v", errs)
	}
	if sawOther {
		t.Error("claude_sessions 를 켜지 않은 호스트를 건드렸다")
	}
	if len(list) != 1 {
		t.Fatalf("1개를 기대했으나 %d개: %+v", len(list), list)
	}
	if list[0].Name != "proj-a-cf" || !list[0].Addressable {
		t.Errorf("이름/붙기 가능 여부가 어긋남: %+v", list[0])
	}
	if list[0].Last != "담기를 지시했습니다." {
		t.Errorf("마지막 한 줄이 어긋남: %q", list[0].Last)
	}
}

func TestCollectSessions_HostFailureIsReportedNotFatal(t *testing.T) {
	fakeSSH(t, func(host, cmd string) (string, error) {
		return "", errors.New("연결 거부")
	})
	list, errs := collectSessions(context.Background(), testCfgWithHost())
	if len(list) != 0 {
		t.Errorf("목록이 비어야 함: %+v", list)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "dev") {
		t.Errorf("어느 호스트가 실패했는지 알려야 함: %v", errs)
	}
}

func TestCollectSessions_NoOptedInHost(t *testing.T) {
	cfg := &Config{SSHEnabled: true, SSHHosts: []SSHHost{{Name: "other"}}}
	list, errs := collectSessions(context.Background(), cfg)
	if len(list) != 0 || len(errs) != 0 {
		t.Errorf("켠 호스트가 없으면 조용해야 함: %+v %v", list, errs)
	}
}

func TestFormatSessionList_TruncatesAndNumbers(t *testing.T) {
	long := strings.Repeat("가", 300)
	out := formatSessionList([]SessionInfo{
		{Name: "proj-a-cf", Host: "dev", Started: "1시간째", Last: long, Addressable: true},
		{Name: "proj-b-4f", Host: "dev", Started: "2시간째", Busy: true, Addressable: true},
		{Name: "lonely", Host: "dev", Addressable: false},
	})
	if !strings.Contains(out, "1. proj-a-cf") || !strings.Contains(out, "2. proj-b-4f") {
		t.Errorf("번호가 붙어야 함:\n%s", out)
	}
	if !strings.Contains(out, "일하는 중") || !strings.Contains(out, "쉬는 중") {
		t.Errorf("busy/idle 이 보여야 함:\n%s", out)
	}
	if !strings.Contains(out, "붙을 수 없음") {
		t.Errorf("이름을 못 읽은 세션은 그렇게 표시해야 함:\n%s", out)
	}
	if strings.Contains(out, strings.Repeat("가", 120)) {
		t.Errorf("마지막 줄이 잘리지 않았다:\n%s", out)
	}
}

func TestFormatSessionList_Empty(t *testing.T) {
	if out := formatSessionList(nil); !strings.Contains(out, "세션이 없습니다") {
		t.Errorf("빈 목록 문구가 어긋남: %q", out)
	}
}
```

시험 파일 import 에 `"context"`, `"errors"` 를 추가한다.

- [ ] **Step 2: 시험이 실패하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run 'TestCollectSessions|TestFormatSessionList' -v`
Expected: FAIL — `undefined: runSSHFn`

- [ ] **Step 3: 최소 구현을 쓴다**

`host/ssh.go` 의 `runSSH` 정의 바로 아래에 이음매를 둔다:

```go
// runSSHFn is the single seam every remote call in the session-attach feature
// goes through, so tests can answer without a network. Production leaves it
// pointing at runSSH.
var runSSHFn = runSSH
```

`host/claudesession_cmd.go` 에 덧붙인다 (import 에 `context`, `fmt`, `strings`, `time` 추가):

```go
// tailBytes is how much of a transcript's end gets pulled for the one-line
// summary. Large enough to contain a few whole records, small enough that a
// listing over a slow link stays quick.
const tailBytes = 200000

// collectSessions asks every opted-in host what it is running. Failures are
// returned per host rather than aborting: one unreachable machine must not
// hide the sessions on a machine that is up.
func collectSessions(ctx context.Context, cfg *Config) ([]SessionInfo, []string) {
	var all []SessionInfo
	var errs []string
	if cfg == nil || !cfg.SSHEnabled {
		return nil, nil
	}
	for _, h := range cfg.SSHHosts {
		if !h.ClaudeSessions {
			continue
		}
		probeOut, err := runSSHFn(ctx, cfg, h.Name, probeCmd())
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", h.Name, err))
			continue
		}
		probe := parseProbe(probeOut)

		// The name/busy lookup is the only part that costs tokens, and it is
		// the only part allowed to fail quietly: without it sessions still
		// list, they just cannot be attached to.
		var peers []PeerSession
		if listOut, err := runSSHFn(ctx, cfg, h.Name, claudeListCmd(claudeBinOf(h))); err == nil {
			peers = parsePeerSessions(listOut)
		}

		sessions := mergeSessions(h.Name, probe, peers)
		for i := range sessions {
			if sessions[i].Transcript == "" {
				continue
			}
			out, err := runSSHFn(ctx, cfg, h.Name, tailCmd(sessions[i].Transcript, tailBytes))
			if err != nil {
				continue
			}
			line, _ := tailSummary([]byte(out))
			sessions[i].Last = truncRunes(line, maxLastLine)
		}
		all = append(all, sessions...)
	}
	return all, errs
}

// formatSessionList renders the list the user picks from. The numbering is what
// makes "이름을 모를 때" workable: it is valid until the next listing.
func formatSessionList(list []SessionInfo) string {
	if len(list) == 0 {
		return "붙을 수 있는 세션이 없습니다."
	}
	var sb strings.Builder
	for i, s := range list {
		state := "쉬는 중"
		if s.Busy {
			state = "일하는 중"
		}
		if !s.Addressable {
			state = "붙을 수 없음(이름을 못 읽음)"
		}
		started := s.Started
		if started == "" {
			started = "?"
		}
		sb.WriteString(fmt.Sprintf("%d. %s  %s  %s · %s\n", i+1, s.Name, s.Host, started, state))
		if s.Last != "" {
			sb.WriteString("   마지막: " + s.Last + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
```

- [ ] **Step 4: 시험이 통과하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run 'TestCollectSessions|TestFormatSessionList' -v`
Expected: PASS (5개)

- [ ] **Step 5: 커밋한다**

```bash
git status --short
git add host/ssh.go host/claudesession_cmd.go host/claudesession_cmd_test.go
git commit -m "feat(host): collect and format the remote session list"
```

---

### Task 8: `!sessions` · `!attach` · `!detach` · `!status`

명령을 봇에 붙인다.

**Files:**
- Modify: `host/bot.go`, `host/claudesession_cmd.go`
- Test: `host/claudesession_cmd_test.go`

**Interfaces:**
- Consumes: Task 6 `attachState`, Task 7 `collectSessions`/`formatSessionList`
- Produces: `Bot.attach *attachState` · `(*Bot) handleSessions(reply replySender, chatID int64, lane string)` · `(*Bot) handleAttach(reply replySender, chatID int64, lane string, fields []string)` · `(*Bot) handleDetach(reply replySender, chatID int64, lane string)` · `func resolveTarget(st *attachState, lane, token string, list []SessionInfo) (SessionInfo, string)`

- [ ] **Step 1: 실패하는 시험을 쓴다**

```go
func TestResolveTarget(t *testing.T) {
	st := newAttachState()
	list := []SessionInfo{
		{Name: "proj-a-cf", Addressable: true},
		{Name: "proj-b-4f", Addressable: true},
		{Name: "lonely", Addressable: false},
	}
	st.Remember("telegram", list)

	if got, why := resolveTarget(st, "telegram", "2", list); why != "" || got.Name != "proj-b-4f" {
		t.Errorf("번호로 고르기 실패: %+v %q", got, why)
	}
	if got, why := resolveTarget(st, "telegram", "proj-a-cf", list); why != "" || got.Name != "proj-a-cf" {
		t.Errorf("이름으로 고르기 실패: %+v %q", got, why)
	}
	if got, why := resolveTarget(st, "telegram", "proj-a", list); why != "" || got.Name != "proj-a-cf" {
		t.Errorf("앞부분만 대도 골라야 함: %+v %q", got, why)
	}
	if _, why := resolveTarget(st, "telegram", "proj", list); why == "" {
		t.Error("여러 개에 걸리면 되물어야 함")
	}
	if _, why := resolveTarget(st, "telegram", "없는이름", list); why == "" {
		t.Error("없는 이름은 거절해야 함")
	}
	if _, why := resolveTarget(st, "telegram", "9", list); why == "" {
		t.Error("범위 밖 번호는 거절해야 함")
	}
	if _, why := resolveTarget(st, "telegram", "lonely", list); why == "" {
		t.Error("이름을 못 읽은 세션에는 붙을 수 없어야 함")
	}
}

func TestHandleAttachDetach(t *testing.T) {
	fakeSSH(t, func(host, cmd string) (string, error) {
		switch {
		case strings.Contains(cmd, "cc-socks"):
			return "1|/home/u1/project/proj-a|/t/a.jsonl|1789700022\n", nil
		case strings.Contains(cmd, "ListAgents"):
			return "  proj-a-cf [aaa]  ·  interactive  ·  idle  ·  started 1h ago\n", nil
		}
		return "", nil
	})
	b := newTestBotForAttach(testCfgWithHost())
	rec := &recordingReply{}

	b.handleAttach(rec, 7, "telegram", []string{"!attach", "proj-a-cf"})
	if _, ok := b.attach.Current("telegram"); !ok {
		t.Fatalf("붙지 않았다: %v", rec.sent)
	}
	if !strings.Contains(strings.Join(rec.sent, "\n"), "proj-a-cf") {
		t.Errorf("무엇에 붙었는지 알려야 함: %v", rec.sent)
	}

	rec.sent = nil
	b.handleDetach(rec, 7, "telegram")
	if _, ok := b.attach.Current("telegram"); ok {
		t.Error("풀리지 않았다")
	}
	if len(rec.sent) == 0 {
		t.Error("풀렸다고 알려야 함")
	}
}

func TestHandleAttach_UnknownName(t *testing.T) {
	fakeSSH(t, func(host, cmd string) (string, error) { return "", nil })
	b := newTestBotForAttach(testCfgWithHost())
	rec := &recordingReply{}
	b.handleAttach(rec, 7, "telegram", []string{"!attach", "없는것"})
	if _, ok := b.attach.Current("telegram"); ok {
		t.Error("없는 세션에 붙으면 안 됨")
	}
	if len(rec.sent) == 0 {
		t.Error("왜 못 붙는지 알려야 함")
	}
}
```

시험 도우미도 같은 파일에 넣는다:

```go
// recordingReply captures what a handler would have sent.
type recordingReply struct{ sent []string }

func (r *recordingReply) Send(chatID int64, text string) error {
	r.sent = append(r.sent, text)
	return nil
}

// newTestBotForAttach builds the smallest Bot the session handlers need: a
// config holder and the attach state. No telegram API, no manager.
func newTestBotForAttach(cfg *Config) *Bot {
	return &Bot{cfgh: NewConfigHolder(cfg), attach: newAttachState()}
}
```

> `replySender` 인터페이스의 실제 메서드 집합을 `host/bot.go` 에서 확인하고 `recordingReply` 를 거기에 맞춘다. `Send` 외에 다른 메서드가 있으면 빈 구현을 더한다. `NewConfigHolder` 의 실제 이름·시그니처도 `host/confighold.go` 에서 확인해 맞춘다.

- [ ] **Step 2: 시험이 실패하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run 'TestResolveTarget|TestHandleAttach|TestHandleDetach' -v`
Expected: FAIL — `undefined: resolveTarget`

- [ ] **Step 3: 최소 구현을 쓴다**

`host/bot.go` 의 `Bot` 구조체에 한 칸을 더한다(`out *Hub` 아래):

```go
	attach *attachState // laneKey → the remote Claude session this conversation talks to
```

`NewBot` 의 반환 리터럴에 `attach: newAttachState(),` 를 더한다.

`handleCommand` 의 `case "!ssh":` 아래에 셋을 더한다:

```go
	case "!sessions":
		b.handleSessions(reply, chatID, laneKeyOf(tgt))
	case "!attach":
		b.handleAttach(reply, chatID, laneKeyOf(tgt), fields)
	case "!detach":
		b.handleDetach(reply, chatID, laneKeyOf(tgt))
```

`case "!status":` 의 `msg += "\n🔧 백엔드: "…` 바로 앞에 한 줄을 더한다:

```go
		if s, ok := b.attach.Current(laneKeyOf(tgt)); ok {
			msg += fmt.Sprintf("\n🔗 붙은 세션: %s (%s)", s.Name, s.Host)
		}
```

`host/claudesession_cmd.go` 에 덧붙인다:

```go
// sessionTimeout bounds one !sessions sweep. A listing runs a `claude -p` on
// each host, which is slower than a plain command but not slow.
const sessionTimeout = 90 * time.Second

// handleSessions shows what can be attached to.
func (b *Bot) handleSessions(reply replySender, chatID int64, lane string) {
	ctx, cancel := context.WithTimeout(context.Background(), sessionTimeout)
	defer cancel()
	list, errs := collectSessions(ctx, b.cfg())
	b.attach.Remember(lane, list)
	msg := formatSessionList(list)
	if len(errs) > 0 {
		msg += "\n⚠️ " + strings.Join(errs, " / ")
	}
	_ = reply.Send(chatID, msg)
}

// handleAttach binds this conversation to one session. It re-lists first so a
// name that died since the last listing is caught here rather than on the next
// message the user types.
func (b *Bot) handleAttach(reply replySender, chatID int64, lane string, fields []string) {
	if len(fields) < 2 {
		_ = reply.Send(chatID, "사용법: !attach <번호|이름>  (먼저 !sessions 로 목록을 보세요)")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionTimeout)
	defer cancel()
	list, _ := collectSessions(ctx, b.cfg())
	b.attach.Remember(lane, list)

	target, why := resolveTarget(b.attach, lane, strings.Join(fields[1:], " "), list)
	if why != "" {
		_ = reply.Send(chatID, why)
		return
	}
	b.attach.Attach(lane, target)
	_ = reply.Send(chatID, fmt.Sprintf("🔗 %s (%s) 에 붙었습니다. 이제 그냥 말하면 그 세션으로 갑니다. 풀려면 !detach", target.Name, target.Host))
}

// handleDetach unbinds this conversation.
func (b *Bot) handleDetach(reply replySender, chatID int64, lane string) {
	if b.attach.Detach(lane) {
		_ = reply.Send(chatID, "🔓 세션에서 풀었습니다. 이제 평문은 원래대로 처리됩니다.")
		return
	}
	_ = reply.Send(chatID, "붙어 있는 세션이 없습니다.")
}

// resolveTarget turns what the user typed into a session, or into the reason it
// could not. A bare number means "the Nth row of the list you were just shown";
// anything else is matched against names, exactly first and then by prefix.
func resolveTarget(st *attachState, lane, token string, list []SessionInfo) (SessionInfo, string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return SessionInfo{}, "어느 세션인지 말씀해 주세요. !sessions 로 목록을 볼 수 있습니다."
	}
	if n, err := strconv.Atoi(token); err == nil {
		s, ok := st.Recall(lane, n)
		if !ok {
			return SessionInfo{}, fmt.Sprintf("%d번은 목록에 없습니다. !sessions 로 다시 보세요.", n)
		}
		if !s.Addressable {
			return SessionInfo{}, fmt.Sprintf("%s 는 이름을 읽지 못해 붙을 수 없습니다.", s.Name)
		}
		return s, ""
	}
	var hits []SessionInfo
	for _, s := range list {
		if s.Name == token {
			if !s.Addressable {
				return SessionInfo{}, fmt.Sprintf("%s 는 이름을 읽지 못해 붙을 수 없습니다.", s.Name)
			}
			return s, ""
		}
		if strings.HasPrefix(s.Name, token) {
			hits = append(hits, s)
		}
	}
	switch len(hits) {
	case 0:
		return SessionInfo{}, fmt.Sprintf("%q 라는 세션이 없습니다. !sessions 로 목록을 보세요.", token)
	case 1:
		if !hits[0].Addressable {
			return SessionInfo{}, fmt.Sprintf("%s 는 이름을 읽지 못해 붙을 수 없습니다.", hits[0].Name)
		}
		return hits[0], ""
	default:
		var names []string
		for _, s := range hits {
			names = append(names, s.Name)
		}
		return SessionInfo{}, fmt.Sprintf("%q 에 걸리는 세션이 여럿입니다: %s", token, strings.Join(names, ", "))
	}
}
```

import 에 `strconv` 를 추가한다.

- [ ] **Step 4: 시험이 통과하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run 'TestResolveTarget|TestHandleAttach|TestHandleDetach' -v`
Expected: PASS (3개)

그리고 기존 명령이 깨지지 않았는지: `GOFLAGS=-mod=readonly go test ./host/ -run 'TestBot|TestStatus|TestChatCmd' -v` → PASS

- [ ] **Step 5: 커밋한다**

```bash
git status --short
git add host/bot.go host/claudesession_cmd.go host/claudesession_cmd_test.go
git commit -m "feat(host): add !sessions, !attach and !detach"
```

---

### Task 9: 평문을 붙은 세션으로 보낸다 — 진입점 **둘 다**

**Files:**
- Modify: `host/bot.go`, `host/chatcontrol.go`, `host/claudesession_cmd.go`
- Test: `host/claudesession_cmd_test.go`

**Interfaces:**
- Consumes: Task 5 `claudeSendCmd`, Task 6 `attachState`
- Produces: `(*Bot) routeToSession(chatID int64, text string, tgt Target) bool`

- [ ] **Step 1: 실패하는 시험을 쓴다**

```go
func TestRouteToSession_NotAttachedReturnsFalse(t *testing.T) {
	b := newTestBotForAttach(testCfgWithHost())
	if b.routeToSession(7, "안녕", TelegramTarget()) {
		t.Error("붙지 않았으면 가로채면 안 됨 — 평문은 워커로 가야 한다")
	}
}

func TestRouteToSession_SendsVerbatim(t *testing.T) {
	var sentCmd string
	fakeSSH(t, func(host, cmd string) (string, error) {
		if strings.Contains(cmd, "SendMessage") {
			sentCmd = cmd
			return `{"success":true,"msg_id":"abc"}`, nil
		}
		return "", nil
	})
	b := newTestBotForAttach(testCfgWithHost())
	b.attach.Attach("telegram", SessionInfo{Host: "dev", Name: "proj-a-cf", Addressable: true})

	text := "2단계 끝났으면 알려줘"
	if !b.routeToSession(7, text, TelegramTarget()) {
		t.Fatal("붙어 있으면 가로채야 함")
	}
	if sentCmd == "" {
		t.Fatal("보내기 명령이 나가지 않았다")
	}
	enc := base64.StdEncoding.EncodeToString([]byte(text))
	if strings.Contains(sentCmd, enc) {
		t.Error("출처 한 줄이 붙지 않았다 — 본문이 사용자가 친 것과 정확히 같다")
	}
	// 본문은 "출처 한 줄 + 사용자가 친 것" 이고, 그 전체가 base64 로 실린다.
	decoded := decodeSentBody(t, sentCmd)
	if !strings.HasSuffix(decoded, text) {
		t.Errorf("사용자가 친 말이 끝에 그대로 있어야 함: %q", decoded)
	}
	if !strings.Contains(decoded, "사용자") {
		t.Errorf("받는 쪽이 사람 말로 받게 하는 출처 줄이 없다: %q", decoded)
	}
	if !strings.Contains(sentCmd, "proj-a-cf") {
		t.Errorf("어느 세션으로 보내는지가 빠졌다: %s", sentCmd)
	}
}

// decodeSentBody pulls the base64 payload back out of the shell command so a
// test can assert on what the remote session will actually see.
func decodeSentBody(t *testing.T, cmd string) string {
	t.Helper()
	re := regexp.MustCompile(`[A-Za-z0-9+/]{16,}={0,2}`)
	for _, m := range re.FindAllString(cmd, -1) {
		if b, err := base64.StdEncoding.DecodeString(m); err == nil && utf8.Valid(b) {
			return string(b)
		}
	}
	t.Fatalf("base64 본문을 찾지 못했다: %s", cmd)
	return ""
}

func TestRouteToSession_WebLaneIsSeparate(t *testing.T) {
	fakeSSH(t, func(host, cmd string) (string, error) { return `{"success":true}`, nil })
	b := newTestBotForAttach(testCfgWithHost())
	b.attach.Attach("telegram", SessionInfo{Host: "dev", Name: "proj-a-cf", Addressable: true})

	web := Target{Kind: "web", ID: "7"}
	if b.routeToSession(7, "안녕", web) {
		t.Error("텔레그램에서 붙었다고 웹 대화까지 가로채면 안 됨")
	}
	b.attach.Attach(laneKeyOf(web), SessionInfo{Host: "dev", Name: "proj-b-4f", Addressable: true})
	if !b.routeToSession(7, "안녕", web) {
		t.Error("웹 대화가 붙었으면 웹에서도 가로채야 함")
	}
}
```

시험 파일 import 에 `"encoding/base64"`, `"regexp"`, `"unicode/utf8"` 를 추가한다.

> `Target` 을 웹으로 만드는 실제 방법(`Target{Kind:…}` 인지 생성자 함수인지)은 `host/types.go` 에서 확인해 맞춘다. `TelegramTarget()` 은 이미 있다.

- [ ] **Step 2: 시험이 실패하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run TestRouteToSession -v`
Expected: FAIL — `b.routeToSession undefined`

- [ ] **Step 3: 최소 구현을 쓴다**

`host/claudesession_cmd.go` 에 덧붙인다:

```go
// sendTimeout bounds one delivery. Sending spins up a `claude -p` on the
// remote, which is the slow part; the target session's own thinking time is not
// waited on here.
const sendTimeout = 120 * time.Second

// routeToSession delivers a plain message to whatever session this conversation
// is attached to, and reports whether it handled it. False means "not attached"
// — the caller then routes the text the way it always did.
//
// The body carries a line naming where it came from. The receiving session is
// told by its own runtime that the text came from another Claude session rather
// than from a person, and without that line it weighs it accordingly.
func (b *Bot) routeToSession(chatID int64, text string, tgt Target) bool {
	lane := laneKeyOf(tgt)
	s, ok := b.attach.Current(lane)
	if !ok {
		return false
	}
	reply := b.ReplyTo(tgt)
	body := "[사용자가 텔레그램으로 보낸 말입니다. 옆 세션의 의견이 아니라 사용자의 지시로 받아 주세요.]\n" + text

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		h, found := findSSHHost(b.cfg().SSHHosts, s.Host)
		if !found {
			_ = reply.Send(chatID, "❌ 등록에서 사라진 호스트입니다: "+s.Host)
			return
		}
		out, err := runSSHFn(ctx, b.cfg(), s.Host, claudeSendCmd(claudeBinOf(h), s.Name, body))
		if err != nil {
			_ = reply.Send(chatID, "❌ 전달 실패: "+err.Error())
			return
		}
		if !strings.Contains(out, `"success":true`) {
			_ = reply.Send(chatID, "❌ 전달되지 않았습니다.\n"+truncRunes(collapseSpace(out), 300))
			return
		}
		b.watchTurn(chatID, tgt, s)
	}()
	return true
}
```

`watchTurn` 은 Task 10 에서 만든다. 이 단계에서는 컴파일이 되도록 빈 것을 둔다:

```go
// watchTurn waits for the session to finish and reports back once. Task 10.
func (b *Bot) watchTurn(chatID int64, tgt Target, s SessionInfo) {}
```

`host/bot.go` 의 텔레그램 진입점, `if strings.HasPrefix(text, "!")` 블록 **뒤**이자 `b.dispatchText(...)` **앞**에 한 줄을 넣는다:

```go
			if b.routeToSession(chatID, text, TelegramTarget()) {
				continue
			}
			b.dispatchText(chatID, text, OriginTelegram)
```

`host/chatcontrol.go` 의 `case "send_text":` 에서 rate-limit 검사 **뒤**이자 `go s.bot.dispatchTargeted(...)` **앞**에 같은 것을 넣는다:

```go
		if s.bot.routeToSession(chatID, text, tgt) {
			return
		}
		go s.bot.dispatchTargeted(chatID, text, m.Target)
```

- [ ] **Step 4: 시험이 통과하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -race -run TestRouteToSession -v`
Expected: PASS (3개)

전체가 성한지: `GOFLAGS=-mod=readonly go test ./host/...` → PASS

- [ ] **Step 5: 커밋한다**

```bash
git status --short
git add host/bot.go host/chatcontrol.go host/claudesession_cmd.go host/claudesession_cmd_test.go
git commit -m "feat(host): route plain text to the attached session from both entry points"
```

---

### Task 10: 턴이 끝나면 한 번 알린다

보낸 뒤 기록 파일 꼬리를 주기적으로 읽어, **조용해지면** 한 번 요약해 보낸다. 판정이 이 기능에서 가장 약한 곳이므로, 틀릴 때 손해가 작은 쪽(중간 상태를 한 번 더 보내는 쪽)으로 기운다.

**Files:**
- Modify: `host/claudesession_cmd.go`
- Test: `host/claudesession_cmd_test.go`

**Interfaces:**
- Consumes: Task 4 `tailSummary`, Task 7 `tailCmd`
- Produces: `func turnSettled(prev, cur time.Time, now time.Time, quiet time.Duration) bool` · `(*Bot) watchTurn(chatID int64, tgt Target, s SessionInfo)` (Task 9 의 빈 것을 채운다)

- [ ] **Step 1: 실패하는 시험을 쓴다**

```go
func TestTurnSettled(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	quiet := 20 * time.Second
	older := now.Add(-30 * time.Second)
	recent := now.Add(-5 * time.Second)

	if !turnSettled(older, older, now, quiet) {
		t.Error("마지막 기록이 오래됐고 더 늘지 않으면 끝난 것으로 봐야 함")
	}
	if turnSettled(older, recent, now, quiet) {
		t.Error("방금 새 기록이 붙었으면 아직 끝난 게 아님")
	}
	if turnSettled(older, now, now, quiet) {
		t.Error("지금 쓰고 있으면 끝난 게 아님")
	}
	if turnSettled(time.Time{}, time.Time{}, now, quiet) {
		t.Error("읽은 것이 없으면 끝났다고 단정하면 안 됨")
	}
}

func TestWatchTurn_ReportsOnceWhenQuiet(t *testing.T) {
	rec := `{"type":"assistant","timestamp":"2026-09-18T03:56:46.000Z","message":{"content":[{"type":"text","text":"2단계 끝났습니다."}]}}`
	fakeSSH(t, func(host, cmd string) (string, error) {
		if strings.Contains(cmd, "tail -c") {
			return "버림\n" + rec + "\n", nil
		}
		return "  proj-a-cf [aaa]  ·  interactive  ·  idle  ·  started 1h ago\n", nil
	})
	b := newTestBotForAttach(testCfgWithHost())
	hub := &recordingReply{}
	b.turnReply = func(Target) replySender { return hub }
	// 0 은 "설정 안 함"이라 기본값으로 덮인다. 시험은 사실상 0 인 값을 쓴다.
	b.turnQuiet = time.Nanosecond
	b.turnPoll = time.Millisecond

	b.watchTurn(7, TelegramTarget(), SessionInfo{Host: "dev", Name: "proj-a-cf", Transcript: "/t/a.jsonl"})

	joined := strings.Join(hub.sent, "\n")
	if !strings.Contains(joined, "2단계 끝났습니다.") {
		t.Fatalf("끝났을 때 한 번 알려야 함: %v", hub.sent)
	}
	if len(hub.sent) != 1 {
		t.Errorf("딱 한 번만 알려야 함: %v", hub.sent)
	}
}

func TestWatchTurn_NoTranscriptStaysQuiet(t *testing.T) {
	fakeSSH(t, func(host, cmd string) (string, error) { return "", nil })
	b := newTestBotForAttach(testCfgWithHost())
	hub := &recordingReply{}
	b.turnReply = func(Target) replySender { return hub }
	b.turnQuiet = time.Nanosecond
	b.turnPoll = time.Millisecond

	b.watchTurn(7, TelegramTarget(), SessionInfo{Host: "dev", Name: "x"}) // Transcript 없음
	if len(hub.sent) != 0 {
		t.Errorf("읽을 기록이 없으면 아무 말도 하지 않아야 함: %v", hub.sent)
	}
}
```

- [ ] **Step 2: 시험이 실패하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run 'TestTurnSettled|TestWatchTurn' -v`
Expected: FAIL — `undefined: turnSettled`

- [ ] **Step 3: 최소 구현을 쓴다**

`host/bot.go` 의 `Bot` 에 시험 이음매 두 칸을 더한다(`attach` 아래):

```go
	// Session-attach timing, overridden in tests so a turn-watch does not take
	// real seconds. Zero means "use the defaults".
	turnQuiet time.Duration
	turnPoll  time.Duration
	turnReply func(Target) replySender // nil → b.ReplyTo
```

`host/claudesession_cmd.go` 에 덧붙인다:

```go
const (
	defaultTurnQuiet = 20 * time.Second // no new record for this long → the turn is over
	defaultTurnPoll  = 5 * time.Second
	maxTurnWatch     = 15 * time.Minute // give up rather than watch forever
)

// turnSettled decides whether the session has stopped writing. prev and cur are
// the last record's timestamp on two consecutive reads.
//
// Getting this wrong in one direction sends one extra mid-turn summary; getting
// it wrong in the other leaves the user waiting on a message that never comes.
// So it leans towards declaring the turn over.
func turnSettled(prev, cur, now time.Time, quiet time.Duration) bool {
	if cur.IsZero() {
		return false
	}
	if !cur.Equal(prev) {
		return false
	}
	return now.Sub(cur) >= quiet
}

// watchTurn polls the session's transcript until it goes quiet, then reports
// its last words once. It reads only the tail — the file is tens of megabytes.
func (b *Bot) watchTurn(chatID int64, tgt Target, s SessionInfo) {
	if s.Transcript == "" {
		return
	}
	quiet, poll := b.turnQuiet, b.turnPoll
	if quiet == 0 {
		quiet = defaultTurnQuiet
	}
	if poll == 0 {
		poll = defaultTurnPoll
	}
	replyFor := b.turnReply
	if replyFor == nil {
		replyFor = b.ReplyTo
	}

	ctx, cancel := context.WithTimeout(context.Background(), maxTurnWatch)
	defer cancel()

	var prev time.Time
	for {
		out, err := runSSHFn(ctx, b.cfg(), s.Host, tailCmd(s.Transcript, tailBytes))
		if err != nil {
			return
		}
		line, last := tailSummary([]byte(out))
		if turnSettled(prev, last, time.Now(), quiet) {
			if line != "" {
				_ = replyFor(tgt).Send(chatID, fmt.Sprintf("💬 %s: %s", s.Name, truncRunes(line, 800)))
			}
			return
		}
		prev = last
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}
```

Task 9 에 둔 빈 `watchTurn` 은 지운다.

- [ ] **Step 4: 시험이 통과하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -race -run 'TestTurnSettled|TestWatchTurn' -v`
Expected: PASS (3개)

- [ ] **Step 5: 커밋한다**

```bash
git status --short
git add host/bot.go host/claudesession_cmd.go host/claudesession_cmd_test.go
git commit -m "feat(host): report once when the attached session's turn goes quiet"
```

---

### Task 11: 평문으로 붙기 · 도움말 · 문서

`!` 를 외우지 않아도 되게 한다. 붙지 **않은** 상태에서 들어온 평문이 세션을 고르는 말처럼 보이면 붙는다. 붙은 뒤의 평문은 전부 세션으로 가므로 여기서 다시 보지 않는다.

**Files:**
- Modify: `host/claudesession_cmd.go`, `host/bot.go`(`helpText`), `host/README.md`
- Test: `host/claudesession_cmd_test.go`

**Interfaces:**
- Consumes: Task 8 `resolveTarget`
- Produces: `func attachIntent(text string) (name string, ok bool)`

- [ ] **Step 1: 실패하는 시험을 쓴다**

```go
func TestAttachIntent(t *testing.T) {
	cases := []struct {
		in   string
		name string
		ok   bool
	}{
		{"proj-a 제어할게", "proj-a", true},
		{"proj-a 제어 할게", "proj-a", true},
		{"proj-a-cf 에 붙어줘", "proj-a-cf", true},
		{"proj-b 연결해줘", "proj-b", true},
		{"세션 목록 보여줘", "", false},
		{"오늘 날씨 어때", "", false},
		{"제어", "", false}, // 이름이 없다
	}
	for _, c := range cases {
		name, ok := attachIntent(c.in)
		if ok != c.ok || name != c.name {
			t.Errorf("%q → (%q,%v), 기대 (%q,%v)", c.in, name, ok, c.name, c.ok)
		}
	}
}
```

- [ ] **Step 2: 시험이 실패하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run TestAttachIntent -v`
Expected: FAIL — `undefined: attachIntent`

- [ ] **Step 3: 최소 구현을 쓴다**

`host/claudesession_cmd.go` 에 덧붙인다 (import 에 `regexp` 추가):

```go
// attachVerbs are the words that, following a name, mean "bind this
// conversation to that session". Kept narrow on purpose: a false positive
// hijacks a message that was meant for the normal worker.
var attachVerbs = regexp.MustCompile(`(제어|붙어|붙여|연결)`)

// attachIntent reads a plain sentence as a request to attach, returning the
// name it names. It only ever runs when the conversation is NOT attached — once
// attached, every plain message goes to the session and is never re-read here.
func attachIntent(text string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return "", false
	}
	rest := strings.Join(fields[1:], " ")
	if !attachVerbs.MatchString(rest) {
		return "", false
	}
	name := fields[0]
	// A name has the shape of an identifier, not of a word in a sentence.
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`).MatchString(name) {
		return "", false
	}
	return name, true
}
```

`host/bot.go` 의 텔레그램 진입점에서, Task 9 에 넣은 `routeToSession` 호출 **뒤**에 덧붙인다:

```go
			if name, ok := attachIntent(text); ok {
				b.handleAttach(b.ReplyTo(TelegramTarget()), chatID, laneKeyOf(TelegramTarget()),
					[]string{"!attach", name})
				continue
			}
```

`host/chatcontrol.go` 에도 같은 것을 `tgt` 로 넣는다.

`helpText()` 에 네 줄을 더한다:

```
!sessions — 붙을 수 있는 Claude 세션 목록
!attach <번호|이름> — 그 세션에 붙기 (이후 평문은 그 세션으로)
!detach — 풀기
"proj-a 제어할게" 처럼 평문으로도 붙을 수 있습니다
```

`host/README.md` 에 절을 하나 더한다 — 무엇을 하는지, `ssh.hosts` 의 `claude_sessions: true` 를 켜야 한다는 것, **SSH 계정이 띄운 세션만 보인다**는 것, 목록에 세션의 마지막 한 줄이 실려 나가므로 민감한 화면을 띄워 둔 세션에는 쓰지 말 것.

- [ ] **Step 4: 시험이 통과하는지 확인한다**

Run: `GOFLAGS=-mod=readonly go test ./host/ -run TestAttachIntent -v`
Expected: PASS

전체: `GOFLAGS=-mod=readonly go test ./host/... -race` → PASS
정적 검사: `GOFLAGS=-mod=readonly go vet ./host/...` → 조용

- [ ] **Step 5: 커밋한다**

```bash
git status --short
git add host/claudesession_cmd.go host/bot.go host/chatcontrol.go host/claudesession_cmd_test.go host/README.md
git commit -m "feat(host): attach by plain sentence, and document the feature"
```

---

### Task 12: 실물 점검

시험이 다 통과해도 원격까지 가 보지 않으면 모르는 것들이 있다. **여기서 처음으로 진짜 원격에 나간다.**

**Files:** 없음(수동 점검). 발견한 것이 있으면 해당 Task 로 돌아가 고친다.

- [ ] **Step 1: 빌드해서 바꿔 단다**

`aglink-tmp` 쪽 작업 폴더가 아니라 HEAD 기준 워크트리에서 빌드한다:

```bash
git worktree add --detach /c/tmp/aglink-attach HEAD
cd /c/tmp/aglink-attach/host && GOFLAGS=-mod=readonly go build -o aglink.exe .
```

돌고 있는 호스트를 바꿔 다는 절차는 기존 배포 절차를 따른다.

- [ ] **Step 2: 설정에 호스트를 켠다**

`config.yaml` 의 해당 `ssh.hosts` 항목에 `claude_sessions: true` 를 넣고 호스트를 재기동한다.

- [ ] **Step 3: 목록을 본다**

텔레그램에서 `!sessions`. 확인할 것:
- 세션이 실제 개수만큼 나오는가
- busy/idle 이 맞는가 (일부러 하나를 일 시켜 놓고 본다)
- "마지막" 한 줄이 110자에서 잘리는가, 한글이 깨지지 않는가
- 목록 한 번에 걸리는 시간이 견딜 만한가

- [ ] **Step 4: 붙어서 한마디 주고받는다**

`!attach 1` → 평문으로 짧은 것을 하나 시킨다 → **한 번만** 답이 오는지 본다.
확인할 것:
- 받는 세션에 **원문 그대로** 도착했는가 (그 세션 화면에서 직접 확인)
- 턴이 끝난 뒤 요약이 한 번만 오는가, 중간에 여러 번 오지 않는가
- `!status` 에 붙은 세션이 보이는가
- `!detach` 후 평문이 원래대로 워커로 가는가

- [ ] **Step 5: 웹 대화에서도 같은 것을 한다**

브라우저 대화에서 `!sessions` → `!attach` → 평문. **진입점이 둘이라 여기서 새는 것이 이 기능의 대표적 실패 방식이다.**

- [ ] **Step 6: 결과를 적고 커밋한다**

`docs/superpowers/plans/` 옆이 아니라, 고칠 것이 나왔으면 코드를 고치고 없으면 README 에 실제 확인한 날짜를 한 줄 남긴다.

```bash
git status --short
git add host/README.md
git commit -m "docs(host): record the end-to-end check of session attach"
```

---

## Self-Review

**Spec coverage**

| 설계 항목 | Task |
|---|---|
| `!sessions` 목록(이름·호스트·가동시간·busy/idle·마지막 한 줄) | 1·2·3·4·7·8 |
| `!attach <번호\|이름>` | 8 |
| `!detach` | 8 |
| `!status` 에 붙은 세션 | 8 |
| 평문 → 세션, 붙지 않았으면 워커 | 9 |
| 평문으로 붙기 | 11 |
| 읽기 = JSONL `tail -c` | 4·7·10 |
| 쓰기 = `SendMessage`, 원문 그대로 | 5·9 |
| 턴 끝 판정과 1회 보고 | 10 |
| 110룬 자르기 | 4·7 |
| 중계 의역 방지(파일로 넘기기) | 5 |
| 출처 한 줄 붙이기 | 9 |
| busy 면 알리기 | 7(목록에 표시)·12(실물 확인) |
| 계정 경계 | 2(주석)·11(문서) |
| 메모리에만 두는 상태 | 6 |
| 진입점 둘 다 | 9·12 |
| 설정 `ssh.hosts` 재사용 | 5 |
| 새 SSH 코드 없음 | 7(이음매만 추가) |

설계의 "남은 것" 두 가지(승인 프롬프트 감지, 세션이 죽은 것 알아채기)는 의도적으로 계획에 넣지 않았다 — 설계에서 미정으로 표시한 항목이다.

**빠진 것 하나:** 설계는 "busy 면 보내기 전에 한 줄로 알린다"고 했는데 Task 9 의 `routeToSession` 은 보내기 전에 busy 를 보지 않는다. 목록을 다시 받아 오는 비용(토큰) 때문에 **의도적으로 뺐다** — busy 는 `!sessions` 에서 보이고, 보내기는 실측상 버려지지 않는다. Task 12 Step 4 에서 실제로 흡수가 문제인지 확인한 뒤, 문제면 그때 추가한다.
