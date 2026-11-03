<script lang="ts">
  // Background is an agent's long-running processes (T-714, T-727): "bg:" a recorded background call, "running" a
  // long foreground command; its description or command and how long it has run.
  import { dur } from "../lib/format";
  import type { Background } from "../lib/types";
  let { list }: { list: Background[] | undefined } = $props();
</script>

{#each list || [] as b (b.pid)}
  <span class="bg small ell" title="pid {b.pid}: {b.command}">{b.background ? "bg:" : "running"} {b.label || b.command} · {dur(b.age)}</span>
{/each}

<style>
  .bg { display: block; font-family: var(--mono); color: var(--muted); }
</style>
