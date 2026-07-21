<script>
  // 업무 관리 (Playbook) 사이드바 패널. Reusable work routines in a group tree,
  // persisted server-side via ControlService. "실행" composes a routine into a
  // prompt, opens a fresh conversation, and dispatches it — automating the repeat.
  import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";
  import { loadConversations, selectTargetFromSidebar } from "./paneStore.svelte.js";

  let groups = $state([]);
  let playbooks = $state([]);
  let loadError = $state("");

  // Editor modal state. editing === null → closed. A routine is now a "skill":
  // name + description(when to use) + a free-form natural-language instructions body.
  let editing = $state(null); // { id, name, groupId, description, workDir, backend, instructions }
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
  async function afterMutate(rawPromise) {
    try {
      const raw = await rawPromise;
      const j = JSON.parse(raw || "{}");
      if (j && j.ok === false) loadError = j.error || "요청 실패";
    } catch (e) { loadError = "요청 실패"; }
    await load();
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
      { value: "run", label: "▶ 실행" },
      { value: "move", label: "📁 그룹 이동" },
      { value: "delete", label: "🗑 삭제", danger: true },
    ]);
    if (choice === "edit") return openEditor(b);
    if (choice === "run") return runPlaybook(b);
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
  function openEditor(b, presetGroupId) {
    if (b) {
      editing = {
        id: b.id, name: b.name, groupId: b.groupId || "", description: b.description || "",
        workDir: b.workDir || "", backend: b.backend || "",
        instructions: b.instructions || legacyToText(b),
      };
    } else {
      editing = { id: "", name: "", groupId: presetGroupId || "", description: "", workDir: "", backend: "", instructions: "" };
    }
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
    if (!name) return;
    // steps/delivery are intentionally omitted → the store clears any legacy
    // structured fields, so the natural-language body is now the single source.
    const payload = { id: editing.id, name, groupId: editing.groupId, description: editing.description.trim(), workDir: editing.workDir.trim(), backend: editing.backend, instructions: editing.instructions };
    editing = null;
    await savePlaybook(payload);
  }
  async function pickWorkDir() {
    try { const p = await ControlService.PickFolder(); if (p) editing.workDir = p; } catch (e) { /* cancelled */ }
  }
</script>

