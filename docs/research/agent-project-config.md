# Agent project config: what each CLI reads from a lane's cwd, and what beats it

Research for owner decision 11-08 ("agent start params: native files first", docs/decisions.md). Date 2026-11-08.
Versions on this machine: **claude 2.1.319**, **codex-cli 0.166.0**, **opencode v2.2.0** (`--version`).
A lane is a git worktree of the repo, so only committed files reach it; gitignored/local files do not.

## Answer first

1. **All three CLIs read project files from the cwd, and all three can carry settings, MCP servers, hooks and
   plugins that way.** Claude: `.claude/settings.json` (+ `.mcp.json`). Codex: `.codex/config.toml` (+ `.codex/hooks.json`),
   **only when the project is trusted**. OpenCode: `opencode.json[c]` / `.opencode/opencode.json[c]` (+ `.opencode/plugins/`).
2. **What obor passes today beats the project file in most cases, so a project cannot override it natively**:
   - Claude: `--settings` sits **above** project and local settings (below only managed); `--model`, `--effort` and
     `--permission-mode` flags beat the files; `--strict-mcp-config` hides `.mcp.json`, user and plugin servers.
   - Codex: `-c key=value` is the top layer, above `.codex/config.toml`. The `-m/-s/-a` flags are CLI flags and rank the
     same (docs; not probed, see Unknown).
   - OpenCode: the opposite. `OPENCODE_CONFIG` is **below** the project file, so the project wins every conflict.
     obor's `--auto` is the one flag; a project `deny` permission still beats it.
