<script lang="ts">
  // A job's page: the prompt serve sent, whole.
  import { onDestroy } from "svelte";
  import { Live } from "../lib/live.svelte";
  import { api } from "../lib/api";
  import { unixOf } from "../lib/format";
  import type { Prompt } from "../lib/types";
  import Pill from "../ui/Pill.svelte";
  import Section from "../ui/Section.svelte";
  import TaskLink from "../ui/TaskLink.svelte";
  import AgentLink from "../ui/AgentLink.svelte";
  import Rel from "../ui/Rel.svelte";

  let { project, id }: { project: string; id: string } = $props();
  const live = new Live<Prompt>((s) => api.job(project, id, s));
  live.start();
  onDestroy(() => live.stop());
  const j = $derived(live.data);
</script>

<div class="page">
  {#if !j}
    <p class={live.error ? "failed" : "loading"}>{live.error ? "Could not load this job: " + live.error : "loading…"}</p>
  {:else}
    <div class="page-head">
      <h1 class="mono">{j.job || j.kind}</h1>
      <Pill state="idle" text={j.kind} />
      {#if j.task}<span class="small">task <TaskLink {project} task={j.task} chip /></span>{/if}
      {#if j.agent}<span class="small">agent <AgentLink {project} agent={j.agent} /></span>{/if}
      <span class="small muted">sent <Rel at={unixOf(j.ts)} /></span>
    </div>
    <Section title="Prompt" count="{String(j.text || '').split('\n').length} lines">
      <div class="card"><pre class="text">{j.text}</pre></div>
    </Section>
  {/if}
</div>
