// State for the 빠른 질문 box (QuickAsk.svelte). In-app callers (Ctrl+K,
// the home card) set a request; the global hotkey arrives as a Wails event.
export const quickAsk = $state({ open: false, request: null });

export function openQuickAsk(clipboard = "") {
  quickAsk.request = { clipboard };
  quickAsk.open = true;
}
