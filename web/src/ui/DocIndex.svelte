<script lang="ts">
  // DocIndex is the docs' index: a filter, All docs, Pinned, and the folder tree (folders fold; the open doc marked
  // with aria-current and an accent edge), or with a filter typed the flat list of matches (↑/↓ move, Enter opens, Esc
  // clears). One component for the reading sidebar and the narrow drawer (part "side"); the list page uses its tree
  // ("tree") and its matches ("matches") on their own. The filter, order and folds belong to the page (they outlive a
  // doc change).
  import { href } from "../lib/route";
  import {
    docLabel, folderOf, marks, matchDocs, matchKey, rows, tree, type Folder, type Folds, type Match, type Order,
  } from "../lib/docs";
  import type { Doc, DocList } from "../lib/types";
  import Rel from "./Rel.svelte";

  let {
    project, list, path = "", q = $bindable(""), order = "", folds = $bindable({}), sel = $bindable(0),
    part = "side", tall = false, onpick = undefined,
  }: {
    project: string; list: DocList; path?: string; q?: string; order?: Order | ""; folds?: Folds; sel?: number;
    part?: "side" | "tree" | "matches"; tall?: boolean; onpick?: () => void;
  } = $props();

  // FilterMode is what the filter searches: the docs' paths and titles. (A second mode, the docs' text, comes later.)
  type FilterMode = "path";
  const mode: FilterMode = "path";
  const total = $derived(list.pinned.length + list.groups.reduce((n, g) => n + g.docs.length, 0));
  const filtering = $derived(q.trim() !== "");
  const matches: Match[] = $derived(mode === "path" ? matchDocs(list, q) : []);
  const root = $derived(tree(list, order || undefined));
  const shown = $derived(rows(root, folds));

  let input: HTMLInputElement | undefined = $state();
  // focus puts the cursor in the filter (the page's "/" and the drawer's opening).
  export function focus() {
    input?.focus();
    input?.select();
  }

  function onkey(e: KeyboardEvent) {
    const r = matchKey(e.key, sel, filtering ? matches.length : 0);
    if (!r) return;
    if (!filtering && !r.clear) return;
    e.preventDefault();
    sel = r.sel;
    if (r.clear) q = "";
    if (r.open) { location.hash = href.docs(project, matches[r.sel].doc.path); onpick?.(); }
  }
  // newest is a folder row's newest age: its docs and below; the top level's own docs only (its folders have rows).
  const newest = (f: Folder) => (f.dir ? f.updated : Math.max(0, ...f.docs.map((d) => d.updated || 0)) || undefined);
  function toggle(dir: string, open: boolean) {
    folds = { ...folds, [dir]: !open };
  }
</script>

