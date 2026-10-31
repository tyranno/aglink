<script>
  // The reply popup above the voice indicator: the whole last reply, of which
  // only the opening sentences were read aloud. It fades out by itself after
  // a time that grows with the reply's length, but stays while the mouse is
  // over it; the indicator's 원문 button brings it back.
  import { Events } from "@wailsio/runtime";
  import { renderMarkdown } from "./markdown.js";

  const FADE_MS = 300;

  let reply = $state("");
  let fading = $state(false);
  let hovered = false;
  let timer = null;
  let box = $state();
  let shown = $state(0);

  // Replies are Markdown; rendered the same safe way as the chat bubbles
  // (textContent only), so ** and - show as bold and lists, not as symbols.
  function markdown(el, text) {
    const update = (value) => el.replaceChildren(renderMarkdown(value || ""));
    update(text);
    return { update };
  }

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
  <div class="text md" bind:this={box} use:markdown={reply}></div>
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
    word-break: keep-all;
    overflow-wrap: anywhere;
    user-select: text;
  }
  .md :global(> :first-child) {
    margin-top: 0;
  }
  .md :global(> :last-child) {
    margin-bottom: 0;
  }
  .md :global(p) {
    margin: 0 0 8px;
  }
  .md :global(strong) {
    color: #ffffff;
    font-weight: 600;
  }
  .md :global(h1),
  .md :global(h2),
  .md :global(h3),
  .md :global(h4),
  .md :global(h5),
  .md :global(h6) {
    margin: 10px 0 4px;
    font-size: 15px;
    line-height: 1.35;
    font-weight: 600;
  }
  .md :global(ul),
  .md :global(ol) {
    margin: 0 0 8px;
    padding-left: 20px;
  }
  .md :global(li) {
    margin: 2px 0;
  }
  .md :global(blockquote) {
    margin: 0 0 8px;
    padding: 2px 0 2px 10px;
    border-left: 3px solid #4a5270;
    color: #a9afc6;
  }
  .md :global(hr) {
    margin: 8px 0;
    border: 0;
    border-top: 1px solid #2f3547;
  }
  .md :global(table) {
    margin: 0 0 8px;
    border-collapse: collapse;
    font-size: 13px;
    line-height: 1.4;
  }
  .md :global(th),
  .md :global(td) {
    padding: 3px 6px;
    border: 1px solid #3a4158;
    text-align: left;
    vertical-align: top;
  }
  .md :global(th) {
    background: #262b3b;
    font-weight: 600;
  }
  .md :global(a) {
    color: #7aa7ff;
  }
  .md :global(code) {
    padding: 1px 4px;
    border-radius: 4px;
    background: #2a3042;
    font-family: ui-monospace, Consolas, monospace;
    font-size: 13px;
  }
  .md :global(pre) {
    margin: 0 0 8px;
    padding: 8px 10px;
    border-radius: 6px;
    background: #161924;
    overflow-x: auto;
  }
  .md :global(pre code) {
    display: block;
    padding: 0;
    background: transparent;
    white-space: pre;
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
