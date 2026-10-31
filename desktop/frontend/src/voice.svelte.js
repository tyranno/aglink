// Voice input and output for the composer.
//
// Input: the mic button records with getUserMedia, trims the clip with a small
// energy VAD (stops on its own after a pause), encodes 16 kHz mono WAV and has
// VoiceService.Transcribe run whisper.cpp locally. The text is sent like a
// typed message.
//
// Output: with "read aloud" on, assistant replies arriving for the focused pane
// are spoken with Supertonic (VoiceService.Synthesize, a local model) or, when
// that is not installed, speechSynthesis (Windows voices). The reply is on
// screen, so only its gist is read — the opening sentences of prose, never
// code, tables, links or paths.
//
// Conversation mode chains the two: the reply is read aloud, and once the turn
// is done and the reading has finished the mic opens again by itself, so the
// user just keeps talking. Silence, "그만" or the 🗣 button ends it.
import { Events } from "@wailsio/runtime";
import { VoiceService } from "../bindings/github.com/tyranno/aglink-desktop";
import {
  chat,
  pane,
  targetKey,
  setPaneComposerValue,
  sendFromPane,
  setAssistantTextListener,
  setTurnDoneListener,
} from "./paneStore.svelte.js";

const SPEAK_STORAGE_KEY = "aglink.voice.speak";
const BARGE_STORAGE_KEY = "aglink.voice.barge"; // "0": take turns, the mic waits for the reading
const CONV_STORAGE_KEY = "aglink.voice.conversation"; // pane id while conversation is on
const TARGET_RATE = 16000;
const SILENCE_STOP_MS = 1300; // pause after speech that ends the clip
const CONV_SILENCE_STOP_MS = 2200; // conversation mode: room for "음…" mid-sentence pauses
const EARLY_STOP_MS = 800; // conversation mode with fast recognition: a pause this long is tried as the end
const SENTENCE_END_RE = /(요|니다|죠)[.!?~]*$|(까|다|네|야|지|어|아|해|래|게|자|니|군|걸)[.!?~]+$/; // a finished Korean sentence
const GOES_ON_RE = /(니까|는데|는데요)[.~]*$/; // "그러니까…", "했는데…" lead into more
const ECHO_TAIL_MS = 1000; // speech starting this soon after the reading may be its tail
const NO_SPEECH_MS = 8000; // give up if nothing is said
const CONV_NO_SPEECH_MS = 20000; // conversation mode re-opens the mic after this much silence
const CONV_RETRY_MS = 5000; // conversation mode retries after a transcription error
const CONV_STOP_RE = /^(그만|그만해|대화\s*(종료|끝)|음성\s*(대화\s*)?(종료|끝)|종료)[.!?\s]*$/;
const BARGE_HUSH_RE = /^(잠깐|잠깐만|잠시만|멈춰|스톱|stop|그만\s*읽어)[.!?\s]*$/i;
const BARGE_ON_RE = /^끼어들기\s*(켜|켜\s*줘|켜기|모드|on)[.!?\s]*$/i;
const BARGE_OFF_RE = /^(끼어들기\s*(꺼|꺼\s*줘|끄기|off)|주고\s*받기(\s*모드)?)[.!?\s]*$/i;
const BARGE_SEGMENT_MS =700; // barge-in: a pause this long ends a segment worth checking
const BARGE_MAX_SEGMENT_MS = 3000; // ...or this much speech without one
const BARGE_MIN_SPEECH_MS =400; // shorter blips are not checked
const BARGE_MIN_LOUD_CHUNKS = 5; // ~0.4s above the echo floor: our own voice only peaks over it now and then
const ECHO_OVERLAP = 0.6; // share of a segment's syllable pairs found in the reply = our own voice
const MAX_CLIP_MS = 60000;
const PREROLL_MS = 300; // kept before the first speech so the onset is not cut
const MAX_SPEAK_CHARS = 600; // a spoken summary; the full reply is on screen
const MAX_SPEAK_SENTENCES = 3; // of the opening paragraph
const MAX_SPEAK_POINTS = 6; // list items summarized by their first sentence
const LIST_ITEM_RE = /^[ ]{0,1}([-*+]|\d+[.)])\s/;
const MIN_GIST_CHARS = 40; // a bare "네." opening pulls in the next paragraph
const TTS_VOICE = "F2"; // Supertonic voice (M1–M5 male, F1–F5 female)

function loadSpeakEnabled() {
  try {
    return localStorage.getItem(SPEAK_STORAGE_KEY) === "1";
  } catch {
    return false;
  }
}

function loadBargeEnabled() {
  try {
    return localStorage.getItem(BARGE_STORAGE_KEY) !== "0";
  } catch {
    return true;
  }
}

export const voice = $state({
  status: "unknown", // unknown | missing | installing | ready | unsupported
  installPercent: 0,
  installStage: "",
  installDoneMB: 0,
  installTotalMB: 0,
  confirmPaneId: null, // pane showing the inline "download?" prompt
  paneId: null, // pane currently recording / transcribing
  convPaneId: null, // pane in conversation mode
  phase: "idle", // idle | listening | transcribing
  level: 0, // 0..1 mic level while listening
  speakEnabled: loadSpeakEnabled(),
  bargeEnabled: loadBargeEnabled(), // conversation mode: talking over the reading interrupts it
  speaking: false,
  fast: false, // recognition runs on the resident GPU server (~0.3 s a clip)
  heard: "", // conversation mode: what was last sent, shown while the reply is awaited
  problem: "", // conversation mode: why the mic is not listening (shown by the indicator)
  note: "", // last error / hint, shown above the composer
  notePaneId: null,
});

