// The edit / navigate / build / debug half of the vscode_* tools: edit and save
// files, ask the language servers (symbols, definition, references, hover), run
// tasks, tests and the debugger, and read git state. Everything goes through
// VS Code APIs, never Node's fs or child_process, so a Remote-SSH window works
// on the remote files the same way a local one works on local files.
"use strict";

const MAX_ROWS = 200;
const MAX_HOVER_CHARS = 4000;
const MAX_DIFF_CHARS = 30000;
const MAX_DIFF_LINES_SHOWN = 20;
const DEFAULT_TIMEOUT_SEC = 60;
const MAX_TIMEOUT_SEC = 600;
const TESTS_WAIT_SEC = 20; // under the daemon's 30s default wait

const SYMBOL_KIND = [
  "file", "module", "namespace", "package", "class", "method", "property", "field", "constructor",
  "enum", "interface", "function", "variable", "constant", "string", "number", "boolean", "array",
  "object", "key", "null", "enum-member", "struct", "event", "operator", "type-parameter",
];

// The git extension's Status enum (extensions/git/src/api/git.d.ts).
const GIT_STATUS = {
  0: "M", 1: "A", 2: "D", 3: "R", 4: "C", 5: "M", 6: "D", 7: "?", 8: "!", 9: "A", 10: "R", 11: "T",
  12: "U", 13: "U", 14: "U", 15: "U", 16: "U", 17: "U", 18: "U",
};

const TEST_ACTIONS = {
  all: "testing.runAll",
  failed: "testing.reRunFailTests",
  last: "testing.reRunLastRun",
  file: "testing.runCurrentFile",
  cursor: "testing.runAtCursor",
  cancel: "testing.cancelRun",
};

function isTrue(v) {
  return v === true || v === 1 || /^(true|1|yes)$/i.test(String(v ?? ""));
}
function isFalse(v) {
  return v === false || v === 0 || /^(false|0|no)$/i.test(String(v ?? ""));
}

function timeoutSec(p, def) {
  const n = Number(p.timeoutSec) || def;
  return Math.max(1, Math.min(MAX_TIMEOUT_SEC, n));
}

function clip(s, max, hint) {
  if (s.length <= max) return s;
  return s.slice(0, max) + `\n… ${s.length - max} more chars${hint ? " — " + hint : ""}`;
}

