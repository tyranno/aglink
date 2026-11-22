<script>
  import { onMount, tick, untrack } from "svelte";
  import { Events } from "@wailsio/runtime";
  import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";
  import PaneNode from "./PaneNode.svelte";
  import GroupNode from "./GroupNode.svelte";
  import PlaybookPanel from "./PlaybookPanel.svelte";
  import ReservationPanel from "./ReservationPanel.svelte";
  import MCPServerPanel from "./MCPServerPanel.svelte";
  import ClaudeAuthPanel from "./ClaudeAuthPanel.svelte";
  import ApprovalPrompt from "./ApprovalPrompt.svelte";
  import UsagePanel from "./UsagePanel.svelte";
  import MeetingPanel from "./MeetingPanel.svelte";
  import VoicePanel from "./VoicePanel.svelte";
  import ImageWindow from "./ImageWindow.svelte";
  import CloneScript from "./CloneScript.svelte";
  import MarkdownPreview from "./MarkdownPreview.svelte";
  import AttentionToast from "./AttentionToast.svelte";
  import QuickSwitcher from "./QuickSwitcher.svelte";
  import BoardView from "./BoardView.svelte";
  import {
    chat,
    MAX_PANES,
    WORKING_GRACE_MS,
    parseJSON,
    findWebConv,
    pane,
    backendLabel,
    addPane,
    focusedPane,
    selectTargetInPane,
    selectTargetFromSidebar,
    isFocusedTarget,
    loadConversations,
    handleFrame,
    setUnread,
    stopWorking,
    clearDraft,
    clearPaneAttachments,
    updatePane,
    handleConversationDragStart,
    handleConversationDragEnd,
    createWebGroup,
    renameWebGroup,
    deleteWebGroup,
    setConversationGroup,
    ungroupedWebConvs,
    archivedWebConvs,
    setConversationArchived,
    setConversationPinned,
    isConversationPinned,
    pinnedWebConvs,
    showToast,
    CONV_LABELS,
    convLabel,
    setConversationLabel,
    rememberRecentTarget,
    rootWebGroups,
    groupPathLabel,
    handleGroupDragOver,
    handleGroupDragLeave,
    handleGroupDrop,
    UNGROUPED_DROP_ZONE,
    closeProgressPopup,
    closeLightbox,
    closePaneBackendMenu,
    setTargetBackend,
    closePaneWorkDirMenu,
    setTargetWorkDir,
  } from "./paneStore.svelte.js";
  import { theme, setTheme } from "./themeStore.svelte.js";
  import { voice, startMeeting, stopMeeting, wakeNameLabel } from "./voice.svelte.js";
  import SnippetManager from "./SnippetManager.svelte";
  import { snippets, openSnippetManager } from "./snippets.svelte.js";
  import SearchPanel from "./SearchPanel.svelte";
  import RecentSwitcher from "./RecentSwitcher.svelte";
  import { searchPanel, openSearchPanel } from "./search.svelte.js";

  // The conversation voice chat is talking to, marked in the list.
  function isVoiceConv(id) {
    const t = voice.convPaneId && pane(voice.convPaneId)?.target;
    return t?.kind === "web" && t.id === id;
  }

  // Sidebar visibility/tab is a UI preference persisted (like the pane layout) so
  // hiding the conversation list survives a restart instead of always reopening.
  const SIDEBAR_STORAGE_KEY = "aglink-desktop:sidebar";
  function loadSidebarPrefs() {
    try {
      const raw = localStorage.getItem(SIDEBAR_STORAGE_KEY);
      if (raw) return JSON.parse(raw) || {};
    } catch {}
    return {};
  }
  const sidebarPrefs = loadSidebarPrefs();

  let view = $state("chat");
  let settingsTab = $state("settings");
  let voiceLoaded = $state(false); // VoicePanel is mounted once then kept (hidden) to avoid re-mount lag
  // 알려진 탭 값만 복원한다 — 저장된 값이 낯설면 "chat"으로 떨어뜨려야, 렌더링
  // 분기의 마지막 else(MCP)가 알 수 없는 값을 떠안는 일이 없다.
  // 탭은 아이콘 아래 짧은 이름(short)을 작게 — 긴 이름은 툴팁으로. 한 줄을 넘지
  // 않도록 short 는 2~3글자로 둔다.
  const SIDEBAR_TABS = [
    { id: "chat", icon: "💬", label: "대화", short: "대화" },
    { id: "playbook", icon: "📋", label: "업무 관리", short: "업무" },
    { id: "reservation", icon: "⏰", label: "예약", short: "예약" },
    { id: "meeting", icon: "🎤", label: "회의", short: "회의" },
    { id: "mcp", icon: "🧩", label: "MCP", short: "MCP" },
    { id: "usage", icon: "📊", label: "사용량", short: "사용량" },
  ];
  let sidebarTab = $state(
    SIDEBAR_TABS.some((t) => t.id === sidebarPrefs.tab) ? sidebarPrefs.tab : "chat",
  ); // 대화 목록 / 업무 관리 / 예약 / 회의 / MCP / 사용량
  let sidebarCollapsed = $state(sidebarPrefs.collapsed === true);
  $effect(() => {
    // Re-runs whenever either changes → persists the current sidebar state.
    try {
      localStorage.setItem(SIDEBAR_STORAGE_KEY, JSON.stringify({ collapsed: sidebarCollapsed, tab: sidebarTab }));
    } catch {}
  });
  let versionInfo = $state({});
  let auxInfo = $state({ features: [] });
  let settingsSchema = $state([]);
  let settingsValues = $state({});
  let settingsOriginal = $state({});
  let settingsMsg = $state("");
  let settingsMsgWarn = $state(false); // settingsMsg is a failure / "saved but not applied"
  let activeSettingsGroup = $state("");
  let configText = $state("");
  let configOriginal = $state(""); // the raw file as loaded, for the unsaved-change check
  let settingsLoading = $state(false);
  let auxLoading = $state(false);
  let settingsQuery = $state(""); // 설정 찾기: filters fields across every group
  let configMsg = $state("");
  let configMsgWarn = $state(false);
  let openMenuId = $state("");

  // Header "⋯ 더보기" menu: less frequent header actions (회의 모드, 회의 목록,
  // 테마) live here. Own popup — closes on outside click (clickAway) and Esc,
  // arrow keys move between items, Esc returns focus to the trigger.
  let moreMenuOpen = $state(false);
  let boardOpen = $state(false); // 작업 보드 (BoardView, also Ctrl+B)
  let moreMenuTrigger = $state(null);
  let moreMenuEl = $state(null);
  function moreMenuItems() {
    return moreMenuEl ? [...moreMenuEl.querySelectorAll('[role="menuitem"]')] : [];
  }
  async function openMoreMenu() {
    moreMenuOpen = true;
    await tick();
    moreMenuItems()[0]?.focus();
  }
  function closeMoreMenu(returnFocus = false) {
    if (!moreMenuOpen) return;
    moreMenuOpen = false;
    if (returnFocus) moreMenuTrigger?.focus();
  }
  function toggleMoreMenu() {
    if (moreMenuOpen) closeMoreMenu();
    else void openMoreMenu();
  }
  function handleMoreMenuKeydown(event) {
    const items = moreMenuItems();
    const index = items.indexOf(document.activeElement);
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      closeMoreMenu(true);
    } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const step = event.key === "ArrowDown" ? 1 : -1;
      items[(index + step + items.length) % items.length]?.focus();
    } else if (event.key === "Home" || event.key === "End") {
      event.preventDefault();
      (event.key === "Home" ? items[0] : items[items.length - 1])?.focus();
    } else if (event.key === "Tab") {
      closeMoreMenu();
    }
  }
  let showArchived = $state(false);

  // In-app file preview panel. A file path shown in a message (markdown.js marks
  // it with class "md-file-link") opens here, rendered — .md as Markdown, other
  // text files verbatim. Relative paths resolve against the focused web
  // conversation's working directory, where the worker wrote them.
  let preview = $state({ open: false, name: "", path: "", content: "", error: "", isMarkdown: false });

  async function openPreview(path) {
    if (!path) return;
    let baseDir = "";
    const t = focusedPane()?.target;
    if (t?.kind === "web" && t.id) baseDir = findWebConv(t.id)?.workDir || "";
    try {
      const data = parseJSON(await ControlService.PreviewFile(path, baseDir), { name: path, path, content: "", error: "열 수 없습니다" });
      const name = data.name || path;
      preview = { open: true, name, path: data.path || path, content: data.content || "", error: data.error || "", isMarkdown: /\.(md|markdown)$/i.test(name) };
    } catch (error) {
      preview = { open: true, name: path, path, content: "", error: String(error), isMarkdown: false };
    }
  }

  // 회의 목록: the meeting tab of the sidebar (header 🗂, tray "회의 목록").
  function showMeetings() {
    view = "chat";
    sidebarCollapsed = false;
    sidebarTab = "meeting";
  }

  function closePreview() {
    preview = { ...preview, open: false };
  }
  let promptState = $state(null);
  let confirmState = $state(null);

  let promptResolver = null;
  let confirmResolver = null;

  function conversationLabel(conv) {
    return conv?.title || conv?.id || "이름 없는 대화";
  }

  // Sidebar filter: while it has text the grouped tree is replaced by one flat
  // list of matching conversations (title, #id or work folder; archived too).
  let convFilter = $state("");
  let labelFilter = $state(""); // color label id: show only conversations with it
  const listFiltering = $derived(!!convFilter.trim() || !!labelFilter);
  // Color chips appear in the filter row once any conversation has a label.
  const usedLabels = $derived(CONV_LABELS.filter((label) => [...chat.convLabels.values()].includes(label.id)));
  $effect(() => {
    if (labelFilter && !usedLabels.some((label) => label.id === labelFilter)) labelFilter = "";
  });
  function filteredWebConvs() {
    const q = convFilter.trim().toLowerCase();
    return chat.webConvs.filter((conv) =>
      (!labelFilter || chat.convLabels.get(String(conv.id)) === labelFilter) &&
      (!q || `${conversationLabel(conv)} #${conv.id} ${conv.workDir || ""}`.toLowerCase().includes(q)));
  }

  // Ctrl+Tab's recent list: every newly focused conversation goes on top.
  $effect(() => {
    const target = focusedPane()?.target;
    if (target) untrack(() => rememberRecentTarget(target));
  });

  // Cycles system → light → dark → system on click; the icon always reflects
  // the currently *selected* mode (not the resolved light/dark), so "system"
  // stays visible even while it happens to resolve to a dark-looking icon.
  const THEME_ORDER = ["system", "light", "dark"];
  function cycleTheme() {
    setTheme(THEME_ORDER[(THEME_ORDER.indexOf(theme.mode) + 1) % THEME_ORDER.length]);
  }
  function themeIcon() {
    if (theme.mode === "light") return "☀";
    if (theme.mode === "dark") return "🌙";
    return "🖥";
  }
  function themeLabel() {
    if (theme.mode === "light") return "라이트";
    if (theme.mode === "dark") return "다크";
    return "시스템 설정";
  }

  // The backend menus (telegram channel / local conversation) mark the current
  // choice with ✓; an empty backend means "follow the default".
  const BACKEND_CHOICES = [
    { id: "default", label: "기본값" },
    { id: "claude", label: "Claude" },
    { id: "codex", label: "Codex" },
    { id: "opencode", label: "OpenCode" },
  ];
  function backendChoice(backend) {
    const value = String(backend || "").toLowerCase();
    return ["claude", "codex", "opencode"].includes(value) ? value : "default";
  }

  async function setChannelBackend(target, backend) {
    openMenuId = "";
    await setTargetBackend(target, backend);
  }

  async function loadVersionInfo() {
    try {
      versionInfo = parseJSON(await ControlService.GetVersion(), {});
    } catch {
      versionInfo = {};
    }
    chat.defaultBackend = versionInfo.backend || "";
  }

  async function loadAuxInfo() {
    try {
      auxInfo = parseJSON(await ControlService.GetAux(), { features: [] });
    } catch {
      auxInfo = { features: [] };
    }
  }

  async function loadSettingsSchema() {
    settingsMsg = "";
    settingsMsgWarn = false;
    settingsLoading = true;
    try {
      const data = parseJSON(await ControlService.GetSettings(), { sections: [] });
      settingsSchema = Array.isArray(data.sections) ? data.sections : [];
      const nextValues = {};
      for (const section of settingsSchema) {
        for (const field of section.fields || []) {
          nextValues[field.key] = field.value;
        }
      }
      settingsValues = nextValues;
      settingsOriginal = { ...nextValues };
    } catch (error) {
      settingsSchema = [];
      settingsMsg = `설정을 불러오지 못했습니다: ${error}`;
    } finally {
      settingsLoading = false;
    }
  }

  async function loadRawConfig() {
    configMsg = "";
    configMsgWarn = false;
    try {
      const data = parseJSON(await ControlService.GetConfig(), { config: "", error: "" });
      if (data.error) {
        configMsg = data.error;
        configText = "";
        configOriginal = "";
        return;
      }
      configText = data.config || "";
      configOriginal = configText;
    } catch (error) {
      configMsg = `설정 파일을 불러오지 못했습니다: ${error}`;
      configText = "";
      configOriginal = "";
    }
  }

  // --- Unsaved settings: how many structured fields differ from what was
  // loaded, plus whether the raw file was edited. Leaving the 설정 tab (back to
  // chat, another tab, Ctrl+N …) with either asks first, since coming back
  // reloads both from the host and the edits would be lost silently. ---
  function fieldChanged(key) {
    return String(settingsValues[key]) !== String(settingsOriginal[key]);
  }
  const settingsChangedCount = $derived(Object.keys(settingsValues).filter((key) => fieldChanged(key)).length);
  const rawConfigChanged = $derived(configText !== configOriginal);
  const settingsDirty = $derived(settingsChangedCount > 0 || rawConfigChanged);

  function discardSettingsEdits() {
    settingsValues = { ...settingsOriginal };
    configText = configOriginal;
    listDrafts = {};
  }

  // Resolves true when it is fine to leave the 설정 tab (nothing unsaved, or
  // the user chose to throw the edits away).
  async function confirmLeaveSettings() {
    if (view !== "settings" || settingsTab !== "settings" || !settingsDirty) return true;
    const parts = [];
    if (settingsChangedCount > 0) parts.push(`항목 ${settingsChangedCount}개`);
    if (rawConfigChanged) parts.push("설정 파일 직접 편집");
    const ok = await askConfirm(
      "저장하지 않은 변경",
      `${parts.join(", ")}에 저장하지 않은 변경이 있습니다.\n저장하지 않고 나가면 고친 내용이 사라집니다.`,
      "버리고 나가기",
    );
    if (ok) discardSettingsEdits();
    return ok;
  }

  async function refreshSettingsTab(tab) {
    if (tab === "voice") {
      voiceLoaded = true;
      return;
    }
    if (tab === "settings") {
      await Promise.all([loadSettingsSchema(), loadRawConfig()]);
      return;
    }
    auxLoading = true;
    try {
      await Promise.all([loadVersionInfo(), loadAuxInfo()]);
    } finally {
      auxLoading = false;
    }
  }

  // A tab button on the settings screen. Clicking the open 설정 tab again keeps
  // the edits (no reload); the other tabs simply refresh.
  async function loadSettingsTab(tab) {
    if (view === "settings" && tab === settingsTab) {
      if (tab !== "settings") await refreshSettingsTab(tab);
      return;
    }
    if (!(await confirmLeaveSettings())) return;
    settingsTab = tab;
    await refreshSettingsTab(tab);
  }

  async function openSettings(tab) {
    if (view === "settings") {
      await loadSettingsTab(tab);
      return;
    }
    view = "settings";
    settingsTab = tab;
    await refreshSettingsTab(tab);
  }

  async function backToChat() {
    if (!(await confirmLeaveSettings())) return false;
    view = "chat";
    await tick();
    return true;
  }

  function updateSettingValue(key, value) {
    settingsValues = { ...settingsValues, [key]: value };
  }

  // "list" fields (e.g. approvals.allow) arrive and are saved as a comma-joined
  // string — the same wire format older clients edit as plain text — and are
  // shown here as removable chips plus an add input.
  let listDrafts = $state({});
  function listItems(value) {
    return String(value ?? "")
      .split(/[,;\n]/)
      .map((item) => item.trim())
      .filter(Boolean);
  }
  function addListItems(key) {
    const items = listItems(settingsValues[key]);
    const seen = new Set(items.map((item) => item.toLowerCase()));
    for (const item of listItems(listDrafts[key])) {
      if (seen.has(item.toLowerCase())) continue;
      seen.add(item.toLowerCase());
      items.push(item);
    }
    listDrafts = { ...listDrafts, [key]: "" };
    updateSettingValue(key, items.join(", "));
  }
  function removeListItem(key, index) {
    const items = listItems(settingsValues[key]);
    items.splice(index, 1);
    updateSettingValue(key, items.join(", "));
  }

  // A select field's current value can be a legacy/custom string the backend
  // didn't detect (e.g. a hand-typed codex model from before this dropdown
  // existed). Keep it selectable and visibly correct instead of the native
  // <select> silently falling back to the first option.
  function selectOptionsFor(field) {
    const options = field.options || [];
    const current = settingsValues[field.key] ?? "";
    if (current === "" || options.includes(current)) return options;
    return [...options, current];
  }

  // Settings are split into tabs by section.group. A section can also be gated
  // on another field's *live* value (section.visibleWhen) — e.g. the opencode
  // provider sections appear only once "사용할 AI" is set to opencode. Because
  // settingsValues is reactive, switching the backend re-evaluates visibility
  // (and empty tabs disappear) without a save/reload.
  function sectionVisible(section) {
    const vw = section?.visibleWhen;
    if (!vw) return true;
    return String(settingsValues[vw.key] ?? "") === String(vw.equals);
  }
  function sectionsInGroup(group) {
    return settingsSchema.filter((s) => (s.group || "기타") === group && sectionVisible(s));
  }
  const settingsGroups = $derived.by(() => {
    const order = [];
    for (const section of settingsSchema) {
      const group = section.group || "기타";
      if (sectionVisible(section) && !order.includes(group)) order.push(group);
    }
    return order;
  });
  // 설정 찾기: with a query the group tabs give way to every visible section
  // that has a matching field (label, description or key); a section whose own
  // title/description matches keeps all its fields.
  const settingsSearchResults = $derived.by(() => {
    const q = settingsQuery.trim().toLowerCase();
    if (!q) return null;
    const out = [];
    for (const section of settingsSchema) {
      if (!sectionVisible(section)) continue;
      const fields = section.fields || [];
      const sectionHit = `${section.title || ""} ${section.desc || ""}`.toLowerCase().includes(q);
      const hits = sectionHit
        ? fields
        : fields.filter((field) => `${field.label || ""} ${field.desc || ""} ${field.key || ""}`.toLowerCase().includes(q));
      if (hits.length) out.push({ ...section, fields: hits });
    }
    return out;
  });

  // Keep the active tab valid: if it vanishes (e.g. leaving opencode hides the
  // "무료·로컬 AI" tab), fall back to the first remaining tab.
  $effect(() => {
    if (settingsGroups.length && !settingsGroups.includes(activeSettingsGroup)) {
      activeSettingsGroup = settingsGroups[0];
    }
  });

  async function saveSettings() {
    const updates = {};
    for (const [key, value] of Object.entries(settingsValues)) {
      if (String(value) !== String(settingsOriginal[key])) updates[key] = value;
    }
    if (Object.keys(updates).length === 0) {
      settingsMsgWarn = false;
      settingsMsg = "변경된 항목이 없습니다.";
      return;
    }
    settingsMsg = "저장 중...";
    settingsMsgWarn = false;
    try {
      const reply = parseJSON(
        await ControlService.SetSettings(JSON.stringify(updates)),
        { ok: false, error: "unknown error" },
      );
      if (reply.ok && reply.applied === false) {
        // The file was replaced but the live apply failed: the running config is
        // still the old one, so don't refetch (that would re-render the old
        // values over the user's edits) — say exactly what happened instead.
        settingsMsgWarn = true;
        settingsMsg = applyFailedMsg(reply.applyError);
        settingsOriginal = { ...settingsValues }; // the file has them now: nothing left unsaved
      } else if (reply.ok) {
        // aglink applies a saved config before replying (host ConfigApplier), so
        // both refetches read the new values — no debounce to wait out. Set the
        // message after them: loadSettingsSchema clears it.
        await Promise.all([loadSettingsSchema(), loadVersionInfo()]);
        settingsMsg = "저장하고 적용했습니다. 주소·기동 항목은 재시작해야 반영됩니다.";
      } else {
        settingsMsgWarn = true;
        settingsMsg = `저장 안 됨: ${reply.error || "알 수 없는 오류"}`;
      }
    } catch (error) {
      settingsMsgWarn = true;
      settingsMsg = `저장 안 됨: ${error}`;
    }
  }

  // "Saved but not applied" (host configSaveReply: ok + applied:false).
  function applyFailedMsg(applyError) {
    return `파일에는 저장됐지만 적용하지 못했습니다: ${applyError || "알 수 없는 오류"} (이전 설정으로 동작 중 — 고친 뒤 다시 저장하거나 재시작하세요)`;
  }

  async function saveRawConfig() {
    configMsg = "저장 중...";
    configMsgWarn = false;
    const sent = configText;
    try {
      const reply = parseJSON(
        await ControlService.SetConfig(sent),
        { ok: false, error: "unknown error" },
      );
      if (reply.ok && reply.applied === false) {
        configMsgWarn = true;
        configMsg = applyFailedMsg(reply.applyError);
        configOriginal = sent;
      } else if (reply.ok) {
        configOriginal = sent;
        configMsg = "설정 파일을 저장하고 적용했습니다.";
      } else {
        configMsgWarn = true;
        configMsg = `저장 안 됨: ${reply.error || "알 수 없는 오류"}`;
      }
    } catch (error) {
      configMsgWarn = true;
      configMsg = `저장 안 됨: ${error}`;
    }
  }

  async function pollWorkers(force = false) {
    if (!force && chat.working.size === 0) return;
    try {
      const data = parseJSON(await ControlService.GetActiveWorkers(), { workers: [] });
      if (typeof data.baseTimeoutMinutes === "number" && data.baseTimeoutMinutes > 0) {
        chat.baseTimeoutMin = data.baseTimeoutMinutes;
      }
      const activeKeys = new Set(
        (data.workers || []).map((worker) =>
          worker.conversationId === "telegram" ? "telegram" : `web:${worker.conversationId}`,
        ),
      );

      let next = new Map(chat.working);
      let changed = false;
      const now = Date.now();

      for (const key of activeKeys) {
        if (!next.has(key)) {
          next.set(key, { startedAt: now, lastAliveAt: now });
          changed = true;
        }
      }

      for (const [key, entry] of [...next.entries()]) {
        if (!activeKeys.has(key) && now - entry.startedAt > WORKING_GRACE_MS) {
          next.delete(key);
          changed = true;
        }
      }

      if (changed) chat.working = next;
    } catch {
      // Leave current state untouched while the control API is unavailable.
    }
  }

  async function newWebConversation() {
    const title = await askText("새 로컬 대화", "선택 사항: 제목", "");
    if (title === null) return;
    try {
      await ControlService.WebNew(title.trim());
      await loadConversations({ forceReloadCurrent: true });
      const active = focusedPane();
      if (chat.webConvs[0] && active) await selectTargetInPane(active.id, { kind: "web", id: chat.webConvs[0].id });
    } catch (error) {
      chat.statusNote = `새 대화를 만들지 못했습니다: ${error}`;
    }
  }

  async function renameWebConversation(conv) {
    const nextTitle = await askText("이름 변경", "새 대화 이름", conv.title || "");
    if (nextTitle === null) return;
    try {
      await ControlService.WebRename(conv.id, nextTitle.trim());
      const shown = chat.panes.some((item) => item.target?.kind === "web" && item.target.id === conv.id);
      await loadConversations({ forceReloadCurrent: shown });
    } catch (error) {
      chat.statusNote = `이름 변경 실패: ${error}`;
    }
  }

  async function changeWebDirectory(conv) {
    openMenuId = "";
    await setTargetWorkDir({ kind: "web", id: conv.id });
  }

  async function deleteWebConversation(conv) {
    const confirmed = await askConfirm(
      "대화 삭제",
      `"${conversationLabel(conv)}" 대화를 삭제하시겠습니까?`,
      "삭제",
    );
    if (!confirmed) return;
    try {
      await ControlService.WebDelete(conv.id);
      const key = `web:${conv.id}`;
      setUnread(key, false);
      stopWorking(key);
      clearDraft(key);
      setConversationGroup(conv.id, null);
      setConversationPinned(conv.id, false);
      for (const item of chat.panes) {
        if (item.target?.kind === "web" && item.target.id === conv.id) {
          clearPaneAttachments(item.id);
          updatePane(item.id, { target: null, composerText: "" });
        }
      }
      await loadConversations({ forceReloadCurrent: true });
    } catch (error) {
      chat.statusNote = `대화 삭제 실패: ${error}`;
    }
  }

  async function newWebGroup() {
    const name = await askText("새 그룹", "그룹 이름", "");
    if (name === null) return;
    createWebGroup(name);
  }

  async function moveConversationToNewGroup(conv) {
    const name = await askText("새 그룹", "그룹 이름", "");
    if (name === null) return;
    const id = createWebGroup(name);
    if (id) setConversationGroup(conv.id, id);
  }

  async function renameWebGroupPrompt(group) {
    const name = await askText("그룹 이름 변경", "새 그룹 이름", group.name);
    if (name === null) return;
    renameWebGroup(group.id, name);
  }

  async function deleteWebGroupConfirm(group) {
    const confirmed = await askConfirm(
      "그룹 삭제",
      `"${group.name}" 그룹을 삭제하시겠습니까? 안의 대화는 "그룹 없음"으로, 하위 그룹은 최상위로 이동합니다.`,
      "삭제",
    );
    if (!confirmed) return;
    deleteWebGroup(group.id);
  }

  function askText(title, label, initialValue) {
    return new Promise((resolve) => {
      promptResolver = resolve;
      promptState = { title, label, value: initialValue || "" };
    });
  }

  function resolvePrompt(result) {
    const resolver = promptResolver;
    promptResolver = null;
    promptState = null;
    resolver?.(result);
  }

  function askConfirm(title, message, confirmLabel = "확인") {
    return new Promise((resolve) => {
      confirmResolver = resolve;
      confirmState = { title, message, confirmLabel };
    });
  }

  function resolveConfirm(result) {
    const resolver = confirmResolver;
    confirmResolver = null;
    confirmState = null;
    resolver?.(result);
  }

  // Focus-on-open for the in-app dialogs (prompt / confirm): the prompt's input
  // gets the caret with its text selected, the confirm its main button.
  function autofocus(node, select = false) {
    requestAnimationFrame(() => {
      node.focus();
      if (select && typeof node.select === "function") node.select();
    });
  }

  // --- Header voice chip: the same `voice` store VoicePanel and the tray
  // indicator read (voice.svelte.js), boiled down to one word. ---
  const voiceChip = $derived.by(() => {
    if (voice.status === "installing") return { label: "음성 준비 중", dot: "bg-sky-300", pulse: true, tip: "음성 인식을 내려받는 중입니다" };
    if (voice.phase === "listening") return { label: voice.warming ? "준비 중" : "듣는 중", dot: "bg-rose-400", pulse: true, tip: "마이크가 말을 듣고 있습니다" };
    if (voice.phase === "transcribing") return { label: "알아듣는 중", dot: "bg-amber-300", pulse: true, tip: "들은 말을 글자로 바꾸는 중입니다" };
    if (voice.speaking) return { label: "말하는 중", dot: "bg-sky-300", pulse: true, tip: "답변을 소리 내어 읽는 중입니다" };
    if (voice.meeting) return { label: "회의 듣는 중", dot: "bg-rose-400", pulse: true, tip: "회의 모드로 주변 대화를 받아 적는 중입니다" };
    if (voice.convPaneId) return { label: "음성 대화", dot: "bg-emerald-400", pulse: false, tip: "음성 대화가 켜져 있습니다" };
    if (voice.waking) return { label: "호출어 대기", dot: "bg-emerald-400/70", pulse: false, tip: `“${wakeNameLabel()}”라고 부르면 듣기 시작합니다` };
    return { label: "음성 꺼짐", dot: "bg-slate-500", pulse: false, tip: "음성 기능이 쉬고 있습니다" };
  });

  // --- "연결 끊김" details: the desktop keeps retrying on its own (control.go:
  // back-off up to 10 s), so the popover explains that and offers a check now. ---
  let connInfoOpen = $state(false);
  let disconnectedAt = $state(0);
  let connChecking = $state(false);
  let connCheckMsg = $state("");
  async function checkConnectionNow() {
    connChecking = true;
    connCheckMsg = "";
    try {
      const ok = await ControlService.Connected();
      chat.connected = !!ok;
      if (ok) {
        await Promise.all([loadConversations({ forceReloadCurrent: true }), loadVersionInfo()]);
        connInfoOpen = false;
      } else {
        connCheckMsg = "아직 연결되지 않았어요. 자동으로 계속 다시 시도합니다.";
      }
    } catch (error) {
      connCheckMsg = `확인하지 못했습니다: ${error}`;
    } finally {
      connChecking = false;
    }
  }
  function disconnectedFor() {
    if (!disconnectedAt) return "";
    const sec = Math.max(0, Math.round((chat.nowTick - disconnectedAt) / 1000));
    if (sec < 60) return `${sec}초째`;
    if (sec < 3600) return `${Math.floor(sec / 60)}분째`;
    return `${Math.floor(sec / 3600)}시간째`;
  }

  // The status banner offers a retry for the failures that have an obvious one.
  function statusRetry(note) {
    if (/^대화 목록을 불러오지 못했습니다/.test(note)) return { label: "다시 불러오기", run: () => loadConversations({ forceReloadCurrent: true }) };
    if (/^새 대화를 만들지 못했습니다/.test(note)) return { label: "다시 만들기", run: () => newWebConversation() };
    return null;
  }

  // --- Unread conversations: counted on the 💬 tab, and Alt+↓ jumps to the
  // next one (in sidebar order, starting after the focused conversation). ---
  const unreadCount = $derived(chat.unread.size);
  function unreadTargetsInOrder() {
    const list = [];
    if (chat.telegram && chat.unread.has("telegram")) list.push({ kind: "telegram" });
    for (const conv of chat.webConvs) {
      if (chat.unread.has(`web:${conv.id}`)) list.push({ kind: "web", id: conv.id });
    }
    return list;
  }
  function openNextUnread() {
    const list = unreadTargetsInOrder();
    if (!list.length) {
      showToast("읽지 않은 대화가 없습니다");
      return;
    }
    selectTargetFromSidebar(list[0]);
  }

  async function toggleSidebar() {
    if (view !== "chat") return;
    sidebarCollapsed = !sidebarCollapsed;
  }

  async function newConversationFromAnywhere() {
    if (view !== "chat" && !(await backToChat())) return;
    if (sidebarCollapsed) sidebarCollapsed = false;
    sidebarTab = "chat";
    await newWebConversation();
  }

  let helpOpen = $state(false);

  // Ctrl+K palette commands (QuickSwitcher lists them after the conversations).
  const paletteActions = [
    { id: "new", icon: "＋", label: "새 로컬 대화 만들기", keys: "Ctrl+N", run: newConversationFromAnywhere },
    { id: "settings", icon: "⚙", label: "설정 열기", keys: "Ctrl+,", run: () => openSettings("settings") },
    { id: "voice", icon: "🎙", label: "음성 설정 열기", run: () => openSettings("voice") },
    { id: "connection", icon: "🔌", label: "연결 상태 / aglink 정보", run: () => openSettings("connection") },
    { id: "sidebar", icon: "◧", label: "사이드바 숨기기/보이기", keys: "Ctrl+\\", run: toggleSidebar },
    { id: "unread", icon: "●", label: "다음 읽지 않은 대화로 이동", keys: "Alt+↓", run: openNextUnread },
    { id: "search-all", icon: "⌕", label: "모든 대화에서 메시지 찾기", keys: "Ctrl+Shift+F", run: () => openSearchPanel("search") },
    { id: "bookmarks", icon: "★", label: "북마크 보기", run: () => openSearchPanel("bookmarks") },
    { id: "split", icon: "⊞", label: "화면 분할 추가", run: () => { if (chat.panes.length < MAX_PANES) addPane(); } },
    { id: "board", icon: "▦", label: "작업 보드 열기", keys: "Ctrl+B", run: () => (boardOpen = true) },
    { id: "snippets", icon: "/", label: "상용구 관리", run: () => openSnippetManager() },
    { id: "meeting", icon: "🗂", label: "회의 목록 보기", run: showMeetings },
    { id: "theme", icon: "◐", label: "테마 전환 (시스템 → 라이트 → 다크)", run: cycleTheme },
    { id: "help", icon: "?", label: "단축키 모음", keys: "Ctrl+/", run: () => (helpOpen = true) },
  ];

  function versionBadgeText() {
    const version = versionInfo.version || "?";
    const parts = [];
    if (versionInfo.updateAvailable) parts.push("업데이트 가능");
    if (versionInfo.opencodeUpdateAvailable) parts.push("opencode 업데이트");
    return parts.length ? `${version} · ${parts.join(" · ")}` : version;
  }

  function backendBadgeText() {
    return versionInfo.backend || chat.telegram?.backend || "";
  }

  function auxStateLabel(state) {
    const labels = { running: "실행 중", idle: "대기 중", stopped: "멈춤", disabled: "꺼짐", error: "오류", missing: "없음" };
    return labels[state] || state || "알 수 없음";
  }

  function auxStateTone(state) {
    if (state === "running") return "bg-emerald-100 dark:bg-emerald-900/40 text-emerald-700 dark:text-emerald-300";
    if (state === "idle") return "bg-slate-200 dark:bg-slate-700 text-slate-600 dark:text-slate-400";
    return "bg-rose-100 dark:bg-rose-900/40 text-rose-700 dark:text-rose-300";
  }

  onMount(() => {
    void Promise.all([loadConversations(), loadVersionInfo(), pollWorkers(true)]);
    ControlService.Connected()
      .then((value) => {
        chat.connected = value;
        if (!value && !disconnectedAt) disconnectedAt = Date.now();
      })
      .catch(() => {});

    const offStatus = Events.On("control:status", (event) => {
      chat.connected = !!event.data;
      if (!chat.connected && !disconnectedAt) disconnectedAt = Date.now();
      if (chat.connected) {
        disconnectedAt = 0;
        connInfoOpen = false;
        connCheckMsg = "";
        void Promise.all([loadConversations({ forceReloadCurrent: true }), loadVersionInfo(), pollWorkers(true)]);
        if (view === "settings" && settingsTab === "connection") void loadAuxInfo();
      } else {
        chat.working = new Map();
      }
    });

    const offFrame = Events.On("frame", (event) => {
      handleFrame(event.data || {});
    });

    // The tray menu's "회의 목록".
    const offMeetings = Events.On("tray:meetings", showMeetings);

    // The tray menu's "음성 설정".
    const offVoiceSettings = Events.On("tray:voiceSettings", () => {
      void openSettings("voice");
    });

    // Panes (PaneNode) ask for the settings screen / a new conversation here.
    const onOpenSettingsRequest = (event) => {
      const tab = event?.detail?.tab;
      void openSettings(["settings", "connection", "voice"].includes(tab) ? tab : "settings");
    };
    const onNewConversationRequest = () => void newConversationFromAnywhere();
    const onShowChatRequest = () => { if (view !== "chat") void backToChat(); };
    window.addEventListener("aglink:show-chat", onShowChatRequest);
    window.addEventListener("aglink:open-settings", onOpenSettingsRequest);
    window.addEventListener("aglink:new-conversation", onNewConversationRequest);

    // App-wide shortcuts. Ctrl+K / Ctrl+/ / font size live in QuickSwitcher,
    // Ctrl+B in BoardView, Ctrl+F / Ctrl+M in PaneNode — none of these overlap.
    const onShortcut = (event) => {
      if (event.defaultPrevented || event.metaKey || event.isComposing) return;
      if (promptState || confirmState || snippets.managerOpen || searchPanel.open) return; // a dialog is asking something
      if (event.altKey && !event.ctrlKey && !event.shiftKey && event.key === "ArrowDown") {
        if (event.target instanceof HTMLSelectElement) return; // Alt+↓ opens a dropdown
        if (view !== "chat") return;
        event.preventDefault();
        openNextUnread();
        return;
      }
      if (event.ctrlKey && event.shiftKey && !event.altKey && (event.code === "KeyF" || event.key.toLowerCase() === "f")) {
        event.preventDefault();
        openSearchPanel("search");
        return;
      }
      if (!event.ctrlKey || event.altKey || event.shiftKey) return;
      if (event.code === "KeyN" || event.key.toLowerCase() === "n") {
        event.preventDefault();
        void newConversationFromAnywhere();
      } else if (event.code === "Comma" || event.key === ",") {
        event.preventDefault();
        void openSettings("settings");
      } else if (event.code === "Backslash" || event.key === "\\" || event.key === "₩") {
        event.preventDefault();
        void toggleSidebar();
      }
    };
    document.addEventListener("keydown", onShortcut);

    const tickTimer = window.setInterval(() => {
      chat.nowTick = Date.now();
    }, 1000);

    const workerTimer = window.setInterval(() => {
      void pollWorkers();
    }, 3000);

    const clickAway = (event) => {
      const target = event.target;
      if (!(target instanceof HTMLElement)) return;
      const fileLink = target.closest(".md-file-link");
      if (fileLink) {
        event.preventDefault();
        void openPreview(fileLink.getAttribute("data-preview-path") || "");
        return;
      }
      if (!target.closest("[data-conv-menu]")) openMenuId = "";
      if (!target.closest("[data-progress-popup]")) closeProgressPopup();
      if (!target.closest("[data-pane-backend-menu]")) closePaneBackendMenu();
      if (!target.closest("[data-pane-workdir-menu]")) closePaneWorkDirMenu();
      if (!target.closest("[data-more-menu]")) closeMoreMenu();
      if (!target.closest("[data-conn-info]")) connInfoOpen = false;
    };

    const onEscape = (event) => {
      if (event.key !== "Escape") return;
      if (preview.open) { closePreview(); return; }
      if (chat.lightboxSrc) closeLightbox();
      if (promptState) resolvePrompt(null);
      if (confirmState) resolveConfirm(false);
      if (chat.progressPopupPaneId) closeProgressPopup();
      if (chat.backendMenuPaneId) closePaneBackendMenu();
      if (chat.workDirMenuPaneId) closePaneWorkDirMenu();
      if (moreMenuOpen) closeMoreMenu(true);
      connInfoOpen = false;
      openMenuId = "";
    };

    document.addEventListener("click", clickAway);
    document.addEventListener("keydown", onEscape);

    return () => {
      offStatus();
      offFrame();
      offVoiceSettings();
      offMeetings();
      window.removeEventListener("aglink:open-settings", onOpenSettingsRequest);
      window.removeEventListener("aglink:new-conversation", onNewConversationRequest);
      window.removeEventListener("aglink:show-chat", onShowChatRequest);
      document.removeEventListener("keydown", onShortcut);
      window.clearInterval(tickTimer);
      window.clearInterval(workerTimer);
      document.removeEventListener("click", clickAway);
      document.removeEventListener("keydown", onEscape);
    };
  });
