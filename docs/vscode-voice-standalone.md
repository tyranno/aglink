# VS Code voice without aglink — plan

Goal: company colleagues install one setup file and get voice conversation
with Claude Code in plain VS Code — no aglink desktop, no host, no Python, no
downloads after install. Everything (recognition, reading, all voices, the
enrolled-voice check) is in the setup. When the aglink desktop app *is*
running, the extension keeps using it as today (`desktop/vscodevoice.go`).

## Pieces

| Piece | Today (desktop) | Standalone |
|---|---|---|
| Mic capture, VAD, wake word, echo filter, commands | `voice.svelte.js` in WebView2 | engine captures; logic ported to Node in the extension (`vscode/voicecore/`) |
| Recognition | whisper.cpp server, ggml-small, CUDA if NVIDIA | same binaries bundled: CUDA build + CPU build, chosen at start |
| Reading | `supertonic-server.exe` (C#, ONNX), 10 voices | same, inside the engine |
| Enrolled-voice check | Python + SpeechBrain ECAPA (`speaker_server.py`) | ECAPA exported to ONNX, run by the engine's ONNX Runtime — no Python |
| Playback | Go PlaySound | engine (NAudio), reports the playing level for the echo filter |
| Paste + Enter into Claude Code | desktop `pasteAndSend` | engine does the same Win32 calls |

## Engine: `aglink-voice-engine.exe`

Grown from `desktop/supertonic` (C#, .NET 9, published self-contained so no
.NET install is needed). One process, JSON lines on stdin/stdout:

- `tts {text, voice, speed}` → wav; `play {wav, gen}` / `stop {gen}`
- `mic {on}` → stream of `{pcm(base64 16 kHz mono), level}` frames
- `embed {wav, out}` / `score {wav, ref}` — ECAPA ONNX
- `paste {pid, title}` — foreground check, Ctrl+V, Enter
- starts and supervises `whisper-server.exe` (CUDA or CPU), `transcribe {wav}`

## Extension

- Settings (VS Code settings + status bar menu): voice M1–M5 / F1–F5, speed,
  wake word on/off and names, summary-only reading, voice-check bar, idle
  minutes, text-only.
- Commands: voice on/off, enroll my voice (15 s), choose voice, test reading.
- Engine source: aglink desktop if it answers on 127.0.0.1:48230, else the
  bundled engine.

## Setup: `aglink-voice-setup.exe` (NSIS, `installer/`)

Installs to `%LOCALAPPDATA%\aglink-voice` (no admin): engine, whisper CPU +
CUDA, ggml-small, Supertonic models and voice styles, ECAPA ONNX, the VSIX;
then `code --install-extension` and the Claude Code Stop hook. Uninstaller
removes both. Size ~2 GB before compression — watch NSIS's 2 GB data limit;
split CUDA into a second data file if needed.

## Order

1. ECAPA → ONNX and C# scoring matching Python scores (biggest risk).
2. Engine: mic, playback, paste, whisper supervision on top of Supertonic.
3. Port the conversation logic to Node; drive it from the extension.
4. Settings UI, enrollment command.
5. NSIS setup; test on a clean Windows user without aglink.
