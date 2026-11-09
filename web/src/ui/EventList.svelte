<script lang="ts">
  // EventList is worded events, newest first: an icon per kind, a line to an agent as "→ agent: text", task ids
  // linked, long lines cut to two lines with the rest on a click; a filter by kind; the project shown when asked.
  import type { Event } from "../lib/types";
  import { EVENT_FILTERS, eventKind, lineTo, unixOf, type EventKind } from "../lib/format";
  import Linked from "./Linked.svelte";
  import AgentLink from "./AgentLink.svelte";
  import Rel from "./Rel.svelte";
  import { href } from "../lib/route";

  type Row = Event & { project: string };
  let { events, showProject = false, filter = $bindable("All") }:
    { events: Row[]; showProject?: boolean; filter?: string } = $props();

  const ICON: Record<EventKind, [string, string]> = {
    merge: ["MG", "done"], gate: ["GT", "info"], accept: ["AC", "ok"], submit: ["SB", "info"],
    dispatch: ["DP", "idle"], message: ["→", "accent"], drive: ["DR", "accent"], problem: ["!", "bad"],
    serve: ["SV", "idle"], other: ["·", "idle"],
  };
  const rows = $derived(events.map((e) => ({ e, k: eventKind(e.kind, e.bad), to: lineTo(e.line) }))
    .filter((r) => EVENT_FILTERS[filter]?.(r.k, r.e.bad) ?? true));
  let open = $state<Record<string, boolean>>({});
  const key = (e: Row) => `${e.project}:${e.seq}:${e.ts}`;
</script>

<div class="card list">
  {#each rows as { e, k, to } (key(e))}
    {@const [icon, t0] = ICON[k]}
    {@const t = e.bad ? "bad" : t0}
    {@const long = e.line.length > 180}
    <div class="ev">
      <span class="icon" style="--c: var(--{t})" aria-hidden="true">{icon}</span>
      <div class="body">
        {#if showProject}<a class="proj mono" href={href.project(e.project)}>{e.project}</a>{/if}
        <span class="text" class:clamp={long && !open[key(e)]}>
          {#if to}<span class="muted">→</span> <AgentLink project={e.project} agent={to.agent} />: <Linked project={e.project} text={to.text} />
          {:else}<Linked project={e.project} text={e.line} />{/if}
        </span>
        {#if long}
          <button class="btn ghost sm more" onclick={() => (open[key(e)] = !open[key(e)])}>{open[key(e)] ? "Less" : "Show all"}</button>
        {/if}
      </div>
      <span class="age mono"><Rel at={unixOf(e.ts)} /></span>
    </div>
  {:else}
    <p class="empty">No event{filter !== "All" ? " of this kind" : ""} yet.</p>
  {/each}
</div>

<style>
  .list { overflow: hidden; }
  .ev {
    display: grid; grid-template-columns: 24px minmax(0, 1fr) auto; gap: 10px; align-items: start;
    padding: 9px 14px; border-bottom: 1px solid var(--line-soft);
  }
  .ev:last-child { border-bottom: 0; }
  .icon {
    width: 22px; height: 22px; border-radius: 6px; display: flex; align-items: center; justify-content: center;
    font: 600 10px var(--mono); color: var(--c); background: color-mix(in srgb, var(--c) var(--tint), transparent);
  }
  .body { min-width: 0; display: flex; flex-direction: column; gap: 2px; font-size: 13px; }
  .proj { font-size: 11px; color: var(--muted); }
  .text { overflow-wrap: anywhere; }
  .clamp { display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
  .more { align-self: flex-start; margin-left: -8px; }
  .age { font-size: 11px; color: var(--muted); white-space: nowrap; padding-top: 2px; }
  .empty { margin: 0; padding: 16px; color: var(--muted); font-size: 13px; }
</style>
