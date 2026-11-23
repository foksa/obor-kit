// obor's opencode plugin (design "opencode lanes"): opencode runs no Claude Code hooks, so each hook here maps
// opencode's event to the Claude Code hook event JSON and runs `<kit>/bin/obor hook <name>`. No obor rule lives here:
// the guard, the session context, the hooks' verdicts and the limit logic stay in the Go binary; this file carries them
// into opencode's prompt and events.
//
//	tool hook execute.before          → pre-tool-use (a deny throws with obor's line, which aborts the tool)
//	a session's first event           → session-start (its context lines, appended to the system prompt by the
//	                                     session hook context)
//	session hook prompt               → user-prompt-submit (its context appended to the prompt's text) and the start's
//	                                     model (OBOR_OPENCODE_MODEL, below)
//	session.execution.succeeded       → stop; its block's reason becomes the session's next prompt (a new turn)
//	session.execution.failed          → stop-failure (error, error_details as Claude Code sends them)
//	session.execution.interrupted     → no hook; like every turn's end, it ends the session's stop loop
//	every turn's end                  → its root session's token file dated to now (activity), created empty when the
//	                                     session has none, content unchanged
//	session.step.ended                → one line in the session's token file (turn what: context), no hook
//	the session's inbox socket        → a line obor serve posts becomes the session's prompt (below)
//
// The plugin acts on the hooks' answers (agents.Caps.HookAnswers: design "Lines to the lead"): the session prompt hook
// runs `obor hook user-prompt-submit` before admission (the hook holds admission) and appends its additionalContext to
// the prompt's text, where the model and the owner see it and the session keeps it; a stop's
// {"decision":"block","reason"} becomes ctx.session.prompt of the reason, marked metadata.obor "stop", so it starts the
// session's next turn, and the stop that ends that turn carries stop_hook_active: true (obor's stop hook then lets that
// stop through, once per loop). A prompt marked "stop" is that reason: the prompt hook runs no second reminder, so it
// starts no reminder loop (an inbox line, marked "line", is a prompt like a typed one). A failing hook leaves the
// prompt as typed, and a stop with no block lets the turn end.
//
// The session's inbox (agents.Caps.Inbox: design "Lines to the lead"): before a session's session-start the plugin
// binds a unix socket for it (inboxPath, beside its token file) and puts its path in that hook's env (INBOX_VAR), so
// the hook records it as the session's inbox (the kind's inbox_record), and obor serve posts the session's lines there
// (the kind's deliver: one JSON line {"type":"user","text"} per line). Each line read becomes
// ctx.session.prompt({sessionID, text, metadata: {obor: "line"}}), delivery steer (opencode's default): an idle session
// starts a turn, a busy one reads it at its next model step (a running step or tool is not interrupted), and the
// owner's half-typed draft stays (a live probe, opencode 2.2.0). The prompt hook runs user-prompt-submit for it as for
// any prompt. No socket (no HOME, a path over a unix socket's 103 bytes, a bind that fails, or no ctx.session.prompt):
// the hook runs without the variable, no inbox is recorded, and the lead's lines come through its hooks. The socket is
// removed when it closes and when the process exits, so a record naming it is no live inbox.
//
// The start's model and effort: obor's opencode kind exports OBOR_OPENCODE_MODEL=<provider>/<model>[#<variant>]
// for the start (the type's --model, the effort as its variant). With it set, the session hook prompt switches each new
// root session of this location to it, once, before its first turn (ctx.session.switchModel); a child (a subagent's)
// is never switched (it inherits its parent's model), and without the variable no switch is made, so the owner's own
// opencode windows, which load this plugin too, keep their model. A live probe (opencode 2.2.0, a --standalone TUI in a
// pty, a fake provider): the TUI switches the session to its own pick (for a new session the variant it remembers in
// model.json first) just before it submits; the prompt hook runs after that switch and before the turn, so the turn
// runs on the plugin's model and variant, and so do the later turns (the TUI follows the session's switch). A switch on
// session.created loses: the TUI's pre-submit switch comes after it. Gated on opencode 2 and switchModel being there;
// else one line to stderr and nothing. The variable is read from the plugin's process env, which is the pane's under
// --standalone (obor's opencode base args: its own server in the pane's process).
//
// opencode v2.2 plugin API (a live probe, opencode v2.2.0: its bundled source, and an isolated `opencode serve` with its
// own HOME/XDG dirs and port, a fake provider and a logging plugin):
//   - setup(ctx) runs once per location (directory), lazily at its first request; a shared `opencode serve --service`
//     runs one instance per location in one process. ctx.location.directory is the cwd; ctx.app is {name, version,
//     channel}.
//   - ctx.event.subscribe({types}) is an async iterable of the server's events, ALL locations' (an instance sees other
//     directories' sessions): {id, type, created (ms), location?: {directory}, data: {sessionID, …}}. session.created,
//     session.inbox.enqueued and session.retry.scheduled carry the location; session.execution.* do not, so a session
//     is this location's once a located event or the (per-location) context hook names it. A session.created with
//     data.parentID is a child (a subagent's): no hook runs for it.
//   - (a) session id: every session event's data.sessionID; no session start event reaches a plugin set up after the
//     session was made, hence "first event". Storage: one SQLite database for every session, <data>/opencode.db
//     (opencode-<channel>.db off the release channels, OPENCODE_DB overrides; opencode's Y4) — no per-session file obor
//     reads: the plugin writes its own per-session token file (tokensPath) and reports that as transcript_path.
//   - (b) turn start: session.execution.started (after session.inbox.enqueued, whose item {type: "user", payload:
//     {text}} is the prompt); turn end: session.execution.succeeded | failed {error} | interrupted {reason}. No
//     session.idle (opencode 2.x emits none).
//   - (c) errors: Session.StructuredError {type, message, status?, response?: {body}, retryAfterMs?}, type one of
//     provider.rate-limit, quota, free-limit, budget-limit, model-budget-limit, auth, content-filter, invalid-request,
//     transport, timeout, internal, … A 429 does not end the turn: opencode retries it (session.retry.scheduled
//     {attempt, at, error}, up to 10 times, each wait min(retryAfterMs, 15 min)), then session.execution.failed.
//     Only that failure ends the turn, so only it is a stop-failure: a retry sends nothing.
//
// The plugin's process is the pane's agent (--standalone, obor's opencode base args: its own server in the pane's
// process), so its env is the agent's start env: obor hook gets it whole, the identity vars (OBOR_ROLE, OBOR_LANE,
// HERDR_PANE_ID) included, as Claude's and Codex's hooks do (OC-L2).
//
// The kit is this plugin's, in every directory: each start's generated config (OPENCODE_CONFIG) names the
// launching kit's plugin package dir (<kit>/opencode, this file its main), so the kit that started the agent runs its
// hooks; no rule picks another one.
//
// Outside an obor project (no .obor/config.json above the directory) every hook is a no-op. bin/obor missing, failing
// or slower than 5 s: the hook gives no answer (no context, no block), except the guard, which allows a call only on
// exit 0 and denies on every other outcome (exit 2 with obor's reason; any other exit, a kill by a signal, a timeout
// or a missing binary with a repair hint), as the Hook transport contract says (docs/agents/kind-protocol.md).
//
// A session is the agent's only when the store names no parent for it (ctx.session.get, asked once per session before
// the plugin runs session-start, binds an inbox or records it): a subagent's session, whose context hook can run before
// its session.created event, is never the agent's. A resumed session (the kind's start sets OBOR_OPENCODE_RESUME to its
// id) gets its inbox and session-start at plugin start, before any turn: no event
// names a session made before setup until its first turn. Every event carries agent_kind "opencode" (the kind's own
// word, as agy's transport sends it).
// Loaded per start from the generated config's plugins (opencode loads a package dir from an env config, not a
// plain .js file, so package.json beside this file names it); plain JS, node built-ins only.

