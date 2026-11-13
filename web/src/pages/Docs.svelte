<script lang="ts">
  // A project's docs: the markdown files git tracks in its main checkout, read-only (editing stays in git). The list
  // (no path) has a filter over every doc's path and title, the pinned docs as cards, the folders as a tree beside the
  // recently changed docs. Reading a doc is three columns: the index (the same filter and tree), the doc with its
  // header and its newer/older neighbours, and "On this page"; under 1000 px a doc bar opens the index as a drawer and
  // the sections as a sheet. The page stays mounted from doc to doc (App keys it on the project only), so the filter,
  // the order, the folds and the index's scroll survive a doc change; the open doc is read by its own Live, made per
  // path. The order and the folds are remembered per viewer in localStorage when it is there. The list's filter and the
  // index's can search the docs' text too (one mode and one TextSearch for the list, the sidebar and the drawer); a
  // text hit opens its doc scrolled to the nearest heading above its line. A doc's section link
  // (`#/docs/<project>/<path>#<heading id>`) opens it scrolled to that heading; a heading picked in "On this page" or a
  // text hit's heading goes into the URL in place of the shown one, and every rendered heading, the title in the header
  // too, carries a "Copy link" anchor. A title shows its inline markdown (code, bold, links) wherever it is a label. A
  // doc's kind (docKind) shows as a dot beside its name and a tint on its pinned card; ages show by freshness (DocAge).
  import { onDestroy, tick, untrack } from "svelte";
  import { Live } from "../lib/live.svelte";
  import { api } from "../lib/api";
  import { TextSearch } from "../lib/docsearch.svelte";
  import { headings, inline, md } from "../lib/md";
  import { replace } from "../lib/router.svelte";
  import { href } from "../lib/route";
  import {
    defaultOrder, docKind, docLinks, docTitle, everyFold, KIND_TONE, fileOf, filterLines, folderOf, isAccepted, jumpReady, labelHtml, matchDocs,
    neighbours, outline, paths, pinnedCaption, progress, recent, sameName, sectionAt, splitTitle, tree, unfold,
    type Folds, type Order,
  } from "../lib/docs";
  import type { Doc, DocList, DocText } from "../lib/types";
  import DocAge from "../ui/DocAge.svelte";
  import DocIndex from "../ui/DocIndex.svelte";
  import DocModes from "../ui/DocModes.svelte";
  import DocOutline from "../ui/DocOutline.svelte";
  import Dot from "../ui/Dot.svelte";
  import Pill from "../ui/Pill.svelte";
  import Section from "../ui/Section.svelte";
  import Segmented from "../ui/Segmented.svelte";
  import Stale from "../ui/Stale.svelte";

  let { project, path = "", section = "" }: { project: string; path?: string; section?: string } = $props();
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
  let mode = $state<"path" | "text">("path"); // what the index's filter searches

  // The text search: the filter's text while the mode is "text" (on the list and in a doc), nothing otherwise.
  // svelte-ignore state_referenced_locally
  const search = new TextSearch((t, s) => api.docsearch(project, t, s));
  onDestroy(() => search.stop());
  $effect(() => search.set(mode === "text" ? q : ""));
  // jump is the line a text hit opened: once its doc is shown, the page scrolls to the nearest heading above it.
  let jump = $state<{ path: string; line: number } | null>(null);
  // scrolled is the path#section the page last scrolled to: a section already shown is not scrolled to again when the
  // doc is read once more.
  let scrolled = "";

  // A doc change: the open doc's folder and its parents unfold, the line filter and the sheets reset, and a section
  // link in the URL scrolls again when the doc is back.
  $effect(() => {
    const p = path;
    untrack(() => {
      if (p) folds = unfold(folds, p);
      lq = "";
      scrolled = "";
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
  const kind = $derived(docKind(path));
  const accepted = $derived(isAccepted(path));
  const filtered = $derived(accepted ? filterLines(text, lq) : null);
  // body is the text as shown (the line filter's), rendered whole so the title's heading id and every other one come
  // from one slugger; the title's heading is then cut out of the HTML into the header (its id and anchor with it).
  const body = $derived(filtered ? filtered.text : text);
  const titleId = $derived(title ? headings(body).find((h) => h.level === 1 && h.text === title)?.id || "" : "");
  const rendered = $derived(splitTitle(md(body, docLinks(project, all, path)), titleId));
  const html = $derived(rendered.rest);
  $effect(() => {
    const j = jump;
    if (!j || j.path !== path) return;
    const id = sectionAt(text, body, j.line);
    if (!jumpReady(body, !!list.data, id)) return;
    jump = null;
    void tick().then(() => {
      const el = id ? document.getElementById(id) : null;
      if (el) el.scrollIntoView({ block: "start" });
      else window.scrollTo(0, 0);
      if (id && j.path === path) showSection(id);
    });
  });
  // A section link scrolls to its heading once the doc is shown with it (jumpReady); an id no heading has leaves the
  // doc at its top.
  $effect(() => {
    const s = section;
    if (!s || !path || !jumpReady(body, !!list.data, s)) return;
    if (scrolled === path + "#" + s) return;
    scrolled = path + "#" + s;
    void tick().then(() => document.getElementById(s)?.scrollIntoView({ block: "start" }));
  });
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
  // listCount is the list's count over its matches: by name, and in text mode the lines found and a search out.
  const listCount = $derived.by(() => {
    if (mode !== "text") return `${matches.length} of ${total}`;
    const found = search.resultFor(q);
    const n = found?.hits.length ?? 0;
    return [`${matches.length} of ${total} by name`, found ? `${n} ${n === 1 ? "line" : "lines"} in the text` : "",
      search.busy ? "searching…" : ""].filter(Boolean).join(" · ");
  });
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
  let listIndex: DocIndex | undefined = $state();
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

  // The list's filter keys go to its matches (a path match or a text hit), shown while something is typed.
  function onListKey(e: KeyboardEvent) {
    if (listIndex) listIndex.key(e);
    else if (e.key === "Escape") q = "";
  }

  let copied = $state(false);
  async function copyPath() {
    try {
      await navigator.clipboard.writeText(path);
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch { /* no clipboard: nothing to say */ }
  }

  // showSection puts a heading picked in "On this page" (or reached by a text hit) in the URL in place of the shown
  // address: no history entry, so Back leaves the doc rather than walking its headings.
  function showSection(id: string) {
    if (!path || !id) return;
    scrolled = path + "#" + id;
    replace(href.docs(project, path, id));
  }

  // A prose click on a heading's anchor copies its link whole (origin, page and section); the anchor stays a link, so
  // its address is there to copy as well.
  function copyLink(node: HTMLElement) {
    const onclick = async (e: MouseEvent) => {
      const a = (e.target as HTMLElement | null)?.closest<HTMLAnchorElement>("a.hlink");
      if (!a) return;
      e.preventDefault();
      try {
        await navigator.clipboard.writeText(location.origin + location.pathname + a.hash);
        a.dataset.copied = "";
        setTimeout(() => delete a.dataset.copied, 1500);
      } catch { /* no clipboard: nothing to say */ }
    };
    node.addEventListener("click", onclick);
    return { destroy: () => node.removeEventListener("click", onclick) };
  }
</script>

<svelte:window onkeydown={onkey} {onscroll} onresize={onscroll} />

{#snippet age(d: Doc)}
  <span class="when"><DocAge updated={d.updated} /></span>
{/snippet}

{#snippet kdot(p: string, gap: boolean)}
  {@const k = docKind(p)}
  {#if k}<span class="kdot"><Dot tone={KIND_TONE[k]} size={6} title={k} /></span>{:else if gap}<span class="kdot"></span>{/if}
{/snippet}

<div class="page docs-page" class:reading={!!path} style="--top: {topH}px">
  {#if !path}
    <header class="page-head">
      <h1>Docs</h1>
      <Pill state="idle" text="read-only" dot={false} />
      <span class="muted small">{total} markdown files git tracks{list.data ? " · at " + list.data.head.slice(0, 7) : ""}</span>
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
            aria-label={mode === "text" ? "Search the docs' text" : "Filter the docs by name or path"}
            placeholder={mode === "text" ? `Search the text of ${total} docs` : `Filter ${total} docs by name or path`} />
          <kbd aria-hidden="true">/</kbd>
        </div>
        <DocModes bind:mode onchange={() => { sel = 0; listInput?.focus(); }} />
        <Segmented label="Order in a folder" bind:value={order}
          options={[["", "Default"], ["new", "Newest"], ["az", "A–Z"]]} />
      </div>

      {#if q.trim()}
        <Section title="Matches" sub="↑ ↓ to move · ↵ opens · Esc clears"
          count={listCount}>
          <div class="card box">
            <DocIndex bind:this={listIndex} part="matches" {project} list={list.data} bind:q bind:sel bind:mode {search}
              onhit={(p, line) => (jump = { path: p, line })} />
          </div>
        </Section>
      {:else}
        {#if list.data.pinned.length}
          <Section title="Pinned">
            <div class="pins">
              {#each list.data.pinned as d (d.path)}
                {@const k = docKind(d.path)}
                <a class="card pin" class:kinded={!!k} style={k ? `--k: var(--${KIND_TONE[k]})` : undefined}
                  data-kind={k || undefined} href={href.docs(project, d.path)} title={d.path}>
                  <span class="pin-name" class:mono={!d.title}>{@html labelHtml(d)}</span>
                  {#if pinnedCaption(d.path)}<span class="cap">{pinnedCaption(d.path)}</span>{/if}
                  <span class="pin-foot">
                    {#if k}<Dot tone={KIND_TONE[k]} size={7} title={k} /><span class="kind">{k}</span><span aria-hidden="true">·</span>{/if}
                    <span class="mono ell">{folderOf(d.path) || "top level"}</span>{@render age(d)}</span>
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
                    <span class="rline">{@render kdot(d.path, true)}<span class="ell" class:mono={!d.title}>{@html labelHtml(d)}</span>{@render age(d)}</span>
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
      {@render kdot(path, false)}
      <span class="bar-path mono" title={path}><bdi>{path}</bdi></span>
      {#if heads.length >= 2}
        <button type="button" class="btn bar-btn" onclick={() => sheet?.showModal()}>Sections
          <svg width="10" height="10" viewBox="0 0 12 12" aria-hidden="true"><path d="M2.5 4.5L6 8l3.5-3.5" fill="none" stroke="currentColor" stroke-width="1.5"></path></svg></button>
      {/if}
      <span class="bar-track" aria-hidden="true"></span>
      <span class="bar-line" style:width="{through * 100}%" aria-hidden="true"></span>
    </div>

    <div class="read" class:outlined={heads.length >= 2}>
      <aside class="side">
        <DocIndex bind:this={sideIndex} {project} list={list.data} {path} bind:q bind:sel {order} bind:folds
          bind:mode {search} onhit={(p, line) => (jump = { path: p, line })} />
      </aside>

      <article class="doc" bind:this={article}>
        <header class="doc-head">
          <div class="path-line mono">
            {#if folderOf(path)}<a href={href.docs(project)}>{folderOf(path)}</a>{/if}<span class="file">{fileOf(path)}</span>
            <button type="button" class="copy" onclick={copyPath} aria-label="Copy the path">{copied ? "Copied" : "Copy path"}</button>
          </div>
          {#if text || !all.has(path) || doc?.error}
            {#if rendered.head}
              <div class="prose doc-title" use:copyLink><h1 id={titleId}>{@html rendered.head}</h1></div>
            {:else}
              <h1>{#if meta?.title}{@html inline(meta.title, null)}{:else}{path}{/if}</h1>
            {/if}
          {:else}
            <div class="skel title" aria-hidden="true"></div>
          {/if}
          <div class="meta">
            {#if kind}<span class="kind-meta"><Dot tone={KIND_TONE[kind]} title={kind} />{kind}</span>{/if}
            {#if meta}<span class="changed">{#if meta.updated}changed {/if}<DocAge updated={meta.updated} mode="ago" /></span>{/if}
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
          <div class="card pad"><div class="prose" use:copyLink>{@html html}</div></div>
        {/if}

        {#if all.has(path)}
          <nav class="near" aria-label="Newer and older in {folderOf(path) || 'top level'}">
            {#if near.newer}
              <a class="card nb" href={href.docs(project, near.newer.path)} title={near.newer.path}>
                <span class="nb-lbl">← {byName ? "previous" : "newer"} in {folderOf(path) || "top level"}</span>
                <span class="nb-line">{@render kdot(near.newer.path, false)}<span class="nb-name" class:mono={!near.newer.title}>{@html labelHtml(near.newer)}</span></span></a>
            {:else}
              <div class="nb none"><span class="nb-lbl">← {byName ? "previous" : "newer"} in {folderOf(path) || "top level"}</span><span class="nb-name">none</span></div>
            {/if}
            {#if near.older}
              <a class="card nb right" href={href.docs(project, near.older.path)} title={near.older.path}>
                <span class="nb-lbl">{byName ? "next" : "older"} in {folderOf(path) || "top level"} →</span>
                <span class="nb-line">{@render kdot(near.older.path, false)}<span class="nb-name" class:mono={!near.older.title}>{@html labelHtml(near.older)}</span></span></a>
            {:else}
              <div class="nb none right"><span class="nb-lbl">{byName ? "next" : "older"} in {folderOf(path) || "top level"} →</span><span class="nb-name">none</span></div>
            {/if}
          </nav>
        {/if}
      </article>

      {#if heads.length >= 2}
        <aside class="toc" aria-label="On this page">
          <h2 class="lbl">On this page · {heads.length}</h2>
          <DocOutline headings={heads} onsection={showSection} />
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
          bind:mode {search} onhit={(p, line) => (jump = { path: p, line })} onpick={() => drawer?.close()} />
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
        <DocOutline headings={heads} compact onsection={showSection} onpick={() => sheet?.close()} />
      </div>
    </dialog>
  {/if}
</div>

<style>
  .docs-page { --scroll-clear: calc(var(--top) + 12px); }
  .docs-page.reading { max-width: 1440px; padding-top: 18px; }
  .pad { padding: 18px 24px; }
  .box { padding: 8px 6px; min-width: 0; }
  .when { margin-left: auto; flex: none; display: inline-flex; font-size: 11px; }
  .kdot { flex: none; width: 6px; display: inline-flex; align-items: center; }
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
  .pin.kinded {
    background: color-mix(in srgb, var(--k) 7%, var(--surface)); border-color: color-mix(in srgb, var(--k) 35%, var(--line));
  }
  .pin.kinded:hover { border-color: color-mix(in srgb, var(--k) 55%, var(--line)); }
  .kind { color: var(--fg-2); }
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
  .doc-head h1 {
    margin: 0; font-size: 24px; font-weight: 600; line-height: 1.3; letter-spacing: -0.015em; overflow-wrap: anywhere;
  }
  .meta { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 14px; font-size: 12px; color: var(--muted); }
  .kind-meta { display: inline-flex; align-items: center; gap: 6px; color: var(--fg-2); }
  .changed { display: inline-flex; align-items: center; gap: 4px; }
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
  a.nb:hover { text-decoration: none; border-color: color-mix(in srgb, var(--accent) 40%, var(--line)); }
  .nb-line { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .nb.right .nb-line { justify-content: flex-end; }
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
  .bar-track { position: absolute; left: 0; right: 0; bottom: -1px; height: 2px; background: var(--line-soft); }
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
