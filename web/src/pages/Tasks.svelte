<script lang="ts">
  // A project's tasks: every task the store knows, newest activity first, with a search over id and title and the
  // state, difficulty and model filters (each value with its count). The page draws PAGE rows at a time: the list is
  // read again every few seconds, and a few hundred rows less keeps each refresh's redraw small.
  import { onDestroy } from "svelte";
  import { Live } from "../lib/live.svelte";
  import { api } from "../lib/api";
  import { ALL, PAGE, counts, emptyFilter, filterRows, stateTone, type Facet, type Filter } from "../lib/tasks";
  import type { TaskRow } from "../lib/types";
  import Section from "../ui/Section.svelte";
  import Pill from "../ui/Pill.svelte";
  import TaskLink from "../ui/TaskLink.svelte";
  import Rel from "../ui/Rel.svelte";
  import Segmented from "../ui/Segmented.svelte";
  import Stale from "../ui/Stale.svelte";

  let { project }: { project: string } = $props();
  // svelte-ignore state_referenced_locally (the page is keyed by its route: a new project is a new page)
  const live = new Live<TaskRow[]>((s) => api.tasks(project, s), project);
  live.start();
  onDestroy(() => live.stop());

  let f = $state<Filter>(emptyFilter());
  let limit = $state(PAGE);
  const rows = $derived(live.data || []);
  const shown = $derived(filterRows(rows, f));
  const c = $derived(counts(rows, f));
  const opts = (k: Facet) => c[k].map(([v, n]): [string, string] => [v, `${v} ${n}`]);
  const facets: [Facet, string][] = [["state", "State"], ["difficulty", "Difficulty"], ["model", "Model"]];
  const reviewFile = (id: string) => id + ".md"; // the review's own file in the reviews dir
  function reset() { limit = PAGE; }
</script>

<div class="page">
  <header class="page-head">
    <h1>Tasks</h1>
    <span class="muted small">{rows.length} known to the store</span>
    <Stale error={live.data ? live.error : ""} />
  </header>

  {#if !live.data}
    <p class={live.error ? "failed" : "loading"}>{live.error ? "Could not load the tasks: " + live.error : "loading…"}</p>
  {:else}
    <div class="card pad filters">
      <input class="search" type="search" bind:value={f.q} oninput={reset} aria-label="Search the tasks' ids and titles"
        placeholder="Search id or title" />
      {#each facets as [k, label] (k)}
        <div class="facet">
          <span class="lbl">{label}</span>
          <Segmented {label} options={opts(k)} bind:value={f[k]} onchange={reset} />
        </div>
      {/each}
      {#if f.q || f.state !== ALL || f.difficulty !== ALL || f.model !== ALL}
        <button class="btn ghost sm clear" onclick={() => { f = emptyFilter(); reset(); }}>Clear</button>
      {/if}
    </div>

    <Section title="Tasks" count={shown.length === rows.length ? rows.length : `${shown.length} of ${rows.length}`}
      sub="newest activity first">
      <div class="card list">
        <table>
          <thead>
            <tr><th>Task</th><th>Title</th><th>State</th><th class="hide-md">Difficulty</th><th class="hide-md">Model</th>
              <th class="hide-sm">Lane</th><th class="hide-sm num">Att.</th><th class="hide-md">Merged</th>
              <th class="hide-md">Review</th><th class="hide-md">Fixes</th><th class="hide-sm">Active</th></tr>
          </thead>
          <tbody>
            {#each shown.slice(0, limit) as t (t.task)}
              <tr>
                <td><TaskLink {project} task={t.task} /></td>
                <td class="title">{t.title || "—"}{#if t.rev && t.rev > 1}<span class="muted small"> r{t.rev}</span>{/if}</td>
                <td><Pill tone={stateTone(t.state)} text={t.state} /></td>
                <td class="hide-md muted">{t.difficulty || "—"}</td>
                <td class="hide-md mono small">
                  {#if t.ran}<span title="ran its last attempt">{t.ran}</span>{/if}
                  {#if t.model && t.model !== t.ran}<span class="muted" title="the spec's Model: line">{t.ran ? " · " : ""}{t.model}</span>{/if}
                  {#if !t.ran && !t.model}<span class="muted">—</span>{/if}
                </td>
                <td class="hide-sm mono small">{t.lane || "—"}</td>
                <td class="hide-sm num">{t.attempts || "—"}</td>
                <td class="hide-md small nowrap">
                  {#if t.merged_at}<span class="mono">{t.merge_sha ? t.merge_sha.slice(0, 7) : "outside"}</span> <span class="muted"><Rel at={t.merged_at} /></span>{:else}<span class="muted">—</span>{/if}
                </td>
                <td class="hide-md mono small">{#if t.review}<span title={reviewFile(t.review)}>{t.review}</span>{:else}<span class="muted">—</span>{/if}</td>
                <td class="hide-md small">
                  {#if t.fixes}
                    <span class="mono" title={(reviewFile(t.fixes.review) + " " + (t.fixes.note || "")).trim()}>{t.fixes.review}</span>
                    {#each t.fixes.tasks || [] as x (x)} <TaskLink {project} task={x} />{/each}
                  {:else}<span class="muted">—</span>{/if}
                </td>
                <td class="hide-sm muted small nowrap"><Rel at={t.last} /></td>
              </tr>
            {:else}
              <tr><td colspan="11" class="empty">No task matches.</td></tr>
            {/each}
          </tbody>
        </table>
        {#if shown.length > limit}
          <div class="more">
            <span class="muted small">{limit} of {shown.length} shown</span>
            <button class="btn sm" onclick={() => (limit += PAGE)}>Show {Math.min(PAGE, shown.length - limit)} more</button>
          </div>
        {/if}
      </div>
    </Section>
  {/if}
</div>

<style>
  .pad { padding: 14px 16px; }
  .filters { display: flex; flex-wrap: wrap; align-items: center; gap: 12px 18px; }
  .search {
    flex: 1 1 240px; min-width: 0; height: 32px; padding: 0 10px; border-radius: 8px; border: 1px solid var(--line);
    background: var(--sunk); color: var(--fg); font-size: 13px; outline: 0;
  }
  .search:focus { border-color: var(--faint); }
  .facet { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 8px; min-width: 0; }
  .clear { margin-left: auto; }
  .list { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th { text-align: left; color: var(--muted); font-weight: 600; font-size: 12px; padding: 9px 12px; white-space: nowrap; }
  td { padding: 8px 12px; border-top: 1px solid var(--line-soft); vertical-align: middle; }
  tbody tr:hover { background: var(--surface-2); }
  .title { min-width: 220px; overflow-wrap: anywhere; }
  .num { text-align: right; font-variant-numeric: tabular-nums; }
  .nowrap { white-space: nowrap; }
  .empty { padding: 16px; color: var(--muted); }
  .more { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 12px; border-top: 1px solid var(--line-soft); }
  @media (max-width: 1100px) { .hide-md { display: none; } }
  @media (max-width: 700px) { .hide-sm { display: none; } .title { min-width: 0; } }
</style>
