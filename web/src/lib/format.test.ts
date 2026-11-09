import { describe, expect, it } from "vitest";
import { EVENT_FILTERS, EVENT_KINDS, dur, eventKind, human, lineTo, parts, relText, splitTasks, tone } from "./format";

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
  // T-1074: serve names every event's kind (app.Kind*); the page no longer reads it from the line's wording.
  it("takes each event's kind from serve", () => {
    expect(EVENT_KINDS.map((k) => eventKind(k))).toEqual([...EVENT_KINDS]);
    expect(eventKind("promotion", true)).toBe("promotion"); // bad news keeps its kind
    expect(eventKind("gate", true)).toBe("gate");
    expect(eventKind("what", false)).toBe("other");
    expect(eventKind("", true)).toBe("problem"); // no kind (older page's cache): bad still lands in Problems
  });
  // T-688, T-1074: the Problems filter shows bad news of any kind, and a problem that is not bad.
  it("shows bad news of any kind in Problems", () => {
    for (const k of EVENT_KINDS) expect(EVENT_FILTERS.Problems(k, true)).toBe(true);
    for (const k of EVENT_KINDS) expect(EVENT_FILTERS.Problems(k, false)).toBe(k === "problem");
    expect(EVENT_FILTERS.Problems("gate", false)).toBe(false);
    expect(EVENT_FILTERS.Problems("promotion", true)).toBe(true);
    expect(EVENT_FILTERS.Flow("promotion")).toBe(true);
  });
  it("reads a line to an agent as its agent and text", () => {
    expect(lineTo("line to aa-rev: verify RV-4")).toEqual({ agent: "aa-rev", text: "verify RV-4" });
    expect(lineTo("merged T-1")).toBeNull();
  });
});
