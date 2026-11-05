<script>
  import { renderMarkdown } from "./markdown.js";

  let {
    open = false,
    name = "",
    path = "",
    content = "",
    error = "",
    isMarkdown = false,
    onClose = () => {},
  } = $props();

  let body = $state(null);

  // Re-render the content into a DOM node whenever the shown file changes. Only
  // ever uses renderMarkdown (textContent-only) or a textContent <pre>, so a
  // file that contains HTML/script can never inject into the page.
  $effect(() => {
    if (!body) return;
    void open;
    void content;
    void isMarkdown;
    void error;
    body.replaceChildren();
    if (error) return;
    if (isMarkdown) {
      body.appendChild(renderMarkdown(content, document));
    } else {
      const pre = document.createElement("pre");
      pre.className = "preview-pre";
      pre.textContent = content;
      body.appendChild(pre);
    }
  });
</script>

{#if open}
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-40 bg-black/25" onclick={onClose}></div>
  <aside
    class="fixed right-0 top-0 bottom-0 z-50 flex w-[min(560px,92vw)] flex-col border-l border-slate-200 bg-white text-slate-900 shadow-2xl dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
  >
    <header class="flex flex-shrink-0 items-center gap-2 border-b border-slate-200 px-3 py-2 dark:border-slate-700">
      <span class="text-xs font-semibold uppercase tracking-wide text-slate-400 dark:text-slate-500">미리보기</span>
      <span class="min-w-0 flex-1 truncate text-sm font-semibold" title={path || name}>{name || "미리보기"}</span>
      <button
        class="rounded-md px-2 py-1 text-sm text-slate-500 hover:bg-slate-100 hover:text-slate-800 dark:text-slate-400 dark:hover:bg-slate-700 dark:hover:text-slate-100"
        onclick={onClose}
        title="닫기 (Esc)"
        aria-label="닫기"
      >✕</button>
    </header>
    {#if error}
      <div class="px-4 py-4 text-sm text-rose-600 dark:text-rose-300">{error}</div>
    {:else}
      <div class="markdown-body flex-1 overflow-auto px-4 py-3 text-[13px] leading-5" bind:this={body}></div>
    {/if}
  </aside>
{/if}

<style>
  .preview-pre {
    white-space: pre-wrap;
    word-break: break-word;
    font-family: ui-monospace, "D2Coding", Consolas, monospace;
    font-size: 12px;
    margin: 0;
  }

  /* File paths inside chat messages that open this panel. Defined global so the
     spans markdown.js injects into message bubbles (class "md-file-link") pick
     it up wherever they render. */
  :global(.md-file-link) {
    color: #2563eb;
    text-decoration: underline;
    text-decoration-style: dotted;
    text-underline-offset: 2px;
    cursor: pointer;
    font-family: ui-monospace, "D2Coding", Consolas, monospace;
    word-break: break-all;
  }
  :global(.md-file-link:hover) {
    text-decoration-style: solid;
  }
  :global(.dark .md-file-link) {
    color: #7dd3fc;
  }
</style>
