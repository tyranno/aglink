// aglink-web MV3 background service worker.
//
// Connects to the local aglink-web daemon and executes the browser commands it
// pushes (list_tabs / navigate / get_page_text …) via the chrome.* APIs. Two
// transports carry the same messages:
//
//   - native messaging (preferred): chrome.runtime.connectNative("com.aglink.web")
//     makes Chrome start aglink-web.exe as a relay to the daemon. While that
//     port is open Chrome keeps this service worker alive (Chrome 105+). Needs
//     the host the installer registers, and the fixed extension id that the
//     manifest's "key" gives.
//   - WebSocket (fallback): ws://127.0.0.1:<port>/ext. The daemon validates our
//     chrome-extension:// Origin at the handshake, so arbitrary web pages that
//     try ws://127.0.0.1 are rejected.
//
// Only one is open at a time: the daemon keeps the newest connection per
// account, so two would evict each other.
//
// Port: defaults to 48219 (must match the daemon's defaultPort / AGLINK_WEB_PORT).
// A browser extension can't read env vars or the daemon's port file, so if you
// override AGLINK_WEB_PORT, set the matching port once in the extension's options
// (chrome://extensions → aglink-web → Details → Extension options). It's stored
// in chrome.storage.local and applied on the next (re)connect.

const DEFAULT_PORT = 48219;
const DEFAULT_MAX_CHARS = 20000;
const NATIVE_HOST = "com.aglink.web";

let ws = null;
let nmPort = null; // native messaging port, from open until it disconnects
let connecting = false; // guards the async gap between the connect guard and socket creation
const MIN_BACKOFF_MS = 1000;
const MAX_BACKOFF_MS = 5000;
let backoffMs = MIN_BACKOFF_MS;
let reconnectTimer = null;

// After the native host fails before reaching the daemon (not installed, or
// the daemon would not start), use the WebSocket for this long before trying
// native again.
const NATIVE_RETRY_MS = 60000;
let nativeUnavailableUntil = 0;

// currentPort reads the configured daemon port from chrome.storage.local,
// falling back to DEFAULT_PORT when unset or invalid.
async function currentPort() {
  try {
    const { port } = await chrome.storage.local.get("port");
    const n = Number(port);
    return Number.isInteger(n) && n > 0 && n < 65536 ? n : DEFAULT_PORT;
  } catch (e) {
    return DEFAULT_PORT;
  }
}

// bootId names this browser run. chrome.storage.session survives worker
// restarts but not a browser restart, which is exactly when Chrome starts
// reusing tab ids — the daemon keeps the agents' remembered tabs only while
// this stays the same.
async function bootId() {
  try {
    const s = chrome.storage.session;
    if (!s) return "";
    const { boot } = await s.get("boot");
    if (boot) return boot;
    const v = Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
    await s.set({ boot: v });
    return v;
  } catch (e) {
    return "";
  }
}

// NOT_SIGNED_IN is surfaced on the options page: without a Chrome account this
// profile has no name the daemon can route to, so it cannot connect at all.
const NOT_SIGNED_IN =
  "this Chrome profile is not signed in — sign in to Chrome, then reload this extension";

// currentAccount returns the Google account this Chrome profile is signed in as.
// It is the profile's identity on the daemon: every command is routed by this
// string, so an empty result means we must not connect.
//
// accountStatus:"ANY" matters. The default ("SYNC") returns an empty email
// unless the user has turned Chrome sync on, and signed-in-without-sync is the
// common case — without ANY this would report "not signed in" for profiles that
// plainly are.
async function currentAccount() {
  try {
    const info = await chrome.identity.getProfileUserInfo({ accountStatus: "ANY" });
    return (info && info.email ? info.email : "").trim().toLowerCase();
  } catch (e) {
    return "";
  }
}

// linkBusy reports a transport that is open or on its way to opening.
function linkBusy() {
  if (nmPort) return true;
  return !!(ws && (ws.readyState === WebSocket.CONNECTING || ws.readyState === WebSocket.OPEN));
}

async function connect() {
  // Idempotent: never open a second transport while one is connecting/open.
  // onInstalled, onStartup, the keepalive alarm, storage changes, tab/network
  // events and the initial load all call connect(); without this guard they
  // would race into several connections that the daemon's "newest wins" then
  // churns. `connecting` extends the guard across the async gap while we read
  // the port and account from storage.
  if (connecting || linkBusy()) return;
  connecting = true;
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }

  const port = await currentPort();
  const account = await currentAccount();
  if (!account) {
    connecting = false;
    console.warn("aglink-web: " + NOT_SIGNED_IN);
    try {
      await chrome.storage.local.set({ lastError: NOT_SIGNED_IN });
    } catch (e) {
      // storage is a convenience for the options page; never block on it
    }
    // Retry anyway: the backoff is short, so signing in to Chrome recovers on
    // its own without the user having to come back and reload the extension.
    scheduleReconnect();
    return;
  }
  const boot = await bootId();

  if (Date.now() >= nativeUnavailableUntil && connectNativeLink(account, boot)) {
    connecting = false;
    return;
  }

  let socket;
  try {
    socket = new WebSocket(
      `ws://127.0.0.1:${port}/ext?account=${encodeURIComponent(account)}&boot=${encodeURIComponent(boot)}`
    );
  } catch (e) {
    connecting = false;
    scheduleReconnect();
    return;
  }
  ws = socket;
  connecting = false;

  let opened = false;
  socket.onopen = () => {
    opened = true;
    onLinkUp(account, "websocket");
  };

  socket.onmessage = (event) => {
    let req;
    try {
      req = JSON.parse(event.data);
    } catch (e) {
      return;
    }
    handleRequest(req, (reply) => send(socket, reply));
  };

  socket.onclose = () => {
    // Only react to the socket we currently own; a superseded older socket
    // closing must not trigger a reconnect loop.
    if (ws === socket) {
      ws = null;
      if (opened) onLinkLost();
      else scheduleReconnect();
    }
  };

  socket.onerror = () => {
    // onclose fires next and drives reconnection.
  };
}

// connectNativeLink opens the native messaging port and says hello; the host
// answers {"type":"ready"} once it reached the daemon. Returns false when the
// API is missing, so the caller goes straight to the WebSocket.
function connectNativeLink(account, boot) {
  if (!chrome.runtime || typeof chrome.runtime.connectNative !== "function") return false;
  let p;
  try {
    p = chrome.runtime.connectNative(NATIVE_HOST);
  } catch (e) {
    return false;
  }
  nmPort = p;
  let ready = false;
  p.onMessage.addListener((msg) => {
    if (nmPort !== p || !msg) return;
    if (msg.type === "ready") {
      ready = true;
      onLinkUp(account, "native");
      return;
    }
    if (msg.type === "error") {
      console.warn("aglink-web: native host: " + msg.error);
      return; // the host exits next; onDisconnect follows
    }
    handleRequest(msg, (reply) => {
      if (nmPort !== p) return;
      try {
        p.postMessage(reply);
      } catch (e) {
        // port closed meanwhile; the daemon fails the call on its side
      }
    });
  });
  p.onDisconnect.addListener(() => {
    const err = chrome.runtime.lastError; // read it, or Chrome logs it as unchecked
    if (nmPort !== p) return;
    nmPort = null;
    if (!ready) {
      // Host missing or could not reach the daemon: fall back to the
      // WebSocket now rather than after a backoff.
      console.log("aglink-web: native host unavailable, using WebSocket" + (err && err.message ? ": " + err.message : ""));
      nativeUnavailableUntil = Date.now() + NATIVE_RETRY_MS;
      connect();
      return;
    }
    onLinkLost();
  });
  try {
    p.postMessage({ type: "hello", account, boot });
  } catch (e) {
    nmPort = null;
    nativeUnavailableUntil = Date.now() + NATIVE_RETRY_MS;
    return false;
  }
  return true;
}

function onLinkUp(account, via) {
  console.log(`aglink-web: connected to daemon as ${account} (${via})`);
  backoffMs = MIN_BACKOFF_MS;
  chrome.storage.local.set({ lastError: "", connectedAs: account, connectedVia: via }).catch(() => {});
}

// handleRequest runs one daemon request and hands the reply to reply().
async function handleRequest(req, reply) {
  if (!req || typeof req !== "object") return;
  // Keepalive ping from the daemon (id 0): reply so the daemon can refresh its
  // read deadline, and — because a message on either transport resets the MV3
  // service-worker idle timer — this exchange keeps this worker alive.
  if (req.method === "__ping") {
    reply({ id: req.id, ok: true });
    return;
  }
  const out = { id: req.id, ok: false };
  try {
    out.text = await withDialogWatch(req.method, req.params || {}, () => dispatch(req.method, req.params || {}));
    out.ok = true;
  } catch (e) {
    out.error = await explainError(req.params || {}, String(e && e.message ? e.message : e));
  }
  reply(out);
}

function send(socket, obj) {
  if (socket && socket.readyState === WebSocket.OPEN) {
    socket.send(JSON.stringify(obj));
  }
}

// scheduleReconnect arms one retry. Short cap with jitter: a call on the daemon
// side waits for us (AGLINK_WEB_RECONNECT_WAIT_MS), so being back within a few
// seconds turns a dropped connection into a delay instead of an error.
// Sequence after a failed attempt: ~1s, 2s, 4s, then every 5s.
function scheduleReconnect() {
  if (reconnectTimer) return;
  const delay = Math.round(backoffMs * (0.8 + Math.random() * 0.4));
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    connect();
  }, delay);
  backoffMs = Math.min(backoffMs * 2, MAX_BACKOFF_MS);
}

// onLinkLost handles a transport that had been up and just dropped (daemon
// restarted, host exited): one immediate retry — usually the daemon is already
// back, or a fresh one is about to bind — and only if that fails the backoff
// above, starting again from its minimum. A link that keeps dropping right
// after it comes up gets the backoff instead, so it cannot spin.
const IMMEDIATE_RETRY_GAP_MS = 5000;
let lastImmediateRetry = 0;
function onLinkLost() {
  const now = Date.now();
  if (now - lastImmediateRetry < IMMEDIATE_RETRY_GAP_MS) {
    scheduleReconnect();
    return;
  }
  lastImmediateRetry = now;
  backoffMs = MIN_BACKOFF_MS;
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    connect();
  }, 0);
}

// reconnectNow skips the backoff when something suggests the daemon may be
// reachable again (network back, the user is using the browser, the worker
// woke up). A no-op while connected or connecting.
function reconnectNow() {
  if (connecting || linkBusy()) return;
  backoffMs = MIN_BACKOFF_MS;
  connect();
}

// ---- command handlers -------------------------------------------------------

async function dispatch(method, params) {
  switch (method) {
    case "list_tabs":
      return await listTabs();
    case "navigate":
      return await navigate(params);
    case "get_page_text":
      return await getPageText(params);
    case "element_exists":
      return await elementExists(params);
    case "click":
      return await click(params);
    case "double_click":
      return await doubleClick(params);
    case "hover":
      return await hover(params);
    case "drag":
      return await drag(params);
    case "get_html":
      return await getHtml(params);
    case "query_all":
      return await queryAll(params);
    case "eval":
      return await evalExpression(params);
    case "get_attribute":
      return await getAttribute(params);
    case "list_elements":
      return await listElements(params);
    case "wait_for_element":
      return await waitForElement(params);
    case "screenshot":
      return await screenshot(params);
    case "type":
      return await typeText(params);
    case "get_value":
      return await getValue(params);
    case "key":
      return await keyCombo(params);
    case "scroll":
      return await scroll(params);
    case "select_option":
      return await selectOption(params);
    case "activate_tab":
      return await activateTab(params);
    case "get_console_logs":
      return await getConsoleLogs(params);
    case "get_network_requests":
      return await getNetworkRequests(params);
    case "reload_extension":
      return await reloadExtension();
    case "close_tab":
      return await closeTab(params);
    case "proceed_insecure":
      return await proceedInsecure(params);
    case "upload_file":
      return await uploadFile(params);
    case "page_snapshot":
      return await pageSnapshot(params);
    case "dialog_status":
      return await dialogStatus(params);
    case "handle_dialog":
      return await handleDialog(params);
    default:
      throw new Error(`unknown method: ${method}`);
  }
}

