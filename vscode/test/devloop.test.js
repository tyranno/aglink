"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { makeVSCode, Uri, Range, Location } = require("./fakevscode");
const { createMethods } = require("../methods");

const noTerminals = { list: () => "(no terminals)", run: async () => "", read: () => "", count: () => 0 };
const base = new Uri("vscode-remote", "ssh-remote+host1", "/home/u/backend");
const A = Uri.joinPath(base, "a.go");
const B = Uri.joinPath(base, "b.go");
const SRC = "package main\n\nfunc main() {\n\tfoo(1)\n\tfoo(2)\n}\n";

function world(extra = {}) {
  const w = makeVSCode({
    remote: "host1",
    files: { [A.toString()]: SRC, [B.toString()]: "package main\n\nfunc foo(n int) {}\n" },
    ...extra,
  });
  w.m = createMethods(w.vscode, noTerminals, { timeoutUnitMs: 1 });
  w.text = (uri) => w.openDocs.find((d) => d.uri.toString() === uri.toString()).text;
  return w;
}

// ---- edit / save / format ---------------------------------------------------

test("edit replaces exact text, saves, and shows a diff", async () => {
  const w = world();
  const out = await w.m.edit({ path: "a.go", old_text: "\tfoo(1)", new_text: "\tfoo(10)\n\tbar()" });
  assert.strictEqual(out, "ok: edited /home/u/backend/a.go:4-5 (-1 +2 lines), saved\n- \tfoo(1)\n+ \tfoo(10)\n+ \tbar()");
  assert.strictEqual(w.text(A), "package main\n\nfunc main() {\n\tfoo(10)\n\tbar()\n\tfoo(2)\n}\n");
  assert.deepStrictEqual(w.calls.saved, [A.toString()]);
});

test("edit refuses missing or ambiguous text; occurrence picks one", async () => {
  const w = world();
  await assert.rejects(w.m.edit({ path: "a.go", old_text: "nope", new_text: "x" }), /not found/);
  await assert.rejects(w.m.edit({ path: "a.go", old_text: "foo(", new_text: "x(" }), /occurs 2 times \(lines 4, 5\)/);
  await assert.rejects(w.m.edit({ path: "a.go", old_text: "foo(", new_text: "x(", occurrence: 3 }), /out of range/);
  await w.m.edit({ path: "a.go", old_text: "foo(", new_text: "bar(", occurrence: 2, save: "false" });
  assert.match(w.text(A), /\tfoo\(1\)\n\tbar\(2\)/);
  assert.deepStrictEqual(w.calls.saved, []);
});

test("edit by line range replaces and deletes whole lines", async () => {
  const w = world();
  await w.m.edit({ path: "a.go", start_line: 4, end_line: 5, new_text: "\tbaz()\n" });
  assert.strictEqual(w.text(A), "package main\n\nfunc main() {\n\tbaz()\n}\n");
  const out = await w.m.edit({ path: "a.go", start_line: 4 });
  assert.match(out, /^ok: edited \/home\/u\/backend\/a\.go:4-4 \(-1 \+0 lines\), saved\n- \tbaz\(\)$/);
  assert.strictEqual(w.text(A), "package main\n\nfunc main() {\n}\n");
  await assert.rejects(w.m.edit({ path: "a.go", start_line: 9 }), /outside 1-5/);
  await assert.rejects(w.m.edit({ path: "a.go" }), /needs either old_text/);
});

test("edit keeps CRLF line endings", async () => {
  const f = Uri.joinPath(base, "w.txt");
  const w = world({ files: { [f.toString()]: "one\r\ntwo\r\nthree\r\n" } });
  await w.m.edit({ path: "w.txt", old_text: "one\ntwo", new_text: "uno\ndos" });
  assert.strictEqual(w.text(f), "uno\r\ndos\r\nthree\r\n");
});

