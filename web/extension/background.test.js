// Lightweight unit tests for the extension's background service worker.
//
// No npm dependencies and no real browser: background.js is loaded into a
// node:vm context with mocked chrome.* / WebSocket globals. Because background.js
// is a plain (non-module) script, its top-level `function`/`async function`
// declarations become properties of the vm context, so tests can call them
// directly (e.g. sb.waitForComplete). Run with: node --test extension/
//
// These cover the browser-side logic that Go tests can't reach: the navigate
// completion guard, the configurable-port resolution, tab-list formatting, and
// command dispatch routing.

const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const SRC = fs.readFileSync(path.join(__dirname, "background.js"), "utf8");
const DEFAULT_PORT = 48219;

function isPlainObject(v) {
  return v && typeof v === "object" && !Array.isArray(v);
}

// deepMerge lets a test override just the chrome.* corners it cares about while
// keeping the rest of the mock intact.
function deepMerge(base, over) {
  const out = { ...base };
  for (const k of Object.keys(over || {})) {
    out[k] = isPlainObject(base[k]) && isPlainObject(over[k]) ? deepMerge(base[k], over[k]) : over[k];
  }
  return out;
}

function makeChrome(overrides = {}) {
  const noop = () => {};
  const base = {
    runtime: { onInstalled: { addListener: noop }, onStartup: { addListener: noop } },
    alarms: { create: noop, onAlarm: { addListener: noop } },
    storage: {
      local: { get: async () => ({}), set: async () => {}, remove: async () => {} },
      onChanged: { addListener: noop },
    },
    tabs: {
      query: async () => [],
      get: async () => ({ status: "complete" }),
      update: async () => ({}),
      create: async () => ({}),
      remove: async () => {},
      onUpdated: { addListener: noop, removeListener: noop },
    },
    scripting: { executeScript: async () => [{ result: null }] },
  };
  return deepMerge(base, overrides);
}

// loadBackground evaluates background.js in a fresh sandbox and returns it so the
// test can call the worker's functions. Loading also runs connect() once; the
// WebSocket mock makes that a harmless no-op.
// Every timer a loaded worker starts. With no signed-in account connect()
// retries forever on a backoff timer, which kept `node --test` from ever
// exiting; they are all cleared once the file's tests are done.
const sandboxTimers = new Set();
function sandboxSetTimeout(fn, ms, ...args) {
  const t = setTimeout(() => {
    sandboxTimers.delete(t);
    fn(...args);
  }, ms);
  sandboxTimers.add(t);
  return t;
}
test.after(() => {
  for (const t of sandboxTimers) clearTimeout(t);
  sandboxTimers.clear();
});

function loadBackground(chrome) {
  class FakeWebSocket {
    constructor() {
      this.readyState = FakeWebSocket.CONNECTING;
    }
    send() {}
    close() {}
  }
  FakeWebSocket.CONNECTING = 0;
  FakeWebSocket.OPEN = 1;
  FakeWebSocket.CLOSING = 2;
  FakeWebSocket.CLOSED = 3;

  const sandbox = {
    chrome,
    WebSocket: FakeWebSocket,
    console: { log() {}, error() {}, warn() {} },
    setTimeout: sandboxSetTimeout,
    clearTimeout,
    Promise,
    Math,
    JSON,
    Number,
    Event: class {
      constructor(type) {
        this.type = type;
      }
    },
  };
  vm.createContext(sandbox);
  vm.runInContext(SRC, sandbox);
  return sandbox;
}

// resolvesFast asserts a promise settles well before its own (large) internal
// timeout — i.e. via real logic, not the fallback timer.
async function resolvesFast(promise, budgetMs = 500) {
  const outcome = await Promise.race([
    promise.then(() => "resolved"),
    new Promise((r) => setTimeout(() => r("hung"), budgetMs)),
  ]);
  assert.strictEqual(outcome, "resolved", "promise did not settle promptly");
}

test("waitForComplete resolves immediately when the tab is already complete", async () => {
  let listenerAttached = false;
  const chrome = makeChrome({
    tabs: {
      get: async () => ({ status: "complete" }),
      onUpdated: { addListener: () => { listenerAttached = true; }, removeListener: () => {} },
    },
  });
  const sb = loadBackground(chrome);
  // Big timeout: if the get-guard didn't work, this would hang until it fires.
  await resolvesFast(sb.waitForComplete(123, 100000));
  assert.ok(listenerAttached, "listener should still be attached for the normal path");
});

