# Upgrading an obor project from v0.45 to v0.46

## v0.46.1 (agy: lane worktrees trusted, the kit's role files readable, a resume checks its model)

From v0.46.0. Nothing to change in a project.

```sh
# after updating the kit, in the project
obor version                     # fetches this release's binary
```

**agy:** a lane's first start no longer stops at "Trust this folder?" (obor trusts the worktrees it made, in the
start's own settings; your saved agy settings are not written), so obor's MCP server is there from the first start.
A worker reads its role file under the kit's `templates/` with no file-access prompt. A resume refuses a model
`agy models` does not list, as a fresh start does.

**Nightly:** a green nightly whose non-gating failures changed sends the lead one line.

## v0.46.0 (no obor-kind-* binaries; validate checks lane args; OpenCode lanes leave out inline config)

From v0.45.0. Nothing committed changes; most projects have nothing to do.

```sh
# after updating the kit, in the project
obor version                     # any obor command: it fetches this release's binary (hooks never fetch)
obor config validate             # a model whose kind refuses its args on a lane: see below
jq '.agents.kinds // {} | map_values(.plugin // empty)' .obor/config.json   # see "No obor-kind-* binaries"
```

**No `obor-kind-*` binaries.** The kit no longer builds, ships or fetches them; obor answers its OpenCode and agy kinds
itself.

- A `plugin` line naming `obor-kind-opencode` or `obor-kind-agy`, by name or by a path ending in it, runs in process
  like no line. Delete it or keep it.
- obor's state sweep removes `.obor/state/kinds/agy.json` and `.obor/state/kinds/opencode.json` (a third-party
  kind's file stays). Old `obor-kind-*` files in the kit's bin directory are left alone; delete them if you like.

**`obor config validate` names a model whose kind would refuse its args on a lane's start** (for Codex, args that set
a key under `mcp_servers`, also written `-ckey=value`): FAIL when a worker difficulty level names the model, WARN
when only a role does. Move such keys to the project's committed config for that CLI.

**OpenCode: a lane's start unsets `OPENCODE_CONFIG_CONTENT`**, so inline config from your shell no longer reaches
workers or drive agents (the lane pane's shell loses it too). The lead and role agents keep it. Put what a lane needs
in `.obor/opencode/opencode_override.json` or the project's `opencode.json`.

**agy:** only `slug<TAB>label` lines of `agy models` count as models. A start that checks the cached list says so,
unless the live call timed out, and a refused model names the list it was checked against.

**The guard records each shell command its parser cannot read** in `.obor/state/unparsed.jsonl` (role, the parser's
error, the command's first 200 characters). The command is still allowed.
