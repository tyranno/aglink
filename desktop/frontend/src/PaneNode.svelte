<script>
  import PaneNode from "./PaneNode.svelte";
  import {
    chat,
    pane,
    focusPane,
    closePane,
    startSplitResize,
    paneConversationMeta,
    backendLabel,
    messagesForPane,
    paneWorking,
    paneWorkingText,
    showCommandMenuForPane,
    commandCandidatesForPane,
    selectCommandForPane,
    updatePane,
    pickAttachment,
    clearPaneAttachment,
    setPaneComposerValue,
    handleComposerKeydown,
    handleComposerPaste,
    registerPaneTextarea,
    registerPaneLog,
    canSendPane,
    sendFromPane,
    handlePaneDragStart,
    handlePaneDragEnd,
    handlePaneDragOver,
    handlePaneDragLeave,
    handlePaneDrop,
    progressForTarget,
    toggleProgressPopup,
    paneColor,
    scrollPaneLogToBottom,
    togglePaneBackendMenu,
    setTargetBackend,
    togglePaneWorkDirMenu,
    setTargetWorkDir,
  } from "./paneStore.svelte.js";
  import { renderMarkdown } from "./markdown.js";

  let { node } = $props();

  // Drives the "jump to latest" FAB: hidden while the log is already near
  // its bottom, shown once the user scrolls up to read earlier messages.
  let atBottom = $state(true);
  const NEAR_BOTTOM_PX = 80;

  function handleLogScroll(event) {
    const el = event.currentTarget;
    atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM_PX;
  }

  // User bubbles stay verbatim (typed text shown as typed); assistant/system
  // output is Markdown and gets rendered, matching aglink-chat's web/app.js
  // add(). Only ever touches textContent inside renderMarkdown — never
  // innerHTML — so model output can't inject markup into the page.
  function renderMarkdownInto(el, text) {
    function update(value) {
      el.replaceChildren(renderMarkdown(value || ""));
    }
    update(text);
    return { update };
  }
  let progressLogEl = $state(null);

  function dockOverlayStyle(zone) {
    if (zone === "left") return "top:0; right:50%; bottom:0; left:0;";
    if (zone === "right") return "top:0; right:0; bottom:0; left:50%;";
    if (zone === "top") return "top:0; right:0; bottom:50%; left:0;";
    if (zone === "bottom") return "top:50%; right:0; bottom:0; left:0;";
    return "top:12%; right:12%; bottom:12%; left:12%;";
  }

  $effect(() => {
    if (node.type !== "leaf" || chat.progressPopupPaneId !== node.paneId || !progressLogEl) return;
    const p = pane(node.paneId);
    if (!p) return;
    void progressForTarget(p.target).length;
    const el = progressLogEl;
    requestAnimationFrame(() => {
      el.scrollTop = el.scrollHeight;
    });
  });
</script>

