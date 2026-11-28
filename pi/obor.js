// obor's pi extension: pi runs no Claude Code hooks, so this extension is the pi kind's hook transport, as
// opencode/obor.js is OpenCode's. Each handler maps pi's event to the Claude Code hook event JSON and runs
// `<kit>/bin/obor hook <name>`. No obor rule lives here: the guard, the session context, the hooks' verdicts and the
// limit logic stay in the Go binary; this file carries them into pi's events.
//
//	load (the factory)          → registers obor's MCP server (<kit>/bin/obor mcp, exposure direct) and the project's
//	                              .agents/mcp_config.json servers, which pi does not read itself
//	tool_call                   → pre-tool-use (toolEvent's map); exit 0 allows, exit 2 is {block: true, reason} with
//	                              obor's line, and every other outcome blocks
//	session_start               → session-start (its output is the session's context, added at its first run); the
//	                              session's inbox binds first and that hook's env names it
//	session_shutdown            → closes that inbox
//	input + before_agent_start  → user-prompt-submit (its context added to the run as a hidden message); a line typed
//	                              into a busy session (input's streamingBehavior) gets its context appended to its text;
//	                              a run the inbox's wake starts reports the wake's text as its prompt
//	session_compact(_failed)    → the inbox's wake, owed when lines came while pi compacted
//	agent_before_settle         → stop: a block appends the reason as a custom_message and asks for one more run
//	                              (continue: true); the stop that ends that run carries stop_hook_active: true
//	agent_settled               → stop-failure when the session's last assistant message ended on an error
//	                              (stopReason "error"), with its errorMessage; a run that ended so runs no stop; the
//	                              inbox's wake, owed when lines came while pi ran
//
// The session's inbox (agents.Caps.Inbox): before a session's session-start the extension binds a unix socket for it
// (${HOME}/.obor/pi/<session>.sock, none over a socket path's 103 bytes) and puts its path in that hook's env
// (INBOX_VAR), so the hook records it as the session's inbox (the kind's inbox_record), and obor serve posts the
// session's lines there (the kind's deliver: one JSON line {"type":"user","text"} per line). Each line read is handed
// to pi once, as a shown custom message (LINE_TYPE) with triggerTurn false, and pi's own queue owns it from there: an
// idle session appends it to its entries at once; a running or starting one holds it and appends it when the turn's
// tool results are in, when the run ends, or when the run fails to start, so no model, no API key or a compaction
// loses no line. The model reads a custom message as a user-role message. The extension then wakes the session (wake):
// one hidden custom message (WAKE_TYPE) with triggerTurn true, which starts a turn at once (pi marks the run active
// before it returns). A post into an idle session wakes it; a post into a busy one owes one wake, sent once pi is idle
// again (agent_settled, a compaction's end), since pi continues a run with a held line only when its turn made tool
// calls. A wake that fails loses nothing: the lines are entries already. The one loss is pi's: a line it still holds
// in memory when it exits mid-run. A bind that fails warns once and the hook runs without the variable, so no inbox is
// recorded and the lines stay typed. The socket is removed when it closes, at the session's shutdown and when the
// process exits, so a record naming it is no live inbox.
//
// pi loads this file with `-e <kit>/pi/obor.js` (jiti, in pi's own Node): plain JS, node built-ins only. Its process is
// the pane's agent, so its env is the start's: obor hook gets it whole, the identity vars (OBOR_ROLE, OBOR_LANE,
// HERDR_PANE_ID) included. pi's own MCP client starts each stdio server with that env plus the entry's env.
//
// An uncaught throw in a handler ends pi's TUI, so every handler catches (safe): a failure warns and gives no answer,
// except tool_call's, which blocks. Outside an obor project (no .obor/config.json at or above the cwd) the extension
// registers nothing: every hook is a no-op. A hook's answer is the stdout it printed before HOOK_TIMEOUT_MS, a cut
// after it keeping what came before; a hook's stderr warns in pi's UI (the guard's denials are its block's reasons; on
// an allow or a cut its stderr is a diagnostic), and so does a non-guard hook's transport failure; the guard allows a
// call only on exit 0.
//
// A resumed start has OBOR_PI_RESUME=<session id> in its env (the kind's start sets it), a fresh start has none:
// session_start's reason says "startup" either way.

