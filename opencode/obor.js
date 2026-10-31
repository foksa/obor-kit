// obor's opencode plugin (design "opencode lanes", T-306): opencode runs no Claude Code hooks, so each hook here maps
// opencode's event to the Claude Code hook event JSON and runs `<kit>/bin/obor hook <name>`. No obor rule lives here:
// the guard, the session context, the stop and the limit logic stay in the Go binary.
//
//	tool hook execute.before          → pre-tool-use (a deny throws with obor's line, which aborts the tool)
//	a session's first event           → session-start (its context lines, appended to the system prompt by the
//	                                     session hook context)
//	session.execution.started         → user-prompt-submit (prompt: the user text the session's inbox took since)
//	session.execution.succeeded       → stop (its verdict is not acted on: opencode has no stop to block)
//	session.execution.failed          → stop-failure (error, error_details as Claude Code sends them)
//
// opencode v2.2 plugin API (T-578 probe, opencode v2.2.0: its bundled source, and an isolated `opencode serve` with its
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
//     (opencode-<channel>.db off the release channels, OPENCODE_DB overrides; opencode's Y4) — no per-session file;
//     transcript_path is that database.
//   - (b) turn start: session.execution.started (after session.inbox.enqueued, whose item {type: "user", payload:
//     {text}} is the prompt); turn end: session.execution.succeeded | failed {error} | interrupted {reason}. No
//     session.idle any more (T-306 saw none in an older 2.x either).
//   - (c) errors: Session.StructuredError {type, message, status?, response?: {body}, retryAfterMs?}, type one of
//     provider.rate-limit, quota, free-limit, budget-limit, model-budget-limit, auth, content-filter, invalid-request,
//     transport, timeout, internal, … A 429 does not end the turn: opencode retries it (session.retry.scheduled
//     {attempt, at, error}, up to 10 times, each wait min(retryAfterMs, 15 min)), then session.execution.failed.
//     Only that failure ends the turn, so only it is a stop-failure: a retry sends nothing (T-578 lead decision).
//
// The plugin may run in a process shared by several panes (opencode serve --service), so its env is not the pane's:
// runHook drops the pane-identity vars and obor takes identity from the cwd and lane state (T-308).
//
// The kit is this plugin's, except inside an obor checkout, which runs its main checkout's bin/obor (kitFor, T-478: the
// same rule as bin/obor's self-hosting, so obor-kit's own lanes run its guard, not an older global kit's).
//
// Outside an obor project (no .obor/config.json above the directory) every hook is a no-op. bin/obor missing, failing
// or slower than 5 s: allow, except the guard, which then denies (bin/obor's own no-binary rule already fails closed).
// Installed by `obor setup opencode-plugin` (a link in ~/.config/opencode/plugins/); plain JS, node built-ins only.

import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const TIMEOUT_MS = 5000;
// The tools hooks/hooks.json's PreToolUse matcher sends to the guard (Read, Glob, … map but are not run).
const GUARDED = new Set(["Bash", "Edit", "Write"]);
// The vars that name a pane: the plugin's process env may be another pane's (see the top), so obor hook never sees them.
const PANE_IDENTITY = ["OBOR_AGENT", "OBOR_ROLE", "OBOR_LANE", "HERDR_PANE_ID"];

// The kit: this file's real path (through the plugins-dir link), two levels up.
function kitRoot() {
  return path.dirname(path.dirname(fs.realpathSync(fileURLToPath(import.meta.url))));
}

