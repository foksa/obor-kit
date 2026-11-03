<script lang="ts">
  // Segmented is a choice of a few options as a row of buttons.
  let { options, value = $bindable(), label, disabled = false, compact = false, onchange = undefined }:
    { options: (string | [string, string])[]; value: string; label: string; disabled?: boolean; compact?: boolean;
      onchange?: (v: string) => void } = $props();
  const opts = $derived(options.map((o) => (Array.isArray(o) ? o : [o, o])));
</script>

<div class="seg" class:compact role="group" aria-label={label}>
  {#each opts as [v, text] (v)}
    <button type="button" class:on={v === value} aria-pressed={v === value} {disabled}
      onclick={() => { value = v; onchange?.(v); }}>{text}</button>
  {/each}
</div>

<style>
  .seg { display: inline-flex; flex-wrap: wrap; gap: 2px; padding: 3px; border-radius: 8px; background: var(--sunk); border: 1px solid var(--line); }
  button {
    height: 26px; padding: 0 10px; border: 0; border-radius: 6px; background: transparent; color: var(--muted);
    font-size: 12px; font-weight: 500; cursor: pointer;
  }
  button:hover:not(:disabled) { color: var(--fg); }
  button.on { background: var(--surface-3); color: var(--fg); }
  .compact { flex-wrap: nowrap; gap: 0; }
  .compact button { padding: 0 7px; flex: 1 1 auto; }
  button:disabled { cursor: default; opacity: 0.6; }
</style>
