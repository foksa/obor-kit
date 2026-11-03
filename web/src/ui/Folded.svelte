<script lang="ts">
  // Folded shows the first part of a long text, with a button for the rest; a short one shows whole.
  import type { Snippet } from "svelte";
  let { long, key = "", children }: { long: boolean; key?: string; children: Snippet } = $props();
  let open = $state(false);
</script>

<div class:fold={long && !open} data-key={key}>{@render children()}</div>
{#if long}<button class="btn sm more" onclick={() => (open = !open)}>{open ? "Show less" : "Show all"}</button>{/if}

<style>
  .fold { max-height: 420px; overflow: hidden; mask-image: linear-gradient(to bottom, #000 75%, transparent); }
  .more { margin-top: 12px; }
</style>
