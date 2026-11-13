<script lang="ts">
  // The shell: the sidebar (the fleet's projects; the shown project's pages), the top bar (where you are, a jump box,
  // the live mark, the theme), and the routed page.
  import { onDestroy } from "svelte";
  import { router, go } from "./lib/router.svelte";
  import { href, pageKey } from "./lib/route";
  import { Live, mark } from "./lib/live.svelte";
  import { api } from "./lib/api";
  import type { ProjectsEntry } from "./lib/types";
  import Fleet from "./pages/Fleet.svelte";
  import Project from "./pages/Project.svelte";
  import Task from "./pages/Task.svelte";
  import Agent from "./pages/Agent.svelte";
  import Job from "./pages/Job.svelte";
  import Setup from "./pages/Setup.svelte";
  import Tasks from "./pages/Tasks.svelte";
  import Docs from "./pages/Docs.svelte";
  import Help from "./pages/Help.svelte";
  import Dot from "./ui/Dot.svelte";

  const route = $derived(router.route);

  // The projects list feeds the sidebar on every page (the fleet page reads its own, live).
  const projects = new Live<ProjectsEntry[]>((s) => api.projects(s));
  projects.start();
  onDestroy(() => projects.stop());

  const pstate = (p: ProjectsEntry) =>
    p.error ? "fail" : !p.up ? "down" : p.needs_you ? "needs-you" : p.busy ? "working" : "free";

  // crumbs is where you are, after the fleet.
  const crumbs = $derived.by(() => {
    const r = route;
    const c: { text: string; href?: string; mono?: boolean }[] = [];
    if (r.kind === "h") c.push({ text: "Help" });
    if (!r.project) return c;
    c.push({ text: r.project, href: r.kind === "p" ? undefined : href.project(r.project) });
    if (r.kind === "s") c.push({ text: "Setup" });
    if (r.kind === "tasks") c.push({ text: "Tasks" });
    if (r.kind === "docs") c.push({ text: "Docs", href: r.id ? href.docs(r.project) : undefined });
    if (r.kind === "docs" && r.id) c.push({ text: r.id, mono: true });
    if (r.kind === "t") c.push({ text: r.id, mono: true });
    if (r.kind === "a") c.push({ text: "agent " + r.id, mono: true });
    if (r.kind === "j") c.push({ text: "job " + r.id, mono: true });
    return c;
  });
  $effect(() => {
    const tail = crumbs.map((c) => c.text).reverse();
    document.title = [...tail, "obor"].join(" · ");
  });

  // The jump box: a task id goes to its page, a project's name to the project, anything else to the agent of that
  // name in the shown project.
  let jump = $state("");
  function onJump(e: SubmitEvent) {
    e.preventDefault();
    const q = jump.trim();
    if (!q) return;
    const list = projects.data || [];
    const proj = list.find((p) => p.name === q);
    const cur = route.project || (list.length === 1 ? list[0].name : "");
    if (proj) go(href.project(proj.name));
    else if (/^t-\d+$/i.test(q) && cur) go(href.task(cur, q.toUpperCase()));
    else if (cur) go(href.agent(cur, q));
    jump = "";
  }
  let jumpEl: HTMLInputElement | undefined = $state();
  function onKey(e: KeyboardEvent) {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") { e.preventDefault(); jumpEl?.focus(); }
  }

  // The theme: the system's until the toggle picks one (kept in localStorage "obor-theme").
  function toggleTheme() {
    const el = document.documentElement;
    const dark = matchMedia("(prefers-color-scheme: dark)").matches;
    let cur = el.getAttribute("data-theme") || (dark ? "dark" : "light");
    const next = cur === "dark" ? "light" : "dark";
    el.setAttribute("data-theme", next);
    try { localStorage.setItem("obor-theme", next); } catch { /* the choice lasts this page only */ }
  }
  let navOpen = $state(false);
</script>

<svelte:window onkeydown={onKey} />

