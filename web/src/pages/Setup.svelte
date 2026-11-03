<script lang="ts">
  // The Setup page (T-750, T-753, T-760): what steers the project's work — its pause, which lane types each
  // difficulty tries, what each lane type runs, the lanes' and roles' types — and how its dispatches spread. Edits are
  // staged and committed together as one commit of .obor/config.json on main; the pause acts at once.
  import { onDestroy } from "svelte";
  import { Live } from "../lib/live.svelte";
  import { api, post } from "../lib/api";
  import { roleName } from "../lib/format";
  import { LOCKED, body, current, joinRef, models, move, show, splitRef, stage, value, type Staged, type Value } from "../lib/setup";
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

  // ---- pause (at once, not staged) ----
  let reason = $state("");
  let pauseFor = $state("1h");
  let pauseErr = $state("");
  async function pause(clear: boolean) {
    if (busy) return;
    busy = true; pauseErr = "";
    try {
      await post(`/api/pause?project=${encodeURIComponent(project)}`,
        clear ? { clear: true } : { reason: reason.trim(), for: pauseFor === "until cleared" ? "" : pauseFor });
      reason = "";
    } catch (e) { pauseErr = (e as Error).message; }
    finally {
      try { await live.read(); } catch { /* marked */ }
      busy = false;
    }
  }

  // ---- what the sections draw ----
  const typeNames = $derived((s?.lane_types || []).map((t) => t.name));
  const kindOf = $derived(Object.fromEntries((s?.lane_types || []).map((t) => [t.name, t.kind])));
  const effortsOf = $derived(Object.fromEntries((s?.lane_types || []).map((t) => [t.name, t.efforts || []])));
  const usedBy = $derived.by(() => {
    const u: Record<string, string[]> = {};
    if (!s) return u;
    const add = (t: string, who: string) => (u[t] = [...(u[t] || []), who]);
    for (const d of s.difficulty) for (const ref of (val("difficulty." + d.level) as string[]) || []) add(splitRef(ref)[0], d.level);
    for (const r of s.roles) { const ref = val(`roles.${r.role}.type`) as string; if (ref) add(splitRef(ref)[0], roleName(r.role)); }
    for (const l of s.lanes) { const t = val("lanes." + l.lane) as string; if (t) add(t, l.lane); }
    return u;
  });
  const laneTypes = $derived([...(s?.lane_types || [])].sort((a, b) => Number(!usedBy[a.name]) - Number(!usedBy[b.name])));
  const recent = $derived.by(() => {
    const r: Record<string, number> = {};
    for (const c of s?.distribution || []) r[c.level] = (r[c.level] || 0) + c.recent;
    return r;
  });

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

  // a lane type whose model is typed in (Other…)
  const OTHER = "\u0000other";
  let typing = $state<Record<string, boolean>>({});

  // level chain edits
  const chain = (level: string) => ((val("difficulty." + level) as string[]) || []);
  const setChain = (level: string, list: string[]) => set("difficulty." + level, list);
</script>

