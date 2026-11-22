// aglink-bridge: the remote half of the per-window aglink channel.
//
// A workspace extension, so in a Remote-SSH window it runs on the server, in
// that window's own extension host. It opens loopback listeners on free ports
// for aglink-web, aglink-screen and the voice Stop hook, and hands every request
// to aglink-vscode's "aglink.relay" command, which VS Code runs on the PC the
// window is shown on. No SSH forwards, no ports agreed in advance: each window
// gets its own ports, so any number of PCs (or one PC over several routes) can
// be connected to the same server account at once.
//
// The ports reach Claude Code through the environment: process.env for the
// Claude Code extension (spawned later from this same extension host) and the
// terminal environment collection for `claude` run in a terminal. The aglink-web
// / aglink-screen MCP clients and the voice Stop hook read AGLINK_*_PORT from
// the environment they inherit; ~/.claude.json does not name the ports.
//
// Every listener is loopback, but the server may be shared, so loopback is not
// proof of who is calling: each window also has a random AGLINK_BRIDGE_TOKEN
// (kept with its ports across reloads in the workspace state),
// exported next to the ports, and a request without it is refused before it
// reaches the PC. The clients send it as "Authorization: Bearer <token>".
"use strict";

const crypto = require("crypto");
const http = require("http");

const TARGETS = { web: "AGLINK_WEB_PORT", screen: "AGLINK_SCREEN_PORT", voice: "AGLINK_VOICE_PORT" };
const TOKEN_ENV = "AGLINK_BRIDGE_TOKEN";
// Set when a listener could not be opened, so clients report the real cause.
const ERROR_ENV = "AGLINK_BRIDGE_ERROR";
const STATE_KEY = "aglink.bridge.channel";
const MAX_BODY = 64 << 20;
// RELAY_WAIT bounds one call through the window, a little past the PC relay's
// own 600s limit so its answer (or its timeout) wins when it comes.
const RELAY_WAIT = 620000;

// authorized reports whether req carries this window's bridge token.
function authorized(req, token) {
  const m = /^Bearer\s+(.+)$/i.exec(req.headers["authorization"] || "");
  if (!m) return false;
  const got = Buffer.from(m[1].trim());
  const want = Buffer.from(token);
  return got.length === want.length && crypto.timingSafeEqual(got, want);
}

let servers = [];

// The voice Stop hook for Claude Code on this server. It posts to the bridge
// port when Claude runs under a bridged window, else to the loopback voices.
const HOOK_MARK = "aglink-voice-stop-hook";
const HOOK_SCRIPT = `// ${HOOK_MARK}: hands Claude Code's finished turn to the aglink voice
// (the VS Code extension on 48231, the aglink desktop app on 48230), which reads
// it out when voice is on. Never fails the turn. Written by aglink-bridge: under
// a Remote-SSH VS Code window AGLINK_VOICE_PORT is that window's bridge, which
// carries the turn to the PC the window is shown on.
const bridge = Number(process.env.AGLINK_VOICE_PORT) || 0;
const auth = bridge && process.env.AGLINK_BRIDGE_TOKEN ? { Authorization: "Bearer " + process.env.AGLINK_BRIDGE_TOKEN } : {};
const targets = bridge ? [[bridge, "/stop"], [bridge, "/vscode/stop"]] : [[48231, "/stop"], [48230, "/vscode/stop"]];
let d = "";
let left = targets.length;
const done = () => process.exit(0);
setTimeout(done, 3000);
process.stdin.on("data", (c) => (d += c)).on("end", () => {
  try { const h = JSON.parse(d); h.entrypoint = process.env.CLAUDE_CODE_ENTRYPOINT || ""; d = JSON.stringify(h); } catch {}
  for (const [port, path] of targets) {
    const r = require("http").request(
      { host: "127.0.0.1", port, path, method: "POST", headers: { "Content-Type": "application/json", ...auth }, timeout: 1500 },
      (res) => { res.resume(); res.on("end", () => --left || done()); }
    );
    r.on("error", () => --left || done());
    r.on("timeout", () => r.destroy());
    r.end(d);
  }
});
`;

