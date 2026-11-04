# DD-agent-driver: how obor drives Claude Code, Codex, opencode and herdr

Deep dive (ob-deep-dive, role pane, main checkout at 9a5346cb), 2026-11-04. The brief asked for
`docs/research/agent-driver-deep-dive.md`. The deep_dive write profile allows only `docs/reviews/`, so the guard
denied that path and the note is here. The lead can move it.

This note compares `internal/agents/*`, `opencode/obor.js`, `internal/launch`, `internal/herdr` and `internal/mcp`
against `docs/research/agent-cli-and-herdr-capabilities.md` (called **CAP** below; `CAP §2.2` is its section 2.2),
`docs/research/herdr-core.md` and `docs/research/opencode-model-selection.md`. I did not edit any source code.

**How to read it.** Every finding has a **Fact** part and a **Judgement** part. A Fact comes from code (file:line),
a CAP section, the git log or BACKLOG, or a read-only observation on this machine that I name (`ps`, `lsof`,
`herdr api schema --json`, `strings` on the codex binary, `~/.codex/models_cache.json`,
`~/.claude/plugins/installed_plugins.json`). A Judgement is my opinion: likelihood, impact and what to do. I ran no
live probe, started no herdr session, ran no headless agent and made no model call. §7 says what is still unverified.

## 0. Summary (judgement)

1. **The T-791 failure came from the opencode lane, which is the only kind whose obor MCP server and plugin come
   from user-global files.** All of them run `~/.obor/kit`, not the project's build. Claude (strict start) and Codex
   already register obor's server on each launch with the launcher's kit. Registering opencode the same way (one
   generated config named by `OPENCODE_CONFIG_CONTENT` or `OPENCODE_CONFIG`) closes the skew. It also takes obor out
   of the owner's own opencode windows. It is the best value for its cost (P1).
2. **Identity has one derivation since T-786, but its inputs come from five channels**: env exported by a typed shell
   line, the cwd, `HERDR_PANE_ID`, pane labels and herdr's agent name. Session ids come from three: obor's hooks,
   herdr's `agent_session`, and an `lsof` search over Codex rollouts. obor's opencode plugin strips the env identity
   that its MCP server keeps (F1.3). That leaves a latent guard hole for any role agent run on opencode.
3. **herdr is mostly used as documented.** There are four exceptions: an exact protocol pin that is stricter than
   herdr's contract (F3.1); a wake typed by `pane.send_input`, which skips herdr's blocked refusal (F3.3); pane labels
   used as durable identity, with a long bug history (F3.2); and two poll loops where herdr offers a server-side
   wait (F3.4).
4. **Small correctness items:** Codex efforts (F4.1); the Claude inbox line format, which is undocumented (F4.2); the
   Claude transcript path re-derived instead of read from the record (F4.4); stale claims in `docs/design.md` (F4.6).
5. **A conformance suite is cheap to start.** Its static tier needs no session and fits nightly. Its live tier is one
   throwaway herdr session per kind (§6). Claude Code shipped about 100 versions in 3 months (CAP §2), so drift is
   certain.

## 1. Identity channel per agent kind (T-786, T-791)

### F1.1 Where each kind gets its inputs (fact)

`project.Self` derives the name from `(role, lane)` (`internal/project/project.go:193-239`). The role comes from
`OBOR_ROLE`, the lane from the cwd's lane worktree, else `OBOR_LANE` (`DetectRole`, :193). `obor mcp` re-derives it
on every call (`internal/mcp/mcp.go:207-221`) and also sends `HERDR_PANE_ID` as `Call.Pane` (:260). Serve checks
that pane against the lane (`internal/app/fetch.go:180`).

