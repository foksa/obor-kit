<script lang="ts">
  // DocModes is what the docs' filter searches: "Names" (the docs' paths and titles) or "Search the text" (their text
  // too). One toggle for the list page's filter, the reading sidebar and the narrow drawer (tall: 44 px targets).
  let { mode = $bindable("path"), tall = false, onchange = undefined }: {
    mode?: "path" | "text"; tall?: boolean; onchange?: () => void;
  } = $props();

  function set(m: "path" | "text") {
    mode = m;
    onchange?.();
  }
</script>

<div class="modes" class:tall role="group" aria-label="What the filter searches">
  <button type="button" class="mode" aria-pressed={mode !== "text"} onclick={() => set("path")}>Names</button>
  <button type="button" class="mode" aria-pressed={mode === "text"} onclick={() => set("text")}>Search the text</button>
</div>

<style>
  .modes { display: flex; gap: 4px; }
  .mode {
    flex: 1 1 0; height: 26px; padding: 0 8px; border: 1px solid var(--line); border-radius: 6px; background: transparent;
    color: var(--muted); font-size: 12px; cursor: pointer; white-space: nowrap;
  }
  .mode:hover { color: var(--fg); }
  .mode[aria-pressed="true"] { background: var(--surface-3); color: var(--fg); border-color: var(--line-strong); font-weight: 600; }
  .tall .mode { height: 44px; font-size: 13px; }
</style>
