// A task's place in dispatched → working → submitted → gate → review → merging → merged, from the step the view places
// it on (Task.step, wire.PhaseStep of serve's phase; on a task's page, when its phase places it on none —
// needs-you, held, ended, no phase — the furthest step its records reached); a task's fields from the view by id; and a
// record as one line of its timeline.
import { tone, type Tone } from "./format";
import type { Task, TaskRecord } from "./types";

export const TASK_STEPS = ["dispatched", "working", "submitted", "gate", "review", "merging", "merged"] as const;

// taskOf is the view's task id (project.tasks): the one place a page reads a task's title, phase, lane and step
// from, so they read the same on every section; a task the view does not know is its id alone.
export function taskOf(view: { tasks?: Record<string, Task> } | null | undefined, id: string | null | undefined): Task {
  return (id && view?.tasks?.[id]) || { task: id || "" };
}

export interface Stage {
  i: number; // the step it is on (TASK_STEPS.length: merged, every step done)
  tone: Tone;
  state: string;
}

// stageOf is t's stage: merged on the merged fact alone (every step done; publish is merging, never merged), else the
// step the view placed it on (0: none), in its phase's tone ("ended" when it has none but records).
export function stageOf(t: { phase?: string; step?: number } | undefined, records: TaskRecord[] = []): Stage {
  if (records.some((r) => r.kind === "fact" && r.step === "merged")) {
    return { i: TASK_STEPS.length, tone: "done", state: "merged" };
  }
  const st = t?.phase || "";
  return { i: t?.step ?? 0, tone: st ? tone(st) : "idle", state: st || (records.length ? "ended" : "") };
}

// gateOf is a task's last gate as its card shows it: the view's gate_pass and gate_line, never the line's
// wording.
export function gateOf(t: { gate_pass?: boolean; gate_line?: string } | null | undefined): { pass: boolean; line: string } {
  return { pass: t?.gate_pass === true, line: t?.gate_line || "" };
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
  if (base === "gate") return m(rec.pass ? "ok" : "bad", rec.line || "gate");
  if (method === "accept") return m("ok", "accepted");
  if (method === "sendback") return m("wait", rec.line || "sent back");
  if (base === "review" || base === "resolve") return m("idle", base === "review" ? "review job sent" : "resolve job sent");
  if (base === "work" || base === "fix") return m("idle", base + " job sent");
  return rec.line ? m("idle", rec.line) : null;
}
