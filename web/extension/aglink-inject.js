// aglink-web shared in-page resolver (ISOLATED world).
//
// Injected via chrome.scripting.executeScript({files:[...]}) right before every
// selector-based command runs, so all of them share ONE element-resolution
// engine instead of each calling a bare document.querySelector. Two capabilities
// this adds over document.querySelector — the Playwright-style "component
// control" gap this closes:
//
//   1. Shadow-DOM piercing. document.querySelector cannot see inside a custom
//      element's (open) shadow root, so any selector targeting a web-component's
//      internals silently returned "no element". deepQueryAll walks open shadow
//      roots too. (Closed roots are unreachable by design — nothing can pierce
//      those.)
//   2. Semantic locators. A selector may be a plain CSS selector (default,
//      shadow-piercing) OR one of these Playwright-like prefixes, which target
//      by ACCESSIBLE role/name/label instead of fragile CSS classes:
//        role=button                     — any element with that ARIA role
//        role=button[name="Save"]        — …whose accessible name matches
//        text=Login                      — visible text contains "Login"
//        text="Login"                    — …equals "Login" exactly
//        label=Email                     — form control labelled "Email"
//        placeholder=Search              — input with that placeholder
//        testid=submit                   — [data-testid=submit] (& -test-id/-test)
//   3. Frames. frame=<selector> >> <selector> resolves the second selector
//      inside the (same-origin) iframe the first one matches, and ref=eN also
//      finds refs a snapshot gave inside same-origin iframes. A cross-origin
//      frame is out of this script's reach: the extension routes those to the
//      frame itself (background.js locate), where only the inner part runs.
//
// Idempotent: re-injecting just re-defines globalThis.__aglink. Everything runs
// in the extension's isolated world, which shares globalThis with the action
// funcs executeScript runs next (same tab+frame+world), so they can call
// __aglink.resolve(...). background.js still falls back to document.querySelector
// if this global is somehow absent.
(function () {
  // Bump __v whenever this file changes: a page that already holds an older
  // copy keeps it until reload otherwise, and a new locator kind would be
  // silently missing there.
  if (globalThis.__aglink && globalThis.__aglink.__v === 4) return;

  // isVisible mirrors the rendered-check used elsewhere (non-zero rect) plus a
  // computed-style gate, so semantic locators prefer the element a human would
  // actually see when several match.
  function isVisible(el) {
    if (!el || !el.getBoundingClientRect) return false;
    const r = el.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) return false;
    const view = (el.ownerDocument && el.ownerDocument.defaultView) || window;
    const st = view.getComputedStyle(el);
    if (!st) return true;
    if (st.visibility === "hidden" || st.display === "none") return false;
    return true;
  }

  function cssEscape(s) {
    if (window.CSS && CSS.escape) return CSS.escape(String(s));
    return String(s).replace(/["\\]/g, "\\$&");
  }

  // deepQueryAll matches a CSS selector across a document (the page's own by
  // default) AND every open shadow root in it, breadth-first so results
  // roughly follow document order.
  function deepQueryAll(css, root) {
    const out = [];
    const roots = [root || document];
    while (roots.length) {
      const r = roots.shift();
      let matches = [];
      try {
        matches = r.querySelectorAll(css);
      } catch (e) {
        matches = [];
      }
      for (const m of matches) out.push(m);
      for (const host of r.querySelectorAll("*")) {
        if (host.shadowRoot) roots.push(host.shadowRoot);
      }
    }
    return out;
  }

  // deepAll returns every element in a document and all its open shadow roots
  // — the candidate set the semantic locators filter down.
  function deepAll(root) {
    const out = [];
    const roots = [root || document];
    while (roots.length) {
      const r = roots.shift();
      for (const el of r.querySelectorAll("*")) {
        out.push(el);
        if (el.shadowRoot) roots.push(el.shadowRoot);
      }
    }
    return out;
  }

  // frameDocOf returns an iframe's document when this script may read it
  // (same origin), else null.
  function frameDocOf(el) {
    if (!el || !el.tagName || (el.tagName !== "IFRAME" && el.tagName !== "FRAME")) return null;
    try {
      return el.contentDocument || null;
    } catch (e) {
      return null; // cross-origin
    }
  }

  // frameDocs lists the documents of the same-origin iframes under root,
  // nested ones included (a few levels deep).
  function frameDocs(root, depth) {
    const out = [];
    if ((depth || 0) > 3) return out;
    for (const f of deepQueryAll("iframe,frame", root || document)) {
      const d = frameDocOf(f);
      if (d) {
        out.push(d);
        out.push(...frameDocs(d, (depth || 0) + 1));
      }
    }
    return out;
  }

  // contains is Node.contains that also climbs out of shadow roots, so an
  // element inside a component counts as inside its host.
  function contains(outer, inner) {
    for (let n = inner; n; n = n.parentNode || n.host) {
      if (n === outer) return true;
    }
    return false;
  }

  // splitFrame parses "frame=<outer> >> <inner>"; null when sel is not one.
  function splitFrame(sel) {
    const m = /^\s*frame=([\s\S]+?)\s*>>\s*([\s\S]+)$/.exec(sel || "");
    return m ? { frame: m[1].trim(), inner: m[2].trim() } : null;
  }

  // roleOf returns an element's explicit ARIA role, or a pragmatic implicit
  // role for the common interactive tags (enough for role= targeting; not a
  // full ARIA-in-HTML implementation).
  function roleOf(el) {
    const explicit = el.getAttribute && el.getAttribute("role");
    if (explicit) return explicit.trim().toLowerCase();
    const tag = el.tagName ? el.tagName.toLowerCase() : "";
    switch (tag) {
      case "a": return el.hasAttribute("href") ? "link" : "";
      case "button": return "button";
      case "select": return "combobox";
      case "textarea": return "textbox";
      case "input": {
        const t = (el.getAttribute("type") || "text").toLowerCase();
        if (t === "checkbox") return "checkbox";
        if (t === "radio") return "radio";
        if (t === "range") return "slider";
        if (t === "button" || t === "submit" || t === "reset" || t === "image") return "button";
        if (t === "search") return "searchbox";
        return "textbox";
      }
      case "h1": case "h2": case "h3": case "h4": case "h5": case "h6": return "heading";
      case "img": return "img";
      case "nav": return "navigation";
      default: return "";
    }
  }

  // accessibleName is a pragmatic accessible-name computation: aria-label,
  // aria-labelledby, an associated <label>, placeholder/value for inputs, then
  // alt/title, falling back to visible text.
  function accessibleName(el) {
    if (!el || !el.getAttribute) return "";
    const al = el.getAttribute("aria-label");
    if (al && al.trim()) return al.trim();
    const lb = el.getAttribute("aria-labelledby");
    if (lb) {
      const doc = el.ownerDocument || document;
      const txt = lb.split(/\s+/)
        .map((id) => { const t = doc.getElementById(id); return t ? t.textContent : ""; })
        .join(" ").trim();
      if (txt) return txt.replace(/\s+/g, " ");
    }
    if (el.labels && el.labels.length) {
      const t = Array.from(el.labels).map((l) => l.textContent).join(" ").trim();
      if (t) return t.replace(/\s+/g, " ");
    }
    const tag = el.tagName ? el.tagName.toLowerCase() : "";
    if (tag === "input" || tag === "textarea") {
      const ph = el.getAttribute("placeholder");
      if (ph && ph.trim()) return ph.trim();
      const t = (el.getAttribute("type") || "").toLowerCase();
      if ((t === "submit" || t === "button" || t === "reset") && el.value) return String(el.value).trim();
    }
    const alt = el.getAttribute("alt");
    if (alt && alt.trim()) return alt.trim();
    const title = el.getAttribute("title");
    if (title && title.trim()) return title.trim();
    return (el.textContent || "").trim().replace(/\s+/g, " ");
  }

  // unquote strips matching surrounding quotes and reports whether the value was
  // quoted (quoted ⇒ exact match, unquoted ⇒ substring match).
  function unquote(s) {
    s = s.trim();
    if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) {
      return { text: s.slice(1, -1), exact: true };
    }
    return { text: s, exact: false };
  }

  function matchText(hay, needle, exact) {
    hay = (hay || "").trim().replace(/\s+/g, " ");
    if (exact) return hay === needle;
    return hay.toLowerCase().includes(String(needle).toLowerCase());
  }

  // ref=e12 targets an element by the number the last snapshot gave it
  // (page-actions.js P.snapshot marks them data-aglink-ref).
  //
  // healRef re-finds a ref whose element is gone — a framework re-rendered it
  // as a new node — by the role and name the snapshot recorded. Only a single
  // visible, unmarked match counts; anything ambiguous stays "not found".
  function healRef(id, root) {
    const info = globalThis.__aglinkRefInfo && globalThis.__aglinkRefInfo[id];
    if (!info || !info.name) return [];
    const r = root || document;
    const hits = [r, ...frameDocs(r)].flatMap((d) => deepAll(d)).filter((el) =>
      el.getAttribute && !el.hasAttribute("data-aglink-ref") &&
      roleOf(el) === info.role && accessibleName(el) === info.name && isVisible(el));
    if (hits.length !== 1) return [];
    hits[0].setAttribute("data-aglink-ref", id);
    return hits;
  }

  const LOCATOR_KINDS = ["role", "text", "label", "placeholder", "testid", "ref", "frame"];

  // innermost drops every candidate that contains another one (text= matches
  // a button and also each of its ancestors up to <html>). A visible match
  // only prunes visible ancestors, so a hidden copy deep inside never wins
  // over the visible element around it.
  function innermost(cands) {
    const set = new Set(cands);
    const drop = new Set();
    for (const c of cands) {
      const vis = isVisible(c);
      for (let n = c.parentNode || c.host; n; n = n.parentNode || n.host) {
        if (set.has(n) && (vis || !isVisible(n))) drop.add(n);
      }
    }
    return cands.filter((c) => !drop.has(c));
  }

  // resolveAll returns every element matching the selector spec, visible ones
  // first (stable within each group). A leading `<kind>=` picks a semantic
  // locator; anything else is a shadow-piercing CSS selector. root is the
  // document to search (an iframe's, for frame=); the page's own by default.
  function resolveAll(selector, root) {
    root = root || document;
    let cands = [];
    const m = /^([a-zA-Z]+)=([\s\S]+)$/.exec(selector || "");
    const kind = m ? m[1].toLowerCase() : "";
    if (m && LOCATOR_KINDS.includes(kind)) {
      const rest = m[2];
      if (kind === "frame") {
        // frame=<outer> >> <inner>: inner, inside the first same-origin frame
        // outer matches. Without ">>" it is the frame element itself.
        const fs = splitFrame(selector);
        if (!fs) return resolveAll(rest, root);
        for (const f of resolveAll(fs.frame, root)) {
          const d = frameDocOf(f);
          if (d) return resolveAll(fs.inner, d);
        }
        return [];
      }
      if (kind === "role") {
        // role=button   or   role=button[name="Save"]  (name is substring; quote for exact)
        const rm = /^\s*([a-zA-Z]+)\s*(?:\[name=([\s\S]+)\])?\s*$/.exec(rest);
        if (rm) {
          const wantRole = rm[1].toLowerCase();
          const nameSpec = rm[2] != null ? unquote(rm[2]) : null;
          cands = deepAll(root).filter((el) => roleOf(el) === wantRole)
            .filter((el) => !nameSpec || matchText(accessibleName(el), nameSpec.text, nameSpec.exact));
        }
      } else if (kind === "ref") {
        // A snapshot numbers controls inside same-origin iframes too.
        const id = unquote(rest).text.trim();
        const q = `[data-aglink-ref="${cssEscape(id)}"]`;
        cands = deepQueryAll(q, root);
        if (!cands.length) {
          for (const d of frameDocs(root)) {
            cands = deepQueryAll(q, d);
            if (cands.length) break;
          }
        }
        if (!cands.length) cands = healRef(id, root);
      } else if (kind === "testid") {
        const v = unquote(rest).text;
        const e = cssEscape(v);
        cands = deepQueryAll(`[data-testid="${e}"],[data-test-id="${e}"],[data-test="${e}"]`, root);
      } else if (kind === "placeholder") {
        const v = unquote(rest);
        cands = deepAll(root).filter((el) => el.getAttribute && matchText(el.getAttribute("placeholder"), v.text, v.exact));
      } else if (kind === "label") {
        const v = unquote(rest);
        cands = deepAll(root).filter((el) => {
          const tag = el.tagName ? el.tagName.toLowerCase() : "";
          const interactive = ["input", "textarea", "select", "button", "a"].includes(tag) || (el.getAttribute && el.getAttribute("role"));
          if (!interactive) return false;
          return matchText(accessibleName(el), v.text, v.exact);
        });
      } else if (kind === "text") {
        const v = unquote(rest);
        cands = deepAll(root).filter((el) => el.textContent && matchText(el.textContent, v.text, v.exact));
        // Every ancestor of a match also matches; keep the innermost so
        // text=Save targets the <button>, not <body>, and so a match count
        // means distinct elements.
        cands = innermost(cands);
        cands.sort((a, b) => (a.textContent || "").length - (b.textContent || "").length);
      }
    } else {
      cands = deepQueryAll(selector, root);
    }
    const vis = [], hid = [];
    for (const el of cands) (isVisible(el) ? vis : hid).push(el);
    return vis.concat(hid);
  }

  function resolve(selector) {
    const all = resolveAll(selector);
    return all.length ? all[0] : null;
  }

  // describe is a short human tag for an element in a result or an error:
  // tag#id.class "name", plus (hidden) when it is not rendered.
  function describe(el) {
    if (!el || !el.tagName) return String(el);
    let s = el.tagName.toLowerCase();
    const id = el.getAttribute && el.getAttribute("id");
    if (id) s += "#" + id;
    const cls = String((el.getAttribute && el.getAttribute("class")) || "").trim().split(/\s+/).filter(Boolean).slice(0, 2);
    if (cls.length) s += "." + cls.join(".");
    if (s.length > 60) s = s.slice(0, 60) + "…";
    const name = accessibleName(el).slice(0, 40);
    if (name) s += " " + JSON.stringify(name);
    if (!isVisible(el)) s += " (hidden)";
    return s;
  }

  // resolveInfo is resolve plus what a result should say about the choice:
  // when several elements matched, note names the first few, so a click that
  // landed on the wrong one of two "Save" buttons shows in the reply.
  function resolveInfo(selector) {
    const all = resolveAll(selector);
    const out = { el: all[0] || null, count: all.length, note: "" };
    if (all.length > 1) {
      const vis = all.filter(isVisible).length;
      const list = all.slice(0, 3).map((e, i) => `${i + 1}) ${describe(e)}`).join(" ");
      out.note = `matched ${all.length} elements${vis !== all.length ? ` (${vis} visible)` : ""}, used the first: ${list}` +
        (all.length > 3 ? " …" : "") + " — narrow the selector (or use ref= from page_snapshot) if that is the wrong one";
    }
    return out;
  }

  // needlesOf pulls out what a selector is looking for, to find elements that
  // nearly match it: the text of a semantic locator, or the id / class /
  // attribute values of a CSS selector.
  function needlesOf(selector) {
    const m = /^([a-zA-Z]+)=([\s\S]+)$/.exec(selector || "");
    const kind = m ? m[1].toLowerCase() : "";
    if (m && LOCATOR_KINDS.includes(kind)) {
      if (kind === "role") {
        const rm = /\[name=([\s\S]+)\]/.exec(m[2]);
        return rm ? [unquote(rm[1]).text] : [];
      }
      return [unquote(m[2]).text];
    }
    const out = [];
    const re = /#([\w-]+)|\.([\w-]+)|\[[\w-]+[*^$~|]?=\s*["']?([^"'\]]+)["']?\s*\]/g;
    let t;
    while ((t = re.exec(selector || ""))) out.push(t[1] || t[2] || t[3]);
    return out;
  }

  // nearMiss explains a selector that matched nothing: a bad CSS selector, a
  // frame that is missing or cross-origin, a ref from an outdated snapshot,
  // or up to three visible elements whose text / label / id contains what it
  // looked for. "" when there is nothing useful to say.
  function nearMiss(selector, root) {
    root = root || document;
    const m = /^([a-zA-Z]+)=([\s\S]+)$/.exec(selector || "");
    const kind = m && LOCATOR_KINDS.includes(m[1].toLowerCase()) ? m[1].toLowerCase() : "";
    if (kind === "frame") {
      const fs = splitFrame(selector);
      if (!fs) return nearMiss(m[2], root);
      const frames = resolveAll(fs.frame, root);
      if (!frames.length) {
        const why = nearMiss(fs.frame, root);
        return `no frame matched ${fs.frame}` + (why ? ` (${why})` : "");
      }
      const f = frames.find((e) => frameDocOf(e));
      if (!f) {
        return frames[0].tagName === "IFRAME" || frames[0].tagName === "FRAME"
          ? `the frame ${describe(frames[0])} is cross-origin — not reachable from this page`
          : `frame=${fs.frame} matched ${describe(frames[0])}, which is not an <iframe>`;
      }
      return nearMiss(fs.inner, frameDocOf(f));
    }
    if (kind === "ref") return "that ref is gone (the page changed since the snapshot) — take a new page_snapshot";
    if (!kind) {
      try {
        root.querySelector(selector);
      } catch (e) {
        return `not a valid CSS selector (${String(e && e.message ? e.message : e).slice(0, 120)}) — for text use text=…`;
      }
    }
    const needles = needlesOf(selector).map((n) => String(n).trim().toLowerCase()).filter((n) => n.length >= 2);
    if (!needles.length) return "";
    const hits = [];
    for (const el of deepAll(root)) {
      if (!el.getAttribute || !isVisible(el)) continue;
      const tag = el.tagName.toLowerCase();
      if (tag === "html" || tag === "body" || tag === "head" || tag === "script" || tag === "style") continue;
      const own = ["id", "class", "name", "placeholder", "aria-label", "title", "data-testid", "value"]
        .map((a) => el.getAttribute(a) || "").join(" ");
      // A big container's text contains everything; only small ones count.
      const text = (el.textContent || "").length <= 200 ? accessibleName(el) : "";
      const hay = (own + " " + text).toLowerCase();
      if (needles.some((n) => hay.includes(n))) hits.push(el);
    }
    const interactive = (el) => /^(a|button|input|select|textarea|label|summary)$/i.test(el.tagName) || roleOf(el) !== "";
    const best = innermost(hits)
      .map((el, i) => ({ el, i, w: interactive(el) ? 0 : 1 }))
      .sort((a, b) => a.w - b.w || a.i - b.i)
      .slice(0, 3)
      .map((x) => x.el);
    if (!best.length) return "";
    return "similar elements: " + best.map(describe).join("; ");
  }

  globalThis.__aglink = {
    __v: 4,
    resolve,
    resolveAll,
    resolveInfo,
    nearMiss,
    describe,
    contains,
    frameDocOf,
    frameDocs,
    splitFrame,
    deepQueryAll,
    deepAll,
    isVisible,
    roleOf,
    accessibleName,
  };
})();
