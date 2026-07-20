import { tick } from "svelte";
import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";

export const MAX_PANES = 4;
export const MIN_PANE_PERCENT = 15;
export const WORKING_GRACE_MS = 30000;
export const STALE_AFTER_MS = 5 * 60 * 1000;

export const SLASH_COMMANDS = [
  { cmd: "!project", desc: "프로젝트 등록/삭제/목록 (add|remove|list)" },
  { cmd: "!chat", desc: "대화 생성/목록/전환 (new|list|use)" },
  { cmd: "!status", desc: "실행 중 작업 + 활성 대화 + 백엔드" },
  { cmd: "!cancel", desc: "진행 중 작업 취소" },
  { cmd: "!parallel", desc: "여러 프롬프트를 동시에 실행 (p1 | p2 | ...)" },
  { cmd: "!task", desc: "예약 작업/알림 (add|once|list|pause|resume|cancel|update|help)" },
  { cmd: "!history", desc: "대화 기록 조회 (list)" },
  { cmd: "!compact", desc: "지금까지 대화 요약 저장 후 세션 새로 시작" },
  { cmd: "!user", desc: "허용 사용자 관리 (add|remove|list)" },
  { cmd: "!screen", desc: "화면 제어 (list|shot|region|preset|click)" },
  { cmd: "!remind", desc: "일회성 알림 (구버전 호환)" },
  { cmd: "!cron", desc: "반복 작업 (구버전 호환)" },
  { cmd: "!backend", desc: "AI 백엔드 전환 (claude|codex)" },
  { cmd: "!update", desc: "새 버전 빌드 & 자동 재시작" },
  { cmd: "!help", desc: "이 도움말" },
];

export const chat = $state({
  connected: false,
  telegram: null,
  webConvs: [],
  panes: [
    { id: "pane-0", target: null, attachments: [], composerText: "", commandMenuHidden: false, highlightedCommandIndex: 0 },
  ],
  layout: { type: "leaf", paneId: "pane-0" },
  focusedPaneId: "pane-0",
  messagesByKey: new Map(),
  drafts: new Map(),
  sentHistoryByKey: new Map(),
  historyIndexByKey: new Map(),
  working: new Map(),
  unread: new Set(),
  loadingBuffers: new Map(),
  statusNote: "",
  nowTick: Date.now(),
  draggingPaneId: null,
  draggingConversation: null,
  overPaneId: null,
  overZone: null,
  webGroups: [],
  webGroupOf: new Map(),
  dragOverGroupId: "",
  draggingGroupId: null,
  dragOverGroupReorder: null,
  dragOverGroupZone: null,
  progressByKey: new Map(),
  progressPopupPaneId: null,
  backendMenuPaneId: null,
  workDirMenuPaneId: null,
});

export const UNGROUPED_DROP_ZONE = "none";
const MAX_PROGRESS_LINES = 500;

let attachmentSeq = 0;
let paneSeq = 1;
let layoutSeq = 0;
let webGroupSeq = 1;
export const paneTextareas = new Map();
export const paneLogs = new Map();

// How close to the bottom still counts as "following the latest message".
export const NEAR_BOTTOM_PX = 80;

// ---- web channel groups (client-side only display grouping; aglink's
// own conversation management is untouched) ----

const WEB_GROUPS_STORAGE_KEY = "aglink-desktop:webGroups";

function loadWebGroupsFromStorage() {
  try {
    const raw = localStorage.getItem(WEB_GROUPS_STORAGE_KEY);
    if (!raw) return;
    const data = JSON.parse(raw);
    if (Array.isArray(data.groups)) chat.webGroups = data.groups;
    if (Array.isArray(data.assignments)) chat.webGroupOf = new Map(data.assignments);
    if (typeof data.seq === "number") webGroupSeq = data.seq;
  } catch {
    // Corrupt or missing local storage just starts ungrouped.
  }
}

function persistWebGroups() {
  try {
    localStorage.setItem(
      WEB_GROUPS_STORAGE_KEY,
      JSON.stringify({ groups: chat.webGroups, assignments: [...chat.webGroupOf.entries()], seq: webGroupSeq }),
    );
  } catch {
    // Ignore quota/serialization errors; grouping just won't persist.
  }
}

loadWebGroupsFromStorage();

export function createWebGroup(name) {
  const trimmed = String(name || "").trim();
  if (!trimmed) return null;
  const id = `wgroup-${webGroupSeq++}`;
  chat.webGroups = [...chat.webGroups, { id, name: trimmed, collapsed: false, parentId: null }];
  persistWebGroups();
  return id;
}

export function renameWebGroup(groupId, name) {
  const trimmed = String(name || "").trim();
  if (!trimmed) return;
  chat.webGroups = chat.webGroups.map((group) => (group.id === groupId ? { ...group, name: trimmed } : group));
  persistWebGroups();
}

export function deleteWebGroup(groupId) {
  // Subgroups of the deleted group are promoted to the top level rather than
  // deleted, matching how their conversations fall back to "no group".
  chat.webGroups = chat.webGroups
    .filter((group) => group.id !== groupId)
    .map((group) => (group.parentId === groupId ? { ...group, parentId: null } : group));
  const next = new Map(chat.webGroupOf);
  for (const [convId, groupIdOfConv] of next) {
    if (groupIdOfConv === groupId) next.delete(convId);
  }
  chat.webGroupOf = next;
  persistWebGroups();
}

export function rootWebGroups() {
  return chat.webGroups.filter((group) => !group.parentId);
}

export function childWebGroups(parentId) {
  return chat.webGroups.filter((group) => group.parentId === parentId);
}

export function groupPathLabel(groupId) {
  const parts = [];
  let current = chat.webGroups.find((group) => group.id === groupId);
  while (current) {
    parts.unshift(current.name);
    current = current.parentId ? chat.webGroups.find((group) => group.id === current.parentId) : null;
  }
  return parts.join(" / ");
}

// True when candidateId names groupId itself or one of its descendants —
// used to refuse drags/menu moves that would create a parent/child cycle.
function isSameOrDescendantGroup(candidateId, groupId) {
  let current = chat.webGroups.find((group) => group.id === candidateId);
  while (current) {
    if (current.id === groupId) return true;
    current = current.parentId ? chat.webGroups.find((group) => group.id === current.parentId) : null;
  }
  return false;
}

function canReparentGroup(sourceGroupId, newParentId) {
  if (!newParentId) return true;
  if (newParentId === sourceGroupId) return false;
  return !isSameOrDescendantGroup(newParentId, sourceGroupId);
}

