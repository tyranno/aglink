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
export const N = String.raw`(\d+|열한|열두|열|한|두|세|네|다섯|여섯|일곱|여덟|아홉|[일이삼사오육칠팔구]?십\s*[일이삼사오육칠팔구]?|[일이삼사오육칠팔구])`;
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
// "3시까지 정리해서 알려 줘", nor "내일 9시 환율 알려 줘" / "1시간 뒤에 비트코인
// 가격 알려 줘" (things to look up, not to be reminded of).
const QUESTION_RE =
  /(날씨|기온|미세먼지|대기질|예보|뭐|무엇|어떻|어떤|언제|어디|누구|몇|얼마|인지|는지|은지|건지|일정|스케줄|뉴스|소식|결과|상황|까지|해서|하고\s*나서|가격|환율|주가|증시|코스피|코스닥|시세|정보|순위|랭킹|점수|스코어|경기\s*결과)/;
const REL_RE = new RegExp(
  String.raw`(?:${N}\s*시간\s*(반)?\s*)?(?:${N}\s*분\s*)?(?:${N}\s*초\s*)?(?:뒤|후|있다가|이따가|지나서|지나면)\s*(?:에|에는)?`,
);
// A clock time, with the day it is on when one is said: 오늘/내일/모레, a
// weekday ("금요일", "다음 주 월요일", "이번 주 수요일"), or a date ("11월 5일").
// "3시 10분 전" is ten minutes before three.
const ABS_RE = new RegExp(
  String.raw`(?:(?<day>오늘|내일|낼|모레)\s*|(?:(?<week>다음\s*주|담주|이번\s*주|매\s*주)\s*)?(?<wd>[월화수목금토일])\s*요일\s*(?:에|날)?\s*|(?<mon>\d{1,2})\s*월\s*(?<dom>\d{1,2})\s*일\s*(?:에|날)?\s*)?(?:(?<part>아침|오전|오후|저녁|밤|낮|점심|새벽)\s*)?(?<hour>${N})\s*시\s*(?:(?<half>반)|(?<min>${N})\s*분(?:\s*(?<before>전))?)?\s*(?:정각|쯤|경)?\s*(?:에|에는)?`,
);
// A day this parser does not place (or a repeat it does not keep) left over
// once the time is taken out: such a request goes to the AI rather than being
// set for the wrong day.
const UNPLACED_DAY_RE = /((다음|담|이번|저번|지난|매)\s*주|요일|평일|주말|글피|\d+\s*월|\d+\s*일|다음\s*달|내년)/;
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
    at = absoluteTime(abs.groups, now, daily, wake);
    if (at === null) return null;
    body = body.slice(0, abs.index) + " " + body.slice(abs.index + abs[0].length);
  }
  if (UNPLACED_DAY_RE.test(body)) return null;
  let msg = body
    .replace(/\s+/g, " ")
    .trim()
    .replace(/^(에|에는|쯤|되면|돼면|되면은)\s*/, "")
    .trim();
  if (QUESTION_RE.test(msg)) return null;
  // A trailing particle goes ("회의를" → "회의"), but 이/가 only off a word of
  // three or more letters: "주가", "약이" are words, not "주" + 가.
  msg = msg.replace(/\s*(을|를|은|는|좀|꼭)$/, "").replace(/(\S{2,})(이|가)$/, "$1").trim();
  if (/^(그때|그\s*시간에?)$/.test(msg)) msg = "";
  if (QUESTION_RE.test(msg)) return null;
  return { at, msg, daily, wake };
}

