// The project page's pipeline and its counts (T-986): the six blocks Waiting · Working · Submitted · Gate · Review ·
// Merging (Held only while a task is), the queue's count and the busy lanes' steps. Since T-961 a task in the merge
// loop still holds its lane, so the counts never add a task twice: the queue counts the dispatches with no lane yet,
// the lanes count every busy lane and say each one's step (serve's phase, Lane.column).
import type { Lane, MergeItem, Queue, WaitItem } from "./types";

// WAITS_NEVER is WaitItem.waits_on of a stored spec never dispatched (view.WaitsNever): no dispatch, so not queued.
export const WAITS_NEVER = "never dispatched";

// WAITS_LANE is WaitItem.waits_on of a dispatch waiting for a free lane of its level (view.WaitsLane): the only wait
// that queues for a lane (an --after dispatch waits for its tasks, T-1162).
export const WAITS_LANE = "lane";

export type Col = { name: string; tone: string; tasks: string[] };

// pipelineOf is the pipeline's blocks: the dispatches waiting, the lanes at work (a lane whose task is in the merge loop
// shows in its merge stage instead), then the merge loop's stages; Held only when a task is held.
export function pipelineOf(q: Pick<Queue, "merge" | "waiting"> | null | undefined, busy: Lane[]): Col[] {
  const merge = q?.merge || [];
  const at = (st: string) => merge.filter((m: MergeItem) => m.stage === st).map((m) => m.task);
  const work = busy.filter((l) => l.task && !merge.some((m) => m.task === l.task)).map((l) => l.task!);
  const cols: Col[] = [
    { name: "Waiting", tone: "wait", tasks: (q?.waiting || []).map((w: WaitItem) => w.task) },
    { name: "Working", tone: "ok", tasks: work },
    { name: "Submitted", tone: "info", tasks: at("submitted") },
    { name: "Gate", tone: "info", tasks: at("gating") },
    { name: "Review", tone: "info", tasks: at("review") },
    { name: "Merging", tone: "done", tasks: at("publish") },
  ];
  const held = at("held");
  if (held.length) cols.push({ name: "Held", tone: "bad", tasks: held });
  return cols;
}

// queuedForLane is the dispatches that wait with no lane yet: a waiting dispatch whose waits_on is WAITS_LANE (a spec
// never dispatched, WAITS_NEVER, and an --after dispatch, which waits for its tasks instead, wait for no lane, T-1162)
// and whose task holds no busy lane (a task in the merge loop holds its lane and is the lanes' count, never the
// queue's).
export function queuedForLane(waiting: WaitItem[] | null | undefined, busy: Lane[]): WaitItem[] {
  return (waiting || []).filter((w) => w.waits_on === WAITS_LANE && !busy.some((l) => l.task === w.task));
}

// laneSteps is the busy lanes by their task's step, in the order the lanes list them: "2 working · 1 testing".
export function laneSteps(busy: Lane[]): string {
  const n = new Map<string, number>();
  for (const l of busy) n.set(l.column || l.state, (n.get(l.column || l.state) || 0) + 1);
  return [...n].map(([step, c]) => c + " " + step).join(" · ");
}

// waitChips is a queued item's chips under its title: its difficulty and its model, each its own ("" left out).
export function waitChips(w: Pick<WaitItem, "difficulty" | "model">): string[] {
  return [w.difficulty || "", w.model || ""].filter(Boolean);
}
