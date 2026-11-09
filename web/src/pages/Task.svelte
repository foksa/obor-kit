<script lang="ts">
  // A task's page: its title and state, its seven steps, what needs you, and its spec, report, jobs and timeline as
  // tabs beside where it runs and its last gate.
  import { onDestroy } from "svelte";
  import { Live } from "../lib/live.svelte";
  import Stale from "../ui/Stale.svelte";
  import { api } from "../lib/api";
  import { href } from "../lib/route";
  import { clock, human, unixOf } from "../lib/format";
  import { milestone, stageOf } from "../lib/stages";
  import { md } from "../lib/md";
  import type { TaskDetail } from "../lib/types";
  import Pill from "../ui/Pill.svelte";
  import Dot from "../ui/Dot.svelte";
  import Stepper from "../ui/Stepper.svelte";
  import AgentLink from "../ui/AgentLink.svelte";
  import Linked from "../ui/Linked.svelte";
  import Rel from "../ui/Rel.svelte";
  import Prompt from "../ui/Prompt.svelte";
  import Folded from "../ui/Folded.svelte";

  let { project, id }: { project: string; id: string } = $props();
  // svelte-ignore state_referenced_locally (the page is keyed by its route: a new project or id is a new page)
  const live = new Live<TaskDetail>((s) => api.task(project, id, s), project);
  live.start();
  onDestroy(() => live.stop());

  const t = $derived(live.data);
  const stage = $derived(t ? stageOf(t, t.records) : null);
  const timeline = $derived((t?.records || []).map((r) => ({ r, m: milestone(r) })).filter((x) => x.m));
  const tabs = $derived.by(() => {
    if (!t) return [];
    const out: [string, string][] = [];
    if (t.spec) out.push(["spec", `Spec r${t.spec_rev}`]);
    if (t.report) out.push(["report", "Report"]);
    out.push(["jobs", `Jobs ${t.jobs.length}`], ["timeline", `Timeline ${timeline.length}`], ["raw", `Records ${t.records.length}`]);
    return out;
  });
  let tab = $state("");
  const shown = $derived(tabs.some(([k]) => k === tab) ? tab : tabs[0]?.[0]);
  const taskHref = (x: string) => href.task(project, x);
  const green = $derived(/^GREEN/.test(t?.gate_line || ""));
</script>

