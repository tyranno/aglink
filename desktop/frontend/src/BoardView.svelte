<script>
  // 작업 보드 (Ctrl+B / 더보기 메뉴): 모든 대화를 상태별 열로 한눈에 보여준다.
  //   작업 중   — chat.working 에 있는 대화 (QuickSwitcher 의 "작업 중"과 같은 기준)
  //   승인 대기 — 호스트가 control:approval 로 승인을 기다리는 대화 (ApprovalPrompt 와
  //               같은 이벤트를 따로 듣는다; session "대화 ab12cd34" 의 앞 8자로 대화를 찾음)
  //   새 답변   — chat.unread (QuickSwitcher 의 NEW)
  //   쉬는 중   — 그 밖의 대화
  // 카드를 누르면 QuickSwitcher 와 똑같이 selectTargetFromSidebar 로 지금 칸에 연다.
  // 미리보기는 화면에 떠 있는 대화면 chat.messagesByKey 를, 아니면(실시간 프레임을
  // 받지 않으니 낡을 수 있음) 보드를 열 때 GetHistory 로 마지막 줄만 읽어 둔다.
  import { tick, untrack } from "svelte";
  import { Events } from "@wailsio/runtime";
  import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";
  import {
    chat,
    selectTargetFromSidebar,
    backendLabel,
    targetKey,
    isTargetVisibleAnywhere,
    parseJSON,
    STALE_AFTER_MS,
  } from "./paneStore.svelte.js";

  let { open = $bindable(false), onnavigate = null } = $props();

  let showArchived = $state(false);
  let approvals = $state([]); // [{ id, tool, session, hasSession, at }]
  let previews = $state(new Map()); // key -> { text, role, at } fetched for hidden conversations
  let boardEl = $state(null);
  let fetchSeq = 0;

  $effect(() => {
    const offAsk = Events.On("control:approval", (e) => {
      const a = e.data || {};
      if (!a.id || approvals.some((q) => q.id === a.id)) return;
      approvals = [...approvals, { id: a.id, tool: a.tool || "", session: a.session || "", hasSession: !!a.hasSession, at: Date.now(), timeoutSec: a.timeoutSec || 0 }];
    });
    const offDone = Events.On("control:approval_done", (e) => {
      const id = (e.data || {}).id;
      approvals = approvals.filter((q) => q.id !== id);
    });
    return () => {
      offAsk();
      offDone();
    };
  });

  // Requests whose window has long passed are dropped even if no _done arrived.
  const liveApprovals = $derived(
    approvals.filter((q) => !q.timeoutSec || chat.nowTick - q.at < (q.timeoutSec + 30) * 1000),
  );

  function approvalFor(item) {
    if (item.kind !== "web") return null;
    for (const q of liveApprovals) {
      const prefix = q.session.replace(/^대화\s*/, "").trim();
      if (prefix && String(item.id).startsWith(prefix)) return q;
    }
    return null;
  }

  const unmatchedApprovals = $derived(
    liveApprovals.filter((q) => {
      const prefix = q.session.replace(/^대화\s*/, "").trim();
      return !prefix || !chat.webConvs.some((c) => String(c.id).startsWith(prefix));
    }).length,
  );

  function items() {
    const list = [];
    if (chat.telegram) {
      list.push({
        key: "telegram",
        kind: "telegram",
        target: { kind: "telegram" },
        title: chat.telegram.title || "텔레그램",
        id: "telegram",
        workDir: chat.telegram.project || "",
        backend: chat.telegram.backend,
        archived: false,
      });
    }
    for (const conv of chat.webConvs) {
      list.push({
        key: `web:${conv.id}`,
        kind: "web",
        target: { kind: "web", id: conv.id },
        title: conv.title || conv.id,
        id: conv.id,
        workDir: conv.workDir || "",
        backend: conv.backend,
        archived: chat.archivedConvs.has(String(conv.id)),
      });
    }
    return list;
  }

  function lastMessage(messages) {
    for (let i = (messages?.length || 0) - 1; i >= 0; i--) {
      const m = messages[i];
      if (m.role !== "user" && m.role !== "assistant" && m.role !== "narration") continue;
      if (!m.text && !m.image) continue;
      return { text: m.text || "", image: !!m.image, role: m.role, at: m.at || 0 };
    }
    return null;
  }

  function previewOf(item) {
    const live = chat.messagesByKey.get(item.key);
    if (isTargetVisibleAnywhere(item.key) && live?.length) return lastMessage(live);
    return previews.get(item.key) || (live?.length ? lastMessage(live) : null);
  }

  function plain(text) {
    return String(text || "")
      .replace(/```[\s\S]*?```/g, " [코드] ")
      .replace(/!\[[^\]]*\]\([^)]*\)/g, " [이미지] ")
      .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
      .replace(/[#>*_`~|]+/g, " ")
      .replace(/\s+/g, " ")
      .trim();
  }

  function previewText(p) {
    if (!p) return "";
    const body = plain(p.text) || (p.image ? "[이미지]" : "");
    return p.role === "user" ? `나: ${body}` : body;
  }

  const COLUMNS = [
    { id: "working", title: "작업 중", dot: "bg-blue-500", ring: "text-blue-600 dark:text-blue-400", empty: "작업 중인 대화가 없습니다" },
    { id: "approval", title: "승인 대기", dot: "bg-amber-500", ring: "text-amber-600 dark:text-amber-400", empty: "승인을 기다리는 대화가 없습니다" },
    { id: "unread", title: "새 답변", dot: "bg-emerald-500", ring: "text-emerald-600 dark:text-emerald-400", empty: "읽지 않은 답변이 없습니다" },
    { id: "idle", title: "쉬는 중", dot: "bg-slate-400 dark:bg-slate-500", ring: "text-slate-500 dark:text-slate-400", empty: "쉬는 대화가 없습니다" },
  ];

  function columnOf(item) {
    if (approvalFor(item)) return "approval";
    if (chat.working.has(item.key)) return "working";
    if (chat.unread.has(item.key)) return "unread";
    return "idle";
  }

  const board = $derived.by(() => {
    const groups = { working: [], approval: [], unread: [], idle: [] };
    let archivedCount = 0;
    for (const item of items()) {
      const col = columnOf(item);
      if (item.archived && col === "idle") {
        archivedCount++;
        if (!showArchived) continue;
      }
      groups[col].push(item);
    }
    return { groups, archivedCount };
  });

  function shortDir(dir) {
    if (!dir) return "";
    const parts = String(dir).replace(/[\\/]+$/, "").split(/[\\/]+/).filter(Boolean);
    if (parts.length <= 2) return parts.join("/") || dir;
    return `…/${parts.slice(-2).join("/")}`;
  }

  function fmtElapsed(ms) {
    const sec = Math.max(0, Math.floor(ms / 1000));
    if (sec < 60) return `${sec}초`;
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}분 ${sec % 60}초`;
    return `${Math.floor(min / 60)}시간 ${min % 60}분`;
  }

  function fmtAgo(at) {
    if (!at) return "";
    const diff = chat.nowTick - at;
    if (diff < 60000) return "방금";
    const min = Math.floor(diff / 60000);
    if (min < 60) return `${min}분 전`;
    const hr = Math.floor(min / 60);
    if (hr < 24) return `${hr}시간 전`;
    const day = Math.floor(hr / 24);
    if (day < 7) return `${day}일 전`;
    return new Date(at).toLocaleDateString("ko-KR", { month: "numeric", day: "numeric" });
  }

  function timeLine(item, col) {
    const w = chat.working.get(item.key);
    if (w) {
      const parts = [`경과 ${fmtElapsed(chat.nowTick - w.startedAt)}`];
      if (w.lastActivityAt) parts.push(`마지막 활동 ${fmtAgo(w.lastActivityAt)}`);
      return parts.join(" · ");
    }
    if (col === "approval") {
      const q = approvalFor(item);
      return q ? `요청 ${fmtAgo(q.at)}` : "";
    }
    const at = previewOf(item)?.at;
    return at ? `마지막 활동 ${fmtAgo(at)}` : "";
  }

  // Reads the last line of every conversation not shown in a pane. Runs a few
  // requests at a time; a newer refresh makes the older one stop early.
  async function refreshPreviews(keys) {
    const seq = keys ? fetchSeq : ++fetchSeq; // only a full refresh supersedes the last
    const todo = items().filter((item) => (!keys || keys.includes(item.key)) && !isTargetVisibleAnywhere(item.key));
    let next = 0;
    async function worker() {
      while (next < todo.length && seq === fetchSeq) {
        const item = todo[next++];
        try {
          const raw = await ControlService.GetHistory(item.kind, item.kind === "web" ? String(item.id) : "");
          const data = parseJSON(raw, { turns: [] });
          const turns = Array.isArray(data.turns) ? data.turns : [];
          const last = lastMessage(turns.map((t) => ({ role: t.role || "assistant", text: t.text || "", image: t.image || t.data || "", at: t.at || 0 })));
          previews = new Map(previews).set(item.key, last);
        } catch {
          // Keep whatever preview we had; the card just shows less.
        }
      }
    }
    await Promise.all([worker(), worker(), worker()]);
  }

  function openBoard() {
    open = true;
  }

  function close() {
    open = false;
    fetchSeq++; // stop loading previews nobody will see
  }

  function choose(item) {
    close();
    onnavigate?.();
    selectTargetFromSidebar(item.target);
  }

  // A new unread answer or a finished turn of a hidden conversation means its
  // cached preview is stale; re-read just those while the board is open.
  let lastSeen = new Set();
  $effect(() => {
    const changed = [...chat.unread, ...chat.working.keys()];
    const snapshot = new Set(changed);
    if (!open) {
      lastSeen = snapshot;
      return;
    }
    const stale = [...new Set([...snapshot, ...lastSeen])].filter((k) => snapshot.has(k) !== lastSeen.has(k));
    lastSeen = snapshot;
    if (stale.length) untrack(() => void refreshPreviews(stale));
  });

  function handleKeydown(event) {
    if (open && event.key === "Escape" && !event.defaultPrevented) {
      event.preventDefault();
      event.stopPropagation();
      close();
      return;
    }
    if (!event.ctrlKey || event.altKey || event.metaKey || event.shiftKey) return;
    if (event.code === "KeyB" || event.key.toLowerCase() === "b") {
      event.preventDefault();
      if (open) close();
      else openBoard();
    }
  }

  // Arrow keys move between cards: ←/→ across columns, ↑/↓ within one.
  function handleBoardKeydown(event) {
    if (!["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"].includes(event.key)) return;
    const card = event.target instanceof HTMLElement ? event.target.closest("[data-card]") : null;
    if (!card) return;
    event.preventDefault();
    const col = Number(card.dataset.col);
    const row = Number(card.dataset.row);
    const find = (c, r) => boardEl?.querySelector(`[data-card][data-col="${c}"][data-row="${r}"]`);
    if (event.key === "ArrowDown") find(col, row + 1)?.focus();
    else if (event.key === "ArrowUp") find(col, row - 1)?.focus();
    else {
      const step = event.key === "ArrowRight" ? 1 : -1;
      for (let c = col + step; c >= 0 && c < COLUMNS.length; c += step) {
        const n = board.groups[COLUMNS[c].id].length;
        if (n) { find(c, Math.min(row, n - 1))?.focus(); break; }
      }
    }
  }

  // On every open (Ctrl+B or bind:open from the menu): load previews, focus a card.
  let wasOpen = false;
  $effect(() => {
    if (open && !wasOpen) {
      untrack(() => void refreshPreviews());
      void tick().then(() => boardEl?.querySelector("[data-card]")?.focus());
    }
    wasOpen = open;
  });
</script>

<svelte:window onkeydown={handleKeydown} />

{#if open}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-50 flex justify-center bg-slate-950/35 px-4 py-[6vh] backdrop-blur-[2px]" onclick={close}>
    <div
      bind:this={boardEl}
      class="flex h-full w-full max-w-7xl flex-col overflow-hidden rounded-2xl border border-slate-200 bg-slate-50 shadow-2xl dark:border-slate-700 dark:bg-slate-900"
      onclick={(event) => event.stopPropagation()}
      onkeydown={handleBoardKeydown}
      role="dialog"
      aria-modal="true"
      aria-label="작업 보드"
      tabindex="-1"
    >
      <div class="flex shrink-0 items-center gap-3 border-b border-slate-200 bg-white px-5 py-3 dark:border-slate-700 dark:bg-slate-900">
        <svg class="h-5 w-5 shrink-0 text-slate-500 dark:text-slate-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <rect x="3" y="4" width="5" height="16" rx="1.5" /><rect x="10" y="4" width="5" height="11" rx="1.5" /><rect x="17" y="4" width="4" height="7" rx="1.5" />
        </svg>
        <div class="min-w-0">
          <div class="text-sm font-semibold text-slate-900 dark:text-slate-100">작업 보드</div>
          <div class="text-[11px] text-slate-500 dark:text-slate-400">
            작업 중 {board.groups.working.length} · 승인 대기 {board.groups.approval.length} · 새 답변 {board.groups.unread.length}
            {#if unmatchedApprovals > 0}
              <span class="text-amber-600 dark:text-amber-400"> · 대화를 알 수 없는 승인 요청 {unmatchedApprovals}건</span>
            {/if}
          </div>
        </div>
        <div class="ml-auto flex items-center gap-2">
          {#if board.archivedCount > 0}
            <button
              type="button"
              class={`flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-[11px] font-medium transition-colors duration-150 ${showArchived ? "border-slate-400 bg-slate-100 text-slate-700 dark:border-slate-500 dark:bg-slate-800 dark:text-slate-200" : "border-slate-200 text-slate-500 hover:bg-slate-100 dark:border-slate-700 dark:text-slate-400 dark:hover:bg-slate-800"}`}
              onclick={() => (showArchived = !showArchived)}
              aria-pressed={showArchived}
            >
              <svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="4" width="18" height="4" rx="1" /><path d="M5 8v11a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8" /><line x1="10" y1="12" x2="14" y2="12" /></svg>
              보관된 대화 {board.archivedCount}개 {showArchived ? "숨기기" : "보기"}
            </button>
          {/if}
          <kbd class="rounded border border-slate-200 px-1.5 text-[10px] text-slate-400 dark:border-slate-700">Esc</kbd>
          <button
            type="button"
            class="grid h-7 w-7 place-items-center rounded-md text-slate-400 transition-colors duration-150 hover:bg-slate-100 hover:text-slate-700 dark:hover:bg-slate-800 dark:hover:text-slate-200"
            onclick={close}
            aria-label="닫기"
            title="닫기 (Esc)"
          >
            <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" aria-hidden="true"><line x1="6" y1="6" x2="18" y2="18" /><line x1="18" y1="6" x2="6" y2="18" /></svg>
          </button>
        </div>
      </div>

      <div class="grid min-h-0 flex-1 grid-cols-4 gap-3 overflow-x-auto p-4">
        {#each COLUMNS as column, colIndex (column.id)}
          {@const cards = board.groups[column.id]}
          <section class="flex min-h-0 min-w-[210px] flex-col rounded-xl bg-slate-100/70 dark:bg-slate-800/50" aria-label={column.title}>
            <div class="flex shrink-0 items-center gap-2 px-3 pb-2 pt-3">
              <span class={`h-2 w-2 rounded-full ${column.dot} ${column.id === "working" && cards.length ? "animate-pulse" : ""}`}></span>
              <span class="text-xs font-semibold text-slate-700 dark:text-slate-200">{column.title}</span>
              <span class="rounded-full bg-white px-1.5 text-[10px] font-semibold text-slate-500 dark:bg-slate-700 dark:text-slate-300">{cards.length}</span>
            </div>
            <div class="min-h-0 flex-1 space-y-2 overflow-y-auto px-2 pb-2">
              {#each cards as item, rowIndex (item.key)}
                {@const p = previewOf(item)}
                {@const w = chat.working.get(item.key)}
                {@const q = column.id === "approval" ? approvalFor(item) : null}
                {@const stale = w && chat.nowTick - w.lastAliveAt > STALE_AFTER_MS}
                <button
                  type="button"
                  data-card
                  data-col={colIndex}
                  data-row={rowIndex}
                  class="group block w-full rounded-lg border border-slate-200 bg-white p-3 text-left shadow-sm outline-none transition-[transform,box-shadow,border-color] duration-150 ease-out hover:-translate-y-0.5 hover:border-slate-300 hover:shadow-md focus-visible:border-blue-400 focus-visible:ring-2 focus-visible:ring-blue-400/40 dark:border-slate-700 dark:bg-slate-900 dark:hover:border-slate-600"
                  onclick={() => choose(item)}
                  title={`${item.title} 열기`}
                >
                  <div class="flex items-center gap-1.5">
                    {#if item.kind === "telegram"}
                      <svg class="h-3.5 w-3.5 shrink-0 text-sky-500" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 4 3 11l6 2 2 6 3-4 5 4z" /></svg>
                    {/if}
                    <span class={`min-w-0 flex-1 truncate text-[13px] font-semibold ${item.archived ? "text-slate-400 dark:text-slate-500" : "text-slate-900 dark:text-slate-100"}`}>{item.title}</span>
                    <span class="shrink-0 font-mono text-[10px] text-slate-400 dark:text-slate-500">#{item.kind === "web" ? item.id : "tg"}</span>
                  </div>

                  {#if item.workDir}
                    <div class="mt-1 flex items-center gap-1 text-[11px] text-slate-500 dark:text-slate-400" title={item.workDir}>
                      <svg class="h-3 w-3 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" /></svg>
                      <span class="truncate font-mono">{shortDir(item.workDir)}</span>
                    </div>
                  {/if}

                  {#if w && w.label}
                    <div class={`mt-2 truncate text-[11px] font-medium ${stale ? "text-amber-600 dark:text-amber-400" : "text-blue-600 dark:text-blue-400"}`}>{stale ? "상태 확인이 오래됨" : w.label}</div>
                  {:else if stale}
                    <div class="mt-2 truncate text-[11px] font-medium text-amber-600 dark:text-amber-400">상태 확인이 오래됨</div>
                  {/if}
                  {#if q}
                    <div class="mt-2 flex items-center gap-1 truncate text-[11px] font-medium text-amber-700 dark:text-amber-300">
                      <svg class="h-3 w-3 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="5" y="11" width="14" height="10" rx="2" /><path d="M8 11V7a4 4 0 0 1 8 0v4" /></svg>
                      <span class="truncate">승인 요청 · {q.tool || "도구"}</span>
                    </div>
                  {/if}

                  <div class={`mt-2 line-clamp-1 text-[12px] leading-5 ${p ? "text-slate-600 dark:text-slate-300" : "italic text-slate-400 dark:text-slate-500"}`}>
                    {p ? previewText(p) || "(내용 없음)" : "아직 메시지가 없습니다"}
                  </div>

                  <div class="mt-2 flex items-center gap-1.5 text-[10px] text-slate-400 dark:text-slate-500">
                    <svg class="h-3 w-3 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></svg>
                    <span class="min-w-0 flex-1 truncate">{timeLine(item, column.id) || "활동 기록 없음"}</span>
                    {#if item.backend}
                      <span class="shrink-0 rounded-full bg-slate-100 px-1.5 py-0.5 font-bold text-slate-500 dark:bg-slate-800 dark:text-slate-400">{backendLabel(item.backend)}</span>
                    {/if}
                  </div>
                </button>
              {:else}
                <div class="rounded-lg border border-dashed border-slate-200 px-3 py-6 text-center text-[11px] text-slate-400 dark:border-slate-700 dark:text-slate-500">{column.empty}</div>
              {/each}
            </div>
          </section>
        {/each}
      </div>

      <div class="flex shrink-0 gap-4 border-t border-slate-200 bg-white px-5 py-1.5 text-[11px] text-slate-400 dark:border-slate-700 dark:bg-slate-900">
        <span>방향키 이동</span><span>Enter 열기</span><span>Esc 닫기</span><span class="ml-auto">Ctrl+B 작업 보드</span>
      </div>
    </div>
  </div>
{/if}
