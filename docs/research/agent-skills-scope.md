# Agent skills scope: keeping the owner's global skills out of obor's agents, and project skills per role

Research for the lead, 2026-11-09. Versions probed: **claude 2.1.319** (the binary auto-updated to 2.1.321 on disk while
the probes ran; the token runs are 2.1.319), **codex-cli 0.167.0**, **opencode v2.2.0**, **herdr 0.9.5**.
Rules applied: "native files first" (docs/decisions.md 2026-11-08 "Agent start params") and "agent-neutral changes first"
(2026-11-07). Nothing in the repo was changed; every probe ran in the session scratchpad (§7).

## Answer first

### Q1: starting an agent without the owner's global plugins, skills and agents

1. **Each CLI can do it natively; the switch differs per kind** (table below). Claude: `--setting-sources project,local`.
   Codex: `-c` keys (`features.plugins`, `features.apps`, `skills.bundled.enabled`, per-name `skills.config`). OpenCode:
   the env var `OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1`. None of them touches auth (live turns completed on all three).
2. **The saving is smaller than the 40k/12k picture suggests.** Measured on the same skill-listing attachment the lead
   read: its text is 22.9 KB (25 KB as a transcript line), and Claude's `/context` prices the 51 skills at **7.7k tokens**,
   not 12k. The 12.8k the lead saw is `cache_creation_input_tokens` of the first call: everything that is new to the cache
   in a fresh session (skill listing, agent listing, environment, deferred-tool names, MCP instructions), not the listing
   alone. The rest of the ~40k is Claude's own prompt and tool definitions (`/context`: system prompt 3.5k, tools 18k).
   Realistic saving per Claude start: **~3-5k tokens (7-12%)** with `--setting-sources project,local`
   (skill listing 51 → 22 skills). Codex: **−2.3k of 16.5k (−14%)**. OpenCode: listing 22 → 5 skills, tokens not measured.
3. **Two switches that look right are net losses on Claude** (measured): `disableBundledSkills: true` takes the skill
   listing from 22 to 5 skills but makes the `Workflow` tool definition grow from 5.5 KB to 22.9 KB (it inlines the
   `workflow-authoring` text that the skill no longer carries), so the first call is 3k tokens *bigger*;
   `--disable-slash-commands` removes the listing but the first call grew to 42,975 tokens in both runs. Do not use either.
4. **Side effect to decide on (verified live): `--setting-sources project,local` also drops the herdr hook in the owner's
   `~/.claude/settings.json`**, so herdr never reports `agent_session` for that pane. obor reads `agent_session` only as a
   cross-check (internal/app/sessions.go:24; it passes `--session-id` itself), but the lead's other kinds keep their user
   hooks, so the kinds diverge. Same for OpenCode if its user config is relocated (not needed with the env var).
5. **Where the real weight is, outside the question**: in Claude's request the `Artifact` tool definition alone is 37 KB
   (~9k tokens, half the tool bytes); `SendFeedback` 5.6 KB, `Workflow` 5.5 KB, `AskUserQuestion` 5.4 KB, `Agent` 5.4 KB,
   `ScheduleWakeup` 4.8 KB. No skills setting reaches these. `--disallowedTools` is the lever; not probed here (§8).

