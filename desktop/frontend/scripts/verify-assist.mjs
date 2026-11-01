// node scripts/verify-assist.mjs — tries the PC / usage / briefing / memo /
// recording-summary command parsing (assist.js).
import { parsePc, parseUsage, parseDaily, parseMemo, parseTapeSummary, dailyDue, dayKey } from "../src/assist.js";
import { parseAlarm } from "../src/alarms.js";

const j = (v) => JSON.stringify(v);
const cases = [
  // [parser, text, expected (JSON-compared; null = not a command)]
  [parsePc, "메모장 열어 줘", { kind: "open", name: "메모장", target: "notepad.exe" }],
  [parsePc, "계산기 켜 줘", { kind: "open", name: "계산기", target: "calc.exe" }],
  [parsePc, "크롬 좀 열어 줘", { kind: "open", name: "크롬", target: "chrome" }],
  [parsePc, "탐색기 열어 줘.", { kind: "open", name: "탐색기", target: "explorer.exe" }],
  [parsePc, "spotify 켜 줘", { kind: "open", name: "spotify", target: "spotify" }],
  [parsePc, "대화 열어 줘", null],
  [parsePc, "3번 대화 열어 줘", null],
  [parsePc, "끼어들기 켜 줘", null],
  [parsePc, "소리 줄여 줘", { kind: "volume", op: "down", value: 10 }],
  [parsePc, "소리 좀 많이 키워 줘", { kind: "volume", op: "up", value: 20 }],
  [parsePc, "소리 꺼 줘", { kind: "volume", op: "mute", value: 0 }],
  [parsePc, "소리 켜 줘", { kind: "volume", op: "unmute", value: 0 }],
  [parsePc, "볼륨 50으로", { kind: "volume", op: "set", value: 50 }],
  [parsePc, "볼륨 30으로 해 줘", { kind: "volume", op: "set", value: 30 }],
  [parsePc, "화면 꺼 줘", { kind: "screenOff" }],
  [parsePc, "90분 뒤에 컴퓨터 꺼 줘", { kind: "shutdown", seconds: 5400 }],
  [parsePc, "한 시간 반 뒤에 컴퓨터 꺼 줘", { kind: "shutdown", seconds: 5400 }],
  [parsePc, "2시간 후에 피시 종료해 줘", { kind: "shutdown", seconds: 7200 }],
  [parsePc, "컴퓨터 꺼 줘", { kind: "shutdownAsk" }],
  [parsePc, "컴퓨터 끄기 취소", { kind: "shutdownCancel" }],
  [parsePc, "컴퓨터 끄는 법 알려 줘", null],
  [parsePc, "소리 줄이는 방법 알려 줘", null],
  [parseUsage, "오늘 얼마 썼어?", "today"],
  [parseUsage, "이번 주 비용 알려 줘", "week"],
  [parseUsage, "이번 달 사용량", "month"],
  [parseUsage, "어제 얼마 썼어", "yesterday"],
  [parseUsage, "오늘 비용 얼마야?", "today"],
  [parseUsage, "오늘 날씨 어때?", null],
  [parseUsage, "오늘", null],
  [parseDaily, "아침 브리핑 7시 반으로 해 줘", { which: "briefing", op: "set", h: 7, m: 30 }],
  [parseDaily, "브리핑 오전 8시로 바꿔 줘", { which: "briefing", op: "set", h: 8, m: 0 }],
  [parseDaily, "아침 브리핑 꺼 줘", { which: "briefing", op: "off" }],
  [parseDaily, "지금 브리핑해 줘", { which: "briefing", op: "now" }],
  [parseDaily, "아침 브리핑 지역 수원으로", { which: "briefing", op: "region", region: "수원" }],
  [parseDaily, "저녁 정리 9시로 해 줘", { which: "evening", op: "set", h: 21, m: 0 }],
  [parseDaily, "저녁 정리 꺼 줘", { which: "evening", op: "off" }],
  [parseDaily, "저녁 정리해 줘", { which: "evening", op: "now" }],
  [parseDaily, "브리핑 자료 만들어 줘", null],
  [parseMemo, "메모해 줘, 내일 견적서 확인", { op: "add", text: "내일 견적서 확인", todo: false }],
  [parseMemo, "메모: 우유 사기", { op: "add", text: "우유 사기", todo: false }],
  [parseMemo, "내일 견적서 확인이라고 메모해 줘", { op: "add", text: "내일 견적서 확인", todo: false }],
  [parseMemo, "할 일 추가: 보고서 쓰기", { op: "add", text: "보고서 쓰기", todo: true }],
  [parseMemo, "보고서 쓰기 할 일에 추가해 줘", { op: "add", text: "보고서 쓰기", todo: true }],
  [parseMemo, "메모해 줘", { op: "ask" }],
  [parseMemo, "메모 뭐 있어?", { op: "list" }],
  [parseMemo, "할 일 뭐 남았어?", { op: "list" }],
  [parseMemo, "메모 다 지워 줘", { op: "clear" }],
  [parseMemo, "2번 메모 지워 줘", { op: "delete", n: 2 }],
  [parseMemo, "두 번째 메모 지워 줘", { op: "delete", n: 2 }],
  [parseMemo, "방금 거 메모해 줘", null],
  [parseMemo, "노션에 메모해 줘", null],
  [parseMemo, "회의 내용 정리해서 메모해 줘", null],
  [parseMemo, "메모 기능 어떻게 써?", null],
  [parseTapeSummary, "방금 녹음 정리해 줘", { notion: false }],
  [parseTapeSummary, "녹음 요약해 줘", { notion: false }],
  [parseTapeSummary, "녹음 요약해서 노션에도 저장해 줘", { notion: true }],
  [parseTapeSummary, "방금 녹음 정리해 줘, 노션에도 저장해 줘", { notion: true }],
  [parseTapeSummary, "녹음 파일 열어 줘", null],
  // None of these may be taken for a reminder first.
  [(t) => parseAlarm(t), "30분 뒤에 컴퓨터 꺼 줘", null],
  [(t) => parseAlarm(t), "아침 브리핑 7시 반으로 해 줘", null],
  [(t) => parseAlarm(t), "저녁 정리 9시로 해 줘", null],
];

let bad = 0;
for (const [fn, text, want] of cases) {
  const got = fn(text);
  const ok = j(got ?? null) === j(want);
  if (!ok) bad++;
  console.log(ok ? "ok " : "BAD", text, "→", j(got ?? null), ok ? "" : `(want ${j(want)})`);
}

// Daily items: due once a day, from their time until two hours after.
const at = (h, m) => new Date(2026, 10, 1, h, m);
const s = { on: true, h: 7, m: 30, last: "" };
for (const [now, item, want] of [
  [at(7, 29), s, false],
  [at(7, 30), s, true],
  [at(9, 29), s, true],
  [at(9, 31), s, false],
  [at(8, 0), { ...s, last: dayKey(at(8, 0)) }, false],
  [at(8, 0), { ...s, on: false }, false],
]) {
  const got = dailyDue(item, now);
  if (got !== want) bad++;
  console.log(got === want ? "ok " : "BAD", "dailyDue", now.toTimeString().slice(0, 5), j(item), "→", got);
}
console.log(bad ? `${bad} failed` : "all ok");
process.exit(bad ? 1 : 0);
