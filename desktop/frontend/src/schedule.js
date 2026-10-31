// Schedules in plain words, for the 예약 panel and the voice side: a repeat is
// picked as "매일 / 평일 / 매주 요일 / N분마다" and turned into the 5-field cron
// the host scheduler runs, and any cron (also ones the AI or !task made) is
// described back in words where it can be. No Svelte or Wails imports, so it can
// be tried with plain node (scripts/verify-schedule.mjs).

export const WEEKDAYS = ["일", "월", "화", "수", "목", "금", "토"];

const pad = (n) => String(n).padStart(2, "0");

// clockLabel: 9:00 → "오전 9시", 15:30 → "오후 3시 30분", 0:00 → "밤 12시".
export function clockLabel(h, m) {
  const part = h === 0 ? "밤" : h < 12 ? "오전" : "오후";
  const h12 = h % 12 === 0 ? 12 : h % 12;
  return `${part} ${h12}시${m ? ` ${m}분` : ""}`;
}

// A repeat as the editor holds it:
//   { kind: "daily" | "weekdays" | "weekly" | "every" | "cron",
//     time: "09:00", days: [1, 3], everyN: 30, everyUnit: "min" | "hour", cron }
export function repeatToCron(r) {
  const [h, m] = String(r.time || "09:00").split(":").map(Number);
  switch (r.kind) {
    case "daily":
      return `${m} ${h} * * *`;
    case "weekdays":
      return `${m} ${h} * * 1-5`;
    case "weekly": {
      const days = [...new Set(r.days || [])].sort((a, b) => a - b);
      return days.length ? `${m} ${h} * * ${days.join(",")}` : "";
    }
    case "every": {
      // */N only repeats evenly when N divides the field's range: "*/90" in the
      // minute field is just "at :00 every hour", "*/7" leaves a 4-minute gap
      // at the top of the hour. Anything else becomes the host scheduler's
      // (robfig/cron) "@every 90m", which counts from when it was saved.
      if (repeatError(r)) return "";
      let n = Number(r.everyN);
      let hour = r.everyUnit === "hour";
      if (!hour && n % 60 === 0) [n, hour] = [n / 60, true];
      if (hour) {
        if (n === 1) return "0 * * * *";
        return n < 24 && 24 % n === 0 ? `0 */${n} * * *` : `@every ${n}h`;
      }
      if (n === 1) return "* * * * *";
      return 60 % n === 0 ? `*/${n} * * * *` : `@every ${n}m`;
    }
    default:
      return String(r.cron || "").trim();
  }
}

// repeatError says what is wrong with a repeat the editor holds, "" when it can
// be saved.
export function repeatError(r) {
  if (r?.kind === "weekly" && !(r.days || []).length) return "요일을 하나 이상 골라 주세요.";
  if (r?.kind === "every") {
    const n = Number(r.everyN);
    if (!Number.isInteger(n) || n < 1) return "간격은 1 이상의 정수로 정해 주세요.";
    if (n > (r.everyUnit === "hour" ? 24 * 31 : 60 * 24 * 31)) return "간격이 너무 길어요. 한 달 이내로 정해 주세요.";
  }
  if (r?.kind === "cron" && !String(r.cron || "").trim()) return "cron 표현식을 써 주세요.";
  return "";
}

