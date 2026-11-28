// node --test pi/obor.test.js: the pi extension's tool map and its hooks against a stub kit.
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { create, kitRoot, mcpServer, projectServers, toolEvent } from "./obor.js";

const cwd = "/w/lane";
const ev = (tool_name, tool_input) => ({ hook_event_name: "PreToolUse", session_id: "s1", cwd, agent_kind: "pi", tool_name, tool_input });
const registered = ["obor", "docs-x", "docs"];

const rows = [
  ["bash is Bash with its command", "bash", { command: "git commit -m x", timeout: 5 }, ev("Bash", { command: "git commit -m x" })],
  ["bash with no command: an empty command, which the guard denies", "bash", {}, ev("Bash", { command: "" })],
  ["write is Write with file_path from path, resolved against the cwd", "write", { path: "b/c.go", content: "x" }, ev("Write", { file_path: "/w/lane/b/c.go" })],
  ["write with an absolute path", "write", { path: "/w/lane/x.md", content: "" }, ev("Write", { file_path: "/w/lane/x.md" })],
  ["write naming no file: an empty file_path, which the guard denies", "write", { content: "x" }, ev("Write", { file_path: "" })],
  ["edit is Edit with file_path from path", "edit", { path: "a.go", edits: [] }, ev("Edit", { file_path: "/w/lane/a.go" })],
  ["read is allowed: no event", "read", { path: "a.go" }, null],
  ["codemode is allowed: its inner calls reach tool_call", "codemode", { code: "x" }, null],
  ["tool_search is allowed", "tool_search", { query: "x" }, null],
  ["obor's MCP tool is the mcp event naming obor", "mcp__obor__job_next", {}, ev("mcp", { mcp_server: "obor" })],
  ["a registered server with - is matched by its spelling, the registered name sent", "mcp__docs_x__search", { q: 1 }, ev("mcp", { mcp_server: "docs-x" })],
  ["the longest registered spelling wins (docs_x over docs)", "mcp__docs_x__a__b", {}, ev("mcp", { mcp_server: "docs-x" })],
  ["a server not registered (the user's own) is the name's part up to the next __", "mcp__user__tool", {}, ev("mcp", { mcp_server: "user" })],
  ["an MCP name with no server part names none", "mcp__", {}, ev("mcp", { mcp_server: "" })],
  ["any other tool is unguarded, naming it", "grep", { pattern: "x" }, ev("unguarded", { tool: "grep", why: "obor has no rule for pi tool grep: update obor" })],
];
for (const [name, tool, input, want] of rows) {
  test(`toolEvent: ${name}`, () => assert.deepEqual(toolEvent(tool, input, cwd, "s1", registered), want));
}

test("mcpServer: matches against the registered names, not the prefix", () => {
  assert.equal(mcpServer("mcp__a_b__t", ["a-b"]), "a-b");
  assert.equal(mcpServer("mcp__a_b__t", []), "a_b");
});

test("kitRoot: the directory above pi/", () => {
  assert.equal(kitRoot(), path.dirname(path.dirname(fs.realpathSync(new URL("./obor.js", import.meta.url)))));
});

// rig is the extension against a stub kit: <base>/kit/bin/obor logs each call ("<hook> <event json>") and answers per
// its mode (allow, deny, exit1, block, answer); project writes .obor/config.json (and mcp, the project's
// .agents/mcp_config.json); noBin leaves bin/obor out. The fake pi records its handlers and MCP registrations; ctx(…)
// is a handler context of session s1 (or id) whose branch is entries.
function rig(t, { mode = "allow", project = true, mcp, noBin = false, env = {}, refuse = [] } = {}) {
  const base = fs.mkdtempSync(path.join(os.tmpdir(), "obor-pi-"));
  t.after(() => fs.rmSync(base, { recursive: true, force: true }));
  const kit = path.join(base, "kit");
  const log = path.join(base, "log");
  if (!noBin) {
    fs.mkdirSync(path.join(kit, "bin"), { recursive: true });
    fs.writeFileSync(
      path.join(kit, "bin", "obor"),
      `#!/bin/bash
event=$(cat)
echo "$2 OBOR_NO_SELF_HOST=$OBOR_NO_SELF_HOST $event" >> ${log}
case "${mode}:$2" in
  deny:pre-tool-use) echo "obor: denied by the stub" >&2; exit 2 ;;
  exit1:pre-tool-use) echo "obor: the stub crashed" >&2; exit 1 ;;
  block:stop) case "$event" in *'"stop_hook_active":true'*) ;; *) echo '{"decision":"block","reason":"obor (worker L-01): not done yet"}' ;; esac ;;
  answer:user-prompt-submit) echo '{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"obor: the lead has lines"}}' ;;
  *:session-start) echo "obor: role worker, lane L-01" ;;
esac
`,
    );
  }
  const dir = path.join(base, "lane");
  fs.mkdirSync(path.join(dir, ".obor"), { recursive: true });
  if (project) fs.writeFileSync(path.join(dir, ".obor", "config.json"), "{}");
  if (mcp) {
    fs.mkdirSync(path.join(dir, ".agents"));
    fs.writeFileSync(path.join(dir, ".agents", "mcp_config.json"), JSON.stringify({ mcpServers: mcp }));
  }
  const handlers = {};
  const servers = [];
  const notes = [];
  const pi = {
    on: (name, fn) => (handlers[name] = fn),
    registerMcpServer: (name, config) => {
      if (refuse.includes(name)) throw new Error("invalid config");
      servers.push([name, config]);
    },
  };
  create(pi, { cwd: dir, kit, env: { ...process.env, ...env } });
  const ctx = ({ id = "s1", entries = [] } = {}) => ({
    cwd: dir,
    ui: { notify: (line, level) => notes.push([level, line]) },
    sessionManager: { getSessionId: () => id, getSessionFile: () => `/sessions/x_${id}.jsonl`, getBranch: () => entries },
  });
  const calls = () => (fs.existsSync(log) ? fs.readFileSync(log, "utf8").trim().split("\n") : []);
  const events = (hook) => calls().filter((l) => l.startsWith(hook + " ")).map((l) => JSON.parse(l.replace(/^\S+ \S+ /, "")));
  return { handlers, servers, notes, ctx, calls, events, dir, kit };
}

