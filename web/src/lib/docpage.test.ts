// The docs page's components as served (svelte/server): the index's tree, matches and marked doc, the outline.
import { describe, expect, it } from "vitest";
import { render } from "svelte/server";
import { TextSearch } from "./docsearch.svelte";
import type { DocHits, DocList } from "./types";
import DocIndex from "../ui/DocIndex.svelte";
import DocModes from "../ui/DocModes.svelte";
import DocOutline from "../ui/DocOutline.svelte";

const list: DocList = {
  head: "abc1234",
  pinned: [{ path: "BACKLOG.md", updated: 10 }],
  groups: [
    { dir: "docs", docs: [{ path: "docs/design.md", title: "obor-kit design (v2)", updated: 9 }, { path: "docs/staged.md" }] },
    { dir: "docs/research", docs: Array.from({ length: 13 }, (_, i) => ({ path: `docs/research/r${i}.md`, updated: i + 1 })) },
  ],
};
const text = (html: string) => html.replace(/<!--[^>]*-->/g, "");

describe("the doc index", () => {
  it("shows the tree with a big folder folded and the open doc marked, titles where present", () => {
    const html = text(render(DocIndex, { props: { project: "pj", list, path: "docs/design.md" } }).body);
    expect(html).toContain("All docs · 16");
    expect(html).toMatch(/aria-expanded="false"[^>]*>[\s\S]*?research\//);
    expect(html).not.toContain("r0.md");
    expect(html).toMatch(/aria-current="page"[^>]*>\s*<span[^>]*>obor-kit design \(v2\)</);
    expect(html).toContain("staged.md");
  });
  it("shows the flat matches with the filter typed, the first marked, and the no-match line", () => {
    const html = text(render(DocIndex, { props: { project: "pj", list, q: "r1" } }).body);
    expect(html).toContain("4 of 16"); // r1, r10, r11, r12
    expect(html.match(/class="match[ "]/g)?.length).toBe(4);
    expect(html).toMatch(/class="match[^"]*sel/);
    expect(html).toMatch(/<mark[^>]*>r1<\/mark>/);
    const none = text(render(DocIndex, { props: { project: "pj", list, q: "kubernetes" } }).body);
    expect(none).toContain("0 of 16");
    expect(none).toContain("Clear the filter");
  });
  it("unfolds a folder the folds open", () => {
    const html = text(render(DocIndex, { props: { project: "pj", list, part: "tree", folds: { "docs/research": true } } }).body);
    expect(html).toContain("r0.md");
    expect(html).toContain("newest");
  });
});

describe("the doc index searching the text", () => {
  const found: DocHits = {
    q: "pause",
    hits: [
      { path: "docs/design.md", line: 12, text: "obor pause stops dispatch" },
      { path: "BACKLOG.md", line: 3, text: "pause per model" },
      { path: "docs/design.md", line: 40, text: "a pause's reason" },
    ],
    truncated: false,
  };
  const search = (result: DocHits | null, busy = false, error = "") => {
    const s = new TextSearch(() => new Promise<DocHits>(() => {}));
    s.result = result;
    s.busy = busy;
    s.error = error;
    return s;
  };

  it("lists the path matches first, then the text hits grouped by doc with their line numbers and the words marked", () => {
    const html = text(render(DocIndex, { props: { project: "pj", list, q: "pause", mode: "text", search: search(found) } }).body);
    expect(html).toContain('aria-pressed="true">Search the text');
    expect(html).toContain("0 of 16 by name · 3 lines in 2 docs");
    expect(html.indexOf("In the text")).toBeGreaterThan(html.indexOf('aria-label="Matches"'));
    const groups = [...html.matchAll(/aria-label="Lines in ([^"]+)"/g)].map((m) => m[1]);
    expect(groups).toEqual(["docs/design.md", "BACKLOG.md"]);
    const design = html.slice(html.indexOf('Lines in docs/design.md'), html.indexOf('Lines in BACKLOG.md'));
    expect(design).toContain("obor-kit design (v2)");
    expect(design.match(/class="hit[ "]/g)?.length).toBe(2);
    expect(design).toMatch(/class="ln mono[^"]*">12</);
    expect(design).toMatch(/class="ln mono[^"]*">40</);
    expect(design).toMatch(/obor <mark[^>]*>pause<\/mark> stops dispatch/);
    expect(html).toMatch(/class="hit[^"]*\bsel"[^>]*title="docs\/design.md:12"/); // no path match: the first hit is marked
    expect(html).not.toContain("showing the first 200 lines");
    expect(html).not.toContain("Clear the filter");
  });
  it("says the hits are cut with truncated", () => {
    const html = text(render(DocIndex, {
      props: { project: "pj", list, q: "pause", mode: "text", search: search({ ...found, truncated: true }) },
    }).body);
    expect(html).toContain("showing the first 200 lines");
  });
  it("searches only the names in the path mode, and says it is searching before the first answer", () => {
    const names = text(render(DocIndex, { props: { project: "pj", list, q: "pause", search: search(found) } }).body);
    expect(names).toContain('aria-pressed="true">Names');
    expect(names).not.toContain("In the text");
    expect(names).toContain("No doc's path or title");
    const busy = text(render(DocIndex, { props: { project: "pj", list, q: "pause", mode: "text", search: search(null, true) } }).body);
    expect(busy).toContain("searching the text…");
    expect(busy).not.toContain("Clear the filter");
  });
  it("shows no mode toggle without a search", () => {
    const html = text(render(DocIndex, { props: { project: "pj", list, q: "r1", mode: "text" } }).body);
    expect(html).not.toContain("Search the text");
    expect(html).toContain("4 of 16");
  });
});

