import { describe, expect, it } from "vitest";
import { milestone, stageOf } from "./stages";

const rec = (step: string, kind: string, line = "") => ({ seq: 1, ts: "", step, kind, line });

describe("a task's stage", () => {
  it("follows serve's state", () => {
    expect(stageOf("working").i).toBe(1);
    expect(stageOf("driving").i).toBe(1);
    expect(stageOf("green").i).toBe(4);
    expect(stageOf("published").i).toBe(5);
  });
  it("is merged on the merged fact alone", () => {
    expect(stageOf("published", [rec("merged", "fact")])).toMatchObject({ i: 7, state: "merged" });
  });
  it("holds at the furthest step its records reached otherwise", () => {
    expect(stageOf("needs-you", [rec("dispatch", "fact"), rec("submitted", "fact"), rec("gate.1", "result", "gate T-1 fail")]))
      .toMatchObject({ i: 3, tone: "bad", state: "needs-you" });
    expect(stageOf("", [rec("dispatch", "fact")]).state).toBe("ended");
  });
  it("words records for the timeline and skips bookkeeping", () => {
    expect(milestone(rec("delivered", "fact"))?.line).toBe("job fetched by its agent");
    expect(milestone(rec("gate.2", "result", "gate T-1 pass (GREEN)"))?.tone).toBe("ok");
    expect(milestone(rec("work.1", "intent"))).toBeNull();
  });
});
