// A task's place in dispatched → working → submitted → gate → review → merging → merged, from serve's phase of it
// (T-788: serve computes it once, wire.Phase*; a lane with no task shows its lane state) or, once serve no longer has
// it, from its records; and a record as one line of its timeline.
import { tone, type Tone } from "./format";
import type { TaskRecord } from "./types";

export const TASK_STEPS = ["dispatched", "working", "submitted", "gate", "review", "merging", "merged"] as const;

// PHASE_STEP is serve's phase of a task as the index of the step it is on. Publish and its hold are merging, never
// merged: merged is the merged fact alone, every step done. needs-you, held and an ended task: their records say.
export const PHASE_STEP: Record<string, number> = {
  waiting: 0, dispatching: 0, working: 1, driving: 1, submitted: 2, gating: 3, "gate pending": 3, review: 4,
  reviewing: 4, publish: 5, merging: 5, "publish pending": 5,
};

// MERGE_HOLD is the phase of a task whose publish waits for a role: "merge hold: <role>" (T-718).
export const MERGE_HOLD = "merge hold: ";

const phaseStep = (ph: string): number | undefined =>
  ph.startsWith(MERGE_HOLD) ? PHASE_STEP.publish : PHASE_STEP[ph];

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

export function stageOf(phase: string | undefined, records: TaskRecord[] = []): Stage {
  if (records.some((r) => r.kind === "fact" && r.step === "merged")) {
    return { i: TASK_STEPS.length, tone: "done", state: "merged" };
  }
  const st = phase || "";
  const at = phaseStep(st);
  if (at !== undefined) return { i: at, tone: tone(st), state: st };
  // needs-you, held or ended: the furthest step its records reached, held there.
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
