// The Setup page's staged edits (T-760, T-766): a change is a key (config.EditKeys: difficulty.<level>, roles.<role>,
// lanes) and its new value, kept until the owner commits them all at once (POST /api/config: one commit, all or
// nothing). They are made on the config revision the page read when the first was staged (base): serve refuses them
// once config.json is another file, so a tab never overwrites what another tab, the lead or a hand edit saved
// meanwhile.
import type { DispatchCount, Setup, SetupModel } from "./types";

export type Value = string | number | string[];
export type Staged = Record<string, Value>;

// current is key's value in s (undefined: s has no such field).
export function current(s: Setup, key: string): Value | undefined {
  const p = key.split(".");
  if (p[0] === "difficulty" && p.length === 2) return s.difficulty.find((d) => d.level === p[1])?.types;
  if (p[0] === "roles" && p.length === 2) return s.roles.find((r) => r.role === p[1])?.type;
  if (key === "lanes") return s.lanes.length;
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
  if (v === "") return "unset";
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

// splitRef is a model ref as its model and effort ("opus:high" → opus, high).
export function splitRef(ref: string): [string, string] {
  const i = ref.indexOf(":");
  return i < 0 ? [ref, ""] : [ref.slice(0, i), ref.slice(i + 1)];
}
export const joinRef = (model: string, effort: string) => (model && effort ? `${model}:${effort}` : model);

export interface RoleOption { value: string; label: string; disabled: boolean }
export const NO_MODEL = "no model — choose one";

// roleModelOptions is the options of a role's model select: the models, behind a disabled placeholder while the role
// has none (a role always names a model, so there is never an empty choice, T-769).
export function roleModelOptions(names: string[], model: string): RoleOption[] {
  const opts = names.map((n) => ({ value: n, label: n, disabled: false }));
  return model ? opts : [{ value: "", label: NO_MODEL, disabled: true }, ...opts];
}

// chip is a model ref as a level's chip shows it: "opus · high", or the model alone at its kind's default effort.
export function chip(ref: string): string {
  const [m, e] = splitRef(ref);
  return e ? `${m} · ${e}` : m;
}

// addRef is a level's list with ref appended; a ref the list already has: the list as it is (dispatch tries it once).
export const addRef = (list: string[], ref: string) => (!ref || list.includes(ref) ? list : [...list, ref]);

// model is the setup's model named name (undefined: none such).
export const model = (s: Setup, name: string): SetupModel | undefined => s.models.find((m) => m.name === name);

// efforts is the efforts model name's kind takes (none: the kind sets its own).
export const efforts = (s: Setup, name: string) => model(s, name)?.efforts || [];

// usedBy is, by model name, what names it as the page shows the setup (staged edits in): its levels and roles. roleName
// is a role as the page labels it.
export function usedBy(staged: Staged, s: Setup, roleName: (r: string) => string): Record<string, string[]> {
  const u: Record<string, string[]> = {};
  const add = (ref: string, who: string) => {
    const m = splitRef(ref)[0];
    if (m && !(u[m] || []).includes(who)) u[m] = [...(u[m] || []), who];
  };
  for (const d of s.difficulty) for (const ref of (value(staged, s, "difficulty." + d.level) as string[]) || []) add(ref, d.level);
  for (const r of s.roles) add((value(staged, s, "roles." + r.role) as string) || "", roleName(r.role));
  return u;
}

// inUse is the models the Models card lists: those a level or role names, and any paused one (its pause still acts),
// in the config's order.
export const inUse = (s: Setup, used: Record<string, string[]>) => s.models.filter((m) => used[m.name] || m.paused);

// busyDropped is the lanes a lanes count of n drops that hold a task (serve refuses the change while one does: its
// setup answer's locked flag).
export const busyDropped = (s: Setup, n: number) => s.lanes.slice(Math.max(0, n)).filter((l) => l.locked).map((l) => l.lane);

// pauseBody is POST /api/pause's body for model's pause (reason, a duration or "until cleared") or its clear.
export function pauseBody(model: string, clear: boolean, reason = "", dur = "") {
  return clear ? { model, clear: true } : { model, reason: reason.trim(), for: dur === "until cleared" ? "" : dur };
}

// The distribution's ranges (T-1089): the last 7 days, and "kept", every dispatch the log still holds (serve's nightly
// prune deletes an ended task's records 30 days after its end, so it is no longer every dispatch ever made).
export const DIST_RANGES = ["7 days", "kept"];
export const distCount = (c: DispatchCount, range: string) => (range === "kept" ? c.all : c.recent);
