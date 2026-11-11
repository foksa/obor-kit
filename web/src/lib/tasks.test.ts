import { describe, expect, it } from "vitest";
import { ALL, NONE, counts, emptyFilter, filterRows, matches, modelOf } from "./tasks";
import type { TaskRow } from "./types";

const rows: TaskRow[] = [
  { task: "T-30", title: "Tasks page in obor web", state: "running", difficulty: "medium", model: "opus", ran: "opus:high" },
  { task: "T-29", title: "Drive names resolve", state: "merged", difficulty: "hard", ran: "opus:high" },
  { task: "T-28", title: "Web docs page", state: "merged", difficulty: "medium", ran: "sonnet" },
  { task: "T-27", title: "A spec alone", state: "never dispatched", difficulty: "simple" },
  { task: "T-260", title: "Old one", state: "dropped" },
];
const ids = (rs: TaskRow[]) => rs.map((r) => r.task);

describe("the tasks list", () => {
  it("searches id and title, every word, case aside", () => {
    expect(ids(filterRows(rows, { ...emptyFilter(), q: "web" }))).toEqual(["T-30", "T-28"]);
    expect(ids(filterRows(rows, { ...emptyFilter(), q: "WEB docs" }))).toEqual(["T-28"]);
    expect(ids(filterRows(rows, { ...emptyFilter(), q: "t-26" }))).toEqual(["T-260"]);
    expect(matches(rows[0], "")).toBe(true);
    expect(ids(filterRows(rows, { ...emptyFilter(), q: "nothing like it" }))).toEqual([]);
  });
  it("filters by state, difficulty and model together, keeping the API's order", () => {
    expect(ids(filterRows(rows, emptyFilter()))).toEqual(ids(rows));
    expect(ids(filterRows(rows, { ...emptyFilter(), state: "merged" }))).toEqual(["T-29", "T-28"]);
    expect(ids(filterRows(rows, { ...emptyFilter(), state: "merged", model: "opus" }))).toEqual(["T-29"]);
    expect(ids(filterRows(rows, { ...emptyFilter(), difficulty: "medium", model: "sonnet" }))).toEqual(["T-28"]);
    expect(ids(filterRows(rows, { ...emptyFilter(), difficulty: NONE }))).toEqual(["T-260"]);
    expect(ids(filterRows(rows, { ...emptyFilter(), model: NONE }))).toEqual(["T-27", "T-260"]);
  });
  it("takes the model that ran, else the spec's first Model: ref, without its effort", () => {
    expect(modelOf(rows[0])).toBe("opus");
    expect(modelOf({ task: "T-1", state: "waiting", model: "sonnet, opus" })).toBe("sonnet");
    expect(modelOf({ task: "T-1", state: "waiting" })).toBe(NONE);
  });
  it("counts each facet's values under the search and the other facets", () => {
    const c = counts(rows, { ...emptyFilter(), state: "merged" });
    // the state facet ignores its own choice: every state's count, in the filter's order, zeros kept
    expect(c.state[0]).toEqual([ALL, 5]);
    expect(Object.fromEntries(c.state)).toMatchObject({ running: 1, merged: 2, "never dispatched": 1, dropped: 1, waiting: 0 });
    // the other facets count the merged rows only
    expect(c.difficulty).toEqual([[ALL, 2], ["hard", 1], ["medium", 1]]);
    expect(c.model).toEqual([[ALL, 2], ["opus", 1], ["sonnet", 1]]);
    const q = counts(rows, { ...emptyFilter(), q: "web" });
    expect(q.state[0]).toEqual([ALL, 2]);
    expect(q.model).toEqual([[ALL, 2], ["opus", 1], ["sonnet", 1]]);
  });
  it("lists none last and keeps a chosen value with no rows as a button", () => {
    const c = counts(rows, emptyFilter());
    expect(c.model.map(([v]) => v)).toEqual([ALL, "opus", "sonnet", NONE]);
    const gone = counts(rows, { ...emptyFilter(), q: "drive", model: "sonnet" });
    expect(gone.model).toEqual([[ALL, 1], ["opus", 1], ["sonnet", 0]]);
  });
});
