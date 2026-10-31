// node scripts/verify-schedule.mjs — schedule wording and cron round trips.
import { repeatToCron, cronToRepeat, describeCron, describeWhen } from "../src/schedule.js";

let bad = 0;
const eq = (got, want, what) => {
  const ok = JSON.stringify(got) === JSON.stringify(want);
  if (!ok) bad++;
  console.log(ok ? "ok " : "BAD", what, "→", got, ok ? "" : `(want ${want})`);
};
eq(describeCron("0 9 * * *"), "매일 오전 9시", "daily");
eq(describeCron("30 15 * * 1-5"), "평일 오후 3시 30분", "weekdays");
eq(describeCron("0 8 * * 1,3,5"), "매주 월·수·금요일 오전 8시", "weekly");
eq(describeCron("*/30 * * * *"), "30분마다", "every 30m");
eq(describeCron("0 * * * *"), "매시간", "hourly");
eq(describeCron("0 */2 * * *"), "2시간마다", "every 2h");
eq(describeCron("0 0 1 * *"), "반복 (0 0 1 * *)", "monthly stays cron");
eq(describeCron("0 0 * * 0,7"), "매주 일요일 밤 12시", "sunday 0/7");
for (const r of [
  { kind: "daily", time: "07:05" },
  { kind: "weekdays", time: "09:00" },
  { kind: "weekly", time: "18:30", days: [2, 4] },
  { kind: "every", everyN: 15, everyUnit: "min" },
  { kind: "every", everyN: 3, everyUnit: "hour" },
]) {
  const back = cronToRepeat(repeatToCron(r));
  eq(repeatToCron(back), repeatToCron(r), `round trip ${r.kind}`);
}
const now = new Date(2026, 9, 31, 21, 0);
eq(describeWhen(new Date(2026, 10, 1, 9, 30), now), "내일 오전 9시 30분", "when tomorrow");
eq(describeWhen(new Date(2026, 10, 4, 15, 0), now), "11월 4일 (수) 오후 3시", "when later");
console.log(bad ? `${bad} failed` : "all ok");
process.exit(bad ? 1 : 0);
