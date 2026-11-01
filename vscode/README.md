# aglink for VS Code

Connects each VS Code window to the **aglink-web** daemon, so an agent working
in one window — or anywhere that reaches the daemon — can see what **another**
window is doing and act in it, as text instead of screenshots.

- which files are open, and their full contents (including unsaved edits)
- the Problems panel
- terminal commands and their output — run a command and get the result back
- any VS Code command, by id

It works for **Remote-SSH windows**. The extension is a *UI extension*: it runs
on the Windows machine even when the window is attached to a remote host, so it
reaches the daemon on `127.0.0.1` directly, and VS Code carries file and
terminal operations to the remote. **Install it once, on the machine that runs
VS Code — nothing is installed on the remote.**

## Install

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File vscode\install.ps1
```

This builds `aglink-vscode-<version>.vsix` and installs it with
`code --install-extension`. Each open window connects on its own; the log is in
the **Output** panel under **aglink**.

The aglink-web daemon must be running (it is started on demand by the
aglink-web MCP bridge, or kept up by `scripts/aglink-always-on.ps1`).

## Use

`list_profiles` shows every connected window:

```
vscode:backend@192-168-123-146-doowon | SSH 192.168.123.146-doowon | /home/doowon/project/llie/backend | connected 5m ago
vscode:aglink | local | C:\Project\88.MyProject\aglink | connected 5m ago
```

Pass one as `profile` to a `vscode_*` tool. A unique prefix is enough
(`vscode:backend`). There is **no default window**: the window the agent itself
runs in is connected too, and acting on the wrong one is what naming prevents.

| Tool | What it does |
|---|---|
| `vscode_workspace` | workspace, remote host, folders, active file and line |
| `vscode_editors` | open file tabs, active and unsaved marked |
| `vscode_read` | a file as the window sees it — unsaved edits included — with line numbers |
| `vscode_open` | open a file, jump to a line |
| `vscode_problems` | errors and warnings |
| `vscode_terminals` | terminals, whether shell integration is on, last command |
| `vscode_terminal_run` | run a command, wait, return output and exit code |
| `vscode_terminal_read` | recent commands and output in a terminal — including ones a person typed |
| `vscode_command` | run any VS Code command by id |

Paths are absolute or relative to the window's first workspace folder. In a
Remote-SSH window an absolute path is on the remote.

### Terminals

Reading output needs VS Code's **shell integration** (on by default for bash,
zsh, fish and PowerShell). `vscode_terminal_run` uses a terminal named
`aglink`, created if missing, unless you name another. Without shell integration
the command is still sent, and the tool says plainly that its output cannot be
read. `vscode_terminal_read` only knows commands run after the extension
started.

## Voice conversation with Claude Code

With the aglink desktop app running, the status bar button **음성** (or
`aglink: 음성 대화 켜기/끄기`, Ctrl+Alt+V) lends the desktop app's voice to
Claude Code in this window: its wake word, Whisper recognition, enrolled-voice
check and Supertonic reading.

- What you say goes into Claude Code's input box and is sent. Claude Code has
  no way to submit a prompt from outside, so the words go on the clipboard and
  the desktop app presses Ctrl+V, Enter in this window, only once it is sure
  this window is in front (otherwise the words stay on the clipboard).
- When the turn ends, Claude Code's Stop hook hands the answer to the desktop
  app, which reads it the way it reads its own replies. The first time voice is
  turned on, the hook (`~/.aglink/vscode-voice-hook.js`) is added to
  `~/.claude/settings.json`; it always exits 0 and does nothing while voice is
  off.
- While voice is on here, conversation mode on the desktop talks to this window
  instead of its own chat. One window holds it at a time; closing the window
  gives it back.

The two sides talk over 127.0.0.1:48230 (`desktop/vscodevoice.go`).

## What it cannot do

- **Read another extension's panel**, such as the Claude Code chat. VS Code does
  not let one extension see another's webview. For a Claude session, aglink's
  `!sessions` / `!attach` reads its transcript directly.
- Edit files. Reading, opening and running commands only.

## Security

`vscode_terminal_run` and `vscode_command` run with that window's full
authority — on this machine for a local window, **on the remote** for a
Remote-SSH window. Anything that can reach the aglink-web daemon can use them,
including a remote session that reaches it through an SSH reverse tunnel. That
is the same level of access aglink-screen already grants (the whole keyboard
and mouse). The daemon only accepts these connections on loopback.

To keep a window out of reach, set **`aglink.enabled`** to `false` in that
window's settings. **`aglink.port`** overrides the daemon port (default: read
from `~/.aglink/aglink-web.port`, else 48219).

## Develop

No build step and no dependencies — VS Code's Node has `WebSocket` built in.

```sh
node --test --test-force-exit vscode/test/*.test.js
```
