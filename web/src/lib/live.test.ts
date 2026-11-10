// The top bar's live mark is the worst of the running Lives' states: the sidebar's healthy reads never hide
// the shown page's failed refresh or lost event poll, and a page navigated away from does not count.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const events = vi.fn();
const view = vi.fn();
vi.mock("./api", () => ({ api: { events: (...a: unknown[]) => events(...a), view: (...a: unknown[]) => view(...a) } }));

const { Live, mark } = await import("./live.svelte");

const flush = () => vi.advanceTimersByTimeAsync(0);
const started: { stop(): void }[] = [];
function live<T>(load: () => Promise<T>, project = "") {
  const l = new Live<T>(() => load(), project);
  started.push(l);
  l.start();
  return l;
}
// page is a Live whose read fails while fail is set.
function page(project = "") {
  const st = { fail: false };
  const l = live(() => (st.fail ? Promise.reject(new Error("serve gone")) : Promise.resolve({ ok: 1 })), project);
  return { l, st };
}

beforeEach(() => {
  vi.useFakeTimers();
  events.mockReset();
  view.mockReset();
  view.mockResolvedValue({ events: [] });
  events.mockImplementation(() => new Promise(() => {})); // a long poll that has no record yet
});
afterEach(() => {
  while (started.length) started.pop()!.stop();
  vi.useRealTimers();
});

describe("the live mark", () => {
  it("stays stale when the page's refresh failed and the sidebar then reads fine", async () => {
    const side = live(() => Promise.resolve([]));
    const { l, st } = page();
    await flush();
    expect(mark).toEqual({ state: "on", text: "live" });
    st.fail = true;
    await expect(l.read()).rejects.toThrow("serve gone");
    expect(mark).toEqual({ state: "stale", text: "refresh failed" });
    expect(l.data).not.toBeNull(); // the page keeps its old data, its error beside it
    expect(l.error).toBe("serve gone");
    expect(await side.read()).toBe(true);
    expect(mark).toEqual({ state: "stale", text: "refresh failed" });
  });

  it("stays stale when the page's event poll disconnected and the sidebar then reads fine", async () => {
    const side = live(() => Promise.resolve([]));
    events.mockRejectedValue(new Error("no serve"));
    page("pj");
    await flush();
    expect(events).toHaveBeenCalled();
    expect(mark).toEqual({ state: "stale", text: "reconnecting" });
    expect(await side.read()).toBe(true);
    expect(mark).toEqual({ state: "stale", text: "reconnecting" });
  });

  it("goes live again once the page recovers", async () => {
    live(() => Promise.resolve([]));
    const { l, st } = page();
    await flush();
    st.fail = true;
    await expect(l.read()).rejects.toThrow();
    expect(mark.state).toBe("stale");
    st.fail = false;
    expect(await l.read()).toBe(true);
    expect(mark).toEqual({ state: "on", text: "live" });
    expect(l.error).toBe("");
  });

  it("goes live again once the poll reconnects", async () => {
    live(() => Promise.resolve([]));
    events.mockRejectedValueOnce(new Error("no serve"));
    page("pj");
    await flush();
    expect(mark.text).toBe("reconnecting");
    events.mockResolvedValueOnce({ seq: 0, events: [] });
    await vi.advanceTimersByTimeAsync(3000);
    expect(mark).toEqual({ state: "on", text: "live" });
  });

  it("goes live when the failed page is navigated away from", async () => {
    live(() => Promise.resolve([]));
    const { l, st } = page();
    await flush();
    st.fail = true;
    await expect(l.read()).rejects.toThrow();
    expect(mark.state).toBe("stale");
    l.stop();
    expect(mark).toEqual({ state: "on", text: "live" });
  });
});
