<script lang="ts">
  // The Help page (T-854): how to install obor and set up a project (the kit's README, read by obor web at each
  // request), then every obor command by group with a filter box (the table `obor help` prints).
  import { onDestroy } from "svelte";
  import { api } from "../lib/api";
  import type { Help } from "../lib/types";
  import HelpView from "../ui/HelpView.svelte";

  let help: Help | null = $state(null);
  let error = $state("");
  const ctl = new AbortController();
  api.help(ctl.signal).then((h) => (help = h), (e) => { if (!ctl.signal.aborted) error = String(e?.message || e); });
  onDestroy(() => ctl.abort());
</script>

<div class="page">
  <div class="page-head"><h1>Help</h1><span class="small muted">every obor command, and how to set up a project</span></div>
  {#if help}<HelpView {help} />
  {:else}<p class={error ? "failed" : "loading"}>{error ? "Could not load the help: " + error : "loading…"}</p>{/if}
</div>