test("waitForComplete resolves on the complete event while loading", async () => {
  let listener;
  const chrome = makeChrome({
    tabs: {
      get: async () => ({ status: "loading" }), // guard must NOT resolve on this
      onUpdated: { addListener: (fn) => { listener = fn; }, removeListener: () => {} },
    },
  });
  const sb = loadBackground(chrome);
  const p = sb.waitForComplete(42, 100000);
  await new Promise((r) => setTimeout(r, 10));
  assert.strictEqual(typeof listener, "function", "listener should be registered");
  listener(99, { status: "complete" }); // wrong tab id — must be ignored
  listener(42, { status: "loading" }); // wrong status — must be ignored
  listener(42, { status: "complete" }); // the real one
  await resolvesFast(p);
});

test("currentPort falls back to the default for unset or invalid values", async () => {
  const cases = [
    [undefined, DEFAULT_PORT],
    [3000, 3000],
    ["8080", 8080],
    [0, DEFAULT_PORT],
    [-1, DEFAULT_PORT],
    [70000, DEFAULT_PORT],
    ["abc", DEFAULT_PORT],
  ];
  for (const [stored, expected] of cases) {
    const chrome = makeChrome({ storage: { local: { get: async () => ({ port: stored }) } } });
    const sb = loadBackground(chrome);
    assert.strictEqual(await sb.currentPort(), expected, `stored=${JSON.stringify(stored)}`);
  }
});

test("currentPort tolerates a storage failure", async () => {
  const chrome = makeChrome({
    storage: { local: { get: async () => { throw new Error("storage boom"); } } },
  });
  const sb = loadBackground(chrome);
  assert.strictEqual(await sb.currentPort(), DEFAULT_PORT);
});

test("listTabs formats and handles the empty case", async () => {
  const withTabs = loadBackground(
    makeChrome({
      tabs: {
        query: async () => [
          { id: 1, active: true, title: "A", url: "http://a" },
          { id: 2, active: false, title: "B", url: "http://b" },
        ],
      },
    })
  );
  assert.strictEqual(await withTabs.listTabs(), "1 | [active] A | http://a\n2 | B | http://b");

  const empty = loadBackground(makeChrome({ tabs: { query: async () => [] } }));
  assert.strictEqual(await empty.listTabs(), "(no open tabs)");
});

test("dispatch rejects unknown methods", async () => {
  const sb = loadBackground(makeChrome());
  await assert.rejects(() => sb.dispatch("nope", {}), /unknown method: nope/);
});

test("navigate opens a tab, waits for load, and returns the final tab info", async () => {
  let created = null;
  const chrome = makeChrome({
    tabs: {
      create: async (opts) => { created = opts; return { id: 7 }; },
      get: async () => ({ id: 7, status: "complete", title: "Example", url: "https://example.com/" }),
    },
  });
  const sb = loadBackground(chrome);
  const out = await sb.navigate({ url: "https://example.com" });
  // created is built inside the vm realm, so compare the field, not the object
  // (deepStrictEqual would fail on the cross-realm prototype).
  assert.strictEqual(created.url, "https://example.com");
  assert.strictEqual(out, "ok: navigated tab 7 — Example — https://example.com/");
});

test("navigate requires a url", async () => {
  const sb = loadBackground(makeChrome());
  await assert.rejects(() => sb.navigate({}), /navigate requires 'url'/);
});

test("click defaults to left and reports the button used", async () => {
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: {
        executeScript: async () => [{ result: { found: true, tag: "button", text: "Submit" } }],
      },
    })
  );
  assert.strictEqual(await sb.click({ selector: "#go" }), 'ok: left-clicked <button> "Submit"');
});

test("click supports right/middle and rejects an unknown button", async () => {
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: true, tag: "div", text: "" } }] },
    })
  );
  assert.strictEqual(await sb.click({ selector: "#menu", button: "right" }), "ok: right-clicked <div>");
  assert.strictEqual(await sb.click({ selector: "#menu", button: "MIDDLE" }), "ok: middle-clicked <div>");
  await assert.rejects(
    () => sb.click({ selector: "#menu", button: "double" }),
    /unknown button "double" \(want left\/right\/middle\)/
  );
});

test("hover reports the hovered element", async () => {
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: true, tag: "a", text: "Menu" } }] },
    })
  );
  assert.strictEqual(await sb.hover({ selector: "role=link[name=Menu]" }), 'ok: hovered <a> "Menu"');
});

test("hover surfaces a missing element", async () => {
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: false } }] },
    })
  );
  await assert.rejects(() => sb.hover({ selector: "#nope" }), /no element matched selector: #nope/);
  await assert.rejects(() => sb.hover({}), /hover requires 'selector'/);
});