import { spawn } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

// HOOK_TIMEOUT_MS bounds one `obor hook` run. A hook's Go side may wait out a serve that is down or restarting: the
// session and turn requests get 8 s (internal/app/wire/client.go's table), and user-prompt-submit and stop ask
// lead_lines, 3 s, before their turn request, so 15 s is the two waits in one hook plus its local work and process
// start — the bound Claude Code's own Stop hooks give the same commands. Each hook prints its answer before it waits
// on serve, so a cut keeps the stdout already received.
const HOOK_TIMEOUT_MS = 15000;
// AGENT_KIND is this kind's name in every event's agent_kind.
const AGENT_KIND = "pi";
// RESUME_VAR names the session a start resumes; unset on a fresh start.
const RESUME_VAR = "OBOR_PI_RESUME";
// INBOX_VAR names the session's inbox socket in the session-start hook's env (kinds/pi/kind.InboxVar, the manifest's
// inbox_record).
const INBOX_VAR = "OBOR_PI_INBOX";
// SOCKET_MAX is the longest unix socket path every platform binds (macOS's sun_path is 104 bytes, its NUL included).
const SOCKET_MAX = 103;
// LINE_TYPE is an inbox line's custom message type; WAKE_TYPE the wake's, whose text WAKE starts with `[obor ` (an
// obor prompt to the hooks, never a person typing).
const LINE_TYPE = "obor-line";
const WAKE_TYPE = "obor-wake";
const WAKE = "[obor inbox] obor lines arrived above: read them and act on them.";

// PROJECT_MCP is the project's committed MCP config (agents.ProjectMCPFile): the servers a lane may call.
const PROJECT_MCP = path.join(".agents", "mcp_config.json");
// OBOR_SERVER is the name obor's MCP server is registered under in every kind.
const OBOR_SERVER = "obor";
// PASS are pi's tools that need no event: read and ls only read a file or list a directory; codemode and tool_search
// reach their inner calls through tool_call themselves. grep and find stay out: at its first run each downloads its
// helper (rg, fd) into pi's user-global agent bin directory when it is missing, a write and a network fetch the guard
// never sees.
const PASS = new Set(["read", "ls", "codemode", "tool_search"]);
// UNICODE_SPACES is pi's set of spaces (utils/paths.js): a write or an edit path gets a plain space in each one's place.
const UNICODE_SPACES = /[\u00A0\u2000-\u200A\u202F\u205F\u3000]/g;

// kitRoot is the kit: this file's real path, two levels up (<kit>/pi/obor.js).
export function kitRoot() {
  return path.dirname(path.dirname(fs.realpathSync(fileURLToPath(import.meta.url))));
}

// projectRoot is the directory at or above dir that holds .obor/config.json; "" outside an obor project.
export function projectRoot(dir) {
  for (let d = path.resolve(dir); ; d = path.dirname(d)) {
    if (fs.existsSync(path.join(d, ".obor", "config.json"))) return d;
    if (path.dirname(d) === d) return "";
  }
}

// toolName is pi's spelling of a server name in its tools' names (mcp__<server>__<tool>): every character other than
// a letter, a digit or _ becomes _.
function toolName(server) {
  return String(server).replace(/[^A-Za-z0-9_]/g, "_");
}

// mcpServer is the server of an MCP tool name (mcp__<server>__<tool>): the registered name whose spelling the name
// starts with (the longest, when several do), since a registered "docs-x" comes back as "docs_x"; a server the
// extension did not register (the user's own mcp.json) is the name's part up to the next "__".
export function mcpServer(tool, registered) {
  const rest = String(tool).slice("mcp__".length);
  let best = "";
  for (const name of registered) {
    if (rest.startsWith(toolName(name) + "__") && toolName(name).length > toolName(best).length) best = name;
  }
  if (best) return best;
  const i = rest.indexOf("__");
  return i < 0 ? rest : rest.slice(0, i);
}

