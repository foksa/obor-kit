import { describe, expect, it } from "vitest";
import { render } from "svelte/server";
import Note from "../ui/Note.svelte";
import { fleetRoles, projectState, waitingText } from "./fleet";
import type { ProjectsEntry } from "./types";

// project is one /api/projects row: up, free and quiet unless a test says otherwise.
const project = (over: Partial<ProjectsEntry> = {}): ProjectsEntry => ({
  name: "pj", root: "/work/pj", up: true, busy: 0, working: 0, free: 2, needs_you: 0, queue: 0, running: [],
  roles: [], waiting: [], background: 0, lanes: [], events: [], ...over,
});

const lead = { role: "lead", agent: "pj-lead", status: "working", age: 90 };

describe("the nav's project state", () => {
  it("is working when the lead works and every lane is free", () => {
    const p = project({ working: 1, roles: [lead] });
    expect(p.busy).toBe(0);
    expect(projectState(p)).toBe("working");
  });
  it("is working when a role agent works", () => {
    expect(projectState(project({ working: 1, roles: [{ role: "reviewer", agent: "pj-rev", status: "working" }] }))).toBe("working");
  });
  it("is working when a lane is busy", () => {
    expect(projectState(project({ busy: 1, free: 1 }))).toBe("working");
  });
  it("is free when every lane and agent is idle", () => {
    expect(projectState(project({ roles: [{ role: "lead", agent: "pj-lead", status: "idle" }] }))).toBe("free");
  });
  it("is needs-you over a busy lane and a working lead", () => {
    expect(projectState(project({ busy: 1, working: 1, free: 1, needs_you: 2, roles: [lead] }))).toBe("needs-you");
  });
  it("is down over every other fact, and fail over down", () => {
    const down = project({ up: false, busy: 1, working: 1, needs_you: 2, roles: [lead] });
    expect(projectState(down)).toBe("down");
    expect(projectState({ ...down, error: "serve: no answer" })).toBe("fail");
  });
});

describe("the agents waiting for you", () => {
  const blocked = { agent: "pj-lead", role: "lead", since: 1000 };
  it("make a project with nothing else in it needs-you, not free", () => {
    const quiet = project();
    expect(projectState(quiet)).toBe("free");
    const p = project({ waiting: [blocked] });
    expect(p.needs_you).toBe(0);
    expect(projectState(p)).toBe("needs-you");
  });
  it("make a working project needs-you", () => {
    expect(projectState(project({ busy: 1, free: 1, working: 1, roles: [lead], waiting: [blocked] }))).toBe("needs-you");
  });
  it("render the card's line with the agent and its age", () => {
    expect(waitingText(blocked, 1021)).toBe("waiting for you: pj-lead 21s");
    expect(waitingText({ agent: "pj-l1", role: "worker", lane: "L-01", since: 1000 }, 1300)).toBe("waiting for you: pj-l1 5m");
  });
  it("render the line without an age when since is unknown", () => {
    expect(waitingText({ agent: "pj-rev", role: "reviewer" }, 1300)).toBe("waiting for you: pj-rev");
  });
  it("show the pane's note as one muted line, its full text in the title, markup as text", () => {
    const { body } = render(Note, { props: { text: "question: <b>Trust</b> this folder?" } });
    expect(body).toContain('class="note muted small ell');
    expect(body).toContain("question: &lt;b>Trust&lt;/b> this folder?</span>");
    expect(body).toContain('title="question: &lt;b>Trust&lt;/b> this folder?"');
    expect(body).not.toContain("<b>");
  });
  it("show no note when the pane has none", () => {
    expect(render(Note, { props: { text: "" } }).body).not.toContain("note");
    expect(render(Note, { props: {} }).body).not.toContain("note");
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
