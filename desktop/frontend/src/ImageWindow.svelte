<script>
  // A non-modal, draggable, resizable in-app window for viewing a chat image.
  // No backdrop: the rest of the UI stays interactive (move/resize + keep typing).
  // Zoom: Ctrl+wheel over the image, or the −/％/＋ buttons in the title bar.
  import { chat, closeLightbox } from "./paneStore.svelte.js";

  let win = $state({ x: 160, y: 90, w: 680, h: 520 });
  let zoom = $state(1);
  let drag = null; // { mode, sx, sy, ox, oy, ow, oh, el, pid }

  function onMove(e) {
    if (!drag) return;
    const dx = e.clientX - drag.sx;
    const dy = e.clientY - drag.sy;
    if (drag.mode === "move") {
      win.x = Math.max(0, drag.ox + dx);
      win.y = Math.max(0, drag.oy + dy);
    } else {
      win.w = Math.max(260, drag.ow + dx);
      win.h = Math.max(180, drag.oh + dy);
    }
  }
  function onUp() {
    if (drag?.el && drag.pid != null) {
      try { drag.el.releasePointerCapture(drag.pid); } catch {}
    }
    drag = null;
    window.removeEventListener("pointermove", onMove);
    window.removeEventListener("pointerup", onUp);
  }
  // Pointer events + setPointerCapture: WebView2 can drop plain mousemove while a
  // button is held, so capture the pointer to guarantee move/up delivery.
  function startDrag(mode, e) {
    e.preventDefault();
    const el = e.currentTarget;
    try { el.setPointerCapture(e.pointerId); } catch {}
    drag = { mode, sx: e.clientX, sy: e.clientY, ox: win.x, oy: win.y, ow: win.w, oh: win.h, el, pid: e.pointerId };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  }

  const clampZoom = (z) => Math.min(8, Math.max(0.2, z));
  const zoomIn = () => (zoom = clampZoom(zoom * 1.25));
  const zoomOut = () => (zoom = clampZoom(zoom / 1.25));
  const zoomReset = () => (zoom = 1);

  // Non-passive wheel via a Svelte action so it attaches when the (conditionally
  // rendered) image area mounts, and Ctrl+wheel can preventDefault the app zoom.
  function ctrlWheelZoom(node) {
    const handler = (e) => {
      if (!e.ctrlKey) return;
      e.preventDefault();
      zoom = clampZoom(zoom * (e.deltaY < 0 ? 1.15 : 1 / 1.15));
    };
    node.addEventListener("wheel", handler, { passive: false });
    return { destroy: () => node.removeEventListener("wheel", handler) };
  }

  // Fresh image → back to fit-to-window.
  $effect(() => {
    if (chat.lightboxSrc) zoom = 1;
  });

  // Image bounds in px from the window size: zoom=1 fits, zoom>1 pan-scrolls.
  let maxW = $derived(Math.max(40, (win.w - 24) * zoom));
  let maxH = $derived(Math.max(40, (win.h - 56) * zoom));
</script>

{#if chat.lightboxSrc}
  <div
    class="fixed z-[60] flex flex-col overflow-hidden rounded-lg border border-slate-300 bg-white shadow-2xl"
    style={`left:${win.x}px; top:${win.y}px; width:${win.w}px; height:${win.h}px;`}
  >
    <!-- title bar: drag to move; zoom controls on the right -->
    <div
      class="flex h-8 shrink-0 cursor-move touch-none select-none items-center gap-1 bg-slate-800 px-2 text-white"
      onpointerdown={(e) => startDrag("move", e)}
      role="toolbar"
      tabindex="-1"
    >
      <span class="mr-auto truncate text-xs font-medium">이미지 보기 · 드래그 이동 / Ctrl+휠 확대</span>
      <button class="grid h-6 w-6 place-items-center rounded text-base leading-none hover:bg-white/20" onpointerdown={(e) => e.stopPropagation()} onclick={zoomOut} title="축소 (Ctrl+휠↓)" aria-label="축소">−</button>
      <button class="grid h-6 min-w-[3.2rem] place-items-center rounded px-1 text-[11px] tabular-nums hover:bg-white/20" onpointerdown={(e) => e.stopPropagation()} onclick={zoomReset} title="원래 크기(맞춤)로" aria-label="원래대로">{Math.round(zoom * 100)}%</button>
      <button class="grid h-6 w-6 place-items-center rounded text-base leading-none hover:bg-white/20" onpointerdown={(e) => e.stopPropagation()} onclick={zoomIn} title="확대 (Ctrl+휠↑)" aria-label="확대">＋</button>
      <button class="ml-1 grid h-6 w-6 place-items-center rounded text-sm hover:bg-white/20" onpointerdown={(e) => e.stopPropagation()} onclick={closeLightbox} title="닫기 (Esc)" aria-label="닫기">✕</button>
    </div>

    <!-- image area: fits at 100%, pan-scrolls when zoomed in; Ctrl+wheel zooms -->
    <div use:ctrlWheelZoom class="min-h-0 flex-1 overflow-auto bg-slate-100">
      <div class="flex min-h-full min-w-full items-center justify-center p-2">
        <img src={chat.lightboxSrc} alt="" style={`max-width:${maxW}px; max-height:${maxH}px;`} />
      </div>
    </div>

    <!-- resize handle -->
    <div
      class="absolute bottom-0 right-0 h-4 w-4 cursor-nwse-resize touch-none"
      onpointerdown={(e) => startDrag("resize", e)}
      role="separator"
      aria-label="크기 조절"
    >
      <div class="absolute bottom-1 right-1 h-2 w-2 border-b-2 border-r-2 border-slate-400"></div>
    </div>
  </div>
{/if}
