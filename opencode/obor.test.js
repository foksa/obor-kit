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
import { toClaudeEvents, patchPaths, claudeError, errorDetails, tokensPath, modelRef, modelSwitch, promptContext, stopBlock } from "./obor.js";

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
// sees) and answers per its mode (allow, deny, answer, block, fail or hang: each stub keeps its own, so the hang test
// runs beside the others), loaded through the opencode 2.x setup entry.
// A checkout option makes the project a git repository: "obor" (an obor checkout: bin/obor and a plugin.json named
// obor) or "other" (plugin.json named other), entered at its main checkout or, with lane, at its lane worktree; each
// bin/obor stub also logs its name (kit, main or lane) to <base>/who.
// model sets OBOR_OPENCODE_MODEL for the setup call (setup reads it first, before any await), version opencode's
// version, and switched collects the ctx.session.switchModel calls (none: the context has no switchModel). prompted
// collects the ctx.session.prompt calls of a stop's block.
async function stubPlugin(t, { project = true, checkout = "", lane = false, mode = "allow", model, version = "2.2.0", switched, prompted = [] } = {}) {
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
event=$(cat)
echo ${name} >> ${base}/who
echo "$2 OBOR_NO_SELF_HOST=$OBOR_NO_SELF_HOST id=\${OBOR_AGENT-}/\${OBOR_ROLE-}/\${OBOR_LANE-}/\${HERDR_PANE_ID-} $event" >> ${log}
case "${mode}:$2" in
  deny:pre-tool-use) echo "obor: denied by the stub" >&2; exit 2 ;;
  answer:user-prompt-submit) echo '{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"obor: the lead has lines"}}' ;;
  block:stop) case "$event" in *'"stop_hook_active":true'*) ;; *) echo '{"decision":"block","reason":"obor (worker L-01): not done yet"}' ;; esac ;;
  fail:user-prompt-submit) echo "obor: the stub failed" >&2; exit 1 ;;
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
  const session = {
    hook: async (name, fn) => (hooked["session:" + name] = fn),
    prompt: async (a) => void prompted.push(a),
  };
  if (switched) session.switchModel = async (a) => void switched.push(a);
  const was = process.env.OBOR_OPENCODE_MODEL;
  if (model === undefined) delete process.env.OBOR_OPENCODE_MODEL;
  else process.env.OBOR_OPENCODE_MODEL = model;
  // HOME is this stub's: a step writes the plugin's token file under it, never the runner's own ~/.obor.
  const wasHome = process.env.HOME;
  process.env.HOME = base;
  const done = mod.default.setup({
    location: { directory: dir },
    app: { name: "cli", version, channel: "latest" },
    tool: { hook: async (name, fn) => (hooked["tool:" + name] = fn) },
    session,
    event: { subscribe: (opts) => ((hooked["event:subscribe"] = opts), events.iterable) },
  });
  if (was === undefined) delete process.env.OBOR_OPENCODE_MODEL;
  else process.env.OBOR_OPENCODE_MODEL = was;
  if (wasHome === undefined) delete process.env.HOME;
  else process.env.HOME = wasHome;
  await done;
  const calls = () => (fs.existsSync(log) ? fs.readFileSync(log, "utf8").trim().split("\n") : []);
  const event = (line) => JSON.parse(line.replace(/^\S+ \S+ \S+ /, ""));
  const who = () => (fs.existsSync(path.join(base, "who")) ? fs.readFileSync(path.join(base, "who"), "utf8").trim().split("\n") : []);
  const names = () => calls().map((l) => l.split(" ")[0]);
  const token = (sid) => tokensPath(sid, { HOME: base });
  return { hooked, calls, event, dir, mod, who, emit: events.emit, names, token, prompted };
}

