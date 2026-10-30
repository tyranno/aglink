<script>
  // 설정 화면의 "Claude 로그인" 카드. 작업용 Claude 는 1년짜리 인증 토큰
  // (claude setup-token 이 발급, config 의 claude.oauth_token)으로 로그인한다.
  // 만료되면 예전엔 터미널에서 setup-token 을 돌리고 config.yaml 을 손으로
  // 고쳐야 했다. 여기서는 [토큰 새로 받기] → 브라우저에서 승인 → 끝.
  //
  // 발급·확인은 오래 걸려(브라우저 승인, 실제 호출) 호스트가 백그라운드로
  // 돌리고, 이 카드는 진행 중인 동안 ClaudeAuthStatus 를 1초마다 읽는다.
  // 토큰 자체는 화면으로 오지 않는다 — 끝 4글자만.
  import { Browser } from "@wailsio/runtime";
  import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";

  let st = $state(null); // { tokenSet, tokenTail, lastFailureAt, lastFailureMsg, canIssue, job }
  let loadError = $state("");
  let actionError = $state("");
  let busy = $state(false);
  let code = $state("");
  let paste = $state("");
  let showPaste = $state(false);
  let confirmClear = $state(false);

  const job = $derived(st?.job || { phase: "idle" });
  const active = $derived(job.phase === "running" || job.phase === "need_code");

  async function load() {
    try {
      st = JSON.parse((await ControlService.ClaudeAuthStatus()) || "{}");
      loadError = "";
    } catch (e) {
      loadError = "aglink 에 연결하지 못해 로그인 상태를 읽지 못했습니다.";
    }
  }

  // 진행 중이면 1초, 아니면 15초마다 다시 읽는다(다른 창에서 저장했을 때 대비).
  $effect(() => {
    load();
    const ms = active ? 1000 : 15000;
    const t = setInterval(load, ms);
    return () => clearInterval(t);
  });

  // 호스트의 {ok,error,message} 응답을 처리하는 공통 경로.
  async function call(fn, ...args) {
    busy = true;
    actionError = "";
    try {
      const r = JSON.parse((await fn(...args)) || "{}");
      if (r && r.ok === false) actionError = r.error || "요청이 거부됐습니다.";
      return r && r.ok !== false;
    } catch (e) {
      actionError = "aglink 와 연결이 끊겨 처리하지 못했습니다. 잠시 후 다시 시도해 주세요.";
      return false;
    } finally {
      busy = false;
      await load();
    }
  }

  const issue = () => call(ControlService.ClaudeAuthIssue);
  const verify = () => call(ControlService.ClaudeAuthVerify);
  const cancel = () => call(ControlService.ClaudeAuthCancel);
  async function sendCode() {
    if (await call(ControlService.ClaudeAuthCode, code.trim())) code = "";
  }
  async function savePaste() {
    if (await call(ControlService.ClaudeAuthSave, paste.trim())) {
      paste = "";
      showPaste = false;
    }
  }
  async function clearToken() {
    confirmClear = false;
    await call(ControlService.ClaudeAuthClear);
  }
  function openURL(u) {
    Browser.OpenURL(u).catch(() => {});
  }
  function when(iso) {
    if (!iso) return "";
    const d = new Date(iso);
    return isNaN(d) ? "" : d.toLocaleString();
  }

  const btn = "rounded-md px-3 py-2 text-sm font-semibold disabled:opacity-50";
  const btnPrimary = `${btn} bg-blue-600 text-white hover:bg-blue-700`;
  const btnPlain = `${btn} border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700`;
  const input = "w-full rounded-md border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-3 py-2 text-sm outline-none focus:border-blue-400 focus:ring-4 focus:ring-blue-100 dark:focus:ring-blue-900/30";
</script>

