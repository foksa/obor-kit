<script lang="ts">
  // Section is a titled part of a page: its title, a count (or meta, a count with its own markup), a line under it,
  // actions on its right.
  import type { Snippet } from "svelte";
  let { title, count = undefined, meta = undefined, sub = undefined, id = undefined, actions = undefined, children }:
    { title: string; count?: number | string; meta?: Snippet; sub?: string; id?: string; actions?: Snippet;
      children: Snippet } = $props();
</script>

<section {id} aria-label={title}>
  <header>
    <h2 class="lbl">{title}</h2>
    {#if meta}{@render meta()}{:else if count !== undefined && count !== ""}<span class="count">{count}</span>{/if}
    {#if sub}<span class="sub">{sub}</span>{/if}
    {#if actions}<div class="actions">{@render actions()}</div>{/if}
  </header>
  {@render children()}
</section>

<style>
  section { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
  header { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 10px; min-height: 26px; }
  .count { font-size: 12px; color: var(--muted); font-variant-numeric: tabular-nums; }
  .sub { font-size: 12px; color: var(--muted); }
  .actions { margin-left: auto; display: flex; gap: 6px; align-items: center; }
</style>