test("getAttribute reads an attribute, absence, and requires name", async () => {
  const present = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 2, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: true, tag: "a", present: true, value: "/next" } }] },
    })
  );
  assert.strictEqual(await present.getAttribute({ selector: "a", name: "href" }), 'href = "/next"');

  const absent = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 2, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: true, tag: "button", present: false, value: null } }] },
    })
  );
  assert.strictEqual(
    await absent.getAttribute({ selector: "button", name: "disabled" }),
    "disabled = (not present) on <button>"
  );

  const sb = loadBackground(makeChrome({ tabs: { query: async () => [{ id: 2, active: true }] } }));
  await assert.rejects(() => sb.getAttribute({ selector: "a" }), /get_attribute requires 'name'/);
  await assert.rejects(() => sb.getAttribute({ name: "href" }), /get_attribute requires 'selector'/);
});

test("doubleClick reports the element and needs a selector", async () => {
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: true, tag: "div", text: "Row 1" } }] },
    })
  );
  assert.strictEqual(await sb.doubleClick({ selector: "#row" }), 'ok: double-clicked <div> "Row 1"');
  await assert.rejects(() => sb.doubleClick({}), /double_click requires 'selector'/);

  const miss = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: false } }] },
    })
  );
  await assert.rejects(() => miss.doubleClick({ selector: "#nope" }), /no element matched selector: #nope/);
});

test("drag reports source/target and validates both selectors", async () => {
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: true, srcTag: "li", dstTag: "ul" } }] },
    })
  );
  assert.strictEqual(await sb.drag({ selector: "#a", target: "#b" }), "ok: dragged <li> onto <ul>");
  await assert.rejects(() => sb.drag({ target: "#b" }), /drag requires 'selector'/);
  await assert.rejects(() => sb.drag({ selector: "#a" }), /drag requires 'target'/);

  const miss = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: false, which: "target", sel: "#b" } }] },
    })
  );
  await assert.rejects(() => miss.drag({ selector: "#a", target: "#b" }), /no element matched target selector: #b/);
});

test("getHtml returns markup and truncates at maxChars", async () => {
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: true, html: "<div>hello</div>" } }] },
    })
  );
  assert.strictEqual(await sb.getHtml({ selector: "#x" }), "<div>hello</div>");

  const long = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: true, html: "abcdefghij" } }] },
    })
  );
  const out = await long.getHtml({ maxChars: 4 });
  assert.ok(out.startsWith("abcd"), "should keep the first maxChars");
  assert.ok(out.includes("truncated at 4 chars"), "should note truncation");

  const miss = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: false } }] },
    })
  );
  await assert.rejects(() => miss.getHtml({ selector: "#nope" }), /no element matched selector: #nope/);
});

test("queryAll formats one line per element and needs a selector", async () => {
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: {
        executeScript: async () => [
          {
            result: [
              { tag: "a", text: "First", attrs: [["href", "/1"]] },
              { tag: "a", text: "Second", attrs: [["href", "/2"]] },
            ],
          },
        ],
      },
    })
  );
  assert.strictEqual(
    await sb.queryAll({ selector: "a[href]" }),
    '0 | a | "First" | href="/1"\n1 | a | "Second" | href="/2"'
  );
  await assert.rejects(() => sb.queryAll({}), /query_all requires 'selector'/);

  const empty = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: { executeScript: async () => [{ result: [] }] },
    })
  );
  assert.strictEqual(await empty.queryAll({ selector: ".none" }), "(no elements matched selector: .none)");
});

test("evalExpression returns json, surfaces page errors, needs an expression", async () => {
  const ok = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: { executeScript: async () => [{ result: { ok: true, json: "42" } }] },
    })
  );
  assert.strictEqual(await ok.evalExpression({ expression: "40 + 2" }), "42");
  await assert.rejects(() => ok.evalExpression({}), /eval requires 'expression'/);

  const bad = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: { executeScript: async () => [{ result: { ok: false, error: "x is not defined" } }] },
    })
  );
  await assert.rejects(() => bad.evalExpression({ expression: "x" }), /eval error: x is not defined/);
});

