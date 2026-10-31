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
