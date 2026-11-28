// node --test pi/obor.test.js: the pi extension's tool map and its hooks against a stub kit. The tests that wait on
// the extension's hook bound start when this file loads and run beside the others.
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { bindInbox, create, inboxLines, inboxPath, kitRoot, mcpServer, projectServers, toolEvent } from "./obor.js";

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
// out. The fake pi records its handlers, MCP registrations and sendUserMessage calls; ctx(…) is a handler context of
// session s1 (or id) whose branch is entries; idle is what ctx.isIdle answers, held.length > 0 ctx.hasPendingMessages.
// A send fires the input event as pi does (source extension, on state.session's context, state.inputDelay ms later
// when set), and none while state.reject is set (pi rejects the prompt: compaction in progress, another prompt
// starting). A send whose input was not handled then is a user message pi appends (message_start, its text input's
// transform), except while state.noAuth is set (pi rejects it after input: no API key); an idle one starts a run
// (idle turns false) and its message comes state.startMs later; a busy one (a steer) waits in held while state.hold
// is set, until deliver() appends the held ones. arrived() is the sent texts pi appended, appended the appended
// messages' texts. HOME is the rig's, so an inbox socket binds under it, never the runner's own ~/.obor; inbox() is
// each hook call's OBOR_PI_INBOX ("" unset). The outbox runs on timing's short bounds.
const timing = { seenMs: 100, takeMs: 200, slowMs: 1000, tries: 3 };
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
  const sends = [];
  const state = { idle: true, session: "s1", reject: false, inputDelay: 0, noAuth: false, startMs: 0, hold: false };
  const inputs = []; // [text, handled]
  const held = []; // [sent text, its message text]: steers pi queued and has not delivered
  const appended = []; // [sent text, its message text]
  const append = (sent, text, id) => {
    appended.push([sent, text]);
    return handlers.message_start({ type: "message_start", message: { role: "user", content: [{ type: "text", text }], timestamp: Date.now() } }, ctx({ id }));
  };
  const deliver = async () => {
    for (const [sent, text] of held.splice(0)) await append(sent, text, state.session);
  };
  const pi = {
    on: (name, fn) => (handlers[name] = fn),
    registerMcpServer: (name, config) => {
      if (refuse.includes(name)) throw new Error("invalid config");
      servers.push([name, config]);
    },
    sendUserMessage: (text, options) => {
      sends.push([text, options]);
      if (state.reject) return;
      const id = state.session;
      const fire = async () => {
        const e = { text, source: "extension", streamingBehavior: state.idle ? undefined : options?.deliverAs };
        const r = await handlers.input(e, ctx({ id }));
        inputs.push([text, r?.action === "handled"]);
        if (r?.action === "handled") return;
        const msg = r?.action === "transform" ? r.text : text;
        if (!state.idle) {
          if (state.hold) held.push([text, msg]);
          else await append(text, msg, id);
          return;
        }
        if (state.noAuth) return;
        state.idle = false;
        if (state.startMs) await sleep(state.startMs);
        await append(text, msg, id);
      };
      if (state.inputDelay) setTimeout(fire, state.inputDelay);
      else fire();
    },
  };
  create(pi, { cwd: dir, kit, env: { ...process.env, HOME: base, ...env }, timing });
  const ctx = ({ id = "s1", entries = [] } = {}) => ({
    cwd: dir,
    ui: { notify: (line, level) => notes.push([level, line]) },
    isIdle: () => state.idle,
    hasPendingMessages: () => held.length > 0,
    sessionManager: { getSessionId: () => id, getSessionFile: () => `/sessions/x_${id}.jsonl`, getBranch: () => entries },
  });
  const calls = () => (fs.existsSync(log) ? fs.readFileSync(log, "utf8").trim().split("\n") : []);
  const events = (hook) => calls().filter((l) => l.startsWith(hook + " ")).map((l) => JSON.parse(l.replace(/^\S+ \S+ /, "")));
  const inbox = () => (fs.existsSync(path.join(base, "inbox")) ? fs.readFileSync(path.join(base, "inbox"), "utf8").trim().split("\n") : []);
  const sock = (sid) => inboxPath(sid, { HOME: base });
  const arrived = () => appended.map(([sent]) => sent);
  return { handlers, servers, notes, sends, state, inputs, held, appended, deliver, arrived, ctx, calls, events, inbox, sock, dir, kit, base };
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
    "message_start",
    "session_before_compact",
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

