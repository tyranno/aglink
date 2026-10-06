<script>
  // 사용량 사이드바 패널. 호스트의 get_usage_stats 결과(전체 합계 + 대화별
  // 누적/최근 턴 통계)를 보여주고, 대화별 모델 고정(set_conv_model)을 바꾼다.
  // 탭이 열려 있고 창이 보이는 동안 15초마다 자동으로 다시 읽어온다 — 이
  // 컴포넌트는 사이드바 탭이 "usage"일 때만 마운트되므로 언마운트 시 타이머를
  // 정리하면 "탭이 보일 때만" 조건이 자연히 성립한다.
  import { onMount } from "svelte";
  import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";

  const REFRESH_MS = 15000;
  const PRESET_MODELS = ["sonnet", "opus", "haiku"];

  let stats = $state(null);
  let loadError = $state("");
  let loading = $state(false);
  let expanded = $state({}); // convKey → bool (최근 턴 표 펼침)
  let pinBusy = $state({}); // convKey → bool
  let pinError = $state({}); // convKey → string
  let customModel = $state({}); // convKey → 직접 입력 중인 모델명 (직접 입력 모드일 때만 키가 있음)

  function convKey(c) {
    return `${c.kind}:${c.project || ""}:${c.id || ""}`;
  }

  async function load() {
    if (loading) return;
    loading = true;
    try {
      const raw = await ControlService.GetUsageStats("{}");
      const data = JSON.parse(raw || "{}");
      stats = data;
      loadError = data?.error || "";
    } catch (e) {
      loadError = "사용량 통계를 불러오지 못했습니다.";
    }
    loading = false;
  }

  // onMount (not $effect): load() reads `loading`, which would make an effect
  // re-run itself on every load.
  onMount(() => {
    load();
    const t = setInterval(() => {
      if (document.visibilityState === "visible") load();
    }, REFRESH_MS);
    const onVis = () => { if (document.visibilityState === "visible") load(); };
    document.addEventListener("visibilitychange", onVis);
    return () => {
      clearInterval(t);
      document.removeEventListener("visibilitychange", onVis);
    };
  });

  // 텔레그램 대화는 target을 생략해야 한다(호스트 기본값).
  function targetOf(c) {
    if (c.kind === "telegram") return undefined;
    const t = { kind: c.kind, id: c.id };
    if (c.project) t.project = c.project;
    return t;
  }

  async function setModel(c, model) {
    const k = convKey(c);
    if (pinBusy[k]) return;
    pinBusy = { ...pinBusy, [k]: true };
    pinError = { ...pinError, [k]: "" };
    try {
      const payload = { model: (model || "").trim() };
      const t = targetOf(c);
      if (t) payload.target = t;
      const raw = await ControlService.SetConvModel(JSON.stringify(payload));
      const j = JSON.parse(raw || "{}");
      if (j && j.ok === false) pinError = { ...pinError, [k]: j.error || "요청 실패" };
      else {
        const next = { ...customModel };
        delete next[k];
        customModel = next;
      }
    } catch (e) {
      pinError = { ...pinError, [k]: "연결이 끊겨 처리하지 못했습니다." };
    }
    pinBusy = { ...pinBusy, [k]: false };
    await load();
  }

  function selectValue(c) {
    const k = convKey(c);
    if (k in customModel) return "__custom";
    const p = c.pinnedModel || "";
    if (p === "") return "";
    return PRESET_MODELS.includes(p) ? p : "__custom";
  }

  function onSelect(c, v) {
    const k = convKey(c);
    if (v === "__custom") {
      customModel = { ...customModel, [k]: PRESET_MODELS.includes(c.pinnedModel || "") ? "" : c.pinnedModel || "" };
      return;
    }
    setModel(c, v);
  }

  function cancelCustom(c) {
    const next = { ...customModel };
    delete next[convKey(c)];
    customModel = next;
  }

  // --- 포맷 ---
  function fmtTokens(n) {
    n = Number(n) || 0;
    if (n >= 1e9) return (n / 1e9).toFixed(2) + "B";
    if (n >= 1e6) return (n / 1e6).toFixed(2) + "M";
    if (n >= 1e4) return (n / 1e3).toFixed(1) + "K";
    return n.toLocaleString();
  }
  function fmtCost(v) {
    v = Number(v) || 0;
    if (v === 0) return "$0";
    if (v < 0.01) return "$" + v.toFixed(4);
    return "$" + v.toFixed(2);
  }
  function fmtPct(r) {
    return ((Number(r) || 0) * 100).toFixed(1) + "%";
  }
  function fmtTime(s) {
    if (!s) return "-";
    const d = new Date(s);
    if (isNaN(d.getTime()) || d.getFullYear() < 2000) return "-";
    const now = new Date();
    const pad = (x) => String(x).padStart(2, "0");
    const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
    if (d.toDateString() === now.toDateString()) return hm;
    return `${d.getMonth() + 1}/${d.getDate()} ${hm}`;
  }
  function fmtDuration(ms) {
    ms = Number(ms) || 0;
    if (!ms) return "";
    return ms >= 1000 ? (ms / 1000).toFixed(1) + "초" : ms + "ms";
  }
  function kindLabel(c) {
    if (c.kind === "telegram") return "텔레그램";
    if (c.kind === "web") return "웹";
    if (c.kind === "project") return "프로젝트";
    return c.kind;
  }
  function convTitle(c) {
    return c.title || (c.kind === "telegram" ? "텔레그램 대화" : c.id || "(제목 없음)");
  }
  function tierBadge(tier) {
    if (tier === "light") return "bg-emerald-100 dark:bg-emerald-900/40 text-emerald-700 dark:text-emerald-300";
    if (tier === "heavy") return "bg-violet-100 dark:bg-violet-900/40 text-violet-700 dark:text-violet-300";
    if (tier === "pinned") return "bg-amber-100 dark:bg-amber-900/40 text-amber-700 dark:text-amber-300";
    return "bg-slate-200 dark:bg-slate-700 text-slate-600 dark:text-slate-400";
  }

  let totals = $derived(stats?.totals || null);
  let convs = $derived(Array.isArray(stats?.conversations) ? stats.conversations : []);
