import { describe, expect, it } from "vitest";
import { mainOf, promoteWarning, stagingOf } from "./promotion";
import type { PromoteStatus } from "./types";

const base: PromoteStatus = { branch: "main", sha: "abcdef0123", at: 1762500000, merges: 2, tasks: ["T-1", "T-2"],
  weight: 3, threshold: 6 };

describe("the Staging column (T-977)", () => {
  it("is absent without a promotion: the page as before", () => {
    expect(stagingOf(undefined)).toBeNull();
    expect(stagingOf(null)).toBeNull();
    expect(mainOf(undefined)).toBeNull();
    expect(promoteWarning(undefined)).toBe("");
  });
  it("shows the tasks merged since with a weight bar n / threshold", () => {
    const s = stagingOf(base)!;
    expect(s.tasks).toEqual(["T-1", "T-2"]);
    expect([s.weight, s.threshold, s.fill]).toEqual([3, 6, 0.5]);
    expect([s.tone, s.state, s.red]).toEqual(["ok", "", undefined]);
    expect(stagingOf({ ...base, weight: 9 })!.fill).toBe(1); // over the threshold: a full bar
    expect(stagingOf({ ...base, threshold: 0 })!.fill).toBe(0);
  });
  it("says a running promotion's step: the full suite, then the wait for review RV-n", () => {
    const suite = stagingOf({ ...base, run: { sha: "h2", since: 1, step: "suite" } })!;
    expect([suite.state, suite.tone]).toEqual(["promoting: full suite", "info"]);
    const rv = stagingOf({ ...base, run: { sha: "h2", since: 1, step: "review", review: "RV-5" } })!;
    expect([rv.state, rv.tone]).toEqual(["waiting for review RV-5", "wait"]);
    expect(stagingOf({ ...base, run: { sha: "h2", since: 1, step: "review" } })!.state).toBe("waiting for review");
  });
  it("makes a RED promotion a bad card naming the tests and the merges since", () => {
    const s = stagingOf({ ...base, red: { sha: "1234567890", tests: "check 1 failed — TestX", at: 1, since: "h0",
      merges: ["T-1", "T-2"] } })!;
    expect([s.tone, s.state]).toEqual(["bad", "RED 1234567"]);
    expect(s.red).toEqual({ sha: "1234567", tests: "check 1 failed — TestX", merges: ["T-1", "T-2"] });
    expect(promoteWarning({ ...base, red: { sha: "1234567890", tests: "x", at: 1 } })).toBe("promotion RED 1234567");
  });
  it("marks main with its promoted sha and age", () => {
    expect(mainOf(base)).toEqual({ branch: "main", sha: "abcdef0", at: 1762500000 });
    expect(mainOf({ ...base, at: undefined })!.at).toBe(0);
  });
});
