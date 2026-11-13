<script lang="ts">
  // DocCount is the docs' count line over a filter's matches, one for the list page's Matches and the index: how many
  // docs match by name (`10 of 139`), and in text mode `by name` and the lines found in how many docs
  // (`6 lines in 3 docs`, from the same grouped hits the index lists), then an info dot and `searching…` while a search
  // is out. The numbers are fg, the words muted.
  import { groupHits, matchDocs } from "../lib/docs";
  import type { TextSearch } from "../lib/docsearch.svelte";
  import type { DocList } from "../lib/types";
  import Dot from "./Dot.svelte";

  let { list, q, text = false, search = undefined }:
    { list: DocList; q: string; text?: boolean; search?: TextSearch } = $props();

  const total = $derived(list.pinned.length + list.groups.reduce((n, g) => n + g.docs.length, 0));
  const matches = $derived(matchDocs(list, q).length);
  const on = $derived(text && !!search);
  const found = $derived(on ? search?.resultFor(q) : null);
  const groups = $derived(groupHits(found?.hits));
  const lines = $derived(groups.reduce((n, g) => n + g.hits.length, 0));
  const n = (k: number, one: string) => `${k} ${k === 1 ? one : one + "s"}`;
  const line = $derived.by(() => {
    const names = `${matches} of ${total}`;
    if (!on) return names;
    const parts = [names + " by name"];
    if (found) parts.push(`${n(lines, "line")} in ${n(groups.length, "doc")}`);
    return parts.join(" · ");
  });
</script>

<span class="dcount">{#each line.split(/(\d+)/) as part, i (i)}{#if i % 2}<span class="num">{part}</span>{:else}{part}{/if}{/each}{#if on && search?.busy}{" · "}<span class="busy"><Dot tone="info" size={7} title="searching" />searching…</span>{/if}</span>

<style>
  .dcount { font-size: 12px; color: var(--muted); }
  .num { color: var(--fg); font-variant-numeric: tabular-nums; }
  .busy { display: inline-flex; align-items: center; gap: 6px; vertical-align: middle; }
</style>
