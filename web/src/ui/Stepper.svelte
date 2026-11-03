<script lang="ts">
  // Stepper is a task's place in its seven steps: compact (a bar per step) or full (numbered circles).
  import { TASK_STEPS, type Stage } from "../lib/stages";
  // labels: the step names under a compact stepper's bars (off where its row already names the state)
  let { stage, compact = false, labels = true }: { stage: Stage; compact?: boolean; labels?: boolean } = $props();
  const color = $derived(stage.tone === "idle" ? "var(--fg-2)" : `var(--${stage.tone})`);
</script>

<ol class:compact style="--cur: {color}" aria-label="Progress: {stage.state || 'not started'}">
  {#each TASK_STEPS as name, i (name)}
    <li class:done={i < stage.i} class:cur={i === stage.i} aria-current={i === stage.i ? "step" : undefined}>
      {#if compact}
        <span class="bar" title={name}></span>{#if labels}<span class="name">{name}</span>{/if}
      {:else}
        <div class="row"><span class="n">{i < stage.i ? "✓" : i + 1}</span><span class="line"></span></div>
        <span class="name">{name}</span>
      {/if}
    </li>
  {/each}
</ol>

<style>
  ol { list-style: none; margin: 0; padding: 0; display: grid; grid-template-columns: repeat(7, minmax(0, 1fr)); }
  .compact { gap: 4px; }
  li { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
  .name { font-size: 12px; color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  li.done .name, li.cur .name { color: var(--fg-2); }
  li.cur .name { color: var(--cur); font-weight: 600; }
  .bar { height: 4px; border-radius: 2px; background: var(--line); }
  li.done .bar { background: var(--ok); }
  li.cur .bar { background: var(--cur); }
  .row { display: flex; align-items: center; }
  .n {
    width: 24px; height: 24px; border-radius: 50%; flex: none; display: flex; align-items: center; justify-content: center;
    font-size: 11px; font-weight: 600; border: 1.5px solid var(--line-strong); color: var(--muted); background: var(--surface);
  }
  li.done .n { background: var(--ok); border-color: var(--ok); color: var(--bg); }
  li.cur .n { border-color: var(--cur); color: var(--cur); background: color-mix(in srgb, var(--cur) 16%, transparent); }
  .line { flex: 1; height: 2px; background: var(--line); }
  li.done .line { background: var(--ok); }
  li:last-child .line { visibility: hidden; }
</style>
