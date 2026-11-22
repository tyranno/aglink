// Tests for the page-side scripts against the fake DOM in fakedom.js.
const test = require("node:test");
const assert = require("node:assert");
const { h, loadPage } = require("./fakedom.js");

const refOf = (snap, name) => {
  const line = snap.split("\n").find((l) => l.includes(`"${name}"`));
  return line && /ref=(e\d+)/.exec(line)[1];
};

test("snapshot keeps refs stable across snapshots and numbers only new controls", () => {
  const list = h("div", {}, h("button", {}, "저장"), h("button", {}, "취소"));
  const sb = loadPage(list);
  const P = sb.__aglinkPage;
  const first = P.snapshot(100);
  assert.strictEqual(refOf(first, "저장"), "e1");
  assert.strictEqual(refOf(first, "취소"), "e2");

  // A new control appears before the old ones: they keep their numbers.
  const extra = h("button", {}, "새로고침");
  list.childNodes.unshift(extra);
  extra.parentNode = list;
  sb.adopt(extra);
  const second = P.snapshot(100);
  assert.strictEqual(refOf(second, "저장"), "e1");
  assert.strictEqual(refOf(second, "취소"), "e2");
  assert.strictEqual(refOf(second, "새로고침"), "e3");
});

test("a cloned ref is renumbered and a vanished element loses its mark", () => {
  const a = h("button", {}, "하나");
  const b = h("button", {}, "둘");
  const sb = loadPage(a, b);
  const P = sb.__aglinkPage;
  P.snapshot(100);
  b.setAttribute("data-aglink-ref", "e1"); // as if cloned from a
  const snap = P.snapshot(100);
  assert.strictEqual(refOf(snap, "하나"), "e1");
  assert.strictEqual(refOf(snap, "둘"), "e3");
  a.hidden = true;
  P.snapshot(100);
  assert.strictEqual(a.getAttribute("data-aglink-ref"), null);
});

test("ref= re-finds an element re-rendered with the same role and name", () => {
  const box = h("div", {}, h("button", {}, "보내기"), h("a", { href: "/x" }, "보내기"));
  const sb = loadPage(box);
  const P = sb.__aglinkPage;
  const snap = P.snapshot(100);
  const id = refOf(snap, "보내기");
  // The framework replaces the button with a fresh node.
  box.childNodes[0].remove();
  const fresh = h("button", {}, "보내기");
  box.childNodes.unshift(fresh);
  fresh.parentNode = box;
  sb.adopt(fresh);
  const got = sb.__aglink.resolve("ref=" + id);
  assert.strictEqual(got, fresh);
  assert.strictEqual(fresh.getAttribute("data-aglink-ref"), id);
});

test("ref= does not guess when the role and name are ambiguous", () => {
  const box = h("div", {}, h("button", {}, "삭제"));
  const sb = loadPage(box);
  const id = refOf(sb.__aglinkPage.snapshot(100), "삭제");
  box.childNodes[0].remove();
  for (let i = 0; i < 2; i++) {
    const b = h("button", {}, "삭제");
    box.append(b);
    sb.adopt(b);
  }
  assert.strictEqual(sb.__aglink.resolve("ref=" + id), null);
});

test("query_all on tables returns rows keyed by the header cells", () => {
  const table = h("table", {},
    h("thead", {}, h("tr", {}, h("th", {}, "이름"), h("th", {}, " 나이 "), h("th", {}, ""))),
    h("tbody", {},
      h("tr", {}, h("td", {}, "홍길동"), h("td", {}, "30"), h("td", {}, "x")),
      h("tr", {}, h("td", {}, "김철수"), h("td", {}, "41"))));
  const sb = loadPage(table);
  const r = sb.__aglinkPage.queryAll("table", [], 200);
  assert.deepStrictEqual(Array.from(r.lines), [
    JSON.stringify({ 이름: "홍길동", 나이: "30", c3: "x" }),
    JSON.stringify({ 이름: "김철수", 나이: "41" }),
  ]);
  const capped = sb.__aglinkPage.queryAll("table", [], 1);
  assert.strictEqual(capped.lines.length, 2);
  assert.match(capped.lines[1], /stopped at 1 rows/);
});

test("a table without thead uses a first row of th, else c1, c2", () => {
  const withTh = h("table", {}, h("tr", {}, h("th", {}, "A")), h("tr", {}, h("td", {}, "1")));
  const plain = h("table", {}, h("tr", {}, h("td", {}, "x"), h("td", {}, "y")));
  const sb = loadPage(withTh, plain);
  const r = sb.__aglinkPage.queryAll("table", [], 200);
  assert.deepStrictEqual(Array.from(r.lines), ["table 0:", '{"A":"1"}', "table 1:", '{"c1":"x","c2":"y"}']);
});

test("query_all still lists non-table matches as before", () => {
  const sb = loadPage(h("a", { href: "/1" }, "First"));
  const r = sb.__aglinkPage.queryAll("a", [], 200);
  assert.ok(Array.isArray(r));
  assert.strictEqual(r[0].text, "First");
});

