import { describe, expect, it } from "vitest";
import { BRIEF_LINE, briefActive, briefBadge, briefCards, briefFirstLine, briefStage, briefText, briefTone } from "./brief";
import type { Role } from "./types";

const role = (over: Partial<Role> = {}, brief: Partial<NonNullable<Role["brief"]>> | null = {}): Role => ({
  role: "deep_dive",
  agent: "pj-deep-dive",
  ...(brief ? { brief: { to: "pj-deep-dive", since: 1, delivered: true, ...brief } } : {}),
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
  it("picks the stage: not delivered, working, done (and delivered before its pane starts)", () => {
    expect(briefStage(role({ status: "working" }, { delivered: false }))).toBe("not delivered");
    expect(briefStage(role({ status: "working" }, { worked: true }))).toBe("working");
    expect(briefStage(role({ status: "idle" }, { worked: true }))).toBe("done");
    expect(briefStage(role({ status: "idle" }))).toBe("delivered");
  });
  it("is active and green only while the agent works", () => {
    expect(briefActive(role({ status: "working" }))).toBe(true);
    expect(briefActive(role({ status: "idle" }, { worked: true }))).toBe(false);
    expect(briefActive(role({ status: "working" }, { delivered: false }))).toBe(false);
    expect(briefTone("working")).toBe("ok");
    expect(briefTone("not delivered")).toBe("wait");
    expect(briefTone("done")).toBe("done");
  });
  it("badges the fleet tile <role> <stage>", () => {
    expect(briefBadge(role({ status: "working" }))).toBe("deep dive working");
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
