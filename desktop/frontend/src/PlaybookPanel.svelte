<script>
  // 업무 관리 (Playbook) 사이드바 패널. Reusable work routines in a group tree,
  // persisted server-side via ControlService. "실행" composes a routine into a
  // prompt, opens a fresh conversation, and dispatches it — automating the repeat.
  //
  // For everyday use: a search box, each routine's "언제 쓰는지" under its name
  // with when it last ran, ready-made examples to start from, and "정기 실행
  // 예약" that hands a routine to the 예약 scheduler (Task.playbookId) so it runs
  // by itself every day / weekday / week.
  import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";
  import { chat, loadConversations, selectTargetFromSidebar } from "./paneStore.svelte.js";
  import { WEEKDAYS, repeatToCron, describeCron } from "./schedule.js";

  let groups = $state([]);
  let playbooks = $state([]);
  let loadError = $state("");
  let notice = $state(""); // what the last action did ("매일 오전 9시에 실행하도록 예약했어요")
  let search = $state("");
  let scheduling = $state(null); // { book, repeat, saving, error } — the 정기 실행 예약 dialog

  // Ready-made routines to start from; they open in the editor to be adjusted.
  const templates = [
    {
      name: "아침 업무 정리",
      description: "하루를 시작할 때",
      instructions: "- 오늘 날짜와 요일을 알려 준다\n- 어제 이후 작업 폴더에서 바뀐 파일과 커밋을 요약한다\n- 오늘 먼저 할 일 세 가지를 제안한다\n- 짧게, 말로 들어도 이해되게 정리한다",
    },
    {
      name: "빌드·테스트 점검",
      description: "코드를 고친 뒤 확인할 때",
      instructions: "- 작업 폴더에서 최신 코드를 받는다\n- 빌드와 테스트를 돌린다\n- 실패하면 원인과 고칠 곳을 요약한다\n- 통과하면 통과했다고 한 줄로 알린다",
    },
    {
      name: "주간 보고 초안",
      description: "매주 금요일 보고서를 쓸 때",
      instructions: "- 이번 주 커밋과 대화 기록을 훑어 한 일을 정리한다\n- 완료, 진행 중, 다음 주 계획으로 나눈다\n- 보고서에 바로 붙일 수 있는 문장으로 쓴다",
    },
  ];

  // Editor modal state. editing === null → closed. A routine is now a "skill":
  // name + description(when to use) + a free-form natural-language instructions body.
  let editing = $state(null); // { id, name, groupId, description, workDir, backend, instructions }
  let editorError = $state(""); // in-modal save error; keeps the editor open so input isn't lost
  let saving = $state(false);   // save in flight — guards against a double-submit
  // Generic small prompt/confirm modal.
  let ask = $state(null); // { kind:"text"|"confirm"|"menu", title, ... , resolve }

  const backends = ["", "claude", "codex", "opencode"];

  async function load() {
    try {
      const raw = await ControlService.ListPlaybooks();
      const data = JSON.parse(raw || "{}");
      groups = Array.isArray(data.groups) ? data.groups : [];
      playbooks = Array.isArray(data.playbooks) ? data.playbooks : [];
      loadError = "";
    } catch (e) {
      loadError = "업무 목록을 불러오지 못했습니다.";
    }
  }
  load();

  function childrenOf(pid) {
    return groups.filter((g) => (g.parentId || "") === (pid || ""));
  }
  function booksOf(gid) {
    return playbooks.filter((b) => (b.groupId || "") === (gid || ""));
  }
  function countIn(gid) {
    return playbooks.filter((b) => (b.groupId || "") === gid).length;
  }

  // A search shows the matching routines as a flat list instead of the tree.
  const found = $derived.by(() => {
    const q = search.trim().toLowerCase();
    if (!q) return null;
    return playbooks.filter((b) => [b.name, b.description, b.instructions].some((s) => (s || "").toLowerCase().includes(q)));
  });

  function lastRunText(b) {
    if (!b.lastRunAt || b.lastRunAt.startsWith("0001")) return "";
    const days = Math.floor((chat.nowTick - new Date(b.lastRunAt).getTime()) / 86400000);
    return days <= 0 ? "오늘 실행" : days === 1 ? "어제 실행" : `${days}일 전 실행`;
  }

  // --- 정기 실행 예약 ---
  function openSchedule(b) {
    scheduling = { book: b, repeat: { kind: "daily", time: "09:00", days: [new Date().getDay()], everyN: 1, everyUnit: "hour", cron: "" }, saving: false, error: "" };
  }
  const scheduleCron = $derived(scheduling ? repeatToCron(scheduling.repeat) : "");
  function toggleScheduleDay(d) {
    const days = scheduling.repeat.days;
    scheduling.repeat.days = days.includes(d) ? days.filter((x) => x !== d) : [...days, d];
  }
  async function saveSchedule() {
    const s = scheduling;
    if (!s || s.saving) return;
    if (!scheduleCron) { s.error = "요일을 하나 이상 골라 주세요."; return; }
    s.saving = true;
    s.error = "";
    const payload = { label: `▶ ${s.book.name}`, isTask: true, prompt: `업무 루틴 "${s.book.name}" 실행`, cronExpr: scheduleCron, playbookId: s.book.id };
    try {
      const j = JSON.parse((await ControlService.SaveTask(JSON.stringify(payload))) || "{}");
      if (j.ok === false) throw new Error(j.error || "저장 실패");
      notice = `"${s.book.name}"을(를) ${describeCron(scheduleCron)}에 실행하도록 예약했어요. 예약 탭에서 바꾸거나 멈출 수 있어요.`;
      scheduling = null;
    } catch (e) {
      s.saving = false;
      s.error = `예약하지 못했어요: ${e?.message || e}`;
    }
  }

  // Flatten the tree into display rows so the template stays simple.
  const rows = $derived(buildRows());
  function buildRows() {
    const out = [];
    for (const b of booksOf("")) out.push({ type: "book", book: b, depth: 0 });
    const walk = (g, depth) => {
      out.push({ type: "group", group: g, depth });
      for (const b of booksOf(g.id)) out.push({ type: "book", book: b, depth: depth + 1 });
      for (const c of childrenOf(g.id)) walk(c, depth + 1);
    };
    for (const g of childrenOf("")) walk(g, 0);
    return out;
  }

  // --- ask helpers (promise-based small modal) ---
  function askText(title, label, value) {
    return new Promise((resolve) => { ask = { kind: "text", title, label, value: value || "", resolve }; });
  }
  function askConfirm(title, message, danger) {
    return new Promise((resolve) => { ask = { kind: "confirm", title, message, danger: !!danger, resolve }; });
  }
  function askMenu(title, options) {
    return new Promise((resolve) => { ask = { kind: "menu", title, options, resolve }; });
  }
  function resolveAsk(val) { const a = ask; ask = null; if (a) a.resolve(val); }

  // --- mutations ---
  // Returns {ok, err} so callers can react to the real outcome. Critically the
  // editor uses this to keep itself open (input preserved) when a save fails —
  // e.g. the control link briefly drops when the host restarts, which otherwise
  // silently ate the routine and looked like "저장 버튼이 아무것도 안 함".
  async function afterMutate(rawPromise) {
    let ok = true, err = "";
    try {
      const raw = await rawPromise;
      const j = JSON.parse(raw || "{}");
      if (j && j.ok === false) { ok = false; err = j.error || "요청 실패"; }
    } catch (e) { ok = false; err = "연결이 끊겨 처리하지 못했습니다. 잠시 후 다시 시도해 주세요."; }
    if (!ok) loadError = err;
    await load();
    return { ok, err };
  }
  const saveGroup = (g) => afterMutate(ControlService.SavePlaybookGroup(JSON.stringify(g)));
  const savePlaybook = (p) => afterMutate(ControlService.SavePlaybook(JSON.stringify(p)));
  const delGroup = (id) => afterMutate(ControlService.DeletePlaybookGroup(id));
  const delPlaybook = (id) => afterMutate(ControlService.DeletePlaybook(id));

  async function newGroup() {
    const name = await askText("새 그룹", "그룹 이름", "");
    if (name && name.trim()) await saveGroup({ name: name.trim() });
  }

  async function groupMenu(g) {
    const choice = await askMenu("그룹: " + g.name, [
      { value: "add", label: "＋ 이 그룹에 업무 루틴 추가" },
      { value: "sub", label: "📁 하위 그룹 추가" },
      { value: "rename", label: "✏️ 이름 변경" },
      { value: "delete", label: "🗑 그룹 삭제 (내용은 상위로)", danger: true },
    ]);
    if (choice === "add") return openEditor(null, g.id);
    if (choice === "sub") {
      const name = await askText("하위 그룹", "그룹 이름", "");
      if (name && name.trim()) await saveGroup({ name: name.trim(), parentId: g.id });
    } else if (choice === "rename") {
      const name = await askText("이름 변경", "그룹 이름", g.name);
      if (name && name.trim()) await saveGroup({ id: g.id, name: name.trim(), parentId: g.parentId || "" });
    } else if (choice === "delete") {
      const ok = await askConfirm("그룹 삭제", `"${g.name}" 그룹을 삭제할까요? 안의 루틴과 하위 그룹은 상위로 옮겨집니다.`, true);
      if (ok) await delGroup(g.id);
    }
  }

  async function rowMenu(b) {
    const choice = await askMenu("업무: " + b.name, [
      { value: "edit", label: "✏️ 편집" },
      { value: "run", label: "▶ 지금 실행" },
      { value: "schedule", label: "⏰ 정기 실행 예약" },
      { value: "copy", label: "📄 복제" },
      { value: "move", label: "📁 그룹 이동" },
      { value: "delete", label: "🗑 삭제", danger: true },
    ]);
    if (choice === "edit") return openEditor(b);
    if (choice === "run") return runPlaybook(b);
    if (choice === "schedule") return openSchedule(b);
    if (choice === "copy") {
      const { id: _id, runCount: _r, lastRunAt: _l, createdAt: _c, updatedAt: _u, ...rest } = b;
      await savePlaybook({ ...rest, name: `${b.name} (복사본)` });
      return;
    }
    if (choice === "move") {
      const opts = [{ value: "", label: "(그룹 없음 · 루트)" }].concat(groups.map((g) => ({ value: g.id, label: g.name })));
      const gid = await askMenu("이동할 그룹 선택", opts);
      if (gid !== null) await savePlaybook({ ...b, groupId: gid });
    } else if (choice === "delete") {
      const ok = await askConfirm("업무 삭제", `"${b.name}" 루틴을 삭제할까요?`, true);
      if (ok) await delPlaybook(b.id);
    }
  }

  async function runPlaybook(b) {
    const ok = await askConfirm("업무 실행", `"${b.name}" 루틴을 새 대화에서 실행할까요? 등록한 업무 내용을 AI가 스킬처럼 이해해 실행합니다.`, false);
    if (!ok) return;
    try {
      const raw = await ControlService.RunPlaybook(b.id);
      const j = JSON.parse(raw || "{}");
      if (j.ok === false) { loadError = j.error || "실행 실패"; return; }
      await load();
      await loadConversations();
      if (j.conversationId) selectTargetFromSidebar({ kind: "web", id: j.conversationId });
    } catch (e) { loadError = "실행 실패"; }
  }

  // --- editor ---
  function openEditor(b, presetGroupId, template) {
    if (template) {
      editing = { id: "", name: template.name, groupId: presetGroupId || "", description: template.description, workDir: "", backend: "", instructions: template.instructions };
    } else if (b) {
      editing = {
        id: b.id, name: b.name, groupId: b.groupId || "", description: b.description || "",
        workDir: b.workDir || "", backend: b.backend || "",
        instructions: b.instructions || legacyToText(b),
      };
    } else {
      editing = { id: "", name: "", groupId: presetGroupId || "", description: "", workDir: "", backend: "", instructions: "" };
    }
    editorError = "";
    saving = false;
  }
  // Surface a legacy structured routine (steps/delivery) as editable natural-language
  // text so pre-skill routines aren't lost the moment they're opened for editing.
  function legacyToText(b) {
    const lines = [];
    const steps = b.steps || [];
    if (steps.length) { lines.push("점검·작업 단계:"); steps.forEach((s, i) => lines.push(`${i + 1}. ${s.text}`)); }
    const del = b.delivery || [];
    if (del.length) {
      if (lines.length) lines.push("");
      lines.push("완료 후 배포·전달:");
      for (const d of del) {
        const lab = d.kind === "folder" ? "공유 폴더" : d.kind === "email" ? "메일" : "전달";
        lines.push(`- ${lab}: ${d.dest}${d.note ? ` (${d.note})` : ""}`);
      }
    }
    return lines.join("\n");
  }
  async function saveEditor() {
    const name = editing.name.trim();
    if (!name || saving) return;
    // steps/delivery are intentionally omitted → the store clears any legacy
    // structured fields, so the natural-language body is now the single source.
    const payload = { id: editing.id, name, groupId: editing.groupId, description: editing.description.trim(), workDir: editing.workDir.trim(), backend: editing.backend, instructions: editing.instructions };
    saving = true;
    editorError = "";
    // Close only AFTER a confirmed save — otherwise a transient control-link drop
    // (e.g. host restart) silently loses the routine. On failure keep the editor
    // open with its input intact so the user can just click 저장 again.
    const { ok, err } = await savePlaybook(payload);
    saving = false;
    if (ok) editing = null;
    else editorError = err || "저장에 실패했습니다. 연결 상태를 확인하고 다시 시도해 주세요.";
  }
  async function pickWorkDir() {
    try { const p = await ControlService.PickFolder(); if (p) editing.workDir = p; } catch (e) { /* cancelled */ }
  }
