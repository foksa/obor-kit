// The tasks page's list logic: a row's filter values, the search over id and title, the filters, and the counts per
// filter value (each facet counted under the search and the other facets, so a count is what clicking it shows).
import type { Tone } from "./format";
import type { TaskRow } from "./types";

// STATES is the order the state filter shows them: what needs a person first, the ended last.
export const STATES = ["needs-you", "running", "in the merge loop", "waiting", "merged", "dropped", "never dispatched"];

const TONE: Record<string, Tone> = {
  "needs-you": "bad", running: "ok", "in the merge loop": "info", waiting: "wait", merged: "done", dropped: "idle",
  "never dispatched": "idle",
};

// stateTone is a list state's pill colour.
export const stateTone = (s: string): Tone => TONE[s] || "idle";

export const ALL = "all";
export const NONE = "none";

export type Facet = "state" | "difficulty" | "model";
export type Filter = Record<Facet, string> & { q: string };

export const emptyFilter = (): Filter => ({ q: "", state: ALL, difficulty: ALL, model: ALL });

// modelOf is the model a row is filtered by: the model of the ref that ran its last attempt, else its spec's first
// Model: ref's ("opus:high" is opus); none when it has neither.
export function modelOf(r: TaskRow): string {
  const ref = r.ran || (r.model || "").split(",")[0];
  return ref.split(":")[0].trim() || NONE;
}

// valueOf is a row's value for facet.
export function valueOf(r: TaskRow, facet: Facet): string {
  if (facet === "state") return r.state;
  if (facet === "difficulty") return r.difficulty || NONE;
  return modelOf(r);
}

// matches is whether r is found by the search q: every word of q is in its id or title, case aside.
export function matches(r: TaskRow, q: string): boolean {
  const hay = (r.task + " " + (r.title || "")).toLowerCase();
  return q.toLowerCase().split(/\s+/).filter(Boolean).every((w) => hay.includes(w));
}

const FACETS: Facet[] = ["state", "difficulty", "model"];

// passes is whether r passes f, leaving out facet skip (the facet being counted).
function passes(r: TaskRow, f: Filter, skip?: Facet): boolean {
  if (!matches(r, f.q)) return false;
  return FACETS.every((k) => k === skip || f[k] === ALL || valueOf(r, k) === f[k]);
}

// filterRows is the rows f shows, in their order (the API's: newest activity first).
export const filterRows = (rows: TaskRow[], f: Filter) => rows.filter((r) => passes(r, f));

// counts is each facet's values with how many rows each shows (all: the facet's total), under the search and the
// other facets. The state facet lists STATES in order (zero counts too: the filter's buttons stay put); the others
// their values by count, then by name, none last.
export function counts(rows: TaskRow[], f: Filter): Record<Facet, [string, number][]> {
  const out = {} as Record<Facet, [string, number][]>;
  for (const k of FACETS) {
    const n = new Map<string, number>();
    let all = 0;
    for (const r of rows) {
      if (!passes(r, f, k)) continue;
      all++;
      n.set(valueOf(r, k), (n.get(valueOf(r, k)) || 0) + 1);
    }
    const vals = k === "state" ? STATES.map((s): [string, number] => [s, n.get(s) || 0])
      : [...n.entries()].sort((a, b) => Number(a[0] === NONE) - Number(b[0] === NONE) || b[1] - a[1] || a[0].localeCompare(b[0]));
    if (k !== "state" && f[k] !== ALL && !n.has(f[k])) vals.push([f[k], 0]); // the chosen value stays a button
    out[k] = [[ALL, all], ...vals];
  }
  return out;
}

// PAGE is how many rows the page draws at a time (Show more draws the next ones).
export const PAGE = 100;
