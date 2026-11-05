<script lang="ts">
  // A project's dashboard: what needs you, the status row, the pipeline, the lanes, then the lead's watches, the
  // queue and the role agents beside the events.
  import { onDestroy } from "svelte";
  import { Live } from "../lib/live.svelte";
  import Stale from "../ui/Stale.svelte";
  import { api } from "../lib/api";
  import { href } from "../lib/route";
  import { dur, now, roleName, splitTasks, unixOf } from "../lib/format";
  import { idleText } from "../lib/idle";
  import { stageOf, taskOf } from "../lib/stages";
  import type { Lane, MergeItem, ProjectView, Role, WaitItem } from "../lib/types";
  import Pill from "../ui/Pill.svelte";
  import Dot from "../ui/Dot.svelte";
  import Stat from "../ui/Stat.svelte";
  import Section from "../ui/Section.svelte";
  import Stepper from "../ui/Stepper.svelte";
  import TaskLink from "../ui/TaskLink.svelte";
  import AgentLink from "../ui/AgentLink.svelte";
  import Linked from "../ui/Linked.svelte";
  import Rel from "../ui/Rel.svelte";
  import EventList from "../ui/EventList.svelte";
  import Segmented from "../ui/Segmented.svelte";
  import Background from "../ui/Background.svelte";

  let { project }: { project: string } = $props();
  // svelte-ignore state_referenced_locally (the page is keyed by its route: a new project or id is a new page)
  const live = new Live<ProjectView>((s) => api.view(project, s), project);
  live.start();
  onDestroy(() => live.stop());

  const v = $derived(live.data);
  const lanes = $derived(v?.lanes || []);
  const busy = $derived(lanes.filter((l) => !l.free));
  const free = $derived(lanes.filter((l) => l.free));
  // task is the view's task of an id (T-883): its title, phase, lane and step read the same on every section.
  const task = (id: string | null | undefined) => taskOf(v, id);
  const q = $derived(v?.queue);
  const merge = $derived(q?.merge || []);
  const waiting = $derived(q?.waiting || []);

  // The pipeline: the dispatches waiting, the lanes at work, then the merge loop's stages (held only when a task is).
  type Col = { name: string; tone: string; tasks: string[] };
  const pipeline = $derived.by((): Col[] => {
    const at = (st: string) => merge.filter((m: MergeItem) => m.stage === st).map((m) => m.task);
    const work = busy.filter((l) => l.task && !merge.some((m) => m.task === l.task)).map((l) => l.task!);
    const cols: Col[] = [
      { name: "Waiting", tone: "wait", tasks: waiting.map((w: WaitItem) => w.task) },
      { name: "Working", tone: "ok", tasks: work },
      { name: "Submitted", tone: "info", tasks: at("submitted") },
      { name: "Gate", tone: "info", tasks: at("gating") },
      { name: "Review", tone: "info", tasks: at("review") },
      { name: "Merging", tone: "done", tasks: at("publish") },
    ];
    const held = at("held");
    if (held.length) cols.push({ name: "Held", tone: "bad", tasks: held });
    return cols;
  });

  const roles = $derived.by((): Role[] => {
    const rs = v?.roles || [];
    return v?.lead ? [v.lead, ...rs] : rs;
  });
  const jobStep = (id: string) => id.split("/").slice(2).join("/") || id;
  const events = $derived((v?.events || []).map((e) => ({ ...e, project })));
  let filter = $state("All");
  const warn = $derived.by(() => {
    const s = v?.serve;
    const w: { text: string; state: string }[] = [];
    if (!s) return w;
    if (s.up && !s.project) w.push({ text: "project not loaded", state: "pending" });
    if (s.up && !s.merging) w.push({ text: "merge loop off", state: "idle" });
    if (s.up && !s.herdr) w.push({ text: "herdr not connected", state: "blocked" });
    if (s.log_error) w.push({ text: "log: " + s.log_error, state: "fail" });
    for (const n of v?.notes || []) w.push({ text: n, state: "pending" });
    return w;
  });
  const laneStage = (l: Lane) => stageOf(task(l.task));
  const waitWhy = (w: WaitItem) => {
    if (w.waits_on === "after") return "after " + (w.after || []).join(", ") + (w.why ? " · " + w.why : "");
    if (w.waits_on === "never dispatched") return "spec r" + (task(w.task).spec_rev || 0) + " stored, never dispatched";
    return "waits for a free " + (w.level ? w.level + " " : "") + "lane" +
      (w.types?.length ? " (" + w.types.join(", ") + ")" : "") + (w.why ? " · " + w.why : "");
  };
  let copied = $state("");
  async function copy(text: string) {
    try { await navigator.clipboard.writeText(text); copied = text; setTimeout(() => (copied = ""), 1500); } catch { /* no clipboard */ }
  }
