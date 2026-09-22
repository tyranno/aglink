// Placeholder until the terminal support lands: lets the extension activate
// and report terminals as unavailable rather than failing to load.
"use strict";

function createTerminals(vscode) {
  return {
    count: () => vscode.window.terminals.length,
    list: () => "terminal support not built yet",
    run: async () => {
      throw new Error("terminal support not built yet");
    },
    read: () => "terminal support not built yet",
    dispose: () => {},
  };
}

module.exports = { createTerminals };
