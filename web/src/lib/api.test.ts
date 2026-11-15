// The page follows the build obor web serves: the tab keeps the build it loaded with, and one response from another
// build reloads it (the hash route survives), so a UI change reaches a tab that was open before it.
import { beforeEach, describe, expect, it, vi } from "vitest";

const reload = vi.fn();
let api: typeof import("./api").api;

// answer makes the next fetch a JSON list with the server's build header (none: an obor web from before it).
function answer(build?: string) {
  const headers: Record<string, string> = build === undefined ? {} : { "X-Obor-Build": build };
  vi.stubGlobal("fetch", vi.fn(async () => new Response("[]", { status: 200, headers })));
}

beforeEach(async () => {
  vi.resetModules(); // a fresh module: no build loaded yet
  reload.mockReset();
  vi.stubGlobal("location", { reload });
  ({ api } = await import("./api"));
});

describe("the page's build", () => {
  it("is the one it loaded with: a response naming it does not reload", async () => {
    answer("b1");
    expect(await api.projects()).toEqual([]);
    answer("b1");
    expect(await api.projects()).toEqual([]);
    expect(reload).not.toHaveBeenCalled();
  });

  it("reloads once when a response names another build", async () => {
    answer("b1");
    expect(await api.projects()).toEqual([]);
    answer("b2");
    expect(await api.projects()).toEqual([]);
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("does not reload on a response without the header: an older obor web", async () => {
    answer("b1");
    expect(await api.projects()).toEqual([]);
    answer();
    expect(await api.projects()).toEqual([]);
    expect(reload).not.toHaveBeenCalled();
  });
});