// ensureStopHook writes the hook script and registers it in
// ~/.claude/settings.json if no aglink voice hook is there yet. An existing
// registration keeps its command (it may name a node the user chose).
function ensureStopHook(log) {
  const fs = require("fs");
  const os = require("os");
  const path = require("path");
  const dir = path.join(os.homedir(), ".aglink-voice");
  const script = path.join(dir, `${HOOK_MARK}.js`);
  fs.mkdirSync(dir, { recursive: true });
  if (!fs.existsSync(script) || fs.readFileSync(script, "utf8") !== HOOK_SCRIPT) fs.writeFileSync(script, HOOK_SCRIPT);
  const sp = path.join(os.homedir(), ".claude", "settings.json");
  let s = {};
  try {
    s = JSON.parse(fs.readFileSync(sp, "utf8"));
  } catch (e) {
    if (fs.existsSync(sp)) return; // unreadable settings: leave them alone
  }
  const hooks = JSON.stringify(s.hooks || {});
  if (hooks.includes(HOOK_MARK)) {
    // A hook on VS Code's bundled node goes stale when the server updates.
    if (!hooks.includes(".vscode-server") || hooks.includes(JSON.stringify(process.execPath).slice(1, -1))) return;
    for (const g of s.hooks.Stop || [])
      for (const h of g.hooks || [])
        if (String(h.command).includes(HOOK_MARK)) h.command = `"${process.execPath}" "${script}"`;
    fs.writeFileSync(sp, JSON.stringify(s, null, 2) + "\n");
    return;
  }
  const { spawnSync } = require("child_process");
  const found = (spawnSync("sh", ["-lc", "command -v node"], { encoding: "utf8", timeout: 5000, stdio: ["ignore", "pipe", "ignore"] }).stdout || "").trim();
  const node = found.startsWith("/") ? found : process.execPath;
  s.hooks = s.hooks || {};
  s.hooks.Stop = s.hooks.Stop || [];
  s.hooks.Stop.push({ hooks: [{ type: "command", command: `"${node}" "${script}"`, timeout: 5 }] });
  fs.mkdirSync(path.dirname(sp), { recursive: true });
  fs.writeFileSync(sp, JSON.stringify(s, null, 2) + "\n");
  log(`voice Stop hook added to ${sp}`);
}

// serve opens target's listener, on port when it is given and free (the port
// this window had before a reload), else on a free one. Right after a reload
// the old extension host may still hold the port for a moment, so a busy port
// is tried again briefly before giving it up.
async function serve(vscode, target, token, log, port = 0, d = { tries: 8, delay: 250 }) {
  if (port) {
    for (let i = 0; i < d.tries; i++) {
      try {
        return await listen(vscode, target, token, log, port);
      } catch (e) {
        if (e.code !== "EADDRINUSE") break;
        await new Promise((r) => setTimeout(r, d.delay));
      }
    }
    log(`${target}: previous port ${port} is taken, using a new one`);
  }
  return listen(vscode, target, token, log, 0);
}

function listen(vscode, target, token, log, port) {
  return new Promise((resolve, reject) => {
    const srv = http.createServer((req, res) => {
      if (!authorized(req, token)) {
        req.resume();
        res.writeHead(401, { "Content-Type": "application/json" });
        res.end(
          JSON.stringify({
            error: `aglink-bridge: ${TOKEN_ENV} missing or wrong. After reconnecting or reloading VS Code, start Claude Code or Codex again from this window. For CLI use, open a new integrated terminal; restored terminals can retain old connection settings. Use aglink-web / aglink-screen 0.2+.`,
          })
        );
        return;
      }
      const chunks = [];
      let size = 0;
      req.on("data", (c) => {
        size += c.length;
        if (size <= MAX_BODY) chunks.push(c);
      });
      req.on("end", async () => {
        if (size > MAX_BODY) {
          // Relaying the cut-off part would hand the PC broken JSON.
          res.writeHead(413, { "Content-Type": "application/json" });
          res.end(JSON.stringify({ error: `aglink-bridge: request body is ${size} bytes, over the ${MAX_BODY}-byte limit` }));
          return;
        }
        // Bounded: while the window is reconnecting (a laptop asleep) the call
        // would otherwise wait for hours, holding the client and its body.
        let timer;
        const cap = new Promise((_, reject) => {
          timer = setTimeout(() => reject(new Error(`no answer from the PC within ${RELAY_WAIT / 60000} minutes`)), RELAY_WAIT);
        });
        try {
          const r = await Promise.race([cap, vscode.commands.executeCommand("aglink.relay", {
            target,
            method: req.method,
            path: req.url,
            headers: { "content-type": req.headers["content-type"] || "", accept: req.headers["accept"] || "" },
            body: Buffer.concat(chunks).toString("base64"),
          })]);
          chunks.length = 0;
          if (res.destroyed) return; // the client gave up meanwhile
          res.writeHead(r.status, { "Content-Type": r.contentType || "application/json" });
          res.end(Buffer.from(r.body || "", "base64"));
        } catch (e) {
          const m = e && e.message ? e.message : String(e);
          log(`${target} ${req.method} ${req.url}: ${m}`);
          if (res.destroyed) return;
          res.writeHead(502, { "Content-Type": "application/json" });
          // "command not found": the PC half (aglink-vscode) is not there yet,
          // so nothing was sent and the client may try again. Any other
          // failure can come after the PC already got the call (the window
          // dropped mid-call), so it is reported as mid-answer: not retried.
          const notSent = /not found|no handler/i.test(m);
          res.end(
            JSON.stringify({
              error: notSent
                ? `aglink-bridge: the PC side is not reachable yet (${m})`
                : `aglink-bridge: the link to the PC dropped mid-answer (the call may have run): ${m}`,
            })
          );
        } finally {
          clearTimeout(timer);
          chunks.length = 0;
        }
      });
    });
    srv.once("error", reject);
    srv.listen(port, "127.0.0.1", () => {
      // From here on an error (out of file handles, say) is logged; a second
      // one would otherwise go unhandled and take the extension host down.
      srv.off("error", reject);
      srv.on("error", (e) => log(`${target}: server error: ${e.message}`));
      resolve(srv);
    });
  });
}