let recorder = null; // active recording session
let resumeTimer = null; // conversation mode: waits for the reading to finish
let micFailures = 0; // conversation mode: mic opens failed in a row
const MAX_MIC_FAILURES = 3;
const pendingSpeech = new Map(); // key -> reply texts held until the turn is done

// vlog records a decision about what was heard in the app's diagnostic log.
function vlog(msg) {
  Events.Emit("voice:log", msg);
}

function quote(text, n = 40) {
  const s = String(text || "");
  return `"${s.length > n ? `${s.slice(0, n)}…` : s}"`;
}

// Host status lines ("⏳ 작업 시작…", "⏳ 작업 진행 중… (2분 경과)", the
// "📊 이번 턴 … · $0.12" usage footer, the "📂 project · 💬 title" routing
// header, queue notices) arrive as ordinary text frames. They all lead with an
// emoji and are not part of the answer, so they are never read out — except
// warnings and errors, which are worth hearing.
const STATUS_LINE_RE = /^\s*(?!⚠|❌)\p{Extended_Pictographic}/u;

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
    voice.fast = s.fast === true;
  } catch {
    voice.status = "unsupported";
  }
}

Events.On("voice:install", (event) => {
  const d = event.data || {};
  if (d.stage === "gpu") return; // the optional GPU build, fetched after speech recognition already works
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
  voice.installDoneMB = Math.round((d.done || 0) / 1048576);
  voice.installTotalMB = d.total > 0 ? Math.round(d.total / 1048576) : 0;
});

// The download prompt is shown inline above the composer instead of with
// window.confirm, which is modal in WebView2 and freezes the whole window.
function askInstallVoice(paneId) {
  voice.confirmPaneId = paneId;
}

export function cancelInstallVoice() {
  voice.confirmPaneId = null;
}

export async function installVoice() {
  const paneId = voice.confirmPaneId;
  voice.confirmPaneId = null;
  voice.notePaneId = paneId;
  voice.status = "installing";
  voice.installPercent = 0;
  voice.installDoneMB = 0;
  voice.installTotalMB = 0;
  voice.installStage = "whisper 엔진";
  try {
    await VoiceService.InstallVoice();
  } catch (error) {
    voice.status = "missing";
    setNote(paneId, `음성 인식 설치 실패: ${error}`);
  }
}

// ---- conversation mode ----

export async function toggleConversation(paneId) {
  if (voice.convPaneId === paneId) {
    endConversation("음성 대화를 끝냈습니다.");
    return;
  }
  clearVoiceNote();
  if (voice.status === "unknown") await refreshVoiceStatus();
  if (voice.status !== "ready") {
    await toggleVoiceInput(paneId); // shows the install prompt or the reason
    return;
  }
  if (voice.phase !== "idle") return;
  if (voice.convPaneId) endConversation();
  voice.convPaneId = paneId;
  saveConversation(paneId);
  await startRecording(paneId);
}

// Conversation mode survives a restart: it is remembered until ended on
// purpose, and resumed on the same pane once that pane is connected again.
function saveConversation(paneId) {
  try {
    if (paneId) localStorage.setItem(CONV_STORAGE_KEY, paneId);
    else localStorage.removeItem(CONV_STORAGE_KEY);
  } catch {}
}

function savedConversationPane() {
  try {
    return localStorage.getItem(CONV_STORAGE_KEY);
  } catch {
    return null;
  }
}

async function resumeSavedConversation() {
  const saved = savedConversationPane();
  if (!saved) return;
  for (let i = 0; i < 120 && !voice.convPaneId; i++) {
    const paneId = pane(saved) ? saved : chat.focusedPaneId;
    if (paneId && pane(paneId)?.target && chat.connected && voice.phase === "idle" && !voice.speaking) {
      await toggleConversation(paneId);
      return;
    }
    await new Promise((r) => setTimeout(r, 1000));
  }
}

export function endConversation(note) {
  const paneId = voice.convPaneId;
  voice.convPaneId = null;
  voice.heard = "";
  voice.problem = "";
  micFailures = 0;
  saveConversation(null);
  clearTimeout(resumeTimer);
  resumeTimer = null;
  if (recorder) stopRecording(false);
  stopSpeaking();
  if (note && paneId) setNote(paneId, note);
}

// voice:state tells the Go side (and the indicator window shown while the app
// is in the tray) whether conversation mode is on and what it is doing.
$effect.root(() => {
  $effect(() => {
    const active = !!voice.convPaneId;
    let phase = "waiting";
    // What was sent is shown while waiting, so a mishearing is caught at once.
    let label = voice.heard ? `${quote(voice.heard, 30)} 답변 기다리는 중` : "답변을 기다리는 중";
    if (voice.problem) [phase, label] = ["problem", voice.problem];
    else if (voice.speaking) [phase, label] = ["speaking", "답변을 읽는 중"];
    else if (voice.phase === "listening") [phase, label] = ["listening", "듣는 중 — 말씀하세요"];
    else if (voice.phase === "transcribing") [phase, label] = ["transcribing", "알아듣는 중"];
    Events.Emit("voice:state", { active, phase, label: `🗣 ${label}`, barge: voice.bargeEnabled });
  });
});

