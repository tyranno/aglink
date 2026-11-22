<script>
  import PaneNode from "./PaneNode.svelte";
  import ChangesCard from "./ChangesCard.svelte";
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
    updateMentionForPane,
    mentionCandidatesForPane,
    selectMentionForPane,
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
    canForkMessage,
    forkConversationAt,
    targetKey,
    appendPaneAttachment,
    convNote,
    setConvNote,
    requestSummary,
    askAnotherBackend,
    selectTargetFromSidebar,
    isConversationPinned,
    pinnedWebConvs,
  } from "./paneStore.svelte.js";
  import { renderMarkdown } from "./markdown.js";
  import { snippets, snippetQuery, snippetCandidates, openSnippetManager, templateFields, autoValues, expandTemplate, askTemplate } from "./snippets.svelte.js";
  import { openSearchPanel } from "./search.svelte.js";
  import { openQuickAsk } from "./quickask.svelte.js";
  import { isBookmarked, toggleBookmark } from "./search.svelte.js";
  import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";
  import { voice, sendNow, skipReading, toggleVoiceInput, setSpeakEnabled, stopSpeaking, clearVoiceNote, installVoice, cancelInstallVoice, toggleConversation, isTypedCommand, runTypedCommand } from "./voice.svelte.js";

  let { node } = $props();

  // App.svelte owns the settings screen and the "새 대화" prompt; panes ask
  // for them through window events so this component needs no callbacks
  // threaded through the recursive split tree.
  function requestSettings(tab = "settings") {
    window.dispatchEvent(new CustomEvent("aglink:open-settings", { detail: { tab } }));
  }
  function requestNewConversation() {
    window.dispatchEvent(new CustomEvent("aglink:new-conversation"));
  }

  // The current backend choice of a conversation, as the menus name it.
  function backendChoice(backend) {
    const value = String(backend || "").toLowerCase();
    return ["claude", "codex", "opencode"].includes(value) ? value : "default";
  }
  const BACKEND_CHOICES = [
    { id: "default", label: "기본값" },
    { id: "claude", label: "Claude" },
    { id: "codex", label: "Codex" },
    { id: "opencode", label: "OpenCode" },
  ];

  // "/" snippet picker above the composer (snippets.svelte.js). Its highlight
  // and dismissal are this pane's own; Esc hides it until the text changes.
  let snippetIndex = $state(0);
  let snippetHiddenFor = $state(null); // composer text the picker was dismissed on

  function snippetMenuFor(p) {
    if (!p?.target || snippetQuery(p.composerText) === null || snippetHiddenFor === p.composerText) return null;
    const list = snippetCandidates(p.composerText);
    if (list.length) return { list };
    // A bare "/" with nothing saved yet: a hint how to make one.
    if (p.composerText === "/" && snippets.list.length === 0) return { list: [] };
    return null;
  }

  // A snippet with {{변수}} slots is a template: the automatic ones fill
  // themselves, the rest are asked in TemplateFill before it goes in.
  async function insertSnippet(p, item) {
    if (!item) return;
    snippetIndex = 0;
    snippetHiddenFor = null;
    const fields = templateFields(item.text);
    if (!fields.length) {
      void editIntoComposer(p.id, item.text);
      return;
    }
    const values = await autoValues(fields);
    const missing = fields.filter((name) => !(name in values));
    if (!missing.length) {
      void editIntoComposer(p.id, expandTemplate(item.text, values));
      return;
    }
    setPaneComposerValue(p.id, "");
    askTemplate(p.id, item.text, missing, values, (text) => void editIntoComposer(p.id, text));
  }

  // --- 요약 / pinned note on top of the conversation ---
  let noteOpen = $state(true);

  // --- "다른 AI에게 같은 질문": the small backend menu on a user message ---
  let compareAsk = $state(null); // { index, busy, error }
  function otherBackends(target) {
    const current = backendChoice(paneConversationMeta(target)?.backend);
    return BACKEND_CHOICES.filter((choice) => choice.id !== "default" && choice.id !== current);
  }
  async function runCompare(p, message, backend) {
    if (!compareAsk || compareAsk.busy) return;
    compareAsk = { ...compareAsk, busy: true, error: "" };
    const error = await askAnotherBackend(p.target, message.text, backend);
    if (error) compareAsk = compareAsk ? { ...compareAsk, busy: false, error } : null;
    else compareAsk = null;
  }

  // "다시 생성": the user message that started this answer's turn, sent again.
  function promptBefore(messages, index) {
    for (let i = index - 1; i >= 0; i--) {
      if (messages[i].role === "user") return messages[i].text || "";
    }
    return "";
  }

  // Home card for a pane with nothing open: what is running, unread, recent.
  function homeItems() {
    const seen = new Set();
    const pick = (target) => {
      if (!target) return null;
      const key = targetKey(target);
      if (seen.has(key)) return null;
      if (target.kind === "web" && !chat.webConvs.some((c) => String(c.id) === String(target.id))) return null;
      if (target.kind === "telegram" && !chat.telegram) return null;
      seen.add(key);
      return { key, target, title: paneConversationMeta(target)?.title || "대화" };
    };
    const fromKey = (key) => (key === "telegram" ? { kind: "telegram" } : key.startsWith("web:") ? { kind: "web", id: key.slice(4) } : null);
    const working = [...chat.working.keys()].map((key) => pick(fromKey(key))).filter(Boolean);
    const unread = [...chat.unread].map((key) => pick(fromKey(key))).filter(Boolean);
    const pinned = pinnedWebConvs().map((conv) => pick({ kind: "web", id: conv.id })).filter(Boolean);
    const recent = chat.recentTargets.map((t) => pick(t)).filter(Boolean).slice(0, 6);
    return { working, unread, pinned, recent };
  }

  // Returns true when the key was the picker's.
  function handleSnippetKeydown(p, event) {
    const menu = snippetMenuFor(p);
    if (!menu || event.isComposing) return false;
    const n = menu.list.length;
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      snippetHiddenFor = p.composerText;
      return true;
    }
    if (!n) return false;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      snippetIndex = (Math.min(snippetIndex, n - 1) + (event.key === "ArrowDown" ? 1 : -1) + n) % n;
      return true;
    }
    if ((event.key === "Enter" && !event.shiftKey) || event.key === "Tab") {
      event.preventDefault();
      insertSnippet(p, menu.list[Math.min(snippetIndex, n - 1)]);
      return true;
    }
    return false;
  }

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

  // --- Bubble grouping: consecutive messages from the same side share one
  // avatar and one tail (on the first bubble of the run). Narration is the
  // AI's side but never starts a group's avatar on its own row. ---
  function msgSide(message) {
    if (message?.role === "user") return "user";
    if (message?.role === "system") return "system";
    return "ai";
  }

  function msgGroupStart(messages, index) {
    if (index === 0) return true;
    const prev = messages[index - 1];
    const cur = messages[index];
    if (msgSide(prev) !== msgSide(cur)) return true;
    // A narration line followed by the answer: the answer still gets the avatar.
    return prev.role === "narration" && cur.role !== "narration" && !msgHasAvatarAbove(messages, index - 1);
  }

  function msgHasAvatarAbove(messages, index) {
    for (let i = index; i >= 0; i--) {
      if (msgSide(messages[i]) !== "ai") return false;
      if (messages[i].role !== "narration") return true;
    }
    return false;
  }

  // --- "여기서 갈라 새 대화": the action button opens a small in-app confirm
  // under the message (not the browser dialog); 만들기 asks the host to fork. ---
  let forkAsk = $state(null); // { index, text, busy, error }

  function isForkAsk(index, message) {
    return !!forkAsk && forkAsk.index === index && forkAsk.text === (message.text || "");
  }

  function toggleForkAsk(index, message) {
    forkAsk = isForkAsk(index, message) ? null : { index, text: message.text || "", busy: false, error: "" };
  }

  async function runFork(paneId, index) {
    if (!forkAsk || forkAsk.busy) return;
    forkAsk = { ...forkAsk, busy: true, error: "" };
    const error = await forkConversationAt(paneId, index);
    if (error) forkAsk = forkAsk ? { ...forkAsk, busy: false, error } : null;
    else forkAsk = null;
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

  // 대화를 .md 파일로 저장: the native save dialog (desktop SaveMarkdownFile).
  async function saveConversationFile(target) {
    try {
      const path = await ControlService.SaveMarkdownFile(paneConversationMeta(target)?.title || "대화", conversationMarkdown(target));
      if (path) showToast(`저장했습니다: ${path.split(/[\\/]/).pop()}`);
    } catch (error) {
      showToast(`저장하지 못했습니다: ${error}`);
    }
  }

  // --- Long paste: past LONG_PASTE_CHARS / LONG_PASTE_LINES the text is held
  // and a small card asks whether to attach it as a .txt file or put it in
  // the composer as usual. ---
  const LONG_PASTE_CHARS = 4000;
  const LONG_PASTE_LINES = 80;
  let longPaste = $state(null); // { text, chars, lines, start, end, busy }

  function handlePaste(p, event) {
    const types = Array.from(event.clipboardData?.types || []);
    if (!types.includes("Files")) {
      const text = event.clipboardData?.getData("text/plain") || "";
      const lines = text ? text.split("\n").length : 0;
      if (text.length > LONG_PASTE_CHARS || lines > LONG_PASTE_LINES) {
        event.preventDefault();
        const el = event.currentTarget;
        longPaste = { paneId: p.id, text, chars: text.length, lines, start: el.selectionStart ?? p.composerText.length, end: el.selectionEnd ?? p.composerText.length, busy: false };
        return;
      }
    }
    handleComposerPaste(p.id, event);
  }

  function pasteInline(p) {
    if (!longPaste) return;
    const { text, start, end } = longPaste;
    longPaste = null;
    const value = p.composerText;
    void editIntoComposer(p.id, value.slice(0, start) + text + value.slice(end));
  }

  async function pasteAsFile(p) {
    if (!longPaste || longPaste.busy) return;
    longPaste = { ...longPaste, busy: true };
    try {
      const path = await ControlService.SaveTextAttachment(longPaste.text);
      const stamp = new Date().toLocaleTimeString("ko-KR", { hour: "2-digit", minute: "2-digit" }).replace(/\s/g, "");
      appendPaneAttachment(p.id, { path, name: `붙여넣은 텍스트 ${stamp}.txt` });
      longPaste = null;
    } catch (error) {
      longPaste = longPaste ? { ...longPaste, busy: false } : null;
      showToast(`파일로 첨부하지 못했습니다: ${error}`);
    }
  }

  function composerStats(text) {
    const chars = text.length;
    const lines = text ? text.split("\n").length : 0;
    return chars >= 500 || lines >= 8 ? `${chars.toLocaleString("ko-KR")}자 · ${lines}줄` : "";
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

  // Computed once per render for the whole message list (not per message —
  // {@const} can't hoist out of the big #each below since it isn't an
  // immediate child of a block there) so highlighting hits in a long
  // conversation doesn't become an O(n^2) scan.
  const messageSearchHits = $derived.by(() => searchMatches(messagesForPane(pane(node.paneId)?.target)));
  const messageSearchHitsSet = $derived.by(() => new Set(messageSearchHits));

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
    if (node.type !== "leaf") return;
    if ((forkAsk || compareAsk) && event.key === "Escape") {
      forkAsk = null;
      compareAsk = null;
      return;
    }
    if (chat.focusedPaneId !== node.paneId) return;
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

{#snippet iconFork()}
  <svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><line x1="6" y1="3" x2="6" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/></svg>
{/snippet}

<!-- Small round avatar beside the first bubble of each run: "나" for the user,
     a spark mark for the AI. -->
{#snippet avatar(side)}
  {#if side === "user"}
    <span class="msg-avatar msg-avatar-user" role="img" aria-label="나" title="나">나</span>
  {:else}
    <span class="msg-avatar msg-avatar-ai" role="img" aria-label="AI" title="AI">
      <svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M12 2.5l1.9 5.6 5.6 1.9-5.6 1.9L12 17.5l-1.9-5.6L4.5 10l5.6-1.9z"/><path d="M18.5 15l.8 2.2 2.2.8-2.2.8-.8 2.2-.8-2.2-2.2-.8 2.2-.8z" opacity=".75"/></svg>
    </span>
  {/if}
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
                {@const currentBackend = backendChoice(paneConversationMeta(p.target).backend)}
                <div class="absolute left-0 top-6 z-40 w-36 rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 p-1.5 shadow-xl" role="menu" aria-label="사용할 AI">
                  {#each BACKEND_CHOICES as choice (choice.id)}
                    <button
                      role="menuitemradio"
                      aria-checked={currentBackend === choice.id}
                      class={`flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left text-xs font-medium hover:bg-slate-100 dark:hover:bg-slate-700 ${currentBackend === choice.id ? "text-blue-700 dark:text-blue-300" : "text-slate-700 dark:text-slate-300"}`}
                      onclick={(event) => { event.stopPropagation(); void setTargetBackend(p.target, choice.id); }}
                    >
                      <span class="w-3 shrink-0 text-center" aria-hidden="true">{currentBackend === choice.id ? "✓" : ""}</span>
                      <span class="min-w-0 flex-1 truncate">{choice.label}</span>
                    </button>
                  {/each}
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
            class="flex h-7 shrink-0 items-center gap-1 rounded-md px-1.5 text-[11.5px] font-semibold text-slate-500 dark:text-slate-400 transition-colors hover:bg-slate-100 dark:hover:bg-slate-700 disabled:cursor-not-allowed disabled:opacity-40"
            disabled={!chat.connected || chat.summaryPending.has(targetKey(p.target))}
            onclick={(event) => { event.stopPropagation(); noteOpen = true; void requestSummary(p.target); }}
            title="지금까지 대화를 AI에게 요약시키고, 그 요약을 대화 맨 위에 고정합니다"
            aria-label="대화 요약"
          >{chat.summaryPending.has(targetKey(p.target)) ? "요약 중…" : "요약"}</button>
          <button
            type="button"
            draggable="false"
            class="grid h-7 w-7 shrink-0 place-items-center rounded-md text-slate-500 dark:text-slate-400 transition-colors hover:bg-slate-100 dark:hover:bg-slate-700"
            onclick={(event) => { event.stopPropagation(); exportConversation(p.target); }}
            title="대화 전체를 마크다운으로 복사"
            aria-label="대화 내보내기"
          >{@render iconExport()}</button>
          <button
            type="button"
            draggable="false"
            class="grid h-7 w-7 shrink-0 place-items-center rounded-md text-slate-500 dark:text-slate-400 transition-colors hover:bg-slate-100 dark:hover:bg-slate-700"
            onclick={(event) => { event.stopPropagation(); void saveConversationFile(p.target); }}
            title="대화를 마크다운(.md) 파일로 저장"
            aria-label="대화를 파일로 저장"
          ><svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/><polyline points="17 21 17 13 7 13 7 21"/><polyline points="7 3 7 8 15 8"/></svg></button>
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

      {#if convNote(p.target)}
        {@const note = convNote(p.target)}
        <div class="relative z-10 shrink-0 border-b border-amber-200 bg-amber-50/80 px-4 py-1.5 dark:border-amber-900/60 dark:bg-amber-950/30">
          <div class="flex items-center gap-2">
            <button type="button" class="flex min-w-0 flex-1 items-center gap-1.5 text-left text-[12px] font-semibold text-amber-800 dark:text-amber-300" onclick={() => (noteOpen = !noteOpen)} aria-expanded={noteOpen}>
              <span aria-hidden="true">📌</span>
              <span class="truncate">{note.kind === "summary" ? "대화 요약" : "고정한 메시지"}</span>
              <span class="shrink-0 font-normal text-amber-700/70 dark:text-amber-300/60">· {new Date(note.at).toLocaleString("ko-KR", { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })}</span>
              <span class="shrink-0 text-[10px]" aria-hidden="true">{noteOpen ? "▾" : "▸"}</span>
            </button>
            {#if note.kind === "summary"}
              <button type="button" class="shrink-0 rounded px-1.5 py-0.5 text-[11px] text-amber-700 hover:bg-amber-100 disabled:opacity-40 dark:text-amber-300 dark:hover:bg-amber-900/40" disabled={!chat.connected || chat.summaryPending.has(targetKey(p.target))} onclick={() => requestSummary(p.target)}>다시 요약</button>
            {/if}
            <button type="button" class="shrink-0 rounded px-1.5 py-0.5 text-[11px] text-amber-700 hover:bg-amber-100 dark:text-amber-300 dark:hover:bg-amber-900/40" onclick={() => setConvNote(p.target, "")} title="고정 해제" aria-label="고정 해제">✕</button>
          </div>
          {#if noteOpen}
            <div class="msg-text markdown-body mt-1 max-h-40 overflow-y-auto pr-1 text-slate-700 dark:text-slate-200" use:renderMarkdownInto={note.text}></div>
          {/if}
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
            {#if !p.target && chat.connected && !chat.convsLoaded}
              <div class="mt-6 flex items-center justify-center gap-2 text-sm text-slate-500 dark:text-slate-400" role="status">
                <span class="h-3.5 w-3.5 shrink-0 animate-spin rounded-full border-2 border-slate-300 border-t-blue-500 dark:border-slate-600 dark:border-t-blue-400"></span>
                불러오는 중…
              </div>
            {:else if messagesForPane(p.target).length === 0 && (!p.target || !chat.connected)}
              <!-- Guide card: nothing to talk to yet — not connected, or no conversation chosen. -->
              <div class="mx-auto mt-6 flex w-full max-w-sm flex-col items-center gap-2 rounded-xl border border-slate-200 bg-white/80 px-6 py-6 text-center shadow-sm dark:border-slate-700 dark:bg-slate-900/70">
                <div class="grid h-10 w-10 place-items-center rounded-full bg-blue-50 text-blue-600 dark:bg-blue-950/50 dark:text-blue-300" aria-hidden="true">
                  <svg class="h-5 w-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 7V3M15 7V3M7 7h10v4a5 5 0 0 1-10 0V7zM12 16v5"/></svg>
                </div>
                {#if !chat.connected}
                  <div class="text-sm font-semibold text-slate-800 dark:text-slate-100">aglink 본체에 연결되지 않았어요</div>
                  <div class="text-[12.5px] leading-5 text-slate-500 dark:text-slate-400">본체가 꺼져 있거나 다시 시작하는 중일 수 있어요. 연결되면 대화를 바로 불러옵니다.</div>
                  <div class="mt-1 flex gap-2">
                    <button type="button" class="ui-btn ui-btn-primary" onclick={(event) => { event.stopPropagation(); requestSettings("connection"); }}>연결 상태 보기</button>
                  </div>
                {:else if !p.target}
                  {@const home = homeItems()}
                  <div class="text-sm font-semibold text-slate-800 dark:text-slate-100">무엇을 이어서 할까요?</div>
                  <div class="w-full space-y-2 text-left">
                    {#each [["진행 중", home.working], ["읽지 않음", home.unread], ["고정됨", home.pinned], ["최근", home.recent]] as [heading, list] (heading)}
                      {#if list.length}
                        <div>
                          <div class="mb-0.5 text-[10.5px] font-bold tracking-[0.04em] text-slate-400 dark:text-slate-500">{heading}</div>
                          {#each list.slice(0, 5) as item (item.key)}
                            <button
                              type="button"
                              class="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-[12.5px] text-slate-700 hover:bg-slate-100 dark:text-slate-200 dark:hover:bg-slate-800"
                              onclick={(event) => { event.stopPropagation(); focusPane(p.id); selectTargetFromSidebar(item.target); }}
                            >
                              <span class={`h-1.5 w-1.5 shrink-0 rounded-full ${chat.working.has(item.key) ? "animate-pulse bg-blue-500" : chat.unread.has(item.key) ? "bg-blue-600" : "bg-slate-300 dark:bg-slate-600"}`}></span>
                              <span class="min-w-0 flex-1 truncate">{item.title}</span>
                              {#if item.target.kind === "web" && isConversationPinned(item.target.id)}<span class="text-[10px] text-amber-500">★</span>{/if}
                            </button>
                          {/each}
                        </div>
                      {/if}
                    {/each}
                  </div>
                  <div class="mt-1 flex flex-wrap justify-center gap-2">
                    <button type="button" class="ui-btn ui-btn-primary" onclick={(event) => { event.stopPropagation(); focusPane(p.id); openQuickAsk(); }}>빠른 질문</button>
                    <button type="button" class="ui-btn ui-btn-ghost" onclick={(event) => { event.stopPropagation(); requestNewConversation(); }}>새 대화</button>
                    <button type="button" class="ui-btn ui-btn-ghost" onclick={(event) => { event.stopPropagation(); openSearchPanel("search"); }}>모든 대화에서 찾기</button>
                  </div>
                  <button type="button" class="text-[11.5px] text-slate-400 hover:text-blue-600 hover:underline dark:text-slate-500 dark:hover:text-blue-300" onclick={(event) => { event.stopPropagation(); requestSettings("settings"); }}>AI가 아직 연결되지 않았다면 → 설정 열기</button>
                {:else}
                  <div class="text-sm font-semibold text-slate-800 dark:text-slate-100">AI 연결하기</div>
                  <div class="text-[12.5px] leading-5 text-slate-500 dark:text-slate-400">왼쪽 목록에서 대화를 고르거나 새로 만들어 시작하세요. AI를 아직 연결하지 않았다면 설정에서 ①②③ 순서대로 채우면 됩니다.</div>
                  <div class="mt-1 flex gap-2">
                    <button type="button" class="ui-btn ui-btn-ghost" onclick={(event) => { event.stopPropagation(); requestNewConversation(); }}>새 대화</button>
                    <button type="button" class="ui-btn ui-btn-primary" onclick={(event) => { event.stopPropagation(); requestSettings("settings"); }}>설정 열기 →</button>
                  </div>
                {/if}
              </div>
            {:else if messagesForPane(p.target).length === 0 && chat.loadingBuffers.has(targetKey(p.target))}
              <div class="mt-6 flex items-center justify-center gap-2 text-sm text-slate-500 dark:text-slate-400" role="status">
                <span class="h-3.5 w-3.5 shrink-0 animate-spin rounded-full border-2 border-slate-300 border-t-blue-500 dark:border-slate-600 dark:border-t-blue-400"></span>
                대화를 불러오는 중…
              </div>
            {:else if messagesForPane(p.target).length === 0}
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
                  <button
                    type="button"
                    class="text-[11.5px] text-slate-400 underline-offset-2 hover:text-blue-600 hover:underline dark:text-slate-500 dark:hover:text-blue-300"
                    onclick={(event) => { event.stopPropagation(); requestSettings("settings"); }}
                  >답이 오지 않나요? AI 연결 설정 열기</button>
                {/if}
              </div>
            {/if}

            {#each messagesForPane(p.target) as message, mIndex (`${mIndex}-${message.role}-${message.text}-${message.image}`)}
              {@const hits = messageSearchHits}
              {@const isHit = messageSearchHitsSet.has(mIndex)}
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
              {@const side = msgSide(message)}
              {@const startsGroup = msgGroupStart(messagesForPane(p.target), mIndex)}
              <div
                data-msg-index={mIndex}
                class={`msg-enter msg-group group/msg flex w-full ${side === "user" ? "flex-row-reverse" : "flex-row"} ${side === "system" ? "justify-center" : "items-start gap-2"} ${startsGroup && mIndex > 0 ? "mt-1.5 msg-group-start" : ""} ${chat.flashMessage?.paneId === p.id && chat.flashMessage.index === mIndex ? "msg-flash" : ""}`}
              >
                {#if side !== "system"}
                  {#if startsGroup && message.role !== "narration"}
                    {@render avatar(side)}
                  {:else}
                    <span class="w-6 shrink-0 msg-row-spacer" aria-hidden="true"></span>
                  {/if}
                {/if}
                <div class={`msg-col relative flex min-w-0 max-w-[88%] flex-col ${side === "user" ? "items-end" : side === "system" ? "items-center" : "items-start"}`}>
                  {#if startsGroup && message.role !== "narration" && side !== "system"}
                    <span class={`bubble-tail ${side === "user" ? "bubble-tail-user" : "bubble-tail-ai"}`} aria-hidden="true"></span>
                    <span class="msg-sender-label">{side === "user" ? "나" : backendModelLabel(paneConversationMeta(p.target).backend) || backendLabel(paneConversationMeta(p.target).backend)}</span>
                  {/if}
                  <div class={`${message.role === "narration"
                    ? "relative min-w-0 max-w-full overflow-hidden border-l-2 border-blue-300 dark:border-blue-700 py-0.5 pl-2.5 pr-2 italic text-slate-500 dark:text-slate-400"
                    : `bubble relative min-w-0 max-w-full overflow-hidden rounded-lg px-3 py-1.5 shadow-sm ring-1 ${
                    message.role === "user"
                      ? `bubble-user ring-blue-200 dark:ring-blue-800/60 ${startsGroup ? "rounded-tr-[4px]" : ""}`
                      : message.role === "system"
                        ? "bubble-system ring-amber-200 dark:ring-amber-800"
                        : `bubble-ai ring-slate-200 dark:ring-slate-700 ${startsGroup ? "rounded-tl-[4px]" : ""}`
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
                        class="msg-image mt-2 max-h-[420px] w-auto max-w-full cursor-zoom-in rounded-lg border border-slate-200 dark:border-slate-700 transition hover:brightness-95"
                        src={`data:image/png;base64,${message.image}`}
                        title="클릭하면 창으로 크게 봅니다 (이동·크기조절 가능)"
                        onclick={() => openLightbox(`data:image/png;base64,${message.image}`)}
                      />
                    {/if}
                  </div>
                  {#if message.changes}<ChangesCard changes={message.changes} />{/if}
                  {#if message.role !== "narration" && (message.text || message.at)}
                    <div class={`msg-meta mt-0.5 flex h-5 items-center gap-1 px-1 text-[10.5px] text-slate-500 dark:text-slate-500 ${message.role === "user" ? "flex-row-reverse" : ""}`}>
                      {#if message.at}
                        <span class="tabular-nums" title={new Date(message.at).toLocaleString("ko-KR")}>{messageTime(message.at)}</span>
                      {/if}
                      {#if replyDuration(messagesForPane(p.target), mIndex)}
                        <span class="tabular-nums" title="질문부터 이 답변까지 걸린 시간">· {replyDuration(messagesForPane(p.target), mIndex)}</span>
                      {/if}
                      {#if message.role === "user" && message.ack}
                        <span class="text-blue-600/90 dark:text-blue-400/80" title="aglink 가 메시지를 받아 작업을 시작했습니다">✓ 확인함</span>
                      {/if}
                      {#if message.text}
                        <span class={`msg-actions flex items-center gap-0.5 transition-opacity duration-150 group-hover/msg:opacity-100 focus-within:opacity-100 ${message.role === "user" ? "flex-row-reverse" : ""} ${isForkAsk(mIndex, message) ? "opacity-100" : "opacity-0"}`}>
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
                          {#if p.target}
                            {@const starred = isBookmarked(p.target, message)}
                            <button
                              type="button"
                              class={`msg-action-btn ${starred ? "text-amber-500 dark:text-amber-400" : ""}`}
                              onclick={(event) => { event.stopPropagation(); showToast(toggleBookmark(p.target, message, mIndex) ? "북마크에 넣었습니다 (Ctrl+Shift+F → 북마크)" : "북마크에서 뺐습니다"); }}
                              title={starred ? "북마크 지우기" : "북마크 — 나중에 이 메시지로 바로 돌아오기"}
                              aria-label={starred ? "북마크 지우기" : "북마크"}
                              aria-pressed={starred}
                            ><span class="text-[13px] leading-none" aria-hidden="true">{starred ? "★" : "☆"}</span></button>
                          {/if}
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
                            {#if p.target}
                              <button
                                type="button"
                                class={`msg-action-btn disabled:cursor-not-allowed disabled:opacity-40 ${compareAsk?.index === mIndex ? "msg-action-btn-on" : ""}`}
                                disabled={!chat.connected}
                                onclick={(event) => { event.stopPropagation(); compareAsk = compareAsk?.index === mIndex ? null : { index: mIndex, busy: false, error: "" }; }}
                                title="다른 AI에게 같은 질문 — 옆 화면에 새 대화로 열어 답을 비교합니다"
                                aria-label="다른 AI에게 같은 질문"
                                aria-expanded={compareAsk?.index === mIndex}
                              ><span class="text-[12px] font-bold leading-none" aria-hidden="true">⇄</span></button>
                            {/if}
                          {:else if message.role === "assistant" && p.target}
                            {#if promptBefore(messagesForPane(p.target), mIndex)}
                              <button
                                type="button"
                                class="msg-action-btn disabled:cursor-not-allowed disabled:opacity-40"
                                disabled={!chat.connected || p.attachments.length > 0 || paneWorking(p.target)}
                                onclick={(event) => { event.stopPropagation(); void resendFromPane(p.id, promptBefore(messagesForPane(p.target), mIndex)); }}
                                title="다시 생성 — 이 답의 질문을 한 번 더 보냅니다"
                                aria-label="다시 생성"
                              >{@render iconRetry()}</button>
                            {/if}
                            <button
                              type="button"
                              class="msg-action-btn"
                              onclick={(event) => { event.stopPropagation(); noteOpen = true; setConvNote(p.target, message.text, "pinned"); showToast("대화 맨 위에 고정했습니다"); }}
                              title="이 답을 대화 맨 위에 고정"
                              aria-label="맨 위에 고정"
                            ><span class="text-[11px] leading-none" aria-hidden="true">📌</span></button>
                          {/if}
                          {#if canForkMessage(message) && p.target}
                            <button
                              type="button"
                              class={`msg-action-btn disabled:cursor-not-allowed disabled:opacity-40 ${isForkAsk(mIndex, message) ? "msg-action-btn-on" : ""}`}
                              disabled={!chat.connected}
                              onclick={(event) => { event.stopPropagation(); toggleForkAsk(mIndex, message); }}
                              title="여기서 갈라 새 대화 — 이 메시지까지의 대화를 이어받은 새 대화를 엽니다"
                              aria-label="여기서 갈라 새 대화"
                              aria-expanded={isForkAsk(mIndex, message)}
                            >{@render iconFork()}</button>
                          {/if}
                        </span>
                      {/if}
                    </div>
                  {/if}
                  {#if compareAsk?.index === mIndex && message.role === "user"}
                    <div class="fork-confirm mt-1 w-72 max-w-full rounded-lg border border-blue-200 px-3 py-2 text-[12px] text-blue-950 shadow-sm dark:border-blue-900/70 dark:text-blue-100" role="dialog" aria-label="다른 AI에게 같은 질문">
                      <p class="font-semibold">어느 AI에게 물어볼까요?</p>
                      <p class="mt-0.5 text-[11.5px] text-blue-900/80 dark:text-blue-200/80">같은 작업 폴더로 새 대화를 만들어 옆 화면에 열고 이 질문을 보냅니다.</p>
                      {#if compareAsk.error}<p class="mt-1 text-rose-700 dark:text-rose-300">보내지 못했습니다: {compareAsk.error}</p>{/if}
                      <div class="mt-2 flex flex-wrap justify-end gap-1.5">
                        <button type="button" class="ui-btn ui-btn-ghost" onclick={(event) => { event.stopPropagation(); compareAsk = null; }}>취소</button>
                        {#each otherBackends(p.target) as choice (choice.id)}
                          <button type="button" class="ui-btn ui-btn-primary" disabled={compareAsk.busy || !chat.connected} onclick={(event) => { event.stopPropagation(); void runCompare(p, message, choice.id); }}>{compareAsk.busy ? "여는 중…" : choice.label}</button>
                        {/each}
                      </div>
                    </div>
                  {/if}
                  {#if isForkAsk(mIndex, message)}
                    <div class="fork-confirm mt-1 w-72 max-w-full rounded-lg border border-blue-200 px-3 py-2 text-[12px] text-blue-950 shadow-sm dark:border-blue-900/70 dark:text-blue-100" role="dialog" aria-label="여기서 갈라 새 대화">
                      <p class="font-semibold">여기서 갈라 새 대화를 만들까요?</p>
                      <p class="mt-0.5 text-[11.5px] text-blue-900/80 dark:text-blue-200/80">이 메시지까지의 대화를 이어받고, 작업 폴더·모델도 그대로 씁니다. 원래 대화는 바뀌지 않습니다.</p>
                      {#if forkAsk.error}<p class="mt-1 text-rose-700 dark:text-rose-300">갈라내지 못했습니다: {forkAsk.error}</p>{/if}
                      <div class="mt-2 flex justify-end gap-1.5">
                        <button type="button" class="ui-btn ui-btn-ghost" onclick={(event) => { event.stopPropagation(); forkAsk = null; }}>취소</button>
                        <button type="button" class="ui-btn ui-btn-primary" disabled={forkAsk.busy || !chat.connected} onclick={(event) => { event.stopPropagation(); void runFork(p.id, mIndex); }}>{forkAsk.busy ? "만드는 중…" : "새 대화 만들기"}</button>
                      </div>
                    </div>
                  {/if}
                </div>
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
        {#if snippetMenuFor(p)}
          {@const snippetMenu = snippetMenuFor(p)}
          <div
            class="absolute bottom-full left-4 right-4 z-30 mb-2 max-h-64 overflow-y-auto rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 shadow-lg"
            role="listbox"
            aria-label="상용구"
          >
            <div class="flex items-center gap-2 border-b border-slate-100 px-3 py-1 text-[11px] text-slate-400 dark:border-slate-800 dark:text-slate-500">
              <span class="min-w-0 flex-1 truncate">상용구 · Tab/Enter 넣기 · Esc 닫기</span>
              <button
                type="button"
                class="shrink-0 font-semibold text-blue-600 hover:underline dark:text-blue-400"
                onmousedown={(event) => event.preventDefault()}
                onclick={() => { snippetHiddenFor = p.composerText; openSnippetManager(); }}
              >관리…</button>
            </div>
            {#each snippetMenu.list as item, sIndex (item.id)}
              <button
                type="button"
                class={`flex w-full items-baseline gap-3 px-3 py-2 text-left ${sIndex === Math.min(snippetIndex, snippetMenu.list.length - 1) ? "bg-blue-50 dark:bg-blue-950/40" : "hover:bg-slate-50 dark:hover:bg-slate-800"}`}
                role="option"
                aria-selected={sIndex === Math.min(snippetIndex, snippetMenu.list.length - 1)}
                onmouseenter={() => (snippetIndex = sIndex)}
                onmousedown={(event) => event.preventDefault()}
                onclick={() => insertSnippet(p, item)}
              >
                <span class="shrink-0 font-mono text-[13px] font-semibold text-blue-700 dark:text-blue-300">/{item.name}</span>
                <span class="min-w-0 flex-1 truncate text-[12px] text-slate-500 dark:text-slate-400">{item.text}</span>
              </button>
            {:else}
              <div class="px-3 py-2.5 text-[12px] text-slate-500 dark:text-slate-400">
                아직 상용구가 없어요. 자주 쓰는 요청을 저장해 두면 <span class="font-mono">/이름</span>으로 바로 넣을 수 있어요.
              </div>
            {/each}
          </div>
        {:else if mentionCandidatesForPane(p).length}
          <div
            class="absolute bottom-full left-4 right-4 mb-2 max-h-64 overflow-y-auto rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 shadow-lg"
            role="listbox"
          >
            <div class="border-b border-slate-100 px-3 py-1 text-[11px] text-slate-400 dark:border-slate-800 dark:text-slate-500">작업 폴더의 파일 · Tab/Enter 넣기 · Esc 닫기</div>
            {#each mentionCandidatesForPane(p) as file, fIndex (file)}
              {@const slash = file.lastIndexOf("/")}
              <button
                type="button"
                class={`flex w-full items-baseline gap-2 px-3 py-1.5 text-left ${fIndex === (p.mention?.index || 0) ? "bg-blue-50 dark:bg-blue-950/40" : "hover:bg-slate-50 dark:hover:bg-slate-800"}`}
                role="option"
                aria-selected={fIndex === (p.mention?.index || 0)}
                onmouseenter={() => updatePane(p.id, { mention: { ...p.mention, index: fIndex } })}
                onmousedown={(event) => event.preventDefault()}
                onclick={() => selectMentionForPane(p.id, file)}
              >
                <span class="shrink-0 font-mono text-[13px] font-semibold text-blue-700 dark:text-blue-300">{file.slice(slash + 1)}</span>
                <span class="min-w-0 flex-1 truncate font-mono text-[11.5px] text-slate-500 dark:text-slate-400">{slash > 0 ? file.slice(0, slash) : ""}</span>
              </button>
            {/each}
          </div>
        {:else if showCommandMenuForPane(p)}
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

        {#if longPaste && longPaste.paneId === p.id}
          <div class="mb-1 flex flex-wrap items-center gap-2 rounded-md border border-blue-300 bg-blue-50 px-2 py-1.5 text-[12px] text-slate-700 dark:border-blue-700 dark:bg-blue-950/40 dark:text-slate-300" role="dialog" aria-label="긴 텍스트 붙여넣기">
            <span class="min-w-0 flex-1">긴 텍스트예요 ({longPaste.chars.toLocaleString("ko-KR")}자 · {longPaste.lines}줄). 파일로 첨부하면 입력창이 깔끔해요.</span>
            <button type="button" class="ui-btn ui-btn-primary" disabled={longPaste.busy} onclick={() => pasteAsFile(p)}>{longPaste.busy ? "첨부 중…" : "파일로 첨부"}</button>
            <button type="button" class="ui-btn ui-btn-ghost" disabled={longPaste.busy} onclick={() => pasteInline(p)}>그대로 넣기</button>
            <button type="button" class="ui-btn ui-btn-ghost" disabled={longPaste.busy} onclick={() => (longPaste = null)}>취소</button>
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

        <div class="composer-row flex w-full items-end gap-2">
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
            oncontextmenu={(event) => { event.preventDefault(); requestSettings("voice"); }}
            disabled={!p.target || (voice.phase !== "idle" && voice.paneId !== p.id) || (voice.paneId === p.id && voice.phase === "transcribing")}
            title={voice.paneId === p.id && voice.phase === "listening" ? "듣기 끄기 — 보내지 않고 취소 (Ctrl+M)" : "말로 입력 (Ctrl+M) · 우클릭: 음성 설정"}
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
            oncontextmenu={(event) => { event.preventDefault(); requestSettings("voice"); }}
            disabled={!p.target || (voice.convPaneId !== p.id && voice.phase !== "idle")}
            title={voice.convPaneId === p.id ? "음성 대화 끝내기 · 우클릭: 음성 설정" : "음성 대화 (Ctrl+Shift+M) — 답변을 읽어 주고 끝나면 다시 듣습니다 · 우클릭: 음성 설정"}
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
            placeholder={p.target ? "메시지, !명령, /상용구를 입력하세요" : "먼저 대화를 선택하세요"}
            value={p.composerText}
            disabled={!p.target}
            rows="1"
            oninput={(event) => { setPaneComposerValue(p.id, event.currentTarget.value); updateMentionForPane(p.id); }}
            onblur={() => setTimeout(() => p.mention && updatePane(p.id, { mention: null }), 150)}
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
              if (event.altKey && !event.ctrlKey && event.key === "ArrowDown") return; // App: next unread conversation
              if (handleSnippetKeydown(p, event)) return;
              if (event.key === "Enter" && !event.shiftKey && !event.isComposing && p.attachments.length === 0 && isTypedCommand(p.composerText)) {
                event.preventDefault();
                const text = p.composerText;
                setPaneComposerValue(p.id, "");
                void runTypedCommand(p.id, text);
                return;
              }
              handleComposerKeydown(p.id, event);
            }}
            onpaste={(event) => handlePaste(p, event)}
            onfocus={() => focusPane(p.id)}
          ></textarea>
          {#if composerStats(p.composerText)}
            <span class="pointer-events-none shrink-0 self-center whitespace-nowrap text-[10.5px] tabular-nums text-slate-400 dark:text-slate-500" title="입력한 글자 수와 줄 수">{composerStats(p.composerText)}</span>
          {/if}

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
            class="btn-gloss grid h-[34px] w-[40px] shrink-0 place-items-center rounded-md bg-blue-600 text-sm font-semibold text-white shadow-sm transition-colors hover:bg-blue-700 disabled:cursor-not-allowed disabled:bg-slate-300 dark:disabled:bg-slate-700"
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
  /* A message just opened from search / a bookmark: a soft ring that fades. */
  :global(.msg-flash .bubble), :global(.msg-flash > div > div) {
    animation: msg-flash 2.4s ease-out;
  }
  @keyframes msg-flash {
    0%, 35% { box-shadow: 0 0 0 3px rgba(245, 158, 11, 0.65); }
    100% { box-shadow: 0 0 0 3px rgba(245, 158, 11, 0); }
  }
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
    width: 22px;
    height: 22px;
    border-radius: 6px;
    color: #64748b;
    transition: background-color 150ms ease, color 150ms ease, box-shadow 150ms ease, transform 150ms ease;
  }
  :global(.dark .msg-action-btn) {
    color: #a3a3a3;
  }
  :global(.msg-action-btn:hover:not(:disabled)),
  :global(.msg-action-btn-on) {
    background: linear-gradient(180deg, rgba(219, 234, 254, 0.9), rgba(191, 219, 254, 0.75));
    color: #1d4ed8;
    box-shadow: 0 0 0 1px rgba(147, 197, 253, 0.7);
  }
  :global(.dark .msg-action-btn:hover:not(:disabled)),
  :global(.dark .msg-action-btn-on) {
    background: linear-gradient(180deg, rgba(59, 130, 246, 0.28), rgba(37, 99, 235, 0.18));
    color: #bfdbfe;
    box-shadow: 0 0 0 1px rgba(96, 165, 250, 0.35);
  }
  :global(.msg-action-btn:active:not(:disabled)) {
    transform: scale(0.97);
  }
  :global(.msg-action-btn:focus-visible) {
    outline: 2px solid #3b82f6;
    outline-offset: 1px;
  }

  /* --- Message bubbles: a soft top-to-bottom gradient per side, a small tail
     on the first bubble of each run. Colors are spelled out (not slate-*)
     because html.dark re-points the slate palette. Contrast (text on the
     gradient's darker end): user 14:1 light / 11:1 dark, AI 16:1 / 10:1. --- */
  :global(.bubble) {
    transition: box-shadow 150ms ease, background-color 150ms ease;
  }
  :global(.bubble-user) {
    background: linear-gradient(160deg, #eff6ff 0%, #dbeafe 100%);
    color: #172554;
  }
  :global(.bubble-ai) {
    background: linear-gradient(180deg, #ffffff 0%, #f6f8fb 100%);
    color: #0f172a;
  }
  :global(.bubble-system) {
    background: linear-gradient(180deg, #fffbeb 0%, #fef3c7 100%);
    color: #78350f;
  }
  :global(.dark .bubble-user) {
    background: linear-gradient(160deg, #1e3a6e 0%, #1a2f57 100%);
    color: #e0ecff;
  }
  :global(.dark .bubble-ai) {
    background: linear-gradient(180deg, #2a2a2c 0%, #242426 100%);
    color: #dcdcdc;
  }
  :global(.dark .bubble-system) {
    background: linear-gradient(180deg, rgba(120, 53, 15, 0.45) 0%, rgba(120, 53, 15, 0.3) 100%);
    color: #fde68a;
  }
  :global(.group\/msg:hover .bubble) {
    box-shadow: 0 1px 2px rgba(15, 23, 42, 0.06), 0 2px 8px rgba(15, 23, 42, 0.06), 0 0 0 1px var(--tw-ring-color, transparent);
  }
  :global(.dark .group\/msg:hover .bubble) {
    box-shadow: 0 2px 8px rgba(0, 0, 0, 0.35), 0 0 0 1px var(--tw-ring-color, transparent);
  }
  /* The tail: a small corner wedge in the bubble's top color, outlined with a
     hairline (drop-shadow on the wrapper, since clip-path would cut a border). */
  :global(.bubble-tail) {
    position: absolute;
    top: 0;
    z-index: 1;
    width: 8px;
    height: 10px;
    pointer-events: none;
  }
  :global(.bubble-tail::before) {
    content: "";
    position: absolute;
    inset: 0;
  }
  :global(.bubble-tail-ai) {
    left: -7px;
    filter: drop-shadow(-0.5px 0 0 #e2e8f0) drop-shadow(0 -0.5px 0 #e2e8f0);
  }
  :global(.bubble-tail-ai::before) {
    background: #ffffff;
    clip-path: polygon(0 0, 100% 0, 100% 100%);
  }
  :global(.bubble-tail-user) {
    right: -7px;
    filter: drop-shadow(0.5px 0 0 #bfdbfe) drop-shadow(0 -0.5px 0 #bfdbfe);
  }
  :global(.bubble-tail-user::before) {
    background: #eff6ff;
    clip-path: polygon(0 0, 100% 0, 0 100%);
  }
  :global(.dark .bubble-tail-ai) {
    filter: drop-shadow(-0.5px 0 0 #3c3c3c) drop-shadow(0 -0.5px 0 #3c3c3c);
  }
  :global(.dark .bubble-tail-ai::before) {
    background: #2a2a2c;
  }
  :global(.dark .bubble-tail-user) {
    filter: drop-shadow(0.5px 0 0 rgba(30, 64, 175, 0.6)) drop-shadow(0 -0.5px 0 rgba(30, 64, 175, 0.6));
  }
  :global(.dark .bubble-tail-user::before) {
    background: #1e3a6e;
  }

  /* --- Avatars (24px): initial "나" for the user, a spark for the AI. --- */
  :global(.msg-avatar) {
    display: grid;
    flex-shrink: 0;
    place-items: center;
    width: 24px;
    height: 24px;
    margin-top: 1px;
    border-radius: 9999px;
    font-size: 11px;
    font-weight: 700;
    line-height: 1;
    user-select: none;
    box-shadow: 0 1px 2px rgba(15, 23, 42, 0.12), inset 0 0 0 1px rgba(255, 255, 255, 0.25);
  }
  :global(.msg-avatar-user) {
    background: linear-gradient(145deg, #e0e7ff 0%, #c7d2fe 100%);
    color: #312e81;
  }
  :global(.msg-avatar-ai) {
    background: linear-gradient(145deg, #4f46e5 0%, #0284c7 100%);
    color: #ffffff;
  }
  :global(.dark .msg-avatar-user) {
    background: linear-gradient(145deg, #3730a3 0%, #312e81 100%);
    color: #e0e7ff;
  }
  :global(.dark .msg-avatar-ai) {
    background: linear-gradient(145deg, #6366f1 0%, #0369a1 100%);
  }

  /* --- In-app confirm card + its small buttons. --- */
  :global(.fork-confirm) {
    background: linear-gradient(180deg, #eff6ff 0%, #e0edff 100%);
    animation: msg-enter-anim 0.15s ease-out;
  }
  :global(.dark .fork-confirm) {
    background: linear-gradient(180deg, rgba(30, 58, 110, 0.6) 0%, rgba(23, 37, 84, 0.55) 100%);
  }
  :global(.ui-btn) {
    border-radius: 6px;
    padding: 2px 10px;
    font-size: 12px;
    font-weight: 600;
    line-height: 1.6;
    transition: background-color 150ms ease, color 150ms ease, box-shadow 150ms ease, transform 150ms ease, opacity 150ms ease;
  }
  :global(.ui-btn:active:not(:disabled)) {
    transform: scale(0.97);
  }
  :global(.ui-btn:disabled) {
    cursor: not-allowed;
    opacity: 0.5;
  }
  :global(.ui-btn:focus-visible) {
    outline: 2px solid #3b82f6;
    outline-offset: 1px;
  }
  :global(.ui-btn-ghost) {
    color: #1e3a8a;
  }
  :global(.ui-btn-ghost:hover:not(:disabled)) {
    background: rgba(147, 197, 253, 0.35);
  }
  :global(.dark .ui-btn-ghost) {
    color: #dbeafe;
  }
  :global(.dark .ui-btn-ghost:hover:not(:disabled)) {
    background: rgba(96, 165, 250, 0.2);
  }
  :global(.ui-btn-primary) {
    background: linear-gradient(180deg, #3b82f6 0%, #2563eb 100%);
    color: #ffffff;
    box-shadow: 0 1px 2px rgba(37, 99, 235, 0.35), inset 0 1px 0 rgba(255, 255, 255, 0.18);
  }
  :global(.ui-btn-primary:hover:not(:disabled)) {
    background: linear-gradient(180deg, #2563eb 0%, #1d4ed8 100%);
    box-shadow: 0 2px 6px rgba(37, 99, 235, 0.4), inset 0 1px 0 rgba(255, 255, 255, 0.18);
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
