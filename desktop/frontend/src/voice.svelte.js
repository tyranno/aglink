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
import { speakNumbers } from "./spokenNumbers.js";
import { ControlService, VoiceService } from "../bindings/github.com/tyranno/aglink-desktop";
import {
  chat,
  pane,
  targetKey,
  findWebConv,
  paneConversationMeta,
  parseJSON,
  loadConversations,
  selectTargetInPane,
  setPaneComposerValue,
  sendFromPane,
  setAssistantTextListener,
  setTurnDoneListener,
} from "./paneStore.svelte.js";
import { alarms, parseAlarm, addAlarm, removeAlarm, startAlarms, fireText, confirmText, timeLabel } from "./alarms.js";
import { describeCron } from "./schedule.js";
import { parsePc, parseUsage, usageText, parseDaily, dailyDue, dayKey, clockLabel, parseMemo, MAX_MEMO_CMD_CHARS, parseTapeSummary, tapeSummaryPrompt, briefingPrompt, meetingAdvicePrompt, meetingSummaryPrompt } from "./assist.js";

const SPEAK_STORAGE_KEY = "aglink.voice.speak";
const BARGE_STORAGE_KEY = "aglink.voice.barge"; // "0": take turns, the mic waits for the reading
const CONV_STORAGE_KEY = "aglink.voice.conversation"; // pane id while conversation is on
const AWAIT_STORAGE_KEY = "aglink.voice.awaiting"; // {pane, key, at} of the spoken request still being worked on
const AWAIT_MAX_MS = 30 * 60000; // an answer never seen stops holding the conversation open after this
// A running turn's "typing" is refreshed every 2 minutes (the host heartbeat);
// a chat with none this long after connecting finished while the app was closed.
const RESUME_GRACE_MS = 150000;
const DONE_SHOW_MS = 8000; // "작업이 끝났어요" stays on the indicator this long
const TARGET_RATE = 16000;
const SILENCE_STOP_MS = 1300; // pause after speech that ends the clip
const CONV_SILENCE_STOP_MS = 2200; // conversation mode: room for "음…" mid-sentence pauses
const EARLY_STOP_MS = 800; // conversation mode with fast recognition: a pause this long is tried as the end
const SENTENCE_END_RE = /(요|니다|죠)[.!?~]*$|(까|다|네|야|지|어|아|해|래|게|자|니|군|걸)[.!?~]+$/; // a finished Korean sentence
const GOES_ON_RE = /(니까|는데|는데요)[.~]*$/; // "그러니까…", "했는데…" lead into more
// Silero VAD (VoiceService.VadProbs) next to the level gate: a chunk counts as
// speech only when it is loud enough and the model also hears a voice in it,
// so a fan, the TV's music or a door neither starts a clip nor keeps one open.
// No answer (not installed, starting, stuck) leaves the level alone to decide.
const VAD_ON = 0.5; // speech probability that starts speech
const VAD_OFF = 0.35; // ...and under which it ends (Silero's own hysteresis)
const VAD_STALE_MS = 400; // an answer older than this is not trusted
const VAD_LOG_CHUNKS = 6; // a loud stretch the VAD turned down this long (~0.5 s) is logged
// Smart Turn v3 (VoiceService.TurnEnd) at a short pause: from the sound of the
// turn so far it tells a finished sentence from a "음…" or a rising "그래서".
// Sure it is finished: sent at once. Sure it is not: the pause may run to
// TURN_HOLD_MS before the clip ends. In between, or with no model, the fixed
// pauses and tryEarlyStop's transcript check decide as before.
const TURN_PAUSE_MS = [400, 1200]; // pauses at which the turn so far is checked
const TURN_COMPLETE = 0.85; // the model's own bar is 0.5; cutting the user off is worse than waiting
const TURN_UNFINISHED = 0.3;
const TURN_HOLD_MS = 3500; // hard cap on a pause the model says is mid-thought
const TURN_MAX_S = 8; // the model reads the last 8 s
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
const ECHO_TRUST_MARGIN = 0.1; // this far over the voice bar is the user, whatever the words
// The enrolled voice scores about 0.5+ for the user, under 0.3 for others; the
// bar (voice.speakerMin) is set in the voice settings. Short clips, or the user
// over our own voice, score lower and get a bar this much lower.
const SPEAKER_MIXED_DROP = 0.1;
const SPEAKER_SHORT_S = 1.2;
// The user talking over the TV makes a long clip that is mostly the TV, and
// the clip as a whole scores far under the bar ("지금 설치해" at 0.10). Such a
// clip is scored again in short windows: the user's own stretch stands out,
// and only that stretch is sent. A window needs a little more than the bar,
// since a short stretch of someone else now and then scores high by chance.
const RESCUE_MIN_S = 3; // shorter clips are only scored whole
const RESCUE_WINDOW_S = 3; // over ADAPT_SECONDS, so a clean window also teaches the voice
const RESCUE_HOP_S = 1.5;
const RESCUE_MAX_WINDOWS = 12;
const RESCUE_EXTRA_BAR = 0.05;
// A long clip that only just passes as a whole may be several people talking
// (a 45 s family conversation scored 0.60 against a bar of 0.5). It is scored
// in windows too: unless most of them sound like the user it is dropped, and
// otherwise only the user's stretch is sent.
const LONG_CHECK_S = 10;
const LONG_CHECK_MARGIN = 0.15;
const LONG_CHECK_SHARE = 0.5;
const ENROLL_MS = 15000;
const LISTEN_ALWAYS_RE = /^(항상|계속|늘)\s*(듣기|들어|들어\s*줘|켜\s*(둬|놔|놓아))(\s*(로|으로))?\s*(해|바꿔|해\s*줘|바꿔\s*줘|줘)?[.!?~\s]*$/;
const LISTEN_WAKE_RE = /^(호출어|부르기|부르면|아글링크\s*(로|하면))\s*(로|으로)?\s*(켜|켜기|시작|바꿔|해|하자)\S*(\s*줘)?[.!?~\s]*$/;
// "부르는 이름을 아그라로 바꿔 줘": a name of the user's own to wake on.
// A bare "이름을 …로 바꿔" is too often about something else, so "부르는" (or 호출어) is required.
const WAKE_NAME_SET_RE = /^(?:(?:부르는|호출|깨우는)\s*이름|호출어)\s*(을|를)?\s*["“']?(.{1,8}?)["”']?\s*(으로|로)\s*(바꿔|정해|해|지정해|설정해)\S*(\s*(줘|주세요))?[.!?~\s]*$/;
const WAKE_NAME_ADD_RE = /^["“']?(.{1,8}?)["”']?\s*(이)?라고\s*(불러도|해도|하면|부르면)\s*(알아들어|대답해|깨어나|켜져|켜)\S*(\s*(줘|주세요))?[.!?~\s]*$/;
const MAX_CLIP_MS = 60000;
const PREROLL_MS = 300; // kept before the first speech so the onset is not cut
// How much of a reply is read: "short" (the voice setting "요약만 읽기") keeps
// to the point, "gist" is the longer spoken summary; "자세히 읽어 줘" reads it
// all ("full") but code, tables and links.
const SPEAK_LIMITS = {
  short: { chars: 220, sentences: 2, points: 3 },
  gist: { chars: 600, sentences: 3, points: 8 },
};
const MAX_FULL_CHARS = 4000;
const CUT_MARGIN = 80; // a full reading this much longer than the summary means it was cut
const LIST_ITEM_RE = /^[ ]{0,1}([-*+]|\d+[.)])\s/;
const MIN_GIST_CHARS = 40; // a bare "네." opening pulls in the next paragraph
const SETTINGS_STORAGE_KEY = "aglink.voice.settings";
export const TTS_VOICES = ["M1", "M2", "M3", "M4", "M5", "F1", "F2", "F3", "F4", "F5"]; // Supertonic: male, female
export const CLONE_VOICE = "C"; // the recorded voice (cloneVoice); only it is repainted
const isTTSVoice = (name) => TTS_VOICES.includes(name) || name === CLONE_VOICE;
export const WAKE_EVERY_TIME = -1; // each answer ends the conversation: every request starts with the name
export const VOICE_DEFAULTS = { ttsVoice: "M2", ttsSpeed: 0.98, speakerMin: 0.5, listenMode: "wake", wakeIdleMin: 3, summary: true, wakeNames: [], wakeDefault: true, textOnly: false, pagePlayback: true };
const OLD_SPEAKER_MIN = 0.3;
const OLD_SPEAKER_MIN_2 = 0.4; // default before speakerMinV 3 — a loud nearby voice scored 0.45 and got through during barge-in
const MAX_WAKE_NAMES = 5;
// How the mic waits: "wake" — off until "아글링크"; "always" — conversation is on
// from the start and stays on; "manual" — only 🗣 / Ctrl+Shift+M / the tray.
// Outside "manual", "아글링크" also brings back a conversation ended with "그만".
export const LISTEN_MODES = ["wake", "always", "manual"];
export const WAKE_IDLE_CHOICES = [WAKE_EVERY_TIME, 0, 1, 3, 5, 10]; // minutes without talk before conversation goes back to waiting; 0 = never
export const SPEED_RANGE = [0.7, 2.0];
export const SPEAKER_MIN_RANGE = [0.1, 0.7];
const SPEED_STEP = 0.1; // "더 천천히" / "더 빨리"
const FLASH_MS = 2500; // a passing note on the indicator ("다른 목소리라 넘겼어요")
const MAX_HEARD_LOG = 30; // recent clips and their voice scores, for the settings

function clamp(v, [lo, hi]) {
  return Math.min(hi, Math.max(lo, v));
}

// cleanWakeNames keeps the user's own wake names: trimmed, 2–8 letters, no repeats.
export function cleanWakeNames(list) {
  const out = [];
  for (const n of Array.isArray(list) ? list : []) {
    const name = String(n || "").replace(/[\s,.!?~"“”']/g, "");
    if (name.length >= 2 && name.length <= 8 && !out.includes(name)) out.push(name);
  }
  return out.slice(0, MAX_WAKE_NAMES);
}

function loadSettings() {
  let s = {};
  try {
    s = JSON.parse(localStorage.getItem(SETTINGS_STORAGE_KEY) || "{}") || {};
  } catch {}
  return {
    ttsVoice: isTTSVoice(s.ttsVoice) ? s.ttsVoice : VOICE_DEFAULTS.ttsVoice,
    ttsSpeed: Number.isFinite(s.ttsSpeed) ? clamp(s.ttsSpeed, SPEED_RANGE) : VOICE_DEFAULTS.ttsSpeed,
    // 0.3 and 0.4 were the old defaults: too loose — nearby talk got through.
    speakerMin:
      Number.isFinite(s.speakerMin) && !(s.speakerMinV !== 3 && (s.speakerMin === OLD_SPEAKER_MIN || s.speakerMin === OLD_SPEAKER_MIN_2))
        ? clamp(s.speakerMin, SPEAKER_MIN_RANGE)
        : VOICE_DEFAULTS.speakerMin,
    listenMode: LISTEN_MODES.includes(s.listenMode) ? s.listenMode : s.wake === false ? "manual" : VOICE_DEFAULTS.listenMode,
    wakeIdleMin: s.wakeIdleV === 2 && WAKE_IDLE_CHOICES.includes(s.wakeIdleMin) ? s.wakeIdleMin : VOICE_DEFAULTS.wakeIdleMin,
    wakeNames: cleanWakeNames(s.wakeNames),
    wakeDefault: typeof s.wakeDefault === "boolean" ? s.wakeDefault : true,
    summary: typeof s.summary === "boolean" ? s.summary : VOICE_DEFAULTS.summary,
    textOnly: typeof s.textOnly === "boolean" ? s.textOnly : VOICE_DEFAULTS.textOnly,
    pagePlayback: typeof s.pagePlayback === "boolean" ? s.pagePlayback : VOICE_DEFAULTS.pagePlayback,
  };
}

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
  hearing: false, // sound loud enough to be speech is in the clip being recorded
  speakEnabled: loadSpeakEnabled(),
  bargeEnabled: loadBargeEnabled(), // conversation mode: talking over the reading interrupts it
  speaking: false,
  enrolling: false, // recording the user's voice to tell it from others'
  readScript: "", // cloneVoice: the text to read, shown large (CloneScript.svelte)
  readUntil: 0, // when that recording ends; 0 = not started yet
  fast: false, // recognition runs on the resident GPU server (~0.3 s a clip)
  heard: "", // conversation mode: what was last sent, shown while the reply is awaited
  problem: "", // conversation mode: why the mic is not listening (shown by the indicator)
  note: "", // last error / hint, shown above the composer
  notePaneId: null,
  flash: "", // conversation mode: a passing note on the indicator
  heardLog: [], // recent clips: {at, text, score, kept, why}
  waking: false, // conversation is off and the mic waits for "아글링크"
  taping: false, // "녹음해 줘": recording until "녹음 끝내"
  tapeStartedAt: 0,
  tapeClock: "", // "03:12", shown while recording
  meeting: false, // "지금부터 듣고 있어": listens to whoever is around, kept in memory only
  meetingStartedAt: 0,
  meetingClock: "", // "03:12", shown while meeting mode is on
  meetingWriting: "", // the meeting file the AI is writing up now
  meetingSavedAt: 0, // a write-up was saved: the meeting list reloads
  done: null, // title of the chat whose work just finished, shown for a while
  doneAt: 0,
  vscode: null, // {folder, title}: what is said goes to Claude Code in that VS Code window
  ...loadSettings(), // ttsVoice, ttsSpeed, speakerMin, listenMode, wakeIdleMin, summary, pagePlayback
});

let flashTimer = null;

function flash(text) {
  voice.flash = text;
  clearTimeout(flashTimer);
  flashTimer = setTimeout(() => (voice.flash = ""), FLASH_MS);
}

// logHeard keeps what each clip was judged as, so the settings can show which
// voices were let through and which were not.
function logHeard(text, score, kept, why = "") {
  voice.heardLog = [{ at: Date.now(), text, score, kept, why }, ...voice.heardLog].slice(0, MAX_HEARD_LOG);
}

// setVoiceSettings changes the voice settings and remembers them. They apply
// from the next sentence on — nothing is restarted.
export function setVoiceSettings(patch) {
  if (patch.ttsVoice !== undefined && isTTSVoice(patch.ttsVoice)) voice.ttsVoice = patch.ttsVoice;
  if (Number.isFinite(patch.ttsSpeed)) voice.ttsSpeed = Math.round(clamp(patch.ttsSpeed, SPEED_RANGE) * 100) / 100;
  if (Number.isFinite(patch.speakerMin)) voice.speakerMin = Math.round(clamp(patch.speakerMin, SPEAKER_MIN_RANGE) * 100) / 100;
  const modeChanged = LISTEN_MODES.includes(patch.listenMode) && patch.listenMode !== voice.listenMode;
  if (modeChanged) voice.listenMode = patch.listenMode;
  if (WAKE_IDLE_CHOICES.includes(patch.wakeIdleMin)) voice.wakeIdleMin = patch.wakeIdleMin;
  if (patch.wakeNames !== undefined) voice.wakeNames = cleanWakeNames(patch.wakeNames);
  if (typeof patch.wakeDefault === "boolean") voice.wakeDefault = patch.wakeDefault;
  if (!voice.wakeDefault && !voice.wakeNames.length) voice.wakeDefault = true; // always some name to wake on
  if (typeof patch.summary === "boolean") voice.summary = patch.summary;
  if (typeof patch.textOnly === "boolean") voice.textOnly = patch.textOnly;
  if (typeof patch.pagePlayback === "boolean") voice.pagePlayback = patch.pagePlayback;
  try {
    localStorage.setItem(
      SETTINGS_STORAGE_KEY,
      JSON.stringify({
        ttsVoice: voice.ttsVoice,
        ttsSpeed: voice.ttsSpeed,
        speakerMin: voice.speakerMin,
        speakerMinV: 3,
        listenMode: voice.listenMode,
        wakeIdleMin: voice.wakeIdleMin,
        wakeIdleV: 2,
        wakeNames: voice.wakeNames,
        wakeDefault: voice.wakeDefault,
        summary: voice.summary,
        textOnly: voice.textOnly,
        pagePlayback: voice.pagePlayback,
      }),
    );
  } catch {}
  prefetched.clear(); // made in the old voice
  if (modeChanged && voice.listenMode === "always") void listenAlways();
}

// previewVoice reads a sample sentence with the current settings.
export function previewVoice() {
  return speak(`안녕하세요. ${voiceLabel(voice.ttsVoice)} 목소리예요. 이 빠르기로 읽어 드릴게요.`);
}

export async function speakerStatus() {
  try {
    return JSON.parse(await VoiceService.SpeakerStatus());
  } catch {
    return {};
  }
}

export async function clearSpeaker() {
  await VoiceService.ClearSpeaker();
  forgetUserLevel();
  vlog("speaker enrollment cleared");
}

// sttModelStatus: {"pref":"auto"|"small","koAvailable":bool} — koAvailable is
// whether the Korean-tuned model is installed; the choice only matters once
// it is.
export async function sttModelStatus() {
  try {
    return JSON.parse(await VoiceService.STTModelInfo());
  } catch {
    return { pref: "auto", koAvailable: false };
  }
}

export async function setSTTModel(model) {
  await VoiceService.SetSTTModel(model);
  vlog(`stt model → ${model}`);
}

// How loudly the user talks into this mic, learned from clips that are surely
// them. People across the room or the TV reach the mic much quieter; a clip
// well under the user's level must sound clearly like them to be sent. This
// only works with the mic's automatic gain off (startRecording, startWake):
// AGC lifts far-off speech to the same level as speech at the desk.
const USER_LEVEL_KEY = "aglink.voice.userLevel2"; // v1 was measured with AGC on
const QUIET_RATIO = 0.4; // under this share of the user's level = far away
const QUIET_EXTRA_BAR = 0.15;
const USER_LEVEL_MIN_CLIPS = 1;
let userLevel = (() => {
  try {
    const v = JSON.parse(localStorage.getItem(USER_LEVEL_KEY) || "null");
    if (v && Number.isFinite(v.level) && Number.isFinite(v.n)) return v;
  } catch {}
  return { level: 0, n: 0 };
})();

function learnUserLevel(level) {
  if (!(level > 0)) return;
  const n = Math.min(userLevel.n + 1, 30); // a moving average over ~30 clips
  userLevel = { level: userLevel.level + (level - userLevel.level) / n, n };
  try {
    localStorage.setItem(USER_LEVEL_KEY, JSON.stringify(userLevel));
  } catch {}
}

function forgetUserLevel() {
  userLevel = { level: 0, n: 0 };
  try {
    localStorage.removeItem(USER_LEVEL_KEY);
  } catch {}
}

function isQuiet(level) {
  return level > 0 && userLevel.n >= USER_LEVEL_MIN_CLIPS && level < userLevel.level * QUIET_RATIO;
}

function speakerMinFor(mixed) {
  return mixed ? Math.max(0.05, voice.speakerMin - SPEAKER_MIXED_DROP) : voice.speakerMin;
}

let recorder = null; // active recording session
let vadSeq = 0; // names each mic stream fed to the VAD
let vadTrusted = false; // the VAD has answered lately; logged when it comes and goes
let vadMissingLogged = false;
let turnMissingLogged = false;
let resumeTimer = null; // conversation mode: waits for the reading to finish
let endAfterReply = false; // "매번 부르기": the answer read out goes back to waiting for the name
let endedByUser = false; // turned off on purpose: in "always" only the name brings it back, not just the voice
let convActiveAt = 0; // conversation mode: last time something was said or answered
let micFailures = 0; // conversation mode: mic opens failed in a row
const MAX_MIC_FAILURES = 3;
let sendFailures = 0; // conversation mode: transcribe/send errors in a row
const MAX_SEND_FAILURES = 3;
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
  if (voice.taping) {
    setNote(paneId, "녹음하는 중이에요. “녹음 끝내”라고 하거나 트레이 메뉴에서 끝낸 뒤에 대화할 수 있어요.");
    return;
  }
  if (voice.meeting) {
    setNote(paneId, "회의 모드로 듣는 중이에요. “그만 들어”라고 하거나 표시등에서 끝낸 뒤에 대화할 수 있어요.");
    return;
  }
  if (voice.convPaneId) endConversation();
  voice.convPaneId = paneId;
  endAfterReply = false;
  endedByUser = false;
  convActiveAt = Date.now();
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
  if (voice.listenMode === "always") {
    await listenAlways();
    return;
  }
  const saved = savedConversationPane();
  if (!saved) return;
  // With the wake word on, a restart waits for "아글링크" instead of listening
  // right away; the remembered pane is the one the name opens.
  if (voice.listenMode === "wake") {
    rememberConvPane(saved);
    // A request still being worked on keeps the conversation (and the
    // indicator) on, so its end is seen and the answer read out.
    const req = awaitedReq();
    if (req?.pane === saved) {
      voice.convPaneId = saved;
      convActiveAt = Date.now();
      vlog("restart: conversation waits for the answer");
      void watchResumedTurn(req);
      return;
    }
    saveConversation(null);
    vlog("restart: conversation waits for the wake word");
    return;
  }
  for (let i = 0; i < 120 && !voice.convPaneId; i++) {
    const paneId = pane(saved) ? saved : chat.focusedPaneId;
    if (paneId && pane(paneId)?.target && chat.connected && voice.phase === "idle" && !voice.speaking) {
      await toggleConversation(paneId);
      return;
    }
    await new Promise((r) => setTimeout(r, 1000));
  }
}

// watchResumedTurn: no "done" comes for a turn that ended while the app was
// closed, so a conversation resumed to wait for it would wait forever, with
// the mic shut and the wake word off. A turn still running shows up in
// chat.working by its next "typing" refresh; none by then means it is over.
async function watchResumedTurn(req) {
  let quietSince = 0; // connected, and the chat not working, since then
  for (;;) {
    await new Promise((r) => setTimeout(r, 1000));
    const cur = readAwaited();
    if (cur?.key !== req.key || voice.convPaneId !== req.pane) return; // answered, or ended meanwhile
    if (Date.now() - req.at > AWAIT_MAX_MS) break;
    if (!chat.connected || chat.working.has(req.key)) quietSince = 0;
    else if (!quietSince) quietSince = Date.now();
    else if (Date.now() - quietSince > RESUME_GRACE_MS) break;
  }
  setAwaited(null);
  vlog("restart: the awaited turn is not running");
  noteDone(keyTarget(req.key));
  if (voice.listenMode === "wake") endConversation(); // back to waiting for the name, as after any restart
  else resumeListening();
}

