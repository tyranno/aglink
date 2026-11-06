<script>
  // 회의 사이드바 패널. Starts and ends meeting mode with a button (the same
  // as saying "지금부터 듣고 있어"), and lists the meetings written down so far
  // in Documents\aglink 회의 (meeting.go), newest first. A meeting opens in
  // the app's file preview (onopen) to read what was said.
  import { VoiceService } from "../bindings/github.com/tyranno/aglink-desktop";
  import { voice, startMeeting, stopMeeting, requestMeetingAdvice, openMeetingFolder } from "./voice.svelte.js";

  let { onopen = () => {} } = $props();

  const REFRESH_MS = 10000; // the live meeting's line count keeps growing

  let meetings = $state([]);
  let loadError = $state("");
  let loading = $state(false);
  let query = $state("");
  let deleting = $state(""); // path waiting for the second "삭제" click

  async function load() {
    loading = true;
    try {
      meetings = JSON.parse((await VoiceService.MeetingList()) || "[]");
      loadError = "";
    } catch (error) {
      loadError = `회의 목록을 불러오지 못했어요: ${error?.message || error}`;
    } finally {
      loading = false;
    }
  }

  async function remove(m) {
    if (deleting !== m.path) {
      deleting = m.path;
      return;
    }
    deleting = "";
    try {
      await VoiceService.MeetingDelete(m.path);
    } catch (error) {
      loadError = `지우지 못했어요: ${error?.message || error}`;
    }
    await load();
  }

  function dayLabel(started) {
    const [date, time] = (started || "").split(" ");
    const today = new Date();
    const key = (d) => `${d.getFullYear()}-${`${d.getMonth() + 1}`.padStart(2, "0")}-${`${d.getDate()}`.padStart(2, "0")}`;
    const yesterday = new Date(today.getTime() - 86400000);
    const day = date === key(today) ? "오늘" : date === key(yesterday) ? "어제" : date;
    return `${day} ${(time || "").slice(0, 5)}`;
  }

  function sizeLabel(bytes) {
    return bytes >= 1024 ? `${Math.round(bytes / 1024)}KB` : `${bytes}B`;
  }

  const shown = $derived(
    query.trim() ? meetings.filter((m) => `${m.name} ${m.preview}`.toLowerCase().includes(query.trim().toLowerCase())) : meetings,
  );

  // Starting or ending meeting mode adds or closes a file: list it right away.
  $effect(() => {
    void voice.meeting;
    const t = setTimeout(load, 300);
    return () => clearTimeout(t);
  });

  $effect(() => {
    const timer = setInterval(load, REFRESH_MS);
    return () => clearInterval(timer);
  });
</script>

<div class="flex h-full min-h-0 flex-col">
  <div class="flex h-11 shrink-0 items-center gap-2 border-b border-slate-200 dark:border-slate-700 px-3">
    <div class="min-w-0 flex-1 truncate text-sm font-semibold text-slate-900 dark:text-slate-100">회의</div>
    <button class="grid h-8 w-8 place-items-center rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={openMeetingFolder} title="회의 폴더 열기" aria-label="회의 폴더 열기">📂</button>
    <button class="grid h-8 w-8 place-items-center rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700 disabled:opacity-50" disabled={loading} onclick={load} title="새로고침" aria-label="새로고침">↻</button>
  </div>

  <div class="shrink-0 border-b border-slate-200 dark:border-slate-700 px-3 py-3">
    {#if voice.meeting}
      <div class="mb-2 flex items-center gap-2 text-sm font-semibold text-rose-600 dark:text-rose-400">
        <span class="h-2 w-2 animate-pulse rounded-full bg-rose-500"></span>
        회의 모드로 듣는 중 · {voice.meetingClock || "00:00"}
      </div>
      <div class="flex gap-2">
        <button class="flex-1 rounded-md bg-rose-600 px-3 py-2 text-sm font-semibold text-white hover:bg-rose-700" onclick={() => stopMeeting()}>■ 회의 끝내기</button>
        <button class="flex-1 rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-3 py-2 text-sm font-medium text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700" onclick={() => requestMeetingAdvice()}>💡 조언 구하기</button>
      </div>
    {:else}
      <button class="w-full rounded-md bg-blue-600 px-3 py-2 text-sm font-semibold text-white hover:bg-blue-700" onclick={() => startMeeting()}>🎙 회의 모드 시작</button>
      <div class="mt-2 text-[11px] leading-4 text-slate-500 dark:text-slate-400">주변에서 하는 말을 받아 적어 날짜·시간 이름의 글 파일로 저장해요. 소리는 저장하지 않아요.</div>
    {/if}
  </div>

  <div class="shrink-0 px-3 pt-3">
    <input class="w-full rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-2 py-1.5 text-sm text-slate-800 dark:text-slate-200 placeholder:text-slate-400" placeholder="회의 내용 검색" bind:value={query} />
  </div>

  <div class="min-h-0 flex-1 overflow-y-auto px-3 py-3">
    {#if loadError}
      <div class="mb-2 rounded bg-rose-50 dark:bg-rose-950/40 px-2 py-1 text-xs text-rose-700 dark:text-rose-300">{loadError}</div>
    {/if}
    {#if shown.length === 0 && !loadError}
      <div class="px-1 py-3 text-sm text-slate-500 dark:text-slate-400">{query.trim() ? "찾는 회의가 없어요." : "아직 저장된 회의가 없어요."}</div>
    {/if}
    {#each shown as m (m.path)}
      <div class="mb-2 rounded-lg border border-slate-200 dark:border-slate-700 bg-white/85 dark:bg-slate-900/85 shadow-sm">
        <button class="block w-full px-3 pt-2 pb-1 text-left hover:bg-slate-50 dark:hover:bg-slate-800/60" onclick={() => onopen(m.path)} title="내용 보기">
          <div class="flex items-center gap-2">
            <span class="min-w-0 flex-1 truncate text-sm font-semibold text-slate-900 dark:text-slate-100">{dayLabel(m.started)}</span>
            {#if m.live}
              <span class="shrink-0 rounded-full bg-rose-600 px-1.5 py-0.5 text-[10px] font-bold text-white">듣는 중</span>
            {/if}
            <span class="shrink-0 text-[10px] text-slate-400 dark:text-slate-500">{m.lines}줄 · {sizeLabel(m.bytes)}</span>
          </div>
          <div class="mt-1 line-clamp-2 text-xs leading-4 text-slate-500 dark:text-slate-400">{m.preview || "받아 적은 말이 없어요."}</div>
        </button>
        <div class="flex justify-end gap-1 px-2 pb-1.5">
          <button class="rounded px-2 py-0.5 text-[11px] text-blue-600 dark:text-blue-400 hover:bg-blue-50 dark:hover:bg-blue-950/40" onclick={() => onopen(m.path)}>보기</button>
          {#if !m.live}
            <button class={`rounded px-2 py-0.5 text-[11px] ${deleting === m.path ? "bg-rose-600 font-semibold text-white" : "text-rose-600 dark:text-rose-400 hover:bg-rose-50 dark:hover:bg-rose-950/40"}`} onclick={() => remove(m)} onblur={() => deleting === m.path && (deleting = "")}>{deleting === m.path ? "정말 지우기" : "지우기"}</button>
          {/if}
        </div>
      </div>
    {/each}
  </div>
</div>
