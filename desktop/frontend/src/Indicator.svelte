<script>
  // The voice indicator window: shows what voice conversation is doing while
  // the main window is in the tray. State comes from the main window's
  // "voice:state" events; clicking the status line opens the app, 원문 shows
  // the last reply again (ReplyToast.svelte), ✕ ends the conversation.
  import { Events } from "@wailsio/runtime";

  let label = $state("음성 대화 중");
  let phase = $state("waiting");
  let barge = $state(true);
  let hasReply = $state(false);

  Events.On("voice:state", (event) => {
    const d = event.data || {};
    phase = d.phase || "waiting";
    label = d.label || "음성 대화 중";
    if (typeof d.barge === "boolean") barge = d.barge;
  });

  Events.On("voice:reply", (event) => {
    if (event.data?.text) hasReply = true;
  });
</script>

<div class="box">
  <div class="pill">
    <button class="main" onclick={() => Events.Emit("indicator:open")} title="aglink 열기">
      <!-- Each phase has its own shape, not just a colour: ripple = listening,
           spinner = working out the words, typing dots = waiting for the reply,
           bars = reading it out. -->
      <span class="icon {phase}" aria-hidden="true">
        {#if phase === "listening"}
          <span class="core"></span><span class="ripple"></span>
        {:else if phase === "transcribing"}
          <span class="spin"></span>
        {:else if phase === "speaking"}
          <span class="bar"></span><span class="bar"></span><span class="bar"></span><span class="bar"></span>
        {:else if phase === "problem"}
          <span class="warn">!</span>
        {:else if phase === "note"}
          <span class="core"></span>
        {:else}
          <span class="d"></span><span class="d"></span><span class="d"></span>
        {/if}
      </span>
      <span class="label" title={label}>{label}</span>
    </button>
    {#if hasReply}
      <button class="mode" onclick={() => Events.Emit("reply:toggle")} title="마지막 답변 원문 보기">원문</button>
    {/if}
    <button
      class="mode"
      onclick={() => Events.Emit("tray:barge")}
      title={barge ? "읽는 중에 말하면 멈추고 들어요 — 눌러서 주고받기로" : "다 읽은 뒤에 들어요 — 눌러서 끼어들기로"}
      >{barge ? "끼어들기" : "주고받기"}</button
    >
    <button class="close" onclick={() => Events.Emit("tray:conversation")} title="음성 대화 끝내기">✕</button>
  </div>
</div>

<style>
  :global(html),
  :global(body) {
    margin: 0;
    height: 100%;
    overflow: hidden;
    background: #1e2230;
  }
  .box {
    display: flex;
    flex-direction: column;
    height: 100vh;
    color: #eef1ff;
    font: 14px/1.2 "Segoe UI", "Malgun Gothic", sans-serif;
  }
  .pill {
    display: flex;
    align-items: center;
    flex: none;
    height: 52px;
    user-select: none;
  }
  button {
    background: none;
    border: 0;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }
  .main {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 10px;
    height: 100%;
    padding: 0 6px 0 16px;
    text-align: left;
  }
  .mode {
    flex: none;
    margin-right: 6px;
    height: 26px;
    padding: 0 10px;
    border: 1px solid #3a4158;
    border-radius: 13px;
    font-size: 12px;
    opacity: 0.85;
  }
  .mode:hover {
    opacity: 1;
    background: #2a3044;
  }
  .close {
    width: 40px;
    height: 100%;
    opacity: 0.6;
  }
  .close:hover {
    opacity: 1;
  }
  .label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .icon {
    position: relative;
    flex: none;
    width: 22px;
    height: 22px;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 2px;
  }
  /* listening: green dot sending out a ripple */
  .core {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: #4ade80;
  }
  .ripple {
    position: absolute;
    inset: 6px;
    border-radius: 50%;
    border: 2px solid #4ade80;
    animation: ripple 1.4s ease-out infinite;
  }
  @keyframes ripple {
    from {
      transform: scale(0.6);
      opacity: 1;
    }
    to {
      transform: scale(1.9);
      opacity: 0;
    }
  }
  /* transcribing: amber spinner */
  .spin {
    width: 14px;
    height: 14px;
    border-radius: 50%;
    border: 3px solid rgba(251, 191, 36, 0.25);
    border-top-color: #fbbf24;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  /* waiting for the reply: three dots typing */
  .d {
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: #a5b4fc;
    animation: typing 1.2s ease-in-out infinite;
  }
  .d:nth-child(2) {
    animation-delay: 0.2s;
  }
  .d:nth-child(3) {
    animation-delay: 0.4s;
  }
  @keyframes typing {
    0%,
    60%,
    100% {
      transform: translateY(0);
      opacity: 0.4;
    }
    30% {
      transform: translateY(-5px);
      opacity: 1;
    }
  }
  /* speaking: blue equaliser bars */
  .bar {
    width: 3px;
    height: 16px;
    border-radius: 2px;
    background: #60a5fa;
    transform-origin: center;
    animation: eq 0.9s ease-in-out infinite;
  }
  .bar:nth-child(2) {
    animation-delay: 0.15s;
  }
  .bar:nth-child(3) {
    animation-delay: 0.3s;
  }
  .bar:nth-child(4) {
    animation-delay: 0.45s;
  }
  @keyframes eq {
    0%,
    100% {
      transform: scaleY(0.3);
    }
    50% {
      transform: scaleY(1);
    }
  }
  /* problem: red badge, no motion */
  .warn {
    width: 18px;
    height: 18px;
    border-radius: 50%;
    background: #f87171;
    color: #1e2230;
    font-weight: 700;
    font-size: 13px;
    line-height: 18px;
    text-align: center;
  }
  .icon.note .core {
    background: #c4b5fd;
  }
</style>
