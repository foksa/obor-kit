# Upgrading an obor project from v0.45 to v0.46

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

**OpenCode: a lane's agent starts without `OPENCODE_CONFIG_CONTENT`**, so inline config from your shell no longer
reaches workers or drive agents. The pane's shell keeps it; the lead and role agents keep it. Put what a lane needs in
`.obor/opencode/opencode_override.json` or the project's `opencode.json`.

**agy:** only `slug<TAB>label` lines of `agy models` count as models. A start that checks the cached list says so,
unless the live call timed out, and a refused model names the list it was checked against.

**The guard records each shell command its parser cannot read** in `.obor/state/unparsed.jsonl` (role, the parser's
error, the command's first 200 characters). The command is still allowed.
