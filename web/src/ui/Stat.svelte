<script lang="ts">
  // Stat is one number of a page's status row: a label, the value, a line under it.
  import type { Snippet } from "svelte";
  let { label, children, foot = undefined }: { label: string; children: Snippet; foot?: Snippet } = $props();
</script>

<div class="card stat">
  <span class="lbl">{label}</span>
  <div class="value">{@render children()}</div>
  {#if foot}<div class="foot">{@render foot()}</div>{/if}
</div>

<style>
  .stat { padding: 14px 16px; display: flex; flex-direction: column; gap: 6px; min-width: 0; }
  .value { font-size: 26px; font-weight: 600; letter-spacing: -0.02em; line-height: 1.15; display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
  .foot { font-size: 12px; color: var(--muted); }
</style>
