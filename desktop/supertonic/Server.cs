// supertonic-server: a long-running Supertonic TTS process for aglink-desktop.
//
// The ONNX models load once at start (a few seconds), then each request is one
// JSON line on stdin and one JSON line answer on stdout:
//   in:  {"text":"안녕하세요","voice":"M1","speed":1.05,"out":"C:\...\a.wav"}
//   out: {"ok":true,"seconds":1.23}  or  {"ok":false,"error":"..."}
// Helper.cs (from supertone-inc/supertonic, MIT) logs to Console, so Console is
// pointed at stderr and the answers go to the real stdout.
using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Text.Json;

namespace Supertonic
{
    class Server
    {
        static int Main(string[] args)
        {
            string root = args.Length > 0 ? args[0] : AppContext.BaseDirectory;
            var stdout = new StreamWriter(Console.OpenStandardOutput(), new UTF8Encoding(false)) { AutoFlush = true };
            Console.SetOut(Console.Error);
            var stdin = new StreamReader(Console.OpenStandardInput(), new UTF8Encoding(false));

            TextToSpeech tts;
            try
            {
                tts = Helper.LoadTextToSpeech(Path.Combine(root, "onnx"), false);
            }
            catch (Exception e)
            {
                stdout.WriteLine(JsonSerializer.Serialize(new { ok = false, error = "load: " + e.Message }));
                return 1;
            }
            stdout.WriteLine(JsonSerializer.Serialize(new { ok = true, ready = true }));

            var styles = new Dictionary<string, Style>();
            string? line;
            while ((line = stdin.ReadLine()) != null)
            {
                if (line.Trim().Length == 0) continue;
                try
                {
                    using var doc = JsonDocument.Parse(line);
                    var req = doc.RootElement;
                    string text = req.GetProperty("text").GetString() ?? "";
                    string voice = req.TryGetProperty("voice", out var v) ? v.GetString() ?? "M1" : "M1";
                    float speed = req.TryGetProperty("speed", out var sp) ? sp.GetSingle() : 1.05f;
                    int steps = req.TryGetProperty("steps", out var st) ? st.GetInt32() : 8;
                    string lang = req.TryGetProperty("lang", out var lg) ? lg.GetString() ?? "ko" : "ko";
                    string outPath = req.GetProperty("out").GetString() ?? throw new ArgumentException("out missing");
                    if (voice.IndexOfAny(Path.GetInvalidFileNameChars()) >= 0 || voice.Contains(".."))
                        throw new ArgumentException("bad voice name");

                    if (!styles.TryGetValue(voice, out var style))
                    {
                        style = Helper.LoadVoiceStyle(new List<string> { Path.Combine(root, "voice_styles", voice + ".json") });
                        styles[voice] = style;
                    }

                    var (wav, duration) = tts.Call(text, lang, style, steps, speed);
                    int len = Math.Min(wav.Length, (int)(tts.SampleRate * duration[0]));
                    var trimmed = new float[len];
                    Array.Copy(wav, trimmed, len);
                    Helper.WriteWavFile(outPath, trimmed, tts.SampleRate);
                    stdout.WriteLine(JsonSerializer.Serialize(new { ok = true, seconds = duration[0] }));
                }
                catch (Exception e)
                {
                    stdout.WriteLine(JsonSerializer.Serialize(new { ok = false, error = e.Message }));
                }
            }
            return 0;
        }
    }
}
