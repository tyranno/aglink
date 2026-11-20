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
    assert.strictEqual((await call(port, { Authorization: "Bearer wrong-1234" })).status, 401);
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
