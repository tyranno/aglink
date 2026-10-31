<script>
  // 예약 (Reservation/Task) 사이드바 패널. Full CRUD over the scheduler's
  // registered reminders/recurring jobs (tasks.json) — the same entities !task
  // manages from Telegram, editable here for the desktop: add, edit, delete,
  // pause/resume.
  //
  // Made for everyday use rather than cron: a one-line quick add that reads
  // "30분 뒤에 회의 알려 줘" (alarms.js, the same parser as voice), repeats
  // picked as 매일 / 평일 / 매주 요일 / N분마다 (schedule.js turns them into
  // cron), schedules shown in words, and a "결과 받을 대화" so a reminder or
  // job made here reports to a desktop conversation instead of Telegram.
  import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";
  import { chat, pane, findWebConv } from "./paneStore.svelte.js";
  import { parseAlarm, fireText } from "./alarms.js";
  import { WEEKDAYS, repeatToCron, repeatError, cronToRepeat, describeCron, describeWhen, untilLabel } from "./schedule.js";

  const REFRESH_MS = 20000; // fired one-shots leave the list; keep it current

  let tasks = $state([]);
  let loadError = $state("");
  let filter = $state("pending"); // pending | paused | cancelled | all
  const filters = [
    { value: "pending", label: "대기중" },
    { value: "paused", label: "일시정지" },
    { value: "cancelled", label: "취소됨" },
    { value: "all", label: "전체" },
  ];

  let ask = $state(null); // { title, message, danger, confirmLabel, resolve }
  let quick = $state(""); // quick add text
  let quickNote = $state(""); // what the quick add did, or why it could not
  let quickBusy = $state(false);

  // Editor modal state. editing === null → closed.
  let editing = $state(null);
  let editorError = $state("");
  let saving = $state(false); // save in flight — guards against a double-submit

  const repeatKinds = [
    { value: "daily", label: "매일" },
    { value: "weekdays", label: "평일" },
    { value: "weekly", label: "매주" },
    { value: "every", label: "일정 간격" },
    { value: "cron", label: "직접 입력" },
  ];

  async function load() {
    try {
      const raw = await ControlService.ListTasks(filter);
      const data = JSON.parse(raw || "[]");
      tasks = Array.isArray(data) ? data : [];
      loadError = "";
    } catch (e) {
      loadError = "예약 목록을 불러오지 못했습니다.";
    }
  }
  $effect(() => {
    void filter; // re-run when the filter tab changes
    load();
    const timer = setInterval(load, REFRESH_MS);
    return () => clearInterval(timer);
  });

  // Soonest first; repeats and one-shots mixed by when they next run.
  const sorted = $derived(
    [...tasks].sort((a, b) => (nextAt(a) || Infinity) - (nextAt(b) || Infinity)),
  );

  function askConfirm(title, message, danger, confirmLabel) {
    return new Promise((resolve) => { ask = { title, message, danger: !!danger, confirmLabel: confirmLabel || "확인", resolve }; });
  }
  function resolveAsk(val) { const a = ask; ask = null; if (a) a.resolve(val); }

  async function mutate(promise) {
    try {
      const raw = await promise;
      const j = JSON.parse(raw || "{}");
      if (j && j.ok === false) { loadError = j.error || "요청 실패"; return false; }
    } catch (e) {
      loadError = "연결이 끊겨 처리하지 못했습니다. 잠시 후 다시 시도해 주세요.";
      return false;
    }
    await load();
    return true;
  }
  const pause = (t) => mutate(ControlService.PauseTask(t.id));
  const resume = (t) => mutate(ControlService.ResumeTask(t.id));
  async function del(t) {
    const ok = await askConfirm("예약 삭제", `"${title(t)}" 예약을 삭제할까요?`, true, "삭제하기");
    if (ok) await mutate(ControlService.DeleteTask(t.id));
  }

  function isZero(iso) {
    return !iso || iso.startsWith("0001-01-01");
  }
  function nextAt(t) {
    const iso = !isZero(t.nextFire) ? t.nextFire : t.cronExpr ? "" : t.fireAt;
    return isZero(iso) ? 0 : new Date(iso).getTime();
  }
  function title(t) {
    return t.label || t.prompt || "(내용 없음)";
  }
  function scheduleText(t) {
    return t.cronExpr ? describeCron(t.cronExpr) : describeWhen(t.fireAt);
  }
  // Countdown reacts to chat.nowTick (ticks every second in App.svelte) so it
  // stays live without this panel running its own timer.
  function nextText(t) {
    const at = nextAt(t);
    if (!at) return "";
    return t.cronExpr ? `다음 ${describeWhen(at)} · ${untilLabel(at, chat.nowTick)}` : untilLabel(at, chat.nowTick);
  }
  function statusBadge(status) {
    if (status === "paused") return "bg-amber-100 dark:bg-amber-900/40 text-amber-800 dark:text-amber-300";
    return "bg-slate-200 dark:bg-slate-700 text-slate-600 dark:text-slate-400";
  }
  function statusLabel(status) {
    if (status === "paused") return "일시정지";
    if (status === "cancelled") return "취소됨";
    return status;
  }

  // --- where a task reports ---
  // "web:<id>" for a desktop conversation, "telegram" for the telegram stream.
  function destOf(t) {
    return t.target?.kind === "web" && t.target.id ? `web:${t.target.id}` : "telegram";
  }
  function destLabel(dest) {
    if (dest === "telegram") return "텔레그램";
    const conv = findWebConv(dest.slice(4));
    return conv ? conv.title || "제목 없는 대화" : "지워진 대화";
  }
  function destTarget(dest) {
    return dest.startsWith("web:") ? { kind: "web", id: dest.slice(4) } : { kind: "telegram" };
  }
  // A new task reports to the conversation in front of the user — or, when
  // that pane is not a desktop conversation, the first one in the list.
  function defaultDest() {
    const t = pane(chat.focusedPaneId)?.target;
    if (t?.kind === "web" && t.id) return `web:${t.id}`;
    if (chat.webConvs[0]) return `web:${chat.webConvs[0].id}`;
    return "telegram";
  }
  // Where a quick add will report, in words, for the hint under the input.
  const quickDestText = $derived.by(() => {
    const t = pane(chat.focusedPaneId)?.target;
    if (t?.kind === "web" && t.id) return "결과는 지금 보고 있는 대화로 와요";
    const dest = defaultDest();
    return dest === "telegram" ? "결과는 텔레그램으로 와요" : `결과는 "${destLabel(dest)}" 대화로 와요`;
  });
  const destChoices = $derived([
    ...chat.webConvs.map((c) => ({ value: `web:${c.id}`, label: c.title || "제목 없는 대화" })),
    { value: "telegram", label: "텔레그램" },
  ]);

  // --- quick add ---
  async function quickAdd() {
    const text = quick.trim();
    if (!text || quickBusy) return;
    const a = parseAlarm(text);
    if (!a) {
      quickNote = "언제 알릴지 알아듣지 못했어요. \"30분 뒤에 회의 알려 줘\", \"매일 7시에 깨워 줘\"처럼 써 보시거나 ＋로 직접 만들어 주세요.";
      return;
    }
    const d = new Date(a.at);
    const payload = {
      label: a.msg || (a.wake ? "깨우기" : "알림"),
      isTask: false,
      prompt: fireText(a),
      cronExpr: a.daily ? `${d.getMinutes()} ${d.getHours()} * * *` : "",
      fireAt: a.daily ? "" : d.toISOString(),
      target: destTarget(defaultDest()),
    };
    quickBusy = true;
    const ok = await mutate(ControlService.SaveTask(JSON.stringify(payload)));
    quickBusy = false;
    if (ok) {
      quick = "";
      quickNote = `${a.daily ? describeCron(payload.cronExpr) : describeWhen(a.at)}에 알려 드릴게요.`;
    }
  }

  // --- datetime-local helpers ---
  // datetime-local has no timezone; treat the value as local time (matches how
  // the user picks it) and let `new Date(...)` interpret it in the local zone.
  const pad = (n) => String(n).padStart(2, "0");
  function toLocalInput(d) {
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }
  function isoToLocalInput(iso) {
    return isZero(iso) ? "" : toLocalInput(new Date(iso));
  }
  function localInputToISO(v) {
    if (!v) return "";
    const d = new Date(v);
    return Number.isNaN(d.getTime()) ? "" : d.toISOString();
  }
  function inMinutes(n) {
    return toLocalInput(new Date(Date.now() + n * 60000));
  }
  function atClock(dayOffset, h, m = 0) {
    const d = new Date();
    d.setDate(d.getDate() + dayOffset);
    d.setHours(h, m, 0, 0);
    return toLocalInput(d);
  }
  const quickTimes = [
    { label: "10분 뒤", value: () => inMinutes(10) },
    { label: "30분 뒤", value: () => inMinutes(30) },
    { label: "1시간 뒤", value: () => inMinutes(60) },
    { label: "오늘 저녁 6시", value: () => atClock(new Date().getHours() >= 18 ? 1 : 0, 18) },
    { label: "내일 아침 9시", value: () => atClock(1, 9) },
  ];

  // --- editor ---
  function openEditor(t) {
    const repeat = cronToRepeat(t?.cronExpr || "0 9 * * *");
    editing = {
      id: t?.id || "",
      label: t?.label || "",
      isTask: !!t?.isTask,
      prompt: t?.prompt || "",
      script: t?.script || "",
      scheduleType: t?.cronExpr ? "repeat" : "once",
      repeat: { ...repeat, days: repeat.days.length ? repeat.days : [new Date().getDay()] },
      fireAtLocal: isoToLocalInput(t?.fireAt) || inMinutes(60),
      dest: t ? destOf(t) : defaultDest(),
      playbookId: t?.playbookId || "", // a scheduled 업무 루틴: kept through an edit
      advanced: !!t?.script,
    };
    editorError = "";
    saving = false;
  }

  const editorCron = $derived(editing && editing.scheduleType === "repeat" ? repeatToCron(editing.repeat) : "");
  // Why the repeat cannot be saved as it is ("" when it can): shown under the
  // picker as the user types, and again on 저장.
  const editorRepeatError = $derived(editing && editing.scheduleType === "repeat" ? repeatError(editing.repeat) : "");
  const editorPreview = $derived.by(() => {
    if (!editing) return "";
    if (editing.scheduleType === "repeat") return editorCron ? describeCron(editorCron) : "";
    const iso = localInputToISO(editing.fireAtLocal);
    return iso ? `${describeWhen(iso)} (${untilLabel(iso, chat.nowTick)})` : "";
  });

  function toggleDay(d) {
    const days = editing.repeat.days;
    editing.repeat.days = days.includes(d) ? days.filter((x) => x !== d) : [...days, d];
  }

  async function saveEditor() {
    const prompt = editing.prompt.trim();
    if (!prompt || saving) return;
    const payload = {
      id: editing.id,
      label: editing.label.trim() || prompt.split("\n")[0].slice(0, 40),
      isTask: editing.isTask,
      prompt,
      script: editing.script.trim(),
      cronExpr: editing.scheduleType === "repeat" ? editorCron : "",
      fireAt: editing.scheduleType === "once" ? localInputToISO(editing.fireAtLocal) : "",
      target: destTarget(editing.dest),
      playbookId: editing.playbookId,
    };
    if (editing.scheduleType === "repeat" && !payload.cronExpr) { editorError = editorRepeatError || "반복 주기를 정해 주세요."; return; }
    if (editing.scheduleType === "once" && !payload.fireAt) { editorError = "실행할 날짜와 시간을 골라 주세요."; return; }
    if (editing.scheduleType === "once" && new Date(payload.fireAt).getTime() <= Date.now()) { editorError = "이미 지난 시간이에요. 앞으로의 시간을 골라 주세요."; return; }
    saving = true;
    editorError = "";
    const { ok, err } = await afterMutateSave(payload);
    saving = false;
    if (ok) editing = null;
    else editorError = err || "저장에 실패했습니다. 연결 상태를 확인하고 다시 시도해 주세요.";
  }
  // Save keeps the editor open (input preserved) on failure — a transient
  // control-link drop (e.g. host restart) must not silently lose the edit.
  async function afterMutateSave(payload) {
    let ok = true, err = "";
    try {
      const raw = await ControlService.SaveTask(JSON.stringify(payload));
      const j = JSON.parse(raw || "{}");
      if (j && j.ok === false) { ok = false; err = j.error || "요청 실패"; }
    } catch (e) { ok = false; err = "연결이 끊겨 처리하지 못했습니다. 잠시 후 다시 시도해 주세요."; }
    await load();
    return { ok, err };
  }

  const chip = "rounded-full border border-slate-300 dark:border-slate-600 px-2 py-0.5 text-[11px] text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-700";
  const seg = (on) =>
    `flex-1 rounded border px-2 py-1.5 text-sm ${on ? "border-blue-500 dark:border-blue-400 bg-blue-50 dark:bg-blue-950/40 font-semibold text-blue-700 dark:text-blue-300" : "border-slate-300 dark:border-slate-600 text-slate-600 dark:text-slate-400 hover:bg-slate-50 dark:hover:bg-slate-800"}`;
  const field = "w-full rounded border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-2 py-1.5 text-sm";
  const smallBtn = "rounded border border-slate-300 dark:border-slate-600 px-2 py-1 text-xs text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700";
