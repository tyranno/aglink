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
// proof of who is calling: each window also makes a random AGLINK_BRIDGE_TOKEN,
// exported next to the ports, and a request without it is refused before it
// reaches the PC. The clients send it as "Authorization: Bearer <token>".
"use strict";

const crypto = require("crypto");
const http = require("http");

const TARGETS = { web: "AGLINK_WEB_PORT", screen: "AGLINK_SCREEN_PORT", voice: "AGLINK_VOICE_PORT" };
const TOKEN_ENV = "AGLINK_BRIDGE_TOKEN";
const MAX_BODY = 64 << 20;

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
  const found = (spawnSync("sh", ["-lc", "command -v node"], { encoding: "utf8" }).stdout || "").trim();
  const node = found.startsWith("/") ? found : process.execPath;
  s.hooks = s.hooks || {};
  s.hooks.Stop = s.hooks.Stop || [];
  s.hooks.Stop.push({ hooks: [{ type: "command", command: `"${node}" "${script}"`, timeout: 5 }] });
  fs.mkdirSync(path.dirname(sp), { recursive: true });
  fs.writeFileSync(sp, JSON.stringify(s, null, 2) + "\n");
  log(`voice Stop hook added to ${sp}`);
}

function serve(vscode, target, token, log) {
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
        try {
          const r = await vscode.commands.executeCommand("aglink.relay", {
            target,
            method: req.method,
            path: req.url,
            headers: { "content-type": req.headers["content-type"] || "", accept: req.headers["accept"] || "" },
            body: Buffer.concat(chunks).toString("base64"),
          });
          res.writeHead(r.status, { "Content-Type": r.contentType || "application/json" });
          res.end(Buffer.from(r.body || "", "base64"));
        } catch (e) {
          log(`${target} ${req.method} ${req.url}: ${e && e.message ? e.message : e}`);
          res.writeHead(502, { "Content-Type": "application/json" });
          res.end(JSON.stringify({ error: `aglink-bridge: ${e && e.message ? e.message : e}` }));
        }
      });
    });
    srv.once("error", reject);
    srv.listen(0, "127.0.0.1", () => resolve(srv));
  });
}

async function activate(context) {
  const vscode = require("vscode");
  if (!vscode.env.remoteName) return; // local window: everything is on loopback already
  const out = vscode.window.createOutputChannel("aglink bridge");
  const log = (s) => out.appendLine(`[${new Date().toISOString()}] ${s}`);
  const env = context.environmentVariableCollection;
  env.persistent = false; // ports and token are per window and die with it
  const token = crypto.randomBytes(24).toString("hex");
  process.env[TOKEN_ENV] = token;
  env.replace(TOKEN_ENV, token);
  for (const [target, name] of Object.entries(TARGETS)) {
    try {
      const srv = await serve(vscode, target, token, log);
      servers.push(srv);
      const port = String(srv.address().port);
      process.env[name] = port;
      env.replace(name, port);
      log(`${target} -> 127.0.0.1:${port}`);
    } catch (e) {
      log(`${target}: listen failed: ${e.message}`);
    }
  }
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
  for (const s of servers) s.close();
  servers = [];
}

module.exports = { activate, deactivate, authorized, serve, refreshCodexNotify, codexNotifyPaths };
