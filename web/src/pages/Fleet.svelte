<script lang="ts">
  // The fleet, obor web's home: what runs now across every project (the busy lanes with their tasks, the role agents at
  // work, the steps running), the counts across them, a card per project, and their events together.
  import { onDestroy } from "svelte";
  import { Live } from "../lib/live.svelte";
  import Stale from "../ui/Stale.svelte";
  import { api } from "../lib/api";
  import { href } from "../lib/route";
  import { dur, human, now, roleName, tone, unixOf } from "../lib/format";
  import { stageOf } from "../lib/stages";
  import type { LaneDot, ProjectsEntry } from "../lib/types";
  import Pill from "../ui/Pill.svelte";
  import Stat from "../ui/Stat.svelte";
  import Section from "../ui/Section.svelte";
  import EventList from "../ui/EventList.svelte";
  import Segmented from "../ui/Segmented.svelte";
  import Rel from "../ui/Rel.svelte";
  import Stepper from "../ui/Stepper.svelte";
  import TaskLink from "../ui/TaskLink.svelte";
  import AgentLink from "../ui/AgentLink.svelte";

  const live = new Live<ProjectsEntry[]>((s) => api.projects(s));
  live.start();
  onDestroy(() => live.stop());

  const ps = $derived(live.data || []);
  const sum = (f: (p: ProjectsEntry) => number) => ps.reduce((n, p) => n + f(p), 0);
  const lanes = $derived(sum((p) => p.busy + p.free));
  const busy = $derived(sum((p) => p.busy));
  const needs = $derived(sum((p) => p.needs_you));
  const queued = $derived(sum((p) => p.queue));
  const busyLanes = (p: ProjectsEntry) => (p.lanes || []).filter((l) => !l.free);
  // running is every busy lane across the fleet, longest running first
  const running = $derived(ps.flatMap((p) => busyLanes(p).map((l) => ({ p: p.name, l })))
    .sort((a, b) => (a.l.since || Infinity) - (b.l.since || Infinity)));
  const roles = $derived(ps.flatMap((p) => (p.roles || []).map((r) => ({ p: p.name, r }))));
  const steps = $derived(ps.flatMap((p) => (p.running || []).map((x) => ({ p: p.name, x }))));
  const events = $derived(ps.flatMap((p) => (p.events || []).map((e) => ({ ...e, project: p.name })))
    .sort((a, b) => unixOf(b.ts) - unixOf(a.ts)).slice(0, 30));
  let filter = $state("All");
  const laneStage = (l: LaneDot) => stageOf(l); // the dot carries its task's step (T-883)
</script>