export function toggleWebGroupCollapsed(groupId) {
  chat.webGroups = chat.webGroups.map((group) => (group.id === groupId ? { ...group, collapsed: !group.collapsed } : group));
  persistWebGroups();
}

export function setConversationGroup(convId, groupId) {
  const next = new Map(chat.webGroupOf);
  if (groupId) next.set(convId, groupId);
  else next.delete(convId);
  chat.webGroupOf = next;
  persistWebGroups();
}

// Reorders sourceGroupId as targetGroupId's sibling (adopting targetGroupId's
// parent), inserted immediately before/after it depending on `position`.
export function moveGroupRelativeTo(sourceGroupId, targetGroupId, position) {
  if (!sourceGroupId || !targetGroupId || sourceGroupId === targetGroupId) return;
  const target = chat.webGroups.find((group) => group.id === targetGroupId);
  if (!target) return;
  const newParentId = target.parentId || null;
  if (!canReparentGroup(sourceGroupId, newParentId)) return;
  const groups = [...chat.webGroups];
  const fromIndex = groups.findIndex((group) => group.id === sourceGroupId);
  if (fromIndex === -1) return;
  const [moved] = groups.splice(fromIndex, 1);
  moved.parentId = newParentId;
  let toIndex = groups.findIndex((group) => group.id === targetGroupId);
  if (position === "after") toIndex += 1;
  groups.splice(toIndex, 0, moved);
  chat.webGroups = groups;
  persistWebGroups();
}

// Makes sourceGroupId the last child of targetGroupId.
export function nestGroupInto(sourceGroupId, targetGroupId) {
  if (!sourceGroupId || !targetGroupId || sourceGroupId === targetGroupId) return;
  if (!canReparentGroup(sourceGroupId, targetGroupId)) return;
  const groups = [...chat.webGroups];
  const fromIndex = groups.findIndex((group) => group.id === sourceGroupId);
  if (fromIndex === -1) return;
  const [moved] = groups.splice(fromIndex, 1);
  moved.parentId = targetGroupId;
  groups.push(moved);
  chat.webGroups = groups;
  persistWebGroups();
}

export function moveGroupToRoot(groupId) {
  const groups = [...chat.webGroups];
  const fromIndex = groups.findIndex((group) => group.id === groupId);
  if (fromIndex === -1) return;
  const [moved] = groups.splice(fromIndex, 1);
  moved.parentId = null;
  groups.push(moved);
  chat.webGroups = groups;
  persistWebGroups();
}

function groupDropZoneFromEvent(event) {
  const rect = event.currentTarget.getBoundingClientRect();
  const fraction = rect.height > 0 ? (event.clientY - rect.top) / rect.height : 0.5;
  if (fraction < 0.3) return "before";
  if (fraction > 0.7) return "after";
  return "into";
}

export function handleGroupHeaderDragStart(event, groupId) {
  chat.draggingGroupId = groupId;
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = "move";
    event.dataTransfer.setData("text/plain", groupId);
  }
}

export function handleGroupHeaderDragEnd() {
  chat.draggingGroupId = null;
  chat.dragOverGroupReorder = null;
  chat.dragOverGroupZone = null;
  chat.dragOverGroupId = "";
}

// Header-level handlers give precise before/after/into zones while a group
// is being dragged over another group's header; they stopPropagation so the
// coarser container-level handlers below (conversation drop, into-fallback)
// don't also run for the same event. When no group is being dragged (i.e. a
// conversation drag), they no-op and let the event bubble to the container.
export function handleGroupHeaderDragOver(event, groupId) {
  if (!chat.draggingGroupId || chat.draggingGroupId === groupId) return;
  event.preventDefault();
  event.stopPropagation();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
  chat.dragOverGroupReorder = groupId;
  chat.dragOverGroupZone = groupDropZoneFromEvent(event);
}

export function handleGroupHeaderDragLeave(groupId) {
  if (chat.dragOverGroupReorder === groupId) {
    chat.dragOverGroupReorder = null;
    chat.dragOverGroupZone = null;
  }
}

export function handleGroupHeaderDrop(event, groupId) {
  if (!chat.draggingGroupId) return;
  event.preventDefault();
  event.stopPropagation();
  const sourceGroupId = chat.draggingGroupId;
  const zone = chat.dragOverGroupZone || "into";
  chat.draggingGroupId = null;
  chat.dragOverGroupReorder = null;
  chat.dragOverGroupZone = null;
  if (sourceGroupId === groupId) return;
  if (zone === "before" || zone === "after") moveGroupRelativeTo(sourceGroupId, groupId, zone);
  else nestGroupInto(sourceGroupId, groupId);
}

export function webConvsInGroup(groupId) {
  return chat.webConvs.filter((conv) => chat.webGroupOf.get(conv.id) === groupId);
}

export function ungroupedWebConvs() {
  return chat.webConvs.filter((conv) => !chat.webGroupOf.has(conv.id));
}

// ---- pane/split layout persistence (which conversations are open, how the
// panes are arranged and sized) so relaunching the desktop restores it ----

const LAYOUT_STORAGE_KEY = "aglink-desktop:paneLayout";

function serializeLayoutTarget(target) {
  if (!target) return null;
  return { kind: target.kind || "telegram", id: target.id || "", project: target.project || "" };
}

export function persistLayout() {
  try {
    localStorage.setItem(
      LAYOUT_STORAGE_KEY,
      JSON.stringify({
        layout: chat.layout,
        panes: chat.panes.map((p) => ({ id: p.id, target: serializeLayoutTarget(p.target) })),
        focusedPaneId: chat.focusedPaneId,
        paneSeq,
        layoutSeq,
      }),
    );
  } catch {
    // Ignore quota/serialization errors; layout just won't persist.
  }
}