| Input | Claude | Codex | opencode (`--standalone`) |
|---|---|---|---|
| Env the MCP server sees | the whole Claude env (CAP §2.2, verified) | allowlist plus `env_vars` = `agents.EnvVars` `OBOR_ROLE OBOR_LANE HERDR_PANE_ID OBOR_STATE_HOME` (`internal/agents/agents.go:201`, `internal/agents/codex/codex.go:43`; CAP §3.2, verified) | the pane's env, because the server is the TUI's child (CAP §4.2 P1, verified; base args `--standalone`, `internal/config/models.go:37`) |
| cwd the MCP server sees | launch cwd (CAP §2.2) | session cwd (CAP §3.2) | location dir (CAP §4.2; location dir vs server cwd not told apart) |
| Env the hooks see | Claude's whole env (CAP §2.3) | **no obor hooks** (`Caps.Hooks: HooksNone`, `codex.go:315`) | the plugin's env **minus** `OBOR_AGENT OBOR_ROLE OBOR_LANE HERDR_PANE_ID` (`opencode/obor.js:68`, `hookEnv` :292-296) |
| Session id | obor's SessionStart hook (`internal/guard/session.go:24-107`), plus herdr `agent_session` | `lsof` of the rollout the process holds open (`internal/launch/session.go:64-130`, :199), plus herdr `agent_session` (herdr's Codex hook, CAP §3.9) | the plugin's first event per session (`obor.js:338-345`) |
| Which kit's `obor` runs MCP | launcher kit on a strict start (`claude.go:30-37,60-63`); plugin dir otherwise | launcher kit (`codex.go:39-46`) | **`~/.obor/kit`** (user-global `opencode.json`, `opencode.go:118-134`) |
| Which kit's `obor` runs hooks | plugin dir (`hooks/hooks.json`, `OBOR_NO_SELF_HOST=1`) | – | `kitFor`: the main checkout in an obor checkout, else the plugin's kit; the JS itself always comes from `~/.obor/kit` (`obor.js:88-92`) |
| How identity env reaches the pane | a typed `cd … && unset … && export OBOR_ROLE=… OBOR_LANE=…` line, scraped for a marker (`internal/launch/launch.go:620-660`, `envLine` :670) | same | same, plus `OBOR_OPENCODE_MODEL` (`opencode.go:64-74`) |

Observed read-only with `ps` and `lsof` (2026-11-04): the Codex reviewer (pid 44177, cwd obor-kit) runs
`dist/obor mcp` as its **direct child**, so no Codex daemon sits in the path. The obor-kit Claude lead and workers run
`obor-kit/dist/obor mcp`. The axis-alies lead runs `~/.obor/bin/obor-0.24.2 mcp` even though
`installed_plugins.json` lists a 0.16.0 cache path for that project. So Claude loads a directory-marketplace plugin from
its source dir, not from the cache. The shared opencode service (pid 2883, `opencode serve --service`, started outside
herdr) runs **two** `obor-0.24.2 mcp` children, one per location it has opened. A Claude session in codex-image-tool
still runs `obor-0.19.1 mcp`, which it started before the kit moved to 0.24.2.

### F1.2 T-791's cause is a kit-version split, not identity (fact + judgement)

Fact: BACKLOG Handoff (11-04): "the opencode failure was kit skew: global opencode MCP runs ~/.obor/kit 0.24.2".
T-786 stopped exporting `OBOR_AGENT` (`launch.go:675` now unsets it). The 0.24.2 binary still read it, so it named no
one (`not -`). T-791 added the refusal text (`mcp.go:207-221`, `:256-258`), but the skew is still there.

Judgement: **certain to recur** at every change to how the name is derived, the call shape or the tool table, until
opencode's registration goes per launch (P1). Claude and Codex cannot hit it, because their MCP command names the
launcher's kit.

### F1.3 The opencode plugin and its MCP server read identity from different inputs (fact + judgement)

Fact: obor.js says that under `--standalone` its process env "is the pane's" (`obor.js:17-23`). It also says that it
"may run in a process shared by several panes" and so strips the identity vars (`obor.js:48-49,68`). obor always
starts opencode with `--standalone` (`config/models.go:37`). The MCP server of the same agent keeps those vars
(F1.1). The guard leaves role `none` in the main checkout unrestricted (`internal/guard/guard.go:198`).

Judgement: for a **worker**, nothing breaks, because the lane cwd gives the role (`DetectRole`). For a **role
agent on opencode** (researcher, integrator, … set to `deepseek` or `crouton`), the hooks would see role `none` in
the main checkout. The guard would then skip protected-file and git-write denials, while its MCP calls still act as
that role. No project's config has an opencode role today (checked: obor-kit, axis-alies, axis-maps,
codex-image-tool, codex-img-viewer), so this is **latent: unlikely now, high impact once someone picks a cheap model
for a role**. The fix is small (P5).

### F1.4 A stale `HERDR_PANE_ID` after a pane moves (fact + judgement)

Fact: herdr pane ids are workspace-qualified and change when a pane moves to another workspace. The old
`HERDR_PANE_ID` stays only as an alias for `--current` (CAP §5.1). obor records `Call.Pane` and the session's `Pane`
from the env (`mcp.go:261`, `guard/session.go:107`) and matches them against serve's ids (`internal/app/messages.go:727`).

Judgement: low likelihood, because obor places panes itself and moves them rarely. The effect is a session not
matched to its pane (a needs-you), never a wrong agent. Keep it on the watch list. No task.

### F1.5 Identity through the shell's exported env (fact + judgement)

Fact: the role, the lane and per-kind vars live in the pane's **shell**, typed there and confirmed by scraping the
screen for a marker (`launch.go:620-660`). History: BACKLOG 10-30 "`obor setup role --pane` keeps the shell's old
env"; T-707 "Every non-opencode start now unsets OBOR_OPENCODE_MODEL"; T-601 and RV-64 #3 (a failed `cd` still
printed the marker).

