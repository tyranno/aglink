<script>
  // Window-wide keyboard layer: Ctrl+K quick switcher (jump to any
  // conversation by name / #id / work folder, or run one of the app's actions
  // — App passes them in as `actions`; ">" lists only those), Ctrl+/ shortcut
  // help (also App's "?" button through the bindable helpOpen), and
  // Ctrl+= / Ctrl+- / Ctrl+0 message text size. Also renders the short-lived
  // chat.toast notice.
  import { tick } from "svelte";
  import { chat, selectTargetFromSidebar, backendLabel, stepFontScale, isConversationPinned } from "./paneStore.svelte.js";

  // actions: [{ id, label, keys?, icon?, run }] — commands listed after the
  // conversations (or alone with a leading ">").
  let { actions = [], helpOpen = $bindable(false) } = $props();
  let open = $state(false);
  let query = $state("");
  let cursor = $state(0);
  let inputEl = $state(null);
  let listEl = $state(null);

  const SHORTCUTS = [
    ["Ctrl+K", "대화 찾아 열기 · 명령 실행 (> 로 명령만)"],
    ["Ctrl+Alt+Space", "빠른 질문 (다른 프로그램에서도)"],
    ["Ctrl+N", "새 로컬 대화"],
    ["Ctrl+,", "설정 열기"],
    ["Ctrl+\\", "사이드바 숨기기/보이기"],
    ["Alt+↓", "다음 읽지 않은 대화로 이동"],
    ["Ctrl+B", "작업 보드 (모든 대화 상태별 보기)"],
    ["Ctrl+F", "지금 대화 안에서 찾기"],
    ["Ctrl+Shift+F", "모든 대화에서 찾기 · 북마크"],
    ["Ctrl+Tab", "최근 대화로 전환 (Ctrl 누른 채 Tab)"],
    ["Enter / Shift+Enter", "보내기 / 줄바꿈"],
    ["↑ / ↓ (빈 입력창)", "보냈던 메시지 다시 불러오기"],
    ["!", "명령어 목록 (입력창 맨 앞)"],
    ["/", "상용구 넣기 — {{날짜}} {{클립보드}} {{선택}} 같은 칸도 채움"],
    ["Ctrl+M", "말로 입력"],
    ["Ctrl+Shift+M", "음성 대화 켜기/끄기"],
    ["Ctrl+= / Ctrl+- / Ctrl+0", "글자 크게 / 작게 / 원래대로"],
    ["Ctrl+V", "이미지 붙여넣어 첨부"],
    ["텍스트 선택", "‘인용해서 묻기’로 입력창에 인용"],
    ["Ctrl+/", "이 도움말"],
  ];

  function items() {
    const list = [];
    if (chat.telegram) {
      list.push({
        key: "telegram",
        target: { kind: "telegram" },
        title: chat.telegram.title || "텔레그램",
        id: "telegram",
        sub: "텔레그램 채널",
        backend: chat.telegram.backend,
        archived: false,
      });
    }
    for (const conv of chat.webConvs) {
      list.push({
        key: `web:${conv.id}`,
        target: { kind: "web", id: conv.id },
        title: conv.title || conv.id,
        id: conv.id,
        sub: conv.workDir || "",
        backend: conv.backend,
        archived: chat.archivedConvs.has(String(conv.id)),
        pinned: isConversationPinned(conv.id),
      });
    }
    return list;
  }

  // Busy and unread conversations float to the top, then pinned ones;
  // archived sink to the end.
  function rank(item) {
    if (chat.working.has(item.key)) return 0;
    if (chat.unread.has(item.key)) return 1;
    if (item.pinned) return 2;
    return item.archived ? 4 : 3;
  }

  function actionItems(q) {
    return (actions || [])
      .filter((action) => !q || `${action.label} ${action.keys || ""}`.toLowerCase().includes(q))
      .map((action) => ({ key: `action:${action.id}`, action }));
  }

  function matches() {
    const raw = query.trim().toLowerCase();
    if (raw.startsWith(">")) return actionItems(raw.slice(1).trim());
    const q = raw;
    const list = items().filter((item) =>
      !q || `${item.title} #${item.id} ${item.sub}`.toLowerCase().includes(q));
    const convs = list
      .map((item, i) => ({ item, i }))
      .sort((a, b) => rank(a.item) - rank(b.item) || a.i - b.i)
      .map(({ item }) => item);
    return [...convs, ...actionItems(q)];
  }

  async function openSwitcher() {
    helpOpen = false;
    open = true;
    query = "";
    cursor = 0;
    await tick();
    inputEl?.focus();
  }

  function choose(item) {
    if (!item) return;
    open = false;
    if (item.action) {
      try {
        void item.action.run?.();
      } catch {
        // An action failing must not leave the switcher half-closed.
      }
      return;
    }
    selectTargetFromSidebar(item.target);
  }

  async function move(step) {
    const n = matches().length;
    if (n === 0) return;
    cursor = (cursor + step + n) % n;
    await tick();
    listEl?.querySelector(`[data-index="${cursor}"]`)?.scrollIntoView({ block: "nearest" });
  }

  function handleKeydown(event) {
    if (event.key === "Escape" && helpOpen) {
      helpOpen = false;
      return;
    }
    if (!event.ctrlKey || event.altKey || event.metaKey) return;
    const code = event.code;
    if (!event.shiftKey && (event.key.toLowerCase() === "k" || code === "KeyK")) {
      event.preventDefault();
      if (open) open = false;
      else void openSwitcher();
    } else if (code === "Slash" || event.key === "/") {
      event.preventDefault();
      open = false;
      helpOpen = !helpOpen;
    } else if (code === "Equal" || code === "NumpadAdd" || event.key === "+" || event.key === "=") {
      event.preventDefault();
      stepFontScale(1);
    } else if (code === "Minus" || code === "NumpadSubtract" || event.key === "-") {
      event.preventDefault();
      stepFontScale(-1);
    } else if (!event.shiftKey && (code === "Digit0" || code === "Numpad0")) {
      event.preventDefault();
      stepFontScale(0);
    }
  }