// listenAlways starts conversation mode on the last conversation pane (or the
// focused one) once one is connected — "always" listening needs no 🗣.
async function listenAlways() {
  for (let i = 0; i < 120 && voice.listenMode === "always" && !voice.convPaneId && !voice.taping && !voice.meeting; i++) {
    const paneId = wakePane();
    if (paneId && chat.connected && voice.status === "ready" && voice.phase === "idle" && !voice.speaking && !voice.enrolling) {
      vlog("always listening: conversation on");
      await toggleConversation(paneId);
      return;
    }
    await new Promise((r) => setTimeout(r, 1000));
  }
}

export function endConversation(note) {
  const paneId = voice.convPaneId;
  if (paneId) vlog(`conversation off${note ? ` (${note})` : ""}`);
  voice.convPaneId = null;
  endAfterReply = false;
  voice.heard = "";
  voice.problem = "";
  micFailures = 0;
  sendFailures = 0;
  saveConversation(null);
  clearTimeout(resumeTimer);
  resumeTimer = null;
  if (recorder) stopRecording(false);
  stopSpeaking();
  if (note && paneId) {
    setNote(paneId, note);
    endedByUser = true;
  }
  // Only an explicit "끝내" (which carries a note) keeps "always" off.
  if (!note && paneId && voice.listenMode === "always") setTimeout(() => void listenAlways(), 1000);
}

// voice:state tells the Go side (and the indicator window shown while the app
// is in the tray) whether conversation mode is on and what it is doing. Go
// handles events on goroutines of their own, so each carries a rising number
// and an older one arriving late is ignored; it starts from the clock so it
// keeps rising across a page reload.
let stateSeq = Date.now();

$effect.root(() => {
  $effect(() => {
    const active = !!voice.convPaneId || voice.taping || voice.meeting || voice.done !== null;
    let phase = "waiting";
    // What was sent is shown while waiting, so a mishearing is caught at once.
    let label = voice.heard ? `${quote(voice.heard, 30)} 답변 기다리는 중` : "답변을 기다리는 중";
    // "작업이 끝났어요" gives way to a passing note and to the mic listening, so
    // the indicator never says the work is done while it is waiting for words;
    // with the conversation off it stays up for its whole time.
    if (voice.taping) [phase, label] = ["recording", `녹음 중 ${voice.tapeClock || "00:00"} — “녹음 끝내”라고 하면 멈춰요`];
    else if (voice.problem) [phase, label] = ["problem", voice.problem];
    else if (voice.flash) [phase, label] = ["note", voice.flash];
    else if (voice.phase === "listening") [phase, label] = ["listening", voice.hearing ? "말소리 듣는 중" : "듣는 중 — 말씀하세요"];
    else if (voice.phase === "transcribing") [phase, label] = ["transcribing", "알아듣는 중"];
    else if (voice.done !== null) [phase, label] = ["done", voice.speaking ? "작업이 끝났어요 — 답변을 읽어요" : "작업이 끝났어요"];
    else if (voice.speaking) [phase, label] = ["speaking", "답변을 읽는 중"];
    // Meeting mode shows after speaking (an advice reply reading through still
    // shows "speaking") but otherwise as its own, so it is clear to anyone in
    // the room that the mic is on, not finishing some other wait.
    else if (voice.meeting) [phase, label] = ["recording", `회의 모드로 듣는 중 ${voice.meetingClock || "00:00"} — “조언해 줘”라고 하면 조언해 드려요`];
    // Which chat the conversation is talking to, shown under the status line.
    const chatTitle = (voice.done || (active && paneConversationMeta(pane(voice.convPaneId)?.target)?.title)) || "";
    Events.Emit("voice:state", {
      seq: ++stateSeq,
      active,
      phase,
      label: `🗣 ${label}`,
      barge: voice.bargeEnabled,
      speed: voice.ttsSpeed,
      chat: chatTitle,
      wake: voice.waking,
      wakeName: wakeNameLabel(), // for the tray tooltip
      textOnly: voice.textOnly,
      meeting: voice.meeting,
      hearing: phase === "listening" && voice.hearing,
    });
  });
});

// The tray menu's "음성 대화" works like 🗣 on the focused pane, so a
// conversation can start while the window is hidden.
Events.On("tray:conversation", () => {
  if (voice.taping) {
    void stopTape();
    return;
  }
  if (voice.meeting) {
    void stopMeeting();
    return;
  }
  if (!voice.convPaneId && voice.done !== null) {
    voice.done = null; // ✕ on "작업이 끝났어요" only closes the notice
    return;
  }
  const paneId = voice.convPaneId || chat.focusedPaneId;
  if (paneId) void toggleConversation(paneId);
});

