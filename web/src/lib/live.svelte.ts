// Live data: a page's read, kept current. A project page holds one /api/events long poll — a record past the cursor
// reads the page again — and every page reads itself again every few seconds (an agent's pane, serve's connection
// and a running command log no record). An answer that lands after a later-started read is dropped, so an older read
// never replaces a newer one.
import { api } from "./api";

const REFRESH_MS = 5000; // the shown page is read again this often
const RETRY_MS = 3000; // a failed poll or read is tried again after this
const BURST_MS = 1200; // a burst of records reads once

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// mark is the live mark in the top bar: on, or why not. It is the worst of every running Live's own state (the
// sidebar's and the shown page's), so a healthy sidebar never hides a page's failed refresh or lost poll; a stopped
// Live (a page navigated away from) does not count.
export const mark = $state<{ state: "on" | "stale" | "off"; text: string }>({ state: "off", text: "" });

type Own = "" | "on" | "refresh failed" | "reconnecting"; // "": not read yet
const rank: Record<Own, number> = { "": 0, on: 1, "refresh failed": 2, reconnecting: 3 };
const running = new Set<Live<unknown>>();
function remark() {
  let worst: Own = "";
  for (const l of running) if (rank[l.own] > rank[worst]) worst = l.own;
  if (worst === "") Object.assign(mark, { state: "off", text: "" });
  else if (worst === "on") Object.assign(mark, { state: "on", text: "live" });
  else Object.assign(mark, { state: "stale", text: worst });
}

const cursors: Record<string, number> = {}; // project → the newest seq its pages are read past; only moves forward
function advance(project: string, seq: number) {
  if (cursors[project] === undefined || seq > cursors[project]) cursors[project] = seq;
}
async function takeCursor(project: string, signal: AbortSignal) {
  const v = await api.view(project, signal);
  advance(project, (v.events || []).reduce((m, e) => Math.max(m, e.seq || 0), 0));
}

export class Live<T> {
  data = $state<T | null>(null);
  error = $state<string>("");
  // at is when data was last read (ms); a page that edits uses it to know its data is fresh.
  at = $state(0);

  private ctl: AbortController | null = null;
  private started = 0;
  private painted = 0;
  private poll = true;
  private drawn = false;
  // own is this Live's part of the top bar's mark.
  own: Own = "";

  constructor(private load: (signal: AbortSignal) => Promise<T>, private project = "") {}

  // read reads the page now; true once its answer is the one shown (or a newer one is).
  async read(): Promise<boolean> {
    const ctl = this.ctl;
    if (!ctl) return false;
    const ticket = ++this.started;
    try {
      const d = await this.load(ctl.signal);
      if (ctl.signal.aborted) return false;
      if (ticket < this.painted) return true;
      this.painted = ticket;
      this.data = d;
      this.error = "";
      this.at = Date.now();
      this.drawn = true;
      return true;
    } catch (e) {
      if (ctl.signal.aborted) return false;
      if (ticket >= this.painted) this.error = (e as Error).message;
      this.drawn = false;
      throw e;
    } finally {
      this.mark();
    }
  }

  start() {
    this.ctl = new AbortController();
    running.add(this as Live<unknown>);
    const ctl = this.ctl;
    (async () => {
      // The cursor is taken before the first read: a record that lands between the two reads the page again.
      if (this.project) {
        try { await takeCursor(this.project, ctl.signal); } catch { /* kept, or taken by the poll */ }
      }
      try { await this.read(); } catch { /* shown as the page's error */ }
      if (ctl.signal.aborted) return;
      if (this.project) void this.events(ctl);
      void this.refresh(ctl);
    })();
  }

  stop() {
    this.ctl?.abort();
    this.ctl = null;
    running.delete(this as Live<unknown>);
    remark();
  }

  private mark() {
    this.own = !this.poll ? "reconnecting" : !this.drawn ? "refresh failed" : "on";
    remark();
  }

  private async refresh(ctl: AbortController) {
    while (!ctl.signal.aborted) {
      await sleep(REFRESH_MS);
      if (ctl.signal.aborted) return;
      try { await this.read(); } catch { /* marked stale */ }
    }
  }

  // events is the long poll: a record past the cursor reads the page again, and only a read that succeeded moves the
  // cursor past it (a failed one is tried again).
  private async events(ctl: AbortController) {
    const p = this.project;
    let last = 0;
    let target = 0; // the seq a pending read moves the cursor to (0: none pending)
    while (!ctl.signal.aborted) {
      if (cursors[p] === undefined) {
        try { await takeCursor(p, ctl.signal); } catch {
          if (ctl.signal.aborted) return;
          this.poll = false; this.mark();
          await sleep(RETRY_MS);
          continue;
        }
        target = cursors[p];
      }
      if (!target) {
        let page;
        try { page = await api.events(p, cursors[p], ctl.signal); } catch {
          if (ctl.signal.aborted) return;
          this.poll = false; this.mark();
          await sleep(RETRY_MS);
          continue;
        }
        if (ctl.signal.aborted) return;
        this.poll = true; this.mark();
        if ((page.seq || 0) <= cursors[p]) continue;
        target = page.seq;
      }
      const wait = BURST_MS - (Date.now() - last);
      if (wait > 0) await sleep(wait);
      if (ctl.signal.aborted) return;
      last = Date.now();
      try {
        if (!(await this.read())) return;
        advance(p, target);
        target = 0;
      } catch {
        if (ctl.signal.aborted) return;
        await sleep(RETRY_MS);
      }
    }
  }
}