// ensureHelpers injects the shared ISOLATED-world resolver (aglink-inject.js)
// into a tab right before a selector-based action runs, so every command shares
// one shadow-DOM-piercing + semantic-locator engine (see aglink-inject.js).
// It runs in the same isolated world as the action func executeScript issues
// next, so that func can call globalThis.__aglink.resolve(...). Idempotent —
// re-injecting just re-defines the global. Injection failures (e.g. a
// chrome:// page that forbids scripting) are swallowed: the action func falls
// back to document.querySelector, preserving the old behavior.
// Takes a tab id or a full executeScript target (a frame, see locate).
async function ensureHelpers(tabOrTarget) {
  const target = typeof tabOrTarget === "object" ? tabOrTarget : { tabId: tabOrTarget };
  try {
    await chrome.scripting.executeScript({
      target,
      files: ["aglink-inject.js", "page-actions.js"],
    });
  } catch (e) {
    // Non-fatal — see comment above.
  }
}

// ---- frames -------------------------------------------------------------------
//
// A selector "frame=<frame selector> >> <selector>" acts inside an iframe.
// The in-page resolver can reach a same-origin frame's document itself, but a
// cross-origin one only by running in that frame — executeScript with its
// frameId. locate walks the frame= hops: each is resolved in the frame found
// so far (page-actions.js P.frameInfo), its Chrome frame id taken from
// runtime.getFrameId (or, failing that, matched by URL in
// webNavigation.getAllFrames), and the rest of the selector then runs there.

function frameTarget(tabId, frameId) {
  return frameId ? { tabId, frameIds: [frameId] } : { tabId };
}

function splitFrameSelector(sel) {
  const m = /^\s*frame=([\s\S]+?)\s*>>\s*([\s\S]+)$/.exec(sel || "");
  return m ? { frame: m[1].trim(), inner: m[2].trim() } : null;
}

// frameIdByUrl finds a child frame of parentFrameId whose URL is src.
async function frameIdByUrl(tabId, parentFrameId, src) {
  if (!src || !chrome.webNavigation || !chrome.webNavigation.getAllFrames) return null;
  try {
    const frames = (await chrome.webNavigation.getAllFrames({ tabId })) || [];
    const kids = frames.filter((f) => f.parentFrameId === (parentFrameId || 0));
    const hit = kids.find((f) => f.url === src) || kids.find((f) => f.url && (f.url.startsWith(src) || src.startsWith(f.url)));
    return hit ? hit.frameId : null;
  } catch (e) {
    return null;
  }
}

// locate returns where a selector runs: {target, selector, frameId}, with
// the frame= hops taken off. A frame that cannot be found throws, with a hint.
// When every hop is same-origin, sameOrigin is set: the full selector also
// works from the top frame (runPage's fallback if the frame itself refuses
// scripting, e.g. some about:blank frames).
async function locate(tabId, selector) {
  let frameId = 0;
  let rest = selector;
  let sameOrigin = true;
  for (let hop = 0; hop < 5; hop++) {
    const fs = splitFrameSelector(rest);
    if (!fs) break;
    const target = frameTarget(tabId, frameId);
    await ensureHelpers(target);
    const results = await chrome.scripting.executeScript({
      target,
      func: (name, args) => globalThis.__aglinkPage[name](...args),
      args: ["frameInfo", [fs.frame]],
    });
    const r = results && results[0] && results[0].result;
    if (!r || !r.found) throw new Error(`no frame matched selector: ${fs.frame}${r && r.hint ? ` — ${r.hint}` : ""}`);
    if (!r.isFrame) throw new Error(`frame=${fs.frame} matched ${r.desc || "<" + r.tag + ">"}, which is not an <iframe>`);
    sameOrigin = sameOrigin && !!r.sameOrigin;
    let id = typeof r.frameId === "number" ? r.frameId : await frameIdByUrl(tabId, frameId, r.src);
    if (id == null) {
      if (sameOrigin) return { target: { tabId }, selector, frameId: 0, sameOrigin };
      throw new Error(`could not find the browser frame for ${fs.frame} (src ${r.src || "none"})`);
    }
    frameId = id;
    rest = fs.inner;
  }
  return { target: frameTarget(tabId, frameId), selector: rest, frameId, sameOrigin };
}

// runPage runs a shared page function (page-actions.js) for selector:
// argsFor(sel) builds its arguments from the selector as it applies in the
// frame it runs in. Returns the function's result.
async function runPage(tabId, selector, name, argsFor) {
  const exec = async (target, sel) => {
    await ensureHelpers(target);
    const results = await chrome.scripting.executeScript({
      target,
      func: (n, args) => globalThis.__aglinkPage[n](...args),
      args: [name, argsFor(sel)],
    });
    return results && results[0] && results[0].result;
  };
  if (!splitFrameSelector(selector)) return exec({ tabId }, selector);
  const loc = await locate(tabId, selector);
  if (!loc.frameId) return exec(loc.target, loc.selector);
  try {
    const r = await exec(loc.target, loc.selector);
    if (r != null || !loc.sameOrigin) return r;
  } catch (e) {
    if (!loc.sameOrigin) throw e;
  }
  return exec({ tabId }, selector);
}

// notFound is the error for a selector that matched nothing, with the page's
// hint at what nearly matched (aglink-inject.js nearMiss).
function notFound(selector, r) {
  return new Error(`no element matched selector: ${selector}${r && r.hint ? ` — ${r.hint}` : ""}`);
}

// withNote appends a page result's ambiguity note ("matched 3 elements, used
// the first: …") to a tool's reply.
function withNote(text, r) {
  return r && r.note ? `${text}\nnote: ${r.note}` : text;
}

// activeTabId returns params.tabId or the active tab of the focused window,
// the default-target resolution every selector command shares.
async function activeTabId(params) {
  if (params.tabId) return params.tabId;
  const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (!active) throw new Error("no active tab");
  return active.id;
}

async function listTabs() {
  const tabs = await chrome.tabs.query({});
  if (tabs.length === 0) return "(no open tabs)";
  return tabs
    .map((t) => `${t.id} | ${t.active ? "[active] " : ""}${t.title || ""} | ${t.url || ""}`)
    .join("\n");
}

async function navigate(params) {
  const url = params.url;
  if (!url) throw new Error("navigate requires 'url'");
  let tab;
  if (params.tabId) {
    tab = await chrome.tabs.update(params.tabId, { url });
  } else {
    try {
      // active:false — open in the background so the agent's navigation does not
      // yank the user's current tab away. The daemon remembers this tab id and
      // targets it for later tabId-less calls, so working in the background tab
      // still flows naturally (see the daemon's navTabs).
      tab = await chrome.tabs.create({ url, active: false });
    } catch (e) {
      // Chrome left running in the background (no window open, e.g. after
      // closing the last one with "continue running background apps") has
      // nowhere to put a tab; open a window for it instead. A window must take
      // focus to exist usefully, but this path only happens when Chrome had no
      // visible window at all, so nothing is being yanked away.
      if (!/no current window/i.test(String(e && e.message ? e.message : e))) throw e;
      const win = await chrome.windows.create({ url, focused: false });
      tab = win.tabs && win.tabs[0];
      if (!tab) throw e;
    }
    // Only a tab this call opened is grouped — never one the caller named,
    // which may be the user's own.
    if (params.group) await addToSessionGroup(tab.id, String(params.group), String(params.groupTitle || ""));
  }
  const loadMs = navigateLoadTimeoutMs();
  const loaded = await waitForComplete(tab.id, loadMs);
  const updated = await chrome.tabs.get(tab.id);
  // A page that is still loading is reported as such, not as done: its
  // content may not be there yet.
  const still = loaded ? "" : ` (still loading after ${Math.round(loadMs / 1000)}s)`;
  const ok = `ok: navigated tab ${updated.id} — ${updated.title || ""} — ${updated.url || ""}${still}`;
  // A certificate problem does not fail the navigation: Chrome shows its own
  // warning page in the tab, and every later tool would fail on it with an
  // opaque "showing error page". Say so here, with the way past it.
  const warn = await certWarning(updated.id);
  return warn ? `${ok}\n${warn}` : ok;
}

// Tab groups: each agent session (the daemon passes its key as params.group)
// gets one Chrome tab group, created on its first navigate, titled with the
// session's short label, one color per session. group key → group id lives in
// storage.session so a worker restart keeps adding to the same group.
const GROUP_COLORS = ["blue", "red", "yellow", "green", "pink", "purple", "cyan", "orange", "grey"];

function groupColor(key) {
  let h = 0;
  for (let i = 0; i < key.length; i++) h = (h * 31 + key.charCodeAt(i)) >>> 0;
  return GROUP_COLORS[h % GROUP_COLORS.length];
}

// Serializes group bookkeeping, so two navigates of one session racing do not
// each create a group.
let groupChain = Promise.resolve();

function addToSessionGroup(tabId, key, title) {
  const run = groupChain.then(() => groupTab(tabId, key, title));
  groupChain = run.catch(() => {});
  return run.catch(() => {}); // grouping is cosmetic; never fail the navigate
}

async function groupTab(tabId, key, title) {
  if (!chrome.tabGroups || typeof chrome.tabs.group !== "function") return;
  const store = chrome.storage.session;
  const groups = (store && (await store.get("tabGroups")).tabGroups) || {};
  let gid = groups[key];
  if (gid !== undefined) {
    try {
      await chrome.tabGroups.get(gid);
      await chrome.tabs.group({ groupId: gid, tabIds: [tabId] });
      return;
    } catch (e) {
      // the group was closed (or is in a window this tab cannot join): start a new one
    }
  }
  gid = await chrome.tabs.group({ tabIds: [tabId] });
  await chrome.tabGroups.update(gid, { title: title || "agent", color: groupColor(key), collapsed: false });
  groups[key] = gid;
  if (store) await store.set({ tabGroups: groups });
}

async function getPageText(params) {
  const tabId = await activeTabId(params);
  const maxChars = params.maxChars || DEFAULT_MAX_CHARS;
  const selector = params.selector || null;
  // int-only params (see command.go): cursor>0 = incremental; offset<0 = tail.
  const hasCursor = Number.isFinite(params.cursor) && params.cursor > 0;
  const cursor = hasCursor ? Math.floor(params.cursor) : -1;
  const offset = Number.isFinite(params.offset) ? Math.floor(params.offset) : 0;

  let r;
  if (selector) {
    r = await runPage(tabId, selector, "getPageText", (sel) => [sel]);
  } else {
    await ensureHelpers(tabId);
    const results = await chrome.scripting.executeScript({
      target: { tabId },
      func: (name, args) => globalThis.__aglinkPage[name](...args),
      args: ["getPageText", [selector, { frames: true }]],
    });
    r = results && results[0] && results[0].result;
  }
  if (selector && (!r || !r.found)) throw notFound(selector, r);
  let full = (r && r.text) || "";
  // Same-origin iframes are already in; read the cross-origin ones in place.
  if (r && Array.isArray(r.xframes)) full += await crossFrameText(tabId, 0, r.xframes, 0);
  const total = full.length;

  // Incremental (cursor) mode: return only what was appended since `cursor`.
  if (hasCursor) {
    if (cursor <= total) {
      let delta = full.slice(cursor);
      let note = `new:${delta.length}`;
      if (delta.length > maxChars) { delta = delta.slice(-maxChars); note += ` truncated-to-last:${maxChars}`; }
      const body = delta.length ? delta : "(no new text since cursor)";
      return `${body}\n[cursor:${total} ${note}]`;
    }
    // The text is now shorter than the cursor — it changed/reset (e.g. a new
    // conversation loaded). Resync by returning a tail and a fresh cursor.
    const t = total > maxChars ? full.slice(-maxChars) : full;
    return `${t}\n[cursor:${total} reset (content shrank; showing last ${t.length})]`;
  }

  // Windowed read: offset<0 reads from the end (tail); otherwise from `offset`.
  let text, span;
  if (offset < 0) {
    const n = Math.min(-offset, maxChars);
    text = full.slice(-n);
    span = `${Math.max(0, total - n)}..${total}`;
  } else {
    const start = Math.min(offset, total);
    text = full.slice(start, start + maxChars);
    span = `${start}..${start + text.length}`;
  }
  const trunc = text.length < total ? ` (${span} of ${total})` : "";
  return `${text}\n[cursor:${total}${trunc}]`;
}