// Hiding the window to the tray starts conversation mode on the focused pane,
// so the tray is a voice-only way of using the app. With the wake word on it
// only waits for "아글링크" (on the focused pane).
Events.On("window:tray", (event) => {
  if (!event.data?.hidden || voice.convPaneId) return;
  const paneId = chat.focusedPaneId;
  if (voice.listenMode === "wake") {
    if (paneId && pane(paneId)?.target) rememberConvPane(paneId);
    return;
  }
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

// sendNow is the "지금 보내기" click: the recording ends now and what was heard
// is sent as it is — the click says it is meant, whoever's voice it is, so the
// speaker and room-noise checks are skipped.
export function sendNow() {
  const rec = recorder;
  if (!rec || rec.barge || voice.phase !== "listening") return;
  if (rec.firstSpeechChunk < 0 && !rec.prefix) {
    flash("아직 들은 말이 없어요");
    return;
  }
  rec.manual = true;
  vlog("send now (clicked)");
  stopRecording(true);
}
Events.On("indicator:send", sendNow);

Events.On("tray:barge", () => setBargeEnabled(!voice.bargeEnabled));
Events.On("tray:textonly", () => setVoiceSettings({ textOnly: !voice.textOnly }));

function everyTime() {
  return voice.listenMode === "wake" && voice.wakeIdleMin === WAKE_EVERY_TIME;
}

// resumeListening opens the mic again once the reply has been read out.
function resumeListening(delay = 200) {
  clearTimeout(resumeTimer);
  resumeTimer = setTimeout(() => {
    resumeTimer = null;
    const paneId = voice.convPaneId;
    // A barge-in recorder keeps listening by itself; "녹음해 줘" has the mic to itself.
    if (!paneId || recorder || voice.enrolling || voice.taping) return;
    const busy =
      voice.speaking ||
      (typeof speechSynthesis !== "undefined" && (speechSynthesis.speaking || speechSynthesis.pending));
    if (busy || voice.phase !== "idle") {
      resumeListening(300);
      return;
    }
    if (endAfterReply && everyTime()) {
      vlog("answered → waiting for the wake word");
      endConversation();
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
  if (texts?.length) speakReply(texts[texts.length - 1], key, speakNext.has(key) ? "full" : speechMode()); // the briefing is read whole
  textOnce.delete(key);
  speakNext.delete(key);
  saveTurnReply(key);
  convActiveAt = Date.now();
  const p = voice.convPaneId && pane(voice.convPaneId);
  // Matched by the chat, not the pane: the pane may have moved on to another one.
  if (readAwaited()?.key === key) {
    setAwaited(null);
    noteDone(keyTarget(key));
  } else if (p?.target && targetKey(p.target) === key) noteDone(p.target);
  if (p?.target && targetKey(p.target) === key) {
    endAfterReply = true;
    resumeListening();
  }
});

// ---- VS Code ----
//
// The aglink-vscode extension borrows this voice for Claude Code in a VS Code
// window (see vscodevoice.go). While it is attached, what is said in
// conversation mode goes there instead of the pane's chat, and the answer that
// Claude Code's Stop hook hands back is read like any reply.

const VSCODE_WAIT_MS = 15 * 60000; // a turn never reported stops holding the mic after this
let vscodeAwait = null; // timer while an answer from VS Code is awaited

function sendToVSCode(text) {
  vlog(`to VS Code ${quote(text)}`);
  voice.heard = text;
  convActiveAt = Date.now();
  if (ONCE_TEXT_RE.test(text)) textOnce.add("vscode");
  clearTimeout(vscodeAwait);
  vscodeAwait = setTimeout(() => {
    vscodeAwait = null;
    vlog("VS Code answer never came");
    resumeListening(0);
  }, VSCODE_WAIT_MS);
  Events.Emit("vscode:prompt", { text });
}

Events.On("vscode:attach", (ev) => {
  const d = ev.data || {};
  if (d.on) {
    voice.vscode = { folder: d.folder || "", title: d.title || "" };
    vlog(`VS Code attached ${quote(d.title || d.folder || "", 30)}`);
    if (voice.convPaneId) void speak("VS Code 로 연결했어요. 말씀하세요.");
    else if (!wakePane()) flash("aglink 대화가 없어 음성을 켜지 못했어요");
    else void wakeUp("");
    return;
  }
  if (!voice.vscode) return;
  voice.vscode = null;
  clearTimeout(vscodeAwait);
  vscodeAwait = null;
  textOnce.delete("vscode");
  vlog("VS Code detached");
  if (voice.convPaneId) endConversation();
  flash("VS Code 음성 연결을 끝냈어요");
});

Events.On("vscode:sent", (ev) => {
  const d = ev.data || {};
  if (d.ok) {
    flash("💻 VS Code 로 보냈어요");
    return;
  }
  clearTimeout(vscodeAwait);
  vscodeAwait = null;
  vlog(`VS Code send failed: ${d.error}`);
  void speak(d.error || "VS Code 로 보내지 못했어요.", "full"); // the mic opens again after it
});

Events.On("vscode:reply", (ev) => {
  const text = String(ev.data?.text || "").trim();
  // Only the answer to what was said here: the hook reports every turn.
  if (!voice.vscode || !vscodeAwait || !text) return;
  clearTimeout(vscodeAwait);
  vscodeAwait = null;
  voice.heard = "";
  convActiveAt = Date.now();
  vlog("VS Code answered");
  void speakReply(text, "vscode");
  textOnce.delete("vscode");
  endAfterReply = true;
  resumeListening(); // text only: nothing is read, so the mic opens now
});

// The spoken request being worked on — its pane, chat and when it was sent —
// kept over a restart.
function readAwaited() {
  try {
    const a = JSON.parse(localStorage.getItem(AWAIT_STORAGE_KEY) || "null");
    return a?.pane && a.key && a.at > 0 ? a : null;
  } catch {
    return null; // also the plain pane id stored before
  }
}

// awaitedReq is the request still worth waiting for: not too old, and its pane
// still on the same chat (switching chats mid-turn gives up on that answer).
function awaitedReq() {
  const a = readAwaited();
  if (!a) return null;
  const t = pane(a.pane)?.target;
  if (Date.now() - a.at > AWAIT_MAX_MS || (t && targetKey(t) !== a.key)) {
    setAwaited(null);
    return null;
  }
  return a;
}

function awaitedPane() {
  return awaitedReq()?.pane || null;
}

function setAwaited(paneId) {
  const t = paneId && pane(paneId)?.target;
  try {
    if (t) localStorage.setItem(AWAIT_STORAGE_KEY, JSON.stringify({ pane: paneId, key: targetKey(t), at: Date.now() }));
    else localStorage.removeItem(AWAIT_STORAGE_KEY);
  } catch {}
}

// keyTarget turns a targetKey back into a target.
function keyTarget(key) {
  return key.startsWith("web:") ? { kind: "web", id: key.slice(4) } : { kind: "telegram" };
}

let doneTimer = null;

// noteDone shows "작업이 끝났어요" on the indicator (and the tray icon) for a
// while, even if the conversation has ended meanwhile, so a long piece of
// work is seen to finish while the window is in the tray.
function noteDone(target) {
  const title = paneConversationMeta(target)?.title || "";
  voice.done = title;
  voice.doneAt = Date.now();
  clearTimeout(doneTimer);
  doneTimer = setTimeout(() => (voice.done = null), DONE_SHOW_MS);
  vlog(`turn done${title ? ` ${quote(title, 20)}` : ""}`);
}

// ---- recording ----

export async function toggleVoiceInput(paneId) {
  clearVoiceNote();
  if (voice.meeting) {
    setNote(paneId, "회의 모드로 듣는 중이에요. 그냥 말씀하시면 들려요 — 끝내시려면 “그만 들어”라고 하세요.");
    return;
  }
  if (voice.phase === "listening") {
    sendNow();
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
  const forConv = voice.convPaneId === paneId;
  let stream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: false },
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
  // The mic takes a moment to open: the conversation may have ended meanwhile
  // ("그만", ✕, "녹음해 줘"), or another recording started.
  if (!barge && (voice.taping || recorder || (forConv && voice.convPaneId !== paneId))) {
    stream.getTracks().forEach((t) => t.stop());
    return;
  }
  micFailures = 0;
  voice.problem = "";
  logMicProcessing(stream);
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
    speechSum: 0, // RMS summed over the loud chunks: how loudly this was said
    lastSpeechAt: 0,
    noiseFloor: 0,
    noiseSamples: 0,
    startedAt,
    barge, // still checking segments against the reply being read
    firstSpeechAt: 0,
    prefix: "", // what the user said over the reading
    earlyFor: 0, // lastSpeechAt of the pause already tried as the end
    bargeDone: Promise.resolve(), // segment checks in flight
    vad: newVad("rec"),
    turnFor: -1, // lastSpeechAt of the pause the turn model has been asked about
    turnStep: 0, // ...how many of TURN_PAUSE_MS it has been asked at
    turnEnd: -1, // lastSpeechAt of a pause the model called the end
    turnEndChunk: 0, // chunks there were when it said so
    turnHold: -1, // lastSpeechAt of a pause the model called mid-thought
  };
  recorder = rec;

  node.onaudioprocess = (e) => {
    if (recorder !== rec) return;
    const input = e.inputBuffer.getChannelData(0);
    rec.chunks.push(new Float32Array(input));
    feedVad(rec.vad, input, ctx.sampleRate);
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
        if (rec.lastSpeechAt - rec.firstSpeechAt >= BARGE_MIN_SPEECH_MS && rec.loudChunks >= BARGE_MIN_LOUD_CHUNKS)
          checkBargeSegment(rec, takeClip(rec), rec.speechSum / rec.loudChunks);
        rec.chunks = [];
        rec.firstSpeechChunk = -1;
      }
      rec.loudChunks = 0;
      rec.speechSum = 0;
      rec.barge = false;
      rec.startedAt = now;
      voice.hearing = false;
      voice.paneId = paneId;
      voice.phase = "listening";
    }
    // While our own voice plays, the speakers alone raise the mic level by a
    // learned share of what is playing; only sound well above that counts.
    const ref = refLevel(now);
    if (ref > 0.01) noteEchoRatio(rms / ref);
    // A small room's reverb keeps echoing after the learned gain reads ~0, so
    // never trust a gain below ECHO_GAIN_MIN while a reply is playing.
    const echoFloor = ref > 0 ? Math.max(echoGain ?? 0, ECHO_GAIN_MIN) * ref * ECHO_MARGIN : 0;
    const threshold = Math.max(0.012, rec.noiseFloor * 3, echoFloor);
    const loud = rms > threshold;
    const vad = vadSays(rec.vad, now);
    noteVadRun(rec, loud && vad === false, rec.barge ? "barge" : "rec");
    if (loud && vad !== false) {
      if (rec.firstSpeechChunk < 0) {
        rec.firstSpeechChunk = rec.chunks.length - 1;
        rec.firstSpeechAt = now;
        if (!rec.barge) voice.hearing = true;
      }
      rec.lastSpeechAt = now;
      rec.loudChunks++;
      rec.speechSum += rms;
    }
    if (rec.barge) {
      // The reading rarely pauses long enough to end a segment, so one is also
      // cut after a few seconds — otherwise talking over it is only noticed
      // when the reading ends.
      const ended = now - rec.lastSpeechAt > BARGE_SEGMENT_MS || now - rec.firstSpeechAt > BARGE_MAX_SEGMENT_MS;
      if (rec.firstSpeechChunk >= 0 && ended) {
        if (rec.lastSpeechAt - rec.firstSpeechAt >= BARGE_MIN_SPEECH_MS && rec.loudChunks >= BARGE_MIN_LOUD_CHUNKS)
          checkBargeSegment(rec, takeClip(rec), rec.speechSum / rec.loudChunks);
        rec.chunks = [];
        rec.firstSpeechChunk = -1;
        rec.loudChunks = 0;
        rec.speechSum = 0;
      } else if (rec.firstSpeechChunk < 0 && rec.chunks.length > 8) {
        rec.chunks.splice(0, rec.chunks.length - 8); // keep only the pre-roll
      }
      return;
    }
    if (rec.firstSpeechChunk >= 0) {
      const step = rec.turnFor === rec.lastSpeechAt ? rec.turnStep : 0;
      if (step < TURN_PAUSE_MS.length && now - rec.lastSpeechAt > TURN_PAUSE_MS[step] && !rec.turnBusy) checkTurn(rec, step);
      // The model's "finished" is acted on a chunk later, and two if this one
      // is loud: the VAD answers a chunk behind, so speech starting again just
      // as the model answered is not yet counted.
      if (rec.turnEnd === rec.lastSpeechAt && rec.chunks.length > rec.turnEndChunk + (loud ? 1 : 0)) {
        stopRecording(true);
        return;
      }
    }
    if (rec.firstSpeechChunk >= 0 && voice.fast && voice.convPaneId === paneId && now - rec.lastSpeechAt > EARLY_STOP_MS && rec.earlyFor !== rec.lastSpeechAt) tryEarlyStop(rec);
    const pause = voice.convPaneId === paneId ? CONV_SILENCE_STOP_MS : SILENCE_STOP_MS;
    if (rec.firstSpeechChunk >= 0 && now - rec.lastSpeechAt > (rec.turnHold === rec.lastSpeechAt ? Math.max(pause, TURN_HOLD_MS) : pause)) {
      if (rec.turnHold === rec.lastSpeechAt) vlog(`turn: held pause reached ${(TURN_HOLD_MS / 1000).toFixed(1)} s, sending`);
      stopRecording(true);
    } else if (rec.firstSpeechChunk < 0 && rec.prefix && now - Math.max(rec.lastSpeechAt, rec.startedAt) > CONV_SILENCE_STOP_MS) stopRecording(true);
    else if (rec.firstSpeechChunk < 0 && now - rec.startedAt > (voice.convPaneId === paneId ? CONV_NO_SPEECH_MS : NO_SPEECH_MS)) {
      stopRecording(false);
      // Conversation mode never times out: it only ends on "그만", 🗣 or the
      // tray menu, so the app can be used by voice alone from the tray.
      if (voice.convPaneId === paneId) {
        if (!endIfIdle(paneId)) resumeListening(0);
      } else setNote(paneId, "음성이 감지되지 않았습니다. 🎤 를 다시 눌러 말해 주세요.");
    } else if (now - rec.startedAt > MAX_CLIP_MS) stopRecording(true);
  };
  source.connect(node);
  node.connect(ctx.destination); // the processor only runs while connected; its output is silence

  voice.level = 0;
  voice.hearing = false;
  if (barge) return; // "listening" only once the reading is over, so the indicator says "읽는 중"
  voice.paneId = paneId;
  voice.phase = "listening";
}

// endIfIdle: with the wake word on, a conversation nobody talks in goes back
// to waiting for "아글링크" instead of listening forever. Only the user's own
// words that were kept (and answers) count as talk, so a noisy room whose
// clips are all dropped still times out.
function endIfIdle(paneId) {
  const idleMin = everyTime() ? 1 : voice.wakeIdleMin; // woken but nothing asked: back to waiting soon
  if (voice.listenMode !== "wake" || idleMin <= 0 || awaitedPane() === paneId) return false;
  if (Date.now() - convActiveAt <= idleMin * 60000) return false;
  vlog("conversation idle → waiting for the wake word");
  endConversation();
  return true;
}

// newVad is one mic's stream into the VAD; Silero keeps its state per stream.
function newVad(tag) {
  return { id: `${tag}-${++vadSeq}`, pending: [], busy: false, first: true, off: false, prob: 0, at: 0, speech: false };
}

// feedVad sends a chunk to the VAD, one request at a time per stream; chunks
// recorded while one is out go with the next, so the stream stays whole.
function feedVad(v, input, rate) {
  if (v.off) return;
  v.pending.push(downsample(input, rate, TARGET_RATE));
  if (v.busy) return;
  const pcm = pcm16Base64(concat(v.pending));
  v.pending = [];
  v.busy = true;
  const reset = v.first;
  v.first = false;
  VoiceService.VadProbs(v.id, pcm, reset)
    .then((probs) => {
      if (!probs.length) return;
      if (!vadTrusted) vlog("vad: answering — speech now needs both the level and a voice");
      vadTrusted = true;
      v.prob = Math.max(...probs);
      v.at = performance.now();
      if (v.prob >= VAD_ON) v.speech = true;
      else if (v.prob < VAD_OFF) v.speech = false;
    })
    .catch((err) => {
      const msg = String(err?.message || err);
      if (msg.includes("starting")) return;
      if (msg.includes("not installed")) {
        v.off = true;
        if (!vadMissingLogged) vlog("vad: not installed — the level alone decides");
        vadMissingLogged = true;
        return;
      }
      if (vadTrusted) vlog(`vad: failed (${msg}) — the level alone decides`);
      vadTrusted = false;
    })
    .finally(() => {
      v.busy = false;
    });
}

// vadSays: true or false when the VAD has heard this stream lately, null when
// it has not and the level must decide alone.
function vadSays(v, now) {
  return v.off || now - v.at > VAD_STALE_MS ? null : v.speech;
}

// noteVadRun logs a loud stretch the VAD did not take for speech, once it is over.
function noteVadRun(o, turnedDown, tag) {
  if (turnedDown) {
    o.vadRun = (o.vadRun || 0) + 1;
    o.vadPeak = Math.max(o.vadPeak || 0, o.vad.prob);
    return;
  }
  if (o.vadRun >= VAD_LOG_CHUNKS) vlog(`vad: ${tag}: ${((o.vadRun * 4096) / o.ctx.sampleRate).toFixed(1)} s of sound with no voice in it (p ≤ ${o.vadPeak.toFixed(2)}) ignored`);
  o.vadRun = 0;
  o.vadPeak = 0;
}

// checkTurn asks the turn model whether the user has finished, from the last
// TURN_MAX_S of the clip with the pause so far at its end. Its answer counts
// only if nobody has spoken since; step is which of TURN_PAUSE_MS this is.
function checkTurn(rec, step) {
  const at = rec.lastSpeechAt;
  rec.turnFor = at;
  rec.turnStep = step + 1;
  rec.turnBusy = true;
  const rate = rec.ctx.sampleRate;
  const chunkMs = (4096 / rate) * 1000;
  const from = Math.max(0, rec.firstSpeechChunk - Math.ceil(PREROLL_MS / chunkMs), rec.chunks.length - Math.ceil((TURN_MAX_S * 1000) / chunkMs));
  const pcm = pcm16Base64(downsample(concat(rec.chunks.slice(from)), rate, TARGET_RATE));
  const pause = ((performance.now() - at) / 1000).toFixed(1);
  const asked = performance.now();
  VoiceService.TurnEnd(pcm)
    .then((p) => {
      if (p < 0) {
        if (!turnMissingLogged) vlog("turn: model unavailable — fixed pauses decide");
        turnMissingLogged = true;
        return;
      }
      turnMissingLogged = false;
      const took = Math.round(performance.now() - asked);
      const said = `${p.toFixed(2)} at ${pause} s pause (${took} ms)`;
      if (recorder !== rec || rec.lastSpeechAt !== at) {
        vlog(`turn: ${said}, but speech went on`);
      } else if (p >= TURN_COMPLETE) {
        vlog(`turn: complete ${said}`);
        rec.turnEnd = at;
        rec.turnEndChunk = rec.chunks.length;
      } else if (p < TURN_UNFINISHED) {
        if (rec.turnHold !== at) vlog(`turn: unfinished ${said} — waiting up to ${(TURN_HOLD_MS / 1000).toFixed(1)} s`);
        else vlog(`turn: still unfinished ${said}`);
        rec.turnHold = at;
      } else {
        vlog(`turn: unsure ${said}`);
      }
    })
    .catch(() => {})
    .finally(() => {
      rec.turnBusy = false;
    });
}

// takeClip encodes the audio from just before the first speech as whisper input.
function takeClip(rec) {
  const rate = rec.ctx.sampleRate;
  const chunkMs = (4096 / rate) * 1000;
  const from = Math.max(0, rec.firstSpeechChunk - Math.ceil(PREROLL_MS / chunkMs));
  return asrWav(downsample(concat(rec.chunks.slice(from)), rate, TARGET_RATE), TARGET_RATE);
}

// tryEarlyStop transcribes the clip at a short pause in conversation mode; a
// finished sentence is sent at once instead of after the full
// CONV_SILENCE_STOP_MS wait. Speech resuming before it answers keeps the
// recording going as usual.
//
// Only one try runs at a time: in a noisy room every short pause would
// otherwise start another transcription of the whole, still growing clip. A
// try that hears no words at all means the clip so far is only noise (a fan,
// music under the TV); it is dropped, so it does not grow into a long clip
// that buries what the user says next.
function tryEarlyStop(rec) {
  if (rec.earlyBusy) return;
  const at = rec.lastSpeechAt;
  rec.earlyFor = at;
  rec.earlyBusy = true;
  const wav = takeClip(rec);
  void (async () => {
    let text = "";
    try {
      await rec.bargeDone;
      text = (await VoiceService.Transcribe(toBase64(wav))).trim();
    } catch {
      return;
    } finally {
      rec.earlyBusy = false;
    }
    if (!text && recorder === rec && rec.lastSpeechAt === at && !rec.prefix && !rec.barge) {
      vlog(`early: no words, ${clipSeconds(wav).toFixed(1)} s of noise dropped`);
      rec.chunks.splice(0, Math.max(0, rec.chunks.length - 8)); // keep only the pre-roll
      rec.firstSpeechChunk = -1;
      rec.loudChunks = 0;
      rec.speechSum = 0;
      rec.startedAt = performance.now();
      voice.hearing = false;
      return;
    }
    if (recorder !== rec || rec.lastSpeechAt !== at || !SENTENCE_END_RE.test(text) || GOES_ON_RE.test(text)) return;
    // The turn model heard a voice trailing off mid-thought; whisper still
    // writes a full stop after "…했어요" that is about to go on.
    if (rec.turnHold === at) {
      vlog(`early: ${quote(text)} reads finished, but the turn model says not yet — waiting`);
      return;
    }
    vlog(`early end ${quote(text)}`);
    stopRecording(true, text);
  })();
}

// checkBargeSegment transcribes one segment heard during the reading, one at a
// time, and decides whether it was the user or our own voice. level is the
// segment's average level — the reading bouncing back off the room reaches the
// mic far quieter than the user's own voice, so a quiet segment needs a
// clearly matching voice and is given more benefit of the doubt on the words
// (whisper garbles a quiet echo more than a close one).
function checkBargeSegment(rec, wavBytes, level) {
  rec.bargeDone = rec.bargeDone.then(async () => {
    if (recorder !== rec && !rec.stopped) return;
    let text = "";
    let score = -1;
    try {
      [text, score] = await Promise.all([VoiceService.Transcribe(toBase64(wavBytes)).then((t) => t.trim()), speakerScore(wavBytes)]);
    } catch {
      return;
    }
    if (!text || (recorder !== rec && !rec.stopped)) return;
    // Our own voice is mixed into these, so the bar is the low one — unless the
    // segment is also quiet, which the mixed bar alone was already passing too
    // easily for echo that happens to score like the user by chance.
    const quiet = isQuiet(level);
    if (!isUserVoice(score, speakerMinFor(true) + (quiet ? QUIET_EXTRA_BAR : 0))) {
      vlog(`other voice dropped (barge, ${score.toFixed(2)}, level ${level.toFixed(3)}${quiet ? " quiet" : ""}) ${quote(text)}`);
      logHeard(text, score, false, quiet ? "작은 소리" : "읽는 중");
      return;
    }
    // Same as the main path: whisper writes a back-and-forth between people in
    // the room as "- 그쵸. - 네.", which can score like the user by chance.
    if ((text.match(DIALOGUE_RE) || []).length >= 2) {
      vlog(`dialogue dropped (barge) ${quote(text)}`);
      logHeard(text, score, false, "주변 대화");
      return;
    }
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
    // A quiet segment is most likely the reading itself heard faintly, and
    // whisper mangles it more than it would a close voice, so it does not need
    // as much of a match to be dropped as our own echo.
    if (isEcho(text, replyText, quiet ? 0.4 : ECHO_OVERLAP)) {
      vlog(`echo dropped${quiet ? " (quiet)" : ""} ${quote(text)}`);
      return;
    }
    vlog(`barge-in ${quote(text)} (echo gain ${echoGain?.toFixed(3) ?? "unlearned"}, voice ${score.toFixed(2)}, level ${level.toFixed(3)})`);
    if (score >= 0) logHeard(text, score, true, "끼어들기");
    stopSpeaking();
    rec.prefix = rec.prefix ? `${rec.prefix} ${text}` : text;
    rec.lastSpeechAt = performance.now();
  });
}

function normalizeForEcho(s) {
  return String(s || "").normalize("NFC").toLowerCase().replace(/[^\p{L}\p{N}]/gu, "");
}

// The reply is written with English words and digits, but whisper hears our
// voice reading them in Hangul ("OpenVoice" → "오픈 보이스", "M2" → "엠투"),
// so the echo check compares against how they are read aloud too.
const READ_WORDS = {
  openvoice: "오픈보이스", supertonic: "슈퍼토닉", whisper: "위스퍼", claude: "클로드", aglink: "에이지링크",
  windows: "윈도우", wav: "웨이브", ok: "오케이", okay: "오케이", go: "고", git: "깃", commit: "커밋",
  push: "푸시", build: "빌드", server: "서버", test: "테스트", log: "로그", app: "앱", mode: "모드",
};
const READ_LETTERS = {
  a: "에이", b: "비", c: "씨", d: "디", e: "이", f: "에프", g: "지", h: "에이치", i: "아이", j: "제이",
  k: "케이", l: "엘", m: "엠", n: "엔", o: "오", p: "피", q: "큐", r: "알", s: "에스", t: "티",
  u: "유", v: "브이", w: "더블유", x: "엑스", y: "와이", z: "지",
};
const SINO_DIGITS = ["", "일", "이", "삼", "사", "오", "육", "칠", "팔", "구"];
const SINO_DIGITS_ALONE = ["영", ...SINO_DIGITS.slice(1)];
// After a letter ("M2", "F2") a digit is read as an English number.
const ENGLISH_DIGITS = ["제로", "원", "투", "쓰리", "포", "파이브", "식스", "세븐", "에잇", "나인"];

function sinoNumber(digits) {
  const n = Number(digits);
  if (!Number.isFinite(n) || digits.length > 4 || n === 0) return [...digits].map((d) => SINO_DIGITS_ALONE[d]).join("");
  let out = "";
  [..."천백십"].forEach((unit, i) => {
    const d = Math.floor(n / 10 ** (3 - i)) % 10;
    if (d) out += (d === 1 ? "" : SINO_DIGITS[d]) + unit;
  });
  return out + SINO_DIGITS[n % 10];
}

export function readAloud(text) {
  const decimals = String(text || "").replace(/(\d+)\.(\d+)/g, (_, a, b) => `${sinoNumber(a)}점${[...b].map((d) => SINO_DIGITS_ALONE[d]).join("")}`);
  return decimals.replace(/[A-Za-z]+(\d*)|\d+/g, (tok, tail = "") => {
    if (/^\d/.test(tok)) return sinoNumber(tok);
    const word = tok.slice(0, tok.length - tail.length);
    const known = READ_WORDS[word.toLowerCase()];
    // Unknown short or all-caps words (GPU, TTS) are spelled out letter by letter.
    const spoken = known ?? (word.length <= 4 || word === word.toUpperCase() ? [...word.toLowerCase()].map((c) => READ_LETTERS[c]).join("") : word);
    return spoken + [...tail].map((d) => ENGLISH_DIGITS[d]).join("");
  });
}

function echoReference(reply) {
  const r = normalizeForEcho(reply);
  return /[a-z0-9]/.test(r) ? r + normalizeForEcho(readAloud(reply)) + normalizeForEcho(speakNumbers(reply)) : r;
}

// isEcho: most of the segment's character pairs also occur in the reply being
// read. Whisper mishears our own voice a little, so this is fuzzy on purpose.
function isEcho(text, reply, overlap = ECHO_OVERLAP) {
  const t = normalizeForEcho(text);
  const r = echoReference(reply);
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

// ---- voice commands ----
//
// In conversation mode a few short sentences run the app instead of going to
// the AI: changing the voice or how fast it reads, reading the last reply
// again, and starting, going back to or finding a conversation. Each has to be
// the whole utterance (give or take a "줘"), so a question that merely
// mentions one of them is still sent.

const CMD_TAIL = String.raw`\s*(줘|주세요|줄래|봐|봐\s*줘|요)?[.!?~\s]*$`;
const cmdRE = (body) => new RegExp(String.raw`^\s*(${body})${CMD_TAIL}`, "i");
const SLOWER_RE = cmdRE(String.raw`(조금|좀|더|조금\s*더|좀\s*더)?\s*(천천히|느리게)\s*(말해|읽어|얘기해)`);
const FASTER_RE = cmdRE(String.raw`(조금|좀|더|조금\s*더|좀\s*더)?\s*(빨리|빠르게)\s*(말해|읽어|얘기해)`);
const SPEED_RESET_RE = cmdRE(String.raw`(말\s*|읽는\s*)?(빠르기|속도)\s*(를|을)?\s*(원래대로|기본으로|처음대로)\s*(해|돌려|바꿔)?`);
const REPEAT_RE = cmdRE(String.raw`(다시|한\s*번\s*더|방금\s*(거|답변)\s*다시)\s*(읽어|말해|들려)`);
const NEW_CONV_RE = cmdRE(String.raw`새\s*(로운\s*)?대화\s*(를)?\s*(시작|시작해|시작하자|열어|만들어|하자)`);
const BACK_RE = cmdRE(String.raw`(아까|이전|전|원래|지난)\s*대화\s*(로|으로)?\s*(돌아가|돌아가자|가|가자|열어|옮겨|바꿔)`);
// "남자 목소리로 바꿔 줘", "목소리 여자 삼 번으로 해 줘", "음성을 여자로 바꿔":
// a short phrase before the verb (group 2 or 5), checked by isVoiceChoice.
const VOICE_CHANGE_RE = cmdRE(String.raw`(.{1,20}?)\s*(으로|로)?\s*(바꿔|바꾸자|바꿀래|변경해|변경)|(.{1,20}?)\s*(으로|로)\s*(해)`);
// Everything a voice choice is said with; anything else left over ("음성 인식
// 모델을 small에서 medium") means the sentence is about something else.
const VOICE_CHOICE_WORDS_RE = /(?<![a-z])[mf][1-5]?(?![a-z])|목소리|보이스|음성|남자|여자|남성|여성|에프|엠|다른|파이브|쓰리|하나|다섯|중에|걸|거|것|번|째|중|으로|로|을|를|은|는|좀|[1-5]|[일이삼사오원투포둘셋넷한두세네첫]|\s/gi;

function isVoiceChoice(phrase) {
  const p = String(phrase || "").trim();
  // "음성" alone is said of speech recognition too, so it needs a gender with it.
  if (!/목소리|보이스/.test(p) && !/^음성\s*(을|를|은)?\s*(남자|여자|남성|여성)/.test(p)) return false;
  return !p.replace(VOICE_CHOICE_WORDS_RE, "");
}
const FIND_RE = /(대화|얘기|이야기|채팅|채팅방)\s*(를|을)?\s*(좀\s*)?(찾아|찾기|찾자|열어)\S*(\s*(줘|주세요|줄래|봐|봐\s*줘))?[.!?~\s]*$/;
const LIST_RE = cmdRE(String.raw`(최근\s*)?(대화|채팅|채팅방)\s*(목록|리스트)\s*(을|를|좀)?\s*(읽어|알려|말해|불러|보여)`);
const NOW_RE = cmdRE(String.raw`(지금|여기)\s*(은|는)?\s*(무슨|어떤|어느|뭔)\s*(대화|채팅|채팅방)\s*(야|이야|예요|이에요|지|인가|인지\s*알려)?\??`);
const NTH_RE = cmdRE(String.raw`(\d+|일|이|삼|사|오|첫|두|세|네|다섯)\s*번\s*(째)?\s*(대화|채팅)?\s*(으로|로|를|을)?\s*(열어|가|가자|바꿔|옮겨|들어가)`);
const DETAIL_RE = cmdRE(String.raw`(방금\s*(거|답변)\s*(을|를)?\s*)?(좀\s*)?(더\s*)?(자세히|전부|전체|끝까지|나머지|다)\s*(다\s*)?(읽어|말해|들려|얘기해)`);
// "글로만 보여 줘": replies are shown, not read, until "다시 말로 해 줘".
// The same words at the end of a request ("오늘 일정 정리해서 글로만 보여
// 줘") hold for that one reply only (ONCE_TEXT_RE).
const NO_SPEECH = String.raw`((말|소리|음성)\s*(로\s*)?(는\s*)?(하지\s*말고|없이|끄고|빼고)|읽지\s*(말고|마))`;
const TEXT_ONLY_ON_RE = cmdRE(String.raw`(이제\s*(부터)?\s*)?(답변\s*(은|을|도)?\s*)?(${NO_SPEECH}\s*)?(글|글자|글씨|텍스트|문자|화면)\s*(으로|로|에)?\s*만\s*(보여|띄워|해|알려|써)|(답변\s*(은|을|도)?\s*)?${NO_SPEECH}\s*(보여|띄워)`);
const TEXT_ONLY_OFF_RE = cmdRE(String.raw`(이제\s*)?(다시\s*)?(답변\s*(은|을|도)?\s*)?(말|소리|음성)\s*(로|으로)\s*(도\s*)?(다시\s*)?(읽어|말해|해|들려|알려|대답해)`);
const ONCE_TEXT_RE = new RegExp(String.raw`(${NO_SPEECH}\s*((글|글자|텍스트|화면)\s*(으로|로|에)?\s*)?(만\s*)?|(글|글자|텍스트|화면)\s*(으로|로|에)\s*만\s*)(보여|띄워|정리해|알려|써)\S*(\s*(줘|주세요|줄래))?[.!?~\s]*$`);
const SHORT_RE = cmdRE(String.raw`(방금\s*(거|답변)\s*(을|를)?\s*)?(좀\s*)?(요약|짧게|간단히|간단하게)\s*(해서\s*)?(다시\s*)?(읽어|말해|들려|얘기해)`);
const ALARM_LIST_RE =cmdRE(String.raw`((맡긴|예약한|맞춘|남은)\s*)?(알림|알람)\s*(목록|들)?\s*(을|를|좀)?\s*(읽어|알려|말해|불러)|(알림|알람)\s*(뭐|뭐가|몇\s*개)\s*(있어|있지|있나|남았어)`);
const ALARM_CANCEL_RE = cmdRE(String.raw`((\S+)\s*번\s*(째\s*)?|방금\s*(맡긴\s*)?)?(알림|알람)\s*(을|를)?\s*(다|전부|모두)?\s*(취소|취소해|지워|꺼|없애|빼)`);
const PB_LIST_RE = cmdRE(String.raw`(업무|루틴|업무\s*루틴)\s*(목록|리스트|뭐\s*있어)?\s*(을|를|좀)?\s*(읽어|알려|말해|불러)?`);
// Asked for, not asked about: "주간 보고 실행해 줘", never "주간 보고 실행했어?".
const PB_RUN_RE = /^\s*(.+?)\s*(을|를)?\s*(지금\s*)?(실행\s*해\s*(줘|주세요|줄래\??)?|실행\s*(해\s*)?주세요|돌려\s*(줘|주세요|줄래\??))[.!~\s]*$/;
const PB_NAME_COVER = 0.6; // share of a routine's name a spoken part of it must cover
const PB_MIN_NAME = 3; // ...and at least this many letters
// "목소리 등록해 줘" records the user's voice; "목소리 등록 해제해 줘" / "목소리
// 지워 줘" forgets it. Whole sentences only: "영상에서 목소리 지우는 법" is a question.
const ENROLL_RE = cmdRE(String.raw`(제\s*|내\s*)?목소리\s*(로|를|좀)?\s*(좀\s*)?(등록|저장|기억)\s*(해|하자|해\s*둬|해\s*놔|할래)?`);
// "목소리 따라 하기 녹음해 줘": someone who agreed reads a short text, and
// replies are read in their voice (OpenVoice) until "원래 목소리로 돌려 줘".
const CLONE_RE = cmdRE(String.raw`(다시\s*|한\s*번\s*더\s*)?((이\s*사람|이|아내|안에|와이프|집사람|엄마|아빠|내|제)\s*(의\s*)?목소리\s*(로\s*)?(따라\s*하기|따라하기|흉내)?\s*(를|을)?\s*(녹음|배워|익혀|따라\s*해|따라해)|목소리\s*(따라\s*하기|따라하기|흉내\s*내기|복제)\s*(녹음)?\s*(을|를)?\s*(해|시작|하자|녹음해))\s*(해|하자|시작해)?`);
// "아내 목소리 녹음해서 그 목소리로 바꿔 줘"처럼 뒤에 말이 더 붙어도 받는다("아내"는 "안에"로도 적힘).
const CLONE_LEAD_RE = new RegExp(String.raw`^\s*(다시\s*)?(아내|안에|와이프|집사람)\s*(의\s*)?목소리\s*(를|을|로)?\s*(녹음|따라\s*하|따라해)`);
// "아내 목소리로 바꿔 줘": back to the recorded voice, made earlier.
const CLONE_USE_RE = cmdRE(String.raw`(아내|안에|와이프|집사람|녹음한|녹음된|따라\s*한)\s*(의\s*)?목소리\s*(로|으로)\s*(바꿔|해|읽어\s*보|읽어|변경해)`);
const CLONE_OFF_RE = cmdRE(String.raw`(원래|기본|처음|예전)\s*(목소리|음색)\s*(로|으로)\s*(돌려|돌아가|바꿔|해)|목소리\s*따라\s*하기\s*(꺼|끄기|그만|해제|취소)`);
const CLONE_MS = 25000;
const CLONE_SCRIPT =
  "안녕하세요. 오늘은 날씨가 참 맑고 바람도 시원하네요. 아침에 따뜻한 커피 한 잔을 마시고 천천히 하루를 시작했어요. " +
  "점심에는 가까운 식당에서 김치찌개를 먹었는데 정말 맛있었어요. 오후에는 잠깐 산책을 하면서 가을 하늘을 올려다봤어요. " +
  "내일은 비가 온다고 하니까 우산을 꼭 챙기세요. 그럼 오늘도 좋은 하루 보내세요.";
const UNENROLL_RE = cmdRE(String.raw`(제\s*|내\s*|등록한\s*|등록된\s*)?목소리\s*(등록\s*)?(을|를|은)?\s*(좀\s*)?(해제|취소|지워|삭제|지우기|없애)\s*(해|하자)?`);
const MAX_ALARM_CHARS = 80; // "내일 아침 9시에 …라고 알려 줘" runs longer than a command
const LIST_COUNT = 5; // conversations read out by "대화 목록 읽어 줘"
const MAX_COMMAND_CHARS = 40; // longer sentences are always meant for the AI
const FIND_SCAN_CONVS = 40; // recent conversations whose text is searched when no title matches
const MAX_TRAIL = 10;

// Spoken voice names: "남자 목소리로", "여자 삼 번", "에프 투로", "F2로".
const VOICE_GENDERS = [
  [/남자|남성|엠|(?<![a-z])m(?![a-z])/i, "M"], // a lone M, not the one in "medium"
  [/여자|여성|에프|(?<![a-z])f(?![a-z])/i, "F"],
];
const VOICE_NUMBERS = [
  ["1", "일", "원", "하나", "한", "첫"],
  ["2", "이", "투", "둘", "두"],
  ["3", "삼", "쓰리", "셋", "세"],
  ["4", "사", "포", "넷", "네"],
  ["5", "오", "파이브", "다섯"],
];
const VOICE_NUMBER_RE = new RegExp(`^(${VOICE_NUMBERS.flat().sort((a, b) => b.length - a.length).join("|")})`);

function voiceNumber(s) {
  const m = VOICE_NUMBER_RE.exec(s);
  return m ? VOICE_NUMBERS.findIndex((names) => names.includes(m[1])) + 1 : 0;
}

export function voiceLabel(name) {
  if (name === CLONE_VOICE) return "녹음한";
  return `${name[0] === "M" ? "남자" : "여자"} ${name[1]}번`;
}

// parseVoiceName finds the voice asked for, or "" when none is named. A bare
// "남자 목소리" means M2, the one the user chose; "다른 목소리" the next one.
function parseVoiceName(text) {
  const cur = voice.ttsVoice === CLONE_VOICE ? "F2" : voice.ttsVoice;
  let gender = "";
  let at = -1;
  for (const [re, g] of VOICE_GENDERS) {
    const m = re.exec(text);
    if (m && (at < 0 || m.index < at)) [gender, at] = [g, m.index + m[0].length];
  }
  const rest = gender ? text.slice(at).replace(/^\s*(목소리|음성|보이스)?\s*(중에?\s*)?/, "") : "";
  let n = gender ? voiceNumber(rest) : 0;
  if (!gender) {
    // "삼 번 목소리로" keeps the current one's gender
    const m = /(\S+)\s*번\s*(째\s*)?(목소리|음성)/.exec(text);
    n = m ? voiceNumber(m[1]) : 0;
    if (n) gender = cur[0];
  }
  if (/다른/.test(text) && !n) {
    gender ||= cur[0];
    n = gender === cur[0] ? (Number(cur[1]) % 5) + 1 : 2;
  }
  if (!gender) return "";
  return `${gender}${n || 2}`;
}

// The conversations the voice pane showed before, newest last, for "아까 대화로".
const convTrail = [];
let trailTarget = null;

$effect.root(() => {
  $effect(() => {
    const t = pane(voice.convPaneId || chat.focusedPaneId)?.target;
    if (!t) return;
    const key = targetKey(t);
    if (trailTarget && targetKey(trailTarget) !== key) {
      const prev = targetKey(trailTarget);
      const i = convTrail.findIndex((x) => targetKey(x) === prev);
      if (i >= 0) convTrail.splice(i, 1);
      convTrail.push(trailTarget);
      if (convTrail.length > MAX_TRAIL) convTrail.shift();
    }
    trailTarget = { ...t };
  });
});

function convTitle(target) {
  if (target.kind === "telegram") return "텔레그램";
  return findWebConv(target.id)?.title || "제목 없는";
}

function convExists(target) {
  return target.kind === "telegram" ? !!chat.telegram : !!findWebConv(target.id);
}

async function newConversation(paneId) {
  await ControlService.WebNew("");
  await loadConversations({ forceReloadCurrent: true });
  const first = chat.webConvs[0];
  if (!first) throw new Error("새 대화가 목록에 없습니다");
  await selectTargetInPane(paneId, { kind: "web", id: first.id });
  speak("새 대화를 시작했어요. 말씀하세요.");
}

// The conversations last read out by "대화 목록 읽어 줘", so "2번 열어 줘"
// opens the one that was heard as number 2 even if the list has moved since.
let listedConvs = [];

function listConversations(paneId) {
  listedConvs = chat.webConvs.slice(0, LIST_COUNT).map((c) => ({ id: c.id, title: c.title || "제목 없는" }));
  if (!listedConvs.length) {
    speak("대화 목록이 비어 있어요.");
    return;
  }
  const cur = pane(paneId)?.target;
  const items = listedConvs.map((c, i) => `${i + 1}번, ${c.title}.`).join(" ");
  const now = cur ? ` 지금은 ${convTitle(cur)} 대화예요.` : "";
  speak(`최근 대화예요. ${items}${now} 번호로 말씀하시면 열어 드릴게요.`);
}

const NTH_WORDS = { 일: 1, 첫: 1, 이: 2, 두: 2, 삼: 3, 세: 3, 사: 4, 네: 4, 오: 5, 다섯: 5 };

async function openNthConversation(paneId, word) {
  const n = /^\d+$/.test(word) ? Number(word) : NTH_WORDS[word];
  if (!listedConvs.length) {
    speak("먼저 대화 목록 읽어 줘, 라고 말씀해 주세요.");
    return;
  }
  const c = listedConvs[n - 1];
  if (!c || !findWebConv(c.id)) {
    speak(`${listedConvs.length}번까지 있어요. 다시 말씀해 주세요.`);
    return;
  }
  await selectTargetInPane(paneId, { kind: "web", id: c.id });
  speak(`${c.title} 대화를 열었어요.`);
}

async function goBackConversation(paneId) {
  const cur = pane(paneId)?.target;
  const curKey = cur ? targetKey(cur) : "";
  for (let i = convTrail.length - 1; i >= 0; i--) {
    const t = convTrail[i];
    if (targetKey(t) === curKey || !convExists(t)) continue;
    await selectTargetInPane(paneId, t);
    speak(`${convTitle(t)} 대화로 돌아왔어요.`);
    return;
  }
  speak("돌아갈 이전 대화가 없어요.");
}

// The words to look for in "지난주에 나노파이 얘기한 대화 찾아 줘": what is
// left once the time words, "얘기한", "대화", "찾아 줘" and particles are gone.
const FIND_STOPWORDS = new Set(["지난주", "지난주에", "지난번", "지난번에", "저번", "저번에", "예전에", "어제", "그저께", "아까", "전에", "좀", "관련", "관련된", "관련해서", "대해", "대해서", "대한", "했던", "하던", "한", "줘", "주세요", "그", "그때"]);
const FIND_STOP_PREFIX_RE = /^(얘기|이야기|애기|대화|채팅|찾|열어)/;

function findWords(text) {
  return text
    .replace(/[.!?~,]/g, " ")
    .split(/\s+/)
    .map((w) => w.replace(/(를|을|에서|에|의|으로|로|랑|이랑|하고)$/, ""))
    .filter((w) => w && !FIND_STOPWORDS.has(w) && !FIND_STOP_PREFIX_RE.test(w));
}

// findConversation opens the conversation that best matches the words: by
// title first, then by what was said in the most recent ones. Ties go to the
// more recent conversation (the list is newest first).
async function findConversation(paneId, text) {
  const keys = findWords(text).map(normalizeForEcho).filter(Boolean);
  if (!keys.length) {
    speak("무슨 대화를 찾을지 말씀해 주세요. 나노파이 얘기한 대화 찾아 줘, 처럼요.");
    return;
  }
  const hits = (s) => {
    const n = echoReference(s);
    return keys.filter((k) => n.includes(k)).length;
  };
  const cur = pane(paneId)?.target;
  const convs = chat.webConvs.filter((c) => !(cur?.kind === "web" && cur.id === c.id));
  let best = null;
  let bestScore = 0;
  for (const c of convs) {
    const s = hits(c.title || "");
    if (s > bestScore) [best, bestScore] = [c, s];
  }
  if (bestScore < keys.length) {
    flash("지난 대화를 찾는 중…");
    for (const c of convs.slice(0, FIND_SCAN_CONVS)) {
      let turns = [];
      try {
        turns = parseJSON(await ControlService.GetHistory("web", c.id), { turns: [] }).turns || [];
      } catch {
        continue;
      }
      const s = hits(turns.map((t) => t.text || "").join("\n"));
      if (s > bestScore) [best, bestScore] = [c, s];
      if (bestScore === keys.length) break;
    }
  }
  if (!best) {
    speak("그런 대화를 찾지 못했어요.");
    return;
  }
  await selectTargetInPane(paneId, { kind: "web", id: best.id });
  speak(`${best.title || "제목 없는"} 대화를 찾아서 열었어요.`);
}

// ---- 업무 루틴 by voice ----
// "업무 목록 읽어 줘" reads the routines; "아침 업무 정리 실행해 줘" runs the one
// whose name matches, in a new conversation the voice follows — so its result
// is read out. A name that matches no routine goes to the AI as usual.

async function playbookList() {
  const data = parseJSON(await ControlService.ListPlaybooks(), {});
  return Array.isArray(data.playbooks) ? data.playbooks : [];
}

async function listPlaybooks() {
  const books = await playbookList();
  if (!books.length) {
    speak("저장된 업무 루틴이 없어요. 업무 탭에서 만들 수 있어요.");
    return;
  }
  const names = books.slice(0, 8).map((b) => b.name).join(", ");
  speak(`업무 루틴이 ${books.length}개 있어요. ${names}. 이름을 말하고 실행해 줘, 라고 하시면 바로 실행해요.`);
}

async function runPlaybookByName(paneId, spoken) {
  const key = normalizeForEcho(spoken.replace(/(업무|루틴)\s*$/, ""));
  if (key.length < 2) return false;
  const books = await playbookList().catch(() => []);
  // The name said in full (spaces and "·" aside), or a good part of it: a
  // word it merely contains ("테스트 실행해 줘" and "빌드·테스트 점검") is
  // a request for the AI.
  let best = null;
  let bestCover = 0;
  for (const b of books) {
    const name = normalizeForEcho(b.name);
    if (!name) continue;
    const cover = name === key ? 1 : key.length >= PB_MIN_NAME && name.includes(key) ? key.length / name.length : 0;
    if (cover > bestCover) [best, bestCover] = [b, cover];
  }
  if (!best || bestCover < PB_NAME_COVER) return false;
  const j = parseJSON(await ControlService.RunPlaybook(best.id), {});
  if (j.ok === false) throw new Error(j.error || "실행 실패");
  vlog(`playbook run ${quote(best.name)}`);
  if (j.conversationId) {
    await loadConversations();
    await selectTargetInPane(paneId, { kind: "web", id: j.conversationId });
  }
  speak(`${best.name} 업무를 시작했어요. 끝나면 결과를 읽어 드릴게요.`);
  return true;
}

function changeSpeed(paneId, delta) {
  const before = voice.ttsSpeed;
  setVoiceSettings({ ttsSpeed: delta === null ? VOICE_DEFAULTS.ttsSpeed : before + delta });
  if (voice.ttsSpeed === before && delta !== null) {
    speak(delta < 0 ? "지금이 가장 느린 빠르기예요." : "지금이 가장 빠른 빠르기예요.");
    return;
  }
  vlog(`speed ${before} → ${voice.ttsSpeed}`);
  speak("이 빠르기로 읽을게요.");
}

// isCommandText tells a voice command, which the echo check must not drop.
function isCommandText(text) {
  const t = text.trim();
  if (isAssistCommand(t)) return true;
  if (t.length > MAX_ALARM_CHARS) return false;
  if (parseAlarm(t)) return true;
  if (t.length > MAX_COMMAND_CHARS) return false;
  return [TAPE_START_RE, MEETING_START_RE, TAPE_FOLDER_RE, WAKE_NAME_SET_RE, WAKE_NAME_ADD_RE, LISTEN_ALWAYS_RE, LISTEN_WAKE_RE, CLONE_RE, CLONE_LEAD_RE, CLONE_USE_RE, CLONE_OFF_RE, TEXT_ONLY_ON_RE, TEXT_ONLY_OFF_RE, ENROLL_RE, UNENROLL_RE, DETAIL_RE, SHORT_RE, REPEAT_RE, SLOWER_RE, FASTER_RE, SPEED_RESET_RE, LIST_RE, NOW_RE, NTH_RE, NEW_CONV_RE, BACK_RE, ALARM_LIST_RE, ALARM_CANCEL_RE, PB_LIST_RE, BARGE_HUSH_RE].some((re) => re.test(t));
}

const DIALOGUE_RE = /(?:^|\s)-\s/g; // a speaker turn in whisper's output

// Typed in the chat box, the same commands work as when spoken ("아내
// 목소리로 바꿔 줘"); "!" commands stay the host's.
export function isTypedCommand(text) {
  const t = String(text || "").trim();
  return !!t && !t.startsWith("!") && isCommandText(t);
}

export async function runTypedCommand(paneId, text) {
  vlog(`typed command ${quote(text.trim())}`);
  return runVoiceCommand(paneId, text);
}

// runVoiceCommand carries out text if it is one of the commands; false means
// it is to be sent as usual.
async function runVoiceCommand(paneId, text) {
  const t = text.trim();
  if (isAssistCommand(t)) return runAssistCommand(paneId, t);
  if (t.length <= MAX_ALARM_CHARS && (await runAlarmCommand(t))) return true;
  if (t.length > MAX_COMMAND_CHARS) return false;
  if (TAPE_START_RE.test(t)) {
    void startTape();
    return true;
  }
  if (MEETING_START_RE.test(t)) {
    void startMeeting();
    return true;
  }
  if (TAPE_FOLDER_RE.test(t)) {
    void openTapeFolder();
    speak("녹음 폴더를 열었어요.");
    return true;
  }
  const named = WAKE_NAME_SET_RE.exec(t) || WAKE_NAME_ADD_RE.exec(t);
  if (named) {
    const set = WAKE_NAME_SET_RE.test(t);
    const name = cleanWakeNames([set ? named[2] : named[1]])[0];
    if (!name) {
      speak("부를 이름은 두 글자에서 여덟 글자로 정해 주세요.");
      return true;
    }
    if (!set && !voice.wakeNames.includes(name) && voice.wakeNames.length >= MAX_WAKE_NAMES) {
      speak(`부르는 이름은 ${MAX_WAKE_NAMES}개까지 정할 수 있어요. 음성 설정에서 하나를 지운 뒤에 말씀해 주세요.`);
      return true;
    }
    setVoiceSettings({ wakeNames: set ? [name] : [...voice.wakeNames, name] });
    vlog(`wake names → ${voice.wakeNames.join(", ")}`);
    const also = voice.wakeDefault ? " 아글링크라고 부르셔도 계속 돼요." : "";
    speak(`이제 ${name}, 라고 부르시면 깨어날게요.${also}`);
    return true;
  }
  if (LISTEN_ALWAYS_RE.test(t) || LISTEN_WAKE_RE.test(t)) {
    const always = LISTEN_ALWAYS_RE.test(t);
    setVoiceSettings({ listenMode: always ? "always" : "wake" });
    speak(always ? "이제 항상 듣고 있을게요. 앱을 켜면 바로 대화가 시작돼요." : `이제 ${wakeNameLabel()}, 라고 부르실 때만 들을게요.`);
    return true;
  }
  if (TEXT_ONLY_ON_RE.test(t) || TEXT_ONLY_OFF_RE.test(t)) {
    const on = TEXT_ONLY_ON_RE.test(t);
    setVoiceSettings({ textOnly: on });
    vlog(`text only → ${on}`);
    speak(on ? "이제 답변은 읽지 않고 글로만 보여 드릴게요. 다시 말로 해 줘, 라고 하시면 읽어 드려요." : "이제 다시 답변을 읽어 드릴게요.");
    return true;
  }
  if (DETAIL_RE.test(t)) {
    speakDetail();
    return true;
  }
  if (SHORT_RE.test(t)) {
    speakAgain("short");
    return true;
  }
  if (CLONE_OFF_RE.test(t)) {
    // The recording is kept: "아내 목소리로 바꿔 줘" brings it back.
    setVoiceSettings({ ttsVoice: VOICE_DEFAULTS.ttsVoice });
    vlog(`voice → ${VOICE_DEFAULTS.ttsVoice}`);
    speak("원래 목소리로 돌렸어요.");
    return true;
  }
  if (CLONE_USE_RE.test(t)) {
    if (!(await cloneVoiceStatus()).on) {
      speak("녹음한 목소리가 없어요. 아내 목소리 녹음해 줘, 라고 하시면 먼저 녹음할게요.");
      return true;
    }
    setVoiceSettings({ ttsVoice: CLONE_VOICE });
    vlog(`voice → ${CLONE_VOICE}`);
    speak("녹음한 목소리로 바꿨어요.");
    return true;
  }
  if (CLONE_RE.test(t) || CLONE_LEAD_RE.test(t)) {
    await cloneVoice(paneId);
    return true;
  }
  if (ENROLL_RE.test(t) || UNENROLL_RE.test(t)) {
    if (UNENROLL_RE.test(t)) {
      await VoiceService.ClearSpeaker().catch(() => {});
      forgetUserLevel();
      vlog("speaker enrollment cleared");
      speak("등록한 목소리를 지웠어요. 이제 들리는 말은 모두 받아요.");
    } else await enrollVoice();
    return true;
  }
  if (BARGE_ON_RE.test(t) || BARGE_OFF_RE.test(t)) {
    const on = BARGE_ON_RE.test(t);
    setBargeEnabled(on);
    speak(on ? "끼어들기를 켰어요. 제가 읽는 중에도 말씀하시면 멈추고 들어요." : "주고받기로 바꿨어요. 제가 다 읽은 뒤에 말씀해 주세요.");
    return true;
  }
  if (BARGE_HUSH_RE.test(t)) {
    stopSpeaking();
    return true;
  }
  if (REPEAT_RE.test(t)) {
    speakAgain();
    return true;
  }
  if (SLOWER_RE.test(t) || FASTER_RE.test(t)) {
    changeSpeed(paneId, SLOWER_RE.test(t) ? -SPEED_STEP : SPEED_STEP);
    return true;
  }
  if (SPEED_RESET_RE.test(t)) {
    changeSpeed(paneId, null);
    return true;
  }
  const vc = VOICE_CHANGE_RE.exec(t);
  if (vc && isVoiceChoice(vc[2] ?? vc[5])) {
    const name = parseVoiceName(t);
    if (!name) {
      speak("어떤 목소리로 바꿀지 못 알아들었어요. 남자 일 번부터 오 번, 여자 일 번부터 오 번 중에서 말씀해 주세요.");
      return true;
    }
    setVoiceSettings({ ttsVoice: name });
    vlog(`voice → ${name}`);
    speak(`${voiceLabel(name)} 목소리로 바꿨어요. 이 목소리 어떠세요?`);
    return true;
  }
  try {
    if (LIST_RE.test(t)) {
      listConversations(paneId);
      return true;
    }
    if (NOW_RE.test(t)) {
      const cur = pane(paneId)?.target;
      speak(cur ? `지금은 ${convTitle(cur)} 대화예요.` : "지금 열린 대화가 없어요.");
      return true;
    }
    const nth = NTH_RE.exec(t);
    if (nth) {
      await openNthConversation(paneId, nth[2]);
      return true;
    }
    if (NEW_CONV_RE.test(t)) {
      await newConversation(paneId);
      return true;
    }
    if (BACK_RE.test(t)) {
      await goBackConversation(paneId);
      return true;
    }
    if (FIND_RE.test(t)) {
      await findConversation(paneId, t);
      return true;
    }
    if (PB_LIST_RE.test(t)) {
      await listPlaybooks();
      return true;
    }
    const run = PB_RUN_RE.exec(t);
    if (run && (await runPlaybookByName(paneId, run[1]))) return true;
  } catch (err) {
    vlog(`command failed: ${err?.message || err}`);
    speak("그 일을 하지 못했어요. 잠시 뒤에 다시 말씀해 주세요.");
    return true;
  }
  return false;
}

// ---- assistant: PC control, cost, memos, briefing, recording summary ----
//
// Whole-sentence commands parsed in assist.js. PC control, the cost question,
// memos and the evening round-up are done here with no AI turn; the morning
// briefing and the recording summary each send one short turn.

const BRIEFING_STORAGE_KEY = "aglink.voice.briefing";
const EVENING_STORAGE_KEY = "aglink.voice.evening";
const DAILY_CHECK_MS = 30000;
const DAILY_DEFAULTS = {
  briefing: { on: false, h: 7, m: 30, region: "서울", last: "" },
  evening: { on: false, h: 21, m: 0, last: "" },
};
const DAILY_KEYS = { briefing: BRIEFING_STORAGE_KEY, evening: EVENING_STORAGE_KEY };
const MAX_READ_MEMOS = 10; // "메모 뭐 있어?" reads this many

function loadDaily(which) {
  try {
    const s = JSON.parse(localStorage.getItem(DAILY_KEYS[which]) || "{}");
    return { ...DAILY_DEFAULTS[which], ...(s && typeof s === "object" ? s : {}) };
  } catch {
    return { ...DAILY_DEFAULTS[which] };
  }
}

// assist: the daily settings and the memo list, for the voice settings.
export const assist = $state({ briefing: loadDaily("briefing"), evening: loadDaily("evening"), memos: [] });

// setDaily changes the briefing's or the round-up's settings. A time set to
// one that has gone by today starts tomorrow, rather than going off at once.
export function setDaily(which, patch) {
  const next = { ...assist[which], ...patch };
  if ("h" in patch || "m" in patch) {
    const at = new Date();
    at.setHours(next.h, next.m, 0, 0);
    if (at.getTime() <= Date.now()) next.last = dayKey();
  }
  assist[which] = next;
  try {
    localStorage.setItem(DAILY_KEYS[which], JSON.stringify(next));
  } catch {}
}

const speakNext = new Set(); // chats whose next reply is read out wherever it lands (the briefing)
const replySaves = new Map(); // chat key -> {path, text}: the turn's answer is saved as the recording's summary

function isAssistCommand(t) {
  return t.length <= MAX_MEMO_CMD_CHARS && !!(parsePc(t) || parseUsage(t) || parseDaily(t) || parseMemo(t) || parseTapeSummary(t));
}

// eulReul picks 을/를 for the word before it.
function eulReul(word) {
  const c = String(word).trim().slice(-1).charCodeAt(0);
  return c >= 0xac00 && c <= 0xd7a3 && (c - 0xac00) % 28 ? "을" : "를";
}

// assistPane is the pane a turn goes to: the one spoken or typed in, else the
// conversation, the focused pane or any with a chat open.
function assistPane(paneId) {
  for (const id of [paneId, voice.convPaneId, chat.focusedPaneId, ...chat.panes.map((p) => p.id)]) if (id && pane(id)?.target) return id;
  return null;
}

// sendTurn sends one AI turn from paneId as if typed, leaving what was in the
// composer as it was. read: the answer is read out even outside conversation
// mode; saveTo: it is saved as that recording's (kind "meeting": meeting's)
// write-up; quiet: the answer is not read out. "" when sent, else why not.
async function sendTurn(paneId, prompt, { read = false, saveTo = "", kind = "tape", quiet = false } = {}) {
  const p = pane(paneId);
  if (!p?.target) return "열린 대화가 없어요";
  if (!chat.connected) return "aglink 에 연결되어 있지 않아요";
  const key = targetKey(p.target);
  if (chat.working.has(key)) return "그 대화가 아직 일하는 중이에요";
  if (p.attachments.length) return "입력창에 첨부한 파일이 있어요";
  const kept = p.composerText;
  if (read) speakNext.add(key);
  if (saveTo) replySaves.set(key, { path: saveTo, text: "", kind, quiet });
  setPaneComposerValue(paneId, prompt);
  await sendFromPane(paneId);
  if (kept.trim()) setPaneComposerValue(paneId, kept);
  return "";
}

// saveTurnReply saves a finished turn's answer as the recording summary it
// was asked for.
function saveTurnReply(key) {
  const save = replySaves.get(key);
  if (!save) return;
  replySaves.delete(key);
  if (save.kind === "meeting") {
    saveMeetingWriteUp(save);
    return;
  }
  if (!save.text) return;
  VoiceService.SaveTapeSummary(save.path, save.text)
    .then((p) => {
      vlog(`tape summary saved ${p}`);
      flash("📝 녹음 요약을 저장했어요");
    })
    .catch((err) => vlog(`tape summary save failed: ${err?.message || err}`));
}

async function runAssistCommand(paneId, t) {
  try {
    const pc = parsePc(t);
    if (pc) await runPcCommand(pc);
    else if (parseUsage(t)) {
      const period = parseUsage(t);
      speak(usageText(period, parseJSON(await VoiceService.UsageSummary(period), {})));
    } else if (parseDaily(t)) await runDailyCommand(paneId, parseDaily(t));
    else if (parseMemo(t)) await runMemoCommand(parseMemo(t));
    else if (parseTapeSummary(t)) await summarizeTape(paneId, parseTapeSummary(t).notion);
    else return false;
  } catch (err) {
    vlog(`command failed: ${err?.message || err}`);
    speak("그 일을 하지 못했어요. 잠시 뒤에 다시 말씀해 주세요.");
  }
  return true;
}

function durationLabel(sec) {
  const min = Math.round(sec / 60);
  if (min < 60 || min % 60 === 30) return `${min}분`;
  return `${Math.floor(min / 60)}시간${min % 60 ? ` ${min % 60}분` : ""}`;
}

async function runPcCommand(pc) {
  vlog(`pc ${pc.kind} ${pc.target || pc.op || pc.seconds || ""}`);
  if (pc.kind === "open") {
    try {
      await VoiceService.PCOpen(pc.target);
      speak(`${pc.name}${eulReul(pc.name)} 열었어요.`);
    } catch (err) {
      vlog(`open failed: ${err?.message || err}`);
      speak(`${pc.name}${eulReul(pc.name)} 찾지 못했어요.`);
    }
  } else if (pc.kind === "volume") {
    const v = parseJSON(await VoiceService.PCVolume(pc.op, pc.value), {});
    flash(v.muted ? "🔇 소리 꺼짐" : `🔊 ${v.level}%`);
    if (pc.op === "mute") speak("소리를 껐어요.");
    else if (pc.op === "unmute") speak(`소리를 켰어요. 지금 ${v.level}퍼센트예요.`);
    else speak(`소리를 ${v.level}퍼센트로 ${pc.op === "down" ? "줄였어요" : pc.op === "up" ? "키웠어요" : "맞췄어요"}.`);
  } else if (pc.kind === "screenOff") {
    await VoiceService.PCScreenOff();
    speak("화면을 끌게요. 마우스를 움직이면 다시 켜져요.");
  } else if (pc.kind === "shutdown") {
    await VoiceService.PCShutdown(pc.seconds);
    speak(`${durationLabel(pc.seconds)} 뒤에 컴퓨터를 꺼요. 취소하려면 '컴퓨터 끄기 취소'라고 하세요.`);
  } else if (pc.kind === "shutdownAsk") {
    speak("언제 끌지 함께 말씀해 주세요. 30분 뒤에 컴퓨터 꺼 줘, 처럼요.");
  } else if (pc.kind === "shutdownCancel") {
    const r = await VoiceService.PCShutdownCancel();
    speak(r === "none" ? "예약된 컴퓨터 끄기가 없어요." : "컴퓨터 끄기를 취소했어요.");
  }
}

async function runDailyCommand(paneId, d) {
  const name = d.which === "briefing" ? "아침 브리핑" : "저녁 정리";
  const what = d.which === "briefing" ? "아침 브리핑을 해 드릴게요." : "오늘 메모와 남은 할 일을 읽어 드릴게요.";
  if (d.op === "set" || d.op === "on") {
    setDaily(d.which, d.op === "set" ? { on: true, h: d.h, m: d.m } : { on: true });
    const s = assist[d.which];
    speak(`매일 ${clockLabel(s.h, s.m)}에 ${what}`);
  } else if (d.op === "off") {
    setDaily(d.which, { on: false });
    speak(`${name}${eulReul(name)} 껐어요.`);
  } else if (d.op === "region") {
    setDaily("briefing", { region: d.region });
    speak(`브리핑 날씨는 ${d.region} 기준으로 알려 드릴게요.`);
  } else if (d.which === "evening") await readEvening();
  else {
    const why = await sendBriefing(paneId);
    if (why) speak(`${why}. 대화를 연 뒤에 다시 말씀해 주세요.`);
  }
}

// todaySchedule lists what is set for the rest of today — 예약 jobs and
// reminders on the host, and reminders kept here — for the briefing prompt.
async function todaySchedule() {
  const end = new Date();
  end.setHours(24, 0, 0, 0);
  let host = [];
  try {
    host = parseJSON(await ControlService.ListTasks("pending"), []);
  } catch {}
  return (Array.isArray(host) ? host : [])
    .map((t) => ({ at: new Date(t.nextFire && !t.nextFire.startsWith("0001") ? t.nextFire : t.fireAt).getTime(), label: t.label || (t.isTask ? "예약 작업" : "알림") }))
    .concat(alarms.map((a) => ({ at: a.at, label: a.msg || "알림" })))
    .filter((x) => Number.isFinite(x.at) && x.at >= Date.now() && x.at < end.getTime())
    .sort((x, y) => x.at - y.at)
    .slice(0, 8)
    .map((x) => {
      const d = new Date(x.at);
      return `${clockLabel(d.getHours(), d.getMinutes())} ${x.label}`;
    })
    .join(", ");
}

// sendBriefing sends the morning briefing's turn; "" when sent, else why not.
async function sendBriefing(paneId) {
  const target = assistPane(paneId);
  if (!target) return "브리핑을 보낼 대화가 없어요";
  const prompt = briefingPrompt(new Date(), assist.briefing.region || DAILY_DEFAULTS.briefing.region, await todaySchedule());
  const why = await sendTurn(target, prompt, { read: true });
  if (!why) vlog("briefing sent");
  return why;
}

// readEvening reads out today's memos and the to-dos still kept.
async function readEvening(auto = false) {
  const list = await refreshMemos();
  const today = dayKey();
  const memos = list.filter((m) => !m.todo && dayKey(new Date(m.at)) === today);
  const todos = list.filter((m) => m.todo);
  const parts = auto ? ["저녁 정리예요."] : [];
  if (!memos.length && !todos.length) parts.push("오늘 메모도, 남은 할 일도 없어요.");
  if (memos.length) parts.push(`오늘 메모 ${memos.length}개. ${memos.map((m) => m.text).join(". ")}.`);
  if (todos.length) parts.push(`남은 할 일 ${todos.length}개. ${todos.map((m) => m.text).join(". ")}.`);
  if (auto) flash("🌙 저녁 정리");
  void speak(parts.join(" "), "full");
}

let briefingSending = false;
let briefingSkip = ""; // why the due briefing was last not sent, logged once

// checkDaily does the briefing and the round-up when their time has come (or
// came less than two hours ago, with the app off). A briefing that cannot be
// sent yet (not connected, the chat busy) is tried again on the next check.
function checkDaily() {
  const now = new Date();
  if (dailyDue(assist.evening, now) && !voice.enrolling && !voice.meeting) {
    setDaily("evening", { last: dayKey(now) });
    vlog("evening round-up");
    void readEvening(true).catch((err) => vlog(`evening round-up failed: ${err?.message || err}`));
  }
  if (dailyDue(assist.briefing, now) && !briefingSending && !voice.enrolling && !voice.taping && !voice.meeting) {
    briefingSending = true;
    sendBriefing(null)
      .then((why) => {
        if (!why) setDaily("briefing", { last: dayKey(now) });
        else if (why !== briefingSkip) vlog(`briefing waits: ${why}`);
        briefingSkip = why;
      })
      .catch((err) => vlog(`briefing failed: ${err?.message || err}`))
      .finally(() => (briefingSending = false));
  }
}

function startDaily() {
  setTimeout(checkDaily, 10000); // after start-up: the chats and the connection come first
  setInterval(checkDaily, DAILY_CHECK_MS);
  void refreshMemos().catch(() => {});
}

// ---- memos ----

export async function refreshMemos() {
  const list = parseJSON(await VoiceService.MemoList(), []);
  assist.memos = Array.isArray(list) ? list : [];
  return assist.memos;
}

// deleteMemo removes one memo by id, or all of them with no id.
export async function deleteMemo(id) {
  assist.memos = parseJSON(await VoiceService.MemoDelete(id === undefined ? [] : [id]), []);
}

function memoLine(m, i) {
  return `${i + 1}번, ${m.todo ? "할 일, " : ""}${m.text}.`;
}

async function runMemoCommand(c) {
  if (c.op === "add") {
    assist.memos = parseJSON(await VoiceService.MemoAdd(c.text, c.todo), []);
    vlog(`memo added${c.todo ? " (todo)" : ""} ${quote(c.text)}`);
    speak(c.todo ? `할 일에 넣었어요. ${c.text}.` : `메모했어요. ${c.text}.`);
    return;
  }
  if (c.op === "ask") {
    speak("무엇을 메모할까요? 메모해 줘, 다음에 내용을 이어서 말씀해 주세요.");
    return;
  }
  const list = await refreshMemos();
  if (!list.length) {
    speak(c.op === "list" ? "메모가 없어요." : "지울 메모가 없어요.");
    return;
  }
  if (c.op === "list") {
    const more = list.length > MAX_READ_MEMOS ? ` 나머지 ${list.length - MAX_READ_MEMOS}개는 음성 설정에서 보세요.` : "";
    speak(`메모가 ${list.length}개 있어요. ${list.slice(0, MAX_READ_MEMOS).map(memoLine).join(" ")}${more}`, "full");
  } else if (c.op === "clear") {
    await deleteMemo();
    speak(`메모 ${list.length}개를 모두 지웠어요.`);
  } else if (c.op === "delete") {
    const m = list[c.n - 1];
    if (!m) {
      speak(`메모는 ${list.length}번까지 있어요. 다시 말씀해 주세요.`);
      return;
    }
    await deleteMemo(m.id);
    speak(`${c.n}번 메모, ${m.text}, 지웠어요.`);
  }
}

// ---- recording summary ----

// summarizeTape sends the latest recording's text for one AI turn that sums
// it up; the answer is saved next to the recording as "… 요약.md".
export async function summarizeTape(paneId = null, notion = false) {
  if (voice.taping) {
    speak("아직 녹음 중이에요. 녹음 끝내, 라고 하신 뒤에 말씀해 주세요.");
    return;
  }
  const last = parseJSON(await VoiceService.TapeLast(), {});
  if (!last.path) {
    speak("정리할 녹음이 없어요.");
    return;
  }
  const body = String(last.text || "").replace(/^녹음 [^\n]*\n/, "").trim();
  if (!body) {
    speak("방금 녹음에는 받아 적은 말이 없어요.");
    return;
  }
  const name = last.path.split(/[/\\]/).pop().replace(/\.txt$/i, "");
  const target = assistPane(paneId);
  const why = target ? await sendTurn(target, tapeSummaryPrompt(name, last.text, notion), { saveTo: last.path }) : "열린 대화가 없어요";
  if (why) {
    speak(`${why}. 잠시 뒤에 다시 말씀해 주세요.`);
    return;
  }
  vlog(`tape summary asked ${name} (${last.chars} chars)${notion ? " +notion" : ""}`);
  speak(notion ? "녹음을 정리해서 노션에도 저장할게요." : "녹음을 정리할게요. 끝나면 녹음 옆에 요약 파일로 저장해 둘게요.");
}

// ---- reminders ----
//
// "30분 뒤에 회의 알려 줘" is set here rather than sent (alarms.js parses it),
// and read out when its time comes — in conversation, waiting for the wake
// word, or with the window in the tray.

const REMINDER_RE = /^\s*🔔\s*([\s\S]+)$/u;

// The conversation a spoken reminder reports to: the one being talked to.
function alarmTarget() {
  const t = pane(voice.convPaneId || chat.focusedPaneId)?.target;
  return t?.kind === "web" && t.id ? { kind: "web", id: t.id } : { kind: "telegram" };
}

// Reminders set by voice live in the 예약 scheduler on the host (so they show
// in the 예약 panel and go off even with this app closed); the local list in
// alarms.js is only the fallback when the host cannot be reached.
async function saveAlarmTask(a) {
  const d = new Date(a.at);
  const payload = {
    label: a.msg || (a.wake ? "깨우기" : "알림"),
    isTask: false,
    prompt: fireText(a),
    cronExpr: a.daily ? `${d.getMinutes()} ${d.getHours()} * * *` : "",
    fireAt: a.daily ? "" : d.toISOString(),
    target: alarmTarget(),
  };
  const j = parseJSON(await ControlService.SaveTask(JSON.stringify(payload)), {});
  if (j?.ok === false) throw new Error(j.error || "저장 실패");
}

// hostReminders lists the pending 🔔 reminders (not AI jobs) as alarm-like
// items, soonest first; null when the host cannot be reached.
async function hostReminders() {
  try {
    const list = parseJSON(await ControlService.ListTasks("pending"), []);
    return (Array.isArray(list) ? list : [])
      .filter((t) => !t.isTask)
      .map((t) => {
        const at = new Date(t.nextFire && !t.nextFire.startsWith("0001") ? t.nextFire : t.fireAt).getTime();
        return { hostId: t.id, at, msg: t.label && !["알림", "깨우기"].includes(t.label) ? t.label : "", daily: !!t.cronExpr, cron: t.cronExpr || "", wake: t.label === "깨우기", made: new Date(t.createdAt).getTime() || 0 };
      })
      .filter((a) => Number.isFinite(a.at))
      .sort((x, y) => x.at - y.at);
  } catch {
    return null;
  }
}

function alarmLine(a) {
  // A host reminder may repeat on weekdays or every 30 minutes, not just daily.
  const when = a.cron ? describeCron(a.cron) : a.daily ? `매일 ${timeLabel(a.at).replace(/^(내일|모레) /, "")}` : timeLabel(a.at);
  return `${when}${a.msg ? `, ${a.msg}` : a.wake ? ", 깨우기" : ""}`;
}

// runAlarmCommand sets, reads out or cancels reminders; false means text is
// none of those.
async function runAlarmCommand(t) {
  const listing = ALARM_LIST_RE.test(t);
  const cancel = !listing && ALARM_CANCEL_RE.exec(t);
  if (listing || cancel) {
    const host = await hostReminders();
    const all = [...(host || []), ...alarms].sort((x, y) => x.at - y.at);
    if (listing) {
      if (!all.length) speak("맡기신 알림이 없어요.");
      else speak(`알림이 ${all.length}개 있어요. ${all.map((a, i) => `${i + 1}번, ${alarmLine(a)}.`).join(" ")}`);
      return true;
    }
    if (!all.length) {
      speak("취소할 알림이 없어요.");
      return true;
    }
    const nthWord = cancel[3];
    let picked;
    if (cancel[8]) picked = all;
    else if (nthWord) {
      const n = /^\d+$/.test(nthWord) ? Number(nthWord) : NTH_WORDS[nthWord];
      if (!all[n - 1]) {
        speak(`알림은 ${all.length}번까지 있어요. 다시 말씀해 주세요.`);
        return true;
      }
      picked = [all[n - 1]];
    } else picked = [all.length === 1 ? all[0] : all.reduce((x, y) => ((y.made || 0) > (x.made || 0) ? y : x))]; // the one set last
    let failed = 0;
    for (const a of picked) {
      if (!a.hostId) {
        removeAlarm(a.id);
        continue;
      }
      // The host answers {ok:false} when it could not delete it.
      const j = await ControlService.DeleteTask(a.hostId)
        .then((raw) => parseJSON(raw, {}))
        .catch((err) => ({ ok: false, error: err?.message || err }));
      if (j?.ok === false) {
        failed++;
        vlog(`alarm cancel failed: ${j.error || "?"}`);
      }
    }
    vlog(`alarms cancelled ${picked.length - failed}`);
    if (failed) speak(picked.length > 1 ? `알림 ${picked.length}개 중 ${failed}개를 취소하지 못했어요. 잠시 뒤에 다시 말씀해 주세요.` : "알림을 취소하지 못했어요. 잠시 뒤에 다시 말씀해 주세요.");
    else speak(picked.length > 1 ? `알림 ${picked.length}개를 모두 취소했어요.` : `${alarmLine(picked[0])} 알림을 취소했어요.`);
    return true;
  }
  const a = parseAlarm(t);
  if (!a) return false;
  try {
    await saveAlarmTask(a);
    vlog(`alarm set on the host ${new Date(a.at).toLocaleString()} ${quote(a.msg)}${a.daily ? " daily" : ""}`);
  } catch (err) {
    // The host is down: kept here and read out while this app runs.
    addAlarm(a);
    vlog(`alarm kept locally (${err?.message || err}) ${quote(a.msg)}`);
  }
  speak(confirmText(a));
  return true;
}

function fireAlarm(a, late) {
  vlog(`alarm ${late ? "(late) " : ""}${quote(alarmLine(a))}`);
  if (voice.enrolling) return;
  flash(`⏰ ${a.msg || "알림"}`);
  void speak(fireText(a, late));
}

// ---- whose voice ----

function clipSeconds(wav) {
  return wav ? (wav.length - 44) / (TARGET_RATE * 2) : 0;
}

// speakerScore: how much a clip sounds like the enrolled user, -1 if nobody
// is enrolled, -2 if someone is but the check could not run (see speaker.go).
function speakerScore(wav) {
  return VoiceService.SpeakerScore(toBase64(wav)).catch(() => -2);
}

// userWindow scores a long clip in RESCUE_WINDOW_S windows and returns the
// stretch covered by the windows that sound like the user ({score, from, to,
// share} in seconds, score the best window's, share the part of the windows
// that pass), or null when none does.
async function userWindow(wav, min) {
  const secs = clipSeconds(wav);
  if (secs < RESCUE_MIN_S) return null;
  const hop = Math.max(RESCUE_HOP_S, (secs - RESCUE_WINDOW_S) / (RESCUE_MAX_WINDOWS - 1));
  const starts = [];
  for (let t = 0; t < secs - RESCUE_WINDOW_S; t += hop) starts.push(t);
  starts.push(Math.max(0, secs - RESCUE_WINDOW_S)); // the end, where a request usually is
  let best = -1;
  let from = -1;
  let to = -1;
  let hits = 0;
  for (const t of starts) {
    const end = Math.min(secs, t + RESCUE_WINDOW_S);
    const sc = await speakerScore(wavSlice(wav, t, end));
    if (sc > best) best = sc;
    if (sc < min) continue;
    hits++;
    if (from < 0) from = t;
    to = end;
  }
  return from >= 0 ? { score: best, from, to, share: hits / starts.length } : null;
}

// wavSlice cuts the from..to seconds out of a 16-bit mono clip at TARGET_RATE.
function wavSlice(wav, from, to) {
  const a = 44 + Math.max(0, Math.floor(from * TARGET_RATE)) * 2;
  const b = Math.min(wav.length, 44 + Math.floor(to * TARGET_RATE) * 2);
  const out = new Uint8Array(44 + Math.max(0, b - a));
  out.set(wav.subarray(0, 44));
  out.set(wav.subarray(a, b), 44);
  const v = new DataView(out.buffer);
  v.setUint32(4, out.length - 8, true);
  v.setUint32(40, out.length - 44, true);
  return out;
}

function isUserVoice(score, min) {
  return score === -1 || score >= min;
}

// enrollVoice records ~15 s of the user talking ("목소리 등록") as the voice to
// listen to; clips that do not sound like it are not sent from then on.
export async function enrollVoice() {
  let status = {};
  try {
    status = JSON.parse(await VoiceService.SpeakerStatus());
  } catch {}
  if (!status.available) {
    speak("목소리 구별 기능이 설치되어 있지 않아서 등록할 수 없어요.");
    return;
  }
  voice.enrolling = true;
  try {
    await speak("지금부터 십오 초 동안 평소 말투로 아무 이야기나 해 주세요. 시작할게요.");
    while (voice.speaking) await new Promise((r) => setTimeout(r, 100));
    const wav = await recordFor(ENROLL_MS);
    if (!wav) {
      speak("마이크를 열지 못해서 등록하지 못했어요.");
      return;
    }
    await VoiceService.EnrollSpeaker(toBase64(wav));
    forgetUserLevel(); // a new voice (or a new mic spot) is learned afresh
    vlog(`speaker enrolled (${clipSeconds(wav).toFixed(1)} s)`);
    speak("목소리를 등록했어요. 이제 다른 사람 말은 보내지 않아요. 지우고 싶으면 목소리 등록 해제라고 말씀해 주세요.");
  } catch (err) {
    vlog(`speaker enroll failed: ${err?.message || err}`);
    speak("목소리 등록에 실패했어요.");
  } finally {
    voice.enrolling = false;
  }
}

// cloneVoice records CLONE_MS of the person reading CLONE_SCRIPT (shown on
// screen) and makes it the voice replies are read in. Nothing heard while it
// records is sent.
export async function cloneVoice(paneId = voice.convPaneId) {
  let status = {};
  try {
    status = JSON.parse(await VoiceService.CloneVoiceStatus());
  } catch {}
  if (!status.available) {
    speak("목소리 바꾸기 기능이 설치되어 있지 않아서 녹음할 수 없어요.");
    return;
  }
  voice.enrolling = true;
  const script = `**이 글을 또박또박 읽어 주세요 (25초)**\n\n${CLONE_SCRIPT}`;
  try {
    if (paneId) setNote(paneId, `이 글을 또박또박 읽어 주세요 (25초): ${CLONE_SCRIPT}`);
    Events.Emit("voice:reply", { text: script, silent: true });
    // Shown large in the main window, brought up from the tray if hidden:
    // the one-line note and the small reply box are too small to read from.
    voice.readScript = CLONE_SCRIPT;
    voice.readUntil = 0;
    Events.Emit("indicator:open");
    await speak("목소리를 녹음할게요. 화면에 뜬 글을 이십오 초 동안 또박또박 읽어 주세요. 띵 소리가 나면 시작하세요.");
    while (voice.speaking) await new Promise((r) => setTimeout(r, 100));
    await playChime();
    flash("🎙 목소리 녹음 중 — 화면의 글을 읽어 주세요");
    voice.readUntil = Date.now() + CLONE_MS;
    const wav = await recordFor(CLONE_MS);
    voice.readScript = "";
    if (!wav) {
      speak("마이크를 열지 못해서 녹음하지 못했어요.");
      return;
    }
    flash("🎙 목소리를 익히는 중");
    await speak("녹음했어요. 목소리를 익히는 중이에요. 잠깐만 기다려 주세요.");
    await VoiceService.SetCloneVoice(toBase64(wav));
    prefetched.clear(); // made in the old voice
    setVoiceSettings({ ttsVoice: CLONE_VOICE });
    vlog(`clone voice set (${clipSeconds(wav).toFixed(1)} s)`);
    speak("이제 이 목소리로 읽어 드릴게요. 원래 목소리로 돌려 줘, 라고 하시면 돌아가요. 다른 목소리들은 그대로예요.");
  } catch (err) {
    vlog(`clone voice failed: ${err?.message || err}`);
    speak("목소리를 익히지 못했어요. 조용한 곳에서 다시 해 주세요.");
  } finally {
    voice.enrolling = false;
    voice.readScript = "";
    resumeListening();
  }
}

export async function cloneVoiceStatus() {
  try {
    return JSON.parse(await VoiceService.CloneVoiceStatus());
  } catch {
    return {};
  }
}

export async function clearCloneVoice() {
  await VoiceService.ClearCloneVoice();
  if (voice.ttsVoice === CLONE_VOICE) setVoiceSettings({ ttsVoice: VOICE_DEFAULTS.ttsVoice });
  prefetched.clear();
  vlog("clone voice cleared");
}

// recordFor records the mic for ms and returns it as 16 kHz WAV, or null.
async function recordFor(ms) {
  let stream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
  } catch (error) {
    vlog(`mic failed: ${error?.message || error}`);
    return null;
  }
  const ctx = new AudioContext();
  const source = ctx.createMediaStreamSource(stream);
  const node = ctx.createScriptProcessor(4096, 1, 1);
  const chunks = [];
  node.onaudioprocess = (e) => {
    const input = e.inputBuffer.getChannelData(0);
    chunks.push(new Float32Array(input));
    let sum = 0;
    for (let i = 0; i < input.length; i++) sum += input[i] * input[i];
    voice.level = Math.min(1, Math.sqrt(sum / input.length) * 12);
  };
  source.connect(node);
  node.connect(ctx.destination);
  const phase = voice.phase;
  voice.phase = "listening";
  await new Promise((r) => setTimeout(r, ms));
  node.onaudioprocess = null;
  source.disconnect();
  node.disconnect();
  stream.getTracks().forEach((t) => t.stop());
  const rate = ctx.sampleRate;
  void ctx.close();
  voice.level = 0;
  voice.phase = phase;
  return encodeWav(downsample(concat(chunks), rate, TARGET_RATE), TARGET_RATE);
}

// heard is the clip's text when tryEarlyStop already has it.
function stopRecording(transcribe, heard = null) {
  const rec = recorder;
  if (!rec) return;
  recorder = null;
  voice.hearing = false;
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
  const screen = () => conv() && !rec?.manual;
  let relisten = false;
  let retry = false;
  try {
    if (rec) await rec.bargeDone;
    const scoring = screen() && wavBytes ? speakerScore(wavBytes) : Promise.resolve(-1);
    let heard = known ?? (wavBytes ? (await VoiceService.Transcribe(toBase64(wavBytes))).trim() : "");
    // Someone else in the room (or the reply from the speakers) is not sent once
    // the user has enrolled their voice.
    let score = await scoring;
    const level = rec && rec.loudChunks > 0 ? rec.speechSum / rec.loudChunks : 0;
    const quiet = screen() && isQuiet(level);
    const bar = speakerMinFor(clipSeconds(wavBytes) < SPEAKER_SHORT_S) + (quiet ? QUIET_EXTRA_BAR : 0);
    const rescued = heard && screen() && wavBytes && !quiet && score >= 0 && score < bar ? await userWindow(wavBytes, bar + RESCUE_EXTRA_BAR) : null;
    if (rescued) {
      const whole = score;
      const secs = clipSeconds(wavBytes);
      score = rescued.score;
      if (rescued.from > 0.2 || rescued.to < secs - 0.2) {
        const part = (await VoiceService.Transcribe(toBase64(wavSlice(wavBytes, rescued.from, rescued.to)))).trim();
        if (part) heard = part;
      }
      vlog(`voice rescued (window ${score.toFixed(2)}, whole ${whole.toFixed(2)}, ${rescued.from.toFixed(1)}-${rescued.to.toFixed(1)} of ${secs.toFixed(1)} s) ${quote(heard, 20)}`);
    }
    const secs = clipSeconds(wavBytes);
    const long = !rescued && heard && screen() && wavBytes && !quiet && score >= bar && score < bar + LONG_CHECK_MARGIN && secs >= LONG_CHECK_S;
    const win = long ? await userWindow(wavBytes, bar) : null;
    if (long && (!win || win.share < LONG_CHECK_SHARE)) {
      vlog(`mixed voices dropped (whole ${score.toFixed(2)}, ${Math.round((win?.share ?? 0) * 100)}% of windows, ${secs.toFixed(1)} s) ${quote(heard)}`);
      logHeard(heard, score, false, "여러 사람 대화");
      flash("여러 사람 목소리라 넘겼어요");
      heard = "";
    } else if (win && (win.from > 0.2 || win.to < secs - 0.2)) {
      const part = (await VoiceService.Transcribe(toBase64(wavSlice(wavBytes, win.from, win.to)))).trim();
      if (part) heard = part;
      vlog(`long clip trimmed (whole ${score.toFixed(2)}, ${Math.round(win.share * 100)}% of windows, ${win.from.toFixed(1)}-${win.to.toFixed(1)} of ${secs.toFixed(1)} s) ${quote(heard, 20)}`);
    }
    if (heard && quiet && score < 0) {
      vlog(`quiet dropped (level ${level.toFixed(3)}) ${quote(heard)}`);
      logHeard(heard, score, false, "작은 소리");
      flash("소리가 작아서 넘겼어요");
      heard = "";
    } else if (heard && !isUserVoice(score, bar)) {
      vlog(`other voice dropped (${score.toFixed(2)}, level ${level.toFixed(3)}${quiet ? " quiet" : ""}) ${quote(heard)}`);
      logHeard(heard, score, false, quiet ? "작은 소리" : "다른 목소리");
      flash(quiet ? "소리가 작아서 넘겼어요" : "다른 목소리라 넘겼어요");
      heard = "";
    } else if (heard && score >= 0) {
      vlog(`voice ${score.toFixed(2)} level ${level.toFixed(3)} ${quote(heard, 20)}`);
      logHeard(heard, score, true);
      if (score >= voice.speakerMin + QUIET_EXTRA_BAR && !rescued) learnUserLevel(level); // a rescued clip's level is mostly the TV
    }
    // whisper writes a back-and-forth as "- 그쵸. - 네.": people talking in the
    // room or on TV, not a request. Sent, each one cost a full AI turn.
    if (heard && screen() && (heard.match(DIALOGUE_RE) || []).length >= 2) {
      vlog(`dialogue dropped ${quote(heard)}`);
      logHeard(heard, score, false, "주변 대화");
      heard = "";
    }
    // Safety net: a clip that is just the reply read back through the speakers
    // (its tail, or all of it) is never sent as the user's words. Speech that
    // starts as the reading ends is most likely the last of it still coming out
    // of the speakers, so even a short, half-heard fragment of it is dropped.
    const tail = rec && rec.firstSpeechAt - readingEndedAt < ECHO_TAIL_MS;
    // The user often says back words the reply just used ("자세히 읽어 줘"
    // right after it was suggested). Our voice from the speakers scores far
    // under the bar, so a clip clearly in the enrolled voice, or one that is a
    // voice command, is never taken for the echo.
    const trusted = score >= 0 && score >= voice.speakerMin + ECHO_TRUST_MARGIN;
    if (conv() && heard && replyText && (trusted || isCommandText(heard))) {
      if (sentencesOf(heard).some((s) => isEcho(s, replyText, tail ? 0.4 : ECHO_OVERLAP)))
        vlog(`echo check skipped (${trusted ? `voice ${score.toFixed(2)}` : "command"}) ${quote(heard)}`);
    } else if (conv() && heard && replyText && !CONV_STOP_RE.test(heard)) {
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
    if (conv()) convActiveAt = Date.now();
    if (conv() && (await runVoiceCommand(paneId, text))) {
      vlog(`command ${quote(text)}`);
      relisten = true;
      return;
    }
    if (conv() && voice.vscode) {
      sendToVSCode(text);
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
      if (conv()) {
        voice.heard = text;
        setAwaited(paneId);
      }
      convActiveAt = Date.now();
      if (ONCE_TEXT_RE.test(text)) textOnce.add(targetKey(p.target));
      sendFailures = 0;
      await sendFromPane(paneId, { voice: true }); // the bubble shows "🎤 인식했습니다", the model gets VOICE_PREFIX
    }
    else {
      setNote(paneId, "aglink 에 연결되어 있지 않아 입력창에만 넣었습니다.");
      if (conv()) retry = true, sendFailures++;
    }
  } catch (error) {
    vlog(`transcribe failed: ${error?.message || error}`);
    setNote(paneId, `음성 인식 실패: ${error?.message || error}`);
    if (conv()) retry = true, sendFailures++;
  } finally {
    voice.phase = "idle";
    voice.paneId = null;
    if (retry && sendFailures >= MAX_SEND_FAILURES && conv()) {
      // Retrying every few seconds only piles up the same error: stop and keep the note.
      sendFailures = 0;
      endConversation();
    } else if (relisten && conv()) {
      if (!endIfIdle(paneId)) resumeListening(0);
    } else if (retry && conv()) resumeListening(CONV_RETRY_MS);
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
// The mic's own echo cancellation only knows what the page plays. The reply
// now plays in the page (playWavContext) so it can, but it still falls back to
// Windows (PlayWav), where the canceller hears nothing of it. Either way we
// know exactly what is playing and when, so the mic level our voice alone
// produces can be predicted: the clip's loudness envelope times a room gain
// learned from the mic while it plays (with the canceller working that gain
// just comes out lower). Sound below a margin over that never starts a
// segment, so our own voice is not transcribed at all; the text comparison
// (isEcho) stays as the second line.
const REF_FRAME_MS = 20;
const REF_BEFORE_MS = 700; // speaker + mic latency + a small room's reverb tail: the mic hears now what played this long ago
const REF_AFTER_MS = 100; // PlayWav starts a little after it is called
const ECHO_MARGIN = 3; // the user's voice is this much above our echo
const ECHO_GAIN_MIN = 0.08; // lowest room gain assumed while a reply plays
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
  // Always told, even between clips: a PlayWav already on its way to Go is
  // then dropped there instead of starting after this.
  nativePlaying = false;
  if (nativeOK) VoiceService.StopWav(speakGen).catch(() => {});
  speakGen++;
  if (audio) {
    audio.pause();
    audio = null;
  }
  if (pageClip) {
    pageClip.stop();
    pageClip = null;
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
const ITEM_LABEL_RE = /^([^.!?:：\d]{1,20})[:：]\s*/; // no digits: "10:30" is not a label

// A subject label ("강남·서초:") is kept as "강남·서초," or the point loses
// what it is about; a report lead-in ("고친 것:") is dropped.
function speakLabel(s) {
  return s.replace(ITEM_LABEL_RE, (_, label) => (/(것|점|일|이유|결과|방법)\s*$/.test(label) ? "" : `${label.trim()}, `));
}

function speakableParas(md) {
  let s = String(md || "").replace(/\r\n/g, "\n");
  s = s.replace(/^\s*(출처|참고|Sources?|References?)\s*:?\s*$[\s\S]*/im, ""); // trailing sources block
  s = s.replace(/```[\s\S]*?```/g, "\n\n");
  s = s.replace(/^\s*\|.*\|\s*$/gm, ""); // tables
  s = s.replace(/^\s{0,3}#{1,6}\s.*$/gm, "\n"); // headings
  return s.split(/\n\s*\n/);
}

function speechMode() {
  return voice.summary ? "short" : "gist";
}

export function speechText(md, mode = speechMode()) {
  const paras = speakableParas(md);
  if (mode === "full") return fullSpeechText(paras);
  const { chars: MAX_SPEAK_CHARS, sentences: MAX_SPEAK_SENTENCES, points: MAX_SPEAK_POINTS } = SPEAK_LIMITS[mode] || SPEAK_LIMITS.gist;
  if (mode === "gist") {
    // A reply that fits is read whole; picking sentences out of it sounds like it broke off.
    const full = fullSpeechText(paras);
    if (full.length <= MAX_SPEAK_CHARS) return full;
  }
  let opening = "";
  let i = 0;
  for (; i < paras.length && opening.length < MIN_GIST_CHARS; i++) {
    if (LIST_ITEM_RE.test(paras[i])) break; // a reply that opens with a list
    const prose = cleanProse(paras[i]);
    if (prose) opening = opening ? `${opening} ${prose}` : prose;
  }
  let out = "";
  for (const x of sentencesOf(opening).slice(0, MAX_SPEAK_SENTENCES)) {
    if (out && (out + x).length > MAX_SPEAK_CHARS) break;
    out += x;
  }
  const points = [];
  let closing = "";
  for (const para of paras.slice(i)) {
    const items = para.split(/\n(?=[ ]{0,1}(?:[-*+]|\d+[.)])\s)/).filter((l) => LIST_ITEM_RE.test(l));
    if (items.length) {
      for (const item of items) {
        // A "**고친 것:**" lead-in read on its own sounds curt, like a report
        // being handed over; the sentence after it carries the point.
        const point = sentencesOf(speakLabel(cleanProse(item.split("\n")[0])))[0];
        if (point) points.push(point);
      }
      closing = "";
      continue;
    }
    const prose = cleanProse(para);
    if (!prose) continue;
    // Later prose paragraphs carry points too; skipping them cut readings short.
    const point = sentencesOf(speakLabel(prose))[0];
    if (point && !/[?？]\s*$/.test(point)) points.push(point);
    closing = prose;
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

// fullSpeechText is the whole reply as speech for "자세히 읽어 줘": every prose
// paragraph and every list item in full, still without code, tables or links.
function fullSpeechText(paras) {
  const parts = [];
  for (const para of paras) {
    const items = para.split(/\n(?=[ ]{0,3}(?:[-*+]|\d+[.)])\s)/);
    for (const item of items) {
      const prose = speakLabel(cleanProse(item.replace(/\n+/g, " ")));
      if (prose) parts.push(/[.!?。？！]$/.test(prose) ? prose : `${prose}.`);
    }
  }
  let out = parts.join(" ").replace(/\s+/g, " ").trim();
  if (out.length > MAX_FULL_CHARS) out = sentencesOf(out.slice(0, MAX_FULL_CHARS)).slice(0, -1).join("").trim();
  return out;
}

function sentencesOf(text) {
  return (text || "").match(/(?:[^.!?。？！]|\.(?=\d))+[.!?。？！]*\s*/g) || []; // "0.09%" does not end a sentence
}

// splitSentences keeps each utterance short: speechSynthesis stalls on long
// ones, and with Supertonic the first sentence starts playing sooner.
function splitSentences(text) {
  const parts = text.match(/(?:[^.!?。？！\n]|\.(?=\d))+[.!?。？！]*\s*/g) || [text];
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

// Bumped by stopSpeaking to abandon a reading in progress. It starts from the
// clock so it keeps rising across a page reload, which Go's StopWav relies on.
let speakGen = Date.now();
const prefetched = new Map(); // sentence -> Synthesize promise started before the turn was done
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

// playWav plays in the page first, through Web Audio: only what the page
// plays reaches the mic's echo canceller as the far end (Chromium's AEC3, made
// page-wide by echoCancelFeatures in main.go), so talking over the reading is
// heard without our own voice under it. When the page cannot play — its
// AudioContext does not run, as the window hidden in the tray kept <audio>
// from playing before — or the setting is off, the OS plays it as before.
async function playWav(b64, gen) {
  if (gen !== speakGen) return;
  const ref = addPlayRef(b64);
  if (voice.pagePlayback && !pageStalled) {
    if (await playWavContext(b64, gen)) return;
    if (gen !== speakGen) return;
    // Started over by the OS: the reference starts over with it.
    if (ref) {
      const length = ref.endAt - ref.startAt;
      ref.startAt = performance.now();
      ref.endAt = ref.startAt + length;
    }
  }
  if (nativeOK) {
    nativePlaying = true;
    try {
      await VoiceService.PlayWav(b64, gen);
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

// The page's player. One AudioContext is kept for every clip: a new one each
// time costs a device open per sentence.
const PAGE_START_MS = 400; // a context whose clock has not moved by then is not playing
let playCtx = null;
let pageClip = null; // {stop} of the clip playWavContext is playing now
let pageStalled = false; // the page could not play — the OS plays until the window is shown again
let aecLogged = false; // the mic's echo cancellation settings are logged once

// logMicProcessing notes once whether the mic really got echo cancellation:
// the constraint is only a request.
function logMicProcessing(stream) {
  if (aecLogged) return;
  aecLogged = true;
  const s = stream.getAudioTracks()[0]?.getSettings?.() || {};
  vlog(`mic echoCancellation=${s.echoCancellation} noiseSuppression=${s.noiseSuppression} autoGainControl=${s.autoGainControl}`);
}

if (typeof document !== "undefined")
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) pageStalled = false;
  });

// playWavContext plays a clip in the page and resolves when it ends or is cut
// off: true, or false when it could not play (then nothing was heard of it).
async function playWavContext(b64, gen) {
  try {
    if (!playCtx || playCtx.state === "closed") playCtx = new AudioContext({ latencyHint: "interactive" });
    const ctx = playCtx;
    const bin = atob(b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    const buffer = await ctx.decodeAudioData(bytes.buffer);
    if (ctx.state !== "running") await Promise.race([ctx.resume(), new Promise((r) => setTimeout(r, PAGE_START_MS))]);
    if (gen !== speakGen) return true;
    if (ctx.state !== "running") throw new Error(`AudioContext ${ctx.state}`);
    const node = ctx.createBufferSource();
    node.buffer = buffer;
    node.connect(ctx.destination);
    const startedAt = ctx.currentTime;
    return await new Promise((resolve) => {
      let watch = 0;
      const clip = {
        stop(played = true) {
          clearTimeout(watch);
          node.onended = null;
          try {
            node.stop();
          } catch {}
          node.disconnect();
          if (pageClip === clip) pageClip = null;
          resolve(played);
        },
      };
      pageClip = clip;
      node.onended = () => clip.stop();
      // A context can report "running" yet not render (a suspended webview):
      // its clock then stands still, and the clip goes to the OS instead.
      watch = setTimeout(() => {
        if (ctx.currentTime - startedAt >= (PAGE_START_MS / 1000) * 0.5) return;
        console.warn("[voice] page playback stalled, playing through Windows");
        pageStalled = true;
        clip.stop(false);
      }, PAGE_START_MS);
      node.start();
    });
  } catch (err) {
    console.warn("[voice] page playback failed, playing through Windows", err);
    pageStalled = true;
    return false;
  }
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
function synthesize(sentence) {
  const early = prefetched.get(sentence);
  prefetched.delete(sentence);
  return early ?? VoiceService.Synthesize(speakNumbers(sentence), voice.ttsVoice, voice.ttsSpeed);
}

// prefetchSpeech makes a reply's first sentence as soon as its text arrives:
// the turn's "done" comes a moment later, and by then it is ready to play.
// A note sent mid-turn that is not read in the end only costs a synthesis.
function prefetchSpeech(md) {
  if (ttsReady !== true) return;
  const first = splitSentences(speechText(md))[0];
  if (!first || prefetched.has(first)) return;
  const p = VoiceService.Synthesize(speakNumbers(first), voice.ttsVoice, voice.ttsSpeed);
  p.catch(() => {});
  prefetched.set(first, p);
  if (prefetched.size > 3) prefetched.delete(prefetched.keys().next().value);
}

async function speakSupertonic(sentences, gen) {
  let b64;
  try {
    b64 = await synthesize(sentences[0]);
  } catch (err) {
    vlog(`Supertonic failed, Windows voice instead: ${err?.message || err}`);
    ttsReady = null; // asked again next time — the Go side retries the server after a while
    flash("음성 엔진을 다시 켜는 중이라 기본 목소리로 읽어요");
    return false;
  }
  let handedOff = false; // the Windows voice reads the rest and ends the reading
  try {
    for (let i = 0; gen === speakGen; i++) {
      const next = i + 1 < sentences.length ? synthesize(sentences[i + 1]) : null;
      next?.catch(() => {}); // awaited below; this only silences an abandoned one
      if (b64) await playWav(b64, gen);
      if (!next) break;
      // A sentence that fails means Supertonic is stuck and being restarted;
      // the rest of the reply is read with the Windows voice.
      b64 = await next.catch((err) => {
        vlog(`Supertonic failed mid-reply, Windows voice for the rest: ${err?.message || err}`);
        return null;
      });
      if (!b64 && gen === speakGen) {
        ttsReady = null;
        handedOff = true;
        speakWindows(sentences.slice(i + 1), gen);
        return true;
      }
    }
  } catch (err) {
    console.warn("[voice] Supertonic sentence failed", err);
  } finally {
    if (gen === speakGen && !handedOff) {
      voice.speaking = false;
      readingEndedAt = performance.now();
      if (echoGain !== null) vlog(`echo gain ${echoGain.toFixed(3)} (${echoRatios.length} samples)`);
    }
  }
  return true;
}

function speakWindows(sentences, gen) {
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
    // A cancelled reading's end arrives late, during the next one; it must not end that.
    if (i === sentences.length - 1)
      u.onend = u.onerror = () => {
        if (gen !== speakGen) return;
        voice.speaking = false;
        readingEndedAt = performance.now();
      };
    speechSynthesis.speak(u);
  });
}

let lastReply = ""; // the last assistant reply read out, for "다시 읽어 줘" / "자세히 읽어 줘"
let detailHinted = false; // "자세히 읽어 줘" is mentioned once a run, the first time a reply is cut

const textOnce = new Set(); // chats whose next reply was asked for "글로만"

function speakReply(md, key = "", mode = speechMode()) {
  lastReply = md;
  if (voice.textOnly || textOnce.has(key)) return showReply(md);
  let tail = "";
  if (voice.summary && !detailHinted && voice.convPaneId) {
    const cut = speechText(md, "full").length > speechText(md).length + CUT_MARGIN;
    if (cut) {
      detailHinted = true;
      tail = "자세히 들으시려면 자세히 읽어 줘, 라고 하세요.";
    }
  }
  emitReply(md);
  return speak(md, mode, tail);
}

// emitReply puts an assistant reply up in the indicator's popup, which shows
// it whole while only its gist is read. The app's own words ("네, 말씀하세요.")
// are not sent, so they never replace the reply there.
function emitReply(md, silent = false) {
  const text = String(md || "").trim();
  if (text && (voice.convPaneId || savedConversationPane())) Events.Emit("voice:reply", silent ? { text, silent } : { text });
}

// speakAgain reads the last reply once more, for "다시 읽어 줘" / "요약해서 읽어 줘".
function speakAgain(mode = speechMode()) {
  if (!lastReply) return speak("아직 읽어 드린 답변이 없어요.");
  emitReply(lastReply);
  return speak(lastReply, mode);
}

// showReply puts the reply up as text without reading it: in the popup above
// the indicator while the window is in the tray, where it stays until closed.
// "다시 읽어 줘" / "자세히 읽어 줘" still read it on request.
function showReply(md) {
  const text = String(md || "").trim();
  if (!text) return;
  vlog("reply shown as text only");
  emitReply(text, true);
  flash("📄 답변을 글로 띄웠어요");
}

// speakDetail reads the last reply in full.
function speakDetail() {
  if (!lastReply) return speak("아직 읽어 드린 답변이 없어요.");
  vlog("reading the last reply in full");
  emitReply(lastReply);
  return speak(lastReply, "full");
}

export async function speak(md, mode = speechMode(), tail = "") {
  const text = [speechText(md, mode), tail].filter(Boolean).join(" ");
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
  if (paneId && voice.enrolling) {
    // the enrollment records on its own
  } else if (paneId && voice.bargeEnabled && !everyTime() && !recorder && voice.phase === "idle") void startRecording(paneId, true);
  else if (paneId && !recorder) resumeListening(); // taking turns: the mic opens after the reading
  if ((await supertonicReady()) && (await speakSupertonic(sentences, gen))) return;
  if (gen !== speakGen) return;
  speakWindows(sentences, gen);
}

setAssistantTextListener((key, text) => {
  // A reminder from the 예약 scheduler ("🔔 회의 시간이에요.") is read out
  // whichever conversation it lands in, as long as the voice is set up.
  const bell = REMINDER_RE.exec(text);
  if (bell) {
    if (voice.status === "ready" && !voice.enrolling) {
      vlog(`reminder ${quote(bell[1])}`);
      flash(`⏰ ${quote(bell[1], 20)}`);
      void speak(bell[1], "full");
    }
    return;
  }
  if (STATUS_LINE_RE.test(text)) return;
  const save = replySaves.get(key);
  if (save) save.text = text; // the last one is the answer
  if (save?.quiet) return; // a meeting write-up is saved, not read out
  // A remembered conversation counts before it has resumed after a restart,
  // so a reply that lands in that gap is still read out.
  const convId = voice.convPaneId || savedConversationPane();
  const conv = convId && pane(convId);
  const focused = pane(chat.focusedPaneId);
  const wanted =
    (conv?.target && targetKey(conv.target) === key) ||
    (voice.speakEnabled && focused?.target && targetKey(focused.target) === key) ||
    speakNext.has(key);
  if (!wanted) return;
  // A reply outside a running turn (a !command answer) has no "done" coming.
  if (!chat.working.has(key)) {
    speakReply(text, key);
    textOnce.delete(key);
    return;
  }
  pendingSpeech.set(key, [...(pendingSpeech.get(key) || []), text]);
  if (!voice.textOnly && !textOnce.has(key)) prefetchSpeech(text);
});

// ---- wake word ----
//
// With conversation mode off, the mic waits for "아글링크": it cuts what it
// hears into short pieces, and a piece that starts with the name — in
// anyone's voice, enrolled or not — starts conversation mode on the last
// conversation pane. Anything said after the name in the same breath ("아글링크,
// 오늘 날씨 어때?") is sent right away. Once conversation mode is on, commands
// are still checked against the enrolled voice (isUserVoice elsewhere) — only
// the wake word itself is open to anyone, so someone nearby cannot be heard
// as a command but can still get the user's attention by name.

// Whisper writes the name many ways ("아그링크~", "어글링크", "아글링", "아글랭크");
// the last syllable is often swallowed.
const WAKE_RE = /([아애에어악앙][글그클굴걸끌구]\s*[링린닝잉랭랑]\s*[크그큰구]?|알\s*링\s*크|에이\s*지\s*링크|a\s*g\s*(link|링크))/i;
const WAKE_TAIL_RE = /^[\s,.!?~]*(야|아|씨|님|가|는|도)?[\s,.!?~]*/; // "아글링크야, …"
const WAKE_SEGMENT_END_MS = 600; // a pause this long ends a piece
const WAKE_MAX_SEGMENT_MS = 4000; // a piece is checked after this much talk anyway
const WAKE_MIN_SPEECH_MS = 300;
const WAKE_PREROLL_CHUNKS = 3;
const WAKE_PAD_S = 0.4;
const LAST_CONV_STORAGE_KEY = "aglink.voice.lastConversation"; // pane the wake word opens

const WAKE_MAX_LEAD = 1; // letters allowed before the name ("어, 아그라")

// Hangul split into its letters (초성/중성/종성), so "아구라" is one letter
// off "아그라" rather than a whole syllable.
function jamo(s) {
  const out = [];
  for (const ch of s) {
    const c = ch.charCodeAt(0) - 0xac00;
    if (c >= 0 && c < 11172) {
      out.push(`L${Math.floor(c / 588)}`, `V${Math.floor((c % 588) / 28)}`);
      if (c % 28) out.push(`T${c % 28}`);
    } else out.push(ch.toLowerCase());
  }
  return out;
}

function editDistance(a, b) {
  let prev = Array.from({ length: b.length + 1 }, (_, j) => j);
  for (let i = 1; i <= a.length; i++) {
    const cur = [i];
    for (let j = 1; j <= b.length; j++) cur[j] = Math.min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (a[i - 1] === b[j - 1] ? 0 : 1));
    prev = cur;
  }
  return prev[b.length];
}

// matchWakeName finds a user-set name opening text, written a little
// differently or not; it returns where the name ends in text, or -1. Short
// names must come out exactly: one letter off "아그라" is already "아그래".
export function matchWakeName(text, name) {
  const letters = []; // [char, index in text] with spaces and marks left out
  for (let i = 0; i < text.length; i++) if (/[\p{L}\p{N}]/u.test(text[i])) letters.push([text[i], i]);
  const want = jamo(name);
  const allowed = Math.floor((want.length - 1) / 6);
  let best = null;
  for (let from = 0; from <= WAKE_MAX_LEAD && from < letters.length; from++) {
    for (const len of [name.length, name.length - 1, name.length + 1]) {
      if (len < 1 || from + len > letters.length) continue;
      const d = editDistance(jamo(letters.slice(from, from + len).map(([c]) => c).join("")), want);
      if (d <= allowed && (!best || d < best.d)) best = { d, end: letters[from + len - 1][1] + 1 };
    }
    if (best) break; // the earliest place it starts
  }
  return best ? best.end : -1;
}

// wakeMatch: where the wake name ends in what was heard, or -1. The name has
// to open the sentence (give or take a "어," before it), so a passing mention
// in someone's talk does not count.
function wakeMatch(text) {
  if (voice.wakeDefault) {
    const m = WAKE_RE.exec(text);
    if (m && m.index <= 4) return m.index + m[0].length;
  }
  for (const name of voice.wakeNames) {
    const end = matchWakeName(text, name);
    if (end >= 0) return end;
  }
  return -1;
}

// wakeNameLabel is how the wake name is said in replies: the user's own first.
export function wakeNameLabel() {
  return voice.wakeNames[0] || "아글링크";
}

let wakeRec = null;
let wakeOn = false; // the listener is wanted (voice.waking mirrors it for the UI)

function rememberConvPane(paneId) {
  try {
    localStorage.setItem(LAST_CONV_STORAGE_KEY, paneId);
  } catch {}
}

function wakePane() {
  let last = null;
  try {
    last = localStorage.getItem(LAST_CONV_STORAGE_KEY);
  } catch {}
  for (const id of [last, chat.focusedPaneId]) if (id && pane(id)?.target) return id;
  return null;
}

async function startWake() {
  let stream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: false },
    });
  } catch (error) {
    vlog(`wake mic failed: ${error?.message || error}`);
    setTimeout(() => {
      if (wakeOn && !wakeRec) void startWake();
    }, CONV_RETRY_MS);
    return;
  }
  if (!wakeOn || wakeRec) {
    stream.getTracks().forEach((t) => t.stop());
    return;
  }
  const ctx = new AudioContext();
  const source = ctx.createMediaStreamSource(stream);
  const node = ctx.createScriptProcessor(4096, 1, 1);
  const startedAt = performance.now();
  const w = { stream, ctx, source, node, chunks: [], first: -1, firstAt: 0, lastAt: 0, loud: 0, noise: 0, noiseN: 0, busy: false, vad: newVad("wake") };
  wakeRec = w;
  node.onaudioprocess = (e) => {
    if (wakeRec !== w) return;
    const input = e.inputBuffer.getChannelData(0);
    w.chunks.push(new Float32Array(input));
    feedVad(w.vad, input, ctx.sampleRate);
    let sum = 0;
    for (let i = 0; i < input.length; i++) sum += input[i] * input[i];
    const rms = Math.sqrt(sum / input.length);
    const now = performance.now();
    if (now - startedAt < 400) {
      w.noise = (w.noise * w.noiseN + rms) / (w.noiseN + 1);
      w.noiseN++;
      return;
    }
    const loud = rms > Math.max(0.012, w.noise * 3);
    const vad = vadSays(w.vad, now);
    noteVadRun(w, loud && vad === false, "wake");
    if (loud && vad !== false) {
      if (w.first < 0) {
        w.first = w.chunks.length - 1;
        w.firstAt = now;
      }
      w.lastAt = now;
      w.loud++;
    } else if (!loud && w.first < 0) {
      // Follow a slowly changing room, but not the talking itself.
      w.noise = w.noise * 0.98 + rms * 0.02;
    }
    if (w.first < 0) {
      if (w.chunks.length > WAKE_PREROLL_CHUNKS) w.chunks.splice(0, w.chunks.length - WAKE_PREROLL_CHUNKS);
      return;
    }
    if (now - w.lastAt > WAKE_SEGMENT_END_MS || now - w.firstAt > WAKE_MAX_SEGMENT_MS) {
      const long = w.lastAt - w.firstAt >= WAKE_MIN_SPEECH_MS && w.loud >= 3;
      const from = Math.max(0, w.first - WAKE_PREROLL_CHUNKS);
      // A bit of silence after the word: Whisper often returns nothing for a
      // lone short word that runs to the very end of the clip.
      const pad = new Float32Array(Math.round(ctx.sampleRate * WAKE_PAD_S));
      const wav = long && !w.busy ? asrWav(downsample(concat([...w.chunks.slice(from), pad]), ctx.sampleRate, TARGET_RATE), TARGET_RATE) : null;
      w.chunks = [];
      w.first = -1;
      w.loud = 0;
      if (wav) void checkWake(w, wav);
    }
  };
  source.connect(node);
  node.connect(ctx.destination);
  vlog("waiting for the wake word");
}

function stopWake() {
  const w = wakeRec;
  if (!w) return;
  wakeRec = null;
  w.node.onaudioprocess = null;
  try {
    w.source.disconnect();
    w.node.disconnect();
  } catch {}
  w.stream.getTracks().forEach((t) => t.stop());
  void w.ctx.close();
}

// checkWake looks for the name in one piece; one piece at a time, so a noisy
// room cannot queue up recognitions.
async function checkWake(w, wav) {
  w.busy = true;
  try {
    const [raw, score] = await Promise.all([VoiceService.Transcribe(toBase64(wav)), speakerScore(wav)]);
    const text = raw.trim();
    if (!text || wakeRec !== w) return;
    const end = wakeMatch(text);
    if (end < 0) {
      // "Always" needs no name: the user's own voice brings the conversation back.
      if (voice.listenMode === "always" && !endedByUser && isUserVoice(score, speakerMinFor(clipSeconds(wav) < SPEAKER_SHORT_S))) {
        vlog(`always: conversation back on the user's voice (${score.toFixed(2)}) ${quote(text)}`);
        await wakeUp(text);
        return;
      }
      vlog(`wake: not the name ${quote(text, 20)}`); // to see how the name gets written
      return;
    }
    // The name alone is not enough: someone else in the room (or a TV) saying
    // it must not open the mic and send what follows.
    if (!isUserVoice(score, speakerMinFor(clipSeconds(wav) < SPEAKER_SHORT_S))) {
      vlog(`wake: other voice dropped (${score.toFixed(2)}) ${quote(text)}`);
      return;
    }
    const rest = text.slice(end).replace(WAKE_TAIL_RE, "").trim();
    vlog(`wake ${quote(text)}`);
    await wakeUp(rest);
  } catch (err) {
    vlog(`wake check failed: ${err?.message || err}`);
  } finally {
    w.busy = false;
  }
}

async function wakeUp(rest) {
  const paneId = wakePane();
  if (!paneId || voice.convPaneId || voice.phase !== "idle") return;
  wakeOn = false;
  voice.waking = false;
  stopWake();
  clearVoiceNote();
  voice.convPaneId = paneId;
  endedByUser = false;
  convActiveAt = Date.now();
  saveConversation(paneId);
  await playChime(); // heard at once, before anything else
  if (rest.length >= 2) {
    await transcribeClip(paneId, null, null, rest); // sends it; the reply reopens the mic
    return;
  }
  void speak("네, 말씀하세요."); // the mic opens as the reading goes, as after any reply
}

// The wake chime: two short rising bell tones, made here so no sound file is
// shipped. It plays through the OS like the readings, so it is heard with the
// window in the tray.
let chimeB64 = null;

function chimeWav() {
  if (chimeB64) return chimeB64;
  const rate = 24000;
  const notes = [
    [880, 0, 0.16],
    [1320, 0.11, 0.42],
  ]; // Hz, start s, length s
  const samples = new Float32Array(Math.ceil(rate * 0.56));
  for (const [f, start, len] of notes) {
    const from = Math.floor(start * rate);
    for (let i = 0; i < len * rate && from + i < samples.length; i++) {
      const t = i / rate;
      const env = Math.min(1, t / 0.005) * Math.exp(-t * 9);
      samples[from + i] += 0.32 * env * (Math.sin(2 * Math.PI * f * t) + 0.25 * Math.sin(4 * Math.PI * f * t));
    }
  }
  chimeB64 = toBase64(encodeWav(samples, rate));
  return chimeB64;
}

async function playChime() {
  try {
    await playWav(chimeWav(), speakGen);
  } catch (err) {
    vlog(`chime failed: ${err?.message || err}`);
  }
}

// The wake listener runs whenever nothing else uses the voice.
$effect.root(() => {
  $effect(() => {
    if (voice.convPaneId) rememberConvPane(voice.convPaneId);
  });
  $effect(() => {
    const want = voice.listenMode !== "manual" && voice.status === "ready" && !voice.convPaneId && !voice.taping && !voice.meeting && voice.phase === "idle" && !voice.enrolling && !voice.speaking;
    if (want && !wakeOn) {
      wakeOn = true;
      void startWake();
    } else if (!want && wakeOn) {
      wakeOn = false;
      stopWake();
    }
    voice.waking = want;
  });
});

// ---- recording ("녹음해 줘") ----
//
// "녹음해 줘" records everything from the mic, with no answer and no time limit,
// until "녹음 끝내" (or the tray menu / the indicator's ✕). The sound goes to a
// WAV file in Documents\aglink 녹음 (tape.go) a couple of seconds at a time,
// and what is said is recognised piece by piece into a text file next to it —
// which is also how "녹음 끝내" is heard. Conversation and the wake word are
// off meanwhile; a conversation that was on comes back afterwards.

const TAPE_START_RE = cmdRE(String.raw`(이거|이것|지금|지금부터|여기|이\s*내용|회의)?\s*(좀\s*)?(녹음|녹화)\s*(을|를|좀)?\s*(좀\s*)?(해|시작해|시작|켜|하자|해\s*봐|해\s*둬|떠|부탁해)`);
// The whole piece heard, so talk that only mentions it ("녹음 끝나고 정리하자") goes into the notes.
const TAPE_STOP_RE = cmdRE(String.raw`(이제\s*)?녹음\s*(을|를|은|는)?\s*(좀\s*)?(이제\s*)?(그만\s*)?(끝|끝내|끝내자|끝낼게|끝내기|그만|그만해|그만하자|그만할게|멈춰|중지|중지해|종료|종료해|정지|정지해|꺼|마쳐|마치자|스톱)`);
const TAPE_FOLDER_RE = cmdRE(String.raw`녹음\s*(한\s*(거|것|파일)|파일|폴더)\s*(을|를|좀)?\s*(열어|보여)`);
const TAPE_STOP_MAX_CHARS = 30; // "녹음 끝내" is said on its own, not inside a long sentence
const TAPE_FLUSH_MS = 2000; // sound reaches the file this often
const TAPE_SEGMENT_END_MS = 700;
const TAPE_MAX_SEGMENT_MS = 12000; // a long stretch of talk is written down in pieces this long
const TAPE_MIN_SPEECH_MS = 300;
const TAPE_PREROLL_CHUNKS = 3;

// ---- meeting mode ("지금부터 듣고 있어") ----
//
// Unlike recording, no audio touches disk — but everyone's talk while it is
// on is written down as it is heard, to a text file under Documents\aglink
// 회의 (meeting.go), named with the date and time it started. The same text
// is also kept in memory (meetingBuf, capped) for "조언해 줘" to send as one
// AI turn; that in-memory copy is thrown away the moment the session ends —
// by "그만 들어", the ✕ on the indicator, or the tray menu close — but the
// file on disk stays, so what was said can be read back afterwards.
// "조언해 줘" sends what has been heard so far as one AI turn and reads the
// answer aloud; listening carries on afterwards unless told to stop. Only
// the enrolled voice (목소리 등록) can start it, ask for advice or end it —
// nobody else in the room can start, extend or stop a session just by what
// they say — and whoever is unenrolled still has everything they say heard
// and included, which is the point of the feature.
// "지금부터"/"이제부터" is required on the bare forms (never just "계속
// 들어"/"항상 들어", which is LISTEN_ALWAYS_RE's always-on wake setting).
const MEETING_START_RE = cmdRE(String.raw`(지금부터|이제부터)\s*(계속\s*)?(듣고\s*있어|들어|듣기\s*시작해?)|회의\s*(모드)?\s*(켜|시작해?)`);
const MEETING_ADVICE_RE = cmdRE(String.raw`(조언|의견)\s*(좀\s*)?(해|구해|부탁해)`);
const MEETING_STOP_RE = cmdRE(String.raw`그만\s*들어|듣기\s*(를|을)?\s*(그만|끝|끝내|끝내자|종료|멈춰|중지|정지|꺼)|회의\s*(모드)?\s*(끝|끝내|종료|꺼)`);
const MEETING_CMD_MAX_CHARS = 20; // "조언해 줘" / "그만 들어" are said on their own
const MEETING_SEGMENT_END_MS = 700;
const MEETING_MAX_SEGMENT_MS = 12000;
const MEETING_MIN_SPEECH_MS = 300;
const MEETING_PREROLL_CHUNKS = 3;
const MEETING_MAX_CHARS = 12000; // the buffer is capped; the oldest talk is dropped first

let tapeRec = null;
let tapeResume = null; // the conversation pane to go back to
let tapeClockTimer = null;
let meetingRec = null;
let meetingResume = null; // the conversation pane to go back to
let meetingClockTimer = null;
let meetingBuf = []; // plain lines heard while meeting mode is on; never written to disk

function tapeLength(sec) {
  const s = Math.round(sec);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return [h && `${h}시간`, m && `${m}분`, !h && (s % 60 || !m) && `${s % 60}초`].filter(Boolean).join(" ");
}

function tapeClock() {
  const s = Math.floor((Date.now() - voice.tapeStartedAt) / 1000);
  const mm = `${Math.floor((s % 3600) / 60)}`.padStart(2, "0");
  const ss = `${s % 60}`.padStart(2, "0");
  return s >= 3600 ? `${Math.floor(s / 3600)}:${mm}:${ss}` : `${mm}:${ss}`;
}

function pcm16Base64(samples) {
  const out = new Uint8Array(samples.length * 2);
  const view = new DataView(out.buffer);
  for (let i = 0; i < samples.length; i++) {
    const v = Math.max(-1, Math.min(1, samples[i]));
    view.setInt16(i * 2, v < 0 ? v * 0x8000 : v * 0x7fff, true);
  }
  return toBase64(out);
}

async function waitSpeechDone() {
  while (voice.speaking || (typeof speechSynthesis !== "undefined" && speechSynthesis.speaking)) await new Promise((r) => setTimeout(r, 150));
}

export async function startTape() {
  if (voice.taping) {
    void speak("이미 녹음하고 있어요. 끝내시려면 녹음 끝내, 라고 하세요.");
    return;
  }
  let path;
  try {
    path = await VoiceService.TapeStart();
  } catch (err) {
    vlog(`tape start failed: ${err?.message || err}`);
    void speak("녹음을 시작하지 못했어요.");
    return;
  }
  tapeResume = voice.convPaneId;
  voice.taping = true; // first, so ending the conversation does not start the wake word
  voice.tapeStartedAt = Date.now();
  voice.tapeClock = "00:00";
  clearInterval(tapeClockTimer);
  tapeClockTimer = setInterval(() => (voice.tapeClock = tapeClock()), 1000);
  if (voice.convPaneId) endConversation();
  vlog(`tape start ${path}`);
  await speak("녹음을 시작할게요. 끝내시려면 녹음 끝내, 라고 말씀하세요.", "full");
  await waitSpeechDone();
  if (!voice.taping) return;
  voice.tapeStartedAt = Date.now(); // the file starts here
  await openTapeMic();
}

async function openTapeMic() {
  let stream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
  } catch (error) {
    vlog(`tape mic failed: ${error?.message || error}`);
    await stopTape(false);
    void speak("마이크를 열 수 없어서 녹음을 끝냈어요.");
    return;
  }
  if (!voice.taping || tapeRec) {
    stream.getTracks().forEach((tr) => tr.stop());
    return;
  }
  const ctx = new AudioContext();
  const source = ctx.createMediaStreamSource(stream);
  const node = ctx.createScriptProcessor(4096, 1, 1);
  const startedAt = performance.now();
  const t = {
    stream,
    ctx,
    source,
    node,
    pending: [], // not yet in the file
    frames: 0, // input frames so far, for the time stamps
    flushing: Promise.resolve(),
    flushedAt: startedAt,
    chunks: [],
    first: -1,
    firstAt: 0,
    lastAt: 0,
    loud: 0,
    segFrames: 0,
    noise: 0,
    noiseN: 0,
    notes: Promise.resolve(), // recognitions, one at a time and in order
  };
  tapeRec = t;
  t.flush = () => {
    if (!t.pending.length) return t.flushing;
    const b64 = pcm16Base64(downsample(concat(t.pending), ctx.sampleRate, TARGET_RATE));
    t.pending = [];
    t.flushing = t.flushing.then(() => VoiceService.TapeAppend(b64)).catch((err) => vlog(`tape write failed: ${err?.message || err}`));
    return t.flushing;
  };
  node.onaudioprocess = (e) => {
    if (tapeRec !== t) return;
    const input = new Float32Array(e.inputBuffer.getChannelData(0));
    t.pending.push(input);
    const chunkStart = t.frames;
    t.frames += input.length;
    const now = performance.now();
    if (now - t.flushedAt >= TAPE_FLUSH_MS) {
      t.flushedAt = now;
      void t.flush();
    }
    // Pieces of talk, cut at pauses as for the wake word, are written down.
    t.chunks.push(input);
    let sum = 0;
    for (let i = 0; i < input.length; i++) sum += input[i] * input[i];
    const rms = Math.sqrt(sum / input.length);
    if (now - startedAt < 400) {
      t.noise = (t.noise * t.noiseN + rms) / (t.noiseN + 1);
      t.noiseN++;
      return;
    }
    if (rms > Math.max(0.012, t.noise * 3)) {
      if (t.first < 0) {
        t.first = t.chunks.length - 1;
        t.firstAt = now;
        t.segFrames = Math.max(0, chunkStart - Math.min(t.first, TAPE_PREROLL_CHUNKS) * input.length);
      }
      t.lastAt = now;
      t.loud++;
    } else if (t.first < 0) t.noise = t.noise * 0.98 + rms * 0.02;
    if (t.first < 0) {
      if (t.chunks.length > TAPE_PREROLL_CHUNKS) t.chunks.splice(0, t.chunks.length - TAPE_PREROLL_CHUNKS);
      return;
    }
    if (now - t.lastAt > TAPE_SEGMENT_END_MS || now - t.firstAt > TAPE_MAX_SEGMENT_MS) {
      const long = t.lastAt - t.firstAt >= TAPE_MIN_SPEECH_MS && t.loud >= 3;
      const from = Math.max(0, t.first - TAPE_PREROLL_CHUNKS);
      const pad = new Float32Array(Math.round(ctx.sampleRate * WAKE_PAD_S));
      const wav = long ? asrWav(downsample(concat([...t.chunks.slice(from), pad]), ctx.sampleRate, TARGET_RATE), TARGET_RATE) : null;
      const at = t.segFrames / ctx.sampleRate;
      t.chunks = [];
      t.first = -1;
      t.loud = 0;
      if (wav) t.notes = t.notes.then(() => noteTapePiece(t, wav, at));
    }
  };
  source.connect(node);
  node.connect(ctx.destination);
  vlog("tape recording");
}

// noteTapePiece writes down one piece, or ends the recording on "녹음 끝내".
async function noteTapePiece(t, wav, at) {
  try {
    const text = (await VoiceService.Transcribe(toBase64(wav))).trim();
    if (!text || tapeRec !== t) return;
    if (text.length <= TAPE_STOP_MAX_CHARS && TAPE_STOP_RE.test(text)) {
      const score = await speakerScore(wav);
      if (isUserVoice(score, speakerMinFor(clipSeconds(wav) < SPEAKER_SHORT_S))) {
        vlog(`tape stop ${quote(text)}`);
        void stopTape(true, false);
        return;
      }
      vlog(`tape stop: other voice (${score.toFixed(2)}) ${quote(text)}`);
    }
    await VoiceService.TapeNote(at, text);
  } catch (err) {
    vlog(`tape note failed: ${err?.message || err}`);
  }
}

// stopTape closes the recording; waitNotes lets the pieces still being
// recognised reach the text file first (not when called from one of them).
export async function stopTape(spoken = true, waitNotes = true) {
  if (!voice.taping) return;
  voice.taping = false;
  clearInterval(tapeClockTimer);
  tapeClockTimer = null;
  const t = tapeRec;
  tapeRec = null;
  if (t) {
    t.node.onaudioprocess = null;
    try {
      t.source.disconnect();
      t.node.disconnect();
    } catch {}
    t.stream.getTracks().forEach((tr) => tr.stop());
    await t.flush();
    void t.ctx.close();
    if (waitNotes) await Promise.race([t.notes, new Promise((r) => setTimeout(r, 4000))]);
  }
  let res = {};
  try {
    res = JSON.parse(await VoiceService.TapeStop());
  } catch (err) {
    vlog(`tape stop failed: ${err?.message || err}`);
  }
  vlog(`tape saved ${res.path || "?"} (${Math.round(res.seconds || 0)} s)`);
  // Back to how it was: the conversation that was on, or listening always.
  const back = tapeResume && pane(tapeResume)?.target ? tapeResume : voice.listenMode === "always" ? wakePane() : null;
  tapeResume = null;
  if (back && !voice.convPaneId) {
    voice.convPaneId = back;
    convActiveAt = Date.now();
    saveConversation(back);
  }
  if (spoken) {
    const len = res.seconds > 0 ? `${tapeLength(res.seconds)} 녹음해서 ` : "";
    void speak(`녹음을 끝냈어요. ${len}문서 폴더의 aglink 녹음에 넣어 두었어요.`, "full");
  }
  if (back) resumeListening();
}

export function openTapeFolder() {
  return VoiceService.OpenTapeFolder().catch((err) => vlog(`tape folder failed: ${err?.message || err}`));
}

Events.On("tray:tape", () => void (voice.taping ? stopTape() : startTape()));

function meetingClockStr() {
  const s = Math.floor((Date.now() - voice.meetingStartedAt) / 1000);
  const mm = `${Math.floor((s % 3600) / 60)}`.padStart(2, "0");
  const ss = `${s % 60}`.padStart(2, "0");
  return s >= 3600 ? `${Math.floor(s / 3600)}:${mm}:${ss}` : `${mm}:${ss}`;
}

export async function startMeeting() {
  if (voice.meeting) {
    void speak("이미 회의 모드로 듣고 있어요. 끝내시려면 그만 들어, 라고 하세요.");
    return;
  }
  if (voice.taping) {
    void speak("녹음하는 중이에요. 녹음 끝내, 라고 하신 뒤에 말씀해 주세요.");
    return;
  }
  let path;
  try {
    path = await VoiceService.MeetingStart();
  } catch (err) {
    vlog(`meeting start failed: ${err?.message || err}`);
    void speak("회의 기록 파일을 만들지 못해서 회의 모드를 시작하지 못했어요.");
    return;
  }
  meetingResume = voice.convPaneId;
  voice.meeting = true; // first, so ending the conversation does not start the wake word
  meetingBuf = [];
  voice.meetingStartedAt = Date.now();
  voice.meetingClock = "00:00";
  clearInterval(meetingClockTimer);
  meetingClockTimer = setInterval(() => (voice.meetingClock = meetingClockStr()), 1000);
  if (voice.convPaneId) endConversation();
  vlog(`meeting mode start ${path}`);
  await speak("회의 모드로 들을게요. 들은 내용은 글로 받아 적어서 저장해 둘게요. 조언해 줘, 라고 하시면 조언해 드리고, 그만 들어, 라고 하시면 끝낼게요.", "full");
  await waitSpeechDone();
  if (!voice.meeting) return;
  voice.meetingStartedAt = Date.now(); // the buffer starts here
  await openMeetingMic();
}

async function openMeetingMic() {
  let stream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
  } catch (error) {
    vlog(`meeting mic failed: ${error?.message || error}`);
    await stopMeeting(false);
    void speak("마이크를 열 수 없어서 회의 모드를 끝냈어요.");
    return;
  }
  if (!voice.meeting || meetingRec) {
    stream.getTracks().forEach((tr) => tr.stop());
    return;
  }
  const ctx = new AudioContext();
  const source = ctx.createMediaStreamSource(stream);
  const node = ctx.createScriptProcessor(4096, 1, 1);
  const startedAt = performance.now();
  const t = {
    stream,
    ctx,
    source,
    node,
    frames: 0,
    chunks: [],
    first: -1,
    firstAt: 0,
    lastAt: 0,
    loud: 0,
    segFrames: 0,
    noise: 0,
    noiseN: 0,
    notes: Promise.resolve(), // recognitions, one at a time and in order
  };
  meetingRec = t;
  node.onaudioprocess = (e) => {
    if (meetingRec !== t) return;
    const input = new Float32Array(e.inputBuffer.getChannelData(0));
    const chunkStart = t.frames;
    t.frames += input.length;
    t.chunks.push(input);
    let sum = 0;
    for (let i = 0; i < input.length; i++) sum += input[i] * input[i];
    const rms = Math.sqrt(sum / input.length);
    const now = performance.now();
    if (now - startedAt < 400) {
      t.noise = (t.noise * t.noiseN + rms) / (t.noiseN + 1);
      t.noiseN++;
      return;
    }
    // Our own voice reading an advice reply back must not be heard again: the
    // same echo gate as a normal conversation's barge-in recorder.
    const ref = refLevel(now);
    if (ref > 0.01) noteEchoRatio(rms / ref);
    const echoFloor = ref > 0 ? Math.max(echoGain ?? 0, ECHO_GAIN_MIN) * ref * ECHO_MARGIN : 0;
    const threshold = Math.max(0.012, t.noise * 3, echoFloor);
    if (rms > threshold) {
      if (t.first < 0) {
        t.first = t.chunks.length - 1;
        t.firstAt = now;
        t.segFrames = Math.max(0, chunkStart - Math.min(t.first, MEETING_PREROLL_CHUNKS) * input.length);
      }
      t.lastAt = now;
      t.loud++;
    } else if (t.first < 0) t.noise = t.noise * 0.98 + rms * 0.02;
    if (t.first < 0) {
      if (t.chunks.length > MEETING_PREROLL_CHUNKS) t.chunks.splice(0, t.chunks.length - MEETING_PREROLL_CHUNKS);
      return;
    }
    if (now - t.lastAt > MEETING_SEGMENT_END_MS || now - t.firstAt > MEETING_MAX_SEGMENT_MS) {
      const long = t.lastAt - t.firstAt >= MEETING_MIN_SPEECH_MS && t.loud >= 3;
      const from = Math.max(0, t.first - MEETING_PREROLL_CHUNKS);
      const pad = new Float32Array(Math.round(ctx.sampleRate * WAKE_PAD_S));
      const wav = long ? asrWav(downsample(concat([...t.chunks.slice(from), pad]), ctx.sampleRate, TARGET_RATE), TARGET_RATE) : null;
      const at = t.segFrames / ctx.sampleRate;
      t.chunks = [];
      t.first = -1;
      t.loud = 0;
      if (wav) t.notes = t.notes.then(() => noteMeetingPiece(t, wav, at));
    }
  };
  source.connect(node);
  node.connect(ctx.destination);
  vlog("meeting mode listening");
}

