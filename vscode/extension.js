// aglink-vscode: connects this VS Code window to the aglink-web daemon so an
// agent in ANOTHER window (or anywhere that can reach the daemon) can read this
// window's files, problems and terminal output, and act in it.
//
// It is a UI extension ("extensionKind": ["ui"]): in a Remote-SSH window it
// still runs on this machine, so it reaches the daemon on 127.0.0.1 without a
// tunnel, while file and terminal operations are carried to the remote by
// VS Code itself. Every window runs its own copy and holds its own connection.
//
// Wire protocol is the Chrome extension's: the daemon sends
// {id, method, params}; we answer {id, ok, text} or {id, ok:false, error}.
// id 0 with method "ping" is the daemon's keepalive and is answered {id:0, ok:true}.
"use strict";

const fs = require("fs");
const os = require("os");
const path = require("path");
const { createMethods, remoteHost } = require("./methods");

const DEFAULT_PORT = 48219;
const MIN_BACKOFF_MS = 1000;
const MAX_BACKOFF_MS = 30000;

// windowIdentity is what this window tells the daemon about itself.
function windowIdentity(vscode) {
  const f = vscode.workspace.workspaceFolders;
  const folder = f && f.length ? (f[0].uri.scheme === "file" ? f[0].uri.fsPath : f[0].uri.path) : "";
  return {
    name: vscode.workspace.name || "",
    remote: remoteHost(vscode),
    folder,
    session: vscode.env.sessionId || "",
  };
}

// readPortFile finds the running daemon's port the way the aglink-web bridge
// does: ~/.aglink/aglink-web.port (or $AGLINK_HOME/aglink-web.port).
function readPortFile() {
  const dir = process.env.AGLINK_HOME || path.join(os.homedir(), ".aglink");
  try {
    const n = parseInt(fs.readFileSync(path.join(dir, "aglink-web.port"), "utf8").trim(), 10);
    if (n > 0 && n < 65536) return n;
  } catch (e) {
    // no daemon has run yet — fall back to the default
  }
  return DEFAULT_PORT;
}

function createConnector({ vscode, WebSocketImpl, readPort, methods, timers, log }) {
  const t = timers || { setTimeout, clearTimeout };
  let socket = null;
  let stopped = true;
  let backoff = MIN_BACKOFF_MS;
  let retry = null;

  function url() {
    const cfg = vscode.workspace.getConfiguration("aglink");
    const port = Number(cfg.get("port", 0)) || readPort();
    const id = windowIdentity(vscode);
    const q = new URLSearchParams(id).toString();
    return `ws://127.0.0.1:${port}/vscode?${q}`;
  }

  function send(obj) {
    if (socket && socket.readyState === WebSocketImpl.OPEN) socket.send(JSON.stringify(obj));
  }

  async function handle(raw) {
    let msg;
    try {
      msg = JSON.parse(typeof raw === "string" ? raw : raw.toString());
    } catch (e) {
      return;
    }
    if (msg.method === "ping") {
      send({ id: 0, ok: true });
      return;
    }
    const fn = methods[msg.method];
    if (!fn) {
      send({ id: msg.id, ok: false, error: `unknown method: ${msg.method}` });
      return;
    }
    try {
      const text = await fn(msg.params || {});
      send({ id: msg.id, ok: true, text: String(text) });
    } catch (e) {
      send({ id: msg.id, ok: false, error: e && e.message ? e.message : String(e) });
    }
  }

  function connect() {
    if (stopped) return;
    let opened = false;
    const target = url();
    socket = new WebSocketImpl(target);
    socket.onopen = () => {
      opened = true;
      backoff = MIN_BACKOFF_MS;
      log(`connected to aglink-web (${target.split("?")[0]})`);
    };
    socket.onmessage = (ev) => {
      handle(ev.data);
    };
    socket.onerror = () => {
      // onclose follows and schedules the retry
    };
    socket.onclose = () => {
      socket = null;
      if (stopped) return;
      if (opened) log("disconnected from aglink-web — retrying");
      const wait = opened ? MIN_BACKOFF_MS : backoff;
      backoff = opened ? MIN_BACKOFF_MS * 2 : Math.min(backoff * 2, MAX_BACKOFF_MS);
      retry = t.setTimeout(connect, wait);
    };
  }

  return {
    start() {
      if (!stopped) return;
      stopped = false;
      backoff = MIN_BACKOFF_MS;
      connect();
    },
    stop() {
      stopped = true;
      if (retry) t.clearTimeout(retry);
      retry = null;
      if (socket) socket.close();
      socket = null;
    },
  };
}

// ---- VS Code entry points --------------------------------------------------

let connector = null;
let terminals = null;

function activate(context) {
  const vscode = require("vscode");
  const out = vscode.window.createOutputChannel("aglink");
  const log = (s) => out.appendLine(`[${new Date().toISOString()}] ${s}`);
  const { createTerminals } = require("./terminals");
  terminals = createTerminals(vscode);
  const methods = createMethods(vscode, terminals);

  const start = () => {
    if (connector) connector.stop();
    connector = null;
    if (vscode.workspace.getConfiguration("aglink").get("enabled", true) === false) {
      log("disabled by setting aglink.enabled");
      return;
    }
    connector = createConnector({ vscode, WebSocketImpl: WebSocket, readPort: readPortFile, methods, log });
    connector.start();
  };
  start();

  context.subscriptions.push(
    out,
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("aglink")) start();
    }),
    { dispose: () => { if (connector) connector.stop(); if (terminals) terminals.dispose(); } }
  );
}

function deactivate() {
  if (connector) connector.stop();
  connector = null;
  if (terminals) terminals.dispose();
  terminals = null;
}

module.exports = { activate, deactivate, createConnector, windowIdentity, readPortFile };
