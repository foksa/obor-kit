// TextSearch is the docs' full-text search as the filter types it: each text waits DELAY ms for the typing to settle,
// then one request goes out; a newer text aborts the request still out and its wait. An empty text sends nothing and
// clears the result. One TextSearch serves the page (the list page, the reading sidebar and the narrow drawer show the
// same result). A failed request clears the result; resultFor shows an answer only under its own words.
import type { DocHits } from "./types";

export const DELAY = 250;

export class TextSearch {
  // result is the last answer (kept while a newer text is on its way, shown only through resultFor), error why the
  // last request failed.
  result = $state.raw<DocHits | null>(null);
  error = $state("");
  // busy is a text waiting or out.
  busy = $state(false);
  #load: (q: string, signal: AbortSignal) => Promise<DocHits>;
  #delay: number;
  #want = "";
  #timer: ReturnType<typeof setTimeout> | undefined;
  #ctl: AbortController | null = null;

  constructor(load: (q: string, signal: AbortSignal) => Promise<DocHits>, delay = DELAY) {
    this.#load = load;
    this.#delay = delay;
  }

  // set is the text to search (trimmed); the same text again does nothing.
  set(q: string) {
    const t = String(q || "").trim();
    if (t === this.#want) return;
    this.#want = t;
    this.#cancel();
    if (!t) {
      this.result = null;
      this.error = "";
      this.busy = false;
      return;
    }
    this.busy = true;
    this.#timer = setTimeout(() => void this.#run(t), this.#delay);
  }

  // resultFor is the last answer when it answers q (trimmed), null otherwise: an older text's hits are neither shown
  // nor opened under newer words.
  resultFor(q: string): DocHits | null {
    const r = this.result;
    return r && r.q === String(q || "").trim() ? r : null;
  }

  // stop drops the wait and aborts the request out (the page's end).
  stop() {
    this.#cancel();
    this.#want = "";
    this.busy = false;
  }

  #cancel() {
    clearTimeout(this.#timer);
    this.#timer = undefined;
    this.#ctl?.abort();
    this.#ctl = null;
  }

  async #run(t: string) {
    const ctl = new AbortController();
    this.#ctl = ctl;
    try {
      const r = await this.#load(t, ctl.signal);
      if (ctl.signal.aborted) return;
      this.result = r;
      this.error = "";
    } catch (e) {
      if (ctl.signal.aborted) return;
      this.result = null;
      this.error = e instanceof Error ? e.message : String(e);
    }
    this.#ctl = null;
    this.busy = false;
  }
}