<div class="page">
  {#if !t || !stage}
    <p class={live.error ? "failed" : "loading"}>{live.error ? "Could not load this task: " + live.error : "loading…"}</p>
  {:else}
    <header class="head">
      <div class="tags">
        <span class="id mono">{t.task}</span>
        {#if stage.state}<Pill state={stage.state === "ended" ? "idle" : stage.state} tone={stage.tone} text={stage.state} />{/if}
        {#if t.dispatched_at}<span class="muted small">dispatched <Rel at={t.dispatched_at} /></span>{/if}
        <Stale error={live.data ? live.error : ""} />
      </div>
      <h1>{t.title || t.task}</h1>
    </header>

    {#if t.records.length}<div class="card steps"><Stepper {stage} /></div>{/if}

    {#each t.needs_you as n (n.reason)}
      <div class="banner bad">
        <Dot state="needs-you" /><strong>Needs you</strong>
        <span class="why"><Linked {project} text={n.reason} /></span>
        {#if n.answer}<code class="answer">{n.answer}</code>{/if}
      </div>
    {/each}

    <div class="cols">
      <div class="wide main">
        <div class="tabs" role="tablist">
          {#each tabs as [k, label] (k)}
            <button role="tab" aria-selected={shown === k} class:on={shown === k} onclick={() => (tab = k)}>{label}</button>
          {/each}
        </div>
        {#if shown === "spec"}
          <div class="card pad"><Folded key="spec" long={(t.spec || "").length > 2400}><div class="prose">{@html md(t.spec || "", taskHref)}</div></Folded></div>
        {:else if shown === "report"}
          <div class="card pad"><Folded key="report" long={(t.report || "").length > 2400}><div class="prose">{@html md(t.report || "", taskHref)}</div></Folded></div>
        {:else if shown === "jobs"}
          <div class="card">
            {#each t.jobs as j, i (j.seq)}<Prompt {project} p={j} open={i === 0 && j.text.length < 6000} />
            {:else}<p class="empty">No job sent yet.</p>{/each}
          </div>
        {:else if shown === "timeline"}
          <div class="card">
            {#each timeline as { r, m } (r.seq)}
              <div class="tl">
                <span class="muted small mono"><Rel at={unixOf(r.ts)} /></span>
                <Dot tone={m!.tone} />
                <span><Linked {project} text={m!.line} /></span>
              </div>
            {:else}<p class="empty">Nothing recorded yet.</p>{/each}
          </div>
        {:else if shown === "raw"}
          <div class="card raw">
            <table>
              <thead><tr><th>seq</th><th>time</th><th>step</th><th>kind</th><th>line</th></tr></thead>
              <tbody>
                {#each t.records as r (r.seq)}
                  <tr><td class="mono muted">{r.seq}</td><td class="mono muted nowrap">{clock(unixOf(r.ts))}</td>
                    <td class="mono">{r.step}</td><td class="muted">{r.kind}</td><td>{r.line || ""}</td></tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </div>

      <aside class="narrow rail">
        <section class="card pad kvs" aria-label="Where it runs">
          <h2 class="lbl">Where it runs</h2>
          <div class="kv"><span>Lane</span><span class="mono">{t.lane || "—"}</span></div>
          <div class="kv"><span>Agent</span>{#if t.agent}<AgentLink {project} agent={t.agent} />{:else}<span>—</span>{/if}</div>
          <div class="kv"><span>State</span><span>{t.task_state || "—"}</span></div>
          <div class="kv"><span>Attempt</span><span>{t.attempt || "—"}</span></div>
          <div class="kv"><span>Spec</span><span>{t.spec_rev ? "r" + t.spec_rev : "—"}</span></div>
        </section>
        {#if t.gate_line}
          <section class="card pad gate" aria-label="Last gate">
            <div class="gh"><h2 class="lbl">Last gate</h2><Pill state={green ? "green" : "fail"} text={green ? "pass" : "fail"} /></div>
            <p class="mono gl">{human(t.gate_line)}</p>
            {#if t.gated_rev}
              <p class="muted small">checked spec r{t.gated_rev}{#if t.spec_rev && t.spec_rev !== t.gated_rev}<span class="warn"> · spec is r{t.spec_rev} now</span>{/if}</p>
            {/if}
          </section>
        {/if}
      </aside>
    </div>
  {/if}
</div>

<style>
  .head { display: flex; flex-direction: column; gap: 10px; }
  .tags { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
  .id { font-size: 13px; color: var(--accent); padding: 3px 8px; border-radius: 5px; background: color-mix(in srgb, var(--accent) 13%, transparent); }
  h1 { margin: 0; font-size: 22px; line-height: 1.3; font-weight: 600; letter-spacing: -0.01em; max-width: 900px; }
  .steps { padding: 18px 20px; }
  .why { flex: 1 1 300px; overflow-wrap: anywhere; }
  .answer { flex-basis: 100%; padding: 6px 10px; overflow-x: auto; white-space: nowrap; }
  .main { gap: 14px !important; }
  .tabs { display: flex; gap: 18px; border-bottom: 1px solid var(--line); overflow-x: auto; }
  .tabs button {
    height: 38px; padding: 0 2px; border: 0; background: none; color: var(--muted); font-size: 13px; font-weight: 500;
    border-bottom: 2px solid transparent; cursor: pointer; white-space: nowrap;
  }
  .tabs button.on { color: var(--fg); border-bottom-color: var(--accent); }
  .pad { padding: 20px 24px; }
  .empty { margin: 0; padding: 16px; color: var(--muted); }
  .tl { display: grid; grid-template-columns: 80px 10px minmax(0, 1fr); gap: 10px; align-items: center; padding: 9px 16px; border-bottom: 1px solid var(--line-soft); font-size: 13px; overflow-wrap: anywhere; }
  .tl:last-child { border-bottom: 0; }
  .raw { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; font-size: 12px; }
  th { text-align: left; color: var(--muted); font-weight: 600; padding: 8px 12px; }
  td { padding: 7px 12px; border-top: 1px solid var(--line-soft); vertical-align: top; }
  .nowrap { white-space: nowrap; }
  .rail { gap: 14px !important; max-width: 420px; }
  .kvs { padding: 16px; }
  .kv { display: flex; justify-content: space-between; gap: 12px; padding: 8px 0; border-top: 1px solid var(--line-soft); font-size: 13px; }
  .kv > span:first-child { color: var(--muted); }
  .kvs h2 { margin-bottom: 8px; }
  .gate { padding: 16px; display: flex; flex-direction: column; gap: 8px; }
  .gh { display: flex; align-items: center; justify-content: space-between; }
  .gl { margin: 0; font-size: 12px; overflow-wrap: anywhere; }
  .gate p { margin: 0; }
  .warn { color: var(--wait); }
</style>
