// The docs page's logic: the docs as one path list, how a doc's text links (a task to its page, an RV-n or DD-… to
// its review file, a repo path to that doc, both only when the doc list has them, a heading to its section), the
// list's tree, order, matches, recent docs and neighbours, a doc's outline (levels 2–3), pinned captions and
// accepted.md's filter.
import { esc, headings, inline, plainText, type Heading, type Links } from "./md";
import { href } from "./route";
import type { Doc, DocHit, DocList } from "./types";

// Order is how a folder's docs are listed: the server's newest first, or A–Z in natural order (v0.9 before v0.10).
export type Order = "new" | "az";

// Folder is one row of the docs tree: its path from the root ("" the root itself), its own docs and child folders, with
// the count of every doc here and below and the newest updated among them.
export interface Folder {
  dir: string;
  name: string;
  docs: Doc[];
  folders: Folder[];
  count: number;
  updated?: number;
}

// Match is one doc matchDocs keeps: the doc, where a word first matches its file name (-1: none does) and where one
// first matches its title (-1: none does, or it has none).
export interface Match {
  doc: Doc;
  at: number;
  tat: number;
}

// paths is every doc's path, pinned first.
export function paths(l: DocList | null | undefined): string[] {
  if (!l) return [];
  return [...(l.pinned || []).map((d) => d.path), ...(l.groups || []).flatMap((g) => g.docs.map((d) => d.path))];
}

const base = (p: string) => p.slice(p.lastIndexOf("/") + 1);
const dirOf = (p: string) => (p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "");

// normalize is p with its "." and ".." steps taken ("" when it climbs out of the root).
export function normalize(p: string): string {
  const out: string[] = [];
  for (const s of p.split("/")) {
    if (s === "" || s === ".") continue;
    if (s === "..") { if (!out.length) return ""; out.pop(); continue; }
    out.push(s);
  }
  return out.join("/");
}