function loadLayoutFromStorage() {
  try {
    const raw = localStorage.getItem(LAYOUT_STORAGE_KEY);
    if (!raw) return;
    const data = JSON.parse(raw);
    if (!data || !data.layout || !Array.isArray(data.panes) || data.panes.length === 0) return;

    const restoredPanes = data.panes.map((p) => ({
      id: p.id,
      target: p.target ? serializeLayoutTarget(p.target) : null,
      attachments: [],
      composerText: "",
      commandMenuHidden: false,
      highlightedCommandIndex: 0,
    }));
    const leafIds = [...collectLeafIds(data.layout)].sort();
    const paneIds = restoredPanes.map((p) => p.id).sort();
    if (JSON.stringify(leafIds) !== JSON.stringify(paneIds)) return;

    chat.panes = restoredPanes;
    chat.layout = data.layout;
    chat.focusedPaneId = paneIds.includes(data.focusedPaneId) ? data.focusedPaneId : restoredPanes[0].id;
    if (typeof data.paneSeq === "number") paneSeq = Math.max(paneSeq, data.paneSeq);
    if (typeof data.layoutSeq === "number") layoutSeq = Math.max(layoutSeq, data.layoutSeq);
  } catch {
    // Corrupt or missing local storage just keeps the default single pane.
  }
}

loadLayoutFromStorage();

export function registerPaneTextarea(node, paneId) {
  paneTextareas.set(paneId, node);
  return {
    destroy() {
      if (paneTextareas.get(paneId) === node) paneTextareas.delete(paneId);
    },
  };
}

// Dragging a pane to a new dock position rebuilds the layout tree, so Svelte
// tears down the moved pane's DOM and mounts it fresh — which resets the log's
// scrollTop to 0. Remembering the offset per pane id lets the remounted log
// come back where the user left it instead of snapping to the top.
const paneScrollOffsets = new Map();

export function recordPaneLogScroll(paneId, el) {
  if (!el) return;
  paneScrollOffsets.set(paneId, {
    top: el.scrollTop,
    pinned: el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM_PX,
  });
}

function restorePaneLogScroll(paneId, el) {
  const saved = paneScrollOffsets.get(paneId);
  if (!saved) return;
  // Pinned logs re-pin instead of restoring a stale pixel offset: the pane's
  // new width reflows messages, so the old scrollTop no longer means "bottom".
  el.scrollTop = saved.pinned ? el.scrollHeight : saved.top;
}

export function registerPaneLog(node, paneId) {
  paneLogs.set(paneId, node);
  restorePaneLogScroll(paneId, node);
  // Markdown bodies and images settle a frame later and change scrollHeight,
  // so re-apply once the post-mount layout is final.
  requestAnimationFrame(() => {
    if (paneLogs.get(paneId) === node) restorePaneLogScroll(paneId, node);
  });

  // Resizing the window (snap layouts, split resizer) reflows the log without
  // touching scrollTop, which silently unpins a bottom-anchored view.
  const observer = new ResizeObserver(() => {
    if (paneScrollOffsets.get(paneId)?.pinned) node.scrollTop = node.scrollHeight;
  });
  observer.observe(node);

  return {
    destroy() {
      observer.disconnect();
      if (paneLogs.get(paneId) === node) {
        recordPaneLogScroll(paneId, node);
        paneLogs.delete(paneId);
      }
    },
  };
}

export function parseJSON(value, fallback) {
  try {
    return JSON.parse(value);
  } catch {
    return fallback;
  }
}

export function normalizeTarget(raw) {
  if (!raw) return { kind: "telegram" };
  if (typeof raw === "string") {
    try {
      return normalizeTarget(JSON.parse(raw));
    } catch {
      return { kind: "telegram" };
    }
  }
  return {
    kind: raw.kind || "telegram",
    id: raw.id || "",
    project: raw.project || "",
  };
}

export function targetKey(target) {
  if (!target || target.kind !== "web") return "telegram";
  return `web:${target.id}`;
}

export function frameTarget(frame) {
  return normalizeTarget(frame?.target);
}

export function findWebConv(id) {
  return chat.webConvs.find((conv) => conv.id === id) || null;
}

export function backendLabel(backend) {
  return backend ? String(backend).toUpperCase() : "DEFAULT";
}

const CHANNEL_BACKENDS = new Set(["default", "claude", "codex"]);

function normalizeBackendChoice(backend) {
  const value = String(backend || "default").toLowerCase();
  return CHANNEL_BACKENDS.has(value) ? value : "default";
}

export function togglePaneBackendMenu(paneId) {
  chat.backendMenuPaneId = chat.backendMenuPaneId === paneId ? null : paneId;
}

export function closePaneBackendMenu() {
  chat.backendMenuPaneId = null;
}

export function togglePaneWorkDirMenu(paneId) {
  chat.workDirMenuPaneId = chat.workDirMenuPaneId === paneId ? null : paneId;
}

export function closePaneWorkDirMenu() {
  chat.workDirMenuPaneId = null;
}

export async function setTargetBackend(target, backend) {
  const normalized = normalizeTarget(target);
  const nextBackend = normalizeBackendChoice(backend);
  try {
    const reply = parseJSON(
      await ControlService.SetChannelBackend(normalized.kind || "telegram", normalized.id || "", nextBackend),
      { ok: false },
    );
    if (!reply.ok) {
      chat.statusNote = `Backend 설정 실패: ${reply.error || "알 수 없는 오류"}`;
      return false;
    }
    chat.statusNote = "";
    closePaneBackendMenu();
    await loadConversations({ forceReloadCurrent: false });
    return true;
  } catch (error) {
    chat.statusNote = `Backend 설정 실패: ${error}`;
    return false;
  }
}

export async function setTargetWorkDir(target) {
  const normalized = normalizeTarget(target);
  if (normalized.kind !== "web") return false;
  try {
    const dir = await ControlService.PickFolder();
    if (!dir) return false;
    await ControlService.WebSetDir(normalized.id || "", dir);
    closePaneWorkDirMenu();
    const shown = chat.panes.some((item) => item.target?.kind === "web" && item.target.id === normalized.id);
    await loadConversations({ forceReloadCurrent: shown });
    return true;
  } catch (error) {
    chat.statusNote = `작업 폴더 변경 실패: ${error}`;
    return false;
  }
}

// ---- layout tree (leaf | split) ----

function makeSplitId() {
  return `split-${layoutSeq++}`;
}

export function collectLeafIds(node, out = []) {
  if (node.type === "leaf") {
    out.push(node.paneId);
    return out;
  }
  for (const child of node.children) collectLeafIds(child.node, out);
  return out;
}

function findSplitNode(node, splitId) {
  if (node.type === "leaf") return null;
  if (node.id === splitId) return node;
  for (const child of node.children) {
    const found = findSplitNode(child.node, splitId);
    if (found) return found;
  }
  return null;
}

