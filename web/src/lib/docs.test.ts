import { describe, expect, it } from "vitest";
import { docLinks, filterLines, normalize, outline, paths, resolve } from "./docs";
import { md } from "./md";
import type { DocList } from "./types";

const list: DocList = {
  head: "h",
  pinned: [{ path: "BACKLOG.md" }, { path: "docs/accepted.md" }],
  groups: [
    { dir: "docs/plan", docs: [{ path: "docs/plan/a.md", updated: 2 }, { path: "docs/plan/b.md", updated: 1 }] },
    { dir: "docs/reviews", docs: [{ path: "docs/reviews/RV-7.md" }, { path: "docs/reviews/DD-v0.38.md" }] },
    { dir: "notes", docs: [{ path: "notes/RV-7.md" }] },
  ],
};
const docs = new Set(paths(list));

describe("the doc list", () => {
  it("is every path, pinned first", () => {
    expect(paths(list)).toEqual(["BACKLOG.md", "docs/accepted.md", "docs/plan/a.md", "docs/plan/b.md",
      "docs/reviews/RV-7.md", "docs/reviews/DD-v0.38.md", "notes/RV-7.md"]);
    expect(paths(null)).toEqual([]);
  });
});

describe("link rules", () => {
  it("resolves a path from the root, then from the doc's directory; never out of the root or to an unlisted file", () => {
    expect(normalize("docs/plan/../accepted.md")).toBe("docs/accepted.md");
    expect(normalize("../x.md")).toBe("");
    expect(resolve(docs, "docs/plan/a.md", "docs/accepted.md")).toBe("docs/accepted.md");
    expect(resolve(docs, "docs/plan/a.md", "b.md")).toBe("docs/plan/b.md");
    expect(resolve(docs, "docs/plan/a.md", "../accepted.md#top")).toBe("docs/accepted.md");
    expect(resolve(docs, "docs/plan/a.md", "./b.md:12")).toBe("docs/plan/b.md");
    expect(resolve(docs, "BACKLOG.md", "../BACKLOG.md")).toBe("");
    expect(resolve(docs, "BACKLOG.md", "docs/draft.md")).toBe("");
    expect(resolve(docs, "BACKLOG.md", "mailto:x@y.md")).toBe("");
  });
  it("links a task to its page, RV-n and DD-… to their listed review file, a listed path to its doc; the rest stays text", () => {
    const L = docLinks("pj", docs, "docs/plan/a.md");
    const out = md("T-12 fixes RV-7 (DD-v0.38, RV-9); see docs/plan/b.md, `../accepted.md`, docs/draft.md and [b](b.md).", L);
    expect(out).toContain('<a href="#/t/pj/T-12">T-12</a>');
    expect(out).toContain('<a href="#/docs/pj/docs/reviews/RV-7.md">RV-7</a>'); // docs/reviews/ before notes/
    expect(out).toContain('<a href="#/docs/pj/docs/reviews/DD-v0.38.md">DD-v0.38</a>');
    expect(out).toContain("RV-9)"); // no RV-9.md listed
    expect(out).not.toContain("RV-9</a>");
    expect(out).toContain('<a href="#/docs/pj/docs/plan/b.md">docs/plan/b.md</a>');
    expect(out).toContain('<a href="#/docs/pj/docs/accepted.md"><code>../accepted.md</code></a>');
    expect(out).toContain(" docs/draft.md and ");
    expect(out).toContain('<a href="#/docs/pj/docs/plan/b.md">b</a>');
    expect(L.url!("https://example.com")).toBeUndefined(); // md links it itself, in a new tab
  });
});

describe("BACKLOG's outline", () => {
  it("is its ## headings with md's ids", () => {
    expect(outline("# Backlog\n## Now\ntext\n### detail\n## Later")).toEqual([
      { level: 2, text: "Now", id: "h-now" }, { level: 2, text: "Later", id: "h-later" }]);
  });
});

describe("accepted.md's filter", () => {
  const text = "# Accepted findings\n\nIntro line.\n\n- (RV-7, accepted) herdr adapter decodes\n- (RV-9, accepted) Codex lead restore\n- (RV-12) the herdr probe";
  it("keeps the headings and the lines with every word, case aside, and counts them", () => {
    const r = filterLines(text, "HERDR rv-7");
    expect(r).toEqual({ text: "# Accepted findings\n- (RV-7, accepted) herdr adapter decodes", shown: 1, total: 3 });
    expect(filterLines(text, "herdr").shown).toBe(2);
    expect(filterLines(text, "nothing like it")).toEqual({ text: "# Accepted findings", shown: 0, total: 3 });
  });
  it("is the whole text with no words", () => {
    expect(filterLines(text, "  ")).toEqual({ text, shown: 3, total: 3 });
  });
});