{#snippet age(d: Doc)}
  {#if d.updated}<span class="age"><Rel at={d.updated} mode="for" /></span>{:else}<span class="staged">staged</span>{/if}
{/snippet}

{#snippet match(m: Match, i: number)}
  {@const label = docLabel(m.doc)}
  <a class="match" class:sel={i === sel} class:on={m.doc.path === path} href={href.docs(project, m.doc.path)}
    title={m.doc.path} aria-current={m.doc.path === path ? "page" : undefined} onclick={() => onpick?.()}>
    <span class="mline">
      <span class="name" class:mono={!m.doc.title}>{#each marks(label, q) as p, k (k)}{#if p.hit}<mark>{p.text}</mark>{:else}{p.text}{/if}{/each}</span>
      {@render age(m.doc)}
    </span>
    <span class="where mono">{#if m.doc.title}{#each marks(m.doc.path, q) as p, k (k)}{#if p.hit}<mark>{p.text}</mark>{:else}{p.text}{/if}{/each}{:else}{folderOf(m.doc.path) || "top level"}{/if}</span>
  </a>
{/snippet}

{#snippet matchList()}
  <div class="matches" role="list" aria-label="Matches">
    {#each matches as m, i (m.doc.path)}<div role="listitem">{@render match(m, i)}</div>{/each}
  </div>
  {#if !matches.length}
    <p class="none">No doc's path or title has every word of “{q.trim()}”.
      <button type="button" class="link" onclick={() => { q = ""; sel = 0; }}>Clear the filter</button></p>
  {/if}
{/snippet}

{#snippet treeRows(full: boolean)}
  <div class="tree">
    {#each shown as r (r.kind === "dir" ? "d:" + r.folder.dir : r.doc.path)}
      {#if r.kind === "dir"}
        <button type="button" class="dir" style:padding-left="{8 + r.depth * 16}px" aria-expanded={r.open}
          onclick={() => toggle(r.folder.dir, r.open)}>
          <svg class="chev" class:open={r.open} width="10" height="10" viewBox="0 0 12 12" aria-hidden="true"><path d="M4 2.5L7.5 6L4 9.5" fill="none" stroke="currentColor" stroke-width="1.5"></path></svg>
          <span class="mono ell">{r.folder.dir ? r.folder.name + "/" : r.folder.name}</span>
          <span class="count">{r.folder.dir ? r.folder.count : r.folder.docs.length}</span>
          {#if full && newest(r.folder)}<span class="newest">newest <Rel at={newest(r.folder)} mode="for" /></span>{/if}
        </button>
      {:else}
        <a class="doc" class:on={r.doc.path === path} style:padding-left="{8 + r.depth * 16 + 4}px"
          href={href.docs(project, r.doc.path)} title={r.doc.path}
          aria-current={r.doc.path === path ? "page" : undefined} onclick={() => onpick?.()}>
          <span class="name ell" class:mono={!r.doc.title}>{docLabel(r.doc)}</span>
          {#if full}{@render age(r.doc)}{/if}
        </a>
      {/if}
    {/each}
  </div>
{/snippet}

{#if part === "tree"}
  {@render treeRows(true)}
{:else if part === "matches"}
  {@render matchList()}
{:else}
  <nav class="index" class:tall aria-label="Docs index">
    <div class="filter">
      <input bind:this={input} class="search" type="search" bind:value={q} oninput={() => (sel = 0)} onkeydown={onkey}
        aria-label="Filter the docs" placeholder="Filter docs" />
      <kbd aria-hidden="true">/</kbd>
    </div>
    {#if filtering}<p class="count-line">{matches.length} of {total}</p>{/if}
    <div class="card body">
      {#if filtering}
        {@render matchList()}
      {:else}
        <a class="all" href={href.docs(project)} onclick={() => onpick?.()}>
          <svg width="12" height="12" viewBox="0 0 12 12" aria-hidden="true"><path d="M7.5 2.5L4 6l3.5 3.5" fill="none" stroke="currentColor" stroke-width="1.5"></path></svg>
          All docs · {total}</a>
        {#if list.pinned.length}
          <h2 class="lbl">Pinned</h2>
          {#each list.pinned as d (d.path)}
            <a class="doc" class:on={d.path === path} href={href.docs(project, d.path)} title={d.path}
              aria-current={d.path === path ? "page" : undefined} onclick={() => onpick?.()}>
              <span class="name ell" class:mono={!d.title}>{d.title || d.path}</span></a>
          {/each}
        {/if}
        {#if shown.length}
          <h2 class="lbl">Folders</h2>
          {@render treeRows(false)}
        {/if}
      {/if}
    </div>
  </nav>
{/if}

<style>
  .index { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
  .filter { position: relative; }
  .search {
    width: 100%; height: 32px; padding: 0 34px 0 10px; border-radius: 8px; border: 1px solid var(--line);
    background: var(--sunk); color: var(--fg); font-size: 13px; outline: 0;
  }
  .search:focus { border-color: var(--accent); }
  .tall .search { height: 44px; font-size: 15px; }
  kbd {
    position: absolute; right: 8px; top: 50%; transform: translateY(-50%); font: 11px var(--mono); color: var(--muted);
    border: 1px solid var(--line-strong); border-radius: 4px; padding: 0 5px;
  }
  .count-line { margin: -4px 2px 0; font-size: 12px; color: var(--muted); }
  .body { padding: 8px 6px; display: flex; flex-direction: column; gap: 1px; min-width: 0; }
  .body h2 { margin: 10px 8px 2px; }
  .all {
    display: flex; align-items: center; gap: 6px; padding: 4px 8px; border-radius: 6px; font-size: 12px;
    color: var(--muted);
  }
  .tree { display: flex; flex-direction: column; gap: 1px; min-width: 0; }
  .doc, .dir, .all, .match { text-decoration: none; }
  .doc {
    display: flex; align-items: center; gap: 8px; min-height: 26px; padding: 3px 8px; border-radius: 6px;
    font-size: 12px; color: var(--fg-2); min-width: 0;
  }
  .doc:hover, .dir:hover, .all:hover, .match:hover { background: var(--surface-2); color: var(--fg); }
  .doc.on { background: var(--surface-3); color: var(--fg); font-weight: 600; box-shadow: inset 2px 0 0 var(--accent); }
  .dir {
    display: flex; align-items: center; gap: 6px; width: 100%; min-height: 26px; padding: 4px 8px; border: 0;
    border-radius: 6px; background: transparent; color: var(--fg-2); font-size: 12px; font-weight: 600;
    text-align: left; cursor: pointer; margin-top: 2px; min-width: 0;
  }
  .chev { flex: none; color: var(--muted); transition: transform 0.12s; }
  .chev.open { transform: rotate(90deg); }
  .count { margin-left: auto; font-size: 11px; font-weight: 400; color: var(--muted); font-variant-numeric: tabular-nums; }
  .newest { font-size: 11px; font-weight: 400; color: var(--muted); white-space: nowrap; min-width: 92px; text-align: right; }
  .name { min-width: 0; flex: 1 1 auto; }
  .age { margin-left: auto; flex: none; font-size: 11px; color: var(--muted); white-space: nowrap; }
  .staged {
    margin-left: auto; flex: none; font-size: 11px; font-weight: 600; padding: 1px 7px; border-radius: 999px;
    color: var(--info); background: color-mix(in srgb, var(--info) var(--tint), transparent);
  }
  .matches { display: flex; flex-direction: column; gap: 1px; min-width: 0; }
  .match {
    display: flex; flex-direction: column; gap: 1px; padding: 5px 10px; border-radius: 6px; color: var(--fg-2);
    font-size: 13px; min-width: 0;
  }
  .match.sel { background: var(--surface-3); box-shadow: inset 0 0 0 1px var(--line-strong); }
  .match.on { color: var(--fg); font-weight: 600; }
  .mline { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .mline .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .where { font-size: 11px; color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tall .match, .tall .doc, .tall .dir { min-height: 44px; }
  mark { background: color-mix(in srgb, var(--accent) 22%, transparent); color: var(--fg); border-radius: 3px; }
  .none { margin: 12px 8px; text-align: center; font-size: 13px; color: var(--muted); }
  .link { background: none; border: 0; padding: 0; color: var(--accent); cursor: pointer; font-size: 13px; }
</style>
