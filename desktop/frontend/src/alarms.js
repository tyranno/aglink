// Spoken reminders: "30분 뒤에 회의 알려 줘", "내일 아침 9시에 약 먹으라고
// 해 줘", "매일 7시에 깨워 줘". They are parsed and kept here, on this
// computer, and the voice side reads each one out when its time comes — no AI
// round trip, so setting one is instant and works offline.
//
// This file has no Svelte or Wails imports, so the parsing can be tried with
// plain node (scripts/verify-alarms.mjs).

const STORAGE_KEY = "aglink.voice.alarms";
const CHECK_MS = 5000;
export const LATE_MS = 2 * 60000; // fired later than this (the app was off): said so

// A number as Whisper writes it (digits, mostly) or as it is said: native
// ("세 시", "열두 시") for hours, Sino ("삼십 분") for minutes.
const N = String.raw`(\d+|열한|열두|열|한|두|세|네|다섯|여섯|일곱|여덟|아홉|[일이삼사오육칠팔구]?십\s*[일이삼사오육칠팔구]?|[일이삼사오육칠팔구])`;
const NATIVE = { 한: 1, 두: 2, 세: 3, 네: 4, 다섯: 5, 여섯: 6, 일곱: 7, 여덟: 8, 아홉: 9, 열: 10, 열한: 11, 열두: 12 };
const SINO = { 일: 1, 이: 2, 삼: 3, 사: 4, 오: 5, 육: 6, 칠: 7, 팔: 8, 구: 9 };

export function numWord(s) {
  if (!s) return NaN;
  s = s.replace(/\s+/g, "");
  if (/^\d+$/.test(s)) return Number(s);
  if (s in NATIVE) return NATIVE[s];
  const m = /^([일이삼사오육칠팔구]?)(십?)([일이삼사오육칠팔구]?)$/.exec(s);
  if (!m || (!m[1] && !m[2] && !m[3])) return NaN;
  if (!m[2]) return SINO[m[3] || m[1]];
  return (m[1] ? SINO[m[1]] : 1) * 10 + (m[3] ? SINO[m[3]] : 0);
}

// The sentence has to end by asking to be told: "알려 줘", "깨워 줘",
// "…라고 해 줘", "알림 맞춰 줘". A bare "해 줘" / "말해 줘" only counts after
// "…라고", so "3시에 회의 준비해 줘" still goes to the AI.
const ASK_RE =
  /\s*(?:(?:좀|꼭)\s*)?(?:(?:알려|깨워)\s*(?:줘|주세요|줄래|줘요)?|(?:알림|알람)\s*(?:을|를)?\s*(?:맞춰|설정해|해|맞춰\s*놔|걸어|줘)\s*(?:줘|주세요|줄래)?|(?<=(?:라고|다고)\s*)(?:말해|얘기해|해)\s*(?:줘|주세요|줄래)?)[.!?~\s]*$/;
// "내일 3시 날씨 알려 줘" asks for something, it is not a reminder; neither is
// "3시까지 정리해서 알려 줘".
const QUESTION_RE = /(날씨|기온|뭐|무엇|어떻|어떤|언제|어디|누구|몇|얼마|인지|는지|은지|건지|일정|스케줄|뉴스|소식|결과|상황|까지|해서|하고\s*나서)/;
const REL_RE = new RegExp(
  String.raw`(?:${N}\s*시간\s*(반)?\s*)?(?:${N}\s*분\s*)?(?:${N}\s*초\s*)?(?:뒤|후|있다가|이따가|지나서|지나면)\s*(?:에|에는)?`,
);
const ABS_RE = new RegExp(
  String.raw`(?:(오늘|내일|낼|모레)\s*)?(?:(아침|오전|오후|저녁|밤|낮|점심|새벽)\s*)?${N}\s*시\s*(?:(반)|${N}\s*분)?\s*(?:정각|쯤|경)?\s*(?:에|에는)?`,
);
const DAILY_RE = /(매일|날마다|매일\s*아침|매일\s*저녁)\s*/;

// parseAlarm reads a reminder request, or returns null when text is not one.
// at is in ms; msg is what to remind of ("회의", "약 먹으라고"), "" for none.
export function parseAlarm(text, now = new Date()) {
  const t = String(text || "").trim();
  const ask = ASK_RE.exec(t);
  if (!ask) return null;
  let body = t.slice(0, ask.index);
  const wake = /깨워/.test(ask[0]);
  const daily = DAILY_RE.test(body);
  body = body.replace(DAILY_RE, " ");
  let at = null;
  const rel = !daily && REL_RE.exec(body);
  if (rel && (rel[1] || rel[2] || rel[3] || rel[4])) {
    const h = rel[1] ? numWord(rel[1]) : 0;
    const half = rel[2] ? 30 : 0;
    const m = rel[3] ? numWord(rel[3]) : 0;
    const s = rel[4] ? numWord(rel[4]) : 0;
    if ([h, m, s].some((x) => !Number.isFinite(x))) return null;
    const ms = ((h * 60 + half + m) * 60 + s) * 1000;
    if (ms <= 0) return null;
    at = now.getTime() + ms;
    body = body.slice(0, rel.index) + " " + body.slice(rel.index + rel[0].length);
  } else {
    const abs = ABS_RE.exec(body);
    if (!abs) return null;
    at = absoluteTime(abs, now, daily);
    if (at === null) return null;
    body = body.slice(0, abs.index) + " " + body.slice(abs.index + abs[0].length);
  }
  let msg = body
    .replace(/\s+/g, " ")
    .trim()
    .replace(/^(에|에는|쯤|되면|돼면|되면은)\s*/, "")
    .replace(/\s*(을|를|이|가|은|는|좀|꼭)$/, "")
    .trim();
  if (/^(그때|그\s*시간에?)$/.test(msg)) msg = "";
  if (QUESTION_RE.test(msg)) return null;
  return { at, msg, daily, wake };
}

