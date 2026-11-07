# turn-server: hears whether the mic has speech in it, and whether the user
# has finished what they were saying, for aglink-desktop's voice input
# (turn.go). Two small ONNX models on the CPU, no torch, under turn/:
#   silero_vad.onnx          Silero VAD v6 (MIT): speech probability per 32 ms
#   smart-turn-v3.2-cpu.onnx Smart Turn v3.2 (BSD-2): end-of-turn probability
#                            of up to 8 s of a turn, from its sound alone
# Either may be missing; the ready line says which are there. One JSON line per
# request on stdin, one answer on stdout. pcm is 16 kHz mono 16-bit
# little-endian PCM, base64:
#   in:  {"op":"vad","id":"rec-3","pcm":"...","reset":true}
#   out: {"ok":true,"probs":[0.02,0.91,...]}    one per whole 512-sample frame
#        A stream's leftover samples and Silero's state carry over to its next
#        request; reset starts it fresh (a newly opened mic).
#   in:  {"op":"turn","pcm":"..."}             the turn so far, pause included
#   out: {"ok":true,"prob":0.93,"seconds":0.04}
#   or:  {"ok":false,"error":"..."}
import base64
import json
import os
import sys
import time

ROOT = os.path.dirname(os.path.abspath(__file__))
MODELS = os.path.join(ROOT, "turn")
RATE = 16000
VAD_FRAME = 512
VAD_CONTEXT = 64  # Silero v5+ reads each frame with the 64 samples before it
VAD_RESET_S = 5.0  # state is renewed this often, as upstream does, but only in a quiet moment
MAX_STREAMS = 8
TURN_SECONDS = 8
N_FFT = 400
HOP = 160
N_MELS = 80


