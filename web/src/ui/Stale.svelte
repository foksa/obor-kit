<script lang="ts">
  // Stale is a page's note that its shown data is old: the last read of a page that has data failed (T-763). It wears
  // the top bar's stale mark.
  let { error }: { error: string } = $props();
</script>

{#if error}<span class="stale" role="status" title={error}><span class="ld"></span><span class="ell">refresh failed: {error}</span></span>{/if}

<style>
  .stale { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--wait); min-width: 0; max-width: 100%; }
  .ld { width: 7px; height: 7px; border-radius: 50%; background: var(--wait); flex: none; }
</style>
