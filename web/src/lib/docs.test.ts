import { describe, expect, it } from "vitest";
import {
  currentHeading, defaultFolded, defaultOrder, docLabel, docLinks, docTitle, everyFold, filterLines, groupHits, isOpen, marks,
  matchDocs, matchKey, neighbours, normalize, outline, paths, pinnedCaption, progress, recent, resolve, rows, sameName,
  sectionAt, sortDocs, tree, unfold,
} from "./docs";
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
  it("gives every heading a section link to its doc, and none where the caller has no section address", () => {
    const out = md("## The plan\n### deep `x`", docLinks("pj", docs, "docs/plan/a.md"));
    expect(out).toContain('<h2 id="h-the-plan">The plan<a class="hlink"');
    expect(out).toContain('href="#/docs/pj/docs/plan/a.md#h-the-plan"');
    expect(out).toContain('href="#/docs/pj/docs/plan/a.md#h-deep-x"');
    expect(out).toContain('aria-label="Copy link to this section"');
    expect(md("## The plan", (t: string) => "#/t/pj/" + t)).toBe('<h2 id="h-the-plan">The plan</h2>');
  });
});

describe("a doc's outline", () => {
  it("is its ## and ### headings with md's ids", () => {
    expect(outline("# Backlog\n## Now\ntext\n### detail\n## Later\n#### deep")).toEqual([
      { level: 2, text: "Now", id: "h-now" }, { level: 3, text: "detail", id: "h-detail" },
      { level: 2, text: "Later", id: "h-later" }]);
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
  const multi = [
    "# Accepted findings",
    "",
    "- (RV-84 2nd verify #3, accepted: rare) `obor stop`: if unregister or the marker write fails",
    "  the prior pause is lost (persist it in a recovery record, ~20-50 lines).",
    "- (RV-84 verify, accepted) `obor stop`: a pause cleared by another client is not",
    "  guarded; a serve-owned stopping state would be (design concern).",
  ].join("\n");
  it("keeps a multiline item whole when its first line matches", () => {
    const r = filterLines(multi, "unregister");
    expect(r).toEqual({ text: "# Accepted findings\n" + multi.split("\n").slice(2, 4).join("\n"), shown: 1, total: 2 });
    expect(md(r.text, (id) => id)).toContain("the prior pause is lost");
  });
  it("keeps a multiline item whole when only a continuation line matches", () => {
    const r = filterLines(multi, "guarded");
    expect(r).toEqual({ text: "# Accepted findings\n" + multi.split("\n").slice(4).join("\n"), shown: 1, total: 2 });
    expect(md(r.text, (id) => id)).toContain("is not guarded");
  });
  it("is the document unchanged with no words", () => {
    expect(filterLines(multi, "")).toEqual({ text: multi, shown: 2, total: 2 });
  });
});

const treeList: DocList = {
  head: "h",
  pinned: [{ path: "BACKLOG.md", updated: 10 }, { path: "AGENTS.md", updated: 9 }],
  groups: [
    { dir: "", docs: [{ path: "CHANGELOG.md", updated: 5 }, { path: "NOTES.md", updated: 1 }] },
    { dir: "docs", docs: [{ path: "docs/design.md", updated: 20 }, { path: "docs/decisions.md", updated: 4 }] },
    { dir: "docs/plan", docs: [{ path: "docs/plan/phase-7.md", updated: 30 }] },
    { dir: "docs/releases", docs: [{ path: "docs/releases/v0.10.md", updated: 8 }, { path: "docs/releases/v0.9.md", updated: 7 }] },
    { dir: "skills/lead", docs: [{ path: "skills/lead/a.md", updated: 3 }] },
  ],
};

describe("the docs tree", () => {
  it("nests folders by path, with each folder's docs, count and newest updated", () => {
    const t = tree(treeList);
    expect(t).toMatchObject({ dir: "", name: "top level", count: 8, updated: 30 });
    expect(t.docs.map((d) => d.path)).toEqual(["CHANGELOG.md", "NOTES.md"]);
    expect(t.folders.map((f) => f.dir)).toEqual(["docs", "skills"]);
    const docs = t.folders[0];
    expect(docs).toMatchObject({ name: "docs", count: 5, updated: 30 });
    expect(docs.docs.map((d) => d.path)).toEqual(["docs/design.md", "docs/decisions.md"]);
    expect(docs.folders.map((f) => f.dir)).toEqual(["docs/plan", "docs/releases"]);
    expect(docs.folders[1].docs.map((d) => d.path)).toEqual(["docs/releases/v0.9.md", "docs/releases/v0.10.md"]); // its default
    const skills = t.folders[1]; // a parent with no docs of its own still gets a row
    expect(skills).toMatchObject({ name: "skills", docs: [], count: 1, updated: 3 });
    expect(skills.folders[0].dir).toBe("skills/lead");
  });
  it("sorts every folder in an order it is given, Newest or A–Z", () => {
    expect(tree(treeList, "new").folders[0].folders[1].docs.map((d) => d.path)).toEqual([
      "docs/releases/v0.10.md", "docs/releases/v0.9.md"]);
    const az = tree(treeList, "az");
    expect(az.folders[0].docs.map((d) => d.path)).toEqual(["docs/decisions.md", "docs/design.md"]);
    expect(az.folders[0].folders[1].docs.map((d) => d.path)).toEqual([
      "docs/releases/v0.9.md", "docs/releases/v0.10.md"]);
  });
  it("is the bare top level for no list", () => {
    expect(tree(null)).toEqual({ dir: "", name: "top level", docs: [], folders: [], count: 0 });
  });
});

describe("sorting docs", () => {
  it("keeps the served order for Newest and sorts file names naturally for A–Z", () => {
    const served = [{ path: "docs/releases/v0.10.md" }, { path: "docs/releases/v0.9.md" }, { path: "docs/a.md" }];
    expect(sortDocs(served, "new")).toEqual(served);
    expect(sortDocs(served, "az").map((d) => d.path)).toEqual([
      "docs/a.md", "docs/releases/v0.9.md", "docs/releases/v0.10.md"]);
    expect(served.map((d) => d.path)).toEqual(["docs/releases/v0.10.md", "docs/releases/v0.9.md", "docs/a.md"]);
    expect(sortDocs(null, "az")).toEqual([]);
  });
});

describe("a folder's default order", () => {
  it("is A–Z when every doc is a version file, Newest otherwise", () => {
    expect(defaultOrder(tree(treeList).folders[0].folders[1])).toBe("az");
    expect(defaultOrder({ docs: [{ path: "docs/UPGRADE-v0.34.md" }, { path: "docs/UPGRADE-v0.35.md" }] })).toBe("az");
    expect(defaultOrder({ docs: [{ path: "docs/UPGRADE-v0.34.md" }, { path: "docs/design.md" }] })).toBe("new");
    expect(defaultOrder({ docs: [] })).toBe("new");
  });
});

describe("matching docs", () => {
  const matchList: DocList = {
    head: "h",
    pinned: [{ path: "README.md", updated: 1 }],
    groups: [
      { dir: "docs", docs: [
        { path: "docs/design.md", updated: 2 },
        { path: "docs/decisions.md", updated: 3 },
      ] },
      { dir: "notes", docs: [{ path: "notes/design-notes.md", updated: 4 }] },
    ],
  };
  it("keeps the docs whose path holds every word, case aside", () => {
    expect(matchDocs(matchList, "DESIGN").map((m) => m.doc.path)).toEqual(["docs/design.md", "notes/design-notes.md"]);
    expect(matchDocs(matchList, "docs design").map((m) => m.doc.path)).toEqual(["docs/design.md"]);
    expect(matchDocs(matchList, "notes design").map((m) => m.doc.path)).toEqual(["notes/design-notes.md"]);
    expect(matchDocs(matchList, "obor")).toEqual([]);
  });
  it("keeps the pinned docs first and says where a word first matches the file name", () => {
    const all = matchDocs(matchList, "");
    expect(all.map((m) => m.doc.path)).toEqual([
      "README.md", "docs/design.md", "docs/decisions.md", "notes/design-notes.md"]);
    expect(all[0].at).toBe(-1);
    expect(matchDocs(matchList, "sign")[0].at).toBe(2); // "design.md"
    expect(matchDocs(matchList, "ign des")[0].at).toBe(0);
    expect(matchDocs(matchList, "docs")[0].at).toBe(-1); // the folder only
    expect(matchDocs(matchList, "nothing here")).toEqual([]);
  });
});

describe("recent docs", () => {
  const list: DocList = {
    head: "h",
    pinned: [{ path: "README.md" }, { path: "AGENTS.md", updated: 4 }],
    groups: [
      { dir: "a", docs: [{ path: "a/staged.md" }, { path: "a/old.md", updated: 1 }] },
      { dir: "b", docs: [{ path: "b/new.md", updated: 9 }] },
    ],
  };
  it("is the n newest across pinned and groups, a staged doc first", () => {
    expect(recent(list, 3).map((d) => d.path)).toEqual(["README.md", "a/staged.md", "b/new.md"]);
    expect(recent(list, 9).map((d) => d.path)).toEqual(["README.md", "a/staged.md", "b/new.md", "AGENTS.md", "a/old.md"]);
    expect(recent(list, 0)).toEqual([]);
    expect(recent(null, 3)).toEqual([]);
  });
});

describe("a doc's neighbours", () => {
  it("is the doc before and after it in its folder's order", () => {
    expect(neighbours(treeList, "docs/design.md", "new").newer).toBeUndefined();
    expect(neighbours(treeList, "docs/design.md", "new").older?.path).toBe("docs/decisions.md");
    expect(neighbours(treeList, "docs/decisions.md", "new").newer?.path).toBe("docs/design.md");
    expect(neighbours(treeList, "docs/decisions.md", "new").older).toBeUndefined();
    expect(neighbours(treeList, "docs/plan/phase-7.md", "new").newer).toBeUndefined();
    expect(neighbours(treeList, "docs/plan/phase-7.md", "new").older).toBeUndefined();
    expect(neighbours(treeList, "docs/design.md", "az").newer?.path).toBe("docs/decisions.md");
    expect(neighbours(treeList, "docs/design.md", "az").older).toBeUndefined();
  });
  it("reads a folder's default order when none is given", () => {
    expect(neighbours(treeList, "docs/design.md").newer).toBeUndefined(); // docs/ is not a version folder
    expect(neighbours(treeList, "docs/design.md").older?.path).toBe("docs/decisions.md");
    expect(neighbours(treeList, "docs/releases/v0.10.md").newer?.path).toBe("docs/releases/v0.9.md"); // A–Z
    expect(neighbours(treeList, "docs/releases/v0.10.md").older).toBeUndefined();
    expect(neighbours(treeList, "docs/releases/v0.10.md", "new").newer).toBeUndefined();
    expect(neighbours(treeList, "docs/releases/v0.10.md", "new").older?.path).toBe("docs/releases/v0.9.md");
  });
  it("reads a pinned doc in its folder's served order and nothing for an unlisted path", () => {
    expect(neighbours(treeList, "AGENTS.md", "new").newer?.path).toBe("BACKLOG.md");
    expect(neighbours(treeList, "AGENTS.md", "new").older?.path).toBe("CHANGELOG.md");
    expect(neighbours(treeList, "docs/draft.md", "new")).toEqual({});
  });
});

describe("same-name docs", () => {
  it("is the listed docs with that file name, the path itself aside", () => {
    expect(sameName(list, "drafts/RV-7.md").map((d) => d.path)).toEqual(["docs/reviews/RV-7.md", "notes/RV-7.md"]);
    expect(sameName(list, "docs/accepted.md")).toEqual([]);
    expect(sameName(null, "x/a.md")).toEqual([]);
  });
});

describe("a doc's title", () => {
  it("is its first level-1 heading, and empty without one", () => {
    expect(docTitle("# Obor design\n\n## Part\ntext")).toBe("Obor design");
    expect(docTitle("## Part\n# Obor design v2")).toBe("Obor design v2");
    expect(docTitle("```\n# not a heading\n```\n# Yes")).toBe("Yes");
    expect(docTitle("no heading")).toBe("");
  });
});

describe("the fold threshold", () => {
  it("folds a folder over 12 docs", () => {
    expect(defaultFolded(12)).toBe(false);
    expect(defaultFolded(13)).toBe(true);
    expect(defaultFolded(0)).toBe(false);
  });
});

describe("pinned captions", () => {
  it("is one line for each pinned file, and empty for the rest", () => {
    expect(pinnedCaption("BACKLOG.md")).toBe("what is open, the handoff");
    expect(pinnedCaption("docs/BACKLOG.md")).toBe("what is open, the handoff");
    expect(pinnedCaption("docs/decisions.md")).toBe("owner decisions on record");
    expect(pinnedCaption("docs/accepted.md")).toBe("review findings accepted, by rule");
    expect(pinnedCaption("AGENTS.md")).toBe("the rules every agent obeys");
    expect(pinnedCaption("README.md")).toBe("what obor is, and how to start");
    expect(pinnedCaption("docs/design.md")).toBe("");
  });
});

describe("matching docs by title (T-1306)", () => {
  const titled: DocList = {
    head: "h",
    pinned: [],
    groups: [{ dir: "docs", docs: [
      { path: "docs/design.md", title: "obor-kit design (v2)", updated: 2 },
      { path: "docs/state-model.md", updated: 1 },
    ] }],
  };
  it("matches a word in the title, and every word in the path or the title", () => {
    expect(matchDocs(titled, "obor")).toEqual([{ doc: titled.groups[0].docs[0], at: -1, tat: 0 }]);
    expect(matchDocs(titled, "docs v2").map((m) => m.doc.path)).toEqual(["docs/design.md"]); // docs: path; v2: title
    expect(matchDocs(titled, "state v2")).toEqual([]);
    expect(matchDocs(titled, "model")[0]).toEqual({ doc: titled.groups[0].docs[1], at: 6, tat: -1 });
  });
});

describe("a match's highlight", () => {
  it("marks every piece that is a word of the filter, case aside", () => {
    expect(marks("herdr-core.md", "CORE")).toEqual([{ text: "herdr-", hit: false }, { text: "core", hit: true }, { text: ".md", hit: false }]);
    expect(marks("a-b-a", "a")).toEqual([
      { text: "a", hit: true }, { text: "-b-", hit: false }, { text: "a", hit: true }]);
    expect(marks("design", "des sign")).toEqual([{ text: "design", hit: true }]);
    expect(marks("x.md", "")).toEqual([{ text: "x.md", hit: false }]);
  });
});

describe("the folds", () => {
  const big = Array.from({ length: 13 }, (_, i) => ({ path: `docs/research/r${i}.md`, updated: i + 1 }));
  const foldList: DocList = {
    head: "h",
    pinned: [],
    groups: [
      { dir: "", docs: [{ path: "README.md", updated: 1 }] },
      { dir: "docs", docs: [{ path: "docs/design.md", updated: 5 }] },
      { dir: "docs/research", docs: big },
      { dir: "skills/lead", docs: [{ path: "skills/lead/SKILL.md", updated: 3 }] },
    ],
  };
  const root = tree(foldList);
  const research = root.folders[0].folders[0];
  it("starts a folder of over 12 own docs folded, the rest open, until the folds say otherwise", () => {
    expect(research.dir).toBe("docs/research");
    expect(isOpen(research, {})).toBe(false);
    expect(isOpen(root.folders[0], {})).toBe(true); // docs/: 1 own doc, 14 with research's
    expect(isOpen(research, { "docs/research": true })).toBe(true);
    expect(isOpen(root.folders[0], { docs: false })).toBe(false);
  });
  it("unfolds the open doc's folder and every parent, keeping the other folds", () => {
    expect(unfold({ skills: false }, "docs/research/r1.md")).toEqual({ skills: false, "docs/research": true, docs: true, "": true });
    expect(unfold({}, "README.md")).toEqual({ "": true });
  });
  it("opens or folds every folder at once", () => {
    expect(everyFold(root, false)).toEqual({
      "": false, docs: false, "docs/research": false, skills: false, "skills/lead": false });
    expect(Object.values(everyFold(root, true)).every(Boolean)).toBe(true);
  });
  it("lists the shown rows: the top level, folders by name nested a step deeper, a folded folder's docs left out", () => {
    const shown = rows(root, {}).map((r) => (r.kind === "dir" ? `${r.depth}:${r.folder.dir || "top"}/${r.open ? "open" : "folded"}` : `${r.depth}:${r.doc.path}`));
    expect(shown).toEqual([
      "0:top/open", "1:README.md",
      "0:docs/open", "1:docs/design.md", "1:docs/research/folded",
      "0:skills/open", "1:skills/lead/open", "2:skills/lead/SKILL.md",
    ]);
    const unfolded = rows(root, unfold({}, "docs/research/r0.md"));
    expect(unfolded.filter((r) => r.kind === "doc" && r.doc.path.startsWith("docs/research/"))).toHaveLength(13);
    const folded = rows(root, { skills: false }).map((r) => (r.kind === "dir" ? r.folder.dir : r.doc.path));
    expect(folded).toContain("skills");
    expect(folded).not.toContain("skills/lead");
  });
});

describe("the filter's keys", () => {
  it("moves the mark with the arrows within the matches, opens on Enter, clears on Escape", () => {
    expect(matchKey("ArrowDown", 0, 3)).toEqual({ sel: 1 });
    expect(matchKey("ArrowDown", 2, 3)).toEqual({ sel: 2 });
    expect(matchKey("ArrowUp", 0, 3)).toEqual({ sel: 0 });
    expect(matchKey("ArrowUp", 2, 3)).toEqual({ sel: 1 });
    expect(matchKey("Enter", 1, 3)).toEqual({ sel: 1, open: true });
    expect(matchKey("Enter", 5, 3)).toEqual({ sel: 2, open: true });
    expect(matchKey("Enter", 0, 0)).toBeNull();
    expect(matchKey("Escape", 2, 3)).toEqual({ sel: 0, clear: true });
    expect(matchKey("a", 0, 3)).toBeNull();
  });
});

describe("the outline's current heading", () => {
  const tops = [{ id: "h-a", top: -400 }, { id: "h-b", top: 120 }, { id: "h-c", top: 900 }];
  it("is the last heading at or above the line, the first before any is", () => {
    expect(currentHeading(tops, 300)).toBe("h-b");
    expect(currentHeading(tops, 100)).toBe("h-a");
    expect(currentHeading(tops, 900)).toBe("h-c");
    expect(currentHeading([{ id: "h-a", top: 500 }, { id: "h-b", top: 800 }], 300)).toBe("h-a");
    expect(currentHeading([], 300)).toBe("");
  });
});

describe("reading progress", () => {
  it("is 0 with the doc's top in view, 1 once its end is, in between by the scroll", () => {
    expect(progress(100, 3000, 800)).toBe(0);
    expect(progress(-1100, 3000, 800)).toBe(0.5);
    expect(progress(-5000, 3000, 800)).toBe(1);
    expect(progress(0, 500, 800)).toBe(1); // a doc shorter than the view
    expect(progress(10, 500, 800)).toBe(0);
  });
});

describe("a doc's label", () => {
  it("is its title where it has one, its file name otherwise", () => {
    expect(docLabel({ path: "docs/design.md", title: "obor-kit design" })).toBe("obor-kit design");
    expect(docLabel({ path: "docs/design.md" })).toBe("design.md");
  });
});

describe("groupHits", () => {
  it("groups the hits by doc, the docs in the order of their first hit, each doc's hits in the served order", () => {
    const g = groupHits([
      { path: "b.md", line: 3, text: "x" }, { path: "a.md", line: 9, text: "x" }, { path: "b.md", line: 7, text: "x" },
    ]);
    expect(g.map((d) => d.path)).toEqual(["b.md", "a.md"]);
    expect(g[0].hits.map((h) => h.line)).toEqual([3, 7]);
    expect(groupHits(null)).toEqual([]);
  });
});

describe("sectionAt", () => {
  const text = "# Title\n\nintro\n\n## Use\n\nuse it\n\n```\n## not a heading\n```\n\n## Use\n\nagain\n";
  const body = "\nintro\n\n## Use\n\nuse it\n\n```\n## not a heading\n```\n\n## Use\n\nagain\n";
  it("is the id of the nearest heading above the line, a repeated heading told apart, fenced ones skipped", () => {
    expect(sectionAt(text, body, 7)).toBe("h-use");
    expect(sectionAt(text, body, 10)).toBe("h-use"); // the fenced line: still under the first Use
    expect(sectionAt(text, body, 15)).toBe("h-use-1");
    expect(sectionAt(text, body, 13)).toBe("h-use-1"); // the heading's own line
  });
  it("is the top for a line under the title (shown in the header) or above every heading", () => {
    expect(sectionAt(text, body, 3)).toBe("");
    expect(sectionAt("plain\n\n## A\n", "plain\n\n## A\n", 1)).toBe("");
  });
});