// crossFrameText reads the text of cross-origin iframes (P.getPageText's
// xframes) by running in each frame, nested ones too, under an
// "[iframe src]" header like the same-origin ones get.
async function crossFrameText(tabId, parentFrameId, xframes, depth) {
  let out = "";
  for (const xf of xframes.slice(0, 10)) {
    const id = typeof xf.frameId === "number" ? xf.frameId : await frameIdByUrl(tabId, parentFrameId, xf.src);
    if (id == null) continue;
    try {
      const target = frameTarget(tabId, id);
      await ensureHelpers(target);
      const results = await chrome.scripting.executeScript({
        target,
        func: (name, args) => globalThis.__aglinkPage[name](...args),
        args: ["getPageText", [null, { frames: true }]],
      });
      const r = results && results[0] && results[0].result;
      if (!r) continue;
      if (r.text && r.text.trim()) out += `\n\n[iframe ${xf.src}]\n${r.text}`;
      if (depth < 2 && Array.isArray(r.xframes)) out += await crossFrameText(tabId, id, r.xframes, depth + 1);
    } catch (e) {
      // a frame the extension may not script (chrome-extension://, a PDF viewer)
    }
  }
  return out;
}

// elementExists cheaply reports whether a selector matches, with NO page text —
// the low-cost primitive for polling a boolean condition (e.g. "is the app busy",
// keyed off a working/stop indicator's presence) instead of get_page_text/query_all.
async function elementExists(params) {
  const selector = params.selector;
  if (!selector) throw new Error("element_exists requires 'selector'");
  const tabId = await activeTabId(params);
  let r = null;
  try {
    r = await runPage(tabId, selector, "elementExists", (sel) => [sel]);
  } catch (e) {
    // A frame= that does not resolve (yet) just means "not there".
    if (!/^no frame matched|could not find the browser frame/.test(String(e && e.message))) throw e;
  }
  return JSON.stringify(r || { exists: false, visible: false, count: 0 });
}

// click left-clicks by default via the real .click() DOM method (a trusted
// primary-click equivalent — this is why click() has always worked reliably
// against real page click handlers). Right/middle are a different code path:
// there is no .rightClick()/.middleClick() DOM method, so those are
// synthesized as a mousedown+mouseup+(contextmenu|auxclick) sequence, which
// is untrusted. That reaches a page's OWN JS context-menu/middle-click
// handler (most web apps implement custom right-click menus this way) but
// will NOT summon the browser's native right-click context menu — that only
// appears for a real, OS-trusted contextmenu event, same category of
// limitation as keyCombo's dispatched KeyboardEvents.
async function click(params) {
  const selector = params.selector;
  if (!selector) throw new Error("click requires 'selector'");
  const button = (params.button || "left").toLowerCase();
  if (button !== "left" && button !== "right" && button !== "middle") {
    throw new Error(`click: unknown button ${JSON.stringify(params.button)} (want left/right/middle)`);
  }
  const tabId = await activeTabId(params);
  const since = Date.now();
  const r = await runPage(tabId, selector, "click", (sel) => [sel, button, actionOpts(params)]);
  if (!r || !r.found) throw notFound(selector, r);
  if (r.blocked) throw new Error(r.blocked);
  const out = `ok: ${button}-clicked <${r.tag}>${r.text ? " " + JSON.stringify(r.text) : ""}`;
  return withNote(out + (await openedTabsText(tabId, since)), r);
}

// actionOpts is the {timeout, force} a click / double-click page function
// takes: wait up to timeout ms for the element to be clickable, or with force
// skip that check.
function actionOpts(params) {
  const o = {};
  const t = Number(params.timeout);
  if (Number.isFinite(t) && t > 0) o.timeout = t;
  if (params.force === true || /^(true|yes|1|on)$/i.test(String(params.force || ""))) o.force = true;
  return o;
}

// hover moves the "pointer" onto an element by dispatching the mouseover /
// mouseenter / mousemove sequence most JS hover menus (dropdowns, tooltips,
// nav flyouts) listen for. Like right/middle click and keyCombo, these are
// untrusted (isTrusted:false) synthesized events: a page's OWN JS hover
// handlers fire, but browser-native :hover-only CSS effects that require a real
// OS pointer won't. Covers the common case (JS-driven menus) — the whole reason
// a caller reaches for hover instead of just clicking.
async function hover(params) {
  const selector = params.selector;
  if (!selector) throw new Error("hover requires 'selector'");
  const tabId = await activeTabId(params);
  const r = await runPage(tabId, selector, "hover", (sel) => [sel]);
  if (!r || !r.found) throw notFound(selector, r);
  return withNote(`ok: hovered <${r.tag}>${r.text ? " " + JSON.stringify(r.text) : ""}`, r);
}

// doubleClick fires the full mousedown/mouseup/click ×2 + dblclick sequence a
// page's own JS double-click handlers listen for (renaming a file, opening a
// row, word-select). Same untrusted-event caveat as click's right/middle path:
// JS handlers fire, but a browser-native default double-click action tied to
// trusted input alone won't.
async function doubleClick(params) {
  const selector = params.selector;
  if (!selector) throw new Error("double_click requires 'selector'");
  const tabId = await activeTabId(params);
  const since = Date.now();
  const r = await runPage(tabId, selector, "doubleClick", (sel) => [sel, actionOpts(params)]);
  if (!r || !r.found) throw notFound(selector, r);
  if (r.blocked) throw new Error(r.blocked);
  const out = `ok: double-clicked <${r.tag}>${r.text ? " " + JSON.stringify(r.text) : ""}`;
  return withNote(out + (await openedTabsText(tabId, since)), r);
}

// drag drags a source element onto a target (Playwright dragTo). It fires BOTH
// a pointer sequence (pointerdown/mousemove/pointerup) and the HTML5 DnD
// sequence (dragstart/dragenter/dragover/drop/dragend) sharing one DataTransfer,
// so both JS-driven reorder handlers (sortable lists, kanban boards, which
// usually listen for pointer/mouse events) and native draggable="true" drop
// zones (which need the DnD events + dataTransfer) respond. Untrusted events, so
// JS handlers fire but a browser-native OS drag won't — same category as the
// other synthesized pointer tools.
async function drag(params) {
  const selector = params.selector;
  const target = params.target;
  if (!selector) throw new Error("drag requires 'selector' (the source)");
  if (!target) throw new Error("drag requires 'target' (the destination)");
  const tabId = await activeTabId(params);
  // Both ends run in one frame: the source's. A target in another frame is
  // out of reach of one in-page drag.
  const fs = splitFrameSelector(selector);
  const ft = splitFrameSelector(target);
  if ((fs || ft) && !(fs && ft && fs.frame === ft.frame)) {
    throw new Error("drag: the source and the target must be in the same frame (give both the same frame=… prefix)");
  }
  const r = await runPage(tabId, selector, "drag", (sel) => [sel, ft ? ft.inner : target]);
  if (!r) throw new Error("drag failed");
  if (!r.found) throw new Error(`no element matched ${r.which} selector: ${r.sel}${r.hint ? ` — ${r.hint}` : ""}`);
  return `ok: dragged <${r.srcTag}> onto <${r.dstTag}>`;
}

// getAttribute reads a single attribute (or, for name "text", the element's
// visible textContent) off the matched element — the read-side counterpart for
// non-value state get_value can't reach: href on a link, aria-checked/
// aria-expanded/aria-selected state, disabled, class, a data-* attribute. Use
// it to confirm a page's own JS toggled state after an interaction.
async function getAttribute(params) {
  const selector = params.selector;
  const name = params.name;
  if (!selector) throw new Error("get_attribute requires 'selector'");
  if (!name) throw new Error("get_attribute requires 'name' (an attribute name, or 'text' for textContent)");
  const tabId = await activeTabId(params);
  const r = await runPage(tabId, selector, "getAttribute", (sel) => [sel, name]);
  if (!r || !r.found) throw notFound(selector, r);
  if (!r.present) return withNote(`${name} = (not present) on <${r.tag}>`, r);
  return withNote(`${name} = ${JSON.stringify(r.value)}`, r);
}

// getHtml returns raw outerHTML — of the whole document (no selector) or of one
// element's subtree (selector) — for scraping structured markup get_page_text's
// innerText flattens away (tag structure, attributes, hrefs, hidden nodes). The
// read-side counterpart to get_page_text for crawling.
async function getHtml(params) {
  const tabId = await activeTabId(params);
  const maxChars = params.maxChars || DEFAULT_MAX_CHARS;
  const selector = params.selector || null;
  const r = await runPage(tabId, selector, "getHtml", (sel) => [sel]);
  if (!r || !r.found) throw notFound(selector, r);
  let html = r.html || "";
  if (html.length > maxChars) {
    html = html.slice(0, maxChars) + `\n… [truncated at ${maxChars} chars]`;
  }
  return html;
}

// queryAll extracts a field set from EVERY element matching the selector in one
// call — the crawling workhorse. Uses the shared resolveAll (semantic locators +
// shadow piercing, visible-first). With no attrs, links auto-include href so
// `query_all a[href]` harvests a page's links; pass attrs to pull specific
// fields (href, data-*, aria-*), or "text" to force the textContent column.
// With 'fields' it returns one JSON object per element (page-actions.js
// P.extract); a selector matching only <table>s returns their rows as JSON.
async function queryAll(params) {
  const selector = params.selector;
  if (!selector) throw new Error("query_all requires 'selector'");
  const tabId = await activeTabId(params);
  const max = params.max || 200;
  if (params.fields) {
    const r = await runPage(tabId, selector, "extract", (sel) => [sel, params.fields, max]);
    return formatExtract(r || {}, selector);
  }
  const attrs = (params.attrs || "")
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  const els = (await runPage(tabId, selector, "queryAll", (sel) => [sel, attrs, max])) || [];
  if (els.lines) return els.lines.join("\n");
  if (els.length === 0) return `(no elements matched selector: ${selector})`;
  return els
    .map((e, i) => {
      const attrStr = e.attrs.map(([k, v]) => `${k}=${JSON.stringify(v)}`).join(" ");
      return `${i} | ${e.tag} | ${JSON.stringify(e.text)}${attrStr ? " | " + attrStr : ""}`;
    })
    .join("\n");
}

// formatExtract renders P.extract's result. Mirrored in apptools.go.
function formatExtract(r, selector) {
  if (r.error) throw new Error(r.error);
  const lines = r.lines || [];
  if (lines.length === 0) return `(no elements matched selector: ${selector})`;
  let out = lines.join("\n");
  if (r.total > lines.length) out += `\n… [${r.total - lines.length} more matched — raise max]`;
  return out;
}

// evalExpression runs a JS expression in the page's MAIN world (like
// Playwright's page.evaluate) and returns the JSON-stringified value — the
// escape hatch for extraction the structured tools don't cover (map over nodes,
// read page globals/framework stores, compute a derived value). MAIN world so it
// can see the page's own JS state, not just the DOM. Caveat: a strict page CSP
// without 'unsafe-eval' blocks the in-page eval and this throws — get_html /
// query_all / get_page_text still work there.
async function evalExpression(params) {
  const expression = params.expression;
  if (!expression) throw new Error("eval requires 'expression'");
  const tabId = await activeTabId(params);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    world: "MAIN",
    func: (expr) => {
      try {
        // Indirect eval so the expression evaluates in global scope; supports a
        // bare expression or an IIFE.
        const val = (0, eval)(expr);
        let json;
        if (val === undefined) {
          json = "undefined";
        } else {
          try {
            json = JSON.stringify(val, null, 2);
          } catch (e) {
            json = String(val); // circular / non-serializable → coerce to string
          }
          if (json === undefined) json = String(val); // JSON.stringify(fn) === undefined
        }
        return { ok: true, json };
      } catch (e) {
        return { ok: false, error: String(e && e.message ? e.message : e) };
      }
    },
    args: [expression],
  });
  const r = results && results[0] && results[0].result;
  if (!r) throw new Error("eval returned no result (the page may block script injection)");
  if (!r.ok) throw new Error(`eval error: ${r.error}`);
  return r.json;
}

