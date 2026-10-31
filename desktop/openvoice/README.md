# openvoice-server

OpenVoice V2 tone color converter used by `openvoice.go`. Set up under `~/.aglink/openvoice`:

```
git clone --depth 1 https://github.com/myshell-ai/OpenVoice.git src
py -3.11 -m venv venv
venv\Scripts\python -m pip install torch==2.5.1 --index-url https://download.pytorch.org/whl/cu121
venv\Scripts\python -m pip install librosa soundfile numpy
# ckpt/config.json, ckpt/checkpoint.pth from huggingface.co/myshell-ai/OpenVoiceV2 (converter/)
copy server.py .
```

Put a 10–30 s clean recording of the target voice at `~/.aglink/openvoice/voice.wav`
to turn it on; delete it to go back to the plain Supertonic voice. Only use a
voice with its owner's consent.

## speaker-server

`speaker_server.py` (used by `speaker.go`) tells the enrolled user's voice from
others' in voice conversation. Same venv, plus:

```
venv\Scripts\python -m pip install torchaudio==2.5.1 --index-url https://download.pytorch.org/whl/cu121
venv\Scripts\python -m pip install speechbrain==1.0.3 --no-deps   # newer ones pull in torch 2.7+
venv\Scripts\python -c "from huggingface_hub import snapshot_download as d; d('speechbrain/spkrec-ecapa-voxceleb', local_dir='ecapa')"
copy speaker_server.py .
```

Say "목소리 등록" in voice conversation to record ~15 s of the user (saved as
`me.wav` / `me.pt`); "목소리 등록 해제" forgets it.
