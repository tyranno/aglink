<script>
  import { onMount, tick } from "svelte";
  import { Events } from "@wailsio/runtime";
  import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";
  import PaneNode from "./PaneNode.svelte";
  import GroupNode from "./GroupNode.svelte";
  import {
    chat,
    MAX_PANES,
    WORKING_GRACE_MS,
    parseJSON,
    findWebConv,
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
    rootWebGroups,
    groupPathLabel,
    handleGroupDragOver,
    handleGroupDragLeave,
    handleGroupDrop,
    UNGROUPED_DROP_ZONE,
    closeProgressPopup,
    closePaneBackendMenu,
    setTargetBackend,
    closePaneWorkDirMenu,
    setTargetWorkDir,
  } from "./paneStore.svelte.js";

  let view = $state("chat");
  let settingsTab = $state("settings");
  let sidebarCollapsed = $state(false);
  let versionInfo = $state({});
  let auxInfo = $state({ features: [] });
  let settingsSchema = $state([]);
  let settingsValues = $state({});
  let settingsOriginal = $state({});
  let settingsMsg = $state("");
  let activeSettingsGroup = $state("");
  let configText = $state("");
  let configMsg = $state("");
  let openMenuId = $state("");
  let promptState = $state(null);
  let confirmState = $state(null);

  let promptResolver = null;
  let confirmResolver = null;

  function conversationLabel(conv) {
    return conv?.title || conv?.id || "이름 없는 대화";
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
    }
  }

  async function loadRawConfig() {
    configMsg = "";
    try {
      const data = parseJSON(await ControlService.GetConfig(), { config: "", error: "" });
      if (data.error) {
        configMsg = data.error;
        configText = "";
        return;
      }
      configText = data.config || "";
    } catch (error) {
      configMsg = `raw config를 불러오지 못했습니다: ${error}`;
      configText = "";
    }
  }

  async function loadSettingsTab(tab) {
    settingsTab = tab;
    if (tab === "settings") {
      await Promise.all([loadSettingsSchema(), loadRawConfig()]);
      return;
    }
    await Promise.all([loadVersionInfo(), loadAuxInfo()]);
  }

  async function openSettings(tab) {
    view = "settings";
    await loadSettingsTab(tab);
  }

  async function backToChat() {
    view = "chat";
    await tick();
  }

  function updateSettingValue(key, value) {
    settingsValues = { ...settingsValues, [key]: value };
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
      settingsMsg = "변경된 항목이 없습니다.";
      return;
    }
    settingsMsg = "저장 중...";
    try {
      const reply = parseJSON(
        await ControlService.SetSettings(JSON.stringify(updates)),
        { ok: false, error: "unknown error" },
      );
      if (reply.ok) {
        settingsMsg = "저장했습니다. 필요한 경우 서비스 재시작이 필요할 수 있습니다.";
        await loadSettingsSchema();
        // aglink's config hot-reload is debounced (~300ms) before it takes
        // effect, so refetch the version/backend info shortly after saving —
        // otherwise the header badge keeps showing the backend from launch.
        window.setTimeout(() => {
          void loadVersionInfo();
        }, 500);
      } else {
        settingsMsg = `저장 실패: ${reply.error || "알 수 없는 오류"}`;
      }
    } catch (error) {
      settingsMsg = `저장 실패: ${error}`;
    }
  }

  async function saveRawConfig() {
    configMsg = "저장 중...";
    try {
      const reply = parseJSON(
        await ControlService.SetConfig(configText),
        { ok: false, error: "unknown error" },
      );
      configMsg = reply.ok ? "raw config를 저장했습니다." : `저장 실패: ${reply.error || "알 수 없는 오류"}`;
    } catch (error) {
      configMsg = `저장 실패: ${error}`;
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

  function auxStateTone(state) {
    if (state === "running") return "bg-emerald-100 text-emerald-700";
    if (state === "idle") return "bg-slate-200 text-slate-600";
    return "bg-rose-100 text-rose-700";
  }

  onMount(() => {
    void Promise.all([loadConversations(), loadVersionInfo(), pollWorkers(true)]);
    ControlService.Connected()
      .then((value) => {
        chat.connected = value;
      })
      .catch(() => {});

    const offStatus = Events.On("control:status", (event) => {
      chat.connected = !!event.data;
      if (chat.connected) {
        void Promise.all([loadConversations({ forceReloadCurrent: true }), loadVersionInfo(), pollWorkers(true)]);
        if (view === "settings" && settingsTab === "connection") void loadAuxInfo();
      } else {
        chat.working = new Map();
      }
    });

    const offFrame = Events.On("frame", (event) => {
      handleFrame(event.data || {});
    });

    const tickTimer = window.setInterval(() => {
      chat.nowTick = Date.now();
    }, 1000);

    const workerTimer = window.setInterval(() => {
      void pollWorkers();
    }, 3000);

    const clickAway = (event) => {
      const target = event.target;
      if (!(target instanceof HTMLElement)) return;
      if (!target.closest("[data-conv-menu]")) openMenuId = "";
      if (!target.closest("[data-progress-popup]")) closeProgressPopup();
      if (!target.closest("[data-pane-backend-menu]")) closePaneBackendMenu();
      if (!target.closest("[data-pane-workdir-menu]")) closePaneWorkDirMenu();
    };

    const onEscape = (event) => {
      if (event.key !== "Escape") return;
      if (promptState) resolvePrompt(null);
      if (confirmState) resolveConfirm(false);
      if (chat.progressPopupPaneId) closeProgressPopup();
      if (chat.backendMenuPaneId) closePaneBackendMenu();
      if (chat.workDirMenuPaneId) closePaneWorkDirMenu();
      openMenuId = "";
    };

    document.addEventListener("click", clickAway);
    document.addEventListener("keydown", onEscape);

    return () => {
      offStatus();
      offFrame();
      window.clearInterval(tickTimer);
      window.clearInterval(workerTimer);
      document.removeEventListener("click", clickAway);
      document.removeEventListener("keydown", onEscape);
    };
  });
</script>

<div class="flex h-full min-h-0 w-full overflow-hidden text-[13px] text-slate-900">
  <div class="flex h-full w-full flex-col overflow-hidden">
    <header class="flex h-12 shrink-0 items-center gap-2 border-b border-slate-800 bg-slate-900 px-3 text-white shadow-sm">
      <span class={`inline-flex shrink-0 rounded-md px-2 py-1 text-[11px] font-semibold ${chat.connected ? "bg-emerald-500 text-white" : "bg-rose-500 text-white"}`}>
        {chat.connected ? "연결됨" : "연결 끊김"}
      </span>
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
          title="설정"
          aria-label="설정"
        >
          ⚙
        </button>
        <button
          class="grid h-8 w-8 place-items-center rounded-md border border-white/20 bg-white/5 text-sm text-slate-100 hover:bg-white/10"
          onclick={() => openSettings("connection")}
          title="연결 / aglink"
          aria-label="연결 / aglink"
        >
          🔌
        </button>
      </div>
    </header>

    {#if view === "chat"}
      <div class="flex min-h-0 flex-1 overflow-hidden">
        {#if !sidebarCollapsed}
          <aside class="flex h-full w-[296px] shrink-0 flex-col border-r border-slate-200 bg-slate-50/90 backdrop-blur">
            <div class="flex h-11 shrink-0 items-center gap-2 border-b border-slate-200 px-3">
              <button
                class="grid h-8 w-8 shrink-0 place-items-center rounded-md border border-slate-300 bg-white text-sm text-slate-700 hover:bg-slate-100"
                onclick={() => (sidebarCollapsed = true)}
                title="대화 목록 숨기기"
                aria-label="대화 목록 숨기기"
              >
                «
              </button>
              <div class="min-w-0 flex-1">
                <div class="truncate text-sm font-semibold text-slate-900">대화 목록</div>
              </div>
              <button
                class="grid h-8 w-8 place-items-center rounded-md border border-slate-300 bg-white text-sm text-slate-700 hover:bg-slate-100"
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

            <div class="min-h-0 flex-1 overflow-y-auto px-3 py-3">
              <div class="mb-4">
                <div class="mb-2 px-1 text-[11px] font-bold tracking-[0.04em] text-slate-500">텔레그램 채널</div>
                <div class="border-l-2 border-slate-200 pl-2">
                  {#if chat.telegram}
                    <div
                      class={`relative flex h-9 w-full items-center gap-1 rounded-md px-1.5 transition ${isFocusedTarget({ kind: "telegram" }) ? "bg-blue-50 text-blue-950 ring-1 ring-blue-200" : "hover:bg-slate-100"}`}
                      data-conv-menu
                    >
                      <button
                        class="min-w-0 flex flex-1 items-center gap-2 rounded px-1.5 py-1 text-left transition hover:bg-slate-100/70"
                        draggable="true"
                        ondragstart={(event) => handleConversationDragStart(event, { kind: "telegram" })}
                        ondragend={handleConversationDragEnd}
                        onclick={() => selectTargetFromSidebar({ kind: "telegram" })}
                        title={chat.telegram.title || "텔레그램"}
                      >
                        <span class="inline-flex h-2.5 w-2.5 shrink-0 rounded-full bg-sky-500"></span>
                        <span class="min-w-0 flex-1 truncate text-sm font-semibold">
                          {chat.telegram.title || "텔레그램"}
                        </span>
                        <span class="shrink-0 rounded-full bg-slate-200 px-1.5 py-0.5 text-[10px] font-bold text-slate-700">{backendLabel(chat.telegram.backend)}</span>
                        {#if chat.unread.has("telegram")}
                          <span class="shrink-0 rounded-full bg-blue-600 px-1.5 py-0.5 text-[10px] font-bold text-white">NEW</span>
                        {/if}
                      </button>
                      <button
                        class="grid h-7 w-7 shrink-0 place-items-center rounded-md border border-slate-200 bg-white/80 text-base leading-none text-slate-600 hover:bg-slate-100"
                        onclick={() => (openMenuId = openMenuId === "telegram" ? "" : "telegram")}
                        title="채널 설정"
                        aria-label="채널 설정"
                      >
                        ⋯
                      </button>
                      {#if openMenuId === "telegram"}
                        <div class="absolute right-0 top-8 z-20 w-40 rounded-lg border border-slate-200 bg-white p-1.5 shadow-xl">
                          <div class="px-3 py-1 text-[11px] font-semibold uppercase tracking-wide text-slate-400">AI backend</div>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-100" onclick={() => setChannelBackend({ kind: "telegram" }, "default")}>Default</button>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-100" onclick={() => setChannelBackend({ kind: "telegram" }, "claude")}>Claude</button>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-100" onclick={() => setChannelBackend({ kind: "telegram" }, "codex")}>Codex</button>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-100" onclick={() => setChannelBackend({ kind: "telegram" }, "opencode")}>OpenCode</button>
                        </div>
                      {/if}
                    </div>
                  {:else}
                    <div class="rounded-md px-2 py-3 text-sm text-slate-500">텔레그램 대화를 찾지 못했습니다.</div>
                  {/if}
                </div>
              </div>

              <div>
                {#snippet webConvRow(conv)}
                  <div
                    class={`relative flex h-10 items-center gap-1 rounded-md border px-1.5 shadow-sm ${isFocusedTarget({ kind: "web", id: conv.id }) ? "border-blue-200 bg-blue-50 text-blue-950" : "border-slate-200 bg-white/85 text-slate-900"}`}
                    data-conv-menu
                    title={conv.workDir ? `${conversationLabel(conv)} · ${conv.workDir}` : conversationLabel(conv)}
                  >
                      <button
                        class="min-w-0 flex flex-1 items-center gap-2 rounded px-1.5 py-1 text-left transition hover:bg-slate-100/70"
                        draggable="true"
                        ondragstart={(event) => handleConversationDragStart(event, { kind: "web", id: conv.id })}
                        ondragend={handleConversationDragEnd}
                        onclick={() => selectTargetFromSidebar({ kind: "web", id: conv.id })}
                      >
                        <span class="min-w-0 flex-1 truncate text-sm font-semibold">{conversationLabel(conv)}</span>
                        <span class="shrink-0 font-mono text-[11px] text-slate-400">#{conv.id}</span>
                        <span class="shrink-0 rounded-full bg-slate-200 px-1.5 py-0.5 text-[10px] font-bold text-slate-700">{backendLabel(conv.backend)}</span>
                        {#if isFocusedTarget({ kind: "web", id: conv.id })}
                          <span class="shrink-0 rounded-full bg-blue-600 px-1.5 py-0.5 text-[10px] font-bold text-white">현재</span>
                        {/if}
                        {#if chat.unread.has(`web:${conv.id}`)}
                          <span class="shrink-0 rounded-full bg-blue-600 px-1.5 py-0.5 text-[10px] font-bold text-white">NEW</span>
                        {/if}
                      </button>

                      <button
                        class="grid h-7 w-7 shrink-0 place-items-center rounded-md border border-slate-200 bg-white/80 text-base leading-none text-slate-600 hover:bg-slate-100"
                        onclick={() => (openMenuId = openMenuId === conv.id ? "" : conv.id)}
                        title="대화 관리"
                        aria-label="대화 관리"
                      >
                        ⋯
                      </button>

                      {#if openMenuId === conv.id}
                        <div class="absolute right-0 top-9 z-20 w-48 rounded-lg border border-slate-200 bg-white p-1.5 shadow-xl">
                          <div class="px-3 py-1 text-[11px] font-semibold uppercase tracking-wide text-slate-400">AI backend</div>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-100" onclick={() => setChannelBackend({ kind: "web", id: conv.id }, "default")}>Default</button>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-100" onclick={() => setChannelBackend({ kind: "web", id: conv.id }, "claude")}>Claude</button>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-100" onclick={() => setChannelBackend({ kind: "web", id: conv.id }, "codex")}>Codex</button>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-100" onclick={() => setChannelBackend({ kind: "web", id: conv.id }, "opencode")}>OpenCode</button>
                          <div class="my-1 h-px bg-slate-100"></div>
                          <div class="px-3 py-1 text-[11px] font-semibold uppercase tracking-wide text-slate-400">그룹</div>
                          {#if chat.webGroupOf.get(conv.id)}
                            <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-100" onclick={() => { openMenuId = ""; setConversationGroup(conv.id, null); }}>
                              그룹에서 빼기
                            </button>
                          {/if}
                          {#each chat.webGroups as group (group.id)}
                            {#if chat.webGroupOf.get(conv.id) !== group.id}
                              <button class="block w-full truncate rounded-md px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-100" onclick={() => { openMenuId = ""; setConversationGroup(conv.id, group.id); }}>
                                {groupPathLabel(group.id)}로 이동
                              </button>
                            {/if}
                          {/each}
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-100" onclick={() => { openMenuId = ""; moveConversationToNewGroup(conv); }}>
                            새 그룹으로 이동...
                          </button>
                          <div class="my-1 h-px bg-slate-100"></div>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-100" onclick={() => { openMenuId = ""; renameWebConversation(conv); }}>
                            이름 변경
                          </button>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-100" onclick={() => { openMenuId = ""; changeWebDirectory(conv); }}>
                            작업 폴더 변경
                          </button>
                          <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-rose-700 hover:bg-rose-50" onclick={() => { openMenuId = ""; deleteWebConversation(conv); }}>
                            삭제
                          </button>
                        </div>
                      {/if}
                  </div>
                {/snippet}

                <!-- svelte-ignore a11y_no_static_element_interactions -->
                <div
                  class={`mb-2 flex items-center gap-1 rounded-md px-1 ${chat.dragOverGroupId === UNGROUPED_DROP_ZONE ? "ring-2 ring-blue-400 bg-blue-50" : ""}`}
                  ondragenter={(event) => handleGroupDragOver(event, UNGROUPED_DROP_ZONE)}
                  ondragover={(event) => handleGroupDragOver(event, UNGROUPED_DROP_ZONE)}
                  ondragleave={() => handleGroupDragLeave(UNGROUPED_DROP_ZONE)}
                  ondrop={(event) => handleGroupDrop(event, UNGROUPED_DROP_ZONE)}
                  title="대화를 여기로 드래그하면 그룹에서 빠집니다"
                >
                  <div class="flex-1 text-[11px] font-bold tracking-[0.04em] text-slate-500">로컬 채널</div>
                  <button
                    class="grid h-5 w-5 shrink-0 place-items-center rounded text-[13px] font-bold leading-none text-slate-400 hover:bg-slate-200 hover:text-slate-700"
                    onclick={newWebGroup}
                    title="새 그룹"
                    aria-label="새 그룹"
                  >
                    ⊕
                  </button>
                </div>
                <div class="space-y-1 border-l-2 border-slate-200 pl-2">
                  {#if chat.webConvs.length === 0}
                    <div class="rounded-md border border-dashed border-slate-300 bg-white/70 px-3 py-4 text-sm text-slate-500">
                      아직 로컬 대화가 없습니다. "새 대화"로 시작하세요.
                    </div>
                  {/if}

                  {#each rootWebGroups() as group (group.id)}
                    <GroupNode {group} bind:openMenuId {webConvRow} onRename={renameWebGroupPrompt} onDelete={deleteWebGroupConfirm} />
                  {/each}

                  <!-- svelte-ignore a11y_no_static_element_interactions -->
                  <div
                    class={`space-y-1 rounded-md ${chat.dragOverGroupId === UNGROUPED_DROP_ZONE ? "ring-2 ring-blue-400 bg-blue-50" : ""}`}
                    ondragenter={(event) => handleGroupDragOver(event, UNGROUPED_DROP_ZONE)}
                    ondragover={(event) => handleGroupDragOver(event, UNGROUPED_DROP_ZONE)}
                    ondragleave={() => handleGroupDragLeave(UNGROUPED_DROP_ZONE)}
                    ondrop={(event) => handleGroupDrop(event, UNGROUPED_DROP_ZONE)}
                  >
                    {#each ungroupedWebConvs() as conv (conv.id)}
                      {@render webConvRow(conv)}
                    {/each}
                  </div>
                </div>
              </div>
            </div>
          </aside>
        {:else}
          <div class="flex h-full w-7 shrink-0 flex-col items-center border-r border-slate-200 bg-slate-50/90 pt-2 backdrop-blur">
            <button
              class="grid h-8 w-6 place-items-center rounded-md border border-slate-300 bg-white text-sm text-slate-700 hover:bg-slate-100"
              onclick={() => (sidebarCollapsed = false)}
              title="대화 목록 보이기"
              aria-label="대화 목록 보이기"
            >
              »
            </button>
          </div>
        {/if}

        <div class="flex min-w-0 flex-1 flex-col overflow-hidden">
          {#if chat.statusNote}
            <div class="shrink-0 border-b border-amber-200 bg-amber-50 px-4 py-2 text-xs text-amber-800">{chat.statusNote}</div>
          {/if}

          <div class="flex min-h-0 flex-1 overflow-hidden">
            <PaneNode node={chat.layout} />
          </div>
        </div>
      </div>
    {:else}
      <div class="flex min-h-0 flex-1 flex-col overflow-hidden">
        <div class="flex h-11 shrink-0 items-center gap-3 border-b border-slate-200/80 bg-white/75 px-4 backdrop-blur">
          <button
            class="grid h-8 w-8 place-items-center rounded-md border border-slate-300 bg-white text-sm font-medium text-slate-700 hover:bg-slate-100"
            onclick={backToChat}
            title="채팅으로"
            aria-label="채팅으로"
          >
            ←
          </button>
          <div class="text-sm font-semibold text-slate-900">설정</div>
          <div class="ml-auto flex gap-2">
            <button
              class={`rounded-full px-3 py-2 text-xs font-semibold ${settingsTab === "settings" ? "bg-blue-600 text-white" : "border border-slate-300 bg-white text-slate-700"}`}
              onclick={() => loadSettingsTab("settings")}
            >
              설정
            </button>
            <button
              class={`rounded-full px-3 py-2 text-xs font-semibold ${settingsTab === "connection" ? "bg-blue-600 text-white" : "border border-slate-300 bg-white text-slate-700"}`}
              onclick={() => loadSettingsTab("connection")}
            >
              연결 / aglink
            </button>
          </div>
        </div>

        <div class="min-h-0 flex-1 overflow-y-auto px-5 py-5">
          <div class="mx-auto flex w-full max-w-5xl flex-col gap-5">
            {#if settingsTab === "settings"}
              {#snippet settingSection(section)}
                <div>
                  <div class="mb-1 text-sm font-bold text-slate-800">{section.title}</div>
                  {#if section.desc}
                    <div class="mb-3 max-w-3xl whitespace-pre-wrap break-words text-xs leading-5 text-slate-500">{section.desc}</div>
                  {/if}
                  <div class="space-y-3">
                    {#each section.fields || [] as field}
                      <div class="rounded-lg border border-slate-200 bg-slate-50/70 p-4">
                        <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
                          <div class="min-w-0 md:max-w-[60%]">
                            <div class="text-sm font-semibold text-slate-900">{field.label}</div>
                            {#if field.desc}
                              <div class="mt-1 whitespace-pre-wrap break-words text-xs leading-5 text-slate-500">{field.desc}</div>
                            {/if}
                          </div>
                          <div class="md:min-w-[220px]">
                            {#if field.type === "bool"}
                              <label class="inline-flex items-center gap-2 text-sm font-medium text-slate-700">
                                <input
                                  class="h-4 w-4 rounded border-slate-300 text-blue-600 focus:ring-blue-500"
                                  type="checkbox"
                                  checked={!!settingsValues[field.key]}
                                  onchange={(event) => updateSettingValue(field.key, event.currentTarget.checked)}
                                />
                                사용
                              </label>
                            {:else if field.type === "select"}
                              <select
                                class="w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm outline-none focus:border-blue-400 focus:ring-4 focus:ring-blue-100"
                                value={settingsValues[field.key] ?? ""}
                                onchange={(event) => updateSettingValue(field.key, event.currentTarget.value)}
                              >
                                {#each selectOptionsFor(field) as option}
                                  <option value={option}>{option === "" ? "기본값" : option}</option>
                                {/each}
                              </select>
                            {:else}
                              <input
                                class="w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm outline-none focus:border-blue-400 focus:ring-4 focus:ring-blue-100"
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

              <section class="rounded-lg border border-slate-200 bg-white/90 p-5 shadow-sm">
                <div class="mb-5">
                  <div class="text-sm font-semibold text-slate-900">설정</div>
                  <div class="mt-1 text-xs text-slate-500">번호(①②③) 순서대로 채우면 AI가 연결됩니다. 잘 모르는 칸은 비워두거나 기본값 그대로 두세요.</div>
                </div>

                {#if settingsSchema.length === 0}
                  <div class="rounded-lg border border-dashed border-slate-300 px-4 py-5 text-sm text-slate-500">
                    {settingsMsg || "표시할 설정이 없습니다."}
                  </div>
                {:else}
                  <div class="mb-5 flex flex-wrap gap-1 border-b border-slate-200">
                    {#each settingsGroups as group}
                      <button
                        class="-mb-px border-b-2 px-3 py-2 text-sm font-semibold transition-colors {activeSettingsGroup ===
                        group
                          ? 'border-blue-600 text-blue-700'
                          : 'border-transparent text-slate-500 hover:text-slate-800'}"
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

                <div class="mt-5 flex items-center gap-3">
                  <button class="rounded-md bg-blue-600 px-4 py-2 text-sm font-semibold text-white hover:bg-blue-700" onclick={saveSettings}>
                    설정 저장
                  </button>
                  {#if settingsMsg}
                    <span class="text-xs text-slate-500">{settingsMsg}</span>
                  {/if}
                </div>
              </section>

              <section class="rounded-lg border border-slate-200 bg-white/90 p-5 shadow-sm">
                <div class="mb-5">
                  <div class="text-sm font-semibold text-slate-900">전문가용: 설정 파일 직접 편집</div>
                  <div class="mt-1 text-xs text-slate-500">위 항목으로 안 되는 세부 설정(예: SSH 호스트 목록)만 여기서 직접 고칩니다. 형식이 틀리면 저장되지 않으니 익숙하지 않으면 건드리지 마세요.</div>
                </div>
                <textarea
                  class="min-h-[320px] w-full rounded-lg border border-slate-300 bg-slate-950 px-4 py-3 font-mono text-[12px] leading-6 text-slate-100 outline-none focus:border-blue-400 focus:ring-4 focus:ring-blue-100"
                  spellcheck="false"
                  value={configText}
                  oninput={(event) => (configText = event.currentTarget.value)}
                ></textarea>
                <div class="mt-4 flex items-center gap-3">
                  <button class="rounded-md bg-slate-900 px-4 py-2 text-sm font-semibold text-white hover:bg-slate-800" onclick={saveRawConfig}>
                    raw 저장
                  </button>
                  {#if configMsg}
                    <span class="text-xs text-slate-500">{configMsg}</span>
                  {/if}
                </div>
              </section>
            {:else}
              <section class="rounded-lg border border-slate-200 bg-white/90 p-5 shadow-sm">
                <div class="mb-5">
                  <div class="text-sm font-semibold text-slate-900">버전 / 백엔드</div>
                  <div class="mt-1 text-xs text-slate-500">웹판의 `/api/status`, `/api/version`, `/api/aux` 정보를 데스크톱 상태와 함께 요약합니다.</div>
                </div>

                <div class="grid gap-4 md:grid-cols-2">
                  <div class="rounded-lg border border-slate-200 bg-slate-50/70 p-4">
                    <div class="mb-3 text-[11px] font-bold uppercase tracking-[0.18em] text-slate-500">aglink</div>
                    <div class="space-y-2 text-sm text-slate-700">
                      <div class="flex items-center justify-between gap-3">
                        <span>현재 백엔드</span>
                        <span class="rounded-full bg-slate-200 px-2.5 py-1 text-xs font-semibold text-slate-700">{backendBadgeText() || "-"}</span>
                      </div>
                      <div class="flex items-center justify-between gap-3">
                        <span>실행 버전</span>
                        <span class="font-mono text-xs text-slate-600">{versionInfo.version || "-"}</span>
                      </div>
                      <div class="flex items-center justify-between gap-3">
                        <span>최신 버전</span>
                        <span class="font-mono text-xs text-slate-600">{versionInfo.latestVersion || "-"}</span>
                      </div>
                      <div class="flex items-center justify-between gap-3">
                        <span>업데이트 상태</span>
                        <span class={`rounded-full px-2.5 py-1 text-xs font-semibold ${versionInfo.updateAvailable ? "bg-amber-100 text-amber-800" : "bg-emerald-100 text-emerald-700"}`}>
                          {versionInfo.updateAvailable ? "업데이트 필요" : "최신"}
                        </span>
                      </div>
                      {#if versionInfo.opencodeInstalled}
                        <div class="flex items-center justify-between gap-3 border-t border-slate-200 pt-2">
                          <span>opencode</span>
                          <span class="font-mono text-xs text-slate-600" title={versionInfo.opencodeLatest ? `최신: ${versionInfo.opencodeLatest}` : ""}>
                            {versionInfo.opencodeInstalled}
                            {#if versionInfo.opencodeUpdateAvailable}
                              <span class="ml-1 rounded-full bg-amber-100 px-2 py-0.5 font-semibold text-amber-800">▲ {versionInfo.opencodeLatest} 있음</span>
                            {/if}
                          </span>
                        </div>
                        {#if versionInfo.opencodeUpdateAvailable}
                          <div class="text-[11px] text-amber-700">터미널에서 <code class="rounded bg-amber-50 px-1">opencode upgrade</code> 로 업데이트하세요.</div>
                        {/if}
                      {/if}
                    </div>
                  </div>

                  <div class="rounded-lg border border-slate-200 bg-slate-50/70 p-4">
                    <div class="mb-3 text-[11px] font-bold uppercase tracking-[0.18em] text-slate-500">desktop control</div>
                    <div class="space-y-2 text-sm text-slate-700">
                      <div class="flex items-center justify-between gap-3">
                        <span>연결 상태</span>
                        <span class={`rounded-full px-2.5 py-1 text-xs font-semibold ${chat.connected ? "bg-emerald-100 text-emerald-700" : "bg-rose-100 text-rose-700"}`}>
                          {chat.connected ? "connected" : "disconnected"}
                        </span>
                      </div>
                      <div class="flex items-center justify-between gap-3">
                        <span>running commit</span>
                        <span class="truncate font-mono text-xs text-slate-600">{versionInfo.commit || "-"}</span>
                      </div>
                      <div class="flex items-center justify-between gap-3">
                        <span>latest commit</span>
                        <span class="truncate font-mono text-xs text-slate-600">{versionInfo.latestCommit || "-"}</span>
                      </div>
                      <div class="flex items-center justify-between gap-3">
                        <span>build time</span>
                        <span class="truncate font-mono text-xs text-slate-600">{versionInfo.buildTime || "-"}</span>
                      </div>
                    </div>
                  </div>
                </div>

                <div class="mt-6">
                  <div class="mb-3 text-[11px] font-bold uppercase tracking-[0.18em] text-slate-500">aglink 보조 기능</div>
                  <div class="space-y-3">
                    {#if (auxInfo.features || []).length === 0}
                      <div class="rounded-lg border border-dashed border-slate-300 px-4 py-4 text-sm text-slate-500">
                        get_aux 응답이 비어 있습니다.
                      </div>
                    {:else}
                      {#each auxInfo.features || [] as feature}
                        <div class="rounded-lg border border-slate-200 bg-slate-50/70 p-4">
                          <div class="flex flex-wrap items-center gap-3">
                            <div class="min-w-0 flex-1">
                              <div class="text-sm font-semibold text-slate-900">
                                {feature.label}{#if feature.version} <span class="font-mono text-xs text-slate-500">({feature.version})</span>{/if}
                              </div>
                              {#if feature.detail}
                                <div class="mt-1 whitespace-pre-wrap break-words text-xs leading-5 text-slate-500">{feature.detail}</div>
                              {/if}
                            </div>
                            <span class={`rounded-full px-2.5 py-1 text-xs font-semibold ${auxStateTone(feature.state)}`}>
                              {feature.state || "unknown"}
                            </span>
                          </div>
                        </div>
                      {/each}
                    {/if}
                  </div>
                </div>
              </section>
            {/if}
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>

{#if promptState}
  <div class="fixed inset-0 z-40 flex items-center justify-center bg-slate-950/35 px-4">
    <div class="w-full max-w-md rounded-lg border border-slate-200 bg-white p-5 shadow-2xl">
      <div class="text-lg font-semibold text-slate-900">{promptState.title}</div>
      {#if promptState.label}
        <div class="mt-2 text-sm text-slate-500">{promptState.label}</div>
      {/if}
      <input
        class="mt-4 w-full rounded-md border border-slate-300 px-4 py-3 text-sm outline-none focus:border-blue-400 focus:ring-4 focus:ring-blue-100"
        value={promptState.value}
        oninput={(event) => (promptState = { ...promptState, value: event.currentTarget.value })}
        onkeydown={(event) => {
          if (event.key === "Enter") resolvePrompt(promptState.value);
        }}
      />
      <div class="mt-5 flex justify-end gap-2">
        <button class="rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-medium text-slate-700 hover:bg-slate-100" onclick={() => resolvePrompt(null)}>
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
  <div class="fixed inset-0 z-40 flex items-center justify-center bg-slate-950/35 px-4">
    <div class="w-full max-w-md rounded-lg border border-slate-200 bg-white p-5 shadow-2xl">
      <div class="text-lg font-semibold text-slate-900">{confirmState.title}</div>
      <div class="mt-3 whitespace-pre-wrap break-words text-sm leading-6 text-slate-600">{confirmState.message}</div>
      <div class="mt-5 flex justify-end gap-2">
        <button class="rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-medium text-slate-700 hover:bg-slate-100" onclick={() => resolveConfirm(false)}>
          취소
        </button>
        <button class="rounded-md bg-rose-600 px-4 py-2 text-sm font-semibold text-white hover:bg-rose-700" onclick={() => resolveConfirm(true)}>
          {confirmState.confirmLabel}
        </button>
      </div>
    </div>
  </div>
{/if}
