<script lang="ts">
  // Pill is a state as a colored label: its tone from the state (or given), a dot unless plain.
  import { tone as toneOf, type Tone } from "../lib/format";
  let { state = "", text = undefined, tone = undefined, dot = true, title = undefined }:
    { state?: string; text?: string; tone?: Tone; dot?: boolean; title?: string } = $props();
  const t = $derived(tone ?? toneOf(state));
</script>

<span class="pill t-{t}" {title}>{#if dot}<span class="dot"></span>{/if}{text ?? state}</span>

<style>
  .pill {
    display: inline-flex; align-items: center; gap: 6px; height: 22px; padding: 0 9px; border-radius: 999px;
    font-size: 12px; font-weight: 500; white-space: nowrap; color: var(--c);
    background: color-mix(in srgb, var(--c) var(--tint), transparent);
  }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: currentColor; flex: none; }
  .t-ok { --c: var(--ok); } .t-wait { --c: var(--wait); } .t-bad { --c: var(--bad); } .t-info { --c: var(--info); }
  .t-done { --c: var(--done); } .t-accent { --c: var(--accent); }
  .t-idle { --c: var(--idle); background: var(--surface-3); }
</style>