{#if node.type === "split"}
  <div class={`flex min-h-0 flex-1 overflow-hidden ${node.direction === "column" ? "flex-col" : "flex-row"}`}>
    {#each node.children as child, index (child.node.type === "leaf" ? child.node.paneId : child.node.id)}
      {#if index > 0}
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
        <div
          class={`split-resizer-hitbox group relative z-20 shrink-0 bg-transparent ${node.direction === "column"
            ? "h-[9px] -my-1 w-full cursor-row-resize"
            : "w-[9px] -mx-1 cursor-col-resize"}`}
          role="separator"
          aria-orientation={node.direction === "column" ? "horizontal" : "vertical"}
          aria-label={node.direction === "column" ? "상하 창 크기 조절" : "좌우 창 크기 조절"}
          onmousedown={(event) => startSplitResize(event, node.id, node.direction, index)}
        >
          <div
            class={node.direction === "column"
              ? "pointer-events-none absolute left-0 right-0 top-1/2 h-px -translate-y-1/2 bg-slate-200 group-hover:bg-blue-300"
              : "pointer-events-none absolute bottom-0 left-1/2 top-0 w-px -translate-x-1/2 bg-slate-200 group-hover:bg-blue-300"}
          ></div>
        </div>
      {/if}
      <div class="flex min-w-0 min-h-0 overflow-hidden" style={`flex: 0 0 ${child.size}%`}>
        <PaneNode node={child.node} />
      </div>
    {/each}
  </div>
{:else}
  {@const p = pane(node.paneId)}
  {@const color = chat.panes.length > 1 ? paneColor(p?.id) : null}
  {#if p}
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <main
      class={`relative flex min-w-0 min-h-0 flex-1 flex-col overflow-hidden ${color ? `border ${color.border}` : ""} ${chat.panes.length > 1 && p.id === chat.focusedPaneId ? "ring-2 ring-inset ring-blue-400" : ""}`}
      onclick={() => focusPane(p.id)}
      ondragover={(event) => handlePaneDragOver(event, p.id)}
      ondragleave={() => handlePaneDragLeave(p.id)}
      ondrop={(event) => handlePaneDrop(event, p.id)}
    >
      {#if chat.overPaneId === p.id}
        <div
          class="pointer-events-none absolute z-30 rounded-md border-2 border-blue-500 bg-blue-400/25"
          style={dockOverlayStyle(chat.overZone)}
        ></div>
      {/if}

      <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div
        class={`relative z-30 flex h-10 shrink-0 cursor-grab items-center gap-2 border-b border-slate-200 px-4 backdrop-blur active:cursor-grabbing ${color ? color.headerBg : "bg-white/75"}`}
        draggable="true"
        ondragstart={(event) => handlePaneDragStart(event, p.id)}
        ondragend={handlePaneDragEnd}
        title="드래그해서 다른 패널 가장자리로 재배치"
      >
        {#if color}
          <span class={`h-2 w-2 shrink-0 rounded-full ${color.dot}`}></span>
        {/if}
        {#if paneConversationMeta(p.target)}
          <span class={`inline-flex shrink-0 rounded-full px-2 py-0.5 text-[11px] font-semibold ${paneConversationMeta(p.target).kind === "web" ? "bg-blue-100 text-blue-700" : "bg-sky-100 text-sky-700"}`}>
            {paneConversationMeta(p.target).kind === "web" ? "로컬" : "텔레그램"}
          </span>
          <div class="flex min-w-0 flex-1 items-center gap-2">
            <div class="min-w-0 truncate text-sm font-semibold text-slate-900">{paneConversationMeta(p.target).title}</div>
            <span class="shrink-0 rounded-full bg-slate-100 px-2 py-0.5 font-mono text-[11px] text-slate-600">#{paneConversationMeta(p.target).id}</span>
            <div class="relative shrink-0" data-pane-backend-menu>
              <button
                type="button"
                draggable="false"
                class="rounded-full bg-slate-200 px-2 py-0.5 text-[11px] font-semibold text-slate-700 hover:bg-slate-300"
                onmousedown={(event) => { event.preventDefault(); event.stopPropagation(); togglePaneBackendMenu(p.id); }}
                onclick={(event) => event.stopPropagation()}
                onkeydown={(event) => {
                  if (event.key !== "Enter" && event.key !== " ") return;
                  event.preventDefault();
                  event.stopPropagation();
                  togglePaneBackendMenu(p.id);
                }}
                title="AI backend 변경"
                aria-label="AI backend 변경"
              >
                {backendLabel(paneConversationMeta(p.target).backend)}
              </button>
              {#if chat.backendMenuPaneId === p.id}
                <div class="absolute left-0 top-6 z-40 w-32 rounded-lg border border-slate-200 bg-white p-1.5 shadow-xl">
                  <button class="block w-full rounded-md px-3 py-2 text-left text-xs font-medium text-slate-700 hover:bg-slate-100" onclick={(event) => { event.stopPropagation(); void setTargetBackend(p.target, "default"); }}>Default</button>
                  <button class="block w-full rounded-md px-3 py-2 text-left text-xs font-medium text-slate-700 hover:bg-slate-100" onclick={(event) => { event.stopPropagation(); void setTargetBackend(p.target, "claude"); }}>Claude</button>
                  <button class="block w-full rounded-md px-3 py-2 text-left text-xs font-medium text-slate-700 hover:bg-slate-100" onclick={(event) => { event.stopPropagation(); void setTargetBackend(p.target, "codex"); }}>Codex</button>
                </div>
              {/if}
            </div>
            {#if paneConversationMeta(p.target).kind === "web"}
              <div class="relative min-w-0 shrink" data-pane-workdir-menu>
                <button
                  type="button"
                  draggable="false"
                  class={`block max-w-[260px] truncate rounded-full px-2 py-0.5 text-left text-[11px] ${paneConversationMeta(p.target).workDir ? "bg-emerald-50 text-emerald-700 hover:bg-emerald-100" : "bg-slate-100 text-slate-500 hover:bg-slate-200"}`}
                  onmousedown={(event) => { event.preventDefault(); event.stopPropagation(); togglePaneWorkDirMenu(p.id); }}
                  onclick={(event) => event.stopPropagation()}
                  onkeydown={(event) => {
                    if (event.key !== "Enter" && event.key !== " ") return;
                    event.preventDefault();
                    event.stopPropagation();
                    togglePaneWorkDirMenu(p.id);
                  }}
                  title={paneConversationMeta(p.target).workDir || "작업 폴더 미지정"}
                  aria-label="작업 폴더 확인 및 변경"
                >
                  {paneConversationMeta(p.target).workDir || "작업 폴더 미지정"}
                </button>
                {#if chat.workDirMenuPaneId === p.id}
                  <div class="absolute left-0 top-6 z-40 w-80 rounded-lg border border-slate-200 bg-white p-2 shadow-xl">
                    <div class="px-2 pb-1 text-[11px] font-semibold uppercase tracking-wide text-slate-400">작업 폴더</div>
                    <div class="max-h-28 overflow-y-auto break-all rounded-md bg-slate-50 px-2 py-2 text-xs leading-5 text-slate-700">
                      {paneConversationMeta(p.target).workDir || "설정되지 않음"}
                    </div>
                    <button
                      class="mt-1 block w-full rounded-md px-2 py-2 text-left text-xs font-medium text-slate-700 hover:bg-slate-100"
                      onclick={(event) => { event.stopPropagation(); void setTargetWorkDir(p.target); }}
                    >
                      작업 폴더 선택...
                    </button>
                  </div>
                {/if}
              </div>
            {/if}
          </div>
        {:else}
          <div class="min-w-0 flex-1 text-sm text-slate-500">대화를 선택하세요.</div>
        {/if}
        {#if chat.panes.length > 1}
          <button
            class="grid h-7 w-7 shrink-0 place-items-center rounded-md border border-slate-200 bg-white text-sm text-slate-600 hover:bg-slate-100"
            onclick={(event) => { event.stopPropagation(); closePane(p.id); }}
            title="분할 닫기"
            aria-label="분할 닫기"
          >
            ✕
          </button>
        {/if}
      </div>

      <div class="relative flex min-h-0 flex-1 flex-col overflow-hidden">
        <div use:registerPaneLog={p.id} onscroll={handleLogScroll} class="flex min-h-0 flex-1 flex-col overflow-y-auto px-4 py-4">
          <div class="flex min-h-full w-full flex-col gap-2">
            {#if messagesForPane(p.target).length === 0}
              <div class="rounded-lg border border-dashed border-slate-300 bg-white/60 px-6 py-10 text-center text-sm text-slate-500">
                메시지가 없습니다. 첫 요청을 보내 보세요.
              </div>
            {/if}

            {#each messagesForPane(p.target) as message, mIndex (`${mIndex}-${message.role}-${message.text}-${message.image}`)}
              <div class={`flex w-full ${message.role === "user" ? "justify-end" : message.role === "system" ? "justify-center" : "justify-start"}`}>
                <div class={`min-w-0 max-w-[92%] overflow-hidden rounded-lg px-3 py-1.5 shadow-sm ring-1 ${
                  message.role === "user"
                    ? "bg-blue-50 text-blue-950 ring-blue-200"
                    : message.role === "system"
                      ? "bg-amber-50 text-amber-900 ring-amber-200"
                      : "bg-white/95 text-slate-900 ring-slate-200"
                }`}>
                  {#if message.text}
                    {#if message.role === "user"}
                      <div class="whitespace-pre-wrap break-words text-[13px] leading-4">{message.text}</div>
                    {:else}
                      <div class="markdown-body text-[13px] leading-4" use:renderMarkdownInto={message.text}></div>
                    {/if}
                  {/if}
                  {#if message.image}
                    <img
                      alt=""
                      class="mt-2 max-h-[420px] w-auto max-w-full rounded-lg border border-slate-200"
                      src={`data:image/png;base64,${message.image}`}
                    />
                  {/if}
                </div>
              </div>
            {/each}
          </div>
        </div>
        {#if !atBottom && messagesForPane(p.target).length > 0}
          <button
            class="absolute bottom-3 right-4 grid h-8 w-8 place-items-center rounded-full border border-slate-200 bg-white text-sm text-slate-600 shadow-md hover:bg-slate-50"
            onclick={() => scrollPaneLogToBottom(p.id)}
            title="마지막 메시지로 이동"
            aria-label="마지막 메시지로 이동"
          >
            ↓
          </button>
        {/if}
      </div>

      {#if paneWorking(p.target)}
        <div class="relative shrink-0" data-progress-popup>
          {#if chat.progressPopupPaneId === p.id}
            <div
              bind:this={progressLogEl}
              class="absolute bottom-full left-4 right-4 mb-2 max-h-64 overflow-y-auto rounded-lg border border-slate-200 bg-slate-950 p-3 font-mono text-[12px] leading-5 text-slate-100 shadow-lg"
            >
              {#each progressForTarget(p.target) as line, index (index)}
                <div class="whitespace-pre-wrap break-words">{line}</div>
              {/each}
              {#if progressForTarget(p.target).length === 0}
                <div class="text-slate-400">아직 진행 메시지가 없습니다.</div>
              {/if}
            </div>
          {/if}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div
            class="flex cursor-pointer items-center gap-3 border-t border-slate-200/80 bg-white/80 px-4 py-2 text-xs text-slate-600 backdrop-blur hover:bg-slate-100"
            onclick={() => toggleProgressPopup(p.id)}
            title="클릭하면 진행 메시지 보기"
          >
            <div class="flex items-center gap-1.5">
              <span class="h-2 w-2 animate-pulse rounded-full bg-blue-500"></span>
              <span class="h-2 w-2 animate-pulse rounded-full bg-blue-400 [animation-delay:150ms]"></span>
              <span class="h-2 w-2 animate-pulse rounded-full bg-blue-300 [animation-delay:300ms]"></span>
            </div>
            <span>{paneWorkingText(p.target)}</span>
            {#if progressForTarget(p.target).length > 0}
              <span class="ml-auto shrink-0 text-[11px] font-semibold text-blue-600">
                진행 메시지 {progressForTarget(p.target).length}건 {chat.progressPopupPaneId === p.id ? "▾" : "▸"}
              </span>
            {/if}
          </div>
        </div>
      {/if}

      <div class="shrink-0 border-t border-slate-200/80 bg-white/92 px-4 py-3 backdrop-blur relative">
        {#if showCommandMenuForPane(p)}
          <div
            class="absolute bottom-full left-4 right-4 mb-2 max-h-64 overflow-y-auto rounded-lg border border-slate-200 bg-white shadow-lg"
            role="listbox"
          >
            {#each commandCandidatesForPane(p) as command, cIndex (command.cmd)}
              <button
                type="button"
                class={`flex w-full items-baseline gap-3 px-3 py-2 text-left ${cIndex === p.highlightedCommandIndex ? "bg-blue-50" : "hover:bg-slate-50"}`}
                role="option"
                aria-selected={cIndex === p.highlightedCommandIndex}
                onmouseenter={() => updatePane(p.id, { highlightedCommandIndex: cIndex })}
                onclick={() => selectCommandForPane(p.id, command)}
              >
                <span class="shrink-0 font-mono text-[13px] font-semibold text-blue-700">{command.cmd}</span>
                <span class="min-w-0 flex-1 truncate text-[12px] text-slate-500">{command.desc}</span>
              </button>
            {/each}
          </div>
        {/if}

        <div class="flex w-full items-end gap-3">
          <button
            class="grid h-[42px] w-[42px] shrink-0 place-items-center rounded-md border border-slate-300 bg-white text-base text-slate-700 hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50"
            onclick={() => pickAttachment(p.id)}
            disabled={!p.target}
            title="파일 첨부"
            aria-label="파일 첨부"
          >
            📎
          </button>

          {#each p.attachments as attachment, aIndex (attachment.id)}
            {#if attachment.previewURL}
              <img
                alt=""
                class="h-8 w-8 shrink-0 self-center rounded-md border border-slate-300 object-cover"
                src={attachment.previewURL}
              />
            {/if}
            <span
              class="max-w-[160px] shrink-0 self-center truncate rounded-full bg-slate-200 px-2 py-1 text-[11px] font-medium text-slate-600"
              title={attachment.name}
            >
              {attachment.name}
            </span>
            <button
              class="grid h-6 w-6 shrink-0 place-items-center self-center rounded-full border border-slate-300 bg-white text-[11px] font-semibold text-slate-600 hover:bg-slate-100"
              onclick={() => clearPaneAttachment(p.id, aIndex)}
              title="첨부 취소"
              aria-label="첨부 취소"
            >
              x
            </button>
          {/each}

          <textarea
            use:registerPaneTextarea={p.id}
            class="min-h-[42px] flex-1 rounded-md border border-slate-300 bg-white px-3 py-2.5 text-sm leading-5 text-slate-900 shadow-sm outline-none placeholder:text-slate-400 focus:border-blue-400 focus:ring-4 focus:ring-blue-100 disabled:bg-slate-100"
            placeholder={p.target ? "메시지 또는 !명령을 입력하세요" : "먼저 대화를 선택하세요"}
            value={p.composerText}
            disabled={!p.target}
            rows="1"
            oninput={(event) => setPaneComposerValue(p.id, event.currentTarget.value)}
            onkeydown={(event) => handleComposerKeydown(p.id, event)}
            onpaste={(event) => handleComposerPaste(p.id, event)}
            onfocus={() => focusPane(p.id)}
          ></textarea>

          <button
            class="grid h-[42px] w-[46px] shrink-0 place-items-center rounded-md bg-blue-600 text-base font-semibold text-white shadow-sm hover:bg-blue-700 disabled:cursor-not-allowed disabled:bg-slate-300"
            onclick={() => sendFromPane(p.id)}
            disabled={(p.attachments.length === 0 && !canSendPane(p)) || !chat.connected}
            title={p.attachments.length > 0 ? "업로드" : "전송"}
            aria-label={p.attachments.length > 0 ? "업로드" : "전송"}
          >
            {p.attachments.length > 0 ? "⇪" : "➤"}
          </button>
        </div>
      </div>
    </main>
  {/if}
{/if}

<style>
  :global(.markdown-body) {
    white-space: normal;
    overflow-x: auto;
  }
  :global(.markdown-body > :first-child) {
    margin-top: 0;
  }
  :global(.markdown-body > :last-child) {
    margin-bottom: 0;
  }
  :global(.markdown-body p) {
    margin: 0 0 6px;
  }
  :global(.markdown-body h1),
  :global(.markdown-body h2),
  :global(.markdown-body h3),
  :global(.markdown-body h4),
  :global(.markdown-body h5),
  :global(.markdown-body h6) {
    margin: 8px 0 4px;
    line-height: 1.3;
    font-weight: 600;
  }
  :global(.markdown-body h1) {
    font-size: 15px;
  }
  :global(.markdown-body h2) {
    font-size: 14px;
  }
  :global(.markdown-body h3),
  :global(.markdown-body h4),
  :global(.markdown-body h5),
  :global(.markdown-body h6) {
    font-size: 13px;
  }
  :global(.markdown-body ul),
  :global(.markdown-body ol) {
    margin: 0 0 6px;
    padding-left: 20px;
  }
  :global(.markdown-body li) {
    margin: 1px 0;
  }
  :global(.markdown-body blockquote) {
    margin: 0 0 6px;
    padding: 2px 0 2px 10px;
    border-left: 3px solid #cbd5e1;
    color: #475569;
  }
  :global(.markdown-body hr) {
    margin: 8px 0;
    border: 0;
    border-top: 1px solid #e2e8f0;
  }
  :global(.markdown-body table) {
    margin: 0 0 6px;
    border-collapse: collapse;
    font-size: 12px;
    line-height: 1.4;
  }
  :global(.markdown-body th),
  :global(.markdown-body td) {
    padding: 4px 6px;
    border: 1px solid #cbd5e1;
    text-align: left;
    vertical-align: top;
    white-space: nowrap;
  }
  :global(.markdown-body th) {
    background: #f8fafc;
    font-weight: 600;
    color: #334155;
  }
  :global(.markdown-body a) {
    color: #1d4ed8;
    text-decoration: underline;
  }
  :global(.markdown-body code) {
    padding: 1px 4px;
    border-radius: 4px;
    background: #f1f5f9;
    font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
    font-size: 12px;
  }
  :global(.markdown-body pre) {
    margin: 0 0 6px;
    padding: 8px 10px;
    border-radius: 6px;
    background: #0f172a;
    overflow-x: auto;
  }
  :global(.markdown-body pre code) {
    display: block;
    padding: 0;
    background: transparent;
    color: #e2e8f0;
    white-space: pre;
  }
</style>