describe("the list page's text search", () => {
  const found: DocHits = { q: "pause", hits: [{ path: "docs/design.md", line: 12, text: "obor pause stops dispatch" }], truncated: false };
  const search = (result: DocHits | null, error = "") => {
    const s = new TextSearch(() => new Promise<DocHits>(() => {}));
    s.result = result;
    s.error = error;
    return s;
  };
  it("lists a text hit under the list's matches, marked for Enter, its link opening its doc", () => {
    const html = text(render(DocIndex, { props: { project: "pj", list, part: "matches", q: "pause", mode: "text", search: search(found) } }).body);
    expect(html).toContain("In the text");
    expect(html).toMatch(/<a class="hit[^"]*\bsel" href="#\/docs\/pj\/docs\/design.md" title="docs\/design.md:12"/);
  });
  it("hides the hits of older words, and shows none after a failed request", () => {
    const old = text(render(DocIndex, { props: { project: "pj", list, part: "matches", q: "pauses", mode: "text", search: search(found) } }).body);
    expect(old).not.toContain("In the text");
    expect(old).not.toContain('class="hit');
    const failed = text(render(DocIndex, {
      props: { project: "pj", list, part: "matches", q: "pauses", mode: "text", search: search(null, "502 Bad Gateway") },
    }).body);
    expect(failed).not.toContain('class="hit');
    expect(failed).toContain("Could not search the text: 502 Bad Gateway");
  });
  it("offers the same mode toggle as the index", () => {
    const html = text(render(DocModes, { props: { mode: "text" } }).body);
    expect(html).toContain('aria-pressed="false">Names');
    expect(html).toContain('aria-pressed="true">Search the text');
  });
});

describe("a title with inline markdown in the index", () => {
  const coded: DocList = {
    head: "h",
    pinned: [{ path: "docs/app.md", title: "`internal/app`: boundaries", updated: 2 }],
    groups: [{ dir: "docs", docs: [{ path: "docs/split.md", title: "`internal/app` split", updated: 1 }] }],
  };
  it("renders its code span in the pinned, tree and filtered rows, and matches and marks its words", () => {
    const side = text(render(DocIndex, { props: { project: "pj", list: coded } }).body);
    expect(side).toContain("<code>internal/app</code>: boundaries");
    expect(side).toContain("<code>internal/app</code> split");
    expect(side).not.toContain("`");
    const found = text(render(DocIndex, { props: { project: "pj", list: coded, q: "app: bound" } }).body);
    expect(found).toContain("1 of 2");
    expect(found).toContain("<code>internal/<mark>app</mark></code><mark>:</mark> <mark>bound</mark>aries");
    expect(found).not.toContain("`");
  });
});

describe("the doc outline", () => {
  const hs = [{ level: 2, text: "One", id: "h-one" }, { level: 3, text: "`Two`", id: "h-two" }];
  it("lists every heading, a level-3 one indented, and nothing for fewer than two", () => {
    const html = render(DocOutline, { props: { headings: hs } }).body;
    expect(html).toContain(">One<");
    expect(html).toMatch(/class="[^"]*sub[^"]*"[^>]*>Two</);
    expect(render(DocOutline, { props: { headings: hs.slice(0, 1) } }).body).not.toContain("<button");
  });
});
