<script>
  // 예약 (Reservation/Task) 사이드바 패널. Full CRUD over the scheduler's
  // registered reminders/recurring jobs (tasks.json) — the same entities !task
  // manages from Telegram, editable here for the desktop: add, edit, delete,
  // pause/resume, cancel.
  import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";
  import { chat } from "./paneStore.svelte.js";

  let tasks = $state([]);
  let loadError = $state("");
  let filter = $state("pending"); // pending | paused | cancelled | all
  const filters = [
    { value: "pending", label: "대기중" },
    { value: "paused", label: "일시정지" },
    { value: "cancelled", label: "취소됨" },
    { value: "all", label: "전체" },
  ];

  let ask = $state(null); // { kind:"confirm", title, message, danger, resolve }

  // Editor modal state. editing === null → closed.
  // scheduleType "once" → fireAt (datetime-local string); "cron" → cronExpr.
  let editing = $state(null);
  let editorError = $state("");
  let saving = $state(false); // save in flight — guards against a double-submit

  const cronPresets = [
    { label: "매일 09:00", expr: "0 9 * * *" },
    { label: "매시간", expr: "0 * * * *" },
    { label: "30분마다", expr: "*/30 * * * *" },
    { label: "평일 09:00", expr: "0 9 * * 1-5" },
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
  });

  function askConfirm(title, message, danger, confirmLabel) {
    return new Promise((resolve) => { ask = { title, message, danger: !!danger, confirmLabel: confirmLabel || "확인", resolve }; });
  }
  function resolveAsk(val) { const a = ask; ask = null; if (a) a.resolve(val); }

  async function mutate(promise) {
    try {
      const raw = await promise;
      const j = JSON.parse(raw || "{}");
      if (j && j.ok === false) { loadError = j.error || "요청 실패"; return; }
    } catch (e) {
      loadError = "연결이 끊겨 처리하지 못했습니다. 잠시 후 다시 시도해 주세요.";
      return;
    }
    await load();
  }
  const pause = (t) => mutate(ControlService.PauseTask(t.id));
  const resume = (t) => mutate(ControlService.ResumeTask(t.id));
  async function cancel(t) {
    const ok = await askConfirm("예약 취소", `"${t.label || t.prompt}" 예약을 취소할까요? (이력은 "전체" 필터에 남습니다)`, true, "취소하기");
    if (ok) await mutate(ControlService.CancelTask(t.id));
  }
  async function del(t) {
    const ok = await askConfirm("예약 삭제", `"${t.label || t.prompt}" 예약을 완전히 삭제할까요? 이 작업은 되돌릴 수 없습니다.`, true, "삭제하기");
    if (ok) await mutate(ControlService.DeleteTask(t.id));
  }

  function isZero(iso) {
    return !iso || iso.startsWith("0001-01-01");
  }
  function fmtDateTime(iso) {
    if (isZero(iso)) return "";
    try {
      return new Date(iso).toLocaleString("ko-KR", { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
    } catch { return iso; }
  }
  function scheduleText(t) {
    return t.cronExpr ? `반복 · ${t.cronExpr}` : `일회성 · ${fmtDateTime(t.fireAt)}`;
  }
  // Countdown reacts to chat.nowTick (ticks every second in App.svelte) so it
  // stays live without this panel running its own timer.
  function nextText(t) {
    const target = !isZero(t.nextFire) ? t.nextFire : (t.cronExpr ? "" : t.fireAt);
    if (isZero(target) || !target) return "";
    const diffMs = new Date(target).getTime() - chat.nowTick;
    if (diffMs <= 0) return "곧 실행";
    const mins = Math.round(diffMs / 60000);
    if (mins < 1) return "1분 이내";
    if (mins < 60) return `${mins}분 후`;
    const hours = Math.floor(mins / 60);
    if (hours < 24) return `${hours}시간 ${mins % 60}분 후`;
    const days = Math.floor(hours / 24);
    return `${days}일 후`;
  }
  function statusBadge(status) {
    if (status === "pending") return "bg-emerald-100 text-emerald-700";
    if (status === "paused") return "bg-amber-100 text-amber-800";
    return "bg-slate-200 text-slate-600";
  }
  function statusLabel(status) {
    if (status === "pending") return "대기중";
    if (status === "paused") return "일시정지";
    if (status === "cancelled") return "취소됨";
    return status;
  }

  // --- datetime-local <-> ISO helpers ---
  // datetime-local has no timezone; treat the value as local time (matches how
  // the user picks it) and let `new Date(...)` interpret it in the local zone.
  function isoToLocalInput(iso) {
    if (isZero(iso)) return "";
    const d = new Date(iso);
    const pad = (n) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }
  function localInputToISO(v) {
    if (!v) return "";
    const d = new Date(v);
    if (Number.isNaN(d.getTime())) return "";
    return d.toISOString();
  }
  function defaultFireAt() {
    const d = new Date(Date.now() + 60 * 60 * 1000); // +1시간
    const pad = (n) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }

  // --- editor ---
  function openEditor(t) {
    if (t) {
      editing = {
        id: t.id,
        label: t.label || "",
        isTask: !!t.isTask,
        prompt: t.prompt || "",
        script: t.script || "",
        scheduleType: t.cronExpr ? "cron" : "once",
        cronExpr: t.cronExpr || "0 9 * * *",
        fireAtLocal: isoToLocalInput(t.fireAt) || defaultFireAt(),
      };
    } else {
      editing = {
        id: "", label: "", isTask: false, prompt: "", script: "",
        scheduleType: "once", cronExpr: "0 9 * * *", fireAtLocal: defaultFireAt(),
      };
    }
    editorError = "";
    saving = false;
  }

  async function saveEditor() {
    const prompt = editing.prompt.trim();
    if (!prompt || saving) return;
    const payload = {
      id: editing.id,
      label: editing.label.trim() || prompt,
      isTask: editing.isTask,
      prompt,
      script: editing.script.trim(),
      cronExpr: editing.scheduleType === "cron" ? editing.cronExpr.trim() : "",
      fireAt: editing.scheduleType === "once" ? localInputToISO(editing.fireAtLocal) : "",
    };
    if (editing.scheduleType === "cron" && !payload.cronExpr) { editorError = "반복 주기(cron 표현식)를 입력해 주세요."; return; }
    if (editing.scheduleType === "once" && !payload.fireAt) { editorError = "실행할 날짜/시간을 선택해 주세요."; return; }
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
</script>

<div class="flex h-full min-h-0 flex-col">
  <div class="flex h-11 shrink-0 items-center gap-2 border-b border-slate-200 px-3">
    <div class="min-w-0 flex-1 truncate text-sm font-semibold text-slate-900">예약된 알림/작업</div>
    <button class="grid h-8 w-8 place-items-center rounded-md bg-blue-600 text-base font-semibold text-white hover:bg-blue-700" onclick={() => openEditor(null)} title="새 예약" aria-label="새 예약">＋</button>
    <button class="grid h-8 w-8 place-items-center rounded-md border border-slate-300 bg-white text-sm text-slate-700 hover:bg-slate-100" onclick={load} title="새로고침" aria-label="새로고침">↻</button>
  </div>

  <div class="flex shrink-0 gap-1 border-b border-slate-200 px-3 py-2">
    {#each filters as f}
      <button
        class={`rounded-full px-2.5 py-1 text-[11px] font-semibold ${filter === f.value ? "bg-blue-600 text-white" : "border border-slate-300 bg-white text-slate-600 hover:bg-slate-100"}`}
        onclick={() => (filter = f.value)}
      >{f.label}</button>
    {/each}
  </div>

  <div class="min-h-0 flex-1 overflow-y-auto px-3 py-3">
    {#if loadError}
      <div class="mb-2 rounded bg-rose-50 px-2 py-1 text-xs text-rose-700">{loadError}</div>
    {/if}
    {#if tasks.length === 0}
      <div class="px-1 py-3 text-sm text-slate-500">
        {filter === "pending" ? "대기중인 예약이 없습니다." : "표시할 예약이 없습니다."}
        ＋로 새 예약을 만들거나, 텔레그램에서 <code class="rounded bg-slate-100 px-1">!task</code> 또는 AI에게 요청해 등록하세요.
      </div>
    {/if}
    <div class="space-y-2">
      {#each tasks as t (t.id)}
        <div class="rounded-lg border border-slate-200 bg-white/85 p-3 shadow-sm">
          <div class="flex items-start gap-2">
            <button class="min-w-0 flex-1 text-left" onclick={() => openEditor(t)} title="편집">
              <div class="truncate text-sm font-semibold text-slate-900" title={t.label}>{t.label || t.prompt}</div>
              <div class="mt-1 whitespace-pre-wrap break-words text-xs leading-5 text-slate-500">{t.prompt}</div>
            </button>
            <span class={`shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold ${statusBadge(t.status)}`}>{statusLabel(t.status)}</span>
          </div>
          <div class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-slate-500">
            <span>{t.isTask ? "🤖 작업" : "🔔 알림"}</span>
            <span>{scheduleText(t)}</span>
            {#if t.status === "pending" && nextText(t)}
              <span class="font-semibold text-blue-600">→ {nextText(t)}</span>
            {/if}
            {#if t.script}
              <span title={t.script}>[스크립트]</span>
            {/if}
          </div>
          <div class="mt-2 flex justify-end gap-1.5">
            <button class="rounded border border-slate-300 px-2 py-1 text-xs text-slate-700 hover:bg-slate-100" onclick={() => openEditor(t)}>✏️ 편집</button>
            {#if t.status === "pending"}
              <button class="rounded border border-slate-300 px-2 py-1 text-xs text-slate-700 hover:bg-slate-100" onclick={() => pause(t)}>⏸ 일시정지</button>
            {:else if t.status === "paused"}
              <button class="rounded border border-emerald-300 px-2 py-1 text-xs text-emerald-700 hover:bg-emerald-50" onclick={() => resume(t)}>▶ 재개</button>
            {/if}
            {#if t.status !== "cancelled"}
              <button class="rounded border border-rose-200 px-2 py-1 text-xs text-rose-700 hover:bg-rose-50" onclick={() => cancel(t)}>✕ 취소</button>
            {/if}
            <button class="rounded border border-rose-300 px-2 py-1 text-xs font-semibold text-rose-700 hover:bg-rose-50" onclick={() => del(t)}>🗑 삭제</button>
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
    <div class="flex max-h-[88vh] w-[520px] max-w-full flex-col overflow-hidden rounded-lg bg-white shadow-xl">
      <div class="border-b border-slate-200 px-4 py-3 text-sm font-semibold text-slate-900">{editing.id ? "예약 편집" : "새 예약"}</div>
      <div class="flex-1 space-y-3 overflow-y-auto px-4 py-3 text-left">
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600">이름 (선택)</span>
          <input class="w-full rounded border border-slate-300 px-2 py-1.5 text-sm" bind:value={editing.label} placeholder="예: 구글 플레이 콘솔 재개 알림" />
        </label>
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600">종류</span>
          <div class="flex gap-2">
            <button type="button" class={`flex-1 rounded border px-2 py-1.5 text-sm ${!editing.isTask ? "border-blue-500 bg-blue-50 font-semibold text-blue-700" : "border-slate-300 text-slate-600"}`} onclick={() => (editing.isTask = false)}>🔔 알림 (메시지만 전송)</button>
            <button type="button" class={`flex-1 rounded border px-2 py-1.5 text-sm ${editing.isTask ? "border-blue-500 bg-blue-50 font-semibold text-blue-700" : "border-slate-300 text-slate-600"}`} onclick={() => (editing.isTask = true)}>🤖 작업 (AI에게 실행 요청)</button>
          </div>
        </label>
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600">{editing.isTask ? "AI에게 시킬 내용" : "알림 메시지"}</span>
          <textarea class="w-full resize-y rounded border border-slate-300 px-2 py-1.5 text-sm" rows="3" bind:value={editing.prompt} placeholder={editing.isTask ? "예: 빌드 로그를 확인하고 실패 시 요약해서 알려줘" : "예: 회의 시작 10분 전입니다"}></textarea>
        </label>
        <div class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600">일정</span>
          <div class="mb-2 flex gap-2">
            <button type="button" class={`flex-1 rounded border px-2 py-1.5 text-sm ${editing.scheduleType === "once" ? "border-blue-500 bg-blue-50 font-semibold text-blue-700" : "border-slate-300 text-slate-600"}`} onclick={() => (editing.scheduleType = "once")}>일회성</button>
            <button type="button" class={`flex-1 rounded border px-2 py-1.5 text-sm ${editing.scheduleType === "cron" ? "border-blue-500 bg-blue-50 font-semibold text-blue-700" : "border-slate-300 text-slate-600"}`} onclick={() => (editing.scheduleType = "cron")}>반복</button>
          </div>
          {#if editing.scheduleType === "once"}
            <input type="datetime-local" class="w-full rounded border border-slate-300 px-2 py-1.5 text-sm" bind:value={editing.fireAtLocal} />
          {:else}
            <input class="w-full rounded border border-slate-300 px-2 py-1.5 font-mono text-sm" bind:value={editing.cronExpr} placeholder="0 9 * * *" />
            <div class="mt-1.5 flex flex-wrap gap-1.5">
              {#each cronPresets as p}
                <button type="button" class="rounded-full border border-slate-300 px-2 py-0.5 text-[11px] text-slate-600 hover:bg-slate-100" onclick={() => (editing.cronExpr = p.expr)}>{p.label}</button>
              {/each}
            </div>
            <span class="mt-1 block text-[11px] text-slate-400">5필드 cron 표현식 (분 시 일 월 요일)</span>
          {/if}
        </div>
      </div>
      {#if editorError}
        <div class="border-t border-rose-100 bg-rose-50 px-4 py-2 text-xs text-rose-700">{editorError}</div>
      {/if}
      <div class="flex justify-end gap-2 border-t border-slate-200 px-4 py-3">
        <button class="rounded border border-slate-300 px-3 py-1.5 text-sm hover:bg-slate-100" onclick={() => (editing = null)}>취소</button>
        <button class="rounded bg-blue-600 px-4 py-1.5 text-sm font-semibold text-white hover:bg-blue-700 disabled:opacity-50" disabled={!editing.prompt.trim() || saving} onclick={saveEditor}>{saving ? "저장 중…" : "저장"}</button>
      </div>
    </div>
  </div>
{/if}

<!-- Confirm modal -->
{#if ask}
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/40 p-4" onclick={(e) => { if (e.target === e.currentTarget) resolveAsk(false); }}>
    <div class="w-[360px] max-w-full rounded-lg bg-white p-4 shadow-xl">
      <div class="mb-3 text-sm font-semibold text-slate-900">{ask.title}</div>
      <div class="mb-3 text-sm text-slate-700">{ask.message}</div>
      <div class="flex justify-end gap-2">
        <button class="rounded border border-slate-300 px-3 py-1.5 text-sm hover:bg-slate-100" onclick={() => resolveAsk(false)}>취소</button>
        <button class={`rounded px-3 py-1.5 text-sm font-semibold text-white ${ask.danger ? "bg-rose-600 hover:bg-rose-700" : "bg-blue-600 hover:bg-blue-700"}`} onclick={() => resolveAsk(true)}>{ask.confirmLabel}</button>
      </div>
    </div>
  </div>
{/if}
