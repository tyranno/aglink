<script>
  // 사용자 정의 MCP 서버 사이드바 패널. config.yaml의 `mcp_servers:` 목록을
  // 통째로 편집한다. 호스트 쪽 저장 API(save_mcp_servers)는 개별 항목 단위가
  // 아니라 "이게 새 전체 목록이다" 방식이라, 추가/수정/삭제/사용여부 토글이
  // 전부 같은 경로를 탄다 — 목록을 복제해 고치고 persist()로 통째로 보낸 뒤
  // 다시 읽어온다. 검증(이름 중복, 내장 이름 충돌, 환경변수 키 등)은 호스트가
  // 하고, 실패하면 반환된 error 문자열을 그대로 보여준다.
  import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";

  let servers = $state([]);
  let reserved = $state([]);
  let loadError = $state("");
  let busy = $state(false); // 목록 단위 변경(토글/삭제) 진행 중

  let ask = $state(null); // { title, message, danger, confirmLabel, resolve }

  // 편집 모달 상태. editing === null → 닫힘.
  // index === -1 → 새 항목, 그 외에는 servers[index]를 교체한다.
  let editing = $state(null);
  let editorError = $state("");
  let saving = $state(false); // 저장 중 — 중복 제출 방지

  async function load() {
    try {
      const raw = await ControlService.ListMCPServers();
      const data = JSON.parse(raw || "{}");
      servers = Array.isArray(data?.servers) ? data.servers : [];
      reserved = Array.isArray(data?.reserved) ? data.reserved : [];
      loadError = "";
    } catch (e) {
      loadError = "MCP 서버 목록을 불러오지 못했습니다.";
    }
  }
  $effect(() => {
    load();
  });

  function askConfirm(title, message, danger, confirmLabel) {
    return new Promise((resolve) => { ask = { title, message, danger: !!danger, confirmLabel: confirmLabel || "확인", resolve }; });
  }
  function resolveAsk(val) { const a = ask; ask = null; if (a) a.resolve(val); }

  // 모든 변경의 공통 경로: 새 전체 목록을 저장하고 다시 읽어온다.
  // 저장이 거부돼도 load()로 서버가 가진 실제 목록으로 되돌려, 화면이 저장되지
  // 않은 상태를 참인 것처럼 보여주지 않게 한다.
  async function persist(newList) {
    let ok = true, err = "";
    try {
      const raw = await ControlService.SaveMCPServers(JSON.stringify({ servers: newList }));
      const j = JSON.parse(raw || "{}");
      if (j && j.ok === false) { ok = false; err = j.error || "요청 실패"; }
    } catch (e) {
      ok = false;
      err = "연결이 끊겨 처리하지 못했습니다. 잠시 후 다시 시도해 주세요.";
    }
    await load();
    return { ok, err };
  }

  // 목록 화면에서 일어나는 변경 — 실패 메시지는 목록 상단에 띄운다.
  async function mutateList(newList) {
    if (busy) return;
    busy = true;
    loadError = "";
    const { ok, err } = await persist(newList);
    busy = false;
    if (!ok) loadError = err;
  }

  function toggleEnabled(idx) {
    const next = servers.map((s, i) => (i === idx ? { ...s, enabled: !s.enabled } : { ...s }));
    return mutateList(next);
  }
  async function del(idx) {
    const s = servers[idx];
    const ok = await askConfirm("MCP 서버 삭제", `"${s.name}" MCP 서버를 목록에서 삭제할까요? 이 작업은 되돌릴 수 없습니다.`, true, "삭제하기");
    if (ok) await mutateList(servers.filter((_, i) => i !== idx));
  }

  function enabledBadge(enabled) {
    return enabled
      ? "bg-emerald-100 dark:bg-emerald-900/40 text-emerald-700 dark:text-emerald-300"
      : "bg-slate-200 dark:bg-slate-700 text-slate-600 dark:text-slate-400";
  }

  // --- 텍스트 영역 <-> 배열/맵 변환 ---
  // 인자와 환경변수는 공백/쉼표를 포함할 수 있어서 한 줄에 하나씩 적는다.
  function argsToText(args) {
    return Array.isArray(args) ? args.join("\n") : "";
  }
  function textToArgs(text) {
    return String(text || "").split("\n").map((l) => l.trim()).filter((l) => l !== "");
  }
  function envToText(env) {
    if (!env || typeof env !== "object") return "";
    return Object.keys(env).map((k) => `${k}=${env[k]}`).join("\n");
  }
  // KEY=VALUE 한 줄씩. '=' 없는 줄은 형식을 알려줘야 하므로 여기서 걸러낸다
  // (키 문자 검증 등 나머지는 호스트가 한다).
  function textToEnv(text) {
    const out = {};
    for (const line of String(text || "").split("\n")) {
      const l = line.trim();
      if (l === "") continue;
      const eq = l.indexOf("=");
      if (eq <= 0) return { err: `환경변수는 KEY=VALUE 형식으로 한 줄에 하나씩 입력해 주세요: "${l}"` };
      out[l.slice(0, eq).trim()] = l.slice(eq + 1).trim();
    }
    return { env: out };
  }

  // --- 편집기 ---
  function openEditor(idx) {
    if (idx >= 0) {
      const s = servers[idx];
      editing = {
        index: idx,
        name: s.name || "",
        enabled: !!s.enabled,
        command: s.command || "",
        argsText: argsToText(s.args),
        envText: envToText(s.env),
        systemPrompt: s.system_prompt || "",
      };
    } else {
      editing = { index: -1, name: "", enabled: true, command: "", argsText: "", envText: "", systemPrompt: "" };
    }
    editorError = "";
    saving = false;
  }

  async function saveEditor() {
    if (saving || !editing) return;
    const name = editing.name.trim();
    const command = editing.command.trim();
    if (!name || !command) return;
    const parsed = textToEnv(editing.envText);
    if (parsed.err) { editorError = parsed.err; return; }

    const entry = { name, enabled: !!editing.enabled, command };
    const args = textToArgs(editing.argsText);
    if (args.length) entry.args = args;
    if (Object.keys(parsed.env).length) entry.env = parsed.env;
    const sys = editing.systemPrompt.trim();
    if (sys) entry.system_prompt = sys;

    // 편집을 연 뒤 목록이 짧아졌다면(다른 클라이언트가 지웠거나 config.yaml이
    // 직접 편집된 경우) 아래 map은 아무것도 갈아끼우지 못한 채 "저장 성공"으로
    // 끝나 입력이 조용히 사라진다. 그 전에 멈추고 다시 시도하게 한다.
    if (editing.index >= servers.length) {
      editorError = "목록이 바뀌었습니다. 창을 닫고 새로고침한 뒤 다시 시도해 주세요.";
      return;
    }
    const next = editing.index >= 0
      ? servers.map((s, i) => (i === editing.index ? entry : { ...s }))
      : [...servers.map((s) => ({ ...s })), entry];

    saving = true;
    editorError = "";
    // 저장에 실패하면 편집기를 열어둔 채로 둔다 — 이름 충돌 같은 거부 사유를
    // 고쳐 바로 다시 저장할 수 있어야 하고, 입력이 사라지면 안 된다.
    const { ok, err } = await persist(next);
    saving = false;
    if (ok) editing = null;
    else editorError = err || "저장에 실패했습니다. 연결 상태를 확인하고 다시 시도해 주세요.";
  }