// noteMeetingPiece hears one piece: the enrolled voice saying "조언해 줘" or
// "그만 들어" is acted on and not kept; anything else — theirs or anyone
// else's — is added to the in-memory buffer (for "조언해 줘") and written
// down in the meeting's text file (at: seconds into the meeting it was said).
async function noteMeetingPiece(t, wav, at) {
  try {
    const text = (await VoiceService.Transcribe(toBase64(wav))).trim();
    if (!text || meetingRec !== t) return;
    if (isEcho(text, replyText)) {
      vlog(`meeting echo dropped ${quote(text)}`);
      return;
    }
    // -1: no voice enrolled, so nobody is told apart and no one is labelled.
    const score = await speakerScore(wav);
    const mine = isUserVoice(score, speakerMinFor(clipSeconds(wav) < SPEAKER_SHORT_S));
    const who = score === -1 ? "" : mine ? "나" : "상대";
    if (text.length <= MEETING_CMD_MAX_CHARS && (MEETING_STOP_RE.test(text) || MEETING_ADVICE_RE.test(text))) {
      if (mine) {
        if (MEETING_STOP_RE.test(text)) {
          vlog(`meeting stop ${quote(text)}`);
          void stopMeeting(true, false);
        } else {
          vlog(`meeting advice asked ${quote(text)}`);
          void requestMeetingAdvice();
        }
        return;
      }
      vlog(`meeting command from other voice (${score.toFixed(2)}) ${quote(text)}`);
    }
    keepMeetingLine(who ? `${who}: ${text}` : text);
    await VoiceService.MeetingNote(at, who, text);
  } catch (err) {
    vlog(`meeting note failed: ${err?.message || err}`);
  }
}