const assistant = (stopReason, more = {}) => ({ type: "message", message: { role: "assistant", stopReason, ...more } });

test("load: obor's MCP server (the kit's bin/obor mcp, direct) and every hook are registered", (t) => {
  const { handlers, servers, kit } = rig(t);
  assert.deepEqual(servers, [["obor", { command: path.join(kit, "bin", "obor"), args: ["mcp"], env: { OBOR_NO_SELF_HOST: "1" }, exposure: "direct" }]]);
  assert.deepEqual(Object.keys(handlers).sort(), ["agent_before_settle", "agent_settled", "before_agent_start", "input", "session_start", "tool_call"]);
});

test("load: the project's .agents/mcp_config.json servers are registered too, its obor entry and a disabled one left out", (t) => {
  const mcp = {
    obor: { command: "obor", args: ["mcp"] },
    "docs-x": { command: "docs-mcp", args: ["--stdio"], env: { K: "v" } },
    web: { serverUrl: "https://example.com/mcp", headers: { A: "b" } },
    off: { command: "off", disabled: true },
    broken: { args: ["x"] },
  };
  const { servers, handlers, ctx, notes } = rig(t, { mcp });
  assert.deepEqual(
    servers.map(([n]) => n),
    ["obor", "docs-x", "web"],
  );
  assert.deepEqual(servers[1][1], { exposure: "direct", command: "docs-mcp", args: ["--stdio"], env: { K: "v" } });
  assert.deepEqual(servers[2][1], { exposure: "direct", url: "https://example.com/mcp", headers: { A: "b" } });
  // The entry with neither a command nor a url warns, at the first handler that has pi's UI.
  return handlers.session_start({}, ctx()).then(() => assert.match(notes.map((n) => n[1]).join("\n"), /server broken has no command or url/));
});

test("projectServers: a missing file names none and warns nothing", (t) => {
  const warned = [];
  const base = fs.mkdtempSync(path.join(os.tmpdir(), "obor-pi-"));
  t.after(() => fs.rmSync(base, { recursive: true, force: true }));
  assert.deepEqual(projectServers(base, (l) => warned.push(l)), []);
  assert.deepEqual(warned, []);
});

test("guard: a project server's tool reaches the guard as mcp naming the registered server", async (t) => {
  const { handlers, ctx, events, dir } = rig(t, { mcp: { "docs-x": { command: "d" } } });
  assert.equal(await handlers.tool_call({ toolName: "mcp__docs_x__search", input: { q: "x" } }, ctx()), undefined);
  assert.deepEqual(events("pre-tool-use"), [{ hook_event_name: "PreToolUse", session_id: "s1", cwd: dir, agent_kind: "pi", tool_name: "mcp", tool_input: { mcp_server: "docs-x" } }]);
});

test("guard: an allowed call runs pre-tool-use with the Claude event and answers nothing; read runs no hook", async (t) => {
  const { handlers, ctx, calls, events, dir } = rig(t);
  assert.equal(await handlers.tool_call({ toolName: "edit", input: { path: "a.go" } }, ctx()), undefined);
  assert.equal(await handlers.tool_call({ toolName: "read", input: { path: "a.go" } }, ctx()), undefined);
  assert.equal(calls().length, 1, calls().join("\n"));
  assert.match(calls()[0], /^pre-tool-use OBOR_NO_SELF_HOST=1 /);
  assert.deepEqual(events("pre-tool-use"), [{ hook_event_name: "PreToolUse", session_id: "s1", cwd: dir, agent_kind: "pi", tool_name: "Edit", tool_input: { file_path: dir + "/a.go" } }]);
});