</script>

<div class="flex h-full min-h-0 flex-col">
  <div class="flex h-11 shrink-0 items-center gap-2 border-b border-slate-200 dark:border-slate-700 px-3">
    <div class="min-w-0 flex-1 truncate text-sm font-semibold text-slate-900 dark:text-slate-100">업무 루틴</div>
    <button class="grid h-8 w-8 place-items-center rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={newGroup} title="새 그룹" aria-label="새 그룹">📁</button>
    <button class="grid h-8 w-8 place-items-center rounded-md bg-blue-600 text-base font-semibold text-white hover:bg-blue-700" onclick={() => openEditor(null, "")} title="새 업무 루틴" aria-label="새 루틴">＋</button>
    <button class="grid h-8 w-8 place-items-center rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={load} title="새로고침" aria-label="새로고침">↻</button>
  </div>

  {#if playbooks.length > 0}
    <div class="shrink-0 border-b border-slate-200 dark:border-slate-700 px-3 py-2">
      <input class="w-full rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-2 py-1.5 text-sm" bind:value={search} placeholder="🔍 업무 찾기" aria-label="업무 찾기" />
    </div>
  {/if}

  <div class="min-h-0 flex-1 overflow-y-auto px-2 py-2">
    {#if loadError}
      <div class="mb-2 rounded bg-rose-50 dark:bg-rose-950/40 px-2 py-1 text-xs text-rose-700 dark:text-rose-300">{loadError}</div>
    {/if}
    {#if notice}
      <div class="mb-2 flex items-start gap-2 rounded bg-emerald-50 dark:bg-emerald-950/40 px-2 py-1.5 text-xs text-emerald-800 dark:text-emerald-300">
        <span class="min-w-0 flex-1">{notice}</span>
        <button class="shrink-0" onclick={() => (notice = "")} aria-label="닫기">✕</button>
      </div>
    {/if}
    {#if rows.length === 0}
      <div class="px-2 py-3 text-sm leading-6 text-slate-500 dark:text-slate-400">
        자주 하는 일을 업무 루틴으로 저장해 두면 ▶ 한 번으로 AI가 처리해요. 정해진 시간에 저절로 돌게 예약할 수도 있어요.
        <div class="mt-2 text-xs font-semibold text-slate-600 dark:text-slate-300">예시로 시작하기</div>
        <div class="mt-1 space-y-1.5">
          {#each templates as tpl}
            <button class="block w-full rounded-md border border-slate-200 dark:border-slate-700 px-2.5 py-2 text-left hover:bg-slate-100 dark:hover:bg-slate-800" onclick={() => openEditor(null, "", tpl)}>
              <div class="text-sm font-medium text-slate-800 dark:text-slate-200">{tpl.name}</div>
              <div class="text-[11px] text-slate-500 dark:text-slate-400">{tpl.description}</div>
            </button>
          {/each}
        </div>
      </div>
    {/if}
    {#if found}
      {#if found.length === 0}
        <div class="px-2 py-3 text-sm text-slate-500 dark:text-slate-400">"{search}"에 맞는 업무가 없어요.</div>
      {/if}
      {#each found as b (b.id)}
        {@render bookRow(b, 0)}
      {/each}
    {:else}
    {#each rows as row (row.type + (row.group?.id || row.book?.id))}
      {#if row.type === "group"}
        <div class="flex items-center gap-1 py-1.5 text-xs font-bold text-slate-600 dark:text-slate-400" style={`padding-left:${8 + row.depth * 14}px`}>
          <span class="min-w-0 flex-1 truncate">📁 {row.group.name}{countIn(row.group.id) ? ` (${countIn(row.group.id)})` : ""}</span>
          <button class="grid h-6 w-6 place-items-center rounded hover:bg-slate-200 dark:hover:bg-slate-600" onclick={() => groupMenu(row.group)} aria-label="그룹 메뉴">⋯</button>
        </div>
      {:else}
        {@render bookRow(row.book, row.depth)}
      {/if}
    {/each}
    {/if}
  </div>
</div>

{#snippet bookRow(b, depth)}
  <div class="group flex items-start gap-1.5 rounded-md py-1.5 pr-1 hover:bg-slate-100 dark:hover:bg-slate-700" style={`padding-left:${8 + depth * 14}px`}>
    <button class="mt-0.5 grid h-6 w-6 shrink-0 place-items-center rounded border border-emerald-200 dark:border-emerald-800 text-xs text-emerald-600 dark:text-emerald-400 hover:bg-emerald-50 dark:hover:bg-emerald-950/40" onclick={() => runPlaybook(b)} title="지금 실행" aria-label="지금 실행">▶</button>
    <button class="min-w-0 flex-1 text-left" onclick={() => openEditor(b)} title={b.description || b.name}>
      <div class="truncate text-sm text-slate-800 dark:text-slate-200">{b.name}</div>
      {#if b.description || lastRunText(b)}
        <div class="truncate text-[11px] text-slate-400 dark:text-slate-500">
          {b.description || ""}{b.description && lastRunText(b) ? " · " : ""}{lastRunText(b)}{b.runCount ? ` (${b.runCount}회)` : ""}
        </div>
      {/if}
    </button>
    <button class="grid h-6 w-6 shrink-0 place-items-center rounded text-slate-500 dark:text-slate-400 hover:bg-slate-200 dark:hover:bg-slate-600" onclick={() => openSchedule(b)} title="정기 실행 예약" aria-label="정기 실행 예약">⏰</button>
    <button class="grid h-6 w-6 shrink-0 place-items-center rounded text-slate-500 dark:text-slate-400 hover:bg-slate-200 dark:hover:bg-slate-600" onclick={() => rowMenu(b)} aria-label="루틴 메뉴">⋯</button>
  </div>
{/snippet}

<!-- 정기 실행 예약 -->
{#if scheduling}
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/40 p-4" onclick={(e) => { if (e.target === e.currentTarget) scheduling = null; }}>
    <div class="w-[400px] max-w-full rounded-lg bg-white dark:bg-slate-900 p-4 shadow-xl">
      <div class="mb-1 text-sm font-semibold text-slate-900 dark:text-slate-100">정기 실행 예약</div>
      <div class="mb-3 text-xs text-slate-500 dark:text-slate-400">"{scheduling.book.name}"을(를) 정한 때마다 새 대화에서 저절로 실행해요.</div>
      <div class="flex flex-wrap gap-1.5">
        {#each [["daily", "매일"], ["weekdays", "평일"], ["weekly", "매주"], ["every", "몇 시간마다"]] as [k, l]}
          <button type="button" class={`rounded-full px-2.5 py-1 text-xs ${scheduling.repeat.kind === k ? "bg-blue-600 font-semibold text-white" : "border border-slate-300 dark:border-slate-600 text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-700"}`} onclick={() => (scheduling.repeat.kind = k)}>{l}</button>
        {/each}
      </div>
      <div class="mt-3">
        {#if scheduling.repeat.kind === "weekly"}
          <div class="mb-2 flex gap-1">
            {#each WEEKDAYS as w, d}
              <button type="button" class={`h-8 w-8 rounded-full text-xs ${scheduling.repeat.days.includes(d) ? "bg-blue-600 font-semibold text-white" : "border border-slate-300 dark:border-slate-600 text-slate-600 dark:text-slate-400"}`} onclick={() => toggleScheduleDay(d)}>{w}</button>
            {/each}
          </div>
        {/if}
        {#if scheduling.repeat.kind === "every"}
          <div class="flex items-center gap-2 text-sm text-slate-700 dark:text-slate-300">
            <input type="number" min="1" max="23" class="w-20 rounded border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-2 py-1.5 text-sm" bind:value={scheduling.repeat.everyN} /> 시간마다
          </div>
        {:else}
          <input type="time" class="w-40 rounded border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-2 py-1.5 text-sm" bind:value={scheduling.repeat.time} />
        {/if}
      </div>
      {#if scheduleCron}
        <div class="mt-3 text-xs font-medium text-blue-700 dark:text-blue-300">→ {describeCron(scheduleCron)}에 실행</div>
      {/if}
      {#if scheduling.error}
        <div class="mt-2 rounded bg-rose-50 dark:bg-rose-950/40 px-2 py-1 text-xs text-rose-700 dark:text-rose-300">{scheduling.error}</div>
      {/if}
      <div class="mt-4 flex justify-end gap-2">
        <button class="rounded border border-slate-300 dark:border-slate-600 px-3 py-1.5 text-sm hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => (scheduling = null)}>닫기</button>
        <button class="rounded bg-blue-600 px-4 py-1.5 text-sm font-semibold text-white hover:bg-blue-700 disabled:opacity-50" disabled={scheduling.saving} onclick={saveSchedule}>{scheduling.saving ? "예약 중…" : "예약하기"}</button>
      </div>
    </div>
  </div>
{/if}

<!-- Editor modal -->
{#if editing}
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/40 p-4" onclick={(e) => { if (e.target === e.currentTarget) editing = null; }}>
    <div class="flex max-h-[88vh] w-[560px] max-w-full flex-col overflow-hidden rounded-lg bg-white dark:bg-slate-900 shadow-xl">
      <div class="border-b border-slate-200 dark:border-slate-700 px-4 py-3 text-sm font-semibold text-slate-900 dark:text-slate-100">{editing.id ? "업무 편집" : "새 업무 루틴"}</div>
      <div class="flex-1 space-y-3 overflow-y-auto px-4 py-3 text-left">
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600 dark:text-slate-400">이름</span>
          <input class="w-full rounded border border-slate-300 dark:border-slate-600 px-2 py-1.5 text-sm" bind:value={editing.name} placeholder="예: 릴리스 점검" />
        </label>
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600 dark:text-slate-400">언제 사용 / 설명 (선택)</span>
          <textarea class="w-full resize-y rounded border border-slate-300 dark:border-slate-600 px-2 py-1.5 text-sm" rows="2" bind:value={editing.description} placeholder="예: 특정 프로젝트의 정기 릴리스를 낼 때"></textarea>
        </label>
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600 dark:text-slate-400">작업 폴더 (선택)</span>
          <div class="flex gap-2">
            <input class="w-full rounded border border-slate-300 dark:border-slate-600 px-2 py-1.5 text-sm" bind:value={editing.workDir} placeholder="예: C:\proj" />
            <button class="shrink-0 rounded border border-slate-300 dark:border-slate-600 px-2 text-sm hover:bg-slate-100 dark:hover:bg-slate-700" onclick={pickWorkDir}>찾기…</button>
          </div>
        </label>
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600 dark:text-slate-400">실행 백엔드</span>
          <select class="w-full rounded border border-slate-300 dark:border-slate-600 px-2 py-1.5 text-sm" bind:value={editing.backend}>
            {#each backends as be}
              <option value={be}>{be === "" ? "기본 백엔드" : be}</option>
            {/each}
          </select>
        </label>
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600 dark:text-slate-400">업무 내용 (자연어)</span>
          <textarea class="w-full resize-y rounded border border-slate-300 dark:border-slate-600 px-2 py-1.5 text-sm leading-relaxed" rows="10" bind:value={editing.instructions} placeholder={"이 업무에서 무엇을 어떻게 하는지 평소 말로 설명하듯 적어 주세요.\n\n예)\n- main 최신화 후 빌드·테스트를 돌린다\n- 실패하면 원인을 요약해서 알려준다\n- 통과하면 결과물을 \\\\share\\rel 폴더에 복사하고 팀에 메일로 알린다"}></textarea>
          <span class="mt-1 block text-[11px] text-slate-400 dark:text-slate-500">AI가 이 내용을 하나의 '스킬'처럼 이해해 스스로 단계를 나눠 실행합니다.</span>
        </label>
      </div>
      {#if editorError}
        <div class="border-t border-rose-100 dark:border-rose-800 bg-rose-50 dark:bg-rose-950/40 px-4 py-2 text-xs text-rose-700 dark:text-rose-300">{editorError}</div>
      {/if}
      <div class="flex justify-end gap-2 border-t border-slate-200 dark:border-slate-700 px-4 py-3">
        <button class="rounded border border-slate-300 dark:border-slate-600 px-3 py-1.5 text-sm hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => (editing = null)}>취소</button>
        <button class="rounded bg-blue-600 px-4 py-1.5 text-sm font-semibold text-white hover:bg-blue-700 disabled:opacity-50" disabled={!editing.name.trim() || saving} onclick={saveEditor}>{saving ? "저장 중…" : "저장"}</button>
      </div>
    </div>
  </div>
{/if}

<!-- Small prompt / confirm / menu modal -->
{#if ask}
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/40 p-4" onclick={(e) => { if (e.target === e.currentTarget) resolveAsk(ask.kind === "confirm" ? false : ask.kind === "menu" ? null : ""); }}>
    <div class="w-[360px] max-w-full rounded-lg bg-white dark:bg-slate-900 p-4 shadow-xl">
      <div class="mb-3 text-sm font-semibold text-slate-900 dark:text-slate-100">{ask.title}</div>
      {#if ask.kind === "text"}
        {#if ask.label}<div class="mb-1 text-xs text-slate-600 dark:text-slate-400">{ask.label}</div>{/if}
        <!-- svelte-ignore a11y_autofocus -->
        <input class="w-full rounded border border-slate-300 dark:border-slate-600 px-2 py-1.5 text-sm" bind:value={ask.value} autofocus
          onkeydown={(e) => { if (e.key === "Enter") resolveAsk(ask.value); if (e.key === "Escape") resolveAsk(""); }} />
        <div class="mt-3 flex justify-end gap-2">
          <button class="rounded border border-slate-300 dark:border-slate-600 px-3 py-1.5 text-sm hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => resolveAsk("")}>취소</button>
          <button class="rounded bg-blue-600 px-3 py-1.5 text-sm font-semibold text-white hover:bg-blue-700" onclick={() => resolveAsk(ask.value)}>확인</button>
        </div>
      {:else if ask.kind === "confirm"}
        <div class="mb-3 text-sm text-slate-700 dark:text-slate-300">{ask.message}</div>
        <div class="flex justify-end gap-2">
          <button class="rounded border border-slate-300 dark:border-slate-600 px-3 py-1.5 text-sm hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => resolveAsk(false)}>취소</button>
          <button class={`rounded px-3 py-1.5 text-sm font-semibold text-white ${ask.danger ? "bg-rose-600 hover:bg-rose-700" : "bg-blue-600 hover:bg-blue-700"}`} onclick={() => resolveAsk(true)}>{ask.danger ? "삭제" : "확인"}</button>
        </div>
      {:else if ask.kind === "menu"}
        <div class="space-y-1.5">
          {#each ask.options as opt}
            <button class={`w-full rounded border px-3 py-2 text-left text-sm ${opt.danger ? "border-rose-200 dark:border-rose-800 text-rose-700 dark:text-rose-300 hover:bg-rose-50 dark:hover:bg-rose-950/40" : "border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700"}`} onclick={() => resolveAsk(opt.value)}>{opt.label}</button>
          {/each}
          <button class="mt-1 w-full rounded border border-slate-300 dark:border-slate-600 px-3 py-1.5 text-sm hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => resolveAsk(null)}>취소</button>
        </div>
      {/if}
    </div>
  </div>
{/if}
