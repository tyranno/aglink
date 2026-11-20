import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// Resolve relative to this file, not the cwd, so running from the repo root or
// chat/ checks the same web/app.js.
const app = readFileSync(fileURLToPath(new URL("./web/app.js", import.meta.url)), "utf8");

function assert(condition, message) {
  if (!condition) {
    console.error(message);
    process.exitCode = 1;
  }
}

assert(app.includes("sentHistory"), "composer must keep sent message history");
assert(app.includes('e.key === "ArrowUp"'), "composer must handle ArrowUp history recall");
assert(app.includes("/api/channel/backend"), "channel backend menu must call the backend API");
assert(app.includes("setChannelBackend"), "channel backend setter must be wired in app.js");
assert(app.includes('fd.append("target", JSON.stringify(currentTarget || { kind: "telegram" }))'), "attachment uploads must include the current conversation target");

if (process.exitCode) process.exit(process.exitCode);
