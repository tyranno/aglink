<script>
  // 상용구 관리: add / edit / delete the snippets the composer inserts with "/".
  // An in-app dialog (never the browser's prompt/confirm); deleting asks once
  // inline on the row itself.
  import { tick, untrack } from "svelte";
  import { snippets, saveSnippet, deleteSnippet, MAX_SNIPPETS } from "./snippets.svelte.js";

  let editId = $state("");
  let name = $state("");
  let text = $state("");
  let error = $state("");
  let askDelete = $state("");
  let nameEl = $state(null);

  async function startNew(prefill = "") {
    editId = "";
    name = "";
    text = prefill;
    error = "";
    await tick();
    nameEl?.focus();
  }

  async function startEdit(item) {
    editId = item.id;
    name = item.name;
    text = item.text;
    error = "";
    askDelete = "";
    await tick();
    nameEl?.focus();
  }

  function submit() {
    error = saveSnippet({ id: editId, name, text });
    if (!error) void startNew();
  }

  function close() {
    snippets.managerOpen = false;
    askDelete = "";
  }

  // Each time the dialog opens: a fresh form, prefilled with what was asked for.
  $effect(() => {
    if (!snippets.managerOpen) return;
    untrack(() => {
      const prefill = snippets.managerDraft;
      snippets.managerDraft = "";
      askDelete = "";
      void startNew(prefill);
    });
  });

  function handleKeydown(event) {
    if (!snippets.managerOpen || event.key !== "Escape") return;
    event.preventDefault();
    if (askDelete) askDelete = "";
    else close();
  }
</script>

<svelte:window onkeydown={handleKeydown} />

{#if snippets.managerOpen}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-950/35 px-4" onclick={close}>
    <div
      class="flex max-h-[80vh] w-full max-w-lg flex-col overflow-hidden rounded-xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900"
      onclick={(event) => event.stopPropagation()}
      role="dialog"
      aria-modal="true"
      aria-label="상용구 관리"
      tabindex="-1"
    >
      <div class="flex items-center gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-700">
        <div class="min-w-0 flex-1">
          <div class="text-sm font-semibold text-slate-900 dark:text-slate-100">상용구 관리</div>
          <div class="mt-0.5 text-[11.5px] text-slate-500 dark:text-slate-400">입력창 맨 앞에 <kbd class="rounded border border-slate-200 px-1 font-mono text-[10.5px] dark:border-slate-700">/</kbd>와 이름 일부를 치면 골라 넣을 수 있어요. <span class="font-mono">{"{{날짜}}"}</span> <span class="font-mono">{"{{시간}}"}</span> <span class="font-mono">{"{{클립보드}}"}</span> <span class="font-mono">{"{{선택}}"}</span>은 저절로 채워지고, <span class="font-mono">{"{{이름}}"}</span>처럼 다른 칸은 넣을 때 물어봐요.</div>
        </div>
        <button type="button" class="rounded px-2 text-slate-400 hover:text-slate-700 dark:hover:text-slate-200" onclick={close} aria-label="닫기">✕</button>
      </div>

      <div class="min-h-0 flex-1 overflow-y-auto p-2">
        {#each snippets.list as item (item.id)}
          <div class={`group flex items-start gap-2 rounded-lg px-2.5 py-2 ${editId === item.id ? "bg-blue-50 dark:bg-blue-950/40" : "hover:bg-slate-50 dark:hover:bg-slate-800"}`}>
            <div class="min-w-0 flex-1">
              <div class="font-mono text-[12.5px] font-semibold text-blue-700 dark:text-blue-300">/{item.name}</div>
              <div class="mt-0.5 line-clamp-2 whitespace-pre-wrap break-words text-[12px] text-slate-500 dark:text-slate-400">{item.text}</div>
            </div>
            {#if askDelete === item.id}
              <button type="button" class="ui-btn ui-btn-ghost shrink-0" onclick={() => (askDelete = "")}>취소</button>
              <button type="button" class="shrink-0 rounded-md bg-rose-600 px-2 py-1 text-[11.5px] font-semibold text-white hover:bg-rose-700" onclick={() => { deleteSnippet(item.id); askDelete = ""; if (editId === item.id) void startNew(); }}>삭제</button>
            {:else}
              <button type="button" class="shrink-0 rounded-md px-2 py-1 text-[11.5px] font-medium text-slate-600 hover:bg-slate-200 dark:text-slate-300 dark:hover:bg-slate-700" onclick={() => startEdit(item)}>고치기</button>
              <button type="button" class="shrink-0 rounded-md px-2 py-1 text-[11.5px] font-medium text-rose-600 hover:bg-rose-50 dark:text-rose-300 dark:hover:bg-rose-950/40" onclick={() => (askDelete = item.id)}>삭제</button>
            {/if}
          </div>
        {:else}
          <div class="px-3 py-6 text-center text-[12.5px] text-slate-400 dark:text-slate-500">아직 상용구가 없어요. 아래에서 자주 쓰는 요청을 저장해 보세요.</div>
        {/each}
      </div>

      <div class="border-t border-slate-200 bg-slate-50/70 px-4 py-3 dark:border-slate-700 dark:bg-slate-800/50">
        <div class="mb-1.5 text-[11.5px] font-semibold text-slate-600 dark:text-slate-300">{editId ? "상용구 고치기" : `새 상용구 (${snippets.list.length}/${MAX_SNIPPETS})`}</div>
        <input
          bind:this={nameEl}
          class="w-full rounded-md border border-slate-300 bg-white px-3 py-1.5 font-mono text-[13px] text-slate-900 outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-100 dark:border-slate-600 dark:bg-slate-900 dark:text-slate-100 dark:focus:ring-blue-900/30"
          placeholder="이름 (예: 리뷰)"
          value={name}
          oninput={(event) => { name = event.currentTarget.value; error = ""; }}
        />
        <textarea
          class="mt-1.5 min-h-[84px] w-full resize-y rounded-md border border-slate-300 bg-white px-3 py-1.5 text-[13px] leading-5 text-slate-900 outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-100 dark:border-slate-600 dark:bg-slate-900 dark:text-slate-100 dark:focus:ring-blue-900/30"
          placeholder="넣을 내용 (예: 이 변경을 꼼꼼히 리뷰하고 문제점을 알려줘)"
          value={text}
          oninput={(event) => { text = event.currentTarget.value; error = ""; }}
          onkeydown={(event) => {
            if (event.key === "Enter" && event.ctrlKey && !event.isComposing) { event.preventDefault(); submit(); }
          }}
        ></textarea>
        <div class="mt-2 flex items-center gap-2">
          {#if error}<span class="min-w-0 flex-1 text-[11.5px] text-rose-600 dark:text-rose-300" role="alert">{error}</span>{:else}<span class="min-w-0 flex-1 text-[11px] text-slate-400 dark:text-slate-500">Ctrl+Enter 저장</span>{/if}
          {#if editId}
            <button type="button" class="ui-btn ui-btn-ghost" onclick={() => startNew()}>새로 만들기</button>
          {/if}
          <button type="button" class="ui-btn ui-btn-primary" onclick={submit}>{editId ? "고친 내용 저장" : "저장"}</button>
        </div>
      </div>
    </div>
  </div>
{/if}
