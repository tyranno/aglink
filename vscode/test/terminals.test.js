"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { createTerminals, stripAnsi } = require("../terminals");

// A controllable stand-in for VS Code's terminal + shell-integration API.
function emitter() {
  const subs = [];
  const event = (fn) => {
    subs.push(fn);
    return { dispose() { subs.splice(subs.indexOf(fn), 1); } };
  };
  event.fire = (e) => subs.slice().forEach((fn) => fn(e));
  return event;
}

// makeExecution yields chunks as the test pushes them, until finish().
function makeExecution(commandLine) {
  const queue = [];
  let wake = null;
  let finished = false;
  return {
    commandLine: { value: commandLine },
    push(s) { queue.push(s); if (wake) { wake(); wake = null; } },
    finish() { finished = true; if (wake) { wake(); wake = null; } },
    read() {
      return (async function* () {
        for (;;) {
          if (queue.length) { yield queue.shift(); continue; }
          if (finished) return;
          await new Promise((r) => (wake = r));
        }
      })();
    },
  };
}

function world() {
  const onStart = emitter();
  const onEnd = emitter();
  const onChangeSI = emitter();
  const terminals = [];
  const sentText = [];
  function makeTerminal(name, withSI = true) {
    const term = { name, show() {}, sendText: (t) => sentText.push({ name, t }) };
    if (withSI) {
      term.shellIntegration = {
        executeCommand(cmd) {
          const ex = makeExecution(cmd);
          term.lastExecution = ex;
          // VS Code announces the start asynchronously.
          setImmediate(() => onStart.fire({ terminal: term, execution: ex }));
          return ex;
        },
      };
    }
    terminals.push(term);
    return term;
  }
  const vscode = {
    window: {
      terminals,
      get activeTerminal() { return terminals[0]; },
      createTerminal: ({ name }) => makeTerminal(name, true),
      onDidStartTerminalShellExecution: onStart,
      onDidEndTerminalShellExecution: onEnd,
      onDidChangeTerminalShellIntegration: onChangeSI,
    },
  };
  return { vscode, makeTerminal, onStart, onEnd, onChangeSI, sentText };
}

const tick = () => new Promise((r) => setImmediate(r));

test("stripAnsi removes colour codes and shell-integration markers", () => {
  const raw = "\x1b]633;C\x07\x1b[31mred\x1b[0m text\r\nnext\x1b]633;D;0\x07";
  assert.strictEqual(stripAnsi(raw), "red text\nnext");
});

test("run executes in the aglink terminal and returns output and exit code", async () => {
  const w = world();
  const t = createTerminals(w.vscode);
  const p = t.run({ command: "ls" });
  await tick(); await tick();
  const term = w.vscode.window.terminals.find((x) => x.name === "aglink");
  assert.ok(term, "the aglink terminal is created on demand");
  const ex = term.lastExecution;
  ex.push("\x1b[34ma.go\x1b[0m\r\n");
  ex.push("b.go\r\n");
  ex.finish();
  w.onEnd.fire({ terminal: term, execution: ex, exitCode: 0 });
  assert.strictEqual(await p, "$ ls\na.go\nb.go\n[exit 0]");
  t.dispose();
});

test("run in a named terminal, and an unknown name is an error", async () => {
  const w = world();
  w.makeTerminal("build");
  const t = createTerminals(w.vscode);
  const p = t.run({ command: "make", terminal: "build" });
  await tick(); await tick();
  const ex = w.vscode.window.terminals[0].lastExecution;
  ex.push("done\n"); ex.finish();
  w.onEnd.fire({ terminal: w.vscode.window.terminals[0], execution: ex, exitCode: 2 });
  assert.strictEqual(await p, "$ make\ndone\n[exit 2]");
  await assert.rejects(t.run({ command: "x", terminal: "nope" }), /no terminal named "nope" — open: build/);
  t.dispose();
});

test("run reports a command still running at the timeout", async () => {
  const w = world();
  const t = createTerminals(w.vscode, { timeoutUnitMs: 1 }); // 1 "second" = 1ms in tests
  const p = t.run({ command: "sleep 100", timeoutSec: 20 });
  await tick(); await tick();
  const term = w.vscode.window.terminals.find((x) => x.name === "aglink");
  term.lastExecution.push("started\n");
  const out = await p;
  assert.strictEqual(out, "$ sleep 100\nstarted\n[still running after 20s]");
  term.lastExecution.finish();
  t.dispose();
});

test("a terminal without shell integration gets the text sent and says output is unreadable", async () => {
  const w = world();
  w.makeTerminal("plain", false);
  const t = createTerminals(w.vscode, { integrationWaitMs: 5 });
  const out = await t.run({ command: "echo hi", terminal: "plain" });
  assert.strictEqual(out, 'ok: sent to "plain" (no shell integration — output cannot be read)');
  assert.deepStrictEqual(w.sentText, [{ name: "plain", t: "echo hi" }]);
  t.dispose();
});

test("read shows commands a person ran, newest last, with a cap", async () => {
  const w = world();
  const term = w.makeTerminal("zsh");
  const t = createTerminals(w.vscode);
  for (let i = 1; i <= 25; i++) {
    const ex = makeExecution(`cmd${i}`);
    w.onStart.fire({ terminal: term, execution: ex });
    ex.push(`out${i}\n`); ex.finish();
    await tick();
    w.onEnd.fire({ terminal: term, execution: ex, exitCode: 0 });
    await tick();
  }
  const out = t.read({ terminal: "zsh", max: 2 });
  assert.strictEqual(out, "$ cmd24\nout24\n[exit 0]\n\n$ cmd25\nout25\n[exit 0]");
  assert.match(t.read({ terminal: "zsh", max: 100 }), /^\$ cmd6\n/, "only the last 20 are kept");
  assert.strictEqual(t.list(), "zsh | shell integration: yes | last: cmd25 [exit 0]");
  t.dispose();
});

test("very long output keeps its tail", async () => {
  const w = world();
  const term = w.makeTerminal("zsh");
  const t = createTerminals(w.vscode);
  const ex = makeExecution("cat big");
  w.onStart.fire({ terminal: term, execution: ex });
  ex.push("x".repeat(70 * 1024)); ex.push("\nEND\n"); ex.finish();
  await tick(); await tick();
  w.onEnd.fire({ terminal: term, execution: ex, exitCode: 0 });
  await tick();
  const out = t.read({ terminal: "zsh" });
  assert.match(out, /^\$ cat big\n… \(earlier output dropped\)/);
  assert.match(out, /END\n\[exit 0\]$/);
  assert.ok(out.length < 66 * 1024);
  t.dispose();
});

test("list and read with nothing to show", () => {
  const w = world();
  const t = createTerminals(w.vscode);
  assert.strictEqual(t.list(), "(no terminals)");
  w.makeTerminal("zsh");
  assert.match(t.read({}), /no commands recorded in "zsh"/);
  t.dispose();
});
