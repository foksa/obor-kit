<script lang="ts">
  // DocAge is a doc's age by its freshness: the staged pill with no commit yet; else the age, fg-2 under a day and
  // muted after, with the live dot while it changed in the last hour. It keeps itself current, as Rel does; its size is
  // the row's.
  import { freshness } from "../lib/docs";
  import { tick } from "../lib/clock.svelte";
  import Dot from "./Dot.svelte";
  import Rel from "./Rel.svelte";
  let { updated = undefined, mode = "for" }: { updated?: number; mode?: "for" | "ago" } = $props();
  const f = $derived(freshness(updated, Math.max(tick.now, Date.now() / 1000)));
</script>

{#if f === "staged"}<span class="staged">staged</span>{:else}<span class="age f-{f}">{#if f === "hour"}<Dot tone="ok" size={6} title="changed in the last hour" />{/if}<Rel at={updated} {mode} /></span>{/if}

<style>
  .age { display: inline-flex; align-items: center; gap: 5px; color: var(--muted); white-space: nowrap; }
  .f-hour, .f-day { color: var(--fg-2); }
  .staged {
    font-size: 11px; font-weight: 600; padding: 1px 7px; border-radius: 999px; white-space: nowrap; color: var(--info);
    background: color-mix(in srgb, var(--info) var(--tint), transparent);
  }
</style>
