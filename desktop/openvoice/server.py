# openvoice-server: OpenVoice V2 tone color converter for aglink-desktop.
#
# Supertonic still speaks (fast, natural Korean); this only repaints the
# timbre of each clip with a target speaker's. Models load once, then each
# request is one JSON line on stdin and one JSON line answer on stdout:
#   in:  {"op":"embed","ref":"C:/.../voice.wav","out":"C:/.../voice.se.pt"}
#   in:  {"op":"convert","src":"a.wav","src_se":"M2.se.pt","tgt_se":"voice.se.pt","out":"b.wav","tau":0.3}
#   out: {"ok":true,"seconds":0.42}  or  {"ok":false,"error":"..."}
import json
import os
import sys
import time
import types

ROOT = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(ROOT, "src"))

# The converter never turns text into speech, and openvoice.text drags in
# Chinese/English phonemizers; a stub keeps those out.
_text = types.ModuleType("openvoice.text")
_text.text_to_sequence = lambda *a, **k: []
sys.modules["openvoice.text"] = _text

import torch  # noqa: E402
from openvoice.api import OpenVoiceBaseClass, ToneColorConverter  # noqa: E402


def answer(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()


def main():
    real_stdout = sys.stdout
    sys.stdout = sys.stderr  # library prints must not mix with answers
    device = "cuda:0" if torch.cuda.is_available() else "cpu"
    # ToneColorConverter(enable_watermark=False) passes the flag on to a base
    # class that rejects it, so it is built by hand without the watermark model.
    conv = ToneColorConverter.__new__(ToneColorConverter)
    OpenVoiceBaseClass.__init__(conv, os.path.join(ROOT, "ckpt", "config.json"), device=device)
    conv.watermark_model = None
    conv.version = getattr(conv.hps, "_version_", "v1")
    conv.load_ckpt(os.path.join(ROOT, "ckpt", "checkpoint.pth"))
    cache = {}

    def se(path):
        if path not in cache:
            cache[path] = torch.load(path, map_location=device, weights_only=True)
        return cache[path]

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
            if req.get("op") == "embed":
                emb = conv.extract_se([req["ref"]])
                torch.save(emb.cpu(), req["out"])
                cache.pop(req["out"], None)
            else:
                conv.convert(
                    audio_src_path=req["src"],
                    src_se=se(req["src_se"]),
                    tgt_se=se(req["tgt_se"]),
                    output_path=req["out"],
                    tau=float(req.get("tau", 0.3)),
                )
            sys.stdout = real_stdout
            answer({"ok": True, "seconds": round(time.time() - started, 3)})
        except Exception as err:  # one bad request must not end the server
            sys.stdout = real_stdout
            answer({"ok": False, "error": str(err)})


if __name__ == "__main__":
    main()