test("selector commands inject the shared resolver helper first", async () => {
  const calls = [];
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 7, active: true }] },
      scripting: {
        executeScript: async (opts) => {
          calls.push(opts);
          return [{ result: { found: true, tag: "button", text: "" } }];
        },
      },
    })
  );
  await sb.click({ selector: "role=button" });
  assert.ok(calls.length >= 2, "expected a helper-injection call plus the action call");
  // Compare element-wise: the array is built in the vm realm, so its prototype
  // differs from this realm's Array and deepStrictEqual would reject it.
  assert.ok(
    calls[0].files && calls[0].files.length === 2 && calls[0].files[0] === "aglink-inject.js" && calls[0].files[1] === "page-actions.js",
    "first call must inject the resolver and the shared page functions"
  );
  assert.ok(!calls[1].files && typeof calls[1].func === "function", "second call runs the action func");
});

test("listElements formats rows and handles the empty case", async () => {
  const withEls = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 9, active: true }] },
      scripting: {
        executeScript: async () => [
          {
            result: [
              { idx: 0, tag: "button", role: "", type: "", label: "Send", disabled: false, x: 10, y: 20 },
              { idx: 1, tag: "input", role: "combobox", type: "text", label: "", disabled: true, x: 30, y: 40 },
            ],
          },
        ],
      },
    })
  );
  assert.strictEqual(
    await withEls.listElements({}),
    '0 | button | "Send" | selector=[data-aglink-id="0"] | viewport(10,20)\n' +
      '1 | input[combobox] type=text | "" | selector=[data-aglink-id="1"] | viewport(30,40) [disabled]'
  );

  const empty = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 9, active: true }] },
      scripting: { executeScript: async () => [{ result: [] }] },
    })
  );
  assert.strictEqual(await empty.listElements({}), "(no visible interactive elements found)");
});

test("listElements requires an active tab when tabId is omitted", async () => {
  const sb = loadBackground(makeChrome({ tabs: { query: async () => [] } }));
  await assert.rejects(() => sb.listElements({}), /no active tab/);
});

test("getValue reads an input's current value", async () => {
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 3, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: true, tag: "input", value: "42500" } }] },
    })
  );
  assert.strictEqual(await sb.getValue({ selector: "#total" }), '#total = "42500"');
});

test("getValue requires a selector and surfaces a missing element", async () => {
  const sb = loadBackground(makeChrome());
  await assert.rejects(() => sb.getValue({}), /get_value requires 'selector'/);

  const missing = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 3, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: false } }] },
    })
  );
  await assert.rejects(() => missing.getValue({ selector: "#nope" }), /no element matched selector: #nope/);
});

test("activateTab requires a tabId and activates the tab + focuses its window", async () => {
  let updatedTabId, updatedTabOpts, focusedWindowId, focusedOpts;
  const sb = loadBackground(
    makeChrome({
      tabs: {
        update: async (id, opts) => {
          updatedTabId = id;
          updatedTabOpts = opts;
          return { id, windowId: 55, title: "Example", url: "https://example.com" };
        },
      },
      windows: { update: async (id, opts) => { focusedWindowId = id; focusedOpts = opts; } },
    })
  );
  const out = await sb.activateTab({ tabId: 7 });
  assert.strictEqual(updatedTabId, 7);
  assert.strictEqual(updatedTabOpts.active, true); // cross-realm object: compare fields, not deepStrictEqual (see navigate test)
  assert.strictEqual(focusedWindowId, 55);
  assert.strictEqual(focusedOpts.focused, true);
  assert.strictEqual(out, "ok: activated tab 7 — Example — https://example.com");

  await assert.rejects(() => sb.activateTab({}), /activate_tab requires 'tabId'/);
});

test("getConsoleLogs formats captured messages and handles the empty case", async () => {
  const withLogs = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 4, active: true }] },
      scripting: {
        executeScript: async () => [
          {
            result: [
              { level: "log", time: 1, text: "hello" },
              { level: "error", time: 2, text: "boom" },
            ],
          },
        ],
      },
    })
  );
  assert.strictEqual(await withLogs.getConsoleLogs({}), "[log] hello\n[error] boom");

  const empty = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 4, active: true }] },
      scripting: { executeScript: async () => [{ result: [] }] },
    })
  );
  assert.strictEqual(await empty.getConsoleLogs({}), "(no console messages captured)");
});

