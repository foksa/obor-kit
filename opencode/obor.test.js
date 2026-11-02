// node --test opencode/obor.test.js (run by cmd/obor's TestOpencodePlugin when node is on PATH).
// Keep the suite well under that test's 20 s limit (shimRun), which the full gate's load eats into: target < 10 s here.
// The one slow test (the 5 s hook timeout) runs beside the others; a new slow test does the same.
import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { toClaudeEvents, patchPaths, claudeError, errorDetails, databasePath, modelRef, modelSwitch } from "./obor.js";

const cwd = "/w/lane";
const ev = (tool_name, tool_input) => ({ hook_event_name: "PreToolUse", session_id: "s1", cwd, tool_name, tool_input });

const cases = [
  ["bash", "bash", { command: "git commit -m x" }, [ev("Bash", { command: "git commit -m x" })]],
  ["shell (opencode 2)", "shell", { command: "ls" }, [ev("Bash", { command: "ls" })]],
  ["bash with workdir", "bash", { command: "git status", workdir: "/o'x" }, [ev("Bash", { command: "cd '/o'\\''x' && git status" })]],
  ["edit filePath", "edit", { filePath: "/w/lane/a.go", oldString: "a", newString: "b" }, [ev("Edit", { file_path: "/w/lane/a.go" })]],
  ["edit path (opencode 2), relative", "edit", { path: "b/c.go" }, [ev("Edit", { file_path: "/w/lane/b/c.go" })]],
  ["write", "write", { filePath: "/w/lane/x.md", content: "" }, [ev("Write", { file_path: "/w/lane/x.md" })]],
  [
    "patch: one Edit per file",
    "patch",
    { patchText: "*** Begin Patch\n*** Add File: n.go\n+x\n*** Update File: /w/lane/u.go\n*** Move to: m.go\n@@\n*** Delete File: d.go\n*** End Patch" },
    [
      ev("Edit", { file_path: "/w/lane/n.go" }),
      ev("Edit", { file_path: "/w/lane/u.go" }),
      ev("Edit", { file_path: "/w/lane/m.go" }),
      ev("Edit", { file_path: "/w/lane/d.go" }),
    ],
  ],
  ["apply_patch", "apply_patch", { patchText: "*** Update File: a.go" }, [ev("Edit", { file_path: "/w/lane/a.go" })]],
  ["read", "read", { filePath: "/etc/hosts" }, [ev("Read", { file_path: "/etc/hosts" })]],
  ["glob", "glob", { pattern: "**/*.go" }, [ev("Glob", { pattern: "**/*.go", path: "" })]],
  ["grep", "grep", { pattern: "x", path: "internal" }, [ev("Grep", { pattern: "x", path: "internal" })]],
  ["list", "list", { path: "." }, [ev("LS", { path: "/w/lane" })]],
  ["unknown tool", "webfetch", { url: "x" }, []],
];

for (const [name, tool, args, want] of cases) {
  test(name, () => assert.deepEqual(toClaudeEvents(tool, args, cwd, "s1"), want));
}

test("patchPaths ignores body lines", () => {
  assert.deepEqual(patchPaths("+*** Add File: no\n*** Add File: yes\n"), ["yes"]);
});

// The headers opencode 2.0.23 itself accepts (trimmed lines): a patch it applies must not slip past the guard.
const patchCases = [
  ["plain", "*** Begin Patch\n*** Update File: a.go\n@@\n*** End Patch", ["a.go"]],
  ["indented", "*** Begin Patch\n  *** Add File: b.go\n+x\n*** End Patch", ["b.go"]],
  ["CRLF", "*** Begin Patch\r\n*** Delete File: c.go\r\n*** End Patch\r\n", ["c.go"]],
  ["tab-indented", "\t*** Update File: d.go\n", ["d.go"]],
  ["Move to with CRLF", "*** Update File: e.go\r\n*** Move to:  f.go\r\n@@\r\n", ["e.go", "f.go"]],
];
for (const [name, text, want] of patchCases) {
  test(`patchPaths: ${name}`, () => assert.deepEqual(patchPaths(text), want));
}

