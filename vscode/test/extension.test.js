"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { makeVSCode } = require("./fakevscode");
const { createConnector, windowIdentity } = require("../extension");

// FakeSocket records the URL it was opened with and what was sent on it; the
// test drives open/message/close by hand.
function socketFactory() {
  const sockets = [];
  class FakeSocket {
    constructor(url) {
      this.url = url;
      this.sent = [];
      this.readyState = 0;
      sockets.push(this);
    }
    send(s) { this.sent.push(JSON.parse(s)); }
    close() { this.readyState = 3; if (this.onclose) this.onclose({}); }
    open() { this.readyState = 1; if (this.onopen) this.onopen({}); }
    receive(obj) { this.onmessage({ data: JSON.stringify(obj) }); }
  }
  FakeSocket.OPEN = 1;
  return { FakeSocket, sockets };
}

function manualTimers() {
  const pending = [];
  return {
    setTimeout: (fn, ms) => { pending.push({ fn, ms }); return pending.length; },
    clearTimeout: () => {},
    pending,
    fire() { const t = pending.shift(); t.fn(); return t.ms; },
  };
}

const tick = () => new Promise((r) => setImmediate(r));

test("windowIdentity names a Remote-SSH window by its host", () => {
  const { vscode } = makeVSCode({ remote: "192.168.0.9-doowon", folder: "/home/u/backend", session: "s9" });
  assert.deepStrictEqual(windowIdentity(vscode), { name: "backend", remote: "192.168.0.9-doowon", folder: "/home/u/backend", session: "s9" });
  const local = makeVSCode({ name: "aglink", folder: "C:\\p\\aglink" });
  assert.deepStrictEqual(windowIdentity(local.vscode), { name: "aglink", remote: "", folder: "C:\\p\\aglink", session: "sess-1" });
});

test("connects with the window's identity, answers pings and requests", async () => {
  const { vscode } = makeVSCode({ remote: "host1" });
  const { FakeSocket, sockets } = socketFactory();
  const timers = manualTimers();
  const methods = { workspace: async () => "name: backend", read: async () => { throw new Error("cannot open x"); } };
  const c = createConnector({ vscode, WebSocketImpl: FakeSocket, readPort: () => 48299, methods, timers, log: () => {} });
  c.start();
  const s = sockets[0];
  assert.strictEqual(s.url, "ws://127.0.0.1:48299/vscode?name=backend&remote=host1&folder=%2Fhome%2Fu%2Fbackend&session=sess-1");
  s.open();

  s.receive({ id: 0, method: "ping" });
  s.receive({ id: 7, method: "workspace", params: {} });
  s.receive({ id: 8, method: "read", params: { path: "x" } });
  s.receive({ id: 9, method: "nope", params: {} });
  await tick(); await tick();
  assert.deepStrictEqual(s.sent[0], { id: 0, ok: true });
  assert.deepStrictEqual(s.sent.find((m) => m.id === 7), { id: 7, ok: true, text: "name: backend" });
  assert.deepStrictEqual(s.sent.find((m) => m.id === 8), { id: 8, ok: false, error: "cannot open x" });
  assert.match(s.sent.find((m) => m.id === 9).error, /unknown method: nope/);
  c.stop();
});

test("reconnects with growing backoff, and resets it after a good connection", async () => {
  const { vscode } = makeVSCode({});
  const { FakeSocket, sockets } = socketFactory();
  const timers = manualTimers();
  const c = createConnector({ vscode, WebSocketImpl: FakeSocket, readPort: () => 48219, methods: {}, timers, log: () => {} });
  c.start();
  sockets[0].close(); // never opened
  assert.strictEqual(timers.fire(), 1000);
  sockets[1].close();
  assert.strictEqual(timers.fire(), 2000);
  sockets[2].open();
  sockets[2].close();
  assert.strictEqual(timers.fire(), 1000, "a connection that opened resets the backoff");
  c.stop();
  sockets[3].close();
  assert.strictEqual(timers.pending.length, 0, "stop() must end the reconnect loop");
});

test("port comes from the setting when set", () => {
  const { vscode } = makeVSCode({ config: { port: 48299 } });
  const { FakeSocket, sockets } = socketFactory();
  const c = createConnector({ vscode, WebSocketImpl: FakeSocket, readPort: () => 48219, methods: {}, timers: manualTimers(), log: () => {} });
  c.start();
  assert.match(sockets[0].url, /^ws:\/\/127\.0\.0\.1:48299\//);
  c.stop();
});
