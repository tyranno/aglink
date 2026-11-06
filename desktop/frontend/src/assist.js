// Everyday helpers said as whole sentences and done here, with no AI turn
// (voice.svelte.js carries them out): PC control ("메모장 열어 줘", "소리 줄여
// 줘", "90분 뒤에 컴퓨터 꺼 줘"), the AI's cost ("오늘 얼마 썼어?"), memos and
// to-dos ("메모해 줘, 내일 견적서 확인"), and the daily morning briefing and
// evening round-up. Only a sentence that is the command as a whole is taken —
// a question about any of it still goes to the AI.
//
// This file has no Svelte or Wails imports, so the parsing can be tried with
// plain node (scripts/verify-assist.mjs).

import { N, numWord } from "./alarms.js";

const TAIL = String.raw`\s*(?:좀\s*)?(?:줘|주세요|줄래|줘요|봐)?[.!?~\s]*$`;
const re = (body) => new RegExp(String.raw`^\s*(?:${body})${TAIL}`, "i");

// ---- PC control ----

// PROGRAMS: what is said → what the shell is given (Win+R style, so App
// Paths names like "chrome" and "winword" work). Keys have no spaces.
export const PROGRAMS = {
  메모장: "notepad.exe",
  노트패드: "notepad.exe",
  계산기: "calc.exe",
  크롬: "chrome",
  구글크롬: "chrome",
  엣지: "msedge",
  탐색기: "explorer.exe",
  파일탐색기: "explorer.exe",
  그림판: "mspaint.exe",
  작업관리자: "taskmgr.exe",
  제어판: "control.exe",
  윈도우설정: "ms-settings:",
  캡처도구: "snippingtool.exe",
  워드: "winword",
  엑셀: "excel",
  파워포인트: "powerpnt",
};
const OPEN_RE = re(String.raw`(.{1,20}?)\s*(?:프로그램|앱)?\s*(?:을|를)?\s*(?:좀\s*)?(?:열어|켜|띄워|실행시켜)`);
// A name not in PROGRAMS is only tried when it is a plain Latin program name.
const LATIN_NAME_RE = /^[A-Za-z][A-Za-z0-9 ._-]{0,30}$/;
const VOL = String.raw`(?:소리|볼륨|음량|스피커\s*소리)\s*(?:을|를|좀)?\s*(?:좀\s*)?`;
const VOL_AMOUNT = String.raw`(?:(조금|살짝|약간|많이|확|더)\s*)?`;
const VOL_DOWN_RE = re(String.raw`${VOL}${VOL_AMOUNT}(?:줄여|낮춰|작게\s*해)`);
const VOL_UP_RE = re(String.raw`${VOL}${VOL_AMOUNT}(?:키워|높여|올려|크게\s*해)`);
const VOL_MUTE_RE = re(String.raw`${VOL}(?:꺼|끄기|죽여)|음소거\s*(?:해)?`);
const VOL_UNMUTE_RE = re(String.raw`${VOL}(?:다시\s*)?켜|음소거\s*(?:풀어|해제해|해제)`);
const VOL_SET_RE = re(String.raw`${VOL}${N}\s*(?:퍼센트|프로|%)?\s*(?:으로|로)\s*(?:해|맞춰|바꿔|줄여|키워|올려|낮춰)?`);
const SCREEN_OFF_RE = re(String.raw`(?:화면|모니터)\s*(?:을|를|좀)?\s*(?:좀\s*)?(?:꺼|끄기|꺼\s*둬|꺼\s*놔)`);
const PC = String.raw`(?:컴퓨터|컴|피시|PC)\s*(?:를|을|좀)?\s*(?:좀\s*)?`;
const SHUTDOWN_RE = re(String.raw`(?:${N}\s*시간\s*(반)?\s*)?(?:${N}\s*분\s*)?(?:뒤|후|있다가|지나서|지나면)\s*(?:에)?\s*${PC}(?:꺼|끄기|종료해|종료|꺼\s*둬)`);
const SHUTDOWN_BARE_RE = re(String.raw`(?:지금\s*)?${PC}(?:꺼|끄기|종료해|종료)`);
const SHUTDOWN_CANCEL_RE = re(String.raw`(?:${PC})?(?:끄기|꺼지는\s*거|종료|끄는\s*거)\s*(?:예약)?\s*(?:을|를)?\s*(?:취소|취소해|멈춰|하지\s*마)`);

