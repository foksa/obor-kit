import { describe, expect, it } from "vitest";
import {
  BRIEF_LINE, briefActive, briefBadge, briefBusy, briefCards, briefFirstLine, briefFull, briefStage, briefText, briefTone,
} from "./brief";
import type { Role } from "./types";

const role = (over: Partial<Role> = {}, brief: Partial<NonNullable<Role["brief"]>> | null = {}): Role => ({
  role: "deep_dive",
  agent: "pj-deep-dive",
  ...(brief ? { brief: { to: "pj-deep-dive", since: 1, delivered: true, stage: "working", ...brief } } : {}),
  ...over,
});

describe("a briefed role agent's card", () => {
  it("cards every briefed role but the reviewer and the integrator, none without a brief", () => {
    const rs = [
      role({ role: "reviewer", agent: "pj-rev" }),
      role({ role: "integrator", agent: "pj-int" }),
      role({ role: "deep_dive" }),
      role({ role: "advisor", agent: "pj-adv" }, null),
      role({ role: "researcher", agent: "pj-res" }),
    ];
    expect(briefCards(rs).map((r) => r.role)).toEqual(["deep_dive", "researcher"]);
  });
  it("reads the stage serve sends, never one of its own from the pane's status", () => {
    expect(briefStage(role({ status: "idle" }, { stage: "not delivered", delivered: false }))).toBe("not delivered");
    expect(briefStage(role({ status: "idle" }, { stage: "working" }))).toBe("working");
    expect(briefStage(role({ status: "blocked" }, { stage: "waiting", worked: true }))).toBe("waiting");
    expect(briefStage(role({ status: "idle" }, { stage: "done", worked: true }))).toBe("done");
    expect(briefStage(role({ status: undefined }, { stage: "working", worked: true }))).toBe("working");
    expect(briefStage(role({}, null))).toBe("");
  });
  it("is active and green only while the agent works, needs-you while it waits", () => {
    expect(briefActive(role({ status: "working" }, { stage: "working" }))).toBe(true);
    expect(briefActive(role({ status: "idle" }, { stage: "done", worked: true }))).toBe(false);
    expect(briefActive(role({ status: "blocked" }, { stage: "waiting", worked: true }))).toBe(false);
    expect(briefTone("working")).toBe("ok");
    expect(briefTone("waiting")).toBe("bad");
    expect(briefTone("not delivered")).toBe("wait");
    expect(briefTone("done")).toBe("done");
    expect(briefTone("")).toBe("idle");
  });
  it("badges the fleet tile <role> <stage> while working or waiting, not before or after", () => {
    expect(briefBadge(role({ status: "working" }, { stage: "working" }))).toBe("deep dive working");
    expect(briefBadge(role({ status: "blocked" }, { stage: "waiting" }))).toBe("deep dive waiting");
    expect(briefBusy(role({}, { stage: "working" }))).toBe(true);
    expect(briefBusy(role({}, { stage: "waiting" }))).toBe(true);
    expect(briefBusy(role({}, { stage: "done" }))).toBe(false);
    expect(briefBusy(role({}, { stage: "not delivered", delivered: false }))).toBe(false);
  });
  it("marks a brief serve clipped with an ellipsis, a whole one not", () => {
    const b = { to: "pj-deep-dive", since: 1, delivered: true, stage: "working" };
    expect(briefFull({ ...b, text: "[obor pj-lead] Deep dive\nread the pl", text_clipped: true })).toBe("Deep dive\nread the pl…");
    expect(briefFull({ ...b, text: "[obor pj-lead] Deep dive\nread the plan" })).toBe("Deep dive\nread the plan");
    expect(briefFull({ ...b, text_clipped: true })).toBe("");
    expect(briefFull(undefined)).toBe("");
    expect(briefFirstLine(briefFull({ ...b, text: "[obor pj-lead] one long first li", text_clipped: true }))).toBe("one long first li…");
  });
  it("shows the brief's first line without its sender prefix, cut to one line", () => {
    expect(briefText("[obor pj-lead] Deep dive v0.45\nread the plan")).toBe("Deep dive v0.45\nread the plan");
    expect(briefFirstLine("[obor pj-lead] Deep dive v0.45\nread the plan")).toBe("Deep dive v0.45");
    expect(briefFirstLine("[obor pj-lead] \n\n  second line first")).toBe("second line first");
    expect(briefFirstLine(undefined)).toBe("");
    const long = "x".repeat(BRIEF_LINE + 20);
    const cut = briefFirstLine(long);
    expect(Array.from(cut)).toHaveLength(BRIEF_LINE);
    expect(cut.endsWith("…")).toBe(true);
    expect(briefFirstLine("é".repeat(10), 5)).toBe("éééé…");
    expect(briefFirstLine("exactly", 7)).toBe("exactly");
  });
});
