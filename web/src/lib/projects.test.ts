// The shell and the fleet page share one /api/projects read: two holders read it once per refresh, and it stops only
// when the last one lets go.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const read = vi.fn();
vi.mock("./api", () => ({ api: { projects: (...a: unknown[]) => read(...a) } }));

const { projects, holdProjects } = await import("./projects.svelte");

beforeEach(() => {
  vi.useFakeTimers();
  read.mockReset();
  read.mockResolvedValue([{ name: "pj" }]);
});
afterEach(() => vi.useRealTimers());

describe("the shared projects read", () => {
  it("reads once per refresh for two holders and stops with the last release", async () => {
    const shell = holdProjects();
    const fleet = holdProjects();
    await vi.advanceTimersByTimeAsync(0);
    expect(read).toHaveBeenCalledTimes(1);
    expect(projects.data).toEqual([{ name: "pj" }]);
    await vi.advanceTimersByTimeAsync(5000);
    expect(read).toHaveBeenCalledTimes(2);

    fleet();
    fleet(); // a second release of the same hold does not count
    await vi.advanceTimersByTimeAsync(5000);
    expect(read).toHaveBeenCalledTimes(3); // the shell still holds it

    shell();
    await vi.advanceTimersByTimeAsync(15000);
    expect(read).toHaveBeenCalledTimes(3);
  });
});
