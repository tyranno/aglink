// aglink-web shared page-side functions.
//
// Every browser tool is two halves: a function that runs INSIDE the page (find
// the element, synthesize the events, read the value) and the formatting of its
// result. The first half lives here so the Chrome extension and the daemon's
// CDP path to Electron/Wails apps run the very same code — selector resolution,
// event synthesis and framework-aware value setting exist exactly once.
//
//   extension: injected with aglink-inject.js, called via executeScript
//              func: (name, args) => globalThis.__aglinkPage[name](...args)
//   daemon:    embedded (go:embed) and evaluated over CDP, then called the same way
//
// The bodies were lifted verbatim from background.js. Some take constants as
// trailing args (keyCombo, listElements); the copies below are what the daemon
// passes, and the extension still passes background.js's own.
//
// evalExpression is the one exception to "exists once": the extension runs it
// in the page's MAIN world, where this file is not injected, so background.js
// keeps its own inline copy. KEEP THE TWO IN SYNC.
//
// Idempotent: re-injecting just re-defines the global.
(function () {
  const AGLINK_ID_ATTR = "data-aglink-id";
  
  const INTERACTIVE_SELECTOR = [
    "a[href]",
    "button",
    "input:not([type=\"hidden\"])",
    "textarea",
    "select",
    "[contenteditable=\"true\"]",
    "[role=\"button\"]",
    "[role=\"link\"]",
    "[role=\"checkbox\"]",
    "[role=\"radio\"]",
    "[role=\"menuitem\"]",
    "[role=\"tab\"]",
    "[role=\"option\"]",
    "[role=\"combobox\"]",
    "[role=\"switch\"]",
    "[onclick]",
  ].join(",");
  
  const KEY_SPECS = {
    enter: { key: "Enter", code: "Enter", keyCode: 13 },
    return: { key: "Enter", code: "Enter", keyCode: 13 },
    tab: { key: "Tab", code: "Tab", keyCode: 9 },
    esc: { key: "Escape", code: "Escape", keyCode: 27 },
    escape: { key: "Escape", code: "Escape", keyCode: 27 },
    space: { key: " ", code: "Space", keyCode: 32 },
    backspace: { key: "Backspace", code: "Backspace", keyCode: 8 },
    delete: { key: "Delete", code: "Delete", keyCode: 46 },
    del: { key: "Delete", code: "Delete", keyCode: 46 },
    up: { key: "ArrowUp", code: "ArrowUp", keyCode: 38 },
    down: { key: "ArrowDown", code: "ArrowDown", keyCode: 40 },
    left: { key: "ArrowLeft", code: "ArrowLeft", keyCode: 37 },
    right: { key: "ArrowRight", code: "ArrowRight", keyCode: 39 },
    home: { key: "Home", code: "Home", keyCode: 36 },
    end: { key: "End", code: "End", keyCode: 35 },
    pageup: { key: "PageUp", code: "PageUp", keyCode: 33 },
    pagedown: { key: "PageDown", code: "PageDown", keyCode: 34 },
  };
  for (let i = 1; i <= 12; i++) {
    KEY_SPECS["f" + i] = { key: "F" + i, code: "F" + i, keyCode: 111 + i };
  }

  const MOD_PROPS = {
    ctrl: "ctrlKey",
    control: "ctrlKey",
    alt: "altKey",
    shift: "shiftKey",
    meta: "metaKey",
    cmd: "metaKey",
    win: "metaKey",
    super: "metaKey",
  };

  const P = {};

  // find resolves a selector to one element plus a note when several matched
  // (aglink-inject.js resolveInfo); miss is the {found:false} result with a
  // hint at what nearly matched. Both fall back to document.querySelector if
  // the resolver is somehow absent.
  function find(sel) {
    const H = globalThis.__aglink;
    if (H && H.resolveInfo) return H.resolveInfo(sel);
    let el = null;
    try {
      el = document.querySelector(sel);
    } catch (e) {
      el = null;
    }
    return { el, count: el ? 1 : 0, note: "" };
  }

  function miss(sel) {
    const H = globalThis.__aglink;
    let hint = "";
    try {
      hint = H && H.nearMiss ? H.nearMiss(sel) : "";
    } catch (e) {
      hint = "";
    }
    return hint ? { found: false, hint } : { found: false };
  }

  // noted adds resolveInfo's ambiguity note to a result, when there is one.
  function noted(result, info) {
    if (info && info.note) result.note = info.note;
    return result;
  }

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const DEFAULT_ACTION_TIMEOUT_MS = 3000;
  const MAX_ACTION_TIMEOUT_MS = 20000;

  const desc = (el) => (globalThis.__aglink && globalThis.__aglink.describe ? globalThis.__aglink.describe(el) : el.tagName.toLowerCase());
  const within = (outer, inner) => (globalThis.__aglink && globalThis.__aglink.contains
    ? globalThis.__aglink.contains(outer, inner)
    : outer === inner || (outer.contains && outer.contains(inner)));

  // blocker says why a real user could not click el right now — "disabled",
  // "not visible", "covered by <what is on top>" — or "" when nothing stops
  // it. The hit test asks what is at the element's center; that being the
  // element, something inside it, an ancestor (el has pointer-events:none)
  // or its <label> all count as clickable.
  function blocker(el) {
    const H = globalThis.__aglink;
    const r = el.getBoundingClientRect();
    if (H ? !H.isVisible(el) : r.width <= 0 || r.height <= 0) return "not visible";
    let disabled = el.disabled === true || (el.getAttribute && el.getAttribute("aria-disabled") === "true");
    try {
      if (!disabled && el.matches && el.matches(":disabled")) disabled = true; // inside a disabled <fieldset>
    } catch (e) {
      // engines without :disabled on this element
    }
    if (disabled) return "disabled";
    const doc = el.ownerDocument || document;
    const view = doc.defaultView || window;
    const x = r.left + r.width / 2;
    const y = r.top + r.height / 2;
    // A center outside the viewport cannot be hit-tested; let the click go.
    if (x < 0 || y < 0 || x >= view.innerWidth || y >= view.innerHeight) return "";
    const root = el.getRootNode ? el.getRootNode() : doc;
    const probe = root && root.elementFromPoint ? root : doc;
    if (!probe.elementFromPoint) return "";
    const hit = probe.elementFromPoint(x, y);
    if (!hit || within(el, hit) || within(hit, el)) return "";
    if (el.labels && Array.from(el.labels).some((l) => within(l, hit))) return "";
    return "covered by " + desc(hit);
  }

  // actionable waits (polling every 100ms, up to opts.timeout ms, default 3s)
  // until sel matches an element a user could click: scrolled into view,
  // visible, enabled and not under another element. opts.force skips the
  // checks and the wait. Returns {el, info} when it is, else {result} — the
  // reply to give: not found (with a hint), or found but blocked, with the
  // reason worded once here for both the extension and the daemon.
  async function actionable(sel, opts, verb) {
    const o = opts || {};
    const t = Number(o.timeout);
    const timeout = o.force ? 0 : Math.min(Math.max(Number.isFinite(t) && t > 0 ? t : DEFAULT_ACTION_TIMEOUT_MS, 0), MAX_ACTION_TIMEOUT_MS);
    const deadline = Date.now() + timeout;
    let info;
    let why = "";
    for (;;) {
      info = find(sel);
      if (info.el) {
        info.el.scrollIntoView({ block: "center", inline: "center" });
        why = o.force ? "" : blocker(info.el);
        if (!why) return { el: info.el, info };
      }
      if (Date.now() >= deadline) break;
      await sleep(100);
    }
    if (!info.el) return { result: miss(sel) };
    return {
      result: {
        found: true,
        tag: info.el.tagName.toLowerCase(),
        blocked: `cannot ${verb} ${desc(info.el)} matched by ${sel}: it is ${why} (waited ${timeout}ms) — pass force="true" to ${verb} it anyway`,
      },
    };
  }

  P.getPageText = (sel, opts) => {
        let el;
        if (sel) {
          const info = find(sel);
          el = info.el;
          if (!el) return miss(sel);
          return { found: true, text: el.innerText || "" };
        }
        el = document.body;
        let text = el ? (el.innerText || "") : "";
        // innerText stops at an iframe: append what each visible one shows.
        // Same-origin frames are read here; cross-origin ones are listed in
        // xframes (with Chrome's frame id when known) for the extension to
        // read in the frame itself.
        const xframes = [];
        const H = globalThis.__aglink;
        if (H && H.frameDocOf) {
          const addFrames = (doc, depth) => {
            for (const f of H.deepQueryAll("iframe,frame", doc)) {
              if (!H.isVisible(f)) continue;
              const src = f.getAttribute("src") || f.src || "about:blank";
              const d = H.frameDocOf(f);
              if (d) {
                const t = d.body ? (d.body.innerText || "") : "";
                if (t.trim()) text += `\n\n[iframe ${src}]\n` + t;
                if (depth < 3) addFrames(d, depth + 1);
              } else {
                xframes.push({ src: f.src || src, frameId: frameIdOf(f) });
              }
            }
          };
          addFrames(document, 0);
        }
        const out = { found: true, text };
        if (opts && opts.frames && xframes.length) out.xframes = xframes;
        return out;
      };

  // frameIdOf is Chrome's frame id for an iframe element, available to the
  // extension's content scripts (Chrome 106+); null elsewhere (an app window).
  function frameIdOf(f) {
    try {
      const rt = globalThis.chrome && globalThis.chrome.runtime;
      if (rt && typeof rt.getFrameId === "function") {
        const id = rt.getFrameId(f);
        if (typeof id === "number" && id >= 0) return id;
      }
    } catch (e) {
      // not a content script
    }
    return null;
  }

  // frameInfo resolves the frame part of a "frame=… >> …" selector for the
  // extension, which then runs the inner part in that frame itself — the only
  // way into a cross-origin one.
  P.frameInfo = (sel) => {
    const info = find(sel);
    const el = info.el;
    if (!el) return miss(sel);
    const tag = el.tagName.toLowerCase();
    if (tag !== "iframe" && tag !== "frame") return { found: true, isFrame: false, tag, desc: desc(el) };
    const H = globalThis.__aglink;
    return {
      found: true,
      isFrame: true,
      tag,
      src: el.src || el.getAttribute("src") || "",
      sameOrigin: !!(H && H.frameDocOf && H.frameDocOf(el)),
      frameId: frameIdOf(el),
    };
  };

  P.elementExists = (sel) => {
        const all = globalThis.__aglink ? globalThis.__aglink.resolveAll(sel)
                                        : Array.from(document.querySelectorAll(sel));
        let visible = false;
        for (const el of all) {
          const rc = el.getBoundingClientRect ? el.getBoundingClientRect() : null;
          if (rc && rc.width > 0 && rc.height > 0) { visible = true; break; }
        }
        return { exists: all.length > 0, visible, count: all.length };
      };

  // click waits for the element to be clickable first (see actionable);
  // opts: {timeout, force}.
  P.click = async (sel, btn, opts) => {
        const got = await actionable(sel, opts, "click");
        if (!got.el) return got.result;
        const el = got.el;
        if (btn === "left") {
          el.click();
        } else {
          const rect = el.getBoundingClientRect();
          const opts = {
            bubbles: true,
            cancelable: true,
            view: window,
            clientX: rect.left + rect.width / 2,
            clientY: rect.top + rect.height / 2,
            button: btn === "right" ? 2 : 1,
          };
          el.dispatchEvent(new MouseEvent("mousedown", opts));
          el.dispatchEvent(new MouseEvent("mouseup", opts));
          el.dispatchEvent(new MouseEvent(btn === "right" ? "contextmenu" : "auxclick", opts));
        }
        return noted({ found: true, tag: el.tagName.toLowerCase(), text: (el.textContent || "").trim().slice(0, 80) }, got.info);
      };

  P.doubleClick = async (sel, waitOpts) => {
        const got = await actionable(sel, waitOpts, "double-click");
        if (!got.el) return got.result;
        const el = got.el;
        const rect = el.getBoundingClientRect();
        const opts = {
          bubbles: true,
          cancelable: true,
          view: window,
          clientX: rect.left + rect.width / 2,
          clientY: rect.top + rect.height / 2,
        };
        for (let i = 0; i < 2; i++) {
          el.dispatchEvent(new MouseEvent("mousedown", opts));
          el.dispatchEvent(new MouseEvent("mouseup", opts));
          el.dispatchEvent(new MouseEvent("click", opts));
        }
        el.dispatchEvent(new MouseEvent("dblclick", { ...opts, detail: 2 }));
        return noted({ found: true, tag: el.tagName.toLowerCase(), text: (el.textContent || "").trim().slice(0, 80) }, got.info);
      };

  P.hover = (sel) => {
        const info = find(sel);
        const el = info.el;
        if (!el) return miss(sel);
        el.scrollIntoView({ block: "center", inline: "center" });
        const rect = el.getBoundingClientRect();
        const opts = {
          bubbles: true,
          cancelable: true,
          view: window,
          clientX: rect.left + rect.width / 2,
          clientY: rect.top + rect.height / 2,
        };
        el.dispatchEvent(new MouseEvent("pointerover", opts));
        el.dispatchEvent(new MouseEvent("mouseover", opts));
        el.dispatchEvent(new MouseEvent("mouseenter", { ...opts, bubbles: false }));
        el.dispatchEvent(new MouseEvent("mousemove", opts));
        return noted({ found: true, tag: el.tagName.toLowerCase(), text: (el.textContent || "").trim().slice(0, 80) }, info);
      };

  P.drag = (srcSel, dstSel) => {
        const src = find(srcSel).el;
        if (!src) return { ...miss(srcSel), which: "source", sel: srcSel };
        const dst = find(dstSel).el;
        if (!dst) return { ...miss(dstSel), which: "target", sel: dstSel };
        src.scrollIntoView({ block: "center", inline: "center" });
        const sr = src.getBoundingClientRect();
        const dr = dst.getBoundingClientRect();
        const at = (r) => ({ clientX: r.left + r.width / 2, clientY: r.top + r.height / 2 });
        const sp = at(sr);
        const dp = at(dr);
        const dt = typeof DataTransfer === "function" ? new DataTransfer() : null;
        const mouse = (type, el, p) =>
          el.dispatchEvent(new MouseEvent(type, { bubbles: true, cancelable: true, view: window, clientX: p.clientX, clientY: p.clientY }));
        const dnd = (type, el, p) => {
          let ev;
          try {
            ev = new DragEvent(type, { bubbles: true, cancelable: true, view: window, clientX: p.clientX, clientY: p.clientY, dataTransfer: dt });
          } catch (e) {
            // Some engines forbid passing dataTransfer to the constructor; fall
            // back to a MouseEvent with dataTransfer patched on.
            ev = new MouseEvent(type, { bubbles: true, cancelable: true, view: window, clientX: p.clientX, clientY: p.clientY });
            if (dt) try { Object.defineProperty(ev, "dataTransfer", { value: dt }); } catch (e2) {}
          }
          el.dispatchEvent(ev);
        };
        // Pointer/mouse path (JS reorder handlers).
        mouse("pointerdown", src, sp);
        mouse("mousedown", src, sp);
        mouse("mousemove", src, sp);
        mouse("mousemove", dst, dp);
        // HTML5 DnD path (native drop zones).
        dnd("dragstart", src, sp);
        dnd("dragenter", dst, dp);
        dnd("dragover", dst, dp);
        dnd("drop", dst, dp);
        dnd("dragend", src, dp);
        // Release the pointer over the target.
        mouse("mouseup", dst, dp);
        mouse("pointerup", dst, dp);
        return { found: true, srcTag: src.tagName.toLowerCase(), dstTag: dst.tagName.toLowerCase() };
      };

  // rectOf scrolls an element into view and returns its box in CSS px
  // relative to the viewport, plus the viewport size and scroll offset, for an
  // element screenshot.
  P.rectOf = (sel) => {
    const el = find(sel).el;
    if (!el) return miss(sel);
    el.scrollIntoView({ block: "center", inline: "center" });
    const r = el.getBoundingClientRect();
    return {
      found: true, x: r.left, y: r.top, width: r.width, height: r.height,
      vw: window.innerWidth, vh: window.innerHeight, sx: window.scrollX || 0, sy: window.scrollY || 0,
    };
  };

  P.getHtml = (sel) => {
        let el;
        if (sel) {
          el = find(sel).el;
          if (!el) return miss(sel);
        } else {
          el = document.documentElement;
        }
        return { found: true, html: el.outerHTML || "" };
      };

  P.queryAll = (sel, attrList, maxEls) => {
        const helper = globalThis.__aglink;
        const cands = helper ? helper.resolveAll(sel) : Array.from(document.querySelectorAll(sel));
        // A selector that matches only tables reads them as rows (P.tableRows).
        if (attrList.length === 0 && cands.length && cands.every((el) => el.tagName === "TABLE")) {
          return { lines: tableRows(cands, maxEls) };
        }
        const out = [];
        for (const el of cands) {
          if (out.length >= maxEls) break;
          const rec = {
            tag: el.tagName ? el.tagName.toLowerCase() : "",
            text: (el.textContent || "").trim().replace(/\s+/g, " ").slice(0, 200),
            attrs: [],
          };
          for (const a of attrList) {
            if (a === "text") continue; // text is already its own column
            rec.attrs.push([a, el.getAttribute ? el.getAttribute(a) : null]);
          }
          // With no explicit attrs, surface href for links so link-harvesting works
          // out of the box.
          if (attrList.length === 0 && el.tagName === "A" && el.getAttribute && el.getAttribute("href") != null) {
            rec.attrs.push(["href", el.getAttribute("href")]);
          }
          out.push(rec);
        }
        return out;
      };

  const clean = (s) => String(s == null ? "" : s).trim().replace(/\s+/g, " ");

  // tableRows turns each <table> into one JSON object per body row, keyed by
  // its header cells (thead, or a first row of <th>); columns without a
  // header are c1, c2, …. Several tables are introduced by "table N:".
  function tableRows(tables, maxRows) {
    const lines = [];
    let rows = 0;
    tables.forEach((t, ti) => {
      if (tables.length > 1) lines.push(`table ${ti}:`);
      const trs = Array.from(t.querySelectorAll("tr"));
      const cells = (tr) => Array.from(tr.children).filter((c) => c.tagName === "TD" || c.tagName === "TH");
      let head = [];
      const theadRow = t.querySelector("thead tr");
      if (theadRow) head = cells(theadRow).map((c) => clean(c.textContent));
      else if (trs.length && cells(trs[0]).every((c) => c.tagName === "TH")) head = cells(trs[0]).map((c) => clean(c.textContent));
      const used = new Set();
      const keys = head.map((k, i) => {
        const key = k && !used.has(k) ? k : "c" + (i + 1);
        used.add(key);
        return key;
      });
      for (const tr of trs) {
        if (theadRow ? tr.parentNode && tr.parentNode.tagName === "THEAD" : head.length && tr === trs[0]) continue;
        const cs = cells(tr);
        if (!cs.length) continue;
        if (rows >= maxRows) { lines.push(`… [stopped at ${maxRows} rows — raise max]`); return; }
        const rec = {};
        cs.forEach((c, i) => { rec[keys[i] || "c" + (i + 1)] = clean(c.textContent).slice(0, 300); });
        lines.push(JSON.stringify(rec));
        rows++;
      }
    });
    return lines;
  }

  // extract reads named fields from every element matching sel and returns one
  // JSON object per element. fieldsJSON maps a name to "css" (text of the first
  // match inside the element), "css@attr" (its attribute), "@attr" (the
  // element's own attribute) or "" (the element's own text); a field that
  // matches nothing is null. Input/select/textarea text is their value.
  P.extract = (sel, fieldsJSON, maxEls) => {
    let fields;
    try {
      fields = JSON.parse(fieldsJSON);
    } catch (e) {
      return { error: "fields must be a JSON object like {\"title\":\"h3\",\"url\":\"a@href\"}" };
    }
    if (!fields || typeof fields !== "object" || Array.isArray(fields)) {
      return { error: "fields must be a JSON object of name -> \"css\" | \"css@attr\" | \"@attr\"" };
    }
    const specs = Object.keys(fields).map((name) => {
      const spec = String(fields[name] == null ? "" : fields[name]).trim();
      const at = spec.lastIndexOf("@");
      const css = (at >= 0 ? spec.slice(0, at) : spec).trim();
      return { name, css: css === "." ? "" : css, attr: at >= 0 ? spec.slice(at + 1).trim() : "" };
    });
    const helper = globalThis.__aglink;
    const cands = helper ? helper.resolveAll(sel) : Array.from(document.querySelectorAll(sel));
    const lines = [];
    for (const el of cands) {
      if (lines.length >= maxEls) break;
      const rec = {};
      for (const f of specs) {
        let node = el;
        if (f.css) {
          try {
            node = el.querySelector(f.css);
          } catch (e) {
            return { error: `field ${f.name}: bad selector ${f.css}` };
          }
        }
        if (!node) { rec[f.name] = null; continue; }
        if (f.attr && f.attr !== "text") {
          rec[f.name] = node.getAttribute(f.attr);
        } else if (!f.attr && /^(INPUT|TEXTAREA|SELECT)$/.test(node.tagName)) {
          rec[f.name] = node.value;
        } else {
          rec[f.name] = clean(node.textContent).slice(0, 500);
        }
      }
      lines.push(JSON.stringify(rec));
    }
    return { lines, total: cands.length };
  };

  P.evalExpression = (expr) => {
        try {
          // Indirect eval so the expression evaluates in global scope; supports a
          // bare expression or an IIFE.
          const val = (0, eval)(expr);
          let json;
          if (val === undefined) {
            json = "undefined";
          } else {
            try {
              json = JSON.stringify(val, null, 2);
            } catch (e) {
              json = String(val); // circular / non-serializable → coerce to string
            }
            if (json === undefined) json = String(val); // JSON.stringify(fn) === undefined
          }
          return { ok: true, json };
        } catch (e) {
          return { ok: false, error: String(e && e.message ? e.message : e) };
        }
      };

  P.getAttribute = (sel, attr) => {
        const info = find(sel);
        const el = info.el;
        if (!el) return miss(sel);
        const tag = el.tagName.toLowerCase();
        if (attr === "text") return noted({ found: true, tag, present: true, value: (el.textContent || "").trim() }, info);
        return noted({ found: true, tag, present: el.hasAttribute(attr), value: el.getAttribute(attr) }, info);
      };

  // snapshot renders the visible page as a compact outline — headings,
  // landmarks/dialogs, interactive controls with their state, short text — and
  // numbers every control (ref=eN, marked data-aglink-ref) so the next call can
  // target it as selector "ref=eN". Cheaper than get_page_text for deciding
  // what to click, and unlike list_elements it keeps the structure (which
  // dialog a button is in, which heading a field sits under).
  //
  // Refs are stable: an element that already has one keeps it, and only new
  // elements get the next number, so a second snapshot after a small change
  // still matches the first. Each ref's role and name are remembered
  // (__aglinkRefInfo) so the resolver can re-find an element a framework
  // re-rendered (aglink-inject.js healRef).
  //
  // An iframe gets a ref too. A same-origin one is outlined inline under it
  // (its controls numbered with everything else, so ref=eN reaches them); a
  // cross-origin one is only named — with opts.frames the result is
  // {text, xframes} so the extension can outline those in the frame itself.
  P.snapshot = (maxLines, opts) => {
    const A = globalThis.__aglink;
    if (!A) return "(page helpers missing)";
    const info = {};
    const seen = new Set();
    let seq = globalThis.__aglinkRefSeq || 0;
    const max = Math.max(20, Math.min(maxLines || 200, 1000));
    const CONTROL = new Set(["button", "link", "textbox", "searchbox", "checkbox", "radio", "combobox", "slider",
      "tab", "menuitem", "menuitemcheckbox", "option", "switch", "spinbutton"]);
    const GROUP = new Set(["dialog", "alertdialog", "navigation", "main", "form", "tablist", "menu", "listbox",
      "banner", "complementary", "region", "toolbar"]);
    const SKIP = new Set(["script", "style", "noscript", "template", "svg", "head"]);
    const lines = [];
    const xframes = [];
    let ref = 0; // refs emitted this time
    let cut = false;
    const q = (s, n) => JSON.stringify(String(s).trim().replace(/\s+/g, " ").slice(0, n));
    const emit = (depth, s) => {
      if (lines.length >= max) { cut = true; return false; }
      lines.push("  ".repeat(depth) + "- " + s);
      return true;
    };
    const hiddenStyle = (el) => {
      const view = (el.ownerDocument && el.ownerDocument.defaultView) || window;
      const st = (view.getComputedStyle || getComputedStyle)(el);
      return st.display === "none" || st.visibility === "hidden" || el.getAttribute("aria-hidden") === "true";
    };
    // mark gives el its ref (keeping one it already has).
    const mark = (el) => {
      ref++;
      // A clone of a marked node carries its ref too; renumber the copy.
      let id = el.getAttribute("data-aglink-ref");
      if (!id || seen.has(id)) {
        id = "e" + (++seq);
        el.setAttribute("data-aglink-ref", id);
      }
      seen.add(id);
      return id;
    };
    const kids = (node) => {
      const out = [];
      if (node.shadowRoot) out.push(...node.shadowRoot.childNodes);
      out.push(...node.childNodes);
      return out;
    };
    const walk = (node, depth) => {
      for (const child of kids(node)) {
        if (cut) return;
        if (child.nodeType === Node.TEXT_NODE) {
          const t = child.textContent.trim();
          if (t.length >= 2) emit(depth, "text " + q(t, 80));
          continue;
        }
        if (child.nodeType !== Node.ELEMENT_NODE) continue;
        const el = child;
        const tag = el.tagName.toLowerCase();
        if (SKIP.has(tag) || hiddenStyle(el)) continue;
        if (tag === "iframe" || tag === "frame") {
          if (!A.isVisible(el)) continue;
          const id = mark(el);
          info[id] = { role: "", name: "" };
          const src = el.getAttribute("src") || el.src || "about:blank";
          const label = el.getAttribute("title") || el.getAttribute("name") || "";
          const doc = A.frameDocOf ? A.frameDocOf(el) : null;
          const head = "iframe " + q(src, 100) + (label ? " " + q(label, 40) : "");
          if (doc && doc.body) {
            if (!emit(depth, head + ` [ref=${id}]:`)) return;
            walk(doc.body, depth + 1);
          } else {
            // Only the extension can run inside a cross-origin frame (it
            // knows the frame id); an app window's DevTools path cannot.
            const fid = frameIdOf(el);
            emit(depth, head + ` [ref=${id}, cross-origin${fid != null ? ` — act inside with selector "frame=ref=${id} >> …"` : " — not reachable from here"}]`);
            xframes.push({ ref: id, src: el.src || src, frameId: fid });
          }
          continue;
        }
        const role = A.roleOf(el);
        if (CONTROL.has(role) || (tag === "input" && role)) {
          if (!A.isVisible(el)) continue;
          const id = mark(el);
          info[id] = { role, name: A.accessibleName(el) };
          // A <select>'s text is every option run together; its name is only
          // what labels it (the chosen option shows as value= below).
          const name = tag === "select"
            ? (el.getAttribute("aria-label") || (el.labels && el.labels[0] && el.labels[0].textContent) || el.getAttribute("title") || "")
            : A.accessibleName(el);
          let s = role + " " + q(name, 60);
          const st = [];
          if (role === "textbox" || role === "searchbox" || role === "spinbutton") {
            if (el.value) st.push("value=" + q(el.type === "password" ? "••••" : el.value, 40));
          }
          if (role === "combobox" && tag === "select" && el.selectedOptions && el.selectedOptions[0]) {
            st.push("value=" + q(el.selectedOptions[0].textContent, 40));
          }
          if (el.checked || el.getAttribute("aria-checked") === "true") st.push("checked");
          if (el.getAttribute("aria-selected") === "true") st.push("selected");
          if (el.getAttribute("aria-expanded") === "true") st.push("expanded");
          if (el.disabled || el.getAttribute("aria-disabled") === "true") st.push("disabled");
          st.push("ref=" + id);
          emit(depth, s + " [" + st.join(", ") + "]");
          continue; // a control's own text is its name
        }
        if (role === "heading") {
          if (A.isVisible(el)) {
            const lvl = /^h([1-6])$/.exec(tag);
            emit(depth, "heading " + q(el.textContent, 80) + (lvl ? " [level=" + lvl[1] + "]" : ""));
          }
          continue;
        }
        if (role === "img") {
          const alt = A.accessibleName(el);
          if (alt && A.isVisible(el)) emit(depth, "img " + q(alt, 60));
          continue;
        }
        if (GROUP.has(role)) {
          const name = el.getAttribute("aria-label") || "";
          if (!emit(depth, role + (name ? " " + q(name, 60) : "") + ":")) return;
          walk(el, depth + 1);
          continue;
        }
        walk(el, depth);
      }
    };
    walk(document.body || document.documentElement, 0);
    // Drop refs on elements this snapshot no longer shows.
    const docs = [document, ...(A.frameDocs ? A.frameDocs(document) : [])];
    for (const doc of docs) {
      for (const el of A.deepQueryAll("[data-aglink-ref]", doc)) {
        if (!seen.has(el.getAttribute("data-aglink-ref"))) el.removeAttribute("data-aglink-ref");
      }
    }
    globalThis.__aglinkRefSeq = seq;
    globalThis.__aglinkRefInfo = info;
    const head = `[snapshot ${lines.length} lines, ${ref} refs — act on one with selector "ref=eN"]`;
    const text = head + "\n" + lines.join("\n") + (cut ? `\n… [truncated at ${max} lines — raise maxLines, or scope with get_page_text selector]` : "");
    return opts && opts.frames ? { text, xframes } : text;
  };

  P.listElements = (selectorList, idAttr, maxEls) => {
        // Pierce open shadow roots when the helper is present, so web-component
        // internals (buttons/inputs inside a custom element) are listed too;
        // fall back to the flat top-document scan otherwise.
        const helper = globalThis.__aglink;
        const clearSet = helper ? helper.deepQueryAll(`[${idAttr}]`) : document.querySelectorAll(`[${idAttr}]`);
        clearSet.forEach((el) => el.removeAttribute(idAttr));
        const candidates = helper ? helper.deepQueryAll(selectorList) : Array.from(document.querySelectorAll(selectorList));
        const out = [];
        let idx = 0;
        for (const el of candidates) {
          if (out.length >= maxEls) break;
          const rect = el.getBoundingClientRect();
          // A non-zero rect is enough to mean "rendered": offsetParent is null
          // (misleadingly) for <body>/<html> and for position:fixed elements
          // too, not just display:none — toasts/modals are commonly fixed, so
          // checking it here would silently drop exactly the elements a caller
          // is most likely waiting to interact with.
          if (rect.width <= 0 || rect.height <= 0) continue;
          el.setAttribute(idAttr, String(idx));
          const label = (
            el.getAttribute("aria-label") ||
            el.getAttribute("placeholder") ||
            el.value ||
            el.textContent ||
            ""
          ).trim().replace(/\s+/g, " ").slice(0, 60);
          out.push({
            idx,
            tag: el.tagName.toLowerCase(),
            role: el.getAttribute("role") || "",
            type: el.getAttribute("type") || "",
            label,
            disabled: !!el.disabled,
            x: Math.round(rect.left + rect.width / 2),
            y: Math.round(rect.top + rect.height / 2),
          });
          idx++;
        }
        return out;
      };

  P.waitForElement = (sel) => {
          const el = globalThis.__aglink ? globalThis.__aglink.resolve(sel) : document.querySelector(sel);
          if (!el) return { found: false };
          const rect = el.getBoundingClientRect();
          // See listElements' matching comment: offsetParent is null for
          // <body>/<html> and position:fixed elements too, not just
          // display:none, so it must not gate visibility here.
          const visible = rect.width > 0 && rect.height > 0;
          return { found: true, visible, tag: el.tagName.toLowerCase() };
        };

  // typeText puts value into an input, textarea or contenteditable. mode
  // "set" (default) replaces what is there, "append" adds to the end.
  // Plain fields go through the native value setter (bypassing a framework's
  // instance setter, so React notices) and get input+change. A rich editor
  // (contenteditable: ProseMirror, Lexical, Slate, …) keeps its own model of
  // the text and would undo — or fall out of sync with — a raw textContent
  // write, so the text is inserted the way typing would: select, then
  // execCommand("insertText"), which fires beforeinput/input the editor
  // handles. textContent stays only as the fallback when that is refused.
  P.typeText = (sel, value, mode) => {
        const info = find(sel);
        const el = info.el;
        if (!el) return miss(sel);
        const append = mode === "append";
        el.scrollIntoView({ block: "center", inline: "center" });
        el.focus();
        let via = "";
        if (el.isContentEditable) {
          via = editableInsert(el, String(value), append);
        } else {
          const view = (el.ownerDocument && el.ownerDocument.defaultView) || window;
          const ctorName = el.tagName === "TEXTAREA" ? "HTMLTextAreaElement" : "HTMLInputElement";
          const Ctor = view[ctorName] || globalThis[ctorName];
          const setter = Ctor && Ctor.prototype && Object.getOwnPropertyDescriptor(Ctor.prototype, "value")?.set;
          const next = append ? String(el.value == null ? "" : el.value) + value : value;
          if (setter && el instanceof Ctor) {
            setter.call(el, next);
          } else {
            el.value = next;
          }
          el.dispatchEvent(new Event("input", { bubbles: true }));
          el.dispatchEvent(new Event("change", { bubbles: true }));
        }
        const out = noted({ found: true, tag: el.tagName.toLowerCase() }, info);
        if (via) out.via = via;
        return out;
      };

  // selectContents selects all of el's content, or just puts the caret at
  // its end (toEnd), for inputs and contenteditables alike.
  function selectContents(el, toEnd) {
    if (!el.isContentEditable) {
      try {
        const n = String(el.value == null ? "" : el.value).length;
        if (toEnd) el.setSelectionRange(n, n);
        else el.select();
      } catch (e) {
        // input types without a selection (number, email in some engines)
      }
      return;
    }
    const doc = el.ownerDocument || document;
    const view = doc.defaultView || window;
    const s = view.getSelection ? view.getSelection() : null;
    if (!s || !doc.createRange) return;
    const range = doc.createRange();
    range.selectNodeContents(el);
    if (toEnd) range.collapse(false);
    s.removeAllRanges();
    s.addRange(range);
  }

  // editableInsert replaces (or appends to) a contenteditable's text through
  // the editing pipeline; see typeText. Returns how it got in.
  function editableInsert(el, value, append) {
    const doc = el.ownerDocument || document;
    selectContents(el, append);
    let ok = false;
    try {
      ok = !!doc.execCommand && (value === "" ? (append ? true : doc.execCommand("delete", false)) : doc.execCommand("insertText", false, value));
    } catch (e) {
      ok = false;
    }
    if (ok) return "insertText";
    el.textContent = append ? (el.textContent || "") + value : value;
    el.dispatchEvent(new Event("input", { bubbles: true }));
    return "textContent";
  }

  // focusForKeys readies an element for real keystrokes (type mode "keys"):
  // scrolled into view, focused, caret at the end of what it holds. The
  // extension / daemon then sends the text through the debugger's
  // Input.insertText, which the page sees exactly as typing.
  P.focusForKeys = (sel) => {
        const info = find(sel);
        const el = info.el;
        if (!el) return miss(sel);
        el.scrollIntoView({ block: "center", inline: "center" });
        el.focus();
        selectContents(el, true);
        const doc = el.ownerDocument || document;
        const focused = doc.activeElement === el || within(el, doc.activeElement);
        return noted({ found: true, tag: el.tagName.toLowerCase(), focused }, info);
      };

  P.getValue = (sel) => {
        const info = find(sel);
        const el = info.el;
        if (!el) return miss(sel);
        const tag = el.tagName.toLowerCase();
        if (el.isContentEditable) {
          return noted({ found: true, tag, value: el.textContent || "" }, info);
        }
        if ("value" in el) {
          return noted({ found: true, tag, value: el.value }, info);
        }
        return noted({ found: true, tag, value: el.textContent || "" }, info);
      };

  P.keyCombo = (comboStr, keySpecs, modProps) => {
        const parts = comboStr.split("+").map((p) => p.trim().toLowerCase()).filter(Boolean);
        if (parts.length === 0) return { ok: false, error: "empty key combo" };
        const mods = {};
        let keyToken = null;
        parts.forEach((p, i) => {
          if (modProps[p] && i !== parts.length - 1) {
            mods[modProps[p]] = true;
          } else {
            keyToken = p;
          }
        });
        if (!keyToken) return { ok: false, error: `no key in combo "${comboStr}"` };
        let spec = keySpecs[keyToken];
        if (!spec && keyToken.length === 1) {
          spec = { key: keyToken, code: "Key" + keyToken.toUpperCase(), keyCode: keyToken.toUpperCase().charCodeAt(0) };
        }
        if (!spec) return { ok: false, error: `unknown key "${keyToken}" in combo "${comboStr}"` };
        let el = document.activeElement || document.body;
        // Focus inside a same-origin iframe shows here as the <iframe>; the
        // keys belong to what is focused inside it.
        for (let i = 0; i < 4 && el && /^(IFRAME|FRAME)$/.test(el.tagName || ""); i++) {
          const H = globalThis.__aglink;
          const d = H && H.frameDocOf ? H.frameDocOf(el) : null;
          if (!d || !d.activeElement) break;
          el = d.activeElement;
        }
        const opts = {
          key: spec.key,
          code: spec.code,
          keyCode: spec.keyCode,
          which: spec.keyCode,
          bubbles: true,
          cancelable: true,
          ...mods,
        };
        el.dispatchEvent(new KeyboardEvent("keydown", opts));
        el.dispatchEvent(new KeyboardEvent("keyup", opts));
        return { ok: true, tag: el.tagName ? el.tagName.toLowerCase() : "document" };
      };

  P.scroll = (sel, dxPx, dyPx) => {
        const target = sel ? find(sel).el : null;
        if (sel && !target) return miss(sel);
        (target || window).scrollBy({ left: dxPx, top: dyPx, behavior: "instant" });
        return { found: true };
      };

  P.selectOption = (sel, val, lbl) => {
        const info = find(sel);
        const el = info.el;
        if (!el) return miss(sel);
        if (el.tagName !== "SELECT") return { found: true, isSelect: false, tag: el.tagName.toLowerCase() };
        let match = null;
        for (const opt of el.options) {
          if (val !== null && val !== undefined && opt.value === String(val)) {
            match = opt;
            break;
          }
          if (lbl !== null && lbl !== undefined && opt.textContent.trim() === String(lbl)) {
            match = opt;
            break;
          }
        }
        if (!match) return { found: true, isSelect: true, matched: false };
        el.value = match.value;
        el.dispatchEvent(new Event("input", { bubbles: true }));
        el.dispatchEvent(new Event("change", { bubbles: true }));
        return noted({ found: true, isSelect: true, matched: true, selected: match.textContent.trim() }, info);
      };

  // The daemon calls these instead of keyCombo/listElements directly, so the
  // key table and the interactive-element selector never have to travel over
  // the wire on every call. The extension keeps passing its own copies.
  P.keyComboDefault = (combo) => P.keyCombo(combo, KEY_SPECS, MOD_PROPS);
  P.listElementsDefault = (max) => P.listElements(INTERACTIVE_SELECTOR, AGLINK_ID_ATTR, max);

  P.constants = { AGLINK_ID_ATTR, INTERACTIVE_SELECTOR, KEY_SPECS, MOD_PROPS };
  // Version of this file. The daemon re-injects into an app window whose copy
  // is older (apptarget.go pageActionsVersion); bump both together.
  P.__v = 8;
  globalThis.__aglinkPage = P;
})();