</script>

<div class="flex h-full min-h-0 flex-col">
  <div class="flex h-11 shrink-0 items-center gap-2 border-b border-slate-200 dark:border-slate-700 px-3">
    <div class="min-w-0 flex-1 truncate text-sm font-semibold text-slate-900 dark:text-slate-100">예약된 알림/작업</div>
    <button class="grid h-8 w-8 place-items-center rounded-md bg-blue-600 text-base font-semibold text-white hover:bg-blue-700" onclick={() => openEditor(null)} title="새 예약" aria-label="새 예약">＋</button>
    <button class="grid h-8 w-8 place-items-center rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={load} title="새로고침" aria-label="새로고침">↻</button>
  </div>

  <!-- Quick add: a sentence as it would be said -->
  <div class="shrink-0 border-b border-slate-200 dark:border-slate-700 px-3 py-2">
    <form class="flex gap-1.5" onsubmit={(e) => { e.preventDefault(); quickAdd(); }}>
      <input
        class="min-w-0 flex-1 rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-2 py-1.5 text-sm"
        bind:value={quick}
        oninput={() => (quickNote = "")}
        placeholder="예: 30분 뒤에 회의 알려 줘"
        aria-label="빠른 알림 추가"
      />
      <button class="shrink-0 rounded-md bg-blue-600 px-2.5 text-xs font-semibold text-white hover:bg-blue-700 disabled:opacity-50" disabled={!quick.trim() || quickBusy}>추가</button>
    </form>
    {#if quickNote}
      <div class="mt-1 text-[11px] leading-4 text-slate-500 dark:text-slate-400">{quickNote}</div>
    {:else}
      <div class="mt-1 text-[11px] leading-4 text-slate-400 dark:text-slate-500">말하듯 쓰면 알림이 돼요 · {quickDestText}</div>
    {/if}
  </div>

  <div class="flex shrink-0 gap-1 border-b border-slate-200 dark:border-slate-700 px-3 py-2">
    {#each filters as f}
      <button
        class={`rounded-full px-2.5 py-1 text-[11px] font-semibold ${filter === f.value ? "bg-blue-600 text-white" : "border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-700"}`}
        onclick={() => (filter = f.value)}
      >{f.label}</button>
    {/each}
  </div>

  <div class="min-h-0 flex-1 overflow-y-auto px-3 py-3">
    {#if loadError}
      <div class="mb-2 rounded bg-rose-50 dark:bg-rose-950/40 px-2 py-1 text-xs text-rose-700 dark:text-rose-300">{loadError}</div>
    {/if}
    {#if sorted.length === 0}
      <div class="px-1 py-3 text-sm leading-6 text-slate-500 dark:text-slate-400">
        {filter === "pending" ? "기다리는 예약이 없어요." : "표시할 예약이 없어요."}
        위 칸에 "내일 아침 9시에 약 먹으라고 알려 줘"처럼 쓰거나, ＋로 반복 알림이나 AI 작업을 만들어 보세요. 음성 대화에서 말로 맡겨도 돼요.
      </div>
    {/if}
    <div class="space-y-2">
      {#each sorted as t (t.id)}
        <div class={`rounded-lg border border-slate-200 dark:border-slate-700 bg-white/85 dark:bg-slate-900/85 p-3 shadow-sm ${t.status !== "pending" ? "opacity-70" : ""}`}>
          <div class="flex items-start gap-2">
            <span class="mt-0.5 shrink-0 text-base" title={t.playbookId ? "업무 루틴" : t.isTask ? "AI 작업" : "알림"}>{t.playbookId ? "▶" : t.isTask ? "🤖" : "🔔"}</span>
            <button class="min-w-0 flex-1 text-left" onclick={() => openEditor(t)} title="편집">
              <div class="truncate text-sm font-semibold text-slate-900 dark:text-slate-100">{title(t)}</div>
              {#if t.prompt && t.prompt !== t.label}
                <div class="mt-0.5 line-clamp-2 break-words text-xs leading-5 text-slate-500 dark:text-slate-400">{t.prompt}</div>
              {/if}
            </button>
            {#if t.status !== "pending"}
              <span class={`shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold ${statusBadge(t.status)}`}>{statusLabel(t.status)}</span>
            {/if}
          </div>
          <div class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-slate-500 dark:text-slate-400">
            <span class="font-medium text-slate-700 dark:text-slate-300">{scheduleText(t)}</span>
            {#if t.status === "pending" && nextText(t)}
              <span class="font-semibold text-blue-600 dark:text-blue-400">{nextText(t)}</span>
            {/if}
            {#if t.playbookId}
              <span>업무 루틴 · 실행할 때마다 새 대화</span>
            {:else}
              <span title="결과 받을 곳">💬 {destLabel(destOf(t))}</span>
            {/if}
            {#if t.script}
              <span title={t.script}>사전 확인 있음</span>
            {/if}
          </div>
          <div class="mt-2 flex justify-end gap-1.5">
            {#if t.status === "pending"}
              <button class={smallBtn} onclick={() => pause(t)}>⏸ 잠시 멈춤</button>
            {:else if t.status === "paused"}
              <button class="rounded border border-emerald-300 dark:border-emerald-700 px-2 py-1 text-xs text-emerald-700 dark:text-emerald-300 hover:bg-emerald-50 dark:hover:bg-emerald-950/40" onclick={() => resume(t)}>▶ 다시 시작</button>
            {/if}
            <button class={smallBtn} onclick={() => openEditor(t)}>✏️ 편집</button>
            <button class="rounded border border-rose-200 dark:border-rose-800 px-2 py-1 text-xs text-rose-700 dark:text-rose-300 hover:bg-rose-50 dark:hover:bg-rose-950/40" onclick={() => del(t)}>🗑 삭제</button>
          </div>
        </div>
      {/each}
    </div>
  </div>
</div>

<!-- Editor modal -->
{#if editing}
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/40 p-4" onclick={(e) => { if (e.target === e.currentTarget) editing = null; }}>
    <div class="flex max-h-[88vh] w-[520px] max-w-full flex-col overflow-hidden rounded-lg bg-white dark:bg-slate-900 shadow-xl">
      <div class="border-b border-slate-200 dark:border-slate-700 px-4 py-3 text-sm font-semibold text-slate-900 dark:text-slate-100">{editing.id ? "예약 편집" : "새 예약"}</div>
      <div class="flex-1 space-y-4 overflow-y-auto px-4 py-3 text-left">
        <div>
          <span class="mb-1 block text-xs font-medium text-slate-600 dark:text-slate-400">무엇을 할까요</span>
          <div class="flex gap-2">
            <button type="button" class={seg(!editing.isTask)} onclick={() => (editing.isTask = false)}>🔔 알림 보내기</button>
            <button type="button" class={seg(editing.isTask)} onclick={() => (editing.isTask = true)}>🤖 AI에게 일 시키기</button>
          </div>
          <span class="mt-1 block text-[11px] text-slate-400 dark:text-slate-500">
            {editing.isTask ? "정한 시간에 아래 내용을 AI에게 보내고, 답은 결과 받을 대화에 와요." : "정한 시간에 아래 메시지를 보내요. 음성 대화를 켜 두면 소리로도 읽어 드려요."}
          </span>
        </div>
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600 dark:text-slate-400">{editing.isTask ? "AI에게 시킬 내용" : "알림 메시지"}</span>
          <textarea class={`${field} resize-y`} rows="3" bind:value={editing.prompt} placeholder={editing.isTask ? "예: 어제 빌드 로그를 확인하고 실패가 있으면 요약해 줘" : "예: 회의 10분 전이에요"}></textarea>
        </label>

        <div>
          <span class="mb-1 block text-xs font-medium text-slate-600 dark:text-slate-400">언제</span>
          <div class="mb-2 flex gap-2">
            <button type="button" class={seg(editing.scheduleType === "once")} onclick={() => (editing.scheduleType = "once")}>한 번</button>
            <button type="button" class={seg(editing.scheduleType === "repeat")} onclick={() => (editing.scheduleType = "repeat")}>반복</button>
          </div>
          {#if editing.scheduleType === "once"}
            <input type="datetime-local" class={field} bind:value={editing.fireAtLocal} />
            <div class="mt-1.5 flex flex-wrap gap-1.5">
              {#each quickTimes as q}
                <button type="button" class={chip} onclick={() => (editing.fireAtLocal = q.value())}>{q.label}</button>
              {/each}
            </div>
          {:else}
            <div class="flex flex-wrap gap-1.5">
              {#each repeatKinds as k}
                <button type="button" class={`rounded-full px-2.5 py-1 text-xs ${editing.repeat.kind === k.value ? "bg-blue-600 font-semibold text-white" : "border border-slate-300 dark:border-slate-600 text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-700"}`} onclick={() => (editing.repeat.kind = k.value)}>{k.label}</button>
              {/each}
            </div>
            <div class="mt-2">
              {#if editing.repeat.kind === "weekly"}
                <div class="mb-2 flex gap-1">
                  {#each WEEKDAYS as w, d}
                    <button type="button" class={`h-8 w-8 rounded-full text-xs ${editing.repeat.days.includes(d) ? "bg-blue-600 font-semibold text-white" : "border border-slate-300 dark:border-slate-600 text-slate-600 dark:text-slate-400"}`} onclick={() => toggleDay(d)}>{w}</button>
                  {/each}
                </div>
              {/if}
              {#if editing.repeat.kind === "daily" || editing.repeat.kind === "weekdays" || editing.repeat.kind === "weekly"}
                <input type="time" class={`${field} w-40`} bind:value={editing.repeat.time} />
              {:else if editing.repeat.kind === "every"}
                <div class="flex items-center gap-2">
                  <input type="number" min="1" class={`${field} w-24`} bind:value={editing.repeat.everyN} />
                  <select class={`${field} w-28`} bind:value={editing.repeat.everyUnit}>
                    <option value="min">분마다</option>
                    <option value="hour">시간마다</option>
                  </select>
                </div>
              {:else}
                <input class={`${field} font-mono`} bind:value={editing.repeat.cron} placeholder="0 9 * * *" />
                <span class="mt-1 block text-[11px] text-slate-400 dark:text-slate-500">cron 표현식 (분 시 일 월 요일). 예: 매달 1일 오전 9시 = 0 9 1 * *</span>
              {/if}
            </div>
          {/if}
          {#if editorRepeatError}
            <div class="mt-2 text-xs font-medium text-rose-700 dark:text-rose-300">{editorRepeatError}</div>
          {:else if editorPreview}
            <div class="mt-2 text-xs font-medium text-blue-700 dark:text-blue-300">→ {editorPreview}</div>
          {/if}
        </div>

        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600 dark:text-slate-400">결과 받을 대화</span>
          <select class={field} bind:value={editing.dest}>
            {#each destChoices as c (c.value)}
              <option value={c.value}>{c.label}</option>
            {/each}
            {#if !destChoices.some((c) => c.value === editing.dest)}
              <option value={editing.dest}>{destLabel(editing.dest)}</option>
            {/if}
          </select>
        </label>

        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600 dark:text-slate-400">이름 (선택)</span>
          <input class={field} bind:value={editing.label} placeholder="비워 두면 내용 첫 줄로 정해요" />
        </label>

        <details class="text-xs text-slate-500 dark:text-slate-400" bind:open={editing.advanced}>
          <summary class="cursor-pointer select-none">고급 설정</summary>
          <label class="mt-2 block">
            <span class="mb-1 block font-medium text-slate-600 dark:text-slate-400">사전 확인 스크립트</span>
            <textarea class={`${field} resize-y font-mono`} rows="2" bind:value={editing.script} placeholder="실행 전에 확인할 스크립트 (비워 두면 바로 실행)"></textarea>
            <span class="mt-1 block text-[11px] text-slate-400 dark:text-slate-500">스크립트가 할 일이 없다고 답하면 그 회차는 건너뛰어요. 설정에서 스크립트 실행을 허용해야 동작해요.</span>
          </label>
        </details>
      </div>
      {#if editorError}
        <div class="border-t border-rose-100 dark:border-rose-800 bg-rose-50 dark:bg-rose-950/40 px-4 py-2 text-xs text-rose-700 dark:text-rose-300">{editorError}</div>
      {/if}
      <div class="flex justify-end gap-2 border-t border-slate-200 dark:border-slate-700 px-4 py-3">
        <button class="rounded border border-slate-300 dark:border-slate-600 px-3 py-1.5 text-sm hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => (editing = null)}>닫기</button>
        <button class="rounded bg-blue-600 px-4 py-1.5 text-sm font-semibold text-white hover:bg-blue-700 disabled:opacity-50" disabled={!editing.prompt.trim() || saving} onclick={saveEditor}>{saving ? "저장 중…" : "저장"}</button>
      </div>
    </div>
  </div>
{/if}

<!-- Confirm modal -->
{#if ask}
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/40 p-4" onclick={(e) => { if (e.target === e.currentTarget) resolveAsk(false); }}>
    <div class="w-[360px] max-w-full rounded-lg bg-white dark:bg-slate-900 p-4 shadow-xl">
      <div class="mb-3 text-sm font-semibold text-slate-900 dark:text-slate-100">{ask.title}</div>
      <div class="mb-3 text-sm text-slate-700 dark:text-slate-300">{ask.message}</div>
      <div class="flex justify-end gap-2">
        <button class="rounded border border-slate-300 dark:border-slate-600 px-3 py-1.5 text-sm hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => resolveAsk(false)}>취소</button>
        <button class={`rounded px-3 py-1.5 text-sm font-semibold text-white ${ask.danger ? "bg-rose-600 hover:bg-rose-700" : "bg-blue-600 hover:bg-blue-700"}`} onclick={() => resolveAsk(true)}>{ask.confirmLabel}</button>
      </div>
    </div>
  </div>
{/if}