test("patchPaths: a non-empty patch naming no file throws; an empty one names none", () => {
  assert.throws(() => patchPaths("*** Begin Patch\n@@\n-x\n+y\n*** End Patch"), /^Error: obor: cannot read the patch's files; denied$/);
  assert.deepEqual(patchPaths(""), []);
});

// feed is a stand-in for opencode's ctx.event.subscribe(): an async iterable the test emits into; emit's promise
// resolves once the plugin asks for the next event with none left (it has handled every one emitted).
function feed() {
  const queue = [];
  let waiter = null, idle = null;
  const iterable = {
    [Symbol.asyncIterator]() {
      return this;
    },
    next() {
      if (queue.length) return Promise.resolve({ value: queue.shift(), done: false });
      if (idle) idle(), (idle = null);
      return new Promise((r) => (waiter = r));
    },
    return: () => Promise.resolve({ done: true }),
  };
  const emit = (...evs) => {
    const handled = new Promise((r) => (idle = r));
    queue.push(...evs);
    if (waiter) {
      const w = waiter;
      waiter = null;
      w({ value: queue.shift(), done: false });
    }
    return handled;
  };
  return { iterable, emit };
}

// The hooks against a stub kit: a copy of obor.js beside a bin/obor that logs each call (with the identity env it
// sees) and answers per its mode (allow, deny or hang: each stub keeps its own, so the hang test runs beside the others), loaded through the opencode 2.x setup entry.
// A checkout option makes the project a git repository: "obor" (an obor checkout: bin/obor and a plugin.json named
// obor) or "other" (plugin.json named other), entered at its main checkout or, with lane, at its lane worktree; each
// bin/obor stub also logs its name (kit, main or lane) to <base>/who.
// model sets OBOR_OPENCODE_MODEL for the setup call (setup reads it first, before any await), version opencode's
// version, and switched collects the ctx.session.switchModel calls (none: the context has no switchModel).
async function stubPlugin(t, { project = true, checkout = "", lane = false, mode = "allow", model, version = "2.2.0", switched } = {}) {
  const base = fs.mkdtempSync(path.join(os.tmpdir(), "obor-oc-"));
  t.after(() => fs.rmSync(base, { recursive: true, force: true }));
  const kit = path.join(base, "kit");
  fs.mkdirSync(path.join(kit, "opencode"), { recursive: true });
  fs.copyFileSync(new URL("./obor.js", import.meta.url), path.join(kit, "opencode", "obor.js"));
  const log = path.join(base, "log");
  const stub = (root, name) => {
    fs.mkdirSync(path.join(root, "bin"), { recursive: true });
    fs.writeFileSync(
      path.join(root, "bin", "obor"),
      `#!/bin/bash
echo ${name} >> ${base}/who
echo "$2 OBOR_NO_SELF_HOST=$OBOR_NO_SELF_HOST id=\${OBOR_AGENT-}/\${OBOR_ROLE-}/\${OBOR_LANE-}/\${HERDR_PANE_ID-} $(cat)" >> ${log}
case "${mode}:$2" in
  deny:pre-tool-use) echo "obor: denied by the stub" >&2; exit 2 ;;
  hang:*) sleep 30 ;;
  *:session-start) echo "obor: role worker, lane D-01" ;;
esac
`,
    );
  };
  stub(kit, "kit");
  let dir = path.join(base, "lane");
  fs.mkdirSync(path.join(dir, ".obor"), { recursive: true });
  if (project) fs.writeFileSync(path.join(dir, ".obor", "config.json"), "{}");
  if (checkout) {
    const git = (...a) => execFileSync("git", ["-c", "user.name=t", "-c", "user.email=t@t", "-C", ...a], { stdio: "ignore" });
    stub(dir, "main");
    fs.mkdirSync(path.join(dir, ".claude-plugin"));
    fs.writeFileSync(path.join(dir, ".claude-plugin", "plugin.json"), JSON.stringify({ name: checkout }));
    git(dir, "init", "-q");
    git(dir, "add", ".");
    git(dir, "commit", "-q", "-m", "x");
    if (lane) {
      const wt = path.join(base, "lanes", "D-01");
      git(dir, "worktree", "add", "-q", "--detach", wt);
      stub(wt, "lane");
      dir = wt;
    }
  }
  const mod = await import(pathToFileURL(path.join(kit, "opencode", "obor.js")).href + "?" + base);
  const hooked = {};
  const events = feed();
  const session = { hook: async (name, fn) => (hooked["session:" + name] = fn) };
  if (switched) session.switchModel = async (a) => void switched.push(a);
  const was = process.env.OBOR_OPENCODE_MODEL;
  if (model === undefined) delete process.env.OBOR_OPENCODE_MODEL;
  else process.env.OBOR_OPENCODE_MODEL = model;
  const done = mod.default.setup({
    location: { directory: dir },
    app: { name: "cli", version, channel: "latest" },
    tool: { hook: async (name, fn) => (hooked["tool:" + name] = fn) },
    session,
    event: { subscribe: (opts) => ((hooked["event:subscribe"] = opts), events.iterable) },
  });
  if (was === undefined) delete process.env.OBOR_OPENCODE_MODEL;
  else process.env.OBOR_OPENCODE_MODEL = was;
  await done;
  const calls = () => (fs.existsSync(log) ? fs.readFileSync(log, "utf8").trim().split("\n") : []);
  const event = (line) => JSON.parse(line.replace(/^\S+ \S+ \S+ /, ""));
  const who = () => (fs.existsSync(path.join(base, "who")) ? fs.readFileSync(path.join(base, "who"), "utf8").trim().split("\n") : []);
  const names = () => calls().map((l) => l.split(" ")[0]);
  return { hooked, calls, event, dir, mod, who, emit: events.emit, names };
}

test("the default export is the 2.x entry only", async (t) => {
  const { mod } = await stubPlugin(t);
  assert.deepEqual(Object.keys(mod.default).sort(), ["id", "setup"]);
  assert.deepEqual(Object.keys(mod).sort(), ["claudeError", "databasePath", "default", "errorDetails", "modelRef", "modelSwitch", "patchPaths", "sessionHooks", "toClaudeEvents"]);
});

test("setup: the guard, the context and the session events through its tool, session and event hooks", async (t) => {
  const { hooked } = await stubPlugin(t);
  assert.deepEqual(Object.keys(hooked).sort(), ["event:subscribe", "session:context", "tool:execute.before"]);
  assert.deepEqual(hooked["event:subscribe"].types.sort(), [
    "session.created",
    "session.execution.failed",
    "session.execution.started",
    "session.execution.succeeded",
    "session.inbox.enqueued",
  ]);
});

test("guard: allow runs pre-tool-use with the Claude event; read is not sent", async (t) => {
  const { hooked, calls, event, dir } = await stubPlugin(t);
  await hooked["tool:execute.before"]({ tool: "edit", sessionID: "s1", input: { path: "a.go" } });
  await hooked["tool:execute.before"]({ tool: "read", sessionID: "s1", input: { path: "a.go" } });
  const c = calls();
  assert.equal(c.length, 1, c.join("\n"));
  assert.match(c[0], /^pre-tool-use OBOR_NO_SELF_HOST=1 /);
  assert.deepEqual(event(c[0]), { hook_event_name: "PreToolUse", session_id: "s1", cwd: dir, tool_name: "Edit", tool_input: { file_path: dir + "/a.go" } });
});

test("guard and session-start: obor hook sees none of the plugin process's pane-identity vars", async (t) => {
  const saved = { OBOR_AGENT: process.env.OBOR_AGENT, OBOR_ROLE: process.env.OBOR_ROLE, OBOR_LANE: process.env.OBOR_LANE, HERDR_PANE_ID: process.env.HERDR_PANE_ID };
  t.after(() => {
    for (const [k, v] of Object.entries(saved)) v === undefined ? delete process.env[k] : (process.env[k] = v);
  });
  Object.assign(process.env, { OBOR_AGENT: "x", OBOR_ROLE: "lead", OBOR_LANE: "L-09", HERDR_PANE_ID: "w1:p1" });
  const { hooked, calls } = await stubPlugin(t);
  await hooked["tool:execute.before"]({ tool: "shell", sessionID: "s1", input: { command: "ls" } });
  await hooked["session:context"]({ sessionID: "s1", system: [] });
  const c = calls();
  assert.equal(c.length, 2, c.join("\n"));
  for (const line of c) assert.match(line, /^\S+ OBOR_NO_SELF_HOST=1 id=\/\/\/ /, line);
});

test("guard: a deny throws obor's line", async (t) => {
  const { hooked, calls, event, dir } = await stubPlugin(t, { mode: "deny" });
  await assert.rejects(hooked["tool:execute.before"]({ tool: "write", sessionID: "s2", input: { path: dir + "/x.go", content: "" } }), /^Error: obor: denied by the stub$/);
  const c = calls();
  assert.equal(c.length, 1);
  assert.equal(event(c[0]).tool_name, "Write");
  assert.equal(event(c[0]).tool_input.file_path, dir + "/x.go");
});

test("guard: an unreadable patch is denied without running obor", async (t) => {
  const { hooked, calls } = await stubPlugin(t);
  await assert.rejects(hooked["tool:execute.before"]({ tool: "apply_patch", sessionID: "s1", input: { patchText: "*** Begin Patch\n+x\n*** End Patch" } }), /cannot read the patch's files; denied/);
  assert.equal(calls().length, 0);
});

test("session-start: context appended on every context call, obor run once per session, with its id and store", async (t) => {
  const { hooked, calls, event, dir } = await stubPlugin(t);
  for (let i = 0; i < 2; i++) {
    const c = { sessionID: "s2", system: [{ type: "text", text: "base" }] };
    await hooked["session:context"](c);
    assert.deepEqual(c.system, [{ type: "text", text: "base" }, { type: "text", text: "obor: role worker, lane D-01" }]);
  }
  const c = calls();
  assert.equal(c.length, 1, c.join("\n"));
  assert.deepEqual(event(c[0]), { hook_event_name: "SessionStart", session_id: "s2", transcript_path: databasePath("latest"), cwd: dir, source: "startup" });
});

test("setup outside an obor project registers nothing: no guard, no context, no session events", async (t) => {
  const { hooked } = await stubPlugin(t, { project: false });
  assert.deepEqual(hooked, {});
});

// T-478: bin/obor's self-hosting rule, so obor-kit's own lanes run its guard, not the global link's older kit.
const kitCases = [
  ["outside a git repository: the plugin's kit", {}, "kit"],
  ["an obor checkout's main checkout: its bin/obor", { checkout: "obor" }, "main"],
  ["an obor checkout's lane worktree: the main checkout's bin/obor, not the lane's", { checkout: "obor", lane: true }, "main"],
  ["another repository's lane: the plugin's kit", { checkout: "other", lane: true }, "kit"],
];
for (const [name, opts, want] of kitCases) {
  test(`kit: ${name}`, async (t) => {
    const { hooked, calls, who } = await stubPlugin(t, opts);
    await hooked["tool:execute.before"]({ tool: "edit", sessionID: "s1", input: { path: "a.go" } });
    await hooked["session:context"]({ sessionID: "s1", system: [] });
    assert.deepEqual(who(), [want, want]);
    for (const line of calls()) assert.match(line, /^\S+ OBOR_NO_SELF_HOST=1 /, line);
  });
}

// T-578: opencode v2.2's session events (the probe at the top of obor.js) mapped to Claude Code's hooks. created is
// each event's time (ms); an event with at names its location (session.execution.* carry none in opencode).
const CREATED = 1793361600000;
const oc = (type, data, at) => ({ id: "evt_" + type, type, created: CREATED, ...(at ? { location: { directory: at } } : {}), data });
const wake = "[obor job T-1/1/work] call the obor job_next tool";
const turn = (dir, id, text) => [
  oc("session.inbox.enqueued", { sessionID: id, inboxID: "msg_1", item: { type: "user", payload: { text }, delivery: "steer" } }, dir),
  oc("session.execution.started", { sessionID: id }),
];
const rateLimit = { type: "provider.rate-limit", message: "You exceeded your current quota; resets at 6pm", status: 429, retryAfterMs: 3600000 };

test("session events: session start, turn start and turn end run session-start, user-prompt-submit and stop", async (t) => {
  const { emit, names, calls, event, dir } = await stubPlugin(t);
  const store = databasePath("latest");
  await emit(oc("session.created", { sessionID: "s1", slug: "x", version: "2.2.0" }, dir), ...turn(dir, "s1", wake), oc("session.execution.succeeded", { sessionID: "s1" }));
  assert.deepEqual(names(), ["session-start", "user-prompt-submit", "stop"]);
  const [start, prompt, stop] = calls().map(event);
  assert.deepEqual(start, { hook_event_name: "SessionStart", session_id: "s1", transcript_path: store, cwd: dir, source: "startup" });
  assert.deepEqual(prompt, { hook_event_name: "UserPromptSubmit", session_id: "s1", transcript_path: store, cwd: dir, prompt: wake });
  assert.deepEqual(stop, { hook_event_name: "Stop", session_id: "s1", transcript_path: store, cwd: dir, stop_hook_active: false });
  for (const line of calls()) assert.match(line, /^\S+ OBOR_NO_SELF_HOST=1 id=\/\/\/ /, line);
});

test("session events: a later turn runs no second session-start; its prompt is that turn's inbox; context reuses the start", async (t) => {
  const { emit, names, calls, event, dir, hooked } = await stubPlugin(t);
  await emit(...turn(dir, "s1", "first"), oc("session.execution.succeeded", { sessionID: "s1" }));
  const c = { sessionID: "s1", system: [] };
  await hooked["session:context"](c);
  assert.deepEqual(c.system, [{ type: "text", text: "obor: role worker, lane D-01" }]);
  await emit(...turn(dir, "s1", "second a"), ...turn(dir, "s1", "second b").slice(0, 1));
  await emit(oc("session.execution.succeeded", { sessionID: "s1" }), ...turn(dir, "s1", "third"));
  assert.deepEqual(names(), ["session-start", "user-prompt-submit", "stop", "user-prompt-submit", "stop", "user-prompt-submit"]);
  const prompts = calls().map(event).filter((e) => e.hook_event_name === "UserPromptSubmit").map((e) => e.prompt);
  assert.deepEqual(prompts, ["first", "second a", "second b\n\nthird"]);
});

test("limit error: the turn's failure sends stop-failure (rate_limit, its reset epoch); its retries send nothing", async (t) => {
  const { emit, names, calls, event, dir } = await stubPlugin(t);
  const retry = (attempt, error) => oc("session.retry.scheduled", { sessionID: "s1", assistantMessageID: "msg_2", attempt, at: CREATED + 900000, error }, dir);
  await emit(...turn(dir, "s1", wake), retry(2, { type: "provider.transport", message: "ECONNREFUSED" }));
  assert.deepEqual(names(), ["session-start", "user-prompt-submit"], "a transport retry is no stop");
  await emit(retry(3, rateLimit), retry(4, rateLimit));
  assert.deepEqual(names(), ["session-start", "user-prompt-submit"], "a rate-limit retry is no stop: opencode retries inside the turn");
  await emit(oc("session.execution.failed", { sessionID: "s1", error: rateLimit }));
  assert.deepEqual(names(), ["session-start", "user-prompt-submit", "stop-failure"]);
  assert.deepEqual(event(calls()[2]), {
    hook_event_name: "StopFailure",
    session_id: "s1",
    transcript_path: databasePath("latest"),
    cwd: dir,
    error: "rate_limit",
    error_details: "You exceeded your current quota; resets at 6pm (provider.rate-limit)|1793365200",
  });
  // The next turn may stop again.
  await emit(...turn(dir, "s1", "again"), oc("session.execution.failed", { sessionID: "s1", error: { type: "provider.budget-limit", message: "budget spent" } }));
  assert.deepEqual(names().slice(3), ["user-prompt-submit", "stop-failure"]);
  const last = event(calls()[4]);
  assert.equal(last.error, "billing_error");
  assert.equal(last.error_details, "budget spent (provider.budget-limit)");
});

test("session events: another directory's session, a child session and an interrupted turn run no hook", async (t) => {
  const { emit, names, dir } = await stubPlugin(t);
  const other = path.join(path.dirname(dir), "elsewhere");
  await emit(...turn(other, "s9", wake), oc("session.execution.succeeded", { sessionID: "s9" }), oc("session.execution.failed", { sessionID: "s9", error: rateLimit }));
  await emit(oc("session.created", { sessionID: "c1", parentID: "s1", slug: "x", version: "2.2.0" }, dir), ...turn(dir, "c1", "sub"), oc("session.execution.succeeded", { sessionID: "c1" }));
  await emit(oc("session.execution.started", { sessionID: "unknown" }), oc("session.execution.succeeded", { sessionID: "unknown" }));
  assert.deepEqual(names(), []);
  await emit(...turn(dir, "s1", wake), oc("session.execution.interrupted", { sessionID: "s1", reason: "user" }));
  assert.deepEqual(names(), ["session-start", "user-prompt-submit"], "precondition: this directory's session runs its hooks");
});

// T-703: the start's model (OBOR_OPENCODE_MODEL) as opencode's Model.Ref.
test("modelRef: provider/model[#variant]; none for an empty or malformed value", () => {
  assert.deepEqual(modelRef("opencode-go/deepseek-v4.1-flash#max"), { providerID: "opencode-go", id: "deepseek-v4.1-flash", variant: "max" });
  assert.deepEqual(modelRef("p/m"), { providerID: "p", id: "m" });
  assert.deepEqual(modelRef("p/org/m#high"), { providerID: "p", id: "org/m", variant: "high" }, "the model id keeps its own slashes");
  for (const bad of ["", undefined, "m", "/m", "p/", "#high"]) assert.equal(modelRef(bad), null, String(bad));
});

// fakeSession is a ctx for modelSwitch: switchModel calls collected, get answering parents (session → parentID).
function fakeSession({ version = "2.2.0", parents = {}, fail = false, noSwitch = false } = {}) {
  const switched = [];
  const session = { get: async ({ sessionID }) => ({ id: sessionID, ...(parents[sessionID] ? { parentID: parents[sessionID] } : {}) }) };
  if (!noSwitch) {
    session.switchModel = async (a) => {
      if (fail) throw new Error("boom");
      switched.push(a);
    };
  }
  return { ctx: { app: { name: "cli", version, channel: "latest" }, session }, switched };
}

test("modelSwitch: each new root session switched once, before its turn; later prompts and other sessions", async () => {
  const { ctx, switched } = fakeSession();
  const warned = [];
  const hook = modelSwitch(ctx, "go/ds#high", () => false, (l) => warned.push(l));
  assert.equal(typeof hook, "function");
  assert.deepEqual(switched, [], "precondition: nothing before a prompt");
  await hook({ sessionID: "s1", prompt: { text: "wake" } });
  assert.deepEqual(switched, [{ sessionID: "s1", model: { providerID: "go", id: "ds", variant: "high" } }]);
  await hook({ sessionID: "s1", prompt: { text: "turn two" } });
  assert.equal(switched.length, 1, "a later turn of the session: no second switch");
  await hook({ sessionID: "s2", prompt: { text: "wake" } });
  assert.deepEqual(switched.map((a) => a.sessionID), ["s1", "s2"], "another new root session: switched too");
  assert.deepEqual(warned, []);
});

test("modelSwitch: never a child, whether the events or the store name its parent", async () => {
  const { ctx, switched } = fakeSession({ parents: { c2: "s1" } });
  const hook = modelSwitch(ctx, "go/ds", (id) => id === "c1");
  await hook({ sessionID: "c1" });
  await hook({ sessionID: "c2" });
  assert.deepEqual(switched, [], "no child switched");
  await hook({ sessionID: "s1" });
  assert.deepEqual(switched, [{ sessionID: "s1", model: { providerID: "go", id: "ds" } }], "precondition: a root session is");
});

test("modelSwitch: nothing without the variable; one warning and nothing off opencode 2, without switchModel or for a malformed value", async () => {
  const warned = [];
  const warn = (l) => warned.push(l);
  assert.equal(modelSwitch(fakeSession().ctx, "", undefined, warn), null);
  assert.equal(modelSwitch(fakeSession().ctx, undefined, undefined, warn), null);
  assert.deepEqual(warned, [], "no variable: not even a warning (the owner's own windows)");
  for (const [name, ctx, value] of [
    ["opencode 1.x", fakeSession({ version: "1.18.3" }).ctx, "go/ds"],
    ["opencode 3.x", fakeSession({ version: "3.0.0" }).ctx, "go/ds"],
    ["no version", { session: fakeSession().ctx.session }, "go/ds"],
    ["no switchModel", fakeSession({ noSwitch: true }).ctx, "go/ds"],
    ["malformed", fakeSession().ctx, "ds"],
  ]) {
    warned.length = 0;
    assert.equal(modelSwitch(ctx, value, undefined, warn), null, name);
    assert.equal(warned.length, 1, `${name}: one line`);
    assert.match(warned[0], /^obor: .*OBOR_OPENCODE_MODEL/, name);
  }
});

test("modelSwitch: a failed switch warns and lets the turn run", async () => {
  const warned = [];
  const hook = modelSwitch(fakeSession({ fail: true }).ctx, "go/ds", undefined, (l) => warned.push(l));
  await hook({ sessionID: "s1" });
  assert.equal(warned.length, 1);
  assert.match(warned[0], /switching session s1 to go\/ds failed: boom/);
});

test("setup: the prompt hook only with OBOR_OPENCODE_MODEL on opencode 2 with switchModel; it switches the root session", async (t) => {
  const switched = [];
  const { hooked } = await stubPlugin(t, { model: "go/ds#max", switched });
  assert.equal(typeof hooked["session:prompt"], "function");
  await hooked["session:prompt"]({ sessionID: "s1", prompt: { text: "wake" } });
  assert.deepEqual(switched, [{ sessionID: "s1", model: { providerID: "go", id: "ds", variant: "max" } }]);
  for (const [name, opts] of [
    ["no variable", { switched: [] }],
    ["opencode 1.x", { model: "go/ds", version: "1.18.3", switched: [] }],
    ["no switchModel", { model: "go/ds" }],
    ["outside an obor project", { model: "go/ds", switched: [], project: false }],
  ]) {
    const errs = [];
    const orig = console.error;
    console.error = (l) => errs.push(l);
    try {
      const { hooked: h } = await stubPlugin(t, opts);
      assert.equal(h["session:prompt"], undefined, name);
    } finally {
      console.error = orig;
    }
  }
});

test("claudeError: Claude Code's StopFailure errors for opencode's error types", () => {
  const cases = [
    [{ type: "provider.rate-limit" }, "rate_limit"],
    [{ type: "provider.free-limit" }, "rate_limit"],
    [{ type: "provider.quota", retryAfterMs: 60000 }, "rate_limit"],
    [{ type: "provider.quota" }, "billing_error"],
    [{ type: "provider.budget-limit" }, "billing_error"],
    [{ type: "provider.model-budget-limit" }, "billing_error"],
    [{ type: "provider.auth" }, "authentication_failed"],
    [{ type: "provider.invalid-request" }, "invalid_request"],
    [{ type: "provider.internal" }, "server_error"],
    [{ type: "provider.something-new" }, "unknown"],
    [undefined, "unknown"],
  ];
  for (const [err, want] of cases) assert.equal(claudeError(err), want, JSON.stringify(err));
});

test("errorDetails: message and type, and |<epoch> only with a reset", () => {
  assert.equal(errorDetails(rateLimit, CREATED), "You exceeded your current quota; resets at 6pm (provider.rate-limit)|1793365200");
  assert.equal(errorDetails({ type: "provider.auth", message: "bad key" }, CREATED), "bad key (provider.auth)");
  assert.equal(errorDetails(undefined, CREATED), "(unknown)");
});

test("databasePath: opencode's store, by data dir, channel and OPENCODE_DB", () => {
  const env = { XDG_DATA_HOME: "/d" };
  assert.equal(databasePath("latest", env), "/d/opencode/opencode.db");
  assert.equal(databasePath("dev", env), "/d/opencode/opencode-dev.db");
  assert.equal(databasePath("dev", { ...env, OPENCODE_DISABLE_CHANNEL_DB: "1" }), "/d/opencode/opencode.db");
  assert.equal(databasePath("latest", { ...env, OPENCODE_DB: "x.db" }), "/d/opencode/x.db");
  assert.equal(databasePath("latest", { ...env, OPENCODE_DB: "/abs/y.db" }), "/abs/y.db");
  assert.equal(databasePath("latest", { ...env, OPENCODE_DB: ":memory:" }), "");
  assert.equal(databasePath("latest", {}), path.join(os.homedir(), ".local", "share", "opencode", "opencode.db"));
});

// Each mapped event carries the fields its Go hook reads: UserPromptSubmit against a Claude capture
// (internal/guard/testdata/prompt-typed.json, read by guard.UserPromptSubmit), the others against the fields their
// hooks read (guard.SessionStart: cwd, session_id; guard.Stop: cwd, session_id, transcript_path, stop_hook_active;
// guard.StopFailure: cwd, error, error_details), each a field Claude Code sends.
test("each mapped event has the fields the Go hook reads", async (t) => {
  const { emit, calls, event, dir } = await stubPlugin(t);
  await emit(...turn(dir, "s1", wake), oc("session.execution.succeeded", { sessionID: "s1" }), ...turn(dir, "s1", "x"), oc("session.execution.failed", { sessionID: "s1", error: rateLimit }));
  const byName = Object.fromEntries(calls().map(event).map((e) => [e.hook_event_name, e]));
  assert.deepEqual(Object.keys(byName).sort(), ["SessionStart", "Stop", "StopFailure", "UserPromptSubmit"]);
  const capture = JSON.parse(fs.readFileSync(new URL("../internal/guard/testdata/prompt-typed.json", import.meta.url), "utf8"));
  for (const k of Object.keys(byName.UserPromptSubmit)) assert.ok(k in capture, `UserPromptSubmit.${k} is not in Claude's capture`);
  const reads = {
    UserPromptSubmit: ["cwd", "session_id", "transcript_path", "prompt"],
    SessionStart: ["cwd", "session_id", "transcript_path"],
    Stop: ["cwd", "session_id", "transcript_path", "stop_hook_active"],
    StopFailure: ["cwd", "session_id", "error", "error_details"],
  };
  for (const [name, keys] of Object.entries(reads)) {
    for (const k of keys) {
      assert.equal(typeof byName[name][k], k === "stop_hook_active" ? "boolean" : "string", `${name}.${k}`);
      if (k !== "stop_hook_active") assert.notEqual(byName[name][k], "", `${name}.${k} empty`);
    }
  }
});

// The hanging guard waits out the plugin's 5 s timeout, so it starts when this file loads and runs beside the tests
// above (its stub keeps its own mode); its test, last, only collects the result.
const hanging = (async () => {
  const cleanups = [];
  const { hooked } = await stubPlugin({ after: (fn) => cleanups.push(fn) }, { mode: "hang" });
  const t0 = Date.now();
  const err = await hooked["tool:execute.before"]({ tool: "write", sessionID: "s1", input: { path: "x" } }).then(() => null, (e) => e);
  return { err, ms: Date.now() - t0, cleanup: () => cleanups.forEach((fn) => fn()) };
})();

test("guard: a hanging obor denies after 5 s (fails closed)", async (t) => {
  const { err, ms, cleanup } = await hanging;
  t.after(cleanup);
  assert.match(String(err), /guard unavailable \(obor hook pre-tool-use timed out/);
  assert.ok(ms >= 4900 && ms < 9000, `${ms} ms`);
});
