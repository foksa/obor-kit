// node --test pi/obor.test.js: the pi extension's tool map and its hooks against a stub kit. The tests that wait on
// the extension's hook bound start when this file loads and run beside the others.
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { create, inboxLines, inboxPath, kitRoot, mcpServer, projectServers, toolEvent } from "./obor.js";

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
// its mode (allow, deny, exit1, panic: an allow with a diagnostic line, block, answer, slow: an answer then six
// seconds, late: an answer then past the hook's bound, stderr: an answer and a diagnostic line, hang: no answer, past
// the bound); project writes .obor/config.json (and mcp, the project's .agents/mcp_config.json); noBin leaves bin/obor
// out. The fake pi records its handlers and MCP registrations, and fakes sendMessage's paths in pi 1.7.0
// (agent-session.js sendCustomMessage): with triggerTurn false a message is an entry at once unless pi runs
// (state.streaming), when it waits in pending until flush() (a turn's end, the run's end, a run that fails to start);
// with triggerTurn true it is a wake (wakes; a steer while streaming). state.refuse makes sendMessage throw. ctx(…) is
// a handler context of session s1 (or id) whose branch is entries; ctx.isIdle is pi's: neither streaming nor
// compacting. HOME is the rig's, so an inbox socket binds under it, never the runner's own ~/.obor; inbox() is each
// hook call's OBOR_PI_INBOX ("" unset).
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
echo "\${OBOR_PI_INBOX-}" >> ${base}/inbox
case "${mode}:$2" in
  deny:pre-tool-use) echo "obor: denied by the stub" >&2; exit 2 ;;
  exit1:pre-tool-use) echo "obor: the stub crashed" >&2; exit 1 ;;
  panic:pre-tool-use) echo "obor: recovered panic: boom" >&2 ;;
  block:stop) case "$event" in *'"stop_hook_active":true'*) ;; *) echo '{"decision":"block","reason":"obor (worker L-01): not done yet"}' ;; esac ;;
  answer:user-prompt-submit) echo '{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"obor: the lead has lines"}}' ;;
  slow:session-start) echo "obor: role worker, lane L-01"; sleep 6 ;;
  slow:stop) case "$event" in *'"stop_hook_active":true'*) sleep 6 ;; *) echo '{"decision":"block","reason":"obor (worker L-01): not done yet"}'; sleep 6 ;; esac ;;
  late:session-start) echo "obor: role worker, lane L-01"; sleep 30 ;;
  stderr:session-start) echo "obor: role worker, lane L-01"; echo "obor: session s1 not recorded (stub)" >&2 ;;
  hang:*) sleep 30 ;;
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
  const state = { streaming: false, compacting: false, refuse: false };
  const entries = []; // custom messages appended to the session
  const pending = []; // custom messages pi holds while it runs
  const wakes = []; // [message, options] of each triggerTurn send
  const pi = {
    on: (name, fn) => (handlers[name] = fn),
    registerMcpServer: (name, config) => {
      if (refuse.includes(name)) throw new Error("invalid config");
      servers.push([name, config]);
    },
    sendMessage: (message, options) => {
      if (state.refuse) throw new Error("pi refused");
      if (options?.triggerTurn) return void wakes.push([message, options]);
      (state.streaming ? pending : entries).push(message);
    },
  };
  const flush = () => entries.push(...pending.splice(0));
  create(pi, { cwd: dir, kit, env: { ...process.env, HOME: base, ...env } });
  const ctx = ({ id = "s1", entries = [] } = {}) => ({
    cwd: dir,
    ui: { notify: (line, level) => notes.push([level, line]) },
    isIdle: () => !state.streaming && !state.compacting,
    sessionManager: { getSessionId: () => id, getSessionFile: () => `/sessions/x_${id}.jsonl`, getBranch: () => entries },
  });
  const calls = () => (fs.existsSync(log) ? fs.readFileSync(log, "utf8").trim().split("\n") : []);
  const events = (hook) => calls().filter((l) => l.startsWith(hook + " ")).map((l) => JSON.parse(l.replace(/^\S+ \S+ /, "")));
  const inbox = () => (fs.existsSync(path.join(base, "inbox")) ? fs.readFileSync(path.join(base, "inbox"), "utf8").trim().split("\n") : []);
  const sock = (sid) => inboxPath(sid, { HOME: base });
  const lines = () => entries.map((m) => m.content);
  return { handlers, servers, notes, state, entries, pending, wakes, flush, lines, ctx, calls, events, inbox, sock, dir, kit, base };
}

