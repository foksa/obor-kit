import { describe, expect, it } from "vitest";
import { fleetRoles, projectState } from "./fleet";
import type { ProjectsEntry } from "./types";

// project is one /api/projects row: up, free and quiet unless a test says otherwise.
const project = (over: Partial<ProjectsEntry> = {}): ProjectsEntry => ({
  name: "pj", root: "/work/pj", up: true, busy: 0, free: 2, needs_you: 0, queue: 0, running: [], roles: [],
  background: 0, lanes: [], events: [], ...over,
});

const lead = { role: "lead", agent: "pj-lead", status: "working", age: 90 };

describe("the nav's project state", () => {
  it("is working when the lead works and every lane is free", () => {
    const p = project({ roles: [lead] });
    expect(p.busy).toBe(0);
    expect(projectState(p)).toBe("working");
  });
  it("is working when a role agent works", () => {
    expect(projectState(project({ roles: [{ role: "reviewer", agent: "pj-rev", status: "working" }] }))).toBe("working");
  });
  it("is working when a lane is busy", () => {
    expect(projectState(project({ busy: 1, free: 1 }))).toBe("working");
  });
  it("is free when every lane and agent is idle", () => {
    expect(projectState(project({ roles: [{ role: "lead", agent: "pj-lead", status: "idle" }] }))).toBe("free");
  });
  it("is needs-you over a busy lane and a working lead", () => {
    expect(projectState(project({ busy: 1, free: 1, needs_you: 2, roles: [lead] }))).toBe("needs-you");
  });
  it("is down over every other fact, and fail over down", () => {
    const down = project({ up: false, busy: 1, needs_you: 2, roles: [lead] });
    expect(projectState(down)).toBe("down");
    expect(projectState({ ...down, error: "serve: no answer" })).toBe("fail");
  });
});

describe("the fleet page's agents at work", () => {
  it("renders a lead entry as its own row, with its role name and agent", () => {
    const rows = fleetRoles([project({ name: "pj", roles: [lead, { role: "deep_dive", agent: "pj-deep", status: "working" }] })]);
    expect(rows.map((r) => r.label)).toEqual(["lead", "deep dive"]);
    expect(rows[0].project).toBe("pj");
    expect(rows[0].role.agent).toBe("pj-lead");
    expect(rows[0].role.age).toBe(90);
  });
});
