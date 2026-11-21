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

// webPortFile is the port the running aglink-web recorded
// (~/.aglink/aglink-web.port), or 0 when there is none.
function webPortFile() {
  try {
    const n = parseInt(fs.readFileSync(path.join(aglinkDir(), "aglink-web.port"), "utf8").trim(), 10);
    return n > 0 && n < 65536 ? n : 0;
  } catch (e) {
    return 0;
  }
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
// Every window's extension host may get here at once. Making or repairing
// the file happens under a lock shared by all of them (a lock folder, which
// only one can create), and the file is checked again once the lock is held,
// so a token another window has just written is taken, never overwritten.
// The token is written to a temporary file and renamed into place, so the
// file only ever appears whole to a reader outside the lock. Throws, with the
// cause, when the token cannot be kept.
const TOKEN_RE = /^[0-9A-Za-z._~+/=-]{16,}$/;
const LOCK_WAIT_MS = 5000;
const LOCK_STALE_MS = 15000; // a lock this old was left by a crashed window

function sleepSync(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

// Windows refuses for a moment to rename over a file another process has
// open, or to create a folder another process is removing: EPERM / EACCES /
// EBUSY there mean "try again shortly", not "not allowed".
const TRANSIENT = new Set(["EPERM", "EACCES", "EBUSY"]);
function retrying(fn, ms = 2000) {
  const until = Date.now() + ms;
  for (;;) {
    try {
      return fn();
    } catch (e) {
      if (!TRANSIENT.has(e.code) || Date.now() > until) throw e;
      sleepSync(10 + Math.floor(Math.random() * 20));
    }
  }
}

// withLock runs fn holding the lock folder lock, taking over one left stale.
function withLock(lock, fn) {
  const until = Date.now() + LOCK_WAIT_MS;
  for (;;) {
    try {
      fs.mkdirSync(lock);
      break;
    } catch (e) {
      if (TRANSIENT.has(e.code) && Date.now() <= until) {
        sleepSync(20); // the folder is being removed by the window that held it
        continue;
      }
      if (e.code !== "EEXIST") throw e;
      try {
        if (Date.now() - fs.statSync(lock).mtimeMs > LOCK_STALE_MS) {
          fs.rmdirSync(lock);
          continue;
        }
      } catch (e2) {
        continue; // released meanwhile
      }
      if (Date.now() > until) throw new Error(`another window holds ${lock}`);
      sleepSync(20);
    }
  }
  try {
    return fn();
  } finally {
    try {
      retrying(() => fs.rmdirSync(lock));
    } catch (e) {}
  }
}

function screenToken() {
  const env = (process.env.AGLINK_SCREEN_REMOTE_TOKEN || "").trim();
  if (env) return env;
  const dir = aglinkDir();
  const f = path.join(dir, "aglink-screen-relay.token");
  const read = () => {
    try {
      const t = retrying(() => fs.readFileSync(f, "utf8")).trim();
      return TOKEN_RE.test(t) ? t : "";
    } catch (e) {
      if (e.code === "ENOENT") return "";
      throw new Error(`cannot read ${f}: ${e.message}`);
    }
  };
  let t = read();
  if (t) return t;
  try {
    fs.mkdirSync(dir, { recursive: true });
    t = withLock(`${f}.lock`, () => {
      const now = read(); // another window may have written it while we waited
      if (now) return now;
      const tmp = `${f}.${process.pid}.${require("crypto").randomBytes(4).toString("hex")}.tmp`;
      try {
        fs.writeFileSync(tmp, require("crypto").randomBytes(24).toString("hex"), { mode: 0o600 });
        retrying(() => fs.renameSync(tmp, f)); // a window outside the lock may be reading it
      } finally {
        try {
          fs.unlinkSync(tmp);
        } catch (e) {}
      }
      return read();
    });
  } catch (e) {
    throw new Error(`cannot keep the relay's screen token in ${f}: ${e.message}`);
  }
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
  webPortFile,
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
function startDaemon(target, readPort, d = deps, webStartupPort = 0) {
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
    // aglink.port names another port than the aglink-web already running: a
    // second daemon there would take over the port file while Chrome stays on
    // the first, splitting the browser tools between two daemons.
    if (target === "web" && webStartupPort) {
      const running = d.webPortFile ? d.webPortFile() : 0;
      if (running && running !== webStartupPort && (await d.healthy(running)))
        return {
          ok: false,
          reason: `aglink-web is already running on 127.0.0.1:${running}, but the VS Code setting aglink.port is ${webStartupPort} — set aglink.port to ${running} or clear it (a second aglink-web was not started)`,
        };
    }
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
    } else if (webStartupPort) {
      opts.env = { ...process.env, AGLINK_WEB_PORT: String(webStartupPort) };
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

async function relay(req, readPort, d = deps, webStartupPort = 0) {
  const managed = req.target === "web" || req.target === "screen";
  let res;
  try {
    res = await d.relayOnce(req, readPort);
  } catch (e) {
    return errorResult(e.message); // e.g. the screen token could not be kept
  }
  if (res.refused && managed) {
    const st = await startDaemon(req.target, readPort, d, webStartupPort);
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
    let settled = false;
    const finish = (v) => {
      if (!settled) {
        settled = true;
        resolve(v);
      }
    };
    const fail = (msg, refused) =>
      finish({ status: 502, contentType: "application/json", body: Buffer.from(JSON.stringify({ error: `aglink relay: ${msg}` })).toString("base64"), refused });
    const r = http.request(
      { host: "127.0.0.1", port: dst.port, path: req.path || "/", method: req.method || "POST", headers, timeout: 600000 },
      (res) => {
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () =>
          finish({ status: res.statusCode, contentType: res.headers["content-type"] || "", body: Buffer.concat(chunks).toString("base64") })
        );
        // The server closing the connection partway through the answer (it
        // was restarted, say) ends neither "end" nor the request's "error":
        // without these the call would wait for ever.
        res.on("error", (e) => fail(`${req.target} (127.0.0.1:${dst.port}) closed the connection mid-answer: ${e.message}`));
        res.on("close", () => {
          if (!res.complete) fail(`${req.target} (127.0.0.1:${dst.port}) closed the connection mid-answer`);
        });
      }
    );
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
async function remoteBridgeVersion(vscode, wait) {
  let lastError;
  for (let i = 0; i < 15; i++) {
    const cmds = await vscode.commands.getCommands(true);
    if (cmds.includes("aglink.bridge.ports")) {
      try {
        const r = await vscode.commands.executeCommand("aglink.bridge.ports");
        return (r && r.version) || "0.1.0";
      } catch (e) {
        lastError = e;
      }
    }
    await wait(1000);
  }
  if (lastError) throw new Error(`the installed bridge did not answer; reload this SSH window: ${lastError.message}`);
  return "";
}

// ensureBridge installs the bundled aglink-bridge on the remote server the
// first time this PC opens a window there (or when the bundled one is newer),
// so connecting a new server needs nothing done by hand.
async function ensureBridge(vscode, extensionPath, log, wait = deps.sleep) {
  if (!vscode.env.remoteName) return;
  const bundled = path.join(extensionPath, "aglink-bridge.vsix");
  if (!fs.existsSync(bundled)) return;
  let want = "";
  try {
    want = JSON.parse(fs.readFileSync(path.join(extensionPath, "bridge-version.json"), "utf8")).version;
  } catch (e) {}
  const have = vscode.extensions.getExtension(BRIDGE_ID);
  // A UI-side extension host cannot see an extension living on the remote
  // one reliably. Ask the bridge in this window; never infer installation
  // from a previous connection of the same type (e.g. another SSH server).
  const running = await remoteBridgeVersion(vscode, wait);
  // Never downgrade: another PC with an older aglink-vscode shares this server's
  // extensions, and replacing its newer bridge would flip it back and forth.
  if (running && (!want || atLeast(running, want))) return;
  const remoteHave = have?.extensionUri?.scheme === "vscode-remote" ? have : null;
  if (remoteHave && (!want || atLeast(remoteHave.packageJSON.version, want))) {
    if (!running) log("aglink-bridge is installed but did not start; reload this SSH window and check the remote Extension Host log");
    return;
  }
  if (!running && !remoteHave) {
    // No bridge answered. When this window lists the remote host's extensions
    // the bridge is truly missing there; when it lists none, the remote
    // extension host may just be slow, and an automatic install could put the
    // bundled bridge over a newer one another PC installed — ask instead.
    const seesRemote = (vscode.extensions.all || []).some((e) => e?.extensionUri?.scheme === "vscode-remote");
    if (!seesRemote) {
      log(`aglink-bridge did not answer on ${vscode.env.remoteName} and the remote extensions are not visible yet — asking before installing ${want}`);
      const ok = await vscode.window.showInformationMessage(
        "aglink: 이 SSH 서버에서 연결 통로(aglink-bridge)가 응답하지 않습니다. 설치되어 있지 않으면 설치할까요? (이미 있으면 창을 다시 불러오세요)",
        "설치",
        "다시 불러오기"
      );
      if (ok === "다시 불러오기") return void (await vscode.commands.executeCommand("workbench.action.reloadWindow"));
      if (ok !== "설치") return;
    }
  }
  log(`installing aglink-bridge ${want} on ${vscode.env.remoteName} (had ${running || (remoteHave ? remoteHave.packageJSON.version : "none")})`);
  try {
    await vscode.commands.executeCommand("workbench.extensions.installExtension", vscode.Uri.file(bundled));
  } catch (e) {
    log(`aglink-bridge install failed: ${e.message}`);
    return;
  }
  const pick = await vscode.window.showInformationMessage(
    "aglink: 이 서버에 연결 통로(aglink-bridge)를 설치했습니다. 창을 다시 불러오면 적용됩니다.",
    "다시 불러오기"
  );
  if (pick) await vscode.commands.executeCommand("workbench.action.reloadWindow");
}

module.exports = { relay, destination, ensureBridge, startDaemon, healthy, atLeast, screenToken, screenFeatures };