Judgement: since T-786, workers need no env for identity (cwd). Role agents need only `OBOR_ROLE`. Passing identity
through the launch itself would retire the shell-state class: Claude `--settings '{"env":{…}}'`, Codex
`-c mcp_servers.obor.env={OBOR_ROLE=…}` in place of `env_vars`, opencode's generated config `environment`. This only
pays off together with P1, and Claude's half is unverified (P8).

## 2. MCP and plugin registration: user-global vs per launch

### F2.1 What is user-global today (fact)

| Registration | Where | Runs | Per-launch alternative (CAP) |
|---|---|---|---|
| opencode MCP `mcp.servers.obor` | `~/.config/opencode/opencode.json` (`opencode.go:126-131`) | `~/.obor/kit/bin/obor mcp`, pinned | `OPENCODE_CONFIG_CONTENT` / `OPENCODE_CONFIG` (CAP §4.1, verified working in P1/P2) |
| opencode plugin | symlink `~/.config/opencode/plugins/obor.js` → `~/.obor/kit/opencode/obor.js` (`opencode.go:132`) | the kit's JS, in every opencode on the machine (gated by `inProject`, `obor.js:333`) | `plugins: ["file://<kit>/opencode/obor.js"]` in the same config (CAP §4.3, docs only) |
| herdr plugin `obor.events` | `~/.obor/herdr-plugin` → kit (`herdr-plugin/herdr-plugin.toml`) | `~/.obor/kit/bin/obor plugin restore` on every herdr start | none in herdr; serve could restore on "herdr: back" (`internal/app/serve.go:86-110`), or obor could report `resume_argv` (CAP §5.3, §5.6) |
| Claude plugin | project `.claude/settings.json` → marketplace dir (obor-kit itself, or `~/.obor/kit`) | the marketplace dir's `bin/obor` | already per project; a strict start adds `--mcp-config` (`claude.go:60-63`) |
| Codex MCP | per launch `-c mcp_servers.obor.*` (`codex.go:39-46`) | launcher kit | already per launch |

### F2.2 Per launch vs today (judgement)

- **opencode: move to per launch (P1).** Cost: `StartEnv` adds one variable. `Install` no longer writes the global
  entry and link. `obor setup agents` removes old ones. `kitFor` in obor.js goes, because the config names the kit.
  Gains: no skew, one mechanism shared with Codex and strict Claude, no obor MCP processes or plugin in the owner's
  own opencode windows (the two orphan `obor mcp` under pid 2883). Unknowns to probe first (§6, OC-L3/OC-L4):
  1. precedence and merge between the env config and a still-present global `obor` entry;
  2. whether `plugins` from the env config loads under `--standalone`;
  3. the size of the typed env line: herdr refuses over-long lines on macOS (CAP §5.2, v0.9.4). A path in
     `OPENCODE_CONFIG` stays short, inline JSON does not.
- **Claude: keep.** Non-strict sessions (lead, role agents) use the marketplace dir, which matches serve's kit in
  every project I checked (ps, above). Strict starts already name the launcher's kit. Resume drops `--mcp-config` and
  `--settings` (CAP §2.1). `claude.go:45-75` passes both on every start, resume included, so this is covered.
- **Codex: keep, and add `--no-daemon` to the base args (P3).** Today the MCP server is a direct child (observed). The
  auto-started shared daemon (`daemon_auto_start` stable, CAP §3.1) would give MCP and hooks the daemon's env and
  cwd. That is the class of herdr #4814 and #4649 (CAP §5.8).
- **herdr plugin: later (P9).** It is the third user-global registration that runs `~/.obor/kit`. BACKLOG (v0.17) has
  already seen it run an old `plugin restore`.

### F2.3 The wire has no version, so skew shows up as a wrong answer (fact + judgement)

Fact: `wire.Call` (`mcp.go:260`) carries no binary version. `initialize` reports `version.Version` to the agent CLI
only (`mcp.go:182-183`). MCP servers outlive kit upgrades: the 0.19.1 server above still runs against a 0.24.2 kit.
In obor-kit, serve rebuilds `dist/obor` after every code merge, while running workers keep their old `obor mcp`
process.

Judgement: per-launch registration (P1) removes the skew *between kinds*, but skew *across time* stays. A version on
every call, with serve naming a mismatch in its refusal, would have turned T-791 into a one-line diagnosis (P4).
Likelihood of another time-skew bug: medium, given how often the wire changes.

## 3. herdr: undocumented behaviour, polling, history

### F3.1 An exact protocol pin, stricter than herdr's own contract (fact + judgement)

