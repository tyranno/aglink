// Voice conversation with Claude Code in this window, using the aglink desktop
// app's voice: its wake word, Whisper recognition, enrolled-voice check and
// Supertonic reading (desktop/vscodevoice.go has the other side).
//
// - This window keeps GET /vscode/events open on the desktop app and turns the
//   voice on or off for itself with POST /vscode/attach (status bar button).
// - What the user says arrives as {type:"prompt", text}: it goes on the
//   clipboard, Claude Code's input box takes the focus, and POST /vscode/paste
//   has the desktop press Ctrl+V, Enter in this window. Claude Code has no way
//   to submit a prompt from outside, so keys are the only way in.
// - Claude Code's Stop hook (installed by ensureStopHook) posts each finished
//   turn back to the desktop, which reads the answer out.
"use strict";

const fs = require("fs");
const http = require("http");
const os = require("os");
const path = require("path");

const PORT = 48230;
const HOOK_MARK = "vscode-voice-hook";
const MIN_BACKOFF_MS = 2000;
const MAX_BACKOFF_MS = 30000;

const HOOK_SCRIPT = `// Claude Code Stop hook: hands the finished turn to the aglink desktop app,
// which reads it out when voice is on for this window. Never fails the turn.
let d = "";
const done = () => process.exit(0);
setTimeout(done, 3000);
process.stdin.on("data", (c) => (d += c)).on("end", () => {
  const r = require("http").request(
    { host: "127.0.0.1", port: ${PORT}, path: "/vscode/stop", method: "POST", headers: { "Content-Type": "application/json" }, timeout: 1500 },
    (res) => { res.resume(); res.on("end", done); }
  );
  r.on("error", done);
  r.on("timeout", () => { r.destroy(); done(); });
  r.end(d);
});
`;

// parseEvents pulls complete "data:" events out of a server-sent event stream
// buffer; it returns them and what is left over for the next chunk.
function parseEvents(buf) {
  const events = [];
  let i;
  while ((i = buf.indexOf("\n\n")) >= 0) {
    const block = buf.slice(0, i);
    buf = buf.slice(i + 2);
    const data = block
      .split("\n")
      .filter((l) => l.startsWith("data:"))
      .map((l) => l.slice(5).trim())
      .join("\n");
    if (!data) continue;
    try {
      events.push(JSON.parse(data));
    } catch (e) {
      // not ours
    }
  }
  return { events, rest: buf };
}

// ensureStopHook installs the hook script and adds it to Claude Code's user
// settings once; other settings and hooks are left as they are.
function ensureStopHook(home = os.homedir()) {
  const dir = process.env.AGLINK_HOME || path.join(home, ".aglink");
  const script = path.join(dir, `${HOOK_MARK}.js`);
  fs.mkdirSync(dir, { recursive: true });
  if (!fs.existsSync(script) || fs.readFileSync(script, "utf8") !== HOOK_SCRIPT) fs.writeFileSync(script, HOOK_SCRIPT);
  const file = path.join(home, ".claude", "settings.json");
  let settings = {};
  if (fs.existsSync(file)) settings = JSON.parse(fs.readFileSync(file, "utf8"));
  const stop = (((settings.hooks = settings.hooks || {}).Stop = settings.hooks.Stop || []));
  if (JSON.stringify(stop).includes(HOOK_MARK)) return false;
  stop.push({ hooks: [{ type: "command", command: `node "${script.replace(/\\/g, "/")}"`, timeout: 5 }] });
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, JSON.stringify(settings, null, 2) + "\n");
  return true;
}

