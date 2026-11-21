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
    backendModelLabel,
    openLightbox,
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
    adjustPaneTimeout,
    cancelPane,
    paneColor,
    scrollPaneLogToBottom,
    recordPaneLogScroll,
    NEAR_BOTTOM_PX,
    togglePaneBackendMenu,
    setTargetBackend,
    togglePaneWorkDirMenu,
    setTargetWorkDir,
    editIntoComposer,
    resendFromPane,
    quoteIntoComposer,
    conversationMarkdown,
    showToast,
  } from "./paneStore.svelte.js";
  import { renderMarkdown } from "./markdown.js";
  import { voice, sendNow, skipReading, toggleVoiceInput, setSpeakEnabled, stopSpeaking, clearVoiceNote, installVoice, cancelInstallVoice, toggleConversation, isTypedCommand, runTypedCommand } from "./voice.svelte.js";

  let { node } = $props();

  // Drives the "jump to latest" FAB: hidden while the log is already near
  // its bottom, shown once the user scrolls up to read earlier messages.
  let atBottom = $state(true);

  function handleLogScroll(event) {
    const el = event.currentTarget;
    atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM_PX;
    recordPaneLogScroll(node.paneId, el);
    quoteSel = null;
  }

  // "새 메시지 N" on the jump-to-latest button: how many messages arrived
  // since the log was last at its bottom (reset when the pane switches chats).
  let seenCount = $state(0);
  let seenKey = "";
  $effect(() => {
    if (node.type !== "leaf") return;
    const target = pane(node.paneId)?.target;
    const key = target ? `${target.kind}:${target.id || ""}` : "";
    const n = messagesForPane(target).length;
    if (atBottom || key !== seenKey) {
      seenKey = key;
      seenCount = n;
    }
  });

  // Select text in the log → a floating "인용" button adds it to the composer
  // as a quote, so a follow-up can point at the exact part of an answer.
  let quoteSel = $state(null); // { text, x, y } relative to the log wrapper

  function handleLogMouseUp(event) {
    const wrap = event.currentTarget;
    setTimeout(() => {
      const sel = window.getSelection();
      const text = sel && sel.rangeCount > 0 ? sel.toString().trim() : "";
      if (!text) { quoteSel = null; return; }
      const range = sel.getRangeAt(0);
      if (!wrap.contains(range.commonAncestorContainer)) { quoteSel = null; return; }
      const r = range.getBoundingClientRect();
      const w = wrap.getBoundingClientRect();
      quoteSel = {
        text,
        x: Math.min(Math.max(r.left - w.left + r.width / 2, 48), w.width - 48),
        y: r.top - w.top > 40 ? r.top - w.top - 34 : r.bottom - w.top + 6,
      };
    }, 0);
  }

  function useQuote(paneId) {
    if (!quoteSel) return;
    const text = quoteSel.text;
    quoteSel = null;
    window.getSelection()?.removeAllRanges();
    void quoteIntoComposer(paneId, text);
  }

  // How long the answer took: from the user message that started the turn to
  // the turn's last reply bubble (shown once per turn, under that bubble).
  function replyDuration(messages, index) {
    const m = messages[index];
    if (m?.role !== "assistant" || !m.at) return "";
    for (let j = index + 1; j < messages.length; j++) {
      const next = messages[j];
      if (next.role === "narration") continue;
      if (next.role === "assistant") return "";
      break;
    }
    for (let j = index - 1; j >= 0; j--) {
      if (messages[j].role !== "user") continue;
      const ms = m.at - (messages[j].at || 0);
      if (!messages[j].at || ms < 1000 || ms > 6 * 3600 * 1000) return "";
      const sec = Math.round(ms / 1000);
      return sec < 60 ? `${sec}초` : `${Math.floor(sec / 60)}분 ${sec % 60}초`;
    }
    return "";
  }

  function exportConversation(target) {
    navigator.clipboard?.writeText(conversationMarkdown(target))
      .then(() => showToast("대화 전체를 마크다운으로 복사했습니다"))
      .catch(() => showToast("복사하지 못했습니다"));
  }

  // User bubbles stay verbatim (typed text shown as typed); assistant/system
  // output is Markdown and gets rendered, matching aglink-chat's web/app.js
  // add(). Only ever touches textContent inside renderMarkdown — never
  // innerHTML — so model output can't inject markup into the page.
  // No declared fence language reaches here (markdown.js drops the info
  // string and its output is test-pinned — see verify-markdown.mjs), so this
  // is a language-agnostic tokenizer rather than a per-language highlighter:
  // comments/strings/numbers/keywords across the common C-like/Python/shell
  // syntax families, which covers most snippets a backend actually sends.
  const CODE_KEYWORDS = new Set([
    "if", "else", "elif", "elsif", "unless", "for", "foreach", "while", "do", "return",
    "function", "def", "fn", "func", "class", "struct", "impl", "interface", "enum", "type",
    "const", "let", "var", "static", "public", "private", "protected", "pub",
    "import", "export", "from", "as", "use", "mod", "namespace", "package", "require", "include",
    "async", "await", "yield", "lambda", "with", "global", "nonlocal",
    "try", "catch", "except", "finally", "throw", "raise",
    "new", "this", "self", "super", "match", "switch", "case", "default", "break", "continue",
    "typeof", "instanceof", "extends", "implements", "in", "of", "range", "defer", "chan", "go",
    "None", "True", "False", "true", "false", "null", "nil", "undefined", "void",
    "int", "string", "bool", "float", "double", "end", "then", "module",
  ]);

  // Builds highlighted spans from plain text using only createTextNode/
  // textContent (same no-innerHTML guarantee as markdown.js), so a code block
  // containing "<script>" etc. still can't inject into the page.
  function highlightCode(code) {
    const text = code.textContent || "";
    const n = text.length;
    const isIdentStart = (c) => /[A-Za-z_$]/.test(c);
    const isIdentPart = (c) => /[A-Za-z0-9_$]/.test(c);
    const isDigit = (c) => c >= "0" && c <= "9";
    const frag = document.createDocumentFragment();
    const push = (cls, str) => {
      if (!str) return;
      if (!cls) { frag.appendChild(document.createTextNode(str)); return; }
      const span = document.createElement("span");
      span.className = cls;
      span.textContent = str;
      frag.appendChild(span);
    };
    let i = 0;
    while (i < n) {
      const c = text[i];
      if (c === "/" && text[i + 1] === "/") {
        let j = i; while (j < n && text[j] !== "\n") j++;
        push("tok-comment", text.slice(i, j)); i = j; continue;
      }
      if (c === "#") {
        let j = i; while (j < n && text[j] !== "\n") j++;
        push("tok-comment", text.slice(i, j)); i = j; continue;
      }
      if (c === "/" && text[i + 1] === "*") {
        let j = i + 2; while (j < n && !(text[j] === "*" && text[j + 1] === "/")) j++;
        j = Math.min(j + 2, n);
        push("tok-comment", text.slice(i, j)); i = j; continue;
      }
      if (c === '"' || c === "'" || c === "`") {
        const quote = c; let j = i + 1;
        while (j < n && text[j] !== quote) { if (text[j] === "\\") j++; j++; }
        j = Math.min(j + 1, n);
        push("tok-string", text.slice(i, j)); i = j; continue;
      }
      if (isDigit(c)) {
        let j = i + 1; while (j < n && /[0-9a-fA-FxX._]/.test(text[j])) j++;
        push("tok-number", text.slice(i, j)); i = j; continue;
      }
      if (isIdentStart(c)) {
        let j = i + 1; while (j < n && isIdentPart(text[j])) j++;
        const word = text.slice(i, j);
        push(CODE_KEYWORDS.has(word) ? "tok-keyword" : "", word); i = j; continue;
      }
      let j = i + 1;
      while (j < n && !"/#\"'`".includes(text[j]) && !isDigit(text[j]) && !isIdentStart(text[j])) j++;
      push("", text.slice(i, j)); i = j;
    }
    code.replaceChildren(frag);
  }

  // Code fences come out of markdown.js as plain <pre><code> (shared,
  // test-pinned structure — see markdown.js/verify-markdown.mjs), so the
  // copy button and highlighting are bolted on here instead of changing that
  // shared renderer.
  function decorateCodeBlocks(root) {
    root.querySelectorAll("pre").forEach((pre) => {
      const code = pre.querySelector("code");
      const text = code ? code.textContent : pre.textContent;
      if (code) highlightCode(code);
      const wrap = document.createElement("div");
      wrap.className = "md-code-wrap";
      pre.replaceWith(wrap);
      wrap.appendChild(pre);
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "md-code-copy";
      btn.textContent = "복사";
      btn.title = "코드 복사";
      btn.setAttribute("aria-label", "코드 복사");
      btn.addEventListener("click", (event) => {
        event.stopPropagation();
        navigator.clipboard?.writeText(text || "").then(() => {
          btn.textContent = "복사됨";
          btn.classList.add("md-code-copy-done");
          setTimeout(() => {
            btn.textContent = "복사";
            btn.classList.remove("md-code-copy-done");
          }, 1200);
        }).catch(() => {});
      });
      wrap.appendChild(btn);
    });
  }

  function renderMarkdownInto(el, text) {
    function update(value) {
      el.replaceChildren(renderMarkdown(value || ""));
      decorateCodeBlocks(el);
    }
    update(text);
    return { update };
  }

  function copyMessageText(text, btnEl) {
    navigator.clipboard?.writeText(text || "").then(() => {
      if (!btnEl) return;
      const copyIcon = btnEl.querySelector(".copy-icon");
      const checkIcon = btnEl.querySelector(".check-icon");
      copyIcon?.classList.add("hidden");
      checkIcon?.classList.remove("hidden");
      setTimeout(() => {
        copyIcon?.classList.remove("hidden");
        checkIcon?.classList.add("hidden");
      }, 1000);
    }).catch(() => {});
  }

  // --- Message time + day separators (at = unix ms; 0 for turns stored before
  // the host sent times, which then just show no time). ---
  function messageTime(at) {
    if (!at) return "";
    return new Date(at).toLocaleTimeString("ko-KR", { hour: "numeric", minute: "2-digit" });
  }

  function dayKey(at) {
    const d = new Date(at);
    return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
  }

  function dayLabel(at) {
    const d = new Date(at);
    const now = new Date();
    const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1);
    if (dayKey(at) === dayKey(now.getTime())) return "오늘";
    if (dayKey(at) === dayKey(yesterday.getTime())) return "어제";
    const opts = { month: "long", day: "numeric", weekday: "short" };
    if (d.getFullYear() !== now.getFullYear()) opts.year = "numeric";
    return d.toLocaleDateString("ko-KR", opts);
  }

  function showDaySeparator(messages, index) {
    const at = messages[index]?.at;
    if (!at) return false;
    for (let i = index - 1; i >= 0; i--) {
      if (messages[i].at) return dayKey(messages[i].at) !== dayKey(at);
    }
    return true;
  }

  // --- Long replies start folded so one huge answer doesn't bury the log. ---
  const LONG_CHARS = 2400;
  const LONG_LINES = 40;
  let expanded = $state(new Set());

  function isLong(message) {
    if (message.role === "user" || message.role === "narration") return false;
    const text = message.text || "";
    return text.length > LONG_CHARS || text.split("\n").length > LONG_LINES;
  }

  function foldKey(message, index) {
    return `${index}:${message.at || 0}:${(message.text || "").length}`;
  }

  function toggleExpanded(key) {
    const next = new Set(expanded);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    expanded = next;
  }

  // --- In-conversation search (Ctrl+F on the focused pane). ---
  let searchOpen = $state(false);
  let searchQuery = $state("");
  let searchCursor = $state(0);
  let searchInput = $state(null);

  function searchMatches(messages) {
    const q = searchQuery.trim().toLowerCase();
    if (!searchOpen || !q) return [];
    const hits = [];
    messages.forEach((m, i) => {
      if ((m.text || "").toLowerCase().includes(q)) hits.push(i);
    });
    return hits;
  }

  function openSearch() {
    searchOpen = true;
    requestAnimationFrame(() => {
      searchInput?.focus();
      searchInput?.select();
    });
  }

  function closeSearch() {
    searchOpen = false;
    searchQuery = "";
    searchCursor = 0;
  }

  function scrollToMessage(index) {
    const el = document.querySelector(`[data-pane-id="${node.paneId}"] [data-msg-index="${index}"]`);
    el?.scrollIntoView({ block: "center", behavior: "smooth" });
  }

  function moveSearch(messages, step) {
    const hits = searchMatches(messages);
    if (hits.length === 0) return;
    searchCursor = (searchCursor + step + hits.length) % hits.length;
    scrollToMessage(hits[searchCursor]);
  }

  function handleWindowKeydown(event) {
    if (node.type !== "leaf" || chat.focusedPaneId !== node.paneId) return;
    if (event.ctrlKey && !event.shiftKey && !event.altKey && event.key.toLowerCase() === "f") {
      event.preventDefault();
      openSearch();
    }
  }

  // Starters for an empty conversation; clicking one only fills the composer.
  const STARTERS = [
    { label: "열린 창 목록 보기", text: "지금 화면에 열린 창 목록을 알려줘" },
    { label: "크롬 탭 요약", text: "크롬에 열려 있는 탭들을 요약해줘" },
    { label: "오늘 할 일 정리", text: "오늘 할 일을 정리해줘" },
    { label: "명령어 목록", text: "!" },
  ];
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