Fact: `Protocol = 22`, and `Dial`/`Snapshot` refuse anything else (`internal/herdr/socket.go:113,163-168,314`).
herdr's docs say JSON clients "should ignore unknown fields and handle unsupported methods as normal errors"; the
numbered protocol is for same-install operations (CAP §5.1 "Compatibility"). herdr-core.md §4 already calls this
"the exact-protocol pin that breaks obor on every herdr protocol bump". Every method obor calls exists in the 0.9.3
schema (checked against `herdr api schema --json`: `session.snapshot agent.{list,get,prompt,send_keys,rename,start}
pane.{get,send_keys,send_input,read,rename,list,layout,split,close,process_info} tab.create workspace.create
plugin.{list,link,unlink} notification.show events.subscribe`).

Judgement: a herdr update that bumps the protocol stops all dispatch until an obor release ships, which is the
"unsafe upgrade" the owner wants gone. Accepting `protocol >= 22` and letting the conformance suite's schema check
catch a removed method or param (§6 H-S1) moves that risk out of the runtime. Value high, cost small (P2). Whether
0.9.4 or 0.9.5 bumped the protocol is unknown (CAP §5.9 does not say).

### F3.2 Pane labels as durable identity: a pattern of bugs (fact + judgement)

Fact: `RenamePane` says "Labels are obor's agent identity (R-28 §2)" (`socket.go:417`). herdr documents `label` as a
pane field separate from the agent name (CAP §5.2). obor's own observation (RV-64 #1, BACKLOG Open): "herdr's rename
emits no event". obor subscribes to `pane.updated` (`socket.go:613,697`), but CAP does not say whether a rename
emits it. History: T-595, T-745, T-757/T-758, T-783, T-784, RV-89 #2, RV-101 #2, BACKLOG 10-30 ("serve keeps a pane's
old label", "Lanes did not pick `ob-*`"): at least eight tasks or accepted findings since 10-30 about label caches
and relabels.

Judgement: the most frequent herdr-related bug class in the last week. First a fact: does `pane.rename` emit
`pane.updated` in 0.9.3 (probe H-L3)? If yes, the stream already handles it and the residuals are race windows. If
no, labels can only be trusted when read right then, and every cache of them is a bug source. In that case prefer
proof by session (obor picks the Claude session id, P6) and keep labels for humans and restore. I do not propose
removing labels now; they are what restore keys on.

### F3.3 A wake typed past herdr's blocked refusal (fact + judgement)

Fact: after a **resume** with a job, the wake is typed with `l.H.Type` (`internal/launch/launch.go:533`). That is
`pane.send_input` with text plus Enter (`socket.go:396-398`), pane-level and without the `agent_blocked` refusal.
Every other wake goes through `Panes.Type`, which is `agent.prompt` (`internal/herdr/panes.go:302-311`), refused
before any byte when the agent is blocked (CAP §5.4, §5.7). BACKLOG 10-26/27: "A typed wake or brief lands in a
Codex/opencode dialog … it picks an option."

Judgement: medium likelihood right after a resume, when Claude or Codex can show a resume or update dialog. The fix
is one line: use the `agent.prompt` path (P7). herdr #5263 (`agent.prompt` types into a Claude permission dialog
while a PermissionRequest hook runs) shows the refusal is best effort, but it is still better than none.

### F3.4 Polling where herdr has a server-side wait (fact + judgement)

| Loop | Code | herdr has | Judgement |
|---|---|---|---|
| env line: reads the screen every poll (1 s) for a marker | `launch.go:633-650` | `pane.wait_for_output` (checks the current snapshot first, then waits; verified, CAP §5.4) | one call replaces the loop; small (P7) |
| agent start: `agent.get` every poll beside `agent.start` | `internal/launch/start.go:110-150` | `agent.start` "returns only after herdr detects the agent and it is ready" (docs; verified for Claude in 3.6 s, CAP §5.4); `pane.agent_detected` / `agent_status_changed` events | **docs and obor disagree**: obor's comment says herdr "answers at once, before the agent runs (launch_pending, live T-544)" (`socket.go:436`). Find out which (probe H-L1) before changing it. obor never sends `timeout_ms` (`socket.go:440-444`; schema has it) |
| Codex session: `lsof` every poll, up to 10 s | `session.go:64-130,199` | nothing in herdr; Codex's SessionStart hook has `session_id` and `transcript_path` (CAP §3.3/§3.4 (a)) | see P6 |
| serve's stream | `socket.go:686-735` | per-pane status subscriptions: `pane_id` is **required** for `pane.agent_status_changed` (schema checked) | conformant. Re-reading per event and reconciling on refresh matches "no shared sequence boundary" (CAP §5.6) |

### F3.5 Other herdr points (fact)

