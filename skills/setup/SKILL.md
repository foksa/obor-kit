---
name: setup
description: Onboard a project to obor — ask the user for name, prefix, lanes, models per role and check commands, then create .obor/config.json and role docs, lane worktrees and agent panes with `obor setup`; also adds lanes / panes / role agents.
disable-model-invocation: true
---

# /obor:setup — make a project obor-ready

You are the lead, setting up the swarm together with the user. Ask, don't guess: every choice below is the user's.
`obor setup …` subcommands are idempotent and end with `OK …` or `FAIL …`; read the last line, fix, rerun.
Nothing is committed for you: show the user the diff and commit by explicit path when they agree.

## 0. Plugin installed?
`obor` must be on the Bash PATH (`command -v obor`). If not, the user installs the plugin for this project:
```
claude plugin marketplace add <path to obor-kit>
claude plugin install obor@obor-kit --scope project
```
then restarts the session. Deps: git, jq, herdr (bash 3.2 is enough).

## 1. Ask the user (one message, numbered questions)
1. Display **name** and **prefix** (`^[a-z][a-z0-9-]{0,7}$`; herdr agent names are global across workspaces, so the
   prefix keeps `<prefix>-o-01` unique — not `o-01`).
2. **Lanes dir** (default `../<repo>-wt`) and **lanes**: how many, of which type (`O-01 opus`, `S-01 sonnet`,
   `T-01 luna` …). Fewer lanes than they think: all Claude panes share one plan's limits.
3. **Lane types / models per role**: kind (`claude`, `codex`, …) and args per type; `max` running agents per type;
   which type the integrator and reviewer run (`roles.integrator.type`, `roles.reviewer.type`) — a reviewer from a
   different model family if possible.
4. **Commands** (null = skipped): `lane_setup` (installs deps in a new lane), `lane_ready` (a test such as `test -d node_modules`; when it fails, dispatch runs lane_setup first), `check` (merge check), `check_fast`
   (named in worker prompts), `baseline` (+ its `dir`), `gate` (+ `paths`), `e2e`; timeouts for slow ones.
5. **Paths**: task specs dir, reviews dir; `lane_links` (big untracked dirs
   to symlink into lanes); `protected_files` (lead-only globs).
6. Per machine (`.obor/local.json`, gitignored): `limits.max_panes_per_tab` (default 4), lane type `max` overrides,
   baseline `dir` overrides.

## 2. Init
```
obor setup init --name "<name>" --prefix <prefix>
```
Creates `.obor/config.json` from the example (no lanes, project commands cleared), `.gitignore` lines
(`.obor/state/`, `.obor/drafts/`, `.obor/local.json`, `.task-run`, `lane_links`), and in `docs/agents/` short
project role docs (a pointer line to the kit's role text + its "This project" prompts), the roster and the task-spec
template. The role texts themselves stay in the kit, read in place. Existing files are never overwritten
(`skip <file> (exists)`). No STATUS file: it is gone as an obor concept (T-470); a project that wants one keeps it as
its own prose.
Labels are the identity: only labelled panes get restores (`obor setup pane` / `role` and
`obor restart` set them).
Then it does what `obor setup herdr-plugin` does: the `obor.events` herdr plugin restarts obor's labelled panes after
a herdr session restore (`obor plugin restore`; the heartbeat's event kicks went with it, T-465). herdr links it
by path and keeps the path resolved at link time (`plugin_root`), so setup links the current obor's `herdr-plugin/`
directly. Not linked yet: at a terminal setup asks once; otherwise (you, running it) it prints
`hint: … herdr plugin link <obor>/herdr-plugin`. Linked to another kit (herdr's `plugin_root` is not
this obor's `herdr-plugin`, e.g. a dev checkout, or an old version's path): at a terminal it offers the re-link,
otherwise it prints `hint: … herdr plugin unlink obor.events && herdr plugin link <path>`. On a machine with two obor
kits (the stable plugin and an obor-kit dev checkout), link the **stable** one: every project's kicks and restores
run it. Linking is global per user (a no-op outside obor projects) — ask the user before running it.
The plugin's startup hook restarts obor panes with `obor restart` after a herdr restart (log `~/.obor/restore.log`;
a worker with no recorded session, such as a Codex lane, starts fresh). For that herdr must not resume agents itself:
unless herdr's config.toml has `resume_agents_on_restore = false` under `[session]`, setup prints a `hint:` line
naming it, linked or not. obor never edits herdr's config: it is user-global, so every herdr pane obor does not
restore (other projects, non-obor agents) comes back as a shell. Tell the user and let them set it, last: after the
labels and the link (otherwise a herdr restart leaves panes without identity, or runs the wrong kit).

Then write the answers into `.obor/config.json` with jq (keep `--indent 2`), for example:
```
jq --indent 2 '.commands.check = {cmd: "make check", timeout: 900} | .roles.integrator.type = "opus"' \
  .obor/config.json > .obor/config.json.tmp && mv .obor/config.json.tmp .obor/config.json
```
Remove example lane types the user doesn't want; per-machine values go to `.obor/local.json` (see
`templates/local.example.json`). Check: `obor config validate` → `OK`.

