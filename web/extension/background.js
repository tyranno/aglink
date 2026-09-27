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
  const account = await currentAccount();
  if (!account) {
    connecting = false;
    console.warn("aglink-web: " + NOT_SIGNED_IN);
    try {
      await chrome.storage.local.set({ lastError: NOT_SIGNED_IN });
    } catch (e) {
      // storage is a convenience for the options page; never block on it
    }
    // Retry anyway: the backoff caps at 30s, so signing in to Chrome recovers on
    // its own without the user having to come back and reload the extension.
    scheduleReconnect();
    return;
  }

  let socket;
  try {
    socket = new WebSocket(`ws://127.0.0.1:${port}/ext?account=${encodeURIComponent(account)}`);
  } catch (e) {
    connecting = false;
    scheduleReconnect();
    return;
  }
  ws = socket;
  connecting = false;

  socket.onopen = () => {
    console.log("aglink-web: connected to daemon as " + account);
    backoffMs = 1000;
    chrome.storage.local.set({ lastError: "", connectedAs: account }).catch(() => {});
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
      reply.text = await withDialogWatch(req.method, req.params || {}, () => dispatch(req.method, req.params || {}));
      reply.ok = true;
    } catch (e) {
      reply.error = await explainError(req.params || {}, String(e && e.message ? e.message : e));
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
async function ensureHelpers(tabId) {
  try {
    await chrome.scripting.executeScript({
      target: { tabId },
      files: ["aglink-inject.js", "page-actions.js"],
    });
  } catch (e) {
    // Non-fatal — see comment above.
  }
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
    tab = await chrome.tabs.create({ url });
  }
  await waitForComplete(tab.id);
  const updated = await chrome.tabs.get(tab.id);
  const ok = `ok: navigated tab ${updated.id} — ${updated.title || ""} — ${updated.url || ""}`;
  // A certificate problem does not fail the navigation: Chrome shows its own
  // warning page in the tab, and every later tool would fail on it with an
  // opaque "showing error page". Say so here, with the way past it.
  const warn = await certWarning(updated.id);
  return warn ? `${ok}\n${warn}` : ok;
}

async function getPageText(params) {
  const tabId = await activeTabId(params);
  const maxChars = params.maxChars || DEFAULT_MAX_CHARS;
  const selector = params.selector || null;
  // int-only params (see command.go): cursor>0 = incremental; offset<0 = tail.
  const hasCursor = Number.isFinite(params.cursor) && params.cursor > 0;
  const cursor = hasCursor ? Math.floor(params.cursor) : -1;
  const offset = Number.isFinite(params.offset) ? Math.floor(params.offset) : 0;

  await ensureHelpers(tabId);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["getPageText", [selector]],
  });
  const r = results && results[0] && results[0].result;
  if (selector && (!r || !r.found)) throw new Error(`no element matched selector: ${selector}`);
  const full = (r && r.text) || "";
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

// elementExists cheaply reports whether a selector matches, with NO page text —
// the low-cost primitive for polling a boolean condition (e.g. "is the app busy",
// keyed off a working/stop indicator's presence) instead of get_page_text/query_all.
async function elementExists(params) {
  const selector = params.selector;
  if (!selector) throw new Error("element_exists requires 'selector'");
  const tabId = await activeTabId(params);
  await ensureHelpers(tabId);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["elementExists", [selector]],
  });
  const r = (results && results[0] && results[0].result) || { exists: false, visible: false, count: 0 };
  return JSON.stringify(r);
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
  await ensureHelpers(tabId);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["click", [selector, button]],
  });
  const r = results && results[0] && results[0].result;
  if (!r || !r.found) throw new Error(`no element matched selector: ${selector}`);
  return `ok: ${button}-clicked <${r.tag}>${r.text ? " " + JSON.stringify(r.text) : ""}`;
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
  await ensureHelpers(tabId);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["hover", [selector]],
  });
  const r = results && results[0] && results[0].result;
  if (!r || !r.found) throw new Error(`no element matched selector: ${selector}`);
  return `ok: hovered <${r.tag}>${r.text ? " " + JSON.stringify(r.text) : ""}`;
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
  await ensureHelpers(tabId);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["doubleClick", [selector]],
  });
  const r = results && results[0] && results[0].result;
  if (!r || !r.found) throw new Error(`no element matched selector: ${selector}`);
  return `ok: double-clicked <${r.tag}>${r.text ? " " + JSON.stringify(r.text) : ""}`;
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
  await ensureHelpers(tabId);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["drag", [selector, target]],
  });
  const r = results && results[0] && results[0].result;
  if (!r) throw new Error("drag failed");
  if (!r.found) throw new Error(`no element matched ${r.which} selector: ${r.sel}`);
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
  await ensureHelpers(tabId);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["getAttribute", [selector, name]],
  });
  const r = results && results[0] && results[0].result;
  if (!r || !r.found) throw new Error(`no element matched selector: ${selector}`);
  if (!r.present) return `${name} = (not present) on <${r.tag}>`;
  return `${name} = ${JSON.stringify(r.value)}`;
}