- `eventName` accepts both `pane_created` and `pane.created` spellings (`socket.go:645-649`). The wire spelling is
  undocumented; the schema uses dotted names. Tolerant code, so no action, but a conformance check (H-L5) keeps it
  honest.
- `named()` renames an agent herdr lost the name of (`start.go:208-232`, T-387). That is upstream **#5627** ("an agent
  started with `agent start` loses its name when its first turn outlasts the start timeout", CAP §5.8). Keep it.
- obor turns herdr's native agent restore off (`resume_agents_on_restore = false`, `internal/setup/plugin.go:230`)
  and restores through its plugin. Needed, because herdr's `claude --resume <id>` would drop obor's `--mcp-config`
  and `--settings` (CAP §2.1, docs only) and its strict start.

## 4. What obor re-implements that the CLIs or herdr now provide, and other driver drift

### F4.1 Codex efforts are out of date (fact + judgement)

Fact: obor accepts `minimal low medium high xhigh` (`codex.go:64`). `~/.codex/models_cache.json` lists, for the
built-in models: terra and sol `low medium high xhigh max ultra`, luna `low medium high xhigh max`. None of them lists
`minimal`. CAP §3.1 agrees ("e.g. low/medium/high/xhigh/max/ultra").

Judgement: high likelihood. `terra:max` is refused by `obor spec set`/dispatch, and `terra:minimal` is accepted and
then rejected or ignored by Codex. Replace the list with the union `low medium high xhigh max ultra`. A tiny task
(P10). The models cache is not a documented interface, so do not read it at runtime.

### F4.2 The Claude inbox line format is reverse-engineered (fact + judgement)

Fact: `Deliver` writes `{"type":"auth","token":…}` then `{"type":"user","message":{"role":"user","content":…}}`,
"the two lines of the binary's own sender … Written against Claude Code 2.1.312" (`claude.go:274-304`). CAP §2.5:
the auth line is documented, messages are "plain text", and "the line format of a message is not documented". CAP §2.5
inbound policy: when `crossSessionInbound` is unset, a message is accepted or **held** (approval dialog) by the
sender's and receiver's permission-mode classes. obor sets no `crossSessionInbound`. obor's Claude panes run
`--permission-mode auto` (`config/models.go:35`).

Judgement: today it works (T-485, T-499, in use since October). It is the one obor channel that depends on an
undocumented wire shape of a CLI that updates daily, so it carries **medium likelihood of a silent break** at some
update. A held message would also put a dialog in a lane pane. Probe CC-L4 settles it: plain text vs envelope, and
held or not under `auto`. Then either switch to the documented plain form or keep the envelope behind the
conformance check, and set `crossSessionInbound: "accept"` in obor's `--settings` for its own panes (P8).

### F4.3 Session ids: three sources where the CLIs offer one each (fact + judgement)

Fact: Claude has `--session-id <uuid>` (verified, CAP §2.1); the id then flows to SessionStart and to the transcript
name. opencode has `--session <id>` "continue, or create if it does not exist" (help text; creation unverified, CAP
§4.1, §4.4). Codex hooks are stable and on by default; SessionStart carries `session_id` and `transcript_path`
(CAP §3.3), but non-managed hooks need trust review or `--dangerously-bypass-hook-trust` (CAP §3.3). Today obor uses
its Claude hook, an `lsof` search for Codex (F3.4) and herdr's `agent_session` as a fallback (`launch.go:432-443`,
`start.go:234-243`). herdr's source has its own issues: #5733, #5348 (no `agent_session` without `python3`), #5443
(CAP §5.8).

Judgement: one source, "obor names or hears the session from the agent itself", fits "one identity source"
(herdr-core.md question 4 asks the same). Order: Claude `--session-id` (small; the store knows the id before the
agent runs), then Codex SessionStart once probe CX-L5 shows a per-launch hook can run without a trust prompt, then
drop the `lsof` search and the herdr fallback (P6). If the Codex hook needs trust each time, keep `lsof` for Codex.

### F4.4 Claude transcript path derived instead of read (fact + judgement)

Fact: `checkTranscript` builds `projects/<cwd slug>/<sid>.jsonl` (`launch.go:552-580`, `claude.go:247-257`). CAP §2.4:
the cwd in that path is **realpath-resolved** and truncated plus hashed past 200 characters. obor's slug does neither.
The SessionStart hook already records `transcript_path` in the store (`guard/session.go:107`).

Judgement: low likelihood (lane paths are real and short here). A symlinked or long project path would make
`obor restart` refuse "recorded session … is not from <cwd> (pass --fresh)". Using the recorded path is simpler and
correct. Fold it into P6.

### F4.5 Codex rollout parsing: undocumented, but the only rate-limit source (fact + judgement)

