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
  let tabId = params.tabId;
  if (!tabId) {
    const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!active) throw new Error("no active tab");
    tabId = active.id;
  }
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (sel, btn) => {
      const el = document.querySelector(sel);
      if (!el) return { found: false };
      el.scrollIntoView({ block: "center", inline: "center" });
      if (btn === "left") {
        el.click();
      } else {
        const rect = el.getBoundingClientRect();
        const opts = {
          bubbles: true,
          cancelable: true,
          view: window,
          clientX: rect.left + rect.width / 2,
          clientY: rect.top + rect.height / 2,
          button: btn === "right" ? 2 : 1,
        };
        el.dispatchEvent(new MouseEvent("mousedown", opts));
        el.dispatchEvent(new MouseEvent("mouseup", opts));
        el.dispatchEvent(new MouseEvent(btn === "right" ? "contextmenu" : "auxclick", opts));
      }
      return { found: true, tag: el.tagName.toLowerCase(), text: (el.textContent || "").trim().slice(0, 80) };
    },
    args: [selector, button],
  });
  const r = results && results[0] && results[0].result;
  if (!r || !r.found) throw new Error(`no element matched selector: ${selector}`);
  return `ok: ${button}-clicked <${r.tag}>${r.text ? " " + JSON.stringify(r.text) : ""}`;
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
  let tabId = params.tabId;
  if (!tabId) {
    const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!active) throw new Error("no active tab");
    tabId = active.id;
  }
  const max = params.max || 200;
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (selectorList, idAttr, maxEls) => {
      document.querySelectorAll(`[${idAttr}]`).forEach((el) => el.removeAttribute(idAttr));
      const out = [];
      let idx = 0;
      for (const el of document.querySelectorAll(selectorList)) {
        if (out.length >= maxEls) break;
        const rect = el.getBoundingClientRect();
        // A non-zero rect is enough to mean "rendered": offsetParent is null
        // (misleadingly) for <body>/<html> and for position:fixed elements
        // too, not just display:none — toasts/modals are commonly fixed, so
        // checking it here would silently drop exactly the elements a caller
        // is most likely waiting to interact with.
        if (rect.width <= 0 || rect.height <= 0) continue;
        el.setAttribute(idAttr, String(idx));
        const label = (
          el.getAttribute("aria-label") ||
          el.getAttribute("placeholder") ||
          el.value ||
          el.textContent ||
          ""
        ).trim().replace(/\s+/g, " ").slice(0, 60);
        out.push({
          idx,
          tag: el.tagName.toLowerCase(),
          role: el.getAttribute("role") || "",
          type: el.getAttribute("type") || "",
          label,
          disabled: !!el.disabled,
          x: Math.round(rect.left + rect.width / 2),
          y: Math.round(rect.top + rect.height / 2),
        });
        idx++;
      }
      return out;
    },
    args: [INTERACTIVE_SELECTOR, AGLINK_ID_ATTR, max],
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
  let tabId = params.tabId;
  if (!tabId) {
    const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!active) throw new Error("no active tab");
    tabId = active.id;
  }
  const timeoutMs = params.timeoutMs || 8000;
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const results = await chrome.scripting.executeScript({
      target: { tabId },
      func: (sel) => {
        const el = document.querySelector(sel);
        if (!el) return { found: false };
        const rect = el.getBoundingClientRect();
        // See listElements' matching comment: offsetParent is null for
        // <body>/<html> and position:fixed elements too, not just
        // display:none, so it must not gate visibility here.
        const visible = rect.width > 0 && rect.height > 0;
        return { found: true, visible, tag: el.tagName.toLowerCase() };
      },
      args: [selector],
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

// getValue reads an element's CURRENT value/text — the read-side counterpart
// to typeText/selectOption. get_page_text can't see this: an <input>'s value
// isn't part of document.body.innerText, so after a page's own JS rewrites a
// field (autocomplete, a calculated total, client-side validation reformatting
// what was typed) this is the only way to confirm what it actually holds now.
async function getValue(params) {
  const selector = params.selector;
  if (!selector) throw new Error("get_value requires 'selector'");
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
      const tag = el.tagName.toLowerCase();
      if (el.isContentEditable) {
        return { found: true, tag, value: el.textContent || "" };
      }
      if ("value" in el) {
        return { found: true, tag, value: el.value };
      }
      return { found: true, tag, value: el.textContent || "" };
    },
    args: [selector],
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
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (comboStr, keySpecs, modProps) => {
      const parts = comboStr.split("+").map((p) => p.trim().toLowerCase()).filter(Boolean);
      if (parts.length === 0) return { ok: false, error: "empty key combo" };
      const mods = {};
      let keyToken = null;
      parts.forEach((p, i) => {
        if (modProps[p] && i !== parts.length - 1) {
          mods[modProps[p]] = true;
        } else {
          keyToken = p;
        }
      });
      if (!keyToken) return { ok: false, error: `no key in combo "${comboStr}"` };
      let spec = keySpecs[keyToken];
      if (!spec && keyToken.length === 1) {
        spec = { key: keyToken, code: "Key" + keyToken.toUpperCase(), keyCode: keyToken.toUpperCase().charCodeAt(0) };
      }
      if (!spec) return { ok: false, error: `unknown key "${keyToken}" in combo "${comboStr}"` };
      const el = document.activeElement || document.body;
      const opts = {
        key: spec.key,
        code: spec.code,
        keyCode: spec.keyCode,
        which: spec.keyCode,
        bubbles: true,
        cancelable: true,
        ...mods,
      };
      el.dispatchEvent(new KeyboardEvent("keydown", opts));
      el.dispatchEvent(new KeyboardEvent("keyup", opts));
      return { ok: true, tag: el.tagName ? el.tagName.toLowerCase() : "document" };
    },
    args: [combo, KEY_SPECS, MOD_PROPS],
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
  let tabId = params.tabId;
  if (!tabId) {
    const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!active) throw new Error("no active tab");
    tabId = active.id;
  }
  const selector = params.selector || null;
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (sel, dxPx, dyPx) => {
      const target = sel ? document.querySelector(sel) : null;
      if (sel && !target) return { found: false };
      (target || window).scrollBy({ left: dxPx, top: dyPx, behavior: "instant" });
      return { found: true };
    },
    args: [selector, dx, dy],
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
  let tabId = params.tabId;
  if (!tabId) {
    const [active] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!active) throw new Error("no active tab");
    tabId = active.id;
  }
  const results = await chrome.scripting.executeScript({
    target: { tabId },
    func: (sel, val, lbl) => {
      const el = document.querySelector(sel);
      if (!el) return { found: false };
      if (el.tagName !== "SELECT") return { found: true, isSelect: false, tag: el.tagName.toLowerCase() };
      let match = null;
      for (const opt of el.options) {
        if (val !== null && val !== undefined && opt.value === String(val)) {
          match = opt;
          break;
        }
        if (lbl !== null && lbl !== undefined && opt.textContent.trim() === String(lbl)) {
          match = opt;
          break;
        }
      }
      if (!match) return { found: true, isSelect: true, matched: false };
      el.value = match.value;
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
      return { found: true, isSelect: true, matched: true, selected: match.textContent.trim() };
    },
    args: [selector, value === undefined ? null : value, label === undefined ? null : label],
  });
  const r = results && results[0] && results[0].result;
  if (!r || !r.found) throw new Error(`no element matched selector: ${selector}`);
  if (r.isSelect === false) throw new Error(`element <${r.tag}> matched by ${selector} is not a <select>`);
  if (!r.matched) throw new Error(`no <option> matching value=${JSON.stringify(value)} label=${JSON.stringify(label)}`);
  return `ok: selected "${r.selected}"`;
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
