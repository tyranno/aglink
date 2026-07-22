<script>
  // Non-modal, draggable, resizable in-app image viewer (no backdrop → the rest
  // of the UI stays usable). Zoom: Ctrl+wheel or −/＋/맞춤/원본. When zoomed in,
  // grab the image and drag to pan the parts that overflow the window.
  import { chat, closeLightbox } from "./paneStore.svelte.js";

  let win = $state({ x: 160, y: 90, w: 700, h: 540 });
  let zoom = $state(1); // scale vs the image's natural pixels (1 = 100%/원본)
  let natW = $state(0);
  let natH = $state(0);
  let panning = $state(false);
  let scrollEl; // the overflow-auto image area
  let drag = null; // title-bar move / resize
  let pan = null; // grab-to-pan the zoomed image

  const clampZoom = (z) => Math.min(8, Math.max(0.05, z));

  function fitScale() {
    const cw = win.w - 24;
    const ch = win.h - 56;
    if (!natW || !natH || cw <= 0 || ch <= 0) return 1;
    return Math.min(cw / natW, ch / natH);
  }
  const fitToWindow = () => (zoom = clampZoom(fitScale())); // 맞춤
  const actualSize = () => (zoom = 1); //                     원본(1:1)
  const zoomIn = () => (zoom = clampZoom(zoom * 1.25));
  const zoomOut = () => (zoom = clampZoom(zoom / 1.25));

  function onImgLoad(e) {
    natW = e.currentTarget.naturalWidth || 0;
    natH = e.currentTarget.naturalHeight || 0;
    fitToWindow();
  }

  // Title-bar move / corner resize. setPointerCapture + INLINE pointer handlers on
  // the captured element (WebView2 doesn't forward captured pointer moves to window).
  function startDrag(mode, e) {
    e.preventDefault();
    try { e.currentTarget.setPointerCapture(e.pointerId); } catch {}
    drag = { mode, sx: e.clientX, sy: e.clientY, ox: win.x, oy: win.y, ow: win.w, oh: win.h };
  }
  function onDragMove(e) {
    if (!drag) return;
    const dx = e.clientX - drag.sx;
    const dy = e.clientY - drag.sy;
    if (drag.mode === "move") {
      win.x = Math.max(0, drag.ox + dx);
      win.y = Math.max(0, drag.oy + dy);
    } else {
      win.w = Math.max(280, drag.ow + dx);
      win.h = Math.max(200, drag.oh + dy);
    }
  }
  function endDrag(e) {
    try { e.currentTarget.releasePointerCapture(e.pointerId); } catch {}
    drag = null;
  }

  // Grab-to-pan: drag inside the image area scrolls the overflow (what the user
  // means by "drag" — reveal the hidden parts of a zoomed-in image).
  function panStart(e) {
    if (e.button !== 0 || !scrollEl) return;
    pan = { sx: e.clientX, sy: e.clientY, sl: scrollEl.scrollLeft, st: scrollEl.scrollTop };
    panning = true;
    try { scrollEl.setPointerCapture(e.pointerId); } catch {}
  }
  function panMove(e) {
    if (!pan || !scrollEl) return;
    scrollEl.scrollLeft = pan.sl - (e.clientX - pan.sx);
    scrollEl.scrollTop = pan.st - (e.clientY - pan.sy);
  }
  function panEnd(e) {
    pan = null;
    panning = false;
    try { scrollEl.releasePointerCapture(e.pointerId); } catch {}
  }

  // Non-passive wheel via a Svelte action so Ctrl+wheel can preventDefault app zoom.
  function ctrlWheelZoom(node) {
    const handler = (e) => {
      if (!e.ctrlKey) return;
      e.preventDefault();
      zoom = clampZoom(zoom * (e.deltaY < 0 ? 1.15 : 1 / 1.15));
    };
    node.addEventListener("wheel", handler, { passive: false });
    return { destroy: () => node.removeEventListener("wheel", handler) };
  }

  $effect(() => {
    chat.lightboxSrc;
    natW = 0;
    natH = 0;
  });
