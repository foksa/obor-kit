<script lang="ts">
  // The fleet: every obor project on this machine — the counts across them, a card per project with its lanes, and
  // their recent events together.
  import { onDestroy } from "svelte";
  import { Live } from "../lib/live.svelte";
  import { api } from "../lib/api";
  import { href } from "../lib/route";
  import { human, tone, unixOf } from "../lib/format";
  import type { ProjectsEntry } from "../lib/types";
  import Pill from "../ui/Pill.svelte";
  import Stat from "../ui/Stat.svelte";
  import Section from "../ui/Section.svelte";
  import EventList from "../ui/EventList.svelte";
  import Segmented from "../ui/Segmented.svelte";
  import Rel from "../ui/Rel.svelte";

  const live = new Live<ProjectsEntry[]>((s) => api.projects(s));
  live.start();
  onDestroy(() => live.stop());

  const ps = $derived(live.data || []);
  const sum = (f: (p: ProjectsEntry) => number) => ps.reduce((n, p) => n + f(p), 0);
  const lanes = $derived(sum((p) => p.busy + p.free));
  const busy = $derived(sum((p) => p.busy));
  const needs = $derived(sum((p) => p.needs_you));
  const queued = $derived(sum((p) => p.queue));
  const roles = $derived(sum((p) => (p.roles || []).length));
  const events = $derived(ps.flatMap((p) => (p.events || []).map((e) => ({ ...e, project: p.name })))
    .sort((a, b) => unixOf(b.ts) - unixOf(a.ts)).slice(0, 30));
  let filter = $state("All");
</script>

<div class="page">
  <div class="page-head"><h1>Fleet</h1><span class="muted small">every obor project on this machine</span></div>
  {#if !live.data}
    <p class={live.error ? "failed" : "loading"}>{live.error || "loading…"}</p>
  {:else if !ps.length}
    <p class="muted">No obor project known on this machine yet: <code>obor init</code> or <code>obor serve</code> registers one.</p>
  {:else}
    <div class="stats">
      <Stat label="Needs you">
        {needs}{#if !needs}<span class="hint ok">all clear</span>{/if}
      </Stat>
      <Stat label="Lanes busy">
        {busy}<span class="of">/ {lanes}</span>
        {#snippet foot()}<div class="meter"><span style="width: {lanes ? (100 * busy) / lanes : 0}%"></span></div>{/snippet}
      </Stat>
      <Stat label="Queued">{queued}</Stat>
      <Stat label="Role agents at work">{roles}</Stat>
    </div>

    <Section title="Projects" count={ps.length}>
      <div class="grid">
        {#each ps as p (p.name)}
          <a class="card proj" href={href.project(p.name)}>
            <div class="row">
              <div class="name">
                <strong>{p.name}</strong>
                <span class="mono path ell" title={p.root}>{p.root}</span>
              </div>
              {#if p.error}<Pill state="fail" text="error" />
              {:else if p.paused}<Pill state="paused" text="paused" />
              {:else}<Pill state={p.up ? "up" : "down"} text={p.up ? "serve up" : "serve down"} />{/if}
            </div>
            {#if p.error}
              <p class="err small">{p.error}</p>
            {:else}
              <div class="lanes">
                <span class="lbl">Lanes</span>
                <span class="muted small">{p.busy} busy · {p.free} free</span>
              </div>
              <div class="strip">
                {#each p.lanes || [] as l (l.lane)}
                  <span class="lane t-{tone(l.column)}" title="{l.lane}{l.task ? ' · ' + l.task : ''} · {l.column}">{l.lane}</span>
                {/each}
              </div>
              <div class="nums">
                <div><span class="lbl">Needs you</span><b class:bad={p.needs_you}>{p.needs_you}</b></div>
                <div><span class="lbl">Queue</span><b>{p.queue}</b></div>
                <div><span class="lbl">Last full check</span>
                  {#if p.full_check}<b class:okc={p.full_check.verdict === "GREEN"} class:bad={p.full_check.verdict !== "GREEN"}>{p.full_check.verdict}</b>
                  {:else}<b class="muted">—</b>{/if}</div>
              </div>
              {#if p.events?.length}
                {@const e = p.events[0]}
                <div class="last small">
                  <span class="ell">{human(e.line)}</span>
                  <span class="muted"><Rel at={unixOf(e.ts)} /></span>
                </div>
              {/if}
            {/if}
          </a>
        {/each}
      </div>
    </Section>

    <Section title="Activity across the fleet">
      {#snippet actions()}<Segmented label="Show" options={["All", "Flow", "Messages", "Problems"]} bind:value={filter} />{/snippet}
      <EventList {events} showProject bind:filter />
    </Section>
  {/if}
</div>

<style>
  .hint { font-size: 13px; font-weight: 500; }
  .ok { color: var(--ok); }
  .of { font-size: 18px; color: var(--muted); font-weight: 500; }
  .meter { height: 6px; border-radius: 3px; background: var(--line); overflow: hidden; }
  .meter span { display: block; height: 100%; background: var(--ok); }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(330px, 1fr)); gap: 14px; }
  .proj { display: flex; flex-direction: column; gap: 14px; padding: 18px; color: var(--fg); }
  .proj:hover { text-decoration: none; border-color: var(--line-strong); }
  .row { display: flex; align-items: flex-start; gap: 10px; }
  .name { display: flex; flex-direction: column; gap: 3px; min-width: 0; flex: 1; }
  .name strong { font-size: 16px; }
  .path { font-size: 11px; color: var(--muted); }
  .err { margin: 0; color: var(--bad); }
  .lanes { display: flex; align-items: center; gap: 8px; }
  .strip { display: flex; gap: 4px; }
  .lane {
    flex: 1; min-width: 0; height: 26px; border-radius: 5px; display: flex; align-items: center; justify-content: center;
    font: 500 10px var(--mono); color: var(--c); border: 1px solid color-mix(in srgb, var(--c) 45%, transparent);
    background: color-mix(in srgb, var(--c) var(--tint), transparent); overflow: hidden;
  }
  .lane.t-idle { color: var(--muted); border-color: var(--line); background: var(--surface-2); }
  .t-ok { --c: var(--ok); } .t-wait { --c: var(--wait); } .t-bad { --c: var(--bad); } .t-info { --c: var(--info); }
  .t-done { --c: var(--done); } .t-accent { --c: var(--accent); } .t-idle { --c: var(--idle); }
  .nums { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; padding-top: 12px; border-top: 1px solid var(--line-soft); }
  .nums div { display: flex; flex-direction: column; gap: 2px; }
  .nums .lbl { font-size: 10px; }
  .bad { color: var(--bad); }
  .okc { color: var(--ok); }
  .last { display: flex; gap: 10px; color: var(--fg-2); }
  .last .muted { margin-left: auto; flex: none; }
</style>