function keepMeetingLine(line) {
  meetingBuf.push(line);
  let total = meetingBuf.reduce((n, s) => n + s.length, 0);
  while (total > MEETING_MAX_CHARS && meetingBuf.length > 1) total -= meetingBuf.shift().length;
}

let meetingAdviceBusy = false;

// requestMeetingAdvice asks the AI about what has been heard so far, apart
// from any chat (AskAI), reads the answer out and writes it into the meeting
// too; the buffer is not cleared, so a later "조언해 줘" in the same meeting
// still has everything said before it. Without claude on this computer it
// goes to a conversation as one turn instead.
export async function requestMeetingAdvice() {
  if (!meetingBuf.length) {
    void speak("아직 들은 말이 없어요.");
    return;
  }
  if (meetingAdviceBusy) {
    void speak("조언을 생각하는 중이에요. 잠시만 기다려 주세요.");
    return;
  }
  const prompt = meetingAdvicePrompt(meetingBuf.join("\n"));
  const pieces = meetingBuf.length;
  meetingAdviceBusy = true;
  void speak("들은 내용을 보고 조언을 생각해 볼게요.");
  try {
    const answer = String(await VoiceService.AskAI(prompt)).trim();
    vlog(`meeting advice (${pieces} pieces) ${quote(answer, 60)}`);
    if (voice.meeting) {
      keepMeetingLine(`aglink 조언: ${answer}`);
      void VoiceService.MeetingNote((Date.now() - voice.meetingStartedAt) / 1000, "aglink 조언", answer).catch(() => {});
    }
    await waitSpeechDone();
    void speak(answer, "full");
    return;
  } catch (err) {
    vlog(`meeting advice AskAI failed: ${err?.message || err}`);
  } finally {
    meetingAdviceBusy = false;
  }
  const target = assistPane(null);
  if (!target) {
    void speak("열린 대화가 없어서 조언을 구하지 못했어요.");
    return;
  }
  const why = await sendTurn(target, prompt, { read: true });
  if (why) {
    void speak(`${why}. 잠시 뒤에 다시 말씀해 주세요.`);
    return;
  }
  vlog(`meeting advice asked in chat (${pieces} pieces)`);
}