<div class="page">
  <div class="page-head">
    <h1>Setup</h1>
    <span class="muted small">what steers {project}'s work · edits land as one commit of <code>.obor/config.json</code> on main</span>
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
        <button class="btn primary resume" disabled={busy} onclick={() => pause(true)}>Resume now</button>
      {:else}
        <form class="pform" onsubmit={(e) => { e.preventDefault(); pause(false); }}>
          <label class="small muted">Reason <input bind:value={reason} required placeholder="e.g. quota resets at 18:00" disabled={busy} /></label>
          <Segmented label="Pause for" options={["30m", "1h", "3h", "until cleared"]} bind:value={pauseFor} disabled={busy} />
          <button class="btn" disabled={busy || !reason.trim()} type="submit">Pause dispatch</button>
        </form>
      {/if}
      {#if pauseErr}<p class="err small">{pauseErr}</p>{/if}
    </section>

    <!-- routing -->
    <Section title="Routing" sub="each difficulty tries its lane types in order; the first with a free lane under its max takes the task">
      <div class="card">
        {#each s.difficulty as d (d.level)}
          {@const list = chain(d.level)}
          {@const key = "difficulty." + d.level}
          <div class="lvl" class:changed={isStaged(key)}>
            <div class="lname"><strong>{d.level}</strong><span class="muted small">{recent[d.level] || 0} dispatches · 7 d</span></div>
            <div class="chain">
              {#each list as ref, i (ref + i)}
                {#if i}<span class="then">then</span>{/if}
                <span class="ref">
                  <span class="mono small muted">{i + 1}</span>
                  <span class="mono">{ref}</span>
                  <span class="small muted">{kindOf[splitRef(ref)[0]] || "?"}</span>
                  <button class="icon" aria-label="Earlier" title="earlier" disabled={busy || i === 0} onclick={() => setChain(d.level, move(list, i, -1))}>‹</button>
                  <button class="icon" aria-label="Later" title="later" disabled={busy || i === list.length - 1} onclick={() => setChain(d.level, move(list, i, 1))}>›</button>
                  <button class="icon" aria-label="Remove {ref}" title="remove" disabled={busy} onclick={() => setChain(d.level, list.filter((_, j) => j !== i))}>×</button>
                </span>
              {:else}<span class="muted small">no lane type: tasks of this level wait</span>{/each}
              <select class="sel add" aria-label="Add a lane type to {d.level}" disabled={busy}
                onchange={(e) => { const v = e.currentTarget.value; e.currentTarget.value = ""; if (v) setChain(d.level, [...list, v]); }}>
                <option value="">+ Add lane type</option>
                {#each typeNames.filter((n) => !list.some((r) => splitRef(r)[0] === n)) as n (n)}<option value={n}>{n} · {kindOf[n]}</option>{/each}
              </select>
            </div>
          </div>
        {/each}
      </div>
    </Section>

    <!-- lane types -->
    <Section title="Lane types" sub="what an agent of each type runs">
      <div class="types">
        {#each laneTypes as t (t.name)}
          {@const mk = `lane_types.${t.name}.model`}
          {@const ek = `lane_types.${t.name}.effort`}
          {@const xk = `lane_types.${t.name}.max`}
          {@const max = val(xk) as number}
          {@const model = val(mk) as string}
          <article class="card ltype" class:unused={!usedBy[t.name]} class:changed={isStaged(mk) || isStaged(ek) || isStaged(xk)}>
            <header>
              <span class="mono tname">{t.name}</span>
              <span class="kind k-{t.kind}">{t.kind}</span>
              <span class="used small">{usedBy[t.name] ? "used by " + usedBy[t.name].join(", ") : "unused"}</span>
            </header>
            <label class="field"><span>Model</span>
              {#if typing[t.name]}
                <input class="sel txt" value={model} placeholder="a model id" disabled={busy}
                  onkeydown={(e) => { if (e.key === "Enter") { set(mk, e.currentTarget.value.trim()); typing[t.name] = false; } if (e.key === "Escape") typing[t.name] = false; }}
                  onblur={(e) => { set(mk, e.currentTarget.value.trim()); typing[t.name] = false; }} />
              {:else}
                <select class="sel" class:st={isStaged(mk)} value={model} disabled={busy} onchange={(e) => {
                  const v = e.currentTarget.value;
                  if (v === OTHER) { e.currentTarget.value = model; typing[t.name] = true; } else set(mk, v);
                }}>
                  <option value="">{t.kind === "opencode" ? "the agent's default" : "kind default"}</option>
                  {#each models(s, t.kind, model) as m (m)}<option value={m}>{m}</option>{/each}
                  <option value={OTHER}>Other…</option>
                </select>
              {/if}
            </label>
            <div class="field"><span>Effort</span>
              {#if t.efforts?.length}
                <Segmented label="Effort of {t.name}" options={[["", "—"], ...t.efforts.map((e): [string, string] => [e, e])]}
                  value={val(ek) as string} disabled={busy} compact onchange={(v) => set(ek, v)} />
              {:else}<span class="muted small">set by the {t.kind} agent, or a type reference's :effort</span>{/if}
            </div>
            <div class="field"><span>Max</span>
              <div class="stepper">
                <button class="btn sm" aria-label="Fewer" disabled={busy || !max} onclick={() => set(xk, Math.max(0, max - 1))}>−</button>
                <span class="mono num" class:st={isStaged(xk)}>{max ? max : "no cap"}</span>
                <button class="btn sm" aria-label="More" disabled={busy} onclick={() => set(xk, max + 1)}>+</button>
                <span class="muted small">agents at once</span>
              </div>
            </div>
          </article>
        {/each}
      </div>
    </Section>

    <div class="cols">
      <div class="wide">
        <Section title="Lanes" sub="a pooled lane runs any type">
          <div class="card tw">
            <table>
              <thead><tr><th>Lane</th><th>Runs</th><th>Agent</th><th>Now</th></tr></thead>
              <tbody>
                {#each s.lanes as l (l.lane)}
                  {@const k = "lanes." + l.lane}
                  {@const locked = LOCKED.has(l.state)}
                  <tr>
                    <td class="mono"><strong>{l.lane}</strong></td>
                    <td>
                      <select class="sel" class:st={isStaged(k)} value={val(k) as string} disabled={busy || locked}
                        title={locked ? "the lane has a task: change its type once it is free" : ""}
                        onchange={(e) => set(k, e.currentTarget.value)}>
                        <option value="">pooled (any type)</option>
                        {#each typeNames as n (n)}<option value={n}>{n}</option>{/each}
                      </select>
                    </td>
                    <td><AgentLink {project} agent={l.agent} /></td>
                    <td>{#if l.state}<Pill state={l.state} />{:else}<Pill state="idle" text="no state" />{/if}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </Section>
      </div>
      <div class="narrow">
        <Section title="Roles" sub="a role's type applies at its next start">
          <div class="card tw">
            <table>
              <thead><tr><th>Role</th><th>Type</th><th>Effort</th></tr></thead>
              <tbody>
                {#each s.roles as r (r.role)}
                  {@const k = `roles.${r.role}.type`}
                  {@const [rt, re] = splitRef((val(k) as string) || "")}
                  <tr>
                    <td><span class="rname"><Dot state={r.status || "idle"} title={r.status || "no pane"} /> {roleName(r.role)}</span><span class="small"><AgentLink {project} agent={r.agent} /></span></td>
                    <td>
                      <select class="sel" class:st={isStaged(k)} value={rt} disabled={busy} onchange={(e) => set(k, joinRef(e.currentTarget.value, re))}>
                        <option value="">role's default</option>
                        {#each typeNames as n (n)}<option value={n}>{n}</option>{/each}
                      </select>
                    </td>
                    <td>
                      <select class="sel" class:st={isStaged(k)} value={re} disabled={busy || !rt || !(effortsOf[rt] || []).length} onchange={(e) => set(k, joinRef(rt, e.currentTarget.value))}>
                        <option value="">type's</option>
                        {#each effortsOf[rt] || [] as e (e)}<option value={e}>{e}</option>{/each}
                      </select>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </Section>
      </div>
    </div>

    <Section title="Distribution" sub="dispatches by difficulty and the type that ran them">
      {#snippet actions()}<Segmented label="Range" options={["7 days", "all"]} bind:value={distRange} />{/snippet}
      <div class="card heat">
        {#if dist.rows.length}
          <div class="hgrid" style="grid-template-columns: 110px repeat({dist.cols.length}, minmax(90px, 1fr)) 70px">
            <span></span>{#each dist.cols as c (c)}<span class="mono small muted">{c || "not recorded"}</span>{/each}<span class="small muted r">total</span>
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
  .pform input { height: 32px; width: 240px; max-width: 60vw; padding: 0 10px; border-radius: 8px; border: 1px solid var(--line-strong); background: var(--sunk); color: var(--fg); }
  .resume { margin-left: auto; }
  .dispatch .err { flex-basis: 100%; }

  .sel {
    height: 32px; padding: 0 28px 0 10px; border-radius: 8px; border: 1px solid var(--line-strong); background-color: var(--surface-2);
    color: var(--fg); font: 13px var(--mono); max-width: 100%; cursor: pointer;
  }
  .sel:disabled { opacity: 0.55; cursor: default; }
  .sel.txt { padding-right: 10px; cursor: text; background: var(--sunk); }
  .sel.st, .num.st { border-color: var(--accent); box-shadow: 0 0 0 1px var(--accent); }
  .num.st { border-radius: 6px; }

  .lvl { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; padding: 14px 18px; border-bottom: 1px solid var(--line-soft); }
  .lvl:last-child { border-bottom: 0; }
  .lvl.changed { box-shadow: inset 3px 0 0 var(--accent); }
  .lname { width: 120px; display: flex; flex-direction: column; gap: 2px; }
  .chain { flex: 1; display: flex; flex-wrap: wrap; align-items: center; gap: 6px; min-width: 260px; }
  .then { font-size: 12px; color: var(--muted); }
  .ref { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 4px 0 9px; border-radius: 8px; background: var(--surface-2); border: 1px solid var(--line-strong); }
  .icon { width: 22px; height: 22px; border: 0; border-radius: 5px; background: transparent; color: var(--muted); cursor: pointer; font-size: 14px; line-height: 1; }
  .icon:hover:not(:disabled) { background: var(--surface-3); color: var(--fg); }
  .icon:disabled { opacity: 0.35; cursor: default; }
  .add { font-family: var(--sans); color: var(--muted); }

  .types { display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr)); gap: 12px; }
  .ltype { padding: 16px; display: flex; flex-direction: column; gap: 12px; }
  .ltype.unused { opacity: 0.72; }
  .ltype.unused:hover, .ltype.unused:focus-within { opacity: 1; }
  .ltype.changed { border-color: color-mix(in srgb, var(--accent) 60%, var(--line)); }
  .ltype header { display: flex; align-items: center; gap: 8px; }
  .tname { font-size: 15px; font-weight: 600; }
  .kind { font-size: 11px; font-weight: 600; padding: 2px 8px; border-radius: 999px; color: var(--c); background: color-mix(in srgb, var(--c) var(--tint), transparent); }
  .k-claude { --c: var(--accent); } .k-codex { --c: var(--info); } .k-opencode { --c: var(--done); }
  .used { margin-left: auto; color: var(--fg-2); text-align: right; }
  .unused .used { color: var(--muted); }
  .field { display: grid; grid-template-columns: 52px minmax(0, 1fr); align-items: center; gap: 8px; font-size: 12px; color: var(--muted); }
  .field :global(.seg) { width: 100%; }
  .field .sel { width: 100%; }
  .stepper { display: flex; align-items: center; gap: 6px; }
  .stepper .btn { width: 30px; padding: 0; }
  .num { min-width: 64px; text-align: center; color: var(--fg); font-size: 13px; padding: 4px 0; }

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