test("guard: exit 2 blocks with obor's line as the reason", async (t) => {
  const { handlers, ctx, events } = rig(t, { mode: "deny" });
  assert.deepEqual(await handlers.tool_call({ toolName: "bash", input: { command: "git push" } }, ctx()), { block: true, reason: "obor: denied by the stub" });
  assert.equal(events("pre-tool-use")[0].tool_input.command, "git push");
});

test("guard: a transport failure blocks (no bin/obor, or any exit but 0 and 2)", async (t) => {
  const missing = rig(t, { noBin: true });
  const r = await missing.handlers.tool_call({ toolName: "bash", input: { command: "ls" } }, missing.ctx());
  assert.equal(r.block, true);
  assert.match(r.reason, /^obor: guard unavailable \(no .*\/bin\/obor\); denied — run: make -C /);
  const crashed = rig(t, { mode: "exit1" });
  const r2 = await crashed.handlers.tool_call({ toolName: "write", input: { path: "x" } }, crashed.ctx());
  assert.equal(r2.block, true);
  assert.match(r2.reason, /^obor: guard failed \(exit 1: obor: the stub crashed\); denied/);
  assert.equal(crashed.events("pre-tool-use").length, 1);
});

test("session_start: session-start with the session id, its file as transcript_path and agent_kind pi; a fresh start is startup", async (t) => {
  const { handlers, ctx, events, dir } = rig(t, { env: { OBOR_PI_RESUME: undefined } });
  await handlers.session_start({ reason: "startup" }, ctx());
  assert.deepEqual(events("session-start"), [{ hook_event_name: "SessionStart", session_id: "s1", transcript_path: "/sessions/x_s1.jsonl", cwd: dir, agent_kind: "pi", source: "startup" }]);
});

test("session_start: OBOR_PI_RESUME naming the session tells a resume, though pi's reason says startup", async (t) => {
  const { handlers, ctx, events } = rig(t, { env: { OBOR_PI_RESUME: "s1" } });
  await handlers.session_start({ reason: "startup" }, ctx());
  await handlers.session_start({ reason: "new" }, ctx({ id: "s2" }));
  assert.deepEqual(
    events("session-start").map((e) => [e.session_id, e.source]),
    [
      ["s1", "resume"],
      ["s2", "startup"],
    ],
  );
});

test("user-prompt-submit: input's raw text at the run's start; the session's and the prompt's context in one hidden message", async (t) => {
  const { handlers, ctx, events } = rig(t, { mode: "answer" });
  await handlers.session_start({}, ctx());
  assert.equal(await handlers.input({ text: "/skill:obor-lead handoff", source: "interactive" }, ctx()), undefined);
  const first = await handlers.before_agent_start({ messageType: "user", prompt: "<the expanded skill>" }, ctx());
  assert.deepEqual(first, { message: { customType: "obor", content: "obor: role worker, lane L-01\n\nobor: the lead has lines", display: false } });
  // The session's context goes with its first run only.
  const second = await handlers.before_agent_start({ messageType: "user", prompt: "next" }, ctx());
  assert.deepEqual(second, { message: { customType: "obor", content: "obor: the lead has lines", display: false } });
  assert.deepEqual(
    events("user-prompt-submit").map((e) => [e.hook_event_name, e.prompt]),
    [
      ["UserPromptSubmit", "/skill:obor-lead handoff"],
      ["UserPromptSubmit", "next"],
    ],
  );
  assert.equal(events("session-start").length, 1);
});

test("user-prompt-submit: a custom run (the stop's own continuation) runs no hook; a busy line gets its context on its text", async (t) => {
  const { handlers, ctx, events } = rig(t, { mode: "answer" });
  assert.equal(await handlers.before_agent_start({ messageType: "custom", message: {} }, ctx()), undefined);
  assert.equal(events("user-prompt-submit").length, 0);
  const r = await handlers.input({ text: "also this", source: "interactive", streamingBehavior: "steer" }, ctx());
  assert.deepEqual(r, { action: "transform", text: "also this\n\nobor: the lead has lines", images: undefined });
  assert.deepEqual(events("user-prompt-submit").map((e) => e.prompt), ["also this"]);
});