// stopMeeting closes the mic and the text file, and throws the in-memory
// advice buffer away (what was written to disk stays); waitNotes lets a
// piece still being recognised land first (not when called from one of them).
export async function stopMeeting(spoken = true, waitNotes = true) {
  if (!voice.meeting) return;
  voice.meeting = false;
  clearInterval(meetingClockTimer);
  meetingClockTimer = null;
  const t = meetingRec;
  meetingRec = null;
  if (t) {
    t.node.onaudioprocess = null;
    try {
      t.source.disconnect();
      t.node.disconnect();
    } catch {}
    t.stream.getTracks().forEach((tr) => tr.stop());
    void t.ctx.close();
    if (waitNotes) await Promise.race([t.notes, new Promise((r) => setTimeout(r, 4000))]);
  }
  let res = {};
  try {
    res = JSON.parse(await VoiceService.MeetingStop());
  } catch (err) {
    vlog(`meeting stop failed: ${err?.message || err}`);
  }
  const heard = meetingBuf.length;
  vlog(`meeting mode end ${res.path || "?"} (${heard} pieces heard)`);
  meetingBuf = []; // the advice buffer only; the text file written along the way stays on disk
  // Back to how it was: the conversation that was on, or listening always.
  const back = meetingResume && pane(meetingResume)?.target ? meetingResume : voice.listenMode === "always" ? wakePane() : null;
  meetingResume = null;
  if (back && !voice.convPaneId) {
    voice.convPaneId = back;
    convActiveAt = Date.now();
    saveConversation(back);
  }
  // Two pieces or more: worth a title and a write-up.
  const writeUp = !!res.path && heard >= MEETING_WRITEUP_MIN_PIECES;
  if (writeUp) void summarizeMeeting(res.path, { auto: true });
  if (spoken) void speak(`회의 모드를 끝냈어요. 들은 내용은 문서 폴더의 aglink 회의에 저장해 두었어요.${writeUp ? " 내용을 정리해서 제목을 붙여 둘게요." : ""}`, "full");
  if (back) resumeListening();
}