</script>

<svelte:window onkeydown={handleKeydown} />

{#if open}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-50 flex justify-center bg-slate-950/30 px-4 pt-[12vh] backdrop-blur-[1px]" onclick={() => (open = false)}>
    <div
      class="flex h-fit max-h-[60vh] w-full max-w-lg flex-col overflow-hidden rounded-xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900"
      onclick={(event) => event.stopPropagation()}
      role="dialog"
      aria-label="대화 찾기 · 명령 실행"
    >
      <div class="flex items-center gap-2 border-b border-slate-200 px-3 py-2 dark:border-slate-700">
        <svg class="h-4 w-4 shrink-0 text-slate-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
        <input
          bind:this={inputEl}
          class="min-w-0 flex-1 bg-transparent py-1 text-sm text-slate-900 outline-none placeholder:text-slate-400 dark:text-slate-100"
          placeholder="대화 이름·#번호·작업 폴더로 찾기, > 로 명령"
          value={query}
          oninput={(event) => { query = event.currentTarget.value; cursor = 0; }}
          onkeydown={(event) => {
            if (event.key === "ArrowDown") { event.preventDefault(); void move(1); }
            else if (event.key === "ArrowUp") { event.preventDefault(); void move(-1); }
            else if (event.key === "Enter" && !event.isComposing) { event.preventDefault(); choose(matches()[cursor]); }
            else if (event.key === "Escape") { event.preventDefault(); open = false; }
          }}
        />
        <kbd class="shrink-0 rounded border border-slate-200 px-1.5 text-[10px] text-slate-400 dark:border-slate-700">Esc</kbd>
      </div>
      <div bind:this={listEl} class="min-h-0 flex-1 overflow-y-auto p-1.5" role="listbox">
        {#each matches() as item, index (item.key)}
          {#if item.action}
            {#if index === 0 || !matches()[index - 1]?.action}
              <div class="px-2.5 pb-1 pt-2 text-[10.5px] font-bold tracking-[0.04em] text-slate-400 dark:text-slate-500">명령</div>
            {/if}
            <button
              type="button"
              data-index={index}
              class={`flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left transition-colors ${index === cursor ? "bg-blue-50 dark:bg-blue-950/50" : "hover:bg-slate-50 dark:hover:bg-slate-800"}`}
              role="option"
              aria-selected={index === cursor}
              onmouseenter={() => (cursor = index)}
              onclick={() => choose(item)}
            >
              <span class="grid w-4 shrink-0 place-items-center text-[13px] leading-none text-slate-500 dark:text-slate-400" aria-hidden="true">{item.action.icon || "›"}</span>
              <span class="min-w-0 flex-1 truncate text-sm font-medium text-slate-800 dark:text-slate-200">{item.action.label}</span>
              {#if item.action.keys}
                <kbd class="shrink-0 rounded border border-slate-200 px-1.5 font-mono text-[10px] text-slate-400 dark:border-slate-700">{item.action.keys}</kbd>
              {/if}
            </button>
          {:else}
          <button
            type="button"
            data-index={index}
            class={`flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left transition-colors ${index === cursor ? "bg-blue-50 dark:bg-blue-950/50" : "hover:bg-slate-50 dark:hover:bg-slate-800"}`}
            role="option"
            aria-selected={index === cursor}
            onmouseenter={() => (cursor = index)}
            onclick={() => choose(item)}
          >
            <span class={`h-2 w-2 shrink-0 rounded-full ${chat.working.has(item.key) ? "animate-pulse bg-blue-500" : item.key === "telegram" ? "bg-sky-500" : "bg-slate-300 dark:bg-slate-600"}`}></span>
            <span class="min-w-0 flex-1">
              <span class={`block truncate text-sm font-medium ${item.archived ? "text-slate-400 dark:text-slate-500" : "text-slate-900 dark:text-slate-100"}`}>{item.title}</span>
              {#if item.sub}
                <span class="block truncate text-[11px] text-slate-400 dark:text-slate-500">{item.sub}</span>
              {/if}
            </span>
            {#if chat.working.has(item.key)}
              <span class="shrink-0 text-[10px] font-semibold text-blue-600 dark:text-blue-400">작업 중</span>
            {:else if chat.unread.has(item.key)}
              <span class="shrink-0 rounded-full bg-blue-600 px-1.5 py-0.5 text-[10px] font-bold text-white">NEW</span>
            {/if}
            {#if item.archived}
              <span class="shrink-0 text-[10px] text-slate-400">보관됨</span>
            {:else if item.pinned}
              <span class="shrink-0 text-[11px] text-amber-500" title="고정됨" aria-label="고정됨">★</span>
            {/if}
            <span class="shrink-0 font-mono text-[11px] text-slate-400 dark:text-slate-500">#{item.id}</span>
            <span class="shrink-0 rounded-full bg-slate-100 px-1.5 py-0.5 text-[10px] font-bold text-slate-600 dark:bg-slate-800 dark:text-slate-400">{backendLabel(item.backend)}</span>
          </button>
          {/if}
        {:else}
          <div class="px-3 py-6 text-center text-sm text-slate-400">맞는 대화나 명령이 없습니다.</div>
        {/each}
      </div>
      <div class="flex gap-3 border-t border-slate-100 px-3 py-1.5 text-[11px] text-slate-400 dark:border-slate-800">
        <span>↑↓ 이동</span><span>Enter 열기·실행</span><span>&gt; 명령만</span><span class="ml-auto">Ctrl+/ 단축키 모음</span>
      </div>
    </div>
  </div>
{/if}

{#if helpOpen}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-950/30 px-4" onclick={() => (helpOpen = false)}>
    <div
      class="w-full max-w-md rounded-xl border border-slate-200 bg-white p-4 shadow-2xl dark:border-slate-700 dark:bg-slate-900"
      onclick={(event) => event.stopPropagation()}
      role="dialog"
      aria-label="단축키 모음"
    >
      <div class="mb-3 flex items-center">
        <div class="text-sm font-semibold text-slate-900 dark:text-slate-100">단축키 모음</div>
        <button type="button" class="ml-auto rounded px-2 text-slate-400 hover:text-slate-700 dark:hover:text-slate-200" onclick={() => (helpOpen = false)} aria-label="닫기">✕</button>
      </div>
      <div class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-[13px]">
        {#each SHORTCUTS as [keys, desc] (keys)}
          <kbd class="whitespace-nowrap rounded border border-slate-200 bg-slate-50 px-1.5 py-0.5 font-mono text-[11px] text-slate-700 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300">{keys}</kbd>
          <span class="text-slate-600 dark:text-slate-400">{desc}</span>
        {/each}
      </div>
    </div>
  </div>
{/if}

{#if chat.toast}
  <div class="pointer-events-none fixed bottom-16 left-1/2 z-50 -translate-x-1/2 rounded-full bg-slate-900/90 px-4 py-1.5 text-xs font-medium text-white shadow-lg dark:bg-slate-100/90 dark:text-slate-900" role="status">
    {chat.toast}
  </div>
{/if}