</script>

<div class="flex h-full min-h-0 w-full overflow-hidden text-[13px] text-slate-900 dark:text-slate-100">
  <div class="flex h-full w-full flex-col overflow-hidden">
    <header class="flex h-12 shrink-0 items-center gap-2 border-b border-slate-800 bg-slate-900 px-3 text-white shadow-sm">
      <div class="relative shrink-0" data-conn-info>
        <button
          type="button"
          class={`inline-flex items-center gap-1 rounded-md px-2 py-1 text-[11px] font-semibold text-white transition-colors ${chat.connected ? "bg-emerald-500 hover:bg-emerald-600" : "bg-rose-500 hover:bg-rose-600"}`}
          onclick={() => { connInfoOpen = !connInfoOpen; connCheckMsg = ""; }}
          title={chat.connected ? "aglink 본체와 연결되어 있습니다 (클릭하면 자세히)" : "aglink 본체와 연결이 끊겼습니다 (클릭하면 원인과 대처)"}
          aria-haspopup="dialog"
          aria-expanded={connInfoOpen}
        >
          {chat.connected ? "연결됨" : "연결 끊김"}
          <span class="text-[9px] opacity-80" aria-hidden="true">▾</span>
        </button>
        {#if connInfoOpen}
          <div class="absolute left-0 top-9 z-50 w-72 rounded-lg border border-slate-200 bg-white p-3 text-slate-700 shadow-xl dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200" role="dialog" aria-label="연결 상태">
            <div class="flex items-center gap-2">
              <span class={`h-2 w-2 shrink-0 rounded-full ${chat.connected ? "bg-emerald-500" : "animate-pulse bg-rose-500"}`}></span>
              <span class="text-[13px] font-semibold">{chat.connected ? "aglink 본체와 연결됨" : `연결 끊김${disconnectedFor() ? ` · ${disconnectedFor()}` : ""}`}</span>
            </div>
            {#if chat.connected}
              <p class="mt-1.5 text-xs leading-5 text-slate-500 dark:text-slate-400">대화·예약·설정이 모두 본체와 실시간으로 주고받고 있어요.</p>
            {:else}
              <p class="mt-1.5 text-xs leading-5 text-slate-500 dark:text-slate-400">이 앱은 PC에서 도는 aglink 본체와 통신합니다. 본체가 꺼져 있거나, 업데이트로 다시 시작하는 중이거나, 접속 주소·토큰이 바뀌었을 수 있어요.</p>
              <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">앱이 몇 초 간격으로 <b class="font-semibold text-slate-700 dark:text-slate-200">자동으로 다시 연결</b>을 시도하고 있습니다.</p>
            {/if}
            {#if connCheckMsg}
              <p class="mt-1.5 text-xs text-amber-700 dark:text-amber-300" role="status">{connCheckMsg}</p>
            {/if}
            <div class="mt-2.5 flex justify-end gap-1.5">
              <button type="button" class="ui-btn ui-btn-ghost" onclick={() => { connInfoOpen = false; void openSettings("connection"); }}>연결 정보</button>
              {#if !chat.connected}
                <button type="button" class="ui-btn ui-btn-primary" disabled={connChecking} onclick={checkConnectionNow}>{connChecking ? "확인 중…" : "지금 다시 확인"}</button>
              {/if}
            </div>
          </div>
        {/if}
      </div>
      <div class="min-w-0 flex-1">
        <div class="truncate text-sm font-semibold">aglink</div>
      </div>
      {#if backendBadgeText()}
        <span class="inline-flex shrink-0 items-center gap-1.5 rounded-full bg-white/10 px-2 py-1 text-[11px] font-semibold capitalize text-slate-100" title="현재 연결된 백엔드">
          <span class={`h-1.5 w-1.5 rounded-full ${backendBadgeText() === "codex" ? "bg-emerald-400" : "bg-orange-300"}`}></span>
          {backendBadgeText()}
        </span>
      {/if}
      <span class={`inline-flex shrink-0 rounded-full px-2 py-1 text-[11px] font-semibold ${versionInfo.updateAvailable || versionInfo.opencodeUpdateAvailable ? "bg-amber-300 text-amber-950" : "bg-white/10 text-slate-100"}`} title="실행 중인 버전">
        {versionBadgeText()}
      </span>
      <div class="ml-auto flex items-center gap-2">
        <button
          type="button"
          class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-white/20 bg-white/5 px-2 text-[11px] font-semibold text-slate-100 hover:bg-white/10"
          onclick={() => openSettings("voice")}
          title={`${voiceChip.tip} — 클릭하면 음성 설정`}
          aria-label={`음성: ${voiceChip.label} (음성 설정 열기)`}
        >
          <span class={`h-1.5 w-1.5 shrink-0 rounded-full ${voiceChip.dot} ${voiceChip.pulse ? "animate-pulse" : ""}`}></span>
          <span class="whitespace-nowrap">{voiceChip.label}</span>
        </button>
        {#if voice.meeting}
          <!-- Live state stays visible; ending it lives in the 더보기 menu. -->
          <span class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-rose-400 bg-rose-600 px-2 text-white" title="회의 모드 진행 중 (더보기 메뉴에서 끝내기)">
            <span class="h-2 w-2 animate-pulse rounded-full bg-white"></span>
            <span class="font-mono text-xs">{voice.meetingClock || "00:00"}</span>
          </span>
        {/if}
        <div class="relative" data-more-menu>
          <button
            bind:this={moreMenuTrigger}
            class={`grid h-8 w-8 place-items-center rounded-md border text-base leading-none text-slate-100 hover:bg-white/10 ${moreMenuOpen ? "border-white/40 bg-white/15" : "border-white/20 bg-white/5"}`}
            onclick={toggleMoreMenu}
            onkeydown={(event) => {
              if (event.key === "ArrowDown" && !moreMenuOpen) {
                event.preventDefault();
                void openMoreMenu();
              }
            }}
            title="더보기"
            aria-label="더보기"
            aria-haspopup="menu"
            aria-expanded={moreMenuOpen}
            aria-controls="header-more-menu"
          >
            ⋯
          </button>
          {#if moreMenuOpen}
            <div
              bind:this={moreMenuEl}
              id="header-more-menu"
              role="menu"
              tabindex="-1"
              aria-label="더보기"
              class="absolute right-0 top-10 z-50 w-52 rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 p-1.5 text-slate-700 dark:text-slate-200 shadow-xl"
              onkeydown={handleMoreMenuKeydown}
            >
              <button
                role="menuitem"
                class={`flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-xs font-medium outline-none focus-visible:bg-slate-100 dark:focus-visible:bg-slate-800 ${voice.meeting ? "text-rose-600 dark:text-rose-300 hover:bg-rose-50 dark:hover:bg-rose-950/40" : "hover:bg-slate-100 dark:hover:bg-slate-800"}`}
                onclick={() => { closeMoreMenu(true); voice.meeting ? stopMeeting() : startMeeting(); }}
                title={voice.meeting ? "회의 모드 끝내기" : "회의 모드 시작 — 주변 대화를 받아 적고, “조언해 줘”라고 하면 조언해요"}
                aria-label={voice.meeting ? "회의 모드 끝내기" : "회의 모드 시작"}
              >
                <span class="w-4 shrink-0 text-center">{voice.meeting ? "■" : "🎙"}</span>
                <span class="min-w-0 flex-1 truncate">{voice.meeting ? "회의 모드 끝내기" : "회의 모드 시작"}</span>
                {#if voice.meeting}
                  <span class="font-mono text-[11px]">{voice.meetingClock || "00:00"}</span>
                {/if}
              </button>
              <button
                role="menuitem"
                class="flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-xs font-medium outline-none hover:bg-slate-100 focus-visible:bg-slate-100 dark:hover:bg-slate-800 dark:focus-visible:bg-slate-800"
                onclick={() => { closeMoreMenu(true); showMeetings(); }}
                title="회의 목록"
                aria-label="회의 목록"
              >
                <span class="w-4 shrink-0 text-center">🗂</span>
                <span class="min-w-0 flex-1 truncate">회의 목록</span>
              </button>
              <button
                role="menuitem"
                class="flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-xs font-medium outline-none hover:bg-slate-100 focus-visible:bg-slate-100 dark:hover:bg-slate-800 dark:focus-visible:bg-slate-800"
                onclick={() => { closeMoreMenu(); boardOpen = true; }}
                title="작업 보드 — 모든 대화를 상태별로 보기"
                aria-label="작업 보드"
              >
                <svg class="h-4 w-4 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="4" width="5" height="16" rx="1.5" /><rect x="10" y="4" width="5" height="11" rx="1.5" /><rect x="17" y="4" width="4" height="7" rx="1.5" /></svg>
                <span class="min-w-0 flex-1 truncate">작업 보드</span>
                <span class="text-[11px] text-slate-400 dark:text-slate-500">Ctrl+B</span>
              </button>
              <div class="my-1 border-t border-slate-200 dark:border-slate-700" role="separator"></div>
              <button
                role="menuitem"
                class="flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-xs font-medium outline-none hover:bg-slate-100 focus-visible:bg-slate-100 dark:hover:bg-slate-800 dark:focus-visible:bg-slate-800"
                onclick={cycleTheme}
                title={`테마: ${themeLabel()} (클릭하여 전환)`}
                aria-label="테마 전환"
              >
                <span class="w-4 shrink-0 text-center">{themeIcon()}</span>
                <span class="min-w-0 flex-1 truncate">테마 전환</span>
                <span class="text-[11px] text-slate-400 dark:text-slate-500">{themeLabel()}</span>
              </button>
            </div>
          {/if}
        </div>
        <button
          class="grid h-8 w-8 place-items-center rounded-md border border-white/20 bg-white/5 text-sm text-slate-100 hover:bg-white/10 disabled:cursor-not-allowed disabled:opacity-40"
          onclick={addPane}
          disabled={chat.panes.length >= MAX_PANES}
          title="화면 분할 추가 (드래그로 재배치 가능)"
          aria-label="화면 분할 추가"
        >
          ⊞
        </button>
        <button
          class="grid h-8 w-8 place-items-center rounded-md border border-white/20 bg-white/5 text-sm text-slate-100 hover:bg-white/10"
          onclick={() => openSettings("settings")}
          title="설정 · 연결 / aglink · 음성 (Ctrl+,)"
          aria-label="설정"
        >
          ⚙
        </button>
        <button
          class="grid h-8 w-8 place-items-center rounded-md border border-white/20 bg-white/5 text-sm font-semibold text-slate-100 hover:bg-white/10"
          onclick={() => (helpOpen = !helpOpen)}
          title="단축키 모음 (Ctrl+/)"
          aria-label="단축키 도움말"
        >
          ?
        </button>
      </div>
    </header>

    {#if view === "chat"}
      <div class="flex min-h-0 flex-1 overflow-hidden">
        {#if !sidebarCollapsed}
          <aside class="flex h-full w-[296px] shrink-0 flex-col border-r border-slate-200 dark:border-slate-700 bg-slate-50/90 dark:bg-slate-800/90 backdrop-blur">
            <div class="flex h-11 shrink-0 items-center border-b border-slate-200 dark:border-slate-700 bg-slate-100/60 dark:bg-slate-800/60">
              <button
                class="grid h-full w-8 shrink-0 place-items-center text-sm text-slate-500 dark:text-slate-400 hover:bg-slate-200 dark:hover:bg-slate-600"
                onclick={() => (sidebarCollapsed = true)}
                title="사이드바 숨기기"
                aria-label="사이드바 숨기기"
              >«</button>
              {#each SIDEBAR_TABS as t (t.id)}
                <button
                  class={`relative flex h-full min-w-0 flex-1 flex-col items-center justify-center gap-0.5 border-b-2 transition ${sidebarTab === t.id ? "border-blue-600 bg-white/80 text-blue-700 dark:border-blue-400 dark:bg-slate-700/70 dark:text-blue-300" : "border-transparent text-slate-500 hover:bg-slate-200/70 dark:text-slate-400 dark:hover:bg-slate-700/50"}`}
                  onclick={() => (sidebarTab = t.id)}
                  title={t.id === "chat" && unreadCount ? `${t.label} · 읽지 않은 대화 ${unreadCount}개` : t.label}
                  aria-label={t.label}
                >
                  <span class="text-[17px] leading-none">{t.icon}</span>
                  <span class="max-w-full truncate px-0.5 text-[10px] font-semibold leading-none">{t.short}</span>
                  {#if t.id === "chat" && unreadCount > 0}
                    <span class="absolute right-1 top-1 min-w-[15px] rounded-full bg-blue-600 px-1 text-center text-[9px] font-bold leading-[15px] text-white" aria-label={`읽지 않은 대화 ${unreadCount}개`}>{unreadCount > 99 ? "99+" : unreadCount}</span>
                  {/if}
                </button>
              {/each}
            </div>
            {#if sidebarTab === "chat"}
            <div class="flex h-11 shrink-0 items-center gap-2 border-b border-slate-200 dark:border-slate-700 px-3">
              <button
                class="grid h-8 w-8 shrink-0 place-items-center rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700"
                onclick={() => (sidebarCollapsed = true)}
                title="대화 목록 숨기기"
                aria-label="대화 목록 숨기기"
              >
                «
              </button>
              <div class="min-w-0 flex-1">
                <div class="truncate text-sm font-semibold text-slate-900 dark:text-slate-100">대화 목록</div>
              </div>
              <button
                class="grid h-8 w-8 place-items-center rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700"
                onclick={() => loadConversations({ forceReloadCurrent: true })}
                title="대화 목록 새로고침"
                aria-label="대화 목록 새로고침"
              >
                ↻
              </button>
              <button
                class="grid h-8 w-8 place-items-center rounded-md bg-blue-600 text-base font-semibold text-white hover:bg-blue-700"
                onclick={newWebConversation}
                title="새 대화"
                aria-label="새 대화"
              >
                ＋
              </button>
            </div>

            <div class="shrink-0 border-b border-slate-200 dark:border-slate-700 px-3 py-2">
              <div class="flex items-center gap-1.5 rounded-md border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 px-2 focus-within:border-blue-400 focus-within:ring-2 focus-within:ring-blue-100 dark:focus-within:ring-blue-900/30">
                <svg class="h-3.5 w-3.5 shrink-0 text-slate-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
                <input
                  class="min-w-0 flex-1 bg-transparent py-1.5 text-[13px] text-slate-800 dark:text-slate-200 outline-none placeholder:text-slate-400 dark:placeholder:text-slate-500"
                  placeholder="대화 찾기 (Ctrl+K)"
                  value={convFilter}
                  oninput={(event) => (convFilter = event.currentTarget.value)}
                  onkeydown={(event) => {
                    if (event.key === "Escape") convFilter = "";
                    else if (event.key === "Enter" && !event.isComposing) {
                      const first = filteredWebConvs()[0];
                      if (first) selectTargetFromSidebar({ kind: "web", id: first.id });
                    }
                  }}
                />
                {#if convFilter}
                  <button type="button" class="shrink-0 text-xs text-slate-400 hover:text-slate-700 dark:hover:text-slate-200" onclick={() => (convFilter = "")} aria-label="찾기 지우기">✕</button>
                {/if}
              </div>
              {#if usedLabels.length > 0}
                <div class="mt-1.5 flex items-center gap-1" role="group" aria-label="색 라벨로 거르기">
                  {#each usedLabels as label (label.id)}
                    <button
                      type="button"
                      class={`grid h-5 w-5 place-items-center rounded-full transition ${labelFilter === label.id ? "ring-2 ring-blue-500 ring-offset-1 ring-offset-slate-50 dark:ring-blue-400 dark:ring-offset-slate-800" : "opacity-70 hover:opacity-100"}`}
                      onclick={() => (labelFilter = labelFilter === label.id ? "" : label.id)}
                      title={labelFilter === label.id ? `${label.name} 라벨 거르기 해제` : `${label.name} 라벨만 보기`}
                      aria-label={`${label.name} 라벨만 보기`}
                      aria-pressed={labelFilter === label.id}
                    ><span class={`h-3 w-3 rounded-full ${label.dot}`}></span></button>
                  {/each}
                  {#if labelFilter}
                    <button type="button" class="ml-1 text-[11px] text-slate-400 hover:text-slate-700 dark:hover:text-slate-200" onclick={() => (labelFilter = "")}>전체 보기</button>
                  {/if}
                </div>
              {/if}
            </div>

            <div class="min-h-0 flex-1 overflow-y-auto px-3 py-3">
              {#if !listFiltering}
              <div class="mb-4">
                <div class="mb-2 px-1 text-[11px] font-bold tracking-[0.04em] text-slate-500 dark:text-slate-400">텔레그램 채널</div>
                <div class="border-l-2 border-slate-200 dark:border-slate-700 pl-2">
                  {#if chat.telegram}
                    <div
                      class={`relative flex h-9 w-full items-center gap-1 rounded-md px-1.5 transition ${isFocusedTarget({ kind: "telegram" }) ? "bg-blue-50 dark:bg-blue-950/40 text-blue-950 dark:text-blue-200 ring-1 ring-blue-200 dark:ring-blue-900/40" : "hover:bg-slate-100 dark:hover:bg-slate-700"}`}
                      data-conv-menu
                    >
                      <button
                        class="min-w-0 flex flex-1 items-center gap-2 rounded px-1.5 py-1 text-left transition hover:bg-slate-100/70 dark:hover:bg-slate-700/50"
                        draggable="true"
                        ondragstart={(event) => handleConversationDragStart(event, { kind: "telegram" })}
                        ondragend={handleConversationDragEnd}
                        onclick={() => selectTargetFromSidebar({ kind: "telegram" })}
                        title={chat.telegram.title || "텔레그램"}
                      >
                        <span class={`inline-flex h-2.5 w-2.5 shrink-0 rounded-full ${chat.working.has("telegram") ? "animate-pulse bg-blue-500" : "bg-sky-500"}`} title={chat.working.has("telegram") ? "작업 중" : ""}></span>
                        <span class="min-w-0 flex-1 truncate text-sm font-semibold">
                          {chat.telegram.title || "텔레그램"}
                        </span>
                        <span class="shrink-0 rounded-full bg-slate-200 dark:bg-slate-700 px-1.5 py-0.5 text-[10px] font-bold text-slate-700 dark:text-slate-300">{backendLabel(chat.telegram.backend)}</span>
                        {#if chat.unread.has("telegram")}
                          <span class="shrink-0 rounded-full bg-blue-600 px-1.5 py-0.5 text-[10px] font-bold text-white">NEW</span>
                        {/if}
                      </button>
                      <button
                        class="grid h-7 w-7 shrink-0 place-items-center rounded-md border border-slate-200 dark:border-slate-700 bg-white/80 dark:bg-slate-900/80 text-base leading-none text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-700"
                        onclick={() => (openMenuId = openMenuId === "telegram" ? "" : "telegram")}
                        title="채널 설정"
                        aria-label="채널 설정"
                      >
                        ⋯
                      </button>
                      {#if openMenuId === "telegram"}
                        <div class="absolute right-0 top-8 z-20 w-44 rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 p-1.5 shadow-xl">
                          <div class="px-3 py-1 text-[11px] font-semibold tracking-wide text-slate-400 dark:text-slate-500">사용할 AI</div>
                          {#each BACKEND_CHOICES as choice (choice.id)}
                            <button
                              role="menuitemradio"
                              aria-checked={backendChoice(chat.telegram.backend) === choice.id}
                              class={`flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left text-sm hover:bg-slate-100 dark:hover:bg-slate-700 ${backendChoice(chat.telegram.backend) === choice.id ? "font-semibold text-blue-700 dark:text-blue-300" : "text-slate-700 dark:text-slate-300"}`}
                              onclick={() => setChannelBackend({ kind: "telegram" }, choice.id)}
                            >
                              <span class="w-3 shrink-0 text-center" aria-hidden="true">{backendChoice(chat.telegram.backend) === choice.id ? "✓" : ""}</span>
                              <span class="min-w-0 flex-1 truncate">{choice.label}</span>
                            </button>
                          {/each}
                        </div>
                      {/if}
                    </div>
                  {:else}
                    <div class="rounded-md px-2 py-3 text-sm text-slate-500 dark:text-slate-400">{chat.convsLoaded ? "텔레그램 대화를 찾지 못했습니다." : chat.connected ? "불러오는 중…" : "연결되면 불러옵니다."}</div>
                  {/if}
                </div>
              </div>
              {/if}

              <div>
                {#snippet webConvRow(conv)}
                  <div
                    class={`relative flex h-10 items-center gap-1 rounded-md border px-1.5 shadow-sm ${isFocusedTarget({ kind: "web", id: conv.id }) ? "border-blue-200 dark:border-blue-800 bg-blue-50 dark:bg-blue-950/40 text-blue-950 dark:text-blue-200" : "border-slate-200 dark:border-slate-700 bg-white/85 dark:bg-slate-900/85 text-slate-900 dark:text-slate-100"}`}
                    data-conv-menu
                    title={conv.workDir ? `${conversationLabel(conv)} · ${conv.workDir}` : conversationLabel(conv)}
                  >
                      <button
                        class="min-w-0 flex flex-1 items-center gap-2 rounded px-1.5 py-1 text-left transition hover:bg-slate-100/70 dark:hover:bg-slate-700/50"
                        draggable="true"
                        ondragstart={(event) => handleConversationDragStart(event, { kind: "web", id: conv.id })}
                        ondragend={handleConversationDragEnd}
                        onclick={() => selectTargetFromSidebar({ kind: "web", id: conv.id })}
                      >
                        {#if chat.working.has(`web:${conv.id}`)}
                          <span class="h-2 w-2 shrink-0 animate-pulse rounded-full bg-blue-500" title="작업 중"></span>
                        {/if}
                        {#if convLabel(conv.id)}
                          <span class={`h-2 w-2 shrink-0 rounded-full ${convLabel(conv.id).dot}`} title={`${convLabel(conv.id).name} 라벨`} aria-label={`${convLabel(conv.id).name} 라벨`}></span>
                        {/if}
                        {#if isConversationPinned(conv.id)}
                          <span class="shrink-0 text-[11px] leading-none text-amber-500" title="고정됨" aria-label="고정됨">★</span>
                        {/if}
                        <span class="min-w-0 flex-1 truncate text-sm font-semibold">{conversationLabel(conv)}</span>
                        <span class="shrink-0 font-mono text-[11px] text-slate-400 dark:text-slate-500">#{conv.id}</span>
                        <span class="shrink-0 rounded-full bg-slate-200 dark:bg-slate-700 px-1.5 py-0.5 text-[10px] font-bold text-slate-700 dark:text-slate-300">{backendLabel(conv.backend)}</span>
                        {#if isVoiceConv(conv.id)}
                          <span class="shrink-0 rounded-full bg-emerald-600 px-1.5 py-0.5 text-[10px] font-bold text-white" title="음성 대화 중인 대화">🎤 음성</span>
                        {/if}
                        {#if isFocusedTarget({ kind: "web", id: conv.id })}
                          <span class="shrink-0 rounded-full bg-blue-600 px-1.5 py-0.5 text-[10px] font-bold text-white">현재</span>
                        {/if}
                        {#if chat.unread.has(`web:${conv.id}`)}
                          <span class="shrink-0 rounded-full bg-blue-600 px-1.5 py-0.5 text-[10px] font-bold text-white">NEW</span>
                        {/if}
                      </button>

                      <button
                        class="grid h-7 w-7 shrink-0 place-items-center rounded-md border border-slate-200 dark:border-slate-700 bg-white/80 dark:bg-slate-900/80 text-base leading-none text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-700"
                        onclick={() => (openMenuId = openMenuId === conv.id ? "" : conv.id)}
                        title="대화 관리"
                        aria-label="대화 관리"
                      >
                        ⋯
                      </button>

                      {#if openMenuId === conv.id}
                        <div class="absolute right-0 top-9 z-20 w-48 rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 p-1.5 shadow-xl">
                          <div class="px-3 py-1 text-[11px] font-semibold tracking-wide text-slate-400 dark:text-slate-500">사용할 AI</div>
                          {#each BACKEND_CHOICES as choice (choice.id)}
                            <button
                              role="menuitemradio"
                              aria-checked={backendChoice(conv.backend) === choice.id}
                              class={`flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left text-sm hover:bg-slate-100 dark:hover:bg-slate-700 ${backendChoice(conv.backend) === choice.id ? "font-semibold text-blue-700 dark:text-blue-300" : "text-slate-700 dark:text-slate-300"}`}
                              onclick={() => setChannelBackend({ kind: "web", id: conv.id }, choice.id)}
                            >
                              <span class="w-3 shrink-0 text-center" aria-hidden="true">{backendChoice(conv.backend) === choice.id ? "✓" : ""}</span>
                              <span class="min-w-0 flex-1 truncate">{choice.label}</span>
                            </button>
                          {/each}
                          <div class="my-1 h-px bg-slate-100 dark:bg-slate-800"></div>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => { openMenuId = ""; setConversationPinned(conv.id, !isConversationPinned(conv.id)); }}>
                            {isConversationPinned(conv.id) ? "고정 해제" : "맨 위에 고정"}
                          </button>
                          <div class="flex items-center gap-1 px-3 py-1.5" role="group" aria-label="색 라벨">
                            <span class="mr-1 text-[11px] font-semibold text-slate-400 dark:text-slate-500">라벨</span>
                            {#each CONV_LABELS as label (label.id)}
                              <button
                                type="button"
                                class={`grid h-5 w-5 place-items-center rounded-full ${convLabel(conv.id)?.id === label.id ? "ring-2 ring-slate-700 ring-offset-1 ring-offset-white dark:ring-slate-200 dark:ring-offset-slate-900" : "hover:scale-110"}`}
                                onclick={() => { setConversationLabel(conv.id, convLabel(conv.id)?.id === label.id ? "" : label.id); }}
                                title={convLabel(conv.id)?.id === label.id ? `${label.name} 라벨 떼기` : `${label.name} 라벨`}
                                aria-label={`${label.name} 라벨`}
                                aria-pressed={convLabel(conv.id)?.id === label.id}
                              ><span class={`h-3 w-3 rounded-full ${label.dot}`}></span></button>
                            {/each}
                          </div>
                          <div class="my-1 h-px bg-slate-100 dark:bg-slate-800"></div>
                          <div class="px-3 py-1 text-[11px] font-semibold uppercase tracking-wide text-slate-400 dark:text-slate-500">그룹</div>
                          {#if chat.webGroupOf.get(conv.id)}
                            <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => { openMenuId = ""; setConversationGroup(conv.id, null); }}>
                              그룹에서 빼기
                            </button>
                          {/if}
                          {#each chat.webGroups as group (group.id)}
                            {#if chat.webGroupOf.get(conv.id) !== group.id}
                              <button class="block w-full truncate rounded-md px-3 py-2 text-left text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => { openMenuId = ""; setConversationGroup(conv.id, group.id); }}>
                                {groupPathLabel(group.id)}로 이동
                              </button>
                            {/if}
                          {/each}
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => { openMenuId = ""; moveConversationToNewGroup(conv); }}>
                            새 그룹으로 이동...
                          </button>
                          <div class="my-1 h-px bg-slate-100 dark:bg-slate-800"></div>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => { openMenuId = ""; renameWebConversation(conv); }}>
                            이름 변경
                          </button>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => { openMenuId = ""; changeWebDirectory(conv); }}>
                            작업 폴더 변경
                          </button>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => { openMenuId = ""; setConversationArchived(conv.id, !chat.archivedConvs.has(String(conv.id))); }}>
                            {chat.archivedConvs.has(String(conv.id)) ? "보관 해제" : "보관 (목록에서 숨김)"}
                          </button>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-rose-700 dark:text-rose-300 hover:bg-rose-50 dark:hover:bg-rose-950/40" onclick={() => { openMenuId = ""; deleteWebConversation(conv); }}>
                            삭제
                          </button>
                        </div>
                      {/if}
                  </div>
                {/snippet}

                <!-- svelte-ignore a11y_no_static_element_interactions -->
                <div
                  class={`mb-2 flex items-center gap-1 rounded-md px-1 ${chat.dragOverGroupId === UNGROUPED_DROP_ZONE ? "ring-2 ring-blue-400 bg-blue-50 dark:bg-blue-950/40" : ""}`}
                  ondragenter={(event) => handleGroupDragOver(event, UNGROUPED_DROP_ZONE)}
                  ondragover={(event) => handleGroupDragOver(event, UNGROUPED_DROP_ZONE)}
                  ondragleave={() => handleGroupDragLeave(UNGROUPED_DROP_ZONE)}
                  ondrop={(event) => handleGroupDrop(event, UNGROUPED_DROP_ZONE)}
                  title="대화를 여기로 드래그하면 그룹에서 빠집니다"
                >
                  <div class="flex-1 text-[11px] font-bold tracking-[0.04em] text-slate-500 dark:text-slate-400">{#if listFiltering}찾은 대화 ({filteredWebConvs().length}){:else}<span>로컬 채널</span>{/if}</div>
                  <button
                    class="grid h-5 w-5 shrink-0 place-items-center rounded text-[13px] font-bold leading-none text-slate-400 dark:text-slate-500 hover:bg-slate-200 dark:hover:bg-slate-600 hover:text-slate-700"
                    onclick={newWebGroup}
                    title="새 그룹"
                    aria-label="새 그룹"
                  >
                    ⊕
                  </button>
                </div>
                <div class="space-y-1 border-l-2 border-slate-200 dark:border-slate-700 pl-2">
                  {#if listFiltering}
                    {#each filteredWebConvs() as conv (conv.id)}
                      {@render webConvRow(conv)}
                    {:else}
                      <div class="px-2 py-3 text-sm text-slate-400 dark:text-slate-500">맞는 대화가 없습니다.</div>
                    {/each}
                  {:else}
                  {#if !chat.convsLoaded}
                    <div class="flex items-center gap-2 rounded-md px-3 py-4 text-sm text-slate-500 dark:text-slate-400" role="status">
                      {#if chat.connected}
                        <span class="h-3.5 w-3.5 shrink-0 animate-spin rounded-full border-2 border-slate-300 border-t-blue-500 dark:border-slate-600 dark:border-t-blue-400"></span>
                        불러오는 중…
                      {:else}
                        aglink 본체에 연결되면 대화 목록을 불러옵니다.
                      {/if}
                    </div>
                  {:else if chat.webConvs.length === 0}
                    <div class="rounded-md border border-dashed border-slate-300 dark:border-slate-600 bg-white/70 dark:bg-slate-900/70 px-3 py-4 text-sm text-slate-500 dark:text-slate-400">
                      아직 로컬 대화가 없습니다. "새 대화"(Ctrl+N)로 시작하세요.
                    </div>
                  {/if}

                  {#if pinnedWebConvs().length > 0}
                    <div class="mb-1 space-y-1">
                      <div class="flex items-center gap-1 px-1 pt-0.5 text-[10.5px] font-bold tracking-[0.04em] text-amber-600 dark:text-amber-400">
                        <span aria-hidden="true">★</span><span>고정됨</span>
                      </div>
                      {#each pinnedWebConvs() as conv (conv.id)}
                        {@render webConvRow(conv)}
                      {/each}
                    </div>
                  {/if}

                  {#each rootWebGroups() as group (group.id)}
                    <GroupNode {group} bind:openMenuId {webConvRow} onRename={renameWebGroupPrompt} onDelete={deleteWebGroupConfirm} />
                  {/each}

                  <!-- svelte-ignore a11y_no_static_element_interactions -->
                  <div
                    class={`space-y-1 rounded-md ${chat.dragOverGroupId === UNGROUPED_DROP_ZONE ? "ring-2 ring-blue-400 bg-blue-50 dark:bg-blue-950/40" : ""}`}
                    ondragenter={(event) => handleGroupDragOver(event, UNGROUPED_DROP_ZONE)}
                    ondragover={(event) => handleGroupDragOver(event, UNGROUPED_DROP_ZONE)}
                    ondragleave={() => handleGroupDragLeave(UNGROUPED_DROP_ZONE)}
                    ondrop={(event) => handleGroupDrop(event, UNGROUPED_DROP_ZONE)}
                  >
                    {#each ungroupedWebConvs() as conv (conv.id)}
                      {@render webConvRow(conv)}
                    {/each}
                  </div>

                  {#if archivedWebConvs().length > 0}
                    <button
                      class="mt-2 flex w-full items-center gap-1 rounded-md px-1 py-1 text-left text-[11px] font-bold tracking-[0.04em] text-slate-500 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-700"
                      onclick={() => (showArchived = !showArchived)}
                    >
                      <span>{showArchived ? "▾" : "▸"}</span>
                      <span>보관함 ({archivedWebConvs().length})</span>
                    </button>
                    {#if showArchived}
                      <div class="space-y-1 opacity-75">
                        {#each archivedWebConvs() as conv (conv.id)}
                          {@render webConvRow(conv)}
                        {/each}
                      </div>
                    {/if}
                  {/if}
                  {/if}
                </div>
              </div>
            </div>
            {:else if sidebarTab === "playbook"}
              <PlaybookPanel />
            {:else if sidebarTab === "reservation"}
              <ReservationPanel />
            {:else if sidebarTab === "meeting"}
              <MeetingPanel onopen={openPreview} />
            {:else if sidebarTab === "usage"}
              <UsagePanel />
            {:else}
              <MCPServerPanel />
            {/if}
          </aside>
        {:else}
          <div class="flex h-full w-7 shrink-0 flex-col items-center border-r border-slate-200 dark:border-slate-700 bg-slate-50/90 dark:bg-slate-800/90 pt-2 backdrop-blur">
            <button
              class="grid h-8 w-6 place-items-center rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700"
              onclick={() => (sidebarCollapsed = false)}
              title="대화 목록 보이기 (Ctrl+\)"
              aria-label="대화 목록 보이기"
            >
              »
            </button>
            {#if unreadCount > 0}
              <button
                class="mt-2 min-w-[18px] rounded-full bg-blue-600 px-1 text-center text-[10px] font-bold leading-[18px] text-white hover:bg-blue-700"
                onclick={openNextUnread}
                title={`읽지 않은 대화 ${unreadCount}개 — 클릭하면 열기 (Alt+↓)`}
                aria-label={`읽지 않은 대화 ${unreadCount}개 열기`}
              >{unreadCount > 99 ? "99+" : unreadCount}</button>
            {/if}
          </div>
        {/if}

        <div class="flex min-w-0 flex-1 flex-col overflow-hidden">
          {#if chat.statusNote}
            <div class="flex shrink-0 items-center gap-2 border-b border-amber-200 dark:border-amber-800 bg-amber-50 dark:bg-amber-950/40 px-4 py-2 text-xs text-amber-800 dark:text-amber-300">
              <span class="min-w-0 flex-1 break-words">{chat.statusNote}</span>
              {#if statusRetry(chat.statusNote)}
                {@const retry = statusRetry(chat.statusNote)}
                <button
                  type="button"
                  class="shrink-0 rounded-md border border-amber-300 bg-white/70 px-2 py-0.5 text-[11px] font-semibold text-amber-800 hover:bg-amber-100 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-200 dark:hover:bg-amber-900/40"
                  onclick={() => { chat.statusNote = ""; void retry.run(); }}
                >{retry.label}</button>
              {/if}
              <button type="button" class="shrink-0 rounded px-1 text-amber-600 hover:bg-amber-100 dark:text-amber-400 dark:hover:bg-amber-900/40" onclick={() => (chat.statusNote = "")} aria-label="안내 닫기" title="닫기">✕</button>
            </div>
          {/if}

          <div class="flex min-h-0 flex-1 overflow-hidden">
            <PaneNode node={chat.layout} />
          </div>
        </div>
      </div>
    {:else}
      <div class="flex min-h-0 flex-1 flex-col overflow-hidden">
        <div class="flex h-11 shrink-0 items-center gap-3 border-b border-slate-200/80 dark:border-slate-700/80 bg-white/75 dark:bg-slate-900/75 px-4 backdrop-blur">
          <button
            class="grid h-8 w-8 place-items-center rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-sm font-medium text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700"
            onclick={backToChat}
            title="채팅으로"
            aria-label="채팅으로"
          >
            ←
          </button>
          <div class="text-sm font-semibold text-slate-900 dark:text-slate-100">설정</div>
          <div class="ml-auto flex gap-2">
            <button
              class={`rounded-full px-3 py-2 text-xs font-semibold ${settingsTab === "settings" ? "bg-blue-600 text-white" : "border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-slate-700 dark:text-slate-300"}`}
              onclick={() => loadSettingsTab("settings")}
            >
              설정{#if settingsDirty}<span class="ml-1 inline-block h-1.5 w-1.5 rounded-full bg-amber-400 align-middle" title="저장하지 않은 변경"></span>{/if}
            </button>
            <button
              class={`rounded-full px-3 py-2 text-xs font-semibold ${settingsTab === "connection" ? "bg-blue-600 text-white" : "border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-slate-700 dark:text-slate-300"}`}
              onclick={() => loadSettingsTab("connection")}
            >
              연결 / aglink
            </button>
            <button
              class={`rounded-full px-3 py-2 text-xs font-semibold ${settingsTab === "voice" ? "bg-blue-600 text-white" : "border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-slate-700 dark:text-slate-300"}`}
              onclick={() => loadSettingsTab("voice")}
            >
              음성
            </button>
          </div>
        </div>

        <div class="min-h-0 flex-1 overflow-y-auto px-5 py-5">
          <div class="mx-auto flex w-full max-w-5xl flex-col gap-5">
            {#if settingsTab === "settings"}
              {#snippet settingSection(section)}
                <div>
                  <div class="mb-1 text-sm font-bold text-slate-800 dark:text-slate-200">{section.title}</div>
                  {#if section.desc}
                    <div class="mb-3 max-w-3xl whitespace-pre-wrap break-words text-xs leading-5 text-slate-500 dark:text-slate-400">{section.desc}</div>
                  {/if}
                  <div class="space-y-3">
                    {#each section.fields || [] as field}
                      <div class="rounded-lg border border-slate-200 dark:border-slate-700 bg-slate-50/70 dark:bg-slate-800/70 p-4">
                        <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
                          <div class="min-w-0 md:max-w-[60%]">
                            <div class="flex items-center gap-1.5 text-sm font-semibold text-slate-900 dark:text-slate-100">
                              {field.label}
                              {#if fieldChanged(field.key)}
                                <span class="h-1.5 w-1.5 shrink-0 rounded-full bg-amber-500" title="저장하지 않은 변경" aria-label="저장하지 않은 변경"></span>
                              {/if}
                            </div>
                            {#if field.desc}
                              <div class="mt-1 whitespace-pre-wrap break-words text-xs leading-5 text-slate-500 dark:text-slate-400">{field.desc}</div>
                            {/if}
                          </div>
                          <div class={field.type === "list" ? "min-w-0 md:w-[420px] md:shrink-0" : "md:min-w-[220px]"}>
                            {#if field.type === "list"}
                              {@const items = listItems(settingsValues[field.key])}
                              <div class="flex flex-col gap-2" data-setting-list={field.key}>
                                {#if items.length}
                                  <ul class="flex flex-wrap gap-1.5" aria-label={`${field.label} 목록`}>
                                    {#each items as item, index (`${index}-${item}`)}
                                      <li class="inline-flex min-w-0 max-w-full items-center gap-1 rounded-full border border-slate-200 dark:border-slate-600 bg-white dark:bg-slate-900 py-0.5 pl-2.5 pr-1 font-mono text-xs text-slate-700 dark:text-slate-300">
                                        <span class="min-w-0 truncate" title={item}>{item}</span>
                                        <button
                                          type="button"
                                          class="grid h-4 w-4 shrink-0 place-items-center rounded-full text-[11px] leading-none text-slate-400 dark:text-slate-500 hover:bg-rose-100 dark:hover:bg-rose-900/40 hover:text-rose-600 dark:hover:text-rose-300"
                                          onclick={() => removeListItem(field.key, index)}
                                          title={`${item} 삭제`}
                                          aria-label={`${item} 삭제`}
                                        >×</button>
                                      </li>
                                    {/each}
                                  </ul>
                                {:else}
                                  <div class="text-xs text-slate-400 dark:text-slate-500">비어 있음</div>
                                {/if}
                                <div class="flex gap-2">
                                  <input
                                    class="min-w-0 flex-1 rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-3 py-1.5 text-sm outline-none focus:border-blue-400 focus:ring-4 focus:ring-blue-100 dark:focus:ring-blue-900/30"
                                    type="text"
                                    placeholder="추가할 항목"
                                    aria-label={`${field.label} 항목 추가`}
                                    value={listDrafts[field.key] ?? ""}
                                    oninput={(event) => (listDrafts = { ...listDrafts, [field.key]: event.currentTarget.value })}
                                    onkeydown={(event) => {
                                      if (event.key !== "Enter" || event.isComposing) return;
                                      event.preventDefault();
                                      addListItems(field.key);
                                    }}
                                  />
                                  <button
                                    type="button"
                                    class="shrink-0 rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-3 py-1.5 text-xs font-semibold text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700 disabled:cursor-not-allowed disabled:opacity-40"
                                    disabled={!listItems(listDrafts[field.key]).length}
                                    onclick={() => addListItems(field.key)}
                                  >추가</button>
                                </div>
                              </div>
                            {:else if field.type === "bool"}
                              <label class="inline-flex items-center gap-2 text-sm font-medium text-slate-700 dark:text-slate-300">
                                <input
                                  class="h-4 w-4 rounded border-slate-300 dark:border-slate-600 text-blue-600 dark:text-blue-400 focus:ring-blue-500"
                                  type="checkbox"
                                  checked={!!settingsValues[field.key]}
                                  onchange={(event) => updateSettingValue(field.key, event.currentTarget.checked)}
                                />
                                사용
                              </label>
                            {:else if field.type === "select"}
                              <select
                                class="w-full rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-3 py-2 text-sm outline-none focus:border-blue-400 focus:ring-4 focus:ring-blue-100 dark:focus:ring-blue-900/30"
                                value={settingsValues[field.key] ?? ""}
                                onchange={(event) => updateSettingValue(field.key, event.currentTarget.value)}
                              >
                                {#each selectOptionsFor(field) as option}
                                  <option value={option}>{option === "" ? "기본값" : option}</option>
                                {/each}
                              </select>
                            {:else}
                              <input
                                class="w-full rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-3 py-2 text-sm outline-none focus:border-blue-400 focus:ring-4 focus:ring-blue-100 dark:focus:ring-blue-900/30"
                                type={field.type === "int" ? "number" : "text"}
                                value={settingsValues[field.key] ?? ""}
                                oninput={(event) =>
                                  updateSettingValue(
                                    field.key,
                                    field.type === "int"
                                      ? event.currentTarget.value === ""
                                        ? 0
                                        : Number(event.currentTarget.value)
                                      : event.currentTarget.value,
                                  )}
                              />
                            {/if}
                          </div>
                        </div>
                      </div>
                    {/each}
                  </div>
                </div>
              {/snippet}

              <ClaudeAuthPanel />

              <section class="rounded-lg border border-slate-200 dark:border-slate-700 bg-white/90 dark:bg-slate-900/90 p-5 shadow-sm">
                <div class="mb-5">
                  <div class="text-sm font-semibold text-slate-900 dark:text-slate-100">설정</div>
                  <div class="mt-1 text-xs text-slate-500 dark:text-slate-400">번호(①②③) 순서대로 채우면 AI가 연결됩니다. 잘 모르는 칸은 비워두거나 기본값 그대로 두세요.</div>
                </div>

                {#if settingsSchema.length === 0}
                  <div class="flex items-center gap-2 rounded-lg border border-dashed border-slate-300 dark:border-slate-600 px-4 py-5 text-sm text-slate-500 dark:text-slate-400" role="status">
                    {#if settingsLoading}
                      <span class="h-3.5 w-3.5 shrink-0 animate-spin rounded-full border-2 border-slate-300 border-t-blue-500 dark:border-slate-600 dark:border-t-blue-400"></span>
                      불러오는 중…
                    {:else}
                      {settingsMsg || (chat.connected ? "표시할 설정이 없습니다." : "aglink 본체에 연결되면 설정을 불러옵니다.")}
                    {/if}
                  </div>
                {:else}
                  <div class="mb-3 flex items-center gap-1.5 rounded-md border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 px-2.5 focus-within:border-blue-400 focus-within:ring-2 focus-within:ring-blue-100 dark:focus-within:ring-blue-900/30">
                    <svg class="h-3.5 w-3.5 shrink-0 text-slate-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
                    <input
                      class="min-w-0 flex-1 bg-transparent py-1.5 text-[13px] text-slate-800 dark:text-slate-200 outline-none placeholder:text-slate-400 dark:placeholder:text-slate-500"
                      placeholder="설정 찾기 (이름·설명으로, 모든 탭에서)"
                      aria-label="설정 찾기"
                      value={settingsQuery}
                      oninput={(event) => (settingsQuery = event.currentTarget.value)}
                      onkeydown={(event) => { if (event.key === "Escape" && settingsQuery) { event.stopPropagation(); settingsQuery = ""; } }}
                    />
                    {#if settingsQuery}
                      <span class="shrink-0 text-[11px] text-slate-400 dark:text-slate-500">{(settingsSearchResults || []).reduce((n, section) => n + section.fields.length, 0)}개</span>
                      <button type="button" class="shrink-0 text-xs text-slate-400 hover:text-slate-700 dark:hover:text-slate-200" onclick={() => (settingsQuery = "")} aria-label="찾기 지우기">✕</button>
                    {/if}
                  </div>
                  {#if settingsSearchResults}
                    <div class="space-y-6">
                      {#each settingsSearchResults as section}
                        <div>
                          <div class="mb-1 inline-flex rounded-full bg-slate-100 dark:bg-slate-800 px-2 py-0.5 text-[10.5px] font-semibold text-slate-500 dark:text-slate-400">{section.group || "기타"}</div>
                          {@render settingSection(section)}
                        </div>
                      {:else}
                        <div class="rounded-lg border border-dashed border-slate-300 dark:border-slate-600 px-4 py-5 text-sm text-slate-500 dark:text-slate-400">“{settingsQuery.trim()}”에 맞는 설정이 없습니다.</div>
                      {/each}
                    </div>
                  {:else}
                  <div class="mb-5 flex flex-wrap gap-1 border-b border-slate-200 dark:border-slate-700">
                    {#each settingsGroups as group}
                      <button
                        class="-mb-px border-b-2 px-3 py-2 text-sm font-semibold transition-colors {activeSettingsGroup ===
                        group
                          ? 'border-blue-600 dark:border-blue-400 text-blue-700 dark:text-blue-300'
                          : 'border-transparent text-slate-500 dark:text-slate-400 hover:text-slate-800'}"
                        onclick={() => (activeSettingsGroup = group)}
                      >
                        {group}
                      </button>
                    {/each}
                  </div>
                  <div class="space-y-6">
                    {#each sectionsInGroup(activeSettingsGroup) as section}
                      {@render settingSection(section)}
                    {/each}
                  </div>
                  {/if}
                {/if}

                <div class="mt-5 flex items-center gap-3">
                  <button class="rounded-md bg-blue-600 px-4 py-2 text-sm font-semibold text-white hover:bg-blue-700" onclick={saveSettings}>
                    설정 저장
                  </button>
                  {#if settingsChangedCount > 0}
                    <span class="inline-flex items-center gap-1.5 rounded-full bg-amber-100 dark:bg-amber-900/40 px-2 py-0.5 text-[11px] font-semibold text-amber-800 dark:text-amber-300">
                      <span class="h-1.5 w-1.5 rounded-full bg-amber-500"></span>저장 안 한 변경 {settingsChangedCount}개
                    </span>
                    <button type="button" class="text-[11px] text-slate-500 underline-offset-2 hover:underline dark:text-slate-400" onclick={() => { settingsValues = { ...settingsOriginal }; listDrafts = {}; }}>되돌리기</button>
                  {/if}
                  {#if settingsMsg}
                    <span class="text-xs {settingsMsgWarn ? 'font-medium text-amber-700 dark:text-amber-300' : 'text-slate-500 dark:text-slate-400'}" role={settingsMsgWarn ? "alert" : undefined}>{settingsMsg}</span>
                  {/if}
                </div>
              </section>

              <section class="rounded-lg border border-slate-200 dark:border-slate-700 bg-white/90 dark:bg-slate-900/90 p-5 shadow-sm">
                <div class="mb-5">
                  <div class="text-sm font-semibold text-slate-900 dark:text-slate-100">전문가용: 설정 파일 직접 편집</div>
                  <div class="mt-1 text-xs text-slate-500 dark:text-slate-400">위 항목으로 안 되는 세부 설정(예: SSH 호스트 목록)만 여기서 직접 고칩니다. 형식이 틀리면 저장되지 않으니 익숙하지 않으면 건드리지 마세요.</div>
                </div>
                <textarea
                  class="min-h-[320px] w-full rounded-lg border border-slate-300 dark:border-slate-600 bg-slate-950 px-4 py-3 font-mono text-[12px] leading-6 text-slate-100 outline-none focus:border-blue-400 focus:ring-4 focus:ring-blue-100 dark:focus:ring-blue-900/30"
                  spellcheck="false"
                  value={configText}
                  oninput={(event) => (configText = event.currentTarget.value)}
                ></textarea>
                <div class="mt-4 flex items-center gap-3">
                  <button class="rounded-md bg-slate-900 px-4 py-2 text-sm font-semibold text-white hover:bg-slate-800" onclick={saveRawConfig}>
                    파일 저장
                  </button>
                  {#if rawConfigChanged}
                    <span class="inline-flex items-center gap-1.5 rounded-full bg-amber-100 dark:bg-amber-900/40 px-2 py-0.5 text-[11px] font-semibold text-amber-800 dark:text-amber-300">
                      <span class="h-1.5 w-1.5 rounded-full bg-amber-500"></span>저장 안 한 변경
                    </span>
                  {/if}
                  {#if configMsg}
                    <span class="text-xs {configMsgWarn ? 'font-medium text-amber-700 dark:text-amber-300' : 'text-slate-500 dark:text-slate-400'}" role={configMsgWarn ? "alert" : undefined}>{configMsg}</span>
                  {/if}
                </div>
              </section>
            {:else if settingsTab === "voice"}
              <!-- VoicePanel is kept mounted below (avoids re-mount lag on tab switch) -->
            {:else}
              <section class="rounded-lg border border-slate-200 dark:border-slate-700 bg-white/90 dark:bg-slate-900/90 p-5 shadow-sm">
                <div class="mb-5">
                  <div class="text-sm font-semibold text-slate-900 dark:text-slate-100">버전 / 백엔드</div>
                  <div class="mt-1 text-xs text-slate-500 dark:text-slate-400">aglink 본체의 버전·사용 중인 AI와, 이 앱의 연결 상태, 보조 기능이 잘 돌고 있는지를 한눈에 봅니다.</div>
                </div>

                <div class="grid gap-4 md:grid-cols-2">
                  <div class="rounded-lg border border-slate-200 dark:border-slate-700 bg-slate-50/70 dark:bg-slate-800/70 p-4">
                    <div class="mb-3 text-[11px] font-bold uppercase tracking-[0.18em] text-slate-500 dark:text-slate-400">aglink</div>
                    <div class="space-y-2 text-sm text-slate-700 dark:text-slate-300">
                      <div class="flex items-center justify-between gap-3">
                        <span>현재 백엔드</span>
                        <span class="rounded-full bg-slate-200 dark:bg-slate-700 px-2.5 py-1 text-xs font-semibold text-slate-700 dark:text-slate-300">{backendBadgeText() || "-"}</span>
                      </div>
                      <div class="flex items-center justify-between gap-3">
                        <span>실행 버전</span>
                        <span class="font-mono text-xs text-slate-600 dark:text-slate-400">{versionInfo.version || "-"}</span>
                      </div>
                      <div class="flex items-center justify-between gap-3">
                        <span>최신 버전</span>
                        <span class="font-mono text-xs text-slate-600 dark:text-slate-400">{versionInfo.latestVersion || "-"}</span>
                      </div>
                      <div class="flex items-center justify-between gap-3">
                        <span>업데이트 상태</span>
                        <span class={`rounded-full px-2.5 py-1 text-xs font-semibold ${versionInfo.updateAvailable ? "bg-amber-100 dark:bg-amber-900/40 text-amber-800 dark:text-amber-300" : "bg-emerald-100 dark:bg-emerald-900/40 text-emerald-700 dark:text-emerald-300"}`}>
                          {versionInfo.updateAvailable ? "업데이트 필요" : "최신"}
                        </span>
                      </div>
                      {#if versionInfo.opencodeInstalled}
                        <div class="flex items-center justify-between gap-3 border-t border-slate-200 dark:border-slate-700 pt-2">
                          <span>opencode</span>
                          <span class="font-mono text-xs text-slate-600 dark:text-slate-400" title={versionInfo.opencodeLatest ? `최신: ${versionInfo.opencodeLatest}` : ""}>
                            {versionInfo.opencodeInstalled}
                            {#if versionInfo.opencodeUpdateAvailable}
                              <span class="ml-1 rounded-full bg-amber-100 dark:bg-amber-900/40 px-2 py-0.5 font-semibold text-amber-800 dark:text-amber-300">▲ {versionInfo.opencodeLatest} 있음</span>
                            {/if}
                          </span>
                        </div>
                        {#if versionInfo.opencodeUpdateAvailable}
                          <div class="text-[11px] text-amber-700 dark:text-amber-300">터미널에서 <code class="rounded bg-amber-50 dark:bg-amber-950/40 px-1">opencode upgrade</code> 로 업데이트하세요.</div>
                        {/if}
                      {/if}
                    </div>
                  </div>

                  <div class="rounded-lg border border-slate-200 dark:border-slate-700 bg-slate-50/70 dark:bg-slate-800/70 p-4">
                    <div class="mb-3 text-[11px] font-bold tracking-[0.08em] text-slate-500 dark:text-slate-400">데스크톱 앱 연결</div>
                    <div class="space-y-2 text-sm text-slate-700 dark:text-slate-300">
                      <div class="flex items-center justify-between gap-3">
                        <span>연결 상태</span>
                        <span class={`rounded-full px-2.5 py-1 text-xs font-semibold ${chat.connected ? "bg-emerald-100 dark:bg-emerald-900/40 text-emerald-700 dark:text-emerald-300" : "bg-rose-100 dark:bg-rose-900/40 text-rose-700 dark:text-rose-300"}`}>
                          {chat.connected ? "연결됨" : "끊김"}
                        </span>
                      </div>
                      <div class="flex items-center justify-between gap-3">
                        <span>실행 중인 빌드</span>
                        <span class="truncate font-mono text-xs text-slate-600 dark:text-slate-400">{versionInfo.commit || "-"}</span>
                      </div>
                      <div class="flex items-center justify-between gap-3">
                        <span>최신 빌드</span>
                        <span class="truncate font-mono text-xs text-slate-600 dark:text-slate-400">{versionInfo.latestCommit || "-"}</span>
                      </div>
                      <div class="flex items-center justify-between gap-3">
                        <span>빌드 시각</span>
                        <span class="truncate font-mono text-xs text-slate-600 dark:text-slate-400">{versionInfo.buildTime || "-"}</span>
                      </div>
                    </div>
                  </div>
                </div>

                <div class="mt-6">
                  <div class="mb-3 text-[11px] font-bold uppercase tracking-[0.18em] text-slate-500 dark:text-slate-400">aglink 보조 기능</div>
                  <div class="space-y-3">
                    {#if (auxInfo.features || []).length === 0}
                      <div class="flex items-center gap-2 rounded-lg border border-dashed border-slate-300 dark:border-slate-600 px-4 py-4 text-sm text-slate-500 dark:text-slate-400" role="status">
                        {#if auxLoading}
                          <span class="h-3.5 w-3.5 shrink-0 animate-spin rounded-full border-2 border-slate-300 border-t-blue-500 dark:border-slate-600 dark:border-t-blue-400"></span>
                          불러오는 중…
                        {:else if !chat.connected}
                          aglink 본체에 연결되면 보조 기능 상태를 보여 드립니다.
                        {:else}
                          켜져 있는 보조 기능이 없습니다.
                        {/if}
                      </div>
                    {:else}
                      {#each auxInfo.features || [] as feature}
                        <div class="rounded-lg border border-slate-200 dark:border-slate-700 bg-slate-50/70 dark:bg-slate-800/70 p-4">
                          <div class="flex flex-wrap items-center gap-3">
                            <div class="min-w-0 flex-1">
                              <div class="text-sm font-semibold text-slate-900 dark:text-slate-100">
                                {feature.label}{#if feature.version} <span class="font-mono text-xs text-slate-500 dark:text-slate-400">({feature.version})</span>{/if}
                              </div>
                              {#if feature.detail}
                                <div class="mt-1 whitespace-pre-wrap break-words text-xs leading-5 text-slate-500 dark:text-slate-400">{feature.detail}</div>
                              {/if}
                            </div>
                            <span class={`rounded-full px-2.5 py-1 text-xs font-semibold ${auxStateTone(feature.state)}`}>
                              {auxStateLabel(feature.state)}
                            </span>
                          </div>
                        </div>
                      {/each}
                    {/if}
                  </div>
                </div>
              </section>
            {/if}
            {#if voiceLoaded}
              <div class:hidden={settingsTab !== "voice"}><VoicePanel /></div>
            {/if}
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>

{#if promptState}
  <div class="fixed inset-0 z-[70] flex items-center justify-center bg-slate-950/35 px-4">
    <div class="w-full max-w-md rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 p-5 shadow-2xl" role="dialog" aria-modal="true" aria-label={promptState.title}>
      <div class="text-lg font-semibold text-slate-900 dark:text-slate-100">{promptState.title}</div>
      {#if promptState.label}
        <div class="mt-2 text-sm text-slate-500 dark:text-slate-400">{promptState.label}</div>
      {/if}
      <input
        use:autofocus={true}
        class="mt-4 w-full rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-950 px-4 py-3 text-sm text-slate-900 dark:text-slate-100 outline-none focus:border-blue-400 focus:ring-4 focus:ring-blue-100 dark:focus:ring-blue-900/30"
        value={promptState.value}
        aria-label={promptState.label || promptState.title}
        oninput={(event) => (promptState = { ...promptState, value: event.currentTarget.value })}
        onkeydown={(event) => {
          // Enter that only commits a Korean IME syllable must not submit.
          if (event.key === "Enter" && !event.isComposing && event.keyCode !== 229) {
            event.preventDefault();
            resolvePrompt(promptState.value);
          }
        }}
      />
      <div class="mt-5 flex justify-end gap-2">
        <button class="rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-4 py-2 text-sm font-medium text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => resolvePrompt(null)}>
          취소
        </button>
        <button class="rounded-md bg-blue-600 px-4 py-2 text-sm font-semibold text-white hover:bg-blue-700" onclick={() => resolvePrompt(promptState.value)}>
          확인
        </button>
      </div>
    </div>
  </div>
{/if}

{#if confirmState}
  <div class="fixed inset-0 z-[70] flex items-center justify-center bg-slate-950/35 px-4">
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <div
      class="w-full max-w-md rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 p-5 shadow-2xl"
      role="alertdialog"
      aria-modal="true"
      aria-label={confirmState.title}
      tabindex="-1"
      onkeydown={(event) => {
        // Enter on the dialog itself confirms; on a focused button the button decides.
        if (event.key === "Enter" && !(event.target instanceof HTMLButtonElement)) {
          event.preventDefault();
          resolveConfirm(true);
        }
      }}
    >
      <div class="text-lg font-semibold text-slate-900 dark:text-slate-100">{confirmState.title}</div>
      <div class="mt-3 whitespace-pre-wrap break-words text-sm leading-6 text-slate-600 dark:text-slate-400">{confirmState.message}</div>
      <div class="mt-5 flex justify-end gap-2">
        <button class="rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-4 py-2 text-sm font-medium text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => resolveConfirm(false)}>
          취소
        </button>
        <button use:autofocus class="rounded-md bg-rose-600 px-4 py-2 text-sm font-semibold text-white hover:bg-rose-700 focus:outline-none focus-visible:ring-4 focus-visible:ring-rose-200 dark:focus-visible:ring-rose-900/50" onclick={() => resolveConfirm(true)}>
          {confirmState.confirmLabel}
        </button>
      </div>
    </div>
  </div>
{/if}

<ImageWindow />
<ApprovalPrompt />
<CloneScript />
<MarkdownPreview
  open={preview.open}
  name={preview.name}
  path={preview.path}
  content={preview.content}
  error={preview.error}
  isMarkdown={preview.isMarkdown}
  onClose={closePreview}
/>
<AttentionToast />
<BoardView bind:open={boardOpen} onnavigate={() => { if (view !== "chat") void backToChat(); }} />
<QuickSwitcher actions={paletteActions} bind:helpOpen />
<SnippetManager />
<SearchPanel />
<RecentSwitcher />