</script>

<div class="page">
  {#if !v}
    <p class={live.error ? "failed" : "loading"}>{live.error ? "Could not load this project: " + live.error : "loading…"}</p>
  {:else}
    <div class="page-head">
      <h1>{project}</h1>
      {#if v.serve.up}<Pill state="up" text="serve up {v.serve.since ? dur(now() - unixOf(v.serve.since)) : ''}" />
      {:else}<Pill state="down" text="serve down" title={v.serve.error} />{/if}
      {#each warn as w (w.text)}<Pill state={w.state} text={w.text} />{/each}
      {#each v.running || [] as r (r.step + r.task)}
        <Pill state="running" text="{r.step}{r.task ? ' ' + r.task : ''} · {dur(now() - r.since)}" title={r.command} />
      {/each}
      <Stale error={live.data ? live.error : ""} />
      <a class="btn sm head-btn" href={href.setup(project)}>Setup</a>
    </div>
    {#if !v.serve.up && v.serve.error}<p class="muted small down"><Linked {project} text={v.serve.error} /></p>{/if}

    {#if v.paused}
      <div class="banner wait">
        <strong>Dispatch paused{#if v.paused.until}&nbsp;<Rel at={v.paused.until} mode="in" />{/if}</strong>
        {#if v.paused.reason}<span>{v.paused.reason}</span>{/if}
        {#if v.paused.by}<span class="muted small">by {v.paused.by}</span>{/if}
        <a class="btn sm" href={href.setup(project)} style="margin-left: auto">Resume in Setup</a>
      </div>
    {/if}

    {#if v.idle}
      <div class="banner" class:wait={!v.idle.acked} class:ok={v.idle.acked}>
        <strong>{idleText(v.idle)}</strong>
        {#if v.idle.last}<span class="muted small">last: {v.idle.last}</span>{/if}
        {#if !v.idle.acked}
          <span class="muted small" style="margin-left: auto">dispatch next, or acknowledge with <code>obor idle "&lt;why&gt;"</code></span>
        {/if}
      </div>
    {/if}

    {#if v.needs_you.length}
      <Section title="Needs you" count={v.needs_you.length}>
        <div class="card needs">
          {#each v.needs_you as n (n.task + n.reason)}
            <div class="need">
              <div class="need-head">
                <Dot state="needs-you" /><TaskLink {project} task={n.task} chip />
                {#if task(n.task).lane}<span class="mono small muted">{task(n.task).lane}</span>{/if}
                <span class="muted small" style="margin-left: auto"><Rel at={n.since} /></span>
              </div>
              <p class="why"><Linked {project} text={n.reason} /></p>
              {#if n.answer}
                <div class="answer">
                  <code>{n.answer}</code>
                  <button class="btn sm" onclick={() => copy(n.answer!)}>{copied === n.answer ? "Copied" : "Copy"}</button>
                </div>
              {/if}
            </div>
          {/each}
        </div>
      </Section>
    {:else}
      <div class="banner ok">
        <svg width="18" height="18" viewBox="0 0 16 16" fill="none" stroke="var(--ok)" stroke-width="1.8" aria-hidden="true"><circle cx="8" cy="8" r="6.5"></circle><path d="M5 8.2l2 2 4-4.4"></path></svg>
        <strong>Nothing needs you</strong>
        <span class="muted small">Questions, red gates, sendbacks and failed starts show here first, with the command that answers them.</span>
      </div>
    {/if}

    <div class="stats">
      <Stat label="Lanes">{busy.length}<span class="of">/ {lanes.length} busy</span></Stat>
      <Stat label="Queue">{merge.length + waiting.length}
        {#snippet foot()}{merge.length} merging · {waiting.length} waiting{/snippet}</Stat>
      <Stat label="Watches">{(v.watches || []).length}</Stat>
      <Stat label="Last full check">
        {#if v.full_check}<Pill state={v.full_check.verdict === "GREEN" ? "green" : "fail"} text={v.full_check.verdict} />{:else}<span class="muted">—</span>{/if}
        {#snippet foot()}
          {#if v.full_check}<span class="mono">{v.full_check.sha.slice(0, 7)}</span> · <Rel at={v.full_check.at} />{/if}
          {#if q?.nightly}<div>next {q.nightly.at} · <Rel at={q.nightly.next} mode="in" /></div>{/if}
        {/snippet}
      </Stat>
    </div>

    <Section title="Pipeline" sub="tasks by stage now">
      <div class="pipe">
        {#each pipeline as c (c.name)}
          <div class="stage t-{c.tasks.length ? c.tone : 'none'}">
            <span class="sname">{c.name}</span>
            <span class="n">{c.tasks.length}</span>
            {#if c.tasks.length}
              <div class="chips">{#each c.tasks.slice(0, 4) as t (t)}<TaskLink {project} task={t} chip />{/each}{#if c.tasks.length > 4}<span class="muted small">+{c.tasks.length - 4}</span>{/if}</div>
            {/if}
          </div>
        {/each}
      </div>
      {#if q?.scratch}<p class="muted small">Merge worktree: <TaskLink {project} task={q.scratch.task} /> ({q.scratch.why})</p>{/if}
    </Section>

    <Section title="Lanes" sub="{busy.length} busy · {free.length} free">
      {#snippet actions()}<a class="small" href={href.setup(project)}>Configure →</a>{/snippet}
      <div class="lanes">
        {#each busy as l (l.lane)}
          {@const st = laneStage(l)}
          {@const tk = task(l.task)}
          <article class="card lane busy t-{st.tone}">
            <div class="lhead">
              <span class="mono lname">{l.lane}</span>
              <Pill state={l.column} />
              <span class="mono small muted ell">{[l.type, tk.spec_rev ? "spec r" + tk.spec_rev : "", tk.attempt && tk.attempt > 1 ? "attempt " + tk.attempt : "", tk.effort ? "effort " + tk.effort : ""].filter(Boolean).join(" · ")}</span>
              <span class="mono elapsed"><Rel at={l.since} mode="for" /></span>
            </div>
            {#if l.task}
              <div class="ltitle"><TaskLink {project} task={l.task} chip /><a class="title" href={href.task(project, l.task)}>{tk.title || "no title"}</a></div>
            {:else}<p class="muted small">no task</p>{/if}
            <Stepper stage={st} compact />
            <div class="lfoot small">
              {#if l.agent}<span>agent <AgentLink {project} agent={l.agent} />{#if l.agent_status}<span class="muted"> · {l.agent_status}</span>{/if}</span>{/if}
              {#if tk.job}<span>job <a class="mono" href={href.job(project, tk.job.id)}>{jobStep(tk.job.id)}</a>{#if !tk.job.delivered} <Pill state="not fetched" />{/if}</span>{/if}
              {#if l.branch}<span class="muted">branch <span class="mono">{l.branch}</span>{#if l.dirty}<span class="dirty"> · dirty</span>{/if}</span>{/if}
              {#if l.op}<span class="muted">{l.op}</span>{/if}
              {#if l.reason}<Pill state="fail" text={l.reason} />{/if}
              {#if l.warning}<Pill state="fail" text={l.warning} />{/if}
            </div>
            <Background list={l.background} />
          </article>
        {/each}
        {#each free as l (l.lane)}
          <article class="card lane free">
            <div class="lhead">
              <span class="mono lname">{l.lane}</span>
              <Pill state={l.state} text={l.state === "dirty-free" ? "free · dirty" : "free"} />
              <span class="mono small muted" style="margin-left: auto">{l.type || "pooled"}</span>
            </div>
            <span class="small muted ell">
              {#if l.task}last <TaskLink {project} task={l.task} />{:else}never used{/if}
              {#if l.agent} · <AgentLink {project} agent={l.agent} />{/if}
            </span>
          </article>
        {/each}
      </div>
    </Section>

    <div class="cols">
      <div class="wide">
        {#if v.watches?.length}
          <Section title="Watches" count={v.watches.length} sub="the lead acts when these tasks land">
            <div class="card">
              {#each v.watches as w, i (i)}
                <article class="watch">
                  <div class="whead">
                    <span class="muted small">when</span>
                    {#each splitTasks(w.task) as t (t)}<TaskLink {project} task={t} chip />{/each}
                    <span class="muted small wby">{w.by || ""}{w.by ? " · " : ""}<Rel at={w.since} /></span>
                  </div>
                  {#if w.note}<p class="wnote"><Linked {project} text={w.note} /></p>{:else}<p class="wnote muted">no note</p>{/if}
                </article>
              {/each}
            </div>
          </Section>
        {/if}

        {#if waiting.length || (q?.review || []).length || v.review || (q?.outbox || []).length}
          <Section title="Queue">
            <div class="card">
              {#each waiting as w (w.task)}
                <div class="qrow">
                  <Pill state={w.waits_on === "never dispatched" ? "idle" : "waiting"} text={w.waits_on} />
                  <div class="qbody"><span><TaskLink {project} task={w.task} /> {task(w.task).title || ""}</span><span class="muted small"><Linked {project} text={waitWhy(w)} /></span></div>
                  <span class="muted small"><Rel at={w.since} /></span>
                </div>
              {/each}
              {#each q?.review || [] as r, i (i)}
                <div class="qrow">
                  <Pill state={r.state === "ready" ? "ready" : "waiting"} text="review {r.state}" />
                  <div class="qbody">
                    <span><Linked {project} text={r.note || "review requested"} /></span>
                    <span class="muted small">
                      {#if r.after?.length}after {#each r.after as t, j (t)}{j ? ", " : ""}<TaskLink {project} task={t} />{/each}{/if}
                      {#if r.waiting?.length} · waits on {#each r.waiting as t, j (t)}{j ? ", " : ""}<TaskLink {project} task={t} />{/each}{/if}
                      {#if r.held} · holds <TaskLink {project} task={r.held} />{/if}
                      {#if r.by} · by {r.by}{/if}
                    </span>
                  </div>
                  <span class="muted small"><Rel at={r.since} /></span>
                </div>
              {/each}
              {#if !(q?.review || []).length && v.review}
                <div class="qrow"><Pill state="waiting" text="review" /><div class="qbody"><Linked {project} text={v.review} /></div></div>
              {/if}
              {#each q?.outbox || [] as o, i (i)}
                <div class="qrow">
                  <Pill state={o.held ? "waiting" : "pending"} text={o.kind === "line" ? "line" : "brief"} />
                  <div class="qbody">
                    {#if o.kind === "line"}
                      <span>→ <AgentLink {project} agent={o.to} />: <Linked {project} text={o.line} /></span>
                      {#if o.from || o.held}<span class="muted small">{o.from ? "from " + o.from : ""}{o.held ? " · undelivered: " + o.held : ""}</span>{/if}
                    {:else}
                      <span>brief to <AgentLink {project} agent={o.to} /> <span class="muted">{o.worked ? "being worked" : o.delivered ? "delivered" : "not delivered"}</span></span>
                    {/if}
                  </div>
                  <span class="muted small"><Rel at={o.since} /></span>
                </div>
              {/each}
            </div>
          </Section>
        {/if}

        <Section title="Role agents" count={roles.length}>
          <div class="card table-wrap">
            <table>
              <thead><tr><th>Role</th><th>Agent</th><th>Status</th><th>Doing</th><th class="r">For</th></tr></thead>
              <tbody>
                {#each roles as r (r.role + r.agent)}
                  <tr>
                    <td>{roleName(r.role)}</td>
                    <td><AgentLink {project} agent={r.agent} /></td>
                    <td>{#if r.status}<Pill state={r.status === "done" ? "idle" : r.status} text={r.status} />{:else}<Pill state="idle" text="no pane" />{/if}</td>
                    <td class="small">
                      {#if r.brief}brief {r.brief.worked ? "being worked" : r.brief.delivered ? "delivered" : "not delivered"} · <Rel at={r.brief.since} />{/if}
                      <Background list={r.background} />
                    </td>
                    <td class="r muted small">{r.age ? dur(r.age) : ""}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </Section>
      </div>

      <div class="narrow">
        <Section title="Events">
          {#snippet actions()}<Segmented label="Show" options={["All", "Flow", "Messages", "Problems"]} bind:value={filter} />{/snippet}
          <EventList {events} bind:filter />
        </Section>
      </div>
    </div>
  {/if}
</div>

<style>
  .head-btn { margin-left: auto; }
  .down { margin: -12px 0 0; }
  .of { font-size: 16px; color: var(--muted); font-weight: 500; }
  .needs { overflow: hidden; }
  .need { padding: 14px 16px; border-bottom: 1px solid var(--line-soft); display: flex; flex-direction: column; gap: 8px; }
  .need:last-child { border-bottom: 0; }
  .need-head { display: flex; align-items: center; gap: 10px; }
  .why { margin: 0; overflow-wrap: anywhere; }
  .answer { display: flex; align-items: center; gap: 8px; }
  .answer code { flex: 1; min-width: 0; padding: 6px 10px; overflow-x: auto; white-space: nowrap; }

  .pipe { display: grid; grid-template-columns: repeat(auto-fit, minmax(120px, 1fr)); gap: 6px; }
  .stage {
    display: flex; flex-direction: column; gap: 6px; padding: 10px 12px; border-radius: 8px; min-width: 0;
    border: 1px solid color-mix(in srgb, var(--c) 40%, transparent); background: color-mix(in srgb, var(--c) 9%, transparent);
  }
  .stage.t-none { --c: var(--line); background: var(--sunk); border-color: var(--line-soft); }
  .t-ok { --c: var(--ok); } .t-wait { --c: var(--wait); } .t-bad { --c: var(--bad); } .t-info { --c: var(--info); }
  .t-done { --c: var(--done); } .t-accent { --c: var(--accent); } .t-idle { --c: var(--idle); }
  .sname { font-size: 12px; color: var(--fg-2); }
  .n { font-size: 22px; font-weight: 600; color: var(--c); line-height: 1; }
  .t-none .n { color: var(--faint); }
  .chips { display: flex; flex-wrap: wrap; gap: 4px; }

  .lanes { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }
  .lane { padding: 14px 16px; display: flex; flex-direction: column; gap: 10px; min-width: 0; }
  .lane.busy { grid-column: span 2; border-color: color-mix(in srgb, var(--c) 40%, var(--line)); }
  .lane.free { background: var(--sunk); gap: 8px; }
  @media (max-width: 700px) { .lane.busy { grid-column: auto; } }
  .lhead { display: flex; align-items: center; gap: 10px; min-width: 0; }
  .lname { font-weight: 600; }
  .elapsed { margin-left: auto; font-size: 13px; color: var(--fg-2); flex: none; }
  .ltitle { display: flex; gap: 8px; align-items: baseline; min-width: 0; }
  .title { color: var(--fg); font-weight: 500; line-height: 1.4; }
  .lfoot { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 14px; padding-top: 10px; border-top: 1px solid var(--line-soft); color: var(--muted); }
  .dirty { color: var(--wait); }

  .watch { padding: 14px 16px; border-bottom: 1px solid var(--line-soft); display: flex; flex-direction: column; gap: 8px; }
  .watch:last-child { border-bottom: 0; }
  .whead { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
  .wby { margin-left: auto; }
  .wnote { margin: 0; font-size: 13px; line-height: 1.55; color: var(--fg-2); overflow-wrap: anywhere; }

  .qrow { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; gap: 12px; align-items: start; padding: 11px 16px; border-bottom: 1px solid var(--line-soft); }
  .qrow:last-child { border-bottom: 0; }
  .qbody { display: flex; flex-direction: column; gap: 2px; min-width: 0; overflow-wrap: anywhere; font-size: 13px; }

  .table-wrap { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; font-size: 13px; min-width: 520px; }
  th { text-align: left; font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; color: var(--muted); font-weight: 600; padding: 10px 12px; }
  td { padding: 10px 12px; border-top: 1px solid var(--line-soft); vertical-align: top; }
  th:first-child, td:first-child { padding-left: 16px; }
  .r { text-align: right; padding-right: 16px; }
</style>