// The session-start hook of each session runs with its own socket in OBOR_PI_INBOX (bound before the hook, so the
// record names a live socket); a line posted there is a sendUserMessage, always a steer: pi starts a turn with it when
// the session is idle and queues it when it is busy.
test("inbox: a socket bound per session and named in its session-start's env; a line is a steer, idle or busy", async (t) => {
  const { handlers, ctx, inbox, sock, sends, state, arrived } = rig(t);
  await handlers.session_start({}, ctx());
  await handlers.session_start({}, ctx({ id: "s2" }));
  assert.deepEqual(inbox(), [sock("s1"), sock("s2")], "one session-start per session, each with its own socket");
  for (const id of ["s1", "s2"]) assert.ok(fs.statSync(sock(id)).isSocket(), `${id}'s socket is bound`);
  assert.deepEqual(sends, [], "precondition: nothing sent before a post");
  await post(sock("s1"), [line("[obor other] hi")]);
  assert.deepEqual(sends, [["[obor other] hi", { deliverAs: "steer" }]]);
  state.idle = false;
  state.session = "s2";
  await post(sock("s2"), [line("[obor] needs-you T-1"), "garbage"]);
  assert.deepEqual(sends[1], ["[obor] needs-you T-1", { deliverAs: "steer" }], "the busy session's line is a steer; a line that does not parse is none");
  assert.ok(await until(() => arrived().length === 2), arrived().join("\n"));
  assert.deepEqual(arrived(), ["[obor other] hi", "[obor] needs-you T-1"]);
  assert.equal(sends.length, 2, "each line sent once");
});

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
// until polls cond every 10 ms for up to ms: whether it came true.
const until = async (cond, ms = 3000) => {
  for (const t0 = Date.now(); Date.now() - t0 < ms; await sleep(10)) if (cond()) return true;
  return cond();
};
const texts = (sends) => sends.map(([text]) => text);

// The outbox: pi's sendUserMessage returns nothing and rejects a prompt later, so a line stays queued until pi appends
// it as a user message (message_start).
test("outbox: a line pi rejects during compaction goes again after the compaction and arrives once", async (t) => {
  const { handlers, ctx, sock, sends, state, arrived } = rig(t);
  await handlers.session_start({}, ctx());
  state.reject = true; // compaction started: pi rejects the prompt, no input event
  await post(sock("s1"), [line("[obor] needs-you T-1")]);
  assert.deepEqual(texts(sends), ["[obor] needs-you T-1"], "precondition: sent once");
  await handlers.session_before_compact({ reason: "threshold" }, ctx());
  await sleep(timing.seenMs * 3);
  assert.deepEqual(arrived(), [], "pi rejected it");
  assert.equal(sends.length, 1, "nothing goes while pi compacts");
  // pi clears its compaction state after session_compact's handlers return.
  await handlers.session_compact({ reason: "threshold" }, ctx());
  state.reject = false;
  assert.ok(await until(() => arrived().length > 0));
  await sleep(timing.seenMs * 3);
  assert.deepEqual(arrived(), ["[obor] needs-you T-1"], "it arrives once");
  assert.equal(sends.length, 2, "sent again once, after the compaction");
});

// The session is busy: each line's input handler waits on its user-prompt-submit hook, and pi appends a line only
// once its handlers return, so the second goes only then.
test("outbox: two lines keep their order; the second waits behind a first pi rejects", async (t) => {
  const { handlers, ctx, sock, sends, state, arrived, events } = rig(t);
  await handlers.session_start({}, ctx());
  state.idle = false;
  state.reject = true;
  await post(sock("s1"), [line("one"), line("two")]);
  assert.ok(await until(() => sends.length >= 2), "the first goes again after its bound");
  assert.deepEqual(new Set(texts(sends)), new Set(["one"]), "the second waits while the first is not seen");
  state.reject = false;
  assert.ok(await until(() => arrived().length === 2), arrived().join("\n"));
  assert.deepEqual(arrived(), ["one", "two"]);
  assert.deepEqual(events("user-prompt-submit").map((e) => e.prompt), ["one", "two"], "each busy line's hook ran once, in order");
  assert.equal(texts(sends).filter((s) => s === "two").length, 1, "the second sent once");
});

