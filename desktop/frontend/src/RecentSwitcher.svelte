<script>
  // Ctrl+Tab: recent conversations, like switching windows. Holding Ctrl, each
  // Tab moves down (Shift+Tab up); letting go of Ctrl opens the highlighted
  // one. A click opens it too; Esc cancels. The list is chat.recentTargets
  // (App records each newly focused conversation).
  import { chat, targetKey, paneConversationMeta, selectTargetFromSidebar, backendLabel } from "./paneStore.svelte.js";

  let open = $state(false);
  let cursor = $state(1);

  // Only conversations that still exist.
  function items() {
    return chat.recentTargets.filter((t) => t.kind === "telegram" ? !!chat.telegram : chat.webConvs.some((c) => String(c.id) === String(t.id)));
  }

  function choose(target) {
    open = false;
    if (target) selectTargetFromSidebar(target);
  }

  function handleKeydown(event) {
    if (event.key === "Tab" && event.ctrlKey && !event.altKey && !event.metaKey) {
      const list = items();
      if (list.length < 2 && !open) return;
      event.preventDefault();
      const n = list.length;
      if (!open) {
        open = true;
        cursor = event.shiftKey ? n - 1 : 1;
      } else if (n) {
        cursor = (cursor + (event.shiftKey ? -1 : 1) + n) % n;
      }
      return;
    }
    if (open && event.key === "Escape") {
      event.preventDefault();
      open = false;
    }
  }

  function handleKeyup(event) {
    if (open && (event.key === "Control" || !event.ctrlKey)) choose(items()[cursor]);
  }
</script>

<svelte:window onkeydown={handleKeydown} onkeyup={handleKeyup} onblur={() => (open = false)} />

{#if open}
  <div class="fixed inset-0 z-[60] flex items-center justify-center bg-slate-950/20 px-4">
    <div class="w-full max-w-sm overflow-hidden rounded-xl border border-slate-200 bg-white p-1.5 shadow-2xl dark:border-slate-700 dark:bg-slate-900" role="listbox" aria-label="최근 대화">
      <div class="px-2.5 pb-1 pt-1 text-[10.5px] font-bold tracking-[0.04em] text-slate-400 dark:text-slate-500">최근 대화 · Ctrl을 떼면 열기</div>
      {#each items() as target, index (targetKey(target))}
        {@const meta = paneConversationMeta(target)}
        <button
          type="button"
          class={`flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left ${index === cursor ? "bg-blue-50 dark:bg-blue-950/50" : "hover:bg-slate-50 dark:hover:bg-slate-800"}`}
          role="option"
          aria-selected={index === cursor}
          onmouseenter={() => (cursor = index)}
          onclick={() => choose(target)}
        >
          <span class={`h-2 w-2 shrink-0 rounded-full ${chat.working.has(targetKey(target)) ? "animate-pulse bg-blue-500" : target.kind === "telegram" ? "bg-sky-500" : "bg-slate-300 dark:bg-slate-600"}`}></span>
          <span class="min-w-0 flex-1 truncate text-sm font-medium text-slate-900 dark:text-slate-100">{meta?.title || "대화"}</span>
          {#if index === 0}<span class="shrink-0 text-[10px] text-slate-400">지금</span>{/if}
          {#if chat.unread.has(targetKey(target))}<span class="shrink-0 rounded-full bg-blue-600 px-1.5 py-0.5 text-[10px] font-bold text-white">NEW</span>{/if}
          <span class="shrink-0 rounded-full bg-slate-100 px-1.5 py-0.5 text-[10px] font-bold text-slate-600 dark:bg-slate-800 dark:text-slate-400">{backendLabel(meta?.backend)}</span>
        </button>
      {/each}
    </div>
  </div>
{/if}
