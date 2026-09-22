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

function doc(uri, text, isDirty = false) {
  return {
    uri,
    isDirty,
    getText: () => text,
    get lineCount() {
      return text.split(/\r?\n/).length;
    },
  };
}

// makeVSCode builds a fake with one workspace folder. opts.remote makes it a
// Remote-SSH window ("ssh-remote+<host>").
function makeVSCode(opts = {}) {
  const calls = { executed: [], shown: [], opened: [] };
  const remoteAuthority = opts.remote ? `ssh-remote+${opts.remote}` : "";
  const folderUri = opts.remote
    ? new Uri("vscode-remote", remoteAuthority, opts.folder || "/home/u/backend")
    : Uri.file(opts.folder || "C:\\p\\aglink");
  const files = new Map(Object.entries(opts.files || {})); // uri string -> text
  const openDocs = (opts.openDocs || []).map((d) => doc(d.uri, d.text, d.dirty));
  const config = Object.assign({ enabled: true, port: 0 }, opts.config || {});

  const vscode = {
    Uri,
    Position,
    Range,
    Selection,
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
        return doc(uri, files.get(uri.toString()));
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
  };
  return { vscode, calls, folderUri, Uri };
}

module.exports = { makeVSCode, Uri, Position, Range };
