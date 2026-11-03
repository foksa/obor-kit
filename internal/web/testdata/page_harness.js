// page_harness.js runs page.html's script in node against a fake browser (DOM, fetch, clock) for page_js_test.go
// (T-676). The test puts the page's script where the PAGE line is; the scenario named by argv[2] runs, and every failed
// check is printed as "FAIL <text>" with exit code 1.
"use strict";

// ---- the clock: timers run only when a scenario advances it -------------------------------------------------------
const realImmediate = setImmediate;
let clockMS = 1_700_000_000_000;
let timers = [];
let timerID = 0;
Date.now = () => clockMS;
globalThis.setTimeout = (fn, ms) => { const id = ++timerID; timers.push({ id, at: clockMS + (ms || 0), fn }); return id; };
globalThis.clearTimeout = (id) => { timers = timers.filter((t) => t.id !== id); };
globalThis.setInterval = (fn, ms) => {
  const id = ++timerID;
  const again = () => { timers.push({ id, at: clockMS + ms, fn: () => { again(); fn(); } }); };
  again();
  return id;
};
const flush = async () => { for (let i = 0; i < 30; i++) await new Promise((r) => realImmediate(r)); };
// tick advances the clock by ms, running each timer due on the way and the promises it settles.
async function tick(ms) {
  const end = clockMS + ms;
  await flush();
  for (;;) {
    timers.sort((a, b) => a.at - b.at || a.id - b.id);
    const t = timers[0];
    if (!t || t.at > end) break;
    timers.shift();
    clockMS = t.at;
    t.fn();
    await flush();
  }
  clockMS = end;
  await flush();
}

// ---- the DOM: just what the page touches ---------------------------------------------------------------------------
const listeners = {};
function el(id) {
  return {
    id, innerHTML: "", className: "", title: "", textContent: "", _span: { textContent: "" },
    querySelector(sel) { return this._span; },
    querySelectorAll() { return []; },
    addEventListener() {},
    setAttribute() {}, removeAttribute() {},
  };
}
const elements = { app: el("app"), crumbs: el("crumbs"), live: el("live"), theme: el("theme") };
globalThis.document = {
  title: "",
  getElementById: (id) => elements[id],
  querySelectorAll: () => [],
  addEventListener: () => {},
  documentElement: el("html"),
};
globalThis.location = { hash: "", replace(h) { this.hash = h; } };
globalThis.window = {
  scrollY: 0,
  scrollTo() {},
  addEventListener(name, fn) { (listeners[name] = listeners[name] || []).push(fn); },
};
globalThis.localStorage = { getItem: () => null, setItem() {} };
globalThis.matchMedia = () => ({ matches: false });
// navigate is a click on a link: the hash changes and the page's hashchange listener runs.
async function navigate(hash) {
  location.hash = hash;
  for (const fn of listeners.hashchange || []) fn();
  await flush();
}

// ---- the server: a scenario's store seq and its answers; /api/events holds a poll until the seq passes since --------
const server = {
  seq: 10,
  requests: [],           // every path asked, in order
  held: [],               // the long polls waiting for a record
  routes: { "/api/projects": () => [] }, // path prefix → () => body, or throws for a 500
  onRequest: null,        // called with each path before it is answered
  // bump is a new record: the seq moves and every held poll it passes answers.
  bump(seq) {
    this.seq = seq;
    const still = [];
    for (const h of this.held) {
      if (this.seq > h.since) h.answer({ seq: this.seq, events: [] });
      else still.push(h);
    }
    this.held = still;
  },
};
const answer = (status, body) => ({ ok: status < 400, status, statusText: String(status), json: async () => body });
globalThis.fetch = (path, opts) => new Promise((resolve, reject) => {
  server.requests.push(path);
  if (server.onRequest) server.onRequest(path);
  const signal = opts && opts.signal;
  if (signal) signal.addEventListener("abort", () => reject(Object.assign(new Error("aborted"), { name: "AbortError" })));
  const m = path.match(/^\/api\/events\?project=[^&]*&since=(\d+)$/);
  if (m) {
    const since = +m[1];
    if (server.seq > since) resolve(answer(200, { seq: server.seq, events: [] }));
    else server.held.push({ since, answer: (body) => resolve(answer(200, body)) });
    return;
  }
  const key = Object.keys(server.routes).find((k) => path.startsWith(k));
  if (!key) { resolve(answer(404, { error: "no route " + path })); return; }
  try { resolve(answer(200, server.routes[key](path))); } catch (e) { resolve(answer(500, { error: e.message })); }
});

