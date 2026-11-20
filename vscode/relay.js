// relay: the local half of the per-window aglink channel (see bridge/extension.js).
//
// In a Remote-SSH window, aglink-bridge runs on the server and hands each
// request it receives to the "aglink.relay" command registered here. VS Code
// runs that command in this UI extension host — on the PC the window is shown
// on — so the request lands on THIS PC's loopback services, whichever route the
// window took to the server and however many other PCs share that server.
//
// Only fixed loopback targets are reachable; the server side cannot name a host
// or port. The aglink-web token is added here, so the server never holds it.
"use strict";

const fs = require("fs");
const http = require("http");
const os = require("os");
const path = require("path");

const BRIDGE_ID = "aglink.aglink-bridge";

function aglinkDir() {
  return process.env.AGLINK_HOME || path.join(os.homedir(), ".aglink");
}

function webToken() {
  try {
    return fs.readFileSync(path.join(aglinkDir(), "aglink-web.token"), "utf8").trim();
  } catch (e) {
    return "";
  }
}

// screenToken is the token the relay sends to this PC's aglink-screen server:
// the PC's own AGLINK_SCREEN_REMOTE_TOKEN when VS Code has one, else a
// relay-only token kept in ~/.aglink/aglink-screen-relay.token (made on first
// use). A server the relay starts gets the same token and --require-token, so
// it answers nothing on this PC that does not carry it — loopback included.
//
// Every window's extension host may get here at once, so the file only ever
// appears whole: a token is written to a temporary file and then hard-linked
// into place (creating it only if absent) or, over an empty or broken one,
// renamed onto it; what is on disk afterwards is what is returned, so every
// caller agrees. Throws, with the cause, when the token cannot be kept.
const TOKEN_RE = /^[0-9A-Za-z._~+/=-]{16,}$/;
function screenToken() {
  const env = (process.env.AGLINK_SCREEN_REMOTE_TOKEN || "").trim();
  if (env) return env;
  const dir = aglinkDir();
  const f = path.join(dir, "aglink-screen-relay.token");
  const read = () => {
    try {
      const t = fs.readFileSync(f, "utf8").trim();
      return TOKEN_RE.test(t) ? t : "";
    } catch (e) {
      if (e.code === "ENOENT") return "";
      throw new Error(`cannot read ${f}: ${e.message}`);
    }
  };
  let t = read();
  if (t) return t;
  const tmp = `${f}.${process.pid}.${require("crypto").randomBytes(4).toString("hex")}.tmp`;
  try {
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(tmp, require("crypto").randomBytes(24).toString("hex"), { mode: 0o600 });
    try {
      fs.linkSync(tmp, f); // only if absent: the first window's token wins
    } catch (e) {
      if (e.code !== "EEXIST") throw e;
      if (!read()) fs.renameSync(tmp, f); // there, but empty or broken: replace it whole
    }
  } catch (e) {
    throw new Error(`cannot keep the relay's screen token in ${f}: ${e.message}`);
  } finally {
    try {
      fs.unlinkSync(tmp);
    } catch (e) {}
  }
  t = read();
  if (!t) throw new Error(`cannot keep the relay's screen token in ${f}: it reads back empty`);
  return t;
}

// screenFeatures lists what an aglink-screen.exe supports (`features`), or
// null for a build too old to have the command — one that would ignore
// --require-token and serve loopback without a token.
function screenFeatures(exe) {
  const r = require("child_process").spawnSync(exe, ["features"], { encoding: "utf8", timeout: 5000, windowsHide: true });
  if (r.error || r.status !== 0) return null;
  return String(r.stdout || "").split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
}

// destination maps a relay request to a loopback port and the headers to add.
function destination(req, readPort, tokenFor = screenToken) {
  switch (req.target) {
    case "web": {
      const tok = webToken();
      return { port: readPort(), auth: tok ? "Bearer " + tok : "" };
    }
    case "screen":
      return { port: 48220, auth: "Bearer " + tokenFor() };
    case "voice": // the Stop hook: this extension's voice on 48231, the desktop app on 48230
      if (String(req.path).startsWith("/vscode/")) return { port: 48230, auth: "" };
      // the voice extension demands this token whenever it is set on the PC
      return { port: 48231, auth: (process.env.AGLINK_VOICE_TOKEN || "").trim() ? "Bearer " + process.env.AGLINK_VOICE_TOKEN.trim() : "" };
    default:
      throw new Error(`unknown relay target: ${req.target}`);
  }
}

// daemonExe finds this PC's aglink-web or aglink-screen: the aglink product
// install, the standalone setup, or AGLINK_WEB_EXE / AGLINK_SCREEN_EXE.
function daemonExe(target) {
  const local = process.env.LOCALAPPDATA || "";
  const cands = [
    process.env[target === "web" ? "AGLINK_WEB_EXE" : "AGLINK_SCREEN_EXE"],
    path.join(process.env.ProgramFiles || "C:\\Program Files", "aglink", `aglink-${target}.exe`),
    local && path.join(local, "Programs", `aglink-${target}`, `aglink-${target}.exe`),
  ];
  return cands.find((p) => p && fs.existsSync(p)) || "";
}

