import { describe, expect, it } from "vitest";
import { milestone, stageOf } from "./stages";

const rec = (step: string, kind: string, line = "") => ({ seq: 1, ts: "", step, kind, line });

describe("a task's stage", () => {
  // T-788: serve ships the phase (wire.Phase*); the stepper places each one, the lane's own word when it has no task.
  it("follows serve's phase", () => {
    const steps: Record<string, number> = {
      waiting: 0, dispatching: 0, working: 1, driving: 1, submitted: 2, gating: 3, "gate pending": 3, review: 4,
      reviewing: 4, publish: 5, merging: 5, "publish pending": 5, "merge hold: reviewer": 5, "merge hold: deep_dive": 5,
    };
    for (const [phase, i] of Object.entries(steps)) {
      expect(stageOf(phase), phase).toMatchObject({ i, state: phase });
    }
  });
  it("is review for a green task, as the lanes row and the pipeline say", () => {
    expect(stageOf("review")).toMatchObject({ i: 4, tone: "info", state: "review" });
  });
  it("colors the merge hold as waiting, not as the review its role's name contains", () => {
    expect(stageOf("merge hold: reviewer").tone).toBe("wait");
  });
  it("is merged on the merged fact alone", () => {
    expect(stageOf("publish", [rec("merged", "fact")])).toMatchObject({ i: 7, state: "merged" });
  });
  it("holds at the furthest step its records reached otherwise", () => {
    expect(stageOf("needs-you", [rec("dispatch", "fact"), rec("submitted", "fact"), rec("gate.1", "result", "gate T-1 fail")]))
      .toMatchObject({ i: 3, tone: "bad", state: "needs-you" });
    expect(stageOf("held", [rec("dispatch", "fact"), rec("submitted", "fact")])).toMatchObject({ i: 2, tone: "bad" });
    expect(stageOf("", [rec("dispatch", "fact")]).state).toBe("ended");
  });
  it("takes no serve state for a phase", () => {
    expect(stageOf("green").i).toBe(0);
    expect(stageOf("awaiting-accept").i).toBe(0);
  });
  it("words records for the timeline and skips bookkeeping", () => {
    expect(milestone(rec("delivered", "fact"))?.line).toBe("job fetched by its agent");
    expect(milestone(rec("gate.2", "result", "gate T-1 pass (GREEN)"))?.tone).toBe("ok");
    expect(milestone(rec("work.1", "intent"))).toBeNull();
  });
});