## 3. Lanes
One per lane the user asked for:
```
obor setup lane O-01 opus
```
Adds `lanes.O-01` to the config, `git worktree add --detach <lanes_dir>/O-01 <current branch>`, symlinks
`lane_links`, runs `commands.lane_setup` there. A `FAIL lane_setup …` shows the command's output: fix with the user.

## 4. Panes and agents
Spawning panes can be blocked by the auto-mode classifier: ask the user to approve, or to run the command.
```
obor setup pane O-01 --new-tab workers        # first lane: a new tab
obor setup pane O-02 --tab <tab-id>           # next lanes: split that tab (herdr tab list)
obor setup role integrator --tab <tab-id>     # lead | integrator | reviewer
obor setup role reviewer --new-tab review     # reviewer gets <lanes_dir>/<roles.reviewer.worktree>
obor setup role triage --tab <tab-id>         # optional bug inbox: <prefix>-triage in the main checkout, docs/BUGS.md
```
Without `--tab` / `--new-tab` the current pane's tab is split; `--pane <id>` uses an existing shell pane. Panes get
`--cwd` the lane and `OBOR_ROLE` / `OBOR_LANE`; agents are named `<prefix>-<lane lower>` (`<prefix>-int`,
`<prefix>-rev`, `<prefix>-lead`); Claude agents except leads start with `--settings '{"autoMemoryEnabled":false,"remoteControlAtStartup":false}'`
(a lead keeps Remote Control and auto memory; lane-type settings overlay those keys).
- `FAIL tab … is full` → use `--new-tab` (max `limits.max_panes_per_tab` panes per tab).
- `FAIL lane type … is at its max` → ask the user: retire an agent or raise `max`.
- `FAIL agent not ready: … Trust this folder? …` → ask the user to answer the dialog in that pane (Enter = trust,
  once per repo). `… updated itself and exited …` → rerun the `herdr agent start` the hint prints.
Rerunning `obor setup pane` for a lane whose agent runs is a no-op (`OK … already running`).
`obor setup agents` (once per user; ask first, it is user-global; its old name `opencode-plugin` still works)
installs what each agent kind needs outside the project: for opencode lanes (`kind: opencode`, no Claude Code hooks)
it links obor's opencode plugin into `~/.config/opencode/plugins/` (`$XDG_CONFIG_HOME` respected), which runs obor's
file guard and session context there (there is no Stop hook, so nothing nudges a missing `task_done`), and writes
the `mcp.servers.obor` entry of `opencode.json`; restart the opencode agents after it. Claude and Codex need nothing
(the plugin manifest, launch args). `--remove` undoes it.
Optional: offer a bug-triage agent (`roles.triage.type` = a lane type, then `obor setup role triage --new-tab triage`): the person reports bugs to it, it keeps `docs/BUGS.md` and hands fixes to the project's lead (`agents/triage.md`).

## 5. Fill "This project" with the user
Each project role doc in `docs/agents/` holds only `<!-- fill in: … -->` prompts for this project. Go through
them with the user one doc at a time (lead, integrator, worker, reviewer first): design/contract docs, the check
commands, lane locations, model palette and limits, who integrates. Replace each prompt with short facts. Add the
rules block from `templates/AGENTS-section.md` to the project's `AGENTS.md` / `CLAUDE.md` (ask where).
Update `docs/agents/roster.md` with the agents now running.

## 6. Finish
- `obor config validate` → `OK`; `obor status` lists every lane as `free`.
- Show `git status`; with the user's OK commit `.obor/config.json .gitignore docs/agents/ AGENTS.md`
  by explicit path (never `git commit -a`; `.obor/local.json` stays uncommitted).
- Tell the user what runs where (tab → agents) and the next step: write the first specs (`docs/agents/task-spec.md`)
  and `obor dispatch` the first task (or a waiting one, `--after`), then `/obor:lead`.
