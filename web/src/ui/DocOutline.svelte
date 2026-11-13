<script lang="ts">
  // DocOutline is a doc's "On this page": its level 2–3 headings (### indented), the one the reader is in marked
  // (aria-current="location"), a click scrolling to it and telling the page the section picked (onsection, the URL's
  // section link). compact is the Sections sheet's style (44 px rows). It tracks the heading in view with one
  // IntersectionObserver over the rendered headings (their ids are md's; a scroll measures again too), and renders
  // nothing for fewer than two headings.
  import { tick } from "svelte";
  import { currentHeading } from "../lib/docs";
  import type { Heading } from "../lib/md";

  let { headings, compact = false, onpick = undefined, onsection = undefined }:
    { headings: Heading[]; compact?: boolean; onpick?: (id: string) => void; onsection?: (id: string) => void } = $props();

  let cur = $state("");
  const plain = (s: string) => s.replace(/[`*]/g, "");

  // line is where a heading becomes the current one: 40% down the viewport.
  const LINE = 0.4;

  function measure() {
    const tops = headings
      .map((h) => ({ id: h.id, el: document.getElementById(h.id) }))
      .filter((t): t is { id: string; el: HTMLElement } => !!t.el)
      .map((t) => ({ id: t.id, top: t.el.getBoundingClientRect().top }));
    cur = currentHeading(tops, window.innerHeight * LINE);
  }

  $effect(() => {
    const hs = headings;
    if (hs.length < 2 || typeof IntersectionObserver === "undefined") return;
    let io: IntersectionObserver | null = null;
    let gone = false;
    // The band is the viewport's top 40%: a heading crossing its bottom edge is the moment the current one changes.
    void tick().then(() => {
      if (gone) return;
      io = new IntersectionObserver(measure, { rootMargin: `0px 0px -${100 - LINE * 100}% 0px` });
      for (const h of hs) {
        const el = document.getElementById(h.id);
        if (el) io.observe(el);
      }
      measure();
    });
    // A jump past several headings with none in the band before or after crosses nothing the observer sees: a scroll
    // measures again too, once a frame.
    let queued = false;
    const onscroll = () => {
      if (queued) return;
      queued = true;
      requestAnimationFrame(() => { queued = false; if (!gone) measure(); });
    };
    window.addEventListener("scroll", onscroll, { passive: true });
    return () => { gone = true; io?.disconnect(); window.removeEventListener("scroll", onscroll); };
  });

  function pick(id: string) {
    cur = id;
    document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" });
    onsection?.(id);
    onpick?.(id);
  }
</script>

{#if headings.length >= 2}
  <ul class="toc" class:compact>
    {#each headings as h (h.id)}
      <li>
        <button type="button" class:sub={h.level === 3} class:on={h.id === cur}
          aria-current={h.id === cur ? "location" : undefined} onclick={() => pick(h.id)}>{plain(h.text)}</button>
      </li>
    {/each}
  </ul>
{/if}

<style>
  .toc { list-style: none; margin: 0; padding: 0; border-left: 1px solid var(--line); }
  button {
    display: block; width: 100%; text-align: left; padding: 3px 10px; margin-left: -1px; border: 0;
    border-left: 2px solid transparent; background: transparent; color: var(--muted); font-size: 12px; line-height: 1.4;
    cursor: pointer; overflow-wrap: anywhere;
  }
  button.sub { padding-left: 22px; }
  button:hover { color: var(--fg); }
  button.on { color: var(--fg); border-left-color: var(--accent); font-weight: 500; }
  .compact { border-left: 0; display: flex; flex-direction: column; gap: 2px; }
  .compact button {
    display: flex; align-items: center; min-height: 44px; margin: 0; padding: 0 12px; border: 0; border-radius: 8px;
    color: var(--fg-2); font-size: 14px;
  }
  .compact button.sub { padding-left: 28px; }
  .compact button.on { background: var(--surface-3); color: var(--fg); box-shadow: inset 2px 0 0 var(--accent); }
</style>
