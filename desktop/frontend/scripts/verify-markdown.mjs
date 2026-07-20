// Same DOM stub and expectations as aglink-chat's markdown.test.mjs — this
// port must stay behaviorally identical since it's the same algorithm.

// Minimal DOM stub: only what the renderer touches. Serialisation escapes text
// the same way a browser would, so any injection shows up as literal markup.
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
function makeNode(tag) {
  return {
    tag, children: [], attrs: {}, _text: null,
    set textContent(v) { this._text = String(v); this.children = []; },
    get textContent() { return this._text; },
    setAttribute(k, v) { this.attrs[k] = v; },
    appendChild(c) { this.children.push(c); return c; },
  };
}
const doc = {
  createElement: (t) => makeNode(t),
  createTextNode: (t) => ({ tag: "#text", _text: String(t) }),
  createDocumentFragment: () => makeNode("#frag"),
};
function ser(n) {
  if (n.tag === "#text") return esc(n._text);
  const inner = n._text !== null && n._text !== undefined ? esc(n._text) : n.children.map(ser).join("");
  if (n.tag === "#frag") return inner;
  const a = Object.entries(n.attrs).map(([k, v]) => ` ${k}="${esc(v)}"`).join("");
  return `<${n.tag}${a}>${inner}</${n.tag}>`;
}

const { renderMarkdown } = await import("../src/markdown.js");
const render = (s) => ser(renderMarkdown(s, doc));

let fail = 0;
function check(name, got, want) {
  const ok = got === want;
  if (!ok) fail++;
  console.log(`${ok ? "PASS" : "FAIL"}  ${name}`);
  if (!ok) { console.log("   got : " + got); console.log("   want: " + want); }
}

check("bold+italic+code",
  render("a **b** and *i* and `c`"),
  "<p>a <strong>b</strong> and <em>i</em> and <code>c</code></p>");

check("fenced code keeps text literal",
  render("```js\nconst a = 1 < 2;\n```"),
  "<pre><code>const a = 1 &lt; 2;</code></pre>");

check("heading + list",
  render("## Title\n- one\n- two"),
  "<h2>Title</h2><ul><li>one</li><li>two</li></ul>");

check("ordered list",
  render("1. first\n2. second"),
  "<ol><li>first</li><li>second</li></ol>");

check("blockquote",
  render("> quoted"),
  "<blockquote><p>quoted</p></blockquote>");

check("safe link",
  render("[go](https://example.com)"),
  '<p><a href="https://example.com" target="_blank" rel="noopener noreferrer">go</a></p>');

// The two that matter: nothing executable can reach the DOM.
check("javascript: link is NOT a link",
  render("[x](javascript:alert(1))"),
  "<p>[x](javascript:alert(1))</p>");

check("raw HTML is escaped text",
  render('<img src=x onerror="alert(1)">'),
  '<p>&lt;img src=x onerror=&quot;alert(1)&quot;&gt;</p>');

check("html inside code fence is escaped",
  render("```\n<script>alert(1)</script>\n```"),
  "<pre><code>&lt;script&gt;alert(1)&lt;/script&gt;</code></pre>");

check("hard break inside paragraph",
  render("line1\nline2"),
  "<p>line1<br></br>line2</p>");

check("unterminated fence does not swallow silently",
  render("```\nabc"),
  "<pre><code>abc</code></pre>");

check("hr", render("---"), "<hr></hr>");

check("pipe table",
  render("| | 지금 | 목표 |\n|---|---|---|\n| 저장소 | 5개 | 1개 (모노레포) |\n| exe | 4개 (39MB) | 2개 (~22MB) |\n| 빌드 | 5곳 순회 | go build ./cmd/... + desktop만 task |"),
  "<table><thead><tr><th></th><th>지금</th><th>목표</th></tr></thead><tbody><tr><td>저장소</td><td>5개</td><td>1개 (모노레포)</td></tr><tr><td>exe</td><td>4개 (39MB)</td><td>2개 (~22MB)</td></tr><tr><td>빌드</td><td>5곳 순회</td><td>go build ./cmd/... + desktop만 task</td></tr></tbody></table>");

console.log(fail === 0 ? "\nALL PASS ✅" : `\n${fail} FAILED ❌`);
process.exitCode = fail ? 1 : 0;
