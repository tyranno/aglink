# aglink-chat — agent notes

## Multiple concurrent sessions touch this repo

This machine regularly has **more than one Claude Code session open on this
same working tree at once** (e.g. a teleclaude-spawned worker and a separate
VS Code Claude Code session), sometimes editing the same files in `web/`.
There is no lock file or coordination between them.

Before multi-file edits or committing:
- Run `git status` / `git diff` first — unrecognized changes likely came from
  another session; read them before overwriting.
- Re-check `git status` right before committing, not just when you started.
- Before rebuilding/redeploying, check `git log -1` — another session may
  have already committed newer work.

## Redeploying

From the sibling `Teleclaude/` repo: `scripts/redeploy-plugin.ps1 -Name
aglink-chat`. It builds here, kills the running deployed process, copies the
binary, and (since teleclaude supervises aglink-chat) confirms it respawned.
Don't hand-compose the kill/copy/verify steps — the script already does it and
polls for respawn instead of guessing a fixed sleep.

## Delegating work to another live agent session

Typing a task into another session's chat UI via screen automation (clicking
into a VS Code panel, etc.) is unreliable in practice — coordinates drift,
"send" triggers get missed, and there is no way to confirm the message was
received short of re-reading that session's own transcript file
(`~/.claude/projects/<encoded-path>/*.jsonl`). If you need another session to
do something, prefer doing the work yourself in the current session, or have
the human relay a written task description — don't assume a screen-typed
delegation landed without independently verifying it in that session's
transcript.
