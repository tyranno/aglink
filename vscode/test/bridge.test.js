"use strict";

const test = require("node:test");
const assert = require("node:assert");
const http = require("http");
const { serve } = require("../bridge/extension");

function call(port, headers) {
  return new Promise((resolve, reject) => {
    const r = http.request({ host: "127.0.0.1", port, path: "/health", method: "GET", headers }, (res) => {
      let b = "";
      res.on("data", (c) => (b += c));
      res.on("end", () => resolve({ status: res.statusCode, body: b }));
    });
    r.on("error", reject);
    r.end();
  });
}

test("bridge relays only requests carrying the window token", async () => {
  const relayed = [];
  const vscode = {
    commands: {
      executeCommand: async (cmd, req) => {
        relayed.push({ cmd, req });
        return { status: 200, contentType: "text/plain", body: Buffer.from("ok").toString("base64") };
      },
    },
  };
  const srv = await serve(vscode, "screen", "secret-123", () => {});
  const port = srv.address().port;
  try {
    assert.strictEqual((await call(port, {})).status, 401);
    const stale = await call(port, { Authorization: "Bearer wrong-1234" });
    assert.strictEqual(stale.status, 401);
    assert.match(stale.body, /new integrated terminal/);
    assert.strictEqual((await call(port, { Authorization: "Bearer secret" })).status, 401);
    assert.strictEqual(relayed.length, 0);

    const ok = await call(port, { Authorization: "Bearer secret-123" });
    assert.strictEqual(ok.status, 200);
    assert.strictEqual(ok.body, "ok");
    assert.strictEqual(relayed.length, 1);
    assert.strictEqual(relayed[0].cmd, "aglink.relay");
    assert.strictEqual(relayed[0].req.target, "screen");
    assert.strictEqual(relayed[0].req.headers.authorization, undefined, "the bridge token must not travel to the PC");
  } finally {
    srv.close();
  }
});

test("refreshCodexNotify points our notify line at a node that runs, and leaves the rest alone", () => {
  const { refreshCodexNotify } = require("../bridge/extension");
  const fs = require("fs");
  const os = require("os");
  const path = require("path");
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "aglink-codex-"));
  const env = { PATH: "" }; // no node on this host's PATH
  const cfg = path.join(home, ".codex", "config.toml");
  fs.mkdirSync(path.dirname(cfg), { recursive: true });
  const script = "/home/u/.aglink-voice/aglink-voice-codex-notify.js";
  const write = (s) => fs.writeFileSync(cfg, s);
  const find = () => "/home/u/.nvm/versions/node/v26/bin/node";
  const logs = [];
  try {
    // ours on a bare "node" that this host cannot find → fixed, other lines kept
    write(`model = "x"\n# aglink-voice-codex-notify: y\nnotify = ["node", "${script}"]\n[tui]\na = 1\n`);
    assert.strictEqual(refreshCodexNotify((m) => logs.push(m), { env, home, find }), true);
    assert.strictEqual(fs.readFileSync(cfg, "utf8"), `model = "x"\n# aglink-voice-codex-notify: y\nnotify = ["/home/u/.nvm/versions/node/v26/bin/node", "${script}"]\n[tui]\na = 1\n`);
    // ours on a VS Code server node that an update removed → fixed
    write(`notify = ["/home/u/.vscode-server/cli/servers/Stable-old/server/node", "${script}"]\n`);
    assert.strictEqual(refreshCodexNotify(() => {}, { env, home, find }), true);
    // ours on a node that exists → left alone
    if (process.platform !== "win32") {
      write(`notify = [${JSON.stringify(process.execPath)}, "${script}"]\n`);
      assert.strictEqual(refreshCodexNotify(() => {}, { env, home, find: () => "/elsewhere/node" }), false);
    }
    // another program's notify → never touched
    write(`notify = ["node", "/opt/other/notify.js"]\n`);
    assert.strictEqual(refreshCodexNotify(() => {}, { env, home, find }), false);
    assert.strictEqual(fs.readFileSync(cfg, "utf8"), `notify = ["node", "/opt/other/notify.js"]\n`);
    // no config → nothing
    fs.unlinkSync(cfg);
    assert.strictEqual(refreshCodexNotify(() => {}, { env, home, find }), false);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});
