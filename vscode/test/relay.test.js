"use strict";

const test = require("node:test");
const assert = require("node:assert");
const { EventEmitter } = require("events");
const { relay, startDaemon } = require("../relay");

const readPort = () => 48219;
const body = (res) => JSON.parse(Buffer.from(res.body, "base64").toString("utf8"));
const ok200 = { status: 200, contentType: "application/json", body: Buffer.from("{}").toString("base64") };
const refused = { status: 502, contentType: "application/json", body: Buffer.from('{"error":"x"}').toString("base64"), refused: true };

// fakeDeps: a child that never exits; healthy after `upAfter` polls.
function fakeDeps({ exe = "C:\\aglink\\aglink-screen.exe", upAfter = 2, child, relayResults = [] } = {}) {
  const d = {
    spawned: [],
    polls: 0,
    relayCalls: 0,
    daemonExe: () => exe,
    screenToken: () => "relay-tok",
    screenFeatures: () => ["require-token"],
    spawn: (e, args, opts) => {
      d.spawned.push({ exe: e, args, opts });
      if (child) return child(e, args, opts);
      const c = new EventEmitter();
      c.unref = () => {};
      return c;
    },
    healthy: async () => ++d.polls >= upAfter,
    sleep: async () => {},
    relayOnce: async () => relayResults[Math.min(d.relayCalls++, relayResults.length - 1)],
  };
  return d;
}

test("screen daemon is started on loopback, detached, token required, with the relay's token", async () => {
  process.env.AGLINK_RELAY_TEST_MARK = "kept";
  const d = fakeDeps();
  assert.deepStrictEqual(await startDaemon("screen", readPort, d), { ok: true });
  assert.strictEqual(d.spawned.length, 1);
  const s = d.spawned[0];
  assert.deepStrictEqual(s.args, ["serve", "--addr", "127.0.0.1:48220", "--require-token"]);
  assert.strictEqual(s.opts.detached, true);
  assert.strictEqual(s.opts.env.AGLINK_SCREEN_REMOTE_TOKEN, "relay-tok");
  assert.strictEqual(s.opts.env.AGLINK_RELAY_TEST_MARK, "kept", "the rest of the environment is inherited");
  delete process.env.AGLINK_RELAY_TEST_MARK;
});

test("web readiness follows the port file as the daemon rewrites it", async () => {
  let port = 48221; // a stale port file
  const d = fakeDeps({ exe: "C:\aglink\aglink-web.exe" });
  const checked = [];
  d.healthy = async (p) => {
    checked.push(p);
    if (checked.length === 2) port = 48219; // the daemon bound and rewrote the file
    return p === 48219;
  };
  const r = await startDaemon("web", () => port, d);
  assert.deepStrictEqual(r, { ok: true });
  assert.deepStrictEqual(checked.slice(0, 2), [48221, 48221]);
  assert.strictEqual(checked[checked.length - 1], 48219);
  assert.strictEqual(d.spawned[0].opts.env, undefined, "web inherits the environment as is");
});