Events.On("tray:meeting", () => void (voice.meeting ? stopMeeting() : startMeeting()));

// ---- meeting write-up ----
//
// When a meeting ends, the AI reads what was written down and answers with a
// title for what the meeting was about, a category, a summary, decisions and
// to-dos; that is saved next to it as "회의 … 정리.md" (meeting.go) and the
// meeting list shows the title. It is asked apart from any chat (AskAI); only
// without claude on this computer does it go to a conversation as a turn
// that is not read out, a busy one retried for a while. The 회의 tab's
// "정리하기" does it by hand.

const MEETING_WRITEUP_MIN_PIECES = 2;
const MEETING_WRITEUP_MAX_CHARS = 150000; // ~5 hours of talk; longer keeps its start and end
const MEETING_WRITEUP_RETRY_MS = 30000;
const MEETING_WRITEUP_RETRIES = 20;

function trimMiddleText(text, max) {
  if (text.length <= max) return text;
  const half = Math.floor(max / 2);
  return `${text.slice(0, half)}\n…(중간 생략)…\n${text.slice(-half)}`;
}

// summarizeMeeting asks for the write-up of the meeting file path. auto: it
// was the meeting just ended; then a busy conversation is tried again later
// and nothing is said unless it goes wrong for good.
export async function summarizeMeeting(path, { auto = false, tries = 0 } = {}) {
  if (voice.meetingWriting) {
    if (!auto) flash(voice.meetingWriting === path ? "이 회의를 정리하는 중이에요" : "다른 회의를 정리하는 중이에요");
    if (auto && tries < MEETING_WRITEUP_RETRIES) setTimeout(() => void summarizeMeeting(path, { auto, tries: tries + 1 }), MEETING_WRITEUP_RETRY_MS);
    return;
  }
  let text = "";
  try {
    text = String(await VoiceService.MeetingRead(path));
  } catch (err) {
    vlog(`meeting read failed: ${err?.message || err}`);
    return;
  }
  const body = text.replace(/^회의 [^\n]*\n/, "").trim();
  if (!body) {
    if (!auto) void speak("그 회의에는 받아 적은 말이 없어요.");
    return;
  }
  const name = path.split(/[/\\]/).pop().replace(/\.txt$/i, "");
  const prompt = meetingSummaryPrompt(name, trimMiddleText(body, MEETING_WRITEUP_MAX_CHARS));
  // Apart from any chat first: no waiting for a busy one, nothing put in it.
  voice.meetingWriting = path;
  if (!auto) flash("📝 회의를 정리하고 있어요");
  vlog(`meeting write-up asked ${name} (${body.length} chars)`);
  try {
    const md = String(await VoiceService.AskAI(prompt));
    saveMeetingWriteUp({ path, text: md });
    return;
  } catch (err) {
    vlog(`meeting write-up AskAI failed: ${err?.message || err}`);
    voice.meetingWriting = "";
  }
  const target = assistPane(null);
  const why = target ? await sendTurn(target, prompt, { saveTo: path, kind: "meeting", quiet: true }) : "열린 대화가 없어요";
  if (why) {
    vlog(`meeting write-up not sent (${why}) ${name}`);
    if (auto && tries < MEETING_WRITEUP_RETRIES) {
      setTimeout(() => void summarizeMeeting(path, { auto, tries: tries + 1 }), MEETING_WRITEUP_RETRY_MS);
      return;
    }
    flash(`회의를 정리하지 못했어요 — ${why}`);
    return;
  }
  voice.meetingWriting = path;
  vlog(`meeting write-up asked in chat ${name}`);
}

