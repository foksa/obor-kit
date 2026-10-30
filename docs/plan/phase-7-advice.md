# Phase 7 advice (advisor hk-fable, 2026-10-30)

Read: `docs/plan/phase-7-draft.md`, `docs/design.md` (Setup, Stable channel, Shipping the binary, herdr integration),
`docs/decisions.md` (S2), `docs/module-map.md`, `skills/setup/SKILL.md`, `internal/setup/`, `bin/herd`,
`cmd/herd/serve.go`, `cmd/herd/lead.go`, `internal/app/dispatch.go`, the owner's `~/.claude/plugins/` state.

## Recommendation in one paragraph
One kit checkout per machine at a release tag (`~/.herd/kit`, the `herd-kit-stable` worktree made official), a
symlink `herd` on PATH, and a `bin/release-url` that resolves: that is the whole install step. `herd init` is a new
Go command that chains what `internal/setup` already does (init, lane, pane, role, herdr-plugin) around one small
detection table and two `claude plugin` calls, then registers serve with launchd. One launchd agent **per project**,
label `herd.serve.<name>`, pointing at `~/.herd/kit/bin/herd serve`. No kit pin this phase: delete the half-pin that
exists instead. The release location is the one decision that blocks everything: **a fresh user's plugin copy has no
binary** (below), so until `bin/release-url` is real, no one but the owner can run a single hook.

## Fact that reshapes the plan
`claude plugin install` from a directory marketplace **copies the kit** into
`~/.claude/plugins/cache/<marketplace>/herd/<plugin.json version>/` (owner's machine: `herd-kit-stable/herd/0.15.0/`
holds a full copy, `dist/herd` included only because the owner had built it before installing). `${CLAUDE_PLUGIN_ROOT}`
in `hooks/hooks.json` and `plugin.json` `mcpServers` is that copy. On a new machine the copy has no `dist/`, so
`bin/herd` goes to `fetch` (`bin/herd:91`) and `bin/release-url` is `https://example.invalid/…`: every hook fails,
PreToolUse denies every Bash/Edit (`bin/herd:159`). Two consequences:
- P7-1 is not "nice to have": it is the gate for the axis-maps test and for any other user.
- `.claude-plugin/plugin.json` `version` (dev: `0.1.0`, stable: `0.15.0`) keys the cache directory. `make release`
  must set it to `VERSION`, or an upgrade is served from the old copy. One `test` line in the Makefile.

## 1. Simplest shape of install and `herd init`

**Install** (`install.sh` at the repo root, run with `curl … | sh`, bash 3.2):
1. `git clone` (or `fetch` + `checkout --detach v<latest tag>`) into `~/.herd/kit`. This *is* the stable channel of
   `docs/design.md` "Stable channel", one per machine instead of `~/Work/herd-kit-stable`.
2. `ln -sf ~/.herd/kit/bin/herd ~/.local/bin/herd`; print the PATH line when `~/.local/bin` is not on PATH.
3. `herd version` once: fetches `~/.herd/bin/herd-<ver>-<os>-<arch>` and verifies the checksum (`bin/herd:91-114`),
   so the install fails loudly if the release is missing.
Upgrade = run the script again (it moves the checkout to the new tag) and `herd serve restart` in each project; no
`herd upgrade` command this phase. No brew formula (a tap is a second repo to keep in step; later, if ever).

**`herd init`** (`cmd/herd/init.go` wiring, logic in `internal/setup`; `herd setup init` is absorbed):
1. `initCmd` as today (`internal/setup/init.go:15`), name = folder, prefix = `defaultPrefix` made unique against
   `s.H.Agents()` names (the `Herdr` interface already has it, `setup.go:55`). herdr unreachable → `FAIL` first: it is
   a hard dependency, nothing later works without it.
2. Detect commands, one function, in order: `Makefile` with a `check:` target → `make check`; `go.mod` →
   `go test ./...`; `package.json` → lockfile picks bun/pnpm/npm, `check` else `test` script, `lane_setup` = the
   install with the lockfile, `lane_ready` = `test -d node_modules`. Nothing → `commands` stay null, the summary says
   so. **Drop `lane_links` detection** (size heuristics over gitignored dirs for a guess that is wrong as often as
   right); the summary names the key.