test("screenToken prefers VS Code's AGLINK_SCREEN_REMOTE_TOKEN, else keeps one relay token on disk", () => {
  const fs = require("fs");
  const os = require("os");
  const path = require("path");
  const { screenToken } = require("../relay");
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "aglink-relay-"));
  const saved = { home: process.env.AGLINK_HOME, tok: process.env.AGLINK_SCREEN_REMOTE_TOKEN };
  try {
    process.env.AGLINK_HOME = home;
    delete process.env.AGLINK_SCREEN_REMOTE_TOKEN;
    const a = screenToken();
    assert.match(a, /^[0-9a-f]{48}$/);
    assert.strictEqual(screenToken(), a, "the same token every time");
    assert.strictEqual(fs.readFileSync(path.join(home, "aglink-screen-relay.token"), "utf8"), a);
    process.env.AGLINK_SCREEN_REMOTE_TOKEN = " pc-token ";
    assert.strictEqual(screenToken(), "pc-token");
  } finally {
    if (saved.home === undefined) delete process.env.AGLINK_HOME;
    else process.env.AGLINK_HOME = saved.home;
    if (saved.tok === undefined) delete process.env.AGLINK_SCREEN_REMOTE_TOKEN;
    else process.env.AGLINK_SCREEN_REMOTE_TOKEN = saved.tok;
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test("web daemon gets plain serve", async () => {
  const d = fakeDeps({ exe: "C:\\aglink\\aglink-web.exe" });
  await startDaemon("web", readPort, d);
  assert.deepStrictEqual(d.spawned[0].args, ["serve"]);
});

test("concurrent requests share one start", async () => {
  const d = fakeDeps({ upAfter: 3 });
  const [a, b] = await Promise.all([startDaemon("screen", readPort, d), startDaemon("screen", readPort, d)]);
  assert.ok(a.ok && b.ok);
  assert.strictEqual(d.spawned.length, 1);
});

test("missing exe is reported, not thrown", async () => {
  const r = await startDaemon("screen", readPort, fakeDeps({ exe: "" }));
  assert.strictEqual(r.ok, false);
  assert.match(r.reason, /aglink-screen\.exe not found/);
  assert.match(r.reason, /Programs\\aglink-screen/);
});

test("spawn error event does not throw and is reported", async () => {
  const d = fakeDeps({
    upAfter: Infinity,
    child: () => {
      const c = new EventEmitter();
      c.unref = () => {};
      setImmediate(() => c.emit("error", new Error("EACCES")));
      return c;
    },
  });
  d.sleep = () => new Promise((r) => setImmediate(r));
  const r = await startDaemon("screen", readPort, d);
  assert.strictEqual(r.ok, false);
  assert.match(r.reason, /could not start .*EACCES/);
});

test("synchronous spawn throw is reported", async () => {
  const d = fakeDeps({
    child: () => {
      throw new Error("bad exe format");
    },
  });
  const r = await startDaemon("screen", readPort, d);
  assert.strictEqual(r.ok, false);
  assert.match(r.reason, /bad exe format/);
});

test("early exit names the port", async () => {
  const d = fakeDeps({
    upAfter: Infinity,
    child: () => {
      const c = new EventEmitter();
      c.unref = () => {};
      setImmediate(() => c.emit("exit", 1, null));
      return c;
    },
  });
  d.sleep = () => new Promise((r) => setImmediate(r));
  const r = await startDaemon("screen", readPort, d);
  assert.strictEqual(r.ok, false);
  assert.match(r.reason, /exited right away \(code 1\).*127\.0\.0\.1:48220/);
});

test("relay starts the daemon on refusal and retries exactly once", async () => {
  const d = fakeDeps({ relayResults: [refused, ok200] });
  const res = await relay({ target: "screen", path: "/mcp" }, readPort, d);
  assert.strictEqual(res.status, 200);
  assert.strictEqual(res.refused, undefined);
  assert.strictEqual(d.relayCalls, 2);
  assert.strictEqual(d.spawned.length, 1);
});

test("relay does not loop when the retry is refused again", async () => {
  const d = fakeDeps({ relayResults: [refused, refused, ok200] });
  const res = await relay({ target: "screen", path: "/mcp" }, readPort, d);
  assert.strictEqual(res.status, 502);
  assert.strictEqual(d.relayCalls, 2);
});

test("relay reports a failed start instead of retrying", async () => {
  const d = fakeDeps({ exe: "", relayResults: [refused, ok200] });
  const res = await relay({ target: "screen", path: "/mcp" }, readPort, d);
  assert.strictEqual(res.status, 502);
  assert.strictEqual(d.relayCalls, 1);
  assert.match(body(res).error, /could not be started: aglink-screen\.exe not found/);
});

test("voice is never auto-started", async () => {
  const d = fakeDeps({ relayResults: [refused] });
  const res = await relay({ target: "voice", path: "/stop" }, readPort, d);
  assert.strictEqual(res.status, 502);
  assert.strictEqual(d.spawned.length, 0);
});

test("401 from the PC server explains the token mismatch and keeps the server", async () => {
  const d = fakeDeps({ relayResults: [{ status: 401, contentType: "application/json", body: Buffer.from('{"error":"nope"}').toString("base64") }] });
  const res = await relay({ target: "web", path: "/call" }, readPort, d);
  assert.strictEqual(res.status, 401);
  assert.match(body(res).error, /aglink-web\.token/);
  assert.strictEqual(body(res).detail, '{"error":"nope"}');
  assert.strictEqual(d.spawned.length, 0);
});

test("bridge versions compare numerically, so a newer bridge is never downgraded", () => {
  const { atLeast } = require("../relay");
  assert.ok(atLeast("0.2.0", "0.1.0"));
  assert.ok(atLeast("0.10.0", "0.9.1"));
  assert.ok(atLeast("0.2.0", "0.2.0"));
  assert.ok(!atLeast("0.1.0", "0.2.0"));
});

test("an aglink-screen.exe that cannot require a token is not started", async () => {
  for (const feats of [null, [], ["something-else"]]) {
    const d = fakeDeps();
    d.screenFeatures = () => feats;
    const r = await startDaemon("screen", readPort, d);
    assert.strictEqual(r.ok, false);
    assert.match(r.reason, /too old to require a token.*update aglink/);
    assert.strictEqual(d.spawned.length, 0);
  }
});

test("a screen token that cannot be kept stops the start with its cause", async () => {
  const d = fakeDeps();
  d.screenToken = () => {
    throw new Error("cannot keep the relay's screen token in X: EACCES");
  };
  const r = await startDaemon("screen", readPort, d);
  assert.strictEqual(r.ok, false);
  assert.match(r.reason, /EACCES/);
  assert.strictEqual(d.spawned.length, 0);
});

test("relay turns a thrown destination error into a 502", async () => {
  const d = fakeDeps();
  d.relayOnce = async () => {
    throw new Error("cannot keep the relay's screen token in X: EROFS");
  };
  const res = await relay({ target: "screen", path: "/mcp" }, readPort, d);
  assert.strictEqual(res.status, 502);
  assert.match(body(res).error, /EROFS/);
});

function withHome(fn) {
  const fs = require("fs");
  const os = require("os");
  const path = require("path");
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "aglink-relay-"));
  const saved = { home: process.env.AGLINK_HOME, tok: process.env.AGLINK_SCREEN_REMOTE_TOKEN };
  process.env.AGLINK_HOME = home;
  delete process.env.AGLINK_SCREEN_REMOTE_TOKEN;
  try {
    return fn(home, path.join(home, "aglink-screen-relay.token"), fs);
  } finally {
    if (saved.home === undefined) delete process.env.AGLINK_HOME;
    else process.env.AGLINK_HOME = saved.home;
    if (saved.tok !== undefined) process.env.AGLINK_SCREEN_REMOTE_TOKEN = saved.tok;
    fs.rmSync(home, { recursive: true, force: true });
  }
}

