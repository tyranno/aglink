<script>
  // The change card under an answer: which files the turn changed in its
  // working folder (host checkpoint.go), each file's diff, and "이 답변 전으로
  // 되돌리기" — which snapshots the current state first so it can be undone.
  import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";
  import { showToast } from "./paneStore.svelte.js";

  let { changes } = $props();

  let open = $state(false);
  let confirming = $state(false);
  let busy = $state(false);
  let undo = $state(""); // set after a restore: snapshot to go back to
  let error = $state("");
  let diff = $state(null); // { title, text, truncated, loading }

  const files = $derived(changes?.files || []);
  const adds = $derived(files.reduce((n, f) => n + Math.max(0, f.add || 0), 0));
  const dels = $derived(files.reduce((n, f) => n + Math.max(0, f.del || 0), 0));

  const STATUS = {
    A: { label: "추가", cls: "bg-emerald-100 text-emerald-700 dark:bg-emerald-900/50 dark:text-emerald-300" },
    M: { label: "수정", cls: "bg-blue-100 text-blue-700 dark:bg-blue-900/50 dark:text-blue-300" },
    D: { label: "삭제", cls: "bg-rose-100 text-rose-700 dark:bg-rose-900/50 dark:text-rose-300" },
  };
  const statusOf = (s) => STATUS[s] || { label: s || "?", cls: "bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-300" };

  function parse(raw) {
    try {
      return JSON.parse(raw || "{}");
    } catch {
      return {};
    }
  }

  async function showDiff(path) {
    diff = { title: path || `파일 ${files.length}개 전체`, text: "", truncated: false, loading: true };
    try {
      const data = parse(await ControlService.CheckpointDiff(changes.workDir, changes.before, changes.after, path || ""));
      if (data.error) throw new Error(data.error);
      diff = { ...diff, text: data.diff || "(내용 차이 없음 — 바이너리 파일일 수 있습니다)", truncated: !!data.truncated, loading: false };
    } catch (e) {
      diff = { ...diff, text: `변경 내용을 불러오지 못했습니다: ${e?.message || e}`, loading: false };
    }
  }

  async function restore(to, label) {
    busy = true;
    error = "";
    try {
      const data = parse(await ControlService.CheckpointRestore(changes.workDir, to));
      if (!data.ok) throw new Error(data.error || "알 수 없는 오류");
      undo = to === changes.before ? data.undo || "" : "";
      confirming = false;
      showToast(`${label} (파일 ${data.changed ?? 0}개)`);
    } catch (e) {
      error = `${e?.message || e}`;
    } finally {
      busy = false;
    }
  }

  function lineClass(line) {
    if (line.startsWith("+++") || line.startsWith("---")) return "text-slate-500 dark:text-slate-400";
    if (line.startsWith("+")) return "bg-emerald-50 text-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-300";
    if (line.startsWith("-")) return "bg-rose-50 text-rose-800 dark:bg-rose-950/40 dark:text-rose-300";
    if (line.startsWith("@@")) return "text-violet-600 dark:text-violet-400";
    if (line.startsWith("diff --git")) return "mt-2 border-t border-slate-200 pt-1 font-semibold text-slate-700 dark:border-slate-700 dark:text-slate-200";
    return "text-slate-600 dark:text-slate-400";
  }

  function onKey(event) {
    if (diff && event.key === "Escape") {
      event.stopPropagation();
      diff = null;
    }
  }
</script>

<svelte:window onkeydown={onKey} />

