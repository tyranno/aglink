// A small stand-in for the `vscode` module — only the surface aglink-vscode
// touches — so the extension's logic runs under `node --test` with no editor.
"use strict";

const path = require("path");

class Uri {
  constructor(scheme, authority, p) {
    this.scheme = scheme;
    this.authority = authority || "";
    this.path = p;
  }
  static file(p) {
    return new Uri("file", "", p.replace(/\\/g, "/").replace(/^([A-Za-z]):/, "/$1:"));
  }
  static parse(s) {
    const m = /^([a-z-]+):\/\/([^/]*)(.*)$/.exec(s);
    return new Uri(m[1], decodeURIComponent(m[2]), m[3] || "/");
  }
  static joinPath(base, ...parts) {
    return new Uri(base.scheme, base.authority, path.posix.join(base.path, ...parts));
  }
  with(change) {
    return new Uri(change.scheme ?? this.scheme, change.authority ?? this.authority, change.path ?? this.path);
  }
  get fsPath() {
    return this.scheme === "file" ? this.path.replace(/^\/([A-Za-z]:)/, "$1").replace(/\//g, "\\") : this.path;
  }
  toString() {
    return `${this.scheme}://${this.authority}${this.path}`;
  }
}

class Position {
  constructor(line, character) {
    this.line = line;
    this.character = character;
  }
}
class Range {
  constructor(a, b, c, d) {
    this.start = new Position(a, b);
    this.end = new Position(c, d);
  }
}
class Selection extends Range {}
class Location {
  constructor(uri, rangeOrPos) {
    this.uri = uri;
    this.range = rangeOrPos instanceof Range ? rangeOrPos : new Range(rangeOrPos.line, rangeOrPos.character, rangeOrPos.line, rangeOrPos.character);
  }
}
class SourceBreakpoint {
  constructor(location, enabled, condition) {
    this.location = location;
    this.enabled = enabled;
    this.condition = condition;
  }
}
class WorkspaceEdit {
  constructor() {
    this.edits = [];
  }
  replace(uri, range, newText) {
    this.edits.push({ uri, range, newText });
  }
}

function doc(uri, text, isDirty = false, calls) {
  const d = {
    uri,
    isDirty,
    text,
    getText: () => d.text,
    get lineCount() {
      return d.text.split(/\r?\n/).length;
    },
    save: async () => {
      if (calls) calls.saved.push(uri.toString());
      d.isDirty = false;
      return true;
    },
  };
  return d;
}

function offsetOf(text, pos) {
  const lines = text.split("\n");
  let off = 0;
  for (let i = 0; i < pos.line; i++) off += lines[i].length + 1;
  return off + pos.character;
}

// makeVSCode builds a fake with one workspace folder. opts.remote makes it a
// Remote-SSH window ("ssh-remote+<host>").
function makeVSCode(opts = {}) {
  const calls = { executed: [], shown: [], opened: [], saved: [], edits: [] };
  const remoteAuthority = opts.remote ? `ssh-remote+${opts.remote}` : "";
  const folderUri = opts.remote
    ? new Uri("vscode-remote", remoteAuthority, opts.folder || "/home/u/backend")
    : Uri.file(opts.folder || "C:\\p\\aglink");
  const files = new Map(Object.entries(opts.files || {})); // uri string -> text
  const openDocs = (opts.openDocs || []).map((d) => doc(d.uri, d.text, d.dirty, calls));
  const breakpoints = [];
  const taskEnd = [];
  const taskProcessEnd = [];
  const on = (list) => (fn) => {
    list.push(fn);
    return { dispose() { list.splice(list.indexOf(fn), 1); } };
  };
  const debugState = { activeDebugSession: undefined };
  const config = Object.assign({ enabled: true, port: 0 }, opts.config || {});

  const vscode = {
    Uri,
    Position,
    Range,
    Selection,
    Location,
    SourceBreakpoint,
    WorkspaceEdit,
    DiagnosticSeverity: { Error: 0, Warning: 1, Information: 2, Hint: 3 },
    TextEditorRevealType: { InCenter: 2 },
    env: { remoteName: opts.remote ? "ssh-remote" : undefined, sessionId: opts.session || "sess-1" },
    workspace: {
      name: opts.name ?? "backend",
      workspaceFolders: opts.noFolder ? undefined : [{ uri: folderUri, name: opts.name ?? "backend", index: 0 }],
      textDocuments: openDocs,
      openTextDocument: async (uri) => {
        calls.opened.push(uri.toString());
        const open = openDocs.find((d) => d.uri.toString() === uri.toString());
        if (open) return open;
        if (!files.has(uri.toString())) throw new Error(`cannot open ${uri.toString()}`);
        return doc(uri, files.get(uri.toString()), false, calls);
      },
      // applyEdit opens the file the way VS Code does, then edits it unsaved.
      applyEdit: async (we) => {
        if (opts.refuseEdit) return false;
        for (const e of we.edits.slice().reverse()) {
          calls.edits.push(e);
          let d = openDocs.find((o) => o.uri.toString() === e.uri.toString());
          if (!d) {
            if (!files.has(e.uri.toString())) return false;
            d = doc(e.uri, files.get(e.uri.toString()), false, calls);
            openDocs.push(d);
          }
          const t = d.text;
          d.text = t.slice(0, offsetOf(t, e.range.start)) + e.newText + t.slice(offsetOf(t, e.range.end));
          d.isDirty = true;
        }
        return true;
      },
      saveAll: async () => {
        for (const d of openDocs) if (d.isDirty) await d.save();
        return true;
      },
      getConfiguration: () => ({ get: (k, def) => (k in config ? config[k] : def) }),
      onDidChangeConfiguration: () => ({ dispose() {} }),
    },
    window: {
      activeTextEditor: opts.active
        ? { document: openDocs.find((d) => d.uri.toString() === opts.active.uri.toString()), selection: { active: new Position(opts.active.line, 0) } }
        : undefined,
      tabGroups: { all: opts.tabGroups || [] },
      terminals: opts.terminals || [],
      showTextDocument: async (uriOrDoc, options) => {
        calls.shown.push({ uri: (uriOrDoc.uri || uriOrDoc).toString(), options });
        return { revealRange() {}, selection: null };
      },
    },
    languages: {
      getDiagnostics: (uri) => {
        const all = opts.diagnostics || [];
        if (uri) {
          const hit = all.find(([u]) => u.toString() === uri.toString());
          return hit ? hit[1] : [];
        }
        return all;
      },
    },
    commands: {
      executeCommand: async (id, ...args) => {
        calls.executed.push({ id, args });
        if (opts.commandResult) return opts.commandResult(id, args);
        return undefined;
      },
    },
    // opts.tasks: [{name, source, ...}]; opts.runTask(task, fire) decides how it
    // ends — fire.process(code) / fire.task() emit the end events.
    tasks: {
      fetchTasks: async () => opts.tasks || [],
      executeTask: async (task) => {
        const execution = { task };
        calls.executed.push({ id: "task", args: [task.name] });
        const fire = {
          process: (exitCode) => taskProcessEnd.slice().forEach((fn) => fn({ execution, exitCode })),
          task: () => taskEnd.slice().forEach((fn) => fn({ execution })),
        };
        if (opts.runTask) setTimeout(() => opts.runTask(task, fire), 0);
        return execution;
      },
      onDidEndTask: on(taskEnd),
      onDidEndTaskProcess: on(taskProcessEnd),
    },
    debug: {
      get activeDebugSession() {
        return debugState.activeDebugSession;
      },
      activeStackItem: undefined,
      breakpoints,
      startDebugging: async (folder, name) => {
        calls.executed.push({ id: "startDebugging", args: [name] });
        if (opts.debugConfigs && !opts.debugConfigs.includes(name)) return false;
        debugState.activeDebugSession = { name, type: "go" };
        return true;
      },
      stopDebugging: async () => {
        debugState.activeDebugSession = undefined;
      },
      addBreakpoints: (bps) => breakpoints.push(...bps),
      removeBreakpoints: (bps) => {
        for (const b of bps) breakpoints.splice(breakpoints.indexOf(b), 1);
      },
    },
    extensions: {
      getExtension: (id) => (opts.extensions || {})[id],
    },
  };
  return { vscode, calls, folderUri, Uri, openDocs };
}

module.exports = { makeVSCode, Uri, Position, Range, Location };