// cronToRepeat reads a cron back into the editor's terms; anything it does not
// recognise stays a hand-written cron.
export function cronToRepeat(cron) {
  const c = String(cron || "").trim();
  const base = { kind: "cron", time: "09:00", days: [], everyN: 30, everyUnit: "min", cron: c };
  // "@every 90m" / "@every 5h" / "@every 1h30m0s" (Go durations, as the host
  // and repeatToCron write them).
  const ev = /^@every\s+(?:(\d+)h)?(?:(\d+)m)?(?:0+s)?$/.exec(c);
  if (ev && (ev[1] || ev[2])) {
    const mins = Number(ev[1] || 0) * 60 + Number(ev[2] || 0);
    if (mins > 0) return mins % 60 === 0 ? { ...base, kind: "every", everyN: mins / 60, everyUnit: "hour" } : { ...base, kind: "every", everyN: mins, everyUnit: "min" };
  }
  const f = c.split(/\s+/);
  if (f.length !== 5) return base;
  const [mi, ho, dom, mon, dow] = f;
  if (dom !== "*" || mon !== "*") return base;
  let mm = /^\*\/(\d+)$/.exec(mi);
  if (mm && ho === "*" && dow === "*") return { ...base, kind: "every", everyN: Number(mm[1]), everyUnit: "min" };
  if (mi === "*" && ho === "*" && dow === "*") return { ...base, kind: "every", everyN: 1, everyUnit: "min" };
  if (mi === "0" && dow === "*") {
    if (ho === "*") return { ...base, kind: "every", everyN: 1, everyUnit: "hour" };
    mm = /^\*\/(\d+)$/.exec(ho);
    if (mm) return { ...base, kind: "every", everyN: Number(mm[1]), everyUnit: "hour" };
  }
  if (!/^\d+$/.test(mi) || !/^\d+$/.test(ho)) return base;
  const time = `${pad(Number(ho))}:${pad(Number(mi))}`;
  if (dow === "*") return { ...base, kind: "daily", time };
  if (dow === "1-5") return { ...base, kind: "weekdays", time };
  if (/^[0-7](,[0-7])*$/.test(dow)) {
    const days = [...new Set(dow.split(",").map((d) => Number(d) % 7))].sort((a, b) => a - b);
    return { ...base, kind: "weekly", time, days };
  }
  return base;
}

// describeCron: "0 9 * * *" → "매일 오전 9시", "*/30 * * * *" → "30분마다",
// "@every 90m" → "1시간 30분마다".
export function describeCron(cron) {
  const r = cronToRepeat(cron);
  const [h, m] = r.time.split(":").map(Number);
  switch (r.kind) {
    case "daily":
      return `매일 ${clockLabel(h, m)}`;
    case "weekdays":
      return `평일 ${clockLabel(h, m)}`;
    case "weekly":
      return `매주 ${r.days.map((d) => WEEKDAYS[d]).join("·")}요일 ${clockLabel(h, m)}`;
    case "every":
      if (r.everyUnit === "hour") return r.everyN === 1 ? "매시간" : `${r.everyN}시간마다`;
      if (r.everyN === 1) return "매분";
      return r.everyN > 60 ? `${Math.floor(r.everyN / 60)}시간 ${r.everyN % 60}분마다` : `${r.everyN}분마다`;
    default:
      return r.cron ? `반복 (${r.cron})` : "반복";
  }
}

// describeWhen: a one-shot time in words relative to now — "오늘 오후 3시",
// "내일 오전 9시 30분", "11월 4일 (화) 오전 9시".
export function describeWhen(at, now = new Date()) {
  const d = new Date(at);
  if (Number.isNaN(d.getTime())) return "";
  const day = (x) => {
    const y = new Date(x);
    y.setHours(0, 0, 0, 0);
    return y.getTime();
  };
  const diff = Math.round((day(d) - day(now)) / 86400000);
  const dayWord =
    diff === 0 ? "오늘" : diff === 1 ? "내일" : diff === 2 ? "모레" : diff === -1 ? "어제" : `${d.getMonth() + 1}월 ${d.getDate()}일 (${WEEKDAYS[d.getDay()]})`;
  return `${dayWord} ${clockLabel(d.getHours(), d.getMinutes())}`;
}

// untilLabel: how long until at — "곧", "25분 후", "3시간 10분 후", "2일 후".
export function untilLabel(at, now = Date.now()) {
  const ms = new Date(at).getTime() - now;
  if (!Number.isFinite(ms)) return "";
  if (ms <= 30000) return "곧";
  const mins = Math.round(ms / 60000);
  if (mins < 60) return `${mins}분 후`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return mins % 60 ? `${hours}시간 ${mins % 60}분 후` : `${hours}시간 후`;
  return `${Math.floor(hours / 24)}일 후`;
}
