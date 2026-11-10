// Wording: durations and times as a person says them, log lines without obor's prefixes, a state's tone.

export const now = () => Date.now() / 1000;

// unixOf is an RFC 3339 time as epoch seconds (0: none or unreadable).
export function unixOf(ts: string | undefined | null): number {
  const t = Date.parse(ts || "");
  return isNaN(t) ? 0 : t / 1000;
}

// dur is a duration in seconds as a person says it: 40s, 12m, 3h 05m, 2d 4h.
export function dur(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  if (s < 60) return s + "s";
  if (s < 3600) return Math.floor(s / 60) + "m";
  if (s < 86400) return Math.floor(s / 3600) + "h " + String(Math.floor((s % 3600) / 60)).padStart(2, "0") + "m";
  return Math.floor(s / 86400) + "d " + Math.floor((s % 86400) / 3600) + "h";
}

export type RelMode = "ago" | "for" | "in";

// relText is at (epoch seconds) relative to t: "3m ago", "for" an elapsed "3m", "in" a coming "in 3m".
export function relText(at: number, mode: RelMode = "ago", t = now()): string {
  const d = t - at;
  if (mode === "for") return dur(d);
  if (mode === "in") return "in " + dur(-d);
  return d < 45 ? "just now" : dur(d) + " ago";
}

export const clock = (at: number) =>
  new Date(at * 1000).toLocaleString([], {
    hour12: false, month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit",
  });

// human is a log line for a person: no [obor …] prefixes, a worktree path as its lane, other paths shortened.
export function human(line: string | undefined | null): string {
  let s = String(line || "").replace(/\[obor(?: [^\]]*)?\]\s*/g, "");
  s = s.replace(/(?:\/[\w.@+-]+){2,}\/?/g, (p) => {
    const m = p.match(/-wt\/([\w.-]+)(\/.*)?$/);
    if (m) return m[1] === "merge" ? "the merge worktree" + (m[2] || "") : m[1] + (m[2] || "");
    const segs = p.replace(/\/$/, "").split("/");
    return segs.length > 3 ? "…/" + segs.slice(-2).join("/") : p;
  });
  return s.trim();
}

export type Tone = "ok" | "wait" | "bad" | "idle" | "info" | "done" | "accent";

// tone is a state's color: green working, amber waiting, red needs-you or failed, blue in the merge loop, violet
// merged, grey free or idle.
export function tone(state: string | undefined | null): Tone {
  const s = String(state || "").toLowerCase();
  if (/held|needs-you|needs you|fail|red|blocked|conflict|down|missing|dropped|unknown/.test(s)) return "bad";
  if (/^driving/.test(s)) return "accent";
  if (/merged|published/.test(s)) return "done";
  if (/pending|waiting|queued|not fetched|stuck|review requested|ready|paused|dirty|^done/.test(s)) return "wait";
  if (/submitted|gating|gate|testing|candidate|merging|review|resolv|accepted|publish/.test(s)) return "info";
  if (/working|dispatching|running|green|live|up|brief/.test(s)) return "ok";
  return "idle";
}

export const roleName = (r: string | undefined) => String(r || "").replace(/_/g, " ");

// splitTasks is a watch's task field ("T-1, T-2" or "T-1 T-2") as its task ids.
export function splitTasks(field: string): string[] {
  return String(field || "").split(/[\s,]+/).map((t) => t.trim()).filter(Boolean);
}

// Part is a piece of a line: text, or a task id to link.
export type Part = { text: string; task?: string };

// parts is text split around its task ids, so a page links them without building HTML.
export function parts(text: string): Part[] {
  const out: Part[] = [];
  const re = /\bT-\d+\b/g;
  let last = 0;
  for (const m of String(text || "").matchAll(re)) {
    if (m.index! > last) out.push({ text: text.slice(last, m.index) });
    out.push({ text: m[0], task: m[0] });
    last = m.index! + m[0].length;
  }
  if (last < text.length) out.push({ text: text.slice(last) });
  return out;
}

// EVENT_KINDS is every event kind serve names (Event.kind, app.Kind*: internal/web's types_test), and EventKind is
// their union: the page's icons and filters switch on this set, never on the line's wording.
export const EVENT_KINDS = [
  "merge", "gate", "accept", "submit", "dispatch", "drive", "message", "serve", "problem", "other",
] as const;

export type EventKind = (typeof EVENT_KINDS)[number];

// eventKind is an event's kind: the one serve names; a bad one keeps it (the Problems filter reads bad too).
export function eventKind(kind: string | undefined, bad?: boolean): EventKind {
  const k = EVENT_KINDS.find((known) => known === kind);
  if (k) return k;
  return bad ? "problem" : "other";
}

// EVENT_FILTERS are the events list's filters: which kinds each shows (bad: the event is bad news, a gate that failed).
export const EVENT_FILTERS: Record<string, (k: EventKind, bad?: boolean) => boolean> = {
  All: () => true,
  Flow: (k) => ["merge", "gate", "accept", "submit", "dispatch", "drive"].includes(k),
  Messages: (k) => k === "message",
  Problems: (k, bad) => k === "problem" || !!bad,
};

// lineTo is an event "line to <agent>: <text>" as its agent and text (null: another kind of line).
export function lineTo(line: string): { agent: string; text: string } | null {
  const m = String(line || "").match(/^line to ([^:]+): ([\s\S]*)$/);
  return m ? { agent: m[1], text: m[2] } : null;
}
