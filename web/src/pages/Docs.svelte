<script lang="ts">
  // A project's docs: the markdown files git tracks in its main checkout, read-only (editing stays in git). The list
  // has the pinned docs first, then the rest by directory, newest commit first; a doc renders with its task ids, review
  // ids and repo paths linked. BACKLOG gets an outline of its sections, accepted.md a filter box over its lines.
  import { onDestroy } from "svelte";
  import { Live } from "../lib/live.svelte";
  import { api } from "../lib/api";
  import { md } from "../lib/md";
  import { href } from "../lib/route";
  import { docLinks, filterLines, groupName, isAccepted, isBacklog, outline, paths } from "../lib/docs";
  import type { Doc, DocList, DocText } from "../lib/types";
  import Rel from "../ui/Rel.svelte";
  import Stale from "../ui/Stale.svelte";

  let { project, path = "" }: { project: string; path?: string } = $props();
  // svelte-ignore state_referenced_locally (the page is keyed by its route: another doc is a new page)
  const list = new Live<DocList>((s) => api.docs(project, s));
  list.start();
  // svelte-ignore state_referenced_locally
  const doc = path ? new Live<DocText>((s) => api.doc(project, path, s)) : null;
  doc?.start();
  onDestroy(() => { list.stop(); doc?.stop(); });

  const all = $derived(new Set(paths(list.data)));
  const meta = $derived(
    [...(list.data?.pinned || []), ...(list.data?.groups || []).flatMap((g) => g.docs)].find((d) => d.path === path),
  );
  const text = $derived(doc?.data?.text || "");
  const backlog = $derived(isBacklog(path));
  const accepted = $derived(isAccepted(path));
  let q = $state("");
  const filtered = $derived(accepted ? filterLines(text, q) : null);
  const html = $derived(md(filtered ? filtered.text : text, docLinks(project, all, path)));
  const sections = $derived(backlog ? outline(text) : []);
  const name = (d: Doc) => d.path.slice(d.path.lastIndexOf("/") + 1);
  const plain = (s: string) => s.replace(/[`*]/g, "");

  function jump(id: string) {
    document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" });
  }
</script>

<div class="page">
  <header class="page-head">
    <h1>Docs</h1>
    <span class="muted small">{all.size} markdown files git tracks{list.data ? " · at " + list.data.head.slice(0, 7) : ""}, read-only</span>
    <Stale error={list.data ? list.error || doc?.error || "" : ""} />
  </header>

  {#if !list.data}
    <p class={list.error ? "failed" : "loading"}>{list.error ? "Could not load the docs: " + list.error : "loading…"}</p>
  {:else}
    <div class="docs" class:reading={!!path}>
      <nav class="card index" aria-label="Docs">
        {#if list.data.pinned.length}
          <h2 class="lbl">Pinned</h2>
          <ul>
            {#each list.data.pinned as d (d.path)}
              <li><a class="mono" class:on={d.path === path} href={href.docs(project, d.path)} title={d.path}>{d.path}</a></li>
            {/each}
          </ul>
        {/if}
        {#each list.data.groups as g (g.dir)}
          <h2 class="lbl mono">{groupName(g.dir)}</h2>
          <ul>
            {#each g.docs as d (d.path)}
              <li><a class="mono" class:on={d.path === path} href={href.docs(project, d.path)} title={d.path}>{name(d)}</a></li>
            {/each}
          </ul>
        {/each}
        {#if !all.size}<p class="muted small">git tracks no markdown file here.</p>{/if}
      </nav>

      {#if path}
        <article class="doc">
          <div class="doc-head">
            <a class="btn ghost sm back" href={href.docs(project)}>All docs</a>
            <h2 class="mono">{path}</h2>
            {#if meta?.updated}<span class="muted small">changed <Rel at={meta.updated} /></span>{/if}
          </div>
          {#if !all.has(path)}
            <p class="failed">{path} is not a markdown file git tracks here.</p>
          {:else if !doc?.data}
            <p class={doc?.error ? "failed" : "loading"}>{doc?.error ? "Could not load " + path + ": " + doc.error : "loading…"}</p>
          {:else}
            {#if sections.length}
              <div class="outline" aria-label="Sections">
                {#each sections as s (s.id)}<button class="btn ghost sm" onclick={() => jump(s.id)}>{plain(s.text)}</button>{/each}
              </div>
            {/if}
            {#if filtered}
              <div class="filter">
                <input class="search" type="search" bind:value={q} aria-label="Filter the accepted findings"
                  placeholder="Filter: RV-…, a package, a word" />
                <span class="muted small">{filtered.shown === filtered.total ? filtered.total : `${filtered.shown} of ${filtered.total}`} lines</span>
              </div>
            {/if}
            <div class="card pad"><div class="prose">{@html html}</div></div>
          {/if}
        </article>
      {/if}
    </div>
  {/if}
</div>

<style>
  .pad { padding: 14px 18px; }
  .docs { display: grid; gap: 16px; grid-template-columns: minmax(0, 1fr); align-items: start; }
  .docs.reading { grid-template-columns: 260px minmax(0, 1fr); }
  .index { padding: 12px 10px; }
  .reading .index { position: sticky; top: 12px; max-height: calc(100vh - 90px); overflow-y: auto; }
  .index h2 { margin: 12px 6px 4px; font-size: 11px; }
  .index h2:first-child { margin-top: 0; }
  .index ul { list-style: none; margin: 0; padding: 0; }
  .docs:not(.reading) .index ul { columns: 3 260px; column-gap: 16px; }
  .index li { break-inside: avoid; }
  .index a {
    display: block; padding: 3px 6px; border-radius: 6px; font-size: 12px; color: var(--fg-2); overflow: hidden;
    text-overflow: ellipsis; white-space: nowrap;
  }
  .index a:hover { background: var(--surface-2); color: var(--fg); }
  .index a.on { background: var(--surface-2); color: var(--fg); font-weight: 600; }
  .doc { min-width: 0; display: flex; flex-direction: column; gap: 12px; }
  .doc-head { display: flex; flex-wrap: wrap; align-items: baseline; gap: 6px 12px; }
  .doc-head h2 { margin: 0; font-size: 15px; overflow-wrap: anywhere; }
  .back { display: none; }
  .outline { display: flex; flex-wrap: wrap; gap: 6px; }
  .filter { display: flex; align-items: center; gap: 12px; }
  .search {
    flex: 1 1 240px; min-width: 0; height: 32px; padding: 0 10px; border-radius: 8px; border: 1px solid var(--line);
    background: var(--sunk); color: var(--fg); font-size: 13px; outline: 0;
  }
  .search:focus { border-color: var(--faint); }
  @media (max-width: 1000px) {
    .docs.reading { grid-template-columns: minmax(0, 1fr); }
    .reading .index { display: none; }
    .back { display: inline-flex; }
  }
</style>
