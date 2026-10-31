// Voice input and output for the composer.
//
// Input: the mic button records with getUserMedia, trims the clip with a small
// energy VAD (stops on its own after a pause), encodes 16 kHz mono WAV and has
// VoiceService.Transcribe run whisper.cpp locally. The text is sent like a
// typed message.
//
// Output: with "read aloud" on, assistant replies arriving for the focused pane
// are spoken with speechSynthesis (Windows voices — no network, no key).
// Markdown, code blocks and URLs are flattened first so they are not read out
// symbol by symbol.
import { Events } from "@wailsio/runtime";
import { VoiceService } from "../bindings/github.com/tyranno/aglink-desktop";
import {
  chat,
  pane,
  targetKey,
  setPaneComposerValue,
  sendFromPane,
  setAssistantTextListener,
} from "./paneStore.svelte.js";

const SPEAK_STORAGE_KEY = "aglink.voice.speak";
const TARGET_RATE = 16000;
const SILENCE_STOP_MS = 1300; // pause after speech that ends the clip
const NO_SPEECH_MS = 8000; // give up if nothing is said
const MAX_CLIP_MS = 60000;
const PREROLL_MS = 300; // kept before the first speech so the onset is not cut
const MAX_SPEAK_CHARS = 1500;

function loadSpeakEnabled() {
  try {
    return localStorage.getItem(SPEAK_STORAGE_KEY) === "1";
  } catch {
    return false;
  }
}

export const voice = $state({
  status: "unknown", // unknown | missing | installing | ready | unsupported
  installPercent: 0,
  installStage: "",
  paneId: null, // pane currently recording / transcribing
  phase: "idle", // idle | listening | transcribing
  level: 0, // 0..1 mic level while listening
  speakEnabled: loadSpeakEnabled(),
  speaking: false,
  note: "", // last error / hint, shown above the composer
  notePaneId: null,
});

let recorder = null; // active recording session

function setNote(paneId, text) {
  voice.note = text;
  voice.notePaneId = paneId;
}

export function clearVoiceNote() {
  voice.note = "";
  voice.notePaneId = null;
}

// ---- install ----

export async function refreshVoiceStatus() {
  try {
    const s = JSON.parse(await VoiceService.VoiceStatus());
    if (!s.supported) voice.status = "unsupported";
    else if (s.installing) voice.status = "installing";
    else voice.status = s.ready ? "ready" : "missing";
  } catch {
    voice.status = "unsupported";
  }
}

Events.On("voice:install", (event) => {
  const d = event.data || {};
  if (d.stage === "done") {
    voice.status = "ready";
    voice.installStage = "";
    if (voice.notePaneId) setNote(voice.notePaneId, "음성 인식 설치 완료 — 🎤 를 눌러 말해 보세요.");
    return;
  }
  if (d.stage === "error") {
    voice.status = "missing";
    voice.installStage = "";
    setNote(voice.notePaneId, `음성 인식 설치 실패: ${d.error}`);
    return;
  }
  voice.status = "installing";
  voice.installStage = d.stage === "model" ? "음성 모델" : "whisper 엔진";
  voice.installPercent = d.total > 0 ? Math.floor((d.done / d.total) * 100) : 0;
});

async function installVoice(paneId) {
  const ok = window.confirm(
    "음성 인식을 처음 쓰려면 whisper 엔진과 한국어 모델(약 500MB)을 내려받아야 합니다.\n" +
      "인식은 이 PC 안에서만 이뤄지고 음성은 밖으로 나가지 않습니다.\n\n지금 내려받을까요?",
  );
  if (!ok) return;
  voice.notePaneId = paneId;
  voice.status = "installing";
  voice.installPercent = 0;
  try {
    await VoiceService.InstallVoice();
  } catch (error) {
    voice.status = "missing";
    setNote(paneId, `음성 인식 설치 실패: ${error}`);
  }
}

// ---- recording ----

export async function toggleVoiceInput(paneId) {
  clearVoiceNote();
  if (voice.phase === "listening") {
    stopRecording(true);
    return;
  }
  if (voice.phase === "transcribing") return;
  if (voice.status === "unknown") await refreshVoiceStatus();
  if (voice.status === "unsupported") {
    setNote(paneId, "음성 인식은 Windows x64 에서만 지원합니다.");
    return;
  }
  if (voice.status === "installing") {
    setNote(paneId, "음성 인식을 설치하는 중입니다. 잠시만 기다려 주세요.");
    return;
  }
  if (voice.status === "missing") {
    await installVoice(paneId);
    return;
  }
  await startRecording(paneId);
}