// AGLINK_ID_ATTR marks each element listElements returns with a fresh,
// guaranteed-unique attribute, so the selector it reports for that element
// (e.g. [data-aglink-id="3"]) always matches exactly the element that was
// seen — no CSS-selector guessing against the page's own classes/attributes,
// which is what caused misclicks on pages like Gmail (a generic selector
// meant for one element matching an unrelated one elsewhere on the page).
const AGLINK_ID_ATTR = "data-aglink-id";

// INTERACTIVE_SELECTOR is the set of element kinds listElements considers —
// native interactive tags plus the common ARIA interactive roles.
const INTERACTIVE_SELECTOR = [
  "a[href]",
  "button",
  "input:not([type=\"hidden\"])",
  "textarea",
  "select",
  "[contenteditable=\"true\"]",
  "[role=\"button\"]",
  "[role=\"link\"]",
  "[role=\"checkbox\"]",
  "[role=\"radio\"]",
  "[role=\"menuitem\"]",
  "[role=\"tab\"]",
  "[role=\"option\"]",
  "[role=\"combobox\"]",
  "[role=\"switch\"]",
  "[onclick]",
].join(",");

// listElements lists currently visible interactive elements in a tab, each
// tagged with a fresh AGLINK_ID_ATTR so the reported selector is guaranteed to
// match only that element. Re-tags from scratch on every call (clearing any
// markers a previous call left) since SPA pages re-render their DOM
// constantly — indices are only valid until the page next changes.
async function listElements(params) {
  const tabId = await activeTabId(params);
  await ensureHelpers(tabId);
  const max = params.max || 200;
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["listElements", [INTERACTIVE_SELECTOR, AGLINK_ID_ATTR, max]],
  });
  const els = (results && results[0] && results[0].result) || [];
  if (els.length === 0) return "(no visible interactive elements found)";
  return els
    .map((e) => {
      const kind = e.role ? `${e.tag}[${e.role}]` : e.tag;
      const typeStr = e.type ? ` type=${e.type}` : "";
      const disabledStr = e.disabled ? " [disabled]" : "";
      return `${e.idx} | ${kind}${typeStr} | "${e.label}" | selector=[${AGLINK_ID_ATTR}="${e.idx}"] | viewport(${e.x},${e.y})${disabledStr}`;
    })
    .join("\n");
}

// waitForElementPollMs is how often waitForElement re-checks the page while
// waiting. A top-level const (not a function default) so tests can shrink it
// via a wrapper if ever needed; kept small since each check is a real
// chrome.scripting.executeScript round trip, not a cheap in-page loop.
const WAIT_FOR_ELEMENT_POLL_MS = 150;

// waitForElement blocks until a selector matches a visible element in the
// tab, instead of the caller polling list_elements/get_page_text by hand —
// useful for SPA content that renders after navigation/a click settles.
// With gone it waits for the element to disappear instead (a spinner, a
// closing dialog); with idleMs it also waits until no fetch/XHR has been in
// flight for that long (network-capture.js keeps the count).
async function waitForElement(params) {
  const selector = params.selector;
  const gone = params.gone === true || /^(true|yes|1|on)$/i.test(String(params.gone || ""));
  const idleMs = params.idleMs || 0;
  if (!selector && !idleMs) {
    throw new Error("wait_for_element requires 'selector' (or 'idleMs' to wait for the network to go quiet)");
  }
  const tabId = await activeTabId(params);
  const timeoutMs = params.timeoutMs || 8000;
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    let elOk = true;
    let tag = "";
    if (selector) {
      let r = null;
      try {
        r = await runPage(tabId, selector, "waitForElement", (sel) => [sel]);
      } catch (e) {
        // A frame that has not appeared yet is waited for like the element.
        if (!/^no frame matched|could not find the browser frame/.test(String(e && e.message))) throw e;
      }
      const shown = !!(r && r.found && r.visible);
      elOk = gone ? !shown : shown;
      if (shown) tag = r.tag;
    }
    let net = null;
    if (elOk && idleMs) {
      const results = await chrome.scripting.executeScript({
        target: { tabId },
        world: "MAIN",
        func: () => {
          const s = window.__aglinkNetState;
          if (!s) return null;
          return { pending: Array.from(s.pending.values()).slice(0, 3), count: s.pending.size, idle: Date.now() - s.last };
        },
      });
      net = results && results[0] && results[0].result;
      if (!net) {
        throw new Error("network tracking is not active in this tab (it loaded before the aglink-web extension was updated) — reload the page");
      }
    }
    const netOk = !idleMs || (net && net.count === 0 && net.idle >= idleMs);
    if (elOk && netOk) return waitDoneText(selector, gone, tag, idleMs);
    if (Date.now() >= deadline) throw new Error(waitTimeoutText(selector, gone, elOk, idleMs, net, timeoutMs));
    await new Promise((resolve) => setTimeout(resolve, WAIT_FOR_ELEMENT_POLL_MS));
  }
}

// waitDoneText / waitTimeoutText word waitForElement's outcome. Mirrored in
// apptools.go.
function waitDoneText(selector, gone, tag, idleMs) {
  const parts = [];
  if (selector) parts.push(gone ? `no visible element matches ${selector}` : `found <${tag}> matching ${selector}`);
  if (idleMs) parts.push(`network idle for ${idleMs}ms`);
  return "ok: " + parts.join("; ");
}

function waitTimeoutText(selector, gone, elOk, idleMs, net, timeoutMs) {
  if (!elOk) {
    return gone
      ? `timed out after ${timeoutMs}ms: an element matching ${selector} is still visible`
      : `timed out after ${timeoutMs}ms waiting for a visible element matching ${selector}`;
  }
  const busy = net.count > 0
    ? `${net.count} request(s) still pending: ${net.pending.join(", ")}`
    : `the last request ended ${net.idle}ms ago`;
  return `timed out after ${timeoutMs}ms waiting for the network to be idle for ${idleMs}ms — ${busy}`;
}

// screenshot captures the visible viewport of a tab as a base64 PNG (no data:
// URL prefix, so the daemon/MCP bridge can pass it straight through as the
// text-result payload — see protocol.go's doc comment on why this stays
// text-based end to end). captureVisibleTab only captures the *active* tab of
// a window, so a non-active tabId is switched to first (mirrors aglink-screen's
// focus_window-before-capture behavior). With a selector the element is
// scrolled into view and the capture is cropped to it (the part inside the
// viewport) — a much smaller image than the whole viewport.
async function screenshot(params) {
  let tabId = params.tabId;
  let windowId;
  if (tabId) {
    const tab = await chrome.tabs.get(tabId);
    windowId = tab.windowId;
    if (!tab.active) {
      await chrome.tabs.update(tabId, { active: true });
      await new Promise((r) => setTimeout(r, 100)); // let the tab actually paint
    }
  } else {
    const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!active) throw new Error("no active tab");
    windowId = active.windowId;
    tabId = active.id;
  }
  let rect = null;
  if (params.selector) {
    if (splitFrameSelector(params.selector)) {
      throw new Error("screenshot: a frame= selector is not supported here — screenshot the iframe element itself (frame=X without >>, or its CSS selector)");
    }
    await ensureHelpers(tabId);
    const results = await chrome.scripting.executeScript({
      target: { tabId },
      func: (name, args) => globalThis.__aglinkPage[name](...args),
      args: ["rectOf", [params.selector]],
    });
    rect = results && results[0] && results[0].result;
    if (!rect || !rect.found) throw notFound(params.selector, rect);
    if (!rect.width || !rect.height) throw new Error(`element matching ${params.selector} is not visible (zero size)`);
    await new Promise((r) => setTimeout(r, 100)); // let the scroll paint
  }
  const dataUrl = await chrome.tabs.captureVisibleTab(windowId, { format: "png" });
  if (rect) return await cropPng(dataUrl, rect);
  return dataUrl.replace(/^data:image\/png;base64,/, "");
}

// cropPng cuts an element's box (CSS px, viewport-relative, from
// page-actions.js P.rectOf) out of a captureVisibleTab data URL. The capture
// is in device pixels, so the scale is its width over the viewport's.
async function cropPng(dataUrl, r) {
  const full = await createImageBitmap(await (await fetch(dataUrl)).blob());
  const scale = full.width / r.vw;
  const x = Math.max(0, Math.floor(r.x * scale));
  const y = Math.max(0, Math.floor(r.y * scale));
  const w = Math.min(full.width, Math.ceil((r.x + r.width) * scale)) - x;
  const h = Math.min(full.height, Math.ceil((r.y + r.height) * scale)) - y;
  if (w <= 0 || h <= 0) throw new Error("the element is outside the visible viewport");
  const canvas = new OffscreenCanvas(w, h);
  canvas.getContext("2d").drawImage(full, x, y, w, h, 0, 0, w, h);
  const bytes = new Uint8Array(await (await canvas.convertToBlob({ type: "image/png" })).arrayBuffer());
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}

// typeText sets an input/textarea/contenteditable's value and fires input+change
// events so page JS (including React/Vue controlled inputs) picks up the change.
// For plain <input>/<textarea> it goes through the *native* value setter (bypassing
// any framework-overridden instance setter) — the standard trick to make React see
// a programmatic value change: React's onChange reads the DOM value after the
// 'input' event, so as long as the DOM value is actually set via the native setter
// first, the event dispatch below is what triggers it. A contenteditable gets
// execCommand("insertText") instead, so rich editors stay in sync (see
// page-actions.js P.typeText).
//
// mode: "set" (default, replace), "append", or "keys" — real typing through
// the debugger's Input.insertText (an Enter key per newline), for editors and
// widgets that only react to genuine input.
const TYPE_MODES = ["set", "append", "keys"];

async function typeText(params) {
  const selector = params.selector;
  const text = params.text;
  if (!selector) throw new Error("type requires 'selector'");
  if (text === undefined || text === null) throw new Error("type requires 'text'");
  const mode = String(params.mode || "set").toLowerCase();
  if (!TYPE_MODES.includes(mode)) throw new Error(`type: unknown mode ${JSON.stringify(params.mode)} (want set, append or keys)`);
  const tabId = await activeTabId(params);
  if (mode === "keys") return await typeKeys(tabId, selector, String(text));
  const r = await runPage(tabId, selector, "typeText", (sel) => [sel, text, mode]);
  if (!r || !r.found) throw notFound(selector, r);
  return withNote(`ok: ${mode === "append" ? "appended to" : "typed into"} <${r.tag}>${r.via === "textContent" ? " (the editor refused insertText; set its text directly)" : ""}`, r);
}

// typeKeys focuses the element (caret at the end) and types text as real
// input: Input.insertText per line, Enter between lines — so a newline in a
// chat box sends, as it would for a person. Focus emulation makes a
// background tab accept the input as if it were in front.
async function typeKeys(tabId, selector, text) {
  const loc = await locate(tabId, selector);
  const focus = async () => {
    await ensureHelpers(loc.target);
    const results = await chrome.scripting.executeScript({
      target: loc.target,
      func: (name, args) => globalThis.__aglinkPage[name](...args),
      args: ["focusForKeys", [loc.selector]],
    });
    return results && results[0] && results[0].result;
  };
  const r = await focus();
  if (!r || !r.found) throw notFound(selector, r);
  if (!chrome.debugger) throw new Error('type mode "keys" needs the debugger permission — reload the aglink-web extension, or use mode "set"');
  await withDebugger(tabId, async (target) => {
    try {
      await chrome.debugger.sendCommand(target, "Emulation.setFocusEmulationEnabled", { enabled: true });
    } catch (e) {
      // older Chrome: typing still works in the front tab
    }
    await focus(); // focus again now that the page counts as focused
    await sendKeys(target, text);
  });
  return withNote(`ok: typed ${text.length} chars as keystrokes into <${r.tag}>`, r);
}

// sendKeys types text into whatever has focus in the debugged tab.
async function sendKeys(target, text) {
  const lines = text.split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    if (i > 0) {
      const enter = { key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 };
      await chrome.debugger.sendCommand(target, "Input.dispatchKeyEvent", { type: "keyDown", text: "\r", unmodifiedText: "\r", ...enter });
      await chrome.debugger.sendCommand(target, "Input.dispatchKeyEvent", { type: "keyUp", ...enter });
    }
    if (lines[i]) await chrome.debugger.sendCommand(target, "Input.insertText", { text: lines[i] });
  }
}