Fact: Codex turns and limits are read from rollout lines (`codex.go:159-268`). CAP §3.4: the format "not stable";
legacy rollouts are being migrated. `token_count.rate_limits` exists only there (CAP §3.7). Codex has no
StopFailure-like hook (CAP §3.7).

Judgement: keep it. It is the only source of reset times. Cover it in the static conformance tier: parse the newest
real rollout with `codex.Session`/`LastTurn`, no model call (CX-S3). Turn start and end could come from Codex
`Stop`/`UserPromptSubmit` hooks once hooks are in (P6), which would shrink this dependency to limits only.

### F4.6 Stale claims in `docs/design.md` (fact)

`docs/design.md:23` says "Hooks only cover Claude Code panes. Codex/opencode lanes rely on their sandbox". `:349` says
"Codex has no hook". opencode lanes run obor's guard through obor.js (`obor.js:347-355`), and Codex 0.166 has stable
PreToolUse, Stop and SessionStart hooks (CAP §3.3). The design doc is lead-owned: QUESTIONS.md or the next task that
edits it.

### F4.7 Overlaps that are fine as they are (judgement)

- Claude's own usage-limit auto-continue (`autoContinueAtUsageLimit`, CAP §2.7) vs obor's continue wake
  (`internal/app/stopwake.go`). obor already recognises Claude's continuation sentence (`internal/guard/prompt.go:136`),
  and its wake ends once a turn starts, so the two do not double-prompt. No change.
- `claude agents --json`, `codex queue` and opencode's HTTP API (CAP §2.4, §3.5, §4.5) are documented delivery or
  status channels, but none of them reaches obor's panes as started (in-process servers, `--standalone`). Watch only.

## 5. Proposals ranked by value vs cost (judgement)

Ranked for the owner's goal: fewer mechanisms, one identity source, safer upgrades. "Probe" means a §6 check must
pass first.

| # | Proposal | Removes | Value | Cost | Probe |
|---|---|---|---|---|---|
| P1 | opencode MCP and plugin registered per launch (generated config through `OPENCODE_CONFIG_CONTENT` or `OPENCODE_CONFIG`; setup stops writing and removes the global entry and link; `kitFor` goes) | user-global registration ×2, the T-791 skew, obor in the owner's own opencode | high | small–medium | OC-L3, OC-L4 |
| P2 | herdr: accept `protocol >= 22`; the schema check in the conformance static tier replaces the exact pin | dispatch stopping on herdr upgrades | high | small | H-S1 |
| P3 | Codex: `--no-daemon` in `KindBaseArgs` | the daemon env/cwd risk to MCP identity | medium | tiny | CX-L1 |
| P4 | Version on every wire call; serve names a mismatch (caller vs serve) in refusals and its log | silent skew across time | medium | small | – |
| P5 | obor.js: stop stripping identity under `--standalone` (or refuse opencode for role agents in config validation) | the hook vs MCP identity split, latent guard hole | medium | small | OC-L2 |
| P6 | One session source: Claude `--session-id` chosen by obor; Codex SessionStart hook if trust allows; the recorded `transcript_path` used everywhere; drop the `lsof` search and the herdr `agent_session` fallback | 3 session channels → 1, path re-derivation | medium–high | medium | CC-L2, CX-L5 |
| P7 | Wake after resume via `agent.prompt`; env-line wait via `pane.wait_for_output` | the blocked-refusal bypass, one poll loop | medium | small | H-L2, H-L4 |
| P8 | Identity and per-kind vars through each kind's launch registration (Claude `--settings` env, Codex `-c …env`, opencode config `environment`), not the shell's env line; `crossSessionInbound: accept` and the documented inbox line | shell-state identity, the undocumented inbox shape | medium | medium | CC-L3, CC-L4 |
| P9 | herdr restore plugin → serve-owned restore on "herdr: back", or a reported `resume_argv` | the last user-global registration | medium | medium–high | H-L6 |
| P10 | Codex efforts `low medium high xhigh max ultra` | wrong refusals and acceptances | low (correctness) | tiny | CX-S2 |

Not proposed: Codex PreToolUse hooks for the guard. It is one more mechanism (trust review) for a kind used only as
a reviewer, and the git-hook layer already covers git writes. Revisit if Codex runs workers again. Moving to
herdr-core: herdr-core.md's triggers still hold.

## 6. Conformance suite per driver

**Goal.** One check per fact a driver depends on, each naming the driver code and the CAP section. It runs after a CLI
or herdr update, and in part nightly. It fails loudly instead of letting a lane find out.

**Shape (judgement).**
- `tests/conformance/` as a Go program behind a build tag, outside `go test ./...`, so `make check` stays stubbed as
  AGENTS.md requires. It runs as `make conformance [KIND=claude|codex|opencode|herdr] [TIER=static|live] [TURN=1]`.