// The tray menu's "음성 대화" works like 🗣 on the focused pane, so a
// conversation can start while the window is hidden.
Events.On("tray:conversation", () => {
  const paneId = voice.convPaneId || chat.focusedPaneId;
  if (paneId) void toggleConversation(paneId);
});

// Hiding the window to the tray starts conversation mode on the focused pane,
// so the tray is a voice-only way of using the app.
Events.On("window:tray", (event) => {
  if (!event.data?.hidden || voice.convPaneId) return;
  const paneId = chat.focusedPaneId;
  if (paneId && pane(paneId)?.target) void toggleConversation(paneId);
});

// The tray menu's and the indicator's "읽는 중 끼어들기" switch between
// interrupting the reading by talking and plainly taking turns.
export function setBargeEnabled(on) {
  voice.bargeEnabled = on;
  try {
    localStorage.setItem(BARGE_STORAGE_KEY, on ? "1" : "0");
  } catch {}
  if (!on && recorder?.barge) {
    stopRecording(false);
    resumeListening(); // the turn-taking mic opens after the reading
  }
}

Events.On("tray:barge", () => setBargeEnabled(!voice.bargeEnabled));

// resumeListening opens the mic again once the reply has been read out.
function resumeListening(delay = 200) {
  clearTimeout(resumeTimer);
  resumeTimer = setTimeout(() => {
    resumeTimer = null;
    const paneId = voice.convPaneId;
    if (!paneId || recorder) return; // a barge-in recorder keeps listening by itself
    const busy =
      voice.speaking ||
      (typeof speechSynthesis !== "undefined" && (speechSynthesis.speaking || speechSynthesis.pending));
    if (busy || voice.phase !== "idle") {
      resumeListening(300);
      return;
    }
    void startRecording(paneId);
  }, delay);
}

setTurnDoneListener((key) => {
  // The answer is read only once the turn is over, so the in-between texts of
  // a turn never come out as separate fragments. Only the last is read: the
  // ones before it are "looking into it…" notes sent while working.
  const texts = pendingSpeech.get(key);
  pendingSpeech.delete(key);
  if (texts?.length) speak(texts[texts.length - 1]);
  const p = voice.convPaneId && pane(voice.convPaneId);
  if (p?.target && targetKey(p.target) === key) resumeListening();
});

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
    askInstallVoice(paneId);
    return;
  }
  await startRecording(paneId);
}

