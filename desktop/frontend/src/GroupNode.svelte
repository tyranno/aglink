<script>
  import GroupNode from "./GroupNode.svelte";
  import {
    chat,
    webConvsInGroup,
    childWebGroups,
    toggleWebGroupCollapsed,
    handleGroupDragOver,
    handleGroupDragLeave,
    handleGroupDrop,
    handleGroupHeaderDragStart,
    handleGroupHeaderDragEnd,
    handleGroupHeaderDragOver,
    handleGroupHeaderDragLeave,
    handleGroupHeaderDrop,
  } from "./paneStore.svelte.js";

  let { group, openMenuId = $bindable(), webConvRow, onRename, onDelete } = $props();

  const groupConvs = $derived(webConvsInGroup(group.id));
  const childGroups = $derived(childWebGroups(group.id));
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class={`rounded-md ${chat.dragOverGroupId === group.id ? "ring-2 ring-blue-400 bg-blue-50" : ""} ${chat.dragOverGroupReorder === group.id && chat.dragOverGroupZone === "into" ? "ring-2 ring-amber-400 bg-amber-50" : ""} ${chat.draggingGroupId === group.id ? "opacity-50" : ""}`}
  ondragenter={(event) => handleGroupDragOver(event, group.id)}
  ondragover={(event) => handleGroupDragOver(event, group.id)}
  ondragleave={() => handleGroupDragLeave(group.id)}
  ondrop={(event) => handleGroupDrop(event, group.id)}
>
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    class={`relative flex h-9 cursor-grab items-center gap-1 rounded-md px-1 hover:bg-slate-100 active:cursor-grabbing ${chat.dragOverGroupReorder === group.id && chat.dragOverGroupZone === "before" ? "border-t-2 border-amber-400" : ""} ${chat.dragOverGroupReorder === group.id && chat.dragOverGroupZone === "after" ? "border-b-2 border-amber-400" : ""}`}
    data-conv-menu
    draggable="true"
    ondragstart={(event) => handleGroupHeaderDragStart(event, group.id)}
    ondragend={handleGroupHeaderDragEnd}
    ondragenter={(event) => handleGroupHeaderDragOver(event, group.id)}
    ondragover={(event) => handleGroupHeaderDragOver(event, group.id)}
    ondragleave={() => handleGroupHeaderDragLeave(group.id)}
    ondrop={(event) => handleGroupHeaderDrop(event, group.id)}
    title="드래그해서 순서 변경(가장자리) 또는 다른 그룹의 하위로 이동(가운데)"
  >
    <button
      class="grid h-6 w-6 shrink-0 place-items-center rounded text-slate-500 hover:bg-slate-200"
      onclick={() => toggleWebGroupCollapsed(group.id)}
      title={group.collapsed ? "그룹 펼치기" : "그룹 접기"}
      aria-label={group.collapsed ? "그룹 펼치기" : "그룹 접기"}
    >
      {group.collapsed ? "▸" : "▾"}
    </button>
    <div class="min-w-0 flex-1 truncate text-sm font-bold text-slate-800">{group.name}</div>
    <span class="shrink-0 rounded-full bg-slate-200 px-1.5 py-0.5 text-[10px] font-bold text-slate-600">{groupConvs.length}</span>
    <button
      class="grid h-6 w-6 shrink-0 place-items-center rounded-md text-slate-500 hover:bg-slate-200"
      onclick={() => (openMenuId = openMenuId === group.id ? "" : group.id)}
      title="그룹 관리"
      aria-label="그룹 관리"
    >
      ⋯
    </button>
    {#if openMenuId === group.id}
      <div class="absolute right-0 top-8 z-20 w-40 rounded-lg border border-slate-200 bg-white p-1.5 shadow-xl">
        <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-100" onclick={() => { openMenuId = ""; onRename(group); }}>
          이름 변경
        </button>
        <button class="block w-full rounded-md px-3 py-2 text-left text-sm text-rose-700 hover:bg-rose-50" onclick={() => { openMenuId = ""; onDelete(group); }}>
          그룹 삭제
        </button>
      </div>
    {/if}
  </div>
  {#if !group.collapsed}
    <div class="ml-2 space-y-1 border-l border-slate-200 pl-2">
      {#each childGroups as child (child.id)}
        <GroupNode group={child} bind:openMenuId {webConvRow} {onRename} {onDelete} />
      {/each}
      {#if groupConvs.length === 0 && childGroups.length === 0}
        <div class="px-2 py-1.5 text-[11px] text-slate-400">비어 있음</div>
      {/if}
      {#each groupConvs as conv (conv.id)}
        {@render webConvRow(conv)}
      {/each}
    </div>
  {/if}
</div>
