<script>
  // 단어장: 받아쓰기가 잘 알아듣게 할 단어, 받아 적은 뒤 고쳐 쓸 말, 말하면
  // 정해 둔 글로 바뀌는 스니펫. ~/.aglink/voice-words.json 하나에 저장되고
  // VS Code 확장(aglink 음성)도 같은 파일을 쓴다. 고치면 바로 저장되어 다음
  // 말부터 쓰인다 (vocab.go).
  import { onMount } from "svelte";
  import { VoiceService } from "../bindings/github.com/tyranno/aglink-desktop";

  let { card, label, desc, field, button } = $props();

  let vocab = $state({ words: [], replace: [], snippets: [], builtin: [], path: "", error: "" });
  let busy = $state(false);
  let msg = $state("");
  let word = $state("");
  let from = $state("");
  let to = $state("");
  let trigger = $state("");
  let text = $state("");

  async function load() {
    try {
      vocab = JSON.parse(await VoiceService.VocabInfo());
    } catch (e) {
      msg = "단어장을 읽지 못했어요: " + (e?.message || e);
    }
  }

  onMount(load);

  // save writes the changed lists and reads them back as the app tidied them.
  async function save(patch) {
    busy = true;
    msg = "";
    try {
      const next = { words: vocab.words, replace: vocab.replace, snippets: vocab.snippets, ...patch };
      await VoiceService.SaveVocab(JSON.stringify(next));
      await load();
      return true;
    } catch (e) {
      msg = "저장하지 못했어요: " + (e?.message || e);
      return false;
    } finally {
      busy = false;
    }
  }

  async function addWord() {
    const ws = word.split(/[,，]/).map((w) => w.trim()).filter(Boolean);
    if (ws.length && (await save({ words: [...vocab.words, ...ws] }))) word = "";
  }

  async function addReplace() {
    if (!from.trim() || !to.trim()) return;
    if (await save({ replace: [...vocab.replace, { from: from.trim(), to: to.trim() }] })) from = to = "";
  }

  async function addSnippet() {
    if (!trigger.trim() || !text.trim()) return;
    if (await save({ snippets: [...vocab.snippets, { trigger: trigger.trim(), text }] })) trigger = text = "";
  }

  const enter = (fn) => (e) => {
    if (e.key === "Enter" && !e.isComposing) fn();
  };
  const remove = "text-xs text-slate-400 hover:text-rose-600";
</script>

<div class={card}>
  <div class={label}>단어장</div>
  <div class={desc}>
    받아쓰기가 자주 틀리는 이름·용어를 적어 두면 더 잘 알아들어요. “고쳐 쓰기”는 받아 적은 말을 바꿔 넣고(띄어쓰기·영문 대소문자는 가리지 않아요),
    “스니펫”은 정해 둔 말을 하면 그 자리에 적어 둔 글을 넣어요 — 그 말만 하면 글만 보내요. VS Code 의 aglink 음성도 같은 단어장을 써요.
  </div>
  {#if vocab.error}
    <div class="mt-2 text-xs text-rose-600 dark:text-rose-400">단어장 파일을 읽지 못해 지금은 쓰지 않고 있어요: {vocab.error}. 여기서 고치면 새로 저장돼요.</div>
  {/if}

  <div class="mt-4 text-xs font-semibold text-slate-700 dark:text-slate-300">잘 알아듣게 할 단어</div>
  <div class="mt-2 flex flex-wrap items-center gap-1.5">
    {#each vocab.words as w, i (w)}
      <span class="inline-flex items-center gap-1 rounded-full border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-900 px-2 py-0.5 text-xs text-slate-800 dark:text-slate-200">
        {w}
        <button class={remove} title="지우기" disabled={busy} onclick={() => save({ words: vocab.words.filter((_, j) => j !== i) })}>✕</button>
      </span>
    {/each}
    <input class={`${field} w-48`} placeholder="예: Headscale, 두원" bind:value={word} disabled={busy} onkeydown={enter(addWord)} />
    <button class={button} disabled={busy || !word.trim()} onclick={addWord}>추가</button>
  </div>
  {#if vocab.builtin.length}
    <div class="mt-1 text-[11px] text-slate-500 dark:text-slate-400">기본으로 들어 있는 단어: {vocab.builtin.join(", ")}</div>
  {/if}

  <div class="mt-4 text-xs font-semibold text-slate-700 dark:text-slate-300">고쳐 쓰기</div>
  {#if vocab.replace.length}
    <ul class="mt-2 space-y-1 text-sm">
      {#each vocab.replace as r, i (r.from)}
        <li class="flex items-center gap-2 border-t border-slate-200 dark:border-slate-700 pt-1">
          <span class="text-slate-500 dark:text-slate-400">{r.from}</span>
          <span class="text-slate-400">→</span>
          <span class="flex-1 text-slate-800 dark:text-slate-200">{r.to}</span>
          <button class={remove} title="지우기" disabled={busy} onclick={() => save({ replace: vocab.replace.filter((_, j) => j !== i) })}>✕</button>
        </li>
      {/each}
    </ul>
  {/if}
  <div class="mt-2 flex flex-wrap items-center gap-2">
    <input class={`${field} w-40`} placeholder="들리는 말 (예: 에이지링크)" bind:value={from} disabled={busy} onkeydown={enter(addReplace)} />
    <span class="text-slate-400">→</span>
    <input class={`${field} w-40`} placeholder="바꿀 말 (예: aglink)" bind:value={to} disabled={busy} onkeydown={enter(addReplace)} />
    <button class={button} disabled={busy || !from.trim() || !to.trim()} onclick={addReplace}>추가</button>
  </div>

  <div class="mt-4 text-xs font-semibold text-slate-700 dark:text-slate-300">스니펫</div>
  {#if vocab.snippets.length}
    <ul class="mt-2 space-y-1 text-sm">
      {#each vocab.snippets as s, i (s.trigger)}
        <li class="flex items-start gap-2 border-t border-slate-200 dark:border-slate-700 pt-1">
          <span class="shrink-0 text-slate-500 dark:text-slate-400">“{s.trigger}”</span>
          <span class="text-slate-400">→</span>
          <span class="flex-1 whitespace-pre-wrap text-slate-800 dark:text-slate-200">{s.text}</span>
          <button class={remove} title="지우기" disabled={busy} onclick={() => save({ snippets: vocab.snippets.filter((_, j) => j !== i) })}>✕</button>
        </li>
      {/each}
    </ul>
  {/if}
  <div class="mt-2 flex flex-wrap items-start gap-2">
    <input class={`${field} w-40`} placeholder="말 (예: 내 메일 주소)" bind:value={trigger} disabled={busy} />
    <textarea class={`${field} min-w-0 flex-1`} rows="2" placeholder="넣을 글 (여러 줄도 돼요)" bind:value={text} disabled={busy}></textarea>
    <button class={button} disabled={busy || !trigger.trim() || !text.trim()} onclick={addSnippet}>추가</button>
  </div>

  {#if msg}
    <div class="mt-2 text-[11px] text-rose-600 dark:text-rose-400">{msg}</div>
  {/if}
  {#if vocab.path}
    <div class="mt-2 text-[11px] text-slate-500 dark:text-slate-400">파일: {vocab.path}</div>
  {/if}
</div>
