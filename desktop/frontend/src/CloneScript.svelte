<script>
  // The text read aloud while a voice is recorded (cloneVoice), shown large in
  // the middle of the window with how long is left — the one-line voice note
  // is too small to read from a step away.
  import { voice } from "./voice.svelte.js";

  let now = $state(Date.now());

  $effect(() => {
    if (!voice.readScript) return;
    const t = setInterval(() => (now = Date.now()), 250);
    return () => clearInterval(t);
  });

  const left = $derived(voice.readUntil ? Math.max(0, Math.ceil((voice.readUntil - now) / 1000)) : 0);
</script>

{#if voice.readScript}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-950/50 px-6">
    <div class="w-full max-w-3xl rounded-xl border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 p-8 shadow-2xl">
      <div class="flex items-center gap-3 text-base font-semibold">
        {#if voice.readUntil}
          <span class="inline-block h-3 w-3 animate-pulse rounded-full bg-rose-600"></span>
          <span class="text-rose-600">녹음 중 · {left}초 남음</span>
        {:else}
          <span class="text-slate-500 dark:text-slate-400">"띵" 소리가 나면 아래 글을 읽어 주세요</span>
        {/if}
      </div>
      <p class="mt-6 text-2xl leading-[1.9] text-slate-900 dark:text-slate-100">{voice.readScript}</p>
      <div class="mt-6 text-sm text-slate-500 dark:text-slate-400">평소 말투로 또박또박, 25초를 꽉 채워 읽어 주세요. 다 읽으면 처음부터 다시 읽으셔도 돼요.</div>
    </div>
  </div>
{/if}
