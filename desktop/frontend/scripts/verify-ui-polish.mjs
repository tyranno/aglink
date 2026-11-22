import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..");
const app = readFileSync(join(root, "src", "App.svelte"), "utf8");
const pane = readFileSync(join(root, "src", "PaneNode.svelte"), "utf8");
const group = readFileSync(join(root, "src", "GroupNode.svelte"), "utf8");
const store = readFileSync(join(root, "src", "paneStore.svelte.js"), "utf8");
const css = readFileSync(join(root, "src", "app.css"), "utf8");
const html = readFileSync(join(root, "index.html"), "utf8");
const all = `${app}\n${pane}\n${group}\n${store}`;

function assert(condition, message) {
  if (!condition) {
    console.error(message);
    process.exitCode = 1;
  }
}

const header = app.match(/<header[\s\S]*?<\/header>/)?.[0] || "";
const chatLog = pane.match(/<div use:registerPaneLog[\s\S]*?{#if paneWorking\(p\.target\)/)?.[0] || "";
const composer = pane.match(/<div class="shrink-0 border-t[\s\S]*?<\/main>/)?.[0] || "";

assert(!all.includes("Wails 3 + Svelte 5"), "desktop shell must not expose framework text");
assert(!app.includes("w-screen") && !app.includes("h-screen"), "root shell must not use viewport classes that create Wails scrollbars");
assert(/body[\s\S]*overflow:\s*hidden/.test(css), "body must hide document-level overflow");
assert(!header.match(/>\s*설정\s*</), "header settings action must be an icon button");
assert(!header.match(/>\s*연결 \/ aglink\s*</), "header connection action must be an icon button");
assert(!all.match(/>\s*관리\s*</), "conversation menu trigger must be an icon button");
assert(!all.match(/>\s*파일 첨부\s*</), "attachment trigger must be an icon button");
assert(store.includes("sentHistoryByKey: new Map()") && store.includes("historyIndexByKey: new Map()"), "sent-message recall history must be keyed per conversation, not one shared global list");
assert(store.includes("export function rememberSentHistory(key, text)"), "remembering a sent message must record it under that conversation's own key");
assert(!store.includes("let sentHistory") && !store.includes("let historyIndex"), "there must be no single global sentHistory/historyIndex shared across every conversation");
assert(store.includes('event.key === "ArrowUp"'), "composer must recall sent history on ArrowUp");
assert(store.includes("function handleComposerPaste"), "composer must handle pasted clipboard images");
assert(pane.includes("onpaste={(event) => handlePaste(p, event)}") && pane.includes("handleComposerPaste(p.id, event)"), "composer textarea must wire the clipboard paste handler (long text is offered as a .txt attachment first)");
assert(store.includes("ControlService.SaveClipboardImage"), "clipboard image paste must persist the image through the desktop service");
assert(store.includes("ControlService.StageAttachment"), "picked files must be staged into aglink attachments before upload");
assert(store.includes("attachments: []"), "each pane must keep its own multiple attachments");
assert(!all.includes("let attachedFilePath"), "composer must not keep only one global attachment path");
assert(store.includes("function imageFilesFromClipboard"), "clipboard paste must collect every pasted image item");
assert(store.includes("function appendPaneAttachment"), "attachment state must append rather than replace");
assert(store.includes("function clearPaneAttachment(paneId, index)"), "each attachment must be individually removable");
assert(store.includes("for (const attachment of pendingAttachments)"), "send must upload queued attachments sequentially");
assert(pane.includes("attachment.previewURL"), "attachment UI must keep an image thumbnail preview URL per item");
assert(store.includes("URL.createObjectURL"), "pasted image attachments must use object URLs for thumbnail preview");
assert(store.includes("URL.revokeObjectURL"), "attachment thumbnail object URLs must be revoked when replaced or cleared");
assert(store.includes("ControlService.PreviewAttachmentImage"), "picked local images must get a desktop preview data URL");
assert(all.includes("SetChannelBackend"), "channel backend setting must be wired to ControlService");
assert(store.includes('UploadAttachment(attachment.path, caption, p.target.kind, p.target.id || "")'), "attachment uploads must preserve the selected conversation target");
assert(!all.includes("기본 텔레그램 스트림"), "sidebar conversation rows must stay single-line");
assert(!all.includes("rounded-[28px]") && !all.includes("rounded-3xl"), "desktop cards must use compact radii");
assert(chatLog.includes("flex-1") && chatLog.includes("overflow-y-auto"), "chat log must be a flexible scrolling region");
assert(!chatLog.includes("max-w-5xl"), "chat message area must expand with the desktop window");
assert(!chatLog.includes("78ch"), "message bubbles must not keep the old narrow fixed reading width");
assert(chatLog.includes("px-3 py-1.5"), "message bubbles must use tighter vertical padding");
assert(chatLog.includes("msg-text") && pane.includes("calc(13px * var(--msg-scale, 1))"), "message text must use the 13px base scaled by the user text size (Ctrl+= / Ctrl+-)");
assert(chatLog.includes("mt-2"), "message image spacing must stay compact");
assert(!composer.includes("max-w-5xl"), "composer and attachment rows must expand with the chat area");
assert(composer.includes("attachments") && composer.includes("attachment.previewURL") && composer.includes("<img"), "attachment thumbnails must render inline in the composer");
assert(!composer.includes("mb-3 flex w-full items-center gap-2 rounded-lg border border-emerald-200"), "attachment UI must not be a separate full-width banner");
assert(!html.includes("/style.css"), "Wails starter CSS must not be loaded");
assert(!html.includes("Wails + Svelte"), "window title must not expose framework text");
assert(!html.includes("wails.png"), "favicon must not use the Wails starter icon");
assert(html.includes("<title>aglink</title>"), "HTML title must be aglink");
assert(!all.includes(">aglink-chat<"), "desktop visible app title must be aglink, not aglink-chat");
assert(!all.includes(">teleclaude<"), "no visible text may still say teleclaude");
assert(store.includes("const SLASH_COMMANDS"), "composer must define the known !command list for autocomplete");
assert(store.includes("function showCommandMenuForPane"), "composer must derive whether the command menu is visible per pane");
assert(composer.includes("bottom-full") && composer.includes("role=\"listbox\""), "command menu must render as a dropdown above the composer input");
assert(store.includes("function selectCommandForPane"), "command menu must support selecting a command");
assert(store.includes('event.key === "Escape"') && store.includes("commandMenuHidden: true"), "command menu must be dismissible with Escape");
assert(store.includes("panes: [") && store.includes("export const chat = $state("), "pane state must live in the shared reactive store");
assert(store.includes("export function addPane") && store.includes("export function closePane"), "app must support adding and closing split panes");
assert(store.includes("export const MAX_PANES"), "split panes must be capped to a sane maximum");
// The collapsed state now starts from the persisted sidebar prefs.
assert(/let sidebarCollapsed = \$state\(/.test(app), "conversation list sidebar must be collapsible");
assert(!header.includes("sidebarCollapsed"), "sidebar visibility toggle must live in the sidebar itself, not the top header");
assert(app.match(/onclick={\(\) => \(sidebarCollapsed = true\)}/), "sidebar toolbar must expose its own collapse control");
assert(app.match(/onclick={\(\) => \(sidebarCollapsed = false\)}/), "a collapsed-sidebar handle must remain in the conversation list area to re-expand it");
assert(!all.includes("let currentTarget"), "chat state must be per-pane, not a single global target");
assert(!all.includes("let splitDirection"), "layout direction must be per-split (docking tree), not one global toggle");

// Docking window structure: recursive split tree instead of a flat pane row.
assert(app.includes('import PaneNode from "./PaneNode.svelte"'), "App shell must delegate pane layout to the recursive PaneNode component");
assert(pane.includes('import PaneNode from "./PaneNode.svelte"'), "PaneNode must recurse into itself to render nested splits");
assert(store.includes("type: \"leaf\"") && store.includes("type: \"split\""), "layout must be a tree of leaf/split nodes, not a flat array");
assert(store.includes("function insertAtEdge"), "docking must support inserting a pane at an edge of another pane");
assert(store.includes("function removeLeaf"), "docking must support removing a pane from the tree and collapsing empty splits");
assert(store.includes("function swapLeaves"), "dropping on the center of a pane must swap the two panes");
assert(store.includes("export function startSplitResize"), "each split node must support independent drag-to-resize of its own children");
assert(store.includes("export const MIN_PANE_PERCENT"), "pane resize must enforce a minimum size so a pane cannot be dragged to zero");
assert(pane.includes("split-resizer-hitbox"), "split resize handles must keep a larger hitbox than the visible 1px divider");
assert(pane.includes('aria-label={node.direction === "column" ? "상하 창 크기 조절" : "좌우 창 크기 조절"}'), "split resize handles must expose the correct vertical/horizontal resize affordance");
assert(pane.includes("startSplitResize(event, node.id, node.direction, index)"), "split resize handles must start resizing with the split direction");
assert(pane.includes('draggable="true"') && pane.includes("ondragstart={(event) => handlePaneDragStart(event, p.id)}"), "a pane's header must be draggable to redock it");
assert(pane.includes("ondragover={(event) => handlePaneDragOver(event, p.id)}") && pane.includes("ondrop={(event) => handlePaneDrop(event, p.id)}"), "a pane must accept drops to dock another pane against one of its edges");
assert(store.includes('if (x < 0.25) return "left"'), "drop targeting must resolve which edge (or center) the cursor is over");
assert(pane.includes("chat.overPaneId === p.id"), "dragging over a pane must show a drop-zone indicator overlay");

// Dragging a conversation straight out of the sidebar list to dock it into a pane.
assert(store.includes("export function handleConversationDragStart"), "sidebar conversations must be draggable, not just existing panes");
assert(store.includes("export function dockConversation"), "dropping a dragged conversation must open it in a new or existing pane depending on the drop zone");
assert(app.includes("ondragstart={(event) => handleConversationDragStart(event, { kind: \"telegram\" })}"), "the telegram sidebar row must be draggable");
assert(app.includes("ondragstart={(event) => handleConversationDragStart(event, { kind: \"web\", id: conv.id })}"), "each web conversation sidebar row must be draggable");
assert(store.includes("chat.draggingConversation") && store.includes("if (draggedConversation)"), "pane drop handling must branch between redocking an existing pane and opening a dragged conversation");

// Sending must not wait for the previous turn on that conversation to finish.
assert(
  store.includes("export function canSendPane(p) {\n  return !!p.target && !!p.composerText.trim() && chat.connected;\n}"),
  "composer must allow sending a follow-up message while the conversation is still working, not gate on paneWorking",
);

// Web channel groups: client-side organization layer, aglink's own
// conversation management (create/rename/move/delete) is untouched.
assert(store.includes("webGroups: []") && store.includes("webGroupOf: new Map()"), "chat state must track web channel groups and per-conversation group assignment");
assert(store.includes("export function createWebGroup"), "must support creating a new web channel group");
assert(store.includes("export function renameWebGroup"), "must support renaming a web channel group");
assert(store.includes("export function deleteWebGroup"), "deleting a group must not delete its conversations, only ungroup them");
assert(store.includes("export function toggleWebGroupCollapsed"), "groups must support expand/collapse");
assert(store.includes("export function setConversationGroup"), "a conversation must be assignable to a group (or ungrouped)");
assert(store.includes("localStorage.setItem(\n      WEB_GROUPS_STORAGE_KEY"), "group assignments must persist locally across restarts");
{
  const groupFns = store.match(/export function (?:createWebGroup|renameWebGroup|deleteWebGroup|setConversationGroup|toggleWebGroupCollapsed)\([^)]*\) \{[\s\S]*?\n\}/g) || [];
  assert(groupFns.length === 5, "expected to find all 5 web-group management functions");
  assert(groupFns.every((fn) => !fn.includes("ControlService")), "web grouping must stay client-side and not call aglink's own channel management API");
}
assert(app.includes("{#snippet webConvRow(conv)}") && app.includes("{@render webConvRow(conv)}"), "sidebar must render conversation rows through a shared snippet so grouped and ungrouped lists stay in sync");
assert(group.includes("toggleWebGroupCollapsed(group.id)"), "each group row must be collapsible");
assert(app.includes("onclick={newWebGroup}"), "sidebar must expose a way to create a new web channel group");
assert(app.includes("moveConversationToNewGroup") && app.includes("setConversationGroup(conv.id, group.id)"), "a conversation's management menu must support moving it into a group");

// Dragging a conversation into/out of a web channel group directly, not just via the menu.
assert(store.includes("export function handleGroupDragOver"), "group headers must accept a dragover from a conversation being dragged");
assert(store.includes("export function handleGroupDrop"), "dropping a conversation on a group must assign it there");
assert(store.includes("export const UNGROUPED_DROP_ZONE"), "there must be a sentinel drop zone for removing a conversation from its group");
assert(group.includes("ondrop={(event) => handleGroupDrop(event, group.id)}"), "each group row must be a drop target for conversations");
assert(app.includes("ondrop={(event) => handleGroupDrop(event, UNGROUPED_DROP_ZONE)}"), "the web channel header must be a drop target for removing a conversation from its group");
assert(store.includes("if (target?.kind !== \"web\") return"), "only web conversations (not telegram) should be droppable into a group");

// Pane/split layout (which conversations are open, arrangement, sizes) must
// persist locally so relaunching the desktop restores the same layout.
assert(store.includes("const LAYOUT_STORAGE_KEY"), "pane layout must have a dedicated local storage key");
assert(store.includes("export function persistLayout"), "must be able to persist the current pane/layout state");
assert(store.includes("function loadLayoutFromStorage"), "must be able to restore pane/layout state on startup");
assert(store.includes("loadLayoutFromStorage();"), "layout must actually be restored at module init, not just definable");
{
  const mutators = ["addPane", "closePane", "dockPane", "selectTargetInPane"].map((name) => {
    const match = store.match(new RegExp(`export (?:async )?function ${name}\\([^)]*\\) \\{[\\s\\S]*?\\n\\}`));
    return { name, body: match?.[0] || "" };
  });
  for (const { name, body } of mutators) {
    assert(body.includes("persistLayout()"), `${name} must persist the layout after changing it`);
  }
}
assert(store.includes("persistLayout();\n  }\n\n  window.addEventListener"), "finishing a pane resize drag must persist the new sizes");
assert(store.includes('!chat.messagesByKey.has(nextKey)'), "restored panes must fetch history on first load, not just on target changes");

// Clicking the "작업 진행 중" indicator must open a small anchored popover (not a
// full-screen modal) with live CLI progress lines, fed by a new "progress"
// control-API frame type (aglink backend).
assert(store.includes("progressByKey: new Map()") && store.includes("progressPopupPaneId: null"), "chat state must track progress lines per conversation and which pane's popover is open");
assert(store.includes('if (frame.type === "progress")'), "handleFrame must recognize the new progress frame type");
assert(store.includes("export function toggleProgressPopup") && store.includes("export function closeProgressPopup"), "must be able to open/close the progress popover");
assert(store.includes("const MAX_PROGRESS_LINES"), "progress buffer must be capped so a long turn cannot grow it unbounded");
assert(pane.includes("onclick={() => toggleProgressPopup(p.id)}"), "the working indicator must be clickable to toggle the progress popover");
assert(pane.includes("data-progress-popup") && pane.includes("absolute bottom-full left-4 right-4 mb-2"), "the progress popover must be anchored just above the working indicator, not a full-screen modal");
assert(!app.includes("진행 메시지"), "the progress popover must live in PaneNode (anchored per-pane), not as a centered App-level modal");
assert(app.includes('if (!target.closest("[data-progress-popup]")) closeProgressPopup();'), "clicking outside the popover must close it");
assert(app.includes("if (chat.progressPopupPaneId) closeProgressPopup();"), "Escape must close the progress popover like the other modals");

// The backend badge in each pane header must be directly actionable, mirroring
// the sidebar channel backend menu without forcing the user to leave the pane.
assert(store.includes("backendMenuPaneId: null"), "chat state must track which pane header backend menu is open");
assert(store.includes("export async function setTargetBackend"), "pane headers must share a store-level backend setter");
assert(store.includes("ControlService.SetChannelBackend"), "the shared backend setter must call the desktop control service");
assert(store.includes("export function togglePaneBackendMenu") && store.includes("export function closePaneBackendMenu"), "pane header backend menus must support toggle and click-away close");
assert(pane.includes("data-pane-backend-menu"), "pane headers must wrap the backend badge/menu in a click-away scope");
assert(pane.includes("togglePaneBackendMenu(p.id)") && pane.includes("setTargetBackend(p.target, choice.id)") && /BACKEND_CHOICES = \[[\s\S]*?"claude"[\s\S]*?"codex"/.test(pane), "pane backend badge must expose Claude and Codex choices");
assert(pane.includes("aria-checked={currentBackend === choice.id}") && app.includes("aria-checked={backendChoice(conv.backend) === choice.id}") && app.includes("aria-checked={backendChoice(chat.telegram.backend) === choice.id}"), "backend menus must mark the current choice");
assert(pane.includes("event.preventDefault(); event.stopPropagation(); togglePaneBackendMenu(p.id);"), "pane backend badge must open from mousedown so the draggable header cannot swallow the click");
assert(pane.includes("relative z-30 flex h-10"), "pane headers must stack above message logs so the backend menu is visible");
assert(app.includes('if (!target.closest("[data-pane-backend-menu]")) closePaneBackendMenu();'), "clicking outside a pane backend menu must close it");
assert(app.includes("if (chat.backendMenuPaneId) closePaneBackendMenu();"), "Escape must close the pane backend menu");

// The work-directory chip in each local pane header must be directly
// inspectable and configurable, using the same picker/backend path as the
// sidebar conversation menu.
assert(store.includes("workDirMenuPaneId: null"), "chat state must track which pane header work-directory menu is open");
assert(store.includes("export async function setTargetWorkDir"), "pane headers must share a store-level work-directory setter");
assert(store.includes("ControlService.PickFolder") && store.includes("ControlService.WebSetDir"), "the shared work-directory setter must call the folder picker and web_setdir control path");
assert(store.includes('if (normalized.kind !== "web")'), "work-directory changes must only apply to local/web conversations");
assert(store.includes("export function togglePaneWorkDirMenu") && store.includes("export function closePaneWorkDirMenu"), "pane header work-directory menus must support toggle and click-away close");
assert(pane.includes("data-pane-workdir-menu"), "pane headers must wrap the work-directory chip/menu in a click-away scope");
assert(pane.includes("togglePaneWorkDirMenu(p.id)") && pane.includes("setTargetWorkDir(p.target)"), "pane work-directory chip must expose a picker action");
assert(pane.includes("event.preventDefault(); event.stopPropagation(); togglePaneWorkDirMenu(p.id);"), "pane work-directory chip must open from mousedown so the draggable header cannot swallow the click");
assert(app.includes('if (!target.closest("[data-pane-workdir-menu]")) closePaneWorkDirMenu();'), "clicking outside a pane work-directory menu must close it");
assert(app.includes("if (chat.workDirMenuPaneId) closePaneWorkDirMenu();"), "Escape must close the pane work-directory menu");

// Saving structured settings (e.g. changing the default backend) must refresh
// the header badge instead of leaving it stuck at whatever backend was active
// when the app launched.
// The host applies a saved config before replying, so the refetch now runs
// right after the reply instead of after a 500ms debounce.
assert(/loadSettingsSchema\(\), loadVersionInfo\(\)/.test(app), "saving settings must refetch version/backend info after the host applies them so the header badge updates without a restart");

// Assistant/system replies are Markdown (headings, **bold**, lists, code
// fences, links) and must render as such instead of showing literal syntax;
// user bubbles stay verbatim.
assert(pane.includes('import { renderMarkdown } from "./markdown.js"'), "PaneNode must use the shared markdown renderer for message bubbles");
assert(pane.includes("function renderMarkdownInto(") && pane.includes("use:renderMarkdownInto={message.text}"), "non-user message bubbles must be rendered through the markdown action");
assert(pane.match(/\{#if message\.role === "user"\}[\s\S]{0,200}msg-text whitespace-pre-wrap break-words[\s\S]{0,200}\{:else\}[\s\S]{0,200}markdown-body/), "user messages must stay plain text while other roles get markdown");
assert(pane.includes(":global(.markdown-body pre)") && pane.includes(":global(.markdown-body code)"), "markdown code blocks/inline code must have their own styling, not fall back to plain paragraph text");
assert(pane.includes(":global(.markdown-body table)") && pane.includes(":global(.markdown-body th)") && pane.includes(":global(.markdown-body td)"), "markdown tables must have readable table/cell styling");

// "웹 채널" was renamed to "로컬 채널" throughout (텔레그램 채널 stays as-is), and a
// group's own name must read at least as prominently as a channel name, not
// smaller/muted, so groups and channels are easy to tell apart at a glance.
assert(app.includes(">로컬 채널<"), "the local channel section label must use the new terminology");
assert(!all.includes("웹 채널") && !all.includes("웹 대화"), "the old 웹 채널/웹 대화 terminology must not remain anywhere");
assert(/text-sm font-bold text-slate-800(?: dark:text-slate-\d+)?">\{group\.name\}/.test(group),"a group's name must be at least as visually prominent as a channel name (text-sm, not smaller/muted)");

// Groups can nest under other groups (folder-like tree), and both conversations
// and whole groups can be dragged to become children of a group.
assert(group.includes('import GroupNode from "./GroupNode.svelte"'), "GroupNode must recurse into itself to render nested subgroups");
assert(store.includes("parentId: null") && store.includes("export function rootWebGroups"), "groups must track a parentId and expose a root-level accessor");
assert(store.includes("export function childWebGroups"), "must be able to list a group's direct subgroups");
assert(store.includes("export function nestGroupInto"), "must support making a group a child of another group");
assert(store.includes("export function moveGroupRelativeTo"), "must support reordering a group as a sibling before/after another");
assert(store.includes("export function moveGroupToRoot"), "must support dragging a nested group back out to the top level");
assert(store.includes("function isSameOrDescendantGroup") && store.includes("function canReparentGroup"), "reparenting must refuse to create a group/subgroup cycle");
assert(app.includes("rootWebGroups()") && app.includes("<GroupNode"), "the sidebar must render only root groups directly, delegating subgroups to GroupNode");
assert(group.includes("childWebGroups(group.id)") && group.includes("<GroupNode group={child}"), "each group must render its own subgroups recursively");

// Model fields (manager/worker/codex worker/codex manager) come from aglink
// as a detected list ("select") or plain text as a fallback — never a
// free-typed field where a typo silently breaks the backend. The select must
// still show a legacy/custom value that isn't in the detected list, instead of
// silently defaulting the picker to the wrong option.
assert(app.includes("function selectOptionsFor(field)"), "settings select fields must be able to surface a legacy value not in the detected options list");
assert(app.includes("selectOptionsFor(field)") && app.includes('option === "" ? "기본값"'), "the settings select must render options through selectOptionsFor and label the blank option as 기본값");

// Voice-transcribed user messages get a compact inline mic badge, not an extra line.
assert(!pane.includes("인식했습니다:"), "voice messages must not render a separate '인식했습니다' line");
assert(pane.includes("{#snippet voiceBadge()}") && pane.includes('aria-label="음성 입력"') && pane.includes("{#if message.voice}{@render voiceBadge()}{/if}{message.text}"), "voice messages must show an inline 음성 입력 badge before the text");

// Less frequent header actions live in a "⋯ 더보기" menu; their accessible names are unchanged.
assert(header.includes('aria-label="더보기"') && header.includes('aria-haspopup="menu"') && header.includes("aria-expanded={moreMenuOpen}"), "header must expose an accessible 더보기 menu trigger");
assert(header.includes('role="menu"') && (header.match(/role="menuitem"/g) || []).length >= 3, "더보기 menu must hold (at least) the meeting / meeting list / theme items");
assert(header.includes('aria-label={voice.meeting ? "회의 모드 끝내기" : "회의 모드 시작"}') && header.includes('aria-label="회의 목록"') && header.includes('aria-label="테마 전환"'), "moved header actions must keep their aria-labels");
// ⚙ opens the settings screen, whose tabs hold 설정 / 연결 / 음성 — one button, plus "?" for the shortcut help.
assert(header.includes('aria-label="설정"') && header.includes('aria-label="화면 분할 추가"') && header.includes('aria-label="단축키 도움말"'), "frequent header actions must stay directly visible");
assert(!header.includes('onclick={() => openSettings("connection")}'), "the connection screen is reached from the settings tabs or the connection badge, not a duplicate header button");
assert(header.includes("openSettings(\"voice\")") && header.includes("voiceChip.label"), "header must show the voice status chip that opens the voice settings");

// Unsaved settings ask before leaving; dialogs focus themselves; Enter ignores IME composition.
assert(app.includes("async function confirmLeaveSettings()") && app.includes("if (!(await confirmLeaveSettings())) return"), "leaving settings with unsaved edits must ask first");
assert(app.includes("use:autofocus={true}") && app.includes("!event.isComposing && event.keyCode !== 229"), "name prompt must autofocus and ignore Enter while composing Korean");
assert(!/\b(?:window\.)?(?:confirm|alert|prompt)\(/.test(app), "no browser alert/confirm/prompt in the app shell");

// Pinned conversations, "/" snippets, palette actions.
assert(store.includes("export function setConversationPinned") && store.includes("!isConversationPinned(conv.id)"), "pinned conversations must be listed once, in the pinned section");
assert(pane.includes("handleSnippetKeydown(p, event)") && pane.includes("snippetMenuFor(p)"), "composer must offer the / snippet picker");
assert(app.includes("<QuickSwitcher actions={paletteActions} bind:helpOpen />"), "Ctrl+K must also run app commands");

// Round 2: search every conversation, bookmarks, color labels, Ctrl+Tab, long paste, .md export.
{
  const search = readFileSync(join(root, "src", "search.svelte.js"), "utf8");
  const panel = readFileSync(join(root, "src", "SearchPanel.svelte"), "utf8");
  const recent = readFileSync(join(root, "src", "RecentSwitcher.svelte"), "utf8");
  assert(search.includes("export async function searchAll") && search.includes("ControlService.GetHistory") && search.includes("historyMessages("), "all-conversation search must read histories through the same mapping the panes use");
  assert(store.includes("export async function jumpToMessage") && store.includes("export function findMessageIndex"), "search hits and bookmarks must jump to the message itself");
  assert(panel.includes("jumpToMessage(item.target") && app.includes("<SearchPanel />") && app.includes('openSearchPanel("search")'), "Ctrl+Shift+F must open the search panel");
  assert(search.includes("export function toggleBookmark") && pane.includes("toggleBookmark(p.target, message, mIndex)"), "message actions must offer a bookmark");
  assert(store.includes("export function setConversationLabel") && app.includes("labelFilter") && app.includes("filteredWebConvs()"), "conversations must take a color label and the sidebar must filter by it");
  assert(recent.includes('event.key === "Tab" && event.ctrlKey') && app.includes("rememberRecentTarget(target)"), "Ctrl+Tab must cycle recent conversations");
  assert(pane.includes("ControlService.SaveTextAttachment") && pane.includes("LONG_PASTE_CHARS"), "a long paste must be offered as a .txt attachment");
  assert(pane.includes("ControlService.SaveMarkdownFile"), "a conversation must be savable as a .md file");
  assert(![search, panel, recent].some((src) => /\b(?:window\.)?(?:confirm|alert|prompt)\(/.test(src)), "no browser dialogs in the round-2 components");
}

// Round 3: quick ask hotkey, templates, ask another AI, summary note, home card, finished-away notice.
{
  const quick = readFileSync(join(root, "src", "QuickAsk.svelte"), "utf8");
  const fill = readFileSync(join(root, "src", "TemplateFill.svelte"), "utf8");
  const snip = readFileSync(join(root, "src", "snippets.svelte.js"), "utf8");
  const goQuick = readFileSync(join(root, "..", "quickask.go"), "utf8");
  assert(goQuick.includes('app.Event.Emit("hotkey:quickask"') && quick.includes('Events.On("hotkey:quickask"'), "the global quick-ask hotkey must open the 빠른 질문 box");
  assert(quick.includes("sendTextToTarget(target, message)") && store.includes("export async function sendTextToTarget"), "quick ask must send without clobbering a pane's composer draft");
  assert(snip.includes("export function templateFields") && snip.includes('"클립보드"') && pane.includes("askTemplate(p.id"), "snippets must support {{변수}} templates");
  assert(fill.includes("expandTemplate("), "the template form must expand the snippet");
  assert(store.includes("export async function askAnotherBackend") && pane.includes("runCompare(p, message, choice.id)"), "a question must be askable of another AI side by side");
  assert(store.includes("export async function requestSummary") && pane.includes("convNote(p.target)") && pane.includes('setConvNote(p.target, message.text, "pinned")'), "summary / pinned note must show on top of the conversation");
  assert(pane.includes("homeItems()") && pane.includes("openQuickAsk()"), "an empty pane must show the home card");
  assert(store.includes("chat.finishedAway = { key, target, at: Date.now() }") && app.includes("noteFinishedAway"), "a turn finished in the background must offer to open it on return");
  assert(![quick, fill].some((src) => /\b(?:window\.)?(?:confirm|alert|prompt)\(/.test(src)), "no browser dialogs in the round-3 components");
}
assert(app.includes('if (!target.closest("[data-more-menu]")) closeMoreMenu();') && app.includes("closeMoreMenu(true)"), "더보기 menu must close on outside click and return focus on Esc");

// "list" settings fields (approvals.allow / approvals.tools) render as chips + add input,
// but keep the comma-joined string value on the wire.
assert(app.includes('field.type === "list"') && app.includes("function addListItems(key)") && app.includes("function removeListItem(key, index)"), "list settings fields must render as a chip editor");
assert(app.includes('updateSettingValue(key, items.join(", "))'), "the chip editor must save the same comma-joined string older clients edit");

// The host's routine heartbeat arrives as a "status" frame that updates the
// working indicator in place (label, real start, last activity) — never a message.
assert(store.includes('frame.type === "status"') && store.includes("applyTurnStatus(key, frame);"), "status frames must update the working indicator, not append a message");
assert(store.includes("마지막 활동"), "working indicator must show the last activity");

// "여기서 갈라 새 대화": every user/AI message's action row can fork the
// conversation through the host (web_fork), confirmed in an in-app card, never
// the browser's confirm().
assert(pane.includes('aria-label="여기서 갈라 새 대화"') && pane.includes("toggleForkAsk(mIndex, message)"), "message actions must offer 여기서 갈라 새 대화");
assert(store.includes("export async function forkConversationAt") && store.includes("ControlService.WebFork"), "forking must go through the desktop WebFork control call");
assert(store.includes('selectTargetInPane(paneId, { kind: "web", id: reply.id })'), "the new forked conversation must open after it is created");
assert(!/\b(?:window\.)?(?:confirm|alert|prompt)\(/.test(pane) && !/\b(?:window\.)?(?:confirm|alert|prompt)\(/.test(store), "no browser alert/confirm/prompt in the pane UI");

// Message polish: avatars (no emoji), bubble tails, gentle gradients, 150ms
// transitions and a pressed state on buttons.
assert(pane.includes("{#snippet avatar(side)}") && pane.includes("msg-avatar-user") && pane.includes("msg-avatar-ai"), "messages must show small user/AI avatars");
assert(pane.includes("bubble-tail-user") && pane.includes("bubble-tail-ai"), "the first bubble of a run must carry a tail");
assert(/\.bubble-user\)[^}]*linear-gradient/.test(pane) && /\.dark \.bubble-ai\)[^}]*linear-gradient/.test(pane), "bubbles must use a soft gradient in light and dark themes");
assert(/\.msg-action-btn:active:not\(:disabled\)\)\s*\{\s*transform: scale\(0\.97\)/.test(pane), "message action buttons must have a pressed (scale .97) state");
assert(pane.includes("transition: background-color 150ms"), "button state changes must transition over 150ms");

// "{@const x = …}" that lost its "@const" still compiles (it assigns an
// undeclared global) and throws at render — the @file list broke this way.
for (const name of readdirSync(join(here, "..", "src")).filter((n) => n.endsWith(".svelte"))) {
  const src = readFileSync(join(here, "..", "src", name), "utf8");
  assert(!/^\s*\{\s+[A-Za-z_$][\w$]*\s*=[^=]/m.test(src), `${name}: a template tag looks like "{@const …}" without "@const"`);
}

if (process.exitCode) {
  process.exit(process.exitCode);
}

// Live-test fixes: one sidebar collapse button; 설정 찾기 also finds the 음성 tab.
assert((app.match(/onclick=\{\(\) => \(sidebarCollapsed = true\)\}/g) || []).length === 1, "the sidebar must have exactly one collapse («) button");
assert(app.includes("VOICE_SETTINGS_INDEX") && app.includes("voiceSearchHits.length") && app.includes('loadSettingsTab("voice")'), "settings search must surface the 음성 tab's settings and open that tab");
assert(app.includes('onclick={() => openSettings("voice")}'), "the header voice chip must open the 음성 settings tab");

if (process.exitCode) {
  process.exit(process.exitCode);
}
