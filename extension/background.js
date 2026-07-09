// aglink-web MV3 background service worker.
//
// Dials OUT to the local aglink-web daemon over a WebSocket and executes the
// browser commands it pushes (list_tabs / navigate / get_page_text) via the
// chrome.* APIs. No Native Messaging is involved. The daemon validates our
// chrome-extension:// Origin at the WS handshake, so arbitrary web pages that
// try ws://127.0.0.1 are rejected.
//
// Port: defaults to 48219 (must match the daemon's defaultPort / AGLINK_WEB_PORT).
// A browser extension can't read env vars or the daemon's port file, so if you
// override AGLINK_WEB_PORT, set the matching port once in the extension's options
// (chrome://extensions → aglink-web → Details → Extension options). It's stored
// in chrome.storage.local and applied on the next (re)connect.

const DEFAULT_PORT = 48219;
const DEFAULT_MAX_CHARS = 20000;

let ws = null;
let connecting = false; // guards the async gap between the connect guard and socket creation
let backoffMs = 1000;
const MAX_BACKOFF_MS = 30000;

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

async function connect() {
  // Idempotent: never open a second socket while one is connecting/open.
  // onInstalled, onStartup, the keepalive alarm, storage changes, and the initial
  // load all call connect(); without this guard they would race into several
  // sockets that the daemon's "newest wins" then churns. `connecting` extends the
  // guard across the async gap while we read the port from storage.
  if (connecting) return;
  if (ws && (ws.readyState === WebSocket.CONNECTING || ws.readyState === WebSocket.OPEN)) {
    return;
  }
  connecting = true;

  const port = await currentPort();
  let socket;
  try {
    socket = new WebSocket(`ws://127.0.0.1:${port}/ext`);
  } catch (e) {
    connecting = false;
    scheduleReconnect();
    return;
  }
  ws = socket;
  connecting = false;

  socket.onopen = () => {
    console.log("aglink-web: connected to daemon");
    backoffMs = 1000;
  };

  socket.onmessage = async (event) => {
    let req;
    try {
      req = JSON.parse(event.data);
    } catch (e) {
      return;
    }
    // Keepalive ping from the daemon (id 0): reply so the daemon can refresh its
    // read deadline, and — because sending/receiving a WS message resets the MV3
    // service-worker idle timer — this exchange keeps this worker alive.
    if (req.method === "__ping") {
      send(socket, { id: req.id, ok: true });
      return;
    }
    const reply = { id: req.id, ok: false };
    try {
      reply.text = await dispatch(req.method, req.params || {});
      reply.ok = true;
    } catch (e) {
      reply.error = String(e && e.message ? e.message : e);
    }
    send(socket, reply);
  };

  socket.onclose = () => {
    // Only react to the socket we currently own; a superseded older socket
    // closing must not trigger a reconnect loop.
    if (ws === socket) {
      ws = null;
      scheduleReconnect();
    }
  };

  socket.onerror = () => {
    // onclose fires next and drives reconnection.
  };
}

function send(socket, obj) {
  if (socket && socket.readyState === WebSocket.OPEN) {
    socket.send(JSON.stringify(obj));
  }
}

function scheduleReconnect() {
  setTimeout(connect, backoffMs);
  backoffMs = Math.min(backoffMs * 2, MAX_BACKOFF_MS);
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
    case "click":
      return await click(params);
    case "screenshot":
      return await screenshot(params);
    case "type":
      return await typeText(params);
    case "close_tab":
      return await closeTab(params);
    default:
      throw new Error(`unknown method: ${method}`);
  }
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
    tab = await chrome.tabs.create({ url });
  }
  await waitForComplete(tab.id);
  const updated = await chrome.tabs.get(tab.id);
  return `ok: navigated tab ${updated.id} — ${updated.title || ""} — ${updated.url || ""}`;
}

async function getPageText(params) {
  let tabId = params.tabId;
  if (!tabId) {
    const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!active) throw new Error("no active tab");
    tabId = active.id;
  }
  const maxChars = params.maxChars || DEFAULT_MAX_CHARS;
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: () => document.body ? document.body.innerText : "",
  });
  let text = (results && results[0] && results[0].result) || "";
  if (text.length > maxChars) {
    text = text.slice(0, maxChars) + `\n… [truncated at ${maxChars} chars]`;
  }
  return text;
}

