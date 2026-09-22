// The window-side half of every vscode_* tool. Each method takes the params the
// daemon forwarded and returns the reply text, or throws — the connector turns
// a throw into an error reply. `vscode` is passed in rather than required so
// the whole thing runs under `node --test` against a fake.
"use strict";

const MAX_READ_LINES = 2000;
const MAX_READ_BYTES = 200 * 1024;
const MAX_PROBLEMS = 200;
const MAX_RESULT_CHARS = 512;
const SEVERITY = ["error", "warning", "info", "hint"];

function createMethods(vscode, terminals) {
  // ---- paths --------------------------------------------------------------

  function firstFolder() {
    const f = vscode.workspace.workspaceFolders;
    return f && f.length ? f[0].uri : undefined;
  }

  // displayPath shows a path the way the person in that window would recognise
  // it: a Windows path for a local window, the remote path for Remote-SSH.
  function displayPath(uri) {
    return uri.scheme === "file" ? uri.fsPath : uri.path;
  }

  function isAbsolute(p) {
    return p.startsWith("/") || /^[A-Za-z]:[\\/]/.test(p) || p.startsWith("\\\\");
  }

  // resolveUri turns what the caller typed into a URI in this window's world.
  // A relative path joins the first workspace folder. An absolute path keeps
  // the folder's scheme and authority, so in a Remote-SSH window
  // "/home/u/x" means the remote file, not a local one.
  function resolveUri(p) {
    const folder = firstFolder();
    if (!isAbsolute(p)) {
      if (!folder) throw new Error(`relative path ${JSON.stringify(p)} needs a workspace folder — give an absolute path`);
      return vscode.Uri.joinPath(folder, p.replace(/\\/g, "/"));
    }
    if (!folder || folder.scheme === "file") return vscode.Uri.file(p);
    return folder.with({ path: p.replace(/\\/g, "/") });
  }

  async function documentFor(p) {
    if (!p) {
      const ed = vscode.window.activeTextEditor;
      if (!ed) throw new Error("no active editor — pass 'path'");
      return ed.document;
    }
    const uri = resolveUri(p);
    // An open document carries unsaved edits; prefer it over the disk copy.
    const open = vscode.workspace.textDocuments.find((d) => d.uri.toString() === uri.toString());
    return open || (await vscode.workspace.openTextDocument(uri));
  }

  function fileTabs() {
    const out = [];
    for (const group of vscode.window.tabGroups.all) {
      for (const tab of group.tabs) {
        const uri = tab.input && tab.input.uri;
        if (!uri) continue; // settings, webviews, diffs without a single uri
        out.push({ uri, active: tab.isActive && group.isActive, dirty: tab.isDirty });
      }
    }
    return out;
  }

  // ---- methods ------------------------------------------------------------

  async function workspace() {
    const folders = (vscode.workspace.workspaceFolders || []).map((f) => displayPath(f.uri));
    const ed = vscode.window.activeTextEditor;
    const active = ed ? `${displayPath(ed.document.uri)}:${ed.selection.active.line + 1}` : "(none)";
    const remote = remoteHost(vscode) || "(local)";
    return [
      `name: ${vscode.workspace.name || "(no folder)"}`,
      `remote: ${remote}`,
      `folders: ${folders.length ? folders.join(", ") : "(none)"}`,
      `active: ${active}`,
      `editors: ${fileTabs().length}`,
      `terminals: ${terminals.count()}`,
    ].join("\n");
  }

  async function editors() {
    const tabs = fileTabs();
    if (!tabs.length) return "(no open files)";
    return tabs
      .map((t) => [t.active ? "[active]" : "", t.dirty ? "[modified]" : "", displayPath(t.uri)].filter(Boolean).join(" "))
      .join("\n");
  }

  async function read(p) {
    const doc = await documentFor(p.path);
    const lines = doc.getText().split(/\r?\n/);
    const total = lines.length;
    const start = Math.max(1, Number(p.startLine) || 1);
    const end = Math.min(total, Number(p.endLine) || start + MAX_READ_LINES - 1, start + MAX_READ_LINES - 1);
    const body = [];
    let bytes = 0;
    let last = start - 1;
    for (let i = start; i <= end; i++) {
      const row = `${String(i).padStart(6)}|${lines[i - 1]}`;
      bytes += Buffer.byteLength(row) + 1;
      if (bytes > MAX_READ_BYTES) break;
      body.push(row);
      last = i;
    }
    const flags = doc.isDirty ? ", unsaved changes" : "";
    let out = `${displayPath(doc.uri)} (lines ${start}-${last} of ${total}${flags})`;
    if (body.length) out += "\n" + body.join("\n");
    if (last < total && !(Number(p.endLine) && last >= Number(p.endLine))) {
      out += `\n… ${total - last} more lines — pass startLine=${last + 1} to continue`;
    }
    return out;
  }

  async function open(p) {
    if (!p.path) throw new Error("open requires 'path'");
    const uri = resolveUri(p.path);
    const line = Number(p.line) || 0;
    const options = { preview: false };
    if (line > 0) options.selection = new vscode.Range(line - 1, 0, line - 1, 0);
    await vscode.window.showTextDocument(uri, options);
    return `ok: opened ${displayPath(uri)}${line > 0 ? ":" + line : ""}`;
  }

  async function problems(p) {
    let entries;
    if (p.path) {
      const uri = resolveUri(p.path);
      entries = [[uri, vscode.languages.getDiagnostics(uri)]];
    } else {
      entries = vscode.languages.getDiagnostics();
    }
    const rows = [];
    for (const [uri, diags] of entries) {
      for (const d of diags) {
        rows.push({
          path: displayPath(uri),
          line: d.range.start.line + 1,
          col: d.range.start.character + 1,
          sev: d.severity,
          text: `${SEVERITY[d.severity] || "info"} ${displayPath(uri)}:${d.range.start.line + 1}:${d.range.start.character + 1} ${d.message.replace(/\s*\n\s*/g, " ")}${d.source ? ` [${d.source}]` : ""}`,
        });
      }
    }
    if (!rows.length) return "no problems";
    rows.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : a.sev - b.sev || a.line - b.line || a.col - b.col));
    const shown = rows.slice(0, MAX_PROBLEMS).map((r) => r.text);
    if (rows.length > MAX_PROBLEMS) shown.push(`… ${rows.length - MAX_PROBLEMS} more`);
    return shown.join("\n");
  }

  async function command(p) {
    if (!p.command) throw new Error("command requires 'command' (a VS Code command id)");
    let args = [];
    if (p.args) {
      try {
        args = JSON.parse(p.args);
      } catch (e) {
        throw new Error("args must be a JSON array, e.g. '[\"build\"]'");
      }
      if (!Array.isArray(args)) throw new Error("args must be a JSON array, e.g. '[\"build\"]'");
    }
    const result = await vscode.commands.executeCommand(p.command, ...args);
    let out = `ok: ran ${p.command}`;
    if (result !== undefined) {
      let s;
      try {
        s = JSON.stringify(result);
      } catch (e) {
        s = String(result);
      }
      if (s !== undefined) out += "\n" + (s.length > MAX_RESULT_CHARS ? s.slice(0, MAX_RESULT_CHARS) + "…" : s);
    }
    return out;
  }

  return {
    workspace,
    editors,
    read,
    open,
    problems,
    command,
    terminals: async () => terminals.list(),
    terminal_run: (p) => terminals.run(p),
    terminal_read: async (p) => terminals.read(p),
  };
}

// remoteHost is the Remote-SSH host a window is attached to ("192.168.0.9-doowon"
// from an authority of "ssh-remote+192.168.0.9-doowon"), or "" for a local window.
function remoteHost(vscode) {
  if (!vscode.env.remoteName) return "";
  const f = vscode.workspace.workspaceFolders;
  const authority = f && f.length ? f[0].uri.authority : "";
  const plus = authority.indexOf("+");
  return plus >= 0 ? decodeURIComponent(authority.slice(plus + 1)) : vscode.env.remoteName;
}

module.exports = { createMethods, remoteHost };