function removeLeaf(node, paneId) {
  if (node.type === "leaf") {
    return node.paneId === paneId ? { node: null, removed: true } : { node, removed: false };
  }
  let removed = false;
  const nextChildren = [];
  for (const child of node.children) {
    if (removed) {
      nextChildren.push(child);
      continue;
    }
    const result = removeLeaf(child.node, paneId);
    if (!result.removed) {
      nextChildren.push(child);
      continue;
    }
    removed = true;
    if (result.node) nextChildren.push({ size: child.size, node: result.node });
  }
  if (!removed) return { node, removed: false };
  if (nextChildren.length === 0) return { node: null, removed: true };
  if (nextChildren.length === 1) return { node: nextChildren[0].node, removed: true };
  const evenSize = 100 / nextChildren.length;
  return { node: { ...node, children: nextChildren.map((c) => ({ ...c, size: evenSize })) }, removed: true };
}

function insertAtEdge(node, targetPaneId, newPaneId, edge) {
  if (node.type === "leaf") {
    if (node.paneId !== targetPaneId) return node;
    const direction = edge === "top" || edge === "bottom" ? "column" : "row";
    const newLeaf = { type: "leaf", paneId: newPaneId };
    const targetLeaf = { type: "leaf", paneId: targetPaneId };
    const children =
      edge === "top" || edge === "left"
        ? [{ size: 50, node: newLeaf }, { size: 50, node: targetLeaf }]
        : [{ size: 50, node: targetLeaf }, { size: 50, node: newLeaf }];
    return { type: "split", id: makeSplitId(), direction, children };
  }
  return {
    ...node,
    children: node.children.map((child) => ({ ...child, node: insertAtEdge(child.node, targetPaneId, newPaneId, edge) })),
  };
}

function swapLeaves(node, paneIdA, paneIdB) {
  if (node.type === "leaf") {
    if (node.paneId === paneIdA) return { ...node, paneId: paneIdB };
    if (node.paneId === paneIdB) return { ...node, paneId: paneIdA };
    return node;
  }
  return { ...node, children: node.children.map((child) => ({ ...child, node: swapLeaves(child.node, paneIdA, paneIdB) })) };
}

function resizeSplitChildren(node, splitId, index, leftSize, rightSize) {
  if (node.type === "leaf") return node;
  if (node.id === splitId) {
    return {
      ...node,
      children: node.children.map((child, i) => {
        if (i === index) return { ...child, size: leftSize };
        if (i === index + 1) return { ...child, size: rightSize };
        return child;
      }),
    };
  }
  return {
    ...node,
    children: node.children.map((child) => ({ ...child, node: resizeSplitChildren(child.node, splitId, index, leftSize, rightSize) })),
  };
}

// ---- pane lifecycle ----

export function pane(paneId) {
  return chat.panes.find((item) => item.id === paneId) || null;
}

export function focusedPane() {
  return pane(chat.focusedPaneId) || chat.panes[0] || null;
}

// Distinct per-pane accent so panes stay easy to tell apart at a glance when
// several are open side by side. Assigned by a pane's left-to-right/
// top-to-bottom position in the layout tree (not its id), so the first split
// is always the same color, the second is always the next color, etc.
const PANE_COLORS = [
  { border: "border-sky-300", headerBg: "bg-sky-50/80", dot: "bg-sky-400" },
  { border: "border-violet-300", headerBg: "bg-violet-50/80", dot: "bg-violet-400" },
  { border: "border-amber-300", headerBg: "bg-amber-50/80", dot: "bg-amber-400" },
  { border: "border-emerald-300", headerBg: "bg-emerald-50/80", dot: "bg-emerald-400" },
  { border: "border-rose-300", headerBg: "bg-rose-50/80", dot: "bg-rose-400" },
  { border: "border-cyan-300", headerBg: "bg-cyan-50/80", dot: "bg-cyan-400" },
];

export function paneColor(paneId) {
  const index = collectLeafIds(chat.layout).indexOf(paneId);
  return PANE_COLORS[(index < 0 ? 0 : index) % PANE_COLORS.length];
}

export function updatePane(paneId, patch) {
  chat.panes = chat.panes.map((item) => (item.id === paneId ? { ...item, ...patch } : item));
}

export function focusPane(paneId) {
  if (chat.focusedPaneId !== paneId) chat.focusedPaneId = paneId;
}

export function addPane() {
  if (chat.panes.length >= MAX_PANES) return;
  const id = `pane-${paneSeq++}`;
  chat.panes = [
    ...chat.panes,
    { id, target: null, attachments: [], composerText: "", commandMenuHidden: false, highlightedCommandIndex: 0 },
  ];
  const targetId = chat.focusedPaneId || chat.panes[0]?.id;
  chat.layout = insertAtEdge(chat.layout, targetId, id, "right");
  chat.focusedPaneId = id;
  persistLayout();
}

export function closePane(paneId) {
  if (chat.panes.length <= 1) return;
  const closing = pane(paneId);
  if (closing) for (const attachment of closing.attachments) revokeAttachmentPreview(attachment);
  paneTextareas.delete(paneId);
  paneLogs.delete(paneId);
  paneScrollOffsets.delete(paneId);
  const result = removeLeaf(chat.layout, paneId);
  if (result.node) chat.layout = result.node;
  chat.panes = chat.panes.filter((item) => item.id !== paneId);
  if (chat.focusedPaneId === paneId) {
    const remainingIds = collectLeafIds(chat.layout);
    chat.focusedPaneId = remainingIds[0] || chat.panes[0]?.id || "";
  }
  persistLayout();
}

export function startSplitResize(event, splitId, direction, childIndex) {
  const container = event.currentTarget.parentElement;
  if (!container) return;
  event.preventDefault();
  const splitNode = findSplitNode(chat.layout, splitId);
  if (!splitNode) return;
  const leftChild = splitNode.children[childIndex - 1];
  const rightChild = splitNode.children[childIndex];
  if (!leftChild || !rightChild) return;

  const rect = container.getBoundingClientRect();
  const containerSize = direction === "column" ? rect.height : rect.width;
  const startPos = direction === "column" ? event.clientY : event.clientX;
  const startLeftSize = leftChild.size;
  const startRightSize = rightChild.size;
  const pairTotal = startLeftSize + startRightSize;

  document.body.style.cursor = direction === "column" ? "row-resize" : "col-resize";
  document.body.style.userSelect = "none";

  function onMove(moveEvent) {
    const pos = direction === "column" ? moveEvent.clientY : moveEvent.clientX;
    const deltaPercent = containerSize > 0 ? ((pos - startPos) / containerSize) * 100 : 0;
    let nextLeft = startLeftSize + deltaPercent;
    let nextRight = startRightSize - deltaPercent;
    if (nextLeft < MIN_PANE_PERCENT) {
      nextLeft = MIN_PANE_PERCENT;
      nextRight = pairTotal - nextLeft;
    } else if (nextRight < MIN_PANE_PERCENT) {
      nextRight = MIN_PANE_PERCENT;
      nextLeft = pairTotal - nextRight;
    }
    chat.layout = resizeSplitChildren(chat.layout, splitId, childIndex - 1, nextLeft, nextRight);
  }

  function onUp() {
    window.removeEventListener("mousemove", onMove);
    window.removeEventListener("mouseup", onUp);
    document.body.style.cursor = "";
    document.body.style.userSelect = "";
    persistLayout();
  }

  window.addEventListener("mousemove", onMove);
  window.addEventListener("mouseup", onUp);
}

