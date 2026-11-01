"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("fs");
const os = require("os");
const path = require("path");
const { parseEvents, ensureStopHook, HOOK_SCRIPT } = require("../voice");

test("parseEvents keeps a half-received event for the next chunk", () => {
  const a = parseEvents(': hi\n\ndata: {"type":"prompt","text":"안녕"}\n\ndata: {"ty');
  assert.deepStrictEqual(a.events, [{ type: "prompt", text: "안녕" }]);
  const b = parseEvents(a.rest + 'pe":"attached","on":false}\n\n');
  assert.deepStrictEqual(b.events, [{ type: "attached", on: false }]);
  assert.strictEqual(b.rest, "");
});

test("ensureStopHook adds the hook once and keeps other settings", () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "aglink-voice-"));
  const old = process.env.AGLINK_HOME;
  delete process.env.AGLINK_HOME;
  try {
    fs.mkdirSync(path.join(home, ".claude"));
    const file = path.join(home, ".claude", "settings.json");
    const other = { hooks: [{ type: "command", command: "echo hi" }] };
    fs.writeFileSync(file, JSON.stringify({ model: "opus", hooks: { Stop: [other] } }));
    assert.strictEqual(ensureStopHook(home), true);
    assert.strictEqual(ensureStopHook(home), false);
    const s = JSON.parse(fs.readFileSync(file, "utf8"));
    assert.strictEqual(s.model, "opus");
    assert.strictEqual(s.hooks.Stop.length, 2);
    assert.deepStrictEqual(s.hooks.Stop[0], other);
    assert.match(s.hooks.Stop[1].hooks[0].command, /^node ".*\/\.aglink\/vscode-voice-hook\.js"$/);
    assert.strictEqual(fs.readFileSync(path.join(home, ".aglink", "vscode-voice-hook.js"), "utf8"), HOOK_SCRIPT);
  } finally {
    if (old !== undefined) process.env.AGLINK_HOME = old;
    fs.rmSync(home, { recursive: true, force: true });
  }
});
