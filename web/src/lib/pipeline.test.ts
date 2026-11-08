import { describe, expect, it } from "vitest";
import { laneSteps, pipelineOf, queuedForLane, waitChips } from "./pipeline";
import { promoteLine } from "./promotion";
import type { Lane, Queue } from "./types";

const lane = (n: string, task: string | null, column: string): Lane => ({ lane: n, type: "", dir: "", branch: null, task,
  state: task ? "working" : "free", reason: null, done_age: null, dirty: false, warning: null, free: !task, column });

// Four lanes busy, two of their tasks in the merge loop (T-961: they keep their lanes), one dispatch waiting for a
// lane and one spec never dispatched: the owner's 11-07 picture.
const busy = [lane("L-01", "T-1", "working"), lane("L-02", "T-2", "testing"), lane("L-03", "T-3", "review"),
  lane("L-04", "T-4", "working")];
const q: Pick<Queue, "merge" | "waiting"> = {
  merge: [{ task: "T-2", stage: "gating" }, { task: "T-3", stage: "review" }],
  waiting: [{ task: "T-5", waits_on: "lane", difficulty: "medium", model: "opus" },
    { task: "T-6", waits_on: "never dispatched" }],
};
const names = ["Waiting", "Working", "Submitted", "Gate", "Review", "Merging"];

describe("the pipeline (T-986)", () => {
  it("keeps its six blocks with promotion on and off", () => {
    const promote = { branch: "main", sha: "abc", merges: 1, weight: 1, threshold: 6 };
    expect(promoteLine(promote)).not.toBe(""); // on: the line shows, the blocks stay six
    expect(pipelineOf(q, busy).map((c) => c.name)).toEqual(names);
    expect(promoteLine(undefined)).toBe(""); // off
    expect(pipelineOf(null, []).map((c) => c.name)).toEqual(names);
    expect(pipelineOf(null, []).every((c) => c.tasks.length === 0)).toBe(true);
  });
  it("puts a lane's task in its merge stage, not in Working too", () => {
    const cols = Object.fromEntries(pipelineOf(q, busy).map((c) => [c.name, c.tasks]));
    expect(cols.Working).toEqual(["T-1", "T-4"]);
    expect([cols.Gate, cols.Review, cols.Waiting]).toEqual([["T-2"], ["T-3"], ["T-5", "T-6"]]);
  });
  it("adds Held only while a task is held", () => {
    const held = pipelineOf({ merge: [{ task: "T-9", stage: "held" }], waiting: [] }, []);
    expect(held.map((c) => c.name)).toEqual([...names, "Held"]);
    expect(held[6].tasks).toEqual(["T-9"]);
  });
});

describe("the counts never take a task twice (T-986)", () => {
  it("queues only the dispatches with no lane yet", () => {
    expect(queuedForLane(q.waiting, busy).map((w) => w.task)).toEqual(["T-5"]); // not T-2/T-3, not never-dispatched
    expect(queuedForLane([{ task: "T-1", waits_on: "after" }], busy)).toEqual([]); // its task holds a lane
    expect(queuedForLane(undefined, busy)).toEqual([]);
  });
  it("says each busy lane's step from serve's phase", () => {
    expect(laneSteps(busy)).toBe("2 working · 1 testing · 1 review");
    expect(laneSteps([])).toBe("");
  });
});

describe("a queued item (T-986)", () => {
  it("has its difficulty and model as chips of their own, apart from the title", () => {
    expect(waitChips(q.waiting[0])).toEqual(["medium", "opus"]);
    expect(waitChips({ model: "opus" })).toEqual(["opus"]);
    expect(waitChips({})).toEqual([]);
  });
});

// T-1043: the queue's order is serve's (the view sends the waits in the scheduler's pass order): the page lists them as
// sent — T-20, dispatched first, before T-10 — never by task id.
describe("the queue's order (T-1043)", () => {
  it("lists the waiting dispatches as serve sent them", () => {
    const sent = { merge: [], waiting: [{ task: "T-20", waits_on: "lane" }, { task: "T-10", waits_on: "lane" }] };
    expect(pipelineOf(sent, []).find((c) => c.name === "Waiting")?.tasks).toEqual(["T-20", "T-10"]);
    expect(queuedForLane(sent.waiting, []).map((w) => w.task)).toEqual(["T-20", "T-10"]);
  });
});