// healthy reports whether an aglink server answers on port: /health must say
// "ok", so some other program that happens to hold the port does not pass.
function healthy(port) {
  return new Promise((resolve) => {
    const r = http.get({ host: "127.0.0.1", port, path: "/health", timeout: 500 }, (res) => {
      let body = "";
      res.setEncoding("utf8");
      res.on("data", (c) => (body += c));
      res.on("end", () => resolve(res.statusCode === 200 && body.trim() === "ok"));
    });
    r.on("error", () => resolve(false));
    r.on("timeout", () => r.destroy());
  });
}

// deps are the side effects relay() and startDaemon() use, swappable in tests.
const deps = {
  spawn: (exe, args, opts) => require("child_process").spawn(exe, args, opts),
  daemonExe,
  healthy,
  screenToken,
  screenFeatures,
  sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
  relayOnce: (req, readPort) => relayOnce(req, readPort),
};

// startDaemon brings up this PC's aglink-web or aglink-screen server when a
// Remote-SSH window asks for it. Locally an aglink-web MCP spawns its daemon on
// first use and nothing ever starts `aglink-screen serve`, so a PC that only
// serves remote windows may have neither running. The screen server gets the
// token destination() sends (screenToken) with --require-token, and is pinned
// to loopback, since with a token it would otherwise listen on every interface.
// aglink-web may move to another port as it starts (its port file), so its
// readiness is checked on the port read afresh each time.
// Concurrent requests for one target share one attempt. Resolves to
// { ok: true } or { ok: false, reason } — never throws, and a child that fails
// to spawn or exits at once is reported instead of waited out.
const starting = {};
function startDaemon(target, readPort, d = deps) {
  if (starting[target]) return starting[target];
  starting[target] = (async () => {
    const exe = d.daemonExe(target);
    const name = `aglink-${target}`;
    if (!exe)
      return {
        ok: false,
        reason: `${name}.exe not found (looked in AGLINK_${target.toUpperCase()}_EXE, Program Files\\aglink, %LOCALAPPDATA%\\Programs\\${name})`,
      };
    const portNow = () => (target === "web" ? readPort() : 48220);
    const port = portNow();
    const screen = target === "screen";
    const args = screen ? ["serve", "--addr", `127.0.0.1:${port}`, "--require-token"] : ["serve"];
    const opts = { detached: true, stdio: "ignore", windowsHide: true };
    if (screen) {
      // An older build would ignore --require-token and come up fine, serving
      // loopback without a token; it is not started at all.
      const feats = d.screenFeatures(exe);
      if (!feats || !feats.includes("require-token"))
        return {
          ok: false,
          reason: `${exe} is too old to require a token on loopback, so it was not started — update aglink on this PC (aglink-Setup.exe) or the standalone aglink-screen setup`,
        };
      try {
        opts.env = { ...process.env, AGLINK_SCREEN_REMOTE_TOKEN: d.screenToken() };
      } catch (e) {
        return { ok: false, reason: e.message };
      }
    }
    let died = "";
    let child;
    try {
      child = d.spawn(exe, args, opts);
    } catch (e) {
      return { ok: false, reason: `could not start ${exe}: ${e.message}` };
    }
    child.on("error", (e) => (died = died || `could not start ${exe}: ${e.message}`));
    child.on("exit", (code, signal) => {
      died =
        died ||
        `${name} ${args.join(" ")} exited right away (${code !== null && code !== undefined ? `code ${code}` : signal})` +
          ` — is 127.0.0.1:${port} held by another program?`;
    });
    if (child.unref) child.unref();
    for (let i = 0; i < 50; i++) {
      await d.sleep(100);
      if (await d.healthy(portNow())) return { ok: true };
      if (died) return { ok: false, reason: died };
    }
    return { ok: false, reason: `${name} started but 127.0.0.1:${portNow()}/health did not answer "ok" within 5s` };
  })().finally(() => {
    delete starting[target];
  });
  return starting[target];
}

function errorResult(msg, extra) {
  return {
    status: 502,
    contentType: "application/json",
    body: Buffer.from(JSON.stringify({ error: `aglink relay: ${msg}`, ...extra })).toString("base64"),
  };
}

const TOKEN_HINT = {
  web: "the token in ~/.aglink/aglink-web.token is not the one the running aglink-web serve was started with — restart aglink-web serve",
  screen:
    "the running aglink-screen serve was started with another token (AGLINK_SCREEN_REMOTE_TOKEN, or ~/.aglink/aglink-screen-relay.token when VS Code has none) — restart that server, or restart VS Code after changing the token",
};

