// Options page: set the daemon port the extension dials. Stored in
// chrome.storage.local under "port"; background.js reads it on (re)connect and
// reconnects automatically when it changes. Blank = clear the override so the
// default (DEFAULT_PORT in background.js) is used.

const DEFAULT_PORT = 48219;

const portInput = document.getElementById("port");
const statusEl = document.getElementById("status");

function setStatus(msg, ok = true) {
  statusEl.textContent = msg;
  statusEl.style.color = ok ? "#2a7" : "#c33";
}

// Prefill with the currently stored port (if any).
chrome.storage.local.get("port").then(({ port }) => {
  if (port) portInput.value = port;
});

document.getElementById("save").addEventListener("click", async () => {
  const raw = portInput.value.trim();
  if (raw === "") {
    await chrome.storage.local.remove("port");
    setStatus(`Saved — using default ${DEFAULT_PORT}.`);
    return;
  }
  const n = Number(raw);
  if (!Number.isInteger(n) || n <= 0 || n >= 65536) {
    setStatus("Invalid port (1–65535).", false);
    return;
  }
  await chrome.storage.local.set({ port: n });
  setStatus(`Saved port ${n}.`);
});