// getValue reads an element's CURRENT value/text — the read-side counterpart
// to typeText/selectOption. get_page_text can't see this: an <input>'s value
// isn't part of document.body.innerText, so after a page's own JS rewrites a
// field (autocomplete, a calculated total, client-side validation reformatting
// what was typed) this is the only way to confirm what it actually holds now.
async function getValue(params) {
  const selector = params.selector;
  if (!selector) throw new Error("get_value requires 'selector'");
  const tabId = await activeTabId(params);
  const r = await runPage(tabId, selector, "getValue", (sel) => [sel]);
  if (!r || !r.found) throw notFound(selector, r);
  return withNote(`${selector} = ${JSON.stringify(r.value)}`, r);
}

// KEY_SPECS maps a key token to the {key, code, keyCode} triple a
// KeyboardEvent needs. Single characters not listed here are synthesized in
// keyCombo's page-side function instead (see there).
const KEY_SPECS = {
  enter: { key: "Enter", code: "Enter", keyCode: 13 },
  return: { key: "Enter", code: "Enter", keyCode: 13 },
  tab: { key: "Tab", code: "Tab", keyCode: 9 },
  esc: { key: "Escape", code: "Escape", keyCode: 27 },
  escape: { key: "Escape", code: "Escape", keyCode: 27 },
  space: { key: " ", code: "Space", keyCode: 32 },
  backspace: { key: "Backspace", code: "Backspace", keyCode: 8 },
  delete: { key: "Delete", code: "Delete", keyCode: 46 },
  del: { key: "Delete", code: "Delete", keyCode: 46 },
  up: { key: "ArrowUp", code: "ArrowUp", keyCode: 38 },
  down: { key: "ArrowDown", code: "ArrowDown", keyCode: 40 },
  left: { key: "ArrowLeft", code: "ArrowLeft", keyCode: 37 },
  right: { key: "ArrowRight", code: "ArrowRight", keyCode: 39 },
  home: { key: "Home", code: "Home", keyCode: 36 },
  end: { key: "End", code: "End", keyCode: 35 },
  pageup: { key: "PageUp", code: "PageUp", keyCode: 33 },
  pagedown: { key: "PageDown", code: "PageDown", keyCode: 34 },
};
for (let i = 1; i <= 12; i++) {
  KEY_SPECS["f" + i] = { key: "F" + i, code: "F" + i, keyCode: 111 + i };
}

// MOD_PROPS maps a modifier token to the KeyboardEventInit flag it sets.
const MOD_PROPS = {
  ctrl: "ctrlKey",
  control: "ctrlKey",
  alt: "altKey",
  shift: "shiftKey",
  meta: "metaKey",
  cmd: "metaKey",
  win: "metaKey",
  super: "metaKey",
};

// keyCombo dispatches a keydown+keyup pair — e.g. "enter", "ctrl+a", "esc" —
// to document.activeElement *within the page*, scoped to that tab only.
//
// This exists specifically so Tab/Enter/Escape/shortcuts inside a page don't
// have to go through aglink-screen's OS-level key() — which requires the
// browser window to have OS focus and sends the keystroke to whatever the OS
// thinks is focused, i.e. the whole browser, not just the page. That distinction
// is exactly what turned an attempted "close this dropdown" Escape into "close
// the entire Gmail compose window" (a real incident — see feedback memory on
// web selector fragility): Gmail's own global Escape handler caught an
// OS-level Escape meant only for an autocomplete popup.
//
// Caveat: dispatched KeyboardEvents are untrusted (isTrusted: false). Page JS
// keydown/keyup listeners (React, Gmail's own handlers, etc.) fire normally,
// but browser-native default actions tied to trusted input only — e.g. a
// plain <input> submitting its <form> on Enter with no JS handler — will NOT
// happen from this alone. Most modern interactive apps handle these keys in
// JS (which is exactly the case this tool targets), so this covers the
// common case; a bare native form submit may still need clicking the submit
// button instead.
async function keyCombo(params) {
  const combo = params.combo;
  if (!combo) throw new Error("key requires 'combo'");
  let tabId = params.tabId;
  if (!tabId) {
    const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!active) throw new Error("no active tab");
    tabId = active.id;
  }
  await ensureHelpers(tabId);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["keyCombo", [combo, KEY_SPECS, MOD_PROPS]],
  });
  const r = results && results[0] && results[0].result;
  if (!r || !r.ok) throw new Error((r && r.error) || "key failed");
  return `ok: pressed "${combo}" on <${r.tag}>`;
}

// scroll scrolls the window (or a specific scrollable element, if 'selector'
// is given) by pixel deltas. Note the sign convention is plain DOM scrollBy
// semantics — positive dy scrolls DOWN (content moves up) — the opposite of
// aglink-screen's scroll(), which mimics physical mouse-wheel notches (positive
// dy scrolls UP) since that one drives a real wheel event. This one sets
// scroll position directly via the DOM, so it follows the DOM's own convention
// instead.
async function scroll(params) {
  const dx = params.dx || 0;
  const dy = params.dy || 0;
  if (dx === 0 && dy === 0) throw new Error("scroll requires a non-zero dx or dy");
  const tabId = await activeTabId(params);
  const selector = params.selector || null;
  let r;
  if (selector) {
    r = await runPage(tabId, selector, "scroll", (sel) => [sel, dx, dy]);
  } else {
    await ensureHelpers(tabId);
    const results = await chrome.scripting.executeScript({
      target: { tabId },
      func: (name, args) => globalThis.__aglinkPage[name](...args),
      args: ["scroll", [selector, dx, dy]],
    });
    r = results && results[0] && results[0].result;
  }
  if (!r || !r.found) throw notFound(selector, r);
  return `ok: scrolled dx=${dx} dy=${dy}${selector ? ` on ${selector}` : ""}`;
}

// selectOption sets a native <select>'s value by option value or visible
// label and fires input+change (mirrors typeText's event dispatch, since
// setting .value directly doesn't trigger page JS on its own).
async function selectOption(params) {
  const selector = params.selector;
  const value = params.value;
  const label = params.label;
  if (!selector) throw new Error("select_option requires 'selector'");
  if (value === undefined && label === undefined) {
    throw new Error("select_option requires 'value' or 'label'");
  }
  const tabId = await activeTabId(params);
  const r = await runPage(tabId, selector, "selectOption", (sel) => [sel, value === undefined ? null : value, label === undefined ? null : label]);
  if (!r || !r.found) throw notFound(selector, r);
  if (r.isSelect === false) throw new Error(`element <${r.tag}> matched by ${selector} is not a <select>`);
  if (!r.matched) throw new Error(`no <option> matching value=${JSON.stringify(value)} label=${JSON.stringify(label)}`);
  return withNote(`ok: selected "${r.selected}"`, r);
}

// activateTab makes an existing tab the active tab of its window (and brings
// that window to the foreground). Unlike every other command here, tabId is
// NOT optional — "activate the currently active tab" is a no-op, so there's
// no sensible default to fall back to. Added after repeatedly needing to open
// a brand-new tab in this same session just to make an already-open one (a
// chrome://extensions tab, mid-reload) the foreground tab for a screenshot —
// this is the tool that should have been used instead.
async function activateTab(params) {
  const tabId = params.tabId;
  if (!tabId) throw new Error("activate_tab requires 'tabId'");
  const tab = await chrome.tabs.update(tabId, { active: true });
  await chrome.windows.update(tab.windowId, { focused: true });
  return `ok: activated tab ${tabId} — ${tab.title} — ${tab.url}`;
}

// getConsoleLogs reads the buffer console-capture.js maintains on the page's
// own window (MAIN world — see that file's comment for why isolated-world
// scripts can't see it). Useful for web debugging tasks ("did this click
// throw a JS error?") that get_page_text can't answer since console output
// isn't part of the rendered DOM at all.
async function getConsoleLogs(params) {
  let tabId = params.tabId;
  if (!tabId) {
    const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!active) throw new Error("no active tab");
    tabId = active.id;
  }
  const max = params.max || 50;
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    world: "MAIN",
    func: (n) => (window.__aglinkConsole || []).slice(-n),
    args: [max],
  });
  const logs = (results && results[0] && results[0].result) || [];
  if (logs.length === 0) return "(no console messages captured)";
  return logs.map((l) => `[${l.level}] ${l.text}`).join("\n");
}

// getNetworkRequests reads the buffer network-capture.js maintains on the
// page's own window (MAIN world — same rationale as getConsoleLogs: an
// isolated-world script has its own separate window and never sees the
// page's own fetch/XHR calls). The primary tool for reverse-engineering a web
// app's own AJAX API — what endpoint a button actually calls, with what
// payload, and what it returns — which get_page_text/get_html/eval can't see
// since it never touches the rendered DOM.
//
// full="true" switches the tab to the debugger's Network domain instead
// (startNetCapture): every request, response headers and bodies, until
// full="false" or NET_CAPTURE_IDLE_MS without a read.
async function getNetworkRequests(params) {
  let tabId = params.tabId;
  if (!tabId) {
    const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!active) throw new Error("no active tab");
    tabId = active.id;
  }
  const max = params.max || 50;
  const filter = (params.filter || "").toLowerCase();
  const full = params.full === undefined || params.full === null || params.full === "" ? null : String(params.full).toLowerCase();
  if (full !== null && /^(false|no|0|off)$/.test(full)) {
    return (await stopNetCapture(tabId))
      ? `ok: stopped full network capture on tab ${tabId} (debugger detached)`
      : `ok: no full network capture was running on tab ${tabId}`;
  }
  let started = false;
  if (full !== null && /^(true|yes|1|on)$/.test(full)) started = await startNetCapture(tabId);
  if (netCaptures.has(tabId)) {
    const body = await netCaptureText(tabId, max, filter);
    if (!started) return body;
    return `ok: full network capture started on tab ${tabId} — the debugger stays attached (Chrome shows its "started debugging" bar) until get_network_requests full="false", or ${NET_CAPTURE_IDLE_MS / 60000} minutes without a read. Reproduce the action, then call get_network_requests again (no need to repeat full).\n${body}`;
  }
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    world: "MAIN",
    func: (n, f) => {
      const all = window.__aglinkNetwork || [];
      const matched = f ? all.filter((e) => (e.url || "").toLowerCase().includes(f)) : all;
      return matched.slice(-n);
    },
    args: [max, filter],
  });
  const entries = (results && results[0] && results[0].result) || [];
  if (entries.length === 0) return "(no network requests captured)";
  return entries
    .map((e, i) => {
      const parts = [`${i} | ${e.type} | ${e.method} ${e.url}`];
      parts.push(e.error ? `ERROR: ${e.error}` : `-> ${e.status} (${e.durationMs}ms)`);
      if (e.requestBody) parts.push(`req=${JSON.stringify(e.requestBody)}`);
      if (e.responseBody) parts.push(`resp=${JSON.stringify(e.responseBody)}`);
      return parts.join(" | ");
    })
    .join("\n");
}

// ---- full network capture (get_network_requests full) ----------------------
//
// The page-script capture (network-capture.js) sees only fetch/XHR, and only
// bodies the page itself reads. The debugger's Network domain sees every
// request — documents, scripts, beacons, redirects, failures — with response
// headers, and can fetch any response body after the fact. It costs an
// attached debugger (and Chrome's bar), so it is opt-in and lets go by itself.

const netCaptures = new Map(); // tabId → { entries, byId, timer }
const NET_CAPTURE_KEEP = 500;
const NET_CAPTURE_IDLE_MS = 5 * 60 * 1000;
const NET_BODY_MAX = 4000;
const NET_HEADERS_MAX = 800;

async function startNetCapture(tabId) {
  if (netCaptures.has(tabId)) return false;
  if (!chrome.debugger) throw new Error("full network capture needs the debugger permission — reload the aglink-web extension");
  const target = { tabId };
  await attachDebugger(target);
  // Registered before Network.enable, so the first events are not lost.
  netCaptures.set(tabId, { entries: [], byId: new Map(), timer: null });
  try {
    await chrome.debugger.sendCommand(target, "Network.enable", { maxPostDataSize: 65536 });
  } catch (e) {
    netCaptures.delete(tabId);
    await detachDebugger(target);
    throw e;
  }
  touchNetCapture(tabId);
  return true;
}

