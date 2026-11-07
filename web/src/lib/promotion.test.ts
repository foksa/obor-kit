import { describe, expect, it } from "vitest";
import { mainOf, promoteLine, promoteState, promoteWarning, promoteWarningDetail } from "./promotion";
import type { PromoteStatus } from "./types";

const T = 1762500000;
const base: PromoteStatus = { branch: "main", sha: "3eaaa98123", at: T - 2 * 3600 - 300, merges: 2, tasks: ["T-1", "T-2"],
  weight: 4, threshold: 6 };

describe("the promotion line (T-986)", () => {
  it("is absent without a promotion: no line, no warning, no marker", () => {
    expect(promoteLine(undefined, T)).toBe("");
    expect(promoteLine(null, T)).toBe("");
    expect(promoteState(undefined)).toBe("");
    expect(promoteWarning(undefined)).toBe("");
    expect(promoteWarningDetail(undefined)).toBe("");
    expect(mainOf(undefined)).toBeNull();
  });
  it("says staging's weight against the threshold and main's sha with its age when idle", () => {
    expect(promoteLine(base, T)).toBe("staging 4/6 · main 3eaaa98 (2h 05m ago)");
    expect(promoteLine({ ...base, at: undefined }, T)).toBe("staging 4/6 · main 3eaaa98"); // no promotion recorded
    expect(promoteLine({ ...base, sha: "", at: undefined }, T)).toBe("staging 4/6 · main —");
  });
  it("adds a running promotion's step: the full suite, then the wait for review RV-n", () => {
    expect(promoteLine({ ...base, run: { sha: "h2", since: 1, step: "suite" } }, T))
      .toBe("staging 4/6 · main 3eaaa98 (2h 05m ago) · promoting: full suite");
    expect(promoteLine({ ...base, run: { sha: "h2", since: 1, step: "review", review: "RV-5" } }, T))
      .toBe("staging 4/6 · main 3eaaa98 (2h 05m ago) · waiting for review RV-5");
    expect(promoteState({ ...base, run: { sha: "h2", since: 1, step: "review" } })).toBe("waiting for review");
  });
  it("makes a RED promotion the project's warning, not a state on the line", () => {
    const red = { ...base, run: { sha: "h2", since: 1, step: "suite" },
      red: { sha: "1234567890", tests: "check 1 failed — TestX", at: 1, merges: ["T-1", "T-2"] } };
    expect(promoteWarning(red)).toBe("promotion RED 1234567");
    expect(promoteWarningDetail(red)).toBe("check 1 failed — TestX · merges since: T-1, T-2");
    expect(promoteLine(red, T)).toBe("staging 4/6 · main 3eaaa98 (2h 05m ago)");
    expect(promoteWarning(base)).toBe("");
  });
  it("marks main on the fleet with its promoted sha and age", () => {
    expect(mainOf(base)).toEqual({ branch: "main", sha: "3eaaa98", at: T - 7500 });
    expect(mainOf({ ...base, at: undefined })!.at).toBe(0);
  });
});