function preToolUse(name, input, cwd, sessionID) {
  return { hook_event_name: "PreToolUse", session_id: sessionID ?? "", cwd, agent_kind: AGENT_KIND, tool_name: name, tool_input: input };
}

// piPath is what pi's write and edit make of a path before writing it (resolveToCwd, core/tools/path-utils.js, over
// normalizePath in utils/paths.js): Unicode spaces become plain spaces, a leading @ is stripped, ~ and ~/ expand to the
// home directory, a file:// URL is read, and the result is resolved against cwd. That is the file pi writes, so it is
// the path the guard checks.
export function piPath(p, cwd) {
  let s = String(p).replace(UNICODE_SPACES, " ");
  if (s.startsWith("@")) s = s.slice(1);
  if (s === "~") s = os.homedir();
  else if (s.startsWith("~/") || (process.platform === "win32" && s.startsWith("~\\"))) s = path.join(os.homedir(), s.slice(2));
  if (/^file:\/\//.test(s)) s = fileURLToPath(s);
  return path.resolve(cwd, s);
}

// toolEvent maps one pi tool call to the Claude Code PreToolUse event obor checks; null for a tool that needs none
// (PASS). bash is Bash; write and edit name their file as path, resolved as pi resolves it (piPath; "" when it is
// missing, which the guard denies); an MCP tool is the guard's mcp event naming its server (registered: the names the
// extension registered); any other tool goes to the guard as unguarded, so a tool a pi update adds is a named deny for
// obor's agents, never a silent bypass.
export function toolEvent(tool, input, cwd, sessionID, registered = []) {
  input = input ?? {};
  const ev = (name, data) => preToolUse(name, data, cwd, sessionID);
  const file = () => (input.path ? piPath(input.path, cwd) : "");
  if (PASS.has(tool)) return null;
  switch (tool) {
    case "bash":
      return ev("Bash", { command: input.command ?? "" });
    case "write":
      return ev("Write", { file_path: file() });
    case "edit":
      return ev("Edit", { file_path: file() });
  }
  if (String(tool ?? "").startsWith("mcp__")) return ev("mcp", { mcp_server: mcpServer(tool, registered) });
  return ev("unguarded", { tool, why: `obor has no rule for pi tool ${tool}: update obor` });
}

// projectServers is the project's MCP servers as pi registrations: [{name, config}] from <root>/.agents/mcp_config.json
// (its mcpServers), obor's own left out (the extension registers the kit's) and a disabled entry too. A stdio entry
// keeps command, args, env and cwd; an HTTP one its url (url, serverUrl or httpUrl) and headers. An entry with neither
// is skipped with one line to warn; a missing file names none.
export function projectServers(root, warn = () => {}) {
  let doc;
  try {
    doc = JSON.parse(fs.readFileSync(path.join(root, PROJECT_MCP), "utf8"));
  } catch (e) {
    if (e?.code !== "ENOENT") warn(`obor: ${PROJECT_MCP} not read (${e?.message ?? e}); its servers are not registered`);
    return [];
  }
  const out = [];
  for (const [name, entry] of Object.entries(doc?.mcpServers ?? {})) {
    if (name === OBOR_SERVER || !entry || entry.disabled === true || entry.enabled === false) continue;
    const config = { exposure: "direct" };
    const url = entry.url ?? entry.serverUrl ?? entry.httpUrl;
    if (entry.command) {
      config.command = entry.command;
      for (const k of ["args", "env", "cwd"]) if (entry[k] !== undefined) config[k] = entry[k];
    } else if (url) {
      config.url = url;
      if (entry.headers !== undefined) config.headers = entry.headers;
    } else {
      warn(`obor: ${PROJECT_MCP} server ${name} has no command or url; not registered`);
      continue;
    }
    out.push({ name, config });
  }
  return out;
}

// inboxPath is a session's inbox socket: ${HOME}/.obor/pi/<session>.sock. "" without HOME or when the path is longer
// than SOCKET_MAX bytes: no socket to bind.
export function inboxPath(sid, env = process.env) {
  if (!env.HOME) return "";
  const p = path.join(env.HOME, ".obor", "pi", sid + ".sock");
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
const BOUND = Symbol.for("obor.pi.inboxes");
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

// bindInbox listens on file for session id's lines, handing a connection's texts to take in one call (one that throws
// warns), then closes the connection (the poster waits for that). A stale file at the path is removed first (a
// session's socket is its own: a resumed session binds its path again). Resolves to {server, file} once listening, or
// null (one line to warn) when it cannot bind; a socket that closes or fails later is removed. The server keeps no
// process alive.
export function bindInbox(file, id, take, warn = (line) => console.error(line)) {
  if (!file || !id || typeof take !== "function") return Promise.resolve(null);
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
        const texts = inboxLines(data);
        try {
          if (texts.length > 0) await take(texts);
        } catch (e) {
          warn(`obor: lines for session ${id} were not handed to pi: ${e?.message ?? e}`);
        }
        conn.end();
      });
    });
    let listening = false;
    const fail = (e) => {
      warn(`obor: session ${id}'s inbox ${file} not bound (${e?.message ?? e}); its lines stay typed`);
      try {
        server.close();
      } catch {}
      resolve(null);
    };
    server.on("error", (e) => (listening ? gone() : fail(e)));
    server.on("close", gone);
    try {
      fs.mkdirSync(path.dirname(file), { recursive: true });
      fs.rmSync(file, { force: true });
    } catch {}
    try {
      server.listen(file, () => {
        listening = true;
        bound.add(file);
        server.unref();
        resolve({ server, file });
      });
    } catch (e) {
      fail(e);
    }
  });
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

