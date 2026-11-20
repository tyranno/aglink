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

// destination maps a relay request to a loopback port and the headers to add.
function destination(req, readPort) {
  switch (req.target) {
    case "web": {
      const tok = webToken();
      return { port: readPort(), auth: tok ? "Bearer " + tok : "" };
    }
    case "screen": {
      const tok = (process.env.AGLINK_SCREEN_REMOTE_TOKEN || "").trim();
      return { port: 48220, auth: tok ? "Bearer " + tok : "" };
    }
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
  sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
  relayOnce: (req, readPort) => relayOnce(req, readPort),
};

// startDaemon brings up this PC's aglink-web or aglink-screen server when a
// Remote-SSH window asks for it. Locally an aglink-web MCP spawns its daemon on
// first use and nothing ever starts `aglink-screen serve`, so a PC that only
// serves remote windows may have neither running. The screen server inherits
// AGLINK_SCREEN_REMOTE_TOKEN (the token destination() sends) and is pinned to
// loopback, since with a token it would otherwise listen on every interface.
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
    const port = destination({ target }, readPort).port;
    const args = target === "screen" ? ["serve", "--addr", `127.0.0.1:${port}`] : ["serve"];
    let died = "";
    let child;
    try {
      child = d.spawn(exe, args, { detached: true, stdio: "ignore", windowsHide: true });
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
      if (await d.healthy(port)) return { ok: true };
      if (died) return { ok: false, reason: died };
    }
    return { ok: false, reason: `${name} started but 127.0.0.1:${port}/health did not answer "ok" within 5s` };
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
    "AGLINK_SCREEN_REMOTE_TOKEN in VS Code's environment is not the one the running aglink-screen serve was started with — restart that server, or restart VS Code after changing the token",
};

async function relay(req, readPort, d = deps) {
  const managed = req.target === "web" || req.target === "screen";
  let res = await d.relayOnce(req, readPort);
  if (res.refused && managed) {
    const st = await startDaemon(req.target, readPort, d);
    res = st.ok
      ? await d.relayOnce(req, readPort)
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
  if (have && (!want || have.packageJSON.version === want)) return;
  // A UI-side extension host cannot see an extension living on the remote
  // one, so getExtension is no proof of absence. Commands do cross hosts: ask
  // the running bridge itself, and only fall back to what was remembered.
  const running = await remoteBridgeVersion(vscode);
  if (running && (!want || running === want)) return;
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

module.exports = { relay, destination, ensureBridge, startDaemon, healthy };