test("outbox: a line seen at once is not sent again", async (t) => {
  const { handlers, ctx, sock, sends, arrived } = rig(t);
  await handlers.session_start({}, ctx());
  await post(sock("s1"), [line("hi")]);
  assert.ok(await until(() => arrived().length === 1));
  await sleep(timing.seenMs * 4);
  assert.deepEqual(texts(sends), ["hi"]);
  assert.deepEqual(arrived(), ["hi"]);
});

test("outbox: after its tries a line warns once naming its first words, stays queued and goes at agent_settled", async (t) => {
  const { handlers, ctx, sock, sends, state, arrived, notes } = rig(t);
  await handlers.session_start({}, ctx());
  assert.deepEqual(notes, [], "precondition: no warning");
  const text = "[obor] needs-you T-9: the lead asks which store to use\nsecond row";
  state.reject = true;
  await post(sock("s1"), [line(text)]);
  assert.ok(await until(() => notes.length > 0));
  assert.deepEqual(notes, [["warning", 'obor: a line has not reached pi after 3 tries; it stays queued and goes again: "[obor] needs-you T-9: the lead asks …"']]);
  assert.equal(sends.length, 3, "three tries before the warning");
  await sleep(timing.seenMs * 3);
  assert.equal(sends.length, 3, "after the warning it waits the slow bound");
  assert.equal(notes.length, 1, "one warning");
  state.reject = false;
  await handlers.agent_settled({ aborted: false }, ctx());
  assert.ok(await until(() => arrived().length === 1));
  assert.deepEqual(arrived(), [text], "still queued: it goes at the next point pi can take it");
  assert.equal(sends.length, 4);
  assert.equal(notes.length, 1);
});

// A send whose input event fires past the bound (pi defers a prompt sent while its agent_settled handlers run) went
// again: its late copy is dropped, so the line arrives once.
test("outbox: a late copy of a line that arrived is dropped (handled): the line arrives once", async (t) => {
  const { handlers, ctx, sock, sends, state, inputs, arrived } = rig(t);
  await handlers.session_start({}, ctx());
  state.inputDelay = timing.seenMs * 1.5;
  await post(sock("s1"), [line("late")]);
  assert.ok(await until(() => inputs.length === 2), "both sends' input events fired");
  assert.deepEqual(texts(sends), ["late", "late"], "precondition: the bound sent it again");
  assert.deepEqual(inputs, [
    ["late", false],
    ["late", true],
  ]);
  await sleep(timing.seenMs * 3);
  assert.deepEqual(arrived(), ["late"]);
  assert.equal(sends.length, 2);
});

// pi emits input before it checks the model and the credentials (agent-session.js _preparePrompt): a line it rejects
// then reached input but was never taken, so it stays queued and goes again under the same bound as a line rejected
// before input.
test("outbox: input followed by a rejection (no API key) keeps the line, retries it, warns after its tries and it arrives once pi takes it", async (t) => {
  const { handlers, ctx, sock, sends, state, inputs, arrived, notes } = rig(t);
  await handlers.session_start({}, ctx());
  state.noAuth = true;
  await post(sock("s1"), [line("[obor] needs-you T-2")]);
  assert.ok(await until(() => inputs.length === 1), "precondition: the line reached pi's input handlers");
  assert.deepEqual(inputs, [["[obor] needs-you T-2", false]], "precondition: input did not drop it");
  assert.ok(await until(() => sends.length === 2), "rejected after input, it goes again");
  assert.ok(await until(() => notes.length > 0));
  assert.deepEqual(notes, [["warning", 'obor: a line has not reached pi after 3 tries; it stays queued and goes again: "[obor] needs-you T-2"']]);
  assert.equal(sends.length, 3, "three tries before the warning");
  assert.equal(inputs.length, 3, "each try reached input");
  assert.ok(inputs.every(([, handled]) => !handled), "no try was dropped as a copy");
  assert.deepEqual(arrived(), [], "pi took none");
  await sleep(timing.takeMs * 2);
  assert.equal(sends.length, 3, "after the warning it waits the slow bound");
  state.noAuth = false;
  await handlers.agent_settled({ aborted: false }, ctx());
  assert.ok(await until(() => arrived().length === 1));
  await sleep(timing.takeMs * 3);
  assert.deepEqual(arrived(), ["[obor] needs-you T-2"], "it arrives once pi takes it");
  assert.equal(sends.length, 4, "taken: not sent again");
  assert.equal(notes.length, 1, "one warning");
});