// ---- drag-to-dock ----

export function handlePaneDragStart(event, paneId) {
  chat.draggingPaneId = paneId;
  event.dataTransfer.effectAllowed = "move";
  event.dataTransfer.setData("text/plain", paneId);
}

export function handlePaneDragEnd() {
  chat.draggingPaneId = null;
  chat.overPaneId = null;
  chat.overZone = null;
}

export function handleConversationDragStart(event, target) {
  chat.draggingConversation = normalizeTarget(target);
  event.dataTransfer.effectAllowed = "copyMove";
  event.dataTransfer.setData("text/plain", JSON.stringify(target));
}

export function handleConversationDragEnd() {
  chat.draggingConversation = null;
  chat.overPaneId = null;
  chat.overZone = null;
  chat.dragOverGroupId = "";
}

// Container-level handlers cover the whole group card (header + body) and
// the "no group" zones. They're the fallback for group drags that land
// outside a header's precise before/after/into band (anywhere in the body
// counts as "into"), and the only handler for conversation drags (a
// conversation dropped anywhere in a group card joins that group).
export function handleGroupDragOver(event, groupId) {
  if (chat.draggingGroupId) {
    if (chat.draggingGroupId === groupId) return;
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
    if (groupId === UNGROUPED_DROP_ZONE) {
      chat.dragOverGroupId = UNGROUPED_DROP_ZONE;
    } else {
      chat.dragOverGroupReorder = groupId;
      chat.dragOverGroupZone = "into";
    }
    return;
  }
  if (chat.draggingConversation?.kind !== "web") return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
  chat.dragOverGroupId = groupId;
}

export function handleGroupDragLeave(groupId) {
  if (chat.dragOverGroupId === groupId) chat.dragOverGroupId = "";
  if (chat.dragOverGroupReorder === groupId) {
    chat.dragOverGroupReorder = null;
    chat.dragOverGroupZone = null;
  }
}

export function handleGroupDrop(event, groupId) {
  event.preventDefault();
  event.stopPropagation();
  if (chat.draggingGroupId) {
    const sourceGroupId = chat.draggingGroupId;
    const zone = chat.dragOverGroupZone;
    chat.draggingGroupId = null;
    chat.dragOverGroupReorder = null;
    chat.dragOverGroupZone = null;
    chat.dragOverGroupId = "";
    if (sourceGroupId === groupId) return;
    if (groupId === UNGROUPED_DROP_ZONE) {
      moveGroupToRoot(sourceGroupId);
      return;
    }
    if (zone === "before" || zone === "after") moveGroupRelativeTo(sourceGroupId, groupId, zone);
    else nestGroupInto(sourceGroupId, groupId);
    return;
  }
  const target = chat.draggingConversation;
  chat.dragOverGroupId = "";
  chat.draggingConversation = null;
  if (target?.kind !== "web") return;
  setConversationGroup(target.id, groupId === UNGROUPED_DROP_ZONE ? null : groupId);
}

function zoneFromEvent(event, rect) {
  const x = rect.width > 0 ? (event.clientX - rect.left) / rect.width : 0.5;
  const y = rect.height > 0 ? (event.clientY - rect.top) / rect.height : 0.5;
  if (x < 0.25) return "left";
  if (x > 0.75) return "right";
  if (y < 0.25) return "top";
  if (y > 0.75) return "bottom";
  return "center";
}

export function handlePaneDragOver(event, paneId) {
  if (!chat.draggingPaneId && !chat.draggingConversation) return;
  if (chat.draggingPaneId === paneId) return;
  event.preventDefault();
  const rect = event.currentTarget.getBoundingClientRect();
  chat.overPaneId = paneId;
  chat.overZone = zoneFromEvent(event, rect);
}

export function handlePaneDragLeave(paneId) {
  if (chat.overPaneId === paneId) {
    chat.overPaneId = null;
    chat.overZone = null;
  }
}

export function dockPane(draggedId, targetId, zone) {
  if (zone === "center") {
    chat.layout = swapLeaves(chat.layout, draggedId, targetId);
    chat.focusedPaneId = draggedId;
    persistLayout();
    return;
  }
  const afterRemoval = removeLeaf(chat.layout, draggedId);
  if (!afterRemoval.node) return;
  chat.layout = insertAtEdge(afterRemoval.node, targetId, draggedId, zone);
  chat.focusedPaneId = draggedId;
  persistLayout();
}

export function dockConversation(target, targetPaneId, zone) {
  if (!zone || zone === "center" || chat.panes.length >= MAX_PANES) {
    if (zone && zone !== "center" && chat.panes.length >= MAX_PANES) {
      chat.statusNote = `패널이 최대 ${MAX_PANES}개라 새 분할 대신 이 패널에 불러옵니다.`;
    }
    chat.focusedPaneId = targetPaneId;
    void selectTargetInPane(targetPaneId, target);
    return;
  }
  const id = `pane-${paneSeq++}`;
  chat.panes = [
    ...chat.panes,
    { id, target: null, attachments: [], composerText: "", commandMenuHidden: false, highlightedCommandIndex: 0 },
  ];
  chat.layout = insertAtEdge(chat.layout, targetPaneId, id, zone);
  chat.focusedPaneId = id;
  void selectTargetInPane(id, target);
}

export function handlePaneDrop(event, paneId) {
  event.preventDefault();
  const draggedId = chat.draggingPaneId;
  const draggedConversation = chat.draggingConversation;
  const zone = chat.overZone;
  chat.draggingPaneId = null;
  chat.draggingConversation = null;
  chat.overPaneId = null;
  chat.overZone = null;

  if (draggedConversation) {
    dockConversation(draggedConversation, paneId, zone);
    return;
  }
  if (!draggedId || draggedId === paneId || !zone) return;
  dockPane(draggedId, paneId, zone);
}

