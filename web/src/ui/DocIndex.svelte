<script lang="ts">
  // DocIndex is the docs' index: a filter, All docs, Pinned, and the folder tree (folders fold; the open doc marked
  // with aria-current and an accent edge), or with a filter typed the flat list of matches (↑/↓ move, Enter opens, Esc
  // clears). Given the page's text search, a toggle beside the filter sets its mode to "Search the text": the matching
  // paths first, under them the text hits grouped by doc, each its line number and the line with the words marked
  // (a hit opens its doc and tells the page its line, onhit); a hit shows only under the words it answers. One component
  // for the reading sidebar and the narrow drawer (part "side"); the list page uses its tree ("tree") and its matches
  // ("matches", hits included) on their own, its filter's keys through key. The filter, mode, order and folds belong to
  // the page (they outlive a doc change). A title shows its inline markdown; the match rows mark its words inside that markup.
  // A doc's kind shows as a dot (a folder's before its name; a doc row's in the tree only where it differs from its
  // folder's; the flat rows keep a gap when it has none), an age by its freshness (DocAge), the open doc and its folder
  // chain in fg with the accent tint, the Enter target with the accent ring. The count line is DocCount's (the list page
  // shows the same one over its matches).
  import { href } from "../lib/route";
  import {
    docKind, folderOf, groupHits, KIND_TONE, labelHtml, markHtml, marks, matchDocs, matchKey, rows, tree, type DocKind,
    type Folder, type Folds, type Match, type Order,
  } from "../lib/docs";
  import type { TextSearch } from "../lib/docsearch.svelte";
  import type { Doc, DocHit, DocList } from "../lib/types";
  import DocAge from "./DocAge.svelte";
  import DocCount from "./DocCount.svelte";
  import DocModes from "./DocModes.svelte";
  import Dot from "./Dot.svelte";

  let {
    project, list, path = "", q = $bindable(""), order = "", folds = $bindable({}), sel = $bindable(0),
    part = "side", tall = false, onpick = undefined, mode = $bindable("path"), search = undefined, onhit = undefined,
  }: {
    project: string; list: DocList; path?: string; q?: string; order?: Order | ""; folds?: Folds; sel?: number;
    part?: "side" | "tree" | "matches"; tall?: boolean; onpick?: () => void;
    mode?: "path" | "text"; search?: TextSearch; onhit?: (path: string, line: number) => void;
  } = $props();

  // mode is what the filter searches: "path" the docs' paths and titles, "text" their text too (only with a search).
  const text = $derived(mode === "text" && !!search);
  const total = $derived(list.pinned.length + list.groups.reduce((n, g) => n + g.docs.length, 0));
  const filtering = $derived(q.trim() !== "");
  const matches: Match[] = $derived(matchDocs(list, q));
  const found = $derived(text ? search?.resultFor(q) : null);
  const groups = $derived(groupHits(found?.hits));
  const hits: DocHit[] = $derived(groups.flatMap((g) => g.hits));
  // byPath is each doc by its path: a hit group's label.
  const byPath = $derived(new Map([...list.pinned, ...list.groups.flatMap((g) => g.docs)].map((d) => [d.path, d])));
  // hitAt is a hit's place in the marked order (after the path matches), for ↑/↓ and Enter.
  const hitAt = $derived(new Map(hits.map((h, i) => [h, matches.length + i])));
  const count = $derived(matches.length + hits.length);
  const root = $derived(tree(list, order || undefined));
  const shown = $derived(rows(root, folds));

  let input: HTMLInputElement | undefined = $state();
  // focus puts the cursor in the filter (the page's "/" and the drawer's opening).
  export function focus() {
    input?.focus();
    input?.select();
  }

  // key is what a key in the filter does: ↑/↓ mark a match or a hit, Enter opens the marked one, Escape clears (the
  // list page's own filter calls it too).
  export function key(e: KeyboardEvent) {
    const r = matchKey(e.key, sel, filtering ? count : 0);
    if (!r) return;
    if (!filtering && !r.clear) return;
    e.preventDefault();
    sel = r.sel;
    if (r.clear) q = "";
    if (!r.open) return;
    if (r.sel < matches.length) {
      location.hash = href.docs(project, matches[r.sel].doc.path);
      onpick?.();
      return;
    }
    const h = hits[r.sel - matches.length];
    location.hash = href.docs(project, h.path);
    pickHit(h);
  }
  function pickHit(h: DocHit) {
    onhit?.(h.path, h.line);
    onpick?.();
  }
  function modeSet() {
    sel = 0;
    input?.focus();
  }
  // dirKind is a folder's kind ("" for the top level); chain is whether a folder row is on the open doc's path.
  const dirKind = (f: Folder): DocKind | "" => (f.dir ? docKind(f.dir + "/") : "");
  const chain = (f: Folder) => !!path && (f.dir ? folderOf(path).startsWith(f.dir + "/") : folderOf(path) === "");
  // newest is a folder row's newest age: its docs and below; the top level's own docs only (its folders have rows).
  const newest = (f: Folder) => (f.dir ? f.updated : Math.max(0, ...f.docs.map((d) => d.updated || 0)) || undefined);
  function toggle(dir: string, open: boolean) {
    folds = { ...folds, [dir]: !open };
  }
