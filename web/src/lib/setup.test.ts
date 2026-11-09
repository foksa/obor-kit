import { describe, expect, it } from "vitest";
import {
  addRef, body, busyDropped, chip, current, DIST_RANGES, distCount, efforts, inUse, joinRef, move, NO_MODEL, roleModelOptions, pauseBody, show, splitRef, stage, usedBy, value,
} from "./setup";
import type { Setup } from "./types";

const CLAUDE = ["low", "medium", "high", "xhigh", "max"];
const s: Setup = {
  difficulty: [{ level: "hard", types: ["opus:high"] }, { level: "medium", types: ["deepseek", "sonnet:medium"] }, { level: "simple", types: [] }],
  types: [],
  models: [
    { name: "opus", kind: "claude", model: "opus", builtin: true, efforts: CLAUDE, paused: null },
    { name: "sonnet", kind: "claude", model: "sonnet", builtin: true, efforts: CLAUDE, paused: null },
    { name: "haiku", kind: "claude", model: "haiku", builtin: true, efforts: CLAUDE, paused: { until: null, reason: "quota", by: "owner" } },
    { name: "fable", kind: "claude", model: "fable", builtin: true, efforts: CLAUDE, paused: null },
    { name: "deepseek", kind: "opencode", model: "opencode-go/deepseek", builtin: true, efforts: [], paused: null },
    { name: "terra", kind: "codex", model: "gpt-6.1-terra", builtin: false, efforts: ["low", "medium", "high", "xhigh"], paused: null },
  ],
  kinds: [{ name: "claude", args: [], job_note: false }],
  lanes: [
    { lane: "L-01", type: "", agent: "pj-l-01", state: "free" },
    { lane: "L-02", type: "opus:high", agent: "pj-l-02", state: "working", locked: true },
    { lane: "L-03", type: "", agent: "pj-l-03", state: "free" },
  ],
  roles: [{ role: "reviewer", agent: "pj-rev", type: "terra:xhigh", status: "" }, { role: "lead", agent: "pj-lead", type: "", status: "" }],
  paused: null,
  distribution: [],
  config_rev: "r1",
};
const name = (r: string) => r.toUpperCase();

describe("staged setup edits", () => {
  it("reads every editable key: a level's refs, a role's ref, the lanes count", () => {
    expect(current(s, "agents.difficulty.hard")).toEqual(["opus:high"]);
    expect(current(s, "agents.roles.reviewer")).toBe("terra:xhigh");
    expect(current(s, "agents.roles.lead")).toBe("");
    expect(current(s, "lanes.count")).toBe(3);
    expect(current(s, "lane_types.opus.max")).toBeUndefined();
    expect(current(s, "agents.roles.reviewer.type")).toBeUndefined();
    expect(current(s, "difficulty.hard")).toBeUndefined(); // shape 1's names are no keys (T-1126)
    expect(current(s, "roles.reviewer")).toBeUndefined();
    expect(current(s, "lanes")).toBeUndefined();
    expect(current(s, "commands.check")).toBeUndefined();
  });
  it("stages a change and drops one that puts the value back", () => {
    let st = stage({}, s, "lanes.count", 2);
    st = stage(st, s, "agents.difficulty.hard", ["opus:high", "terra"]);
    expect(value(st, s, "lanes.count")).toBe(2);
    expect(value(st, s, "agents.roles.reviewer")).toBe("terra:xhigh");
    st = stage(st, s, "lanes.count", 3);
    expect(Object.keys(st)).toEqual(["agents.difficulty.hard"]);
    expect(body(st, "r1")).toEqual({ changes: [{ key: "agents.difficulty.hard", value: ["opus:high", "terra"] }], base: "r1" });
  });
  it("moves within a list and keeps it at its ends", () => {
    expect(move(["a", "b", "c"], 2, -1)).toEqual(["a", "c", "b"]);
    expect(move(["a", "b"], 0, -1)).toEqual(["a", "b"]);
  });
  it("splits and joins model refs", () => {
    expect(splitRef("opus:high")).toEqual(["opus", "high"]);
    expect(splitRef("terra")).toEqual(["terra", ""]);
    expect(joinRef("opus", "")).toBe("opus");
    expect(joinRef("", "high")).toBe("");
  });
  it("shows values for the review", () => {
    expect([show(["a", "b"]), show([]), show(""), show(0), show(3), show(undefined)]).toEqual(["a, b", "none", "unset", "0", "3", "—"]);
  });
});

