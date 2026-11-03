<script lang="ts">
  // The Setup page (T-750, T-753, T-760, T-766): what steers the project's work — its pause, the model refs each
  // difficulty tries, the roles' models, how many lanes, the models in use with their pauses, the kinds' extras — and
  // how its dispatches spread. Edits are staged and committed together as one commit of .obor/config.json on main; a
  // pause (the project's or a model's) acts at once.
  import { onDestroy } from "svelte";
  import { Live } from "../lib/live.svelte";
  import Stale from "../ui/Stale.svelte";
  import { api, post } from "../lib/api";
  import { roleName } from "../lib/format";
  import {
    addRef, body, busyDropped, chip, current, efforts, inUse, joinRef, model, move, pauseBody, show, splitRef, stage,
    usedBy, value, type Staged, type Value,
  } from "../lib/setup";
  import type { Setup } from "../lib/types";
  import Pill from "../ui/Pill.svelte";
  import Dot from "../ui/Dot.svelte";
  import Section from "../ui/Section.svelte";
  import Segmented from "../ui/Segmented.svelte";
  import AgentLink from "../ui/AgentLink.svelte";
  import Rel from "../ui/Rel.svelte";

  let { project }: { project: string } = $props();
  // svelte-ignore state_referenced_locally (the page is keyed by its route: a new project is a new page)
  const live = new Live<Setup>((sig) => api.setup(project, sig), project);
  live.start();
  onDestroy(() => live.stop());
  const s = $derived(live.data);

  // ---- staged edits ----
  let staged = $state<Staged>({});
  let base = $state(""); // the config revision the staged edits are made on: the page's when the first was staged
  let busy = $state(false);
  let saveErr = $state("");
  let saved = $state("");
  let reviewing = $state(false);
  const count = $derived(Object.keys(staged).length);
  const moved = $derived(!!s && count > 0 && base !== "" && base !== s.config_rev);

  function set(key: string, v: Value) {
    if (!s || busy) return;
    if (!count) base = s.config_rev;
    staged = stage(staged, s, key, v);
    if (!Object.keys(staged).length) base = "";
    saved = "";
    saveErr = "";
  }
  const val = (key: string) => (s ? value(staged, s, key) : undefined);
  const isStaged = (key: string) => key in staged;
  function discard() { staged = {}; base = ""; saveErr = ""; reviewing = false; }
  // rebase: the owner has seen the setup as it is now and keeps the staged edits on it
  function rebase() { if (s) base = s.config_rev; saveErr = ""; reviewing = true; }
  async function commit() {
    if (!s || busy || !count) return;
    busy = true; saveErr = ""; saved = "";
    try {
      const r = await post<{ reply: string }>(`/api/config?project=${encodeURIComponent(project)}`, body(staged, base));
      staged = {}; base = ""; reviewing = false;
      saved = r.reply;
    } catch (e) {
      saveErr = (e as Error).message;
    } finally {
      try { await live.read(); } catch { /* the live mark says so */ }
      busy = false;
    }
  }
  const review = $derived(s ? Object.keys(staged).sort().map((k) => ({ key: k, from: show(current(s, k)), to: show(staged[k]) })) : []);

  // ---- pauses (at once, not staged): the project's (model "") or a model's ----
  let reason = $state("");
  let pauseFor = $state("1h");
  let pauseErr = $state("");
  let pausing = $state(""); // the model whose pause form is open ("": none)
  let mReason = $state("");
  let mFor = $state("1h");
  let mErr = $state<Record<string, string>>({});
  async function pause(m: string, clear: boolean, why: string, dur: string) {
    if (busy) return;
    busy = true;
    if (m) mErr = { ...mErr, [m]: "" }; else pauseErr = "";
    try {
      await post(`/api/pause?project=${encodeURIComponent(project)}`, pauseBody(m, clear, why, dur));
      if (m) { pausing = ""; mReason = ""; } else reason = "";
    } catch (e) {
      if (m) mErr = { ...mErr, [m]: (e as Error).message }; else pauseErr = (e as Error).message;
    } finally {
      try { await live.read(); } catch { /* marked */ }
      busy = false;
    }
  }
  const DURATIONS = ["30m", "1h", "3h", "until cleared"];

  // ---- what the sections draw ----
  const names = $derived((s?.models || []).map((m) => m.name));
  const used = $derived(s ? usedBy(staged, s, roleName) : {});
  const models = $derived(s ? inUse(s, used) : []);
  const recent = $derived.by(() => {
    const r: Record<string, number> = {};
    for (const c of s?.distribution || []) r[c.level] = (r[c.level] || 0) + c.recent;
    return r;
  });

  // level chains: a ref's chips, and the pick (model, effort) a level's add control holds
  const chain = (level: string) => ((val("difficulty." + level) as string[]) || []);
  const setChain = (level: string, list: string[]) => set("difficulty." + level, list);
  let pick = $state<Record<string, { m: string; e: string }>>({});
  function add(level: string) {
    const p = pick[level];
    if (!p?.m) return;
    setChain(level, addRef(chain(level), joinRef(p.m, p.e)));
    pick = { ...pick, [level]: { m: "", e: "" } };
  }

  // a role's model change keeps its effort only when the new model's kind takes it
  function setRole(role: string, m: string, e: string) {
    if (!s) return;
    set("roles." + role, joinRef(m, efforts(s, m).includes(e) ? e : ""));
  }

  // lanes: the count, and the lanes a lower count would drop that hold a task (serve refuses it)
  const lanes = $derived((val("lanes") as number) ?? 0);
  const dropsBusy = $derived(s ? busyDropped(s, lanes) : []);

  let distRange = $state("7 days");
  const dist = $derived.by(() => {
    const cells = s?.distribution || [];
    const rows: string[] = [], cols: string[] = [];
    for (const c of cells) {
      if (!rows.includes(c.level)) rows.push(c.level);
      if (!cols.includes(c.type)) cols.push(c.type);
    }
    cols.sort((a, b) => (a === "" ? 1 : b === "" ? -1 : a.localeCompare(b)));
    const n = (l: string, t: string) => {
      const c = cells.find((x) => x.level === l && x.type === t);
      return c ? (distRange === "all" ? c.all : c.recent) : 0;
    };
    const max = Math.max(1, ...rows.flatMap((l) => cols.map((t) => n(l, t))));
    return { rows, cols, n, max };
  });
  const heat = (n: number, max: number) => (n ? `color-mix(in srgb, var(--accent) ${Math.round(12 + (60 * n) / max)}%, transparent)` : "var(--sunk)");