function plural(n, word) {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

// posAt turns an offset in text into a {line, character} pair.
function posAt(text, offset) {
  const before = text.slice(0, offset);
  const line = (before.match(/\n/g) || []).length;
  return { line, character: offset - (before.lastIndexOf("\n") + 1) };
}

function createDevMethods(vscode, h) {
  const { resolveUri, displayPath, documentFor, firstFolder } = h;
  const unitMs = h.timeoutUnitMs || 1000;

  const range = (a, b) => new vscode.Range(a.line, a.character, b.line, b.character);
  const where = (uri, r) => `${displayPath(uri)}:${r.start.line + 1}`;

  async function docByUri(uri) {
    const open = vscode.workspace.textDocuments.find((d) => d.uri.toString() === uri.toString());
    return open || (await vscode.workspace.openTextDocument(uri));
  }

  // lineTexts fetches the text of each [uri, line] pair, opening every file at
  // most once; a file that cannot be opened just has no text.
  async function lineTexts(pairs) {
    const docs = new Map();
    const out = [];
    for (const [uri, line] of pairs) {
      const k = uri.toString();
      if (!docs.has(k)) docs.set(k, await docByUri(uri).then((d) => d.getText().split(/\r?\n/), () => null));
      const lines = docs.get(k);
      out.push(lines && lines[line] !== undefined ? lines[line].trim() : "");
    }
    return out;
  }

  async function saveDoc(doc) {
    const fresh = await docByUri(doc.uri);
    if (!fresh.isDirty) return "saved";
    return (await fresh.save()) ? "saved" : "NOT saved (save failed)";
  }

  // ---- edit / save / format -----------------------------------------------

  async function edit(p) {
    if (!p.path) throw new Error("edit requires 'path'");
    const doc = await documentFor(p.path);
    const text = doc.getText();
    const eol = text.includes("\r\n") ? "\r\n" : "\n";
    const fix = (s) => String(s ?? "").replace(/\r\n/g, "\n").replace(/\n/g, eol);
    let newText = fix(p.new_text);
    let start, end, before;

    if (p.old_text !== undefined && p.old_text !== "") {
      const oldText = fix(p.old_text);
      const hits = [];
      for (let i = text.indexOf(oldText); i >= 0; i = text.indexOf(oldText, i + 1)) hits.push(i);
      if (!hits.length) throw new Error(`old_text not found in ${displayPath(doc.uri)} — read the file and copy the text exactly`);
      let at = hits[0];
      if (p.occurrence) {
        const n = Number(p.occurrence);
        if (!(n >= 1 && n <= hits.length)) throw new Error(`occurrence ${p.occurrence} out of range — old_text occurs ${hits.length} times`);
        at = hits[n - 1];
      } else if (hits.length > 1) {
        const lines = hits.slice(0, 10).map((o) => posAt(text, o).line + 1).join(", ");
        throw new Error(`old_text occurs ${hits.length} times (lines ${lines}) — add surrounding context or pass occurrence`);
      }
      start = posAt(text, at);
      end = posAt(text, at + oldText.length);
      before = oldText.split(/\r?\n/);
    } else if (p.start_line) {
      const lines = text.split(/\r?\n/);
      const s = Number(p.start_line);
      const e = Number(p.end_line) || s;
      if (!(s >= 1 && e >= s && e <= lines.length)) throw new Error(`line range ${s}-${e} is outside 1-${lines.length}`);
      before = lines.slice(s - 1, e);
      if (newText === "") {
        // delete the lines themselves, not just their contents
        if (e < lines.length) {
          start = { line: s - 1, character: 0 };
          end = { line: e, character: 0 };
        } else if (s > 1) {
          start = { line: s - 2, character: lines[s - 2].length };
          end = { line: e - 1, character: lines[e - 1].length };
        } else {
          start = { line: 0, character: 0 };
          end = { line: e - 1, character: lines[e - 1].length };
        }
      } else {
        if (newText.endsWith(eol)) newText = newText.slice(0, -eol.length);
        start = { line: s - 1, character: 0 };
        end = { line: e - 1, character: lines[e - 1].length };
      }
    } else {
      throw new Error("edit needs either old_text (+ new_text) or start_line (+ end_line, new_text)");
    }

    const we = new vscode.WorkspaceEdit();
    we.replace(doc.uri, range(start, end), newText);
    if (!(await vscode.workspace.applyEdit(we))) throw new Error(`VS Code refused the edit to ${displayPath(doc.uri)}`);
    const saved = isFalse(p.save) ? "not saved" : await saveDoc(doc);

    const after = newText === "" ? [] : newText.split(/\r?\n/);
    const show = (sign, rows) => {
      const out = rows.slice(0, MAX_DIFF_LINES_SHOWN).map((l) => sign + " " + l);
      if (rows.length > MAX_DIFF_LINES_SHOWN) out.push(`${sign} … ${rows.length - MAX_DIFF_LINES_SHOWN} more lines`);
      return out;
    };
    const first = p.old_text ? start.line + 1 : Number(p.start_line);
    return [
      `ok: edited ${displayPath(doc.uri)}:${first}-${first + Math.max(after.length, 1) - 1} (-${before.length} +${after.length} lines), ${saved}`,
      ...show("-", before),
      ...show("+", after),
    ].join("\n");
  }

  async function save(p) {
    if (p.path) {
      const doc = await documentFor(p.path);
      if (!doc.isDirty) return `ok: ${displayPath(doc.uri)} had no unsaved changes`;
      if (!(await doc.save())) throw new Error(`could not save ${displayPath(doc.uri)}`);
      return `ok: saved ${displayPath(doc.uri)}`;
    }
    const dirty = vscode.workspace.textDocuments.filter((d) => d.isDirty && d.uri.scheme !== "untitled");
    if (!dirty.length) return "ok: nothing to save";
    const ok = await vscode.workspace.saveAll(false);
    const left = vscode.workspace.textDocuments.filter((d) => d.isDirty && d.uri.scheme !== "untitled");
    const head = ok && !left.length ? `ok: saved ${plural(dirty.length, "file")}` : `saved ${dirty.length - left.length} of ${dirty.length} files`;
    const lines = [head, ...dirty.map((d) => displayPath(d.uri))];
    if (left.length) lines.push("still unsaved: " + left.map((d) => displayPath(d.uri)).join(", "));
    return lines.join("\n");
  }

  async function format(p) {
    const doc = await documentFor(p.path);
    const cfg = vscode.workspace.getConfiguration("editor", doc.uri);
    const options = { tabSize: cfg.get("tabSize", 4), insertSpaces: cfg.get("insertSpaces", true) };
    const edits = await vscode.commands.executeCommand("vscode.executeFormatDocumentProvider", doc.uri, options);
    let note = "no changes";
    if (edits && edits.length) {
      const we = new vscode.WorkspaceEdit();
      for (const e of edits) we.replace(doc.uri, e.range, e.newText);
      if (!(await vscode.workspace.applyEdit(we))) throw new Error(`VS Code refused the formatting edits to ${displayPath(doc.uri)}`);
      note = plural(edits.length, "edit");
    } else if (!edits) {
      note = "no formatter answered for this file";
    }
    const saved = isFalse(p.save) ? "not saved" : await saveDoc(doc);
    return `ok: formatted ${displayPath(doc.uri)} (${note}), ${saved}`;
  }

  // ---- language features --------------------------------------------------

  async function symbols(p) {
    const rows = [];
    if (p.path) {
      const doc = await documentFor(p.path);
      const found = (await vscode.commands.executeCommand("vscode.executeDocumentSymbolProvider", doc.uri)) || [];
      const q = (p.query || "").toLowerCase();
      const walk = (list, depth) => {
        for (const s of list) {
          const r = s.selectionRange || s.range || (s.location && s.location.range);
          if (!q || s.name.toLowerCase().includes(q)) {
            rows.push(`${"  ".repeat(q ? 0 : depth)}${SYMBOL_KIND[s.kind] || "symbol"} ${s.name}${s.detail ? " " + s.detail : ""} :${r.start.line + 1}`);
          }
          if (s.children) walk(s.children, depth + 1);
        }
      };
      walk(found, 0);
      if (!rows.length) return `no symbols${q ? ` matching ${JSON.stringify(p.query)}` : ""} in ${displayPath(doc.uri)}`;
      rows.unshift(displayPath(doc.uri));
    } else {
      if (!p.query) throw new Error("symbols needs 'query' (workspace search) or 'path' (one file's outline)");
      const found = (await vscode.commands.executeCommand("vscode.executeWorkspaceSymbolProvider", p.query)) || [];
      for (const s of found) {
        rows.push(`${SYMBOL_KIND[s.kind] || "symbol"} ${s.name}${s.containerName ? ` (${s.containerName})` : ""} ${where(s.location.uri, s.location.range)}`);
      }
      if (!rows.length) return `no workspace symbols match ${JSON.stringify(p.query)}`;
    }
    const shown = rows.slice(0, MAX_ROWS + (p.path ? 1 : 0));
    if (rows.length > shown.length) shown.push(`… ${rows.length - shown.length} more`);
    return shown.join("\n");
  }

  // position resolves path + line + (column | symbol) to a document position;
  // with no path and no line it is the active editor's cursor.
  async function position(p) {
    if (!p.path && !p.line) {
      const ed = vscode.window.activeTextEditor;
      if (!ed) throw new Error("no active editor — pass 'path' and 'line'");
      return { doc: ed.document, pos: ed.selection.active };
    }
    if (!p.line) throw new Error("pass 'line' (1-based) with 'path'");
    const doc = await documentFor(p.path);
    const lines = doc.getText().split(/\r?\n/);
    const line = Number(p.line);
    if (!(line >= 1 && line <= lines.length)) throw new Error(`line ${line} is outside 1-${lines.length}`);
    const text = lines[line - 1];
    let ch;
    if (p.column) {
      ch = Number(p.column) - 1;
    } else if (p.symbol) {
      const re = new RegExp(`(?<![\\w$])${p.symbol.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}(?![\\w$])`);
      const m = re.exec(text);
      ch = m ? m.index : text.indexOf(p.symbol);
      if (ch < 0) throw new Error(`${JSON.stringify(p.symbol)} is not on line ${line}: ${text.trim()}`);
    } else {
      ch = text.length - text.trimStart().length;
    }
    return { doc, pos: new vscode.Position(line - 1, ch) };
  }

  async function locations(list, empty) {
    const locs = (list || []).map((l) => (l.targetUri ? { uri: l.targetUri, range: l.targetSelectionRange || l.targetRange } : l));
    if (!locs.length) return empty;
    const shown = locs.slice(0, MAX_ROWS);
    const texts = await lineTexts(shown.map((l) => [l.uri, l.range.start.line]));
    const rows = shown.map((l, i) => `${displayPath(l.uri)}:${l.range.start.line + 1}:${l.range.start.character + 1}  ${texts[i]}`);
    if (locs.length > shown.length) rows.push(`… ${locs.length - shown.length} more`);
    return rows.join("\n");
  }

  const at = (doc, pos) => `${displayPath(doc.uri)}:${pos.line + 1}:${pos.character + 1}`;

  async function definition(p) {
    const { doc, pos } = await position(p);
    const found = await vscode.commands.executeCommand("vscode.executeDefinitionProvider", doc.uri, pos);
    return locations(found, `no definition found at ${at(doc, pos)}`);
  }

  async function references(p) {
    const { doc, pos } = await position(p);
    const found = (await vscode.commands.executeCommand("vscode.executeReferenceProvider", doc.uri, pos)) || [];
    const body = await locations(found, `no references found at ${at(doc, pos)}`);
    return found.length ? `${plural(found.length, "reference")}\n${body}` : body;
  }

  async function hover(p) {
    const { doc, pos } = await position(p);
    const found = (await vscode.commands.executeCommand("vscode.executeHoverProvider", doc.uri, pos)) || [];
    const parts = [];
    for (const hv of found) {
      for (const c of hv.contents || []) {
        const s = typeof c === "string" ? c : c.language ? "```" + c.language + "\n" + c.value + "\n```" : c.value;
        if (s && s.trim()) parts.push(s.trim());
      }
    }
    if (!parts.length) return `no hover info at ${at(doc, pos)}`;
    return clip(parts.join("\n---\n"), MAX_HOVER_CHARS);
  }

  // ---- tasks --------------------------------------------------------------

  const taskLabel = (t) => `${t.source}: ${t.name}`;

  async function tasks() {
    const all = await vscode.tasks.fetchTasks();
    if (!all.length) return "(no tasks)";
    const rows = all.slice(0, MAX_ROWS).map((t) => {
      const group = t.group && t.group.id ? ` (${t.group.id})` : "";
      return `${t.name} [${t.source}]${group}${t.isBackground ? " background" : ""}${t.detail ? " — " + t.detail : ""}`;
    });
    if (all.length > rows.length) rows.push(`… ${all.length - rows.length} more`);
    return rows.join("\n");
  }

  async function task_run(p) {
    if (!p.name) throw new Error("task_run requires 'name' (see vscode_tasks)");
    const all = await vscode.tasks.fetchTasks();
    const want = String(p.name).toLowerCase();
    let hits = all.filter((t) => t.name === p.name || taskLabel(t) === p.name);
    if (!hits.length) hits = all.filter((t) => t.name.toLowerCase() === want || taskLabel(t).toLowerCase() === want);
    if (!hits.length) throw new Error(`no task named ${JSON.stringify(p.name)} — tasks: ${all.map((t) => t.name).join(", ") || "(none)"}`);
    if (hits.length > 1) throw new Error(`task ${JSON.stringify(p.name)} is ambiguous — give one of: ${hits.map(taskLabel).join(", ")}`);
    const task = hits[0];
    const secs = timeoutSec(p, DEFAULT_TIMEOUT_SEC);
    const started = Date.now();

    // Listen before starting, so a task that ends at once is not missed.
    const ends = [];
    let wake = null;
    const push = (kind) => (e) => {
      ends.push({ kind, execution: e.execution, exitCode: e.exitCode });
      if (wake) wake();
    };
    const subs = [vscode.tasks.onDidEndTaskProcess(push("process")), vscode.tasks.onDidEndTask(push("task"))];
    let timer;
    try {
      const execution = await vscode.tasks.executeTask(task);
      const mine = (e) => e.execution === execution || (e.execution && e.execution.task && taskLabel(e.execution.task) === taskLabel(task));
      const deadline = new Promise((r) => (timer = setTimeout(r, secs * unitMs)));
      for (;;) {
        const proc = ends.find((e) => e.kind === "process" && mine(e));
        if (proc) return finished(task, proc.exitCode, started);
        const done = ends.find((e) => e.kind === "task" && mine(e));
        if (done) {
          // onDidEndTask can land just before onDidEndTaskProcess; give it a moment.
          await new Promise((r) => setTimeout(r, 0.3 * unitMs));
          const late = ends.find((e) => e.kind === "process" && mine(e));
          return finished(task, late ? late.exitCode : undefined, started);
        }
        const woke = await Promise.race([new Promise((r) => (wake = () => r(true))), deadline.then(() => false)]);
        if (!woke) {
          const bg = task.isBackground ? " (a background task — it keeps running by design)" : "";
          return `task '${task.name}' still running after ${secs}s${bg}. Its output stays in its terminal; check vscode_problems for what its problem matcher found.`;
        }
      }
    } finally {
      clearTimeout(timer);
      subs.forEach((s) => s.dispose());
    }
  }

  function finished(task, code, started) {
    const secs = ((Date.now() - started) / 1000).toFixed(1);
    const status = code === undefined ? "finished (no exit code reported)" : `exited with code ${code}`;
    return `${code ? "failed" : "ok"}: task '${task.name}' ${status} after ${secs}s. Task output is not readable through the API — see vscode_problems for what its problem matcher found.`;
  }

  // ---- debug --------------------------------------------------------------

  function bpLine(b) {
    const flags = [b.enabled === false ? "disabled" : "", b.condition ? `if ${b.condition}` : "", b.hitCondition ? `hit ${b.hitCondition}` : "", b.logMessage ? `log ${JSON.stringify(b.logMessage)}` : ""].filter(Boolean);
    const head = b.location ? where(b.location.uri, b.location.range) : `function ${b.functionName}`;
    return head + (flags.length ? " " + flags.join(", ") : "");
  }

  async function debug(p) {
    const action = p.action || "status";
    const dbg = vscode.debug;
    switch (action) {
      case "start": {
        const folder = vscode.workspace.workspaceFolders && vscode.workspace.workspaceFolders[0];
        if (p.name) {
          if (!(await dbg.startDebugging(folder, p.name))) throw new Error(`could not start debug configuration ${JSON.stringify(p.name)} — check its name in launch.json`);
        } else {
          await vscode.commands.executeCommand("workbench.action.debug.start");
        }
        const s = dbg.activeDebugSession;
        return s ? `ok: debugging ${s.name} [${s.type}]` : "ok: start requested (no active session yet — check status)";
      }
      case "stop": {
        const s = dbg.activeDebugSession;
        if (!s) return "ok: no debug session was running";
        await dbg.stopDebugging();
        return `ok: stopped ${s.name}`;
      }
      case "status": {
        const s = dbg.activeDebugSession;
        const lines = [s ? `session: ${s.name} [${s.type}]` : "session: (none)"];
        const item = dbg.activeStackItem;
        if (s && item && item.threadId !== undefined && item.session === s) {
          try {
            const r = await s.customRequest("stackTrace", { threadId: item.threadId, startFrame: 0, levels: 5 });
            const frames = (r && r.stackFrames) || [];
            if (frames.length) lines.push("stopped at:", ...frames.map((f) => `  ${f.name} ${(f.source && (f.source.path || f.source.name)) || "?"}:${f.line}`));
          } catch (e) {
            // not stopped, or the adapter does not answer stackTrace
          }
        }
        lines.push(`breakpoints: ${dbg.breakpoints.length}`);
        return lines.join("\n");
      }
      case "breakpoints": {
        if (!dbg.breakpoints.length) return "(no breakpoints)";
        return dbg.breakpoints.map(bpLine).join("\n");
      }
      case "breakpoint_add": {
        if (!p.path || !p.line) throw new Error("breakpoint_add requires 'path' and 'line'");
        const uri = resolveUri(p.path);
        const loc = new vscode.Location(uri, new vscode.Position(Number(p.line) - 1, 0));
        const bp = new vscode.SourceBreakpoint(loc, true, p.condition || undefined);
        dbg.addBreakpoints([bp]);
        return `ok: breakpoint at ${displayPath(uri)}:${p.line}${p.condition ? ` if ${p.condition}` : ""}`;
      }
      case "breakpoint_remove": {
        if (!p.path) throw new Error("breakpoint_remove requires 'path' (and 'line', or every breakpoint in the file goes)");
        const uri = resolveUri(p.path).toString();
        const hits = dbg.breakpoints.filter((b) => b.location && b.location.uri.toString() === uri && (!p.line || b.location.range.start.line === Number(p.line) - 1));
        if (!hits.length) throw new Error(`no breakpoint at ${p.path}${p.line ? ":" + p.line : ""}`);
        dbg.removeBreakpoints(hits);
        return `ok: removed ${plural(hits.length, "breakpoint")}`;
      }
      default:
        throw new Error(`unknown debug action ${JSON.stringify(action)} — use start, stop, status, breakpoints, breakpoint_add or breakpoint_remove`);
    }
  }

  // ---- git ----------------------------------------------------------------

  async function gitRepo(p) {
    const ext = vscode.extensions.getExtension("vscode.git");
    const why = vscode.env.remoteName
      ? "in a Remote-SSH window the Git extension runs on the remote, where aglink-vscode (a UI extension) cannot call it — use vscode_terminal_run with git instead"
      : "the built-in Git extension is missing or disabled";
    if (!ext) throw new Error(`git is not available: ${why}`);
    const exports = ext.isActive ? ext.exports : await ext.activate();
    if (!exports || !exports.getAPI) throw new Error(`git is not available: ${why}`);
    const api = exports.getAPI(1);
    let repo;
    if (p.path) repo = api.getRepository(resolveUri(p.path));
    if (!repo) {
      const folder = firstFolder();
      repo = (folder && api.getRepository(folder)) || api.repositories[0];
    }
    if (!repo) throw new Error("no git repository is open in this window");
    return repo;
  }

  async function git(p) {
    const action = p.action || "status";
    const repo = await gitRepo(p);
    const rel = (uri) => {
      const root = repo.rootUri.path.replace(/\/$/, "");
      return uri.path.startsWith(root + "/") ? uri.path.slice(root.length + 1) : displayPath(uri);
    };
    switch (action) {
      case "status": {
        const st = repo.state;
        const head = st.HEAD || {};
        const branch = head.name || (head.commit ? `(detached ${head.commit.slice(0, 8)})` : "(no commits)");
        const up = head.upstream ? ` → ${head.upstream.remote}/${head.upstream.name}, ahead ${head.ahead || 0}, behind ${head.behind || 0}` : " (no upstream)";
        const lines = [`repo: ${displayPath(repo.rootUri)}`, `branch: ${branch}${up}`];
        const work = st.workingTreeChanges || [];
        const untracked = (st.untrackedChanges || []).concat(work.filter((c) => c.status === 7));
        const unstaged = work.filter((c) => c.status !== 7);
        const list = (title, changes, mark) => {
          if (!changes.length) return;
          lines.push(`${title} (${changes.length}):`);
          for (const c of changes.slice(0, MAX_ROWS)) lines.push(`  ${mark ? GIT_STATUS[c.status] || "?" : ""}${mark ? " " : ""}${rel(c.uri)}`);
          if (changes.length > MAX_ROWS) lines.push(`  … ${changes.length - MAX_ROWS} more`);
        };
        list("merge conflicts", st.mergeChanges || [], true);
        list("staged", st.indexChanges || [], true);
        list("unstaged", unstaged, true);
        list("untracked", untracked, false);
        if (lines.length === 2) lines.push("clean");
        return lines.join("\n");
      }
      case "diff": {
        const staged = isTrue(p.staged);
        let out;
        if (p.path) {
          const fsPath = resolveUri(p.path).fsPath;
          out = staged ? await repo.diffIndexWithHEAD(fsPath) : await repo.diffWithHEAD(fsPath);
        } else {
          out = await repo.diff(staged);
        }
        if (!out) return `no ${staged ? "staged" : "unstaged"} changes${p.path ? " in " + p.path : ""}`;
        return clip(out, MAX_DIFF_CHARS, "pass 'path' to narrow it");
      }
      case "log": {
        const n = Math.max(1, Math.min(100, Number(p.max) || 10));
        const commits = await repo.log({ maxEntries: n });
        if (!commits.length) return "(no commits)";
        return commits
          .map((c) => {
            const date = c.authorDate ? new Date(c.authorDate).toISOString().slice(0, 10) : "";
            return `${c.hash.slice(0, 8)} ${date} ${c.authorName || ""}: ${(c.message || "").split("\n")[0]}`;
          })
          .join("\n");
      }
      default:
        throw new Error(`unknown git action ${JSON.stringify(action)} — use status, diff or log (read-only)`);
    }
  }

  // ---- tests --------------------------------------------------------------

  async function tests(p) {
    const action = p.action || "all";
    const id = TEST_ACTIONS[action];
    if (!id) throw new Error(`unknown tests action ${JSON.stringify(action)} — use ${Object.keys(TEST_ACTIONS).join(", ")}`);
    if ((action === "file" || action === "cursor") && p.path) {
      const uri = resolveUri(p.path);
      const line = Math.max(1, Number(p.line) || 1);
      await vscode.window.showTextDocument(uri, { preview: false, selection: new vscode.Range(line - 1, 0, line - 1, 0) });
    }
    const secs = timeoutSec(p, TESTS_WAIT_SEC);
    let timer;
    const run = Promise.resolve(vscode.commands.executeCommand(id)).then(() => true);
    const done = await Promise.race([run, new Promise((r) => (timer = setTimeout(() => r(false), secs * unitMs)))]);
    clearTimeout(timer);
    if (action === "cancel") return "ok: cancelled the running tests";
    const state = done ? "the run finished" : `still running after ${secs}s`;
    return `ok: ran ${id} (${state}). Pass/fail details are in the Test Explorer, which the API does not expose — failures with a location show up in vscode_problems.`;
  }

  return {
    edit, save, format, symbols, definition, references, hover, tasks, task_run, debug, git, tests,
  };
}

module.exports = { createDevMethods };