// touchNetCapture re-arms the idle stop.
function touchNetCapture(tabId) {
  const c = netCaptures.get(tabId);
  if (!c) return;
  clearTimeout(c.timer);
  c.timer = setTimeout(() => stopNetCapture(tabId), NET_CAPTURE_IDLE_MS);
}

async function stopNetCapture(tabId) {
  const c = netCaptures.get(tabId);
  if (!c) return false;
  clearTimeout(c.timer);
  netCaptures.delete(tabId);
  const target = { tabId };
  try {
    await chrome.debugger.sendCommand(target, "Network.disable");
  } catch (e) {
    // tab gone or already detached
  }
  // A certificate bypass that needs the debugger keeps it.
  if (!overrideCertTabs.has(tabId)) await detachDebugger(target);
  return true;
}

function onNetworkEvent(source, method, params) {
  const c = source && netCaptures.get(source.tabId);
  if (!c || !params) return;
  switch (method) {
    case "Network.requestWillBeSent": {
      const rq = params.request || {};
      if (/^data:/.test(rq.url || "")) return;
      const e = {
        id: params.requestId, type: params.type || "", method: rq.method || "", url: rq.url || "",
        postData: rq.postData || "", status: 0, headers: null, error: "", start: params.timestamp || 0, end: 0,
      };
      c.entries.push(e);
      c.byId.set(params.requestId, e); // a redirect reuses the id: the newest hop wins
      if (c.entries.length > NET_CAPTURE_KEEP) {
        const drop = c.entries.shift();
        if (c.byId.get(drop.id) === drop) c.byId.delete(drop.id);
      }
      return;
    }
    case "Network.responseReceived": {
      const e = c.byId.get(params.requestId);
      if (!e) return;
      const rs = params.response || {};
      e.status = rs.status || 0;
      e.headers = rs.headers || null;
      e.mime = rs.mimeType || "";
      if (params.type) e.type = params.type;
      return;
    }
    case "Network.loadingFinished":
    case "Network.loadingFailed": {
      const e = c.byId.get(params.requestId);
      if (!e) return;
      e.end = params.timestamp || 0;
      if (method === "Network.loadingFailed") e.error = params.canceled ? "canceled" : params.errorText || "failed";
      return;
    }
  }
}

// netCaptureText renders the capture in the same line format as an app
// window's get_network_requests (applogs.go networkText), plus response
// headers; bodies are fetched for finished text responses.
async function netCaptureText(tabId, max, filter) {
  const c = netCaptures.get(tabId);
  touchNetCapture(tabId);
  const all = c ? c.entries : [];
  const entries = (filter ? all.filter((e) => e.url.toLowerCase().includes(filter)) : all).slice(-max);
  if (entries.length === 0) return "(no requests captured yet — capture runs from when full network capture started; reproduce the action now)";
  const clip = (s, n) => (s.length > n ? s.slice(0, n) + "…" : s);
  const lines = [];
  for (let i = 0; i < entries.length; i++) {
    const e = entries[i];
    const parts = [`${i} | ${e.type.toLowerCase()} | ${e.method} ${e.url}`];
    if (e.error) parts.push(`ERROR: ${e.error}`);
    else if (!e.status) parts.push("-> (pending)");
    else parts.push(`-> ${e.status}${e.end && e.start ? ` (${Math.round((e.end - e.start) * 1000)}ms)` : ""}`);
    if (e.postData) parts.push(`req=${JSON.stringify(clip(e.postData, NET_BODY_MAX))}`);
    if (e.headers) parts.push(`headers=${clip(JSON.stringify(e.headers), NET_HEADERS_MAX)}`);
    const textual = !/^(image|font|video|audio)\//.test(e.mime || "") && !/^(Image|Font|Media|Stylesheet|Script)$/.test(e.type);
    if (e.end && !e.error && e.status && textual) {
      try {
        const b = await chrome.debugger.sendCommand({ tabId }, "Network.getResponseBody", { requestId: e.id });
        if (b && b.base64Encoded) parts.push(`resp=(binary, ${Math.floor(((b.body || "").length * 3) / 4)} bytes)`);
        else if (b && b.body) parts.push(`resp=${JSON.stringify(clip(b.body, NET_BODY_MAX))}`);
      } catch (err) {
        // evicted from the browser's cache, or a redirect / preflight without a body
      }
    }
    lines.push(parts.join(" | "));
  }
  return lines.join("\n");
}

// ---- popups (tabs a page opens) -----------------------------------------------
//
// A click that opens a new tab (target=_blank, window.open) leaves the agent
// on the old one without knowing. Tabs record their opener; click reports
// those that appeared while it ran, so the caller (or run_steps with
// follow_popup) can move to the new tab.

const openedTabs = []; // { id, opener, at }

function onTabCreated(tab) {
  if (!tab || !tab.openerTabId || openedTabs.some((t) => t.id === tab.id)) return;
  openedTabs.push({ id: tab.id, opener: tab.openerTabId, at: Date.now() });
  if (openedTabs.length > 50) openedTabs.shift();
}

// POPUP_SETTLE_MS is how long a click waits for a tab it opened to show up.
function popupSettleMs() {
  return 100;
}

// openedTabsText is the line(s) a click appends for tabs its tab opened
// since `since`; "" when there are none.
async function openedTabsText(tabId, since) {
  const pick = () => openedTabs.filter((t) => t.opener === tabId && t.at >= since);
  let got = pick();
  if (!got.length) {
    await new Promise((r) => setTimeout(r, popupSettleMs()));
    got = pick();
  }
  let out = "";
  for (const t of got) {
    let url = "";
    try {
      const tab = await chrome.tabs.get(t.id);
      url = (tab && (tab.pendingUrl || tab.url)) || "";
    } catch (e) {
      continue; // closed again already
    }
    out += `\nopened new tab id=${t.id} url=${url} — pass tabId=${t.id} to work in it`;
  }
  return out;
}

// reloadExtension restarts the extension itself (chrome.runtime.reload()) —
// a dev-workflow convenience added after manually doing the
// navigate-to-chrome://extensions, screenshot, click-reload dance about 8
// times in one session whenever background.js changed. From here on, a
// background.js/manifest.json edit can be picked up with a single call
// instead of that whole sequence. The reload is delayed slightly so this
// function's "ok" response actually reaches the caller over the WebSocket
// before the service worker context is torn down — without the delay the
// reload could win the race and the caller would see a connection-closed
// error instead of a clean acknowledgement (still harmless, just less clean).
// Not useful to an end user driving their own browsing — only relevant when
// actively developing this extension.
async function reloadExtension() {
  setTimeout(() => chrome.runtime.reload(), 150);
  return "ok: reloading extension in 150ms";
}

async function closeTab(params) {
  let tabId = params.tabId;
  if (!tabId) {
    const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!active) throw new Error("no active tab");
    tabId = active.id;
  }
  await chrome.tabs.remove(tabId);
  return `ok: closed tab ${tabId}`;
}

// ---- certificate warnings ---------------------------------------------------
//
// An https site with a bad certificate (self-signed, internal CA, expired, wrong
// name) makes Chrome show its own warning page instead of the site. That page is
// a chrome-error:// document: executeScript cannot touch it, so every tool fails
// on it and its "Proceed" link cannot be clicked the usual way.
//
// Detection: webNavigation reports the main-frame net error (net::ERR_CERT_…)
// for the load the warning replaced; it is kept per tab, in memory and in
// storage.session so a restarted service worker still knows.
//
// Getting past it: proceed_insecure attaches chrome.debugger — the one extension
// API that reaches the warning page — and tries, in order, what a person would
// do (the page's own proceed link), Chrome's keyboard bypass for warnings that
// have no such link (HSTS), and finally ignoring certificate errors outright.
// The first two are remembered by Chrome for the host for the rest of the
// browser session, exactly as if the user had clicked through.

const navErrors = new Map(); // tabId → { url, error }
const debuggerTabs = new Set(); // tabs this worker holds chrome.debugger on
const BYPASS_KEYS = "thisisunsafe";

function isCertError(error) {
  return /^net::ERR_(CERT_|SSL_|BAD_SSL_)/.test(error || "") || error === "net::ERR_INSECURE_RESPONSE";
}

// isErrorPageMessage matches what executeScript throws on a browser error or
// warning page — the only documents it refuses with these words.
function isErrorPageMessage(msg) {
  return /showing error page|chrome-error:/i.test(msg || "");
}

function hostOf(url) {
  const m = /^[a-z][a-z0-9+.-]*:\/\/([^/?#]+)/i.exec(url || "");
  return m ? m[1] : url || "";
}

async function rememberNavError(tabId, value) {
  if (value) navErrors.set(tabId, value);
  else navErrors.delete(tabId);
  try {
    const key = `navErr:${tabId}`;
    if (value) await chrome.storage.session.set({ [key]: value });
    else await chrome.storage.session.remove(key);
  } catch (e) {
    // storage.session only carries this across a worker restart; never block on it
  }
}

async function lastNavError(tabId) {
  if (navErrors.has(tabId)) return navErrors.get(tabId);
  try {
    const key = `navErr:${tabId}`;
    const got = await chrome.storage.session.get(key);
    return (got && got[key]) || null;
  } catch (e) {
    return null;
  }
}

// probePage runs a no-op in the tab: "ok" if the page is scriptable, "error"
// if it is a browser error/warning page, "other" for anything else (tab gone,
// chrome:// page) — so a failure for an unrelated reason is never mistaken for
// having left the warning.
async function probePage(tabId) {
  try {
    await chrome.scripting.executeScript({ target: { tabId }, func: () => 1 });
    return "ok";
  } catch (e) {
    return isErrorPageMessage(String(e && e.message ? e.message : e)) ? "error" : "other";
  }
}

// certWarning returns the line navigate appends when the tab ended up on a
// warning page rather than the site, or "" when it did not.
async function certWarning(tabId) {
  if ((await probePage(tabId)) !== "error") return "";
  let err = await lastNavError(tabId);
  if (!err) {
    // The error event can trail the load's "complete" by a moment.
    await new Promise((r) => setTimeout(r, 300));
    err = await lastNavError(tabId);
  }
  if (err && !isCertError(err.error)) {
    return `warning: the page failed to load (${err.error} for ${err.url}) — the tab shows Chrome's error page`;
  }
  const what = err ? `certificate warning on tab ${tabId} (${err.error}, host ${hostOf(err.url)})` : `error or warning page on tab ${tabId}`;
  return `warning: ${what} — Chrome is showing its warning instead of the page. To continue past it, call proceed_insecure with tabId=${tabId}`;
}

// explainError turns executeScript's opaque "showing error page" into what is
// actually on screen and what to do about it.
async function explainError(params, msg) {
  if (!isErrorPageMessage(msg)) return msg;
  let tabId = params.tabId;
  if (!tabId) {
    try {
      const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
      tabId = active && active.id;
    } catch (e) {
      // fall through with the raw message
    }
  }
  if (!tabId) return msg;
  const err = await lastNavError(tabId);
  if (err && isCertError(err.error)) {
    return `tab ${tabId} is showing Chrome's certificate warning (${err.error}, host ${hostOf(err.url)}), not the page — call proceed_insecure with tabId=${tabId} to continue past it`;
  }
  if (err) return `tab ${tabId} is showing Chrome's error page: ${err.error} loading ${err.url}`;
  return `${msg} — tab ${tabId} is on a browser error or warning page; if it is a certificate warning, call proceed_insecure with tabId=${tabId}`;
}

function bypassFallback(tabId, why) {
  return (
    `${why}. Other ways past it: ` +
    `(1) with aglink-screen, bring this Chrome window (tab ${tabId}) to the front and type the keys "thisisunsafe" while the warning is showing — Chrome's own bypass; ` +
    `(2) ask the user to trust the site's certificate once with "aglink-web trust-cert <url>", which removes the warning for good; ` +
    `(3) list the host in ~/.aglink/aglink-web-insecure-hosts so navigate continues past it by itself`
  );
}

async function attachDebugger(target) {
  if (debuggerTabs.has(target.tabId)) return;
  try {
    await chrome.debugger.attach(target, "1.3");
  } catch (e) {
    // A worker restart forgets the set but not the attachment.
    if (!/already attached/i.test(String(e && e.message ? e.message : e))) throw e;
  }
  debuggerTabs.add(target.tabId);
}

async function detachDebugger(target) {
  // A full network capture holds the debugger until it is stopped; a
  // borrowed attach (dialog check, upload, typing) must not end it.
  if (netCaptures.has(target.tabId)) return;
  debuggerTabs.delete(target.tabId);
  overrideCertTabs.delete(target.tabId);
  // Detached, the tab's dialog events stop arriving; what we knew goes stale.
  openDialogs.delete(target.tabId);
  try {
    await chrome.debugger.detach(target);
  } catch (e) {
    // already gone
  }
}

// leaveWarningTimeoutMs is how long each bypass step gets to bring the site
// up. A function so tests can shorten it.
function leaveWarningTimeoutMs() {
  return 8000;
}

// waitLeftWarning polls until the tab holds a scriptable page again (the site
// loaded) or the time runs out.
async function waitLeftWarning(tabId, timeoutMs = leaveWarningTimeoutMs()) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 250));
    if ((await probePage(tabId)) === "ok") {
      await waitForComplete(tabId, 10000);
      return true;
    }
  }
  return false;
}

