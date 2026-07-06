(function () {
  // Token: explicit ?token= wins, then the token the server injects into the page
  // (always the current valid one), then previously stored. The injected token
  // makes the page authenticate regardless of stale localStorage or which URL
  // (localhost / 127.0.0.1) was opened.
  const params = new URLSearchParams(location.search);
  let token = params.get("token") || window.__TC_TOKEN__ || localStorage.getItem("tc_token") || "";
  if (token) localStorage.setItem("tc_token", token);

  const log = document.getElementById("log");
  const statusEl = document.getElementById("status");
  const form = document.getElementById("composer");
  const input = document.getElementById("input");
  const fileEl = document.getElementById("file");
  const fileNameEl = document.getElementById("file-name");
  const attachBtn = document.getElementById("attach-btn");
  const topicList = document.getElementById("topic-list");
  const refreshTopics = document.getElementById("refresh-topics");
  const newChat = document.getElementById("new-chat");
  const shell = document.getElementById("shell");
  const toggleSidebar = document.getElementById("toggle-sidebar");
  const workingEl = document.getElementById("working");
  const workingLabel = document.getElementById("working-label");
  let ws, backoff = 500;
  // Which stream the composer currently targets: a specific web conversation,
  // or (only if explicitly picked) the single Telegram stream. Web starts with
  // no target until an active web conversation is found or the user picks one.
  let currentTarget = { kind: "telegram" }; // telegram is the always-present default channel

  if (attachBtn) attachBtn.addEventListener("click", () => fileEl.click());
  if (fileEl) fileEl.addEventListener("change", () => {
    if (!fileNameEl) return;
    if (fileEl.files.length > 0) { fileNameEl.textContent = fileEl.files[0].name; fileNameEl.hidden = false; }
    else { fileNameEl.textContent = ""; fileNameEl.hidden = true; }
  });

  let workingTimer = null, workingStart = 0;
  function workingElapsedText() {
    const secs = Math.max(0, Math.floor((Date.now() - workingStart) / 1000));
    if (secs < 60) return `작업 진행 중… (${secs}초)`;
    return `작업 진행 중… (${Math.floor(secs / 60)}분 ${secs % 60}초)`;
  }
  function showWorking() {
    if (!workingEl) return;
    if (workingTimer) return; // already showing
    workingStart = Date.now();
    workingEl.hidden = false;
    if (workingLabel) workingLabel.textContent = workingElapsedText();
    workingTimer = setInterval(() => {
      if (workingLabel) workingLabel.textContent = workingElapsedText();
    }, 1000);
  }
  function hideWorking() {
    if (!workingEl) return;
    if (workingTimer) { clearInterval(workingTimer); workingTimer = null; }
    workingEl.hidden = true;
  }

  function applySidebarHidden(hidden) {
    if (!shell) return;
    shell.classList.toggle("sidebar-hidden", hidden);
    if (toggleSidebar) toggleSidebar.setAttribute("aria-pressed", String(hidden));
  }
  if (toggleSidebar) {
    let sidebarHidden = localStorage.getItem("tc_sidebar_hidden") === "1";
    applySidebarHidden(sidebarHidden);
    toggleSidebar.addEventListener("click", () => {
      sidebarHidden = !sidebarHidden;
      localStorage.setItem("tc_sidebar_hidden", sidebarHidden ? "1" : "0");
      applySidebarHidden(sidebarHidden);
    });
  }

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

  function resizeInput() {
    input.style.height = "auto";
    input.style.height = input.scrollHeight + "px";
  }

  function sendText(text, echo) {
    if (!text) return false;
    if (!currentTarget) {
      add("system", "먼저 대화를 선택하거나 ＋로 새 대화를 만들어 주세요.");
      return false;
    }
    if (!ws || ws.readyState !== WebSocket.OPEN) return false;
    if (echo) add("user", text);
    ws.send(JSON.stringify({ type: "send", text, target: currentTarget }));
    return true;
  }

  // Load and render stored history for a target, then hand off to the live
  // stream (frames keep appending after this). Falls back silently on error
  // so whatever is already shown stays put.
  async function selectTarget(tgt) {
    currentTarget = tgt;
    try {
      const qs = tgt.kind === "telegram"
        ? "kind=telegram"
        : "kind=web&id=" + encodeURIComponent(tgt.id);
      const resp = await fetch("/api/history?" + qs, { headers: { Authorization: "Bearer " + token } });
      if (resp.ok) {
        const data = await resp.json();
        log.replaceChildren();
        for (const turn of (data.turns || [])) {
          add(turn.role === "user" ? "user" : "assistant", turn.text);
        }
      }
    } catch (e) { /* keep whatever is shown; live continues */ }
    loadConversations(); // refresh highlight
  }

  function makeTelegramButton(tg) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "topic telegram-topic";
    if (currentTarget && currentTarget.kind === "telegram") button.classList.add("active");
    const title = document.createElement("span");
    title.className = "topic-title";
    title.textContent = "📱 " + (tg.title || "텔레그램 대화");
    button.appendChild(title);
    if (tg.project) {
      const sub = document.createElement("span");
      sub.className = "topic-summary";
      sub.textContent = "작업: " + tg.project;
      button.appendChild(sub);
    }
    button.addEventListener("click", () => selectTarget({ kind: "telegram" }));
    return button;
  }

  // Top-level web conversations (per-conversation workDir, web-first flow).
  // Distinct from the legacy project-topic buttons below.
  function makeWebConvButton(wc) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "topic";
    if (currentTarget && currentTarget.kind === "web" && currentTarget.id === wc.id) button.classList.add("active");
    button.dataset.id = wc.id;
    const title = document.createElement("span");
    title.className = "topic-title";
    title.textContent = wc.title || wc.id;
    button.appendChild(title);
    if (wc.workDir) {
      const sub = document.createElement("span");
      sub.className = "topic-summary";
      sub.textContent = "📁 " + wc.workDir;
      button.appendChild(sub);
    }
    button.addEventListener("click", (e) => {
      if (e.target && e.target.dataset && e.target.dataset.gear) return;
      selectTarget({ kind: "web", id: wc.id });
    });
    const gear = document.createElement("span");
    gear.textContent = "⚙";
    gear.dataset.gear = "1";
    gear.style.cursor = "pointer";
    gear.style.marginLeft = "6px";
    gear.title = "작업 폴더 설정";
    gear.addEventListener("click", (ev) => {
      ev.stopPropagation();
      const path = prompt("이 대화의 작업 폴더 경로:", wc.workDir || "");
      if (path && ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "web_setdir", id: wc.id, path: path }));
        window.setTimeout(loadConversations, 400);
      }
    });
    button.appendChild(gear);
    return button;
  }

  function renderConversations(data) {
    if (!topicList) return;
    topicList.replaceChildren();

    if (data && data.telegram) {
      topicList.appendChild(makeTelegramButton(data.telegram));
    }

    const webConvs = Array.isArray(data && data.webConvs) ? data.webConvs : [];
    if (webConvs.length === 0) {
      const empty = document.createElement("div");
      empty.className = "topic-empty";
      empty.textContent = "＋로 새 대화를 만들어 보세요";
      topicList.appendChild(empty);
      return;
    }

    for (const wc of webConvs) topicList.appendChild(makeWebConvButton(wc));
  }

  async function loadConversations() {
    if (!topicList) return;
    try {
      const resp = await fetch("/api/conversations", {
        headers: { Authorization: "Bearer " + token },
      });
      if (!resp.ok) throw new Error("status " + resp.status);
      const data = await resp.json();
      renderConversations(data);
      if (!currentTarget) {
        const act = (data.webConvs || []).find((w) => w.active);
        if (act) selectTarget({ kind: "web", id: act.id });
      }
    } catch (err) {
      topicList.replaceChildren();
      const d = document.createElement("div");
      d.className = "topic-empty";
      d.textContent = "대화 목록을 불러오지 못했습니다";
      topicList.appendChild(d);
    }
  }

  function connect() {
    const scheme = location.protocol === "https:" ? "wss" : "ws";
    ws = new WebSocket(`${scheme}://${location.host}/ws?token=${encodeURIComponent(token)}`);
    ws.onopen = () => { statusEl.textContent = "연결됨"; statusEl.className = "on"; backoff = 500; selectTarget(currentTarget); };
    ws.onclose = () => {
      statusEl.textContent = "연결 끊김"; statusEl.className = "off";
      hideWorking();
      setTimeout(connect, backoff);
      backoff = Math.min(backoff * 2, 10000);
    };
    ws.onmessage = (ev) => {
      let f; try { f = JSON.parse(ev.data); } catch { return; }
      if (f.type === "text") add("assistant", f.text);
      else if (f.type === "image") addImage(f.caption || "", f.data);
      else if (f.type === "user") add("user", f.text); // input echoed from another channel (e.g. Telegram)
      else if (f.type === "typing") showWorking();
      else if (f.type === "done") hideWorking();
    };
  }

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (fileEl.files.length > 0) {
      const fd = new FormData();
      fd.append("file", fileEl.files[0]);
      fd.append("caption", input.value.trim());
      add("user", "📎 " + fileEl.files[0].name + (input.value.trim() ? " — " + input.value.trim() : ""));
      showWorking();
      const resp = await fetch("/api/upload", { method: "POST", headers: { Authorization: "Bearer " + token }, body: fd });
      if (!resp.ok) { add("system", "업로드 실패: " + resp.status); hideWorking(); }
      fileEl.value = ""; input.value = "";
      if (fileNameEl) { fileNameEl.textContent = ""; fileNameEl.hidden = true; }
      resizeInput();
      return;
    }
    const text = input.value.trim();
    if (sendText(text, true)) {
      showWorking();
      input.value = "";
      resizeInput();
      window.setTimeout(loadConversations, 500);
    }
  });

  input.addEventListener("input", resizeInput);
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      form.requestSubmit();
    }
  });
  if (refreshTopics) refreshTopics.addEventListener("click", loadConversations);
  if (newChat) newChat.addEventListener("click", () => {
    const title = prompt("새 대화 제목 (선택):", "") || "";
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify({ type: "web_new", title: title }));
    // After creation, refresh and select the newest web conv.
    window.setTimeout(async () => {
      await loadConversations();
      // pick the most-recent web conv as the new target
      try {
        const resp = await fetch("/api/conversations", { headers: { Authorization: "Bearer " + token } });
        const data = await resp.json();
        if (data.webConvs && data.webConvs.length) selectTarget({ kind: "web", id: data.webConvs[0].id });
      } catch (e) { /* ignore; sidebar will still refresh on next loadConversations */ }
    }, 400);
  });
  resizeInput();
  loadConversations();
  connect();
})();
