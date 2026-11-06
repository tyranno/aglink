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
