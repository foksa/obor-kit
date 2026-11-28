# Upgrading an obor project from v0.47 to v0.48

## v0.48.0 (pi as an experimental agent kind)

From v0.47.x. Nothing to change.

```sh
# after updating the kit, in the project
# nothing to do
```

**pi (experimental)** can run obor's agents. obor starts it as
`pi --approve -e <kit>/pi/obor.js --model <provider/id> [--thinking <effort>]`, and the extension carries obor's hooks,
guard, MCP server and inbox. To try it, add a model of kind `pi` to `.obor/config.json` and name it where a model goes:

```json
"models": {
  "pi-sonnet": { "kind": "pi", "id": "anthropic/claude-sonnet-5-5" }
}
```

Then `Model: pi-sonnet` in a spec, `obor dispatch T-xx --model pi-sonnet`, or `pi-sonnet:high` (effort is pi's
`--thinking`: `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`). pi must be signed in to that provider
already; obor configures no provider.

- pi agents keep your own pi extensions, packages and skills, workers and roles too (pi's extensions are its
  features). A project turns one off in its `.pi/settings.json`, which pi ranks above your user settings.
- A bare `pi` from an agent's shell is denied: without a terminal pi runs headless and without obor's guard.
- Live checks: `make conformance TIER=live KIND=pi TURN=1` (`TERMINAL=hc` or `TERMINAL=rex` for those terminals).

An old project whose config has no `agents.models` now gets one hint for all of its names: `obor setup agents` adds
every former built-in model the config uses.
