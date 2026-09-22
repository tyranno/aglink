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

  P.getPageText = (sel) => {
        let el;
        if (sel) {
          el = globalThis.__aglink ? globalThis.__aglink.resolve(sel) : document.querySelector(sel);
          if (!el) return { found: false };
        } else {
          el = document.body;
        }
        return { found: true, text: el ? (el.innerText || "") : "" };
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

  P.click = (sel, btn) => {
        const el = globalThis.__aglink ? globalThis.__aglink.resolve(sel) : document.querySelector(sel);
        if (!el) return { found: false };
        el.scrollIntoView({ block: "center", inline: "center" });
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
        return { found: true, tag: el.tagName.toLowerCase(), text: (el.textContent || "").trim().slice(0, 80) };
      };

  P.doubleClick = (sel) => {
        const el = globalThis.__aglink ? globalThis.__aglink.resolve(sel) : document.querySelector(sel);
        if (!el) return { found: false };
        el.scrollIntoView({ block: "center", inline: "center" });
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
        return { found: true, tag: el.tagName.toLowerCase(), text: (el.textContent || "").trim().slice(0, 80) };
      };

  P.hover = (sel) => {
        const el = globalThis.__aglink ? globalThis.__aglink.resolve(sel) : document.querySelector(sel);
        if (!el) return { found: false };
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
        return { found: true, tag: el.tagName.toLowerCase(), text: (el.textContent || "").trim().slice(0, 80) };
      };

  P.drag = (srcSel, dstSel) => {
        const resolve = (s) => (globalThis.__aglink ? globalThis.__aglink.resolve(s) : document.querySelector(s));
        const src = resolve(srcSel);
        if (!src) return { found: false, which: "source", sel: srcSel };
        const dst = resolve(dstSel);
        if (!dst) return { found: false, which: "target", sel: dstSel };
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

  P.getHtml = (sel) => {
        let el;
        if (sel) {
          el = globalThis.__aglink ? globalThis.__aglink.resolve(sel) : document.querySelector(sel);
          if (!el) return { found: false };
        } else {
          el = document.documentElement;
        }
        return { found: true, html: el.outerHTML || "" };
      };

  P.queryAll = (sel, attrList, maxEls) => {
        const helper = globalThis.__aglink;
        const cands = helper ? helper.resolveAll(sel) : Array.from(document.querySelectorAll(sel));
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
        const el = globalThis.__aglink ? globalThis.__aglink.resolve(sel) : document.querySelector(sel);
        if (!el) return { found: false };
        const tag = el.tagName.toLowerCase();
        if (attr === "text") return { found: true, tag, present: true, value: (el.textContent || "").trim() };
        return { found: true, tag, present: el.hasAttribute(attr), value: el.getAttribute(attr) };
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

  P.typeText = (sel, value) => {
        const el = globalThis.__aglink ? globalThis.__aglink.resolve(sel) : document.querySelector(sel);
        if (!el) return { found: false };
        el.scrollIntoView({ block: "center", inline: "center" });
        el.focus();
        if (el.isContentEditable) {
          el.textContent = value;
        } else {
          const proto = el.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
          const setter = Object.getOwnPropertyDescriptor(proto, "value")?.set;
          if (setter) {
            setter.call(el, value);
          } else {
            el.value = value;
          }
        }
        el.dispatchEvent(new Event("input", { bubbles: true }));
        el.dispatchEvent(new Event("change", { bubbles: true }));
        return { found: true, tag: el.tagName.toLowerCase() };
      };

  P.getValue = (sel) => {
        const el = globalThis.__aglink ? globalThis.__aglink.resolve(sel) : document.querySelector(sel);
        if (!el) return { found: false };
        const tag = el.tagName.toLowerCase();
        if (el.isContentEditable) {
          return { found: true, tag, value: el.textContent || "" };
        }
        if ("value" in el) {
          return { found: true, tag, value: el.value };
        }
        return { found: true, tag, value: el.textContent || "" };
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
        const el = document.activeElement || document.body;
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
        const target = sel ? (globalThis.__aglink ? globalThis.__aglink.resolve(sel) : document.querySelector(sel)) : null;
        if (sel && !target) return { found: false };
        (target || window).scrollBy({ left: dxPx, top: dyPx, behavior: "instant" });
        return { found: true };
      };

  P.selectOption = (sel, val, lbl) => {
        const el = globalThis.__aglink ? globalThis.__aglink.resolve(sel) : document.querySelector(sel);
        if (!el) return { found: false };
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
        return { found: true, isSelect: true, matched: true, selected: match.textContent.trim() };
      };

  // The daemon calls these instead of keyCombo/listElements directly, so the
  // key table and the interactive-element selector never have to travel over
  // the wire on every call. The extension keeps passing its own copies.
  P.keyComboDefault = (combo) => P.keyCombo(combo, KEY_SPECS, MOD_PROPS);
  P.listElementsDefault = (max) => P.listElements(INTERACTIVE_SELECTOR, AGLINK_ID_ATTR, max);

  P.constants = { AGLINK_ID_ATTR, INTERACTIVE_SELECTOR, KEY_SPECS, MOD_PROPS };
  globalThis.__aglinkPage = P;
})();
