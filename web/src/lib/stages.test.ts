import { describe, expect, it } from "vitest";
import { gateOf, milestone, stageOf, taskOf } from "./stages";

const rec = (step: string, kind: string, line = "", outcome = "", pass = false) =>
  ({ seq: 1, ts: "", step, kind, line, outcome, pass });

describe("a task's stage", () => {
  // T-883: the view places the task (Task.step, wire.PhaseStep of serve's phase, T-788); the stepper shows that step.
  it("is the step the view places it on", () => {
    expect(stageOf({ phase: "gating", step: 3 })).toMatchObject({ i: 3, state: "gating" });
    expect(stageOf({ phase: "waiting", step: 0 })).toMatchObject({ i: 0, state: "waiting" });
  });
  it("is review for a green task, as the lanes row and the pipeline say", () => {
    expect(stageOf({ phase: "review", step: 4 })).toMatchObject({ i: 4, tone: "info", state: "review" });
  });
  it("is merged on the merged fact alone", () => {
    expect(stageOf({ phase: "publish", step: 5 }, [rec("merged", "fact")])).toMatchObject({ i: 7, state: "merged" });
  });
  // T-893: the task page's step of a task its phase places on none is the view's (the furthest its records reached).
  it("holds at the step the task page's view gives it otherwise", () => {
    expect(stageOf({ phase: "needs-you", step: 3 }, [rec("dispatch", "fact"), rec("gate.1", "result", "gate T-1 fail", "red")]))
      .toMatchObject({ i: 3, tone: "bad", state: "needs-you" });
    expect(stageOf({ phase: "held", step: 2 }, [rec("dispatch", "fact"), rec("submitted", "fact")])).toMatchObject({ i: 2, tone: "bad" });
    expect(stageOf({ step: 0 }, [rec("dispatch", "fact")])).toMatchObject({ i: 0, tone: "idle", state: "ended" });
  });
  it("places no step itself: a phase without the view's step is not placed", () => {
    expect(stageOf({ phase: "working" }).i).toBe(0); // a lane git calls working, serve down: no phase, no step
    expect(stageOf(undefined).state).toBe("");
  });
  it("reads a task's fields from the view by id, its id alone when the view has none", () => {
    const v = { tasks: { "T-1": { task: "T-1", title: "one", phase: "working", step: 1 } } };
    expect(taskOf(v, "T-1")).toMatchObject({ title: "one", step: 1 });
    expect(taskOf(v, "T-2")).toEqual({ task: "T-2" });
    expect(taskOf(undefined, null)).toEqual({ task: "" });
  });
  it("words records for the timeline and skips bookkeeping", () => {
    expect(milestone(rec("delivered", "fact"))?.line).toBe("job fetched by its agent");
    expect(milestone(rec("gate.2", "result", "gate T-1 pass (GREEN)", "green", true))?.tone).toBe("ok");
    expect(milestone(rec("gate.2", "result", "gate T-1 fail (passes 3 pass )", "red"))?.tone).toBe("bad"); // the view's pass, never the wording
    expect(milestone(rec("gate.2", "result", "check 3 pass, 1 fail"))?.tone).toBe("bad"); // nor the outcome's
    expect(milestone(rec("work.1", "intent"))).toBeNull();
  });
  // T-1105: the last gate's card follows the view's gate_pass, even when its line does not start with GREEN.
  it("shows the last gate from the view's gate_pass, with a line that does not start with GREEN", () => {
    const line = "check 25 pass, 0 fail";
    expect(gateOf({ gate_pass: true, gate_line: line })).toEqual({ pass: true, line });
    expect(gateOf({ gate_pass: false, gate_line: line })).toEqual({ pass: false, line });
    expect(gateOf(undefined)).toEqual({ pass: false, line: "" });
  });
});
