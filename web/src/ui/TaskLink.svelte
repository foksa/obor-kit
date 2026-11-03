<script lang="ts">
  // TaskLink is a task id linking to its page: plain, or a chip.
  import { href } from "../lib/route";
  let { project, task, chip = false }: { project: string; task: string; chip?: boolean } = $props();
</script>

<a class:chip class="mono" href={href.task(project, task)}>{task}</a>

<style>
  .chip {
    display: inline-flex; align-items: center; height: 22px; padding: 0 7px; border-radius: 5px; font-size: 12px;
    font-weight: 500; white-space: nowrap; background: color-mix(in srgb, var(--accent) 13%, transparent);
  }
  .chip:hover { text-decoration: none; background: color-mix(in srgb, var(--accent) 22%, transparent); }
</style>
