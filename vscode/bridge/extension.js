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
// terminal environment collection for `claude` run in a terminal. On the server
// ~/.claude.json reads them as ${AGLINK_WEB_PORT:-48219} and so on, and the
// Stop hook reads AGLINK_VOICE_PORT.
"use strict";

const http = require("http");

const TARGETS = { web: "AGLINK_WEB_PORT", screen: "AGLINK_SCREEN_PORT", voice: "AGLINK_VOICE_PORT" };
const MAX_BODY = 64 << 20;

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
const targets = bridge ? [[bridge, "/stop"], [bridge, "/vscode/stop"]] : [[48231, "/stop"], [48230, "/vscode/stop"]];
let d = "";
let left = targets.length;
const done = () => process.exit(0);
setTimeout(done, 3000);
process.stdin.on("data", (c) => (d += c)).on("end", () => {
  try { const h = JSON.parse(d); h.entrypoint = process.env.CLAUDE_CODE_ENTRYPOINT || ""; d = JSON.stringify(h); } catch {}
  for (const [port, path] of targets) {
    const r = require("http").request(
      { host: "127.0.0.1", port, path, method: "POST", headers: { "Content-Type": "application/json" }, timeout: 1500 },
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

function serve(vscode, target, log) {
  return new Promise((resolve, reject) => {
    const srv = http.createServer((req, res) => {
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
  env.persistent = false; // ports are per window and die with it
  for (const [target, name] of Object.entries(TARGETS)) {
    try {
      const srv = await serve(vscode, target, log);
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
    vscode.commands.registerCommand("aglink.bridge.ports", () =>
      Object.fromEntries(Object.entries(TARGETS).map(([t, n]) => [t, Number(process.env[n]) || 0]))
    ),
    { dispose: deactivate }
  );
}

function deactivate() {
  for (const s of servers) s.close();
  servers = [];
}

module.exports = { activate, deactivate };
