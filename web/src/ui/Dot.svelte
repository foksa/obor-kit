<script lang="ts">
  import { tone as toneOf, type Tone } from "../lib/format";
  let { state = "", tone = undefined, title = undefined, size = 8 }:
    { state?: string; tone?: Tone; title?: string; size?: number } = $props();
  const t = $derived(tone ?? toneOf(state));
</script>

<span class="dot" style="background: var(--{t === 'accent' ? 'accent' : t}); width: {size}px; height: {size}px" title={title ?? state} aria-hidden="true"></span>

<style>
  .dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; flex: none; }
</style>
