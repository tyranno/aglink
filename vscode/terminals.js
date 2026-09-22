// Terminal support: run a command in a window's terminal and wait for its
// output, and remember what every terminal ran so it can be read back later.
//
// It rests on VS Code's shell-integration API (stable since 1.93). One quirk
// shapes the design: an execution's read() only yields output written AFTER it
// is first called. So a single recorder subscribes to every execution the
// moment it starts and reads it to the end; terminal_run does not read on its
// own, it waits for the recorder to finish the execution it started.
"use strict";

const KEEP_PER_TERMINAL = 20;
const MAX_OUTPUT_CHARS = 64 * 1024;
const DEFAULT_TIMEOUT_SEC = 60;
const MAX_TIMEOUT_SEC = 600;
const EXIT_CODE_GRACE_MS = 300;
const AGLINK_TERMINAL = "aglink";

// stripAnsi removes colour/cursor escapes and the OSC 633 markers shell
// integration writes, and folds CRLF, so output reads as plain text.
function stripAnsi(s) {
  return s
    .replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, "") // OSC … BEL / ST
    .replace(/\x1b\[[0-?]*[ -\/]*[@-~]/g, "") // CSI
    .replace(/\x1b[@-Z\\-_]/g, "") // other two-byte escapes
    .replace(/\r\n/g, "\n")
    .replace(/\r/g, "");
}

