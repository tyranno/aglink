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
      return { port: String(req.path).startsWith("/vscode/") ? 48230 : 48231, auth: "" };
    default:
      throw new Error(`unknown relay target: ${req.target}`);
  }
}

function relay(req, readPort) {
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
    const fail = (msg) =>
      resolve({ status: 502, contentType: "application/json", body: Buffer.from(JSON.stringify({ error: `aglink relay: ${msg}` })).toString("base64") });
    r.on("error", (e) => fail(`${req.target} (127.0.0.1:${dst.port}) is not reachable on this PC: ${e.message}`));
    r.on("timeout", () => r.destroy(new Error("timed out")));
    r.end(body);
  });
}

// ensureBridge installs the bundled aglink-bridge on the remote server the
// first time this PC opens a window there (or when the bundled one is newer),
// so connecting a new server needs nothing done by hand.
async function ensureBridge(vscode, extensionPath, log) {
  if (!vscode.env.remoteName) return;
  const bundled = path.join(extensionPath, "aglink-bridge.vsix");
  if (!fs.existsSync(bundled)) return;
  let want = "";
  try {
    want = JSON.parse(fs.readFileSync(path.join(extensionPath, "bridge-version.json"), "utf8")).version;
  } catch (e) {}
  const have = vscode.extensions.getExtension(BRIDGE_ID);
  if (have && (!want || have.packageJSON.version === want)) return;
  log(`installing aglink-bridge ${want} on ${vscode.env.remoteName} (had ${have ? have.packageJSON.version : "none"})`);
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

module.exports = { relay, destination, ensureBridge };