describe("the Setup page's controls", () => {
  it("labels a level's chip as model · effort, the model alone at its kind's effort", () => {
    expect(chip("opus:high")).toBe("opus · high");
    expect(chip("deepseek")).toBe("deepseek");
  });
  it("adds a ref to a level once, and nothing for no model", () => {
    const list = ["deepseek", "sonnet:medium"];
    expect(addRef(list, "opus:high")).toEqual(["deepseek", "sonnet:medium", "opus:high"]);
    expect(addRef(list, "sonnet")).toEqual(["deepseek", "sonnet:medium", "sonnet"]); // another effort is another ref
    expect(addRef(list, "sonnet:medium")).toBe(list);
    expect(addRef(list, "")).toBe(list);
  });
  it("offers a model's kind's efforts, none for a kind that sets its own or an unknown model", () => {
    expect(efforts(s, "terra")).toEqual(["low", "medium", "high", "xhigh"]);
    expect(efforts(s, "deepseek")).toEqual([]);
    expect(efforts(s, "nope")).toEqual([]);
  });
  it("names what uses each model, staged edits in", () => {
    const u = usedBy({}, s, name);
    expect(u).toEqual({ opus: ["hard"], deepseek: ["medium"], sonnet: ["medium"], terra: ["REVIEWER"] });
    const st = stage(stage({}, s, "agents.roles.lead", "opus:medium"), s, "agents.difficulty.hard", []);
    expect(usedBy(st, s, name).opus).toEqual(["LEAD"]);
  });
  it("lists the models in use and a paused one, in the config's order", () => {
    expect(inUse(s, usedBy({}, s, name)).map((m) => m.name)).toEqual(["opus", "sonnet", "haiku", "deepseek", "terra"]);
  });
  it("names the lanes a lower count drops that hold a task", () => {
    expect(busyDropped(s, 3)).toEqual([]);
    expect(busyDropped(s, 2)).toEqual([]); // L-03 is free
    expect(busyDropped(s, 1)).toEqual(["L-02"]);
    expect(busyDropped(s, 0)).toEqual(["L-02"]);
  });
  it("posts a model's pause with its reason and duration, and its clear", () => {
    expect(pauseBody("sonnet", false, " quota ", "3h")).toEqual({ model: "sonnet", reason: "quota", for: "3h" });
    expect(pauseBody("sonnet", false, "quota", "until cleared")).toEqual({ model: "sonnet", reason: "quota", for: "" });
    expect(pauseBody("sonnet", true)).toEqual({ model: "sonnet", clear: true });
    expect(pauseBody("", false, "lunch", "1h")).toEqual({ model: "", reason: "lunch", for: "1h" });
  });
});

describe("a role's model select", () => {
  it("offers a disabled placeholder first and no empty option while the role has no model", () => {
    const [none] = splitRef(current(s, "agents.roles.lead") as string);
    expect(none).toBe(""); // precondition: the lead has no model
    const opts = roleModelOptions(["opus", "sonnet"], none);
    expect(opts[0]).toEqual({ value: "", label: NO_MODEL, disabled: true });
    expect(opts.slice(1).map((o) => o.value)).toEqual(["opus", "sonnet"]);
    expect(opts.slice(1).some((o) => o.value === "" || o.disabled)).toBe(false);
  });
  it("offers only the models once the role names one", () => {
    const opts = roleModelOptions(["opus", "sonnet"], "opus");
    expect(opts.map((o) => o.value)).toEqual(["opus", "sonnet"]);
    expect(opts.some((o) => o.value === "" || o.label === NO_MODEL)).toBe(false);
  });
});

describe("the distribution's ranges (T-1089)", () => {
  it("labels the long range by what it counts: the dispatches the log still keeps, not every one ever made", () => {
    expect(DIST_RANGES).toEqual(["7 days", "kept"]);
    const c = { level: "hard", type: "opus:high", recent: 2, all: 5 };
    expect(distCount(c, "7 days")).toBe(2);
    expect(distCount(c, "kept")).toBe(5);
  });
});
