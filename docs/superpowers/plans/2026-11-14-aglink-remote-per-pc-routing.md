# aglink Remote Per-PC Routing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `aglink-web-remote` / `aglink-screen-remote` / voice reach the PC running the current Remote-SSH VS Code session, automatically, with no warnings and no shared-port collisions.

**Architecture:** Drop the fixed-port reverse tunnels. Each PC's daemon binds (token-gated) to the interface the server reaches it on; the remote resolves the session's client IP from `SSH_CONNECTION` into `AGLINK_CLIENT_IP`; the shared `~/.claude` MCP entries use `http://${AGLINK_CLIENT_IP}:PORT/mcp`; the voice Stop hook POSTs to `http://${AGLINK_CLIENT_IP}:48231/stop`.

**Tech Stack:** Go (host/web/screen), JavaScript (aglink-voice extension), PowerShell/bash (aglink-always-on, install scripts), Claude Code MCP config.

**Spec:** docs/superpowers/specs/2026-11-14-aglink-remote-per-pc-routing-design.md

## Global Constraints

- MCP URL env expansion is `${VAR}` / `${VAR:-default}` only; an unset var yields a literal (broken) URL — **never use a `:-default`** for the client IP (silent misrouting is a stated anti-goal).
- Any non-loopback bind MUST require the access token (web/screen `authtoken`); bind to the specific reachable interface, not `0.0.0.0`, when possible.
- Work on `main`; `git add` only files this plan creates/modifies (other sessions share the tree). Deploy builds from a detached HEAD worktree; `GOFLAGS=-mod=readonly`.
- Local (non-Remote-SSH) behavior unchanged: loopback bind stays the default.
- Ports: web 48219, screen 48220, voice extension 48231.

## Review Focus

- **Anchor missing** (`SSH_CONNECTION`/`AGLINK_CLIENT_IP` unset): tools must fail visibly, not fall back to loopback or another PC. (Task 4 test.)
- **Non-loopback bind without token**: must refuse to serve / require the token. (Tasks 1–2 tests.)
- **Local use regression**: with no `AGLINK_CLIENT_IP` and loopback bind, the existing local path still works. (Task 1 test.)
- **Multi-client same account**: two PCs connected at once each route to themselves. (Task 6 live check — not unit-testable.)
- **Server→client unreachable** (firewall/iface): clear failure, not a hang. (Task 6 live check.)

---

### Task 1: web daemon — optional interface bind, token required when non-loopback

**Files:**
- Modify: `web/daemon.go` (runDaemon bind addr), `web/datadir.go`/`web/main.go` (serve arg/flag)
- Modify: `web/authtoken.go` (enforce token when bind is non-loopback)
- Test: `web/daemon_test.go` (or nearest existing web test file)

**Interfaces:**
- Produces: `aglink-web serve [--addr host:port]` (default `127.0.0.1:48219`); when host is non-loopback, startup fails unless an access token is configured.

- [ ] **Step 1: Write failing test** — `TestServeNonLoopbackRequiresToken`: building the daemon with addr `0.0.0.0:48219` (or a non-loopback host) and no token returns an error; with a token it succeeds. Also `TestServeDefaultsLoopback`: no `--addr` → `127.0.0.1:48219`.
- [ ] **Step 2: Run, verify fail** — `GOFLAGS=-mod=readonly go test ./web/ -run TestServe -v` → FAIL (undefined/addr not honored).
- [ ] **Step 3: Implement** — parse `--addr` in the serve path; thread it to `runDaemon`'s `net.Listen` addr; in startup, if the host is not loopback and no token is set, return an error directing the user to set a token.
- [ ] **Step 4: Run, verify pass** — same command → PASS.
- [ ] **Step 5: Commit** — `git add web/daemon.go web/datadir.go web/authtoken.go web/*_test.go` (only changed) + message.

### Task 2: screen — require token when `--addr` is non-loopback

**Files:**
- Modify: `screen/mcphttp_windows.go` (RunScreenRemote), `screen/main.go` (serve)
- Test: `screen/*_test.go`

**Interfaces:**
- Consumes: existing `serve --addr host:port`.
- Produces: non-loopback `--addr` requires the token (parallel to Task 1).

- [ ] **Step 1: Write failing test** — `TestScreenRemoteNonLoopbackRequiresToken`.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** — mirror Task 1's token-gate on non-loopback addr in `RunScreenRemote`.
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Commit** (screen files only).

