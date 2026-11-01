# speaker-server: tells the enrolled user's voice from anyone else's, for
# aglink-desktop's voice conversation (speaker.go). SpeechBrain's ECAPA-TDNN
# (VoxCeleb) turns a clip into a voice embedding; the score is its cosine
# similarity to the enrolled one. One JSON line per request on stdin, one
# answer on stdout:
#   in:  {"op":"embed","wav":"C:/.../me.wav","out":"C:/.../me.pt"}
#   in:  {"op":"score","wav":"C:/.../clip.wav","ref":"C:/.../me.pt","adapt_min":0.6}
#
# adapt_min: a clip of at least ADAPT_SECONDS scoring this high is surely the
# user, so the enrolled embedding is nudged toward it (and saved). One
# enrollment recording was made once, in one room, on one day; everyday clips
# teach it how the user sounds through this mic, which widens the gap to others.
#   out: {"ok":true,"score":0.71,"seconds":0.03}  or  {"ok":false,"error":"..."}
import json
import os
import sys
import time

ROOT = os.path.dirname(os.path.abspath(__file__))
ADAPT_SECONDS = 2.0
ADAPT_WEIGHT = 0.1
ANCHOR_MIN = 0.45  # learned clips must still match the enrollment itself


def answer(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()


def main():
    real_stdout = sys.stdout
    sys.stdout = sys.stderr  # library prints must not mix with answers
    import torch
    import torchaudio
    from speechbrain.lobes.features import Fbank
    from speechbrain.lobes.models.ECAPA_TDNN import ECAPA_TDNN

    device = "cuda:0" if torch.cuda.is_available() else "cpu"
    # Built by hand from ecapa/hyperparams.yaml: SpeechBrain's own loader
    # insists on fetching from the Hugging Face hub, even for a local copy.
    fbank = Fbank(n_mels=80).to(device)
    model = ECAPA_TDNN(
        input_size=80,
        channels=[1024, 1024, 1024, 1024, 3072],
        kernel_sizes=[5, 3, 3, 3, 1],
        dilations=[1, 2, 3, 4, 1],
        attention_channels=128,
        lin_neurons=192,
    )
    state = torch.load(os.path.join(ROOT, "ecapa", "embedding_model.ckpt"), map_location="cpu", weights_only=True)
    model.load_state_dict(state)
    model.eval().to(device)
    refs = {}

    @torch.no_grad()
    def embed(path):
        wav, rate = torchaudio.load(path)
        wav = wav.mean(0, keepdim=True)
        if rate != 16000:
            wav = torchaudio.functional.resample(wav, rate, 16000)
        feats = fbank(wav.to(device))
        feats = feats - feats.mean(dim=1, keepdim=True)  # per-clip mean normalization, as trained
        return model(feats).squeeze().float().cpu(), wav.shape[1] / 16000

    def ref(path):
        if path not in refs:
            refs[path] = torch.load(path, map_location="cpu", weights_only=True)
        return refs[path]

    embed(os.path.join(ROOT, "ecapa", "example1.wav"))  # first run pays the CUDA start-up
    sys.stdout = real_stdout
    answer({"ok": True, "ready": True, "device": device})
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        started = time.time()
        try:
            req = json.loads(line)
            sys.stdout = sys.stderr
            out = {"ok": True}
            if req.get("op") == "embed":
                torch.save(embed(req["wav"])[0], req["out"])
                refs.pop(req["out"], None)
            else:
                e, seconds = embed(req["wav"])
                r = ref(req["ref"])
                score = torch.nn.functional.cosine_similarity(e, r, dim=0).item()
                out["score"] = round(score, 3)
                lo = req.get("adapt_min")
                base = req.get("base")  # the enrollment as recorded; never moves
                sure = lo and score >= lo and seconds >= ADAPT_SECONDS
                anchored = base and os.path.exists(base)
                if sure and anchored and torch.nn.functional.cosine_similarity(e, ref(base), dim=0).item() >= ANCHOR_MIN:
                    n = torch.nn.functional.normalize
                    r = (1 - ADAPT_WEIGHT) * n(r, dim=0) + ADAPT_WEIGHT * n(e, dim=0)
                    torch.save(r, req["ref"])
                    refs[req["ref"]] = r
                    out["adapted"] = True
            out["seconds"] = round(time.time() - started, 3)
            sys.stdout = real_stdout
            answer(out)
        except Exception as err:  # one bad request must not end the server
            sys.stdout = real_stdout
            answer({"ok": False, "error": str(err)})


if __name__ == "__main__":
    main()
