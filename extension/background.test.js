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
    setTimeout,
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