async function relay(req, readPort, d = deps) {
  const managed = req.target === "web" || req.target === "screen";
  let res;
  try {
    res = await d.relayOnce(req, readPort);
  } catch (e) {
    return errorResult(e.message); // e.g. the screen token could not be kept
  }
  if (res.refused && managed) {
    const st = await startDaemon(req.target, readPort, d);
    res = st.ok
      ? await d.relayOnce(req, readPort).catch((e) => errorResult(e.message))
      : errorResult(`${req.target} is not running on this PC and could not be started: ${st.reason}`);
  }
  if (res.status === 401 && managed) {
    // The PC's server refused the token this relay added. Say which one; the
    // running server is left alone, it may belong to someone else.
    let detail = "";
    try {
      detail = Buffer.from(res.body || "", "base64").toString("utf8");
    } catch (e) {}
    res = { ...errorResult(`this PC's aglink-${req.target} refused the relay's token (401): ${TOKEN_HINT[req.target]}`, { detail }), status: 401 };
  }
  const { refused, ...out } = res; // eslint-disable-line no-unused-vars
  return out;
}

function relayOnce(req, readPort) {
  const dst = destination(req, readPort);
  const body = Buffer.from(req.body || "", "base64");
  const headers = { "Content-Length": body.length };
  if (req.headers && req.headers["content-type"]) headers["Content-Type"] = req.headers["content-type"];
  if (req.headers && req.headers.accept) headers.Accept = req.headers.accept;
  if (dst.auth) headers.Authorization = dst.auth;
  return new Promise((resolve) => {
    const r = http.request(
      { host: "127.0.0.1", port: dst.port, path: req.path || "/", method: req.method || "POST", headers, timeout: 600000 },
      (res) => {
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () =>
          resolve({ status: res.statusCode, contentType: res.headers["content-type"] || "", body: Buffer.concat(chunks).toString("base64") })
        );
      }
    );
    const fail = (msg, refused) =>
      resolve({ status: 502, contentType: "application/json", body: Buffer.from(JSON.stringify({ error: `aglink relay: ${msg}` })).toString("base64"), refused });
    r.on("error", (e) => fail(`${req.target} (127.0.0.1:${dst.port}) is not reachable on this PC: ${e.message}`, e.code === "ECONNREFUSED"));
    r.on("timeout", () => r.destroy(new Error("timed out")));
    r.end(body);
  });
}

// atLeast reports whether version a >= b (dotted numbers).
function atLeast(a, b) {
  const pa = String(a).split(".").map(Number);
  const pb = String(b).split(".").map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const x = pa[i] || 0;
    const y = pb[i] || 0;
    if (x !== y) return x > y;
  }
  return true;
}

// remoteBridgeVersion reports the aglink-bridge running on the remote extension
// host, or "" when none shows up. The bridge activates on "*" but may still be
// starting, so wait a little for its command. 0.1.0 answers without a version.
async function remoteBridgeVersion(vscode, tries = 15) {
  for (let i = 0; i < tries; i++) {
    const cmds = await vscode.commands.getCommands(true);
    if (cmds.includes("aglink.bridge.ports")) {
      try {
        const r = await vscode.commands.executeCommand("aglink.bridge.ports");
        return (r && r.version) || "0.1.0";
      } catch (e) {
        return "0.1.0";
      }
    }
    await new Promise((r) => setTimeout(r, 1000));
  }
  return "";
}

// ensureBridge installs the bundled aglink-bridge on the remote server the
// first time this PC opens a window there (or when the bundled one is newer),
// so connecting a new server needs nothing done by hand.
async function ensureBridge(vscode, extensionPath, log, state) {
  if (!vscode.env.remoteName) return;
  const bundled = path.join(extensionPath, "aglink-bridge.vsix");
  if (!fs.existsSync(bundled)) return;
  let want = "";
  try {
    want = JSON.parse(fs.readFileSync(path.join(extensionPath, "bridge-version.json"), "utf8")).version;
  } catch (e) {}
  const have = vscode.extensions.getExtension(BRIDGE_ID);
  if (have && (!want || atLeast(have.packageJSON.version, want))) return;
  // A UI-side extension host cannot see an extension living on the remote
  // one, so getExtension is no proof of absence. Commands do cross hosts: ask
  // the running bridge itself, and only fall back to what was remembered.
  const running = await remoteBridgeVersion(vscode);
  // Never downgrade: another PC with an older aglink-vscode shares this server's
  // extensions, and replacing its newer bridge would flip it back and forth.
  if (running && (!want || atLeast(running, want))) return;
  const key = `aglink.bridge.${vscode.env.remoteName}`;
  if (!running && state && want && state.get(key) === want) return;
  log(`installing aglink-bridge ${want} on ${vscode.env.remoteName} (had ${running || (have ? have.packageJSON.version : "none")})`);
  try {
    await vscode.commands.executeCommand("workbench.extensions.installExtension", vscode.Uri.file(bundled));
  } catch (e) {
    log(`aglink-bridge install failed: ${e.message}`);
    return;
  }
  if (state && want) await state.update(key, want);
  const pick = await vscode.window.showInformationMessage(
    "aglink: 이 서버에 연결 통로(aglink-bridge)를 설치했습니다. 창을 다시 불러오면 적용됩니다.",
    "다시 불러오기"
  );
  if (pick) await vscode.commands.executeCommand("workbench.action.reloadWindow");
}

module.exports = { relay, destination, ensureBridge, startDaemon, healthy, atLeast, screenToken, screenFeatures };
