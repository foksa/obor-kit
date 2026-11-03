<script lang="ts">
  // Prompt is one prompt serve sent an agent (a job, a brief): its header, and its text, open or folded.
  import { unixOf } from "../lib/format";
  import { href } from "../lib/route";
  import type { Prompt } from "../lib/types";
  import Rel from "./Rel.svelte";
  import AgentLink from "./AgentLink.svelte";
  let { project, p, open = false, showTask = false }: { project: string; p: Prompt; open?: boolean; showTask?: boolean } = $props();
  const lines = $derived(String(p.text || "").split("\n").length);
</script>

<details open={open || lines <= 14}>
  <summary>
    <span class="chev" aria-hidden="true">▸</span>
    {#if p.job}<strong class="mono">{p.job}</strong>{:else}<strong>{p.kind}</strong>{/if}
    <span class="muted small">{p.agent}{showTask && p.task ? " · " + p.task : ""} · <Rel at={unixOf(p.ts)} /> · {lines} lines</span>
  </summary>
  <pre class="text">{p.text}</pre>
  <div class="links small">
    {#if p.job}<a href={href.job(project, p.job)}>open</a>{/if}
    {#if p.agent}<AgentLink {project} agent={p.agent} />{/if}
  </div>
</details>

<style>
  details { border-bottom: 1px solid var(--line-soft); }
  details:last-child { border-bottom: 0; }
  summary { list-style: none; display: flex; flex-wrap: wrap; align-items: baseline; gap: 8px; padding: 12px 16px; cursor: pointer; }
  summary::-webkit-details-marker { display: none; }
  .chev { color: var(--muted); transition: transform 0.15s; display: inline-block; }
  details[open] .chev { transform: rotate(90deg); }
  pre { max-height: 520px; overflow: auto; border-top: 1px solid var(--line-soft); background: var(--sunk); }
  .links { display: flex; gap: 12px; padding: 8px 16px 12px; }
</style>