<section class="rounded-lg border border-slate-200 dark:border-slate-700 bg-white/90 dark:bg-slate-900/90 p-5 shadow-sm">
  <div class="mb-3 flex flex-wrap items-center gap-2">
    <div class="text-sm font-semibold text-slate-900 dark:text-slate-100">Claude 로그인</div>
    {#if st}
      {#if st.lastFailureAt}
        <span class="rounded-full bg-red-100 dark:bg-red-950/50 px-2 py-0.5 text-xs font-semibold text-red-700 dark:text-red-300">만료됨 · 다시 받아야 함</span>
      {:else if st.tokenSet}
        <span class="rounded-full bg-emerald-100 dark:bg-emerald-950/50 px-2 py-0.5 text-xs font-semibold text-emerald-700 dark:text-emerald-300">토큰 설정됨 {st.tokenTail}</span>
      {:else}
        <span class="rounded-full bg-slate-100 dark:bg-slate-800 px-2 py-0.5 text-xs font-semibold text-slate-600 dark:text-slate-300">토큰 없음 · 이 PC 의 claude 로그인 사용</span>
      {/if}
    {/if}
  </div>
  <div class="mb-4 max-w-3xl text-xs leading-5 text-slate-500 dark:text-slate-400">
    AI 작업은 Claude 계정의 인증 토큰으로 로그인합니다. 토큰은 1년 동안 쓸 수 있고, 만료되면 아래 버튼으로 다시 받으면 됩니다 — 브라우저에서 승인만 누르면 자동으로 저장됩니다.
  </div>

  {#if loadError}
    <div class="mb-3 text-xs text-red-600 dark:text-red-400">{loadError}</div>
  {/if}

  {#if st?.lastFailureAt && !active}
    <div class="mb-4 rounded-md border border-red-200 dark:border-red-900 bg-red-50 dark:bg-red-950/40 px-3 py-2 text-xs text-red-700 dark:text-red-300">
      {when(st.lastFailureAt)} 에 인증이 거부됐습니다. 토큰이 만료된 것 같습니다 — [토큰 새로 받기] 를 눌러 주세요.
    </div>
  {/if}

  <!-- 진행 중인 발급/확인 -->
  {#if job.phase !== "idle"}
    <div
      class={`mb-4 rounded-md border px-3 py-3 text-sm ${
        job.phase === "error"
          ? "border-red-200 dark:border-red-900 bg-red-50 dark:bg-red-950/40 text-red-700 dark:text-red-300"
          : job.phase === "done"
            ? "border-emerald-200 dark:border-emerald-900 bg-emerald-50 dark:bg-emerald-950/40 text-emerald-800 dark:text-emerald-300"
            : "border-blue-200 dark:border-blue-900 bg-blue-50 dark:bg-blue-950/40 text-blue-800 dark:text-blue-200"
      }`}
    >
      <div class="flex items-start gap-2">
        {#if active}<span class="mt-0.5 inline-block h-3 w-3 shrink-0 animate-spin rounded-full border-2 border-current border-t-transparent"></span>{/if}
        <div class="min-w-0 flex-1 whitespace-pre-wrap break-words">{job.message}</div>
      </div>

      {#if job.phase === "need_code"}
        <div class="mt-3 flex gap-2">
          <input class={input} placeholder="브라우저에 표시된 코드" bind:value={code} onkeydown={(e) => e.key === "Enter" && code.trim() && sendCode()} />
          <button class={btnPrimary} disabled={busy || !code.trim()} onclick={sendCode}>확인</button>
        </div>
      {/if}

      {#if active}
        <div class="mt-3 flex flex-wrap gap-2">
          {#if job.url}
            <button class={btnPlain} onclick={() => openURL(job.url)}>로그인 페이지 열기</button>
          {/if}
          <button class={btnPlain} disabled={busy} onclick={cancel}>취소</button>
        </div>
        {#if job.kind === "issue"}
          <div class="mt-2 text-xs opacity-80">브라우저가 열리지 않으면 [로그인 페이지 열기] 를 누르세요. 승인하면 이 창이 저절로 끝납니다.</div>
        {/if}
      {/if}
    </div>
  {/if}

  {#if actionError}
    <div class="mb-3 text-xs text-red-600 dark:text-red-400">{actionError}</div>
  {/if}

  <div class="flex flex-wrap items-center gap-2">
    {#if st?.canIssue}
      <button class={btnPrimary} disabled={busy || active} onclick={issue}>토큰 새로 받기</button>
    {/if}
    {#if st?.tokenSet}
      <button class={btnPlain} disabled={busy || active} onclick={verify}>연결 확인</button>
    {/if}
    <button class={btnPlain} disabled={active} onclick={() => (showPaste = !showPaste)}>
      {showPaste ? "붙여넣기 닫기" : "토큰 직접 붙여넣기"}
    </button>
    {#if st?.tokenSet}
      {#if confirmClear}
        <span class="text-xs text-slate-600 dark:text-slate-300">토큰을 지우면 이 PC 의 claude 로그인을 씁니다.</span>
        <button class={`${btn} bg-red-600 text-white hover:bg-red-700`} disabled={busy} onclick={clearToken}>지우기</button>
        <button class={btnPlain} onclick={() => (confirmClear = false)}>취소</button>
      {:else}
        <button class="px-2 text-xs text-slate-500 underline hover:text-red-600 dark:text-slate-400" disabled={active} onclick={() => (confirmClear = true)}>토큰 지우기</button>
      {/if}
    {/if}
  </div>

  {#if st && !st.canIssue}
    <div class="mt-2 text-xs text-slate-500 dark:text-slate-400">이 aglink 는 자동 발급을 지원하지 않는 기기에서 돌고 있습니다. 다른 PC 의 터미널에서 <code>claude setup-token</code> 으로 받은 토큰을 붙여넣어 주세요.</div>
  {/if}

  {#if showPaste}
    <div class="mt-4 flex flex-col gap-2 md:flex-row">
      <input
        class={input}
        type="password"
        autocomplete="off"
        placeholder="sk-ant-oat01-…"
        bind:value={paste}
        onkeydown={(e) => e.key === "Enter" && paste.trim() && savePaste()}
      />
      <button class={btnPrimary} disabled={busy || !paste.trim()} onclick={savePaste}>저장</button>
    </div>
    <div class="mt-1 text-xs text-slate-500 dark:text-slate-400">저장하면 다음 메시지부터 바로 적용됩니다. 형식이 틀리면 저장되지 않습니다.</div>
  {/if}
</section>
