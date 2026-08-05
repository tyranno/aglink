// Light/dark/system theme preference, persisted like the sidebar/pane layout
// prefs (localStorage) so it survives a restart. "system" tracks the OS theme
// live via matchMedia instead of only reading it once at startup.
const STORAGE_KEY = "aglink-desktop:theme";

function loadInitialMode() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw === "light" || raw === "dark" || raw === "system") return raw;
  } catch {}
  return "system";
}

export const theme = $state({ mode: loadInitialMode() });

const media = window.matchMedia("(prefers-color-scheme: dark)");

function resolvedDark() {
  return theme.mode === "dark" || (theme.mode === "system" && media.matches);
}

function applyTheme() {
  document.documentElement.classList.toggle("dark", resolvedDark());
}

export function setTheme(mode) {
  theme.mode = mode;
  try {
    localStorage.setItem(STORAGE_KEY, mode);
  } catch {}
  applyTheme();
}

media.addEventListener("change", () => {
  if (theme.mode === "system") applyTheme();
});

applyTheme();
