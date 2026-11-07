<script>
  // 음성 설정: 읽어 주는 목소리·빠르기, 등록한 목소리를 가려 듣는 기준.
  // 값은 voice.svelte.js 가 localStorage 에 저장하고 다음 문장부터 바로 쓴다 —
  // 앱을 다시 켤 필요가 없다. 아래의 "최근 들린 말" 표는 기준을 정할 때 보라고
  // 둔 것으로, 들린 말마다 등록한 목소리와 얼마나 비슷했는지와 받았는지를 보여 준다.
  import { onMount } from "svelte";
  import {
    voice,
    TTS_VOICES,
    VOICE_DEFAULTS,
    SPEED_RANGE,
    SPEAKER_MIN_RANGE,
    WAKE_IDLE_CHOICES,
    WAKE_EVERY_TIME,
    setVoiceSettings,
    setBargeEnabled,
    previewVoice,
    voiceLabel,
    CLONE_VOICE,
    enrollVoice,
    speakerStatus,
    clearSpeaker,
    cloneVoice,
    cloneVoiceStatus,
    clearCloneVoice,
    startTape,
    stopTape,
    openTapeFolder,
    summarizeTape,
    startMeeting,
    stopMeeting,
    requestMeetingAdvice,
    openMeetingFolder,
    assist,
    setDaily,
    refreshMemos,
    deleteMemo,
  } from "./voice.svelte.js";

  let speaker = $state({});
  let clone = $state({});
  let busy = $state(false);

  onMount(async () => {
    speaker = await speakerStatus();
    clone = await cloneVoiceStatus();
    refreshMemos().catch(() => {});
  });

  // {h, m} as "07:30" for a time input, and back.
  const hhmm = (s) => `${String(s.h).padStart(2, "0")}:${String(s.m).padStart(2, "0")}`;
  function setTime(which, value) {
    const [h, m] = String(value).split(":").map(Number);
    if (Number.isFinite(h) && Number.isFinite(m)) setDaily(which, { h, m });
  }

  async function recordClone() {
    busy = true;
    try {
      await cloneVoice();
    } finally {
      clone = await cloneVoiceStatus();
      busy = false;
    }
  }

  async function dropClone() {
    busy = true;
    try {
      await clearCloneVoice();
    } catch {}
    clone = await cloneVoiceStatus();
    busy = false;
  }

  async function enroll() {
    busy = true;
    try {
      await enrollVoice();
    } finally {
      speaker = await speakerStatus();
      busy = false;
    }
  }

  async function unenroll() {
    busy = true;
    try {
      await clearSpeaker();
    } catch {}
    speaker = await speakerStatus();
    busy = false;
  }

  function time(at) {
    return new Date(at).toLocaleTimeString("ko-KR", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  }

  const card = "rounded-lg border border-slate-200 dark:border-slate-700 bg-slate-50/70 dark:bg-slate-800/70 p-4";
  const label = "text-sm font-semibold text-slate-900 dark:text-slate-100";
  const desc = "mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400";
  const field = "rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-2 py-1 text-sm text-slate-700 dark:text-slate-300";
  const button =
    "rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-3 py-1.5 text-xs font-semibold text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700 disabled:opacity-50";
</script>

<section class="rounded-lg border border-slate-200 dark:border-slate-700 bg-white/90 dark:bg-slate-900/90 p-5 shadow-sm">
  <div class="mb-5">
    <div class="text-sm font-semibold text-slate-900 dark:text-slate-100">음성</div>
    <div class="mt-1 text-xs text-slate-500 dark:text-slate-400">
      바꾼 값은 다음 문장부터 바로 쓰여요. 음성 대화 중에 "여자 목소리로 바꿔", "더 천천히 말해"처럼 말로 바꿀 수도 있어요.
    </div>
  </div>

  <div class="space-y-3">
    <div class={card}>
      <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <div class={label}>읽어 주는 목소리</div>
          <div class={desc}>Supertonic 목소리예요. 남자 1~5번, 여자 1~5번이 있어요. 목소리를 녹음했으면 “녹음한 목소리”도 고를 수 있어요.</div>
        </div>
        <div class="flex items-center gap-2">
          <select
            class="rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-3 py-2 text-sm"
            value={voice.ttsVoice}
            onchange={(e) => setVoiceSettings({ ttsVoice: e.currentTarget.value })}
          >
            {#each TTS_VOICES as name}
              <option value={name}>{voiceLabel(name)} ({name}){name === VOICE_DEFAULTS.ttsVoice ? " · 기본" : ""}</option>
            {/each}
            {#if clone.on || voice.ttsVoice === CLONE_VOICE}<option value={CLONE_VOICE}>녹음한 목소리</option>{/if}
          </select>
          <button class={button} onclick={previewVoice}>미리 듣기</button>
        </div>
      </div>
    </div>

    <div class={card}>
      <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <div class={label}>읽는 빠르기</div>
          <div class={desc}>1.0 이 보통이에요. 작을수록 천천히 읽어요.</div>
        </div>
        <div class="flex items-center gap-3">
          <input
            type="range"
            min={SPEED_RANGE[0]}
            max={SPEED_RANGE[1]}
            step="0.02"
            value={voice.ttsSpeed}
            onchange={(e) => setVoiceSettings({ ttsSpeed: Number(e.currentTarget.value) })}
          />
          <span class="w-10 text-right text-sm tabular-nums">{voice.ttsSpeed.toFixed(2)}</span>
          <button class={button} onclick={() => setVoiceSettings({ ttsSpeed: VOICE_DEFAULTS.ttsSpeed })}>기본값</button>
        </div>
      </div>
    </div>

    <div class={card}>
      <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <div class={label}>다른 사람 목소리로 읽기 {clone.on ? "— 녹음 있음" : ""}</div>
          <div class={desc}>허락한 분이 화면에 뜬 글을 25초 동안 읽으면, “녹음한 목소리”를 고르면 그 목소리로 읽어요. 다른 목소리는 그대로예요. 말로 “아내 목소리 녹음해 줘”, “아내 목소리로 바꿔 줘”, “원래 목소리로 돌려 줘”라고 하셔도 돼요.{clone.available === false ? " (목소리 바꾸기 기능이 설치되어 있지 않아요)" : ""}</div>
        </div>
        <div class="flex gap-2">
          <button class={button} disabled={busy || clone.available === false} onclick={recordClone}>녹음하기</button>
          {#if clone.on}<button class={button} disabled={busy} onclick={dropClone}>녹음 지우기</button>{/if}
        </div>
      </div>
    </div>

    <div class={card}>
      <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <div class={label}>긴 답변은 요약만 읽기</div>
          <div class={desc}>켜면 답변의 핵심 두세 문장만 읽어요. “자세히 읽어 줘”라고 하시면 방금 답변을 처음부터 끝까지 읽어요. 끄면 조금 더 길게 읽어요.</div>
        </div>
        <label class="inline-flex items-center gap-2 text-sm font-medium text-slate-700 dark:text-slate-300">
          <input type="checkbox" checked={voice.summary} onchange={(e) => setVoiceSettings({ summary: e.currentTarget.checked })} />
          사용
        </label>
      </div>
    </div>

    <div class={card}>
      <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <div class={label}>답변은 글로만 보여 주기</div>
          <div class={desc}>켜면 말로 시키신 일의 답변을 읽지 않고 글로만 띄워요. 창을 트레이에 두면 오른쪽 아래에 답변 창이 떠서 닫을 때까지 남아요. “글로만 보여 줘”, “다시 말로 해 줘”라고 말해서 바꿀 수도 있고, 부탁 끝에 “글로만 보여 줘”를 붙이면 그 답변만 글로 보여요.</div>
        </div>
        <label class="inline-flex items-center gap-2 text-sm font-medium text-slate-700 dark:text-slate-300">
          <input type="checkbox" checked={voice.textOnly} onchange={(e) => setVoiceSettings({ textOnly: e.currentTarget.checked })} />
          사용
        </label>
      </div>
    </div>

    <div class={card}>
      <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <div class={label}>읽는 중 끼어들기</div>
          <div class={desc}>켜면 제가 읽는 중에 말씀하실 때 읽기를 멈추고 들어요. 끄면 다 읽은 뒤에 마이크가 켜져요.</div>
        </div>
        <label class="inline-flex items-center gap-2 text-sm font-medium text-slate-700 dark:text-slate-300">
          <input type="checkbox" checked={voice.bargeEnabled} onchange={(e) => setBargeEnabled(e.currentTarget.checked)} />
          사용
        </label>
      </div>
    </div>

    <div class={card}>
      <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <div class={label}>앱 안에서 읽기 (에코 제거)</div>
          <div class={desc}>켜면 답변을 앱 안에서 재생해서, 마이크가 제 목소리를 지우고 끼어드는 말씀만 들어요. 앱 안에서 재생할 수 없을 때는 저절로 Windows로 재생해요. 트레이에 둘 때 소리가 나지 않으면 꺼 주세요.</div>
        </div>
        <label class="inline-flex items-center gap-2 text-sm font-medium text-slate-700 dark:text-slate-300">
          <input type="checkbox" checked={voice.pagePlayback} onchange={(e) => setVoiceSettings({ pagePlayback: e.currentTarget.checked })} />
          사용
        </label>
      </div>
    </div>

    <div class={card}>
      <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <div class={label}>듣는 방식</div>
          <div class={desc}>
            {#if voice.listenMode === "wake"}
              평소에는 쉬다가 “아글링크”라고 부르면 대화를 시작해요. “아글링크, 오늘 날씨 어때?”처럼 이어서 말하면 바로 보내요.
            {:else if voice.listenMode === "always"}
              앱을 켜면 바로 음성 대화가 시작되고 계속 들어요. “그만”이라고 하면 쉬고, “아글링크”라고 부르면 다시 들어요.
            {:else}
              🗣 버튼이나 Ctrl+Shift+M, 트레이 메뉴로 켤 때만 들어요.
            {/if}
            {voice.waking ? "지금 부르기를 기다리는 중이에요." : ""}
          </div>
        </div>
        <div class="flex shrink-0 gap-3 text-sm font-medium text-slate-700 dark:text-slate-300">
          {#each [["wake", "부르면 듣기"], ["always", "항상 듣기"], ["manual", "버튼으로만"]] as [mode, name] (mode)}
            <label class="inline-flex items-center gap-1.5">
              <input type="radio" name="listenMode" checked={voice.listenMode === mode} onchange={() => setVoiceSettings({ listenMode: mode })} />
              {name}
            </label>
          {/each}
        </div>
      </div>
      <div class="mt-3 flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <div class={label}>부르는 이름</div>
          <div class={desc}>
            “아글링크” 말고 직접 정한 이름으로도 부를 수 있어요. 쉼표로 나눠 다섯 개까지 적을 수 있어요. 짧은 이름은 적힌 그대로 들려야 깨어나니, 받아 적기가 다르게 나오면 그 모양도 함께 적어 두세요(예: 아그라, 아구라).
            말로 “부르는 이름을 아그라로 바꿔 줘”라고 해도 돼요.
          </div>
        </div>
        <div class="flex shrink-0 flex-col items-end gap-2">
          <input
            class="w-56 rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-2 py-1 text-sm text-slate-700 dark:text-slate-300"
            placeholder="예: 아그라, 자비스"
            value={voice.wakeNames.join(", ")}
            onchange={(e) => setVoiceSettings({ wakeNames: e.currentTarget.value.split(/[,，]/) })}
          />
          <label class="inline-flex items-center gap-1.5 text-sm font-medium text-slate-700 dark:text-slate-300">
            <input
              type="checkbox"
              checked={voice.wakeDefault}
              disabled={!voice.wakeNames.length}
              onchange={(e) => setVoiceSettings({ wakeDefault: e.currentTarget.checked })}
            />
            “아글링크”도 계속 쓰기
          </label>
        </div>
      </div>
      <div class="mt-3 flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <div class={label}>말이 없으면 대기로</div>
          <div class={desc}>“부르면 듣기”일 때, 음성 대화에서 이 시간 동안 아무 말이 없으면 대화를 끝내고 다시 부르기를 기다려요. “매번 부르기”는 대답을 읽고 나면 바로 기다리기로 돌아가서, 말할 때마다 “아글링크”라고 불러야 해요.</div>
        </div>
        <select
          class="rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-2 py-1 text-sm text-slate-700 dark:text-slate-300"
          value={voice.wakeIdleMin}
          disabled={voice.listenMode !== "wake"}
          onchange={(e) => setVoiceSettings({ wakeIdleMin: Number(e.currentTarget.value) })}
        >
          {#each WAKE_IDLE_CHOICES as m (m)}
            <option value={m}>{m === WAKE_EVERY_TIME ? "매번 부르기" : m === 0 ? "끝내지 않음" : `${m}분`}</option>
          {/each}
        </select>
      </div>
    </div>

    <div class={card}>
      <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <div class={label}>녹음</div>
          <div class={desc}>
            “녹음해 줘”라고 하면 대답하지 않고 “녹음 끝내”라고 할 때까지 계속 녹음해요. 소리 파일과 받아 적은 글이 문서 폴더의 “aglink 녹음”에 함께 저장돼요. “방금 녹음 정리해 줘”라고 하면 AI가 요약·결정 사항·할 일로 정리해서 녹음 옆에 “… 요약.md”로 저장해요.
            {voice.taping ? `지금 녹음 중이에요 (${voice.tapeClock || "00:00"}).` : ""}
          </div>
        </div>
        <div class="flex shrink-0 gap-2">
          <button class={button} onclick={() => (voice.taping ? stopTape() : startTape())}>{voice.taping ? "녹음 끝내기" : "녹음 시작"}</button>
          <button class={button} disabled={voice.taping} onclick={() => summarizeTape()}>방금 녹음 정리</button>
          <button class={button} onclick={openTapeFolder}>폴더 열기</button>
        </div>
      </div>
    </div>

    <div class={card}>
      <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <div class={label}>회의 모드</div>
          <div class={desc}>
            “지금부터 듣고 있어”라고 하면 녹음과 달리 소리 파일은 남기지 않고, 주변에서 하는 말을 받아 적어 문서 폴더의 “aglink 회의”에 글로 저장해요.
            “조언해 줘”라고 하면 그때까지 들은 내용을 보고 조언을 말해 드리고(계속 들어요), “그만 들어”라고 하면 끝내요 — 받아 적은 글은 끝낸 뒤에도 파일로 남아요.
            등록한 목소리(사장님)만 시작·조언 요청·끝내기를 할 수 있어요 — 다른 사람 말로는 켜지거나 꺼지지 않아요.
            {voice.meeting ? `지금 회의 모드로 듣고 있어요 (${voice.meetingClock || "00:00"}).` : ""}
          </div>
        </div>
        <div class="flex shrink-0 gap-2">
          <button class={button} onclick={() => (voice.meeting ? stopMeeting() : startMeeting())}>{voice.meeting ? "회의 모드 끝내기" : "회의 모드 시작"}</button>
          <button class={button} disabled={!voice.meeting} onclick={() => requestMeetingAdvice()}>지금까지로 조언 구하기</button>
          <button class={button} onclick={openMeetingFolder}>폴더 열기</button>
        </div>
      </div>
    </div>

    <div class={card}>
      <div class={label}>아침 브리핑 · 저녁 정리</div>
      <div class={desc}>
        아침 브리핑은 정한 시각에 열린 대화로 날짜, 오늘 일정, 날씨, 주요 뉴스를 물어 읽어 드려요(AI 한 번). 저녁 정리는 오늘 메모와 남은 할 일을 읽어요.
        “아침 브리핑 7시 반으로 해 줘”, “브리핑 지역 수원으로”, “지금 브리핑해 줘”, “저녁 정리 9시로 해 줘”라고 말해도 돼요.
      </div>
      <div class="mt-3 flex flex-wrap items-center gap-3 text-sm text-slate-700 dark:text-slate-300">
        <label class="inline-flex items-center gap-1.5">
          <input type="checkbox" checked={assist.briefing.on} onchange={(e) => setDaily("briefing", { on: e.currentTarget.checked })} />
          아침 브리핑
        </label>
        <input type="time" class={field} value={hhmm(assist.briefing)} onchange={(e) => setTime("briefing", e.currentTarget.value)} />
        <input class={`${field} w-24`} placeholder="지역" value={assist.briefing.region} onchange={(e) => setDaily("briefing", { region: e.currentTarget.value.trim() || "서울" })} />
        <label class="inline-flex items-center gap-1.5 md:ml-4">
          <input type="checkbox" checked={assist.evening.on} onchange={(e) => setDaily("evening", { on: e.currentTarget.checked })} />
          저녁 정리
        </label>
        <input type="time" class={field} value={hhmm(assist.evening)} onchange={(e) => setTime("evening", e.currentTarget.value)} />
      </div>
    </div>

    <div class={card}>
      <div class="flex items-center justify-between gap-3">
        <div>
          <div class={label}>메모 · 할 일</div>
          <div class={desc}>“메모해 줘, 내일 견적서 확인”, “할 일 추가: 보고서”, “메모 뭐 있어?”, “2번 메모 지워 줘”라고 말하면 돼요.</div>
        </div>
        {#if assist.memos.length}<button class={button} onclick={() => deleteMemo()}>모두 지우기</button>{/if}
      </div>
      {#if assist.memos.length === 0}
        <div class="mt-3 text-xs text-slate-500 dark:text-slate-400">아직 없어요.</div>
      {:else}
        <ul class="mt-3 space-y-1 text-sm">
          {#each assist.memos as m, i (m.id)}
            <li class="flex items-center gap-2 border-t border-slate-200 dark:border-slate-700 pt-1">
              <span class="w-6 tabular-nums text-xs text-slate-500 dark:text-slate-400">{i + 1}</span>
              {#if m.todo}<span class="text-xs font-semibold text-emerald-600 dark:text-emerald-400">할 일</span>{/if}
              <span class="flex-1 text-slate-800 dark:text-slate-200">{m.text}</span>
              <button class="text-xs text-slate-400 hover:text-rose-600" title="지우기" onclick={() => deleteMemo(m.id)}>✕</button>
            </li>
          {/each}
        </ul>
      {/if}
    </div>

    <div class={card}>
      <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <div class={label}>내 목소리만 듣기</div>
          <div class={desc}>
            {#if !speaker.available}
              목소리 구별 기능이 설치되어 있지 않아요.
            {:else if speaker.enrolled}
              등록한 목소리와 비슷한 정도가 기준보다 낮은 말은 보내지 않아요. 짧은 말과 읽는 중 끼어든 말은 기준을 0.1만큼 낮춰서 봐요.
            {:else}
              아직 등록한 목소리가 없어서 들리는 말을 모두 받아요. 등록하면 15초 동안 평소 말투로 말해 주시면 돼요.
            {/if}
          </div>
        </div>
        {#if speaker.available}
          <div class="flex flex-wrap items-center gap-2">
            <button class={button} disabled={busy} onclick={enroll}>{speaker.enrolled ? "다시 등록" : "등록하기"}</button>
            {#if speaker.enrolled}
              <button class={button} disabled={busy} onclick={unenroll}>등록 해제</button>
            {/if}
          </div>
        {/if}
      </div>
      {#if speaker.enrolled}
        <div class="mt-4 flex items-center gap-3">
          <span class="text-xs text-slate-500 dark:text-slate-400">기준</span>
          <input
            type="range"
            min={SPEAKER_MIN_RANGE[0]}
            max={SPEAKER_MIN_RANGE[1]}
            step="0.05"
            value={voice.speakerMin}
            onchange={(e) => setVoiceSettings({ speakerMin: Number(e.currentTarget.value) })}
          />
          <span class="w-10 text-right text-sm tabular-nums">{voice.speakerMin.toFixed(2)}</span>
          <button class={button} onclick={() => setVoiceSettings({ speakerMin: VOICE_DEFAULTS.speakerMin })}>기본값</button>
        </div>
      {/if}
    </div>

    <div class={card}>
      <div class={label}>최근 들린 말</div>
      <div class={desc}>앱을 켠 뒤 들린 말과 등록한 목소리와 비슷한 정도예요. 기준을 정할 때 참고하세요.</div>
      {#if voice.heardLog.length === 0}
        <div class="mt-3 text-xs text-slate-500 dark:text-slate-400">아직 없어요.</div>
      {:else}
        <table class="mt-3 w-full text-xs">
          <thead>
            <tr class="text-left text-slate-500 dark:text-slate-400">
              <th class="py-1 pr-3 font-medium">시각</th>
              <th class="py-1 pr-3 font-medium">점수</th>
              <th class="py-1 pr-3 font-medium">처리</th>
              <th class="py-1 font-medium">들린 말</th>
            </tr>
          </thead>
          <tbody>
            {#each voice.heardLog as h}
              <tr class="border-t border-slate-200 dark:border-slate-700">
                <td class="py-1 pr-3 tabular-nums text-slate-500 dark:text-slate-400">{time(h.at)}</td>
                <td class="py-1 pr-3 tabular-nums">{h.score.toFixed(2)}</td>
                <td class={`py-1 pr-3 ${h.kept ? "text-emerald-600 dark:text-emerald-400" : "text-rose-600 dark:text-rose-400"}`}>
                  {h.kept ? "받음" : "버림"}{h.why ? ` · ${h.why}` : ""}
                </td>
                <td class="py-1 text-slate-800 dark:text-slate-200">{h.text}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </div>
  </div>
</section>
