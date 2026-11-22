<script>
  // 빠른 질문: Ctrl+Alt+Space anywhere in Windows (desktop quickask.go) brings
  // the window up with this box. The clipboard text comes along and can be
  // added below the question with one click. Enter sends to the chosen
  // conversation (or a new one) and opens it.
  import { tick, untrack, onMount } from "svelte";
  import { Events } from "@wailsio/runtime";
  import { ControlService } from "../bindings/github.com/tyranno/aglink-desktop";
  import {
    chat,
    focusedPane,
    targetKey,
    paneConversationMeta,
    selectTargetInPane,
    loadConversations,
    sendTextToTarget,
    waitForHistory,
  } from "./paneStore.svelte.js";
  import { quickAsk } from "./quickask.svelte.js";

  let text = $state("");
  let clip = $state("");
  let withClip = $state(false);
  let choice = $state(""); // target key, or "new"
  let busy = $state(false);
  let error = $state("");
  let inputEl = $state(null);

  function choices() {
    const list = [];
    const seen = new Set();
    const add = (target) => {
      if (!target) return;
      const key = targetKey(target);
      if (seen.has(key)) return;
      if (target.kind === "web" && !chat.webConvs.some((c) => String(c.id) === String(target.id))) return;
      if (target.kind === "telegram" && !chat.telegram) return;
      seen.add(key);
      list.push({ key, target, title: paneConversationMeta(target)?.title || "대화" });
    };
    add(focusedPane()?.target);
    for (const t of chat.recentTargets) add(t);
    return list.slice(0, 10);
  }

  async function openBox(clipboard = "") {
    clip = String(clipboard || "");
    withClip = false;
    error = "";
    busy = false;
    const first = choices()[0];
    if (!choice || (choice !== "new" && !choices().some((c) => c.key === choice))) choice = first ? first.key : "new";
    quickAsk.open = true;
    await tick();
    inputEl?.focus();
  }

  $effect(() => {
    if (quickAsk.open && quickAsk.request) {
      const clipboard = quickAsk.request.clipboard;
      quickAsk.request = null;
      untrack(() => void openBox(clipboard));
    }
  });

  onMount(() =>
    Events.On("hotkey:quickask", (event) => {
      window.dispatchEvent(new CustomEvent("aglink:show-chat"));
      void openBox(event?.data?.clipboard || "");
    }),
  );

  function close() {
    quickAsk.open = false;
  }

  function body() {
    const q = text.trim();
    if (!withClip || !clip.trim()) return q;
    const quoted = clip.trim().split(/\r?\n/).map((line) => `> ${line}`).join("\n");
    return q ? `${q}\n\n${quoted}` : quoted;
  }

  async function send() {
    const message = body();
    if (!message || busy) return;
    if (!chat.connected) {
      error = "aglink 본체에 연결되지 않아 보낼 수 없어요.";
      return;
    }
    busy = true;
    error = "";
    try {
      let target;
      if (choice === "new") {
        const before = new Set(chat.webConvs.map((c) => String(c.id)));
        await ControlService.WebNew("빠른 질문");
        await loadConversations();
        const created = chat.webConvs.find((c) => !before.has(String(c.id)));
        if (!created) throw new Error("새 대화를 만들지 못했습니다");
        target = { kind: "web", id: created.id };
      } else {
        target = choices().find((c) => c.key === choice)?.target;
        if (!target) throw new Error("보낼 대화를 고르세요");
      }
      const key = targetKey(target);
      if (!chat.panes.some((p) => p.target && targetKey(p.target) === key)) {
        const p = focusedPane();
        if (p) await selectTargetInPane(p.id, target);
      }
      await waitForHistory(key);
      if (!(await sendTextToTarget(target, message))) throw new Error("보내지 못했습니다");
      text = "";
      clip = "";
      close();
    } catch (err) {
      error = String(err?.message || err);
    } finally {
      busy = false;
    }
  }