function absoluteTime(m, now, daily) {
  const [, day, part, hourWord, half, minWord] = m;
  let h = numWord(hourWord);
  const min = half ? 30 : minWord ? numWord(minWord) : 0;
  if (!Number.isFinite(h) || !Number.isFinite(min) || h < 0 || h > 24 || min > 59) return null;
  const at = (dayOffset, hour) => {
    const d = new Date(now);
    d.setDate(d.getDate() + dayOffset);
    d.setHours(hour, min, 0, 0);
    return d.getTime();
  };
  if (part) {
    if (/오후|저녁|밤/.test(part) && h < 12) h += 12;
    else if (/밤/.test(part) && h === 12) h = 24;
    else if (/오전|아침|새벽/.test(part) && h === 12) h = 0;
    else if (/낮|점심/.test(part) && h <= 6) h += 12;
  }
  if (h === 24) h = 0; // "밤 12시" is the coming midnight
  const offset = { 내일: 1, 낼: 1, 모레: 2 }[day] ?? 0;
  if (offset || day === "오늘") {
    // "내일 3시" means the afternoon; "내일 9시" the morning.
    if (!part && h >= 1 && h <= 6) h += 12;
    const t = at(offset, h);
    return t > now.getTime() ? t : null;
  }
  // No day said: the next time it comes round. Without 오전/오후, "3시" is the
  // nearer of 3 a.m. and 3 p.m.
  const hours = !part && h >= 1 && h < 12 ? [h, h + 12] : [h];
  let best = Infinity;
  for (const dOff of [0, 1]) for (const hh of hours) {
    const t = at(dOff, hh);
    if (t > now.getTime() && t < best) best = t;
  }
  if (h === 0 && /밤/.test(part || "")) best = Math.min(best, at(1, 0));
  return Number.isFinite(best) ? best : null;
}

// timeLabel says when an alarm goes off: "오후 3시 30분", "내일 오전 9시".
export function timeLabel(at, now = new Date()) {
  const d = new Date(at);
  // Midnight belongs to the night before: "밤 12시", not "내일 밤 12시".
  const dayOf = d.getHours() === 0 && d.getMinutes() === 0 ? new Date(at - 1) : d;
  const days = Math.round((startOfDay(dayOf) - startOfDay(now)) / 86400000);
  const dayWord = days === 0 ? "" : days === 1 ? "내일 " : days === 2 ? "모레 " : `${d.getMonth() + 1}월 ${d.getDate()}일 `;
  const h = d.getHours();
  const part = h === 0 ? "밤" : h < 12 ? "오전" : "오후";
  const h12 = h % 12 === 0 ? 12 : h % 12;
  const m = d.getMinutes();
  return `${dayWord}${part} ${h12}시${m ? ` ${m}분` : ""}`;
}

function startOfDay(d) {
  const x = new Date(d);
  x.setHours(0, 0, 0, 0);
  return x.getTime();
}

// fireText is what is read out when the alarm goes off.
export function fireText(a, late = false) {
  const lead = late ? `${timeLabel(a.at)}에 맡기신 알림이에요. ` : "";
  if (!a.msg) return lead + (a.wake ? "일어나실 시간이에요." : "말씀하신 시간이 됐어요.");
  if (/(라고|다고|하래|하라고)$/.test(a.msg)) return `${lead}${a.msg} 하셨던 시간이에요.`;
  return `${lead}${a.msg} 시간이에요.`;
}

// confirmText is said when an alarm is set.
export function confirmText(a) {
  const when = a.daily ? `매일 ${timeLabel(a.at).replace(/^(내일|모레) /, "")}` : timeLabel(a.at);
  const what = a.msg ? `${a.msg} ` : "";
  return `${when}에 ${what}${a.wake && !a.msg ? "깨워" : "알려"} 드릴게요.`;
}

// ---- the list ----

export const alarms = [];
let nextId = 1;

function save() {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(alarms));
  } catch {}
}

function load() {
  try {
    const list = JSON.parse(localStorage.getItem(STORAGE_KEY) || "[]");
    if (Array.isArray(list)) for (const a of list) if (Number.isFinite(a?.at)) alarms.push(a);
  } catch {}
  nextId = alarms.reduce((n, a) => Math.max(n, (a.id || 0) + 1), 1);
}

export function addAlarm(a) {
  const item = { id: nextId++, at: a.at, msg: a.msg || "", daily: !!a.daily, wake: !!a.wake, made: Date.now() };
  alarms.push(item);
  sortAlarms();
  save();
  return item;
}

function sortAlarms() {
  alarms.sort((x, y) => x.at - y.at);
}

export function removeAlarm(id) {
  const i = alarms.findIndex((a) => a.id === id);
  if (i < 0) return null;
  const [a] = alarms.splice(i, 1);
  save();
  return a;
}

export function clearAlarms() {
  const n = alarms.length;
  alarms.splice(0);
  save();
  return n;
}

// startAlarms checks the list every few seconds and hands each due alarm to
// onFire(alarm, late). A daily one is moved to the next day.
export function startAlarms(onFire) {
  load();
  const tick = () => {
    const now = Date.now();
    const due = alarms.filter((a) => a.at <= now);
    if (!due.length) return;
    for (const a of due) {
      const late = now - a.at > LATE_MS;
      if (a.daily) {
        const fired = { ...a };
        while (a.at <= now) a.at += 86400000;
        onFire(fired, late);
      } else {
        alarms.splice(alarms.indexOf(a), 1);
        onFire(a, late);
      }
    }
    sortAlarms();
    save();
  };
  tick();
  return setInterval(tick, CHECK_MS);
}