async function startRecording(paneId) {
  stopSpeaking();
  let stream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
  } catch (error) {
    setNote(paneId, `마이크를 열 수 없습니다: ${error?.message || error}`);
    return;
  }

  const ctx = new AudioContext();
  const source = ctx.createMediaStreamSource(stream);
  // ScriptProcessor is deprecated but needs no separate worklet file, and a
  // 4096-frame callback (~90 ms) is plenty for a level meter and pause detection.
  const node = ctx.createScriptProcessor(4096, 1, 1);
  const startedAt = performance.now();
  const rec = {
    paneId,
    stream,
    ctx,
    source,
    node,
    chunks: [],
    firstSpeechChunk: -1,
    lastSpeechAt: 0,
    noiseFloor: 0,
    noiseSamples: 0,
    startedAt,
  };
  recorder = rec;

  node.onaudioprocess = (e) => {
    if (recorder !== rec) return;
    const input = e.inputBuffer.getChannelData(0);
    rec.chunks.push(new Float32Array(input));
    let sum = 0;
    for (let i = 0; i < input.length; i++) sum += input[i] * input[i];
    const rms = Math.sqrt(sum / input.length);
    voice.level = Math.min(1, rms * 12);

    const now = performance.now();
    // Learn the background level from the first ~0.4 s, before most people start talking.
    if (now - startedAt < 400) {
      rec.noiseFloor = (rec.noiseFloor * rec.noiseSamples + rms) / (rec.noiseSamples + 1);
      rec.noiseSamples++;
      return;
    }
    const threshold = Math.max(0.012, rec.noiseFloor * 3);
    if (rms > threshold) {
      if (rec.firstSpeechChunk < 0) rec.firstSpeechChunk = rec.chunks.length - 1;
      rec.lastSpeechAt = now;
    }
    if (rec.firstSpeechChunk >= 0 && now - rec.lastSpeechAt > SILENCE_STOP_MS) stopRecording(true);
    else if (rec.firstSpeechChunk < 0 && now - startedAt > NO_SPEECH_MS) {
      stopRecording(false);
      setNote(paneId, "음성이 감지되지 않았습니다. 🎤 를 다시 눌러 말해 주세요.");
    } else if (now - startedAt > MAX_CLIP_MS) stopRecording(true);
  };
  source.connect(node);
  node.connect(ctx.destination); // the processor only runs while connected; its output is silence

  voice.paneId = paneId;
  voice.phase = "listening";
  voice.level = 0;
}

function stopRecording(transcribe) {
  const rec = recorder;
  if (!rec) return;
  recorder = null;
  rec.node.onaudioprocess = null;
  try {
    rec.source.disconnect();
    rec.node.disconnect();
  } catch {}
  rec.stream.getTracks().forEach((t) => t.stop());
  const rate = rec.ctx.sampleRate;
  void rec.ctx.close();
  voice.level = 0;

  if (!transcribe || rec.firstSpeechChunk < 0) {
    voice.phase = "idle";
    voice.paneId = null;
    return;
  }
  const chunkMs = (4096 / rate) * 1000;
  const from = Math.max(0, rec.firstSpeechChunk - Math.ceil(PREROLL_MS / chunkMs));
  const samples = concat(rec.chunks.slice(from));
  void transcribeClip(rec.paneId, encodeWav(downsample(samples, rate, TARGET_RATE), TARGET_RATE));
}

async function transcribeClip(paneId, wavBytes) {
  voice.phase = "transcribing";
  try {
    const text = (await VoiceService.Transcribe(toBase64(wavBytes))).trim();
    if (!text) {
      setNote(paneId, "말씀을 알아듣지 못했습니다. 다시 한 번 말해 주세요.");
      return;
    }
    const p = pane(paneId);
    if (!p?.target) {
      setNote(paneId, `대화가 선택되지 않아 보내지 못했습니다: "${text}"`);
      return;
    }
    const existing = p.composerText.trim();
    setPaneComposerValue(paneId, existing ? `${existing} ${text}` : text);
    if (chat.connected) await sendFromPane(paneId);
    else setNote(paneId, "aglink 에 연결되어 있지 않아 입력창에만 넣었습니다.");
  } catch (error) {
    setNote(paneId, `음성 인식 실패: ${error?.message || error}`);
  } finally {
    voice.phase = "idle";
    voice.paneId = null;
  }
}

function concat(chunks) {
  const out = new Float32Array(chunks.reduce((n, c) => n + c.length, 0));
  let off = 0;
  for (const c of chunks) {
    out.set(c, off);
    off += c.length;
  }
  return out;
}

// downsample averages each output sample's source window — a crude low-pass
// that is good enough for speech going to whisper.
function downsample(input, fromRate, toRate) {
  if (fromRate === toRate) return input;
  const ratio = fromRate / toRate;
  const out = new Float32Array(Math.floor(input.length / ratio));
  for (let i = 0; i < out.length; i++) {
    const start = Math.floor(i * ratio);
    const end = Math.min(input.length, Math.floor((i + 1) * ratio));
    let sum = 0;
    for (let j = start; j < end; j++) sum += input[j];
    out[i] = sum / Math.max(1, end - start);
  }
  return out;
}