// parsePc reads a PC command: {kind: "open", name, target}, {kind: "volume",
// op, value}, {kind: "screenOff"}, {kind: "shutdown", seconds},
// {kind: "shutdownAsk"} (no time said), {kind: "shutdownCancel"}; null for
// anything else.
export function parsePc(text) {
  const t = String(text || "").trim();
  if (SHUTDOWN_CANCEL_RE.test(t)) return { kind: "shutdownCancel" };
  const sd = SHUTDOWN_RE.exec(t);
  if (sd) {
    const h = sd[1] ? numWord(sd[1]) : 0;
    const m = sd[3] ? numWord(sd[3]) : 0;
    const minutes = h * 60 + (sd[2] ? 30 : 0) + m;
    if (!Number.isFinite(minutes) || minutes <= 0) return { kind: "shutdownAsk" };
    return { kind: "shutdown", seconds: minutes * 60 };
  }
  if (SHUTDOWN_BARE_RE.test(t)) return { kind: "shutdownAsk" };
  if (SCREEN_OFF_RE.test(t)) return { kind: "screenOff" };
  const set = VOL_SET_RE.exec(t);
  if (set) {
    const v = numWord(set[1]);
    if (Number.isFinite(v) && v >= 0 && v <= 100) return { kind: "volume", op: "set", value: v };
  }
  const down = VOL_DOWN_RE.exec(t);
  const up = !down && VOL_UP_RE.exec(t);
  if (down || up) {
    const how = (down || up)[1] || "";
    const value = /많이|확/.test(how) ? 20 : /조금|살짝|약간/.test(how) ? 5 : 10;
    return { kind: "volume", op: down ? "down" : "up", value };
  }
  if (VOL_MUTE_RE.test(t)) return { kind: "volume", op: "mute", value: 0 };
  if (VOL_UNMUTE_RE.test(t)) return { kind: "volume", op: "unmute", value: 0 };
  const open = OPEN_RE.exec(t);
  if (open) {
    const said = open[1].trim();
    const key = said.replace(/\s+/g, "");
    if (PROGRAMS[key]) return { kind: "open", name: said, target: PROGRAMS[key] };
    if (LATIN_NAME_RE.test(said)) return { kind: "open", name: said, target: said };
  }
  return null;
}

// ---- usage ----

const USAGE_RE = new RegExp(
  String.raw`^\s*(오늘|어제|이번\s*주|금주|이번\s*달|이달)\s*(?:은|에|에는|까지)?\s*(?:(?:AI|에이아이|클로드)\s*)?(?:(비용|사용량|요금|쓴\s*돈|사용\s*금액|돈)\s*(?:은|이|을|를)?\s*)?(?:(얼마나?)\s*)?(?:(?:썼|나왔|들었|됐|쓴\s*거)\s*(?:어|니|지|나|어요|는지|야)?\s*)?(?:(?:야|예요|이야|이에요|인지|인가)\s*)?(?:(?:알려|말해|읽어)\s*)?(?:줘|주세요|줄래)?[.!?~\s]*$`,
  "i",
);
const USAGE_PERIODS = { 오늘: "today", 어제: "yesterday", 이번주: "week", 금주: "week", 이번달: "month", 이달: "month" };
export const USAGE_LABELS = { today: "오늘은", yesterday: "어제는", week: "이번 주는", month: "이번 달은" };

// parseUsage reads "오늘 얼마 썼어?" as "today" / "yesterday" / "week" /
// "month"; null for anything else.
export function parseUsage(text) {
  const m = USAGE_RE.exec(String(text || "").trim());
  if (!m || !(m[2] || m[3])) return null;
  return USAGE_PERIODS[m[1].replace(/\s+/g, "")] || null;
}

// usageText says a usage sum ({turns, costUsd, top}) out loud.
export function usageText(period, s) {
  const when = USAGE_LABELS[period] || "";
  if (!s?.turns) return `${when} 아직 AI를 쓰지 않았어요.`;
  const top = s.top ? ` 가장 많이 쓴 건 '${s.top}' 대화예요.` : "";
  return `${when} ${s.turns}번 대화에 ${Number(s.costUsd || 0).toFixed(2)}달러 썼어요.${top}`;
}

// ---- daily: morning briefing, evening round-up ----