</script>

{#if chat.lightboxSrc}
  <div
    class="fixed z-[60] flex flex-col overflow-hidden rounded-lg border border-slate-300 bg-white shadow-2xl"
    style={`left:${win.x}px; top:${win.y}px; width:${win.w}px; height:${win.h}px;`}
  >
    <!-- title bar: drag to move window; zoom controls on the right -->
    <div
      class="flex h-8 shrink-0 cursor-move touch-none select-none items-center gap-1 bg-slate-800 px-2 text-white"
      onpointerdown={(e) => startDrag("move", e)}
      onpointermove={onDragMove}
      onpointerup={endDrag}
      role="toolbar"
      tabindex="-1"
    >
      <span class="mr-auto truncate text-xs font-medium">이미지 보기</span>
      <button class="grid h-6 w-6 place-items-center rounded text-base leading-none hover:bg-white/20" onpointerdown={(e) => e.stopPropagation()} onclick={zoomOut} title="축소 (Ctrl+휠↓)" aria-label="축소">−</button>
      <span class="min-w-[3rem] text-center text-[11px] tabular-nums">{Math.round(zoom * 100)}%</span>
      <button class="grid h-6 w-6 place-items-center rounded text-base leading-none hover:bg-white/20" onpointerdown={(e) => e.stopPropagation()} onclick={zoomIn} title="확대 (Ctrl+휠↑)" aria-label="확대">＋</button>
      <button class="ml-1 grid h-6 w-6 place-items-center rounded hover:bg-white/20" onpointerdown={(e) => e.stopPropagation()} onclick={fitToWindow} title="창에 맞추기" aria-label="맞춤">
        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 4H6a2 2 0 0 0-2 2v3M15 4h3a2 2 0 0 1 2 2v3M9 20H6a2 2 0 0 1-2-2v-3M15 20h3a2 2 0 0 0 2-2v-3"/></svg>
      </button>
      <button class="grid h-6 w-6 place-items-center rounded text-[10px] font-bold leading-none hover:bg-white/20" onpointerdown={(e) => e.stopPropagation()} onclick={actualSize} title="원래 크기 (100%)" aria-label="원본">1:1</button>
      <button class="ml-1 grid h-6 w-6 place-items-center rounded text-sm hover:bg-white/20" onpointerdown={(e) => e.stopPropagation()} onclick={closeLightbox} title="닫기 (Esc)" aria-label="닫기">✕</button>
    </div>

    <!-- image area: Ctrl+wheel zoom; grab-drag to pan when zoomed in -->
    <div
      bind:this={scrollEl}
      use:ctrlWheelZoom
      class={`min-h-0 flex-1 overflow-auto bg-slate-100 ${panning ? "cursor-grabbing" : "cursor-grab"}`}
      onpointerdown={panStart}
      onpointermove={panMove}
      onpointerup={panEnd}
      onpointercancel={panEnd}
      ondragstart={(e) => e.preventDefault()}
    >
      <div class="flex min-h-full min-w-full items-center justify-center p-2">
        <img
          src={chat.lightboxSrc}
          alt=""
          draggable="false"
          onload={onImgLoad}
          style={natW ? `width:${Math.round(natW * zoom)}px; height:${Math.round(natH * zoom)}px; max-width:none; max-height:none;` : ""}
        />
      </div>
    </div>

    <!-- resize handle -->
    <div
      class="absolute bottom-0 right-0 h-4 w-4 cursor-nwse-resize touch-none"
      onpointerdown={(e) => startDrag("resize", e)}
      onpointermove={onDragMove}
      onpointerup={endDrag}
      role="separator"
      aria-label="크기 조절"
    >
      <div class="absolute bottom-1 right-1 h-2 w-2 border-b-2 border-r-2 border-slate-400"></div>
    </div>
  </div>
{/if}