test("save saves one file or all dirty ones", async () => {
  const w = world({ openDocs: [{ uri: A, text: SRC, dirty: true }, { uri: B, text: "x", dirty: true }] });
  assert.strictEqual(await w.m.save({ path: "a.go" }), "ok: saved /home/u/backend/a.go");
  assert.strictEqual(await w.m.save({ path: "a.go" }), "ok: /home/u/backend/a.go had no unsaved changes");
  assert.strictEqual(await w.m.save({}), "ok: saved 1 file\n/home/u/backend/b.go");
  assert.strictEqual(await w.m.save({}), "ok: nothing to save");
});

test("format applies the formatter's edits and saves", async () => {
  const w = world({
    commandResult: (id) => (id === "vscode.executeFormatDocumentProvider" ? [{ range: new Range(3, 0, 3, 1), newText: "    " }] : undefined),
  });
  assert.strictEqual(await w.m.format({ path: "a.go" }), "ok: formatted /home/u/backend/a.go (1 edit), saved");
  assert.match(w.text(A), /\n    foo\(1\)/);
  assert.strictEqual(w.calls.executed[0].args[1].tabSize, 4);
  const none = world();
  assert.strictEqual(await none.m.format({ path: "b.go" }), "ok: formatted /home/u/backend/b.go (no formatter answered for this file), saved");
});

// ---- language features ------------------------------------------------------

test("symbols searches the workspace or outlines one file", async () => {
  const w = world({
    commandResult: (id, args) => {
      if (id === "vscode.executeWorkspaceSymbolProvider") {
        return [{ name: "foo", kind: 11, containerName: "main", location: new Location(B, new Range(2, 5, 2, 8)) }];
      }
      if (id === "vscode.executeDocumentSymbolProvider") {
        return [{ name: "Server", kind: 22, range: new Range(0, 0, 9, 0), selectionRange: new Range(1, 5, 1, 11),
          children: [{ name: "Start", kind: 5, detail: "func()", range: new Range(3, 0, 5, 0), selectionRange: new Range(3, 5, 3, 10) }] }];
      }
    },
  });
  assert.strictEqual(await w.m.symbols({ query: "foo" }), "function foo (main) /home/u/backend/b.go:3");
  assert.strictEqual(await w.m.symbols({ path: "a.go" }), "/home/u/backend/a.go\nstruct Server :2\n  method Start func() :4");
  assert.strictEqual(await w.m.symbols({ path: "a.go", query: "sta" }), "/home/u/backend/a.go\nmethod Start func() :4");
  await assert.rejects(w.m.symbols({}), /needs 'query'/);
});

test("definition and references resolve a symbol on a line and show line text", async () => {
  const w = world({
    commandResult: (id, args) => {
      w.lastPos = args[1];
      if (id === "vscode.executeDefinitionProvider") return [{ targetUri: B, targetRange: new Range(2, 0, 2, 20), targetSelectionRange: new Range(2, 5, 2, 8) }];
      if (id === "vscode.executeReferenceProvider") return [new Location(A, new Range(3, 1, 3, 4)), new Location(A, new Range(4, 1, 4, 4))];
    },
  });
  assert.strictEqual(await w.m.definition({ path: "a.go", line: 4, symbol: "foo" }), "/home/u/backend/b.go:3:6  func foo(n int) {}");
  assert.deepStrictEqual([w.lastPos.line, w.lastPos.character], [3, 1]);
  assert.strictEqual(await w.m.references({ path: "a.go", line: 5, column: 3 }),
    "2 references\n/home/u/backend/a.go:4:2  foo(1)\n/home/u/backend/a.go:5:2  foo(2)");
  assert.deepStrictEqual([w.lastPos.line, w.lastPos.character], [4, 2]);
  await assert.rejects(w.m.definition({ path: "a.go", line: 4, symbol: "bar" }), /not on line 4/);
  await assert.rejects(w.m.definition({ path: "a.go" }), /pass 'line'/);
  const empty = world();
  assert.strictEqual(await empty.m.references({ path: "a.go", line: 4 }), "no references found at /home/u/backend/a.go:4:2");
});

