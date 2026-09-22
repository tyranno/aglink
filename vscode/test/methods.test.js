"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { makeVSCode, Uri } = require("./fakevscode");
const { createMethods } = require("../methods");

const noTerminals = { list: () => "(no terminals)", run: async () => "", read: () => "", count: () => 0 };

function remoteWindow(extra = {}) {
  const base = new Uri("vscode-remote", "ssh-remote+host1", "/home/u/backend");
  const a = Uri.joinPath(base, "src/a.go");
  return makeVSCode({
    remote: "host1",
    folder: "/home/u/backend",
    files: { [Uri.joinPath(base, "README.md").toString()]: "# backend\nline two\n" },
    openDocs: [{ uri: a, text: "package main\n\nfunc main() {\n\tprintln(\"unsaved\")\n}", dirty: true }],
    active: { uri: a, line: 3 },
    tabGroups: [{ isActive: true, tabs: [
      { label: "a.go", input: { uri: a }, isActive: true, isDirty: true },
      { label: "README.md", input: { uri: Uri.joinPath(base, "README.md") }, isActive: false, isDirty: false },
      { label: "Settings", input: {}, isActive: false, isDirty: false }, // not a file tab
    ] }],
    ...extra,
  });
}

test("workspace summarizes a Remote-SSH window", async () => {
  const { vscode } = remoteWindow();
  const out = await createMethods(vscode, noTerminals).workspace({});
  assert.strictEqual(out, [
    "name: backend",
    "remote: host1",
    "folders: /home/u/backend",
    "active: /home/u/backend/src/a.go:4",
    "editors: 2",
    "terminals: 0",
  ].join("\n"));
});

test("editors lists file tabs with active and unsaved marks", async () => {
  const { vscode } = remoteWindow();
  const out = await createMethods(vscode, noTerminals).editors({});
  assert.strictEqual(out, "[active] [modified] /home/u/backend/src/a.go\n/home/u/backend/README.md");
});

test("read returns unsaved editor content with line numbers", async () => {
  const { vscode } = remoteWindow();
  const out = await createMethods(vscode, noTerminals).read({ path: "src/a.go" });
  const lines = out.split("\n");
  assert.strictEqual(lines[0], "/home/u/backend/src/a.go (lines 1-5 of 5, unsaved changes)");
  assert.strictEqual(lines[4], '     4|\tprintln("unsaved")');
});

test("read resolves a relative path against the remote folder and pages by line", async () => {
  const { vscode, calls } = remoteWindow();
  const out = await createMethods(vscode, noTerminals).read({ path: "README.md", startLine: 2, endLine: 2 });
  assert.strictEqual(calls.opened[0], "vscode-remote://ssh-remote+host1/home/u/backend/README.md");
  assert.strictEqual(out, "/home/u/backend/README.md (lines 2-2 of 3)\n     2|line two");
});

test("read with no path uses the active editor; a missing file is an error", async () => {
  const { vscode } = remoteWindow();
  const m = createMethods(vscode, noTerminals);
  assert.match(await m.read({}), /^\/home\/u\/backend\/src\/a\.go /);
  await assert.rejects(m.read({ path: "nope.txt" }), /cannot open/);
});

test("read truncates very long files", async () => {
  const base = new Uri("vscode-remote", "ssh-remote+host1", "/home/u/backend");
  const big = Array.from({ length: 5000 }, (_, i) => `line ${i + 1}`).join("\n");
  const { vscode } = makeVSCode({ remote: "host1", files: { [Uri.joinPath(base, "big.txt").toString()]: big } });
  const out = await createMethods(vscode, noTerminals).read({ path: "big.txt" });
  assert.match(out.split("\n")[0], /lines 1-2000 of 5000/);
  assert.match(out, /more lines — pass startLine=2001/);
});

test("open shows the file and moves to the line", async () => {
  const { vscode, calls } = remoteWindow();
  const out = await createMethods(vscode, noTerminals).open({ path: "README.md", line: 2 });
  assert.strictEqual(out, "ok: opened /home/u/backend/README.md:2");
  assert.strictEqual(calls.shown[0].options.selection.start.line, 1);
  await assert.rejects(createMethods(vscode, noTerminals).open({}), /requires 'path'/);
});

test("problems lists diagnostics sorted, one line each", async () => {
  const base = new Uri("vscode-remote", "ssh-remote+host1", "/home/u/backend");
  const a = Uri.joinPath(base, "a.go");
  const b = Uri.joinPath(base, "b.go");
  const diag = (line, col, sev, msg, source) => ({ range: { start: { line, character: col } }, severity: sev, message: msg, source });
  const { vscode } = makeVSCode({
    remote: "host1",
    diagnostics: [
      [b, [diag(9, 0, 1, "unused variable x", "go")]],
      [a, [diag(2, 4, 0, "undefined: foo", "compiler"), diag(0, 0, 2, "consider gofmt")]],
    ],
  });
  const m = createMethods(vscode, noTerminals);
  assert.strictEqual(await m.problems({}), [
    "error /home/u/backend/a.go:3:5 undefined: foo [compiler]",
    "info /home/u/backend/a.go:1:1 consider gofmt",
    "warning /home/u/backend/b.go:10:1 unused variable x [go]",
  ].join("\n"));
  assert.strictEqual(await m.problems({ path: "b.go" }), "warning /home/u/backend/b.go:10:1 unused variable x [go]");
  const empty = makeVSCode({ remote: "host1" });
  assert.strictEqual(await createMethods(empty.vscode, noTerminals).problems({}), "no problems");
});

test("command runs by id with JSON args and reports a result", async () => {
  const { vscode, calls } = makeVSCode({ commandResult: () => ({ saved: 2 }) });
  const out = await createMethods(vscode, noTerminals).command({ command: "workbench.action.files.saveAll", args: '["x", 1]' });
  assert.strictEqual(out, 'ok: ran workbench.action.files.saveAll\n{"saved":2}');
  assert.deepStrictEqual(calls.executed[0].args, ["x", 1]);
  await assert.rejects(createMethods(vscode, noTerminals).command({ command: "x", args: "not json" }), /args must be a JSON array/);
});

test("a local window reads Windows paths", async () => {
  const f = Uri.file("C:\\p\\aglink\\go.mod");
  const { vscode } = makeVSCode({ folder: "C:\\p\\aglink", name: "aglink", files: { [f.toString()]: "module x\n" } });
  const m = createMethods(vscode, noTerminals);
  assert.match(await m.workspace({}), /remote: \(local\)\nfolders: C:\\p\\aglink/);
  assert.match(await m.read({ path: "go.mod" }), /^C:\\p\\aglink\\go\.mod \(lines 1-2 of 2\)/);
  assert.match(await m.read({ path: "C:\\p\\aglink\\go.mod" }), /module x/);
});