// resolve is the listed doc a target names, read from the doc at from: a path from the root first, then one relative
// to from's directory ("": the list has neither). A target's #anchor and :line are dropped.
export function resolve(docs: Set<string>, from: string, target: string): string {
  const t = target.replace(/#.*$/, "").replace(/:\d+(-\d+)?$/, "");
  if (!t || /^[a-z][a-z0-9+.-]*:/i.test(t)) return "";
  const tries = t.startsWith("/") ? [t] : [t, (dirOf(from) ? dirOf(from) + "/" : "") + t];
  for (const c of tries) {
    const n = normalize(c);
    if (n && docs.has(n)) return n;
  }
  return "";
}

// ID is a review's or deep dive's id: its file is <id>.md wherever the list has it.
const ID = /^(RV-\d+|DD-[A-Za-z0-9](?:[\w.-]*[A-Za-z0-9])?)$/;

// docLinks is how the doc at from links (md's Links): a task to its page; an RV-n or DD-… to the listed <id>.md (the
// one under docs/reviews/ first); a path or a markdown link's relative target to that doc when listed. Anything the
// list does not have stays plain text.
export function docLinks(project: string, docs: Set<string>, from: string): Links {
  const toDoc = (p: string) => (p ? href.docs(project, p) : undefined);
  const ref = (token: string) => {
    if (/^T-\d+$/.test(token)) return href.task(project, token);
    if (ID.test(token)) {
      const named = [...docs].filter((p) => base(p) === token + ".md");
      named.sort((a, b) => Number(b.startsWith("docs/reviews/")) - Number(a.startsWith("docs/reviews/")) || a.localeCompare(b));
      return toDoc(named[0] || "");
    }
    return /\.md$/.test(token) ? toDoc(resolve(docs, from, token)) : undefined;
  };
  return {
    task: (id) => href.task(project, id),
    ref,
    url: (target) => {
      if (/^https?:\/\//i.test(target)) return undefined; // md links an external URL itself
      return toDoc(resolve(docs, from, target));
    },
    // a heading's anchor is this doc's address at that section; the page copies it whole, origin included
    section: (id) => href.docs(project, from, id),
  };
}

export const isAccepted = (p: string) => base(p) === "accepted.md";

// outline is every level-2 and level-3 heading: the page's "On this page" list.
export const outline = (text: string): Heading[] => headings(text).filter((h) => h.level === 2 || h.level === 3);

// filterLines is accepted.md under the filter box: its headings and every list item with each word of q (case aside),
// an item whole with its indented continuation lines; the count is of its list items, shown and in all. An empty q is
// the whole text.
export function filterLines(text: string, q: string): { text: string; shown: number; total: number } {
  const lines = String(text || "").split("\n");
  const item = (l: string) => /^\s*(?:[-*]|\d+\.)\s+/.test(l);
  const heading = (l: string) => /^#{1,4}\s/.test(l);
  const cont = (l: string) => /^\s+\S/.test(l);
  // blocks is each line, a list item carrying the indented lines that continue it (md joins them back).
  const blocks: { item: boolean; lines: string[] }[] = [];
  for (const l of lines) {
    const last = blocks[blocks.length - 1];
    if (last?.item && !item(l) && !heading(l) && cont(l)) { last.lines.push(l); continue; }
    blocks.push({ item: item(l), lines: [l] });
  }
  const total = blocks.filter((b) => b.item).length;
  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return { text, shown: total, total };
  const kept = blocks.filter((b) =>
    b.item
      ? words.every((w) => b.lines.join("\n").toLowerCase().includes(w))
      : heading(b.lines[0]));
  return { text: kept.flatMap((b) => b.lines).join("\n"), shown: kept.filter((b) => b.item).length, total };
}

// sortDocs is docs in order: "new" keeps the served (newest first) order, "az" sorts the file names naturally.
export function sortDocs(docs: Doc[] | null | undefined, order: Order): Doc[] {
  const out = [...(docs || [])];
  if (order === "az") {
    out.sort((a, b) =>
      base(a.path).localeCompare(base(b.path), undefined, { numeric: true }) || a.path.localeCompare(b.path));
  }
  return out;
}

// isVersion is a release note or upgrade guide: v<digits>.…md or UPGRADE-v*.md.
const isVersion = (d: Doc) => /^(?:v\d+\..*|UPGRADE-v.*)\.md$/.test(base(d.path));

// defaultOrder is the order a folder lists in until the page's toggle says otherwise: A–Z when every doc in it is a
// version file, Newest otherwise.
export function defaultOrder(folder: Pick<Folder, "docs">): Order {
  return folder.docs.length > 0 && folder.docs.every(isVersion) ? "az" : "new";
}

// tree is list's groups nested by path: the root ("top level") and every directory row (one with no docs of its own
// included), each with its docs in order, the count of the docs here and below, and the newest updated among them. An
// order given applies to every folder; without one each folder uses defaultOrder.
export function tree(list: DocList | null | undefined, order?: Order): Folder {
  const root: Folder = { dir: "", name: "top level", docs: [], folders: [], count: 0 };
  const byDir = new Map<string, Folder>([["", root]]);
  const folder = (dir: string): Folder => {
    const have = byDir.get(dir);
    if (have) return have;
    const cut = dir.lastIndexOf("/");
    const parent = folder(cut < 0 ? "" : dir.slice(0, cut));
    const made: Folder = { dir, name: dir.slice(cut + 1), docs: [], folders: [], count: 0 };
    parent.folders.push(made);
    byDir.set(dir, made);
    return made;
  };
  for (const g of list?.groups || []) {
    const f = folder(g.dir || "");
    f.docs.push(...sortDocs(g.docs, order ?? defaultOrder({ docs: g.docs })));
  }
  const measure = (f: Folder): void => {
    let count = 0;
    let updated = 0;
    for (const d of f.docs) {
      count++;
      if (d.updated && d.updated > updated) updated = d.updated;
    }
    for (const c of f.folders) {
      measure(c);
      count += c.count;
      if (c.updated && c.updated > updated) updated = c.updated;
    }
    f.count = count;
    if (updated) f.updated = updated;
  };
  measure(root);
  const sort = (f: Folder): void => {
    f.folders.sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }));
    f.folders.forEach(sort);
  };
  sort(root);
  return root;
}

// words is a filter's words, lower case.
const words = (q: string) => String(q || "").toLowerCase().split(/\s+/).filter(Boolean);

// matchDocs is list's docs whose path or title (its words, the markup aside) holds every word of q (case aside; a word
// may be in either): the pinned ones first, then the list's order, each with where a word first matches its file name
// and its title (for highlighting; -1 when none does). An empty q is every doc.
export function matchDocs(list: DocList | null | undefined, q: string): Match[] {
  const ws = words(q);
  const out: Match[] = [];
  const first = (s: string) => {
    const found = ws.map((w) => s.indexOf(w)).filter((i) => i >= 0);
    return found.length ? Math.min(...found) : -1;
  };
  for (const d of [...(list?.pinned || []), ...(list?.groups || []).flatMap((g) => g.docs)]) {
    const title = plainText(d.title || "").toLowerCase();
    const path = d.path.toLowerCase();
    if (!ws.every((w) => path.includes(w) || title.includes(w))) continue;
    out.push({ doc: d, at: first(base(path)), tat: title ? first(title) : -1 });
  }
  return out;
}

