// The docs page's logic: the docs as one path list, how a doc's text links (a task to its page, an RV-n or DD-… to
// its review file and a repo path to that doc, both only when the doc list has them), BACKLOG's outline and
// accepted.md's filter.
import { headings, type Heading, type Links } from "./md";
import { href } from "./route";
import type { DocList } from "./types";

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
  };
}

export const isBacklog = (p: string) => base(p) === "BACKLOG.md";
export const isAccepted = (p: string) => base(p) === "accepted.md";

// outline is BACKLOG's sections: its ## headings.
export const outline = (text: string): Heading[] => headings(text).filter((h) => h.level === 2);

// filterLines is accepted.md under the filter box: its headings and every line with each word of q (case aside); the
// count is of its list lines, shown and in all. An empty q is the whole text.
export function filterLines(text: string, q: string): { text: string; shown: number; total: number } {
  const lines = String(text || "").split("\n");
  const item = (l: string) => /^\s*(?:[-*]|\d+\.)\s+/.test(l);
  const total = lines.filter(item).length;
  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return { text, shown: total, total };
  const kept = lines.filter((l) => /^#{1,4}\s/.test(l) || (item(l) && words.every((w) => l.toLowerCase().includes(w))));
  return { text: kept.join("\n"), shown: kept.filter(item).length, total };
}

// groupName is a directory's label in the list.
export const groupName = (dir: string) => (dir ? dir + "/" : "root");