</script>

{#snippet age(d: Doc)}
  <span class="when"><DocAge updated={d.updated} /></span>
{/snippet}

{#snippet kdot(p: string)}
  {@const k = docKind(p)}
  {#if k}<span class="kdot"><Dot tone={KIND_TONE[k]} size={6} title={k} /></span>{:else}<span class="kdot"></span>{/if}
{/snippet}

{#snippet match(m: Match, i: number)}
  <a class="match" class:sel={i === sel} class:on={m.doc.path === path} href={href.docs(project, m.doc.path)}
    title={m.doc.path} aria-current={m.doc.path === path ? "page" : undefined} onclick={() => onpick?.()}>
    <span class="mline">
      {@render kdot(m.doc.path)}
      <span class="name" class:mono={!m.doc.title}>{@html markHtml(labelHtml(m.doc), q)}</span>
      {@render age(m.doc)}
    </span>
    <span class="where mono">{#if m.doc.title}{#each marks(m.doc.path, q) as p, k (k)}{#if p.hit}<mark>{p.text}</mark>{:else}{p.text}{/if}{/each}{:else}{folderOf(m.doc.path) || "top level"}{/if}</span>
  </a>
{/snippet}

{#snippet hitList()}
  {#if groups.length}
    <h2 class="lbl">In the text</h2>
    {#each groups as g (g.path)}
      {@const d = byPath.get(g.path)}
      <div class="hgroup" role="list" aria-label="Lines in {g.path}">
        <a class="hdoc" href={href.docs(project, g.path)} title={g.path} onclick={() => onpick?.()}>
          {@render kdot(g.path)}
          <span class="name ell" class:mono={!d?.title}>{#if d}{@html labelHtml(d)}{:else}{g.path}{/if}</span>
          <span class="hcount">{g.hits.length}</span>
        </a>
        {#each g.hits as h (h.line)}
          <div role="listitem">
            <a class="hit" class:sel={hitAt.get(h) === sel} href={href.docs(project, h.path)} title="{h.path}:{h.line}"
              onclick={() => pickHit(h)}>
              <span class="ln mono">{h.line}</span>
              <span class="htext">{#each marks(h.text, q) as p, k (k)}{#if p.hit}<mark>{p.text}</mark>{:else}{p.text}{/if}{/each}</span>
            </a>
          </div>
        {/each}
      </div>
    {/each}
    {#if found?.truncated}<p class="note">showing the first 200 lines</p>{/if}
  {/if}
  {#if search?.error}<p class="note bad">Could not search the text: {search.error}</p>{/if}
{/snippet}

{#snippet matchList()}
  <div class="matches" role="list" aria-label="Matches">
    {#each matches as m, i (m.doc.path)}<div role="listitem">{@render match(m, i)}</div>{/each}
  </div>
  {#if text}
    {@render hitList()}
    {#if !matches.length && !hits.length && !search?.busy && !search?.error}
      <p class="none">No doc's path, title or text has every word of “{q.trim()}”.
        <button type="button" class="link" onclick={() => { q = ""; sel = 0; }}>Clear the filter</button></p>
    {:else if !hits.length && search?.busy}
      <p class="note"><span class="busy"><Dot tone="info" size={7} title="searching" />searching the text…</span></p>
    {/if}
  {:else if !matches.length}
    <p class="none">No doc's path or title has every word of “{q.trim()}”.
      <button type="button" class="link" onclick={() => { q = ""; sel = 0; }}>Clear the filter</button></p>
  {/if}
{/snippet}

{#snippet treeRows(full: boolean)}
  <div class="tree">
    {#each shown as r (r.kind === "dir" ? "d:" + r.folder.dir : r.doc.path)}
      {#if r.kind === "dir"}
        {@const fk = dirKind(r.folder)}
        <button type="button" class="dir" class:chain={chain(r.folder)} style:padding-left="{8 + r.depth * 16}px"
          aria-expanded={r.open} onclick={() => toggle(r.folder.dir, r.open)}>
          <svg class="chev" class:open={r.open} width="10" height="10" viewBox="0 0 12 12" aria-hidden="true"><path d="M4 2.5L7.5 6L4 9.5" fill="none" stroke="currentColor" stroke-width="1.5"></path></svg>
          {#if fk}<Dot tone={KIND_TONE[fk]} title={fk} />{/if}
          <span class="mono ell">{r.folder.dir ? r.folder.name + "/" : r.folder.name}</span>
          <span class="count">{r.folder.dir ? r.folder.count : r.folder.docs.length}</span>
          {#if full && newest(r.folder)}<span class="newest">newest <DocAge updated={newest(r.folder)} /></span>{/if}
        </button>
      {:else}
        {@const k = docKind(r.doc.path)}
        <a class="doc" class:on={r.doc.path === path} style:padding-left="{8 + r.depth * 16 + 4}px"
          href={href.docs(project, r.doc.path)} title={r.doc.path}
          aria-current={r.doc.path === path ? "page" : undefined} onclick={() => onpick?.()}>
          {#if k && k !== docKind(folderOf(r.doc.path))}<span class="kdot"><Dot tone={KIND_TONE[k]} size={6} title={k} /></span>{/if}
          <span class="name ell" class:mono={!r.doc.title}>{@html labelHtml(r.doc)}</span>
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
      <input bind:this={input} class="search" type="search" bind:value={q} oninput={() => (sel = 0)} onkeydown={key}
        aria-label={text ? "Search the docs' text" : "Filter the docs"} placeholder={text ? "Search the text" : "Filter docs"} />
      <kbd aria-hidden="true">/</kbd>
    </div>
    {#if search}
      <div class="modes"><DocModes bind:mode {tall} onchange={modeSet} /></div>
    {/if}
    {#if filtering}
      <p class="count-line"><DocCount {list} {q} {text} {search} /></p>
    {/if}
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
              {@render kdot(d.path)}
              <span class="name ell" class:mono={!d.title}>{#if d.title}{@html labelHtml(d)}{:else}{d.path}{/if}</span></a>
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
  .count-line { margin: -4px 2px 0; }
  .busy { display: inline-flex; align-items: center; gap: 6px; }
  .kdot { flex: none; width: 6px; display: inline-flex; align-items: center; }
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
  .doc.on, .match.on {
    background: color-mix(in srgb, var(--accent) 12%, transparent); color: var(--fg); font-weight: 600;
    box-shadow: inset 2px 0 0 var(--accent);
  }
  .dir {
    display: flex; align-items: center; gap: 6px; width: 100%; min-height: 26px; padding: 4px 8px; border: 0;
    border-radius: 6px; background: transparent; color: var(--fg-2); font-size: 12px; font-weight: 600;
    text-align: left; cursor: pointer; margin-top: 2px; min-width: 0;
  }
  .dir.chain { color: var(--fg); }
  .chev { flex: none; color: var(--muted); transition: transform 0.12s; }
  .chev.open { transform: rotate(90deg); }
  .count { margin-left: auto; font-size: 11px; font-weight: 400; color: var(--muted); font-variant-numeric: tabular-nums; }
  .newest { font-size: 11px; font-weight: 400; color: var(--muted); white-space: nowrap; min-width: 92px; text-align: right; }
  .name { min-width: 0; flex: 1 1 auto; }
  .when { margin-left: auto; flex: none; display: inline-flex; font-size: 11px; font-weight: 400; }
  .matches { display: flex; flex-direction: column; gap: 1px; min-width: 0; }
  .match {
    display: flex; flex-direction: column; gap: 1px; padding: 5px 10px; border-radius: 6px; color: var(--fg-2);
    font-size: 13px; min-width: 0;
  }
  .match.sel, .hit.sel {
    background: color-mix(in srgb, var(--accent) 10%, transparent);
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--accent) 50%, transparent);
  }
  .match.sel.on { box-shadow: inset 2px 0 0 var(--accent), inset 0 0 0 1px color-mix(in srgb, var(--accent) 50%, transparent); }
  .mline { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .mline .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .where { font-size: 11px; color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tall .match, .tall .doc, .tall .dir { min-height: 44px; }
  mark, .name :global(mark) { background: color-mix(in srgb, var(--accent) 22%, transparent); color: var(--fg); border-radius: 3px; }
  .modes { margin-top: -4px; }
  .body .lbl { margin: 10px 8px 2px; }
  .hgroup { display: flex; flex-direction: column; gap: 1px; min-width: 0; margin-top: 4px; }
  .hdoc, .hit { text-decoration: none; }
  .hdoc {
    display: flex; align-items: center; gap: 8px; padding: 3px 10px; border-radius: 6px; font-size: 12px; font-weight: 600;
    color: var(--fg-2); min-width: 0;
  }
  .hit {
    display: flex; align-items: baseline; gap: 8px; padding: 3px 10px 3px 14px; border-radius: 6px; font-size: 12px;
    color: var(--fg-2); min-width: 0;
  }
  .hdoc:hover, .hit:hover { background: var(--surface-2); color: var(--fg); }
  .hcount {
    margin-left: auto; flex: none; font-size: 11px; font-weight: 600; padding: 0 6px; border-radius: 999px;
    background: var(--surface-3); color: var(--fg-2); font-variant-numeric: tabular-nums;
  }
  .ln { flex: none; min-width: 3ch; text-align: right; font-size: 11px; color: var(--muted); font-variant-numeric: tabular-nums; }
  .htext {
    min-width: 0; overflow: hidden; overflow-wrap: anywhere; display: -webkit-box; -webkit-box-orient: vertical;
    -webkit-line-clamp: 2; line-clamp: 2;
  }
  .tall .hit, .tall .hdoc { min-height: 44px; align-items: center; }
  .note { margin: 6px 10px; font-size: 12px; color: var(--muted); }
  .note.bad { color: var(--bad); }
  .none { margin: 12px 8px; text-align: center; font-size: 13px; color: var(--muted); }
  .link { background: none; border: 0; padding: 0; color: var(--accent); cursor: pointer; font-size: 13px; }
</style>
