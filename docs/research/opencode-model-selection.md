# opencode v2: selecting model and variant from a plugin or from outside

Research for the owner, 2026-11-02, opencode v2.2.0 (binary strings, the running server's `/openapi.json`, the v2
docs: https://opencode.ai/v2/docs/build/plugins, …/plugins/server, …/plugins/cli, https://opencode.ai/v2/docs/models).
Run as a lead subagent (should have been a role pane). No agent was started.

## Answer
Yes, as a separate project. The server plugin context has `ctx.session.switchModel`, and the HTTP API has
`session.create` and `session.switchModel` (`POST /api/session`, `POST /api/session/{id}/model`), both taking a
`Model.Ref` `{providerID, id, variant?}`. Model and variant can be set per session from outside the TUI.

## Findings
1. **Server plugin hooks** (v2: `export default {id, setup(ctx)}`). Hooks firing in 2.2.0: session `prompt`,
   `context`, `compaction`, `generate`, `title`, `model.request`, `retry`, `http.request`/`response`,
   `experimental.ws.*`, tool `execute.before`/`after`, permission `evaluate`, shell `create.before`. No `chat.params`
   (v1). No hook rewrites the model: `context`/`generate` see it, but only `system`, `messages`, `tools`, `options`
   are read back (raw `options` such as `reasoningEffort` can be forced per request, below the variant level);
   `model.request` changes only `baseURL`/`headers`. The context offers `ctx.session.create/switchModel/prompt`,
   `ctx.event.subscribe`, `ctx.agent.transform` (an agent's `model` and `variant`), `ctx.model.transform`
   (`default.set(provider, model)`, no variant). So: per session (`switchModel`) or per agent (`agent.transform`),
   not per message.
2. **TUI plugins** (`setup(api)`; `plugins` in `cli.json` or a package's `./tui` export): `ui.model.current()`,
   `ui.model.variant.list()/set(v)`, `keymap.layer({commands:[{bind, palette, slash, run}]})`, `ui.dialog.select/prompt`,
   `ui.router`, `client` (the SDK, with `session.switchModel`). No `ui.model.set`: a picker sets the model only for an
   open session (via `client.session.switchModel`); on the home screen only the variant. `variant.set` writes
   `model.json` (the shared state).
3. **Outside process**: `session.create` takes `model`, `agent`, `location`; `session.switchModel` switches subsequent
   turns; `session.prompt` has no model field (the TUI switches before every submit). Reachable only on the background
   service or a `--server`; a `--standalone` TUI's private server is not reachable from outside.
4. **Shapes for a separate project**:
   - A (obor-facing, small): a server plugin reading `OBOR_MODEL=provider/model#variant` (env works under
     `--standalone`: the server runs in the pane's process) and calling `ctx.session.switchModel` once per new root
     session in the `prompt` hook. Local file in `~/.config/opencode/plugins/` or an npm name in `plugins`.
   - B (humans): a TUI package (`./tui`): a `keymap.layer` command opening `ui.dialog.select` of models and variants
     from the client's list; `client.session.switchModel` in a session, `ui.model.variant.set` on the home screen.
   - Risks: v2 plugin APIs are beta ("may change"), ws hooks experimental; the docs' `switchModel` example omits
     `variant` (schema and TUI pass it). Gate on `ctx.app.version` major 2 and feature-check
     `typeof ctx.session?.switchModel === "function"`.
5. **The `XDG_STATE_HOME` workaround**: the TUI's main command has no `--model` (root params: standalone, server, auto,
   continue, session, prompt; `run`/`mini` take `#variant`). For a new session the TUI's variant order is `model.json`
   `variant[model]` (a stored "default" wins) → the agent's variant (if the agent's model) → the config model's
   variant: why the workaround exists. For an existing session route: an in-memory pick, then the session's stored
   `model.variant`; `model.json` is not read. So creating the session first (3) and opening the TUI with
   `--session <id>` would retire the workaround, but needs a reachable server (not `--standalone`). Plugin A might
   too (unverified).

## Open (needs a live probe in a pane)
- Does a `switchModel` inside the `prompt` hook apply to the turn that prompt starts, or deadlock?
- Can the TUI's own pre-submit `switchModel` race the plugin on the first turn?
- Does `--session <new-id>` under `--standalone` create the session with the TUI's selection?
- Do both `tui.jsonc` (herdr) and `cli.json` `plugins` still load?