// marks is text cut into pieces, each piece that is a word of q (case aside) marked as a hit: a match row's highlight.
export function marks(text: string, q: string): { text: string; hit: boolean }[] {
  const ws = words(q);
  const low = text.toLowerCase();
  const hit = new Array<boolean>(text.length).fill(false);
  for (const w of ws) {
    for (let i = low.indexOf(w); i >= 0; i = low.indexOf(w, i + 1)) hit.fill(true, i, i + w.length);
  }
  const out: { text: string; hit: boolean }[] = [];
  for (let i = 0; i < text.length; i++) {
    const last = out[out.length - 1];
    if (last && last.hit === hit[i]) last.text += text[i];
    else out.push({ text: text[i], hit: hit[i] });
  }
  return out;
}

// HitGroup is one doc's text-search hits, in the served order.
export interface HitGroup {
  path: string;
  hits: DocHit[];
}

// groupHits is a text search's hits grouped by doc: the docs in the order their first hit came, each doc's hits in the
// served order.
export function groupHits(hits: DocHit[] | null | undefined): HitGroup[] {
  const out: HitGroup[] = [];
  const at = new Map<string, HitGroup>();
  for (const h of hits || []) {
    let g = at.get(h.path);
    if (!g) {
      g = { path: h.path, hits: [] };
      at.set(h.path, g);
      out.push(g);
    }
    g.hits.push(h);
  }
  return out;
}

// sectionAt is the id, in body as rendered, of the nearest heading at or above line (1-based) of text, the doc's file:
// body is text as shown (accepted.md's line filter may drop lines), so the heading is found by its text and how many
// headings of that text follow it. "" when no heading is above the line or the heading is not in body: the doc's top.
export function sectionAt(text: string, body: string, line: number): string {
  const upTo = headings(String(text || "").split("\n").slice(0, Math.max(0, line)).join("\n"));
  const above = upTo[upTo.length - 1];
  if (!above) return "";
  const same = (h: Heading) => h.text === above.text;
  const after = headings(text).filter(same).length - upTo.filter(same).length;
  const shown = headings(body).filter(same);
  return shown[shown.length - 1 - after]?.id || "";
}

// byNewest is docs newest first: a staged one (no updated) first, then by updated descending (a tie keeps its place).
function byNewest(docs: Doc[]): Doc[] {
  const staged = docs.filter((d) => !d.updated);
  const dated = docs.filter((d) => d.updated).sort((a, b) => (b.updated || 0) - (a.updated || 0));
  return [...staged, ...dated];
}

// recent is the n newest docs across pinned and groups, a staged one (no updated) first.
export function recent(list: DocList | null | undefined, n: number): Doc[] {
  return byNewest([...(list?.pinned || []), ...(list?.groups || []).flatMap((g) => g.docs)]).slice(0, Math.max(0, n));
}

// folderDocs is every doc of path's directory, the pinned ones among them.
function folderDocs(list: DocList | null | undefined, path: string): Doc[] {
  const dir = dirOf(path);
  const pinned = (list?.pinned || []).filter((d) => dirOf(d.path) === dir);
  return [...pinned, ...((list?.groups || []).find((g) => (g.dir || "") === dir)?.docs || [])];
}

// neighbours is the doc before and after path among every doc of its folder, the pinned ones too: newer and older by
// updated under Newest (a staged one newest), the previous and next file name under A–Z. Without an order the folder's
// defaultOrder applies.
export function neighbours(
  list: DocList | null | undefined,
  path: string,
  order?: Order,
): { newer?: Doc; older?: Doc } {
  const docs = folderDocs(list, path);
  const sorted = (order ?? defaultOrder({ docs })) === "az" ? sortDocs(docs, "az") : byNewest(docs);
  const i = sorted.findIndex((d) => d.path === path);
  return i < 0 ? {} : { newer: sorted[i - 1], older: sorted[i + 1] };
}

// sameName is the listed docs with path's file name, path itself aside: what the not-tracked state offers instead.
export function sameName(list: DocList | null | undefined, path: string): Doc[] {
  const name = base(path);
  return [...(list?.pinned || []), ...(list?.groups || []).flatMap((g) => g.docs)].filter(
    (d) => d.path !== path && base(d.path) === name,
  );
}

// docTitle is the doc's first level-1 heading's text ("" when it has none).
export const docTitle = (text: string): string => headings(text).find((h) => h.level === 1)?.text || "";

// defaultFolded is whether a folder of count docs starts folded: over 12 docs.
export const defaultFolded = (count: number): boolean => count > 12;

// pinnedCaption is a pinned doc's one-line caption. It knows PinnedDocs and a pinned BACKLOG.md wherever it is; any
// other doc gets "".
export function pinnedCaption(path: string): string {
  switch (path) {
    case "docs/decisions.md": return "owner decisions on record";
    case "docs/accepted.md": return "review findings accepted, by rule";
    case "AGENTS.md": return "the rules every agent obeys";
    case "README.md": return "what obor is, and how to start";
  }
  return base(path) === "BACKLOG.md" ? "what is open, the handoff" : "";
}