async function activate(context) {
  const vscode = require("vscode");
  if (!vscode.env.remoteName) return; // local window: everything is on loopback already
  const out = vscode.window.createOutputChannel("aglink bridge");
  const log = (s) => out.appendLine(`[${new Date().toISOString()}] ${s}`);
  const env = context.environmentVariableCollection;
  env.persistent = false; // ports and token are per window and die with it
  // The token and ports are kept in this workspace's state, so a window reload
  // or reconnect comes back with the same ones: agents and terminals started
  // before it keep working instead of getting 401 or "nothing answers".
  const saved = context.workspaceState.get(STATE_KEY) || {};
  const token = typeof saved.token === "string" && /^[0-9a-f]{48}$/.test(saved.token) ? saved.token : crypto.randomBytes(24).toString("hex");
  const ports = {};
  const failed = [];
  // After a reload the old ports are put in place at once, so an agent or
  // terminal started while the listeners are still coming up (a busy port is
  // retried for up to 2s) is not left without one; a port that then had to
  // change is corrected below.
  if (saved.token === token) {
    process.env[TOKEN_ENV] = token;
    env.replace(TOKEN_ENV, token);
    for (const [target, name] of Object.entries(TARGETS)) {
      const old = Number((saved.ports || {})[target]) || 0;
      if (old) {
        process.env[name] = String(old);
        env.replace(name, String(old));
      }
    }
  }
  await Promise.all(
    Object.entries(TARGETS).map(async ([target]) => {
      try {
        const srv = await serve(vscode, target, token, log, Number((saved.ports || {})[target]) || 0);
        servers.push(srv);
        ports[target] = srv.address().port;
        log(`${target} -> 127.0.0.1:${ports[target]}`);
      } catch (e) {
        failed.push(`${target}: ${e.message}`);
        log(`${target}: listen failed: ${e.message}`);
      }
    })
  );
  // The token goes in with the ports, not before them: an agent started in
  // between would see a bridge session with no port and give up for good.
  process.env[TOKEN_ENV] = token;
  env.replace(TOKEN_ENV, token);
  for (const [target, name] of Object.entries(TARGETS)) {
    if (ports[target]) {
      process.env[name] = String(ports[target]);
      env.replace(name, String(ports[target]));
    } else {
      delete process.env[name];
      env.delete(name);
    }
  }
  // A port that had to change leaves agents and terminals started before the
  // reload pointing at nothing; say so instead of letting them time out.
  const moved = Object.keys(ports).filter((t) => (saved.ports || {})[t] && Number(saved.ports[t]) !== ports[t]);
  if (moved.length) {
    vscode.window.showWarningMessage(
      `aglink bridge: the ${moved.join(", ")} port changed after the reload. Restart Claude Code / Codex and open a new terminal in this window so they use the new port.`
    );
  }
  // The clients name this instead of a bare "port is not set".
  if (failed.length) {
    process.env[ERROR_ENV] = failed.join("; ");
    env.replace(ERROR_ENV, failed.join("; "));
  } else {
    delete process.env[ERROR_ENV];
  }
  context.workspaceState.update(STATE_KEY, { token, ports: { ...(saved.ports || {}), ...ports } }).then(undefined, (e) => log(`saving ports: ${e.message}`));
  try {
    ensureStopHook(log);
  } catch (e) {
    log(`voice Stop hook: ${e.message}`);
  }
  context.subscriptions.push(
    out,
    // relay.js reads `version` to decide whether this bridge needs replacing.
    vscode.commands.registerCommand("aglink.bridge.ports", () => ({
      ...Object.fromEntries(Object.entries(TARGETS).map(([t, n]) => [t, Number(process.env[n]) || 0])),
      version: context.extension && context.extension.packageJSON ? context.extension.packageJSON.version : "",
    })),
    // aglink-voice (on the PC) writes the Codex notify line through these.
    vscode.commands.registerCommand("aglink.bridge.codexNotifyPaths", () => {
      const p = codexNotifyPaths();
      return { config: vscode.Uri.file(p.config).path, script: vscode.Uri.file(p.script).path, commandPath: p.script, node: findNode() };
    }),
    { dispose: deactivate }
  );
  try {
    refreshCodexNotify(log);
  } catch (e) {
    log(`Codex notify: ${e.message}`);
  }
}