import { spawn } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";

const TIMEOUT_MS = 5000;
// AGENT_KIND is this kind's name in every event's agent_kind (kinds/opencode/kind.Name).
const AGENT_KIND = "opencode";
// The tools hooks/hooks.json's PreToolUse matcher sends to the guard (Read, Glob, … map but are not run).
const GUARDED = new Set(["Bash", "Edit", "Write"]);

// The kit: this file's real path, two levels up.
function kitRoot() {
  return path.dirname(path.dirname(fs.realpathSync(fileURLToPath(import.meta.url))));
}

// inProject: a directory at or above dir holds .obor/config.json.
function inProject(dir) {
  for (let d = path.resolve(dir); ; d = path.dirname(d)) {
    if (fs.existsSync(path.join(d, ".obor", "config.json"))) return true;
    if (path.dirname(d) === d) return false;
  }
}

function shellQuote(s) {
  return "'" + String(s).replace(/'/g, "'\\''") + "'";
}

// patchPaths: the files a patch text (opencode's patch / apply_patch tool) adds, updates, deletes or moves to, read the
// way opencode 2.0.23 reads them (lines trimmed, CRLF and indented headers count). A non-empty patch naming no file
// throws: obor cannot guard what it cannot read.
const PATCH_HEADERS = ["*** Add File:", "*** Update File:", "*** Delete File:", "*** Move to:"];
export function patchPaths(text) {
  const out = [];
  for (const raw of String(text ?? "").split("\n")) {
    const line = raw.replace(/\r$/, "").trim();
    const h = PATCH_HEADERS.find((h) => line.startsWith(h));
    const p = h ? line.slice(h.length).trim() : "";
    if (p) out.push(p);
  }
  if (out.length === 0 && String(text ?? "").trim()) throw new Error("obor: cannot read the patch's files; denied");
  return out;
}

// toClaudeEvents maps one opencode tool call to the Claude Code PreToolUse events obor checks: [] when the tool has
// no Claude twin. opencode 1.x names the shell tool bash and takes filePath; 2.x names it shell and takes path.
export function toClaudeEvents(tool, args, cwd, sessionID) {
  args = args ?? {};
  const ev = (name, input) => ({ hook_event_name: "PreToolUse", session_id: sessionID ?? "", cwd, agent_kind: AGENT_KIND, tool_name: name, tool_input: input });
  const file = (p) => (p ? path.resolve(cwd, p) : "");
  switch (tool) {
    case "bash":
    case "shell": {
      const dir = args.workdir ?? args.cwd;
      const command = dir ? `cd ${shellQuote(dir)} && ${args.command ?? ""}` : (args.command ?? "");
      return [ev("Bash", { command })];
    }
    case "edit":
    case "multiedit":
      return [ev("Edit", { file_path: file(args.filePath ?? args.path) })];
    case "write":
      return [ev("Write", { file_path: file(args.filePath ?? args.path) })];
    case "patch":
    case "apply_patch":
      return patchPaths(args.patchText ?? args.patch).map((p) => ev("Edit", { file_path: file(p) }));
    case "read":
      return [ev("Read", { file_path: file(args.filePath ?? args.path) })];
    case "glob":
      return [ev("Glob", { pattern: args.pattern ?? "", path: args.path ?? "" })];
    case "grep":
      return [ev("Grep", { pattern: args.pattern ?? "", path: args.path ?? "" })];
    case "list":
      return [ev("LS", { path: file(args.path) })];
  }
  return [];
}

// tokensPath is a session's token file, in the state dir the kind's manifest names (kinds/opencode/kind.TranscriptGlob):
// ${HOME}/.obor/opencode/<session>.jsonl. The plugin appends one line per root session's step; turn what: context
// reads its last line. "" without HOME: no file to name.
export function tokensPath(sid, env = process.env) {
  if (!env.HOME) return "";
  return path.join(env.HOME, ".obor", "opencode", sid + ".jsonl");
}

// RESUME_VAR names the session a start resumes (kinds/opencode/kind.ResumeVar): the kind's start sets it on a resume
// and unsets it on a fresh start. The plugin's hooks run in a server process opencode spawns, whose argv carries no
// --session but whose env is the TUI's.
const RESUME_VAR = "OBOR_OPENCODE_RESUME";

// INBOX_VAR names the session's inbox socket in the session-start hook's env (kinds/opencode/kind.InboxVar, the
// manifest's inbox_record).
const INBOX_VAR = "OBOR_OPENCODE_INBOX";

// SOCKET_MAX is the longest unix socket path every platform binds (macOS's sun_path is 104 bytes, its NUL included).
const SOCKET_MAX = 103;

// inboxPath is a session's inbox socket, beside its token file: ${HOME}/.obor/opencode/<session>.sock. "" without HOME
// or when the path is longer than SOCKET_MAX bytes: no socket to bind.
export function inboxPath(sid, env = process.env) {
  if (!env.HOME) return "";
  const p = path.join(env.HOME, ".obor", "opencode", sid + ".sock");
  return Buffer.byteLength(p) <= SOCKET_MAX ? p : "";
}

// inboxLines is the texts of an inbox connection's bytes: one JSON line {"type":"user","text"} each; a line that does
// not parse, or names no text, is none.
export function inboxLines(data) {
  const out = [];
  for (const line of String(data ?? "").split("\n")) {
    try {
      const o = JSON.parse(line);
      if (o?.type === "user" && typeof o.text === "string" && o.text !== "") out.push(o.text);
    } catch {}
  }
  return out;
}

// bound are the inbox sockets this process listens on, removed at its exit: one set and one exit handler for the
// process, whichever copy of this module bound them.
const BOUND = Symbol.for("obor.opencode.inboxes");
const bound =
  globalThis[BOUND] ??
  (globalThis[BOUND] = (() => {
    const set = new Set();
    process.on("exit", () => {
      for (const p of set) {
        try {
          fs.unlinkSync(p);
        } catch {}
      }
    });
    return set;
  })());

// bindInbox listens on file for session id's lines: each becomes prompt({sessionID, text, metadata: {obor: "line"}})
// once the poster's connection ends, then the connection is closed (the poster waits for that). A stale file at the
// path is removed first (a session's socket is its own: a resumed session binds its path again). Resolves to file once
// listening, or "" (one line to warn) when it cannot bind; a socket that closes or fails later is removed. The server
// keeps no process alive.
export function bindInbox(file, id, prompt, warn = (line) => console.error(line)) {
  if (!file || typeof prompt !== "function") return Promise.resolve("");
  return new Promise((resolve) => {
    const gone = () => {
      bound.delete(file);
      try {
        fs.unlinkSync(file);
      } catch {}
    };
    const server = net.createServer((conn) => {
      conn.setEncoding("utf8");
      let data = "";
      conn.on("data", (b) => (data += b));
      conn.on("error", () => {});
      conn.on("end", async () => {
        for (const text of inboxLines(data)) {
          try {
            await prompt({ sessionID: id, text, metadata: { obor: "line" } });
          } catch (e) {
            warn(`obor: a line for session ${id} was not prompted: ${e?.message ?? e}`);
          }
        }
        conn.end();
      });
    });
    let listening = false;
    server.on("error", (e) => {
      if (listening) return gone();
      warn(`obor: session ${id}'s inbox ${file} not bound (${e?.message ?? e}); its lines come through its hooks`);
      resolve("");
    });
    server.on("close", gone);
    try {
      fs.mkdirSync(path.dirname(file), { recursive: true });
      fs.rmSync(file, { force: true });
    } catch {}
    server.listen(file, () => {
      listening = true;
      bound.add(file);
      server.unref();
      resolve(file);
    });
  });
}

// claudeError is Claude Code's StopFailure error for an opencode error (Session.StructuredError): rate_limit for a
// usage or rate limit (a quota only when it names its reset), billing_error for a spent quota or budget, … unknown.
const CLAUDE_ERRORS = {
  "provider.rate-limit": "rate_limit",
  "provider.free-limit": "rate_limit",
  "provider.budget-limit": "billing_error",
  "provider.model-budget-limit": "billing_error",
  "provider.auth": "authentication_failed",
  "provider.invalid-request": "invalid_request",
  "provider.content-filter": "invalid_request",
  "provider.internal": "server_error",
  "provider.timeout": "server_error",
  "provider.transport": "server_error",
};
export function claudeError(err) {
  if (err?.type === "provider.quota") return err.retryAfterMs > 0 ? "rate_limit" : "billing_error";
  return CLAUDE_ERRORS[err?.type] ?? "unknown";
}

// errorDetails is StopFailure's error_details for an opencode error seen at created (ms): its message and type, and
// "|<epoch>" when it names its reset (retryAfterMs), the form the Go side reads a reset from (agents.ResetIn).
export function errorDetails(err, created) {
  let s = `${err?.message ?? ""} (${err?.type ?? "unknown"})`.trim();
  if (err?.retryAfterMs > 0 && created > 0) s += "|" + Math.floor((created + err.retryAfterMs) / 1000);
  return s;
}

// promptContext is a user-prompt-submit hook's answer: its additionalContext ("" when it has none or does not parse).
export function promptContext(stdout) {
  try {
    const c = JSON.parse(String(stdout ?? "")).hookSpecificOutput?.additionalContext;
    return typeof c === "string" ? c.trim() : "";
  } catch {
    return "";
  }
}

// stopBlock is a stop hook's answer: the reason it blocks with ("" when it does not block or does not parse).
export function stopBlock(stdout) {
  try {
    const o = JSON.parse(String(stdout ?? ""));
    return o?.decision === "block" && typeof o.reason === "string" ? o.reason.trim() : "";
  } catch {
    return "";
  }
}

// The opencode events the plugin reads (a session's first event or hook names it; a turn's start runs no hook: the
// prompt hook reports the user's prompt, its answer included, and a turn's end only ends its stop loop).
const EVENT_TYPES = [
  "session.created",
  "session.inbox.enqueued",
  "session.execution.succeeded",
  "session.execution.failed",
  "session.execution.interrupted",
  "session.step.ended",
];

// TURN_ENDS are opencode's turn endings: each ends any stop loop the session has, whatever the outcome (a continuation
// that failed or was interrupted leaves no loop, so the next independent turn's stop is a new one).
const TURN_ENDS = new Set(["session.execution.succeeded", "session.execution.failed", "session.execution.interrupted"]);

// sessionHooks is the state of one location's sessions (which are its, the turn a stop's block began) and turns each
// opencode event into the Claude hook calls it means: [{name, event}], appending a root session's step to its token file
// as a side effect. prompt(id, text) is a user prompt's user-prompt-submit call; block(id) records a stop's block, so
// the session's next stop ends that turn (stop_hook_active). transcriptOf(id) is a session's token file (tokensPath);
// start(id) is a session's session-start call.
export function sessionHooks(cwd, transcriptOf) {
  const mine = new Map(); // session → this location's (true) or another's (false)
  const children = new Set(); // subagent sessions: no hook
  const stopping = new Set(); // sessions whose stop loop a block began: the next stop ends its turn
  const base = (name, id) => ({ hook_event_name: name, session_id: id, transcript_path: transcriptOf(id), cwd, agent_kind: AGENT_KIND });
  const own = (id) => {
    if (id && !children.has(id)) mine.set(id, true);
  };
  const isMine = (id) => mine.get(id) === true && !children.has(id);

  // step appends a root session's step to its token file: tokens is the prompt side (input + cache read + cache write,
  // the sum obor reads from Claude's transcript) and at the step's time. A failed write leaves the last line: it is no
  // turn to fail.
  function step(id, tk, created) {
    const file = transcriptOf(id);
    if (!file || !tk) return;
    const tokens = (tk.input ?? 0) + (tk.cache?.read ?? 0) + (tk.cache?.write ?? 0);
    try {
      fs.mkdirSync(path.dirname(file), { recursive: true });
      fs.appendFileSync(file, JSON.stringify({ type: "context", tokens, at: new Date(created || Date.now()).toISOString() }) + "\n");
    } catch {}
  }

  // touch dates a root session's token file to now, its content unchanged, and creates it empty when the session has
  // none yet: obor reads a role agent's done time from that file's mtime, and a turn's end is activity even when its
  // first turn failed before any model step (its last step wrote nothing, or there was none).
  function touch(id) {
    const file = transcriptOf(id);
    if (!file) return;
    try {
      fs.mkdirSync(path.dirname(file), { recursive: true });
      if (!fs.existsSync(file)) fs.writeFileSync(file, "");
      const now = new Date();
      fs.utimesSync(file, now, now);
    } catch {}
  }

  // event returns the hook calls one event means (setup runs session-start before a session's first).
  function event(e) {
    const d = e?.data ?? {};
    const id = d.sessionID;
    if (!id) return [];
    if (e.type === "session.created" && d.parentID) children.add(id);
    if (e.location?.directory) mine.set(id, path.resolve(e.location.directory) === cwd);
    if (!isMine(id)) return [];
    // A stop loop's state ends with its turn, in one place and on every outcome: only a succeeded turn's stop reports
    // the loop it ends; a failure or an interruption leaves none for the next independent turn.
    const looping = TURN_ENDS.has(e.type) && stopping.delete(id);
    // A turn's end is the session's activity, whatever its outcome.
    if (TURN_ENDS.has(e.type)) touch(id);
    const calls = [];
    switch (e.type) {
      case "session.execution.succeeded":
        calls.push({ name: "stop", event: { ...base("Stop", id), stop_hook_active: looping } });
        break;
      case "session.execution.failed":
        calls.push({ name: "stop-failure", event: { ...base("StopFailure", id), error: claudeError(d.error), error_details: errorDetails(d.error, e.created) } });
        break;
      case "session.step.ended":
        step(id, d.tokens, e.created);
        break;
    }
    return calls;
  }
  return {
    event,
    own,
    child: (id) => {
      children.add(id);
      mine.delete(id);
    },
    isMine,
    isChild: (id) => children.has(id),
    start: (id) => ({ name: "session-start", event: { ...base("SessionStart", id), source: "startup" } }),
    prompt: (id, text) => ({ name: "user-prompt-submit", event: { ...base("UserPromptSubmit", id), prompt: text } }),
    block: (id) => stopping.add(id),
  };
}

// MODEL_VAR names the start's model for the plugin (agents/opencode.ModelVar).
const MODEL_VAR = "OBOR_OPENCODE_MODEL";

// modelRef is opencode's Model.Ref for <provider>/<model>[#<variant>]: {providerID, id, variant?}; null for "" or a
// value without a provider and a model.
export function modelRef(value) {
  const [ref, variant] = String(value ?? "").trim().split("#", 2);
  const i = ref.indexOf("/");
  if (i <= 0 || i === ref.length - 1) return null;
  return { providerID: ref.slice(0, i), id: ref.slice(i + 1), ...(variant ? { variant } : {}) };
}

// modelSwitch is the session prompt hook that switches each new root session to value (OBOR_OPENCODE_MODEL), once:
// null when value is empty, and, after one line to warn, when it is malformed or this opencode is not 2.x with
// ctx.session.switchModel. isChild(id) is what the session events know of a child; a session the store names a parent
// of (ctx.session.get, through lookup) is one too. A failed switch warns and leaves the turn to run.
export function modelSwitch(ctx, value, isChild = () => false, warn = (line) => console.error(line), lookup = (a) => ctx.session.get(a)) {
  if (!value) return null;
  const model = modelRef(value);
  if (!model) {
    warn(`obor: ${MODEL_VAR}=${value} is not provider/model[#variant]; the model is opencode's own`);
    return null;
  }
  const version = String(ctx.app?.version ?? "");
  if (version.split(".")[0] !== "2" || typeof ctx.session?.switchModel !== "function") {
    warn(`obor: opencode ${version || "unknown"} has no session.switchModel (2.x); ${MODEL_VAR} is not applied`);
    return null;
  }
  const seen = new Set();
  return async (p) => {
    const id = p?.sessionID;
    if (!id || seen.has(id)) return;
    seen.add(id);
    try {
      if (isChild(id)) return;
      const info = typeof ctx.session.get === "function" ? await lookup({ sessionID: id }) : null;
      if (info?.parentID) return;
      await ctx.session.switchModel({ sessionID: id, model });
    } catch (e) {
      warn(`obor: switching session ${id} to ${value} failed: ${e?.message ?? e}`);
    }
  };
}

// hookEnv is the plugin's env (the agent's, identity included: see the top), plus OBOR_NO_SELF_HOST (the kit's own
// build runs) and a call's own variables (more: session-start's inbox).
function hookEnv(more = {}, env = process.env) {
  return { ...env, OBOR_NO_SELF_HOST: "1", ...more };
}

// runHook runs `bin/obor hook <name>` with the event on stdin (more: variables added to its env): {code, signal, stdout,
// stderr} (code is null when a signal ended the process), or {failed: why}.
function runHook(kit, name, event, cwd, more) {
  const bin = path.join(kit, "bin", "obor");
  if (!fs.existsSync(bin)) return Promise.resolve({ failed: `no ${bin}` });
  return new Promise((resolve) => {
    let stdout = "", stderr = "", done = false;
    const finish = (r) => {
      if (!done) {
        done = true;
        clearTimeout(timer);
        resolve(r);
      }
    };
    const child = spawn("bash", [bin, "hook", name], { cwd, env: hookEnv(more), detached: true });
    const timer = setTimeout(() => {
      try {
        process.kill(-child.pid, "SIGKILL");
      } catch {}
      finish({ failed: `obor hook ${name} timed out after ${TIMEOUT_MS / 1000} s` });
    }, TIMEOUT_MS);
    child.stdout.on("data", (b) => (stdout += b));
    child.stderr.on("data", (b) => (stderr += b));
    child.on("error", (e) => finish({ failed: String(e) }));
    child.on("close", (code, signal) => finish({ code, signal, stdout, stderr }));
    child.stdin.on("error", () => {});
    child.stdin.end(JSON.stringify(event));
  });
}

// setup is the opencode 2.x entry (its runtime context; the published 1.18 types do not describe it): the guard
// (tool hook execute.before), the session context and inbox (session hook context, session-start), the prompt's
// answer and the start's model (session hook prompt) and the session events (event.subscribe).
async function setup(ctx) {
  const model = process.env[MODEL_VAR];
  const resumed = process.env[RESUME_VAR] ?? "";
  const env = { ...process.env };
  const cwd = ctx.location?.directory || process.cwd();
  if (!inProject(cwd)) return;
  const kit = kitRoot();
  const sessions = sessionHooks(path.resolve(cwd), (id) => tokensPath(id, env));
  const context = new Map(); // session → its session-start run: the context text

  // started runs session-start once per session (its first event, context or prompt call), before any other hook of it,
  // with the session's inbox bound first and named in its env (none bound: no variable).
  const prompt = typeof ctx.session?.prompt === "function" ? (a) => ctx.session.prompt(a) : null;
  const started = (id) => {
    if (!context.has(id)) {
      const s = sessions.start(id);
      const run = bindInbox(inboxPath(id, env), id, prompt)
        .then((sock) => runHook(kit, s.name, s.event, cwd, sock ? { [INBOX_VAR]: sock } : {}))
        .then((r) => (r.failed || r.code !== 0 ? "" : r.stdout.trim()));
      context.set(id, run);
    }
    return context.get(id);
  };

  // lookup is ctx.session.get, once per session (a failed call is forgotten: the next one asks again).
  const infos = new Map();
  const lookup = ({ sessionID }) => {
    if (!infos.has(sessionID)) {
      infos.set(sessionID, Promise.resolve(ctx.session.get({ sessionID })).catch((e) => (infos.delete(sessionID), Promise.reject(e))));
    }
    return infos.get(sessionID);
  };
  // adopt resolves to whether id is the agent's session: this location's and with no parent in the store. A session
  // with a parent is a child, never recorded or bound; one the store cannot answer for now is not the agent's yet.
  const verdicts = new Map();
  const adopt = (id) => {
    if (!id || sessions.isChild(id)) return Promise.resolve(false);
    if (!verdicts.has(id)) {
      verdicts.set(
        id,
        (typeof ctx.session?.get === "function" ? lookup({ sessionID: id }) : Promise.resolve(null)).then(
          (info) => {
            if (info?.parentID) sessions.child(id);
            else sessions.own(id);
            return !info?.parentID;
          },
          (e) => {
            verdicts.delete(id);
            console.error(`obor: session ${id} not read (${e?.message ?? e}); not the agent's yet`);
            return false;
          },
        ),
      );
    }
    return verdicts.get(id).then((root) => root && sessions.isMine(id));
  };

  // guard throws obor's line when obor denies one of the call's events: only exit 0 allows.
  await ctx.tool.hook("execute.before", async (call) => {
    for (const ev of toClaudeEvents(call.tool, call.input, cwd, call.sessionID)) {
      if (!GUARDED.has(ev.tool_name)) continue;
      const r = await runHook(kit, "pre-tool-use", ev, cwd);
      if (r.failed) throw new Error(`obor: guard unavailable (${r.failed}); denied — run: make -C ${kit} build`);
      if (r.code === 2) throw new Error(r.stderr.trim() || "obor: denied");
      if (r.code !== 0) {
        const how = r.signal ? `killed by ${r.signal}` : `exit ${r.code}`;
        throw new Error(`obor: guard failed (${how}${r.stderr.trim() ? ": " + r.stderr.trim().split("\n")[0] : ""}); denied — run: make -C ${kit} build`);
      }
    }
  });
  // context: the hook runs per location, so its session is this location's.
  await ctx.session.hook("context", async (c) => {
    if (!(await adopt(c.sessionID))) return;
    const text = await started(c.sessionID);
    if (text && Array.isArray(c.system)) c.system.push({ type: "text", text });
  });
  // prompt: the user-prompt-submit answer on the prompt's text, and the start's model before a new root session's
  // first turn (OBOR_OPENCODE_MODEL).
  const switchModel = modelSwitch(ctx, model, sessions.isChild, undefined, lookup);
  await ctx.session.hook("prompt", async (p) => {
    if (switchModel) await switchModel(p);
    const id = p?.sessionID;
    if (!id || p.metadata?.obor === "stop") return; // a stop's reason: no second reminder
    if (!(await adopt(id))) return;
    await started(id);
    const text = typeof p.prompt?.text === "string" ? p.prompt.text : "";
    const ask = sessions.prompt(id, text);
    const r = await runHook(kit, ask.name, ask.event, cwd);
    if (r.failed || r.code !== 0) return;
    const more = promptContext(r.stdout);
    if (more && typeof p.prompt?.text === "string") p.prompt.text += "\n\n" + more;
  });

  // stopAnswer acts on a stop's answer: a block's reason becomes the session's next prompt, marked as obor's own, and
  // the stop that ends that turn then carries stop_hook_active. No prompt call, or a failed one: the turn ends.
  const stopAnswer = async (id, r) => {
    const reason = r.failed || r.code !== 0 ? "" : stopBlock(r.stdout);
    if (!reason || typeof ctx.session?.prompt !== "function") return;
    try {
      await ctx.session.prompt({ sessionID: id, text: reason, metadata: { obor: "stop" } });
      sessions.block(id);
    } catch (e) {
      console.error(`obor: the stop's line for session ${id} did not go: ${e?.message ?? e}`);
    }
  };

  // A resumed session (the start's OBOR_OPENCODE_RESUME) is bound and started now: no event names it until its first turn, and
  // its lines must reach it before that. It goes through the same check as any session. A failure leaves the first
  // turn's hooks to start it.
  if (resumed) adopt(resumed).then((root) => root && started(resumed)).catch(() => {});

  // The session events, one at a time in their order; an opencode without event.subscribe keeps its tool, context and
  // prompt hooks.
  if (typeof ctx.event?.subscribe !== "function") return;
  (async () => {
    for await (const e of ctx.event.subscribe({ types: EVENT_TYPES })) {
      const calls = sessions.event(e);
      if (calls.length === 0) continue;
      if (!(await adopt(e.data.sessionID))) continue;
      await started(e.data.sessionID);
      for (const c of calls) {
        const r = await runHook(kit, c.name, c.event, cwd);
        if (c.name === "stop") await stopAnswer(c.event.session_id, r);
      }
    }
  })().catch(() => {});
}

export default {
  id: "obor.opencode",
  setup,
};
