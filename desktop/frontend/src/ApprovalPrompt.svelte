<script>
  // 승인 요청 창. 설정의 "실행 전 승인받을 도구"에 넣은 웹·화면 제어 도구를
  // AI 가 쓰려 하면, 호스트가 control:approval 을 보내고 답을 기다린다
  // (host/approval.go). 답은 범위(scope)와 함께 간다:
  //   이번만 — 이 호출만 / 이 대화에서 — 이 대화가 끝날 때까지 이 도구는 묻지 않음
  //   이 대상만 항상 — 이 앱·명령·사이트만 다음부터 묻지 않음 (approvals.allow)
  //   항상 허용 — 이 도구를 승인 목록에서 빼서 다음부터 묻지 않음
  // 텔레그램 등 다른 곳에서 먼저 답하거나 시간이 지나면 control:approval_done
  // 으로 창이 닫힌다.
  import { Events } from "@wailsio/runtime";
  import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";

  let queue = $state([]); // [{ id, source, tool, summary, timeoutSec, target, session, hasSession, at }]
  let busy = $state(false);
  let error = $state("");
  let now = $state(Date.now());

  $effect(() => {
    const offAsk = Events.On("control:approval", (e) => {
      const a = e.data || {};
      if (!a.id || queue.some((q) => q.id === a.id)) return;
      queue = [...queue, { ...a, at: Date.now() }];
    });
    const offDone = Events.On("control:approval_done", (e) => {
      const id = (e.data || {}).id;
      queue = queue.filter((q) => q.id !== id);
    });
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => {
      offAsk();
      offDone();
      clearInterval(t);
    };
  });

  const current = $derived(queue[0] || null);
  const left = $derived(current ? Math.max(0, Math.round((current.at + current.timeoutSec * 1000 - now) / 1000)) : 0);

  // ok=false refuses; otherwise scope is once | session | target | always.
  async function answer(ok, scope = "once") {
    if (!current) return;
    busy = true;
    error = "";
    const id = current.id;
    let keep = false;
    try {
      const raw = await ControlService.ApprovalAnswerScope(id, ok, ok ? scope : "");
      const r = JSON.parse(raw || "{}");
      if (r && r.ok === false) {
        error = r.error || "처리하지 못했습니다.";
        // A scope the request cannot take leaves it pending: let the user pick another.
        keep = /쓸 수 없습니다/.test(error);
      }
    } catch (e) {
      error = "aglink 와 연결이 끊겨 답하지 못했습니다.";
    } finally {
      busy = false;
      if (!keep) queue = queue.filter((q) => q.id !== id);
    }
  }

  // app.css gives buttons font: inherit (unlayered, so it beats text-* on the
  // button itself) — the sizes are set on the wrapping divs instead.
  // Esc refuses (the safe answer); approving always takes a click.
  function onKey(e) {
    if (current && !busy && e.key === "Escape") {
      e.preventDefault();
      answer(false);
    }
  }

  const pct = $derived(current && current.timeoutSec > 0 ? Math.min(100, (left / current.timeoutSec) * 100) : 0);
  const btn = "inline-flex h-7 items-center justify-center gap-1 rounded-md px-3 leading-none transition-colors disabled:opacity-50";
  const chip = "inline-flex h-6 items-center rounded-full border border-slate-200 px-2 leading-none text-slate-600 transition-colors hover:border-blue-300 hover:bg-blue-50 hover:text-blue-700 disabled:opacity-50 dark:border-slate-700 dark:text-slate-300 dark:hover:border-blue-700 dark:hover:bg-blue-950/40 dark:hover:text-blue-300";
</script>

<svelte:window onkeydown={onKey} />