const CODEX_NOTIFY_MARK = "aglink-voice-codex-notify";

function codexNotifyPaths(env = process.env, home = require("os").homedir()) {
  const path = require("path");
  return {
    config: path.resolve(env.CODEX_HOME || path.join(home, ".codex"), "config.toml"),
    script: path.resolve(env.AGLINK_VOICE_HOME || path.join(home, ".aglink-voice"), `${CODEX_NOTIFY_MARK}.js`),
  };
}

// findNode is a node Codex can run from its notify line. Codex on the server
// inherits this extension host's PATH, which often has no node (nvm adds it in
// interactive shells only), so an absolute path is looked up: a login shell,
// then an interactive one (nvm), else VS Code's own bundled node — that last
// one moves when the VS Code server updates (refreshCodexNotify follows it).
let foundNode = "";
function findNode() {
  if (foundNode) return foundNode;
  const { spawnSync } = require("child_process");
  for (const args of [["sh", ["-lc", "command -v node"]], ["bash", ["-lic", "command -v node"]]]) {
    try {
      const r = spawnSync(args[0], args[1], { encoding: "utf8", timeout: 5000, stdio: ["ignore", "pipe", "ignore"] });
      const hit = String(r.stdout || "").split(/\r?\n/).map((l) => l.trim()).reverse().find((l) => l.startsWith("/"));
      if (hit) return (foundNode = hit);
    } catch (e) {}
  }
  return (foundNode = process.execPath);
}

// refreshCodexNotify fixes our notify line in the server's Codex config when
// the node it names cannot run: a bare "node" that is not on this host's PATH,
// or a VS Code server node removed by an update. Another program's notify,
// or ours on a node that exists, is left alone.
function refreshCodexNotify(log, { env = process.env, home, find = findNode } = {}) {
  const fs = require("fs");
  const { config } = codexNotifyPaths(env, home || require("os").homedir());
  let toml;
  try {
    toml = fs.readFileSync(config, "utf8");
  } catch (e) {
    return false; // no Codex config on this server
  }
  const re = new RegExp(String.raw`^([ \t]*notify[ \t]*=[ \t]*\[[ \t]*)("(?:[^"\\]|\\.)*")([ \t]*,[ \t]*"[^"\n]*` + CODEX_NOTIFY_MARK + String.raw`[^"\n]*".*)$`, "m");
  const m = re.exec(toml);
  if (!m) return false;
  let node;
  try {
    node = JSON.parse(m[2]);
  } catch (e) {
    return false;
  }
  const runnable = node.startsWith("/") ? fs.existsSync(node) : onPath(node, env);
  if (runnable) return false;
  const next = find();
  if (!next || next === node) return false;
  fs.writeFileSync(config, toml.replace(re, (_, a, _n, c) => a + JSON.stringify(next) + c));
  log(`Codex notify: ${node} cannot run here, now ${next} (${config})`);
  return true;
}

function onPath(name, env) {
  const fs = require("fs");
  const path = require("path");
  return String(env.PATH || "")
    .split(path.delimiter)
    .some((d) => d && fs.existsSync(path.join(d, name)));
}

function deactivate() {
  for (const s of servers) {
    s.close();
    if (s.closeAllConnections) s.closeAllConnections();
  }
  servers = [];
}

module.exports = { activate, deactivate, authorized, serve, refreshCodexNotify, codexNotifyPaths };