### Task 3: aglink-voice extension — listen on interface + token, install remote Stop hook at the client IP

**Files (aglink-voice repo):**
- Modify: `extension/standalone.js` (listener bind + token), `extension/hook.js` (remote hook install + POST target), `extension/extension.js` (wire remote-window hook install)
- Test: `extension/test/*.test.js`

**Interfaces:**
- Produces: the standalone voice listener binds the interface + requires a token; in a Remote-SSH window, a Stop hook is installed in the REMOTE `~/.claude` that POSTs to `http://${AGLINK_CLIENT_IP}:48231/stop` with the token.

- [ ] **Step 1: Write failing test** — hook-install produces a command POSTing to `http://${AGLINK_CLIENT_IP}:48231/stop` (env-expanded form) with the token header; listener rejects un-tokened non-loopback requests.
- [ ] **Step 2: Run, verify fail** — `node --test extension/test/*.test.js`.
- [ ] **Step 3: Implement** — bind+token in the listener; extend `ensureStopHook` to target `${AGLINK_CLIENT_IP}:48231` and write to the remote `~/.claude` when in a Remote-SSH window.
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Commit** (aglink-voice).

### Task 4: `AGLINK_CLIENT_IP` injection + MCP registration migration (remote setup)

**Files:**
- Create: `whisper-server-linux/`-style helper OR extend `scripts/` — a small remote setup snippet that exports `AGLINK_CLIENT_IP=${SSH_CONNECTION%% *}` for login shells (and/or sshd `SetEnv`), idempotent.
- Modify: the registration step so remote MCP entries use `http://${AGLINK_CLIENT_IP}:48219/mcp` / `:48220` with the token header (replacing the static `127.0.0.1` registration in `web/authtoken.go`'s printed command and `aglink-always-on.ps1`'s registration note).
- Test: a bash unit asserting `${SSH_CONNECTION%% *}` extraction; manual verification of `claude mcp get`.

**Interfaces:**
- Consumes: Tasks 1–2 (interface bind + token).
- Produces: a remote account where each session's `aglink-*-remote` MCP resolves to its own client IP.

- [ ] **Step 1: Write failing test** — bash test: given `SSH_CONNECTION="10.0.0.5 1 2 3"`, the export snippet sets `AGLINK_CLIENT_IP=10.0.0.5`; empty `SSH_CONNECTION` leaves it unset (no default).
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** the snippet + the registration-command change (env-URL + token, no default).
- [ ] **Step 4: Run, verify pass** + `claude mcp get aglink-web-remote` shows the `${AGLINK_CLIENT_IP}` URL.
- [ ] **Step 5: Commit.**

### Task 5: retire the reverse tunnels

**Files:**
- Modify: `scripts/aglink-always-on.ps1` (remove/neutralize the `-R 48219/48220` tunnel for the per-PC-routing path; keep only what's still needed), docs.

**Interfaces:**
- Consumes: Tasks 1–4 (direct dial replaces the tunnel).

- [ ] **Step 1:** Remove the 48219/48220 RemoteForward from the always-on tunnel path; update the doc comment.
- [ ] **Step 2:** `GOFLAGS=-mod=readonly go build ./...` + lint the script (`powershell -NoProfile -Command "..."` parse check).
- [ ] **Step 3: Commit.**

### Task 6: deploy + live multi-machine verification (operational — needs the real two-PC environment)

- [ ] Build host/web/screen from a detached HEAD worktree; deploy per the deploy-swap memory.
- [ ] On each client PC: bind the daemons to the reachable interface + token; open firewall for 48219/48220/48231 from the server.
- [ ] Inject `AGLINK_CLIENT_IP` on the remote; register the env-URL MCP entries.
- [ ] From the office Remote-SSH window: `aglink-web-remote` controls the office PC. From the home (Tailscale) window **simultaneously**: controls the home PC. Confirm no cross-talk.
- [ ] Finish a Claude turn in each: voice reads on the correct PC.
- [ ] Negative: unset `AGLINK_CLIENT_IP` → visible failure, not misroute.

---

## Notes

- Tasks 1–5 are codeable/testable from one machine; Task 6 requires the live two-PC (home+office) setup and is operational.
- aglink-voice (Task 3) is a separate repo (`C:\Project\88.MyProject\aglink-voice`), currently with uncommitted WIP from another session — coordinate before committing there.
- Keep each component's non-loopback exposure limited to the LAN/Tailscale interface + token.
