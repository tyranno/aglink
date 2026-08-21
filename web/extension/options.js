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

// Show which account this profile reports to the daemon. When a profile cannot
// connect the reason is almost always "not signed in to Chrome", and that is
// invisible from the daemon side — it never sees the connection at all, so
// there is nothing in the log to look at either.
(async () => {
  const el = document.getElementById("account");
  try {
    const info = await chrome.identity.getProfileUserInfo({ accountStatus: "ANY" });
    const { lastError, connectedAs } = await chrome.storage.local.get(["lastError", "connectedAs"]);
    if (info && info.email) {
      el.textContent = `이 프로필의 계정: ${info.email}` + (connectedAs ? " (연결됨)" : "");
      el.style.color = "#2a7";
    } else {
      el.textContent =
        lastError || "Chrome에 로그인되어 있지 않습니다. 로그인 후 이 확장을 새로고침하세요.";
      el.style.color = "#c33";
    }
  } catch (e) {
    el.textContent = `계정을 읽지 못했습니다: ${e}`;
    el.style.color = "#c33";
  }
})();