| | Switch (exact) | Drops | Keeps | Live check |
|---|---|---|---|---|
| **Claude** | `--setting-sources project,local` | user `~/.claude/settings.json` (user `enabledPlugins`, user hooks incl. herdr's, `env`, `model`, `skillOverrides`, statusLine), `~/.claude/skills` (personal + synced `anthropic-skills:*`), `~/.claude/CLAUDE.md` and rules | project/local settings, project `enabledPlugins` (obor's plugin, hooks, `obor:lead` all still loaded), project skills/agents, managed settings, `~/.claude.json` (trust), auth, `--settings`, `--strict-mcp-config` | skills 51 → 22; first-turn 39.7k/41.7k → 36.8k |
| **Codex** | `-c features.plugins=false -c features.apps=false -c skills.bundled.enabled=false` and per skill `-c 'skills.config=[{name="…",enabled=false}]'` | plugin skills (`documents`, `pdf`, …), app connectors, bundled system skills, the named user skills | auth, project `.codex/config.toml`, obor's `-c mcp_servers.obor`; user hooks (herdr's `hooks.json`) not probed: removed from the clone | catalog 28 → 4 entries; first call 16,489 → 14,228 |
| **OpenCode** | env `OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1` (in the kind's `start` reply `env`) | `~/.claude/skills` (incl. `synced/*`) **and the project's `.claude/skills`** | `.agents/skills`, `.opencode/skills`, everything else | skills 22 → 5 (3 built in, 2 project) |

### Q2: project skills that differ per role

1. **One committed skill tree serves all three kinds.** Put skills in `.agents/skills/<name>/SKILL.md` (Codex and OpenCode
   read it natively) and make `.claude/skills` a symlink to it (Claude reads `.claude/skills`). Verified: Claude loads a
   root symlink `.claude/skills -> ../.agents/skills` and a per-entry symlink; Codex loads a symlinked skill root.
2. **Per-role *selection* has a neutral form only if obor passes it**: Claude `skillOverrides {"name":"off"}` in
   `--settings` (verified), Codex `-c skills.config=[{name,enabled=false}]` (verified), OpenCode `permissions` `skill` deny
   in the generated config (documented, per-agent form verified, global form not). That needs obor to know the role
   (`agents.Start` has no role today) and the skill names.
3. **The role-specific native mechanisms are not neutral**: Claude `--agent` / `.claude/agents`, OpenCode agents +
   `default_agent`, Claude `--add-dir` role dirs. Codex has no committed per-role equivalent (profiles are not read from
   a project; `--add-dir` grants writes, not skills).
4. **Cheapest honest option: no code.** All project skills load for every role; the role text (already delivered at start
   by the SessionStart hook and the OpenCode plugin) says which ones the role uses.

Recommendation and options with sizes: §9.

## 1. What is in a fresh Claude start (measured)

Evidence: lead transcript `~/.claude/projects/-Users-djordjeradakovic-Work-obor-kit-wt-L-01/0443919c-*.jsonl` (line 13
`skill_listing`: `skillCount` 48, `content` 22,912 chars; line 35 first assistant turn: input 2, cache_create 12,805,
cache_read 26,802 = 39.6k; line 12 `agent_listing_delta`: 4.9 KB). Reproduced in a scratch project with obor's flags
(`--strict-mcp-config --settings {remoteControlAtStartup:false,autoMemoryEnabled:false}`, sonnet, low effort, one turn
"Reply with exactly the single word OK"). Runs, first call (`usage` of the first assistant record):

| Run | Flags added to the obor shape | Skills listed (chars of listing) | First-call input = new + cache_create + cache_read |
|---|---|---|---|
| A, A2 baseline | none | 51 (23,215) | 39,665 = 2+12,861+26,802 ; 41,704 = 2+11,218+30,484 |
| E | `--settings` `enabledPlugins` false ×4 (openseo, typesafe, 2 LSP) + `skillOverrides` off ×4 | 34 (18,875) | 40,209 |
| **B** | `--setting-sources project,local` | **22 (9,112)** | **36,755** = 2+8,220+28,533 |
| I1 | B + `--plugin-dir` stub + `skillOverrides` off ×2 | 21 (8,984) | 36,715 |
| B2 | B + `disableBundledSkills:true` | 5 (841) | 40,147 (Workflow tool def +17.4 KB) |
| B3 | B2 + `permissions.deny:["Agent"]` | 5 (841), no agent listing | 36,963 (cache miss, all new) |
| C, C2 | `--disable-slash-commands` | none | 42,976 ; 42,974 |
| I2 | `--agent wk` (project agent) | 51 (23,215) | 40,026 |
| S | `--safe-mode` (floor, drops obor's plugin: not usable) | 17 (7,989) | 34,824 |

- **Noise**: A and A2 have the same flags and differ by 2k. Cause found in the recorded request shape
  (`api-request-shape`): `SendFeedback` (5.6 KB tool definition) appears from A2 on, not in A. So read first-call totals
  as ±2k and compare listing sizes, which are deterministic.
- `/context` (local, no model call), baseline: system prompt 3.5k, system tools 18k, **skills 7.7k (51)**, custom agents
  16 tokens; `--safe-mode`: skills 2.7k (17); B2: the five skills sum to ~0.3k.
- Agent listing: 4.5 KB (≈1.1k tokens), built-in agents plus the project's. B3 shows `permissions.deny:["Agent"]` removes it
  entirely, but also removes subagents; not recommended.
- `claude-in-chrome` tools (20, "loaded on-demand", 0 tokens) appear under `--strict-mcp-config` too; not user MCP config.

### 1.1 Claude: what each switch does (docs and probes)

| Mechanism | Result | Source |
|---|---|---|
| `--setting-sources user,project,local` subset | `claude --help`: "Comma-separated list of setting sources to load (user, project, local)". Excluding `user` skips user settings, user CLAUDE.md and rules, `~/.claude/skills`, and the claude.ai skill sync. `~/.claude.json` and managed settings are read regardless | cli-reference.md L129; skills.md L256; memory.md L166,L192; agent-sdk/claude-code-features.md "What settingSources does not control" |
| project plugin survives | probe B: `obor:lead` from `enabledPlugins` in the project's `.claude/settings.json` was listed; plugin hooks fire (probe I1: stub plugin's SessionStart hook wrote its log under `--setting-sources project,local --plugin-dir`) | probes B, I1 |
| obor installs are project-scope | `~/.claude/plugins/installed_plugins.json`: `obor@obor-kit` scope `project` for four projects, `obor@obor-kit-dev` for this repo. A project that installed it at user scope would **lose obor's hooks and guards** under the trim | installed_plugins.json |
| `--settings` `enabledPlugins` false / `skillOverrides` off | works (probe E, I1), but names are the owner's: a deny-list. `skillOverrides` "off" does not apply to plugin skills (skills.md L941); the 14 synced `anthropic-skills:*` stayed in E | skills.md L909-941 |
| `--disable-slash-commands` | no listing, but bigger first call (C, C2) and it also removes plugin skills, `/obor:lead` included | probes C, C2 |
| `disableBundledSkills` | smaller listing, bigger tool definition (B2 vs B) | probe B2, request shape |
| `--bare` | skips hooks, plugins, keychain: "Anthropic auth is strictly ANTHROPIC_API_KEY or apiKeyHelper (OAuth and keychain are never read)"; unusable with the owner's subscription login | `claude --help` |
| `--safe-mode`, `--restricted` | drops obor's plugin and hooks (S shows the plugin skill gone); floor measurement only | `claude --help`, probe S |
| fresh `CLAUDE_CONFIG_DIR` | starts on the first-run onboarding screen, no login | probe J (pane read) |
| herdr's user hook | verified on the panes still registered after their runs: the B pane has **no `agent_session`**; the C and C2 panes (user source kept) have `agent_session {source: herdr:claude}` | `herdr agent list` of the probe session |

obor today (internal/agents/claude/claude.go:51-86): passes `--strict-mcp-config` (lane agents), `--settings` with
`remoteControlAtStartup`/`autoMemoryEnabled` (non-lead, internal/config/models.go:247-256), no `--setting-sources`.
A key the project's committed `.claude/settings.json` sets is left out of `--settings` (claude.go:117 `unset`): that
rule would also cover a settings key such as `skillOverrides`.

## 2. Codex 0.167.0 (source tarball `rust-v0.167.0` read, docs, probes)

- **Skill roots** (ext/skills/src/host_roots.rs:27-28 and `roots_from_layer_stack`; docs skills.md "Where Codex loads
  local skills"): repo `.agents/skills` in every dir from the cwd up to the repo root; the project `.codex/skills`
  (config-layer folder, so only for a trusted project); user `$CODEX_HOME/skills` (deprecated) and `$HOME/.agents/skills`;
  admin `/etc/codex/skills`; plugin roots; bundled "system" skills. Verified with `skills/list` over `codex app-server
  --listen stdio://` (no model call): `.agents/skills` + `.codex/skills` (repo scope), `~/.codex/skills/codex-img` and
  the plugin skills (user), `imagegen`, `openai-docs`, `skill-creator`, `skill-installer`, `review-agent` (system).
- **Switches** (config-reference.md L387-425; config/src/skills_config.rs): `skills.bundled.enabled`,
  `skills.include_instructions`, `skills.max_context_tokens`, `skills.config = [{name|path, enabled}]` (last match wins),
  `features.plugins`, `features.apps`. `-c` takes all of them (probed). `skip_host_skill_discovery` exists but is
  "under development", default off (features list; core/src/session/session.rs:655): not tried.
- **First call of a real turn, scratch `CODEX_HOME` cloned from the owner's** (config, plugins, skills; hooks removed),
  `--no-daemon`, low effort, one turn; first `token_count.last_token_usage.input_tokens`:

| Run | `-c` added | Catalog | First call input |
|---|---|---|---|
| CA baseline | none | 28 entries, 12,110 chars (4 system, 3 project, `codex-img`, 12 plugin skills, 8 app/MCP entries) | **16,489** |
| CC | `features.plugins=false` | 16 entries, 7,013 chars | 15,149 |
| CD | `skills.config` off for `codex-img`, `lead-skill` | 26 entries, 11,432 chars | 16,288 |
| CB | `skills.include_instructions=false` | no catalog (project skills too) | 14,229 |
| **CE** | `features.plugins=false`, `features.apps=false`, `skills.bundled.enabled=false`, `skills.config` off `codex-img` | **4 entries, 3,277 chars (the 3 project skills + `node_repl`)** | **14,228** |

  CB saves about as much as CE although CE still lists 3.3 KB; I cannot explain the 1-token match. A real obor reviewer's
  rollout (read only, `01a1c1b8…`) shows the same shape: skills block 10,956 chars, first call 20,231 tokens.
- User skill dirs have no wholesale switch: only per-name/path `skills.config` (CD) or relocating `CODEX_HOME`, which also
  drops auth, config, plugins, hooks and trust (a scratch home needs `auth.json` copied). obor's Codex start passes only
  `-c mcp_servers.obor.*`, the update-check key and flags (internal/agents/codex/codex.go:39-79), registers no plugin,
  so `features.plugins=false` does not remove obor's own wiring.
- Trust: the trust dialog says "Your trust decision will be saved"; probes therefore ran in a clone (the real
  `~/.codex/config.toml` md5 was identical before and after).
- Symlinks: a symlinked skill root works (`.agents/skills -> ../.claude/skills` listed `sk1` with the resolved path;
  docs: "Codex supports symlinked skill folders"). That project was not in the clone's trust list, so `.agents/skills`
  loads without trust; `.codex/skills` untrusted was not tried.

## 3. OpenCode v2.2.0

- **Roots** (docs skills.md "Discovery" and "Precedence"): global `~/.config/opencode/skills`, global compat
  `~/.claude/skills` and `~/.agents/skills`; project `.opencode/skills`, `.claude/skills`, `.agents/skills`; config
  `skills: [dirs or URLs]` (arrays of every config file are combined). Later source wins on the same id (path-derived).
  OpenCode does **not** read Claude plugin skills, only those directories.
- **Probe** (scratch `XDG_*`, `opencode serve --port 4973x`, `GET /api/skill` with the server's basic-auth password; skills
  are loaded lazily, the first call can be empty): baseline 22 = 3 built in, `codex-img`, `procedural-print-art`, 14 from
  `~/.claude/skills/synced/<uuid>/*` (SKILL.md nested up to 6 deep), plus the project's `cl-skill` (`.claude`), `ag-skill`
  (`.agents`), `oc-skill` (`.opencode`).
- **`OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1`** (docs rules.md "Claude Code Compatibility": "Disable only .claude/skills";
  also in the binary's strings): 5 skills left, **`cl-skill` gone too**, so a project using it must also ship
  `.agents/skills` or `.opencode/skills`. `OPENCODE_DISABLE_CLAUDE_CODE=1` gave the same skill list (it also drops the
  `CLAUDE.md` fallback). Not covered: `~/.agents/skills` and `~/.config/opencode/skills` (empty on this machine; they would
  need a per-id `permissions` deny, last match wins, or relocating `XDG_CONFIG_HOME`).
- **Not only skills**: the owner's `~/.config/opencode/opencode.json` registers an MCP server `herd` and `plugins/` plus
  `herdr-tui-session.js`; obor's OpenCode agents load them (OPENCODE_CONFIG ranks below the user config's own layer only
  for conflicts, docs/research/agent-project-config.md §3). Relocating `XDG_CONFIG_HOME` would drop them and the herdr
  integration (the same `agent_session` trade as Claude). Not probed beyond `opencode debug config` source listing.
- Token saving: not measured. A model turn needs provider credentials the scratch `XDG_*` does not have; copying
  `opencode.db` was not done.
- obor today: the kind builds `OPENCODE_CONFIG` (kinds/opencode/kind/kind.go:144-165, `Config` L189) and declares the vars
  it sets (`vars`, L124); the env var would go in the same `env` map and the `vars` list.

## 4. Per-role project skills: what each kind offers

| | Committed skill dirs | Hide some from one role | Role-specific agent | Verified |
|---|---|---|---|---|
| **Claude** | `.claude/skills/<n>/SKILL.md` (project; a linked worktree uses its own root, else the main checkout's since 2.1.277, skills.md L175-185); plugin `skills/` namespaced `plugin:skill`; `--add-dir <d>` loads `<d>/.claude/skills`; symlinks, root and per entry | `--settings` `skillOverrides` `"off"` (project/personal skills only); `permissions` `Skill(name)` deny; frontmatter `disable-model-invocation: true` | `.claude/agents/<n>.md` + `--agent <n>`: the file's body **replaces the system prompt** (probe I2: its text is in the request, the skill listing unchanged); `skills:` preload documented for subagents, **not verified for the main session** | listing, symlinks, `--add-dir` (`wk2-skill` listed), `skillOverrides` (lead-skill/rv-skill hidden, wk-skill kept) |
| **Codex** | `.agents/skills` (cwd → repo root), `.codex/skills` (trusted); no CLI/config key for extra roots (only the app-server request `skills/extraRoots/set`) | `-c skills.config=[{name\|path,enabled=false}]` (CD); per skill `agents/openai.yaml` `policy.allow_implicit_invocation: false` | none committed: a project config cannot set `profile`/`profiles` (docs/research/agent-project-config.md §2); profile files live in `$CODEX_HOME`; `[agents.*]` roles are spawn roles (core/src/agent/role.rs) | listing, `skills.config`, symlink |
| **OpenCode** | `.opencode/skills`, `.claude/skills`, `.agents/skills`, config `skills: [...]` | `permissions` `{action:"skill",resource:"<id>",effect:"deny"}` globally or under `agents.<id>.permissions`; frontmatter `metadata.opencode/autoinvoke: false` | `.opencode/agents/<n>.md` with its own `permissions`; `default_agent` in the config selects it | `debug agents`: project agent `wk` found, its `skill` rules merged; `default_agent` accepted from `OPENCODE_CONFIG` |

What obor would have to pass: nothing for the neutral file shape; for selection, the role plus the skill names.
`agents.Start` (internal/agents/agents.go:74) carries no role (`Model.Launch(role, in)` has it, models.go:247), and
the plugin kind protocol's `start` request (docs/agents/kind-protocol.md) has no `role` field; adding one is additive
(version stays 1). Role cwd: lead and custom roles main checkout, worker a lane worktree, integrator/reviewer/deep dive
a role worktree (internal/roles/roles.go:77-86): all checkouts of one commit, so cwd cannot carry per-role skills.

## 5. Unknown or not verified

- OpenCode token saving; OpenCode global `~/.agents/skills` and `~/.config/opencode/skills` (absent here); the global
  `permissions` skill deny of a generated config (only the per-agent form was read back).
- Claude: whether `--agent`'s `skills:` preload applies to the main session; the +3k of `--disable-slash-commands`
  (reproduced twice, cause unknown); effect of `--disallowedTools Artifact,…` on the first call (sizes only).
- Codex: `include_instructions=false` vs an explicit `$skill` mention; `.codex/skills` and `.agents/skills` in an
  untrusted project (partly); `skip_host_skill_discovery`; why CB and CE match to one token.
- A herdr hook re-added by obor through `--settings` (its script path is user-global); whether obor wants it.
- All first-call totals are single runs per configuration (two for A and C).

## 6. Side effects of the probes

`~/.claude.json` has trust entries for the scratch dirs (`…/scratchpad/probe/{proj,sym1,sym2}`); about a dozen small
transcripts sit under `~/.claude/projects/-private-tmp-…-probe-*`. The real `~/.codex/config.toml` is unchanged
(md5 before = after); Codex ran in a clone whose `auth.json` copy was deleted afterwards. Herdr sessions
`probe-skills` and `probe-skills2` were stopped and deleted (the default session was never addressed); the OpenCode servers
were stopped by pid. Files read: `~/.claude/settings.json`, `plugins/installed_plugins.json`, `~/.codex/config.toml`,
`hooks.json`, `~/.config/opencode/opencode.json`, one real reviewer rollout (token numbers only).

## 7. Sources and probes

- Claude docs (fetched 2026-11-09, code.claude.com/docs/en/): cli-reference, settings, skills, sub-agents, plugins-reference,
  memory, env-vars, agent-sdk/claude-code-features; `claude --help`.
- Codex: developers.openai.com/codex/{skills,config-reference,config-basic,plugins,subagents}.md; source tarball
  `rust-v0.167.0` (ext/skills/src/host_roots.rs, config/src/skills_config.rs, core/src/session/session.rs,
  core/src/config/mod.rs:4250, features/src/lib.rs:1815, app-server-protocol `skills/list`); `codex --help`, `codex features list`.
- OpenCode: opencode.ai/v2/docs/{skills,agents,config,permissions,rules}; `opencode --help`, `opencode debug agents|config`,
  binary strings for `OPENCODE_*`.
- obor code: internal/agents/claude/claude.go, codex/codex.go, agents.go, internal/config/models.go, internal/roles/roles.go,
  kinds/opencode/kind/kind.go. Earlier research: agent-project-config.md, agent-cli-and-herdr-capabilities.md.
- Probe scripts and outputs (not committed): the session scratchpad `probe/` (`run.sh`, `ctx.sh`, `cdx.sh`, `cdxskills.py`,
  `usage.py`, `*.out`, `oc/`). Claude: throwaway herdr sessions `probe-skills`, `probe-skills2`, a scratch project with
  `.claude/skills/{wk,lead,rv}-skill`, `.claude/agents/wk.md`, a project `enabledPlugins` for `obor@obor-kit-dev` and a stub
  plugin; `/context` for token counts, the transcript's first assistant `usage` for turns.

## 8. Adjacent, flagged not expanded

- Claude's tool definitions are ~18k tokens of every start; `Artifact` is 37 KB of the 73-78 KB. A role that never
  publishes artifacts could start with `--disallowedTools Artifact` (and likewise `SendFeedback`, `Workflow`); needs a probe.
- The obor plugin must be enabled by the project's `.claude/settings.json` for the trim to keep obor's hooks. `ProjectWarnings`
  (claude.go:129) already warns on `disableAllHooks`; a missing `enabledPlugins` entry is the same class.

## 9. Recommendation

### Q1 options

| Option | What obor changes | Size | Saving |
|---|---|---|---|
| **1A (recommended): per-kind trim, non-lead roles** | claude.go `Start`: add `--setting-sources project,local` unless the type's args already name it; add the plugin check to `ProjectWarnings`. codex.go `Start`: `-c features.plugins=false -c features.apps=false -c skills.bundled.enabled=false`, each skipped when the project's `.codex/config.toml` sets the key (existing `topKeys` scan; dotted keys need a small extension). kinds/opencode: `OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1` in `start`'s `env` and `vars`. Lead stays untrimmed (same rule as `autoMemoryEnabled`: `role != "lead"`, models.go:253) | ~60-90 prod lines + tests, one line each in docs/agents/kind-protocol.md and the role docs | Claude 3-5k/start, Codex 2.3k/start, OpenCode skills 22 → 5 |
| 1B: also hide named user skills | Codex `skills.config` per name, Claude `skillOverrides` per name, from a list in config | +40 lines and an owner-maintained list | Codex: 0.2k per skill; Claude: little, since 1A already drops user skills |
| 1C: do nothing | none | 0 | the listing stays 3-8k per start (cache-written on each fresh session) |

Not recommended: `disableBundledSkills`, `--disable-slash-commands` (net losses), `--bare`/fresh config dirs (lose login),
`permissions.deny Agent` (loses subagents).

### Q2 options

| Option | What obor changes | Size | Kinds that cannot |
|---|---|---|---|
| **2A (recommended now): one neutral tree, roles chosen by the role text** | nothing in code. The project commits `.agents/skills/` and `.claude/skills -> ../.agents/skills`; `docs/agents/<role>.md` (the project's own role text, already delivered) names each role's skills; the kit's `templates/agents/*.md` get one "Skills" line under "This project" | docs only | none |
| 2B: obor hides other roles' skills | `Role` in `agents.Start` and in the kind protocol's `start` request; a project table `agents.roles.<role>.skills` (or a `roles:` list in each skill's frontmatter) and a lister of project skill names; Claude `skillOverrides` into `--settings`, Codex `-c skills.config`, OpenCode `permissions` in the generated config | ~150-250 prod lines + tests, kind-protocol.md | none; OpenCode's global form is unverified |
| 2C: per-role skill dirs | Claude `--add-dir`, OpenCode `skills: [dir]` | ~60 lines | **Codex** (no CLI/config key) |
| 2D: per-role agent files | Claude `--agent`, OpenCode `default_agent` | ~100 lines | **Codex**; Claude's `--agent` replaces the system prompt |

### Open choices for the owner

1. Take 1A for all three kinds, or only the kinds where the saving is measured (Claude, Codex)?
2. The Claude trim drops herdr's user hook, so no `agent_session` for Claude workers. Accept (obor passes `--session-id`
   and treats `agent_session` as a cross-check), or have obor add herdr's hook to `--settings`?
3. Opt-out for a project: through `agents.kinds.<kind>.args` naming `--setting-sources` (no new file key), or a committed
   file key (Claude has none for the sources list)?
4. Should the lead and the owner's drive agent stay untrimmed (they use the owner's chrome/artifact/skill setup)? The
   proposal trims every role except the lead.
5. Is hiding role skills (2B) wanted, or is "all project skills visible, role text says which" (2A) enough? The catalog of
   a project's own skills is small (3 skills ≈ 0.1-0.8k tokens); 2B pays for itself only with many skills or a leak risk.
6. OpenCode: the project must then ship `.agents/skills`, not only `.claude/skills`; fine if 2A's neutral tree is adopted.
7. Follow-up probe on `--disallowedTools` for the 37 KB `Artifact` definition (§8): a bigger Claude saving than any skills switch.
