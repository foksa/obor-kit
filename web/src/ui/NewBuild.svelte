<script lang="ts">
  // The nav's notice that obor web serves a newer build than this tab runs. The tab never reloads by itself (Setup's
  // staged edits, a pause form, a filter or the jump would go), so the owner reloads when it suits.
  let { newer, onreload }: { newer: boolean; onreload: () => void } = $props();
</script>

{#if newer}
  <button class="btn sm newer" onclick={onreload}>New version: reload</button>
{/if}

<style>
  .newer { color: var(--info); border-color: color-mix(in srgb, var(--info) 45%, transparent); }
</style>