function createTerminals(vscode, opts = {}) {
  const unitMs = opts.timeoutUnitMs || 1000;
  const integrationWaitMs = opts.integrationWaitMs ?? 5000;
  const byTerminal = new Map(); // terminal -> record[] (oldest first)
  const byExecution = new Map(); // execution -> record
  const subs = [];

  function recordFor(terminal, execution) {
    let rec = byExecution.get(execution);
    if (rec) return rec;
    let resolveStream;
    rec = {
      command: (execution.commandLine && execution.commandLine.value) || "",
      raw: "",
      dropped: false,
      exitCode: undefined,
      ended: false,
      reading: false,
      streamDone: new Promise((r) => (resolveStream = r)),
      resolveStream,
    };
    byExecution.set(execution, rec);
    const list = byTerminal.get(terminal) || [];
    list.push(rec);
    while (list.length > KEEP_PER_TERMINAL) {
      const old = list.shift();
      for (const [ex, r] of byExecution) if (r === old) byExecution.delete(ex);
    }
    byTerminal.set(terminal, list);
    return rec;
  }

  function startReading(rec, execution) {
    if (rec.reading) return;
    rec.reading = true;
    (async () => {
      try {
        for await (const chunk of execution.read()) {
          rec.raw += chunk;
          if (rec.raw.length > MAX_OUTPUT_CHARS) {
            rec.raw = rec.raw.slice(-MAX_OUTPUT_CHARS);
            rec.dropped = true;
          }
        }
      } catch (e) {
        // the terminal went away mid-read; keep what we have
      } finally {
        rec.resolveStream();
      }
    })();
  }

  const w = vscode.window;
  if (w.onDidStartTerminalShellExecution) {
    subs.push(
      w.onDidStartTerminalShellExecution((e) => startReading(recordFor(e.terminal, e.execution), e.execution))
    );
  }
  if (w.onDidEndTerminalShellExecution) {
    subs.push(
      w.onDidEndTerminalShellExecution((e) => {
        const rec = byExecution.get(e.execution);
        if (rec) {
          rec.exitCode = e.exitCode;
          rec.ended = true;
        }
      })
    );
  }
  if (w.onDidCloseTerminal) {
    subs.push(w.onDidCloseTerminal((t) => byTerminal.delete(t)));
  }

  function format(rec, stillRunningSec) {
    const lines = [`$ ${rec.command}`];
    if (rec.dropped) lines.push("… (earlier output dropped)");
    const body = stripAnsi(rec.raw).replace(/\s+$/, "");
    if (body) lines.push(body);
    if (stillRunningSec !== undefined) lines.push(`[still running after ${stillRunningSec}s]`);
    else lines.push(rec.exitCode === undefined ? "[exit unknown]" : `[exit ${rec.exitCode}]`);
    return lines.join("\n");
  }

  function findTerminal(name) {
    const t = w.terminals.find((x) => x.name === name);
    if (!t) {
      const open = w.terminals.map((x) => x.name).join(", ") || "(none)";
      throw new Error(`no terminal named ${JSON.stringify(name)} — open: ${open}`);
    }
    return t;
  }

  function waitForIntegration(terminal) {
    if (terminal.shellIntegration) return Promise.resolve(terminal.shellIntegration);
    if (!w.onDidChangeTerminalShellIntegration) return Promise.resolve(undefined);
    return new Promise((resolve) => {
      const sub = w.onDidChangeTerminalShellIntegration((e) => {
        if (e.terminal === terminal) {
          clearTimeout(timer);
          sub.dispose();
          resolve(e.shellIntegration);
        }
      });
      const timer = setTimeout(() => {
        sub.dispose();
        resolve(terminal.shellIntegration);
      }, integrationWaitMs);
    });
  }

  async function run(p) {
    const command = p.command;
    if (!command) throw new Error("terminal_run requires 'command'");
    const sec = Math.min(Math.max(Number(p.timeoutSec) || DEFAULT_TIMEOUT_SEC, 1), MAX_TIMEOUT_SEC);

    let term;
    if (p.terminal) term = findTerminal(p.terminal);
    else term = w.terminals.find((x) => x.name === AGLINK_TERMINAL) || w.createTerminal({ name: AGLINK_TERMINAL });
    if (term.show) term.show(true); // let whoever watches that window see it, without stealing focus

    const si = await waitForIntegration(term);
    if (!si) {
      term.sendText(command);
      return `ok: sent to ${JSON.stringify(term.name)} (no shell integration — output cannot be read)`;
    }

    const execution = si.executeCommand(command);
    const rec = recordFor(term, execution);
    if (!rec.command) rec.command = command;

    const timedOut = new Promise((r) => setTimeout(() => r("timeout"), sec * unitMs));
    const outcome = await Promise.race([rec.streamDone.then(() => "done"), timedOut]);
    if (outcome === "timeout") return format(rec, sec);
    if (rec.exitCode === undefined) {
      // The end event can land a moment after the stream closes.
      await new Promise((r) => setTimeout(r, EXIT_CODE_GRACE_MS));
    }
    return format(rec);
  }

  function read(p) {
    const term = p.terminal ? findTerminal(p.terminal) : w.activeTerminal;
    if (!term) return "(no terminals)";
    const recs = byTerminal.get(term) || [];
    const si = term.shellIntegration ? "yes" : "no";
    if (!recs.length) {
      return `(no commands recorded in ${JSON.stringify(term.name)} since aglink started — shell integration: ${si})`;
    }
    const max = Math.min(Math.max(Number(p.max) || 5, 1), KEEP_PER_TERMINAL);
    return recs.slice(-max).map((r) => format(r)).join("\n\n");
  }

  function list() {
    if (!w.terminals.length) return "(no terminals)";
    return w.terminals
      .map((t) => {
        const recs = byTerminal.get(t) || [];
        const last = recs[recs.length - 1];
        const lastStr = last ? `${last.command} ${last.exitCode === undefined ? "[running]" : `[exit ${last.exitCode}]`}` : "(none)";
        return `${t.name} | shell integration: ${t.shellIntegration ? "yes" : "no"} | last: ${lastStr}`;
      })
      .join("\n");
  }

  return {
    count: () => w.terminals.length,
    list,
    run,
    read,
    dispose: () => subs.forEach((s) => s.dispose()),
  };
}

module.exports = { createTerminals, stripAnsi };
