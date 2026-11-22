// 상용구 (prompt snippets): short texts the user saves once and drops into the
// composer by typing "/" and a part of the name. Kept in localStorage only —
// they are this desktop's own convenience, nothing on the host knows them.
//
// The composer picker opens only for a "/" at the very start with no space yet
// (so pasting a path like "/home/me/a b" or writing "/ 2" never triggers it),
// and an empty "/" shows a hint only while no snippet exists.

const STORAGE_KEY = "aglink-desktop:snippets";
export const MAX_SNIPPETS = 50;
export const MAX_SNIPPET_CHARS = 8000;

function load() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    const data = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(data)) return [];
    return data
      .filter((item) => item && typeof item.name === "string" && typeof item.text === "string")
      .map((item, index) => ({ id: String(item.id || `s${Date.now()}-${index}`), name: item.name, text: item.text }))
      .slice(0, MAX_SNIPPETS);
  } catch {
    return [];
  }
}

export const snippets = $state({
  list: load(),
  managerOpen: false,
  managerDraft: "", // text to prefill the "새 상용구" form with (e.g. the composer's text)
});

function persist() {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(snippets.list));
  } catch {
    // Storage full/unavailable: snippets just stay for this run.
  }
}

// cleanName keeps a name usable after "/": no spaces (a space closes the picker).
export function cleanSnippetName(name) {
  return String(name || "").trim().replace(/\s+/g, "-").slice(0, 40);
}

// Returns an error message, or "" when saved.
export function saveSnippet({ id = "", name, text }) {
  const n = cleanSnippetName(name);
  const t = String(text || "");
  if (!n) return "이름을 적어 주세요.";
  if (!t.trim()) return "넣을 내용을 적어 주세요.";
  if (t.length > MAX_SNIPPET_CHARS) return `내용은 ${MAX_SNIPPET_CHARS.toLocaleString("ko-KR")}자까지 저장할 수 있어요.`;
  const clash = snippets.list.find((item) => item.name.toLowerCase() === n.toLowerCase() && item.id !== id);
  if (clash) return `“${n}” 이름의 상용구가 이미 있어요.`;
  if (id) {
    snippets.list = snippets.list.map((item) => (item.id === id ? { ...item, name: n, text: t } : item));
  } else {
    if (snippets.list.length >= MAX_SNIPPETS) return `상용구는 ${MAX_SNIPPETS}개까지 만들 수 있어요.`;
    snippets.list = [...snippets.list, { id: `s${Date.now()}`, name: n, text: t }];
  }
  persist();
  return "";
}

export function deleteSnippet(id) {
  snippets.list = snippets.list.filter((item) => item.id !== id);
  persist();
}

export function openSnippetManager(prefill = "") {
  snippets.managerDraft = String(prefill || "");
  snippets.managerOpen = true;
}

// snippetQuery: the search after a leading "/", or null when the composer is
// not asking for a snippet.
export function snippetQuery(text) {
  const value = String(text || "");
  if (!value.startsWith("/") || /\s/.test(value)) return null;
  return value.slice(1).toLowerCase();
}

export function snippetCandidates(text) {
  const q = snippetQuery(text);
  if (q === null) return [];
  const starts = [];
  const contains = [];
  for (const item of snippets.list) {
    const name = item.name.toLowerCase();
    if (name.startsWith(q)) starts.push(item);
    else if (q && (name.includes(q) || item.text.toLowerCase().includes(q))) contains.push(item);
  }
  return [...starts, ...contains].slice(0, 12);
}
