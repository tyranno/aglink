// Minimal DOM stand-in for testing the page-side scripts (aglink-inject.js,
// page-actions.js) under node without a browser. Supports only what those
// scripts touch: element trees, attributes, text, simple selectors
// (tag, .class, [attr], [attr="v"], comma lists, descendant combinator),
// and a rect that is zero for elements marked hidden.
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

class Text {
  constructor(t) {
    this.nodeType = 3;
    this.textContent = t;
  }
}

class El {
  constructor(tag, attrs = {}, children = []) {
    this.nodeType = 1;
    this.tagName = tag.toUpperCase();
    this.attrs = new Map(Object.entries(attrs));
    this.childNodes = [];
    this.parentNode = null;
    this.hidden = false;
    this.value = attrs.value || "";
    for (const c of children) this.append(c);
  }
  append(c) {
    if (typeof c === "string") c = new Text(c);
    c.parentNode = this;
    this.childNodes.push(c);
    return c;
  }
  remove() {
    const p = this.parentNode;
    if (p) p.childNodes = p.childNodes.filter((n) => n !== this);
    this.parentNode = null;
  }
  getAttribute(n) { return this.attrs.has(n) ? this.attrs.get(n) : null; }
  setAttribute(n, v) { this.attrs.set(n, String(v)); }
  removeAttribute(n) { this.attrs.delete(n); }
  hasAttribute(n) { return this.attrs.has(n); }
  get children() { return this.childNodes.filter((n) => n.nodeType === 1); }
  get textContent() { return this.childNodes.map((n) => n.textContent).join(""); }
  get innerText() { return this.textContent; }
  get outerHTML() { return `<${this.tagName.toLowerCase()}>${this.textContent}</${this.tagName.toLowerCase()}>`; }
  getBoundingClientRect() {
    const h = this.hidden || (this.parentNode && this.parentNode.getBoundingClientRect && this.parentNode.getBoundingClientRect().width === 0);
    return h ? { left: 0, top: 0, width: 0, height: 0 } : { left: 10, top: 20, width: 100, height: 30 };
  }
  scrollIntoView() {}
  descendants() {
    const out = [];
    const walk = (n) => { for (const c of n.children) { out.push(c); walk(c); } };
    walk(this);
    return out;
  }
  querySelectorAll(sel) { return this.descendants().filter((el) => matches(el, sel, this)); }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
}

function matchSimple(el, part) {
  if (part === "*") return true;
  const m = /^([a-zA-Z0-9]+)?((?:\[[^\]]+\])*)((?:\.[\w-]+)*)$/.exec(part);
  if (!m) throw new Error("fakedom: unsupported selector " + part);
  if (m[1] && el.tagName !== m[1].toUpperCase()) return false;
  for (const a of m[2].match(/\[[^\]]+\]/g) || []) {
    const am = /^\[([\w-]+)(?:="([^"]*)")?\]$/.exec(a);
    if (!am) throw new Error("fakedom: unsupported attribute selector " + a);
    if (!el.hasAttribute(am[1])) return false;
    if (am[2] !== undefined && el.getAttribute(am[1]) !== am[2]) return false;
  }
  for (const c of (m[3].match(/\.[\w-]+/g) || [])) {
    if (!(el.getAttribute("class") || "").split(/\s+/).includes(c.slice(1))) return false;
  }
  return true;
}

function matches(el, sel, scope) {
  return sel.split(",").some((alt) => {
    const parts = alt.trim().split(/\s+/);
    if (!matchSimple(el, parts[parts.length - 1])) return false;
    let node = el.parentNode;
    for (let i = parts.length - 2; i >= 0; i--) {
      while (node && node !== scope && !(node.nodeType === 1 && matchSimple(node, parts[i]))) node = node.parentNode;
      if (!node || node === scope) return false;
      node = node.parentNode;
    }
    return true;
  });
}

const h = (tag, attrs, ...children) => new El(tag, attrs || {}, children.flat());

// loadPage builds a sandbox whose document body holds the given children and
// runs the shared page scripts in it, returning the sandbox.
function loadPage(...children) {
  const body = h("body", {}, ...children);
  const doc = h("html", {}, body);
  doc.body = body;
  doc.documentElement = doc;
  doc.getElementById = (id) => doc.querySelector(`[id="${id}"]`);
  const style = () => ({ display: "", visibility: "" });
  const sb = { Node: { TEXT_NODE: 3, ELEMENT_NODE: 1 }, getComputedStyle: style, JSON, Math, Array, Set, Map, String, Object };
  sb.window = sb;
  sb.document = doc;
  doc.defaultView = sb;
  const setOwner = (n) => { n.ownerDocument = doc; for (const c of n.children || []) setOwner(c); };
  setOwner(doc);
  sb.adopt = setOwner;
  vm.createContext(sb);
  for (const f of ["aglink-inject.js", "page-actions.js"]) {
    vm.runInContext(fs.readFileSync(path.join(__dirname, f), "utf8"), sb);
  }
  return sb;
}

module.exports = { El, h, loadPage };
