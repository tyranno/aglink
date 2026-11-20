// Static regression checks (same approach as web-ui-regression.test.mjs) for
// the acknowledged web-conversation management actions in web/app.js.
import { readFileSync } from "node:fs";

const app = readFileSync("web/app.js", "utf8");

function assert(condition, message) {
  if (!condition) {
    console.error("FAIL  " + message);
    process.exitCode = 1;
  }
}

function between(src, start, end) {
  const i = src.indexOf(start);
  if (i < 0) return "";
  const j = src.indexOf(end, i + start.length);
  return src.slice(i, j < 0 ? undefined : j);
}

const workdir = between(app, 'action === "workdir"', 'action === "delete"');
const del = between(app, 'action === "delete"', "button.appendChild(menu)");

assert(!app.includes('type: "web_setdir"'), "workdir change must not fire-and-forget web_setdir over the WebSocket");
assert(!app.includes('type: "web_delete"'), "delete must not fire-and-forget web_delete over the WebSocket");
assert(!/setTimeout\(loadConversations,\s*400\)/.test(app), "no blind 400 ms list refresh after a management action");
assert(workdir.includes('webconvRequest("setdir"'), "workdir change must await the acknowledged /api/webconv/setdir request");
assert(del.includes('webconvRequest("delete"'), "delete must await the acknowledged /api/webconv/delete request");

// Delete: nothing on screen changes before success is confirmed.
const ack = del.indexOf('webconvRequest("delete"');
const failReturn = del.indexOf("if (!r.ok)");
assert(ack >= 0 && failReturn > ack, "delete must check the reply before touching the screen");
assert(del.indexOf('unread.delete("web:"') > failReturn, "unread badge must be cleared only after a successful delete");
assert(del.indexOf("log.replaceChildren()") > failReturn, "open conversation log must be cleared only after a successful delete");

// Errors use the app's own dialog, never browser popups; replies time out.
assert(/function showNotice\(/.test(app), "app must have its own notice dialog for errors");
assert(workdir.includes("showNotice(") && del.includes("showNotice("), "failures must be shown with the app's notice dialog");
assert(!/\b(window\.)?(alert|confirm|prompt)\(/.test(app.replace(/\/\/.*$/gm, "")), "no browser alert/confirm/prompt");
assert(/WEBCONV_TIMEOUT_MS/.test(app) && app.includes("AbortError"), "management requests must time out and report it");

if (process.exitCode) process.exit(process.exitCode);
console.log("ALL PASS");