test("hover joins the hover contents", async () => {
  const w = world({
    commandResult: (id) => (id === "vscode.executeHoverProvider" ? [{ contents: [{ value: "```go\nfunc foo(n int)\n```" }, "doc text"] }] : undefined),
  });
  assert.strictEqual(await w.m.hover({ path: "a.go", line: 4, symbol: "foo" }), "```go\nfunc foo(n int)\n```\n---\ndoc text");
  assert.strictEqual(await world().m.hover({ path: "a.go", line: 1 }), "no hover info at /home/u/backend/a.go:1:1");
});

// ---- tasks ------------------------------------------------------------------

const TASKS = [
  { name: "build", source: "Workspace", group: { id: "build" } },
  { name: "test", source: "Workspace", detail: "go test ./..." },
  { name: "test", source: "npm" },
  { name: "watch", source: "Workspace", isBackground: true },
];

test("tasks lists the window's tasks", async () => {
  const w = world({ tasks: TASKS });
  assert.strictEqual(await w.m.tasks({}), [
    "build [Workspace] (build)",
    "test [Workspace] — go test ./...",
    "test [npm]",
    "watch [Workspace] background",
  ].join("\n"));
  assert.strictEqual(await world().m.tasks({}), "(no tasks)");
});

test("task_run waits for the exit code", async () => {
  const w = world({ tasks: TASKS, runTask: (t, fire) => { fire.process(t.name === "build" ? 0 : 2); fire.task(); } });
  assert.match(await w.m.task_run({ name: "build" }), /^ok: task 'build' exited with code 0 after [\d.]+s\. Task output is not readable/);
  assert.match(await w.m.task_run({ name: "npm: test" }), /^failed: task 'test' exited with code 2/);
  await assert.rejects(w.m.task_run({ name: "test" }), /ambiguous — give one of: Workspace: test, npm: test/);
  await assert.rejects(w.m.task_run({ name: "deploy" }), /no task named "deploy"/);
});