function post(p, body) {
  return new Promise((resolve, reject) => {
    const data = JSON.stringify(body);
    const r = http.request(
      { host: "127.0.0.1", port: PORT, path: p, method: "POST", headers: { "Content-Type": "application/json" }, timeout: 5000 },
      (res) => {
        let s = "";
        res.on("data", (c) => (s += c));
        res.on("end", () => {
          if (res.statusCode >= 300) reject(new Error(s.trim() || `HTTP ${res.statusCode}`));
          else resolve(s ? JSON.parse(s) : {});
        });
      }
    );
    r.on("error", reject);
    r.on("timeout", () => r.destroy(new Error("timeout")));
    r.end(data);
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function createVoice(vscode, log) {
  const item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 1000);
  item.command = "aglink.voice.toggle";
  let req = null;
  let linked = false; // the event stream to the desktop app is open
  let on = false;
  let stopped = false;
  let retry = null;
  let backoff = MIN_BACKOFF_MS;

  function identity() {
    const f = vscode.workspace.workspaceFolders;
    return {
      folder: f && f.length && f[0].uri.scheme === "file" ? f[0].uri.fsPath : "",
      title: vscode.workspace.name || "",
      pid: process.ppid, // the VS Code main process, which owns the window
    };
  }

  function render() {
    if (!linked) {
      item.text = "$(mic) 음성";
      item.tooltip = "aglink 데스크톱이 꺼져 있어요";
      item.backgroundColor = undefined;
    } else if (on) {
      item.text = "$(mic-filled) 음성 켜짐";
      item.tooltip = "말하면 Claude Code 로 보내고 답을 읽어 줘요. 누르면 꺼요.";
      item.backgroundColor = new vscode.ThemeColor("statusBarItem.warningBackground");
    } else {
      item.text = "$(mic) 음성";
      item.tooltip = "aglink 음성 대화 켜기";
      item.backgroundColor = undefined;
    }
    item.show();
  }

  async function onPrompt(text) {
    const old = await vscode.env.clipboard.readText();
    await vscode.env.clipboard.writeText(text);
    await vscode.commands.executeCommand("claude-vscode.focus");
    await sleep(250);
    let res;
    try {
      res = await post("/vscode/paste", identity());
    } catch (e) {
      res = { ok: false, error: e.message };
    }
    log(`voice prompt ${res.ok ? "sent" : `not sent: ${res.error}`}`);
    if (!res.ok) {
      vscode.window.showWarningMessage(`aglink 음성: ${res.error}`);
      return; // the words stay on the clipboard
    }
    await sleep(800);
    await vscode.env.clipboard.writeText(old);
  }

  function connect() {
    if (stopped) return;
    const q = new URLSearchParams(Object.entries(identity()).map(([k, v]) => [k, String(v)])).toString();
    let buf = "";
    req = http.get({ host: "127.0.0.1", port: PORT, path: `/vscode/events?${q}` }, (res) => {
      if (res.statusCode !== 200) {
        res.resume();
        return;
      }
      linked = true;
      backoff = MIN_BACKOFF_MS;
      render();
      log("voice: linked to aglink desktop");
      // The desktop app restarted while voice was on: take it again.
      if (on) post("/vscode/attach", { on: true, ...identity() }).catch(() => {});
      res.setEncoding("utf8");
      res.on("data", (chunk) => {
        const r = parseEvents(buf + chunk.replace(/\r/g, ""));
        buf = r.rest;
        for (const ev of r.events) {
          if (ev.type === "prompt" && ev.text) onPrompt(ev.text).catch((e) => log(`voice prompt failed: ${e.message}`));
          else if (ev.type === "attached" && !ev.on) {
            on = false;
            render();
          }
        }
      });
    });
    req.on("close", () => {
      req = null;
      if (linked) log("voice: aglink desktop gone");
      linked = false;
      render();
      if (stopped) return;
      retry = setTimeout(connect, backoff);
      backoff = Math.min(backoff * 2, MAX_BACKOFF_MS);
    });
    req.on("error", () => {}); // close follows
  }

  async function toggle() {
    if (!linked) {
      vscode.window.showWarningMessage("aglink 데스크톱 앱이 켜져 있지 않아요. 앱을 켠 뒤 다시 눌러 주세요.");
      return;
    }
    const want = !on;
    if (want) {
      try {
        if (ensureStopHook()) log("voice: Claude Code Stop hook added to ~/.claude/settings.json");
      } catch (e) {
        vscode.window.showErrorMessage(`aglink 음성: Claude Code 설정에 답 읽기를 넣지 못했어요 — ${e.message}`);
        return;
      }
    }
    try {
      await post("/vscode/attach", { on: want, ...identity() });
      on = want;
    } catch (e) {
      vscode.window.showWarningMessage(`aglink 음성: ${e.message}`);
    }
    render();
  }

  render();
  connect();
  return {
    toggle,
    dispose() {
      if (stopped) return;
      stopped = true;
      clearTimeout(retry);
      if (on && linked) post("/vscode/attach", { on: false, ...identity() }).catch(() => {});
      if (req) req.destroy();
      item.dispose();
    },
  };
}

module.exports = { createVoice, parseEvents, ensureStopHook, HOOK_SCRIPT };