// An idle line's run starts after pi's checks: the line is taken at its user message, though that comes past the take
// bound (pi is busy meanwhile), and the next line goes only then, once.
test("outbox: input followed by the turn's start settles the line once; the next one goes only after it", async (t) => {
  const { handlers, ctx, sock, sends, state, inputs, arrived } = rig(t);
  await handlers.session_start({}, ctx());
  state.startMs = timing.takeMs * 3;
  await post(sock("s1"), [line("one"), line("two")]);
  assert.ok(await until(() => inputs.length === 1), "precondition: the first reached input");
  await sleep(timing.takeMs * 2);
  assert.deepEqual(arrived(), [], "precondition: its run has not started yet");
  assert.deepEqual(texts(sends), ["one"], "not sent again while pi starts its run; the second waits");
  assert.ok(await until(() => arrived().length === 2), arrived().join("\n"));
  assert.deepEqual(arrived(), ["one", "two"]);
  await sleep(timing.takeMs * 3);
  assert.deepEqual(texts(sends), ["one", "two"], "each sent once");
  assert.deepEqual(arrived(), ["one", "two"], "each taken once");
});

// A line into a busy session is a steer: pi queues it after input and appends it only when the running turn's tool
// calls end, its text with the prompt's context input appended. It settles then, not before, and is not sent again
// while pi holds it.
test("outbox: a steer settles at its appended message, its context on its text; pi holding it sends nothing again", async (t) => {
  const { handlers, ctx, sock, sends, state, held, appended, deliver, arrived } = rig(t, { mode: "answer" });
  await handlers.session_start({}, ctx());
  state.idle = false;
  state.hold = true;
  await post(sock("s1"), [line("one"), line("two")]);
  assert.ok(await until(() => held.length === 1), "precondition: pi queued the first steer");
  await sleep(timing.takeMs * 3);
  assert.deepEqual(texts(sends), ["one"], "not sent again while pi holds it; the second waits");
  assert.deepEqual(arrived(), []);
  state.hold = false;
  await deliver();
  assert.ok(await until(() => arrived().length === 2), arrived().join("\n"));
  assert.deepEqual(appended, [
    ["one", "one\n\nobor: the lead has lines"],
    ["two", "two\n\nobor: the lead has lines"],
  ]);
  await sleep(timing.takeMs * 3);
  assert.deepEqual(texts(sends), ["one", "two"], "each sent once");
});

// A steer pi drops before delivering it (an abort clears its queue) is not taken: once pi is idle with nothing queued,
// it goes again.
test("outbox: a steer pi drops before its message goes again once pi is idle", async (t) => {
  const { handlers, ctx, sock, sends, state, held, arrived } = rig(t);
  await handlers.session_start({}, ctx());
  state.idle = false;
  state.hold = true;
  await post(sock("s1"), [line("dropped")]);
  assert.ok(await until(() => held.length === 1), "precondition: pi queued the steer");
  await sleep(timing.takeMs * 2);
  assert.equal(sends.length, 1, "precondition: not sent again while pi holds it");
  held.length = 0;
  state.hold = false;
  state.idle = true;
  assert.ok(await until(() => arrived().length === 1), "sent again and taken");
  assert.deepEqual(texts(sends), ["dropped", "dropped"]);
  assert.deepEqual(arrived(), ["dropped"]);
});

test("outbox: at the session's shutdown its queue is dropped; nothing goes after it", async (t) => {
  const { handlers, ctx, sock, sends, state } = rig(t);
  await handlers.session_start({}, ctx());
  state.reject = true;
  await post(sock("s1"), [line("lost")]);
  assert.equal(sends.length, 1, "precondition: sent and rejected");
  await handlers.session_shutdown({ reason: "quit" }, ctx());
  await sleep(timing.seenMs * 3);
  assert.equal(sends.length, 1);
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