3. **A "default the project may override" has three forms, per kind**: (a) pass nothing and let the CLI's own default or
   the project file decide (works for Claude `--effort`/`--model`, Codex `-m`/effort, OpenCode model; obor already does
   this for an effort-less ref); (b) read the lane's committed project file and pass the key only when it does not set it;
   (c) put the default in a layer **below** the project (OpenCode: already so; Codex: a `-p` profile file; Claude: no such
   layer exists except the owner's user settings).
4. **Hazards found while probing** (details below): a project OpenCode `mcp.servers.obor` entry silently replaces obor's
   command; a project Claude `disableAllHooks: true` switches off the obor plugin's guard hooks; Codex silently skips the
   whole `.codex/` layer of an untrusted project (config, hooks, rules); under `--strict-mcp-config` a project's own
   `.mcp.json` servers never load.

## 1. Claude Code 2.1.319

### Project files read from cwd
| File | What | Source |
| --- | --- | --- |
| `.claude/settings.json` | shared project settings: permissions, hooks, `enabledPlugins`, `env`, `model`, `effortLevel`, `enabledMcpjsonServers`, ... Read from the session's primary working directory. | settings.md "Settings files", L28, L94 |
| `.claude/settings.local.json` | project-local; **gitignored**, so absent in a worktree (docs: saved approvals go to the *main checkout's* root; whether a worktree session *reads* the main checkout's local file is not stated, not probed) | settings.md L50, L85 |
| `.mcp.json` | project MCP servers; need approval (interactive prompt; `enabledMcpjsonServers` / `enableAllProjectMcpServers` in settings approve them, **ignored in an untrusted folder**) | mcp.md L260-270, L800-806 |
| plugin MCP / hooks / skills | from plugins enabled in any settings scope, incl. `enabledPlugins` in the committed `.claude/settings.json` (obor-kit's own file does exactly this: `.claude/settings.json` enables `obor@obor-kit-dev`) | settings-reference `enabledPlugins` |
| `CLAUDE.md`, `.claude/skills|agents|commands` | not in scope (context, not start config) | |

Keys per area: settings `model`, `effortLevel`, `permissions.*` (`defaultMode` `auto`/`bypassPermissions` **do not take
effect from project or local files**, settings-reference L1140-1146), `hooks` (merge across files and with plugin hooks,
hooks.md L266, plugins-reference L638), `enabledPlugins` (any file), `remoteControlAtStartup` and `autoMemoryEnabled`
(scope "Any file").

### Precedence (settings.md "Settings precedence", L208-224)
managed > **command line (`--settings`, flags)** > project local > shared project > user. Lists merge across files
(`permissions.allow`, hooks); scalars take the highest level. `--settings` "overrides the same keys in your
settings.json files... Keys you omit keep their file-based values" (cli-reference L130), so obor's `--settings` blocks only
its two keys; everything else in the project file still applies.
Per-key flags: `--model` > `ANTHROPIC_MODEL` > file `model`; `--effort` > file `effortLevel`/`modelSettings`, and
`CLAUDE_CODE_EFFORT_LEVEL` beats both (settings-reference `model` L531, `effortLevel` L391; env-vars L109);
`--permission-mode` > file `defaultMode`.

MCP (mcp.md L822-833): same server name in several places is connected once from the highest source, **whole entry, no
field merge**: local > project (`.mcp.json`) > user > plugin > claude.ai connectors. **`--strict-mcp-config`: "Only use
MCP servers from `--mcp-config`, ignoring all other MCP configurations"** (cli-reference L131), so a strict start loads
no `.mcp.json`, user or plugin server (obor's code states the plugin manifest's server is hidden too: claude.go
`StrictFlag`, T-445). Where `--mcp-config` ranks against a same-named `.mcp.json` entry in a non-strict start is not
stated in the docs (Unknown).

### obor's Claude start vs the project (claude.go `Start`, models.go `LaunchArgs`)
| obor passes | Beats project file? | Class | Native project override today | How obor could make it a default |
| --- | --- | --- | --- | --- |
| prompt after `--`; `--session-id <uuid>` / `--resume <sid>` | n/a (not config) | **needed** | none | n/a |
| `--mcp-config {mcpServers.obor}` (kit path, pinned) | adds a server; a project/user entry named `obor` is ignored under strict | **needed** (obor's tools) | none wanted | n/a |
| `--strict-mcp-config` (lane agents, T-770) | yes: hides `.mcp.json`, user, plugin servers; no file can undo it | **policy** | none (only by removing the flag) | drop the flag for projects that commit a `.mcp.json`/`enabledMcpjsonServers`; then also decide who answers the `.mcp.json` approval prompt (an unattended lane would sit on "Pending approval" unless settings pre-approve it, and approvals in the committed file only count in a trusted folder). Whether the plugin's `obor` and `--mcp-config`'s `obor` double-register without strict: not probed |
| `--settings {"remoteControlAtStartup":false,"autoMemoryEnabled":false}` (non-lead) | **yes**: `--settings` > project/local/user; only managed beats it | **policy** | none; a project file cannot flip these for a worker | (b) read worktree `.claude/settings.json` and skip a key it sets; or (a) not pass, and commit the same two keys in `.claude/settings.json` (all roles) with the lead overriding in its own `.claude/settings.local.json` or user settings (local > shared). `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` is an env alternative for auto memory but it also beats any file in both directions; no flag/env turns Remote Control off (settings-reference L5034), so the key is the only carrier |
| `--permission-mode auto` (KindBaseArgs) | yes: flag > `defaultMode`; and `auto` cannot come from project/local files at all | **policy** (unattended lanes) | none: a project file cannot select `auto`; `acceptEdits`/`plan`/`default` can be set but lose to the flag | only user settings can carry `auto`; so obor's flag stays the only project-reachable way, overridable via obor's `kinds.claude.args`, not natively |
| `--model <id>` from the model table | yes (`--model` > `model` key) | **policy** (obor routes models by difficulty/role) | none | model ref with empty id starts the kind without `--model` (already supported, models.go "Model "" starts the kind without a model"); the project's `model` then applies |
| `--effort <e>` only for a ref `model:effort` | yes when given | **policy**, but absent for effort-less refs | project `effortLevel` applies when no flag | already a default for effort-less refs |
| plugin install (user scope) providing obor's hooks/guards | project `disableAllHooks: true` disables user, project, local **and plugin hooks** (settings-reference L3613) | **needed** | n/a | a hazard: obor's guard hooks live in the plugin; only managed settings are immune |

## 2. Codex 0.166.0

### Project files and trust
- **Yes, a project `.codex/config.toml` is read.** Docs (config-basic "Configuration precedence"; config-advanced
  "Project config files"): Codex walks from the project root (a dir with `.git`; `project_root_markers`) down to the cwd
  and loads every `.codex/config.toml`, closest wins; also `<repo>/.codex/hooks.json`, project rules.
- **Only for a trusted project.** Untrusted: "Codex skips project-scoped `.codex/` layers, including project-local config,
  hooks, and rules". Trust is a user-config entry `[projects."<path>"] trust_level = "trusted"` (this machine's
  `~/.codex/config.toml` has one for `/Users/djordjeradakovic/Work/obor-kit`, line 184).
- **Probe (scratch `CODEX_HOME`, fake repo, app-server `config/read` with `includeLayers`, no model call)**:
  untrusted: project layer present but disabled (`disabledReason: "To load project-local config, hooks, and exec policies,
  add <path> as a trusted project in <home>/config.toml"`), `model`/`mcp_servers` empty. Trusted: `model = "proj-model"` and
  both project MCP servers appear.
- **A worktree uses the main repo's trust.** Probe with a worktree-shaped layout (`.git` file → `gitdir: …/worktrees/wt`,
  `commondir`): only the main repo path trusted, run in the worktree → project layer loaded, `.codex/config.toml` of the
  worktree applied. Explicit `trust_level = "untrusted"` on the worktree path → layer disabled. (Real `git worktree add`
  could not be run: the researcher guard denies git writes; the layout is what git writes.)
- Keys a project file **cannot** set (ignored with a startup warning): `openai_base_url`, `chatgpt_base_url`,
  `apps_mcp_product_sku`, `model_provider`, `model_providers`, `notify`, `profile`, `profiles`,
  `experimental_realtime_ws_base_url`, `otel` (config-advanced). Everything obor cares about (`model`,
  `model_reasoning_effort`, `sandbox_mode`, `approval_policy`, `mcp_servers.*`, `check_for_update_on_startup`, hooks,
  plugins) is allowed.
- Project hooks additionally need hash trust review (a changed/new non-managed hook is skipped until reviewed;
  `--dangerously-bypass-hook-trust`), docs/research/agent-cli-and-herdr-capabilities.md §3.3 (hooks.md L63-79).

### Precedence (config-basic, highest first)
CLI flags and `-c` > project `.codex/config.toml` (root→cwd, closest wins) > **profile** (`-p name` →
`$CODEX_HOME/name.config.toml`) > user `~/.codex/config.toml` > cloud-managed > `/etc/codex/config.toml` > built-ins.
The doc says it outright: a profile file "is a layer above your base user config and below project and CLI config".
Probes (scratch home; `codex mcp list --json` and app-server `config/read`):
- `-c` overrides the project **per key**, tables deep-merged: project `[mcp_servers.obor] command=/proj/obor, args=[mcp-proj],
  startup_timeout_sec=77`; `-c mcp_servers.obor.command=/cli/obor` → command `/cli/obor`, args stay `mcp-proj`, timeout 77 survives.
  With `-c mcp_servers.obor.env={OBOR_NO_SELF_HOST="1"}` and a project `env` table, the result had **both** keys. A project-only
  server (`projonly`) is added alongside obor's; Codex has no "strict" mode.
- `-c check_for_update_on_startup=false` beats a project `true`; `-c sandbox_mode="workspace-write"` beats a project
  `read-only`; `-c model=…` beats a project `model`. Without `-c`, the project values apply.
- A `-p obor` profile (`model="profile-model"`, `mcp_servers.obor.command=/profile/obor`) loses to the project file
  (`command` stayed `/proj/obor`) and a `-c` beats both. `--profile` is accepted by `codex` and `codex mcp` but not by
  `codex app-server`, and `-p` needs the file in `$CODEX_HOME` (user dir).

### obor's Codex start vs the project (codex.go `Start`, models.go `KindBaseArgs`)
| obor passes | Beats project file? | Class | Native project override today | How obor could make it a default |
| --- | --- | --- | --- | --- |
| `resume <sid>`; prompt | n/a | **needed** | none | n/a |
| `-c mcp_servers.obor.command/args/env_vars/env`, `default_tools_approval_mode="approve"` | yes, per key (probe) | **needed** (obor's tools; approve because `-a never` fails MCP calls otherwise, T-390) | project can add other servers and extra keys of `obor` (timeouts, extra env); cannot change the keys obor sets | none wanted |
| `--no-daemon` | a flag | **needed** (shared daemon would give MCP/hooks the daemon's env and cwd, T-810) | none | n/a |
| `--add-dir <d>` (write dirs of reviewer/deep dive, T-853) | flag | **needed** for a sandboxed kind | project `sandbox_workspace_write.writable_roots` merges in config (not probed) | n/a |
| `-s workspace-write -a never` (KindBaseArgs) | flags beat project (docs; `-c` equivalent probed) | `-a never` **needed** unattended; sandbox mode **policy** | a project's `sandbox_mode`/`approval_policy` loses | (a) drop `-s` and let project/user decide (risk: a lane then runs the user's mode, maybe `read-only`/prompting); (b) read `.codex/config.toml` and skip the key; (c) profile below project, but needs a file in `$CODEX_HOME` |
| `-m <id>` | flag | **policy** | project `model` loses | empty-id model ref → no `-m`; project/user `model` applies |
| `-c model_reasoning_effort=<e>` for `model:effort` | yes | **policy**, absent for effort-less refs | project value applies when absent | already default for effort-less refs |
| `-c check_for_update_on_startup=false` (T-570, skipped when the type's args set it) | yes (probe) | **policy** (an "Update available" menu blocks a fresh start) | none; a project `true` loses | (b) read project file and skip when it sets the key; or (c) profile below project |

## 3. OpenCode v2.2.0

### Project files
Docs (opencode.ai/docs/config; v2): project settings in `opencode.json[c]` or `.opencode/opencode.json[c]`, found by
walking from the cwd up to the filesystem root; direct files merge farthest→closest, then `.opencode/` dirs in the same
order (every `.opencode` config overrides every direct one). `.opencode/plugins/` (project) auto-loads `.ts/.js` and package
dirs (plugins doc). Keys: `model`, `permissions` (rules; legacy `permission` still parses, probe), `mcp.servers`,
`plugins`, `policies`, `snapshots`, ... Higher than the project file: only system/managed config. A repo `opencode.json`
needs no trust step in the docs or the probe (no trust prompt appeared; the CLI was not run interactively).

### OPENCODE_CONFIG vs the project file: **merged, project wins**
Probe (scratch `XDG_*`, `opencode debug config --standalone`, then a throwaway `opencode serve` on port 49733 stopped
afterwards, `/api/mcp`, `/api/plugin`). `debug config` lists sources in this order: global dir (`~/.config/opencode`) →
**`OPENCODE_CONFIG` file** → **project `opencode.json`** (later = higher). With `model` set in both, the effective
source for conflicts is the project's. Concretely:
- `mcp.servers.obor` in both (env file `/env/obor`, project `/proj/obor`): `/api/mcp` tried to spawn `/proj/obor mcp`
  (the **project entry won**); a project-only server `projonly` was added too. So a project can silently shadow obor's
  server. (Whether the same-name entry merges field-wise, e.g. the env file's `environment`, was not probed.)
- `plugins` in both: **both** loaded (`/api/plugin` listed `/env/plugin` and `/proj/plugin`; docs: "plugin arrays... are applied
  from lowest to highest precedence instead of replacing one another"). Both failed to load only because the paths do not exist.
- The env config is **below** the project, so obor's generated config works as "defaults a project may override" by
  construction, except where obor needs to win (the `obor` server name).

### obor's OpenCode start vs the project (opencode.go; models.go `KindBaseArgs`)
| obor passes | Beats project file? | Class | Native project override today | How obor could express it |
| --- | --- | --- | --- | --- |
| `--prompt` | n/a | **needed** | none | n/a |
| `OPENCODE_CONFIG` → `mcp.servers.obor`, `plugins:[<kit>/opencode]` | **no**: project wins on a conflict; plugin arrays concatenate | **needed** (tools, hook transport) | a project can replace `mcp.servers.obor` (breaks obor) or add servers/plugins | to keep obor's server safe obor would need a layer above the project (`OPENCODE_CONFIG_CONTENT`, named in docs/research but not probed here, or a different server name) |
| `--auto` | project `permission(s)` with `deny` still win ("Auto-approve permissions that are not explicitly denied", `opencode --help`) | **policy** (unattended) | yes: a committed deny rule restricts it | already overridable toward stricter |
| `--standalone` | n/a | **needed** (inference): `--help` says "private server instead of the background service"; a shared service would not see this start's env (`OPENCODE_CONFIG`, `OBOR_OPENCODE_MODEL`) | none | n/a |
| `OBOR_OPENCODE_MODEL=<provider/model>[#variant]` read by obor's plugin, which switches each root session's model after the TUI's own pre-submit switch (opencode.go `StartEnv`) | yes when set: the plugin's switch overrides the project `model` | **policy** | none when the type names a model; project/user `model` stands when the type names none | model ref without id → var unset → OpenCode/project model stands (already so, T-697) |

## 4. Table: obor start param → needed or policy → native override (all kinds)
| Param | Kind | Needed / policy | Project can override natively? |
| --- | --- | --- | --- |
| session id / resume, prompt | claude, codex (resume), opencode (`--prompt`) | needed | no |
| obor MCP server (`--mcp-config`; `-c mcp_servers.obor.*`; `OPENCODE_CONFIG`) | all | needed | no (claude/codex: obor's keys win; opencode: project **can** shadow it, a risk) |
| obor plugin / hooks transport | claude (installed plugin), opencode (`plugins`) | needed | claude: `disableAllHooks` in a project file turns it off; opencode: no |
| `--strict-mcp-config` | claude | policy (T-770) | no |
| `--settings {remoteControlAtStartup:false, autoMemoryEnabled:false}` | claude | policy | no (flag layer above project/local) |
| `--permission-mode auto` | claude | policy | no (`auto` not allowed in project files; flag wins) |
| `-s workspace-write -a never` | codex | `-a never` needed unattended; `-s` policy | no (flags beat project) |
| `--no-daemon`, `--add-dir` | codex | needed | no |
| `-c check_for_update_on_startup=false` | codex | policy | no (`-c` beats project); would need profile or skip-the-key |
| `--auto`, `--standalone` | opencode | `--auto` policy, `--standalone` needed | `--auto`: a project deny wins |
| `--model` / `-m` / `OBOR_OPENCODE_MODEL` | all | policy (model routing) | no where passed; yes where the ref has no id |
| `--effort` / `-c model_reasoning_effort` / `#variant` | all | policy | no where passed; yes for effort-less refs |

## 5. Mechanisms for "a default the project may override"
1. **Pass nothing** (flag absent). Works wherever the CLI has its own default or reads the project file: Claude `--model`,
   `--effort`; Codex `-m`, effort; OpenCode model/variant. obor already does this for effort-less refs and `Model ""`.
2. **Read the lane's committed project file and pass the key only if it is unset.** Needed where the only carrier is a
   top layer: Claude `--settings` keys (`.claude/settings.json`), Codex `-c` keys (`.codex/config.toml`, only meaningful
   when the project is trusted). The file is a committed worktree file, so obor can read it from the lane dir
   (or main checkout) before building argv; JSON (Claude) is cheap, TOML (Codex) needs a parser or a narrow key scan.
3. **Put the default in a layer below the project.** OpenCode already does (`OPENCODE_CONFIG`). Codex: a `-p` profile file
   in `$CODEX_HOME` (Codex ≥ 0.134 layout; written by obor, not editing `config.toml`; flag valid for `codex`, not
   `app-server`). Claude: none: plugin `settings.json` honours only `agent`/`subagentStatusLine`
   (plugins-reference L216), so the only layer below project is the owner's `~/.claude/settings.json`.
4. **Commit it as the project's own policy.** For Claude's two settings keys the project could commit the keys in
   `.claude/settings.json` for all roles; the lead overrides in `.claude/settings.local.json` (local > shared) or user
   settings. This loses obor's role split unless the lead's override is set up by `obor init`.

## 6. Unknown / not verified
- Claude: where `--mcp-config` ranks against a same-named `.mcp.json` entry in a **non-strict** start, and whether the plugin
  manifest's `obor` and `--mcp-config`'s `obor` double-register (docs list scopes local/project/user/plugin only). Not
  probed: it needs a Claude session (`claude -p` is denied; an interactive one needs a pane and the trust dialog).
- Claude: whether a worktree session *reads* `.claude/settings.local.json` from the main checkout root (docs state it for
  *writes*, settings.md L85).
- Codex: `-m/-s/-a` beat the project file by docs only. The app-server probe cannot take top-level TUI flags, and the TUI
  needs a pane. `-c` equivalents were probed. `sandbox_workspace_write.writable_roots` merge with `--add-dir` not probed.
- Codex: real `git worktree add` trust inheritance (probed with the layout git writes, not with git).
- OpenCode: whether a project same-name MCP entry merges field-wise with the env config's entry; whether
  `OPENCODE_CONFIG_CONTENT` ranks above the project file (not probed; docs/research/agent-driver-deep-dive.md L399 lists
  the precedence as open). The config API used (`debug config`, `/api/mcp`, `/api/plugin`) shows sources and effective
  servers, not a merged `permissions`/`model` object.
- OpenCode interactive project trust: none seen, TUI not run.

## 7. Sources and probes
- Claude docs fetched 2026-11-08 (`code.claude.com/docs/en/`): settings.md, settings-reference.md, mcp.md, cli-reference.md,
  permissions.md, plugins-reference.md, hooks.md, env-vars.md, worktrees.md. `claude --help`.
- Codex docs (`developers.openai.com/codex/`): config-basic, config-advanced, config-reference, mcp, hooks, plugins, security.
  `codex --help`, `codex mcp --help`, `codex doctor --help`, binary strings (`ConfigToml`, `ProjectConfig`, `trust_level`).
- OpenCode docs (`opencode.ai/docs/`): config, plugins, permissions, mcp-servers. `opencode --help`, `opencode debug config`.
- obor code read: internal/agents/claude/claude.go, codex/codex.go, opencode/opencode.go, internal/agents/agents.go
  (`Start`), internal/config/models.go (`KindBaseArgs`, `LaunchArgs`). Earlier research:
  docs/research/agent-cli-and-herdr-capabilities.md (§3.3 hooks, trust dialogs), agent-driver-deep-dive.md.
- Probes (all in the session scratchpad, never a herdr session, no model call, no `claude -p`/`codex exec`):
  - Codex: `CODEX_HOME=<scratch>/probe/home`, fake repo `<scratch>/probe/repo` (`.git` dir marker), a worktree-shaped
    `<scratch>/probe/wt`; `codex mcp list --json` and `probe_codex.py` (JSON-RPC `initialize` + `config/read`
    `includeLayers:true` over `codex app-server --listen stdio://`, killed afterwards).
  - OpenCode: scratch `XDG_CONFIG_HOME/DATA/STATE/CACHE`, project dir with `opencode.json`, `OPENCODE_CONFIG=env.json`;
    `opencode debug config --standalone`; a throwaway `opencode serve --port 49733` (pid stopped; port checked free).
  - Real files touched: none; `~/.codex/config.toml` was only read (grep for the obor-kit trust entry).
