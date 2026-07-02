// aglink-web MV3 background service worker.
//
// Dials OUT to the local aglink-web daemon over a WebSocket and executes the
// browser commands it pushes (list_tabs / navigate / get_page_text) via the
// chrome.* APIs. No Native Messaging is involved. The daemon validates our
// chrome-extension:// Origin at the WS handshake, so arbitrary web pages that
// try ws://127.0.0.1 are rejected.
//
// If you override AGLINK_WEB_PORT on the daemon, change PORT here to match.

const PORT = 48219;
const WS_URL = `ws://127.0.0.1:${PORT}/ext`;
const DEFAULT_MAX_CHARS = 20000;

let ws = null;
let backoffMs = 1000;
const MAX_BACKOFF_MS = 30000;

function connect() {
  // Idempotent: never open a second socket while one is connecting/open.
  // onInstalled, onStartup, the keepalive alarm, and the initial load all call
  // connect(); without this guard they would race into several sockets that the
  // daemon's "newest wins" then churns.
  if (ws && (ws.readyState === WebSocket.CONNECTING || ws.readyState === WebSocket.OPEN)) {
    return;
  }

  let socket;
  try {
    socket = new WebSocket(WS_URL);
  } catch (e) {
    scheduleReconnect();
    return;
  }
  ws = socket;

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

// waitForComplete resolves when the tab finishes loading, or after a timeout so
// a slow/hung page never blocks the command indefinitely.
function waitForComplete(tabId, timeoutMs = 15000) {
  return new Promise((resolve) => {
    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      chrome.tabs.onUpdated.removeListener(listener);
      resolve();
    };
    const listener = (id, info) => {
      if (id === tabId && info.status === "complete") finish();
    };
    chrome.tabs.onUpdated.addListener(listener);
    setTimeout(finish, timeoutMs);
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

// Also connect when this worker first loads.
connect();