test("getNetworkRequests formats captured requests, applies filter, and handles the empty case", async () => {
  const entries = [
    { type: "fetch", method: "GET", url: "https://example.com/api/list", status: 200, durationMs: 12, responseBody: '{"ok":true}' },
    { type: "xhr", method: "POST", url: "https://example.com/api/approve", status: 500, durationMs: 34, requestBody: '{"id":1}', error: undefined },
  ];
  const withReqs = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 4, active: true }] },
      scripting: { executeScript: async () => [{ result: entries }] },
    })
  );
  assert.strictEqual(
    await withReqs.getNetworkRequests({}),
    '0 | fetch | GET https://example.com/api/list | -> 200 (12ms) | resp="{\\"ok\\":true}"\n' +
      '1 | xhr | POST https://example.com/api/approve | -> 500 (34ms) | req="{\\"id\\":1}"'
  );

  const empty = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 4, active: true }] },
      scripting: { executeScript: async () => [{ result: [] }] },
    })
  );
  assert.strictEqual(await empty.getNetworkRequests({}), "(no network requests captured)");
});

test("keyCombo requires a combo", async () => {
  const sb = loadBackground(makeChrome());
  await assert.rejects(() => sb.keyCombo({}), /key requires 'combo'/);
});

test("keyCombo reports success from the page-side result", async () => {
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 5, active: true }] },
      scripting: { executeScript: async () => [{ result: { ok: true, tag: "input" } }] },
    })
  );
  assert.strictEqual(await sb.keyCombo({ combo: "ctrl+a" }), 'ok: pressed "ctrl+a" on <input>');
});

test("keyCombo surfaces a page-side error (e.g. unknown key)", async () => {
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 5, active: true }] },
      scripting: {
        executeScript: async () => [{ result: { ok: false, error: 'unknown key "zzz" in combo "zzz"' } }],
      },
    })
  );
  await assert.rejects(() => sb.keyCombo({ combo: "zzz" }), /unknown key "zzz"/);
});

test("waitForElement resolves once the element becomes visible", async () => {
  let calls = 0;
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 6, active: true }] },
      scripting: {
        executeScript: async () => {
          calls++;
          if (calls < 2) return [{ result: { found: false } }];
          return [{ result: { found: true, visible: true, tag: "div" } }];
        },
      },
    })
  );
  const out = await sb.waitForElement({ selector: "#late", timeoutMs: 2000 });
  assert.strictEqual(out, "ok: found <div> matching #late");
  assert.ok(calls >= 2, "expected at least 2 polls before success");
});

test("waitForElement times out when the element never appears", async () => {
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 6, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: false } }] },
    })
  );
  const start = Date.now();
  await assert.rejects(
    () => sb.waitForElement({ selector: "#never", timeoutMs: 100 }),
    /timed out after 100ms waiting for a visible element matching #never/
  );
  assert.ok(Date.now() - start < 2000, "should not block far past the timeout");
});

test("waitForElement requires a selector", async () => {
  const sb = loadBackground(makeChrome());
  await assert.rejects(() => sb.waitForElement({}), /wait_for_element requires 'selector'/);
});

test("scroll requires a non-zero dx or dy", async () => {
  const sb = loadBackground(makeChrome());
  await assert.rejects(() => sb.scroll({}), /scroll requires a non-zero dx or dy/);
  await assert.rejects(() => sb.scroll({ dx: 0, dy: 0 }), /scroll requires a non-zero dx or dy/);
});

test("scroll reports success and surfaces a missing selector", async () => {
  const ok = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: true } }] },
    })
  );
  assert.strictEqual(await ok.scroll({ dy: 100 }), "ok: scrolled dx=0 dy=100");

  const missing = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 1, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: false } }] },
    })
  );
  await assert.rejects(
    () => missing.scroll({ dy: 100, selector: "#nope" }),
    /no element matched selector: #nope/
  );
});

test("selectOption requires a value or label", async () => {
  const sb = loadBackground(makeChrome());
  await assert.rejects(() => sb.selectOption({ selector: "#s" }), /select_option requires 'value' or 'label'/);
});

test("selectOption reports the selected option", async () => {
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 2, active: true }] },
      scripting: {
        executeScript: async () => [{ result: { found: true, isSelect: true, matched: true, selected: "Korea" } }],
      },
    })
  );
  assert.strictEqual(await sb.selectOption({ selector: "#country", label: "Korea" }), 'ok: selected "Korea"');
});

test("selectOption rejects a non-<select> element and an unmatched option", async () => {
  const notSelect = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 2, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: true, isSelect: false, tag: "input" } }] },
    })
  );
  await assert.rejects(
    () => notSelect.selectOption({ selector: "#x", value: "a" }),
    /element <input> matched by #x is not a <select>/
  );

  const noMatch = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 2, active: true }] },
      scripting: { executeScript: async () => [{ result: { found: true, isSelect: true, matched: false } }] },
    })
  );
  await assert.rejects(
    () => noMatch.selectOption({ selector: "#x", value: "zz" }),
    /no <option> matching value="zz"/
  );
});