// getHtml returns raw outerHTML — of the whole document (no selector) or of one
// element's subtree (selector) — for scraping structured markup get_page_text's
// innerText flattens away (tag structure, attributes, hrefs, hidden nodes). The
// read-side counterpart to get_page_text for crawling.
async function getHtml(params) {
  const tabId = await activeTabId(params);
  const maxChars = params.maxChars || DEFAULT_MAX_CHARS;
  const selector = params.selector || null;
  await ensureHelpers(tabId);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["getHtml", [selector]],
  });
  const r = results && results[0] && results[0].result;
  if (!r || !r.found) throw new Error(`no element matched selector: ${selector}`);
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
async function queryAll(params) {
  const selector = params.selector;
  if (!selector) throw new Error("query_all requires 'selector'");
  const tabId = await activeTabId(params);
  await ensureHelpers(tabId);
  const max = params.max || 200;
  const attrs = (params.attrs || "")
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["queryAll", [selector, attrs, max]],
  });
  const els = (results && results[0] && results[0].result) || [];
  if (els.length === 0) return `(no elements matched selector: ${selector})`;
  return els
    .map((e, i) => {
      const attrStr = e.attrs.map(([k, v]) => `${k}=${JSON.stringify(v)}`).join(" ");
      return `${i} | ${e.tag} | ${JSON.stringify(e.text)}${attrStr ? " | " + attrStr : ""}`;
    })
    .join("\n");
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
async function waitForElement(params) {
  const selector = params.selector;
  if (!selector) throw new Error("wait_for_element requires 'selector'");
  const tabId = await activeTabId(params);
  const timeoutMs = params.timeoutMs || 8000;
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    await ensureHelpers(tabId);
    const results = await chrome.scripting.executeScript({
      target: { tabId },
      func: (name, args) => globalThis.__aglinkPage[name](...args),
      args: ["waitForElement", [selector]],
    });
    const r = results && results[0] && results[0].result;
    if (r && r.found && r.visible) {
      return `ok: found <${r.tag}> matching ${selector}`;
    }
    if (Date.now() >= deadline) {
      throw new Error(`timed out after ${timeoutMs}ms waiting for a visible element matching ${selector}`);
    }
    await new Promise((resolve) => setTimeout(resolve, WAIT_FOR_ELEMENT_POLL_MS));
  }
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
  const tabId = await activeTabId(params);
  await ensureHelpers(tabId);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["typeText", [selector, text]],
  });
  const r = results && results[0] && results[0].result;
  if (!r || !r.found) throw new Error(`no element matched selector: ${selector}`);
  return `ok: typed into <${r.tag}>`;
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
  await ensureHelpers(tabId);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["getValue", [selector]],
  });
  const r = results && results[0] && results[0].result;
  if (!r || !r.found) throw new Error(`no element matched selector: ${selector}`);
  return `${selector} = ${JSON.stringify(r.value)}`;
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
  await ensureHelpers(tabId);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["scroll", [selector, dx, dy]],
  });
  const r = results && results[0] && results[0].result;
  if (!r || !r.found) throw new Error(`no element matched selector: ${selector}`);
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
  await ensureHelpers(tabId);
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (name, args) => globalThis.__aglinkPage[name](...args),
    args: ["selectOption", [selector, value === undefined ? null : value, label === undefined ? null : label]],
  });
  const r = results && results[0] && results[0].result;
  if (!r || !r.found) throw new Error(`no element matched selector: ${selector}`);
  if (r.isSelect === false) throw new Error(`element <${r.tag}> matched by ${selector} is not a <select>`);
  if (!r.matched) throw new Error(`no <option> matching value=${JSON.stringify(value)} label=${JSON.stringify(label)}`);
  return `ok: selected "${r.selected}"`;
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
async function getNetworkRequests(params) {
  let tabId = params.tabId;
  if (!tabId) {
    const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!active) throw new Error("no active tab");
    tabId = active.id;
  }
  const max = params.max || 50;
  const filter = (params.filter || "").toLowerCase();
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
  chrome.tabs.onRemoved.addListener((tabId) => rememberNavError(tabId, null));
}
if (chrome.debugger) {
  chrome.debugger.onDetach.addListener((source) => {
    if (source && source.tabId) {
      debuggerTabs.delete(source.tabId);
      overrideCertTabs.delete(source.tabId);
      openDialogs.delete(source.tabId);
    }
  });
  if (chrome.debugger.onEvent) {
    chrome.debugger.onEvent.addListener(onCertificateError);
    chrome.debugger.onEvent.addListener(onDialogEvent);
  }
}

// Also connect when this worker first loads.
connect();