test("task_run reports a task that outlives the timeout", async () => {
  const w = world({ tasks: TASKS });
  assert.match(await w.m.task_run({ name: "watch", timeoutSec: 20 }), /still running after 20s \(a background task/);
});

// ---- debug ------------------------------------------------------------------

test("debug starts, reports and stops a session", async () => {
  const w = world({ debugConfigs: ["Launch server"] });
  assert.strictEqual(await w.m.debug({ action: "status" }), "session: (none)\nbreakpoints: 0");
  assert.strictEqual(await w.m.debug({ action: "start", name: "Launch server" }), "ok: debugging Launch server [go]");
  assert.match(await w.m.debug({}), /^session: Launch server \[go\]/);
  assert.strictEqual(await w.m.debug({ action: "stop" }), "ok: stopped Launch server");
  await assert.rejects(w.m.debug({ action: "start", name: "nope" }), /could not start/);
  await w.m.debug({ action: "start" });
  assert.strictEqual(w.calls.executed.at(-1).id, "workbench.action.debug.start");
  await assert.rejects(w.m.debug({ action: "jump" }), /unknown debug action/);
});

test("debug adds, lists and removes breakpoints", async () => {
  const w = world();
  assert.strictEqual(await w.m.debug({ action: "breakpoints" }), "(no breakpoints)");
  assert.strictEqual(await w.m.debug({ action: "breakpoint_add", path: "a.go", line: 4, condition: "n > 1" }), "ok: breakpoint at /home/u/backend/a.go:4 if n > 1");
  await w.m.debug({ action: "breakpoint_add", path: "b.go", line: 3 });
  assert.strictEqual(await w.m.debug({ action: "breakpoints" }), "/home/u/backend/a.go:4 if n > 1\n/home/u/backend/b.go:3");
  assert.strictEqual(await w.m.debug({ action: "breakpoint_remove", path: "a.go", line: 4 }), "ok: removed 1 breakpoint");
  await assert.rejects(w.m.debug({ action: "breakpoint_remove", path: "a.go", line: 4 }), /no breakpoint/);
  assert.strictEqual(w.vscode.debug.breakpoints.length, 1);
});

// ---- git --------------------------------------------------------------------

function gitWorld() {
  const root = Uri.file("C:\\p\\aglink");
  const f = (p) => Uri.joinPath(root, p);
  const repo = {
    rootUri: root,
    state: {
      HEAD: { name: "main", commit: "abc", upstream: { remote: "origin", name: "main" }, ahead: 2, behind: 0 },
      indexChanges: [{ uri: f("web/a.go"), status: 0 }],
      workingTreeChanges: [{ uri: f("README.md"), status: 5 }, { uri: f("new.txt"), status: 7 }],
      mergeChanges: [],
    },
    diff: async (cached) => (cached ? "staged diff" : "x".repeat(40000)),
    diffWithHEAD: async (p) => `head diff ${p}`,
    diffIndexWithHEAD: async (p) => `index diff ${p}`,
    log: async ({ maxEntries }) => [
      { hash: "0123456789abcdef", message: "web: add tools\n\nbody", authorName: "Doowon", authorDate: new Date("2026-11-20T10:00:00Z") },
    ].slice(0, maxEntries),
  };
  const api = { repositories: [repo], getRepository: () => repo };
  const w = makeVSCode({ folder: "C:\\p\\aglink", extensions: { "vscode.git": { isActive: true, exports: { getAPI: () => api } } } });
  return createMethods(w.vscode, noTerminals);
}

test("git status lists branch and changes", async () => {
  assert.strictEqual(await gitWorld().git({}), [
    "repo: C:\\p\\aglink",
    "branch: main → origin/main, ahead 2, behind 0",
    "staged (1):",
    "  M web/a.go",
    "unstaged (1):",
    "  M README.md",
    "untracked (1):",
    "  new.txt",
  ].join("\n"));
});

test("git diff and log", async () => {
  const m = gitWorld();
  assert.strictEqual(await m.git({ action: "diff", staged: "true" }), "staged diff");
  assert.match(await m.git({ action: "diff" }), /\n… 10000 more chars — pass 'path' to narrow it$/);
  assert.strictEqual(await m.git({ action: "diff", path: "web/a.go" }), "head diff C:\\p\\aglink\\web\\a.go");
  assert.strictEqual(await m.git({ action: "diff", path: "web/a.go", staged: "true" }), "index diff C:\\p\\aglink\\web\\a.go");
  assert.strictEqual(await m.git({ action: "log", max: 5 }), "01234567 2026-11-20 Doowon: web: add tools");
  await assert.rejects(m.git({ action: "commit" }), /read-only/);
});

test("git in a Remote-SSH window says why it cannot", async () => {
  await assert.rejects(world().m.git({}), /Remote-SSH window the Git extension runs on the remote/);
});

// ---- tests ------------------------------------------------------------------

test("tests runs the testing command for the action", async () => {
  const w = world();
  assert.match(await w.m.tests({}), /^ok: ran testing\.runAll \(the run finished\)\. .*vscode_problems/);
  await w.m.tests({ action: "cursor", path: "a.go", line: 4 });
  assert.strictEqual(w.calls.executed.at(-1).id, "testing.runAtCursor");
  assert.strictEqual(w.calls.shown[0].options.selection.start.line, 3);
  await assert.rejects(w.m.tests({ action: "everything" }), /unknown tests action/);

  const slow = world({ commandResult: () => new Promise(() => {}) });
  assert.match(await slow.m.tests({ action: "failed", timeoutSec: 5 }), /testing\.reRunFailTests \(still running after 5s\)/);
});
