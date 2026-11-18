// Silero VAD v5 (silero_vad.onnx) for aglink-desktop's mic, run in this C#
// server because its onnxruntime already works here. One VAD per mic stream
// (keyed by id); reset begins a stream afresh. Returns a speech probability for
// each 512-sample window (~32 ms at 16 kHz).
//
// The v5 model runs in 512-sample steps but the CALLER must prepend the last 64
// samples of the previous window — model input is 64+512 = 576. Feeding only
// 512 gives ~0 on real speech. I/O: input float32[1,576], state float32[2,1,128],
// sr int64 scalar -> output float32[1,1] (prob), stateN float32[2,1,128].
using System;
using System.Collections.Generic;
using Microsoft.ML.OnnxRuntime;
using Microsoft.ML.OnnxRuntime.Tensors;

namespace Supertonic
{
    sealed class Vad
    {
        const int Window = 512;
        const int Context = 64;

        readonly InferenceSession sess;

        sealed class Stream
        {
            public readonly float[] State = new float[2 * 1 * 128];
            public readonly float[] Ctx = new float[Context]; // last 64 samples of the previous window
            public readonly List<short> Buf = new();          // samples not yet in a full window
        }

        readonly Dictionary<string, Stream> streams = new();

        public Vad(string modelPath) => sess = new InferenceSession(modelPath);

        // Probs feeds s16 mono 16 kHz samples for stream id and returns the speech
        // probability of each 512-sample window that completed inside them. reset
        // starts the stream fresh (clears its state, context and buffer).
        public float[] Probs(string id, byte[] pcm, bool reset)
        {
            if (reset) streams.Remove(id);
            if (!streams.TryGetValue(id, out var s)) { s = new Stream(); streams[id] = s; }

            int n = pcm.Length / 2;
            for (int i = 0; i < n; i++) s.Buf.Add((short)(pcm[i * 2] | (pcm[i * 2 + 1] << 8)));

            var outp = new List<float>();
            int off = 0;
            while (s.Buf.Count - off >= Window)
            {
                outp.Add(Run(s, off));
                off += Window;
            }
            if (off > 0) s.Buf.RemoveRange(0, off);
            return outp.ToArray();
        }

        float Run(Stream s, int off)
        {
            var input = new float[Context + Window];
            for (int i = 0; i < Context; i++) input[i] = s.Ctx[i];
            for (int i = 0; i < Window; i++) input[Context + i] = s.Buf[off + i] / 32768f;

            var feeds = new[]
            {
                NamedOnnxValue.CreateFromTensor("input", new DenseTensor<float>(input, new[] { 1, Context + Window })),
                NamedOnnxValue.CreateFromTensor("state", new DenseTensor<float>(s.State, new[] { 2, 1, 128 })),
                NamedOnnxValue.CreateFromTensor("sr", new DenseTensor<long>(new long[] { 16000 }, new[] { 1 })),
            };

            using var res = sess.Run(feeds);
            float prob = 0f;
            foreach (var r in res)
            {
                if (r.Name == "output") prob = r.AsTensor<float>().GetValue(0);
                else if (r.Name == "stateN")
                {
                    var t = r.AsTensor<float>();
                    for (int i = 0; i < s.State.Length; i++) s.State[i] = t.GetValue(i);
                }
            }
            for (int i = 0; i < Context; i++) s.Ctx[i] = input[Window + i]; // last 64 of this window
            return prob;
        }
    }
}
