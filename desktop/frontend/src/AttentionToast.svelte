<script>
  // A persistent "확인 필요" toast for the management channel (연구소 업무). It is
  // driven purely by that channel's unread state, so it needs no message parsing
  // and no paneStore changes: when the daily digest (or anything) lands in the
  // channel while it is not being viewed, the channel goes unread and this shows;
  // opening the channel clears the unread (selectTargetInPane → setUnread false)
  // and this disappears. It never auto-dismisses — the whole point is that it
  // nags until the user looks, so a busy morning doesn't bury the briefing.
  import { chat, findWebConv, selectTargetFromSidebar } from "./paneStore.svelte.js";

  // Which conversation is the management channel: an explicit id in localStorage
  // (aglink.mgmtChannelId) wins; otherwise the local conversation whose title
  // contains "연구소 업무". Resolving by title keeps it working across machines
  // where the numeric id differs.
  const MGMT_TITLE = "연구소 업무";

  let conv = $derived.by(() => {
    let id = null;
    try { id = localStorage.getItem("aglink.mgmtChannelId"); } catch {}
    const byId = id ? findWebConv(id) : null;
    return byId || (chat.webConvs || []).find((c) => String(c.title || "").includes(MGMT_TITLE)) || null;
  });

  let show = $derived(!!conv && chat.unread.has("web:" + conv.id));

  function openChannel() {
    if (!conv) return;
    selectTargetFromSidebar({ kind: "web", id: conv.id });
  }
</script>

{#if show}
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <button
    class="attention-toast group fixed bottom-4 right-4 z-[60] flex max-w-sm items-start gap-3 rounded-xl border border-amber-300 bg-amber-50 px-4 py-3 text-left shadow-2xl ring-1 ring-amber-200 hover:bg-amber-100 dark:border-amber-500/40 dark:bg-amber-950/80 dark:ring-amber-500/20 dark:hover:bg-amber-900/70"
    onclick={openChannel}
    title="클릭하면 채널을 열고 확인 처리됩니다"
  >
    <span class="attention-bell mt-0.5 text-lg leading-none">🔔</span>
    <span class="min-w-0">
      <span class="block text-sm font-bold text-amber-900 dark:text-amber-200">{conv?.title || "연구소 업무"} — 확인 필요</span>
      <span class="mt-0.5 block text-xs text-amber-800/90 dark:text-amber-200/80">오늘의 확인 사항이 있습니다. 눌러서 열어 확인하세요.</span>
    </span>
    <span class="ml-1 mt-0.5 shrink-0 rounded-md px-2 py-1 text-xs font-semibold text-amber-700 group-hover:bg-amber-200/70 dark:text-amber-300 dark:group-hover:bg-amber-800/50">열기 →</span>
  </button>
{/if}

<style>
  .attention-toast {
    animation: attention-pop 0.25s ease-out;
  }
  .attention-bell {
    display: inline-block;
    animation: attention-wiggle 1.6s ease-in-out infinite;
    transform-origin: 50% 0;
  }
  @keyframes attention-pop {
    from { opacity: 0; transform: translateY(10px); }
  }
  @keyframes attention-wiggle {
    0%, 70%, 100% { transform: rotate(0deg); }
    75% { transform: rotate(14deg); }
    80% { transform: rotate(-12deg); }
    85% { transform: rotate(9deg); }
    90% { transform: rotate(-6deg); }
  }
  @media (prefers-reduced-motion: reduce) {
    .attention-toast, .attention-bell { animation: none; }
  }
</style>