3. Lanes from the CLIs on PATH: `claude` → `O-01 opus`, `S-01 sonnet` (the example config's types); `codex` on PATH →
   `roles.reviewer.type = codex`, else opus; integrator = sonnet. `herd setup lane` (`lane.go:17`) twice.
4. Panes: `setup pane O-01 --new-tab workers`, `setup pane S-01 --tab <that>`, `setup role integrator`, `setup role
   reviewer --new-tab review`. Needed: a dispatch into a lane with no pane is a needs-you, not a start
   (`internal/app/dispatch.go:694`). The "Trust this folder?" dialog is the one interaction init cannot remove; it
   must say which panes wait for Enter (the `FAIL agent not ready` path already exists).
5. Plugin: `claude plugin marketplace add ~/.herd/kit` then `claude plugin install herd@herd-kit --scope project`
   (`run.Group` with timeouts, exit code only). Marketplace name = the kit's `marketplace.json` `name`, fixed
   `herd-kit`; the owner's dev machine keeps its `herd-kit-stable` alias as today and nothing in herd depends on it
   once `warnPlugin` (`setup.go:170`) is gone.
6. `s.events(ctx, true)` (`plugin.go:51`): link herd.events without asking (the owner accepted no questions), the
   `resume_agents_on_restore` hint stays a printed line (herd never edits herdr's config).
7. `herd serve install` (§2) and the summary: what it picked, the file to change (`.herd/config.json`), the panes
   waiting on a trust dialog, the herdr config line, then "next: `herd lead`".
Every step is idempotent today except the plugin calls, so `herd init` rerun after a fix is the repair path and
`/herd:setup` shrinks to "change something".

**Delete** (P7-5, after P7-2 merges):
- `skills/setup/SKILL.md` §0 (plugin by hand), §1 (the six questions), §2 (jq editing walk-through, the herdr-plugin
  prose that the code no longer matches: `events` never asks at a terminal), §5 (filling "This project" with the user;
  the stubs are still written and the lead fills them while working), §6. Keep §3–§4 as "add a lane / pane / role",
  one screen.
- `herd setup init` (its flags move to `herd init [--name] [--prefix]`); `warnPlugin`; the `WARN herd plugin not
  enabled` test cases in `setup_test.go`.
- `docs/design.md` "Setup" and "Stable channel" paragraphs (lead-owned): stable = `~/.herd/kit`.
- `bin/release-url` placeholder and the "placeholders pending" line of `RELEASING.md`.

## 2. launchd: one per project
One launchd agent per project, not one per user. S2 made the project the unit (flock `.herd/state/serve.flock`,
socket `.herd/state/serve.sock`, `herd send` dials the target project, `cmd/herd/serve.go:58-75`); a per-user service
would be a supervisor with a project registry that re-implements what launchd does, and one project's restart or
upgrade would take the others down (the 10-30 incident again, by our own hand).
- Plist `~/Library/LaunchAgents/herd.serve.<name>.plist`: `ProgramArguments` `[~/.herd/kit/bin/herd, serve]`,
  `WorkingDirectory` = root, `RunAtLoad`, `KeepAlive`, stdout/err → `.herd/state/logs/serve.log` (where serve's logs
  already live), `EnvironmentVariables.PATH` = the caller's PATH at install (launchd's own PATH has no `claude`,
  `codex`, `herdr`, `jq`; `HERDR_SOCKET_PATH` only if set, `internal/herdr/socket.go:117`).
- `<name>` = config `name`, already treated as machine-unique by the merge marker (`docs/design.md` "Machine-wide
  check slots"); install `FAIL`s when a plist of that label names another `WorkingDirectory`.
- Finding it: `herd status` derives the label from the config and asks `launchctl print gui/<uid>/<label>`: the serve
  line becomes `serve: launchd pid N since …` / `serve: pane pid N` (flock holder, `app.FlockFile`) / `serve: down
  (herd serve install)`. `herd serve install | uninstall | restart` (`launchctl bootstrap/bootout/kickstart -k`); no
  `herd upgrade`. A `herd serve` typed in a pane still works for development: the flock already makes the two
  mutually exclusive (`serve.go:64`).
- Package: `internal/service` (adapter layer in `docs/module-map.md`), darwin only; Linux prints "run `herd serve`
  yourself (systemd --user later)" and `herd init` still succeeds.
- The must-verify in P7-3: serve under launchd outlives a herdr restart only if it tolerates herdr down (start:
  `serve.go:77-88` only prints; the stream: `refreshed`, `serve.go:204`). If `app.Serve` returns on a dead herdr
  socket, `KeepAlive` restarts it every 10 s (launchd's throttle), which works but logs a line per retry; better a
  reconnect wait inside serve. The spec names this as its first test (stub herdr dies and comes back).

## 3. Per-project kit pin: not this phase, and delete the half of it that exists
- With one kit per machine at a tag, a project's CLI and serve run that tag; its hooks and MCP run the plugin copy in
  Claude's cache at the version last installed for that project (`installed_plugins.json` records scope `project`).
  That is already a per-project pin of the plugin half, made by Claude Code; herd pays nothing for it. The skew it
  allows (CLI at 0.16, hooks at 0.15) is the known rollout lesson, and the fix is `claude plugin update herd` per
  project, one summary line in `herd init`/`herd status` when `~/.herd/kit/VERSION` ≠ the installed plugin version.
- A real pin (a kit checkout per project) costs N checkouts, N binaries and cross-version `herd send`; nobody needs
  it until a second adopter runs a different version. Leave the BACKLOG line.
- `herd.version` (`internal/config/config.go:65`, `bin/herd:70-77` "pinned", `HERD_VERSION_SOURCE`) pins the binary
  alone, not hooks or templates, and no project sets it (axis-alies: null). Delete it in P7-4 (simple) instead of
  extending it: a kit at a tag has one binary version.

## 4. Task split
| # | Task | Difficulty | Depends on |
|---|---|---|---|
| P7-1 | Release location: `bin/release-url` real, `make release` uploads (`gh release create`) and asserts `plugin.json` version = `VERSION`; `install.sh` (clone to `~/.herd/kit`, symlink, `herd version`); `RELEASING.md` steps 3 and 5 | simple once decided | owner decision |
| P7-2 | `herd init`: §1 steps 1–7, summary, idempotent rerun; `setup_test.go` with the stub herdr and a fake `claude` on PATH | hard | P7-3 for step 7 (stub it, merge first) |
| P7-3 | `internal/service` + `herd serve install/uninstall/restart`, the status line, serve tolerating herdr down | medium; the herdr-down part is the hard half | — |
| P7-4 | Delete `herd.version` (config, shim, docs) | simple | — |
| P7-5 | Delete the setup skill's flow, `herd setup init`, `warnPlugin`; design doc Setup/Stable paragraphs (lead) | simple | P7-2 merged |
Order: P7-1 (owner) and P7-3, P7-4 in parallel; P7-2 right after; P7-5 last. The axis-maps run needs a tag that
includes P7-1 to P7-3 and `~/.herd/kit` on the owner's machine (not `~/Work/herd-kit-stable`): a run against the dev
checkout would not test the copy-without-binary path.

**Owner decides:** (a) release location: a public GitHub repo for the kit plus GitHub Releases for the four binaries
(publishing the repo publishes `BACKLOG.md`, `docs/decisions.md` and the reviews with it); (b) `~/.herd/kit` and
`~/.local/bin/herd` as the install paths (no sudo); (c) the marketplace name `herd-kit` fixed, so the dev machine's
two marketplaces are the owner's concern only; (d) one launchd agent per project, label `herd.serve.<name>`;
(e) `herd.version` deleted.

## Risks not in the draft
- `claude plugin marketplace add` with a name already registered to another path (the owner's machine) exits non-zero;
  init treats "already added" as OK only when `claude plugin marketplace list` names the same path, else FAIL with the
  two paths.
- `~/.local/bin` is not on PATH in a fresh macOS shell; the install script prints the one line, it does not edit
  rc files.
- The plist's frozen PATH goes stale when the user installs a new agent CLI elsewhere; `herd serve install` is
  idempotent and rewrites it, so the hint on a `codex: not found` in serve's log is "herd serve install".

## 5. P7-0: the rename herd → obor (addendum, lead's question 2026-10-30)
Counts: `\bherd\b` 4349 hits (cmd 809, internal 2079, docs 997, skills 142, templates 98, opencode 75, bin 42);
`herdr` 1439 hits and 20 `HERDR_*` vars that must **not** change (`herdr`, `herdr-plugin/`, `internal/herdr`).
`module herd` and every `herd/internal/…` import: internal, zero risk. The risk is not the count, it is the eleven
places where the name is a contract with something that outlives the release:

| Surface | Today | Outlives the release in |
|---|---|---|
| project dir | `.herd/` (12 `Join(root, ".herd")` sites, `guard.go:427`, `bin/herd` `in_project`, `githooks/pre-commit:12`) | every project's tree and gitignored state |
| store | `.herd/state/herd.db` (`store.go:135`) | the same |
| env | `HERD_ROLE/AGENT/LANE/STATE_HOME` (`agents.go:171`), `HERD_MERGE`, `HERD_PLUGIN_ROOT`, `HERD_NO_SELF_HOST` | live panes keep the env they were split with |
| machine dir | `~/.herd/` (`project.go:169`: sids, inboxes, bin, restore.log) | live agents' sids and inbox records |
| herdr plugin id | `herd.events` (user-global link) | herdr's registry |
| Claude plugin | `herd@herd-kit[-stable]` in each project's `.claude/settings.json`; the cache copy; `/herd:lead` | each project, Claude's cache |
| MCP server name | `herd` (`mcp.go:181`; Codex `mcp_servers.herd`, OpenCode `mcp.servers.herd`, tool names `mcp__herd__…`) | OpenCode's user config; role texts |
| shim path | `bin/herd` in `hooks/hooks.json`, `plugin.json`, the herdr plugin toml, `core.hooksPath` | old plugin copies, herdr's resolved path, each repo's git config |
| line tags | `[herd job <id>]` (`prompt.go:103`), `[herd]`, `[herd answer]`; the guard's captured payloads | lines recorded before the upgrade, delivered after |
| binary names | `~/.herd/bin/herd-<ver>-<os>-<arch>`, `dist/herd` | the user's machine |
| agent labels | `<prefix>-lead`, `hk-…`: carry no "herd" | nothing: **do not rename labels or lanes** |

**One mechanical merge, not a split.** A half-renamed tree (two names in one binary) is the worst state, and a
rename across 307 files conflicts with every open lane. So: P7-0a is one task, one lane, merged when no other lane
holds work (the lead pauses dispatch for it). Its diff is produced by a committed `scripts/rename.sh` (sed over
`\bherd\b` → `obor`, `\bHERD_` → `OBOR_`, `herd-kit` → `obor-kit`, with an exclusion list) plus the hand-written
compat below; the reviewer reruns the script on the parent commit and diffs, so the mechanical part reviews itself.
Exclusions: `herdr` and `HERDR_*` (word boundaries do most of it; `herd.events`, `herd-kit`, `herd.db` need their own
lines), `herdr-plugin/` as a directory name, history docs (`docs/decisions.md`, `docs/reviews/`, `docs/UPGRADE-*.md`,
`docs/research/`, `docs/plan/phase-5*`, `phase-7-*`): they describe the past and get one new decisions line instead.
The herd-kit repo is itself a herd project (`.herd/`, prefix `hk`): the same commit moves its own `.herd/` → `.obor/`,
and the lead's first `obor migrate` run (below) on herd-kit, right after the merge, is the migration's test before
axis-alies.

**What stays readable for one release (v0.16 ships it, v0.17 deletes it; the UPGRADE guide names the list):**
1. `bin/herd`: a three-line shim, `exec "$(dirname "$0")/obor" "$@"`. Old plugin copies in Claude's cache, herdr's
   resolved `herd.events` path and old `core.hooksPath` keep working until the project reinstalls.
2. `HERD_ROLE/AGENT/LANE/STATE_HOME` read as a fallback after `OBOR_*`, in one helper (`project.Env` lookup; the
   reads are 14 sites, 4 vars matter). Live panes keep their env until recreated; nothing else needs it.
3. The guard recognizes both `[herd job` and `[obor job` tags (one alternation); the captured `herd`-era payloads in
   `internal/guard/testdata` stay as the test that old tags still pass, one new `obor` capture is added.
4. A project with `.herd/config.json` and no `.obor/`: every command and the PreToolUse hook **fail closed** with
   `run obor migrate` (the one check in the project finder). No read fallback for `.herd/`: twelve dual paths in hook
   code for a one-time `mv` is the wrong trade; failing closed is what keeps an unmigrated lane from running unguarded.
Not kept: `~/.herd/` (moved by migrate), `herd.events` (unlinked by migrate; two linked plugins would restore panes
twice), the MCP server name (the agent restarts fresh anyway), `herd.db` (renamed with the state dir).

**`obor migrate` (P7-0b, medium; deleted in v0.17 with the fallbacks).** One idempotent Go command, `OK`/`FAIL` per
step, because the owner runs it three times (herd-kit, axis-alies, later anything on stable) and the fail-closed hook
points at it. Steps, project first, then user-global:
1. Refuse while `.obor/state/serve.flock` or `.herd/state/serve.flock` is held (serve must be stopped) or a herdr
   agent of this project runs (`Agents()`; the panes may stay).
2. `git mv .herd/config.json .obor/config.json` (and `local.json`, `drafts/`), `mv .herd/state .obor/state`, rename
   `herd.db*` → `obor.db*`, rewrite the `.gitignore` lines; `git mv`, never `rm`.
3. `claude plugin uninstall herd@<marketplace> --scope project` (read the name from `.claude/settings.json`), then the
   two install calls of `obor init` step 5.
4. `obor setup githooks` must accept re-pointing a `core.hooksPath` that ends in `/githooks` of a herd or obor kit
   (today it refuses anything not its own, `plugin.go:38`).
5. User-global, once: `mv ~/.herd ~/.obor`, unlink `herd.events` and link `obor.events`, `obor setup agents` when
   an OpenCode entry exists.
6. Print the files of the project's own prose that still say `herd` (`AGENTS.md` rules block, `docs/agents/*.md`
   pointer lines, `BACKLOG.md`): listed, never edited.
7. Print the finish: start serve (`obor serve` or `obor serve install`), then `obor restart <agent> --fresh` for every
   labelled pane (new env, new MCP tool names, new hooks); a lead loses its context, so do it at a handoff.

**axis-alies (live):** at a handoff with no lane working: `herd pause`, let the merges finish, stop serve, quit the
agents. Install obor (P7-1: `~/.obor/kit`, `obor` on PATH), remove the `/opt/homebrew/bin/herd` symlink and the
`herd-kit-stable` marketplace (`claude plugin marketplace remove`), `obor migrate`, start serve, restart every agent
fresh, `obor status` + `obor whoami` in one worker pane + `obor setup mcp` as the check. Lanes, branches
(`task/T-xx`), labels (`aa-…`), the store's history and the name `axis-alies` do not change. Expect one afternoon;
the install-path move (`~/Work/herd-kit-stable` → `~/.obor/kit`) rides on the same migration, so it happens once.

**axis-maps (new):** nothing to migrate if it waits for the release with P7-0 and P7-2: `obor init`. Setting it up
with herd first and migrating a week later is the worse path. Before that run, the owner's machine drops the old
`herd-kit`/`herd-kit-stable` marketplaces so the fixed name `obor-kit` is the only one.

**Release shape:** v0.16.0 = P7-0a + P7-0b + P7-1 (rename, migrate, install at `~/.obor/kit`): axis-alies moves once.
v0.17.0 = P7-2 + P7-3 + P7-5 (init, launchd, deletions) and drops the four fallbacks: axis-maps starts on it.
Owner decides the names: command and plugin `obor`, marketplace and repo `obor-kit`, dir `.obor/`, env `OBOR_*`,
home `~/.obor`, herdr plugin `obor.events`, tags `[obor …]`; prefixes and labels stay.
