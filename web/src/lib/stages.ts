// A task's place in dispatched → working → submitted → gate → review → merging → merged, from serve's state of it or,
// once serve no longer has it, from its records; and a record as one line of its timeline.
import { tone, type Tone } from "./format";
import type { TaskRecord } from "./types";

export const TASK_STEPS = ["dispatched", "working", "submitted", "gate", "review", "merging", "merged"] as const;

// STATE_STEP is serve's task state as the index of the step it is on. Accepted and published are merging (the
// publish), never merged: merged is the merged fact alone, every step done.
const STATE_STEP: Record<string, number> = {
  waiting: 0, dispatched: 0, driving: 1, working: 1, submitted: 2, resolved: 3, moved: 3, red: 3, green: 4,
  "awaiting-accept": 4, conflict: 4, resolving: 4, accepted: 5, published: 5,
};

// recordStep is the step a record shows the task reached (-1: none).
function recordStep(r: TaskRecord): number {
  const step = r.step || "";
  if (r.kind === "fact") return ({ dispatch: 0, delivered: 1, sendback: 1, submitted: 2 } as Record<string, number>)[step] ?? -1;
  if (r.kind !== "result") return -1;
  if (step.startsWith("accept") || step.startsWith("publish")) return 5;
  if (step.startsWith("gate")) return / pass /.test(r.line || "") ? 4 : 3;
  if (step.startsWith("review")) return 4;
  return -1;
}

export interface Stage {
  i: number; // the step it is on (TASK_STEPS.length: merged, every step done)
  tone: Tone;
  state: string;
}

export function stageOf(state: string | undefined, records: TaskRecord[] = []): Stage {
  if (records.some((r) => r.kind === "fact" && r.step === "merged")) {
    return { i: TASK_STEPS.length, tone: "done", state: "merged" };
  }
  const st = state || "";
  if (st in STATE_STEP) return { i: STATE_STEP[st], tone: tone(st), state: st };
  // needs-you, fail or ended: the furthest step its records reached, held there.
  const i = records.reduce((m, r) => Math.max(m, recordStep(r)), 0);
  return { i, tone: st ? tone(st) : "idle", state: st || (records.length ? "ended" : "") };
}

// milestone is a record as one line of the task's timeline (null: bookkeeping a person need not read).
export function milestone(rec: TaskRecord): { tone: Tone; line: string } | null {
  const base = rec.step.replace(/\.\d+$/, "");
  const method = rec.step.split(".")[0];
  const m = (t: Tone, line: string) => ({ tone: t, line });
  if (rec.kind === "fact") {
    switch (rec.step) {
      case "dispatch": return m("ok", rec.line || "dispatched");
      case "waiting": return m("wait", "waits to be dispatched");
      case "delivered": return m("idle", "job fetched by its agent");
      case "submitted": return m("info", "submitted");
      case "sendback": return m("wait", rec.line || "sent back");
      case "needs-you": return m("bad", rec.line || "needs you");
      case "merged": return m("done", rec.line || "merged");
      case "target.moved": return m("idle", "target moved: the gate runs again");
    }
    return rec.line ? m("idle", rec.line) : null;
  }
  if (rec.kind !== "result") return null;
  if (base === "gate") return m(/ pass /.test(rec.line || "") ? "ok" : "bad", rec.line || "gate");
  if (method === "accept") return m("ok", "accepted");
  if (method === "sendback") return m("wait", rec.line || "sent back");
  if (base === "review" || base === "resolve") return m("idle", base === "review" ? "review job sent" : "resolve job sent");
  if (base === "work" || base === "fix") return m("idle", base + " job sent");
  return rec.line ? m("idle", rec.line) : null;
}
