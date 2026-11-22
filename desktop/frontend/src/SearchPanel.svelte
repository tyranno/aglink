<script>
  // "모든 대화에서 찾기" (Ctrl+Shift+F) and the 북마크 list, as two tabs of one
  // in-app dialog. Opening a result or bookmark jumps to that message
  // (jumpToMessage) and closes the dialog.
  import { tick } from "svelte";
  import { chat, jumpToMessage, findWebConv } from "./paneStore.svelte.js";
  import { searchPanel, searchAll, bookmarks, removeBookmark } from "./search.svelte.js";

  let query = $state("");
  let hits = $state([]);
  let progress = $state({ done: 0, total: 0, finished: true, failed: 0 });
  let cursor = $state(0);
  let inputEl = $state(null);
  let listEl = $state(null);
  let debounce = 0;

  function runSearch() {
    clearTimeout(debounce);
    debounce = setTimeout(() => {
      cursor = 0;
      void searchAll(query, (done, total, list, finished, failed = 0) => {
        hits = list;
        progress = { done, total, finished, failed };
      });
    }, 250);
  }

  const visible = $derived(searchPanel.tab === "search" ? hits : bookmarks.list);

  function close() {
    searchPanel.open = false;
  }

  function convTitle(item) {
    if (item.target?.kind === "web") return findWebConv(item.target.id)?.title || item.title;
    return chat.telegram?.title || item.title || "텔레그램";
  }

  function whoLabel(role) {
    return role === "user" ? "나" : role === "system" ? "시스템" : "AI";
  }

  function when(at) {
    if (!at) return "";
    const d = new Date(at);
    const sameDay = d.toDateString() === new Date().toDateString();
    return sameDay ? d.toLocaleTimeString("ko-KR", { hour: "numeric", minute: "2-digit" }) : d.toLocaleDateString("ko-KR", { month: "short", day: "numeric" });
  }

  async function open(item) {
    if (!item) return;
    close();
    window.dispatchEvent(new CustomEvent("aglink:show-chat"));
    await tick();
    await jumpToMessage(item.target, { at: item.at, text: item.text, index: item.index });
  }

  async function move(step) {
    const n = visible.length;
    if (!n) return;
    cursor = (cursor + step + n) % n;
    await tick();
    listEl?.querySelector(`[data-index="${cursor}"]`)?.scrollIntoView({ block: "nearest" });
  }

  async function switchTab(tab) {
    searchPanel.tab = tab;
    cursor = 0;
    await tick();
    inputEl?.focus();
  }

  // Each time it opens: focus the box and rerun the last query (histories are
  // cached for a few minutes, so this is cheap).
  $effect(() => {
    if (!searchPanel.open) return;
    cursor = 0;
    tick().then(() => {
      inputEl?.focus();
      inputEl?.select();
      if (searchPanel.tab === "search" && query.trim()) runSearch();
    });
  });

  function handleKeydown(event) {
    if (!searchPanel.open) return;
    if (event.key === "Escape") {
      event.preventDefault();
      close();
    }
  }
</script>

<svelte:window onkeydown={handleKeydown} />

