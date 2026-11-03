<script lang="ts">
  // Rel is a time relative to now that keeps itself current; its clock time on hover.
  import { clock, relText, type RelMode } from "../lib/format";
  import { tick } from "../lib/clock.svelte";
  let { at, mode = "ago" }: { at: number | null | undefined; mode?: RelMode } = $props();
</script>

{#if at}<time datetime={new Date(at * 1000).toISOString()} title={clock(at)}>{relText(at, mode, Math.max(tick.now, Date.now() / 1000))}</time>{/if}