// A barge-in recording runs while a reply is being read in conversation mode.
// It cuts the audio into short segments and transcribes each: one that matches
// the reply is our own voice coming back through the speakers and is dropped;
// anything else is the user talking over the reply, which stops the reading.
// Once the reading ends the same recording carries on as a normal one, with
// what the user already said kept as its prefix.
async function startRecording(paneId, barge = false) {
  if (!barge) {
    stopSpeaking();
    // The GPU server comes up a few seconds after the app, and is replaced if it fails.
    void refreshVoiceStatus();
    if (recorder) stopRecording(false); // a barge-in recorder the reading left behind
  }
  clearTimeout(resumeTimer);
  resumeTimer = null;
  let stream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
  } catch (error) {
    if (barge) return; // the normal recording after the reading reports it
    const reason = `마이크를 열 수 없습니다: ${error?.message || error}`;
    vlog(`mic failed: ${error?.message || error}`);
    // A headset being switched or another app holding the mic is usually over
    // in a few seconds; conversation mode waits it out before giving up.
    if (voice.convPaneId === paneId && ++micFailures < MAX_MIC_FAILURES) {
      voice.problem = "마이크를 다시 여는 중…";
      resumeListening(CONV_RETRY_MS);
      return;
    }
    micFailures = 0;
    voice.problem = "";
    if (voice.convPaneId) endConversation();
    setNote(paneId, reason);
    return;
  }
  micFailures = 0;
  voice.problem = "";
  if (!barge) voice.heard = "";
  if (barge && (recorder || voice.convPaneId !== paneId || !voice.speaking)) {
    stream.getTracks().forEach((t) => t.stop());
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
    loudChunks: 0,
    lastSpeechAt: 0,
    noiseFloor: 0,
    noiseSamples: 0,
    startedAt,
    barge, // still checking segments against the reply being read
    firstSpeechAt: 0,
    prefix: "", // what the user said over the reading
    earlyFor: 0, // lastSpeechAt of the pause already tried as the end
    bargeDone: Promise.resolve(), // segment checks in flight
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
    if (rec.barge && !voice.speaking) {
      // The reading is over: from here on this is an ordinary recording. A
      // segment still open is mostly the reading itself (it has no long
      // pauses), so it is checked like any other rather than carried over.
      if (rec.firstSpeechChunk >= 0) {
        if (rec.lastSpeechAt - rec.firstSpeechAt >= BARGE_MIN_SPEECH_MS && rec.loudChunks >= BARGE_MIN_LOUD_CHUNKS) checkBargeSegment(rec, takeClip(rec));
        rec.chunks = [];
        rec.firstSpeechChunk = -1;
      }
      rec.loudChunks = 0;
      rec.barge = false;
      rec.startedAt = now;
      voice.paneId = paneId;
      voice.phase = "listening";
    }
    // While our own voice plays, the speakers alone raise the mic level by a
    // learned share of what is playing; only sound well above that counts.
    const ref = refLevel(now);
    if (ref > 0.01) noteEchoRatio(rms / ref);
    const echoFloor = echoGain !== null ? echoGain * ref * ECHO_MARGIN : 0;
    const threshold = Math.max(0.012, rec.noiseFloor * 3, echoFloor);
    if (rms > threshold) {
      if (rec.firstSpeechChunk < 0) {
        rec.firstSpeechChunk = rec.chunks.length - 1;
        rec.firstSpeechAt = now;
      }
      rec.lastSpeechAt = now;
      rec.loudChunks++;
    }
    if (rec.barge) {
      // The reading rarely pauses long enough to end a segment, so one is also
      // cut after a few seconds — otherwise talking over it is only noticed
      // when the reading ends.
      const ended = now - rec.lastSpeechAt > BARGE_SEGMENT_MS || now - rec.firstSpeechAt > BARGE_MAX_SEGMENT_MS;
      if (rec.firstSpeechChunk >= 0 && ended) {
        if (rec.lastSpeechAt - rec.firstSpeechAt >= BARGE_MIN_SPEECH_MS && rec.loudChunks >= BARGE_MIN_LOUD_CHUNKS) checkBargeSegment(rec, takeClip(rec));
        rec.chunks = [];
        rec.firstSpeechChunk = -1;
        rec.loudChunks = 0;
      } else if (rec.firstSpeechChunk < 0 && rec.chunks.length > 8) {
        rec.chunks.splice(0, rec.chunks.length - 8); // keep only the pre-roll
      }
      return;
    }
    if (rec.firstSpeechChunk >= 0 && voice.fast && voice.convPaneId === paneId && now - rec.lastSpeechAt > EARLY_STOP_MS && rec.earlyFor !== rec.lastSpeechAt) tryEarlyStop(rec);
    if (rec.firstSpeechChunk >= 0 && now - rec.lastSpeechAt > (voice.convPaneId === paneId ? CONV_SILENCE_STOP_MS : SILENCE_STOP_MS)) stopRecording(true);
    else if (rec.firstSpeechChunk < 0 && rec.prefix && now - Math.max(rec.lastSpeechAt, rec.startedAt) > CONV_SILENCE_STOP_MS) stopRecording(true);
    else if (rec.firstSpeechChunk < 0 && now - rec.startedAt > (voice.convPaneId === paneId ? CONV_NO_SPEECH_MS : NO_SPEECH_MS)) {
      stopRecording(false);
      // Conversation mode never times out: it only ends on "그만", 🗣 or the
      // tray menu, so the app can be used by voice alone from the tray.
      if (voice.convPaneId === paneId) resumeListening(0);
      else setNote(paneId, "음성이 감지되지 않았습니다. 🎤 를 다시 눌러 말해 주세요.");
    } else if (now - rec.startedAt > MAX_CLIP_MS) stopRecording(true);
  };
  source.connect(node);
  node.connect(ctx.destination); // the processor only runs while connected; its output is silence

  voice.level = 0;
  if (barge) return; // "listening" only once the reading is over, so the indicator says "읽는 중"
  voice.paneId = paneId;
  voice.phase = "listening";
}

// takeClip encodes the audio from just before the first speech as whisper input.
function takeClip(rec) {
  const rate = rec.ctx.sampleRate;
  const chunkMs = (4096 / rate) * 1000;
  const from = Math.max(0, rec.firstSpeechChunk - Math.ceil(PREROLL_MS / chunkMs));
  return encodeWav(downsample(concat(rec.chunks.slice(from)), rate, TARGET_RATE), TARGET_RATE);
}

// tryEarlyStop transcribes the clip at a short pause in conversation mode; a
// finished sentence is sent at once instead of after the full
// CONV_SILENCE_STOP_MS wait. Speech resuming before it answers keeps the
// recording going as usual.
function tryEarlyStop(rec) {
  const at = rec.lastSpeechAt;
  rec.earlyFor = at;
  const wav = takeClip(rec);
  void (async () => {
    await rec.bargeDone;
    let text = "";
    try {
      text = (await VoiceService.Transcribe(toBase64(wav))).trim();
    } catch {
      return;
    }
    if (recorder !== rec || rec.lastSpeechAt !== at || !SENTENCE_END_RE.test(text) || GOES_ON_RE.test(text)) return;
    vlog(`early end ${quote(text)}`);
    stopRecording(true, text);
  })();
}

// checkBargeSegment transcribes one segment heard during the reading, one at a
// time, and decides whether it was the user or our own voice.
function checkBargeSegment(rec, wavBytes) {
  rec.bargeDone = rec.bargeDone.then(async () => {
    if (recorder !== rec && !rec.stopped) return;
    let text = "";
    try {
      text = (await VoiceService.Transcribe(toBase64(wavBytes))).trim();
    } catch {
      return;
    }
    if (!text || (recorder !== rec && !rec.stopped)) return;
    if (BARGE_HUSH_RE.test(text)) {
      vlog(`barge-in hush ${quote(text)}`);
      stopSpeaking(); // "잠깐" only stops the reading
      return;
    }
    if (CONV_STOP_RE.test(text)) {
      vlog(`barge-in stop ${quote(text)}`);
      endConversation("음성 대화를 끝냈습니다.");
      return;
    }
    if (isEcho(text, replyText)) {
      vlog(`echo dropped ${quote(text)}`);
      return;
    }
    vlog(`barge-in ${quote(text)} (echo gain ${echoGain?.toFixed(3) ?? "unlearned"})`);
    stopSpeaking();
    rec.prefix = rec.prefix ? `${rec.prefix} ${text}` : text;
    rec.lastSpeechAt = performance.now();
  });
}

