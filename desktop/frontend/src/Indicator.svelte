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
      <span class="dot {phase}"></span>
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
  .dot {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: #8a90a8;
    flex: none;
  }
  .dot.listening {
    background: #4ade80;
    animation: pulse 1.2s ease-in-out infinite;
  }
  .dot.transcribing,
  .dot.waiting {
    background: #fbbf24;
    animation: pulse 2s ease-in-out infinite;
  }
  .label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dot.problem {
    background: #f87171;
  }
  .dot.speaking {
    background: #60a5fa;
    animation: pulse 0.8s ease-in-out infinite;
  }
  @keyframes pulse {
    50% {
      opacity: 0.35;
    }
  }
</style>
