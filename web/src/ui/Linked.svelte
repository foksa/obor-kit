<script lang="ts">
  // Linked is a line for a person (human: no obor prefixes, short paths) with its task ids linked.
  import { human, parts } from "../lib/format";
  import { href } from "../lib/route";
  let { project, text, raw = false }: { project: string; text: string | undefined; raw?: boolean } = $props();
  const ps = $derived(parts(raw ? String(text || "") : human(text)));
</script>

{#each ps as p, i (i)}{#if p.task}<a class="mono" href={href.task(project, p.task)}>{p.text}</a>{:else}{p.text}{/if}{/each}