function encodeWav(samples, rate) {
  const buf = new ArrayBuffer(44 + samples.length * 2);
  const v = new DataView(buf);
  const str = (off, s) => {
    for (let i = 0; i < s.length; i++) v.setUint8(off + i, s.charCodeAt(i));
  };
  str(0, "RIFF");
  v.setUint32(4, 36 + samples.length * 2, true);
  str(8, "WAVE");
  str(12, "fmt ");
  v.setUint32(16, 16, true);
  v.setUint16(20, 1, true); // PCM
  v.setUint16(22, 1, true); // mono
  v.setUint32(24, rate, true);
  v.setUint32(28, rate * 2, true);
  v.setUint16(32, 2, true);
  v.setUint16(34, 16, true);
  str(36, "data");
  v.setUint32(40, samples.length * 2, true);
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i]));
    v.setInt16(44 + i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
  }
  return new Uint8Array(buf);
}

function toBase64(bytes) {
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    bin += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
  }
  return btoa(bin);
}

// ---- read aloud ----

export function setSpeakEnabled(on) {
  voice.speakEnabled = on;
  try {
    localStorage.setItem(SPEAK_STORAGE_KEY, on ? "1" : "0");
  } catch {}
  if (!on) stopSpeaking();
}

export function stopSpeaking() {
  if (typeof speechSynthesis === "undefined") return;
  speechSynthesis.cancel();
  voice.speaking = false;
}

function koreanVoice() {
  const voices = speechSynthesis.getVoices().filter((v) => v.lang?.toLowerCase().startsWith("ko"));
  return voices.find((v) => /natural/i.test(v.name)) || voices[0] || null;
}

// speechText flattens markdown into something worth hearing.
export function speechText(md) {
  let s = String(md || "");
  s = s.replace(/```[\s\S]*?```/g, " 코드는 화면을 참고해 주세요. ");
  s = s.replace(/`([^`]+)`/g, "$1");
  s = s.replace(/!\[[^\]]*\]\([^)]*\)/g, " ");
  s = s.replace(/\[([^\]]+)\]\([^)]*\)/g, "$1");
  s = s.replace(/https?:\/\/\S+/g, " 링크 ");
  s = s.replace(/^\s*\|?\s*:?-{3,}.*$/gm, " "); // table separator rows
  s = s.replace(/^\s*\|(.*)\|\s*$/gm, (_, row) =>
    row.split("|").map((c) => c.trim()).filter(Boolean).join(", ") + ".",
  );
  s = s.replace(/\|/g, ", ");
  s = s.replace(/^\s{0,3}(#{1,6}|>|[-*+]|\d+[.)])\s+/gm, "");
  s = s.replace(/(\*\*|__|~~|\*|_)(\S[^*_~]*?)\1/g, "$2");
  s = s.replace(/[#*_~>]+/g, " ");
  s = s.replace(/\s+/g, " ").trim();
  if (s.length > MAX_SPEAK_CHARS) s = s.slice(0, MAX_SPEAK_CHARS) + " … 이하 내용은 화면을 확인해 주세요.";
  return s;
}

// splitSentences keeps each utterance short; long single utterances are where
// speechSynthesis tends to stall.
function splitSentences(text) {
  const parts = text.match(/[^.!?。？！\n]+[.!?。？！]*\s*/g) || [text];
  const out = [];
  let cur = "";
  for (const p of parts) {
    if ((cur + p).length > 220 && cur) {
      out.push(cur.trim());
      cur = "";
    }
    cur += p;
  }
  if (cur.trim()) out.push(cur.trim());
  return out;
}

export function speak(md) {
  if (typeof speechSynthesis === "undefined") return;
  const text = speechText(md);
  if (!text) return;
  const v = koreanVoice();
  const sentences = splitSentences(text);
  sentences.forEach((sentence, i) => {
    const u = new SpeechSynthesisUtterance(sentence);
    u.lang = "ko-KR";
    if (v) u.voice = v;
    u.rate = 1.05;
    if (i === 0) u.onstart = () => (voice.speaking = true);
    if (i === sentences.length - 1) {
      u.onend = () => (voice.speaking = speechSynthesis.pending);
      u.onerror = () => (voice.speaking = false);
    }
    speechSynthesis.speak(u);
  });
}

setAssistantTextListener((key, text) => {
  if (!voice.speakEnabled) return;
  const focused = pane(chat.focusedPaneId);
  if (!focused?.target || targetKey(focused.target) !== key) return;
  speak(text);
});

// getVoices() is empty until the voice list loads; touching it starts the load.
if (typeof speechSynthesis !== "undefined") speechSynthesis.getVoices();
void refreshVoiceStatus();