// Tabs whose certificate errors this worker answers with "continue".
const overrideCertTabs = new Set();

// ignoreCertErrors makes the attached tab load past bad certificates. The
// modern Security.setIgnoreCertificateErrors is browser-level and an
// extension's debugger does not get it ("wasn't found"), so the older per-tab
// form is the fallback: Chrome raises Security.certificateError for each bad
// certificate and waits for handleCertificateError (see onCertificateError).
async function ignoreCertErrors(target) {
  try {
    await chrome.debugger.sendCommand(target, "Security.setIgnoreCertificateErrors", { ignore: true });
    return;
  } catch (e) {
    // fall back below
  }
  await chrome.debugger.sendCommand(target, "Security.enable");
  await chrome.debugger.sendCommand(target, "Security.setOverrideCertificateErrors", { override: true });
  overrideCertTabs.add(target.tabId);
}

function onCertificateError(source, method, params) {
  if (method !== "Security.certificateError" || !source || !overrideCertTabs.has(source.tabId)) return;
  chrome.debugger
    .sendCommand(source, "Security.handleCertificateError", { eventId: params.eventId, action: "continue" })
    .catch(() => {});
}

// CERT_BYPASS_STEPS run in order until one gets the site on screen.
const CERT_BYPASS_STEPS = [
  {
    // What a person clicks under "Advanced". Absent on HSTS hosts.
    name: "proceed link",
    run: async (target) => {
      const r = await chrome.debugger.sendCommand(target, "Runtime.evaluate", {
        expression: `(() => { const a = document.getElementById("proceed-link"); if (!a) return false; a.click(); return true; })()`,
        returnByValue: true,
        userGesture: true,
      });
      return !!(r && r.result && r.result.value === true);
    },
  },
  {
    // Chrome's keyboard bypass on its warning page; the one way past a warning
    // that offers no proceed link.
    name: "thisisunsafe keys",
    run: async (target) => {
      for (const ch of BYPASS_KEYS) {
        const key = { key: ch, code: `Key${ch.toUpperCase()}`, windowsVirtualKeyCode: ch.toUpperCase().charCodeAt(0) };
        await chrome.debugger.sendCommand(target, "Input.dispatchKeyEvent", { type: "keyDown", text: ch, unmodifiedText: ch, ...key });
        await chrome.debugger.sendCommand(target, "Input.dispatchKeyEvent", { type: "keyUp", ...key });
      }
      return true;
    },
  },
  {
    // Tell this tab to ignore certificate errors and load the site again.
    // Nothing is remembered by Chrome, so the debugger has to stay attached or
    // the site's own later requests would hit the same error.
    name: "ignore certificate errors",
    keepAttached: true,
    note:
      "certificate errors stay ignored in this tab only while aglink-web's debugger remains attached (Chrome shows a 'started debugging this browser' bar); closing the tab or cancelling that bar ends it",
    run: async (target, err) => {
      const tab = await chrome.tabs.get(target.tabId);
      await ignoreCertErrors(target);
      await chrome.debugger.sendCommand(target, "Page.navigate", { url: (err && err.url) || tab.url });
      return true;
    },
  },
];

// CERT_STUCK starts the error when no in-browser step got past the warning.
// The daemon keys its last resort off it — typing "thisisunsafe" into the
// Chrome window from outside the browser — so keep the wording.
const CERT_STUCK = "certificate warning is still showing";

// proceedInsecure continues past Chrome's certificate warning in a tab, the
// way a user would by clicking "Proceed to … (unsafe)". It refuses on any
// other kind of error page: there is nothing there to proceed to.
//
// As of Chrome 14x an extension's debugger can neither attach to the warning
// page ("Cannot attach to this target") nor use the Security domain, so in
// practice every in-browser step is skipped and the daemon finishes the job
// by typing Chrome's bypass keys (see CERT_STUCK); it then calls back with
// waitOnly to learn whether the site came up. The steps stay for browsers
// that do allow them.
async function proceedInsecure(params) {
  const tabId = await activeTabId(params);
  if (params.waitOnly) {
    if (await waitLeftWarning(tabId)) {
      await rememberNavError(tabId, null);
      const tab = await chrome.tabs.get(tabId);
      return `ok: continued past the certificate warning for ${hostOf(tab.url)} via thisisunsafe typed into the Chrome window — ${tab.title || ""} — ${tab.url || ""}`;
    }
    throw new Error(bypassFallback(tabId, `${CERT_STUCK} on tab ${tabId} after typing thisisunsafe into it`));
  }
  if ((await probePage(tabId)) !== "error") {
    const tab = await chrome.tabs.get(tabId);
    return `ok: tab ${tabId} is not on a warning page — nothing to do — ${tab.title || ""} — ${tab.url || ""}`;
  }
  const err = await lastNavError(tabId);
  if (err && !isCertError(err.error)) {
    throw new Error(`tab ${tabId} shows Chrome's error page for ${err.error}, not a certificate warning — there is nothing to proceed past`);
  }
  if (!chrome.debugger) {
    throw new Error(bypassFallback(tabId, "the extension has no debugger permission yet"));
  }
  const host = hostOf(err ? err.url : (await chrome.tabs.get(tabId)).url);
  const target = { tabId };
  // Attaching to the warning page itself fails in current Chrome; the steps
  // that need it are then skipped and the last one attaches elsewhere.
  let attachError = "";
  try {
    await attachDebugger(target);
  } catch (e) {
    attachError = String(e && e.message ? e.message : e);
  }

  let keep = false;
  const tried = [];
  try {
    for (const step of CERT_BYPASS_STEPS) {
      if (attachError) break;
      let applied = false;
      try {
        applied = await step.run(target, err);
      } catch (e) {
        tried.push(`${step.name} (${e && e.message ? e.message : e})`);
        continue;
      }
      if (!applied) continue;
      tried.push(step.name);
      if (await waitLeftWarning(tabId)) {
        keep = !!step.keepAttached;
        await rememberNavError(tabId, null);
        const tab = await chrome.tabs.get(tabId);
        let out = `ok: continued past the certificate warning for ${host} via ${step.name}${err ? ` (was ${err.error})` : ""} — ${tab.title || ""} — ${tab.url || ""}`;
        if (keep) out += `\nnote: ${step.note}`;
        return out;
      }
    }
  } finally {
    if (!keep) await detachDebugger(target);
  }
  let why = `${CERT_STUCK} on tab ${tabId}`;
  if (tried.length) why += ` after trying: ${tried.join(", ")}`;
  if (attachError) why += ` (the browser's debugger cannot reach its warning page: ${attachError})`;
  throw new Error(bypassFallback(tabId, why));
}

// pageSnapshot runs the shared outline (page-actions.js P.snapshot): visible
// structure and controls, each control numbered for "ref=eN" selectors.
// Same-origin iframes are outlined inline by the page itself; each
// cross-origin one is outlined here by running in that frame, appended as its
// own section — its refs are the frame's own, reached with
// "frame=ref=<iframe ref> >> ref=eN".
async function pageSnapshot(params) {
  const tabId = await activeTabId(params);
  const maxLines = params.maxLines || 200;
  await ensureHelpers(tabId);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["snapshot", [maxLines, { frames: true }]],
  });
  const r = results && results[0] && results[0].result;
  const text = r && typeof r === "object" ? r.text : r;
  if (typeof text !== "string") throw new Error("page_snapshot returned no result (the page may block script injection)");
  const xframes = r && typeof r === "object" && Array.isArray(r.xframes) ? r.xframes : [];
  let out = text;
  for (const xf of xframes.slice(0, 5)) {
    const id = typeof xf.frameId === "number" ? xf.frameId : await frameIdByUrl(tabId, 0, xf.src);
    if (id == null) continue;
    try {
      const target = frameTarget(tabId, id);
      await ensureHelpers(target);
      const fr = await chrome.scripting.executeScript({
        target,
        func: (name, args) => globalThis.__aglinkPage[name](...args),
        args: ["snapshot", [Math.min(maxLines, 100)]],
      });
      const s = fr && fr[0] && fr[0].result;
      if (typeof s !== "string") continue;
      const body = s.split("\n").slice(1).join("\n");
      out += `\n[iframe ref=${xf.ref} ${xf.src} — its controls: selector "frame=ref=${xf.ref} >> ref=eN"]\n${body || "(nothing visible)"}`;
    } catch (e) {
      // a frame the extension may not script
    }
  }
  return out;
}

// ---- file upload -------------------------------------------------------------
//
// Page script cannot put files into an <input type="file">; the DevTools
// DOM.setFileInputFiles can (it also fires input/change). The element is found
// with the shared resolver in the isolated world and marked; the debugger then
// finds the mark from the page's own world — walking open shadow roots too,
// which DOM.querySelector would not — and sets the files. The daemon has
// already checked that every path is a file on this PC.

async function uploadFile(params) {
  const tabId = await activeTabId(params);
  const selector = params.selector;
  const files = Array.isArray(params.paths) ? params.paths : [];
  if (!selector || files.length === 0) throw new Error("upload_file requires 'selector' and 'paths'");
  await ensureHelpers(tabId);
  const mark = "u" + Math.random().toString(36).slice(2, 10);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (sel, m) => {
      const r = globalThis.__aglink;
      const el = r && r.resolve ? r.resolve(sel) : document.querySelector(sel);
      if (!el) return { found: false, hint: r && r.nearMiss ? r.nearMiss(sel) : "" };
      const ok = el.tagName === "INPUT" && el.type === "file";
      if (ok) el.setAttribute("data-aglink-upload", m);
      return { found: true, ok, tag: el.tagName.toLowerCase(), multiple: !!el.multiple };
    },
    args: [selector, mark],
  });
  const r = (results && results[0] && results[0].result) || { found: false };
  if (!r.found) throw notFound(selector, r);
  if (!r.ok) {
    throw new Error(`element <${r.tag}> matched by ${selector} is not an <input type="file"> — target the file input itself (often hidden), e.g. input[type=file]`);
  }
  if (files.length > 1 && !r.multiple) throw new Error(`this file input takes one file, ${files.length} given`);

  try {
    await withDebugger(tabId, async (target) => {
      const found = await chrome.debugger.sendCommand(target, "Runtime.evaluate", {
        expression: `(() => {
          const m = ${JSON.stringify(mark)};
          const walk = (root) => {
            const hit = root.querySelector('[data-aglink-upload="' + m + '"]');
            if (hit) return hit;
            for (const el of root.querySelectorAll("*")) {
              if (el.shadowRoot) { const h = walk(el.shadowRoot); if (h) return h; }
            }
            return null;
          };
          return walk(document);
        })()`,
        returnByValue: false,
      });
      const objectId = found && found.result && found.result.objectId;
      if (!objectId) throw new Error("the marked file input disappeared before the files could be set");
      await chrome.debugger.sendCommand(target, "DOM.setFileInputFiles", { files, objectId });
    });
  } finally {
    await chrome.scripting
      .executeScript({
        target: { tabId },
        func: (m) => document.querySelectorAll(`[data-aglink-upload="${m}"]`).forEach((e) => e.removeAttribute("data-aglink-upload")),
        args: [mark],
      })
      .catch(() => {});
  }
  const names = files.map((f) => String(f).split(/[\\/]/).pop());
  return `ok: attached ${files.length} file(s): ${names.join(", ")}`;
}