const CLOCK = String.raw`(?:(오전|아침|오후|저녁|밤|새벽)\s*)?${N}\s*시\s*(?:(반)|${N}\s*분)?`;
const SET_TAIL = String.raw`\s*(?:에|으로|로)?\s*(?:(?:해|맞춰|설정해|바꿔|켜|정해|해\s*둬)\s*)?`;
const BRIEF = String.raw`(?:아침\s*)?브리핑\s*(?:을|를|은|시간\s*(?:을|를|은)?)?\s*(?:매일\s*)?`;
const BRIEF_SET_RE = re(String.raw`${BRIEF}${CLOCK}${SET_TAIL}`);
const BRIEF_OFF_RE = re(String.raw`${BRIEF}(?:그만|꺼|끄기|해제|취소|중지|없애)\S{0,3}`);
const BRIEF_ON_RE = re(String.raw`${BRIEF}(?:다시\s*)?(?:켜|시작해|매일\s*해)`);
const BRIEF_NOW_RE = re(String.raw`(?:지금\s*)?(?:아침\s*)?브리핑\s*(?:을|를|좀)?\s*(?:좀\s*)?(?:해|들려|해\s*봐|읽어|부탁해)`);
const BRIEF_REGION_RE = re(String.raw`${BRIEF}(?:의\s*)?(?:지역|날씨\s*지역|동네|위치)\s*(?:을|를|은)?\s*(\S{1,10}?)\s*(?:으로|로)\s*(?:(?:해|바꿔|설정해|정해|맞춰)\s*)?`);
const EVE = String.raw`저녁\s*(?:정리|요약)\s*(?:을|를|은|시간\s*(?:을|를|은)?)?\s*(?:매일\s*)?`;
const EVE_SET_RE = re(String.raw`${EVE}${CLOCK}${SET_TAIL}`);
const EVE_OFF_RE = re(String.raw`${EVE}(?:그만|꺼|끄기|해제|취소|중지|없애)\S{0,3}`);
const EVE_ON_RE = re(String.raw`${EVE}(?:다시\s*)?(?:켜|시작해|매일\s*해)`);
const EVE_NOW_RE = re(String.raw`(?:지금\s*)?(?:오늘\s*)?저녁\s*(?:정리|요약)\s*(?:을|를|좀)?\s*(?:좀\s*)?(?:해|들려|해\s*봐|읽어)`);

// clockOf turns CLOCK's groups into {h, m}; evening times without 오전/오후
// are taken as p.m. ("저녁 정리 9시" is 21:00).
function clockOf([, part = "", hour, half, min], evening) {
  let h = numWord(hour);
  const m = half ? 30 : min ? numWord(min) : 0;
  if (!Number.isFinite(h) || !Number.isFinite(m) || h > 24 || m > 59) return null;
  if (/오후|저녁|밤/.test(part) && h < 12) h += 12;
  else if (/밤/.test(part) && h === 12) h = 0;
  else if (/오전|아침|새벽/.test(part) && h === 12) h = 0;
  else if (!part && evening && h >= 1 && h < 12) h += 12;
  return { h: h % 24, m };
}

// parseDaily reads a briefing or evening command: {which: "briefing" |
// "evening", op: "set" (h, m) | "on" | "off" | "now" | "region" (region)};
// null for anything else.
export function parseDaily(text) {
  const t = String(text || "").trim();
  let m;
  if ((m = BRIEF_REGION_RE.exec(t))) return { which: "briefing", op: "region", region: m[1] };
  if ((m = BRIEF_SET_RE.exec(t))) {
    const c = clockOf(m, false);
    return c && { which: "briefing", op: "set", ...c };
  }
  if ((m = EVE_SET_RE.exec(t))) {
    const c = clockOf(m, true);
    return c && { which: "evening", op: "set", ...c };
  }
  if (BRIEF_OFF_RE.test(t)) return { which: "briefing", op: "off" };
  if (EVE_OFF_RE.test(t)) return { which: "evening", op: "off" };
  if (BRIEF_ON_RE.test(t)) return { which: "briefing", op: "on" };
  if (EVE_ON_RE.test(t)) return { which: "evening", op: "on" };
  if (BRIEF_NOW_RE.test(t)) return { which: "briefing", op: "now" };
  if (EVE_NOW_RE.test(t)) return { which: "evening", op: "now" };
  return null;
}

export const DAILY_CATCHUP_MS = 2 * 3600000; // missed (the app was off) by less than this: still done