<svelte:window onkeydown={handleWindowKeydown} />

<!-- Compact inline marker on voice-transcribed user messages (message.voice). -->
{#snippet voiceBadge()}
  <span
    class="mr-1 inline-flex h-4 w-4 select-none items-center justify-center rounded-full bg-blue-100 align-[-3px] text-blue-600 dark:bg-blue-900/50 dark:text-blue-300"
    title="음성 입력"
    aria-label="음성 입력"
    role="img"
  ><svg class="h-2.5 w-2.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="2" width="6" height="12" rx="3"/><path d="M5 10a7 7 0 0 0 14 0"/><path d="M12 17v4"/></svg></span>
{/snippet}

<!-- Composer/header icon set — replaces the previous emoji glyphs so every
     button in the toolbar reads as one consistent icon style. -->
{#snippet iconAttach()}
  <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21.44 11.05l-9.19 9.19a5.5 5.5 0 0 1-7.78-7.78l9.19-9.19a3.5 3.5 0 0 1 4.95 4.95l-9.2 9.19a1.5 1.5 0 0 1-2.12-2.12l8.49-8.48"/></svg>
{/snippet}

{#snippet iconMic()}
  <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="2" width="6" height="12" rx="3"/><path d="M5 10a7 7 0 0 0 14 0"/><path d="M12 17v4"/><path d="M8 21h8"/></svg>
{/snippet}

{#snippet iconChat()}
  <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z"/></svg>
{/snippet}

{#snippet iconSpeakerOn()}
  <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/><path d="M18.5 5.5a9 9 0 0 1 0 13"/></svg>
{/snippet}

{#snippet iconSpeakerOff()}
  <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><line x1="22" y1="9" x2="16" y2="15"/><line x1="16" y1="9" x2="22" y2="15"/></svg>
{/snippet}

{#snippet iconStop()}
  <svg class="h-4 w-4" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><rect x="6" y="6" width="12" height="12" rx="2"/></svg>
{/snippet}

{#snippet iconSend()}
  <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><line x1="22" y1="2" x2="11" y2="13"/><polygon points="22 2 15 22 11 13 2 9 22 2"/></svg>
{/snippet}

{#snippet iconUpload()}
  <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>
{/snippet}

{#snippet iconClose()}
  <svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
{/snippet}

{#snippet iconCopy()}
  <svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
{/snippet}

{#snippet iconEdit()}
  <svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4 12.5-12.5z"/></svg>
{/snippet}

{#snippet iconRetry()}
  <svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="1 4 1 10 7 10"/><path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10"/></svg>
{/snippet}

{#snippet iconSearch()}
  <svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
{/snippet}

{#snippet iconChevron(up)}
  <svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points={up ? "18 15 12 9 6 15" : "6 9 12 15 18 9"}/></svg>
{/snippet}

{#snippet iconExport()}
  <svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>
{/snippet}

{#snippet iconQuote()}
  <svg class="h-3 w-3" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M7.2 6C4.9 6 3 7.9 3 10.2c0 2.1 1.5 3.8 3.5 4.1-.3 1.5-1.2 2.6-2.6 3.2l.7 1.5C8.3 18 10.5 15.4 10.5 11V9.7C10.5 7.7 9.1 6 7.2 6zm10 0C14.9 6 13 7.9 13 10.2c0 2.1 1.5 3.8 3.5 4.1-.3 1.5-1.2 2.6-2.6 3.2l.7 1.5c3.7-1 5.9-3.6 5.9-8V9.7C20.5 7.7 19.1 6 17.2 6z"/></svg>
{/snippet}

{#snippet iconCheck()}
  <svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="20 6 9 17 4 12"/></svg>
{/snippet}

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
              ? "pointer-events-none absolute left-0 right-0 top-1/2 h-px -translate-y-1/2 bg-slate-200 dark:bg-slate-700 group-hover:bg-blue-300"
              : "pointer-events-none absolute bottom-0 left-1/2 top-0 w-px -translate-x-1/2 bg-slate-200 dark:bg-slate-700 group-hover:bg-blue-300"}
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
      class={`relative flex min-w-0 min-h-0 flex-1 flex-col overflow-hidden ${color ? `border ${chat.panes.length > 1 && p.id === chat.focusedPaneId ? "border-blue-200 dark:border-blue-800/40" : color.border}` : ""}`}
      onclick={() => focusPane(p.id)}
      data-file-drop-target
      data-pane-id={p.id}
      style={`--msg-scale:${chat.fontScale}`}
      ondragover={(event) => handlePaneDragOver(event, p.id)}
      ondragleave={() => handlePaneDragLeave(p.id)}
      ondrop={(event) => handlePaneDrop(event, p.id)}
    >
      {#if chat.overPaneId === p.id}
        <div
          class="pointer-events-none absolute z-30 rounded-md border-2 border-blue-500 dark:border-blue-400 bg-blue-400/25"
          style={dockOverlayStyle(chat.overZone)}
        ></div>
      {/if}

      <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div
        class={`relative z-30 flex h-10 shrink-0 cursor-grab items-center gap-2 border-b border-slate-200 dark:border-slate-700 px-4 backdrop-blur active:cursor-grabbing ${color ? color.headerBg : "bg-white/75 dark:bg-slate-900/75"}`}
        draggable="true"
        ondragstart={(event) => handlePaneDragStart(event, p.id)}
        ondragend={handlePaneDragEnd}
        title="드래그해서 다른 패널 가장자리로 재배치"
      >
        {#if color}
          <span class={`h-2 w-2 shrink-0 rounded-full ${color.dot}`}></span>
        {/if}
        {#if paneConversationMeta(p.target)}
          <span class={`inline-flex shrink-0 whitespace-nowrap rounded px-1.5 py-px text-[10px] font-semibold leading-4 ${paneConversationMeta(p.target).kind === "web" ? "bg-blue-100 dark:bg-blue-900/40 text-blue-700 dark:text-blue-300" : "bg-sky-100 dark:bg-sky-900/40 text-sky-700 dark:text-sky-300"}`}>
            {paneConversationMeta(p.target).kind === "web" ? "로컬" : "텔레그램"}
          </span>
          <!-- One compact line: title #id · backend model · workdir. Nothing wraps;
               the title and work folder truncate (full text in their tooltips). -->
          <div class="flex min-w-0 flex-1 items-center gap-1.5 whitespace-nowrap text-[11px] leading-5">
            <div class="min-w-[2.5rem] shrink truncate text-sm font-semibold text-slate-900 dark:text-slate-100" title={paneConversationMeta(p.target).title}>{paneConversationMeta(p.target).title}</div>
            <span class="shrink-0 font-mono text-slate-400 dark:text-slate-500">#{paneConversationMeta(p.target).id}</span>
            <span class="shrink-0 select-none text-slate-300 dark:text-slate-600" aria-hidden="true">·</span>
            <div class="relative shrink-0" data-pane-backend-menu>
              <button
                type="button"
                draggable="false"
                class="rounded bg-slate-200/80 dark:bg-slate-700/80 px-1.5 py-px text-[11px] font-semibold leading-4 text-slate-700 dark:text-slate-300 hover:bg-slate-300 dark:hover:bg-slate-600"
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
                <div class="absolute left-0 top-6 z-40 w-32 rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 p-1.5 shadow-xl">
                  <button class="block w-full rounded-md px-3 py-2 text-left text-xs font-medium text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={(event) => { event.stopPropagation(); void setTargetBackend(p.target, "default"); }}>Default</button>
                  <button class="block w-full rounded-md px-3 py-2 text-left text-xs font-medium text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={(event) => { event.stopPropagation(); void setTargetBackend(p.target, "claude"); }}>Claude</button>
                  <button class="block w-full rounded-md px-3 py-2 text-left text-xs font-medium text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={(event) => { event.stopPropagation(); void setTargetBackend(p.target, "codex"); }}>Codex</button>
                  <button class="block w-full rounded-md px-3 py-2 text-left text-xs font-medium text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={(event) => { event.stopPropagation(); void setTargetBackend(p.target, "opencode"); }}>OpenCode</button>
                </div>
              {/if}
            </div>
            {#if backendModelLabel(paneConversationMeta(p.target).backend)}
              <span
                class="max-w-[8rem] shrink-0 truncate font-mono text-[11px] text-slate-500 dark:text-slate-400"
                title={`연결된 LLM: ${backendModelLabel(paneConversationMeta(p.target).backend)}`}
              >
                {backendModelLabel(paneConversationMeta(p.target).backend)}
              </span>
            {/if}
            {#if paneConversationMeta(p.target).kind === "web"}
              <span class="shrink-0 select-none text-slate-300 dark:text-slate-600" aria-hidden="true">·</span>
              <div class="relative min-w-0 shrink-[2]" data-pane-workdir-menu>
                <button
                  type="button"
                  draggable="false"
                  class={`block w-full max-w-[260px] truncate rounded px-1.5 py-px text-left text-[11px] leading-4 ${paneConversationMeta(p.target).workDir ? "bg-emerald-50 dark:bg-emerald-950/40 text-emerald-700 dark:text-emerald-300 hover:bg-emerald-100 dark:hover:bg-emerald-900/50" : "bg-slate-100 dark:bg-slate-800 text-slate-500 dark:text-slate-400 hover:bg-slate-200 dark:hover:bg-slate-600"}`}
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
                  <div class="absolute left-0 top-6 z-40 w-80 rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 p-2 shadow-xl">
                    <div class="px-2 pb-1 text-[11px] font-semibold uppercase tracking-wide text-slate-400 dark:text-slate-500">작업 폴더</div>
                    <div class="max-h-28 overflow-y-auto break-all rounded-md bg-slate-50 dark:bg-slate-800 px-2 py-2 text-xs leading-5 text-slate-700 dark:text-slate-300">
                      {paneConversationMeta(p.target).workDir || "설정되지 않음"}
                    </div>
                    <button
                      class="mt-1 block w-full rounded-md px-2 py-2 text-left text-xs font-medium text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700"
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
          <div class="min-w-0 flex-1 text-sm text-slate-500 dark:text-slate-400">대화를 선택하세요.</div>
        {/if}
        {#if p.target && messagesForPane(p.target).length > 0}
          <button
            type="button"
            draggable="false"
            class="grid h-7 w-7 shrink-0 place-items-center rounded-md text-slate-500 dark:text-slate-400 transition-colors hover:bg-slate-100 dark:hover:bg-slate-700"
            onclick={(event) => { event.stopPropagation(); exportConversation(p.target); }}
            title="대화 전체를 마크다운으로 복사"
            aria-label="대화 내보내기"
          >{@render iconExport()}</button>
        {/if}
        {#if p.target}
          <button
            type="button"
            draggable="false"
            class={`grid h-7 w-7 shrink-0 place-items-center rounded-md text-slate-500 dark:text-slate-400 transition-colors hover:bg-slate-100 dark:hover:bg-slate-700 ${searchOpen ? "bg-slate-100 text-blue-600 dark:bg-slate-700 dark:text-blue-300" : ""}`}
            onclick={(event) => { event.stopPropagation(); focusPane(p.id); if (searchOpen) closeSearch(); else openSearch(); }}
            title="대화 안에서 찾기 (Ctrl+F)"
            aria-label="대화 안에서 찾기"
            aria-pressed={searchOpen}
          >{@render iconSearch()}</button>
        {/if}
        {#if chat.panes.length > 1}
          <button
            class="grid h-7 w-7 shrink-0 place-items-center rounded-md border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 text-slate-600 dark:text-slate-400 transition-colors hover:bg-slate-100 dark:hover:bg-slate-700"
            onclick={(event) => { event.stopPropagation(); closePane(p.id); }}
            title="분할 닫기"
            aria-label="분할 닫기"
          >
            {@render iconClose()}
          </button>
        {/if}
      </div>

      {#if searchOpen}
        {@const hits = searchMatches(messagesForPane(p.target))}
        <div class="relative z-20 flex shrink-0 items-center gap-2 border-b border-slate-200 dark:border-slate-700 bg-white/90 dark:bg-slate-900/90 px-4 py-1.5 backdrop-blur">
          <span class="text-slate-400 dark:text-slate-500">{@render iconSearch()}</span>
          <input
            bind:this={searchInput}
            class="min-w-0 flex-1 bg-transparent text-[13px] text-slate-800 dark:text-slate-200 outline-none placeholder:text-slate-400 dark:placeholder:text-slate-500"
            placeholder="이 대화에서 찾기"
            value={searchQuery}
            oninput={(event) => {
              searchQuery = event.currentTarget.value;
              searchCursor = 0;
              const first = searchMatches(messagesForPane(p.target))[0];
              if (first !== undefined) scrollToMessage(first);
            }}
            onkeydown={(event) => {
              if (event.key === "Enter") { event.preventDefault(); moveSearch(messagesForPane(p.target), event.shiftKey ? -1 : 1); }
              else if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); closeSearch(); }
            }}
          />
          <span class="shrink-0 text-[11px] tabular-nums text-slate-500 dark:text-slate-400">
            {searchQuery.trim() ? (hits.length ? `${searchCursor + 1} / ${hits.length}` : "없음") : ""}
          </span>
          <button type="button" class="msg-action-btn disabled:opacity-40" disabled={hits.length === 0} onclick={() => moveSearch(messagesForPane(p.target), -1)} title="이전 (Shift+Enter)" aria-label="이전 찾기">{@render iconChevron(true)}</button>
          <button type="button" class="msg-action-btn disabled:opacity-40" disabled={hits.length === 0} onclick={() => moveSearch(messagesForPane(p.target), 1)} title="다음 (Enter)" aria-label="다음 찾기">{@render iconChevron(false)}</button>
          <button type="button" class="msg-action-btn" onclick={closeSearch} title="닫기 (Esc)" aria-label="찾기 닫기">{@render iconClose()}</button>
        </div>
      {/if}

      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="relative flex min-h-0 flex-1 flex-col overflow-hidden" onmouseup={handleLogMouseUp}>
        {#if quoteSel}
          <button
            type="button"
            class="absolute z-30 flex -translate-x-1/2 items-center gap-1 rounded-full bg-slate-900 px-2.5 py-1 text-[11px] font-semibold text-white shadow-lg transition hover:bg-blue-600 dark:bg-slate-100 dark:text-slate-900 dark:hover:bg-blue-300"
            style={`left:${quoteSel.x}px; top:${quoteSel.y}px`}
            onmousedown={(event) => event.preventDefault()}
            onmouseup={(event) => event.stopPropagation()}
            onclick={(event) => { event.stopPropagation(); useQuote(p.id); }}
            title="선택한 부분을 인용해서 입력창에 넣습니다"
          >{@render iconQuote()} 인용해서 묻기</button>
        {/if}
        <div use:registerPaneLog={p.id} onscroll={handleLogScroll} class="flex min-h-0 flex-1 flex-col overflow-y-auto px-4 py-4">
          <div class="flex min-h-full w-full flex-col gap-1">
            {#if messagesForPane(p.target).length === 0}
              <div class="flex flex-col items-center gap-3 rounded-xl border border-dashed border-slate-300 dark:border-slate-600 bg-white/60 dark:bg-slate-900/60 px-6 py-10 text-center text-sm text-slate-500 dark:text-slate-400">
                <div>메시지가 없습니다. 첫 요청을 보내 보세요.</div>
                {#if p.target}
                  <div class="flex flex-wrap justify-center gap-2">
                    {#each STARTERS as starter (starter.label)}
                      <button
                        type="button"
                        class="rounded-full border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800 px-3 py-1 text-xs font-medium text-slate-600 dark:text-slate-300 shadow-sm transition-colors hover:border-blue-300 hover:text-blue-700 dark:hover:border-blue-700 dark:hover:text-blue-300"
                        onclick={(event) => { event.stopPropagation(); void editIntoComposer(p.id, starter.text); }}
                      >{starter.label}</button>
                    {/each}
                  </div>
                {/if}
              </div>
            {/if}

            {#each messagesForPane(p.target) as message, mIndex (`${mIndex}-${message.role}-${message.text}-${message.image}`)}
              {@const hits = searchMatches(messagesForPane(p.target))}
              {@const isHit = hits.includes(mIndex)}
              {@const isCurrentHit = isHit && hits[searchCursor] === mIndex}
              {@const long = isLong(message)}
              {@const folded = long && !expanded.has(foldKey(message, mIndex))}
              {#if showDaySeparator(messagesForPane(p.target), mIndex)}
                <div class="my-1 flex items-center gap-3 text-[11px] font-medium text-slate-400 dark:text-slate-500" role="separator">
                  <span class="h-px flex-1 bg-slate-200 dark:bg-slate-700"></span>
                  <span>{dayLabel(message.at)}</span>
                  <span class="h-px flex-1 bg-slate-200 dark:bg-slate-700"></span>
                </div>
              {/if}
              <div
                data-msg-index={mIndex}
                class={`msg-enter group/msg flex w-full flex-col ${message.role === "user" ? "items-end" : message.role === "system" ? "items-center" : "items-start"}`}
              >
                <div class={`${message.role === "narration"
                  ? "relative min-w-0 max-w-[92%] overflow-hidden border-l-2 border-blue-300 dark:border-blue-700 py-0.5 pl-2.5 pr-2 italic text-slate-500 dark:text-slate-400"
                  : `relative min-w-0 max-w-[92%] overflow-hidden rounded-lg px-3 py-1.5 shadow-sm ring-1 ${
                  message.role === "user"
                    ? "bg-blue-50 dark:bg-blue-950/40 text-blue-950 dark:text-blue-300 ring-blue-200 dark:ring-blue-900/40"
                    : message.role === "system"
                      ? "bg-amber-50 dark:bg-amber-950/40 text-amber-900 dark:text-amber-300 ring-amber-200 dark:ring-amber-800"
                      : "bg-white/95 dark:bg-slate-900/95 text-slate-900 dark:text-slate-300 ring-slate-200 dark:ring-slate-700"
                }`} ${isCurrentHit ? "search-current" : isHit ? "search-hit" : ""}`}>
                  {#if message.role === "user" && message.voice && !message.text}
                    {@render voiceBadge()}
                  {/if}
                  {#if message.text}
                    <div class={folded ? "msg-folded" : ""}>
                      {#if message.role === "user"}
                        <div class="msg-text whitespace-pre-wrap break-words">{#if message.voice}{@render voiceBadge()}{/if}{message.text}</div>
                      {:else}
                        <div class="msg-text markdown-body" use:renderMarkdownInto={message.text}></div>
                      {/if}
                    </div>
                    {#if long}
                      <button
                        type="button"
                        class="mt-1 text-[11px] font-semibold text-blue-600 hover:underline dark:text-blue-400"
                        onclick={(event) => { event.stopPropagation(); toggleExpanded(foldKey(message, mIndex)); }}
                      >{folded ? `전체 보기 (${(message.text || "").split("\n").length}줄)` : "접기"}</button>
                    {/if}
                  {/if}
                  {#if message.image}
                    <img
                      alt=""
                      class="mt-2 max-h-[420px] w-auto max-w-full cursor-zoom-in rounded-lg border border-slate-200 dark:border-slate-700 transition hover:brightness-95"
                      src={`data:image/png;base64,${message.image}`}
                      title="클릭하면 창으로 크게 봅니다 (이동·크기조절 가능)"
                      onclick={() => openLightbox(`data:image/png;base64,${message.image}`)}
                    />
                  {/if}
                </div>
                {#if message.role !== "narration" && (message.text || message.at)}
                  <div class={`mt-0.5 flex h-4 items-center gap-1 px-1 text-[10px] text-slate-400 dark:text-slate-500 ${message.role === "user" ? "flex-row-reverse" : ""}`}>
                    {#if message.at}
                      <span class="tabular-nums" title={new Date(message.at).toLocaleString("ko-KR")}>{messageTime(message.at)}</span>
                    {/if}
                    {#if replyDuration(messagesForPane(p.target), mIndex)}
                      <span class="tabular-nums" title="질문부터 이 답변까지 걸린 시간">· {replyDuration(messagesForPane(p.target), mIndex)}</span>
                    {/if}
                    {#if message.role === "user" && message.ack}
                      <span class="text-blue-500/80 dark:text-blue-400/70" title="aglink 가 메시지를 받아 작업을 시작했습니다">✓ 확인함</span>
                    {/if}
                    {#if message.text}
                      <span class={`msg-actions flex items-center gap-0.5 opacity-0 transition-opacity group-hover/msg:opacity-100 focus-within:opacity-100 ${message.role === "user" ? "flex-row-reverse" : ""}`}>
                        <button
                          type="button"
                          class="msg-action-btn"
                          onclick={(event) => { event.stopPropagation(); copyMessageText(message.text, event.currentTarget); }}
                          title="메시지 복사"
                          aria-label="메시지 복사"
                        >
                          <span class="copy-icon grid">{@render iconCopy()}</span>
                          <span class="check-icon hidden text-emerald-500">{@render iconCheck()}</span>
                        </button>
                        {#if message.role === "user"}
                          <button
                            type="button"
                            class="msg-action-btn"
                            onclick={(event) => { event.stopPropagation(); void editIntoComposer(p.id, message.text); }}
                            title="입력창에 넣어 고쳐서 다시 보내기"
                            aria-label="고쳐서 다시 보내기"
                          >{@render iconEdit()}</button>
                          <button
                            type="button"
                            class="msg-action-btn disabled:cursor-not-allowed disabled:opacity-40"
                            disabled={!chat.connected || p.attachments.length > 0}
                            onclick={(event) => { event.stopPropagation(); void resendFromPane(p.id, message.text); }}
                            title="이 메시지를 그대로 다시 보내기"
                            aria-label="다시 보내기"
                          >{@render iconRetry()}</button>
                        {/if}
                      </span>
                    {/if}
                  </div>
                {/if}
              </div>
            {/each}
          </div>
        </div>
        {#if !atBottom && messagesForPane(p.target).length > 0}
          {@const newCount = Math.max(0, messagesForPane(p.target).length - seenCount)}
          <button
            class={`absolute bottom-3 right-4 flex h-8 items-center justify-center gap-1 rounded-full border shadow-md transition-colors ${newCount > 0 ? "border-blue-500 bg-blue-600 px-3 text-xs font-semibold text-white hover:bg-blue-700" : "w-8 border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 text-slate-600 dark:text-slate-400 hover:bg-slate-50 dark:hover:bg-slate-700"}`}
            onclick={() => scrollPaneLogToBottom(p.id)}
            title="마지막 메시지로 이동"
            aria-label="마지막 메시지로 이동"
          >
            {@render iconChevron(false)}{#if newCount > 0}<span>새 메시지 {newCount}</span>{/if}
          </button>
        {/if}
      </div>

      {#if paneWorking(p.target)}
        <div class="relative shrink-0" data-progress-popup>
          {#if chat.progressPopupPaneId === p.id}
            <div
              bind:this={progressLogEl}
              class="absolute bottom-full left-4 right-4 mb-2 max-h-64 overflow-y-auto rounded-lg border border-slate-200 dark:border-slate-700 bg-slate-950 p-3 font-mono text-[12px] leading-5 text-slate-100 shadow-lg"
            >
              {#each progressForTarget(p.target) as line, index (index)}
                <div class="whitespace-pre-wrap break-words">{line}</div>
              {/each}
              {#if progressForTarget(p.target).length === 0}
                <div class="text-slate-400 dark:text-slate-500">아직 진행 메시지가 없습니다.</div>
              {/if}
            </div>
          {/if}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div
            class="flex cursor-pointer items-center gap-2 border-t border-slate-200/80 dark:border-slate-700/80 bg-white/80 dark:bg-slate-900/80 px-4 py-1 text-xs text-slate-600 dark:text-slate-400 backdrop-blur hover:bg-slate-100 dark:hover:bg-slate-700"
            onclick={() => toggleProgressPopup(p.id)}
            title="클릭하면 진행 메시지 보기"
          >
            <div class="flex items-center gap-1.5">
              <span class="h-2 w-2 animate-pulse rounded-full bg-blue-500"></span>
              <span class="h-2 w-2 animate-pulse rounded-full bg-blue-400 [animation-delay:150ms]"></span>
              <span class="h-2 w-2 animate-pulse rounded-full bg-blue-300 [animation-delay:300ms]"></span>
            </div>
            <span>{paneWorkingText(p.target)}</span>
            <select
              class="ml-auto shrink-0 cursor-pointer rounded border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-1.5 py-0.5 text-[11px] text-slate-600 dark:text-slate-400 hover:border-slate-400"
              title="이 작업의 제한 시간만 조절합니다 (기본 설정값 미만으로는 줄지 않습니다)"
              onclick={(e) => e.stopPropagation()}
              onchange={(e) => {
                const v = e.currentTarget.value;
                e.currentTarget.value = "";
                if (v) adjustPaneTimeout(p.id, v);
              }}
            >
              <option value="" selected>⏱ 제한시간</option>
              <optgroup label="연장">
                <option value="+5">+5분</option>
                <option value="+10">+10분</option>
                <option value="+30">+30분</option>
                <option value="+60">+1시간</option>
              </optgroup>
              <optgroup label="단축">
                <option value="-5">−5분</option>
                <option value="-10">−10분</option>
              </optgroup>
              <option value="reset">기본값으로</option>
            </select>
            <button
              class="shrink-0 rounded border border-rose-300 dark:border-rose-700 bg-white dark:bg-slate-900 px-2 py-0.5 text-[11px] font-semibold text-rose-600 dark:text-rose-400 hover:bg-rose-50 dark:hover:bg-rose-950/40"
              title="진행 중인 작업을 멈춥니다 (!cancel)"
              onclick={(e) => { e.stopPropagation(); cancelPane(p.id); }}
            >■ 취소</button>
            {#if progressForTarget(p.target).length > 0}
              <span class="shrink-0 text-[11px] font-semibold text-blue-600 dark:text-blue-400">
                진행 메시지 {progressForTarget(p.target).length}건 {chat.progressPopupPaneId === p.id ? "▾" : "▸"}
              </span>
            {/if}
          </div>
        </div>
      {/if}

      <div class="shrink-0 border-t border-slate-200/80 dark:border-slate-700/80 bg-white/92 dark:bg-slate-900/92 px-4 py-1.5 backdrop-blur relative">
        {#if showCommandMenuForPane(p)}
          <div
            class="absolute bottom-full left-4 right-4 mb-2 max-h-64 overflow-y-auto rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 shadow-lg"
            role="listbox"
          >
            {#each commandCandidatesForPane(p) as command, cIndex (command.cmd)}
              <button
                type="button"
                class={`flex w-full items-baseline gap-3 px-3 py-2 text-left ${cIndex === p.highlightedCommandIndex ? "bg-blue-50 dark:bg-blue-950/40" : "hover:bg-slate-50 dark:hover:bg-slate-800"}`}
                role="option"
                aria-selected={cIndex === p.highlightedCommandIndex}
                onmouseenter={() => updatePane(p.id, { highlightedCommandIndex: cIndex })}
                onclick={() => selectCommandForPane(p.id, command)}
              >
                <span class="shrink-0 font-mono text-[13px] font-semibold text-blue-700 dark:text-blue-300">{command.cmd}</span>
                <span class="min-w-0 flex-1 truncate text-[12px] text-slate-500 dark:text-slate-400">{command.desc}</span>
              </button>
            {/each}
          </div>
        {/if}

        {#if voice.confirmPaneId === p.id}
          <div class="mb-1 flex flex-wrap items-center gap-2 rounded-md border border-blue-300 bg-blue-50 px-2 py-1.5 text-[12px] text-slate-700 dark:border-blue-700 dark:bg-blue-950/40 dark:text-slate-300">
            <span class="min-w-0 flex-1">음성 인식을 쓰려면 whisper 엔진과 한국어 모델(약 500MB)을 내려받아야 합니다. 인식은 이 PC 안에서만 이뤄집니다.</span>
            <button type="button" class="shrink-0 rounded bg-blue-600 px-2 py-0.5 text-white hover:bg-blue-700" onclick={installVoice}>내려받기</button>
            <button type="button" class="shrink-0 rounded border border-slate-300 px-2 py-0.5 hover:bg-slate-100 dark:border-slate-600 dark:hover:bg-slate-700" onclick={cancelInstallVoice}>취소</button>
          </div>
        {/if}

        {#if voice.paneId === p.id || voice.convPaneId === p.id || voice.status === "installing" || (voice.note && voice.notePaneId === p.id)}
          <div class="mb-1 flex items-center gap-2 text-[12px] text-slate-600 dark:text-slate-400">
            {#if voice.paneId === p.id && voice.phase === "listening"}
              <span class="inline-block h-2 w-2 shrink-0 animate-pulse rounded-full bg-red-500"></span>
              <span>{voice.warming ? "음성 인식 준비 중… 잠시 후 말씀하세요" : voice.convPaneId === p.id ? "음성 대화 · 듣는 중… (끝내려면 “그만”)" : "듣는 중… 말을 멈추면 자동으로 보냅니다"}</span>
              <span class="h-1.5 w-24 overflow-hidden rounded-full bg-slate-200 dark:bg-slate-700">
                <span class="block h-full bg-red-500 transition-[width] duration-75" style={`width:${Math.round(voice.level * 100)}%`}></span>
              </span>
              <button
                type="button"
                class="shrink-0 rounded-full border border-red-400 px-2 py-0.5 text-[11px] font-medium text-red-600 hover:bg-red-50 dark:border-red-500 dark:text-red-300 dark:hover:bg-red-950"
                onclick={sendNow}
                title="듣기를 멈추고 지금까지 들은 말을 그대로 보냅니다 (다른 사람 목소리도 보냄)"
              >지금 보내기</button>
            {:else if voice.paneId === p.id && voice.phase === "transcribing"}
              <span>음성을 글자로 바꾸는 중…</span>
            {:else if voice.convPaneId === p.id}
              <span class="inline-block h-2 w-2 shrink-0 rounded-full bg-emerald-500"></span>
              <span>음성 대화 중 · {voice.speaking ? "답변을 읽는 중…" : "답변을 기다리는 중…"} 끝나면 다시 듣습니다</span>
              {#if voice.speaking}
                <button
                  type="button"
                  class="shrink-0 rounded-full border border-emerald-500 px-2 py-0.5 text-[11px] font-medium text-emerald-700 hover:bg-emerald-50 dark:border-emerald-400 dark:text-emerald-300 dark:hover:bg-emerald-950"
                  onclick={skipReading}
                  title="답변 읽기를 여기서 멈추고 바로 다음 말을 듣습니다"
                >그만 읽기</button>
              {/if}
            {:else if voice.status === "installing"}
              <span class="shrink-0">음성 인식 내려받는 중 · {voice.installStage}</span>
              <span class="h-1.5 min-w-16 flex-1 overflow-hidden rounded-full bg-slate-200 dark:bg-slate-700">
                <span class="block h-full bg-blue-500 transition-[width]" style={`width:${voice.installPercent}%`}></span>
              </span>
              <span class="shrink-0 tabular-nums">{voice.installPercent}%{voice.installTotalMB ? ` (${voice.installDoneMB}/${voice.installTotalMB}MB)` : ""}</span>
            {:else}
              <span class="min-w-0 flex-1 truncate" title={voice.note}>{voice.note}</span>
              <button type="button" class="shrink-0 text-slate-400 hover:text-slate-600" onclick={clearVoiceNote} aria-label="닫기">✕</button>
            {/if}
          </div>
        {/if}

        <div class="flex w-full items-end gap-2">
          <button
            class="grid h-[34px] w-[34px] shrink-0 place-items-center rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-slate-700 dark:text-slate-300 transition-colors hover:bg-slate-100 dark:hover:bg-slate-700 disabled:cursor-not-allowed disabled:opacity-50"
            onclick={() => pickAttachment(p.id)}
            disabled={!p.target}
            title="파일 첨부"
            aria-label="파일 첨부"
          >
            {@render iconAttach()}
          </button>

          <button
            class={`grid h-[34px] w-[34px] shrink-0 place-items-center rounded-md border text-sm transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${voice.paneId === p.id && voice.phase === "listening" ? "border-red-500 bg-red-500 text-white shadow-[0_0_0_3px_rgba(239,68,68,0.3)] animate-pulse dark:border-red-400 dark:bg-red-500" : "border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700"}`}
            onclick={() => toggleVoiceInput(p.id)}
            disabled={!p.target || (voice.phase !== "idle" && voice.paneId !== p.id) || (voice.paneId === p.id && voice.phase === "transcribing")}
            title={voice.paneId === p.id && voice.phase === "listening" ? "듣기 끄기 — 보내지 않고 취소 (Ctrl+M)" : "말로 입력 (Ctrl+M)"}
            aria-label="음성 입력"
          >
            {#if voice.paneId === p.id && voice.phase === "transcribing"}
              …
            {:else}
              {@render iconMic()}
            {/if}
          </button>

          <button
            class={`grid h-[34px] w-[34px] shrink-0 place-items-center rounded-md border text-sm transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${voice.convPaneId === p.id ? "border-emerald-500 bg-emerald-500 text-white shadow-[0_0_0_3px_rgba(16,185,129,0.3)] dark:border-emerald-400 dark:bg-emerald-500" : "border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700"}`}
            onclick={() => toggleConversation(p.id)}
            disabled={!p.target || (voice.convPaneId !== p.id && voice.phase !== "idle")}
            title={voice.convPaneId === p.id ? "음성 대화 끝내기" : "음성 대화 (Ctrl+Shift+M) — 답변을 읽어 주고 끝나면 다시 듣습니다"}
            aria-label="음성 대화"
            aria-pressed={voice.convPaneId === p.id}
          >
            {@render iconChat()}
          </button>

          {#each p.attachments as attachment, aIndex (attachment.id)}
            {#if attachment.previewURL}
              <img
                alt=""
                class="h-8 w-8 shrink-0 self-center rounded-md border border-slate-300 dark:border-slate-600 object-cover"
                src={attachment.previewURL}
              />
            {/if}
            <span
              class="max-w-[160px] shrink-0 self-center truncate rounded-full bg-slate-200 dark:bg-slate-700 px-2 py-1 text-[11px] font-medium text-slate-600 dark:text-slate-400"
              title={attachment.name}
            >
              {attachment.name}
            </span>
            <button
              class="grid h-6 w-6 shrink-0 place-items-center self-center rounded-full border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-slate-600 dark:text-slate-400 transition-colors hover:bg-slate-100 dark:hover:bg-slate-700"
              onclick={() => clearPaneAttachment(p.id, aIndex)}
              title="첨부 취소"
              aria-label="첨부 취소"
            >
              {@render iconClose()}
            </button>
          {/each}

          <textarea
            use:registerPaneTextarea={p.id}
            class="min-h-[34px] flex-1 rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-3 py-1.5 text-sm leading-5 text-slate-900 dark:text-slate-100 shadow-sm outline-none placeholder:text-slate-400 dark:placeholder:text-slate-500 focus:border-blue-400 focus:ring-2 focus:ring-blue-100 dark:focus:ring-blue-900/30 disabled:bg-slate-100 dark:disabled:bg-slate-800"
            placeholder={p.target ? "메시지 또는 !명령을 입력하세요" : "먼저 대화를 선택하세요"}
            value={p.composerText}
            disabled={!p.target}
            rows="1"
            oninput={(event) => setPaneComposerValue(p.id, event.currentTarget.value)}
            onkeydown={(event) => {
              if (event.ctrlKey && event.shiftKey && !event.altKey && event.key.toLowerCase() === "m") {
                event.preventDefault();
                void toggleConversation(p.id);
                return;
              }
              if (event.ctrlKey && !event.shiftKey && !event.altKey && event.key.toLowerCase() === "m") {
                event.preventDefault();
                void toggleVoiceInput(p.id);
                return;
              }
              if (event.key === "Enter" && !event.shiftKey && !event.isComposing && p.attachments.length === 0 && isTypedCommand(p.composerText)) {
                event.preventDefault();
                const text = p.composerText;
                setPaneComposerValue(p.id, "");
                void runTypedCommand(p.id, text);
                return;
              }
              handleComposerKeydown(p.id, event);
            }}
            onpaste={(event) => handleComposerPaste(p.id, event)}
            onfocus={() => focusPane(p.id)}
          ></textarea>

          <button
            class={`grid h-[34px] w-[34px] shrink-0 place-items-center rounded-md border text-sm transition-colors ${voice.speakEnabled ? "border-blue-400 bg-blue-50 text-blue-700 dark:border-blue-500 dark:bg-blue-950/40 dark:text-blue-300" : "border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-slate-400 dark:text-slate-500 hover:bg-slate-100 dark:hover:bg-slate-700"}`}
            onclick={() => (voice.speaking ? skipReading() : setSpeakEnabled(!voice.speakEnabled))}
            title={voice.speaking ? "읽기 멈추기" : voice.speakEnabled ? "답변 읽어주기 켜짐 (끄려면 클릭)" : "답변 읽어주기 꺼짐 (켜려면 클릭)"}
            aria-label="답변 읽어주기"
            aria-pressed={voice.speakEnabled}
          >
            {#if voice.speaking}
              {@render iconStop()}
            {:else if voice.speakEnabled}
              {@render iconSpeakerOn()}
            {:else}
              {@render iconSpeakerOff()}
            {/if}
          </button>

          <button
            class="grid h-[34px] w-[40px] shrink-0 place-items-center rounded-md bg-blue-600 text-sm font-semibold text-white shadow-sm transition-colors hover:bg-blue-700 disabled:cursor-not-allowed disabled:bg-slate-300 dark:disabled:bg-slate-700"
            onclick={() => sendFromPane(p.id)}
            disabled={(p.attachments.length === 0 && !canSendPane(p)) || !chat.connected}
            title={p.attachments.length > 0 ? "업로드" : "전송"}
            aria-label={p.attachments.length > 0 ? "업로드" : "전송"}
          >
            {#if p.attachments.length > 0}
              {@render iconUpload()}
            {:else}
              {@render iconSend()}
            {/if}
          </button>
        </div>
      </div>
    </main>
  {/if}
{/if}

<style>
  /* Message body text: 13px × the user's text size (Ctrl+= / Ctrl+-), with a
     roomier line height than the old 16px so long answers are easier to read. */
  :global(.msg-text) {
    font-size: calc(13px * var(--msg-scale, 1));
    line-height: 1.55;
  }
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
    font-size: 1.16em;
  }
  :global(.markdown-body h2) {
    font-size: 1.08em;
  }
  :global(.markdown-body h3),
  :global(.markdown-body h4),
  :global(.markdown-body h5),
  :global(.markdown-body h6) {
    font-size: 1em;
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
    font-size: 0.92em;
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
    font-size: 0.92em;
  }
  :global(.markdown-body pre) {
    margin: 0;
    padding: 8px 10px;
    border-radius: 6px;
    background: #1e1e1e;
    overflow-x: auto;
  }
  :global(.md-code-wrap) {
    position: relative;
    margin: 0 0 6px;
  }
  :global(.md-code-copy) {
    position: absolute;
    top: 6px;
    right: 6px;
    padding: 2px 8px;
    border-radius: 5px;
    border: 1px solid rgba(255, 255, 255, 0.15);
    background: rgba(255, 255, 255, 0.08);
    font-family: ui-sans-serif, system-ui, sans-serif;
    font-size: 11px;
    line-height: 1.4;
    color: #cbd5e1;
    cursor: pointer;
    opacity: 0;
    transition: opacity 0.15s, background-color 0.15s;
  }
  :global(.md-code-wrap:hover .md-code-copy) {
    opacity: 1;
  }
  :global(.md-code-copy:hover) {
    background: rgba(255, 255, 255, 0.16);
  }
  :global(.md-code-copy-done) {
    opacity: 1 !important;
    color: #6ee7b7;
    border-color: rgba(110, 231, 183, 0.4);
  }
  /* VS Code Dark+ token colors — the code box itself is always dark (see
     .markdown-body pre above), so these apply the same in light/dark mode. */
  :global(.markdown-body pre .tok-comment) {
    color: #6a9955;
  }
  :global(.markdown-body pre .tok-string) {
    color: #ce9178;
  }
  :global(.markdown-body pre .tok-number) {
    color: #b5cea8;
  }
  :global(.markdown-body pre .tok-keyword) {
    color: #569cd6;
  }
  :global(.msg-folded) {
    max-height: 320px;
    overflow: hidden;
    -webkit-mask-image: linear-gradient(to bottom, #000 70%, transparent);
    mask-image: linear-gradient(to bottom, #000 70%, transparent);
  }
  :global(.msg-action-btn) {
    display: grid;
    place-items: center;
    width: 20px;
    height: 20px;
    border-radius: 5px;
    color: #94a3b8;
    transition: background-color 0.15s, color 0.15s;
  }
  :global(.msg-action-btn:hover:not(:disabled)) {
    background: rgba(148, 163, 184, 0.18);
    color: #2563eb;
  }
  :global(.dark .msg-action-btn:hover:not(:disabled)) {
    color: #93c5fd;
  }
  :global(.search-hit) {
    box-shadow: 0 0 0 2px rgba(250, 204, 21, 0.55);
  }
  :global(.search-current) {
    box-shadow: 0 0 0 2px #f59e0b, 0 0 0 5px rgba(245, 158, 11, 0.25);
  }
  :global(.msg-enter) {
    animation: msg-enter-anim 0.18s ease-out;
  }
  @keyframes msg-enter-anim {
    from {
      opacity: 0;
      transform: translateY(4px);
    }
    to {
      opacity: 1;
      transform: translateY(0);
    }
  }
  :global(.markdown-body pre code) {
    display: block;
    padding: 0;
    background: transparent;
    color: #d4d4d4;
    white-space: pre;
  }

  :global(.dark .markdown-body blockquote) {
    border-left-color: #4d4d4d;
    color: #9d9d9d;
  }
  :global(.dark .markdown-body hr) {
    border-top-color: #3c3c3c;
  }
  :global(.dark .markdown-body th),
  :global(.dark .markdown-body td) {
    border-color: #3c3c3c;
  }
  :global(.dark .markdown-body th) {
    background: #252526;
    color: #cccccc;
  }
  :global(.dark .markdown-body a) {
    color: #3794ff;
  }
  :global(.dark .markdown-body code) {
    background: #252526;
    color: #d4d4d4;
  }
</style>