</script>

<svelte:window onkeydown={(event) => { if (quickAsk.open && event.key === "Escape") { event.preventDefault(); close(); } }} />

{#if quickAsk.open}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="fixed inset-0 z-[60] flex justify-center bg-slate-950/30 px-4 pt-[14vh] backdrop-blur-[1px]" onclick={close}>
    <div
      class="h-fit w-full max-w-xl rounded-xl border border-slate-200 bg-white p-3 shadow-2xl dark:border-slate-700 dark:bg-slate-900"
      onclick={(event) => event.stopPropagation()}
      role="dialog"
      aria-modal="true"
      aria-label="빠른 질문"
      tabindex="-1"
    >
      <div class="mb-2 flex items-center gap-2">
        <span class="text-sm font-semibold text-slate-900 dark:text-slate-100">빠른 질문</span>
        <span class="text-[11px] text-slate-400 dark:text-slate-500">어디서든 Ctrl+Alt+Space</span>
        <select
          class="ml-auto max-w-[55%] rounded-md border border-slate-300 bg-white px-2 py-1 text-[12px] text-slate-700 outline-none focus:border-blue-400 dark:border-slate-600 dark:bg-slate-950 dark:text-slate-200"
          value={choice}
          onchange={(event) => (choice = event.currentTarget.value)}
          aria-label="보낼 대화"
        >
          {#each choices() as item (item.key)}
            <option value={item.key}>{item.title}</option>
          {/each}
          <option value="new">＋ 새 대화로</option>
        </select>
      </div>
      <textarea
        bind:this={inputEl}
        rows="3"
        class="w-full resize-y rounded-md border border-slate-300 bg-white px-3 py-2 text-sm leading-5 text-slate-900 outline-none placeholder:text-slate-400 focus:border-blue-400 focus:ring-2 focus:ring-blue-100 dark:border-slate-600 dark:bg-slate-950 dark:text-slate-100 dark:focus:ring-blue-900/30"
        placeholder="무엇이든 물어보세요 — Enter 보내기, Shift+Enter 줄바꿈"
        value={text}
        oninput={(event) => (text = event.currentTarget.value)}
        onkeydown={(event) => {
          if (event.key === "Enter" && !event.shiftKey && !event.isComposing && event.keyCode !== 229) {
            event.preventDefault();
            void send();
          }
        }}
      ></textarea>
      {#if clip.trim()}
        <label class="mt-1.5 flex cursor-pointer items-start gap-2 rounded-md bg-slate-50 px-2.5 py-1.5 text-[12px] text-slate-600 dark:bg-slate-800 dark:text-slate-300">
          <input type="checkbox" class="mt-0.5" checked={withClip} onchange={(event) => (withClip = event.currentTarget.checked)} />
          <span class="min-w-0 flex-1">
            <span class="font-semibold">클립보드 내용 붙이기</span> <span class="text-slate-400">({clip.length.toLocaleString("ko-KR")}자)</span>
            <span class="mt-0.5 line-clamp-2 block whitespace-pre-wrap break-words text-[11.5px] text-slate-500 dark:text-slate-400">{clip}</span>
          </span>
        </label>
      {/if}
      <div class="mt-2 flex items-center gap-2">
        {#if error}<span class="min-w-0 flex-1 text-[11.5px] text-rose-600 dark:text-rose-300" role="alert">{error}</span>{:else}<span class="min-w-0 flex-1 text-[11px] text-slate-400 dark:text-slate-500">보내면 그 대화가 열립니다 · Esc 닫기</span>{/if}
        <button type="button" class="ui-btn ui-btn-ghost" onclick={close}>취소</button>
        <button type="button" class="ui-btn ui-btn-primary" disabled={busy || !body()} onclick={send}>{busy ? "보내는 중…" : "보내기"}</button>
      </div>
    </div>
  </div>
{/if}