async function click(params) {
  const selector = params.selector;
  if (!selector) throw new Error("click requires 'selector'");
  let tabId = params.tabId;
  if (!tabId) {
    const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!active) throw new Error("no active tab");
    tabId = active.id;
  }
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (sel) => {
      const el = document.querySelector(sel);
      if (!el) return { found: false };
      el.scrollIntoView({ block: "center", inline: "center" });
      el.click();
      return { found: true, tag: el.tagName.toLowerCase(), text: (el.textContent || "").trim().slice(0, 80) };
    },
    args: [selector],
  });
  const r = results && results[0] && results[0].result;
  if (!r || !r.found) throw new Error(`no element matched selector: ${selector}`);
  return `ok: clicked <${r.tag}>${r.text ? " " + JSON.stringify(r.text) : ""}`;
}

// screenshot captures the visible viewport of a tab as a base64 PNG (no data:
// URL prefix, so the daemon/MCP bridge can pass it straight through as the
// text-result payload — see protocol.go's doc comment on why this stays
// text-based end to end). captureVisibleTab only captures the *active* tab of
// a window, so a non-active tabId is switched to first (mirrors aglink-screen's
// focus_window-before-capture behavior).
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
  }
  const dataUrl = await chrome.tabs.captureVisibleTab(windowId, { format: "png" });
  return dataUrl.replace(/^data:image\/png;base64,/, "");
}

// typeText sets an input/textarea/contenteditable's value and fires input+change
// events so page JS (including React/Vue controlled inputs) picks up the change.
// For plain <input>/<textarea> it goes through the *native* value setter (bypassing
// any framework-overridden instance setter) — the standard trick to make React see
// a programmatic value change: React's onChange reads the DOM value after the
// 'input' event, so as long as the DOM value is actually set via the native setter
// first, the event dispatch below is what triggers it.
async function typeText(params) {
  const selector = params.selector;
  const text = params.text;
  if (!selector) throw new Error("type requires 'selector'");
  if (text === undefined || text === null) throw new Error("type requires 'text'");
  let tabId = params.tabId;
  if (!tabId) {
    const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!active) throw new Error("no active tab");
    tabId = active.id;
  }
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (sel, value) => {
      const el = document.querySelector(sel);
      if (!el) return { found: false };
      el.scrollIntoView({ block: "center", inline: "center" });
      el.focus();
      if (el.isContentEditable) {
        el.textContent = value;
      } else {
        const proto = el.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
        const setter = Object.getOwnPropertyDescriptor(proto, "value")?.set;
        if (setter) {
          setter.call(el, value);
        } else {
          el.value = value;
        }
      }
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
      return { found: true, tag: el.tagName.toLowerCase() };
    },
    args: [selector, text],
  });
  const r = results && results[0] && results[0].result;
  if (!r || !r.found) throw new Error(`no element matched selector: ${selector}`);
  return `ok: typed into <${r.tag}>`;
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

// waitForComplete resolves when the tab finishes loading, or after a timeout so
// a slow/hung page never blocks the command indefinitely.
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
    const finish = () => {
      if (done) return;
      done = true;
      chrome.tabs.onUpdated.removeListener(listener);
      clearTimeout(timer);
      resolve();
    };
    const listener = (id, info) => {
      if (id === tabId && info.status === "complete") finish();
    };
    chrome.tabs.onUpdated.addListener(listener);
    timer = setTimeout(finish, timeoutMs);
    // Guard against the load finishing before the listener was attached.
    chrome.tabs.get(tabId).then((tab) => {
      if (tab && tab.status === "complete") finish();
    }).catch(() => {});
  });
}

// ---- lifecycle --------------------------------------------------------------

// Connect on install and on browser startup, and keep a heartbeat alarm so the
// service worker is periodically revived to re-establish a dropped socket.
chrome.runtime.onInstalled.addListener(connect);
chrome.runtime.onStartup.addListener(connect);
chrome.alarms.create("aglink-web-keepalive", { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === "aglink-web-keepalive" && (!ws || ws.readyState !== WebSocket.OPEN)) {
    connect();
  }
});

// Reconnect immediately when the port is changed from the options page. Drop the
// current socket first, clearing our reference so its onclose won't schedule a
// competing reconnect (same "superseded socket stays quiet" rule as elsewhere).
chrome.storage.onChanged.addListener((changes, area) => {
  if (area !== "local" || !changes.port) return;
  const old = ws;
  ws = null;
  if (old) {
    try {
      old.close();
    } catch (e) {
      // ignore
    }
  }
  backoffMs = 1000;
  connect();
});

// Also connect when this worker first loads.
connect();