- Each check prints `PASS|FAIL|SKIP <id> <fact> (<driver file:line>, CAP §x)`.
- It writes the versions that passed to `.obor/state/conformance.json`. `obor status` can then show "claude 2.1.320
  not checked" when the installed version differs.
- **Static tier** (no session, no model; nightly): schema, help text, binary strings, on-disk files.
- **Live tier** (lead or owner, on a version change): one throwaway session `herdr --session obor-conf-<kind>` with the
  server started under a clean env, in a scratch dir holding a minimal `.obor/config.json` and a lane worktree. The
  stub MCP server is a tiny Go program that writes `{pid, ppid, cwd, env}` and answers `initialize`/`tools/list`
  (CAP §1.3 did this in Python; the kit ships no Python). The default session is never touched, no herdr plugin is
  linked (AGENTS.md "Tests"), no `claude -p`/`codex exec`/`opencode run` is used (the guard denies them; interactive
  only). Model calls only with `TURN=1`: one "Reply with exactly OK" per kind. Codex runs on a scratch `CODEX_HOME`
  copy (auth.json copied and removed after); opencode on scratch `XDG_*` plus `OPENCODE_CONFIG_CONTENT` (CAP §1.3).
  Claude needs the real `~/.claude` for auth, so it uses a scratch project dir; the trust entry it adds to
  `~/.claude.json` is a known leftover.

### herdr (driver: `internal/herdr`)

| Id | Tier | Check | Guards |
|---|---|---|---|
| H-S1 | static | every method and param obor sends exists in `herdr api schema --json`; `pane.agent_status_changed` still needs `pane_id` | `socket.go` call sites (F3.1), P2 |
| H-S2 | static | `ping` protocol and capabilities recorded; the run warns on a new protocol rather than failing | `socket.go:113` |
| H-L1 | live | `agent.start` over the socket: answers within obor's 10 s client timeout (the line records when, what status and whether `launch_pending`); `timeout_ms` honoured | `socket.go:436`, `start.go:110` (F3.4) |
| H-L2 | live | `agent.prompt` to a pane showing a known dialog → `agent_blocked`, nothing typed; `pane.send_input` → typed | `launch.go:533` (F3.3) |
| H-L3 | live | `pane.rename` emits no `pane.updated` on a status-subscribed stream: the stream's reread is where a relabel is seen | F3.2 |
| H-L4 | live | `pane.wait_for_output --match` sees a marker already on screen and one printed later | `launch.go:633` (P7) |
| H-L5 | live | lifecycle event spellings on the wire (`pane_created` vs `pane.created`) | `socket.go:645` |
| H-L6 | live | stop/start of the named session: panes come back as shells with labels and cwd; whether the startup hook would fire (log only, no plugin linked) | `internal/launch/restore.go` (P9) |
| H-L7 | live | injected `HERDR_*` in the pane env and in a stub MCP server child | F1.1 |

### Claude Code (driver: `internal/agents/claude`, `hooks/hooks.json`)