{#if searchPanel.open}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-50 flex justify-center bg-slate-950/30 px-4 pt-[10vh] backdrop-blur-[1px]" onclick={close}>
    <div
      class="flex h-fit max-h-[70vh] w-full max-w-2xl flex-col overflow-hidden rounded-xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900"
      onclick={(event) => event.stopPropagation()}
      role="dialog"
      aria-modal="true"
      aria-label={searchPanel.tab === "search" ? "모든 대화에서 찾기" : "북마크"}
      tabindex="-1"
    >
      <div class="flex items-center gap-1 border-b border-slate-200 px-2 pt-1.5 dark:border-slate-700">
        {#each [["search", "모든 대화에서 찾기"], ["bookmarks", `북마크 ${bookmarks.list.length || ""}`]] as [id, label] (id)}
          <button
            type="button"
            class={`-mb-px border-b-2 px-3 py-1.5 text-[12.5px] font-semibold ${searchPanel.tab === id ? "border-blue-600 text-blue-700 dark:border-blue-400 dark:text-blue-300" : "border-transparent text-slate-500 hover:text-slate-800 dark:text-slate-400 dark:hover:text-slate-200"}`}
            onclick={() => switchTab(id)}
          >{label}</button>
        {/each}
        <button type="button" class="ml-auto rounded px-2 text-slate-400 hover:text-slate-700 dark:hover:text-slate-200" onclick={close} aria-label="닫기">✕</button>
      </div>

      {#if searchPanel.tab === "search"}
        <div class="flex items-center gap-2 border-b border-slate-200 px-3 py-2 dark:border-slate-700">
          <svg class="h-4 w-4 shrink-0 text-slate-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
          <input
            bind:this={inputEl}
            class="min-w-0 flex-1 bg-transparent py-1 text-sm text-slate-900 outline-none placeholder:text-slate-400 dark:text-slate-100"
            placeholder="모든 대화의 메시지 내용에서 찾기 (두 글자 이상)"
            aria-label="모든 대화에서 찾기"
            value={query}
            oninput={(event) => { query = event.currentTarget.value; runSearch(); }}
            onkeydown={(event) => {
              if (event.key === "ArrowDown") { event.preventDefault(); void move(1); }
              else if (event.key === "ArrowUp") { event.preventDefault(); void move(-1); }
              else if (event.key === "Enter" && !event.isComposing) { event.preventDefault(); void open(visible[cursor]); }
            }}
          />
          <span class="shrink-0 text-[11px] tabular-nums text-slate-400 dark:text-slate-500" role="status">
            {#if query.trim().length >= 2}
              {#if !progress.finished}
                대화 {progress.done}/{progress.total} 확인 중 · {hits.length}건
              {:else}
                {hits.length}{hits.length >= 200 ? "+" : ""}건{progress.failed ? ` · ${progress.failed}개 대화 읽기 실패` : ""}
              {/if}
            {/if}
          </span>
        </div>
      {:else}
        <!-- keeps Esc / focus inside the dialog on the bookmarks tab -->
        <input bind:this={inputEl} class="sr-only" aria-hidden="true" tabindex="-1"
          onkeydown={(event) => {
            if (event.key === "ArrowDown") { event.preventDefault(); void move(1); }
            else if (event.key === "ArrowUp") { event.preventDefault(); void move(-1); }
            else if (event.key === "Enter") { event.preventDefault(); void open(visible[cursor]); }
          }} />
      {/if}

      <div bind:this={listEl} class="min-h-0 flex-1 overflow-y-auto p-1.5" role="listbox">
        {#each visible as item, index (searchPanel.tab === "search" ? `${item.key}:${item.index}` : item.id)}
          <div
            data-index={index}
            class={`group flex w-full items-start gap-2 rounded-lg px-2.5 py-2 text-left transition-colors ${index === cursor ? "bg-blue-50 dark:bg-blue-950/50" : "hover:bg-slate-50 dark:hover:bg-slate-800"}`}
            role="option"
            tabindex="-1"
            aria-selected={index === cursor}
            onmouseenter={() => (cursor = index)}
          >
            <button type="button" class="min-w-0 flex-1 text-left" onclick={() => open(item)}>
              <span class="flex items-center gap-1.5 text-[11px] text-slate-400 dark:text-slate-500">
                <span class="truncate font-semibold text-slate-600 dark:text-slate-300">{convTitle(item)}</span>
                <span>· {whoLabel(item.role)}</span>
                {#if item.at}<span class="tabular-nums">· {when(item.at)}</span>{/if}
              </span>
              {#if searchPanel.tab === "search"}
                <span class="mt-0.5 block break-words text-[12.5px] leading-5 text-slate-700 dark:text-slate-300">{item.snippet.before}<mark class="rounded bg-amber-200/80 px-0.5 text-slate-900 dark:bg-amber-500/40 dark:text-amber-50">{item.snippet.match}</mark>{item.snippet.after}</span>
              {:else}
                <span class="mt-0.5 line-clamp-2 block whitespace-pre-wrap break-words text-[12.5px] leading-5 text-slate-700 dark:text-slate-300">{item.text}</span>
              {/if}
            </button>
            {#if searchPanel.tab === "bookmarks"}
              <button
                type="button"
                class="shrink-0 rounded px-1.5 py-0.5 text-[11px] text-slate-400 opacity-0 transition-opacity hover:bg-rose-50 hover:text-rose-600 group-hover:opacity-100 focus:opacity-100 dark:hover:bg-rose-950/40 dark:hover:text-rose-300"
                onclick={() => removeBookmark(item.id)}
                title="북마크 지우기"
                aria-label="북마크 지우기"
              >✕</button>
            {/if}
          </div>
        {:else}
          <div class="px-3 py-8 text-center text-[12.5px] text-slate-400 dark:text-slate-500">
            {#if searchPanel.tab === "bookmarks"}
              북마크가 없어요. 메시지에 마우스를 올리고 ☆를 누르면 여기 모입니다.
            {:else if query.trim().length < 2}
              찾을 말을 두 글자 이상 입력하세요. 텔레그램과 모든 로컬 대화의 메시지를 뒤집니다.
            {:else if !progress.finished}
              찾는 중…
            {:else}
              “{query.trim()}”이(가) 들어간 메시지가 없습니다.
            {/if}
          </div>
        {/each}
      </div>
      <div class="flex gap-3 border-t border-slate-100 px-3 py-1.5 text-[11px] text-slate-400 dark:border-slate-800">
        <span>↑↓ 이동</span><span>Enter 열기</span><span class="ml-auto">Esc 닫기</span>
      </div>
    </div>
  </div>
{/if}