function absoluteTime(g, now, daily, wake) {
  let h = numWord(g.hour);
  const min = g.half ? 30 : g.min ? numWord(g.min) : 0;
  if (!Number.isFinite(h) || !Number.isFinite(min) || h < 0 || h > 24 || min > 59) return null;
  const before = !!g.before; // "3시 10분 전": min is taken off the hour
  // at(dayOffset, hour) → ms. An hour of 24 (or more) rolls into the next day,
  // so "밤 12시" is the midnight that ends that day.
  const at = (dayOffset, hour) => {
    const d = new Date(now);
    d.setDate(d.getDate() + dayOffset);
    d.setHours(hour, before ? 0 : min, 0, 0);
    return d.getTime() - (before ? min * 60000 : 0);
  };
  const part = g.part || "";
  if (/오후|저녁|밤/.test(part) && h < 12) h += 12;
  else if (/밤/.test(part) && h === 12) h = 24;
  else if (/오전|아침|새벽/.test(part) && h === 12) h = 0;
  else if (/낮|점심/.test(part) && h <= 6) h += 12;

  const day = dayOffsetOf(g, now);
  if (day === null) return null;
  if (day !== undefined) {
    if (daily) return null; // "매일 금요일" makes no sense
    // "내일 3시" means the afternoon; "내일 9시" the morning.
    if (!part && h >= 1 && h <= 6) h += 12;
    // "오늘 8시" said at 2 p.m. is tonight.
    if (!part && day === 0 && h <= 12 && at(0, h) <= now.getTime()) h += 12;
    let t = at(day, h);
    // A bare weekday whose time has gone by today is next week's.
    if (g.wd && !g.week && t <= now.getTime()) t = at(day + 7, h);
    return t > now.getTime() ? t : null;
  }
  if (daily) {
    // Every day at the same hour, whatever the time is now. Without
    // 오전/오후: a wake-up at 1–11 is in the morning; otherwise 5–11 is the
    // morning and 1–4 the afternoon ("매일 3시" is 15:00, "매일 7시" 07:00).
    if (!part && !wake && h >= 1 && h <= 4) h += 12;
    const t = at(0, h);
    return t > now.getTime() ? t : at(1, h);
  }
  // No day said: the next time it comes round. Without 오전/오후, "3시" is the
  // nearer of 3 a.m. and 3 p.m., and "12시" of noon and midnight.
  const hours = !part && h >= 1 && h <= 12 ? [h, h + 12] : [h];
  let best = Infinity;
  for (const dOff of [0, 1]) for (const hh of hours) {
    const t = at(dOff, hh);
    if (t > now.getTime() && t < best) best = t;
  }
  return Number.isFinite(best) ? best : null;
}

const WEEKDAY_INDEX = { 일: 0, 월: 1, 화: 2, 수: 3, 목: 4, 금: 5, 토: 6 };

// dayOffsetOf: how many days from today the said day is; undefined when no
// day was said, null when it cannot be placed (a past date this week, "매주"
// which this list does not repeat, a date that does not exist).
function dayOffsetOf(g, now) {
  if (g.day) return { 오늘: 0, 내일: 1, 낼: 1, 모레: 2 }[g.day];
  if (g.wd) {
    const week = (g.week || "").replace(/\s+/g, "");
    if (week === "매주") return null;
    const wd = WEEKDAY_INDEX[g.wd];
    const today = now.getDay();
    if (!week) return (wd - today + 7) % 7;
    // A week runs Monday to Sunday: "다음 주 일요일" is the Sunday after next
    // Monday, "이번 주 월요일" said on Wednesday has gone.
    const mon = (d) => (d + 6) % 7;
    const off = mon(wd) - mon(today) + (week === "이번주" ? 0 : 7);
    return off < 0 ? null : off;
  }
  if (g.mon) {
    const mo = Number(g.mon) - 1;
    const dd = Number(g.dom);
    const today = startOfDay(now);
    for (const y of [now.getFullYear(), now.getFullYear() + 1]) {
      const d = new Date(y, mo, dd);
      if (d.getMonth() !== mo || d.getDate() !== dd) return null;
      // Today counts (its time is checked by the caller); an earlier date is next year's.
      if (d.getTime() >= today) return Math.round((d.getTime() - today) / 86400000);
    }
  }
  return undefined;
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
