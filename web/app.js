(function () {
  // Token: from ?token= (persist to localStorage) or previously stored.
  const params = new URLSearchParams(location.search);
  let token = params.get("token");
  if (token) localStorage.setItem("tc_token", token);
  else token = localStorage.getItem("tc_token") || "";

  const log = document.getElementById("log");
  const statusEl = document.getElementById("status");
  const form = document.getElementById("composer");
  const input = document.getElementById("input");
  const fileEl = document.getElementById("file");
  let ws, backoff = 500;

  function add(role, text) {
    const d = document.createElement("div");
    d.className = "msg " + role;
    d.textContent = text;
    log.appendChild(d);
    log.scrollTop = log.scrollHeight;
    return d;
  }
  function addImage(caption, b64) {
    const d = document.createElement("div");
    d.className = "msg assistant";
    if (caption) { const c = document.createElement("div"); c.textContent = caption; d.appendChild(c); }
    const img = document.createElement("img");
    img.src = "data:image/png;base64," + b64;
    d.appendChild(img);
    log.appendChild(d);
    log.scrollTop = log.scrollHeight;
  }

  function connect() {
    const scheme = location.protocol === "https:" ? "wss" : "ws";
    ws = new WebSocket(`${scheme}://${location.host}/ws?token=${encodeURIComponent(token)}`);
    ws.onopen = () => { statusEl.textContent = "연결됨"; statusEl.className = "on"; backoff = 500; };
    ws.onclose = () => {
      statusEl.textContent = "연결 끊김"; statusEl.className = "off";
      setTimeout(connect, backoff);
      backoff = Math.min(backoff * 2, 10000);
    };
    ws.onmessage = (ev) => {
      let f; try { f = JSON.parse(ev.data); } catch { return; }
      if (f.type === "text") add("assistant", f.text);
      else if (f.type === "image") addImage(f.caption || "", f.data);
      // "typing" frames are ignored for v1 (no indicator element).
    };
  }

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (fileEl.files.length > 0) {
      const fd = new FormData();
      fd.append("file", fileEl.files[0]);
      fd.append("caption", input.value.trim());
      add("user", "📎 " + fileEl.files[0].name + (input.value.trim() ? " — " + input.value.trim() : ""));
      await fetch("/api/upload", { method: "POST", headers: { Authorization: "Bearer " + token }, body: fd });
      fileEl.value = ""; input.value = "";
      return;
    }
    const text = input.value.trim();
    if (!text || !ws || ws.readyState !== WebSocket.OPEN) return;
    add("user", text);
    ws.send(JSON.stringify({ type: "send", text }));
    input.value = "";
  });

  connect();
})();
