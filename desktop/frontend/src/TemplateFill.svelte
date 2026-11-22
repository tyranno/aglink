<script>
  // The small form a template snippet asks with: one input per {{변수}} that
  // did not fill itself. 넣기 expands the template into the composer.
  import { tick } from "svelte";
  import { templateFill, expandTemplate } from "./snippets.svelte.js";

  let firstEl = $state(null);
  let values = $state({});

  $effect(() => {
    if (!templateFill.open) return;
    values = Object.fromEntries(templateFill.fields.map((name) => [name, ""]));
    tick().then(() => firstEl?.focus());
  });

  function close() {
    templateFill.open = false;
    templateFill.onDone = null;
  }

  function submit() {
    const text = expandTemplate(templateFill.text, { ...templateFill.values, ...values });
    const done = templateFill.onDone;
    close();
    done?.(text);
  }

  function preview() {
    return expandTemplate(templateFill.text, { ...templateFill.values, ...Object.fromEntries(Object.entries(values).filter(([, v]) => v)) });
  }
</script>

<svelte:window onkeydown={(event) => { if (templateFill.open && event.key === "Escape") { event.preventDefault(); close(); } }} />

{#if templateFill.open}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-[65] flex items-center justify-center bg-slate-950/35 px-4" onclick={close}>
    <div
      class="w-full max-w-md rounded-xl border border-slate-200 bg-white p-4 shadow-2xl dark:border-slate-700 dark:bg-slate-900"
      onclick={(event) => event.stopPropagation()}
      role="dialog"
      aria-modal="true"
      aria-label="상용구 칸 채우기"
      tabindex="-1"
    >
      <div class="text-sm font-semibold text-slate-900 dark:text-slate-100">상용구 칸 채우기</div>
      <div class="mt-0.5 text-[11.5px] text-slate-500 dark:text-slate-400">비워 둔 칸은 그대로 남습니다.</div>
      <div class="mt-3 space-y-2">
        {#each templateFill.fields as name, index (name)}
          <label class="block">
            <span class="mb-0.5 block text-[11.5px] font-semibold text-slate-600 dark:text-slate-300">{name}</span>
            {#if index === 0}
              <textarea
                bind:this={firstEl}
                rows={name === "클립보드" || name === "선택" ? 3 : 1}
                class="w-full resize-y rounded-md border border-slate-300 bg-white px-3 py-1.5 text-[13px] text-slate-900 outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-100 dark:border-slate-600 dark:bg-slate-950 dark:text-slate-100 dark:focus:ring-blue-900/30"
                value={values[name] ?? ""}
                oninput={(event) => (values = { ...values, [name]: event.currentTarget.value })}
                onkeydown={(event) => { if (event.key === "Enter" && !event.shiftKey && !event.isComposing && event.keyCode !== 229) { event.preventDefault(); submit(); } }}
              ></textarea>
            {:else}
              <textarea
                rows={name === "클립보드" || name === "선택" ? 3 : 1}
                class="w-full resize-y rounded-md border border-slate-300 bg-white px-3 py-1.5 text-[13px] text-slate-900 outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-100 dark:border-slate-600 dark:bg-slate-950 dark:text-slate-100 dark:focus:ring-blue-900/30"
                value={values[name] ?? ""}
                oninput={(event) => (values = { ...values, [name]: event.currentTarget.value })}
                onkeydown={(event) => { if (event.key === "Enter" && !event.shiftKey && !event.isComposing && event.keyCode !== 229) { event.preventDefault(); submit(); } }}
              ></textarea>
            {/if}
          </label>
        {/each}
      </div>
      <div class="mt-3 max-h-28 overflow-y-auto whitespace-pre-wrap break-words rounded-md bg-slate-50 px-3 py-2 text-[12px] leading-5 text-slate-600 dark:bg-slate-800 dark:text-slate-300">{preview()}</div>
      <div class="mt-3 flex justify-end gap-2">
        <button type="button" class="ui-btn ui-btn-ghost" onclick={close}>취소</button>
        <button type="button" class="ui-btn ui-btn-primary" onclick={submit}>입력창에 넣기</button>
      </div>
    </div>
  </div>
{/if}