test("page-actions.js defines every shared page function", () => {
  const src = fs.readFileSync(path.join(__dirname, "page-actions.js"), "utf8");
  const sb = {};
  vm.createContext(sb);
  vm.runInContext(src, sb);
  const want = ["getPageText", "elementExists", "click", "doubleClick", "hover", "drag", "getHtml",
    "queryAll", "evalExpression", "getAttribute", "listElements", "waitForElement", "typeText",
    "getValue", "keyCombo", "scroll", "selectOption"];
  for (const n of want) assert.strictEqual(typeof sb.__aglinkPage[n], "function", n);
  assert.strictEqual(sb.__aglinkPage.constants.KEY_SPECS.f5.key, "F5", "F-key table must be complete");
});

test("shared handlers call through __aglinkPage with the original args", async () => {
  const calls = [];
  const sb = loadBackground(
    makeChrome({
      tabs: { query: async () => [{ id: 5, active: true }] },
      scripting: {
        executeScript: async (opts) => {
          calls.push(opts);
          return [{ result: { found: true, text: "hi" } }];
        },
      },
    })
  );
  await sb.getPageText({});
  const run = calls.find((c) => typeof c.func === "function");
  assert.strictEqual(run.args[0], "getPageText");
  assert.strictEqual(run.args[1].length, 1);
  assert.strictEqual(run.args[1][0], null, "no selector is passed through as null, as before");
  assert.ok(calls.some((c) => c.files), "selectorless calls must still inject page-actions.js");
});

// ---- certificate warnings ---------------------------------------------------

// certChrome mocks a tab (id 7) that is on Chrome's certificate warning until
// one of the bypasses the test enables takes effect.
function certChrome({ onWarning = true, proceedLink = false, keysWork = false, ignoreWorks = false } = {}) {
  const st = { onWarning, attached: 0, detached: 0, keys: [], commands: [], nav: {} };
  const errorPage = () => new Error("Frame with ID 0 is showing error page");
  const chrome = makeChrome({
    tabs: {
      query: async () => [{ id: 7, active: true }],
      get: async (id) => ({ id, status: "complete", title: st.onWarning ? "Privacy error" : "NAS", url: "https://nas.local/" }),
      update: async (id) => ({ id }),
    },
    scripting: {
      executeScript: async () => {
        if (st.onWarning) throw errorPage();
        return [{ result: 1 }];
      },
    },
    webNavigation: {
      onBeforeNavigate: { addListener: (fn) => (st.nav.before = fn) },
      onErrorOccurred: { addListener: (fn) => (st.nav.error = fn) },
    },
    debugger: {
      attach: async () => { st.attached++; },
      detach: async () => { st.detached++; },
      onDetach: { addListener: () => {} },
      sendCommand: async (target, method, params) => {
        st.commands.push(method);
        if (method === "Runtime.evaluate") {
          if (proceedLink) st.onWarning = false;
          return { result: { value: proceedLink } };
        }
        if (method === "Input.dispatchKeyEvent" && params.type === "keyDown") {
          st.keys.push(params.text);
          if (keysWork && st.keys.join("") === "thisisunsafe") st.onWarning = false;
        }
        if (method === "Page.navigate" && ignoreWorks && st.commands.includes("Security.setIgnoreCertificateErrors")) {
          st.onWarning = false;
        }
        return {};
      },
    },
  });
  const sb = loadBackground(chrome);
  sb.leaveWarningTimeoutMs = () => 600;
  const certError = (error = "net::ERR_CERT_AUTHORITY_INVALID") =>
    st.nav.error({ frameId: 0, tabId: 7, url: "https://nas.local/", error });
  return { sb, st, certError };
}

test("navigate says when the tab is left on a certificate warning, and how past it", async () => {
  const { sb, certError } = certChrome();
  certError();
  const out = await sb.navigate({ url: "https://nas.local/", tabId: 7 });
  assert.match(out, /^ok: navigated tab 7/);
  assert.match(out, /certificate warning on tab 7 \(net::ERR_CERT_AUTHORITY_INVALID, host nas\.local\)/);
  assert.match(out, /call proceed_insecure with tabId=7/, "the daemon keys auto-proceed off this phrase");
});