test("extract reads named fields per element, in the caller's order", () => {
  const card = (name, href, price) => h("div", { class: "card", "data-id": name },
    h("h3", {}, " ", name, " "), href ? h("a", { href }, "more") : h("span", {}, "-"), h("span", { class: "price" }, price));
  const sb = loadPage(card("사과", "/a", "1,000원"), card("배", null, "2,000원"), h("input", { class: "card", value: "v" }));
  const fields = JSON.stringify({ name: "h3", url: "a@href", price: ".price", id: "@data-id", self: "" });
  const r = sb.__aglinkPage.extract(".card", fields, 2);
  assert.strictEqual(r.total, 3);
  assert.deepStrictEqual(Array.from(r.lines), [
    '{"name":"사과","url":"/a","price":"1,000원","id":"사과","self":"사과 more1,000원"}',
    '{"name":"배","url":null,"price":"2,000원","id":"배","self":"배 -2,000원"}',
  ]);
  const input = sb.__aglinkPage.extract("input", JSON.stringify({ v: "" }), 5);
  assert.deepStrictEqual(Array.from(input.lines), ['{"v":"v"}']);
});

test("extract rejects fields that are not a JSON object", () => {
  const sb = loadPage(h("div", {}));
  assert.match(sb.__aglinkPage.extract("div", "nope", 5).error, /JSON object/);
  assert.match(sb.__aglinkPage.extract("div", "[1]", 5).error, /JSON object/);
});

test("network-capture.js counts fetch and XHR in flight", async () => {
  const fs = require("node:fs");
  const path = require("node:path");
  const vm = require("node:vm");
  let finish;
  const fetchDone = new Promise((r) => (finish = r));
  class XHR {
    open() {}
    setRequestHeader() {}
    addEventListener(type, fn) { this.onend = fn; }
    send() {}
  }
  const sb = {
    Date, Map, String, JSON, Promise, Headers: class {},
    fetch: () => fetchDone,
    XMLHttpRequest: XHR,
  };
  sb.window = sb;
  vm.createContext(sb);
  vm.runInContext(fs.readFileSync(path.join(__dirname, "network-capture.js"), "utf8"), sb);
  const st = sb.__aglinkNetState;
  const p = sb.fetch("/api/a");
  const x = new sb.XMLHttpRequest();
  x.open("GET", "/api/b");
  x.send();
  assert.deepStrictEqual(Array.from(st.pending.values()), ["/api/a", "/api/b"]);
  x.onend();
  assert.strictEqual(st.pending.size, 1);
  finish({ status: 200, headers: { get: () => "application/json" }, clone: () => ({ text: async () => "{}" }) });
  await p;
  await new Promise((r) => setTimeout(r, 10));
  assert.strictEqual(st.pending.size, 0);
});

// ---- honest results: ambiguity notes, near misses ----------------------------

test("text= counts distinct elements, not every ancestor, and notes an ambiguous pick", () => {
  const sb = loadPage(h("div", {}, h("button", { id: "a" }, "Save")), h("div", {}, h("button", { id: "b" }, "Save")));
  const info = sb.__aglink.resolveInfo("text=Save");
  assert.strictEqual(info.count, 2, "the two buttons, not their divs, <body> and <html>");
  assert.strictEqual(info.el.getAttribute("id"), "a");
  assert.match(info.note, /^matched 2 elements, used the first: 1\) button#a "Save" 2\) button#b "Save"/);
  const one = sb.__aglink.resolveInfo("#b");
  assert.strictEqual(one.note, "");
  const r = sb.__aglinkPage.getValue("button");
  assert.match(r.note, /matched 2 elements/);
});