function normalizeForEcho(s) {
  return String(s || "").normalize("NFC").toLowerCase().replace(/[^\p{L}\p{N}]/gu, "");
}

// isEcho: most of the segment's character pairs also occur in the reply being
// read. Whisper mishears our own voice a little, so this is fuzzy on purpose.
function isEcho(text, reply, overlap = ECHO_OVERLAP) {
  const t = normalizeForEcho(text);
  const r = normalizeForEcho(reply);
  if (t.length < 2) return true; // too short to tell — not worth stopping the reply for
  if (!r) return false;
  const pairs = new Set();
  for (let i = 0; i + 1 < r.length; i++) pairs.add(r.slice(i, i + 2));
  let hit = 0;
  for (let i = 0; i + 1 < t.length; i++) if (pairs.has(t.slice(i, i + 2))) hit++;
  const pairShare = hit / (t.length - 1);
  if (pairShare >= overlap) return true;
  // A misheard echo ("여섯 군데를 고쳤어요" → "6군대를 붙였어요") breaks many
  // pairs but keeps most single syllables; the user's own words rarely do both.
  const chars = new Set(r);
  let charHit = 0;
  for (const c of t) if (chars.has(c)) charHit++;
  return pairShare >= overlap * 0.65 && charHit / t.length >= 0.7;
}

// heard is the clip's text when tryEarlyStop already has it.
function stopRecording(transcribe, heard = null) {
  const rec = recorder;
  if (!rec) return;
  recorder = null;
  rec.stopped = transcribe; // a segment check still in flight may add to the prefix
  rec.node.onaudioprocess = null;
  try {
    rec.source.disconnect();
    rec.node.disconnect();
  } catch {}
  rec.stream.getTracks().forEach((t) => t.stop());
  const rate = rec.ctx.sampleRate;
  void rec.ctx.close();
  voice.level = 0;

  if (!transcribe || (rec.firstSpeechChunk < 0 && !rec.prefix)) {
    voice.phase = "idle";
    voice.paneId = null;
    return;
  }
  const wav = rec.firstSpeechChunk >= 0 ? takeClip(rec) : null;
  void transcribeClip(rec.paneId, wav, rec, heard);
}

