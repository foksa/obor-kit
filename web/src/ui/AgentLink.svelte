<script lang="ts">
  import { href } from "../lib/route";
  let { project, agent }: { project: string; agent: string | undefined } = $props();
</script>

{#if agent}<a class="mono" href={href.agent(project, agent)}>{agent}</a>{/if}
