import { describe, expect, it } from "vitest";
import { dur, eventKind, human, lineTo, parts, relText, splitTasks, tone } from "./format";

describe("wording", () => {
  it("says durations as a person does", () => {
    expect([dur(40), dur(12 * 60), dur(3 * 3600 + 5 * 60), dur(2 * 86400 + 4 * 3600)]).toEqual(["40s", "12m", "3h 05m", "2d 4h"]);
    expect(relText(100, "ago", 120)).toBe("just now");
    expect(relText(0, "ago", 600)).toBe("10m ago");
    expect(relText(700, "in", 100)).toBe("in 10m");
  });
  it("drops obor's prefixes and names a worktree path by its lane", () => {
    expect(human("[obor obor-kit] merged T-1 in /Users/x/Work/proj-wt/L-02/internal/a.go"))
      .toBe("merged T-1 in L-02/internal/a.go");
    expect(human("see /Users/x/Work/proj-wt/merge/x")).toBe("see the merge worktree/x");
    expect(human("log /a/b/c/d/e.log")).toBe("log …/d/e.log");
  });
  it("splits a watch's tasks and links task ids", () => {
    expect(splitTasks("T-1441, T-1442,T-1443")).toEqual(["T-1441", "T-1442", "T-1443"]);
    expect(splitTasks("T-1 T-2")).toEqual(["T-1", "T-2"]);
    expect(parts("fix T-1 then T-22.")).toEqual([{ text: "fix " }, { text: "T-1", task: "T-1" }, { text: " then " }, { text: "T-22", task: "T-22" }, { text: "." }]);
  });
  it("colors states", () => {
    expect(["working", "waiting", "needs-you", "merged", "gating", "free", "driving", "done", "unknown"].map(tone))
      .toEqual(["ok", "wait", "bad", "done", "info", "idle", "accent", "wait", "bad"]);
  });
  // T-971: a lane test is part of the merge loop (T-961): serve reports "testing" on the gate step and "test pending"
  // while its intent waits.
  it("colors a lane test like the merge loop's other stages", () => {
    expect(tone("testing")).toBe("info");
    expect(tone("candidate")).toBe("info");
    expect(tone("test pending")).toBe("wait");
    expect(tone("gating")).toBe("info");
  });
  it("sorts events by kind", () => {
    expect(eventKind("merged T-1 abc")).toBe("merge");
    expect(eventKind("gate T-1 pass (GREEN)")).toBe("gate");
    expect(eventKind("line to aa-rev: verify")).toBe("message");
    expect(eventKind("dispatched T-1 (D-02)")).toBe("dispatch");
    expect(eventKind("anything", true)).toBe("problem");
    expect(lineTo("line to aa-rev: verify RV-4")).toEqual({ agent: "aa-rev", text: "verify RV-4" });
    expect(lineTo("merged T-1")).toBeNull();
  });
  // T-971: the lane test's lines are the gate's other half (T-961), and a pass's count ("0 fail") is no problem.
  it("reads a lane test's line as the gate's, with a count no problem", () => {
    expect(eventKind("test T-1 pass (GREEN T-1 (33 pass, 0 fail))")).toBe("gate");
    expect(eventKind("test T-1 red (RED T-1 (1 fail))", true)).toBe("problem");
    expect(eventKind("gate T-1 pass (GREEN T-1 (33 pass, 0 fail))")).toBe("gate");
    expect(eventKind("check 33 pass, 0 fail")).toBe("other");
  });
});