// ---- pane content helpers ----

export function paneConversationMeta(target) {
  if (!target) return null;
  if (target.kind === "telegram") {
    return {
      kind: "telegram",
      title: chat.telegram?.title || "텔레그램",
      id: "telegram",
      workDir: "",
      backend: chat.telegram?.backend || "",
    };
  }
  const conv = findWebConv(target.id);
  return {
    kind: "web",
    title: conv?.title || target.title || target.id,
    id: target.id,
    workDir: conv?.workDir || "",
    backend: conv?.backend || "",
  };
}

export function scrollPaneLogToBottom(paneId) {
  const el = paneLogs.get(paneId);
  if (!el) return;
  paneScrollOffsets.set(paneId, { top: el.scrollHeight, pinned: true });
  requestAnimationFrame(() => {
    requestAnimationFrame(() => {
      el.scrollTop = el.scrollHeight;
    });
  });
}

export async function resizePaneComposer(paneId) {
  await tick();
  const el = paneTextareas.get(paneId);
  if (!el) return;
  el.style.height = "auto";
  el.style.height = `${Math.min(el.scrollHeight, 240)}px`;
}

export function setDraft(key, value) {
  const next = new Map(chat.drafts);
  next.set(key, value);
  chat.drafts = next;
}

export function clearDraft(key) {
  const next = new Map(chat.drafts);
  next.delete(key);
  chat.drafts = next;
}

export function rememberSentHistory(key, text) {
  const item = String(text || "").trim();
  if (!item) return;
  const current = chat.sentHistoryByKey.get(key) || [];
  const next = current[current.length - 1] === item ? [...current] : [...current, item];
  const trimmed = next.slice(-100);
  chat.sentHistoryByKey = new Map(chat.sentHistoryByKey).set(key, trimmed);
  chat.historyIndexByKey = new Map(chat.historyIndexByKey).set(key, trimmed.length);
}

export async function recallSentHistoryForPane(paneId, direction, event) {
  const p = pane(paneId);
  const el = paneTextareas.get(paneId);
  if (!p?.target || !el) return false;
  const key = targetKey(p.target);
  const history = chat.sentHistoryByKey.get(key) || [];
  if (history.length === 0) return false;
  const value = el.value || "";
  const atStart = el.selectionStart === 0 && el.selectionEnd === 0;
  const atEnd = el.selectionStart === value.length && el.selectionEnd === value.length;
  if (direction < 0 && value && !atStart) return false;
  if (direction > 0 && value && !atEnd) return false;
  event.preventDefault();
  const currentIndex = chat.historyIndexByKey.get(key) ?? history.length;
  const nextIndex = Math.max(0, Math.min(history.length, currentIndex + direction));
  chat.historyIndexByKey = new Map(chat.historyIndexByKey).set(key, nextIndex);
  const nextValue = nextIndex < history.length ? history[nextIndex] : "";
  setPaneComposerValue(paneId, nextValue);
  await tick();
  el.setSelectionRange(nextValue.length, nextValue.length);
  return true;
}

export function setUnread(key, active) {
  const next = new Set(chat.unread);
  if (active) next.add(key);
  else next.delete(key);
  chat.unread = next;
}

export function startWorking(key) {
  const now = Date.now();
  const next = new Map(chat.working);
  const prev = next.get(key);
  next.set(key, {
    startedAt: prev?.startedAt || now,
    lastAliveAt: now,
  });
  chat.working = next;
}

export function stopWorking(key) {
  if (!chat.working.has(key)) return;
  const next = new Map(chat.working);
  next.delete(key);
  chat.working = next;
}

export function paneWorking(target) {
  return target ? chat.working.get(targetKey(target)) : undefined;
}

export function paneWorkingText(target) {
  const active = paneWorking(target);
  if (!active) return "";
  const elapsedSec = Math.max(0, Math.floor((chat.nowTick - active.startedAt) / 1000));
  const elapsed =
    elapsedSec < 60
      ? `${elapsedSec}초`
      : `${Math.floor(elapsedSec / 60)}분 ${elapsedSec % 60}초`;
  if (chat.nowTick - active.lastAliveAt > STALE_AFTER_MS) {
    return `작업 상태 확인이 오래되었습니다 (${elapsed})`;
  }
  return `작업 진행 중 (${elapsed})`;
}

function messageFromFrame(frame) {
  if (!frame) return null;
  if (frame.type === "text") {
    return { role: "assistant", text: frame.text || "", image: "" };
  }
  if (frame.type === "image") {
    return {
      role: "assistant",
      text: frame.caption || "",
      image: frame.data || "",
    };
  }
  if (frame.type === "user") {
    return { role: "user", text: frame.text || "", image: "" };
  }
  return null;
}

function sameMessage(a, b) {
  return (
    !!a &&
    !!b &&
    a.role === b.role &&
    (a.text || "") === (b.text || "") &&
    (a.image || "") === (b.image || "")
  );
}

export function messagesForPane(target) {
  if (!target) return [];
  return chat.messagesByKey.get(targetKey(target)) || [];
}

export function pushMessageToKey(key, message) {
  if (!message) return;
  const current = chat.messagesByKey.get(key) || [];
  const last = current[current.length - 1];
  if (sameMessage(last, message)) return;
  chat.messagesByKey = new Map(chat.messagesByKey).set(key, [...current, message]);
  for (const item of chat.panes) {
    if (item.target && targetKey(item.target) === key) scrollPaneLogToBottom(item.id);
  }
}

function appendLiveFrameToKey(key, frame) {
  const message = messageFromFrame(frame);
  if (message) pushMessageToKey(key, message);
}

export function bufferFrame(key, frame) {
  if (!chat.loadingBuffers.has(key)) return false;
  const next = new Map(chat.loadingBuffers);
  const pending = [...(next.get(key) || []), frame];
  next.set(key, pending);
  chat.loadingBuffers = next;
  return true;
}

export function flushBufferedFrames(key, messages) {
  const frames = chat.loadingBuffers.get(key) || [];
  const nextMessages = [...messages];
  for (const frame of frames) {
    const message = messageFromFrame(frame);
    const last = nextMessages[nextMessages.length - 1];
    if (message && !sameMessage(last, message)) nextMessages.push(message);
  }
  const nextBuffers = new Map(chat.loadingBuffers);
  nextBuffers.delete(key);
  chat.loadingBuffers = nextBuffers;
  return nextMessages;
}