test("a selector that matches nothing names what nearly matched", () => {
  const sb = loadPage(h("button", { id: "save-draft" }, "Save draft"), h("a", { href: "/x" }, "Help"));
  const r = sb.__aglinkPage.getAttribute('text="Save"', "id");
  assert.strictEqual(r.found, false);
  assert.match(r.hint, /^similar elements: button#save-draft "Save draft"/);
  assert.match(sb.__aglink.nearMiss("#save-draft-btn") || "", /^$/, "no needle matches: nothing to say");
  assert.match(sb.__aglink.nearMiss("ref=e99"), /take a new page_snapshot/);
});

// ---- click actionability ------------------------------------------------------

test("click waits for a disabled button and then says why it gave up", async () => {
  const btn = h("button", { id: "pay" }, "Pay");
  btn.disabled = true;
  const sb = loadPage(btn);
  const r = await sb.__aglinkPage.click("#pay", "left", { timeout: 150 });
  assert.strictEqual(btn.clicks, undefined, "a disabled button is not clicked");
  assert.match(r.blocked, /^cannot click button#pay "Pay" matched by #pay: it is disabled \(waited 150ms\)/);
  // It is clicked as soon as it becomes enabled within the wait.
  setTimeout(() => { btn.disabled = false; }, 120);
  const ok = await sb.__aglinkPage.click("#pay", "left", { timeout: 1000 });
  assert.strictEqual(ok.found, true);
  assert.strictEqual(ok.blocked, undefined);
  assert.strictEqual(btn.clicks, 1);
  // force clicks at once, checks or not.
  btn.disabled = true;
  await sb.__aglinkPage.click("#pay", "left", { force: true });
  assert.strictEqual(btn.clicks, 2);
});

test("click reports what covers the element, and a hidden one as not visible", async () => {
  const btn = h("button", { id: "go" }, "Go");
  const overlay = h("div", { class: "modal-backdrop" });
  const hidden = h("button", { id: "later" }, "Later");
  hidden.hidden = true;
  const sb = loadPage(btn, overlay, hidden);
  sb.innerWidth = 1000;
  sb.innerHeight = 800;
  sb.document.elementFromPoint = () => overlay;
  const r = await sb.__aglinkPage.click("#go", "left", { timeout: 50 });
  assert.match(r.blocked, /it is covered by div\.modal-backdrop/);
  sb.document.elementFromPoint = () => btn.childNodes[0].parentNode; // the button itself
  assert.strictEqual((await sb.__aglinkPage.click("#go", "left", { timeout: 50 })).blocked, undefined);
  const h2 = await sb.__aglinkPage.click("#later", "left", { timeout: 50 });
  assert.match(h2.blocked, /it is not visible/);
  const miss = await sb.__aglinkPage.click("#nope", "left", { timeout: 50 });
  assert.strictEqual(miss.found, false);
});

// ---- type modes -----------------------------------------------------------------

test("type into a contenteditable goes through insertText, append keeps the text", () => {
  const ed = h("div", { contenteditable: "true" }, "old");
  ed.isContentEditable = true;
  const input = h("input", { id: "q", value: "ab" });
  const sb = loadPage(ed, input);
  const cmds = [];
  sb.document.execCommand = (cmd, ui, v) => { cmds.push([cmd, v]); return true; };
  const r = sb.__aglinkPage.typeText("div", "new", "set");
  assert.strictEqual(r.via, "insertText");
  assert.deepStrictEqual(cmds, [["insertText", "new"]]);
  // An input appends through its value.
  sb.HTMLInputElement = undefined;
  sb.__aglinkPage.typeText("#q", "c", "append");
  assert.strictEqual(input.value, "abc");
  sb.__aglinkPage.typeText("#q", "z", "set");
  assert.strictEqual(input.value, "z");
  // An editor that refuses insertText falls back to setting the text.
  sb.document.execCommand = () => false;
  assert.strictEqual(sb.__aglinkPage.typeText("div", "x", "set").via, "textContent");
});

test("focusForKeys focuses the element for real typing", () => {
  const ta = h("textarea", { id: "m" });
  const sb = loadPage(ta);
  const r = sb.__aglinkPage.focusForKeys("#m");
  assert.strictEqual(r.found, true);
  assert.strictEqual(r.focused, true);
  assert.strictEqual(sb.document.activeElement, ta);
});

// ---- iframes --------------------------------------------------------------------

test("frame= reaches into a same-origin iframe; snapshot and page text include it", () => {
  const { makeDoc } = require("./fakedom.js");
  const frame = h("iframe", { src: "/inner.html", id: "f" });
  const sb = loadPage(h("h1", {}, "Top"), frame);
  const inner = makeDoc(sb, h("button", { id: "pay" }, "결제"));
  frame.contentDocument = inner;
  const el = sb.__aglink.resolve("frame=#f >> #pay");
  assert.strictEqual(el && el.getAttribute("id"), "pay");
  assert.strictEqual(sb.__aglink.resolve("#pay"), null, "the top document does not see inside");
  const snap = sb.__aglinkPage.snapshot(100);
  assert.match(snap, /- iframe "\/inner\.html" \[ref=e1\]:\n  - button "결제" \[ref=e2\]/);
  assert.strictEqual(sb.__aglink.resolve("ref=e2"), el, "refs inside a same-origin iframe resolve from the top");
  const text = sb.__aglinkPage.getPageText(null).text;
  assert.match(text, /\[iframe \/inner\.html\]\n결제/);
  // A cross-origin frame (no readable document) is named, and listed for the extension.
  frame.contentDocument = null;
  const x = sb.__aglinkPage.snapshot(100, { frames: true });
  assert.match(x.text, /iframe "\/inner\.html" \[ref=e1, cross-origin/);
  assert.strictEqual(x.xframes[0].ref, "e1");
  assert.match(sb.__aglink.nearMiss("frame=#f >> #pay"), /cross-origin/);
  const info = sb.__aglinkPage.frameInfo("#f");
  assert.strictEqual(info.isFrame, true);
  assert.strictEqual(info.sameOrigin, false);
});
