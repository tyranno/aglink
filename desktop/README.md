# aglink-desktop

Wails v3 desktop client for aglink. Talks to the host over the control API on
`127.0.0.1:27270`.

This module is **excluded from `go.work`** (see the comment there): it depends on a
local `wails/v3` checkout outside this repo, so it is always built standalone with
`GOWORK=off`.

## Build

```
cd desktop
wails3 task build
```

`wails3 task build` does three things a plain `go build` does not:

1. builds the frontend into `frontend/dist/` (embedded via `go:embed` — an empty
   dist directory fails to compile),
2. generates `frontend/bindings/`,
3. generates `wails_windows_<arch>.syso`, which carries the **application icon**
   and the version resource.

`frontend/dist/`, `frontend/bindings/` and `*.syso` are all gitignored, so a fresh
clone has none of them.

### Building by hand

If you build with `go build` directly, generate the resource file first or the
executable ships with the generic Windows icon:

```
cd desktop/build
wails3 generate syso -arch amd64 \
  -icon windows/icon.ico \
  -manifest windows/wails.exe.manifest \
  -info windows/info.json \
  -out ../wails_windows_amd64.syso

cd ..
GOWORK=off go build -ldflags "-H windowsgui -s -w" -o bin/aglink-desktop.exe .
```

**`-H windowsgui` is not optional.** Without it the binary links as a console
subsystem app and Windows opens a terminal window next to the GUI every launch.

## Logs

Because the binary is GUI-subsystem it has no stderr, so all logging goes to a
file under the data dir (`$AGLINK_HOME`, else `~/.aglink`):

```
<data dir>/aglink-desktop.log        # current
<data dir>/aglink-desktop.log.old    # previous, rotated at 10 MiB on startup
```

It captures both the standard logger (control API connect/disconnect) and the
Wails system logger. It is deliberately separate from the host's `aglink.log`:
the two processes share a data dir and would otherwise interleave.

## Icon

The source artwork is `frontend/public/aglink-icon.svg`. The build assets derived
from it:

| File | Used for |
|---|---|
| `build/appicon.png` | source for regenerating the platform icons |
| `build/windows/icon.ico` | Windows executable + window/taskbar icon |
| `build/darwin/icons.icns` | macOS bundle |
| `build/linux/…` | Linux desktop entry |

After changing the artwork, regenerate with `wails3 task common:generate:icons`,
then rebuild so the new `.syso` is linked in.

Executable metadata (product name, company, description) lives in
`build/windows/info.json`.
