// The docs' text search as the filter types it: one request per settled text, the stale one aborted, none for an
// empty text.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DELAY, TextSearch } from "./docsearch.svelte";
import type { DocHits } from "./types";

const answer = (q: string): DocHits => ({ q, hits: [{ path: "a.md", line: 1, text: q }], truncated: false });

let calls: { q: string; signal: AbortSignal; resolve: (h: DocHits) => void; reject: (e: Error) => void }[];
let search: TextSearch;

beforeEach(() => {
  vi.useFakeTimers();
  calls = [];
  search = new TextSearch((q, signal) => new Promise<DocHits>((resolve, reject) => {
    calls.push({ q, signal, resolve, reject });
    signal.addEventListener("abort", () => reject(new Error("aborted")));
  }));
});
afterEach(() => {
  search.stop();
  vi.useRealTimers();
});

describe("the text search", () => {
  it("waits for the typing to settle and sends one request for the last text", async () => {
    search.set("p");
    await vi.advanceTimersByTimeAsync(DELAY - 50);
    search.set("pa");
    await vi.advanceTimersByTimeAsync(DELAY - 50);
    search.set("pause");
    expect(search.busy).toBe(true);
    await vi.advanceTimersByTimeAsync(DELAY - 1);
    expect(calls).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(1);
    expect(calls.map((c) => c.q)).toEqual(["pause"]);
    calls[0].resolve(answer("pause"));
    await vi.advanceTimersByTimeAsync(0);
    expect(search.result?.q).toBe("pause");
    expect(search.busy).toBe(false);
  });

  it("aborts the request still out when the text changes, and keeps only the newer answer", async () => {
    search.set("lane");
    await vi.advanceTimersByTimeAsync(DELAY);
    expect(calls).toHaveLength(1);
    search.set("lanes");
    expect(calls[0].signal.aborted).toBe(true);
    await vi.advanceTimersByTimeAsync(DELAY);
    expect(calls.map((c) => c.q)).toEqual(["lane", "lanes"]);
    calls[0].resolve(answer("lane")); // late: ignored
    calls[1].resolve(answer("lanes"));
    await vi.advanceTimersByTimeAsync(0);
    expect(search.result?.q).toBe("lanes");
    expect(search.error).toBe("");
  });

  it("sends nothing for an empty text and clears the result", async () => {
    search.set("gate");
    await vi.advanceTimersByTimeAsync(DELAY);
    calls[0].resolve(answer("gate"));
    await vi.advanceTimersByTimeAsync(0);
    expect(search.result).not.toBeNull();
    search.set("   ");
    await vi.advanceTimersByTimeAsync(DELAY * 4);
    expect(calls).toHaveLength(1);
    expect(search.result).toBeNull();
    expect(search.busy).toBe(false);
  });

  it("drops a text still waiting when it is emptied, and sends nothing for the same text twice", async () => {
    search.set("x");
    search.set("");
    await vi.advanceTimersByTimeAsync(DELAY * 2);
    expect(calls).toHaveLength(0);
    search.set("y");
    await vi.advanceTimersByTimeAsync(DELAY);
    search.set(" y ");
    await vi.advanceTimersByTimeAsync(DELAY);
    expect(calls.map((c) => c.q)).toEqual(["y"]);
  });

  it("says why a request failed", async () => {
    search.set("boom");
    await vi.advanceTimersByTimeAsync(DELAY);
    calls[0].reject(new Error("500 Internal Server Error"));
    await vi.advanceTimersByTimeAsync(0);
    expect(search.error).toBe("500 Internal Server Error");
    expect(search.busy).toBe(false);
  });
});