test("stop: a block appends a custom_message and continues; the next settle carries stop_hook_active true and ends", async (t) => {
  const { handlers, ctx, events } = rig(t, { mode: "block" });
  const kept = { type: "custom_message", customType: "other", content: "x" };
  const r = await handlers.agent_before_settle({ entries: [kept] }, ctx({ entries: [assistant("stop")] }));
  assert.deepEqual(r, { entries: [kept, { type: "custom_message", customType: "obor-stop", content: "obor (worker L-01): not done yet", display: true }], continue: true });
  assert.equal(await handlers.agent_before_settle({ entries: [] }, ctx({ entries: [assistant("stop")] })), undefined);
  assert.deepEqual(events("stop").map((e) => [e.hook_event_name, e.stop_hook_active, e.transcript_path]), [
    ["Stop", false, "/sessions/x_s1.jsonl"],
    ["Stop", true, "/sessions/x_s1.jsonl"],
  ]);
});

test("stop: without a block the run settles, stop_hook_active false; agent_settled ends a continuation it did not see end", async (t) => {
  const plain = rig(t);
  assert.equal(await plain.handlers.agent_before_settle({ entries: [] }, plain.ctx()), undefined);
  assert.deepEqual(plain.events("stop").map((e) => e.stop_hook_active), [false]);
  // A blocked stop whose continuation was aborted (no agent_before_settle): the next independent stop is a new one.
  const b = rig(t, { mode: "block" });
  assert.equal((await b.handlers.agent_before_settle({ entries: [] }, b.ctx())).continue, true);
  await b.handlers.agent_settled({ aborted: true }, b.ctx());
  assert.equal((await b.handlers.agent_before_settle({ entries: [] }, b.ctx())).continue, true);
  assert.deepEqual(b.events("stop").map((e) => e.stop_hook_active), [false, false]);
});

test("stop-failure: a run whose last assistant message ended on an error runs stop-failure with its errorMessage, and no stop", async (t) => {
  const { handlers, ctx, events, dir } = rig(t, { mode: "block" });
  const entries = [assistant("toolUse"), { type: "message", message: { role: "toolResult" } }, assistant("error", { errorMessage: "You have hit your ChatGPT usage limit (plus plan). Try again in ~12 min." })];
  assert.equal(await handlers.agent_before_settle({ entries: [] }, ctx({ entries })), undefined);
  await handlers.agent_settled({ aborted: false }, ctx({ entries }));
  assert.deepEqual(events("stop"), []);
  assert.deepEqual(events("stop-failure"), [
    { hook_event_name: "StopFailure", session_id: "s1", transcript_path: "/sessions/x_s1.jsonl", cwd: dir, agent_kind: "pi", error: "unknown", error_details: "You have hit your ChatGPT usage limit (plus plan). Try again in ~12 min." },
  ]);
});

test("stop-failure: a run that ended well runs none", async (t) => {
  const { handlers, ctx, calls } = rig(t);
  await handlers.agent_settled({ aborted: false }, ctx({ entries: [assistant("error"), assistant("stop")] }));
  assert.deepEqual(calls(), []);
});

test("a throwing handler does not escape: it warns, the guard blocks, the others answer nothing", async (t) => {
  const { handlers, notes } = rig(t, { mode: "block" });
  const notes0 = notes.length;
  const bad = { cwd: "/nowhere", ui: { notify: (line, level) => notes.push([level, line]) }, sessionManager: { getSessionId: () => { throw new Error("boom"); } } };
  const r = await handlers.tool_call({ toolName: "bash", input: { command: "ls" } }, bad);
  assert.deepEqual(r, { block: true, reason: "obor: guard failed (boom); denied" });
  for (const name of ["session_start", "input", "before_agent_start", "agent_before_settle", "agent_settled"]) {
    assert.equal(await handlers[name]({ messageType: "user", text: "x" }, bad), undefined, name);
  }
  assert.equal(notes.length - notes0, 6);
  assert.match(notes.at(-1)[1], /^obor: the agent_settled handler failed: boom$/);
});

test("a registration pi refuses warns and is no registered name", async (t) => {
  const { servers, handlers, ctx, notes, events } = rig(t, { mcp: { docs: { command: "d" } }, refuse: ["docs"] });
  assert.deepEqual(servers.map(([n]) => n), ["obor"]);
  await handlers.session_start({}, ctx());
  assert.match(notes.map((n) => n[1]).join("\n"), /MCP server docs not registered: invalid config/);
  // docs is not a registered name: its tool's server is the name's part, as for any server the extension did not add.
  await handlers.tool_call({ toolName: "mcp__docs__q", input: {} }, ctx());
  assert.equal(events("pre-tool-use")[0].tool_input.mcp_server, "docs");
});

test("outside an obor project the extension registers nothing: every hook is a no-op", (t) => {
  const { handlers, servers } = rig(t, { project: false });
  assert.deepEqual(handlers, {});
  assert.deepEqual(servers, []);
});
