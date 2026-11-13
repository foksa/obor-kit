// The docs page's components as served (svelte/server): the index's tree, matches and marked doc, the outline.
import { describe, expect, it } from "vitest";
import { render } from "svelte/server";
import type { DocList } from "./types";
import DocIndex from "../ui/DocIndex.svelte";
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

describe("the doc outline", () => {
  const hs = [{ level: 2, text: "One", id: "h-one" }, { level: 3, text: "`Two`", id: "h-two" }];
  it("lists every heading, a level-3 one indented, and nothing for fewer than two", () => {
    const html = render(DocOutline, { props: { headings: hs } }).body;
    expect(html).toContain(">One<");
    expect(html).toMatch(/class="[^"]*sub[^"]*"[^>]*>Two</);
    expect(render(DocOutline, { props: { headings: hs.slice(0, 1) } }).body).not.toContain("<button");
  });
});