def answer(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()


def mel_filters(np):
    # Slaney-style mel filter bank, as Whisper's feature extractor builds it
    # (librosa.filters.mel(sr=16000, n_fft=400, n_mels=80), norm="slaney").
    def hz_to_mel(f):
        f = np.asarray(f, dtype=np.float64)
        mel = 3.0 * f / 200.0
        log = f >= 1000.0
        return np.where(log, 15.0 + np.log(np.maximum(f, 1e-10) / 1000.0) / (np.log(6.4) / 27.0), mel)

    def mel_to_hz(m):
        m = np.asarray(m, dtype=np.float64)
        f = 200.0 * m / 3.0
        log = m >= 15.0
        return np.where(log, 1000.0 * np.exp(np.log(6.4) / 27.0 * (m - 15.0)), f)

    fft_freqs = np.linspace(0, RATE / 2, 1 + N_FFT // 2)
    mel_pts = mel_to_hz(np.linspace(hz_to_mel(0.0), hz_to_mel(RATE / 2), N_MELS + 2))
    fdiff = np.diff(mel_pts)
    ramps = mel_pts[:, None] - fft_freqs[None, :]
    lower = -ramps[:-2] / fdiff[:-1, None]
    upper = ramps[2:] / fdiff[1:, None]
    weights = np.maximum(0, np.minimum(lower, upper))
    weights *= (2.0 / (mel_pts[2 : N_MELS + 2] - mel_pts[:N_MELS]))[:, None]
    return weights.astype(np.float32)  # (80, 201)


def main():
    real_stdout = sys.stdout
    sys.stdout = sys.stderr  # library prints must not mix with answers
    import numpy as np
    import onnxruntime as ort

    def session(name):
        path = os.path.join(MODELS, name)
        if not os.path.exists(path):
            return None
        so = ort.SessionOptions()
        so.execution_mode = ort.ExecutionMode.ORT_SEQUENTIAL
        so.inter_op_num_threads = 1
        so.intra_op_num_threads = 1 if name.startswith("silero") else 0
        so.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
        so.log_severity_level = 3
        return ort.InferenceSession(path, sess_options=so, providers=["CPUExecutionProvider"])

    vad = session("silero_vad.onnx")
    turn = session("smart-turn-v3.2-cpu.onnx")
    streams = {}  # id -> {"state","context","rest","reset_at","last"}
    filters = mel_filters(np)
    window = np.hanning(N_FFT + 1)[:-1].astype(np.float32)  # periodic Hann, as Whisper
    sr = np.array(RATE, dtype=np.int64)

    def pcm(b64):
        return np.frombuffer(base64.b64decode(b64), dtype="<i2").astype(np.float32) / 32768.0

    def fresh():
        return {
            "state": np.zeros((2, 1, 128), dtype=np.float32),
            "context": np.zeros((1, VAD_CONTEXT), dtype=np.float32),
            "rest": np.zeros(0, dtype=np.float32),
            "reset_at": time.time(),
            "last": 0.0,
        }

    def vad_probs(req):
        sid = str(req.get("id", ""))
        if req.get("reset") or sid not in streams:
            streams.pop(sid, None)
            while len(streams) >= MAX_STREAMS:
                streams.pop(next(iter(streams)))
            streams[sid] = fresh()
        s = streams[sid]
        audio = np.concatenate([s["rest"], pcm(req["pcm"])])
        n = len(audio) // VAD_FRAME
        probs = []
        for i in range(n):
            if s["last"] < 0.1 and time.time() - s["reset_at"] >= VAD_RESET_S:
                s["state"] = np.zeros((2, 1, 128), dtype=np.float32)
                s["reset_at"] = time.time()
            x = np.concatenate([s["context"], audio[i * VAD_FRAME : (i + 1) * VAD_FRAME][None, :]], axis=1)
            out, s["state"] = vad.run(None, {"input": x, "state": s["state"], "sr": sr})
            s["context"] = x[:, -VAD_CONTEXT:]
            s["last"] = float(out[0][0])
            probs.append(round(s["last"], 3))
        s["rest"] = audio[n * VAD_FRAME :]
        return probs

    def features(audio):
        # Whisper's log-mel input, as smart-turn's inference.py makes it with
        # WhisperFeatureExtractor(chunk_length=8): the last 8 s, zero-padded at
        # the start so the turn's end lines up with the input's end, then
        # normalized to zero mean and unit variance.
        n = TURN_SECONDS * RATE
        audio = audio[-n:]
        if len(audio) < n:
            audio = np.pad(audio, (n - len(audio), 0))
        audio = (audio - audio.mean()) / np.sqrt(audio.var() + 1e-7)
        padded = np.pad(audio, N_FFT // 2, mode="reflect")
        frames = np.lib.stride_tricks.sliding_window_view(padded, N_FFT)[::HOP]
        power = np.abs(np.fft.rfft(frames * window, n=N_FFT)) ** 2  # (801, 201)
        mel = filters @ power.T.astype(np.float32)
        log = np.log10(np.maximum(mel, 1e-10))[:, :-1]
        log = np.maximum(log, log.max() - 8.0)
        return ((log + 4.0) / 4.0).astype(np.float32)[None]  # (1, 80, 800)

    def turn_prob(req):
        out = turn.run(None, {"input_features": features(pcm(req["pcm"]))})
        return float(np.asarray(out[0]).reshape(-1)[0])

    # First runs pay ONNX Runtime's start-up, not the first clip.
    if vad is not None:
        vad_probs({"id": "warm", "pcm": base64.b64encode(np.zeros(VAD_FRAME * 2, dtype="<i2").tobytes()).decode(), "reset": True})
        streams.clear()
    if turn is not None:
        turn_prob({"pcm": base64.b64encode(np.zeros(RATE, dtype="<i2").tobytes()).decode()})
    sys.stdout = real_stdout
    answer({"ok": True, "ready": True, "vad": vad is not None, "turn": turn is not None})
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        started = time.time()
        try:
            req = json.loads(line)
            sys.stdout = sys.stderr
            out = {"ok": True}
            op = req.get("op")
            if op == "vad" and vad is not None:
                out["probs"] = vad_probs(req)
            elif op == "turn" and turn is not None:
                out["prob"] = round(turn_prob(req), 3)
            else:
                raise ValueError(f"{op}: no model")
            out["seconds"] = round(time.time() - started, 4)
            sys.stdout = real_stdout
            answer(out)
        except Exception as err:  # one bad request must not end the server
            sys.stdout = real_stdout
            answer({"ok": False, "error": str(err)})


if __name__ == "__main__":
    main()
