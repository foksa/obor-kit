<script lang="ts">
  // An agent's page: what it is on now, its long-running processes, the jobs and briefs it was sent, the lines posted
  // to it and its recorded sessions.
  import { onDestroy } from "svelte";
  import { Live } from "../lib/live.svelte";
  import { api } from "../lib/api";
  import { href } from "../lib/route";
  import { human, unixOf } from "../lib/format";
  import type { AgentDetail } from "../lib/types";
  import Pill from "../ui/Pill.svelte";
  import Section from "../ui/Section.svelte";
  import TaskLink from "../ui/TaskLink.svelte";
  import Linked from "../ui/Linked.svelte";
  import Rel from "../ui/Rel.svelte";
  import Prompt from "../ui/Prompt.svelte";
  import Background from "../ui/Background.svelte";

  let { project, id }: { project: string; id: string } = $props();
  // svelte-ignore state_referenced_locally (the page is keyed by its route: a new project or id is a new page)
  const live = new Live<AgentDetail>((s) => api.agent(project, id, s), project);
  live.start();
  onDestroy(() => live.stop());
  const a = $derived(live.data);
</script>

<div class="page">
  {#if !a}
    <p class={live.error ? "failed" : "loading"}>{live.error ? "Could not load this agent: " + live.error : "loading…"}</p>
  {:else}
    <div class="page-head">
      <h1 class="mono">{a.agent}</h1>
      {#if a.status}<Pill state={a.status === "done" ? "idle" : a.status} text={a.status} />{:else}<Pill state="idle" text="no pane" />{/if}
      {#if a.task}<span class="small">on <TaskLink {project} task={a.task} chip /></span>{/if}
      {#if a.lane}<span class="small muted">lane <span class="mono">{a.lane}</span></span>{/if}
    </div>

    {#if a.background?.length}
      <Section title="Long-running processes" count={a.background.length}>
        <div class="card pad"><Background list={a.background} /></div>
      </Section>
    {/if}
    {#if a.jobs.length}
      <Section title="Jobs" count={a.jobs.length}>
        <div class="card">{#each a.jobs as j, i (j.seq)}<Prompt {project} p={j} open={i === 0 && j.text.length < 6000} showTask />{/each}</div>
      </Section>
    {/if}
    {#if a.briefs.length}
      <Section title="Briefs" count={a.briefs.length}>
        <div class="card">{#each a.briefs as j, i (j.seq)}<Prompt {project} p={j} open={i === 0} showTask />{/each}</div>
      </Section>
    {/if}
    {#if a.lines.length}
      <Section title="Lines sent" count={a.lines.length}>
        <div class="card">
          {#each a.lines as l (l.seq)}
            <div class="line"><span class="muted small mono"><Rel at={unixOf(l.ts)} /></span>
              <span>{#if l.from}<span class="muted small">{l.from} </span>{/if}<Linked {project} text={l.line} /></span></div>
          {/each}
        </div>
      </Section>
    {/if}
    {#if a.sessions.length}
      <Section title="Sessions" count={a.sessions.length}>
        <div class="card">
          {#each a.sessions as s (s.id)}
            <div class="sess">
              <span class="mono small" title={s.id}>{s.id.slice(0, 8)}</span>
              <div class="sbody">
                <span>{s.kind || ""}{#if s.task} · <a class="mono" href={href.task(project, s.task)}>{s.task}</a>{/if}{s.lane ? " · " + s.lane : ""}{#if s.pane}<span class="muted small"> pane {s.pane}</span>{/if}</span>
                {#if s.transcript}<span class="muted small mono ell" title={s.transcript}>{human(s.transcript)}</span>{/if}
              </div>
              <span class="muted small">started <Rel at={s.started} /></span>
              <span class="muted small r">{#if s.last_write}wrote <Rel at={s.last_write} />{/if}</span>
            </div>
          {/each}
        </div>
      </Section>
    {/if}
    {#if !a.jobs.length && !a.briefs.length && !a.lines.length && !a.sessions.length}
      <p class="muted">Nothing recorded for this agent yet.</p>
    {/if}
  {/if}
</div>

<style>
  .pad { padding: 12px 16px; }
  .line { display: grid; grid-template-columns: 80px minmax(0, 1fr); gap: 12px; padding: 9px 16px; border-bottom: 1px solid var(--line-soft); font-size: 13px; overflow-wrap: anywhere; }
  .line:last-child, .sess:last-child { border-bottom: 0; }
  .sess { display: grid; grid-template-columns: 80px minmax(0, 1fr) auto auto; gap: 14px; align-items: center; padding: 10px 16px; border-bottom: 1px solid var(--line-soft); font-size: 13px; }
  .sbody { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .r { text-align: right; min-width: 90px; }
  @media (max-width: 700px) { .sess { grid-template-columns: 70px minmax(0, 1fr); } }
</style>
