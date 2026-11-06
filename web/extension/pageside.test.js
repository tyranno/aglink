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