test("navigate reports a plain load failure without offering proceed_insecure", async () => {
  const { sb, certError } = certChrome();
  certError("net::ERR_NAME_NOT_RESOLVED");
  const out = await sb.navigate({ url: "https://nas.local/", tabId: 7 });
  assert.match(out, /failed to load \(net::ERR_NAME_NOT_RESOLVED/);
  assert.doesNotMatch(out, /proceed_insecure/);
});

test("the opaque 'showing error page' error is explained", async () => {
  const { sb, certError } = certChrome();
  certError();
  const msg = await sb.explainError({ tabId: 7 }, "Frame with ID 0 is showing error page");
  assert.match(msg, /tab 7 is showing Chrome's certificate warning .* call proceed_insecure with tabId=7/);
  assert.strictEqual(await sb.explainError({}, "no element matched selector: #x"), "no element matched selector: #x");
});

test("a new navigation forgets the previous certificate error", async () => {
  const { sb, st, certError } = certChrome();
  certError();
  st.nav.before({ frameId: 0, tabId: 7 });
  assert.strictEqual(await sb.lastNavError(7), null);
});

test("proceed_insecure uses the warning page's proceed link first, then detaches", async () => {
  const { sb, st, certError } = certChrome({ proceedLink: true });
  certError();
  const out = await sb.proceedInsecure({ tabId: 7 });
  assert.match(out, /^ok: continued past the certificate warning for nas\.local via proceed link \(was net::ERR_CERT_AUTHORITY_INVALID\)/);
  assert.deepStrictEqual(st.commands, ["Runtime.evaluate"]);
  assert.strictEqual(st.detached, 1, "a remembered decision needs no debugger afterwards");
});

test("proceed_insecure types thisisunsafe when there is no proceed link (HSTS)", async () => {
  const { sb, st, certError } = certChrome({ keysWork: true });
  certError("net::ERR_CERT_COMMON_NAME_INVALID");
  const out = await sb.proceedInsecure({ tabId: 7 });
  assert.match(out, /via thisisunsafe keys/);
  assert.strictEqual(st.keys.join(""), "thisisunsafe");
  assert.strictEqual(st.detached, 1);
});

test("proceed_insecure falls back to ignoring certificate errors and keeps the debugger", async () => {
  const { sb, st, certError } = certChrome({ ignoreWorks: true });
  certError();
  const out = await sb.proceedInsecure({ tabId: 7 });
  assert.match(out, /via ignore certificate errors/);
  assert.match(out, /note: certificate errors stay ignored/);
  assert.strictEqual(st.detached, 0, "ignoring only lasts while attached");
});

test("proceed_insecure names the remaining ways when nothing works", async () => {
  const { sb, st, certError } = certChrome();
  certError();
  await assert.rejects(() => sb.proceedInsecure({ tabId: 7 }), (e) => {
    assert.match(e.message, /^certificate warning is still showing on tab 7 after trying: thisisunsafe keys, ignore certificate errors/, "the daemon keys its keyboard fallback off this phrase");
    assert.match(e.message, /aglink-screen/);
    assert.match(e.message, /aglink-web trust-cert/);
    return true;
  });
  assert.strictEqual(st.detached, 1);
});

test("proceed_insecure refuses a page that is not a certificate warning", async () => {
  const { sb, st, certError } = certChrome();
  certError("net::ERR_CONNECTION_REFUSED");
  await assert.rejects(() => sb.proceedInsecure({ tabId: 7 }), /not a certificate warning/);
  assert.strictEqual(st.attached, 0);
});

test("proceed_insecure on a normal page does nothing", async () => {
  const { sb, st } = certChrome({ onWarning: false });
  assert.match(await sb.proceedInsecure({ tabId: 7 }), /not on a warning page — nothing to do/);
  assert.strictEqual(st.attached, 0);
});

test("when the debugger cannot reach the warning page, proceed_insecure hands over to the daemon", async () => {
  const { sb, st, certError } = certChrome();
  certError();
  // Current Chrome: "Cannot attach to this target." on its warning page.
  sb.attachDebugger = async () => { throw new Error("Cannot attach to this target."); };
  await assert.rejects(() => sb.proceedInsecure({ tabId: 7 }), (e) => {
    assert.match(e.message, /^certificate warning is still showing on tab 7 \(the browser's debugger cannot reach its warning page: Cannot attach to this target\.\)/);
    return true;
  });
  assert.deepStrictEqual(st.commands, [], "no step may run without the debugger");
});

test("waitOnly reports whether the site came up after the daemon typed the bypass", async () => {
  const ok = certChrome({ onWarning: false });
  assert.match(await ok.sb.proceedInsecure({ tabId: 7, waitOnly: 1 }), /^ok: continued past the certificate warning for nas\.local via thisisunsafe typed into the Chrome window/);
  const stuck = certChrome();
  await assert.rejects(() => stuck.sb.proceedInsecure({ tabId: 7, waitOnly: 1 }), /certificate warning is still showing on tab 7 after typing thisisunsafe/);
});
// ---- JavaScript dialogs ----------------------------------------------------

// dialogChrome mocks tab 7 whose page is frozen behind a dialog while
// st.dialog is set. announce makes Page.enable report it the way Chrome does
// to a debugger attached before the dialog opened; handles makes
// Page.handleJavaScriptDialog work.
function dialogChrome({ dialog = null, announce = false, handles = true } = {}) {
  const st = { dialog, attached: 0, detached: 0, handled: null, onEvent: null };
  const chrome = makeChrome({
    tabs: { query: async () => [{ id: 7, active: true }] },
    scripting: {
      executeScript: () =>
        st.dialog ? new Promise(() => {}) : Promise.resolve([{ result: { found: true, text: "page" } }]),
    },
    debugger: {
      attach: async () => { st.attached++; },
      detach: async () => { st.detached++; },
      onDetach: { addListener: () => {} },
      onEvent: { addListener: (fn) => { if (!st.onEvent) st.onEvent = []; st.onEvent.push(fn); } },
      sendCommand: async (target, method, params) => {
        if (method === "Page.enable") {
          if (st.dialog && announce) for (const fn of st.onEvent) fn({ tabId: 7 }, "Page.javascriptDialogOpening", st.dialog);
          if (st.dialog) return new Promise(() => {}); // the frozen renderer never answers
          return {};
        }
        if (method === "Runtime.evaluate") return st.dialog ? new Promise(() => {}) : { result: { value: 1 } };
        if (method === "Page.handleJavaScriptDialog") {
          if (!handles) throw new Error("No dialog is showing");
          st.handled = params;
          st.dialog = null;
          return {};
        }
        return {};
      },
    },
  });
  const sb = loadBackground(chrome);
  sb.dialogCheckAfterMs = () => 50;
  return { sb, st };
}

test("a tool stuck behind a dialog fails fast, saying what the dialog asks", async () => {
  const { sb, st } = dialogChrome({ dialog: { type: "confirm", message: "Delete?" }, announce: true });
  await assert.rejects(
    () => sb.withDialogWatch("get_page_text", {}, () => sb.getPageText({})),
    /^Error: dialog open on tab 7: confirm: "Delete\?" — call handle_dialog/
  );
  assert.strictEqual(st.detached, 1, "the debugger is only borrowed to look");
});

test("a dialog Chrome does not announce is still detected from the frozen page", async () => {
  const { sb } = dialogChrome({ dialog: { type: "alert", message: "hi" } });
  assert.match(await sb.dialogStatus({ tabId: 7 }), /^a dialog \(its text is not readable from the browser/);
});

test("a slow tool on a page without a dialog is left to finish", async () => {
  const { sb } = dialogChrome();
  const out = await sb.withDialogWatch("get_page_text", {}, () => new Promise((r) => setTimeout(() => r("done"), 150)));
  assert.strictEqual(out, "done");
  assert.strictEqual(await sb.dialogStatus({ tabId: 7 }), "no dialog open");
});

test("handle_dialog answers through the debugger, with prompt text", async () => {
  const { sb, st } = dialogChrome({ dialog: { type: "prompt", message: "Name?", defaultPrompt: "x" }, announce: true });
  const out = await sb.handleDialog({ tabId: 7, prompt_text: "Kim" });
  assert.strictEqual(out, 'ok: accepted prompt: "Name?" (default "x")');
  assert.deepStrictEqual({ ...st.handled }, { accept: true, promptText: "Kim" });
});

test("handle_dialog with accept=false presses Cancel", async () => {
  const { sb, st } = dialogChrome({ dialog: { type: "confirm", message: "Sure?" }, announce: true });
  assert.match(await sb.handleDialog({ tabId: 7, accept: "false" }), /^ok: dismissed confirm/);
  assert.strictEqual(st.handled.accept, false);
});

test("when the debugger cannot answer, handle_dialog hands over to the daemon's keyboard", async () => {
  const { sb } = dialogChrome({ dialog: { type: "alert", message: "hi" }, handles: false });
  await assert.rejects(() => sb.handleDialog({ tabId: 7 }), /^Error: dialog needs the keyboard on tab 7 /);
});

test("handle_dialog with no dialog says so, not 'needs the keyboard'", async () => {
  const { sb } = dialogChrome();
  await assert.rejects(() => sb.handleDialog({ tabId: 7 }), /^Error: no dialog open$/);
});