// Folds is which folders are open, by dir ("" the top level); a folder not in it is open unless defaultFolded.
export type Folds = Record<string, boolean>;

// isOpen is whether folder f shows its rows: what folds says, else open unless its own docs are over the threshold.
export const isOpen = (f: Folder, folds: Folds): boolean => folds[f.dir] ?? !defaultFolded(f.docs.length);

// unfold is folds with path's folder and every parent of it open (the open doc shows in the tree).
export function unfold(folds: Folds, path: string): Folds {
  const out = { ...folds };
  let dir = dirOf(path);
  out[dir] = true;
  while (dir) {
    dir = dirOf(dir);
    out[dir] = true;
  }
  return out;
}

// everyFold is folds with every folder of root set to open.
export function everyFold(root: Folder, open: boolean): Folds {
  const out: Folds = {};
  const walk = (f: Folder) => { out[f.dir] = open; f.folders.forEach(walk); };
  walk(root);
  return out;
}

// Row is one line of the folder tree as shown: a folder (its fold state) or a doc, at a depth.
export type Row =
  | { kind: "dir"; folder: Folder; depth: number; open: boolean }
  | { kind: "doc"; doc: Doc; depth: number };

// rows is the tree as its shown lines: the top level's row when it has docs of its own, then each folder by name,
// nested ones a step deeper; a folded folder's docs and folders are left out.
export function rows(root: Folder, folds: Folds): Row[] {
  const out: Row[] = [];
  const walk = (f: Folder, depth: number) => {
    const open = isOpen(f, folds);
    out.push({ kind: "dir", folder: f, depth, open });
    if (!open) return;
    for (const d of f.docs) out.push({ kind: "doc", doc: d, depth: depth + 1 });
    for (const c of f.folders) walk(c, depth + 1);
  };
  if (root.docs.length) {
    const open = isOpen(root, folds);
    out.push({ kind: "dir", folder: root, depth: 0, open });
    if (open) for (const d of root.docs) out.push({ kind: "doc", doc: d, depth: 1 });
  }
  for (const c of root.folders) walk(c, 0);
  return out;
}

// matchKey is what a key in the filter does over n matches with sel the marked one: ↑/↓ move the mark (kept in range),
// Enter opens the marked match, Escape clears the filter; any other key does nothing (null).
export function matchKey(key: string, sel: number, n: number): { sel: number; open?: boolean; clear?: boolean } | null {
  if (key === "ArrowDown") return { sel: n ? Math.min(n - 1, sel + 1) : 0 };
  if (key === "ArrowUp") return { sel: Math.max(0, sel - 1) };
  if (key === "Enter") return n ? { sel: Math.min(sel, n - 1), open: true } : null;
  if (key === "Escape") return { sel: 0, clear: true };
  return null;
}

// currentHeading is the heading a reader is in: the last one whose top (px from the viewport's top) is at or above
// line; the first when none is yet; "" with none.
export function currentHeading(tops: { id: string; top: number }[], line: number): string {
  let cur = tops[0]?.id || "";
  for (const t of tops) {
    if (t.top <= line) cur = t.id;
    else break;
  }
  return cur;
}

// progress is how far through a box (its top px from the viewport's top, its height) a reader of a view this tall is,
// 0 to 1: 0 while its top is in view, 1 once its end is.
export function progress(top: number, height: number, view: number): number {
  const span = height - view;
  if (span <= 0) return top <= 0 ? 1 : 0;
  return Math.min(1, Math.max(0, -top / span));
}

// docLabel is how a doc reads in a row: its title's words (the markup aside) when it has one, its file name otherwise.
export const docLabel = (d: Doc): string => (d.title ? plainText(d.title) : base(d.path));

// labelHtml is how a doc shows in a row, as HTML: its title's inline markdown (code, bold; nothing links: the row is
// the link), its file name otherwise.
export const labelHtml = (d: Doc): string => (d.title ? inline(d.title, null) : esc(base(d.path)));

// splitTitle is a doc's rendered HTML cut at its title heading (the <h1> with that id): the heading's inner HTML (its
// text and section anchor) for the page's header, the rest for the body. No such heading: head "", the HTML whole.
export function splitTitle(html: string, id: string): { head: string; rest: string } {
  const open = `<h1 id="${id}">`;
  const i = id ? html.indexOf(open) : -1;
  if (i < 0) return { head: "", rest: html };
  const j = html.indexOf("</h1>", i);
  return { head: html.slice(i + open.length, j), rest: html.slice(0, i) + html.slice(j + "</h1>".length) };
}

// folderOf is a path's folder with its slash ("" at the top level).
export const folderOf = (p: string): string => (dirOf(p) ? dirOf(p) + "/" : "");

// fileOf is a path's file name.
export const fileOf = base;