// dayKey is the local date as "2026-11-01".
export function dayKey(d = new Date()) {
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

// dailyDue tells whether a daily item ({on, h, m, last}) is to be done now:
// its time today has come, by less than DAILY_CATCHUP_MS, and it was not done
// today yet.
export function dailyDue(s, now = new Date()) {
  if (!s?.on || s.last === dayKey(now)) return false;
  const at = new Date(now);
  at.setHours(s.h, s.m, 0, 0);
  const late = now.getTime() - at.getTime();
  return late >= 0 && late < DAILY_CATCHUP_MS;
}

// clockLabel: 7, 30 → "오전 7시 30분".
export function clockLabel(h, m) {
  return `${h < 12 ? "오전" : "오후"} ${h % 12 || 12}시${m ? ` ${m}분` : ""}`;
}

// ---- memos and to-dos ----

const MEMO_VERB = String.raw`메모\s*(?:좀\s*)?(?:해|해\s*둬|해\s*놔|남겨)`;
const MEMO_LEAD_RE = new RegExp(String.raw`^\s*(?:${MEMO_VERB}\s*(?:줘|주세요|줄래)?|메모)\s*[,:：]\s*(.+?)[.!~\s]*$|^\s*${MEMO_VERB}\s*(?:줘|주세요)\s+(.+?)[.!~\s]*$`);
const MEMO_TAIL_RE = new RegExp(String.raw`^\s*(.+?)\s*(?:(?:이?라고|을|를)\s*)?${MEMO_VERB}\s*(?:줘|주세요|줄래)?[.!~\s]*$`);
const MEMO_BARE_RE = re(MEMO_VERB);
const TODO_LEAD_RE = /^\s*할\s*일\s*(?:추가|등록)\s*(?:해\s*(?:줘|주세요)?)?\s*[,:：]?\s*(.+?)[.!~\s]*$/;
const TODO_TAIL_RE = /^\s*(.+?)\s*(?:(?:을|를|이?라고)\s*)?할\s*일\s*(?:에|로|으로|목록에)?\s*(?:추가|등록|넣어)\s*(?:해)?\s*(?:줘|주세요|줄래)?[.!~\s]*$/;
const MEMO_LIST_RE = re(String.raw`(?:(?:오늘|남은)\s*)?(?:메모|할\s*일)\s*(?:목록|들)?\s*(?:을|를|은|이|좀)?\s*(?:(?:뭐|뭐가|몇\s*개)\s*)?(?:있어|있지|있나|있어요|남았어|남았지|읽어|알려|말해|불러|보여)|(?:메모|할\s*일)\s*목록`);
const MEMO_CLEAR_RE = re(String.raw`(?:메모|할\s*일)\s*(?:을|를)?\s*(?:다|전부|모두|싹)\s*(?:지워|삭제해|없애|비워)`);
const MEMO_DELETE_RE = re(String.raw`(?:(\d+|첫|두|세|네|다섯|여섯|일곱|여덟|아홉|열|[일이삼사오육칠팔구])\s*번\s*(?:째)?\s*(?:메모|할\s*일)|(?:메모|할\s*일)\s*(\d+|첫|두|세|네|다섯|[일이삼사오육칠팔구])\s*번\s*(?:째)?)\s*(?:을|를)?\s*(?:지워|삭제해|없애|빼|지우기)`);
// What is not a memo itself but points at something else ("방금 거 메모해
// 줘"), asks for another place (노션, 캘린더) or for work first ("정리해서
// 메모해 줘") goes to the AI.
const MEMO_NOT_RE = /^(이거|이것|그거|그것|저거|방금\s*(거|것|답변|내용|말)|이\s*내용|그\s*내용|위\s*내용|답변|대화)(을|를)?$|노션|notion|캘린더|구글|일정\s*에|(해서|하고|해\s*가지고)$/i;
const NTH = { 첫: 1, 두: 2, 세: 3, 네: 4, 다섯: 5, 여섯: 6, 일곱: 7, 여덟: 8, 아홉: 9, 열: 10 };
export const MAX_MEMO_CMD_CHARS = 100;

// parseMemo reads {op: "add", text, todo} | {op: "ask"} (no text) | {op:
// "list"} | {op: "clear"} | {op: "delete", n}; null for anything else.
export function parseMemo(text) {
  const t = String(text || "").trim();
  if (MEMO_CLEAR_RE.test(t)) return { op: "clear" };
  const del = MEMO_DELETE_RE.exec(t);
  if (del) {
    const w = del[1] || del[2];
    const n = NTH[w] ?? numWord(w);
    return Number.isFinite(n) && n > 0 ? { op: "delete", n } : null;
  }
  if (MEMO_LIST_RE.test(t)) return { op: "list" };
  if (MEMO_BARE_RE.test(t)) return { op: "ask" };
  const add = (body, todo) => {
    const s = body.trim().replace(/^[,:：]\s*/, "");
    return s && !MEMO_NOT_RE.test(s) ? { op: "add", text: s, todo } : null;
  };
  let m;
  if ((m = TODO_LEAD_RE.exec(t))) return add(m[1], true);
  if ((m = TODO_TAIL_RE.exec(t))) return add(m[1], true);
  if ((m = MEMO_LEAD_RE.exec(t))) return add(m[1] ?? m[2], false);
  if ((m = MEMO_TAIL_RE.exec(t))) return add(m[1], false);
  return null;
}

// ---- recording summary ----

const TAPE_SUM_RE = new RegExp(
  String.raw`^\s*(?:(?:방금|마지막|아까|최근)\s*)?(?:한\s*)?녹음\s*(?:한\s*(?:거|것|내용)|내용|파일)?\s*(?:을|를|좀)?\s*(?:좀\s*)?(?:정리|요약)\s*(?:해|해서|하고)?\s*(?:줘|주세요|줄래)?\s*[,.]?\s*(노션\s*(?:에도|에)\s*(?:저장|올려|정리)\s*(?:해)?\s*(?:줘|주세요)?)?[.!?~\s]*$`,
  "i",
);

// parseTapeSummary reads "방금 녹음 정리해 줘" as {notion}; null otherwise.
export function parseTapeSummary(text) {
  const m = TAPE_SUM_RE.exec(String(text || "").trim());
  return m ? { notion: !!m[1] } : null;
}

// tapeSummaryPrompt is the one AI turn that sums up a recording's text.
export function tapeSummaryPrompt(name, text, notion) {
  const save = notion
    ? `정리한 내용을 노션에도 "${name} 요약" 페이지로 저장해 주세요. 마지막 답에는 정리한 내용 전체를 써 주세요.`
    : "도구는 쓰지 말고 정리한 내용만 답해 주세요.";
  return `[녹음 정리] 아래는 녹음(${name})을 받아 적은 글이에요. 한국어 마크다운으로 정리해 주세요: ## 3줄 요약 / ## 결정 사항 / ## 할 일 (담당·기한이 나오면 함께). 없는 항목은 "없음". ${save}\n\n---\n${text}`;
}

// meetingAdvicePrompt is the one AI turn "조언해 줘" sends with what the room
// has said so far (also kept, as it is heard, in a text file on disk).
export function meetingAdvicePrompt(text) {
  return `[회의 조언] 아래는 지금 자리에서 받아 적은, 아직 끝나지 않은 대화예요. 지금까지 나온 이야기를 보고 한국어로 조언해 주세요 — 결론 먼저, 필요하면 이유를 한두 마디만, 도구는 쓰지 말고 말로 읽을 답만 주세요.\n\n---\n${text}`;
}

// MEETING_CATEGORIES are the kinds a meeting write-up picks one from, so the
// meeting list can be sorted by them.
export const MEETING_CATEGORIES = ["업무 회의", "기술 검토", "일정·계획", "고객·영업", "인사·면담", "기타"];

// meetingSummaryPrompt is the one AI turn that writes up a finished meeting:
// a title from what it was about, a category, a summary, decisions and to-dos.
// meeting.go reads the title and category back from the first lines.
export function meetingSummaryPrompt(name, text) {
  return `[회의 정리] 아래는 회의(${name})에서 받아 적은 글이에요 (음성 인식이라 잘못 들은 말이 섞여 있을 수 있어요). 도구는 쓰지 말고 아래 형식의 한국어 마크다운만 답해 주세요.
첫 줄: "# 제목" — 무슨 내용의 회의였는지 알 수 있는 20자 안팎의 제목
둘째 줄: "분류: " 뒤에 ${MEETING_CATEGORIES.join(" / ")} 중 하나
그다음: ## 3줄 요약 / ## 주요 내용 (안건별로 짧게) / ## 결정 사항 / ## 할 일 (담당·기한이 나오면 함께). 없는 항목은 "없음".

---
${text}`;
}

// briefingPrompt is the morning briefing's one AI turn, kept short.
export function briefingPrompt(now, region, schedule) {
  const date = now.toLocaleDateString("ko-KR", { year: "numeric", month: "long", day: "numeric", weekday: "long" });
  return `[아침 브리핑] 오늘은 ${date}이에요. 6문장 이내로 짧게 알려 주세요: 날짜·요일, 오늘 일정(${schedule || "없음"}), ${region} 오늘 날씨(기온·비), 한국 주요 뉴스 헤드라인 3개. 웹 검색만 쓰고 다른 도구는 쓰지 마세요.`;
}
