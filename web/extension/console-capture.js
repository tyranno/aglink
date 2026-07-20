// Runs in the MAIN world (the page's own JS realm, not the extension's
// isolated world) on every page load, so it can override the page's actual
// console object — an isolated-world script has its own separate `window`
// and would only see its own console calls, never the page's.
//
// Purely observational: buffers messages, always forwards to the real
// console via .apply() afterward, and never blocks or changes behavior. Safe
// to run unconditionally on every page, unlike a functional override (e.g.
// window.confirm) that would change what the page/user actually experiences.
(function () {
  if (window.__aglinkConsole) return; // already installed (re-injection guard)
  const MAX_ENTRIES = 200;
  const buf = [];
  window.__aglinkConsole = buf;

  function record(level, text) {
    buf.push({ level, time: Date.now(), text });
    if (buf.length > MAX_ENTRIES) buf.shift();
  }

  ["log", "warn", "error", "info", "debug"].forEach((level) => {
    const original = console[level];
    console[level] = function (...args) {
      try {
        record(
          level,
          args
            .map((a) => {
              try {
                return typeof a === "string" ? a : JSON.stringify(a);
              } catch (e) {
                return String(a);
              }
            })
            .join(" ")
        );
      } catch (e) {
        // never let capture failure break the page's own logging
      }
      return original.apply(console, args);
    };
  });

  window.addEventListener("error", (e) => {
    record("uncaught", `${e.message} (${e.filename}:${e.lineno}:${e.colno})`);
  });
  window.addEventListener("unhandledrejection", (e) => {
    record("unhandledrejection", String(e.reason));
  });
})();
