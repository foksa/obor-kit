// The build obor web serves: the tab keeps the build it loaded with, and a response naming another one raises the nav's
// reload control instead of reloading the tab (Setup's staged edits, a pause form, a filter or the jump would go).
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "svelte/server";
import NewBuild from "../ui/NewBuild.svelte";

const reload = vi.fn();
let api: typeof import("./api").api;
let build: typeof import("./build.svelte");

// answer makes the next fetch a JSON list with the server's build header (none: an obor web from before it).
function answer(id?: string) {
  const headers: Record<string, string> = id === undefined ? {} : { "X-Obor-Build": id };
  vi.stubGlobal("fetch", vi.fn(async () => new Response("[]", { status: 200, headers })));
}

beforeEach(async () => {
  vi.resetModules(); // a fresh module: no build loaded, no control raised
  reload.mockReset();
  vi.stubGlobal("location", { reload });
  ({ api } = await import("./api"));
  build = await import("./build.svelte");
});

describe("the page's build", () => {
  it("stays the one it loaded with: the same header again raises no control, and no read reloads the tab", async () => {
    answer("b1");
    expect(await api.projects()).toEqual([]);
    answer("b1");
    expect(await api.projects()).toEqual([]);
    expect(build.build.newer).toBe(false);
    answer("b2"); // b1 was the build this tab loaded, so b2 is a newer one
    expect(await api.projects()).toEqual([]);
    expect(build.build.newer).toBe(true);
    expect(reload).not.toHaveBeenCalled();
  });

  it("raises the control and never reloads the tab when a response names another build", async () => {
    answer("b1");
    expect(await api.projects()).toEqual([]);
    answer("b2");
    expect(await api.projects()).toEqual([]);
    expect(build.build.newer).toBe(true);
    answer("b2"); // every later read names it too: still the owner's click that reloads
    expect(await api.projects()).toEqual([]);
    expect(build.build.newer).toBe(true);
    expect(reload).not.toHaveBeenCalled();
  });

  it("is unchanged by a response without the header: an obor web from before it", async () => {
    answer("b1");
    expect(await api.projects()).toEqual([]);
    answer();
    expect(await api.projects()).toEqual([]);
    expect(build.build.newer).toBe(false);
    answer("b1"); // the headerless answer did not replace the loaded build
    expect(await api.projects()).toEqual([]);
    expect(build.build.newer).toBe(false);
    answer("b2");
    expect(await api.projects()).toEqual([]);
    expect(build.build.newer).toBe(true);
    expect(reload).not.toHaveBeenCalled();
  });
});

describe("the reload control", () => {
  it("is in the nav only while a newer build answers", () => {
    const off = render(NewBuild, { props: { newer: false, onreload: reload } }).body;
    expect(off).not.toContain("New version: reload");
    const on = render(NewBuild, { props: { newer: true, onreload: reload } }).body;
    expect(on).toMatch(/<button[^>]*>New version: reload<\/button>/);
  });

  it("loads the page again, once: its click handler calls location.reload", () => {
    build.reload(); // what the control runs on a click
    expect(reload).toHaveBeenCalledTimes(1);
  });
});