// lastAssistant is the session's last assistant message (its current branch's), or null.
export function lastAssistant(ctx) {
  const sm = ctx?.sessionManager;
  const entries = (typeof sm?.getBranch === "function" ? sm.getBranch() : sm?.getEntries?.()) ?? [];
  for (let i = entries.length - 1; i >= 0; i--) {
    const m = entries[i]?.type === "message" ? entries[i].message : null;
    if (m?.role === "assistant") return m;
  }
  return null;
}

// runHook runs `bin/obor hook <name>` with the event on stdin and env (OBOR_NO_SELF_HOST: the kit's own build runs,
// more: a call's own variables): {code, signal, stdout, stderr} (code null when a signal ended it), or {failed: why}.
export function runHook(kit, name, event, cwd, env, more = {}) {
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
    const child = spawn("bash", [bin, "hook", name], { cwd, env: { ...env, OBOR_NO_SELF_HOST: "1", ...more }, detached: true });
    const timer = setTimeout(() => {
      try {
        process.kill(-child.pid, "SIGKILL");
      } catch {}
      finish({ failed: `obor hook ${name} timed out after ${HOOK_TIMEOUT_MS / 1000} s`, stdout, stderr });
    }, HOOK_TIMEOUT_MS);
    child.stdout.on("data", (b) => (stdout += b));
    child.stderr.on("data", (b) => (stderr += b));
    child.on("error", (e) => finish({ failed: String(e) }));
    child.on("close", (code, signal) => finish({ code, signal, stdout, stderr }));
    child.stdin.on("error", () => {});
    child.stdin.end(JSON.stringify(event));
  });
}

// answer is a hook's answer: the stdout it printed, kept when a cut ended it after its answer went out; "" when it
// exited nonzero (a failing hook gives no answer) or printed nothing.
function answer(r) {
  return r.code !== 0 && !r.failed ? "" : (r.stdout ?? "");
}

// guardBlock is pi's tool_call answer for a pre-tool-use run: none on exit 0, obor's line on exit 2, and a block naming
// the failure on every other outcome.
function guardBlock(r, kit) {
  if (r.failed) return { block: true, reason: `obor: guard unavailable (${r.failed}); denied — run: make -C ${kit} build` };
  if (r.code === 0) return undefined;
  if (r.code === 2) return { block: true, reason: r.stderr.trim() || "obor: denied" };
  const how = r.signal ? `killed by ${r.signal}` : `exit ${r.code}`;
  const first = r.stderr.trim().split("\n")[0];
  return { block: true, reason: `obor: guard failed (${how}${first ? ": " + first : ""}); denied — run: make -C ${kit} build` };
}

