// A task's place in dispatched → working → submitted → gate → review → merging → merged, from the step the view places
// it on (T-883: Task.step, wire.PhaseStep of serve's phase) or, when its phase places it on none (needs-you, held,
// ended, no phase), from its records; a task's fields from the view by id; and a record as one line of its timeline.
import { tone, type Tone } from "./format";
import type { Task, TaskRecord } from "./types";

export const TASK_STEPS = ["dispatched", "working", "submitted", "gate", "review", "merging", "merged"] as const;

// taskOf is the view's task id (project.tasks, T-883): the one place a page reads a task's title, phase, lane and step
// from, so they read the same on every section; a task the view does not know is its id alone.
export function taskOf(view: { tasks?: Record<string, Task> } | null | undefined, id: string | null | undefined): Task {
  return (id && view?.tasks?.[id]) || { task: id || "" };
}

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

// stageOf is t's stage: merged on the merged fact alone (every step done; publish is merging, never merged), else the
// step the view placed it on, else the furthest step its records reached, held there.
export function stageOf(t: { phase?: string; step?: number } | undefined, records: TaskRecord[] = []): Stage {
  if (records.some((r) => r.kind === "fact" && r.step === "merged")) {
    return { i: TASK_STEPS.length, tone: "done", state: "merged" };
  }
  const st = t?.phase || "";
  if (t?.step !== undefined) return { i: t.step, tone: tone(st), state: st };
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