export function applyDraftToPane(paneId, key) {
  updatePane(paneId, { composerText: chat.drafts.get(key) || "" });
  resizePaneComposer(paneId);
}

export async function selectTargetInPane(paneId, rawTarget) {
  const normalized = normalizeTarget(rawTarget);
  const key = targetKey(normalized);
  clearPaneAttachments(paneId);
  updatePane(paneId, { target: { ...normalized }, commandMenuHidden: false, highlightedCommandIndex: 0 });
  setUnread(key, false);
  chat.statusNote = "";
  applyDraftToPane(paneId, key);
  persistLayout();

  const nextBuffers = new Map(chat.loadingBuffers);
  nextBuffers.set(key, []);
  chat.loadingBuffers = nextBuffers;
  chat.messagesByKey = new Map(chat.messagesByKey).set(key, []);

  try {
    const raw = await ControlService.GetHistory(normalized.kind, normalized.id || "");
    const data = parseJSON(raw, { turns: [] });
    const history = Array.isArray(data.turns)
      ? data.turns.map((turn) => ({
          role: turn.role || "assistant",
          text: turn.text || "",
          image: turn.image || turn.data || "",
        }))
      : [];
    const flushed = flushBufferedFrames(key, history);
    chat.messagesByKey = new Map(chat.messagesByKey).set(key, flushed);
    await tick();
    scrollPaneLogToBottom(paneId);
  } catch (error) {
    const nextLoading = new Map(chat.loadingBuffers);
    nextLoading.delete(key);
    chat.loadingBuffers = nextLoading;
    chat.messagesByKey = new Map(chat.messagesByKey).set(key, [{ role: "system", text: `히스토리를 불러오지 못했습니다: ${error}` }]);
  }
}

export function selectTargetFromSidebar(target) {
  const active = focusedPane();
  if (!active) return;
  void selectTargetInPane(active.id, target);
}

export function isFocusedTarget(target) {
  const active = focusedPane();
  if (!active?.target) return false;
  return targetKey(active.target) === targetKey(normalizeTarget(target));
}

export function isTargetVisibleAnywhere(key) {
  return chat.panes.some((item) => item.target && targetKey(item.target) === key);
}

export function chooseFallbackTarget(data) {
  const activeWeb = (data.webConvs || []).find((conv) => conv.active);
  if (activeWeb) return { kind: "web", id: activeWeb.id };
  if ((data.webConvs || []).length > 0) return { kind: "web", id: data.webConvs[0].id };
  if (data.telegram) return { kind: "telegram" };
  return null;
}

function syncPaneTarget(target, data) {
  if (target.kind === "telegram") {
    return data.telegram ? target : chooseFallbackTarget(data);
  }
  const existing = (data.webConvs || []).find((conv) => conv.id === target.id);
  if (!existing) return chooseFallbackTarget(data);
  return { kind: "web", id: existing.id };
}

export async function loadConversations(options = {}) {
  try {
    const raw = await ControlService.ListConversations();
    const data = parseJSON(raw, {});
    chat.statusNote = "";
    chat.telegram = data.telegram || null;
    chat.webConvs = Array.isArray(data.webConvs) ? data.webConvs : [];

    for (const item of chat.panes) {
      if (!item.target) {
        if (chat.panes.length === 1) {
          const fallback = chooseFallbackTarget(data);
          if (fallback) await selectTargetInPane(item.id, fallback);
        }
        continue;
      }
      const nextTarget = syncPaneTarget(item.target, data);
      if (!nextTarget) {
        updatePane(item.id, { target: null });
        continue;
      }
      const nextKey = targetKey(nextTarget);
      if (options.forceReloadCurrent || nextKey !== targetKey(item.target) || !chat.messagesByKey.has(nextKey)) {
        await selectTargetInPane(item.id, nextTarget);
      } else {
        updatePane(item.id, { target: { ...item.target, ...nextTarget } });
      }
    }
  } catch (error) {
    chat.statusNote = `대화 목록을 불러오지 못했습니다: ${error}`;
  }
}

export function setPaneComposerValue(paneId, value) {
  const p = pane(paneId);
  updatePane(paneId, { composerText: value, commandMenuHidden: false, highlightedCommandIndex: 0 });
  if (p?.target) setDraft(targetKey(p.target), value);
  resizePaneComposer(paneId);
}

export async function selectCommandForPane(paneId, command) {
  if (!command) return;
  setPaneComposerValue(paneId, `${command.cmd} `);
  await tick();
  const el = paneTextareas.get(paneId);
  el?.focus();
  el?.setSelectionRange(el.value.length, el.value.length);
}

export function clearPaneComposer(paneId) {
  const p = pane(paneId);
  updatePane(paneId, { composerText: "" });
  if (p?.target) clearDraft(targetKey(p.target));
  resizePaneComposer(paneId);
}

export function canSendPane(p) {
  return !!p.target && !!p.composerText.trim() && chat.connected;
}

export function commandCandidatesForPane(p) {
  const text = p.composerText;
  if (!text.startsWith("!") || /\s/.test(text)) return [];
  const query = text.slice(1).toLowerCase();
  return SLASH_COMMANDS.filter((command) => command.cmd.slice(1).toLowerCase().startsWith(query));
}

export function showCommandMenuForPane(p) {
  return commandCandidatesForPane(p).length > 0 && !p.commandMenuHidden;
}

export async function sendFromPane(paneId) {
  const p = pane(paneId);
  if (!p?.target) return;
  const key = targetKey(p.target);
  const caption = p.composerText.trim();

  if (p.attachments.length > 0) {
    const pendingAttachments = p.attachments;
    const attachmentNames = pendingAttachments.map((attachment) => attachment.name).join(", ");
    pushMessageToKey(key, {
      role: "user",
      text: caption ? `[첨부] ${attachmentNames}\n${caption}` : `[첨부] ${attachmentNames}`,
      image: "",
    });
    startWorking(key);
    clearPaneComposer(paneId);
    clearPaneAttachments(paneId);
    try {
      for (const attachment of pendingAttachments) {
        await ControlService.UploadAttachment(attachment.path, caption, p.target.kind, p.target.id || "");
      }
    } catch (error) {
      pushMessageToKey(key, { role: "system", text: `첨부 업로드 실패: ${error}` });
      stopWorking(key);
    }
    return;
  }

  if (!canSendPane(p)) return;
  pushMessageToKey(key, { role: "user", text: caption, image: "" });
  startWorking(key);
  rememberSentHistory(key, caption);
  clearPaneComposer(paneId);
  try {
    await ControlService.SendText(caption, p.target.kind, p.target.id || "");
    void loadConversations();
  } catch (error) {
    pushMessageToKey(key, { role: "system", text: `전송 실패: ${error}` });
    stopWorking(key);
  }
}