const assistant = (stopReason, more = {}) => ({ type: "message", message: { role: "assistant", stopReason, ...more } });

test("load: obor's MCP server (the kit's bin/obor mcp, direct) and every hook are registered", (t) => {
  const { handlers, servers, kit } = rig(t);
  assert.deepEqual(servers, [["obor", { command: path.join(kit, "bin", "obor"), args: ["mcp"], env: { OBOR_NO_SELF_HOST: "1" }, exposure: "direct" }]]);
  assert.deepEqual(Object.keys(handlers).sort(), [
    "agent_before_settle",
    "agent_settled",
    "before_agent_start",
    "input",
    "session_compact",
    "session_compact_failed",
    "session_shutdown",
    "session_start",
    "tool_call",
  ]);
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

// An allow's stderr is a diagnostic (a recovered panic's line), shown in pi's UI; the call still goes through.
test("guard: an allow's stderr warns and blocks nothing", async (t) => {
  const { handlers, ctx, notes } = rig(t, { mode: "panic" });
  assert.equal(await handlers.tool_call({ toolName: "bash", input: { command: "ls" } }, ctx()), undefined);
  assert.deepEqual(notes, [["warning", "obor: recovered panic: boom"]]);
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

// post writes lines to an inbox socket and waits for the server to close the connection.
const post = (file, lines) =>
  new Promise((resolve, reject) => {
    const c = net.connect(file, () => c.end(lines.join("\n") + "\n"));
    c.on("error", reject);
    c.on("data", () => {});
    c.on("close", resolve);
  });
const line = (text) => JSON.stringify({ type: "user", text });

test("inboxPath: a session's socket under ~/.obor/pi; none without HOME or over a unix socket's 103 bytes", () => {
  assert.equal(inboxPath("s1", { HOME: "/h" }), "/h/.obor/pi/s1.sock");
  assert.equal(inboxPath("s1", {}), "", "no HOME");
  const home = "/" + "h".repeat(103 - "/.obor/pi/s1.sock".length - 1);
  assert.equal(Buffer.byteLength(inboxPath("s1", { HOME: home })), 103, "precondition: 103 bytes bind");
  assert.equal(inboxPath("s1", { HOME: home + "x" }), "", "104 bytes: none");
});

test("inboxLines: one text per user line; a line that does not parse or names no text is none", () => {
  assert.deepEqual(inboxLines(line("[obor other] one") + "\n" + line("two\nrows") + "\n"), ["[obor other] one", "two\nrows"]);
  assert.deepEqual(inboxLines('not json\n{"type":"auth","token":"x"}\n{"type":"user"}\n{"type":"user","text":""}\n'), []);
});

const WAKE = { customType: "obor-wake", content: "[obor inbox] obor lines arrived above: read them and act on them.", display: false };
const entry = (text) => ({ customType: "obor-line", content: text, display: true });
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// The session-start hook of each session runs with its own socket in OBOR_PI_INBOX (bound before the hook, so the
// record names a live socket); each line posted there is handed to pi once as a custom message with triggerTurn false,
// which an idle pi appends at once, and one wake (triggerTurn true) per post starts the turn.
test("inbox: a socket bound per session and named in its session-start's env; an idle session's lines are entries at once and one wake starts its turn", async (t) => {
  const { handlers, ctx, inbox, sock, entries, wakes } = rig(t);
  await handlers.session_start({}, ctx());
  await handlers.session_start({}, ctx({ id: "s2" }));
  assert.deepEqual(inbox(), [sock("s1"), sock("s2")], "one session-start per session, each with its own socket");
  for (const id of ["s1", "s2"]) assert.ok(fs.statSync(sock(id)).isSocket(), `${id}'s socket is bound`);
  assert.deepEqual([entries, wakes], [[], []], "precondition: nothing handed before a post");
  await post(sock("s1"), [line("[obor other] hi"), "garbage", line("[obor] needs-you T-1")]);
  assert.deepEqual(entries, [entry("[obor other] hi"), entry("[obor] needs-you T-1")], "each line an entry once, in order; one that does not parse is none");
  assert.deepEqual(wakes, [[WAKE, { triggerTurn: true }]], "one wake for the post, not one per line");
});

// A busy pi holds the lines in its own queue and appends them at its turn's end; a run whose turn made no tool call
// ends without another model call, so the lines owe one wake, sent at agent_settled (pi is idle there), once.
test("inbox: a busy session's lines wait in pi's queue until its turn ends; no wake while busy, one at agent_settled", async (t) => {
  const { handlers, ctx, sock, state, entries, pending, wakes, flush, lines } = rig(t);
  await handlers.session_start({}, ctx());
  state.streaming = true;
  await post(sock("s1"), [line("one")]);
  await post(sock("s1"), [line("two")]);
  assert.deepEqual(entries, [], "nothing appended while pi runs");
  assert.deepEqual(pending.map((m) => m.content), ["one", "two"], "pi holds each line once");
  assert.deepEqual(wakes, [], "no wake while busy");
  flush(); // the turn's tool results are in (or the run ended)
  assert.deepEqual(lines(), ["one", "two"]);
  await handlers.agent_before_settle({ entries: [] }, ctx());
  assert.deepEqual(wakes, [], "no wake before the run settles");
  state.streaming = false;
  await handlers.agent_settled({ aborted: false }, ctx());
  assert.deepEqual(wakes, [[WAKE, { triggerTurn: true }]], "one wake at agent_settled for both posts");
  await handlers.agent_settled({ aborted: false }, ctx());
  assert.equal(wakes.length, 1, "a settle with no new lines wakes nothing");
});

// A run that fails to start (_startRun's catch: no model, a compaction that failed) flushes the lines pi held; it
// emits no agent_settled, so the owed wake goes with the next post into the idle session, still one.
test("inbox: a run that fails to start keeps its held lines (pi flushes them); the owed wake goes once pi is idle", async (t) => {
  const { handlers, ctx, sock, state, wakes, flush, lines } = rig(t);
  await handlers.session_start({}, ctx());
  state.streaming = true; // a run is starting
  await post(sock("s1"), [line("held")]);
  assert.deepEqual(lines(), [], "precondition: pi holds it");
  flush(); // the run's prepare threw
  state.streaming = false;
  assert.deepEqual(lines(), ["held"], "the line is an entry");
  assert.deepEqual(wakes, [], "no wake was sent while busy");
  await post(sock("s1"), [line("next")]);
  assert.deepEqual(lines(), ["held", "next"]);
  assert.deepEqual(wakes, [[WAKE, { triggerTurn: true }]], "one wake for both");
});

// pi appends a line at once while it compacts outside a run (it is not streaming), but it is not idle: the wake goes
// on the tick after the compaction's end, when pi has cleared its compaction state.
test("inbox: a line during a compaction is an entry at once; its wake goes after the compaction ends, once", async (t) => {
  const { handlers, ctx, sock, state, wakes, lines } = rig(t);
  await handlers.session_start({}, ctx());
  state.compacting = true;
  await post(sock("s1"), [line("[obor] needs-you T-1")]);
  assert.deepEqual(lines(), ["[obor] needs-you T-1"]);
  assert.deepEqual(wakes, [], "no wake while pi compacts");
  await handlers.session_compact({ reason: "manual" }, ctx());
  state.compacting = false;
  await sleep(20);
  assert.deepEqual(wakes, [[WAKE, { triggerTurn: true }]]);
  await handlers.session_compact_failed({ reason: "manual" }, ctx());
  await sleep(20);
  assert.equal(wakes.length, 1, "a compaction's end with no new lines wakes nothing");
});

// The wake's run is obor's prompt: user-prompt-submit reports the wake's text (tagged `[obor `, so no wrong-tab
// guard) and its context goes in the run's hidden message, as for a typed prompt.
test("inbox: the wake's run reports user-prompt-submit with the wake's text and gets its context", async (t) => {
  const { handlers, ctx, events } = rig(t, { mode: "answer" });
  await handlers.session_start({}, ctx());
  const r = await handlers.before_agent_start({ messageType: "custom", message: { role: "custom", ...WAKE } }, ctx());
  assert.deepEqual(r, { message: { customType: "obor", content: "obor: role worker, lane L-01\n\nobor: the lead has lines", display: false } });
  assert.deepEqual(events("user-prompt-submit").map((e) => e.prompt), [WAKE.content]);
});

test("inbox: a sendMessage pi refuses warns per line and the post still ends; the session's next post goes", async (t) => {
  const { handlers, ctx, sock, state, notes, lines, wakes } = rig(t);
  await handlers.session_start({}, ctx());
  state.refuse = true;
  await post(sock("s1"), [line("a"), line("b")]);
  assert.deepEqual(
    notes.map((n) => n[1]),
    ["obor: a line for session s1 was not handed to pi: pi refused", "obor: a line for session s1 was not handed to pi: pi refused", "obor: the inbox's wake failed (pi refused); its lines are in the session"],
  );
  state.refuse = false;
  await post(sock("s1"), [line("c")]);
  assert.deepEqual(lines(), ["c"]);
  assert.equal(wakes.length, 1);
});

test("inbox: a stale file at the socket's path is replaced; the socket is removed at the session's shutdown", async (t) => {
  const { handlers, ctx, sock } = rig(t);
  fs.mkdirSync(path.dirname(sock("s1")), { recursive: true });
  fs.writeFileSync(sock("s1"), "stale");
  await handlers.session_start({}, ctx());
  assert.ok(fs.statSync(sock("s1")).isSocket(), "precondition: bound over the stale file");
  await handlers.session_shutdown({ reason: "quit" }, ctx());
  assert.equal(fs.existsSync(sock("s1")), false, "the shutdown removed the socket");
});

// No socket: a bind that fails warns once and the session-start hook runs without the variable, so no inbox is
// recorded and the lead's lines stay typed.
test("bind failure: one warning and the session-start hook runs without the variable; the lines stay typed", async (t) => {
  const { handlers, ctx, inbox, sock, notes } = rig(t);
  const dir = path.dirname(sock("s1"));
  fs.mkdirSync(path.dirname(dir), { recursive: true });
  fs.writeFileSync(dir, "not a directory");
  await handlers.session_start({}, ctx());
  assert.equal(fs.existsSync(sock("s1")), false);
  assert.deepEqual(inbox(), [""], "the hook ran with no inbox in its env");
  assert.equal(notes.length, 1);
  assert.match(notes[0][1], /^obor: session s1's inbox .* not bound \(.*\); its lines stay typed$/);
  await handlers.before_agent_start({ messageType: "user", prompt: "go" }, ctx());
  assert.equal(notes.length, 1, "the failed bind warns once, not per hook");
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

// A hook's answer is printed before the Go side waits on serve (session-start's context, stop's block): the
// extension's bound is past those waits, so six seconds after the answer still delivers it (RV-284 finding 1).
test("a hook that prints its answer then waits six seconds still delivers it: session-start's context and stop's block", async (t) => {
  const { handlers, ctx } = rig(t, { mode: "slow" });
  const [start, stop] = await Promise.all([
    handlers.before_agent_start({ messageType: "user", prompt: "go" }, ctx()),
    handlers.agent_before_settle({ entries: [] }, ctx({ entries: [assistant("stop")] })),
  ]);
  assert.deepEqual(start, { message: { customType: "obor", content: "obor: role worker, lane L-01", display: false } });
  assert.deepEqual(stop, {
    entries: [{ type: "custom_message", customType: "obor-stop", content: "obor (worker L-01): not done yet", display: true }],
    continue: true,
  });
});

// A hook's diagnostics reach pi's UI, on exit 0 too, and stdout stays the answer (RV-284 finding 2).
test("session_start: a hook's stderr on exit 0 warns in pi's UI; its stdout stays the answer", async (t) => {
  const { handlers, ctx, notes } = rig(t, { mode: "stderr" });
  const r = await handlers.before_agent_start({ messageType: "user", prompt: "go" }, ctx());
  assert.deepEqual(r, { message: { customType: "obor", content: "obor: role worker, lane L-01", display: false } });
  assert.deepEqual(notes, [["warning", "obor: session s1 not recorded (stub)"]]);
});

// A non-guard hook that did not run at all is one actionable warning; the guard's own failure stays its block answer.
test("a non-guard hook's transport failure gives one actionable warning; the guard still blocks", async (t) => {
  const { handlers, ctx, notes, kit } = rig(t, { noBin: true });
  assert.equal(await handlers.session_start({}, ctx()), undefined);
  assert.deepEqual(notes, [["warning", `obor: the session-start hook failed (no ${path.join(kit, "bin", "obor")}); obor may not have recorded it — run: make -C ${kit} build`]]);
  const r = await handlers.tool_call({ toolName: "bash", input: { command: "ls" } }, ctx());
  assert.equal(r.block, true);
  assert.equal(notes.length, 1, "the guard's failure is its block's reason, not a second warning");
});

test("a throwing handler does not escape: it warns, the guard blocks, the others answer nothing", async (t) => {
  const { handlers, notes } = rig(t, { mode: "block" });
  const notes0 = notes.length;
  const bad = { cwd: "/nowhere", ui: { notify: (line, level) => notes.push([level, line]) }, sessionManager: { getSessionId: () => { throw new Error("boom"); } } };
  const r = await handlers.tool_call({ toolName: "bash", input: { command: "ls" } }, bad);
  assert.deepEqual(r, { block: true, reason: "obor: guard failed (boom); denied" });
  for (const name of ["session_start", "session_shutdown", "input", "before_agent_start", "agent_before_settle", "agent_settled"]) {
    assert.equal(await handlers[name]({ messageType: "user", text: "x" }, bad), undefined, name);
  }
  assert.equal(notes.length - notes0, 7);
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

// The two probes below run past the extension's hook bound, so they start when this file loads and run beside the
// other tests; their tests only collect the results. Each probe's rig is removed by its test.
function probe(mode, run) {
  const cleanups = [];
  const r = rig({ after: (fn) => cleanups.push(fn) }, { mode });
  return run(r).then((got) => ({ ...got, cleanup: () => cleanups.forEach((fn) => fn()) }));
}

const hanging = probe("hang", async ({ handlers, ctx }) => {
  const t0 = Date.now();
  const r = await handlers.tool_call({ toolName: "bash", input: { command: "ls" } }, ctx());
  return { r, ms: Date.now() - t0 };
});

const late = probe("late", async ({ handlers, ctx, notes }) => ({
  r: await handlers.before_agent_start({ messageType: "user", prompt: "go" }, ctx()),
  notes,
}));

test("guard: past its deadline a hook's call blocks (fails closed) with the timeout and the repair hint", async (t) => {
  const { r, ms, cleanup } = await hanging;
  t.after(cleanup);
  assert.equal(r.block, true);
  assert.match(r.reason, /^obor: guard unavailable \(obor hook pre-tool-use timed out after 15 s\); denied — run: make -C /);
  assert.ok(ms >= 14900 && ms < 25000, `${ms} ms`);
});

test("a cut past the deadline keeps the answer already printed and warns that it failed", async (t) => {
  const { r, notes, cleanup } = await late;
  t.after(cleanup);
  assert.deepEqual(r, { message: { customType: "obor", content: "obor: role worker, lane L-01", display: false } });
  assert.deepEqual(notes.map((n) => n[0]), ["warning"]);
  assert.match(notes[0][1], /^obor: the session-start hook failed \(obor hook session-start timed out after 15 s\); obor may not have recorded it — run: make -C .* build$/);
});