{#if files.length}
  <div class="mt-1 w-full max-w-[92%] rounded-lg border border-slate-200 bg-slate-50/80 text-[12px] dark:border-slate-700 dark:bg-slate-900/60">
    <div class="flex items-center gap-2 px-2.5 py-1.5">
      <button
        type="button"
        class="flex min-w-0 items-center gap-1.5 font-medium text-slate-700 hover:text-blue-600 dark:text-slate-300 dark:hover:text-blue-400"
        title={changes.workDir}
        onclick={() => (open = !open)}
      >
        <svg class={`h-3 w-3 shrink-0 transition-transform ${open ? "rotate-90" : ""}`} viewBox="0 0 16 16" fill="currentColor"><path d="M6 3l5 5-5 5z" /></svg>
        <svg class="h-3.5 w-3.5 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z" /><path d="M14 3v6h6" /></svg>
        <span class="truncate">파일 {files.length}{changes.truncated ? "+" : ""}개 변경</span>
        {#if adds}<span class="tabular-nums text-emerald-600 dark:text-emerald-400">+{adds}</span>{/if}
        {#if dels}<span class="tabular-nums text-rose-600 dark:text-rose-400">−{dels}</span>{/if}
      </button>
      <span class="flex-1"></span>
      {#if undo}
        <span class="text-amber-600 dark:text-amber-400">되돌렸음</span>
        <button type="button" disabled={busy} class="rounded px-1.5 py-0.5 font-semibold text-blue-600 hover:bg-blue-50 disabled:opacity-50 dark:text-blue-400 dark:hover:bg-blue-950/40" onclick={() => restore(undo, "되돌리기를 취소했습니다").then(() => (undo = ""))}>되돌리기 취소</button>
      {:else}
        <button type="button" class="rounded px-1.5 py-0.5 text-slate-600 hover:bg-slate-200/70 dark:text-slate-300 dark:hover:bg-slate-800" onclick={() => showDiff("")}>변경 내용</button>
        <button type="button" class="rounded px-1.5 py-0.5 text-slate-600 hover:bg-rose-100 hover:text-rose-700 dark:text-slate-300 dark:hover:bg-rose-950/40 dark:hover:text-rose-300" onclick={() => (confirming = !confirming)}>이 답변 전으로</button>
      {/if}
    </div>

    {#if confirming && !undo}
      <div class="mx-2.5 mb-2 rounded-md border border-amber-200 bg-amber-50 px-2.5 py-2 text-amber-900 dark:border-amber-800 dark:bg-amber-950/40 dark:text-amber-200">
        <p>작업 폴더의 파일을 이 답변이 시작되기 전 상태로 되돌립니다. 이 답변 뒤에 바뀐 내용도 함께 되돌아갑니다.</p>
        <p class="mt-0.5 text-[11px] opacity-80">지금 상태는 따로 저장되므로 "되돌리기 취소"로 다시 돌아올 수 있습니다. 대화 내용은 그대로입니다.</p>
        {#if error}<p class="mt-1 text-rose-600 dark:text-rose-400">되돌리지 못했습니다: {error}</p>{/if}
        <div class="mt-1.5 flex justify-end gap-1.5">
          <button type="button" class="rounded px-2 py-0.5 hover:bg-amber-100 dark:hover:bg-amber-900/40" onclick={() => (confirming = false)}>취소</button>
          <button type="button" disabled={busy} class="rounded bg-rose-600 px-2 py-0.5 font-semibold text-white hover:bg-rose-700 disabled:opacity-50" onclick={() => restore(changes.before, "이 답변 전 상태로 되돌렸습니다")}>{busy ? "되돌리는 중…" : "되돌리기"}</button>
        </div>
      </div>
    {/if}

    {#if open}
      <ul class="max-h-64 overflow-y-auto border-t border-slate-200 py-1 dark:border-slate-700">
        {#each files as file (file.path)}
          {@const st = statusOf(file.status)}
          <li>
            <button type="button" class="flex w-full items-center gap-2 px-2.5 py-0.5 text-left hover:bg-slate-200/60 dark:hover:bg-slate-800" title="이 파일의 변경 내용 보기" onclick={() => showDiff(file.path)}>
              <span class={`shrink-0 rounded px-1 text-[10px] font-semibold ${st.cls}`}>{st.label}</span>
              <span class="min-w-0 flex-1 truncate font-mono text-[11.5px] text-slate-700 dark:text-slate-300">{file.path}</span>
              {#if file.add < 0}
                <span class="shrink-0 text-[10px] text-slate-400">바이너리</span>
              {:else}
                <span class="shrink-0 tabular-nums text-[10.5px]"><span class="text-emerald-600 dark:text-emerald-400">+{file.add}</span> <span class="text-rose-600 dark:text-rose-400">−{file.del}</span></span>
              {/if}
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
{/if}

{#if diff}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/40 p-6 backdrop-blur-[1px]" onclick={() => (diff = null)}>
    <div class="flex max-h-full w-full max-w-4xl flex-col overflow-hidden rounded-xl bg-white shadow-2xl ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700" onclick={(e) => e.stopPropagation()}>
      <div class="flex items-center gap-2 border-b border-slate-200 px-4 py-2 dark:border-slate-700">
        <span class="min-w-0 flex-1 truncate font-mono text-[13px] font-semibold text-slate-800 dark:text-slate-200" title={diff.title}>{diff.title}</span>
        <button type="button" class="rounded px-2 py-0.5 text-[12px] text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800" onclick={() => navigator.clipboard?.writeText(diff.text).then(() => showToast("변경 내용을 복사했습니다"))}>복사</button>
        <button type="button" class="rounded px-2 py-0.5 text-[15px] leading-none text-slate-500 hover:bg-slate-100 dark:hover:bg-slate-800" title="닫기 (Esc)" onclick={() => (diff = null)}>✕</button>
      </div>
      <div class="min-h-0 flex-1 overflow-auto px-4 py-2">
        {#if diff.loading}
          <p class="text-[12px] text-slate-500">불러오는 중…</p>
        {:else}
          <pre class="font-mono text-[11.5px] leading-[1.45]">{#each diff.text.split("\n") as line, i (i)}<div class={`whitespace-pre-wrap break-all px-1 ${lineClass(line)}`}>{line || " "}</div>{/each}</pre>
          {#if diff.truncated}<p class="mt-2 text-[11px] text-amber-600">내용이 길어 앞부분만 보여 줍니다.</p>{/if}
        {/if}
      </div>
    </div>
  </div>
{/if}