<div class="shell">
  <nav class="side" class:open={navOpen} aria-label="obor">
    <div class="brand">
      <a href={href.fleet()} class="logo">
        <svg width="18" height="18" viewBox="0 0 18 18" aria-hidden="true"><rect x="4" y="4" width="10" height="10" rx="2" transform="rotate(45 9 9)" fill="var(--accent)"></rect></svg>
        <span>obor</span>
      </a>
      <button class="btn ghost sm menu" aria-label="Menu" aria-expanded={navOpen} onclick={() => (navOpen = !navOpen)}>
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true"><path d="M2 4h12M2 8h12M2 12h12"></path></svg>
      </button>
    </div>
    <div class="items">
      <a class="item" class:on={route.kind === ""} href={href.fleet()}>
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><rect x="2" y="2" width="5" height="5" rx="1"></rect><rect x="9" y="2" width="5" height="5" rx="1"></rect><rect x="2" y="9" width="5" height="5" rx="1"></rect><rect x="9" y="9" width="5" height="5" rx="1"></rect></svg>
        Fleet
      </a>
      <a class="item" class:on={route.kind === "h"} href={href.help()}>
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><circle cx="8" cy="8" r="6.2"></circle><path d="M6.2 6.2a1.9 1.9 0 1 1 2.6 1.75c-.5.2-.8.6-.8 1.1v.4"></path><circle cx="8" cy="11.6" r="0.4" fill="currentColor"></circle></svg>
        Help
      </a>
    </div>
    <div class="items">
      <h2 class="lbl group">Projects</h2>
      {#each projects.data || [] as p (p.name)}
        {@const here = route.project === p.name}
        <a class="item" class:on={here && route.kind === "p"} href={href.project(p.name)}>
          <Dot state={pstate(p)} title={pstate(p)} />
          <span class="ell">{p.name}</span>
          {#if p.needs_you}<span class="badge bad">{p.needs_you}</span>
          {:else}<span class="meta mono">{p.busy}/{p.busy + p.free}</span>{/if}
        </a>
        {#if here}
          <a class="item sub" class:on={route.kind === "s"} href={href.setup(p.name)}>
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><circle cx="8" cy="8" r="2.2"></circle><path d="M8 1.5v2M8 12.5v2M1.5 8h2M12.5 8h2M3.4 3.4l1.4 1.4M11.2 11.2l1.4 1.4M3.4 12.6l1.4-1.4M11.2 4.8l1.4-1.4"></path></svg>
            Setup
          </a>
          <a class="item sub" class:on={route.kind === "tasks"} href={href.tasks(p.name)}>
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><path d="M6 4h8M6 8h8M6 12h8"></path><circle cx="2.8" cy="4" r="0.9" fill="currentColor"></circle><circle cx="2.8" cy="8" r="0.9" fill="currentColor"></circle><circle cx="2.8" cy="12" r="0.9" fill="currentColor"></circle></svg>
            Tasks
          </a>
          <a class="item sub" class:on={route.kind === "docs"} href={href.docs(p.name)}>
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><path d="M4 1.8h5.2L12.5 5v9.2H4z"></path><path d="M9 1.8V5.2h3.5M6.2 8.2h4M6.2 11h4"></path></svg>
            Docs
          </a>
        {/if}
      {:else}
        {#if projects.error}<p class="note">{projects.error}</p>{/if}
      {/each}
    </div>
  </nav>

  <div class="main">
    <header class="top">
      <nav class="crumbs" aria-label="Breadcrumb">
        <a href={href.fleet()} class:here={!crumbs.length}>Fleet</a>
        {#each crumbs as c, i (i)}
          <span class="sep">/</span>
          {#if c.href}<a href={c.href} class:mono={c.mono}>{c.text}</a>{:else}<span class="here ell" class:mono={c.mono}>{c.text}</span>{/if}
        {/each}
      </nav>
      <form class="jump" onsubmit={onJump} role="search">
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true"><circle cx="7" cy="7" r="5"></circle><path d="M11 11l3 3"></path></svg>
        <input bind:this={jumpEl} bind:value={jump} aria-label="Jump to a task, agent or project" placeholder="Jump to T-…, an agent, a project" />
        <kbd>⌘K</kbd>
      </form>
      <span class="live {mark.state}" title={mark.state === "on" ? "live: new records and a refresh every few seconds update the page" : mark.text}>
        <span class="ld"></span>{mark.state === "off" ? "" : mark.text}
      </span>
      <button class="btn ghost sm" aria-label="Light or dark" onclick={toggleTheme}>
        <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="1.5"></circle><path d="M8 2a6 6 0 0 1 0 12z" fill="currentColor"></path></svg>
      </button>
    </header>

    <main>
      {#key pageKey(route, router.n)}
        {#if route.kind === ""}<Fleet />
        {:else if route.kind === "p"}<Project project={route.project} />
        {:else if route.kind === "s"}<Setup project={route.project} />
        {:else if route.kind === "tasks"}<Tasks project={route.project} />
        {:else if route.kind === "docs"}<Docs project={route.project} path={route.id} />
        {:else if route.kind === "t"}<Task project={route.project} id={route.id} />
        {:else if route.kind === "a"}<Agent project={route.project} id={route.id} />
        {:else if route.kind === "j"}<Job project={route.project} id={route.id} />
        {:else if route.kind === "h"}<Help />
        {/if}
      {/key}
    </main>
  </div>
</div>

<style>
  .shell { display: flex; min-height: 100vh; }
  .side {
    width: 240px; flex: none; background: var(--side); border-right: 1px solid var(--line-soft); padding: 14px 12px;
    display: flex; flex-direction: column; gap: 18px; position: sticky; top: 0; height: 100vh; overflow-y: auto;
  }
  .brand { display: flex; align-items: center; justify-content: space-between; padding: 2px 6px; }
  .logo { display: flex; align-items: center; gap: 10px; color: var(--fg); font-weight: 700; font-size: 15px; }
  .logo:hover { text-decoration: none; }
  .menu { display: none; }
  .items { display: flex; flex-direction: column; gap: 2px; }
  .group { padding: 0 10px 6px; }
  .item {
    display: flex; align-items: center; gap: 10px; height: 34px; padding: 0 10px; border-radius: 7px;
    color: var(--fg-2); font-size: 13px; min-width: 0;
  }
  .item:hover { background: var(--surface-2); color: var(--fg); text-decoration: none; }
  .item.on { background: var(--surface-3); color: var(--fg); font-weight: 500; }
  .item.sub { padding-left: 28px; height: 30px; }
  .meta { margin-left: auto; font-size: 11px; color: var(--muted); }
  .badge { margin-left: auto; font-size: 11px; font-weight: 600; padding: 1px 7px; border-radius: 999px; }
  .badge.bad { color: var(--bad); background: color-mix(in srgb, var(--bad) var(--tint), transparent); }
  .note { margin: 0; padding: 0 10px; font-size: 12px; color: var(--bad); }

  .main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
  .top {
    display: flex; flex-wrap: wrap; align-items: center; gap: 10px 14px; padding: 10px 28px;
    border-bottom: 1px solid var(--line-soft); position: sticky; top: 0; z-index: 5;
    background: color-mix(in srgb, var(--bg) 88%, transparent); backdrop-filter: blur(8px);
  }
  .crumbs { display: flex; align-items: center; gap: 8px; font-size: 13px; min-width: 0; flex: 1 1 200px; }
  .crumbs a { color: var(--muted); }
  .crumbs .here { color: var(--fg); font-weight: 600; }
  .sep { color: var(--faint); }
  .jump {
    display: flex; align-items: center; gap: 8px; height: 32px; padding: 0 10px; border-radius: 8px;
    background: var(--surface); border: 1px solid var(--line); color: var(--muted); width: 280px; max-width: 100%;
  }
  .jump input { flex: 1; min-width: 0; background: transparent; border: 0; outline: 0; color: var(--fg); font-size: 13px; }
  kbd { font: 11px var(--mono); padding: 1px 5px; border-radius: 4px; border: 1px solid var(--line-strong); }
  .live { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--muted); }
  .ld { width: 7px; height: 7px; border-radius: 50%; background: var(--faint); }
  .live.on .ld { background: var(--ok); }
  .live.stale { color: var(--wait); }
  .live.stale .ld { background: var(--wait); }
  main { flex: 1; }

  @media (max-width: 860px) {
    .shell { flex-direction: column; }
    .side { width: auto; height: auto; position: static; gap: 8px; padding: 10px 16px; }
    .menu { display: inline-flex; }
    .side:not(.open) .items { display: none; }
    .top { padding: 10px 16px; }
    .jump { width: 100%; }
  }
</style>