<div class="flex h-full min-h-0 flex-col">
  <div class="flex h-11 shrink-0 items-center gap-2 border-b border-slate-200 px-3">
    <div class="min-w-0 flex-1 truncate text-sm font-semibold text-slate-900">업무 루틴</div>
    <button class="grid h-8 w-8 place-items-center rounded-md border border-slate-300 bg-white text-sm text-slate-700 hover:bg-slate-100" onclick={newGroup} title="새 그룹" aria-label="새 그룹">📁</button>
    <button class="grid h-8 w-8 place-items-center rounded-md bg-blue-600 text-base font-semibold text-white hover:bg-blue-700" onclick={() => openEditor(null, "")} title="새 업무 루틴" aria-label="새 루틴">＋</button>
    <button class="grid h-8 w-8 place-items-center rounded-md border border-slate-300 bg-white text-sm text-slate-700 hover:bg-slate-100" onclick={load} title="새로고침" aria-label="새로고침">↻</button>
  </div>

  <div class="min-h-0 flex-1 overflow-y-auto px-2 py-2">
    {#if loadError}
      <div class="mb-2 rounded bg-rose-50 px-2 py-1 text-xs text-rose-700">{loadError}</div>
    {/if}
    {#if rows.length === 0}
      <div class="px-2 py-3 text-sm text-slate-500">＋로 업무 루틴을, 📁로 그룹을 만들어 보세요.</div>
    {/if}
    {#each rows as row (row.type + (row.group?.id || row.book?.id))}
      {#if row.type === "group"}
        <div class="flex items-center gap-1 py-1.5 text-xs font-bold text-slate-600" style={`padding-left:${8 + row.depth * 14}px`}>
          <span class="min-w-0 flex-1 truncate">📁 {row.group.name}{countIn(row.group.id) ? ` (${countIn(row.group.id)})` : ""}</span>
          <button class="grid h-6 w-6 place-items-center rounded hover:bg-slate-200" onclick={() => groupMenu(row.group)} aria-label="그룹 메뉴">⋯</button>
        </div>
      {:else}
        <div class="group flex items-center gap-1.5 rounded-md py-1 pr-1 hover:bg-slate-100" style={`padding-left:${8 + row.depth * 14}px`}>
          <button class="grid h-6 w-6 shrink-0 place-items-center rounded border border-emerald-200 text-xs text-emerald-600 hover:bg-emerald-50" onclick={() => runPlaybook(row.book)} title="실행" aria-label="실행">▶</button>
          <button class="min-w-0 flex-1 truncate text-left text-sm text-slate-800" onclick={() => openEditor(row.book)} title={row.book.description || row.book.name}>
            {row.book.name}
            <span class="text-[11px] text-slate-400">
              {#if row.book.runCount}· 실행 {row.book.runCount}{/if}
            </span>
          </button>
          <button class="grid h-6 w-6 shrink-0 place-items-center rounded text-slate-500 hover:bg-slate-200" onclick={() => rowMenu(row.book)} aria-label="루틴 메뉴">⋯</button>
        </div>
      {/if}
    {/each}
  </div>
</div>

<!-- Editor modal -->
{#if editing}
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/40 p-4" onclick={(e) => { if (e.target === e.currentTarget) editing = null; }}>
    <div class="flex max-h-[88vh] w-[560px] max-w-full flex-col overflow-hidden rounded-lg bg-white shadow-xl">
      <div class="border-b border-slate-200 px-4 py-3 text-sm font-semibold text-slate-900">{editing.id ? "업무 편집" : "새 업무 루틴"}</div>
      <div class="flex-1 space-y-3 overflow-y-auto px-4 py-3 text-left">
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600">이름</span>
          <input class="w-full rounded border border-slate-300 px-2 py-1.5 text-sm" bind:value={editing.name} placeholder="예: 릴리스 점검" />
        </label>
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600">언제 사용 / 설명 (선택)</span>
          <textarea class="w-full resize-y rounded border border-slate-300 px-2 py-1.5 text-sm" rows="2" bind:value={editing.description} placeholder="예: 특정 프로젝트의 정기 릴리스를 낼 때"></textarea>
        </label>
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600">작업 폴더 (선택)</span>
          <div class="flex gap-2">
            <input class="w-full rounded border border-slate-300 px-2 py-1.5 text-sm" bind:value={editing.workDir} placeholder="예: C:\proj" />
            <button class="shrink-0 rounded border border-slate-300 px-2 text-sm hover:bg-slate-100" onclick={pickWorkDir}>찾기…</button>
          </div>
        </label>
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600">실행 백엔드</span>
          <select class="w-full rounded border border-slate-300 px-2 py-1.5 text-sm" bind:value={editing.backend}>
            {#each backends as be}
              <option value={be}>{be === "" ? "기본 백엔드" : be}</option>
            {/each}
          </select>
        </label>
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600">업무 내용 (자연어)</span>
          <textarea class="w-full resize-y rounded border border-slate-300 px-2 py-1.5 text-sm leading-relaxed" rows="10" bind:value={editing.instructions} placeholder={"이 업무에서 무엇을 어떻게 하는지 평소 말로 설명하듯 적어 주세요.\n\n예)\n- main 최신화 후 빌드·테스트를 돌린다\n- 실패하면 원인을 요약해서 알려준다\n- 통과하면 결과물을 \\\\share\\rel 폴더에 복사하고 팀에 메일로 알린다"}></textarea>
          <span class="mt-1 block text-[11px] text-slate-400">AI가 이 내용을 하나의 '스킬'처럼 이해해 스스로 단계를 나눠 실행합니다.</span>
        </label>
      </div>
      <div class="flex justify-end gap-2 border-t border-slate-200 px-4 py-3">
        <button class="rounded border border-slate-300 px-3 py-1.5 text-sm hover:bg-slate-100" onclick={() => (editing = null)}>취소</button>
        <button class="rounded bg-blue-600 px-4 py-1.5 text-sm font-semibold text-white hover:bg-blue-700 disabled:opacity-50" disabled={!editing.name.trim()} onclick={saveEditor}>저장</button>
      </div>
    </div>
  </div>
{/if}

<!-- Small prompt / confirm / menu modal -->
{#if ask}
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/40 p-4" onclick={(e) => { if (e.target === e.currentTarget) resolveAsk(ask.kind === "confirm" ? false : ask.kind === "menu" ? null : ""); }}>
    <div class="w-[360px] max-w-full rounded-lg bg-white p-4 shadow-xl">
      <div class="mb-3 text-sm font-semibold text-slate-900">{ask.title}</div>
      {#if ask.kind === "text"}
        {#if ask.label}<div class="mb-1 text-xs text-slate-600">{ask.label}</div>{/if}
        <!-- svelte-ignore a11y_autofocus -->
        <input class="w-full rounded border border-slate-300 px-2 py-1.5 text-sm" bind:value={ask.value} autofocus
          onkeydown={(e) => { if (e.key === "Enter") resolveAsk(ask.value); if (e.key === "Escape") resolveAsk(""); }} />
        <div class="mt-3 flex justify-end gap-2">
          <button class="rounded border border-slate-300 px-3 py-1.5 text-sm hover:bg-slate-100" onclick={() => resolveAsk("")}>취소</button>
          <button class="rounded bg-blue-600 px-3 py-1.5 text-sm font-semibold text-white hover:bg-blue-700" onclick={() => resolveAsk(ask.value)}>확인</button>
        </div>
      {:else if ask.kind === "confirm"}
        <div class="mb-3 text-sm text-slate-700">{ask.message}</div>
        <div class="flex justify-end gap-2">
          <button class="rounded border border-slate-300 px-3 py-1.5 text-sm hover:bg-slate-100" onclick={() => resolveAsk(false)}>취소</button>
          <button class={`rounded px-3 py-1.5 text-sm font-semibold text-white ${ask.danger ? "bg-rose-600 hover:bg-rose-700" : "bg-blue-600 hover:bg-blue-700"}`} onclick={() => resolveAsk(true)}>{ask.danger ? "삭제" : "확인"}</button>
        </div>
      {:else if ask.kind === "menu"}
        <div class="space-y-1.5">
          {#each ask.options as opt}
            <button class={`w-full rounded border px-3 py-2 text-left text-sm ${opt.danger ? "border-rose-200 text-rose-700 hover:bg-rose-50" : "border-slate-200 text-slate-700 hover:bg-slate-100"}`} onclick={() => resolveAsk(opt.value)}>{opt.label}</button>
          {/each}
          <button class="mt-1 w-full rounded border border-slate-300 px-3 py-1.5 text-sm hover:bg-slate-100" onclick={() => resolveAsk(null)}>취소</button>
        </div>
      {/if}
    </div>
  </div>
{/if}