// ---- checks -----------------------------------------------------------------------------------------------------------
const failures = [];
function check(ok, text) { if (!ok) failures.push(text); }
const html = () => elements.app.innerHTML;
const liveText = () => elements.live._span.textContent;
const viewWith = (over) => Object.assign({ serve: { up: true, project: true, merging: true, herdr: true }, lanes: [],
  roles: [], needs_you: [], queue: {}, events: [{ seq: server.seq, ts: "2026-11-01T00:00:00Z", line: "x" }] }, over);

//PAGE//

// ---- scenarios --------------------------------------------------------------------------------------------------------
const scenarios = {
  // RV-76 #1: a transition that lands after the page's first read is past the cursor, so the page shows it.
  async transitionAfterFirstRead() {
    let state = "working";
    let reads = 0;
    server.routes["/api/view"] = () => viewWith({});
    server.routes["/api/task"] = () => { reads++; return { task: "T-1", state, records: [] }; };
    server.onRequest = (path) => {
      // the record lands right after the task page's first read, before anything else is asked
      if (path.startsWith("/api/task") && reads === 0) {
        Promise.resolve().then(() => { state = "awaiting-accept"; server.bump(11); });
      }
    };
    await navigate("#/t/proj/T-1");
    await tick(100);
    const firstTask = server.requests.findIndex((p) => p.startsWith("/api/task"));
    const cursorRead = server.requests.findIndex((p) => p.startsWith("/api/view"));
    check(cursorRead >= 0 && cursorRead < firstTask, `the cursor is read before the page: ${server.requests}`);
    check(server.requests.some((p) => p.endsWith("since=10")), `the poll starts from the cursor 10: ${server.requests}`);
    await tick(1500);
    check(reads >= 2, `the page was read again after the record (reads ${reads})`);
    check(html().includes("awaiting-accept"), "the page shows the transition: " + html().slice(0, 300));
    check(cursors.proj === 11, `the cursor moved to 11 after the redraw (${cursors.proj})`);
    check(liveText() === "live", `the live mark says live (${liveText()})`);
  },

  // A change that appends no record (a role agent's pane status) shows after the periodic refresh, not before.
  async changeWithoutRecord() {
    let status = "working";
    server.routes["/api/view"] = () => viewWith({ roles: [{ role: "reviewer", agent: "rv", status }] });
    await navigate("#/p/proj");
    await tick(100);
    check(html().includes(">working<"), "precondition: the page shows working: " + html().slice(0, 200));
    status = "idle";
    await tick(1000);
    check(!html().includes(">idle<"), "no redraw before the refresh is due");
    await tick(REFRESH_MS);
    check(html().includes(">idle<") && !html().includes(">working<"), "the refresh shows the new status");
    check(!server.requests.some((p) => /since=(?!10$)/.test(p)), `no record moved the cursor: ${server.requests}`);
    check(liveText() === "live", `the live mark says live (${liveText()})`);
    // a refresh that fails turns the mark stale
    server.routes["/api/view"] = () => { throw new Error("serve gone"); };
    await tick(REFRESH_MS);
    check(liveText() === "refresh failed", `a failed refresh turns the mark stale (${liveText()})`);
  },

  // A redraw that fails is tried again; the cursor does not skip past the record it missed.
  async failedRedrawRetried() {
    let state = "working";
    let fail = false;
    let reads = 0;
    server.routes["/api/view"] = () => viewWith({});
    server.routes["/api/task"] = () => {
      reads++;
      if (fail) { fail = false; throw new Error("busy"); }
      return { task: "T-1", state, records: [] };
    };
    await navigate("#/t/proj/T-1");
    await tick(100);
    check(html().includes(">working<"), "precondition: the page shows working");
    state = "accepted";
    fail = true;
    server.bump(12);
    await tick(100);
    check(reads === 2, `the record started a redraw (reads ${reads})`);
    check(!html().includes(">accepted<"), "the failed redraw drew nothing");
    check(cursors.proj === 10, `the cursor stays before the missed record (${cursors.proj})`);
    check(liveText() === "refresh failed", `the mark is stale after a failed redraw (${liveText()})`);
    check(REFRESH_MS > RETRY_MS + 200, "the retry is due before the refresh");
    await tick(RETRY_MS + 100);
    check(html().includes(">accepted<"), "the retried redraw shows the change before the refresh: " + html().slice(0, 200));
    check(cursors.proj === 12, `the cursor moved after the retry (${cursors.proj})`);
    check(liveText() === "live", `the mark is live again (${liveText()})`);
  },

  // RV-76 #1: a first draw that fails leaves the page not live, on every page kind; the next successful refresh
  // clears the stale mark.
  async failedFirstDraw() {
    const pages = {
      "/api/projects": () => [],
      "/api/view": () => viewWith({}),
      "/api/task": () => ({ task: "T-1", state: "working", records: [] }),
      "/api/agent": () => ({ agent: "rv", status: "stopped", jobs: [], briefs: [], lines: [], sessions: [] }),
      "/api/job": () => ({ kind: "job", job: "T-1/1/work", text: "x", ts: "2026-11-01T00:00:00Z" }),
    };
    for (const c of [
      { kind: "landing", hash: "#/", path: "/api/projects" },
      { kind: "project", hash: "#/p/proj", path: "/api/view" },
      { kind: "task", hash: "#/t/proj/T-1", path: "/api/task" },
      { kind: "agent", hash: "#/a/proj/rv", path: "/api/agent" },
      { kind: "job", hash: "#/j/proj/T-1/1/work", path: "/api/job" },
    ]) {
      cursors.proj = server.seq; // a known cursor: the live poll holds, so the refresh is what redraws
      let calls = 0;
      let fail = true;
      server.routes = {};
      for (const [path, body] of Object.entries(pages)) {
        server.routes[path] = () => {
          if (path === c.path) {
            calls++;
            // the project page's first /api/view is the cursor read: the page's own draw is the second
            if (fail && (path !== "/api/view" || calls === 2)) { fail = false; throw new Error("cannot load"); }
          }
          return body();
        };
      }
      await navigate(c.hash);
      await tick(100);
      check(html().includes("could not load this page"), `${c.kind}: the failed load is shown: ` + html().slice(0, 200));
      check(liveText() !== "live", `${c.kind}: a failed first draw is not live (${liveText()})`);
      await tick(REFRESH_MS);
      check(liveText() === "live", `${c.kind}: the next refresh clears the stale mark (${liveText()})`);
    }
  },

  // The router keeps each project's cursor: navigating away and back never moves it back, even when the cursor's read
  // fails or answers an older seq, and the poll resumes from it.
  async cursorKeptPerProject() {
    server.seq = 20;
    let viewOK = true;
    let viewSeq = 20;
    server.routes["/api/view"] = () => {
      if (!viewOK) throw new Error("down");
      return viewWith({ events: [{ seq: viewSeq, ts: "2026-11-01T00:00:00Z", line: "x" }] });
    };
    server.routes["/api/task"] = () => ({ task: "T-1", state: "working", records: [] });
    await navigate("#/p/proj");
    await tick(100);
    check(cursors.proj === 20, `precondition: the project's cursor is 20 (${cursors.proj})`);
    await navigate("#/p/other");
    await tick(100);
    check(cursors.other === 20 && cursors.proj === 20, "another project has its own cursor");
    viewOK = false;
    server.requests = [];
    await navigate("#/t/proj/T-1");
    await tick(100);
    check(cursors.proj === 20, `a failed cursor read keeps the cursor (${cursors.proj})`);
    check(server.requests.some((p) => p.includes("project=proj&since=20")), `the poll resumes from 20: ${server.requests}`);
    viewOK = true;
    viewSeq = 15; // fewer worded records than the cursor has seen
    server.requests = [];
    await navigate("#/p/proj");
    await tick(100);
    check(cursors.proj === 20, `the cursor never moves back (${cursors.proj})`);
    check(!server.requests.some((p) => /since=(?!20$)/.test(p)), `every poll is from 20: ${server.requests}`);
  },

  // RV-76 #4: the pipeline's step for each stage, merged only with the merged fact.
  async pipeline() {
    const cur = (t) => {
      const s = stageOfTask(t);
      return s.i >= TASK_STEPS.length ? "all done" : TASK_STEPS[s.i];
    };
    const rec = (kind, step, line) => ({ kind, step, line: line || "" });
    const cases = [
      ["dispatched", { state: "dispatched", records: [rec("fact", "dispatch")] }, "dispatched"],
      ["working", { state: "working", records: [rec("fact", "delivered"), rec("fact", "dispatch")] }, "working"],
      ["awaiting accept", { state: "awaiting-accept", records: [rec("fact", "submitted")] }, "review"],
      ["accepted", { state: "accepted", records: [rec("result", "accept", "accepted")] }, "merging"],
      ["publish pending", { state: "published", records: [rec("result", "accept")] }, "merging"],
      ["held after accept", { state: "needs-you", records: [rec("fact", "needs-you"), rec("result", "accept"),
        rec("result", "gate", "GREEN pass x"), rec("fact", "submitted"), rec("fact", "dispatch")] }, "merging"],
      ["held at dispatch", { state: "fail", records: [rec("fact", "needs-you"), rec("fact", "dispatch")] }, "dispatched"],
      ["held after a red gate", { state: "", records: [rec("result", "gate", "RED fail x"), rec("fact", "submitted")] }, "gate"],
      ["merged", { state: "merged", records: [rec("fact", "merged"), rec("result", "accept")] }, "all done"],
    ];
    for (const [name, t, want] of cases) check(cur(t) === want, `${name}: step ${cur(t)}, want ${want}`);
    const held = stageOfTask(cases[5][1]);
    check(held.tone === "bad", `held after accept is red (${held.tone})`);
    const done = steps({ state: "merged", records: [rec("fact", "merged")] });
    check((done.match(/class="step done"/g) || []).length === TASK_STEPS.length, "merged: every step done");
    const acc = steps({ state: "accepted", records: [] });
    const cls = {}; // step name → its class in the rendered steps
    for (const m of acc.matchAll(/<div class="step ([a-z]*)"[^>]*>(?:<span[^>]*>[^<]*<\/span>)*([a-z]+)<\/div>/g)) cls[m[2]] = m[1];
    check(Object.keys(cls).length === TASK_STEPS.length, "precondition: every step rendered: " + JSON.stringify(cls));
    check(cls.merging === "cur" && cls.merged === "", "accepted: merging is current, merged not reached: " + JSON.stringify(cls));
  },

  // A stopped role agent shows its status as the view gives it; "No pane" only for a role the view gives none.
  async stoppedRole() {
    server.routes["/api/view"] = () => viewWith({ roles: [
      { role: "reviewer", agent: "rv", status: "stopped" }, { role: "integrator", agent: "ig" },
      { role: "advisor", agent: "ad", brief: { delivered: true, since: 1 } }] });
    server.routes["/api/agent"] = () => ({ agent: "rv", status: "stopped", jobs: [], briefs: [], lines: [], sessions: [] });
    await navigate("#/p/proj");
    await tick(100);
    const page = html();
    check(page.includes(">stopped<"), "the stopped role shows stopped");
    const idle = (page.match(/No pane: (.*?)<\/p>/s) || [])[1] || "";
    check(idle.includes("ig") && !idle.includes("rv") && !idle.includes("ad"), "No pane lists only the role with no status: " + idle);
    check(/advisor[\s\S]*?no pane/.test(page), "a role with a brief and no status says no pane");
    await navigate("#/a/proj/rv");
    await tick(100);
    check(html().includes(">stopped<") && !html().includes("no pane"), "the agent page shows stopped, not no pane");
  },

  // T-714, T-727: an agent's long-running processes on the overview card (a count), the project page (under the
  // lane's and the lead's rows: bg for a recorded background call, running for a long foreground command) and the
  // agent page.
  async background() {
    const conv = { pid: 42, command: "python convert.py", background: true, label: "convert maps", age: 900 };
    server.routes["/api/projects"] = () => [{ name: "proj", root: "/r", up: true, busy: 1, free: 0, needs_you: 0,
      queue: 0, running: [], roles: [], background: 2 }];
    await navigate("#/");
    await tick(100);
    check(html().includes("2 bg"), "the overview card counts the background processes");
    server.routes["/api/view"] = () => viewWith({
      lanes: [{ lane: "O-01", state: "working", column: "working", task: "T-1", agent: "w1", background: [conv] }],
      lead: { role: "lead", agent: "ld", status: "working", background: [{ pid: 7, command: "sleep 600", age: 45 }] } });
    server.routes["/api/agent"] = () => ({ agent: "w1", status: "working", jobs: [], briefs: [], lines: [], sessions: [],
      background: [conv] });
    await navigate("#/p/proj");
    await tick(100);
    const page = html();
    check(page.includes("bg: convert maps · 15m"), "the lane's row shows the process by its description: " + page);
    check(page.includes("running sleep 600 · 45s") && !page.includes("bg: sleep 600") && page.includes("ld"),
      "the lead's own row shows its unrecorded process as running, by its command: " + page);
    await navigate("#/a/proj/w1");
    await tick(100);
    check(html().includes("Long-running processes") && html().includes("convert maps") &&
      html().includes("python convert.py") && html().includes("bg: 15m"), "the agent page lists the process as bg: " + html());
  },

  // T-721: a busy lane's row shows the type its task's dispatch chose, before its effort.
  async laneType() {
    server.routes["/api/view"] = () => viewWith({
      lanes: [{ lane: "O-01", state: "working", column: "working", task: "T-1", agent: "w1", type: "sonnet",
        effort: "high" }] });
    await navigate("#/p/proj");
    await tick(100);
    const page = html();
    check(page.includes("T-1"), "precondition: the lane's row is drawn: " + page);
    check(page.includes(" · sonnet · effort high"), "the lane's row shows its dispatch's type and effort: " + page);
  },

  // T-750: the setup route draws the four cards — difficulty, the lanes with the pool, the roles and the pause, and
  // the distribution table with its 7 days / all switch — and the project page links it.
  async setupRoute() {
    server.routes["/api/view"] = () => viewWith({});
    server.routes["/api/setup"] = () => ({
      difficulty: [{ level: "hard", types: ["opus:high"] }, { level: "medium", types: ["sonnet"] },
        { level: "simple", types: ["haiku"] }],
      lanes: [{ lane: "O-01", type: "opus", agent: "pj-o-01", state: "working" },
        { lane: "L-01", type: "", agent: "pj-l-01", state: "free" }],
      roles: [{ role: "lead", agent: "pj-lead", type: "opus:high", status: "working" },
        { role: "reviewer", agent: "pj-rev", type: "", status: "" }],
      paused: { reason: "lunch", by: "owner" },
      distribution: [{ level: "hard", type: "opus:high", recent: 2, all: 5 },
        { level: "override", type: "sonnet", recent: 0, all: 3 }],
    });
    await navigate("#/s/proj");
    await tick(100);
    const page = html();
    check(server.requests.some((p) => p.startsWith("/api/setup?project=proj")),
      `the setup page asks /api/setup: ${server.requests}`);
    check(page.includes("2 lanes") && page.includes("1 pooled") && page.includes("1 typed"),
      "the lanes card counts the pool and the typed lanes: " + page);
    check(page.includes("opus:high") && page.includes("haiku"), "the difficulty card lists each level's types");
    check(page.includes("pooled"), "a lane with no configured type says pooled");
    check(page.includes("lunch") && page.includes("owner"), "the pause is on the roles card");
    check(page.includes("override") && page.includes("sonnet") && page.includes(">2<"),
      "the distribution's level rows × type columns, 7 days by default: " + page);
    check(/data-dist="recent" class="on"/.test(page), "the 7 days switch starts on: " + page);
    distAll = true; // the switch's click handler does this, then redraws the page: all records
    await current.draw(true);
    await tick(10);
    check(/data-dist="all" class="on"/.test(html()) && html().includes(">5<") && !html().includes(">2<"),
      "all shows the all counts: " + html());
    await navigate("#/p/proj");
    await tick(100);
    check(html().includes('href="#/s/proj"'), "the project page links the setup page: " + html());
  },
};

(async () => {
  const name = process.argv[2];
  if (!scenarios[name]) { console.log("FAIL no scenario " + name); process.exit(1); }
  await flush(); // the page's own go() on load (the landing page) settles first
  try { await scenarios[name](); } catch (e) { failures.push("threw: " + (e.stack || e)); }
  for (const f of failures) console.log("FAIL " + f);
  process.exit(failures.length ? 1 : 0);
})();
