<script>
  // 승인 요청 창. 설정의 "실행 전 승인받을 도구"에 넣은 웹·화면 제어 도구를
  // AI 가 쓰려 하면, 호스트가 control:approval 을 보내고 답을 기다린다
  // (host/approval.go). [승인]/[거부] 가 그 답이고, 텔레그램 등 다른 곳에서
  // 먼저 답하거나 시간이 지나면 control:approval_done 으로 창이 닫힌다.
  import { Events } from "@wailsio/runtime";
  import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";

  let queue = $state([]); // [{ id, source, tool, summary, timeoutSec, at }]
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

  async function answer(ok) {
    if (!current) return;
    busy = true;
    error = "";
    const id = current.id;
    try {
      const r = JSON.parse((await ControlService.ApprovalAnswer(id, ok)) || "{}");
      if (r && r.ok === false) error = r.error || "처리하지 못했습니다.";
    } catch (e) {
      error = "aglink 와 연결이 끊겨 답하지 못했습니다.";
    } finally {
      busy = false;
      queue = queue.filter((q) => q.id !== id);
    }
  }
</script>

{#if current}
  <div class="fixed inset-0 z-[60] flex items-center justify-center bg-slate-900/40 p-4">
    <div class="w-[520px] max-w-full rounded-lg border border-amber-300 dark:border-amber-700 bg-white dark:bg-slate-900 p-5 shadow-xl">
      <div class="mb-1 flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-slate-100">
        🔐 승인 요청 <span class="font-mono text-xs text-slate-500">#{current.id}</span>
        {#if queue.length > 1}<span class="text-xs font-normal text-slate-500">(대기 {queue.length - 1}건 더)</span>{/if}
      </div>
      <div class="mb-3 text-xs text-slate-600 dark:text-slate-300">
        AI 가 <b>{current.source}</b> 의 <b class="font-mono">{current.tool}</b> 을(를) 실행하려고 합니다. 의도한 작업이 맞을 때만 승인하세요.
      </div>
      <pre class="mb-3 max-h-56 overflow-auto whitespace-pre-wrap break-all rounded-md bg-slate-100 dark:bg-slate-800 p-3 font-mono text-[11px] leading-5 text-slate-700 dark:text-slate-200">{current.summary}</pre>
      {#if error}<div class="mb-2 text-xs text-red-600 dark:text-red-400">{error}</div>{/if}
      <div class="flex items-center gap-2">
        <button class="rounded-md bg-blue-600 px-4 py-2 text-sm font-semibold text-white hover:bg-blue-700 disabled:opacity-50" disabled={busy} onclick={() => answer(true)}>승인</button>
        <button class="rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-4 py-2 text-sm font-semibold text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700 disabled:opacity-50" disabled={busy} onclick={() => answer(false)}>거부</button>
        <span class="ml-auto text-xs text-slate-500">{left}초 뒤 자동 거부</span>
      </div>
    </div>
  </div>
{/if}