</script>

<div class="flex h-full min-h-0 flex-col">
  <div class="flex h-11 shrink-0 items-center gap-2 border-b border-slate-200 dark:border-slate-700 px-3">
    <div class="min-w-0 flex-1 truncate text-sm font-semibold text-slate-900 dark:text-slate-100">사용량</div>
    {#if stats?.generatedAt}
      <span class="shrink-0 text-[10px] text-slate-400 dark:text-slate-500" title="마지막 갱신">{fmtTime(stats.generatedAt)}</span>
    {/if}
    <button class="grid h-8 w-8 place-items-center rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-sm text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700 disabled:opacity-50" disabled={loading} onclick={load} title="새로고침 (15초마다 자동)" aria-label="새로고침">↻</button>
  </div>

  <div class="min-h-0 flex-1 overflow-y-auto px-3 py-3">
    {#if loadError}
      <div class="mb-2 rounded bg-rose-50 dark:bg-rose-950/40 px-2 py-1 text-xs text-rose-700 dark:text-rose-300">{loadError}</div>
    {/if}

    {#if totals}
      <div class="mb-3 rounded-lg border border-slate-200 dark:border-slate-700 bg-white/85 dark:bg-slate-900/85 p-3 shadow-sm">
        <div class="mb-2 text-xs font-semibold text-slate-600 dark:text-slate-400">전체 합계 · 대화 {totals.conversations}개</div>
        <div class="grid grid-cols-2 gap-x-3 gap-y-1.5 text-xs">
          <div class="text-slate-500 dark:text-slate-400">턴</div>
          <div class="text-right font-mono text-slate-900 dark:text-slate-100">{(totals.turns || 0).toLocaleString()}</div>
          <div class="text-slate-500 dark:text-slate-400">컨텍스트 리셋</div>
          <div class="text-right font-mono text-slate-900 dark:text-slate-100">{(totals.resets || 0).toLocaleString()}</div>
          <div class="text-slate-500 dark:text-slate-400">총 토큰</div>
          <div class="text-right font-mono text-slate-900 dark:text-slate-100" title={(totals.totalTokens || 0).toLocaleString()}>{fmtTokens(totals.totalTokens)}</div>
          <div class="text-slate-500 dark:text-slate-400">비용</div>
          <div class="text-right font-mono font-semibold text-slate-900 dark:text-slate-100">{fmtCost(totals.usage?.costUsd)}</div>
          <div class="text-slate-500 dark:text-slate-400">캐시 적중률</div>
          <div class="text-right font-mono text-slate-900 dark:text-slate-100">{fmtPct(totals.cacheHitRatio)}</div>
        </div>
        {#if totals.usage}
          <div class="mt-2 border-t border-slate-100 dark:border-slate-800 pt-2 text-[10px] leading-4 text-slate-400 dark:text-slate-500">
            입력 {fmtTokens(totals.usage.input)} · 캐시읽기 {fmtTokens(totals.usage.cacheRead)} · 캐시쓰기 {fmtTokens(totals.usage.cacheWrite)} · 출력 {fmtTokens(totals.usage.output)}
          </div>
        {/if}
      </div>
    {:else if !loadError}
      <div class="px-1 py-3 text-sm text-slate-500 dark:text-slate-400">불러오는 중…</div>
    {/if}

    {#if stats && convs.length === 0}
      <div class="px-1 py-3 text-sm text-slate-500 dark:text-slate-400">아직 기록된 사용량이 없습니다.</div>
    {/if}

    <div class="space-y-2">
      {#each convs as c (convKey(c))}
        {@const k = convKey(c)}
        {@const turns = Array.isArray(c.recentTurns) ? [...c.recentTurns].reverse() : []}
        <div class="rounded-lg border border-slate-200 dark:border-slate-700 bg-white/85 dark:bg-slate-900/85 p-3 shadow-sm">
          <div class="flex items-start gap-2">
            <div class="min-w-0 flex-1">
              <div class="truncate text-sm font-semibold text-slate-900 dark:text-slate-100" title={convTitle(c)}>{convTitle(c)}</div>
              <div class="mt-0.5 truncate text-[11px] text-slate-500 dark:text-slate-400">
                {kindLabel(c)}{c.backend ? ` · ${c.backend}` : ""}{c.lastModel ? ` · ${c.lastModel}` : ""}
              </div>
            </div>
            <span class="shrink-0 font-mono text-sm font-semibold text-slate-900 dark:text-slate-100">{fmtCost(c.usage?.costUsd)}</span>
          </div>

          <div class="mt-2 grid grid-cols-2 gap-x-3 gap-y-1 text-[11px]">
            <div class="text-slate-500 dark:text-slate-400">턴 / 리셋</div>
            <div class="text-right font-mono text-slate-800 dark:text-slate-200">{c.turns || 0} / {c.resets || 0}</div>
            <div class="text-slate-500 dark:text-slate-400">총 토큰</div>
            <div class="text-right font-mono text-slate-800 dark:text-slate-200" title={(c.totalTokens || 0).toLocaleString()}>{fmtTokens(c.totalTokens)}</div>
            <div class="text-slate-500 dark:text-slate-400">캐시 적중률</div>
            <div class="text-right font-mono text-slate-800 dark:text-slate-200">{fmtPct(c.cacheHitRatio)}</div>
            <div class="text-slate-500 dark:text-slate-400">현재 컨텍스트</div>
            <div class="text-right font-mono text-slate-800 dark:text-slate-200" title={(c.contextTokens || 0).toLocaleString()}>{fmtTokens(c.contextTokens)}</div>
            <div class="text-slate-500 dark:text-slate-400">마지막 활동</div>
            <div class="text-right font-mono text-slate-800 dark:text-slate-200">{fmtTime(c.lastActivity)}</div>
          </div>

          <div class="mt-2 flex flex-wrap gap-1 text-[10px] font-bold">
            {#if c.pinnedModel}
              <span class="rounded-full bg-amber-100 dark:bg-amber-900/40 px-2 py-0.5 text-amber-700 dark:text-amber-300" title="고정된 모델">📌 {c.pinnedModel}</span>
            {/if}
            {#if c.hasSummary}
              <span class="rounded-full bg-sky-100 dark:bg-sky-900/40 px-2 py-0.5 text-sky-700 dark:text-sky-300">요약 있음</span>
            {/if}
            {#if c.screenUsed}
              <span class="rounded-full bg-indigo-100 dark:bg-indigo-900/40 px-2 py-0.5 text-indigo-700 dark:text-indigo-300">화면 제어 사용</span>
            {/if}
          </div>

          <!-- 모델 고정 -->
          <div class="mt-2 flex items-center gap-1.5">
            <span class="shrink-0 text-[11px] text-slate-500 dark:text-slate-400">모델</span>
            <select
              class="min-w-0 flex-1 rounded border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-1.5 py-1 text-xs text-slate-800 dark:text-slate-200 disabled:opacity-50"
              disabled={!!pinBusy[k]}
              value={selectValue(c)}
              onchange={(e) => onSelect(c, e.currentTarget.value)}
            >
              <option value="">기본 (설정 따름)</option>
              {#each PRESET_MODELS as m}
                <option value={m}>{m}</option>
              {/each}
              <option value="__custom">직접 입력…</option>
            </select>
          </div>
          {#if k in customModel || (c.pinnedModel && !PRESET_MODELS.includes(c.pinnedModel))}
            <div class="mt-1.5 flex items-center gap-1.5">
              <input
                class="min-w-0 flex-1 rounded border border-slate-300 dark:border-slate-600 px-2 py-1 font-mono text-xs"
                placeholder="예: claude-sonnet-4-5"
                value={k in customModel ? customModel[k] : c.pinnedModel || ""}
                oninput={(e) => (customModel = { ...customModel, [k]: e.currentTarget.value })}
                onkeydown={(e) => { if (e.key === "Enter" && k in customModel) setModel(c, customModel[k]); if (e.key === "Escape") cancelCustom(c); }}
              />
              <button
                class="shrink-0 rounded bg-blue-600 px-2 py-1 text-xs font-semibold text-white hover:bg-blue-700 disabled:opacity-50"
                disabled={!!pinBusy[k] || !(k in customModel) || !String(customModel[k] || "").trim()}
                onclick={() => setModel(c, customModel[k])}
              >적용</button>
            </div>
          {/if}
          {#if pinError[k]}
            <div class="mt-1.5 rounded bg-rose-50 dark:bg-rose-950/40 px-2 py-1 text-[11px] text-rose-700 dark:text-rose-300">{pinError[k]}</div>
          {/if}

          <!-- 최근 턴 -->
          {#if c.recent && c.recent.turns}
            <div class="mt-2 text-[10px] leading-4 text-slate-400 dark:text-slate-500">
              최근 {c.recent.turns}턴: {fmtCost(c.recent.costUsd)} · 캐시 {fmtPct(c.recent.cacheHitRatio)} · 평균 컨텍스트 {fmtTokens(c.recent.avgContextTokens)} / 최대 {fmtTokens(c.recent.maxContextTokens)}{c.recent.lightTurns ? ` · 경량 ${c.recent.lightTurns}` : ""}{c.recent.summaryUsed ? ` · 요약 ${c.recent.summaryUsed}` : ""}{c.recent.recovered ? ` · 복구 ${c.recent.recovered}` : ""}
            </div>
          {/if}
          {#if turns.length}
            <button
              class="mt-2 w-full rounded border border-slate-300 dark:border-slate-600 px-2 py-1 text-xs text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700"
              onclick={() => (expanded = { ...expanded, [k]: !expanded[k] })}
            >{expanded[k] ? "▲ 최근 턴 접기" : `▼ 최근 턴 ${turns.length}개 보기`}</button>
            {#if expanded[k]}
              <div class="mt-2 overflow-x-auto rounded border border-slate-200 dark:border-slate-700">
                <table class="w-max min-w-full border-collapse text-[10px] leading-4">
                  <thead class="bg-slate-100 dark:bg-slate-800 text-slate-500 dark:text-slate-400">
                    <tr>
                      <th class="px-1.5 py-1 text-left font-semibold">시각</th>
                      <th class="px-1.5 py-1 text-left font-semibold">모델</th>
                      <th class="px-1.5 py-1 text-left font-semibold">등급</th>
                      <th class="px-1.5 py-1 text-right font-semibold">입력</th>
                      <th class="px-1.5 py-1 text-right font-semibold">캐시읽기</th>
                      <th class="px-1.5 py-1 text-right font-semibold">캐시쓰기</th>
                      <th class="px-1.5 py-1 text-right font-semibold">출력</th>
                      <th class="px-1.5 py-1 text-right font-semibold">비용</th>
                      <th class="px-1.5 py-1 text-right font-semibold">컨텍스트</th>
                      <th class="px-1.5 py-1 text-left font-semibold">표시</th>
                    </tr>
                  </thead>
                  <tbody class="font-mono text-slate-700 dark:text-slate-300">
                    {#each turns as t, ti (ti)}
                      <tr class={`border-t border-slate-100 dark:border-slate-800 ${t.isError ? "bg-rose-50/70 dark:bg-rose-950/30" : ""}`} title={[t.backend, fmtDuration(t.durationMs), t.isError ? "오류" : ""].filter(Boolean).join(" · ")}>
                        <td class="whitespace-nowrap px-1.5 py-0.5">{fmtTime(t.at)}</td>
                        <td class="whitespace-nowrap px-1.5 py-0.5">{t.model || "-"}</td>
                        <td class="whitespace-nowrap px-1.5 py-0.5">
                          {#if t.tier}<span class={`rounded px-1 font-sans font-bold ${tierBadge(t.tier)}`}>{t.tier}</span>{:else}-{/if}
                        </td>
                        <td class="whitespace-nowrap px-1.5 py-0.5 text-right">{fmtTokens(t.input)}</td>
                        <td class="whitespace-nowrap px-1.5 py-0.5 text-right">{fmtTokens(t.cacheRead)}</td>
                        <td class="whitespace-nowrap px-1.5 py-0.5 text-right">{fmtTokens(t.cacheWrite)}</td>
                        <td class="whitespace-nowrap px-1.5 py-0.5 text-right">{fmtTokens(t.output)}</td>
                        <td class="whitespace-nowrap px-1.5 py-0.5 text-right">{fmtCost(t.costUsd)}</td>
                        <td class="whitespace-nowrap px-1.5 py-0.5 text-right">{t.contextTokens ? fmtTokens(t.contextTokens) : "-"}</td>
                        <td class="whitespace-nowrap px-1.5 py-0.5 font-sans">
                          {#if t.reset}<span class="mr-0.5 rounded bg-orange-100 dark:bg-orange-900/40 px-1 font-bold text-orange-700 dark:text-orange-300" title="컨텍스트 크기 초과로 새 세션에서 실행">리셋</span>{/if}
                          {#if t.recovered}<span class="mr-0.5 rounded bg-rose-100 dark:bg-rose-900/40 px-1 font-bold text-rose-700 dark:text-rose-300" title="기존 세션을 잃어 새 세션에서 실행">복구</span>{/if}
                          {#if t.summaryUsed}<span class="mr-0.5 rounded bg-sky-100 dark:bg-sky-900/40 px-1 font-bold text-sky-700 dark:text-sky-300" title="긴 기록 대신 요약을 주입">요약</span>{/if}
                          {#if t.isError}<span class="rounded bg-rose-200 dark:bg-rose-800/60 px-1 font-bold text-rose-800 dark:text-rose-200">오류</span>{/if}
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            {/if}
          {/if}
        </div>
      {/each}
    </div>
  </div>
</div>