// saveMeetingWriteUp saves the finished write-up turn next to its meeting.
function saveMeetingWriteUp(save) {
  if (voice.meetingWriting === save.path) voice.meetingWriting = "";
  if (!save.text.trim()) {
    flash("회의 정리 답이 비어 있어요");
    return;
  }
  VoiceService.MeetingSaveSummary(save.path, save.text)
    .then((title) => {
      vlog(`meeting write-up saved ${quote(title || "?")}`);
      voice.meetingSavedAt = Date.now();
      flash(`📝 회의 정리: ${title || "저장했어요"}`);
      if (title && !voice.meeting && !voice.taping) void speak(`회의를 ${title}, 으로 정리해 두었어요.`, "full");
    })
    .catch((err) => vlog(`meeting write-up save failed: ${err?.message || err}`));
}

export function openMeetingFolder() {
  return VoiceService.OpenMeetingFolder().catch((err) => vlog(`meeting folder failed: ${err?.message || err}`));
}

// getVoices() is empty until the voice list loads; touching it starts the load.
if (typeof speechSynthesis !== "undefined") speechSynthesis.getVoices();
void refreshVoiceStatus().then(resumeSavedConversation);
startAlarms(fireAlarm);
startDaily();

// asrWav encodes a clip for whisper, raising a quiet one (auto gain is off, so
// a laptop or Bluetooth mic often arrives far under full scale) to a steady
// level. Whisper misreads quiet speech; the level is logged to tell the two apart.
function asrWav(samples, rate) {
  let peak = 0;
  let sum = 0;
  for (let i = 0; i < samples.length; i++) {
    const a = Math.abs(samples[i]);
    if (a > peak) peak = a;
    sum += samples[i] * samples[i];
  }
  const gain = peak > 0.005 ? Math.min(8, 0.7 / peak) : 1;
  vlog(`clip peak ${peak.toFixed(3)} rms ${Math.sqrt(sum / Math.max(1, samples.length)).toFixed(3)} gain x${gain.toFixed(1)}`);
  if (gain > 1.2) {
    const out = new Float32Array(samples.length);
    for (let i = 0; i < samples.length; i++) out[i] = samples[i] * gain;
    return encodeWav(out, rate);
  }
  return encodeWav(samples, rate);
}
