<script>
  // The reply popup above the voice indicator: the whole last reply, of which
  // only the opening sentences were read aloud. It fades out by itself after
  // a time that grows with the reply's length, but stays while the mouse is
  // over it; the indicator's 원문 button brings it back.
  import { Events } from "@wailsio/runtime";

  const FADE_MS = 300;

  let reply = $state("");
  let fading = $state(false);
  let hovered = false;
  let timer = null;
  let box = $state();
  let shown = $state(0);

  function lingerMs(text) {
    return Math.min(25000, Math.max(8000, 4000 + text.length * 60));
  }

  function arm() {
    clearTimeout(timer);
    fading = false;
    if (!hovered) timer = setTimeout(fadeOut, lingerMs(reply));
  }

  function fadeOut() {
    clearTimeout(timer);
    fading = true;
    timer = setTimeout(() => {
      Events.Emit("reply:hide");
      fading = false;
    }, FADE_MS);
  }

  // Go sends the reply along each time it pops this window up.
  Events.On("reply:show", (event) => {
    reply = event.data?.text || "";
    shown++; // re-mounts the box (scrolled to the top), replaying the pop-in
    arm();
  });
</script>

{#key shown}
<div
  class="toast"
  class:fading
  role="dialog"
  aria-label="답변 원문"
  onmouseenter={() => {
    hovered = true;
    clearTimeout(timer);
    fading = false;
  }}
  onmouseleave={() => {
    hovered = false;
    arm();
  }}
>
  <div class="head">
    <span>답변 원문</span>
    <button onclick={fadeOut} title="닫기">✕</button>
  </div>
  <div class="text" bind:this={box}>{reply}</div>
</div>
{/key}

<style>
  :global(html),
  :global(body) {
    margin: 0;
    height: 100%;
    overflow: hidden;
    background: #1e2230;
  }
  .toast {
    display: flex;
    flex-direction: column;
    height: 100vh;
    color: #eef1ff;
    font: 14px/1.2 "Segoe UI", "Malgun Gothic", sans-serif;
    animation: fadein 0.25s ease-out;
    transition: opacity 0.3s;
  }
  .toast.fading {
    opacity: 0;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex: none;
    height: 34px;
    padding: 0 4px 0 16px;
    font-size: 12px;
    color: #8a90a8;
    border-bottom: 1px solid #2f3547;
    user-select: none;
  }
  .head button {
    width: 34px;
    height: 34px;
    background: none;
    border: 0;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }
  .head button:hover {
    color: #eef1ff;
  }
  .text {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 10px 16px 14px;
    font-size: 14.5px;
    line-height: 1.6;
    white-space: pre-wrap;
    word-break: keep-all;
    overflow-wrap: anywhere;
    user-select: text;
  }
  .text::-webkit-scrollbar {
    width: 8px;
  }
  .text::-webkit-scrollbar-thumb {
    background: #3a4158;
    border-radius: 4px;
  }
  @keyframes fadein {
    from {
      opacity: 0;
      transform: translateY(8px);
    }
  }
</style>
