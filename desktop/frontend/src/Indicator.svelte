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
  let speed = $state(0);
  let chatTitle = $state("");

  Events.On("voice:state", (event) => {
    const d = event.data || {};
    phase = d.phase || "waiting";
    label = (d.label || "음성 대화 중").replace(/^🗣\s*/u, ""); // the icon already says it
    if (typeof d.barge === "boolean") barge = d.barge;
    if (Number.isFinite(d.speed)) speed = d.speed;
    chatTitle = typeof d.chat === "string" ? d.chat : "";
  });

  Events.On("voice:reply", (event) => {
    if (event.data?.text) hasReply = true;
  });
</script>

<div class="box">
  <div class="pill">
    <button class="main" onclick={() => Events.Emit("indicator:open")} title="aglink 열기">
      <!-- Each phase is its own pictogram, the same as the tray icon, so the
           state is told by shape rather than colour: mic = listening, lines =
           working out the words, hourglass = waiting for the reply, speaker =
           reading it out, ! = problem, ring and dot = recording. -->
      <svg class="icon {phase}" viewBox="0 0 24 24" aria-hidden="true">
        {#if phase === "listening"}
          <rect x="9" y="3" width="6" height="11" rx="3" class="fill" />
          <path d="M6 11a6 6 0 0 0 12 0M12 17v4M8.5 21h7" />
        {:else if phase === "transcribing"}
          <path class="ln l1" d="M5 7h14" /><path class="ln l2" d="M5 12h14" /><path class="ln l3" d="M5 17h8" />
        {:else if phase === "speaking"}
          <path d="M4 9.5h3.5L12 5.5v13l-4.5-4H4z" class="fill" />
          <path class="wave w1" d="M15 9.5a3.5 3.5 0 0 1 0 5" />
          <path class="wave w2" d="M17.5 7a7 7 0 0 1 0 10" />
        {:else if phase === "problem"}
          <path d="M12 4 3 20h18z" /><path d="M12 10v4.5M12 17.2v.3" />
        {:else if phase === "done"}
          <circle cx="12" cy="12" r="8.5" /><path d="M8 12.5l2.8 2.8L16.5 9.5" />
        {:else if phase === "recording"}
          <circle cx="12" cy="12" r="8.5" /><circle cx="12" cy="12" r="4" class="fill dot" />
        {:else if phase === "note"}
          <circle cx="12" cy="12" r="8.5" /><path d="M12 11v5.5M12 7.8v.3" />
        {:else}
          <g class="glass">
            <path d="M6.5 3.5h11M6.5 20.5h11M8 3.5c0 5 8 5 8 8.5s-8 3.5-8 8.5M16 3.5c0 5-8 5-8 8.5s8 3.5 8 8.5" />
            <path d="M9.5 19.5h5l-2.5-3z" class="fill" />
          </g>
        {/if}
      </svg>
      <span class="text">
        <span class="label" title={label}>{label}</span>
        {#if chatTitle || speed}
          <span class="sub">
            {#if chatTitle}<span class="chat" title={chatTitle}>💬 {chatTitle}</span>{/if}
            {#if chatTitle && speed}<span>·</span>{/if}
            {#if speed}<span class="speed">읽기 속도 {Math.round(speed * 100) / 100}배</span>{/if}
          </span>
        {/if}
      </span>
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
    <button class="close" onclick={() => Events.Emit("tray:conversation")} title={phase === "recording" ? "녹음 끝내기" : "음성 대화 끝내기"}>✕</button>
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
  .text {
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .sub {
    display: flex;
    gap: 4px;
    min-width: 0;
    font-size: 11px;
    color: rgba(238, 241, 255, 0.6);
    white-space: nowrap;
  }
  .chat {
    min-width: 0;
    opacity: 1;
    color: #fff;
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .speed {
    flex: none;
  }
  .label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .icon {
    flex: none;
    width: 22px;
    height: 22px;
    fill: none;
    stroke: currentColor;
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .icon .fill {
    fill: currentColor;
    stroke: none;
  }
  /* recording: the dot blinks */
  .icon.recording .dot {
    animation: blink 1.2s ease-in-out infinite;
  }
  @keyframes blink {
    50% {
      opacity: 0.25;
    }
  }
  /* listening: the mic breathes */
  .icon.listening {
    animation: breathe 1.4s ease-in-out infinite;
  }
  @keyframes breathe {
    50% {
      transform: scale(0.82);
      opacity: 0.6;
    }
  }
  /* transcribing: lines being written one after another */
  .ln {
    stroke-dasharray: 14;
    animation: write 1.5s ease-in-out infinite;
  }
  .l2 {
    animation-delay: 0.25s;
  }
  .l3 {
    animation-delay: 0.5s;
  }
  @keyframes write {
    0% {
      stroke-dashoffset: 14;
    }
    50%,
    100% {
      stroke-dashoffset: 0;
    }
  }
  /* waiting: the hourglass turns over */
  .glass {
    transform-origin: 12px 12px;
    animation: flip 2s ease-in-out infinite;
  }
  @keyframes flip {
    0%,
    60% {
      transform: rotate(0);
    }
    100% {
      transform: rotate(180deg);
    }
  }
  /* speaking: sound waves come and go */
  .wave {
    animation: wave 1s ease-in-out infinite;
  }
  .w2 {
    animation-delay: 0.25s;
  }
  @keyframes wave {
    0%,
    100% {
      opacity: 0.2;
    }
    50% {
      opacity: 1;
    }
  }
</style>