// transcribeClip sends what was said: the barge-in prefix (once its checks
// are done) followed by the clip, either of which may be missing.
async function transcribeClip(paneId, wavBytes, rec = null, known = null) {
  voice.paneId = paneId;
  voice.phase = "transcribing";
  const conv = () => voice.convPaneId === paneId;
  let relisten = false;
  let retry = false;
  try {
    if (rec) await rec.bargeDone;
    let heard = known ?? (wavBytes ? (await VoiceService.Transcribe(toBase64(wavBytes))).trim() : "");
    // Safety net: a clip that is just the reply read back through the speakers
    // (its tail, or all of it) is never sent as the user's words. Speech that
    // starts as the reading ends is most likely the last of it still coming out
    // of the speakers, so even a short, half-heard fragment of it is dropped.
    const tail = rec && rec.firstSpeechAt - readingEndedAt < ECHO_TAIL_MS;
    if (conv() && heard && replyText && !CONV_STOP_RE.test(heard)) {
      // Sentence by sentence: the user often starts talking while the reading's
      // last words are still coming out, and their part must survive.
      const thr = tail ? 0.4 : ECHO_OVERLAP;
      const kept = sentencesOf(heard).filter((s) => !((tail || normalizeForEcho(s).length >= 6) && isEcho(s, replyText, thr)));
      const rest = kept.join("").trim();
      if (rest !== heard) vlog(`echo dropped${tail ? " (tail)" : ""} ${quote(heard)} kept ${quote(rest)}`);
      heard = rest;
    }
    const text = [rec?.prefix, heard].filter(Boolean).join(" ");
    if (!text) {
      if (conv()) relisten = true;
      else setNote(paneId, "말씀을 알아듣지 못했습니다. 다시 한 번 말해 주세요.");
      return;
    }
    if (conv() && CONV_STOP_RE.test(text)) {
      endConversation("음성 대화를 끝냈습니다.");
      return;
    }
    if (conv() && (BARGE_ON_RE.test(text) || BARGE_OFF_RE.test(text))) {
      const on = BARGE_ON_RE.test(text);
      setBargeEnabled(on);
      speak(on ? "끼어들기를 켰어요. 제가 읽는 중에도 말씀하시면 멈추고 들어요." : "주고받기로 바꿨어요. 제가 다 읽은 뒤에 말씀해 주세요.");
      relisten = true;
      return;
    }
    const p = pane(paneId);
    if (!p?.target) {
      setNote(paneId, `대화가 선택되지 않아 보내지 못했습니다: "${text}"`);
      return;
    }
    const existing = p.composerText.trim();
    setPaneComposerValue(paneId, existing ? `${existing} ${text}` : text);
    if (chat.connected) {
      vlog(`sent ${quote(text)}`);
      if (conv()) voice.heard = text;
      await sendFromPane(paneId);
    }
    else {
      setNote(paneId, "aglink 에 연결되어 있지 않아 입력창에만 넣었습니다.");
      if (conv()) retry = true;
    }
  } catch (error) {
    vlog(`transcribe failed: ${error?.message || error}`);
    setNote(paneId, `음성 인식 실패: ${error?.message || error}`);
    if (conv()) retry = true;
  } finally {
    voice.phase = "idle";
    voice.paneId = null;
    if (relisten && conv()) resumeListening(0);
    else if (retry && conv()) resumeListening(CONV_RETRY_MS);
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

// ---- echo reference ----
//
// The mic's own echo cancellation cannot help: it only knows what the page
// plays, and the reply plays through Windows (PlayWav). But we know exactly
// what is playing and when, so the mic level our voice alone produces can be
// predicted: the clip's loudness envelope times a room gain learned from the
// mic while it plays. Sound below a margin over that never starts a segment,
// so our own voice is not transcribed at all; the text comparison (isEcho)
// stays as the second line.
const REF_FRAME_MS = 20;
const REF_BEFORE_MS = 450; // speaker + mic latency: the mic hears now what played this long ago
const REF_AFTER_MS = 100; // PlayWav starts a little after it is called
const ECHO_MARGIN = 2.5; // the user's voice is this much above our echo
let playRefs = []; // {startAt, endAt, env} for clips playing or just played
const echoRatios = []; // recent mic/reference level ratios
// Their median: the room's echo gain. The reference is the loudest frame in a
// latency window, so echo-only ratios already sit below the true gain; a low
// percentile made the floor too low to keep our voice out. Kept across
// restarts so the first reading after one is gated too.
const ECHO_GAIN_KEY = "aglink.voice.echoGain";
let echoGain = (() => {
  try {
    const v = parseFloat(localStorage.getItem(ECHO_GAIN_KEY));
    return v > 0 ? v : null;
  } catch {
    return null;
  }
})();

function noteEchoRatio(r) {
  echoRatios.push(r);
  if (echoRatios.length > 300) echoRatios.shift();
  // The user talking over the reply only raises ratios; a low percentile is
  // the echo-only level as long as they talk for under ~2/3 of the reading.
  if (echoRatios.length >= 20 && echoRatios.length % 10 === 0) {
    const sorted = [...echoRatios].sort((a, b) => a - b);
    echoGain = sorted[Math.floor(sorted.length * 0.5)];
    try {
      localStorage.setItem(ECHO_GAIN_KEY, String(echoGain));
    } catch {}
  }
}

function refLevel(now) {
  let level = 0;
  for (const ref of playRefs) {
    const t = now - ref.startAt;
    if (ref.endAt && now > ref.endAt + REF_BEFORE_MS) continue;
    const from = Math.max(0, Math.floor((t - REF_BEFORE_MS) / REF_FRAME_MS));
    const to = Math.min(ref.env.length - 1, Math.floor((t + REF_AFTER_MS) / REF_FRAME_MS));
    for (let i = from; i <= to; i++) if (ref.env[i] > level) level = ref.env[i];
  }
  return level;
}

// wavEnvelope: the RMS of each REF_FRAME_MS of a 16-bit or float PCM WAV.
function wavEnvelope(b64) {
  try {
    const bin = atob(b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    const v = new DataView(bytes.buffer);
    let fmt = null;
    for (let off = 12; off + 8 <= bytes.length; ) {
      const id = bin.slice(off, off + 4);
      const size = v.getUint32(off + 4, true);
      const body = off + 8;
      if (id === "fmt ") fmt = { format: v.getUint16(body, true), channels: v.getUint16(body + 2, true), rate: v.getUint32(body + 4, true), bits: v.getUint16(body + 14, true) };
      if (id === "data" && fmt) {
        const step = (fmt.bits / 8) * fmt.channels;
        const n = Math.floor(Math.min(size, bytes.length - body) / step);
        const per = Math.max(1, Math.round((fmt.rate * REF_FRAME_MS) / 1000));
        const env = new Float32Array(Math.ceil(n / per));
        const float = fmt.format === 3 && fmt.bits === 32;
        for (let f = 0; f < env.length; f++) {
          let sum = 0;
          const end = Math.min(n, (f + 1) * per);
          for (let i = f * per; i < end; i++) {
            const s = float ? v.getFloat32(body + i * step, true) : v.getInt16(body + i * step, true) / 32768;
            sum += s * s;
          }
          env[f] = Math.sqrt(sum / Math.max(1, end - f * per));
        }
        return env;
      }
      off = body + size + (size & 1);
    }
  } catch {}
  return null;
}

function addPlayRef(b64) {
  const now = performance.now();
  playRefs = playRefs.filter((r) => !(r.endAt && now > r.endAt + 2000));
  const env = wavEnvelope(b64);
  if (!env) return null;
  const ref = { startAt: now, endAt: now + env.length * REF_FRAME_MS, env };
  playRefs.push(ref);
  return ref;
}

export function stopSpeaking() {
  const cut = performance.now();
  for (const ref of playRefs) if (ref.endAt > cut) ref.endAt = cut;
  speakGen++;
  if (nativePlaying) {
    nativePlaying = false;
    VoiceService.StopWav().catch(() => {});
  }
  if (audio) {
    audio.pause();
    audio = null;
  }
  if (typeof speechSynthesis !== "undefined") speechSynthesis.cancel();
  voice.speaking = false;
}

function koreanVoice() {
  const voices = speechSynthesis.getVoices().filter((v) => v.lang?.toLowerCase().startsWith("ko"));
  return voices.find((v) => /natural/i.test(v.name)) || voices[0] || null;
}

// cleanProse flattens one markdown paragraph to plain spoken text. Anything
// there to be looked at rather than heard — links, URLs, paths, inline code
// with dots or slashes — is dropped.
function cleanProse(p) {
  let s = p;
  s = s.replace(/^\s*([-*+]|\d+[.)])\s+\[[^\]]*\]\([^)]*\)\s*$/gm, " "); // link-only list items
  s = s.replace(/!\[[^\]]*\]\([^)]*\)/g, " ");
  s = s.replace(/\[([^\]]+)\]\([^)]*\)/g, "$1");
  s = s.replace(/https?:\/\/\S+/g, " ");
  s = s.replace(/`[^`]*[\/:.][^`]*`/g, " "); // `paths`, `file.go:12`, `a.b()`
  s = s.replace(/`([^`]+)`/g, "$1");
  s = s.replace(/(?:[A-Za-z]:)?(?:[\w.~-]*[\/])+[\w.-]+/g, " "); // bare paths
  s = s.replace(/^\s{0,3}(>|[-*+]|\d+[.)])\s+/gm, "");
  s = s.replace(/(\*\*|__|~~|\*|_)(\S[^*_~]*?)\1/g, "$2");
  s = s.replace(/(\d)\s*~\s*(\d)/g, "$1에서 $2"); // "4~6초" is read "4에서 6초"
  s = s.replace(/[#*_~>|]+/g, " ");
  s = s.replace(/\p{Extended_Pictographic}️?/gu, " ");
  s = s.replace(/\s+([.,!?])/g, "$1");
  s = s.replace(/\s+/g, " ").trim();
  return /[\p{L}\p{N}]/u.test(s) ? s : "";
}

// speechText picks what is worth hearing from a reply, as a spoken summary of
// all of it: the opening prose paragraph (plus the next one when the first is a
// bare "네."), the first sentence of each top-level list item — the point it
// makes — and a closing question to the user. Code, tables, headings and a
// trailing "출처:" list never count.
export function speechText(md) {
  let s = String(md || "").replace(/\r\n/g, "\n");
  s = s.replace(/^\s*(출처|참고|Sources?|References?)\s*:?\s*$[\s\S]*/im, ""); // trailing sources block
  s = s.replace(/```[\s\S]*?```/g, "\n\n");
  s = s.replace(/^\s*\|.*\|\s*$/gm, ""); // tables
  s = s.replace(/^\s{0,3}#{1,6}\s.*$/gm, "\n"); // headings
  const paras = s.split(/\n\s*\n/);
  let opening = "";
  let i = 0;
  for (; i < paras.length && opening.length < MIN_GIST_CHARS; i++) {
    if (LIST_ITEM_RE.test(paras[i])) break; // a reply that opens with a list
    const prose = cleanProse(paras[i]);
    if (prose) opening = opening ? `${opening} ${prose}` : prose;
  }
  let out = sentencesOf(opening).slice(0, MAX_SPEAK_SENTENCES).join("");
  const points = [];
  let closing = "";
  for (const para of paras.slice(i)) {
    const items = para.split(/\n(?=[ ]{0,1}(?:[-*+]|\d+[.)])\s)/).filter((l) => LIST_ITEM_RE.test(l));
    if (items.length) {
      for (const item of items) {
        const point = sentencesOf(cleanProse(item.split("\n")[0]))[0];
        if (point) points.push(point);
      }
      closing = "";
      continue;
    }
    const prose = cleanProse(para);
    if (prose) closing = prose;
  }
  for (const point of points.slice(0, MAX_SPEAK_POINTS)) {
    if ((out + point).length > MAX_SPEAK_CHARS) break;
    out += ` ${point}`;
  }
  // A question at the end ("진행할까요?") is what the user has to answer.
  const ask = sentencesOf(closing).filter((x) => /[?？]\s*$/.test(x)).pop();
  if (ask && !out.includes(ask.trim())) out += ` ${ask}`;
  out = out.replace(/\s+/g, " ").trim();
  if (out.length > MAX_SPEAK_CHARS + 120) out = out.slice(0, MAX_SPEAK_CHARS + 120);
  return out;
}

function sentencesOf(text) {
  return (text || "").match(/[^.!?。？！]+[.!?。？！]*\s*/g) || [];
}

// splitSentences keeps each utterance short: speechSynthesis stalls on long
// ones, and with Supertonic the first sentence starts playing sooner.
function splitSentences(text) {
  const parts = text.match(/[^.!?。？！\n]+[.!?。？！]*\s*/g) || [text];
  const out = [];
  let cur = "";
  for (const p of parts) {
    // The first utterance is kept to one sentence: nothing plays until it is
    // synthesized, while the later ones are made during playback.
    if (cur && (!out.length || (cur + p).length > 120)) {
      out.push(cur.trim());
      cur = "";
    }
    cur += p;
  }
  if (cur.trim()) out.push(cur.trim());
  return out;
}

let speakGen = 0; // bumped by stopSpeaking to abandon a reading in progress
let audio = null; // the Supertonic clip playing now
let replyText = ""; // what is (or was last) read aloud — barge-in drops segments matching it
let readingEndedAt = -Infinity; // when the last reading finished on its own
let nativePlaying = false; // VoiceService.PlayWav is playing a clip
let nativeOK = true; // false once PlayWav has failed — use <audio> from then on
let ttsReady = null; // null until VoiceService.TTSStatus has answered

async function supertonicReady() {
  if (ttsReady === null) {
    try {
      ttsReady = JSON.parse(await VoiceService.TTSStatus()).ready === true;
    } catch {
      ttsReady = false;
    }
  }
  return ttsReady;
}

// playWav plays through the OS first: the page cannot play <audio> while the
// window is hidden in the tray.
async function playWav(b64, gen) {
  if (gen !== speakGen) return;
  const ref = addPlayRef(b64);
  if (nativeOK) {
    nativePlaying = true;
    try {
      await VoiceService.PlayWav(b64);
      if (ref && ref.endAt > performance.now()) ref.endAt = performance.now();
      return;
    } catch (err) {
      console.warn("[voice] native playback failed, using <audio>", err);
      nativeOK = false;
    } finally {
      if (gen === speakGen) nativePlaying = false;
    }
  }
  return playWavPage(b64, gen);
}

function playWavPage(b64, gen) {
  return new Promise((resolve) => {
    if (gen !== speakGen) return resolve();
    const a = new Audio(`data:audio/wav;base64,${b64}`);
    audio = a;
    a.onended = a.onerror = () => {
      if (audio === a) audio = null;
      resolve();
    };
    a.play().catch(() => resolve());
  });
}

// speakSupertonic synthesizes sentence by sentence, the next one while the
// current one plays. False means it could not start, so the caller falls back.
async function speakSupertonic(sentences, gen) {
  let b64;
  try {
    b64 = await VoiceService.Synthesize(sentences[0], TTS_VOICE);
  } catch (err) {
    vlog(`Supertonic failed, Windows voice instead: ${err?.message || err}`);
    ttsReady = null; // asked again next time — the Go side retries the server after a while
    return false;
  }
  try {
    for (let i = 0; gen === speakGen; i++) {
      const next = i + 1 < sentences.length ? VoiceService.Synthesize(sentences[i + 1], TTS_VOICE) : null;
      next?.catch(() => {}); // awaited below; this only silences an abandoned one
      if (b64) await playWav(b64, gen);
      if (!next) break;
      // One sentence that fails to synthesize is skipped, not the rest of the reply.
      b64 = await next.catch((err) => {
        vlog(`Supertonic skipped a sentence: ${err?.message || err}`);
        return null;
      });
    }
  } catch (err) {
    console.warn("[voice] Supertonic sentence failed", err);
  } finally {
    if (gen === speakGen) {
      voice.speaking = false;
      readingEndedAt = performance.now();
      if (echoGain !== null) vlog(`echo gain ${echoGain.toFixed(3)} (${echoRatios.length} samples)`);
    }
  }
  return true;
}

function speakWindows(sentences) {
  if (typeof speechSynthesis === "undefined") {
    voice.speaking = false;
    return;
  }
  const v = koreanVoice();
  voice.speaking = true;
  sentences.forEach((sentence, i) => {
    const u = new SpeechSynthesisUtterance(sentence);
    u.lang = "ko-KR";
    if (v) u.voice = v;
    u.rate = 1.05;
    if (i === sentences.length - 1)
      u.onend = u.onerror = () => {
        voice.speaking = false;
        readingEndedAt = performance.now();
      };
    speechSynthesis.speak(u);
  });
}

export async function speak(md) {
  // The indicator shows the whole reply while only its gist is read.
  if (voice.convPaneId || savedConversationPane()) Events.Emit("voice:reply", { text: String(md || "").trim() });
  const text = speechText(md);
  if (!text) return;
  stopSpeaking();
  const gen = speakGen;
  const sentences = splitSentences(text);
  replyText = text;
  voice.speaking = true; // covers the first synthesis, so the mic waits
  const paneId = voice.convPaneId;
  // A reply can land while the mic is already listening (one finishing after a
  // restart, or a !command answer); a recording with nothing said yet is
  // stopped, so it does not take in the whole reading.
  if (recorder && !recorder.barge && recorder.firstSpeechChunk < 0 && !recorder.prefix) stopRecording(false);
  if (paneId && voice.bargeEnabled && !recorder && voice.phase === "idle") void startRecording(paneId, true);
  else if (paneId && !recorder) resumeListening(); // taking turns: the mic opens after the reading
  if ((await supertonicReady()) && (await speakSupertonic(sentences, gen))) return;
  if (gen !== speakGen) return;
  speakWindows(sentences);
}

setAssistantTextListener((key, text) => {
  if (STATUS_LINE_RE.test(text)) return;
  // A remembered conversation counts before it has resumed after a restart,
  // so a reply that lands in that gap is still read out.
  const convId = voice.convPaneId || savedConversationPane();
  const conv = convId && pane(convId);
  const focused = pane(chat.focusedPaneId);
  const wanted =
    (conv?.target && targetKey(conv.target) === key) ||
    (voice.speakEnabled && focused?.target && targetKey(focused.target) === key);
  if (!wanted) return;
  // A reply outside a running turn (a !command answer) has no "done" coming.
  if (!chat.working.has(key)) {
    speak(text);
    return;
  }
  pendingSpeech.set(key, [...(pendingSpeech.get(key) || []), text]);
});

// getVoices() is empty until the voice list loads; touching it starts the load.
if (typeof speechSynthesis !== "undefined") speechSynthesis.getVoices();
void refreshVoiceStatus().then(resumeSavedConversation);
