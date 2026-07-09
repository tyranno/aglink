# teleclaude — agent notes

## Multiple concurrent sessions touch this repo

This machine regularly has **more than one Claude Code session open on this
same working tree at once** — e.g. a teleclaude-spawned worker (a Telegram/web
chat turn) and a separate VS Code Claude Code session, sometimes on the same
files. There is no lock file or coordination between them.

Before making multi-file edits or anything hard to undo:
- Run `git status` / `git diff` first. If you see changes you don't recognize
  making, another session likely made them — read them before overwriting.
- Re-run `git status` again right before committing — a change can land
  between when you started editing and when you're about to commit.
- If you're about to rebuild/redeploy a binary (`scripts/redeploy-plugin.ps1`
  or manual `go build`), check `git log -1` first: another session may have
  already committed newer work you'd otherwise silently skip deploying.

## Redeploying aglink-* plugins

Use `scripts/redeploy-plugin.ps1 -Name aglink-chat|aglink-screen|aglink-web`
instead of hand-composing `go build` + kill-process + copy — it does all three
and confirms the result. See the script's header comment for details.

## `!update` cannot run mid-turn

The `!update` chat command is rejected while a worker is actively responding
(including the turn that sent the `!update` message itself, if it's a reply
in the same conversation). Send it in a follow-up message after the current
response has finished.
