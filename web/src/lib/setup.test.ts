import { describe, expect, it } from "vitest";
import { body, current, joinRef, models, move, show, splitRef, stage, value } from "./setup";
import type { Setup } from "./types";

const s: Setup = {
  difficulty: [{ level: "hard", types: ["opus:high"] }, { level: "medium", types: ["deepseek"] }, { level: "simple", types: [] }],
  types: [],
  lanes: [{ lane: "L-01", type: "", agent: "pj-l1", state: "free" }],
  roles: [{ role: "reviewer", agent: "pj-rev", type: "terra", status: "" }],
  paused: null,
  lane_types: [
    { name: "opus", kind: "claude", model: "opus", effort: "medium", max: 4, efforts: ["low", "medium", "high"] },
    { name: "terra", kind: "codex", model: "gpt-6.1-terra", effort: "xhigh", max: 0, efforts: [] },
    { name: "luna", kind: "codex", model: "gpt-6-luna", effort: "", max: 0, efforts: [] },
  ],
  distribution: [],
  config_rev: "r1",
};

describe("staged setup edits", () => {
  it("reads every editable key", () => {
    expect(current(s, "difficulty.hard")).toEqual(["opus:high"]);
    expect(current(s, "lane_types.opus.max")).toBe(4);
    expect(current(s, "lane_types.terra.model")).toBe("gpt-6.1-terra");
    expect(current(s, "lanes.L-01")).toBe("");
    expect(current(s, "roles.reviewer.type")).toBe("terra");
    expect(current(s, "commands.check")).toBeUndefined();
  });
  it("stages a change and drops one that puts the value back", () => {
    let st = stage({}, s, "lane_types.opus.max", 2);
    st = stage(st, s, "difficulty.hard", ["opus:high", "terra"]);
    expect(value(st, s, "lane_types.opus.max")).toBe(2);
    expect(value(st, s, "lanes.L-01")).toBe("");
    st = stage(st, s, "lane_types.opus.max", 4);
    expect(Object.keys(st)).toEqual(["difficulty.hard"]);
    expect(body(st, "r1")).toEqual({ changes: [{ key: "difficulty.hard", value: ["opus:high", "terra"] }], base: "r1" });
  });
  it("moves within a list and keeps it at its ends", () => {
    expect(move(["a", "b", "c"], 2, -1)).toEqual(["a", "c", "b"]);
    expect(move(["a", "b"], 0, -1)).toEqual(["a", "b"]);
  });
  it("splits and joins type references", () => {
    expect(splitRef("opus:high")).toEqual(["opus", "high"]);
    expect(splitRef("terra")).toEqual(["terra", ""]);
    expect(joinRef("opus", "")).toBe("opus");
    expect(joinRef("", "high")).toBe("");
  });
  it("offers every model of the kind, never a select with only the current one", () => {
    expect(models(s, "claude", "opus")).toEqual(["opus", "sonnet", "haiku", "fable"]);
    expect(models(s, "codex", "gpt-6-luna")).toEqual(["gpt-6.1-terra", "gpt-6-luna"]);
    expect(models(s, "codex", "custom-x")).toContain("custom-x");
  });
  it("shows values for the review", () => {
    expect([show(["a", "b"]), show([]), show(""), show(0), show(3), show(undefined)]).toEqual(["a, b", "none", "unset", "0", "3", "—"]);
  });
});