// oborCheckout: the main checkout of the repository at dir when it is an obor checkout (bin/obor and a
// .claude-plugin/plugin.json named obor: bin/obor's is_obor_checkout), else "".
function oborCheckout(dir) {
  try {
    const common = execFileSync("git", ["-C", dir, "rev-parse", "--git-common-dir"], { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim();
    const main = path.dirname(fs.realpathSync(path.resolve(dir, common)));
    const manifest = JSON.parse(fs.readFileSync(path.join(main, ".claude-plugin", "plugin.json"), "utf8"));
    return fs.existsSync(path.join(main, "bin", "obor")) && manifest.name === "obor" ? main : "";
  } catch {
    return "";
  }
}

// kitFor is the kit whose bin/obor runs for dir: bin/obor's self-hosting rule (T-478), so inside an obor checkout (main
// or a lane worktree) the MAIN checkout's, never a lane's own build; elsewhere this plugin's kit.
function kitFor(dir) {
  return oborCheckout(dir) || kitRoot();
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
  const ev = (name, input) => ({ hook_event_name: "PreToolUse", session_id: sessionID ?? "", cwd, tool_name: name, tool_input: input });
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

// databasePath is where opencode keeps its sessions (opencode's Y4 with xdg-basedir's data dir): OPENCODE_DB, else
// opencode.db (opencode-<channel>.db off the release channels) in $XDG_DATA_HOME/opencode; "" for an in-memory one.
// env is the plugin process's (the server's), whose data dir it is: no identity is read from it.
const RELEASE_CHANNELS = ["latest", "beta", "next", "prod"];
export function databasePath(channel, env = process.env) {
  const data = path.join(env.XDG_DATA_HOME || path.join(os.homedir(), ".local", "share"), "opencode");
  let name = env.OPENCODE_DB;
  if (!name) {
    const plain = !channel || RELEASE_CHANNELS.includes(channel) || env.OPENCODE_DISABLE_CHANNEL_DB === "1" || env.OPENCODE_DISABLE_CHANNEL_DB === "true";
    name = plain ? "opencode.db" : `opencode-${channel.replace(/[^a-zA-Z0-9._-]/g, "-")}.db`;
  }
  return name === ":memory:" ? "" : path.resolve(data, name);
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

// The opencode events the plugin reads (session-start comes from the first of them naming a session).
const EVENT_TYPES = [
  "session.created",
  "session.inbox.enqueued",
  "session.execution.started",
  "session.execution.succeeded",
  "session.execution.failed",
];

// sessionHooks is the state of one location's sessions (which are its, their pending prompt) and turns each opencode
// event into the Claude hook calls it means: [{name, event}].
// transcript is the session store's path (databasePath); start(id) is a session's session-start call.
export function sessionHooks(cwd, transcript) {
  const mine = new Map(); // session → this location's (true) or another's (false)
  const children = new Set(); // subagent sessions: no hook
  const prompts = new Map(); // session → the user texts its inbox took since its last turn start
  const base = (name, id) => ({ hook_event_name: name, session_id: id, transcript_path: transcript, cwd });
  const own = (id) => {
    if (id && !children.has(id)) mine.set(id, true);
  };
  const isMine = (id) => mine.get(id) === true && !children.has(id);

  // event returns the hook calls one event means (setup runs session-start before a session's first).
  function event(e) {
    const d = e?.data ?? {};
    const id = d.sessionID;
    if (!id) return [];
    if (e.type === "session.created" && d.parentID) children.add(id);
    if (e.location?.directory) mine.set(id, path.resolve(e.location.directory) === cwd);
    if (!isMine(id)) return [];
    const calls = [];
    switch (e.type) {
      case "session.inbox.enqueued":
        if (d.item?.type === "user" && d.item.payload?.text) prompts.set(id, [...(prompts.get(id) ?? []), d.item.payload.text]);
        break;
      case "session.execution.started":
        calls.push({ name: "user-prompt-submit", event: { ...base("UserPromptSubmit", id), prompt: (prompts.get(id) ?? []).join("\n\n") } });
        prompts.delete(id);
        break;
      case "session.execution.succeeded":
        calls.push({ name: "stop", event: { ...base("Stop", id), stop_hook_active: false } });
        break;
      case "session.execution.failed":
        calls.push({ name: "stop-failure", event: { ...base("StopFailure", id), error: claudeError(d.error), error_details: errorDetails(d.error, e.created) } });
        break;
    }
    return calls;
  }
  return { event, own, isMine, start: (id) => ({ name: "session-start", event: { ...base("SessionStart", id), source: "startup" } }) };
}

// hookEnv is the plugin's env without the pane-identity vars, plus OBOR_NO_SELF_HOST (the kit's own build runs).
function hookEnv() {
  const env = { ...process.env, OBOR_NO_SELF_HOST: "1" };
  for (const k of PANE_IDENTITY) delete env[k];
  return env;
}

// runHook runs `bin/obor hook <name>` with the event on stdin: {code, stdout, stderr}, or {failed: why}.
function runHook(kit, name, event, cwd) {
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
    const child = spawn("bash", [bin, "hook", name], { cwd, env: hookEnv(), detached: true });
    const timer = setTimeout(() => {
      try {
        process.kill(-child.pid, "SIGKILL");
      } catch {}
      finish({ failed: `obor hook ${name} timed out after ${TIMEOUT_MS / 1000} s` });
    }, TIMEOUT_MS);
    child.stdout.on("data", (b) => (stdout += b));
    child.stderr.on("data", (b) => (stderr += b));
    child.on("error", (e) => finish({ failed: String(e) }));
    child.on("close", (code) => finish({ code, stdout, stderr }));
    child.stdin.on("error", () => {});
    child.stdin.end(JSON.stringify(event));
  });
}

// setup is the opencode 2.x entry (its runtime context; the published 1.18 types do not describe it): the guard
// (tool hook execute.before), the session context (session hook context) and the session events (event.subscribe).
async function setup(ctx) {
  const cwd = ctx.location?.directory || process.cwd();
  if (!inProject(cwd)) return;
  const kit = kitFor(cwd);
  const sessions = sessionHooks(path.resolve(cwd), databasePath(ctx.app?.channel));
  const context = new Map(); // session → its session-start run: the context text

  // started runs session-start once per session (its first event or context call), before any other hook of it.
  const started = (id) => {
    if (!context.has(id)) {
      const s = sessions.start(id);
      context.set(id, runHook(kit, s.name, s.event, cwd).then((r) => (r.failed || r.code !== 0 ? "" : r.stdout.trim())));
    }
    return context.get(id);
  };

  // guard throws obor's line when obor denies one of the call's events.
  await ctx.tool.hook("execute.before", async (call) => {
    for (const ev of toClaudeEvents(call.tool, call.input, cwd, call.sessionID)) {
      if (!GUARDED.has(ev.tool_name)) continue;
      const r = await runHook(kit, "pre-tool-use", ev, cwd);
      if (r.failed) throw new Error(`obor: guard unavailable (${r.failed}); denied — run: make -C ${kit} build`);
      if (r.code === 2) throw new Error(r.stderr.trim() || "obor: denied");
    }
  });
  // context: the hook runs per location, so its session is this location's.
  await ctx.session.hook("context", async (c) => {
    if (!c.sessionID) return;
    sessions.own(c.sessionID);
    if (!sessions.isMine(c.sessionID)) return;
    const text = await started(c.sessionID);
    if (text && Array.isArray(c.system)) c.system.push({ type: "text", text });
  });
  // The session events, one at a time in their order; an opencode without event.subscribe keeps the two hooks above.
  if (typeof ctx.event?.subscribe !== "function") return;
  (async () => {
    for await (const e of ctx.event.subscribe({ types: EVENT_TYPES })) {
      const calls = sessions.event(e);
      if (calls.length === 0) continue;
      await started(e.data.sessionID);
      for (const c of calls) await runHook(kit, c.name, c.event, cwd);
    }
  })().catch(() => {});
}

export default {
  id: "obor.opencode",
  setup,
};