{#if current}
  <div class="fixed inset-0 z-[60] flex items-center justify-center bg-slate-900/40 p-4 backdrop-blur-[1px]">
    <div class="w-[440px] max-w-full overflow-hidden rounded-xl bg-white shadow-2xl ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700" role="alertdialog" aria-labelledby="approval-title">
      <div class="h-[3px] bg-slate-100 dark:bg-slate-800">
        <div class="h-full bg-amber-400 transition-[width] duration-1000 ease-linear" style="width: {pct}%"></div>
      </div>
      <div class="px-4 pb-3 pt-3.5">
        <div class="flex items-start gap-3">
          <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-amber-50 text-amber-600 ring-1 ring-amber-200 dark:bg-amber-950/40 dark:text-amber-400 dark:ring-amber-800">
            <svg viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l7 3v5c0 4.5-3 8.5-7 10-4-1.5-7-5.5-7-10V6l7-3z" /><path d="M12 9v4" /><path d="M12 16.5h.01" /></svg>
          </div>
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <h2 id="approval-title" class="text-[14px] font-semibold text-slate-900 dark:text-slate-100">실행 승인 요청</h2>
              {#if queue.length > 1}<span class="rounded-full bg-slate-100 px-1.5 py-px text-[10.5px] text-slate-500 dark:bg-slate-800">+{queue.length - 1}건 대기</span>{/if}
              <span class="ml-auto text-[11px] tabular-nums text-slate-400" title="답이 없으면 자동으로 거부합니다">{left}초 뒤 거부</span>
            </div>
            <p class="mt-0.5 text-[12px] leading-5 text-slate-600 dark:text-slate-300">
              AI 가 <b class="font-medium text-slate-800 dark:text-slate-100">{current.source}</b> 의 <code class="rounded bg-slate-100 px-1 py-px font-mono text-[11px] text-slate-800 dark:bg-slate-800 dark:text-slate-100">{current.tool}</code> 을(를) 실행하려고 합니다.
            </p>
            <div class="mt-1 flex flex-wrap items-center gap-x-2 text-[11px] text-slate-400">
              {#if current.target}<span>대상 <span class="font-mono text-slate-600 dark:text-slate-300">{current.target}</span></span>{/if}
              {#if current.session}<span>대화 {current.session}</span>{/if}
              <span class="font-mono">#{current.id}</span>
            </div>
          </div>
        </div>
        <pre class="mt-3 max-h-48 overflow-auto whitespace-pre-wrap break-all rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 font-mono text-[11px] leading-[1.6] text-slate-700 dark:border-slate-700 dark:bg-slate-950/60 dark:text-slate-200">{current.summary}</pre>
        {#if error}<div class="mt-2 text-[12px] text-red-600 dark:text-red-400">{error}</div>{/if}
      </div>
      <div class="flex flex-wrap items-center gap-2 border-t border-slate-100 bg-slate-50/70 px-4 py-2 dark:border-slate-800 dark:bg-slate-950/30">
        <div class="flex flex-wrap items-center gap-1 text-[11px]">
          <span class="mr-0.5 text-slate-400">계속 허용</span>
          {#if current.hasSession}
            <button type="button" class={chip} disabled={busy} title="이 대화가 끝날 때까지 이 도구는 다시 묻지 않습니다 (aglink 를 다시 시작하면 초기화)" onclick={() => answer(true, "session")}>이 대화에서</button>
          {/if}
          {#if current.target}
            <button type="button" class={chip} disabled={busy} title="{current.tool}:{current.target} 만 다음부터 묻지 않습니다 (설정의 '대상별 항상 허용'에 추가)" onclick={() => answer(true, "target")}>이 대상만</button>
          {/if}
          <button type="button" class={chip} disabled={busy} title="이 도구는 다음부터 묻지 않고 실행합니다 (설정의 '실행 전 승인받을 도구'에서 빠짐)" onclick={() => answer(true, "always")}>항상</button>
        </div>
        <div class="ml-auto flex items-center gap-1.5 text-[12px] font-medium">
          <button type="button" class="{btn} text-slate-600 hover:bg-slate-200/70 dark:text-slate-300 dark:hover:bg-slate-800" disabled={busy} title="거부 (Esc)" onclick={() => answer(false)}>거부</button>
          <button type="button" class="{btn} bg-blue-600 text-white shadow-sm hover:bg-blue-700" disabled={busy} onclick={() => answer(true, "once")}>이번만 승인</button>
        </div>
      </div>
    </div>
  </div>
{/if}