test("the default export is the 2.x entry only", async (t) => {
  const { mod } = await stubPlugin(t);
  assert.deepEqual(Object.keys(mod.default).sort(), ["id", "setup"]);
  assert.deepEqual(Object.keys(mod).sort(), ["claudeError", "default", "errorDetails", "modelRef", "modelSwitch", "patchPaths", "promptContext", "sessionHooks", "stopBlock", "toClaudeEvents", "tokensPath"]);
});

test("setup: the guard, the context, the prompt and the session events through its tool, session and event hooks", async (t) => {
  const { hooked } = await stubPlugin(t);
  assert.deepEqual(Object.keys(hooked).sort(), ["event:subscribe", "session:context", "session:prompt", "tool:execute.before"]);
  assert.deepEqual(hooked["event:subscribe"].types.sort(), [
    "session.created",
    "session.execution.failed",
    "session.execution.started",
    "session.execution.succeeded",
    "session.inbox.enqueued",
    "session.step.ended",
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

test("guard and session-start: obor hook sees the plugin process's identity vars (the pane's agent's, T-812)", async (t) => {
  const saved = { OBOR_AGENT: process.env.OBOR_AGENT, OBOR_ROLE: process.env.OBOR_ROLE, OBOR_LANE: process.env.OBOR_LANE, HERDR_PANE_ID: process.env.HERDR_PANE_ID };
  t.after(() => {
    for (const [k, v] of Object.entries(saved)) v === undefined ? delete process.env[k] : (process.env[k] = v);
  });
  delete process.env.OBOR_AGENT;
  Object.assign(process.env, { OBOR_ROLE: "lead", OBOR_LANE: "L-09", HERDR_PANE_ID: "w1:p1" });
  const { hooked, calls } = await stubPlugin(t);
  await hooked["tool:execute.before"]({ tool: "shell", sessionID: "s1", input: { command: "ls" } });
  await hooked["session:context"]({ sessionID: "s1", system: [] });
  const c = calls();
  assert.equal(c.length, 2, c.join("\n"));
  for (const line of c) assert.match(line, /^\S+ OBOR_NO_SELF_HOST=1 id=\/lead\/L-09\/w1:p1 /, line);
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

test("session-start: context appended on every context call, obor run once per session, with its id and token file", async (t) => {
  const { hooked, calls, event, dir, token } = await stubPlugin(t);
  for (let i = 0; i < 2; i++) {
    const c = { sessionID: "s2", system: [{ type: "text", text: "base" }] };
    await hooked["session:context"](c);
    assert.deepEqual(c.system, [{ type: "text", text: "base" }, { type: "text", text: "obor: role worker, lane D-01" }]);
  }
  const c = calls();
  assert.equal(c.length, 1, c.join("\n"));
  assert.deepEqual(event(c[0]), { hook_event_name: "SessionStart", session_id: "s2", transcript_path: token("s2"), cwd: dir, source: "startup" });
});

test("setup outside an obor project registers nothing: no guard, no context, no session events", async (t) => {
  const { hooked } = await stubPlugin(t, { project: false });
  assert.deepEqual(hooked, {});
});

// the plugin's own kit runs in every directory, an obor checkout's main checkout and lane included (there is no
// self-hosting rule: the start's generated config names the launching kit).
const kitCases = [
  ["outside a git repository: the plugin's kit", {}, "kit"],
  ["an obor checkout's main checkout: the plugin's kit, not its bin/obor", { checkout: "obor" }, "kit"],
  ["an obor checkout's lane worktree: the plugin's kit, not the main checkout's or the lane's", { checkout: "obor", lane: true }, "kit"],
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

// opencode v2.2's session events (the probe at the top of obor.js) mapped to Claude Code's hooks. created is
// each event's time (ms); an event with at names its location (session.execution.* carry none in opencode).
const CREATED = 1793361600000;
const oc = (type, data, at) => ({ id: "evt_" + type, type, created: CREATED, ...(at ? { location: { directory: at } } : {}), data });
const wake = "[obor job T-1/1/work] call the obor job_next tool";
const turn = (dir, id, text) => [
  oc("session.inbox.enqueued", { sessionID: id, inboxID: "msg_1", item: { type: "user", payload: { text }, delivery: "steer" } }, dir),
  oc("session.execution.started", { sessionID: id }),
];
// promptOf runs the session prompt hook for one admitted prompt (opencode runs it before admission) and returns the
// payload it may have mutated.
const promptOf = (hooked, id, text, metadata) => {
  const p = { sessionID: id, prompt: { text }, ...(metadata ? { metadata } : {}) };
  return hooked["session:prompt"](p).then(() => p);
};
const rateLimit = { type: "provider.rate-limit", message: "You exceeded your current quota; resets at 6pm", status: 429, retryAfterMs: 3600000 };

test("session events: session start, turn start and turn end run session-start, user-prompt-submit and stop", async (t) => {
  const { emit, names, calls, event, dir, token, hooked } = await stubPlugin(t);
  const store = token("s1");
  await promptOf(hooked, "s1", wake);
  await emit(oc("session.created", { sessionID: "s1", slug: "x", version: "2.2.0" }, dir), ...turn(dir, "s1", wake), oc("session.execution.succeeded", { sessionID: "s1" }));
  assert.deepEqual(names(), ["session-start", "user-prompt-submit", "stop"]);
  const [start, prompt, stop] = calls().map(event);
  assert.deepEqual(start, { hook_event_name: "SessionStart", session_id: "s1", transcript_path: store, cwd: dir, source: "startup" });
  assert.deepEqual(prompt, { hook_event_name: "UserPromptSubmit", session_id: "s1", transcript_path: store, cwd: dir, prompt: wake });
  assert.deepEqual(stop, { hook_event_name: "Stop", session_id: "s1", transcript_path: store, cwd: dir, stop_hook_active: false });
  for (const line of calls()) assert.match(line, /^\S+ OBOR_NO_SELF_HOST=1 id=/, line); // the identity: the identity vars test
});

test("session events: a later turn runs no second session-start; each prompt carries its own text; context reuses the start", async (t) => {
  const { emit, names, calls, event, dir, hooked } = await stubPlugin(t);
  await promptOf(hooked, "s1", "first");
  await emit(...turn(dir, "s1", "first"), oc("session.execution.succeeded", { sessionID: "s1" }));
  const c = { sessionID: "s1", system: [] };
  await hooked["session:context"](c);
  assert.deepEqual(c.system, [{ type: "text", text: "obor: role worker, lane D-01" }]);
  await promptOf(hooked, "s1", "second");
  await emit(...turn(dir, "s1", "second"), oc("session.execution.succeeded", { sessionID: "s1" }));
  await promptOf(hooked, "s1", "third");
  await emit(...turn(dir, "s1", "third"));
  assert.deepEqual(names(), ["session-start", "user-prompt-submit", "stop", "user-prompt-submit", "stop", "user-prompt-submit"]);
  const prompts = calls().map(event).filter((e) => e.hook_event_name === "UserPromptSubmit").map((e) => e.prompt);
  assert.deepEqual(prompts, ["first", "second", "third"]);
});

test("limit error: the turn's failure sends stop-failure (rate_limit, its reset epoch); its retries send nothing", async (t) => {
  const { emit, names, calls, event, dir, token, hooked } = await stubPlugin(t);
  const retry = (attempt, error) => oc("session.retry.scheduled", { sessionID: "s1", assistantMessageID: "msg_2", attempt, at: CREATED + 900000, error }, dir);
  await promptOf(hooked, "s1", wake);
  await emit(...turn(dir, "s1", wake), retry(2, { type: "provider.transport", message: "ECONNREFUSED" }));
  assert.deepEqual(names(), ["session-start", "user-prompt-submit"], "a transport retry is no stop");
  await emit(retry(3, rateLimit), retry(4, rateLimit));
  assert.deepEqual(names(), ["session-start", "user-prompt-submit"], "a rate-limit retry is no stop: opencode retries inside the turn");
  await emit(oc("session.execution.failed", { sessionID: "s1", error: rateLimit }));
  assert.deepEqual(names(), ["session-start", "user-prompt-submit", "stop-failure"]);
  assert.deepEqual(event(calls()[2]), {
    hook_event_name: "StopFailure",
    session_id: "s1",
    transcript_path: token("s1"),
    cwd: dir,
    error: "rate_limit",
    error_details: "You exceeded your current quota; resets at 6pm (provider.rate-limit)|1793365200",
  });
  // The next turn may stop again.
  await promptOf(hooked, "s1", "again");
  await emit(...turn(dir, "s1", "again"), oc("session.execution.failed", { sessionID: "s1", error: { type: "provider.budget-limit", message: "budget spent" } }));
  assert.deepEqual(names().slice(3), ["user-prompt-submit", "stop-failure"]);
  const last = event(calls()[4]);
  assert.equal(last.error, "billing_error");
  assert.equal(last.error_details, "budget spent (provider.budget-limit)");
});

test("session events: another directory's session, a child session and an interrupted turn run no hook", async (t) => {
  const { emit, names, dir, hooked } = await stubPlugin(t);
  const other = path.join(path.dirname(dir), "elsewhere");
  await emit(...turn(other, "s9", wake), oc("session.execution.succeeded", { sessionID: "s9" }), oc("session.execution.failed", { sessionID: "s9", error: rateLimit }));
  await emit(oc("session.created", { sessionID: "c1", parentID: "s1", slug: "x", version: "2.2.0" }, dir), ...turn(dir, "c1", "sub"), oc("session.execution.succeeded", { sessionID: "c1" }));
  await emit(oc("session.execution.started", { sessionID: "unknown" }), oc("session.execution.succeeded", { sessionID: "unknown" }));
  await promptOf(hooked, "c1", "sub"); // a child's prompt: no hook
  assert.deepEqual(names(), []);
  await promptOf(hooked, "s1", wake);
  await emit(...turn(dir, "s1", wake), oc("session.execution.interrupted", { sessionID: "s1", reason: "user" }));
  assert.deepEqual(names(), ["session-start", "user-prompt-submit"], "precondition: this directory's session runs its hooks");
});

// A root session's step appends one line to its token file: the prompt side (input + cache read + cache write) and the
// step's time. A step runs no hook; a child's or another directory's session's writes no file.
test("session steps: a root session's tokens append one line to its token file", async (t) => {
  const { emit, names, dir, token } = await stubPlugin(t);
  const stepEvent = (id, tokens, created) => ({ id: "evt_step", type: "session.step.ended", created, data: { sessionID: id, tokens } });
  await emit(oc("session.created", { sessionID: "s1", slug: "x", version: "2.2.0" }, dir));
  await emit(stepEvent("s1", { input: 7507, output: 11, reasoning: 0, cache: { read: 500, write: 0 } }, CREATED));
  await emit(stepEvent("s1", { input: 10507, output: 3, reasoning: 0, cache: { read: 500, write: 0 } }, CREATED + 5000));
  const file = token("s1");
  assert.ok(fs.existsSync(file), "precondition: the first step wrote the session's token file " + file);
  const lines = fs.readFileSync(file, "utf8").trim().split("\n").map((l) => JSON.parse(l));
  assert.deepEqual(lines, [
    { type: "context", tokens: 8007, at: new Date(CREATED).toISOString() },
    { type: "context", tokens: 11007, at: new Date(CREATED + 5000).toISOString() },
  ]);
  assert.deepEqual(names(), [], "a step runs no hook");
  await emit(oc("session.created", { sessionID: "c1", parentID: "s1", slug: "x", version: "2.2.0" }, dir));
  await emit(stepEvent("c1", { input: 1, cache: { read: 0, write: 0 } }, CREATED + 6000));
  const other = path.join(path.dirname(dir), "elsewhere");
  await emit(oc("session.created", { sessionID: "s9", slug: "x", version: "2.2.0" }, other));
  await emit(stepEvent("s9", { input: 1, cache: { read: 0, write: 0 } }, CREATED + 7000));
  assert.equal(fs.readFileSync(file, "utf8").trim().split("\n").length, 2, "the root session's file keeps its two lines");
  assert.equal(fs.existsSync(token("c1")), false, "a child writes none");
  assert.equal(fs.existsSync(token("s9")), false, "another directory's session writes none");
});

// the start's model (OBOR_OPENCODE_MODEL) as opencode's Model.Ref.
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

test("setup: the prompt hook carries the answers always; it switches the root session with OBOR_OPENCODE_MODEL on opencode 2 with switchModel", async (t) => {
  const switched = [];
  const { hooked } = await stubPlugin(t, { model: "go/ds#max", switched });
  assert.equal(typeof hooked["session:prompt"], "function");
  await hooked["session:prompt"]({ sessionID: "s1", prompt: { text: "wake" } });
  assert.deepEqual(switched, [{ sessionID: "s1", model: { providerID: "go", id: "ds", variant: "max" } }]);
  for (const [name, opts] of [
    ["no variable", { switched: [] }],
    ["opencode 1.x", { model: "go/ds", version: "1.18.3", switched: [] }],
    ["no switchModel", { model: "go/ds" }],
  ]) {
    const errs = [];
    const orig = console.error;
    console.error = (l) => errs.push(l);
    try {
      const { hooked: h } = await stubPlugin(t, opts);
      assert.equal(typeof h["session:prompt"], "function", name);
      await h["session:prompt"]({ sessionID: "s1", prompt: { text: "wake" } });
      assert.deepEqual(opts.switched ?? [], [], name);
    } finally {
      console.error = orig;
    }
  }
  const { hooked: outside } = await stubPlugin(t, { model: "go/ds", project: false });
  assert.deepEqual(Object.keys(outside), [], "outside an obor project");
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

test("tokensPath: a session's token file under the HOME the kind's transcript glob names", () => {
  assert.equal(tokensPath("s1", { HOME: "/h" }), "/h/.obor/opencode/s1.jsonl");
  assert.equal(tokensPath("ses_a", { HOME: "/h" }), "/h/.obor/opencode/ses_a.jsonl", "the session id is the file name");
  assert.equal(tokensPath("s1", {}), "", "no HOME: no file to name");
});

// Each mapped event carries the fields its Go hook reads: UserPromptSubmit against a Claude capture
// (internal/guard/testdata/prompt-typed.json, read by guard.UserPromptSubmit), the others against the fields their
// hooks read (guard.SessionStart: cwd, session_id; guard.Stop: cwd, session_id, transcript_path, stop_hook_active;
// guard.StopFailure: cwd, error, error_details), each a field Claude Code sends.
test("each mapped event has the fields the Go hook reads", async (t) => {
  const { emit, calls, event, dir, hooked } = await stubPlugin(t);
  await promptOf(hooked, "s1", wake);
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

// The answers' shape: obor's user-prompt-submit writes its context in hookSpecificOutput.additionalContext, its stop a
// block with a reason (guard.promptContext, guard.Stop); anything else is no answer.
test("promptContext and stopBlock: the hooks' answers, and nothing for no answer", () => {
  assert.equal(promptContext('{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"obor: hi"}}'), "obor: hi");
  for (const none of ["", "not json", "{}", '{"hookSpecificOutput":{}}', '{"hookSpecificOutput":{"additionalContext":7}}']) {
    assert.equal(promptContext(none), "", none);
  }
  assert.equal(stopBlock('{"decision":"block","reason":"go on"}'), "go on");
  for (const none of ["", "not json", "{}", '{"decision":"allow"}', '{"decision":"block"}']) assert.equal(stopBlock(none), "", none);
});

// The prompt hook acts on user-prompt-submit's answer: its context is appended to the prompt's text, which opencode
// stores in the user message (the model and the owner see it).
test("prompt: user-prompt-submit's context is appended to the prompt's text", async (t) => {
  const { hooked, calls, names, event } = await stubPlugin(t, { mode: "answer" });
  const p = await promptOf(hooked, "s1", wake);
  assert.equal(p.prompt.text, wake + "\n\nobor: the lead has lines");
  assert.deepEqual(names(), ["session-start", "user-prompt-submit"]);
  const asked = calls().map(event).find((e) => e.hook_event_name === "UserPromptSubmit");
  assert.equal(asked.prompt, wake, "the hook asked with the prompt as typed, before the context");
});

test("prompt: no answer or a failing hook leaves the prompt as typed", async (t) => {
  for (const [name, mode, want] of [["no answer", "allow", ["session-start", "user-prompt-submit"]], ["a failing hook", "fail", ["session-start", "user-prompt-submit"]]]) {
    const { hooked, names } = await stubPlugin(t, { mode });
    const p = await promptOf(hooked, "s1", wake);
    assert.equal(p.prompt.text, wake, name);
    assert.deepEqual(names(), want, name);
  }
});

// A stop's block becomes the session's next prompt, marked metadata.obor (obor's own line: the prompt hook runs no
// second reminder for it), and the stop that ends that turn carries stop_hook_active: true — once per stop loop.
test("stop: a block prompts the session with its reason, and the loop's next stop says stop_hook_active", async (t) => {
  const { emit, hooked, calls, event, dir, prompted } = await stubPlugin(t, { mode: "block" });
  const stops = () => calls().map(event).filter((e) => e.hook_event_name === "Stop").map((e) => e.stop_hook_active);
  await promptOf(hooked, "s1", wake);
  await emit(...turn(dir, "s1", wake), oc("session.execution.succeeded", { sessionID: "s1" }));
  assert.deepEqual(prompted, [{ sessionID: "s1", text: "obor (worker L-01): not done yet", metadata: { obor: "stop" } }],
    "one prompt with the block's reason, obor's own");
  assert.deepEqual(stops(), [false]);
  await emit(...turn(dir, "s1", "obor (worker L-01): not done yet"), oc("session.execution.succeeded", { sessionID: "s1" }));
  assert.deepEqual(stops(), [false, true], "the stop ending the block's turn is the stop loop's");
  assert.equal(prompted.length, 1, "that stop blocks nothing again");
  await promptOf(hooked, "s1", "next");
  await emit(...turn(dir, "s1", "next"), oc("session.execution.succeeded", { sessionID: "s1" }));
  assert.deepEqual(stops(), [false, true, false], "a turn after the loop is a new one");
  assert.equal(prompted.length, 2, "and its block is a new stop loop");
});

test("stop: no block (or a failure) prompts nothing and the next stop is a new loop", async (t) => {
  const { emit, hooked, calls, event, dir, prompted } = await stubPlugin(t);
  await promptOf(hooked, "s1", wake);
  await emit(...turn(dir, "s1", wake), oc("session.execution.succeeded", { sessionID: "s1" }), ...turn(dir, "s1", "next"), oc("session.execution.succeeded", { sessionID: "s1" }));
  assert.deepEqual(prompted, []);
  assert.deepEqual(calls().map(event).filter((e) => e.hook_event_name === "Stop").map((e) => e.stop_hook_active), [false, false]);
});

test("prompt: a prompt that carries metadata.obor is obor's own: no user-prompt-submit runs", async (t) => {
  const { hooked, names } = await stubPlugin(t, { mode: "answer" });
  const text = "obor (worker L-01): not done yet";
  const p = await promptOf(hooked, "s1", text, { obor: "stop" });
  assert.equal(p.prompt.text, text, "no context appended");
  assert.deepEqual(names(), [], "no hook, not even session-start");
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
