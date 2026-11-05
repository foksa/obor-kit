<script lang="ts">
  // HelpView is the Help page's body: README's setup sections, then every obor command by group under a filter box.
  import { md } from "../lib/md";
  import { filterHelp } from "../lib/help";
  import type { Help } from "../lib/types";
  import Section from "./Section.svelte";

  let { help, q = $bindable("") }: { help: Help; q?: string } = $props();
  const groups = $derived(filterHelp(help, q));
  const shown = $derived(groups.reduce((n, g) => n + g.commands.length, 0));
  const noTask = () => "#/"; // README's sections name no task
</script>

{#each help.sections as s (s.title)}
  <Section title={s.title}>
    <div class="card pad"><div class="prose">{@html md(s.body, noTask)}</div></div>
  </Section>
{/each}

<Section title="Commands" count={q.trim() ? `${shown} of ${help.commands.length}` : help.commands.length}>
  {#snippet actions()}
    <input class="filter" type="search" bind:value={q} aria-label="Filter the commands" placeholder="Filter: dispatch, lane, --after…" />
  {/snippet}
  {#each groups as g (g.group)}
    <div class="card group" data-group={g.group}>
      <h3 class="lbl">{g.group}</h3>
      <dl>
        {#each g.commands as c (c.name)}
          <div class="row">
            <dt class="mono">{c.name}</dt>
            <dd><span class="desc">{c.desc}</span><code class="usage">{c.usage}</code></dd>
          </div>
        {/each}
      </dl>
    </div>
  {:else}
    <p class="muted small none">No command matches “{q}”.</p>
  {/each}
</Section>

<style>
  .pad { padding: 14px 18px; }
  .filter {
    height: 30px; padding: 0 10px; border-radius: 8px; background: var(--surface); border: 1px solid var(--line);
    font-size: 13px; width: 260px; max-width: 100%;
  }
  .group { padding: 12px 18px; }
  .group h3 { margin-bottom: 6px; }
  dl { margin: 0; }
  .row { display: grid; grid-template-columns: 110px 1fr; gap: 4px 16px; padding: 8px 0; border-top: 1px solid var(--line-soft); }
  .row:first-child { border-top: 0; }
  dt { color: var(--fg); font-weight: 600; font-size: 13px; }
  dd { margin: 0; display: flex; flex-direction: column; gap: 4px; min-width: 0; color: var(--fg-2); }
  .usage { align-self: flex-start; max-width: 100%; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 12px; }
  .none { margin: 0; }
  @media (max-width: 600px) { .row { grid-template-columns: 1fr; } }
</style>