| Id | Tier | Check | Guards |
|---|---|---|---|
| CC-S1 | static | `claude --help` lists `--effort` with exactly `low medium high xhigh max`, plus `--mcp-config`, `--strict-mcp-config`, `--settings`, `--resume`, `--session-id`, `-p/--print` | `claude.go:79`, `Headless` |
| CC-S2 | static | every event `hooks/hooks.json` names (UserPromptSubmit, PreToolUse, Stop, StopFailure, SessionStart) is in the hooks doc's event list | `hooks/hooks.json` |
| CC-L1 | live | `Start()` argv in a lane: the stub server sees `OBOR_ROLE`, `OBOR_LANE`, `HERDR_PANE_ID`, cwd = lane; strict hides a second (manifest) server; `--resume` keeps `--settings` and `--mcp-config` | `claude.go:45-75` (F1.1) |
| CC-L2 | live | SessionStart payload `session_id`, `transcript_path`; with `--session-id` they equal the given id; `transcript_path` is a realpath (symlinked scratch dir) | `guard/session.go`, F4.3, F4.4 |
| CC-L3 | live | `--settings '{"env":{"X":"1"}}'` reaches hooks and the stub MCP server | P8 |
| CC-L4 | live, TURN | inbox: the envelope starts a turn under `--permission-mode auto`, not held (the plain-text line and `crossSessionInbound`'s effect are not probed: one model call per kind) | `claude.go:279-304` (F4.2) |
| CC-L5 | live | trust dialog text still matches `trustRE`; `/exit` + recipe exits 0 (the exit dialog regex is not probed: it needs a background task, which needs model turns) | `start.go` `notReady`, `claude.go:210-214` |
| CC-L6 | live, TURN | Stop payload `last_assistant_message`; StopFailure stays docs-only (cannot be forced safely) | `guard/stop.go` |

### Codex (driver: `internal/agents/codex`)

| Id | Tier | Check | Guards |
|---|---|---|---|
| CX-S1 | static | `codex --help`: `-m`, `-c`, `--no-daemon`, `--strict-config`, `resume`, `exec`; obor's `-c` keys present in the binary (`default_tools_approval_mode`, `check_for_update_on_startup`, `env_vars`, `model_reasoning_effort`: all present in 0.166.0 by `strings`) | `codex.go:39-51`, `Headless` |
| CX-S2 | static | the built-in models' efforts (models cache, read in the test only) ⊆ `Efforts()` and `Efforts()` ⊆ their union | `codex.go:64` (F4.1) |
| CX-S3 | static | the newest real rollout: line 1 parses with `Session`; `LastTurn` finds a turn; a `token_count.rate_limits` window parses | `codex.go:159-268` (F4.5) |
| CX-L1 | live | `Start()` argv with `--strict-config`: starts; stub MCP sees only the allowlist + forwarded vars, cwd = lane, ppid = the codex pid, with and without `--no-daemon` | F1.1, P3 |
| CX-L2 | live | the update screen does not show with `check_for_update_on_startup=false`; `/quit` exits 0 | `codex.go:48-52,127` |
| CX-L3 | live, TURN | a rollout appears after the first turn and the process holds it open (`lsof`), as `reportRollout` expects | `session.go:64` |
| CX-L5 | – | not in the suite: a SessionStart hook given per launch and its trust prompt are P6's Codex half (deleted until then; the driver sends none) | P6 |

### opencode (driver: `internal/agents/opencode`, `opencode/obor.js`)

| Id | Tier | Check | Guards |
|---|---|---|---|
| OC-S1 | static | `opencode --help` TUI: `--standalone`, `--auto`, `--prompt`, `--session`; still no `--model` (if one appears, `StartEnv`'s plugin path can retire); `run` exists | `opencode.go:28-34`, `Headless` |
| OC-S2 | static | `node --check opencode/obor.js` plus its unit tests (`obor.test.js`) | obor.js |
| OC-L1 | live | `--standalone`: the stub MCP is the TUI's grandchild, sees the pane env, cwd = lane | F1.1 |
| OC-L2 | live | obor.js hook env: what the hook sees for a role agent in the main checkout (today: role `none`) | F1.3, P5 |
| OC-L3 | live | `OPENCODE_CONFIG` with `mcp.servers.obor`: the per-start entry is the only obor server that runs; which `command` runs | P1 |
| OC-L4 | live | `plugins: ["file://…/obor.js"]` from the env config loads under `--standalone` (plugin writes a marker), set up once, with no global link | P1 |
| OC-L5 | live, TURN (fake or free provider, as T-703) | `session.execution.started/succeeded` reach the plugin; `OBOR_OPENCODE_MODEL` switches the first turn; `/exit` + second Enter exits | obor.js `sessionHooks`, `modelSwitch`; `opencode.go:105` |

**Running it (judgement).** Nightly runs `TIER=static` for all kinds; it takes seconds and needs no session. The
lead runs `TIER=live KIND=<kind>` when the installed version differs from the last pass recorded, and before a
release that touches a driver. Add `TURN=1` when a check marked TURN guards the change. A FAIL is a task. A SKIP
names why (no auth, version too old). The first run of each live check also turns the unverified CAP rows (CAP §6)
into verified ones.

## 7. Not verified here

- Whether `agent.start` answers at once (`launch_pending`) or once the agent is ready (docs vs T-544; H-L1): the live
  line records it per start and the 2026-11-04 run saw an at-once answer with the agent up 3.7–4.4 s later, so obor's
  poll is still needed.
- Precedence of `OPENCODE_CONFIG_CONTENT`/`OPENCODE_CONFIG` over the global config, and plugin loading from it
  (OC-L3, OC-L4).
- Claude: `--settings` `env` reaching MCP and hooks (CC-L3).
- Claude: not probed: the plain-text inbox line (rather than the envelope) starting a turn under
  `--permission-mode auto`, and `crossSessionInbound`'s effect on a held message (CC-L4: one model call per kind); the
  exit dialog (CC-L5: needs a background task, which needs model turns).
- Codex: when the daemon would be used for an interactive pane with the real `~/.codex` (only observed: not for the
  running reviewer); per-launch hooks and trust are not probed (CX-L5 is deleted until P6's Codex half is a task: the
  driver sends no per-launch hook).
- herdr 0.9.4/0.9.5 protocol numbers.
- That Claude never runs the plugin from `~/.claude/plugins/cache/*` for these projects: inferred from the running
  MCP binaries' versions (§1.1), not from Claude's docs.
