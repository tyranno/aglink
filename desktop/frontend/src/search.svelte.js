// 모든 대화에서 찾기 + 북마크.
//
// Search reads every conversation's history: what a pane already holds is
// used as is, the rest is fetched once through GetHistory (a few at a time)
// and cached for CACHE_MS, so typing a new query does not refetch. Nothing is
// written to the panes' own message store — opening a hit goes through
// jumpToMessage like any other navigation.
//
// Bookmarks are a local list (localStorage) of {key, target, title, at, text}
// — enough for findMessageIndex to find the message again later.
import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";
import { chat, parseJSON, historyMessages, targetKey, paneConversationMeta } from "./paneStore.svelte.js";

export const searchPanel = $state({ open: false, tab: "search" }); // tab: search | bookmarks

export function openSearchPanel(tab = "search") {
  searchPanel.tab = tab;
  searchPanel.open = true;
}

// ---- search ----

const CACHE_MS = 5 * 60 * 1000;
const FETCH_CONCURRENCY = 3;
export const MAX_HITS = 200;
const historyCache = new Map(); // key → { at, messages }

export function searchTargets() {
  const list = [];
  if (chat.telegram) list.push({ kind: "telegram" });
  for (const conv of chat.webConvs) list.push({ kind: "web", id: conv.id });
  return list;
}

function loadedMessages(key) {
  if (chat.loadingBuffers.has(key)) return null; // still loading in a pane
  return chat.messagesByKey.get(key) || null;
}

async function messagesFor(target) {
  const key = targetKey(target);
  const own = loadedMessages(key);
  if (own) return own;
  const cached = historyCache.get(key);
  if (cached && Date.now() - cached.at < CACHE_MS) return cached.messages;
  const raw = await ControlService.GetHistory(target.kind, target.id || "");
  const messages = historyMessages(parseJSON(raw, { turns: [] }));
  historyCache.set(key, { at: Date.now(), messages });
  return messages;
}

// snippet: ~60 chars on each side of the first match, with the match split out
// so the UI can mark it without innerHTML.
export function snippetAround(text, q) {
  const flat = String(text || "").replace(/\s+/g, " ");
  const i = flat.toLowerCase().indexOf(q);
  if (i < 0) return { before: flat.slice(0, 120), match: "", after: "" };
  const start = Math.max(0, i - 60);
  const end = Math.min(flat.length, i + q.length + 60);
  return {
    before: (start > 0 ? "…" : "") + flat.slice(start, i),
    match: flat.slice(i, i + q.length),
    after: flat.slice(i + q.length, end) + (end < flat.length ? "…" : ""),
  };
}

let searchSeq = 0;

// searchAll runs one query over every conversation. onProgress(done, total,
// hits) is called as conversations finish; a newer call cancels older ones
// (their progress stops being reported). Returns the hits, newest first.
export async function searchAll(query, onProgress) {
  const q = String(query || "").trim().toLowerCase();
  const seq = ++searchSeq;
  if (q.length < 2) {
    onProgress?.(0, 0, [], true);
    return [];
  }
  const targets = searchTargets();
  const hits = [];
  let done = 0;
  let failed = 0;
  let next = 0;
  async function worker() {
    while (next < targets.length && seq === searchSeq) {
      const target = targets[next++];
      try {
        const messages = await messagesFor(target);
        const meta = paneConversationMeta(target);
        messages.forEach((m, index) => {
          if (m.role === "narration" || !m.text) return;
          if (!m.text.toLowerCase().includes(q)) return;
          hits.push({ target, key: targetKey(target), title: meta?.title || "대화", role: m.role, at: m.at || 0, index, text: m.text, snippet: snippetAround(m.text, q) });
        });
      } catch {
        failed++;
      }
      done++;
      if (seq === searchSeq) onProgress?.(done, targets.length, sortHits(hits), false, failed);
    }
  }
  await Promise.all(Array.from({ length: Math.min(FETCH_CONCURRENCY, targets.length) }, worker));
  const sorted = sortHits(hits);
  if (seq === searchSeq) onProgress?.(done, targets.length, sorted, true, failed);
  return sorted;
}

function sortHits(hits) {
  return [...hits].sort((a, b) => (b.at || 0) - (a.at || 0)).slice(0, MAX_HITS);
}

export function clearSearchCache() {
  historyCache.clear();
}

// ---- bookmarks ----

const BOOKMARKS_KEY = "aglink-desktop:bookmarks";
export const MAX_BOOKMARKS = 300;

function loadBookmarks() {
  try {
    const data = JSON.parse(localStorage.getItem(BOOKMARKS_KEY) || "[]");
    return Array.isArray(data) ? data.filter((b) => b && b.key && b.target && typeof b.text === "string").slice(0, MAX_BOOKMARKS) : [];
  } catch {
    return [];
  }
}

export const bookmarks = $state({ list: loadBookmarks() });

function persistBookmarks() {
  try {
    localStorage.setItem(BOOKMARKS_KEY, JSON.stringify(bookmarks.list));
  } catch {
    // memory only
  }
}

function bookmarkId(key, message) {
  return `${key}|${message.at || 0}|${String(message.text || "").slice(0, 60)}`;
}

export function isBookmarked(target, message) {
  if (!target || !message) return false;
  const id = bookmarkId(targetKey(target), message);
  return bookmarks.list.some((b) => b.id === id);
}

// Returns true when the message is bookmarked afterwards.
export function toggleBookmark(target, message, index) {
  if (!target || !message?.text) return false;
  const key = targetKey(target);
  const id = bookmarkId(key, message);
  if (bookmarks.list.some((b) => b.id === id)) {
    bookmarks.list = bookmarks.list.filter((b) => b.id !== id);
    persistBookmarks();
    return false;
  }
  const meta = paneConversationMeta(target);
  const entry = {
    id,
    key,
    target: { kind: target.kind, ...(target.id ? { id: target.id } : {}) },
    title: meta?.title || "대화",
    role: message.role,
    at: message.at || 0,
    index,
    text: String(message.text).slice(0, 400),
    savedAt: Date.now(),
  };
  bookmarks.list = [entry, ...bookmarks.list].slice(0, MAX_BOOKMARKS);
  persistBookmarks();
  return true;
}

export function removeBookmark(id) {
  bookmarks.list = bookmarks.list.filter((b) => b.id !== id);
  persistBookmarks();
}