<div class="page">
  <div class="page-head"><h1>Fleet</h1><span class="muted small">every obor project on this machine</span><Stale error={live.data ? live.error : ""} /></div>
  {#if !live.data}
    <p class={live.error ? "failed" : "loading"}>{live.error || "loading…"}</p>
  {:else if !ps.length}
    <p class="muted">No obor project known on this machine yet: <code>obor init</code> or <code>obor serve</code> registers one.</p>
  {:else}
    <div class="stats">
      <Stat label="Running now">
        {busy}<span class="of">/ {lanes} lanes</span>
        {#snippet foot()}<div class="meter"><span style="width: {lanes ? (100 * busy) / lanes : 0}%"></span></div>{/snippet}
      </Stat>
      <Stat label="Role agents at work">{roles.length}</Stat>
      <Stat label="Needs you">
        <span class:bad={needs}>{needs}</span>{#if !needs}<span class="hint ok">all clear</span>{/if}
      </Stat>
      <Stat label="Queued">{queued}</Stat>
    </div>

    <Section title="Running now" count={running.length + roles.length + steps.length}>
      <div class="card run">
        {#each running as { p, l } (p + l.lane)}
          {@const st = laneStage(l)}
          <div class="rrow">
            <a class="proj" href={href.project(p)}>{p}</a>
            <span class="mono lane">{l.lane}</span>
            <div class="what">
              <div class="t">{#if l.task}<TaskLink project={p} task={l.task} chip /><a class="title ell" href={href.task(p, l.task)}>{l.title || "no title"}</a>{/if}</div>
              <div class="sub small">
                <Pill state={l.column} />
                {#if l.agent}<AgentLink project={p} agent={l.agent} />{/if}
                {#if l.type}<span class="muted mono">{l.type}</span>{/if}
              </div>
            </div>
            <div class="steps" title="{st.state}: step {Math.min(st.i + 1, 7)} of 7"><Stepper stage={st} compact labels={false} /></div>
            <span class="mono el"><Rel at={l.since} mode="for" /></span>
          </div>
        {/each}
        {#each roles as { p, r } (p + r.agent)}
          <div class="rrow">
            <a class="proj" href={href.project(p)}>{p}</a>
            <span class="lane muted small">{roleName(r.role)}</span>
            <div class="what">
              <div class="t"><AgentLink project={p} agent={r.agent} /></div>
              <div class="sub small">
                {#if r.status}<Pill state={r.status} />{/if}
                {#if r.brief}<span class="muted">brief {r.brief.worked ? "being worked" : r.brief.delivered ? "delivered" : "not delivered"}</span>{/if}
              </div>
            </div>
            <div class="steps"></div>
            <span class="mono el">{r.age ? dur(r.age) : ""}</span>
          </div>
        {/each}
        {#each steps as { p, x }, i (p + i)}
          <div class="rrow">
            <a class="proj" href={href.project(p)}>{p}</a>
            <span class="lane muted small">{x.step}</span>
            <div class="what">
              <div class="t">{#if x.task}<TaskLink project={p} task={x.task} chip />{/if}<span class="mono small muted ell">{x.command || ""}</span></div>
            </div>
            <div class="steps"></div>
            <span class="mono el">{dur(now() - x.since)}</span>
          </div>
        {/each}
        {#if !running.length && !roles.length && !steps.length}
          <p class="empty">Nothing runs now: every lane is free.</p>
        {/if}
      </div>
    </Section>

    <Section title="Projects" count={ps.length}>
      <div class="grid">
        {#each ps as p (p.name)}
          {@const bl = busyLanes(p)}
          <article class="card proj-card" class:active={bl.length > 0}>
            <div class="row">
              <div class="name">
                <a class="pname" href={href.project(p.name)}>{p.name}</a>
                <span class="mono path ell" title={p.root}>{p.root}</span>
              </div>
              {#if p.needs_you}<Pill state="needs-you" text="{p.needs_you} needs you" />{/if}
              {#if p.error}<Pill state="fail" text="error" />
              {:else if p.paused}<Pill state="paused" text="paused" />
              {:else}<Pill state={p.up ? "up" : "down"} text={p.up ? "serve up" : "serve down"} />{/if}
            </div>
            {#if p.error}
              <p class="err small">{p.error}</p>
            {:else}
              <div class="strip" aria-label="Lanes">
                {#each p.lanes || [] as l (l.lane)}
                  <span class="lane-dot t-{tone(l.column)}" title="{l.lane}{l.task ? ' · ' + l.task : ''} · {l.column}">{l.lane}</span>
                {/each}
              </div>
              {#if bl.length}
                <div class="busy">
                  {#each bl as l (l.lane)}
                    <div class="brow">
                      <span class="mono small lane">{l.lane}</span>
                      {#if l.task}<TaskLink project={p.name} task={l.task} />{/if}
                      <span class="ell small">{l.title || ""}</span>
                      <Pill state={l.column} />
                      <span class="mono small muted"><Rel at={l.since} mode="for" /></span>
                    </div>
                  {/each}
                </div>
              {:else}
                <p class="idle small">All {p.free} lanes free</p>
              {/if}
              <div class="foot small">
                <span>{p.busy} busy · {p.free} free</span>
                <span>queue {p.queue}</span>
                {#if p.full_check}<span>full check <b class:okc={p.full_check.verdict === "GREEN"} class:bad={p.full_check.verdict !== "GREEN"}>{p.full_check.verdict}</b></span>{/if}
              </div>
              {#if p.events?.length}
                {@const e = p.events[0]}
                <div class="last small"><span class="ell">{human(e.line)}</span><span class="muted"><Rel at={unixOf(e.ts)} /></span></div>
              {/if}
            {/if}
          </article>
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
  .bad { color: var(--bad); }
  .okc { color: var(--ok); }
  .of { font-size: 16px; color: var(--muted); font-weight: 500; }
  .meter { height: 6px; border-radius: 3px; background: var(--line); overflow: hidden; }
  .meter span { display: block; height: 100%; background: var(--ok); }

  .run { overflow: hidden; }
  .rrow {
    display: grid; grid-template-columns: 110px 64px minmax(0, 1fr) minmax(180px, 280px) 70px; gap: 14px; align-items: center;
    padding: 12px 16px; border-bottom: 1px solid var(--line-soft);
  }
  .rrow:last-of-type { border-bottom: 0; }
  .proj { font: 500 12px var(--mono); color: var(--fg-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .lane { font-weight: 600; }
  .what { display: flex; flex-direction: column; gap: 5px; min-width: 0; }
  .t { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .title { color: var(--fg); font-weight: 500; }
  .sub { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
  .el { text-align: right; color: var(--fg-2); font-size: 13px; }
  .empty { margin: 0; padding: 16px; color: var(--muted); }
  @media (max-width: 900px) {
    .rrow { grid-template-columns: 90px 54px minmax(0, 1fr) 56px; }
    .steps { display: none; }
  }

  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr)); gap: 14px; }
  .proj-card { display: flex; flex-direction: column; gap: 12px; padding: 16px 18px; }
  .proj-card.active { border-color: color-mix(in srgb, var(--ok) 35%, var(--line)); }
  .row { display: flex; align-items: flex-start; gap: 8px; }
  .name { display: flex; flex-direction: column; gap: 3px; min-width: 0; flex: 1; }
  .pname { font-size: 16px; font-weight: 600; color: var(--fg); }
  .path { font-size: 11px; color: var(--muted); }
  .err { margin: 0; color: var(--bad); }
  .strip { display: flex; gap: 4px; }
  .lane-dot {
    flex: 1; min-width: 0; height: 24px; border-radius: 5px; display: flex; align-items: center; justify-content: center;
    font: 500 10px var(--mono); color: var(--c); border: 1px solid color-mix(in srgb, var(--c) 45%, transparent);
    background: color-mix(in srgb, var(--c) var(--tint), transparent); overflow: hidden;
  }
  .lane-dot.t-idle { color: var(--muted); border-color: var(--line); background: var(--surface-2); }
  .t-ok { --c: var(--ok); } .t-wait { --c: var(--wait); } .t-bad { --c: var(--bad); } .t-info { --c: var(--info); }
  .t-done { --c: var(--done); } .t-accent { --c: var(--accent); } .t-idle { --c: var(--idle); }
  .busy { display: flex; flex-direction: column; gap: 6px; }
  .brow { display: grid; grid-template-columns: 44px auto minmax(0, 1fr) auto 52px; gap: 8px; align-items: center; }
  .brow .muted { text-align: right; }
  .idle { margin: 0; color: var(--muted); }
  .foot { display: flex; flex-wrap: wrap; gap: 4px 14px; color: var(--muted); padding-top: 10px; border-top: 1px solid var(--line-soft); }
  .foot b { font-weight: 600; }
  .last { display: flex; gap: 10px; color: var(--fg-2); }
  .last .muted { margin-left: auto; flex: none; }
</style>
