<script lang="ts">
  // A project's docs: the markdown files git tracks in its main checkout, read-only (editing stays in git). The list
  // (no path) has a filter over every doc's path and title, the pinned docs as cards, the folders as a tree beside the
  // recently changed docs. Reading a doc is three columns: the index (the same filter and tree), the doc with its
  // header and its newer/older neighbours, and "On this page"; under 1000 px a doc bar opens the index as a drawer and
  // the sections as a sheet. The page stays mounted from doc to doc (App keys it on the project only), so the filter,
  // the order, the folds and the index's scroll survive a doc change; the open doc is read by its own Live, made per
  // path. The order and the folds are remembered per viewer in localStorage when it is there.
  import { onDestroy, untrack } from "svelte";
  import { Live } from "../lib/live.svelte";
  import { api } from "../lib/api";
  import { md } from "../lib/md";
  import { go } from "../lib/router.svelte";
  import { href } from "../lib/route";
  import {
    defaultOrder, docLabel, docLinks, docTitle, everyFold, fileOf, filterLines, folderOf, isAccepted, matchDocs, matchKey,
    neighbours, outline, paths, pinnedCaption, progress, recent, sameName, tree, unfold, type Folds, type Order,
  } from "../lib/docs";
  import type { Doc, DocList, DocText } from "../lib/types";
  import DocIndex from "../ui/DocIndex.svelte";
  import DocOutline from "../ui/DocOutline.svelte";
  import Rel from "../ui/Rel.svelte";
  import Section from "../ui/Section.svelte";
  import Segmented from "../ui/Segmented.svelte";
  import Stale from "../ui/Stale.svelte";

  let { project, path = "" }: { project: string; path?: string } = $props();
  // svelte-ignore state_referenced_locally (App keys the page on its project: another project is a new page)
  const list = new Live<DocList>((s) => api.docs(project, s));
  list.start();
  onDestroy(() => list.stop());

  // The open doc's read: a Live per path, the old one stopped when the path changes.
  let doc = $state.raw<Live<DocText> | null>(null);
  $effect(() => {
    const p = path;
    if (!p) { doc = null; return; }
    const l = new Live<DocText>((s) => api.doc(project, p, s));
    l.start();
    doc = l;
    return () => l.stop();
  });

  // What a viewer set, kept per viewer when localStorage is there (every access may throw: the page works without).
  const ORDER_KEY = "obor-docs-order";
  // svelte-ignore state_referenced_locally
  const FOLDS_KEY = "obor-docs-folds:" + project;
  function load(key: string): unknown {
    try { const v = localStorage.getItem(key); return v ? JSON.parse(v) : null; } catch { return null; }
  }
  function save(key: string, v: unknown) {
    try { localStorage.setItem(key, JSON.stringify(v)); } catch { /* kept for this page only */ }
  }
  const savedOrder = load(ORDER_KEY);
  const savedFolds = load(FOLDS_KEY);
  // order "" is each folder's default (A–Z for version folders, Newest otherwise); the toggle sets one for all.
  let order = $state<Order | "">(savedOrder === "new" || savedOrder === "az" ? savedOrder : "");
  let folds = $state<Folds>(savedFolds && typeof savedFolds === "object" ? (savedFolds as Folds) : {});
  $effect(() => save(ORDER_KEY, order));
  $effect(() => save(FOLDS_KEY, folds));

  let q = $state(""); // the index's filter, one for the list page and the reading index
  let sel = $state(0); // the marked match (Enter opens it)
  let lq = $state(""); // accepted.md's line filter

  // A doc change: the open doc's folder and its parents unfold, the line filter and the sheets reset.
  $effect(() => {
    const p = path;
    untrack(() => {
      if (p) folds = unfold(folds, p);
      lq = "";
      drawer?.close();
      sheet?.close();
    });
  });

  const all = $derived(new Set(paths(list.data)));
  const total = $derived(all.size);
  const meta = $derived(
    [...(list.data?.pinned || []), ...(list.data?.groups || []).flatMap((g) => g.docs)].find((d) => d.path === path),
  );
  const text = $derived(doc?.data && doc.data.path === path ? doc.data.text : "");
  const title = $derived(docTitle(text));
  const accepted = $derived(isAccepted(path));
  const filtered = $derived(accepted ? filterLines(text, lq) : null);
  // body is the text as shown: the line filter's, without the title heading the header already shows.
  const body = $derived.by(() => {
    const t = filtered ? filtered.text : text;
    if (!title) return t;
    const lines = t.split("\n");
    const i = lines.findIndex((l) => /^#\s+/.test(l) && l.replace(/^#\s+/, "") === title);
    if (i >= 0) lines.splice(i, 1);
    return lines.join("\n");
  });
  const html = $derived(md(body, docLinks(project, all, path)));
  const heads = $derived(outline(body));
  const lineCount = $derived(text ? text.replace(/\n$/, "").split("\n").length : 0);
  const near = $derived(path ? neighbours(list.data, path, order || undefined) : {});
  // byName is whether the open doc's folder lists A–Z (the toggle's, or the folder's default): its neighbours are then
  // previous and next, not newer and older.
  const byName = $derived.by(() => {
    if (order) return order === "az";
    const dir = folderOf(path).replace(/\/$/, "");
    return defaultOrder({ docs: list.data?.groups.find((g) => (g.dir || "") === dir)?.docs || [] }) === "az";
  });
  const matches = $derived(matchDocs(list.data, q));
  const folderCount = $derived.by(() => {
    let n = 0;
    const walk = (f: ReturnType<typeof tree>) => { n += f.folders.length; f.folders.forEach(walk); };
    const root = tree(list.data);
    walk(root);
    return n + (root.docs.length ? 1 : 0);
  });

  // The narrow width's drawer (the index) and sheet (the sections), native dialogs.
  let drawer: HTMLDialogElement | undefined = $state();
  let sheet: HTMLDialogElement | undefined = $state();
  let drawerIndex: DocIndex | undefined = $state();
  let sideIndex: DocIndex | undefined = $state();
  let listInput: HTMLInputElement | undefined = $state();
  function openDrawer() {
    drawer?.showModal();
    drawerIndex?.focus();
  }
  // backdrop closes a dialog on a click outside its box.
  function backdrop(e: MouseEvent) {
    const d = e.currentTarget as HTMLDialogElement;
    if (e.target === d) d.close();
  }

  // The top bar's height: the sticky index, outline and doc bar sit under it, and heading jumps clear it.
  let topH = $state(64);
  $effect(() => {
    const el = document.querySelector<HTMLElement>("header.top");
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => (topH = el.offsetHeight));
    ro.observe(el);
    topH = el.offsetHeight;
    return () => ro.disconnect();
  });

  // How far through the doc the reader is (the doc bar's line).
  let article: HTMLElement | undefined = $state();
  let through = $state(0);
  function onscroll() {
    if (!article) return;
    const r = article.getBoundingClientRect();
    through = progress(r.top - topH, r.height, window.innerHeight - topH);
  }
  $effect(() => { void html; void article; requestAnimationFrame(onscroll); });

  // "/" focuses the filter when no field has focus: the index's (the drawer under 1000 px) or the list's.
  function onkey(e: KeyboardEvent) {
    if (e.key !== "/" || e.metaKey || e.ctrlKey || e.altKey) return;
    const a = document.activeElement as HTMLElement | null;
    if (a && (a.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName))) return;
    if (document.querySelector("dialog[open]")) return;
    e.preventDefault();
    if (!path) { listInput?.focus(); return; }
    if (matchMedia("(max-width: 999px)").matches) openDrawer();
    else sideIndex?.focus();
  }

  function onListKey(e: KeyboardEvent) {
    const filtering = q.trim() !== "";
    const r = matchKey(e.key, sel, filtering ? matches.length : 0);
    if (!r || (!filtering && !r.clear)) return;
    e.preventDefault();
    sel = r.sel;
    if (r.clear) q = "";
    if (r.open) go(href.docs(project, matches[r.sel].doc.path));
  }

  let copied = $state(false);
  async function copyPath() {
    try {
      await navigator.clipboard.writeText(path);
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch { /* no clipboard: nothing to say */ }
  }
</script>

<svelte:window onkeydown={onkey} {onscroll} onresize={onscroll} />

{#snippet age(d: Doc | undefined)}
  {#if d?.updated}<Rel at={d.updated} mode="for" />{:else if d}<span class="staged">staged</span>{/if}
{/snippet}

<div class="page docs-page" class:reading={!!path} style="--top: {topH}px">
  {#if !path}
    <header class="page-head">
      <h1>Docs</h1>
      <span class="muted small">{total} markdown files git tracks{list.data ? " · at " + list.data.head.slice(0, 7) : ""} · read-only</span>
      <Stale error={list.data ? list.error : ""} />
    </header>
  {/if}

  {#if !list.data}
    <p class={list.error ? "failed" : "loading"}>{list.error ? "Could not load the docs: " + list.error : "loading…"}</p>
  {:else if !path}
    {#if !total}
      <div class="card empty">
        <svg width="28" height="28" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.2" aria-hidden="true"><path d="M4 1.8h5.2L12.5 5v9.2H4z"></path><path d="M9 1.8V5.2h3.5"></path></svg>
        <p>git tracks no markdown file here.</p>
      </div>
    {:else}
      <div class="filter-row" role="search">
        <div class="big">
          <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true"><circle cx="7" cy="7" r="5"></circle><path d="M11 11l3 3"></path></svg>
          <input bind:this={listInput} type="search" bind:value={q} oninput={() => (sel = 0)} onkeydown={onListKey}
            aria-label="Filter the docs by name or path" placeholder="Filter {total} docs by name or path" />
          <kbd aria-hidden="true">/</kbd>
        </div>
        <Segmented label="Order in a folder" bind:value={order}
          options={[["", "Default"], ["new", "Newest"], ["az", "A–Z"]]} />
      </div>

      {#if q.trim()}
        <Section title="Matches" count="{matches.length} of {total}" sub="↑ ↓ to move · ↵ opens · Esc clears">
          <div class="card box">
            <DocIndex part="matches" {project} list={list.data} bind:q bind:sel />
          </div>
        </Section>
      {:else}
        {#if list.data.pinned.length}
          <Section title="Pinned">
            <div class="pins">
              {#each list.data.pinned as d (d.path)}
                <a class="card pin" href={href.docs(project, d.path)} title={d.path}>
                  <span class="pin-name" class:mono={!d.title}>{docLabel(d)}</span>
                  {#if pinnedCaption(d.path)}<span class="cap">{pinnedCaption(d.path)}</span>{/if}
                  <span class="pin-foot"><span class="mono ell">{folderOf(d.path) || "top level"}</span><span class="age">{@render age(d)}</span></span>
                </a>
              {/each}
            </div>
          </Section>
        {/if}
        <div class="cols lists">
          <div class="wide">
            <Section title="Folders" count={folderCount} sub="a folder over 12 docs starts folded">
              {#snippet actions()}
                <button type="button" class="btn ghost sm" onclick={() => (folds = everyFold(tree(list.data), true))}>Open all</button>
                <button type="button" class="btn ghost sm" onclick={() => (folds = everyFold(tree(list.data), false))}>Fold all</button>
              {/snippet}
              <div class="card box">
                <DocIndex part="tree" {project} list={list.data} {order} bind:folds />
              </div>
            </Section>
          </div>
          <div class="narrow">
            <Section title="Recently changed" sub="newest commit, any folder">
              <div class="card box recent">
                {#each recent(list.data, 10) as d (d.path)}
                  <a class="rrow" href={href.docs(project, d.path)} title={d.path}>
                    <span class="rline"><span class="ell" class:mono={!d.title}>{docLabel(d)}</span><span class="age">{@render age(d)}</span></span>
                    <span class="mono where ell">{folderOf(d.path) || "top level"}</span>
                  </a>
                {/each}
              </div>
            </Section>
          </div>
        </div>
      {/if}
    {/if}
  {:else}
    <!-- Narrow: the doc bar under the top bar. -->
    <div class="docbar">
      <button type="button" class="btn bar-btn" onclick={openDrawer}>
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><path d="M4 1.8h5.2L12.5 5v9.2H4z"></path><path d="M9 1.8V5.2h3.5"></path></svg>Docs</button>
      <span class="bar-path mono" title={path}><bdi>{path}</bdi></span>
      {#if heads.length >= 2}
        <button type="button" class="btn bar-btn" onclick={() => sheet?.showModal()}>Sections
          <svg width="10" height="10" viewBox="0 0 12 12" aria-hidden="true"><path d="M2.5 4.5L6 8l3.5-3.5" fill="none" stroke="currentColor" stroke-width="1.5"></path></svg></button>
      {/if}
      <span class="bar-line" style:width="{through * 100}%" aria-hidden="true"></span>
    </div>

    <div class="read" class:outlined={heads.length >= 2}>
      <aside class="side">
        <DocIndex bind:this={sideIndex} {project} list={list.data} {path} bind:q bind:sel {order} bind:folds />
      </aside>

      <article class="doc" bind:this={article}>
        <header class="doc-head">
          <div class="path-line mono">
            {#if folderOf(path)}<a href={href.docs(project)}>{folderOf(path)}</a>{/if}<span class="file">{fileOf(path)}</span>
            <button type="button" class="copy" onclick={copyPath} aria-label="Copy the path">{copied ? "Copied" : "Copy path"}</button>
          </div>
          {#if text || !all.has(path) || doc?.error}
            <h1>{title || meta?.title || path}</h1>
          {:else}
            <div class="skel title" aria-hidden="true"></div>
          {/if}
          <div class="meta">
            {#if meta}<span>{#if meta.updated}changed <Rel at={meta.updated} />{:else}<span class="staged">staged</span>{/if}</span>{/if}
            {#if text}<span>{lineCount} lines</span><span>{heads.length} sections</span>{/if}
            <span>at <span class="mono">{list.data.head.slice(0, 7)}</span></span>
            <Stale error={list.error || doc?.error || ""} />
            {#if heads.length >= 2}
              <button type="button" class="btn sm sections" onclick={() => sheet?.showModal()}>Sections · {heads.length}</button>
            {/if}
          </div>
          {#if filtered}
            <div class="lfilter">
              <input class="search" type="search" bind:value={lq} aria-label="Filter the accepted findings"
                placeholder="Filter: RV-…, a package, a word" />
              <span class="muted small">{filtered.shown === filtered.total ? filtered.total : `${filtered.shown} of ${filtered.total}`} lines</span>
            </div>
          {/if}
        </header>

        {#if !all.has(path)}
          <div class="card box missing">
            <p class="failed-line">{path} is not a markdown file git tracks here.</p>
            {#if sameName(list.data, path).length}
              <h2 class="lbl">Same name elsewhere</h2>
              <ul>
                {#each sameName(list.data, path) as d (d.path)}<li><a class="mono" href={href.docs(project, d.path)}>{d.path}</a></li>{/each}
              </ul>
            {/if}
            <a href={href.docs(project)}>All docs</a>
          </div>
        {:else if !text}
          {#if doc?.error}
            <p class="failed-line card box">Could not load {path}: {doc.error}</p>
          {:else}
            <div class="card box skels" aria-busy="true">
              <div class="skel"></div><div class="skel" style:width="92%"></div><div class="skel" style:width="85%"></div>
              <p class="muted small">loading…</p>
            </div>
          {/if}
        {:else}
          <div class="card pad"><div class="prose">{@html html}</div></div>
        {/if}

        {#if all.has(path)}
          <nav class="near" aria-label="Newer and older in {folderOf(path) || 'top level'}">
            {#if near.newer}
              <a class="card nb" href={href.docs(project, near.newer.path)} title={near.newer.path}>
                <span class="nb-lbl">← {byName ? "previous" : "newer"} in {folderOf(path) || "top level"}</span>
                <span class="nb-name" class:mono={!near.newer.title}>{docLabel(near.newer)}</span></a>
            {:else}
              <div class="nb none"><span class="nb-lbl">← {byName ? "previous" : "newer"} in {folderOf(path) || "top level"}</span><span class="nb-name">none</span></div>
            {/if}
            {#if near.older}
              <a class="card nb right" href={href.docs(project, near.older.path)} title={near.older.path}>
                <span class="nb-lbl">{byName ? "next" : "older"} in {folderOf(path) || "top level"} →</span>
                <span class="nb-name" class:mono={!near.older.title}>{docLabel(near.older)}</span></a>
            {:else}
              <div class="nb none right"><span class="nb-lbl">{byName ? "next" : "older"} in {folderOf(path) || "top level"} →</span><span class="nb-name">none</span></div>
            {/if}
          </nav>
        {/if}
      </article>

      {#if heads.length >= 2}
        <aside class="toc" aria-label="On this page">
          <h2 class="lbl">On this page · {heads.length}</h2>
          <DocOutline headings={heads} />
          <button type="button" class="btn sm top-btn" onclick={() => window.scrollTo({ top: 0, behavior: "smooth" })}>↑ Top</button>
        </aside>
      {/if}
    </div>

    <dialog class="drawer" bind:this={drawer} onclick={backdrop} aria-label="Docs">
      <header class="dlg-head">
        <h2>Docs</h2>
        <span class="muted small">{total} · at <span class="mono">{list.data.head.slice(0, 7)}</span></span>
        <button type="button" class="close" aria-label="Close" onclick={() => drawer?.close()}>
          <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true"><path d="M3 3l8 8M11 3l-8 8" stroke="currentColor" stroke-width="1.6"></path></svg></button>
      </header>
      <div class="dlg-body">
        <DocIndex bind:this={drawerIndex} {project} list={list.data} {path} bind:q bind:sel {order} bind:folds tall
          onpick={() => drawer?.close()} />
      </div>
    </dialog>

    <dialog class="sheet" bind:this={sheet} onclick={backdrop} aria-label="Sections">
      <div class="grip" aria-hidden="true"></div>
      <header class="dlg-head">
        <h2>Sections</h2>
        <span class="muted small">{heads.length} in {fileOf(path)}</span>
        <button type="button" class="close" aria-label="Close" onclick={() => sheet?.close()}>
          <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true"><path d="M3 3l8 8M11 3l-8 8" stroke="currentColor" stroke-width="1.6"></path></svg></button>
      </header>
      <div class="dlg-body">
        <DocOutline headings={heads} compact onpick={() => sheet?.close()} />
      </div>
    </dialog>
  {/if}
</div>

<style>
  .docs-page { --scroll-clear: calc(var(--top) + 12px); }
  .docs-page.reading { max-width: 1440px; padding-top: 18px; }
  .pad { padding: 18px 24px; }
  .box { padding: 8px 6px; min-width: 0; }
  .age { margin-left: auto; flex: none; font-size: 11px; color: var(--muted); white-space: nowrap; }
  .staged {
    font-size: 11px; font-weight: 600; padding: 1px 7px; border-radius: 999px; color: var(--info);
    background: color-mix(in srgb, var(--info) var(--tint), transparent);
  }
  .where { font-size: 11px; color: var(--muted); }

  /* the list */
  .empty {
    display: flex; flex-direction: column; align-items: center; gap: 8px; padding: 48px 18px; color: var(--muted);
    text-align: center;
  }
  .empty p { margin: 0; color: var(--fg-2); font-size: 13px; }
  .filter-row { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 12px; }
  .big {
    flex: 1 1 360px; min-width: 0; display: flex; align-items: center; gap: 8px; height: 40px; padding: 0 10px 0 12px;
    border-radius: 9px; border: 1px solid var(--line); background: var(--sunk); color: var(--muted);
  }
  .big:focus-within { border-color: var(--accent); }
  .big input { flex: 1; min-width: 0; height: 100%; border: 0; outline: 0; background: transparent; color: var(--fg); font-size: 14px; }
  kbd { font: 11px var(--mono); color: var(--muted); border: 1px solid var(--line-strong); border-radius: 4px; padding: 0 5px; }
  .pins { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 12px; }
  .pin { display: flex; flex-direction: column; gap: 4px; padding: 12px 14px; color: var(--fg-2); min-width: 0; }
  .pin:hover { text-decoration: none; border-color: var(--line-strong); }
  .pin-name { color: var(--fg); font-size: 13px; font-weight: 600; overflow-wrap: anywhere; }
  .cap { font-size: 12px; color: var(--muted); }
  .pin-foot { display: flex; align-items: center; gap: 8px; margin-top: 4px; font-size: 11px; color: var(--muted); min-width: 0; }
  .recent { display: flex; flex-direction: column; gap: 1px; }
  .rrow { display: flex; flex-direction: column; gap: 1px; padding: 5px 10px; border-radius: 6px; color: var(--fg-2); font-size: 13px; min-width: 0; }
  .rrow:hover { background: var(--surface-2); color: var(--fg); text-decoration: none; }
  .rline { display: flex; align-items: center; gap: 8px; min-width: 0; }

  /* reading */
  .read { display: grid; gap: 24px; grid-template-columns: 248px minmax(0, 1fr); align-items: start; }
  .read.outlined { grid-template-columns: 248px minmax(0, 1fr) 220px; }
  .side, .toc {
    position: sticky; top: calc(var(--top) + 12px); max-height: calc(100vh - var(--top) - 24px); overflow-y: auto;
    overscroll-behavior: contain;
  }
  .toc { display: flex; flex-direction: column; gap: 8px; }
  .top-btn { align-self: flex-start; margin-top: 6px; }
  .doc { min-width: 0; display: flex; flex-direction: column; gap: 14px; }
  .doc-head { display: flex; flex-direction: column; gap: 8px; min-width: 0; }
  .path-line { display: flex; flex-wrap: wrap; align-items: center; gap: 2px 6px; font-size: 12px; color: var(--muted); min-width: 0; }
  .path-line a { color: var(--muted); }
  .path-line .file { color: var(--fg-2); overflow-wrap: anywhere; margin-left: -6px; }
  .copy {
    height: 22px; padding: 0 6px; border: 1px solid var(--line); border-radius: 6px; background: transparent;
    color: var(--muted); font: 11px var(--sans); cursor: pointer;
  }
  .copy:hover { color: var(--fg); }
  .doc-head h1 { margin: 0; font-size: 24px; font-weight: 600; letter-spacing: -0.015em; overflow-wrap: anywhere; }
  .meta { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 14px; font-size: 12px; color: var(--muted); }
  .sections { display: none; }
  .lfilter { display: flex; align-items: center; gap: 12px; }
  .search {
    flex: 1 1 240px; min-width: 0; height: 32px; padding: 0 10px; border-radius: 8px; border: 1px solid var(--line);
    background: var(--sunk); color: var(--fg); font-size: 13px; outline: 0;
  }
  .search:focus { border-color: var(--accent); }
  .skel { height: 10px; border-radius: 4px; background: var(--surface-2); }
  .skel.title { width: 60%; height: 22px; background: var(--surface-3); }
  .skels { display: flex; flex-direction: column; gap: 10px; padding: 18px; }
  .skels p { margin: 4px 0 0; }
  .failed-line { margin: 0; color: var(--bad); font-size: 13px; }
  p.failed-line.card { padding: 16px 18px; }
  .missing { display: flex; flex-direction: column; align-items: flex-start; gap: 10px; padding: 16px 18px; }
  .missing h2 { margin-top: 4px; }
  .missing ul { margin: 0; padding-left: 18px; }
  .near { display: flex; flex-wrap: wrap; gap: 12px; }
  .nb { flex: 1 1 220px; display: flex; flex-direction: column; gap: 2px; padding: 12px 14px; min-width: 0; color: var(--fg-2); }
  .nb.right { text-align: right; }
  a.nb:hover { text-decoration: none; border-color: var(--line-strong); }
  .nb.none { border: 1px dashed var(--line); border-radius: 10px; }
  .nb-lbl { font-size: 11px; color: var(--muted); }
  .nb-name { font-size: 13px; overflow-wrap: anywhere; }
  .nb.none .nb-name { color: var(--muted); }

  /* the doc bar (narrow) */
  .docbar {
    display: none; position: sticky; top: var(--top); z-index: 4; align-items: center; gap: 8px; padding: 8px 0;
    margin: -8px 0 0; background: var(--bg); border-bottom: 1px solid var(--line-soft);
  }
  .bar-btn { height: 44px; flex: none; }
  .bar-path {
    flex: 1; min-width: 0; font-size: 12px; color: var(--muted); overflow: hidden; text-overflow: ellipsis;
    white-space: nowrap; direction: rtl; text-align: left;
  }
  .bar-line { position: absolute; left: 0; bottom: -1px; height: 2px; background: var(--accent); }

  /* the drawer and the sheet */
  dialog {
    padding: 0; border: 1px solid var(--line); background: var(--side); color: var(--fg); box-shadow: var(--shadow);
    max-width: 100vw; max-height: 100vh;
  }
  dialog::backdrop { background: rgba(0, 0, 0, 0.55); }
  dialog[open] { display: flex; flex-direction: column; }
  .drawer { margin: 0; inset: 0 auto 0 0; width: min(340px, 92vw); height: 100vh; border-width: 0 1px 0 0; }
  .sheet {
    margin: auto 0 0; inset: auto 0 0 0; width: 100vw; max-height: 72vh; background: var(--surface);
    border-radius: 14px 14px 0 0; border-width: 1px 0 0;
  }
  .grip { align-self: center; width: 36px; height: 4px; border-radius: 2px; background: var(--line-strong); margin-top: 8px; flex: none; }
  .dlg-head { display: flex; align-items: center; gap: 8px; padding: 6px 8px 6px 16px; border-bottom: 1px solid var(--line-soft); flex: none; }
  .dlg-head h2 { margin: 0; font-size: 15px; font-weight: 600; }
  .close {
    margin-left: auto; width: 44px; height: 44px; border: 0; background: transparent; color: var(--muted);
    display: flex; align-items: center; justify-content: center; cursor: pointer;
  }
  .close:hover { color: var(--fg); }
  .dlg-body { flex: 1; overflow-y: auto; padding: 12px; overscroll-behavior: contain; }

  @media (max-width: 1279px) {
    .read.outlined { grid-template-columns: 248px minmax(0, 1fr); }
    .toc { display: none; }
    .sections { display: inline-flex; }
    /* 1000–1279: the Sections list as a popover at the top right */
    .sheet { margin: calc(var(--top) + 8px) 16px auto auto; inset: 0 0 auto auto; width: 320px; max-height: 70vh; border-radius: 10px; border-width: 1px; }
    .sheet .grip { display: none; }
  }
  @media (max-width: 999px) {
    .docs-page { --scroll-clear: calc(var(--top) + 72px); }
    .docs-page.reading { padding-top: 8px; }
    .read, .read.outlined { grid-template-columns: minmax(0, 1fr); }
    .side { display: none; }
    .sections { display: none; }
    .docbar { display: flex; }
    .sheet { margin: auto 0 0; inset: auto 0 0 0; width: 100vw; max-height: 72vh; border-radius: 14px 14px 0 0; border-width: 1px 0 0; }
    .sheet .grip { display: block; }
    .doc-head h1 { font-size: 20px; }
    .pad { padding: 14px 16px; }
  }
  @media (max-width: 860px) {
    .lists .narrow { order: -1; }
    .recent .rrow:nth-child(n + 6) { display: none; }
  }
</style>
