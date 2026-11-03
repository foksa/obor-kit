// The Setup page's staged edits (T-760): a change is a key (config.EditKeys) and its new value, kept until the owner
// commits them all at once (POST /api/config: one commit, all or nothing). They are made on the config revision the
// page read when the first was staged (base): serve refuses them once config.json is another file, so a tab never
// overwrites what another tab, the lead or a hand edit saved meanwhile.
import type { Setup } from "./types";

export type Value = string | number | string[];
export type Staged = Record<string, Value>;

// current is key's value in s (undefined: s has no such field).
export function current(s: Setup, key: string): Value | undefined {
  const p = key.split(".");
  if (p[0] === "difficulty" && p.length === 2) return s.difficulty.find((d) => d.level === p[1])?.types;
  if (p[0] === "lane_types" && p.length === 3) {
    const t = s.lane_types.find((x) => x.name === p[1]);
    if (!t) return undefined;
    return p[2] === "model" ? t.model : p[2] === "effort" ? t.effort : p[2] === "max" ? t.max : undefined;
  }
  if (p[0] === "lanes" && p.length === 2) return s.lanes.find((l) => l.lane === p[1])?.type;
  if (p[0] === "roles" && p.length === 3 && p[2] === "type") return s.roles.find((r) => r.role === p[1])?.type;
  return undefined;
}

export const same = (a: Value | undefined, b: Value | undefined) => JSON.stringify(a) === JSON.stringify(b);

// stage is staged with key set to v; a value equal to the setup's own is no change (unstaged).
export function stage(staged: Staged, s: Setup, key: string, v: Value): Staged {
  const next = { ...staged };
  if (same(current(s, key), v)) delete next[key];
  else next[key] = v;
  return next;
}

// value is key's value as the page shows it: staged, else the setup's.
export const value = (staged: Staged, s: Setup, key: string) => (key in staged ? staged[key] : current(s, key));

// show is a value as the review lists it.
export function show(v: Value | undefined): string {
  if (v === undefined) return "—";
  if (Array.isArray(v)) return v.length ? v.join(", ") : "none";
  if (v === "" ) return "unset";
  if (v === 0) return "0";
  return String(v);
}

// body is the staged changes as POST /api/config's body, in a stable order.
export function body(staged: Staged, base: string) {
  return { changes: Object.keys(staged).sort().map((key) => ({ key, value: staged[key] })), base };
}

// move is list with the item at i moved by d (−1 earlier, +1 later); out of range: list as it is.
export function move<T>(list: T[], i: number, d: number): T[] {
  const j = i + d;
  if (i < 0 || j < 0 || i >= list.length || j >= list.length) return list;
  const out = list.slice();
  [out[i], out[j]] = [out[j], out[i]];
  return out;
}

// typeRef is a role's or level's type reference as its type and effort ("opus:high" → opus, high).
export function splitRef(ref: string): [string, string] {
  const i = ref.indexOf(":");
  return i < 0 ? [ref, ""] : [ref.slice(0, i), ref.slice(i + 1)];
}
export const joinRef = (type: string, effort: string) => (type && effort ? `${type}:${effort}` : type);

// CLAUDE_MODELS are the model aliases Claude Code takes; other kinds' choices come from this project's own lane types.
const CLAUDE_MODELS = ["opus", "sonnet", "haiku", "fable"];

// models is the model choices for a lane type of kind: the known ones, every model this project's lane types of that
// kind name, and the type's own (so the select always holds it).
export function models(s: Setup, kind: string, own: string): string[] {
  const out = new Set<string>(kind === "claude" ? CLAUDE_MODELS : []);
  for (const t of s.lane_types) if (t.kind === kind && t.model) out.add(t.model);
  if (own) out.add(own);
  return [...out];
}

// LOCKED are a lane's states that hold a task: serve refuses its type change until it is free.
export const LOCKED = new Set(["working", "done", "missing"]);