</script>

<div class="page">
  <div class="page-head">
    <h1>Setup</h1>
    <span class="muted small">what steers {project}'s work · edits land as one commit of <code>.obor/config.json</code> on main</span>
    <Stale error={live.data ? live.error : ""} />
  </div>

  {#if !s}
    <p class={live.error ? "failed" : "loading"}>{live.error ? "Could not load the setup: " + live.error : "loading…"}</p>
  {:else}
    {#if saved}<div class="banner ok" role="status"><strong>Saved</strong><span class="small">{saved}</span></div>{/if}

    <!-- dispatch: pause / resume -->
    <section class="card dispatch" class:paused={!!s.paused} aria-label="Dispatch">
      <div class="dhead">
        <span class="dicon" aria-hidden="true">
          {#if s.paused}<svg width="18" height="18" viewBox="0 0 16 16" fill="var(--wait)"><rect x="4" y="3" width="3" height="10" rx="1"></rect><rect x="9" y="3" width="3" height="10" rx="1"></rect></svg>
          {:else}<svg width="18" height="18" viewBox="0 0 16 16" fill="var(--ok)"><path d="M5 3l8 5-8 5z"></path></svg>{/if}
        </span>
        <div>
          {#if s.paused}
            <h2>Dispatch paused{#if s.paused.until} · ends <Rel at={s.paused.until} mode="in" />{/if}</h2>
            <p class="muted small">{s.paused.reason}{s.paused.by ? " · " + s.paused.by : ""} · no new task goes to a lane; work in flight goes on</p>
          {:else}
            <h2>Dispatch is running</h2>
            <p class="muted small">queued tasks go to free lanes as they are ready</p>
          {/if}
        </div>
      </div>
      {#if s.paused}
        <button class="btn primary resume" disabled={busy} onclick={() => pause("", true, "", "")}>Resume now</button>
      {:else}
        <form class="pform" onsubmit={(e) => { e.preventDefault(); pause("", false, reason, pauseFor); }}>
          <label class="small muted">Reason <input bind:value={reason} required placeholder="e.g. quota resets at 18:00" disabled={busy} /></label>
          <Segmented label="Pause for" options={DURATIONS} bind:value={pauseFor} disabled={busy} />
          <button class="btn" disabled={busy || !reason.trim()} type="submit">Pause dispatch</button>
        </form>
      {/if}
      {#if pauseErr}<p class="err small">{pauseErr}</p>{/if}
    </section>

    <!-- routing -->
    <Section title="Routing" sub="each difficulty tries its models in order; the first not paused runs the task on a free lane">
      <div class="card">
        {#each s.difficulty as d (d.level)}
          {@const list = chain(d.level)}
          {@const key = "difficulty." + d.level}
          {@const p = pick[d.level] || { m: "", e: "" }}
          <div class="lvl" class:changed={isStaged(key)}>
            <div class="lname"><strong>{d.level}</strong><span class="muted small">{recent[d.level] || 0} dispatches · 7 d</span></div>
            <div class="chain">
              {#each list as ref, i (ref)}
                {@const m = model(s, splitRef(ref)[0])}
                {#if i}<span class="then">then</span>{/if}
                <span class="ref" class:paused={!!m?.paused} title={m?.paused ? "paused: " + m.paused.reason : ""}>
                  <span class="mono">{chip(ref)}</span>
                  {#if i === 0}<span class="def">default</span>{/if}
                  <span class="small muted">{m?.kind || "?"}</span>
                  {#if m?.paused}<Dot state="waiting" title="paused" />{/if}
                  <button class="icon" aria-label="Earlier" title="earlier" disabled={busy || i === 0} onclick={() => setChain(d.level, move(list, i, -1))}>‹</button>
                  <button class="icon" aria-label="Later" title="later" disabled={busy || i === list.length - 1} onclick={() => setChain(d.level, move(list, i, 1))}>›</button>
                  <button class="icon" aria-label="Remove {ref}" title="remove" disabled={busy} onclick={() => setChain(d.level, list.filter((_, j) => j !== i))}>×</button>
                </span>
              {:else}<span class="muted small">no model: tasks of this level wait</span>{/each}
              <span class="addref">
                <select class="sel add" aria-label="Model to add to {d.level}" value={p.m} disabled={busy}
                  onchange={(e) => (pick = { ...pick, [d.level]: { m: e.currentTarget.value, e: "" } })}>
                  <option value="">+ Add model</option>
                  {#each s.models as m (m.name)}<option value={m.name}>{m.name} · {m.kind}</option>{/each}
                </select>
                {#if p.m}
                  <select class="sel" aria-label="Effort of the model to add to {d.level}" value={p.e} disabled={busy || !efforts(s, p.m).length}
                    onchange={(e) => (pick = { ...pick, [d.level]: { m: p.m, e: e.currentTarget.value } })}>
                    <option value="">kind's effort</option>
                    {#each efforts(s, p.m) as e (e)}<option value={e}>{e}</option>{/each}
                  </select>
                  <button class="btn sm" disabled={busy || list.includes(joinRef(p.m, p.e))} onclick={() => add(d.level)}
                    title={list.includes(joinRef(p.m, p.e)) ? "already in this level" : ""}>Add</button>
                {/if}
              </span>
            </div>
          </div>
        {/each}
      </div>
    </Section>

    <div class="cols">
      <div class="wide">
        <Section title="Models" sub="the models in use; a paused one is skipped and its level's next one runs">
          <div class="card tw">
            <table>
              <thead><tr><th>Model</th><th>Kind</th><th>Model id</th><th>Used by</th><th>Now</th></tr></thead>
              <tbody>
                {#each models as m (m.name)}
                  <tr class:mpaused={!!m.paused}>
                    <td class="mono"><strong>{m.name}</strong>{#if !m.builtin}<span class="small muted"> · project's</span>{/if}</td>
                    <td><span class="kind k-{m.kind}">{m.kind}</span></td>
                    <td class="mono small">{m.model || "kind's choice"}</td>
                    <td class="small">{(used[m.name] || []).join(", ") || "—"}</td>
                    <td>
                      {#if m.paused}
                        <div class="mstate">
                          <Pill state="waiting" text="paused" title={m.paused.by ? "by " + m.paused.by : undefined} />
                          <span class="small muted">{m.paused.reason}{#if m.paused.until} · ends <Rel at={m.paused.until} mode="in" />{/if}</span>
                          <button class="btn sm" disabled={busy} onclick={() => pause(m.name, true, "", "")}>Clear</button>
                        </div>
                      {:else if pausing === m.name}
                        <form class="mform" aria-label="Pause {m.name}" onsubmit={(e) => { e.preventDefault(); pause(m.name, false, mReason, mFor); }}>
                          <input bind:value={mReason} required placeholder="reason" aria-label="Reason to pause {m.name}" disabled={busy} />
                          <Segmented label="Pause {m.name} for" options={DURATIONS} bind:value={mFor} disabled={busy} compact />
                          <button class="btn sm" type="submit" disabled={busy || !mReason.trim()}>Pause</button>
                          <button class="btn ghost sm" type="button" disabled={busy} onclick={() => (pausing = "")}>Cancel</button>
                        </form>
                      {:else}
                        <div class="mstate">
                          <Pill state="idle" text="running" dot={false} />
                          <button class="btn ghost sm" disabled={busy} onclick={() => { pausing = m.name; mReason = ""; mFor = "1h"; }}>Pause…</button>
                        </div>
                      {/if}
                      {#if mErr[m.name]}<p class="err small">{mErr[m.name]}</p>{/if}
                    </td>
                  </tr>
                {:else}<tr><td colspan="5" class="muted small">no level or role names a model</td></tr>{/each}
              </tbody>
            </table>
          </div>
        </Section>

        <Section title="Lanes" sub="every lane runs any model; a lane is removed from the end">
          {#snippet actions()}
            <div class="stepper" class:st={isStaged("lanes")}>
              <button class="btn sm" aria-label="Fewer lanes" disabled={busy || lanes <= 0} onclick={() => set("lanes", Math.max(0, lanes - 1))}>−</button>
              <span class="mono num">{lanes} lane{lanes === 1 ? "" : "s"}</span>
              <button class="btn sm" aria-label="More lanes" disabled={busy} onclick={() => set("lanes", lanes + 1)}>+</button>
            </div>
          {/snippet}
          {#if dropsBusy.length}
            <p class="warn small"><Dot state="waiting" /> {dropsBusy.join(", ")} {dropsBusy.length > 1 ? "have" : "has"} a task: serve refuses fewer lanes until {dropsBusy.length > 1 ? "they are" : "it is"} free.</p>
          {/if}
          <div class="card tw">
            <table>
              <thead><tr><th>Lane</th><th>Runs</th><th>Agent</th><th>Now</th></tr></thead>
              <tbody>
                {#each s.lanes as l, i (l.lane)}
                  <tr class:dropped={i >= lanes}>
                    <td class="mono"><strong>{l.lane}</strong>{#if i >= lanes}<span class="small muted"> · removed</span>{/if}</td>
                    <td class="mono small">{l.type || "—"}</td>
                    <td><AgentLink {project} agent={l.agent} /></td>
                    <td>{#if l.state}<Pill state={l.state} />{:else}<Pill state="idle" text="no state" />{/if}</td>
                  </tr>
                {/each}
                {#each Array.from({ length: Math.max(0, lanes - s.lanes.length) }, (_, j) => s.lanes.length + j + 1) as n (n)}
                  <tr class="added"><td class="mono"><strong>L-{String(n).padStart(2, "0")}</strong><span class="small muted"> · new</span></td><td></td><td></td><td></td></tr>
                {/each}
              </tbody>
            </table>
          </div>
        </Section>
      </div>
      <div class="narrow">
        <Section title="Roles" sub="a role's model applies at its next start">
          <div class="card tw">
            <table>
              <thead><tr><th>Role</th><th>Model</th><th>Effort</th></tr></thead>
              <tbody>
                {#each s.roles as r (r.role)}
                  {@const k = "roles." + r.role}
                  {@const [rm, re] = splitRef((val(k) as string) || "")}
                  <tr>
                    <td><span class="rname"><Dot state={r.status || "idle"} title={r.status || "no pane"} /> {roleName(r.role)}</span><span class="small"><AgentLink {project} agent={r.agent} /></span></td>
                    <td>
                      <select class="sel" class:st={isStaged(k)} aria-label="Model of {roleName(r.role)}" value={rm} disabled={busy} onchange={(e) => setRole(r.role, e.currentTarget.value, re)}>
                        {#if !r.optional}<option value="">role's default</option>{/if}
                        {#each names as n (n)}<option value={n}>{n}</option>{/each}
                      </select>
                    </td>
                    <td>
                      <select class="sel" class:st={isStaged(k)} aria-label="Effort of {roleName(r.role)}" value={re} disabled={busy || !rm || !efforts(s, rm).length} onchange={(e) => setRole(r.role, rm, e.currentTarget.value)}>
                        <option value="">kind's</option>
                        {#each efforts(s, rm) as e (e)}<option value={e}>{e}</option>{/each}
                      </select>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </Section>

        <Section title="Kinds" sub="what each agent kind's starts get (obor setup or a hand edit sets it)">
          <div class="card tw">
            <table>
              <thead><tr><th>Kind</th><th>Args</th><th>Prompt extra</th></tr></thead>
              <tbody>
                {#each s.kinds as k (k.name)}
                  <tr>
                    <td><span class="kind k-{k.name}">{k.name}</span></td>
                    <td class="mono small">{k.args.length ? k.args.join(" ") : "—"}</td>
                    <td class="small">{k.prompt_extra ? "set" : "—"}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </Section>
      </div>
    </div>

    <Section title="Distribution" sub="dispatches by difficulty and the model ref that ran them">
      {#snippet actions()}<Segmented label="Range" options={["7 days", "all"]} bind:value={distRange} />{/snippet}
      <div class="card heat">
        {#if dist.rows.length}
          <div class="hgrid" style="grid-template-columns: 110px repeat({dist.cols.length}, minmax(90px, 1fr)) 70px">
            <span></span>{#each dist.cols as c (c)}<span class="mono small muted">{c ? chip(c) : "unknown"}</span>{/each}<span class="small muted r">total</span>
            {#each dist.rows as l (l)}
              <span class="small">{l || "no level"}</span>
              {#each dist.cols as c (c)}
                {@const n = dist.n(l, c)}
                <span class="cell" style="background: {heat(n, dist.max)}" class:zero={!n} class:strong={n / dist.max > 0.55}>{n}</span>
              {/each}
              <span class="r"><strong>{dist.cols.reduce((a, c) => a + dist.n(l, c), 0)}</strong></span>
            {/each}
          </div>
        {:else}<p class="muted small" style="margin: 0">No dispatch has placed a task on a lane yet.</p>{/if}
      </div>
    </Section>
  {/if}
</div>

{#if count && s}
  <div class="dock" role="region" aria-label="Unsaved changes">
    <div class="bar card">
      {#if reviewing}
        <div class="review">
          <h2 class="lbl">{count} change{count > 1 ? "s" : ""} to commit</h2>
          <ul>
            {#each review as r (r.key)}
              <li><span class="mono key">{r.key}</span><span class="from">{r.from}</span><span class="arrow">→</span><span class="to">{r.to}</span>
                <button class="btn ghost sm" disabled={busy} onclick={() => { const n = { ...staged }; delete n[r.key]; staged = n; if (!Object.keys(n).length) discard(); }}>undo</button></li>
            {/each}
          </ul>
        </div>
      {/if}
      {#if moved}
        <p class="warn small"><Dot state="waiting" /> The setup changed since you started editing (another tab, the lead or a hand edit). Review your changes against it before you commit.</p>
      {/if}
      {#if saveErr}<p class="err small" role="alert">{saveErr}</p>{/if}
      <div class="row">
        <Dot tone="accent" />
        <strong>{count} unsaved change{count > 1 ? "s" : ""}</strong>
        <span class="muted small hide-sm">serve checks them together before it commits</span>
        <span class="grow"></span>
        <button class="btn ghost" disabled={busy} onclick={discard}>Discard</button>
        {#if moved}
          <button class="btn" disabled={busy} onclick={rebase}>Review against the new setup</button>
        {:else if !reviewing}
          <button class="btn" disabled={busy} onclick={() => (reviewing = true)}>Review</button>
          <button class="btn primary" disabled={busy} onclick={commit}>Commit to main</button>
        {:else}
          <button class="btn" disabled={busy} onclick={() => (reviewing = false)}>Hide</button>
          <button class="btn primary" disabled={busy} onclick={commit}>{busy ? "Committing…" : "Commit to main"}</button>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .err { color: var(--bad); margin: 0; }
  .dispatch { padding: 16px 18px; display: flex; flex-wrap: wrap; align-items: center; gap: 14px; }
  .dispatch.paused { border-color: color-mix(in srgb, var(--wait) 50%, var(--line)); }
  .dhead { display: flex; align-items: center; gap: 12px; min-width: 260px; flex: 1; }
  .dhead h2 { margin: 0; font-size: 15px; }
  .dhead p { margin: 2px 0 0; }
  .dicon { width: 40px; height: 40px; border-radius: 10px; display: flex; align-items: center; justify-content: center; background: color-mix(in srgb, var(--ok) 14%, transparent); flex: none; }
  .paused .dicon { background: color-mix(in srgb, var(--wait) 14%, transparent); }
  .pform { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
  .pform label { display: flex; align-items: center; gap: 8px; }
  .pform input, .mform input { height: 32px; width: 240px; max-width: 60vw; padding: 0 10px; border-radius: 8px; border: 1px solid var(--line-strong); background: var(--sunk); color: var(--fg); }
  .mform input { width: 160px; height: 28px; }
  .resume { margin-left: auto; }
  .dispatch .err { flex-basis: 100%; }

  .sel {
    height: 32px; padding: 0 28px 0 10px; border-radius: 8px; border: 1px solid var(--line-strong); background-color: var(--surface-2);
    color: var(--fg); font: 13px var(--mono); max-width: 100%; cursor: pointer;
  }
  .sel:disabled { opacity: 0.55; cursor: default; }
  .sel.st, .stepper.st .num { border-color: var(--accent); box-shadow: 0 0 0 1px var(--accent); }
  .stepper.st .num { border-radius: 6px; }

  .lvl { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 14px 18px; border-bottom: 1px solid var(--line-soft); }
  .lvl:last-child { border-bottom: 0; }
  .lvl.changed { box-shadow: inset 3px 0 0 var(--accent); }
  .lname { width: 120px; display: flex; flex-direction: column; gap: 2px; }
  .chain { flex: 1; display: flex; flex-wrap: wrap; align-items: center; gap: 6px; min-width: 260px; }
  .then { font-size: 12px; color: var(--muted); }
  .ref { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 4px 0 9px; border-radius: 8px; background: var(--surface-2); border: 1px solid var(--line-strong); }
  .ref.paused { border-style: dashed; opacity: 0.8; }
  .def { font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; padding: 1px 6px; border-radius: 999px; color: var(--accent); background: color-mix(in srgb, var(--accent) var(--tint), transparent); }
  .icon { width: 22px; height: 22px; border: 0; border-radius: 5px; background: transparent; color: var(--muted); cursor: pointer; font-size: 14px; line-height: 1; }
  .icon:hover:not(:disabled) { background: var(--surface-3); color: var(--fg); }
  .icon:disabled { opacity: 0.35; cursor: default; }
  .addref { display: inline-flex; flex-wrap: wrap; align-items: center; gap: 6px; }
  .add { font-family: var(--sans); color: var(--muted); }

  .kind { font-size: 11px; font-weight: 600; padding: 2px 8px; border-radius: 999px; color: var(--c); background: color-mix(in srgb, var(--c) var(--tint), transparent); }
  .k-claude { --c: var(--accent); } .k-codex { --c: var(--info); } .k-opencode { --c: var(--done); }
  .mstate { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
  .mform { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
  tr.mpaused td:first-child { box-shadow: inset 3px 0 0 var(--wait); }
  tr.dropped td { opacity: 0.5; text-decoration: line-through; }
  tr.added td { color: var(--accent); }
  .stepper { display: flex; align-items: center; gap: 6px; }
  .stepper .btn { width: 30px; padding: 0; }
  .num { min-width: 64px; text-align: center; color: var(--fg); font-size: 13px; padding: 4px 6px; }

  .tw { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th { text-align: left; font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; color: var(--muted); font-weight: 600; padding: 10px 12px; }
  td { padding: 8px 12px; border-top: 1px solid var(--line-soft); vertical-align: middle; }
  th:first-child, td:first-child { padding-left: 16px; }
  .rname { display: flex; align-items: center; gap: 8px; }

  .heat { padding: 16px 18px; overflow-x: auto; }
  .hgrid { display: grid; gap: 6px; align-items: center; min-width: 480px; }
  .cell { height: 38px; border-radius: 6px; display: flex; align-items: center; padding: 0 12px; font-weight: 600; color: var(--fg); }
  .cell.zero { color: var(--faint); font-weight: 400; }
  .cell.strong { color: var(--accent-fg); }
  .r { text-align: right; }

  .dock { position: sticky; bottom: 16px; display: flex; justify-content: center; padding: 0 16px; z-index: 6; pointer-events: none; }
  .bar { pointer-events: auto; width: min(860px, 100%); padding: 12px 14px; box-shadow: var(--shadow); border-color: var(--line-strong); background: var(--surface-2); display: flex; flex-direction: column; gap: 10px; }
  .row { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
  .grow { flex: 1; }
  .review ul { list-style: none; margin: 8px 0 0; padding: 0; display: flex; flex-direction: column; gap: 4px; max-height: 40vh; overflow: auto; }
  .review li { display: grid; grid-template-columns: minmax(0, 1.3fr) minmax(0, 1fr) auto minmax(0, 1fr) auto; gap: 10px; align-items: center; font-size: 13px; padding: 4px 0; border-bottom: 1px solid var(--line-soft); }
  .key { font-size: 12px; overflow-wrap: anywhere; }
  .from { color: var(--muted); text-decoration: line-through; overflow-wrap: anywhere; }
  .arrow { color: var(--muted); }
  .to { color: var(--fg); font-weight: 500; overflow-wrap: anywhere; }
  .warn { margin: 0; display: flex; align-items: center; gap: 8px; color: var(--wait); }
  @media (max-width: 700px) { .hide-sm { display: none; } .review li { grid-template-columns: 1fr; gap: 2px; } }
</style>