</script>

<div class="flex h-full min-h-0 flex-col">
  <div class="flex h-11 shrink-0 items-center gap-2 border-b border-slate-200 dark:border-slate-700 px-3">
    <div class="min-w-0 flex-1 truncate text-sm font-semibold text-slate-900 dark:text-slate-100">사용자 정의 MCP 서버</div>
    <button class="grid h-8 w-8 place-items-center rounded-md bg-blue-600 text-base font-semibold text-white hover:bg-blue-700" onclick={() => openEditor(-1)} title="새 MCP 서버" aria-label="새 MCP 서버">＋</button>
    <button class="grid h-8 w-8 place-items-center rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={load} title="새로고침" aria-label="새로고침">↻</button>
  </div>

  <div class="min-h-0 flex-1 overflow-y-auto px-3 py-3">
    {#if loadError}
      <div class="mb-2 rounded bg-rose-50 dark:bg-rose-950/40 px-2 py-1 text-xs text-rose-700 dark:text-rose-300">{loadError}</div>
    {/if}
    {#if servers.length === 0}
      <div class="px-1 py-3 text-sm text-slate-500 dark:text-slate-400">
        등록된 사용자 정의 MCP 서버가 없습니다. ＋로 추가하세요.
        <div class="mt-1 text-xs text-slate-400 dark:text-slate-500">
          여기서 추가한 서버는 <code class="rounded bg-slate-100 dark:bg-slate-800 px-1">config.yaml</code>에 저장되고, 다음 대화부터 AI가 사용할 수 있습니다.
        </div>
      </div>
    {/if}
    <div class="space-y-2">
      {#each servers as s, i (s.name + ":" + i)}
        <div class="rounded-lg border border-slate-200 dark:border-slate-700 bg-white/85 dark:bg-slate-900/85 p-3 shadow-sm">
          <div class="flex items-start gap-2">
            <button class="min-w-0 flex-1 text-left" onclick={() => openEditor(i)} title="편집">
              <div class="truncate font-mono text-sm font-semibold text-slate-900 dark:text-slate-100" title={s.name}>{s.name}</div>
              <div class="mt-1 truncate text-xs leading-5 text-slate-500 dark:text-slate-400" title={s.command}>{s.command}</div>
            </button>
            <span class={`shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold ${enabledBadge(s.enabled)}`}>{s.enabled ? "사용중" : "중지됨"}</span>
          </div>
          <div class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-slate-500 dark:text-slate-400">
            {#if s.args && s.args.length}
              <span title={s.args.join(" ")}>인자 {s.args.length}개</span>
            {/if}
            {#if s.env && Object.keys(s.env).length}
              <span title={Object.keys(s.env).join(", ")}>환경변수 {Object.keys(s.env).length}개</span>
            {/if}
            {#if s.system_prompt}
              <span title={s.system_prompt}>[안내 문구]</span>
            {/if}
          </div>
          <div class="mt-2 flex justify-end gap-1.5">
            <button class="rounded border border-slate-300 dark:border-slate-600 px-2 py-1 text-xs text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700 disabled:opacity-50" disabled={busy} onclick={() => openEditor(i)}>✏️ 편집</button>
            {#if s.enabled}
              <button class="rounded border border-slate-300 dark:border-slate-600 px-2 py-1 text-xs text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700 disabled:opacity-50" disabled={busy} onclick={() => toggleEnabled(i)}>⏸ 사용 중지</button>
            {:else}
              <button class="rounded border border-emerald-300 dark:border-emerald-700 px-2 py-1 text-xs text-emerald-700 dark:text-emerald-300 hover:bg-emerald-50 disabled:opacity-50" disabled={busy} onclick={() => toggleEnabled(i)}>▶ 사용</button>
            {/if}
            <button class="rounded border border-rose-300 dark:border-rose-700 px-2 py-1 text-xs font-semibold text-rose-700 dark:text-rose-300 hover:bg-rose-50 dark:hover:bg-rose-950/40 disabled:opacity-50" disabled={busy} onclick={() => del(i)}>🗑 삭제</button>
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
      <div class="border-b border-slate-200 dark:border-slate-700 px-4 py-3 text-sm font-semibold text-slate-900 dark:text-slate-100">{editing.index >= 0 ? "MCP 서버 편집" : "새 MCP 서버"}</div>
      <div class="flex-1 space-y-3 overflow-y-auto px-4 py-3 text-left">
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600 dark:text-slate-400">이름</span>
          <input class="w-full rounded border border-slate-300 dark:border-slate-600 px-2 py-1.5 font-mono text-sm" bind:value={editing.name} placeholder="예: filesystem" />
          <span class="mt-1 block text-[11px] text-slate-400 dark:text-slate-500">
            영문/숫자/<code>_</code>/<code>-</code>만 쓸 수 있습니다.
            {#if reserved.length}
              {reserved.join(", ")}은(는) 내장 서버 이름이라 사용할 수 없습니다.
            {/if}
          </span>
        </label>
        <label class="flex items-center gap-2">
          <input type="checkbox" class="h-4 w-4 rounded border-slate-300 dark:border-slate-600" bind:checked={editing.enabled} />
          <span class="text-xs font-medium text-slate-600 dark:text-slate-400">사용 여부 (끄면 대화에 이 MCP가 로드되지 않습니다)</span>
        </label>
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600 dark:text-slate-400">실행 명령</span>
          <input class="w-full rounded border border-slate-300 dark:border-slate-600 px-2 py-1.5 font-mono text-sm" bind:value={editing.command} placeholder="예: npx" />
        </label>
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600 dark:text-slate-400">인자 (한 줄에 하나)</span>
          <textarea class="w-full resize-y rounded border border-slate-300 dark:border-slate-600 px-2 py-1.5 font-mono text-sm" rows="3" bind:value={editing.argsText} placeholder={"-y\n@modelcontextprotocol/server-filesystem\nC:\\work"}></textarea>
          <span class="mt-1 block text-[11px] text-slate-400 dark:text-slate-500">공백이나 쉼표가 들어간 인자도 한 줄에 그대로 적으면 됩니다.</span>
        </label>
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600 dark:text-slate-400">환경변수 (한 줄에 KEY=VALUE 하나)</span>
          <textarea class="w-full resize-y rounded border border-slate-300 dark:border-slate-600 px-2 py-1.5 font-mono text-sm" rows="3" bind:value={editing.envText} placeholder={"API_KEY=xxxx\nBASE_URL=https://example.com"}></textarea>
        </label>
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-slate-600 dark:text-slate-400">안내 문구 (선택)</span>
          <textarea class="w-full resize-y rounded border border-slate-300 dark:border-slate-600 px-2 py-1.5 text-sm" rows="3" bind:value={editing.systemPrompt} placeholder="예: 사내 위키를 찾을 때는 이 MCP의 search 도구를 먼저 사용하세요."></textarea>
          <span class="mt-1 block text-[11px] text-slate-400 dark:text-slate-500">이 MCP를 언제/어떻게 써야 하는지 AI에게 알려주는 설명입니다.</span>
        </label>
      </div>
      {#if editorError}
        <div class="border-t border-rose-100 dark:border-rose-800 bg-rose-50 dark:bg-rose-950/40 px-4 py-2 text-xs text-rose-700 dark:text-rose-300">{editorError}</div>
      {/if}
      <div class="flex justify-end gap-2 border-t border-slate-200 dark:border-slate-700 px-4 py-3">
        <button class="rounded border border-slate-300 dark:border-slate-600 px-3 py-1.5 text-sm hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => (editing = null)}>취소</button>
        <button class="rounded bg-blue-600 px-4 py-1.5 text-sm font-semibold text-white hover:bg-blue-700 disabled:opacity-50" disabled={!editing.name.trim() || !editing.command.trim() || saving} onclick={saveEditor}>{saving ? "저장 중…" : "저장"}</button>
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