// ---- JavaScript dialogs (alert / confirm / prompt) --------------------------
//
// A dialog freezes the page's JavaScript, so executeScript on that tab just
// waits until someone closes it — every tool used to sit out the daemon's 30s
// timeout and fail with nothing to show for it, and the dialog itself is not
// in the DOM to be read or clicked.
//
// chrome.debugger reaches it: Page.javascriptDialogOpening says what is up and
// Page.handleJavaScriptDialog answers it. The debugger is attached only while
// looking (Chrome shows its "started debugging" bar meanwhile), never kept.
// Where it cannot answer, the daemon presses the dialog's keys itself (Enter /
// Esc, typed text for prompt) — see DIALOG_NEEDS_KEYS.

const openDialogs = new Map(); // tabId → { type, message, defaultPrompt, url }

// DIALOG_NEEDS_KEYS starts handle_dialog's error when the debugger could not
// answer; the daemon keys its keyboard fallback off it. Keep the wording.
const DIALOG_NEEDS_KEYS = "dialog needs the keyboard";

// How long a tool may run before the tab is checked for a dialog. Most tools
// answer in well under a second; a function so tests can shorten it.
function dialogCheckAfterMs() {
  return 2500;
}

// Tools that run script in the tab, and so freeze behind a dialog.
const DIALOG_WATCHED = new Set([
  "get_page_text", "element_exists", "click", "double_click", "hover", "drag", "get_html",
  "query_all", "eval", "get_attribute", "list_elements", "wait_for_element", "type",
  "get_value", "key", "scroll", "select_option", "get_console_logs", "get_network_requests",
]);

function onDialogEvent(source, method, params) {
  if (!source || !source.tabId) return;
  if (method === "Page.javascriptDialogOpening") {
    openDialogs.set(source.tabId, {
      type: params.type,
      message: params.message || "",
      defaultPrompt: params.defaultPrompt || "",
      url: params.url || "",
    });
  } else if (method === "Page.javascriptDialogClosed") {
    openDialogs.delete(source.tabId);
  }
}

function dialogLine(d) {
  // Chrome announces a dialog only to a debugger that was attached when it
  // opened, and this one attaches after. The page is frozen, so its text is
  // not readable from here; the screen still shows it.
  if (d.type === "unknown") return "a dialog (its text is not readable from the browser — aglink-screen can read it off the screen)";
  let s = `${d.type}: ${JSON.stringify(d.message)}`;
  if (d.type === "prompt" && d.defaultPrompt) s += ` (default ${JSON.stringify(d.defaultPrompt)})`;
  return s;
}

// withDebugger runs fn with the debugger on the tab, detaching afterwards
// unless it was already attached for something else (a certificate bypass).
async function withDebugger(tabId, fn) {
  const target = { tabId };
  const had = debuggerTabs.has(tabId);
  await attachDebugger(target);
  try {
    return await fn(target);
  } finally {
    if (!had) await detachDebugger(target);
  }
}

// findDialog reports the dialog open in an attached tab, or null. Enabling
// the Page domain makes Chrome announce a dialog that is already showing; if
// it stays silent but the page does not answer a trivial evaluation, a dialog
// is up all the same, just unnamed.
async function findDialog(target) {
  // Page.enable itself waits on the renderer, which a dialog has frozen: it
  // is never awaited for long. The announcement it triggers arrives as an
  // event regardless.
  const enabled = await answersWithin(chrome.debugger.sendCommand(target, "Page.enable"), 500);
  if (!openDialogs.has(target.tabId)) await new Promise((r) => setTimeout(r, 200));
  if (openDialogs.has(target.tabId)) return openDialogs.get(target.tabId);
  const answered =
    enabled && (await answersWithin(chrome.debugger.sendCommand(target, "Runtime.evaluate", { expression: "1", returnByValue: true }), 800));
  if (openDialogs.has(target.tabId)) return openDialogs.get(target.tabId);
  return answered ? null : { type: "unknown", message: "", defaultPrompt: "", url: "" };
}

// answersWithin reports whether a promise settles (either way) within ms.
function answersWithin(promise, ms) {
  let timer;
  return Promise.race([
    promise.then(() => true, () => true),
    new Promise((r) => (timer = setTimeout(() => r(false), ms))),
  ]).finally(() => clearTimeout(timer));
}

// withDialogWatch runs a tool, and if it has not answered after a moment,
// looks for a dialog in its tab: when there is one the tool fails at once
// with what the dialog says, instead of waiting out the timeout. The tool's
// own call keeps running and completes once the dialog is answered.
async function withDialogWatch(method, params, run) {
  const pending = run();
  if (!DIALOG_WATCHED.has(method)) return pending;
  const SLOW = {};
  let timer;
  const first = await Promise.race([
    pending,
    new Promise((r) => (timer = setTimeout(() => r(SLOW), dialogCheckAfterMs()))),
  ]).finally(() => clearTimeout(timer));
  if (first !== SLOW) return first;

  let d = null;
  try {
    const tabId = await activeTabId(params);
    d = await withDebugger(tabId, findDialog);
    if (d) {
      pending.catch(() => {});
      throw new Error(`dialog open on tab ${tabId}: ${dialogLine(d)} — call handle_dialog to answer it (accept="false" for Cancel)`);
    }
  } catch (e) {
    if (d) throw e;
    // Could not look (no debugger); keep waiting for the tool itself.
  }
  return pending;
}

async function dialogStatus(params) {
  const tabId = await activeTabId(params);
  const d = await withDebugger(tabId, findDialog);
  return d ? `${dialogLine(d)} (tab ${tabId})` : "no dialog open";
}

// handleDialog presses OK (accept, the default) or Cancel, typing promptText
// into a prompt() first.
async function handleDialog(params) {
  const tabId = await activeTabId(params);
  const accept = !["false", "no", "0"].includes(String(params.accept === undefined ? "" : params.accept).toLowerCase());
  const promptText = params.prompt_text || "";
  let d = null;
  try {
    return await withDebugger(tabId, async (target) => {
      d = await findDialog(target);
      if (!d) throw new Error("no dialog open");
      const args = { accept };
      if (promptText) args.promptText = promptText;
      let timer;
      await Promise.race([
        chrome.debugger.sendCommand(target, "Page.handleJavaScriptDialog", args),
        new Promise((_, rej) => (timer = setTimeout(() => rej(new Error("the debugger did not answer the dialog in time")), 3000))),
      ]).finally(() => clearTimeout(timer));
      openDialogs.delete(tabId);
      return `ok: ${accept ? "accepted" : "dismissed"} ${dialogLine(d)}`;
    });
  } catch (e) {
    const msg = String(e && e.message ? e.message : e);
    if (msg === "no dialog open") throw e;
    throw new Error(`${DIALOG_NEEDS_KEYS} on tab ${tabId}${d ? ` (${dialogLine(d)})` : ""}: ${msg}`);
  }
}

// navigateLoadTimeoutMs is how long navigate waits for the page to load. A
// function so tests can shorten it.
function navigateLoadTimeoutMs() {
  return 15000;
}

// waitForComplete resolves true when the tab finishes loading, or false after
// a timeout — so a slow/hung page never blocks the command indefinitely, and
// the caller can say the page is still loading.
//
// A fast page can reach "complete" before onUpdated is even attached, so the
// event alone would be missed and we'd wait out the whole timeout. To catch
// that, after attaching the listener we poll the tab's current status once: if
// it's already "complete", finish immediately. The listener still covers the
// normal (still-loading) case.
function waitForComplete(tabId, timeoutMs = 15000) {
  return new Promise((resolve) => {
    let done = false;
    let timer;
    const finish = (loaded) => {
      if (done) return;
      done = true;
      chrome.tabs.onUpdated.removeListener(listener);
      clearTimeout(timer);
      resolve(loaded);
    };
    const listener = (id, info) => {
      if (id === tabId && info.status === "complete") finish(true);
    };
    chrome.tabs.onUpdated.addListener(listener);
    timer = setTimeout(() => finish(false), timeoutMs);
    // Guard against the load finishing before the listener was attached.
    chrome.tabs.get(tabId).then((tab) => {
      if (tab && tab.status === "complete") finish(true);
    }).catch(() => {});
  });
}

// ---- lifecycle --------------------------------------------------------------

// Connect on install and on browser startup, and keep a heartbeat alarm so the
// service worker is periodically revived to re-establish a dropped connection.
// 0.5 minutes is the shortest period chrome.alarms allows (Chrome 120+; older
// versions clamp it to 1 minute), so a stopped worker is back within ~30s at
// worst. While a link is up the worker does not stop at all: the daemon's 10s
// pings reset the idle timer, and an open native port keeps it alive. A worker
// that is awake retries on its own 1-5s timers, well inside the daemon's 20s
// wait for a call.
chrome.runtime.onInstalled.addListener(connect);
chrome.runtime.onStartup.addListener(connect);
chrome.alarms.create("aglink-web-keepalive", { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === "aglink-web-keepalive") reconnectNow();
});

// Skip the backoff when the network comes back or the user is using the
// browser: these events also wake a stopped worker, which then reconnects at
// once instead of waiting for the alarm.
if (typeof self !== "undefined" && self.addEventListener) {
  self.addEventListener("online", reconnectNow);
}
for (const ev of [chrome.tabs.onActivated, chrome.tabs.onCreated, chrome.windows && chrome.windows.onFocusChanged]) {
  if (ev && ev.addListener) ev.addListener(() => reconnectNow());
}

// Reconnect immediately when the port is changed from the options page. Drop the
// current socket first, clearing our reference so its onclose won't schedule a
// competing reconnect (same "superseded socket stays quiet" rule as elsewhere).
// The native transport does not use the port (the host reads the daemon's port
// file), so it is left alone.
chrome.storage.onChanged.addListener((changes, area) => {
  if (area !== "local" || !changes.port || nmPort) return;
  const old = ws;
  ws = null;
  if (old) {
    try {
      old.close();
    } catch (e) {
      // ignore
    }
  }
  backoffMs = MIN_BACKOFF_MS;
  connect();
});

// Track each tab's last main-frame load error, so a certificate warning can be
// told apart from any other error page (see "certificate warnings").
if (chrome.webNavigation) {
  chrome.webNavigation.onBeforeNavigate.addListener((d) => {
    if (d.frameId === 0) rememberNavError(d.tabId, null);
  });
  chrome.webNavigation.onErrorOccurred.addListener((d) => {
    if (d.frameId === 0) rememberNavError(d.tabId, { url: d.url, error: d.error });
  });
}
if (chrome.tabs.onRemoved) {
  chrome.tabs.onRemoved.addListener((tabId) => {
    rememberNavError(tabId, null);
    const c = netCaptures.get(tabId);
    if (c) clearTimeout(c.timer);
    netCaptures.delete(tabId);
  });
}
if (chrome.tabs.onCreated && chrome.tabs.onCreated.addListener) {
  chrome.tabs.onCreated.addListener(onTabCreated);
}
if (chrome.debugger) {
  chrome.debugger.onDetach.addListener((source) => {
    if (source && source.tabId) {
      debuggerTabs.delete(source.tabId);
      overrideCertTabs.delete(source.tabId);
      openDialogs.delete(source.tabId);
      // Detached from outside (the user cancelled Chrome's bar, tab closed).
      const c = netCaptures.get(source.tabId);
      if (c) clearTimeout(c.timer);
      netCaptures.delete(source.tabId);
    }
  });
  if (chrome.debugger.onEvent) {
    chrome.debugger.onEvent.addListener(onCertificateError);
    chrome.debugger.onEvent.addListener(onDialogEvent);
    chrome.debugger.onEvent.addListener(onNetworkEvent);
  }
}

// Also connect when this worker first loads.
connect();
