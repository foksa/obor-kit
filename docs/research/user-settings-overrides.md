# User-level settings: overriding any key of an agent obor starts, keeping the rest

Research for the lead, 2026-11-10. Versions probed: **claude 2.1.322**, **codex-cli 0.167.0**, **opencode v2.2.0**,
**herdr 0.9.5**. Question (owner 11-10): obor must be able to override any user-level setting of an agent it starts,
global hooks included, and keep every user setting it does not override. Context: docs/research/agent-skills-scope.md
§1.1-§3, docs/decisions.md 2026-11-10 "The user-scope trim overrides, it does not drop a source", T-1211.
Nothing in the repo was changed; the owner's real user config was not edited (§6). Probes ran in a throwaway headless herdr
session (`uso`, deleted), on the owner's real login for Claude and Codex where a model was needed, in a scratch
`CLAUDE_CONFIG_DIR` / `CODEX_HOME` where a controlled user layer was needed. No `claude -p`, no `codex exec`, no
`opencode run`.

## Answer first

1. **Claude: every settings key except one single user hook can be overridden from `--settings` while the user source
   stays.** Scalars: the flag wins. `env`: merged per variable. `permissions` allow and deny lists: concatenated.
   `enabledPlugins`: per plugin. `hooks`: **flag hooks are added to the user's, never replace them**, and **no
   `--settings` key switches one user hook off**: an empty list, an empty `hooks` array under the same matcher and an
   identical command all leave it running; `disableAllHooks: true` switches off *all* hooks (user, flag and plugin hooks,
   so obor's own guards too). The only generic way to drop one user hook is to drop the user source
   (`--setting-sources project,local`) and re-inject the user's settings minus that hook. A hook script that checks an
   environment variable can also be silenced through `env` (herdr's does: `HERDR_ENV=0`), for that script only.
2. **Claude re-inject design works for settings and costs the user's memory files.** Re-injected through `--settings`
   under `--setting-sources project,local`: env, hooks (herdr's hook fired and herdr stored `agent_session`), statusLine,
   permission mode, permission lists, `enabledPlugins` of user-scope installs (plugins and their skills loaded exactly
   as without the trim). Lost with the user source and not carried by `--settings`: the user `CLAUDE.md`, `rules/`,
   `~/.claude/skills`. Restorable: skills (`--add-dir` on a dir whose `.claude/skills` links to them), `CLAUDE.md`
   (`--add-dir` plus `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1`), rules (real files copied into the dir's
   `.claude/rules/`; links and `@`-imports did not load). The restored text is framed as project memory. The claude.ai
   skill sync cannot be restored (and T-1211 wants it off).
3. **Codex: one user hook can be switched off per start**, with `-c 'hooks.state={"<hooks.json path>:<event>:<group>:<index>"={enabled=false}}'`
   (inline table; the dotted form does not work because the key contains dots). All hooks off: `-c features.hooks=false`.
   Every other `-c` key overrides the user's config key by key, and tables merge (the trust hashes stayed). Hooks of all
   layers run together; nothing replaces them. A hook's key is positional, so obor has to read the user's `hooks.json`
   to build it.
4. **OpenCode: one plugin and one MCP server can be switched off per start**, from `OPENCODE_CONFIG` (file) or
   `OPENCODE_CONFIG_CONTENT`: `"plugins":["-<plugin id>"]` removed the owner's `herdr.opencode` and kept `herd.opencode`;
   an MCP server needs its whole definition repeated with `"disabled": true` (a bare `{"disabled":true}` was ignored).
   Other user keys keep applying.
5. **One design for every kind**: a per-kind override object in obor's config in the kind's own native shape
   (`settings`) plus one neutral list shape (`off`: hooks, plugins, skills, MCP servers, each matched by a substring of
   the user's own entry). Per-role objects layer over the per-kind one. Size and what each kind cannot do: §5.

6. **The owner's copy-merge-load variant (follow-up, §5b):** works on Claude as a `--settings` file under
   `--setting-sources project,local` (hooks, herdr's `agent_session`, env, statusLine, user-scope plugins, permissions all
   verified), and is the way to switch a single hook off. It ranks above the project, so obor must drop what the project
   sets. As `.claude/settings.local.json` in the worktree it is worse (Claude writes approvals into it, it collides with the
   person's file). Memory, rules and user skills can be restored; claude.ai synced skills cannot. For Codex and OpenCode
   the copy-merge idea is not better than `-c` and `OPENCODE_CONFIG`; a merged `CODEX_HOME` keeps login with a linked
   `auth.json` but risks credentials and history.

## 1. Claude: how `--settings` combines with `~/.claude/settings.json`

Probe: real `~/.claude/settings.json` as the user layer (keys: `env` ×3 incl. `MAX_THINKING_TOKENS=128000`, `model`,
`permissions.defaultMode: auto`, `skillOverrides`, `hooks.SessionStart` = herdr's script, `statusLine`, `enabledPlugins`
×3, `extraKnownMarketplaces`, scalars), a scratch project with no project settings, `--debug-file` for hook and plugin
lines, `!`-lines for the shell's env, `/context`, `/permissions`, `/hooks`. A scratch `CLAUDE_CONFIG_DIR` (copy of
`~/.claude.json` for onboarding and trust, own `settings.json`, `CLAUDE.md`, `rules/`, `skills/`) for the controlled user
layer. Not logged in there (the keychain entry is per config dir), which `/context`, `/permissions` and hooks do not need.

| Key | Result | Probe |
|---|---|---|
| scalars | flag wins: `statusLine` (screen showed the flag's text, not the user script), `permissions.defaultMode` ("manual mode on" over the user's `auto`; scratch: re-injected `acceptEdits` shown) | r1, c2 |
| `env` | **per variable**: flag `MAX_THINKING_TOKENS=1111` beat the user's 128000; the user's `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS`, `CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING` stayed; a new `PROBE_A` was added. Seen both by hooks and by the `!` shell | r1 |
| `permissions.allow` / `deny` | **lists concatenated**: `/permissions` Allow tab showed the flag's and the user's rule, Deny tab likewise. That a deny outranks an allow across layers was not probed (needs a tool call) | c7 |
| `enabledPlugins` | **per plugin**: `false` for `openseo@openseo` in the flag took enabled plugins from 8 to 7 (debug `Found N plugins`), the user's other two stayed | r1, base |
| `hooks`, same event | **added**: the flag's `SessionStart` hook and the user's herdr hook both ran (`hooks.log` line plus debug `Hooks: SessionStart:startup [bash …herdr-agent-state.sh]`; herdr stored `agent_session`) | r1 |
| `hooks`, flag key empty (`"SessionStart":[]`) | user hook **still runs** | r2 |
| `hooks`, flag matcher equal to the user's, `hooks:[]` | user hook **still runs** | r4 |
| `hooks`, flag repeats the user's exact command | runs **once** (deduplicated by command string): no way to shadow | r5 |
| `disableAllHooks: true` | **no hook runs**: neither the user's herdr hook nor the flag's own hook nor plugin hooks (debug: `Skipping plugin hooks`). Not scoped to a layer | r3 |
| flag `env` `HERDR_ENV=0` | the herdr hook ran (status 0) but reported nothing: the pane had no `agent_session`. The script exits unless `HERDR_ENV=1`, `HERDR_SOCKET_PATH`, `HERDR_PANE_ID` are set (`~/.claude/hooks/herdr-agent-state.sh` lines 17-22) | e1 |

Notes:
- `/hooks` lists only file-layer hooks: it showed "1 hook on 1 event" (the user's) while the flag hook also ran. Do not
  verify flag hooks with `/hooks`.
- Switching one user hook off from the flag layer: **not possible**. The hooks that exist as alternatives: managed
  settings (`allowManagedHooksOnly`: not available to obor), `disableAllHooks` (all or nothing, takes obor's plugin
  guards with it), the script's own env gate (works only for scripts that have one), and dropping the user source (§2).
  A herdr-specific consequence: herdr's own header says the script is overwritten on update, so editing it is out.

## 2. Claude: `--setting-sources project,local` plus the user's settings re-injected

Probe: `--setting-sources project,local` with and without `--settings <user settings.json minus overridden keys + a
marker hook>`. Real user layer for plugins and the herdr hook; scratch user layer for memory files.

**What the flag layer brings back (verified):**
- `env`: the scratch user's `CFG_ENV`, `MAX_THINKING_TOKENS=4444` visible in a `!` shell and in hooks (c2, reinject).
- `hooks`: the marker hook and **herdr's SessionStart hook ran** with the user layer dropped; herdr stored
  `agent_session {source: herdr:claude}` for the pane (reinject). Without the re-injection that hook is absent
  (`trim`: `Found 6 plugins (5 enabled)`, no herdr line), as agent-skills-scope.md §1.1 found.
- `statusLine`: the scratch `USERSTATUS` showed (c2). Permission mode: `accept edits on` (c2), `auto mode on` (reinject).
- `enabledPlugins` of **user-scope installs**: with the key re-injected, `Found 9 plugins (8 enabled)` and 32 skills, the
  same plugin count as the untrimmed start (48 skills); without it, 6 plugins (5 enabled) and 18 skills. So the
  trim's plugin switch-off and the re-inject use the same lever.
- Permission lists: the flag layer carries them (c7); mode verified under the trim. The rule lists themselves were not
  read back under the trim.
- Not separately checked under the trim: `model`, `effortLevel`, `modelSettings` (re-injected in the file, same
  mechanism as the scalars above).

**What is lost and the flag layer cannot carry (scratch user layer, `/context` memory and skill sections):**

| Lost | Evidence | Restore |
|---|---|---|
| `~/.claude/CLAUDE.md` | `/context` listed `cfg/CLAUDE.md` untrimmed, nothing under the trim (c0 vs c2) | `--add-dir <dir>` with `<dir>/CLAUDE.md` (a link works) **and** `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1` (put it in the flag's `env`). `/context` listed it (19 tokens); a live turn quoted its marker line and obeyed it, calling it "the project's CLAUDE.md" (live2). Alternative: `--append-system-prompt-file <file>` (live turn saw its marker, live1): it lands in the system prompt, not in memory |
| `~/.claude/rules/*.md` | listed untrimmed, absent trimmed | **a real file** in `<dir>/.claude/rules/` loads (11 tokens, c5). A symlinked `rules` dir (c3) and a symlinked rule file (c6) did **not** load: obor would copy the files |
| `~/.claude/skills/*` | `uskill` listed as User skill untrimmed, absent trimmed | `--add-dir <dir>` with `<dir>/.claude/skills` a symlink to the user's skills dir: `uskill` listed (c3, c4) |
| claude.ai synced skills | in the untrimmed list (48 vs 32 skills with plugins re-enabled; 16 = the user's 2 skills + 14 synced, count from agent-skills-scope.md §3) | not probed: they live under the skills dir, the scratch dir had none. T-1211 accepts them staying |
| `@path` imports in a restore `CLAUDE.md` | pointing at the user files: a "disable external imports" dialog appeared (c4); the model did not see the imported markers (live1) | do not use imports; copy the text |

Other facts: user-scope MCP servers live in `~/.claude.json`, which no source switch touches (the scratch run still
listed the owner's MCP tools); `--add-dir` also grants tool access to the dir (`claude --help`: "Additional directories
to allow tool access to"), so the restore dir should hold only the linked or copied files.

**Verdict:** the re-inject is complete for everything in `settings.json`, and costs a generated restore dir plus one env
var for memory, rules and skills. It is the only route to switch off a single user hook on Claude. For anything else
the per-key `--settings` override of §1 (T-1211's design) keeps the user source and needs none of this.

## 3. Codex 0.167.0

Probe: scratch `CODEX_HOME` = copy of `~/.codex/config.toml` + a `hooks.json` with two `SessionStart` hooks + a copied
`auth.json` (deleted afterwards), `--no-daemon`, `/hooks` view for installed/active counts, a real turn ("Reply with
exactly OK") with the hooks writing a log. The docs (learn.chatgpt.com/docs/hooks, config-reference, config-basic,
config-advanced) say hooks of every layer all run ("Higher-precedence config layers don't replace lower-precedence
hooks"), `features.hooks=false` turns them off, and name `/hooks` as the only per-hook disable; they do not mention
`hooks.state`. The live probes found it:

- Trust and enable state is a config table `hooks.state."<abs hooks.json path>:<event snake_case>:<group index>:<hook index>"`
  with `trusted_hash` and optional `enabled = false`. Toggling a hook in `/hooks` (space) wrote `enabled = false` into
  that table in the user's `config.toml`; the owner's own file has such an entry for herdr's hook
  (`…/.codex/hooks.json:session_start:0:0`).
- A new, untrusted hook is listed "needs review" and does not run; `--dangerously-bypass-hook-trust` did not make it
  active in the `/hooks` count.
- `-c 'hooks.state."<path>:session_start:0:1".enabled=false'` (dotted, quoted): **no effect** (still active): the key
  contains dots, `-c` does not honor the quotes. Inline table `-c 'hooks.state={"<path>:session_start:0:1"={enabled=false}}'`:
  **effective**: `/hooks` Active 1 → 0 with no "needs review" line (the other hooks' trust hashes survived, so the
  `-c` table merges into the config's table key by key), and in a real turn **no hook fired** (rollout shows the `OK`
  reply), while the same start without the override fired the hook (f2 vs f3).
- `-c features.hooks=false`: Installed 2 → 0. All hooks off, project hooks too (obor's `.codex/hooks.json`: not probed
  but the same flag; do not use it for a trim that must keep obor's hooks).
- Other keys: `-c` ranks above project, profile and user config (docs, config-basic "Configuration precedence"), and the
  skills/features keys of agent-skills-scope.md §2 already rely on it. `--profile <n>` overlays `$CODEX_HOME/<n>.config.toml`
  (a file in the user's home: obor would have to write there); a project config cannot name a profile. A `CODEX_HOME`
  copy keeps nothing the user did not copy (auth, trust, plugins, hooks): not a per-start override.
- Not probed: hooks declared inline as `[hooks]` in `config.toml` (the key's path part is not known), plugin hooks
  (`hooks/hooks.json` in a plugin: `features.plugins=false` already removes them, T-1201), arrays inside `-c` tables.

## 4. OpenCode 2.2.0

Probe: `opencode serve --port 4973x` with the owner's real user config, `OPENCODE_SERVER_PASSWORD=probe`, `GET /api/plugin`
and `/api/mcp` after one warm-up call (the first call returns empty lists), `/api/config` for the source list.
Baseline: user plugins `herd.opencode` and `herdr.opencode` (auto-discovered from `~/.config/opencode/plugins/*.js`;
their ids come from the plugin, not the file name), MCP server `herd` connected, 80+ builtin plugins.
Docs (opencode.ai/v2/docs/{config,plugins,mcp-servers,policies}): plugin arrays of all config files are combined, an
entry prefixed with `-` disables an id, `mcp.servers.<name>` objects are replaced whole, `disabled: true` keeps a server
configured but not connected, policies `plugin.use` / `mcp.use` with `effect: deny` remove one by name (not probed).

| Probe | Result |
|---|---|
| `OPENCODE_CONFIG_CONTENT={"plugins":["-herdr.opencode"]}` | `herdr.opencode` gone, `herd.opencode` active, MCP unchanged (o1) |
| `OPENCODE_CONFIG=<file>` with the same `plugins` | same result (o4) |
| `mcp.servers.herd = {"disabled":true}` | **ignored**: still `connected` (o2) |
| `mcp.servers.herd = {"type":"local","command":["true"],"disabled":true}` (content or file) | `disabled` (o3, o4) |
| `OPENCODE_CONFIG` file redefining `herd`'s command | the file's value won over the user's `opencode.json` (`herd` ran `true` and `failed`, o5); `/api/config` lists the user file first, the env file after it |

Notes:
- The user layer stays whole in every probe; only the named entries change.
- **Discrepancy to settle:** agent-skills-scope.md §3 (quoting agent-project-config.md §3) says `OPENCODE_CONFIG` ranks
  below the user config's own layer for conflicts. In this probe a conflicting key (`mcp.servers.herd.command`) from the
  env file won. It may depend on the key; not reconciled here.
- Not probed: `OPENCODE_CONFIG_CONTENT` against `OPENCODE_CONFIG` precedence; the owner's TUI plugin, listed in
  `~/.config/opencode/cli.json` (`"plugins": ["./herdr-opencode"]`), which is a different file from `opencode.json`;
  policies; per-skill and global `permissions` (agent-skills-scope.md §5 still holds).
- To disable an MCP server obor must copy its definition (`type`, `command`/`url`, `environment`) from the user's file
  into the override, or use an `mcp.use` deny policy (docs only).

## 5. Recommendation: one override design for all kinds

**Config shape the owner writes** (names are a proposal; per kind, optionally per role under
`agents.roles.<role>`, the role's object laid over the kind's):

```yaml
agents:
  overrides:
    claude:
      settings: { env: { MAX_THINKING_TOKENS: "8000" }, statusLine: { type: command, command: "…" } }
      off: { hooks: ["herdr-agent-state.sh"], plugins: ["openseo@openseo"], skills: ["auto-mode-setup"] }
    codex:
      settings: { model_reasoning_effort: low }
      off: { hooks: ["herdr-agent-state.sh"], plugins: ["documents@openai-primary-runtime"], skills: ["codex-img"] }
    opencode:
      settings: { … }                      # keys of the generated config
      off: { plugins: ["herdr.opencode"], mcp: ["herd"] }
```

- `settings` is the kind's **native** shape and the one thing that is not neutral (the keys differ per CLI). Claude: merged
  into obor's `--settings` object under T-1211's per-entry "project wins" rule. Codex: each key becomes `-c key=value`.
  OpenCode: merged into the generated `OPENCODE_CONFIG` file.
- `off` is neutral: names or **substrings of the user's own entry** (a hook's command, a plugin id, a skill dir name,
  an MCP name); obor reads the user's file, finds the matches and writes the kind's switch. The owner never writes a
  positional key or an index.

| | `settings` | `off.plugins` / `off.skills` | `off.hooks` |
|---|---|---|---|
| Claude | `--settings` keys (§1: scalars, env per variable, lists concatenate, plugins per key) | `enabledPlugins` false, `skillOverrides` off (T-1211) | **re-inject path** (§2): `--setting-sources project,local`, user `settings.json` minus the matching hooks in `--settings`, a restore dir for memory, rules and skills. Only when `off.hooks` is non-empty; otherwise the user source stays |
| Codex | `-c key=value` | `features.plugins`, `skills.config` (T-1211) | `-c hooks.state={…enabled=false}` per matching hook, built from the user's `hooks.json` |
| OpenCode | generated config | `plugins: ["-id"]`; skills via `OPENCODE_DISABLE_CLAUDE_CODE_SKILLS` (existing) | n/a: its hooks are plugins: `off.plugins` |

**Size** (estimates from the code shapes in claude.go, codex.go, kinds/opencode/kind/kind.go, not measured): config
group and validation ~40 lines; Claude `settings` merge with the project rule ~30 (the `unset` rule in claude.go exists);
Claude `off.hooks` re-inject ~120-180 (read and filter the user's settings, build the restore dir with copied rules,
linked skills and `CLAUDE.md`, the env var, tests for every key class); Codex `settings` ~15 and `off.hooks` ~60-80
(parse `hooks.json`, build the key and inline TOML, tests); OpenCode `settings` ~15, `off.plugins` ~20, `off.mcp`
~40 (copy the user's definition); docs in README "Customizing agents", design.md, kind-protocol.md (the kind protocol's
`start` request needs the override object and the role: additive). Cheapest slice first: `settings` and `off.plugins` /
`off.skills` for all three kinds (~200 lines with tests) cover everything except hooks; `off.hooks` per kind after.

**What each kind cannot do:**
- **Claude:** switch off one user hook while keeping the user source (§1). With the re-inject: the user's `CLAUDE.md`
  and rules arrive as project memory with a copied (stale until the next start) text, no `@` imports, symlinked rules do not
  load; claude.ai synced skills are not restored; the restore dir widens tool access to itself. `disableAllHooks` also
  drops obor's plugin guards: never use it. Env-gating a script (`HERDR_ENV=0`) is per script and only for scripts that
  check a variable; it is a workaround, not part of the design.
- **Codex:** a hook's key is positional (path, event, group, index): a reordered `hooks.json` changes it, so obor builds
  it on every start; the dotted `-c` form does not work and the inline table must be used; inline `[hooks]` in `config.toml`
  not probed; `features.hooks=false` is all-or-nothing and would also stop obor's own project hooks.
- **OpenCode:** a bare `disabled` on an MCP server does nothing (full definition needed); the TUI plugin file
  (`cli.json`) was not probed; whether `-id` reaches plugins from other sources than the user's plugin directory is not
  known; the env-file precedence discrepancy (§4) must be settled before obor relies on override-over-user for
  conflicting keys.

## 5b. Follow-up: the owner's variant, "copy the user's settings, merge the overrides, load only that plus the project"

Variant: obor copies the user's settings file, applies the overrides (drops switched-off hooks, plugins, skills; sets
overridden keys), writes the result per agent start outside the repo (`.obor/state/...`), and the agent loads that plus the
project. Probed per kind on 2026-11-10 (same versions; throwaway herdr session `uso`, deleted).

### Claude: (a) the file given to `--settings`, (b) `.claude/settings.local.json` instead, (c) what stays lost

**(a) `--setting-sources project,local --settings <file>` with the file = the real user `settings.json` minus `openseo`,
`MAX_THINKING_TOKENS` set to 2222, a permission allow and a deny rule added, a marker hook added** (probe m1; the flag
accepts a path as well as inline JSON). Behaves like the user layer for every key checked:

| Key | Result |
|---|---|
| hooks | the marker hook and **herdr's SessionStart hook both ran**; herdr stored `agent_session {source: herdr:claude}` for the pane |
| env | hook saw `MAX_THINKING_TOKENS=2222` (the file's override of the user's 128000); the user's other env vars come from the same file |
| `statusLine` | the user's script ran (its line in the footer) |
| `enabledPlugins` (user-scope installs) | `Found 8 plugins (7 enabled)`: the user-scope plugins named in the file loaded (`improve:improve`, `typesafe:typesafe-ai` in `/skills`), `openseo` (removed from the copy) did not |
| permissions | `/permissions` Allow and Deny tabs listed the file's rules; `defaultMode: auto` applied ("auto mode on") |
| skills | 20 listed (plugins in the file plus built-ins); the user's own skills are not (§2) |

Not an exact stand-in for the user layer in two ways: **rank** and **write-back**.
- *Rank.* The user layer ranks below the project's files; the flag layer ranks above both (local > project, flag >
  local; probed r1, l1). A user copy in `--settings` therefore **beats the project's committed scalars, env variables and
  `statusLine`** instead of losing to them (`permissions` and `hooks` lists still add). obor must drop the copy's entries
  that the project's `.claude/settings.json` and `settings.local.json` set: the per-entry "project wins" rule already in
  T-1211, applied to every key of the copy, not only `enabledPlugins` and `skillOverrides`.
- *Write-back.* Interactive "don't ask again" and `/permissions` rule saving offer three destinations: project local
  (`.claude/settings.local.json`, the default), project (`.claude/settings.json`), **user** (`~/.claude/settings.json`,
  still offered under `--setting-sources project,local`: the owner's real file; I only looked at the dialog). The `--settings` file is
  **not** a destination, so the generated file is never modified; a person's approvals in an obor agent's pane go to the
  worktree's local file, as today.

**(b) The same copy written as `.claude/settings.local.json` in the worktree** (probe l1, project with a committed
`settings.json` that disagrees on `defaultMode`, `MAX_THINKING_TOKENS`, `statusLine`; `--setting-sources project,local`, no
`--settings`):
- It loads and wins over the project file for scalars and per-variable env (`auto mode on` over the project's `plan`,
  `MAX_THINKING_TOKENS=2222` over 9999, the user's `statusLine` over the project's); allow lists concatenate (copy's and
  project's rule both listed). Same rank as (a) for project conflicts, but below obor's own `--settings` keys.
- **Claude writes into it**: adding a rule in `/permissions` with the default destination changed the file's hash and
  appended the rule to its `permissions.allow` (verified). Approvals made in an agent's pane end up in a file obor
  generates and may overwrite at the next start, or keep and then must merge.
- **Collides** with a project's or person's own `.claude/settings.local.json` (the file Claude itself writes approvals
  to): a generated file replaces it, or obor merges into a file it does not own. It sits in the worktree: untracked
  noise the project must gitignore, and a lane worktree's file is not the main checkout's.
- **(a) is better**: not in the repo, not a write target, no collision with the person's local file, regenerated each
  start from current user settings. (b) has no advantage except working without `--settings`.

**(c) What is not in `settings.json` and stays lost** (scratch user layer and the real skills dir; §2 has the first
probes, new ones marked *new*):

| Lost | Restorable? | Recommendation |
|---|---|---|
| user `CLAUDE.md` | yes, partly: `--add-dir <state dir>` holding a copy or link of it, with `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1` in the copy's `env`; the model quoted and obeyed it, framed as the project's `CLAUDE.md` | the owner has none today; offer a `keep` list in config, default off |
| `rules/*.md` | yes, as **real copied files** in `<state dir>/.claude/rules/` (a symlinked dir or file did not load) | same; copy at start, so stale until the next start |
| `~/.claude/skills/<name>/` (user skills) | **yes (new)**: `--add-dir` on a dir whose `.claude/skills` links to `~/.claude/skills`: the two user skills (`codex-img`, `procedural-print-art`) came back, listed as "project" skills (20 → 22 skills) | leave out: removing them is the point of the trim; `skillOverrides` per name covers the exception |
| claude.ai synced skills (`~/.claude/skills/synced/…`, 14 here) | **no (new)**: the same link did not bring them back (22 skills, none of the 14) | leave out |
| user-scope MCP servers | not lost: they live in `~/.claude.json`, not a settings source (the scratch run still listed the owner's MCP tools); obor's `--strict-mcp-config` already governs them for lanes | n/a |

Restore costs: a state dir per start, widened tool access to that dir (`--add-dir`), the project-memory framing, no
`@` imports (the external-import prompt, §2).

### Codex: a merged `CODEX_HOME` per start, or `-c`

Probe y1: `CODEX_HOME=<state dir>` holding a copy of the real `config.toml` (the `hooks.state` key for herdr's hook rebased
to the new `hooks.json` path, same `trusted_hash`), a copy of `hooks.json`, and **`auth.json` as a symlink to the real
one**; `--no-daemon`.
- **Login survives**, with `auth.json` present: no login screen, a real turn completed ("OK"). The real `auth.json`
  (mtime, size), `config.toml` and `hooks.json` were unchanged afterwards.
- **Hook trust is by content, not path**: the rebased key with the same hash gave `/hooks` Installed 1, Active 1, no
  review; herdr's hook ran in the turn and herdr stored `agent_session {source: herdr:codex}`. So obor can write a
  merged `hooks.json` plus the state entries and copy the hashes it finds in the user's `config.toml`; a changed or
  new hook needs a hash obor cannot compute (only Codex's `/hooks` does), so a *new* hook of obor's own must come from
  a layer that needs no trust (project `.codex/hooks.json` is trusted by the project's trust entry, §3) or `-c`.
- **What a per-start home does not carry**: everything the person did not link: sessions (new rollouts landed in the
  per-start home, so `codex resume <sid>` from another home would not find them unless `sessions` is shared; not
  probed), plugins, `skills/`, memories, `models_cache.json`, sqlite state, trust entries for projects.
- **Credential risk (not probed)**: Codex rewrites `auth.json` when it refreshes a token. If it replaces the file, the
  symlink in the per-start home is replaced by a file and the real login keeps the old token; a *copy* diverges for
  sure. Either can log the owner out. No refresh happened in the probe.
- **A better way for Codex: `-c` keys** (§3): the same switches without a home: `-c key=value` per overridden key,
  `-c hooks.state={…enabled=false}` per switched-off hook, `-c features.plugins=false`, `skills.config`. Nothing is
  copied, login, sessions, trust and plugins are the user's. A merged home is only needed for what `-c` cannot delete
  (a user-config table entry other than a hook, plugin or skill: not probed).

### OpenCode: `OPENCODE_CONFIG` merged file, or a relocated config dir

- **A merged file in `OPENCODE_CONFIG` cannot remove by omission**: the user's `opencode.json` and plugin dir still load
  first (§4: o1-o5); the file only overrides, adds, `-id`s plugins and `disabled`-replaces MCP servers.
- **Relocate the config dir instead (probe p1)**: `XDG_CONFIG_HOME=<state dir>` moved only the `config` path
  (`opencode debug paths`: `data`, `state`, `cache`, `log` unchanged, so the session db and the login stay the user's). With a
  merged `opencode/opencode.json` (the user's file minus `mcp.servers.herd`, plus a key) and `opencode/plugins/` holding only
  `herd.js` (the herdr plugin file left out): **only `herd.opencode` loaded, no MCP server, no `herdr.opencode`**,
  and the account-level providers document still loaded (`/api/config`), which suggests the login is intact. A model turn
  was not run. So omission works there for MCP, plugins, `cli.json` (the TUI plugin file lives in that dir too, so it can
  be copied minus entries) and skills in `~/.config/opencode/skills`.
- Costs: the per-start dir must carry whatever the person keeps (themes `tui.jsonc`, `cli.json`, plugin packages: copy or
  link, minus the off list); `OPENCODE_CONFIG_DIR`, `OPENCODE_DISABLE_PROJECT_CONFIG` and `OPENCODE_CONFIG_PROJECT_DISABLE`
  exist in the binary's strings but were not probed.
- **Better for the common case**: `OPENCODE_CONFIG` with `-id` entries and full-object MCP `disabled` (§4): no copy.

### Comparison

| Design | Keeps every unnamed user setting | Switches one hook/plugin off | Login and history | Cost / risks |
|---|---|---|---|---|
| Claude, per-key `--settings` (T-1211, §1) | yes, the user source stays | plugins, skills, any key: yes; **one hook: no** | untouched | smallest; no restore |
| **Claude, owner's variant (a): `--setting-sources project,local` + merged file in `--settings`** | every `settings.json` key, as the user layer, but at flag rank; **not** CLAUDE.md, rules, skills, claude.ai sync | **any hook, plugin, skill, key** | untouched | file per start; drop project-set entries; optional restore dir |
| Claude, merged `settings.local.json` | as (a) | as (a) | untouched | written into the repo; Claude writes back; collides with the person's file |
| Claude, per-start `CLAUDE_CONFIG_DIR` (memory, rules, skills linked, settings merged) | would keep them natively | any | **lost**: the scratch config dirs of §1-§2 (c0-c7) all showed "Not logged in" with a copied `.claude.json` (keychain entry per config dir) | rejected |
| Codex `-c` | yes | hook, plugin, skill, any key | untouched | smallest |
| Codex merged `CODEX_HOME` | what is linked or copied | anything it omits | login via `auth.json` link works; history and refresh-token risk | link set, hash copying, credential risk |
| OpenCode `OPENCODE_CONFIG` merged file | yes | plugin (`-id`), MCP (full object), not by omission | untouched | smallest |
| OpenCode relocated `XDG_CONFIG_HOME` | what is copied or linked | anything it omits (incl. `cli.json`) | data dir untouched | copy set; login not turn-tested |

**Recommendation.** The owner's variant is the right design **for Claude when something must be removed that the
per-key flags cannot remove (a hook)**, and it works as stated; write it as the `--settings` file in `.obor/state` (a),
never into the worktree (b). Use the per-key design (T-1211) as the default and switch to the merged file only when
the override config switches off a hook, so the common start keeps the user source and the user's memory, rules and
skills without a restore dir. Both paths build from the same override object (§5); the merged file is that
object applied to a copy. For **Codex** and **OpenCode** copy-and-merge is not the better way: `-c` and `OPENCODE_CONFIG`
do the same without copying a home (Codex: credential and history risk), and OpenCode's omission case is covered by the
relocated config dir only if the owner needs removal of things `-id` and `disabled` cannot reach.

## 6. Not verified, and side effects of the probes

Not verified: Claude `model`/`effortLevel` flag overrides by observation; a deny rule outranking an allow across layers;
synced claude.ai skills under `--add-dir` (none in the scratch layer); Codex inline `[hooks]` and plugin hooks;
OpenCode `OPENCODE_CONFIG_CONTENT` vs `OPENCODE_CONFIG` order, `cli.json` plugins, policies. All results are single runs.

Side effects: `~/.claude.json` has trust entries for the scratch dirs under the session scratchpad (`…/p1/proj`) and a
dozen small transcripts sit under `~/.claude/projects/-private-tmp-…p1-proj`. Real model turns: two one-question turns
(sonnet, low effort) on the real login, one short accidental turn from a mis-sent key sequence in a probe pane, three
`OK` turns on Codex in the scratch home. The scratch `CLAUDE_CONFIG_DIR` (which held a copy of `~/.claude.json`) and the
scratch `CODEX_HOME` (which held a copy of `auth.json`) were deleted; `~/.codex/config.toml` has the same md5 before and
after; `~/.claude/settings.json` and the OpenCode config were never written. The herdr session `uso` was stopped and
deleted (twice: the follow-up used a second one of the same name), the OpenCode probe servers (ports 49731-49741) were stopped.
Follow-up probes: two more Claude starts on the real login with no turn, one Codex turn ("OK") in a per-start home
whose `auth.json` was a symlink (removed; the real file's mtime and size were unchanged, `config.toml` and `hooks.json`
md5 unchanged), one rule saved through `/permissions` into a scratch project's `.claude/settings.local.json` (the option
"User settings" was not chosen), scratch dirs under the scratchpad deleted (links into `~/.claude/skills` and
`~/.config/opencode/plugins` were removed with their parent dirs; the targets are intact). The obor guard refused one
`rm` that named a link into the herd-kit checkout by path; the dirs were removed by their parents instead. Not verified in
the follow-up: a Codex token refresh through the linked `auth.json`, `codex resume` across homes, an OpenCode model turn
under a relocated config dir, the `User settings` save destination under the trim (dialog only). Reading `~/.config/opencode/service.json` printed
its server password into this session's transcript (not used, not in this doc).

## 7. Sources and probes

- Docs fetched 2026-11-10: learn.chatgpt.com/docs/{hooks,config-file/config-reference,config-file/config-basic,config-file/config-advanced}
  (the developers.openai.com/codex paths redirect there); opencode.ai/v2/docs/{config,plugins,mcp-servers,permissions,skills,policies,cli}.
  Claude behavior is from probes (debug log, `/context`, `/permissions`, `/hooks`, hook logs) and agent-skills-scope.md §1.1.
- Local files read: `~/.claude/settings.json`, `~/.claude/hooks/herdr-agent-state.sh`, `~/.codex/config.toml`, `~/.codex/hooks.json`,
  `~/.config/opencode/{opencode.json,cli.json}`; obor: internal/agents/claude/claude.go, codex/codex.go,
  kinds/opencode/kind/kind.go, `obor spec show T-1211`.
- Probe names used above: Claude r1-r5 (hooks), base/trim/reinject/reinjectfull (real user layer), c0-c7 (scratch user
  layer), live1/live2 (model turns), e1 (`HERDR_ENV`); Codex x0-x4, f1-f3; OpenCode o0-o5. Scripts and logs (not committed):
  the session scratchpad `p1/`, `ev/`.