test("an empty or broken token file is replaced once, and every call then agrees", () => {
  const { screenToken } = require("../relay");
  for (const junk of ["", "   \n", "short"]) {
    withHome((home, f, fs) => {
      fs.writeFileSync(f, junk);
      const a = screenToken();
      assert.match(a, /^[0-9a-f]{48}$/);
      assert.strictEqual(screenToken(), a);
      assert.strictEqual(screenToken(), a);
      assert.strictEqual(fs.readFileSync(f, "utf8"), a);
      assert.deepStrictEqual(fs.readdirSync(home).filter((n) => n.endsWith(".tmp")), [], "no temporary file left");
    });
  }
});

test("a token that cannot be written is an error, not a fresh value per call", () => {
  const { screenToken } = require("../relay");
  withHome((home, f, fs) => {
    fs.mkdirSync(f); // the token path is a folder: it can be neither read nor written
    assert.throws(() => screenToken(), /aglink-screen-relay\.token/);
    assert.throws(() => screenToken(), /aglink-screen-relay\.token/);
  });
});

test("windows repairing the token at the same moment all end up with the one on disk", async () => {
  const { spawn } = require("child_process");
  const path = require("path");
  const relayPath = path.join(__dirname, "..", "relay.js");
  const worker = `
    const until = Number(process.argv[1]);
    while (Date.now() < until) {} // start together
    process.stdout.write(require(${JSON.stringify(relayPath)}).screenToken());
  `;
  const run = (home, until) =>
    new Promise((resolve, reject) => {
      const env = { ...process.env, AGLINK_HOME: home };
      delete env.AGLINK_SCREEN_REMOTE_TOKEN;
      const c = spawn(process.execPath, ["-e", worker, String(until)], { env });
      let out = "";
      let err = "";
      c.stdout.on("data", (d) => (out += d));
      c.stderr.on("data", (d) => (err += d));
      c.on("exit", (code) => (code === 0 ? resolve(out) : reject(new Error(err))));
    });
  const fs = require("fs");
  const os = require("os");
  for (let round = 0; round < 5; round++) {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), "aglink-race-"));
    const f = path.join(home, "aglink-screen-relay.token");
    try {
      if (round % 2 === 0) fs.writeFileSync(f, ""); // an empty file to repair; odd rounds: none yet
      const until = Date.now() + 700;
      const got = await Promise.all(Array.from({ length: 6 }, () => run(home, until)));
      const disk = fs.readFileSync(f, "utf8");
      assert.match(disk, /^[0-9a-f]{48}$/);
      assert.deepStrictEqual(got, Array(6).fill(disk), `round ${round}: every window returns the token on disk`);
      assert.ok(!fs.existsSync(`${f}.lock`), "the lock is released");
    } finally {
      fs.rmSync(home, { recursive: true, force: true });
    }
  }
});

test("a lock left by a crashed window is taken over", () => {
  const { screenToken } = require("../relay");
  withHome((home, f, fs) => {
    fs.mkdirSync(`${f}.lock`);
    const old = new Date(Date.now() - 60000);
    fs.utimesSync(`${f}.lock`, old, old);
    const t = screenToken();
    assert.match(t, /^[0-9a-f]{48}$/);
    assert.ok(!fs.existsSync(`${f}.lock`));
  });
});