// create wires the extension into pi for the session of cwd (kit: the kit whose bin/obor runs, env: the start's env).
// Exported for the tests; pi calls the default export.
export function create(pi, { cwd, kit, env }) {
  const root = projectRoot(cwd);
  if (!root) return;
  const resumed = env[RESUME_VAR] ?? "";

  // warn shows a line in pi's UI (the last handler's ctx); before the first one, lines wait for session_start. live is
  // the last handler's ctx too: the inbox asks it whether pi is idle.
  let ui = null;
  let live = null;
  const waiting = [];
  const warn = (line) => {
    if (typeof ui?.notify === "function") {
      try {
        return void ui.notify(line, "warning");
      } catch {}
    }
    waiting.push(line);
  };
  // safe wraps a handler: a throw warns and answers failed(error) (none, unless the handler says otherwise).
  const safe = (event, fn, failed = () => undefined) => async (e, ctx) => {
    try {
      if (ctx?.ui) ui = ctx.ui;
      if (ctx) live = ctx;
      return await fn(e ?? {}, ctx ?? {});
    } catch (err) {
      warn(`obor: the ${event} handler failed: ${err?.message ?? err}`);
      return failed(err);
    }
  };

  // The MCP servers: obor's (the kit's bin/obor, pinned to the kit) and the project's; registered names the guard's
  // server matching. A registration pi refuses warns and is not a registered name.
  const registered = [];
  const register = (name, config) => {
    try {
      pi.registerMcpServer(name, config);
      registered.push(name);
    } catch (e) {
      warn(`obor: MCP server ${name} not registered: ${e?.message ?? e}`);
    }
  };
  register(OBOR_SERVER, { command: path.join(kit, "bin", "obor"), args: ["mcp"], env: { OBOR_NO_SELF_HOST: "1" }, exposure: "direct" });
  for (const s of projectServers(root, warn)) register(s.name, s.config);

  const sessionOf = (ctx) => {
    const sm = ctx.sessionManager;
    return { id: String(sm?.getSessionId?.() ?? ""), file: String(sm?.getSessionFile?.() ?? "") };
  };
  const dirOf = (ctx) => ctx.cwd || cwd;
  const base = (name, ctx) => {
    const s = sessionOf(ctx);
    return { hook_event_name: name, session_id: s.id, transcript_path: s.file, cwd: dirOf(ctx), agent_kind: AGENT_KIND };
  };
  const hook = (name, event, ctx, more) => runHook(kit, name, event, dirOf(ctx), env, more);

  // diagnostics shows a hook's non-empty stderr lines in pi's UI, one warning each.
  const diagnostics = (r) => {
    for (const line of String(r.stderr ?? "").split("\n")) if (line.trim()) warn(line.trim());
  };

  // ask runs a non-guard hook and shows its diagnostics: its stderr (a refusal or a warning, on exit 0 too), and one
  // actionable line when it did not run at all. It resolves to its answer (answer, above).
  const ask = async (name, event, ctx, more) => {
    const r = await hook(name, event, ctx, more);
    diagnostics(r);
    if (r.failed) warn(`obor: the ${name} hook failed (${r.failed}); obor may not have recorded it — run: make -C ${kit} build`);
    return answer(r);
  };

  // idle: pi runs nothing and compacts nothing (the last handler's ctx.isIdle); a ctx pi has replaced throws: not idle.
  const idle = () => {
    try {
      return live?.isIdle?.() === true;
    } catch {
      return false;
    }
  };
  // owed: lines came since the last wake; wake sends the one wake they are owed once pi is idle (a busy pi gets it at
  // its agent_settled or a compaction's end). A wake pi refuses warns; the lines stay entries.
  let owed = false;
  const wake = () => {
    if (!owed || !idle()) return;
    owed = false;
    try {
      pi.sendMessage({ customType: WAKE_TYPE, content: WAKE, display: false }, { triggerTurn: true });
    } catch (e) {
      warn(`obor: the inbox's wake failed (${e?.message ?? e}); its lines are in the session`);
    }
  };
  // take hands a post's lines to pi, each once with triggerTurn false (pi appends it now or holds it in its own queue),
  // then owes them a wake and sends it when pi is idle.
  const take = (id, texts) => {
    for (const text of texts) {
      try {
        pi.sendMessage({ customType: LINE_TYPE, content: text, display: true }, { triggerTurn: false });
      } catch (e) {
        warn(`obor: a line for session ${id} was not handed to pi: ${e?.message ?? e}`);
      }
    }
    owed = true;
    wake();
  };

  // inboxes are the sessions' bound inbox sockets, closed at their shutdown; a bind that fails warns once, and the
  // session-start hook runs without the variable, so no inbox is recorded and the lead's lines stay typed.
  const inboxes = new Map(); // session id → {server, file}
  const inboxEnv = async (id) => {
    let b = null;
    try {
      b = await bindInbox(inboxPath(id, env), id, (texts) => take(id, texts), warn);
    } catch (e) {
      warn(`obor: session ${id}'s inbox not bound (${e?.message ?? e}); its lines stay typed`);
    }
    if (!b) return {};
    inboxes.set(id, b);
    return { [INBOX_VAR]: b.file };
  };

  // started runs session-start once per session: its output is the session's context, added at its first run.
  const starts = new Map(); // session id → its session-start run: the context text
  const contextGiven = new Set(); // sessions whose first run had that context
  const started = (ctx) => {
    const { id } = sessionOf(ctx);
    if (!starts.has(id)) {
      const event = { ...base("SessionStart", ctx), source: resumed && resumed === id ? "resume" : "startup" };
      const run = inboxEnv(id)
        .then((more) => ask("session-start", event, ctx, more))
        .then((s) => s.trim());
      starts.set(id, run);
    }
    return starts.get(id);
  };

  // session_shutdown: the session's inbox closes (its socket file is removed with it); a later start of the same id
  // binds and starts again.
  pi.on(
    "session_shutdown",
    safe("session_shutdown", async (_e, ctx) => {
      const { id } = sessionOf(ctx);
      starts.delete(id);
      contextGiven.delete(id);
      const b = inboxes.get(id);
      if (!b) return;
      inboxes.delete(id);
      try {
        b.server.close();
      } catch {}
      try {
        fs.unlinkSync(b.file);
      } catch {}
    }),
  );

  // promptAsk runs user-prompt-submit for text: its context ("" for none).
  const promptAsk = async (text, ctx) => promptContext(await ask("user-prompt-submit", { ...base("UserPromptSubmit", ctx), prompt: text }, ctx));

  const typed = new Map(); // session id → the raw text of its pending prompt (input), for its run's start
  const continuing = new Set(); // sessions whose stop blocked: the next stop ends that continuation

  pi.on(
    "tool_call",
    safe(
      "tool_call",
      async (e, ctx) => {
        const ev = toolEvent(e.toolName, e.input, dirOf(ctx), sessionOf(ctx).id, registered);
        if (!ev) return undefined;
        const r = await hook("pre-tool-use", ev, ctx);
        // A guard's stderr is a diagnostic on its allow and on a cut; on a denial it is the block's reason.
        if (r.code === 0 || r.failed) diagnostics(r);
        const block = guardBlock(r, kit);
        // pi writes the path the guard allowed: input is mutable, and the checked absolute path is written back. pi
        // normalizes that path again, so it is checked to be a fixed point of piPath first: one that is not (a file://
        // URL decoding to a Unicode space, which the second pass turns into a plain space) is denied, naming the path
        // pi would write instead, rather than writing back a path the guard did not check.
        const checked = ev.tool_input.file_path;
        if (!block && e.input && checked) {
          const again = piPath(checked, dirOf(ctx));
          if (again !== checked) return { block: true, reason: `obor: denied: the path checked (${checked}) is not the path pi writes (${again}); obor cannot guard the difference` };
          e.input.path = checked;
        }
        return block;
      },
      (err) => ({ block: true, reason: `obor: guard failed (${err?.message ?? err}); denied` }),
    ),
  );

  pi.on(
    "session_start",
    safe("session_start", async (_e, ctx) => {
      for (const line of waiting.splice(0)) warn(line);
      await started(ctx);
    }),
  );

  // input: an idle session's prompt is kept for its run's start (before_agent_start, which reports it); a line typed
  // into a busy session starts no run, so its hook runs here and its context goes on the line's text.
  pi.on(
    "input",
    safe("input", async (e, ctx) => {
      const text = typeof e.text === "string" ? e.text : "";
      const { id } = sessionOf(ctx);
      if (!e.streamingBehavior) return void typed.set(id, text);
      await started(ctx);
      const more = await promptAsk(text, ctx);
      return more ? { action: "transform", text: text + "\n\n" + more, images: e.images } : undefined;
    }),
  );

  // before_agent_start: a user run (a prompt) reports its prompt (input's raw text, else the expanded one), and a run
  // the inbox's wake starts reports the wake's text; the session's context on its first run and the prompt's context
  // go in one hidden message. Another custom run (the stop's own continuation) reports nothing.
  pi.on(
    "before_agent_start",
    safe("before_agent_start", async (e, ctx) => {
      const woken = e.messageType === "custom" && e.message?.customType === WAKE_TYPE;
      if (e.messageType !== undefined && e.messageType !== "user" && !woken) return;
      const { id } = sessionOf(ctx);
      let text = WAKE;
      if (!woken) {
        text = typed.has(id) ? typed.get(id) : typeof e.prompt === "string" ? e.prompt : "";
        typed.delete(id);
      }
      const parts = [];
      const start = await started(ctx);
      if (!contextGiven.has(id)) {
        contextGiven.add(id);
        if (start) parts.push(start);
      }
      const more = await promptAsk(text, ctx);
      if (more) parts.push(more);
      if (parts.length === 0) return;
      return { message: { customType: "obor", content: parts.join("\n\n"), display: false } };
    }),
  );

  // agent_before_settle: the stop. A run that ended on an error runs no stop (agent_settled reports it); a block
  // appends its reason and asks for one more run, whose own settle carries stop_hook_active: true.
  pi.on(
    "agent_before_settle",
    safe("agent_before_settle", async (e, ctx) => {
      const { id } = sessionOf(ctx);
      const active = continuing.delete(id);
      if (lastAssistant(ctx)?.stopReason === "error") return;
      const reason = stopBlock(await ask("stop", { ...base("Stop", ctx), stop_hook_active: active }, ctx));
      if (!reason) return;
      continuing.add(id);
      const entries = Array.isArray(e.entries) ? e.entries : [];
      return { entries: [...entries, { type: "custom_message", customType: "obor-stop", content: reason, display: true }], continue: true };
    }),
  );

  // session_compact or session_compact_failed: lines that came while pi compacted are entries already and owe their
  // wake. pi clears its compaction state after these handlers return, so the wake goes on the next tick (a compaction
  // inside a run leaves pi busy: the run's agent_settled sends it).
  const compacted = async () => {
    setTimeout(wake, 0).unref?.();
  };
  pi.on("session_compact", safe("session_compact", compacted));
  pi.on("session_compact_failed", safe("session_compact_failed", compacted));

  // agent_settled: final. It ends any continuation (an aborted run skips agent_before_settle), sends the wake lines
  // that came during the run owe (pi starts it once the settled handlers are done), and a run whose last assistant
  // message ended on an error is a stop-failure.
  pi.on(
    "agent_settled",
    safe("agent_settled", async (_e, ctx) => {
      continuing.delete(sessionOf(ctx).id);
      wake();
      const m = lastAssistant(ctx);
      if (m?.stopReason !== "error") return;
      await ask("stop-failure", { ...base("StopFailure", ctx), error: "unknown", error_details: String(m.errorMessage ?? "") }, ctx);
    }),
  );
}

export default function (pi) {
  try {
    create(pi, { cwd: process.cwd(), kit: kitRoot(), env: { ...process.env } });
  } catch (e) {
    console.error(`obor: the pi extension did not load: ${e?.message ?? e}`);
  }
}