export async function pickAttachment(paneId) {
  try {
    const sourcePath = await ControlService.PickFile();
    if (!sourcePath) return;
    const name = sourcePath.split(/[/\\]/).pop() || sourcePath;
    let previewURL = "";
    try {
      previewURL = await ControlService.PreviewAttachmentImage(sourcePath);
    } catch {
      previewURL = "";
    }
    const path = await ControlService.StageAttachment(sourcePath);
    appendPaneAttachment(paneId, { path, name, previewURL });
  } catch (error) {
    chat.statusNote = `첨부 파일 선택 실패: ${error}`;
  }
}

export function revokeAttachmentPreview(attachment) {
  if (attachment?.previewIsObjectURL && attachment.previewURL) {
    URL.revokeObjectURL(attachment.previewURL);
  }
}

export function appendPaneAttachment(paneId, { path, name, previewURL = "", previewIsObjectURL = false }) {
  const p = pane(paneId);
  if (!p) return;
  updatePane(paneId, {
    attachments: [
      ...p.attachments,
      { id: `${Date.now()}-${attachmentSeq++}`, path, name, previewURL, previewIsObjectURL },
    ],
  });
}

export function clearPaneAttachment(paneId, index) {
  const p = pane(paneId);
  if (!p) return;
  const attachment = p.attachments[index];
  revokeAttachmentPreview(attachment);
  updatePane(paneId, { attachments: p.attachments.filter((_, itemIndex) => itemIndex !== index) });
}

export function clearPaneAttachments(paneId) {
  const p = pane(paneId);
  if (!p) return;
  for (const attachment of p.attachments) revokeAttachmentPreview(attachment);
  updatePane(paneId, { attachments: [] });
}

function clipboardImageExtension(mimeType) {
  const normalized = String(mimeType || "").toLowerCase();
  if (normalized === "image/jpeg" || normalized === "image/jpg") return "jpg";
  if (normalized === "image/png") return "png";
  if (normalized === "image/gif") return "gif";
  if (normalized === "image/webp") return "webp";
  if (normalized === "image/bmp") return "bmp";
  if (normalized === "image/svg+xml") return "svg";
  return "png";
}

function pastedImageName(file) {
  if (file?.name) return file.name;
  return `pasted-image.${clipboardImageExtension(file?.type)}`;
}

function imageFilesFromClipboard(event) {
  const items = event.clipboardData?.items || [];
  const files = [];
  for (const item of items) {
    if (item.type && item.type.indexOf("image/") === 0) {
      const file = item.getAsFile();
      if (file) files.push(file);
    }
  }
  return files;
}

function readFileAsDataURL(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || ""));
    reader.onerror = () => reject(reader.error || new Error("failed to read clipboard image"));
    reader.readAsDataURL(file);
  });
}

export async function handleComposerPaste(paneId, event) {
  const p = pane(paneId);
  if (!p?.target) return;
  const files = imageFilesFromClipboard(event);
  if (files.length === 0) return;

  event.preventDefault();
  try {
    for (const file of files) {
      const dataURL = await readFileAsDataURL(file);
      const path = await ControlService.SaveClipboardImage(dataURL);
      appendPaneAttachment(paneId, {
        path,
        name: pastedImageName(file),
        previewURL: URL.createObjectURL(file),
        previewIsObjectURL: true,
      });
    }
    chat.statusNote = "";
  } catch (error) {
    chat.statusNote = `Clipboard image paste failed: ${error}`;
  }
}

export function handleComposerKeydown(paneId, event) {
  const p = pane(paneId);
  if (!p) return;
  const candidates = commandCandidatesForPane(p);
  if (candidates.length > 0 && !p.commandMenuHidden) {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      updatePane(paneId, { highlightedCommandIndex: Math.min(p.highlightedCommandIndex + 1, candidates.length - 1) });
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      updatePane(paneId, { highlightedCommandIndex: Math.max(p.highlightedCommandIndex - 1, 0) });
      return;
    }
    if (event.key === "Tab" || event.key === "Enter") {
      event.preventDefault();
      void selectCommandForPane(paneId, candidates[p.highlightedCommandIndex] || candidates[0]);
      return;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      updatePane(paneId, { commandMenuHidden: true });
      return;
    }
  }
  if (event.key === "ArrowUp") {
    void recallSentHistoryForPane(paneId, -1, event);
    return;
  }
  if (event.key === "ArrowDown") {
    void recallSentHistoryForPane(paneId, 1, event);
    return;
  }
  if (event.key === "Enter" && !event.shiftKey) {
    event.preventDefault();
    void sendFromPane(paneId);
  }
}

export function progressForTarget(target) {
  if (!target) return [];
  return chat.progressByKey.get(targetKey(target)) || [];
}

export function toggleProgressPopup(paneId) {
  chat.progressPopupPaneId = chat.progressPopupPaneId === paneId ? null : paneId;
}

export function closeProgressPopup() {
  chat.progressPopupPaneId = null;
}

function appendProgress(key, text) {
  if (!text) return;
  const current = chat.progressByKey.get(key) || [];
  const trimmed = current.length >= MAX_PROGRESS_LINES ? current.slice(current.length - MAX_PROGRESS_LINES + 1) : current;
  chat.progressByKey = new Map(chat.progressByKey).set(key, [...trimmed, text]);
}

export function handleFrame(frame) {
  const target = frameTarget(frame);
  const key = targetKey(target);

  if (frame.type === "typing") {
    startWorking(key);
    chat.progressByKey = new Map(chat.progressByKey).set(key, []);
  }
  if (frame.type === "done") stopWorking(key);
  if (frame.type === "progress") {
    appendProgress(key, frame.text || "");
    return;
  }

  if (!isTargetVisibleAnywhere(key)) {
    if (frame.type === "text" || frame.type === "image" || frame.type === "user") {
      setUnread(key, true);
    }
    return;
  }

  if (bufferFrame(key, frame)) return;
  appendLiveFrameToKey(key, frame);
}
