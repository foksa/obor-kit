# OpenCode leftovers in obor's Go code

Question: after OpenCode became a plugin kind (`kinds/opencode`, protocol `docs/agents/kind-protocol.md`,
`internal/agents/plugin`), what OpenCode knowledge is still in non-test Go under `internal/` and `cmd/`?

## Answer

`grep -rniI opencode internal cmd --include='*.go'`, minus `_test.go`, gives 59 lines in 31 files. (`kinds/` is out of
scope; the grep also finds `internal/web/dist/assets/index-*.css`, a built asset, and `web/src/pages/Setup.svelte`
(`.k-opencode` colour); neither is Go, noted at the end.)

- **(a) comment/example only:** most of the lines (the last table row and `cmd/obor/main.go`). Nothing to do.
- **(c) generic code citing OpenCode as an example:** the code branches on a `Caps` field, not the name. Two
  carry a hard-coded name in a user-visible message (below).
- **(b) real special cases: 5** (4 in `config/models.go`, 1 in `setup/rename.go`); the rename one is probably dead code.
  (The line counts per class are approximate: the table groups lines by site, not one row per line.)

The protocol covers none of the (b) items today: the manifest has no field for base args, hc-required args, a built-in
model, or a lane config file. The best two moves are: delete `setup/rename.go`'s lane `opencode.json` rewrite, and move
`KindBaseArgs` / `HCRequiredArgs` for OpenCode out of `config/models.go` (a manifest field, or into the kind's `start`).

## Table

Line numbers are as of `staging` at 75c92076.

| file:line | class | what it is |
|---|---|---|
| `internal/config/models.go:31` | **(b)** | `BuiltinModels`: `{"deepseek","opencode","opencode-go/deepseek-v4.1-flash"}`, a built-in model name mapped to the kind and its id. |
| `internal/config/models.go:43` | **(b)** | `KindBaseArgs["opencode"] = {--auto, --standalone}`: the args every OpenCode start begins with (`models.go:191` seeds `m.Args` from it). |
| `internal/config/models.go:49` | **(b)** | `HCRequiredArgs["opencode"] = "--standalone"`; `validate.go:422` refuses an hc-project model of that kind without it. |
| `internal/config/models.go:65` | **(b)** | `KitKinds = {"opencode": "obor-kind-opencode"}`: which plugin binary the kit ships. Used at `models.go:73,78,113`, `edit.go:375`, `validate.go:264`, `testkit.go:37,407`. |
| `internal/setup/rename.go:275-322` (+ `setup.go:47`, `rename.go:5,15`) | **(b)** | `readLanes`/`kitPaths` read and rewrite `<lane worktree>/opencode.json`, replacing a quoted `…/bin/obor` path with this kit's shim; reports `<lane>/opencode.json` as moved. |
| `internal/config/validate.go:254` | (c) | Message `required (claude, codex, opencode)`: a hard-coded list of kinds in an error (the code beside it uses `agents.Names()`). |
| `internal/config/validate.go:693-697` | (c) | The condition is generic (`Caps().ModelArg && m.ID == ""`), but the WARN text says "opencode model without a model id" and suggests `opencode-go/deepseek-v4.1-flash` for any such kind. |
| `internal/view/view.go:516` | (c) | `pluginMissing` branches on `Caps().Hooks == HooksPlugin` / `ProjectPlugin`, not the name; comment cites opencode. |
| `internal/guard/session.go:96` | (c) | Writes the plugin marker when `Caps().Hooks == HooksPlugin`; comment says "an opencode lane's plugin". |
| `internal/hc/control.go:149` | (c) | `turnEnded` treats `idle` and `done` as the same ready state in both directions; comment says OpenCode never says done. Generic as coded (no kind name). |
| `internal/launch/restore.go:213`, `launch.go:503` | (c) | `agents.Resumes(kind)` / `Caps.Resume`; comment cites OpenCode as the kind that resumes nothing. |
| `internal/launch/launch.go:9,819,848,850` | (c) | Comments citing `OBOR_OPENCODE_MODEL` and OpenCode's env as examples; code unsets every registry var generically (`agents.StartVars`). |
| `internal/agents/agents.go:743,770` | (c) | `Resumes`, `ArgsChecker`: generic; comments cite OpenCode (`--model`). |
| `internal/agents/plugin/calls.go:210`, `plugin.go:319` | (c) | Generic; comments cite OpenCode's model id and `kinds/opencode/kind.Handle`. |
| `internal/testkit/testkit.go:30-37,390,407` | (c) | Test harness imports `kinds/opencode/kind` as the in-process plugin kind and writes `agents.kinds.opencode.plugin` into test configs. Test-only support code in a non-`_test` file; it is the reference kind, so correct. |
| `cmd/obor/main.go:22` | (a) | Comment only. |
| `internal/background/background.go:287`, `app/sessions.go:22`, `hc/panes.go:33`, `launch/versions.go:16`, `launch/stop.go:341`, `config/models.go:311`, `setup/show.go:64`, `setup/init.go:135`, `setup/plugin.go:154`, `setup/mcp.go:19,182`, `setup/oborinit.go:636-637`, `setup/agent.go:320`, `agents/agents.go:5,101,124`, `project/sweep.go:50`, `project/plugin.go:117`, `guard/shell.go:476`, `guard/stopfailure.go:38`, `guard/guard.go:17`, `store/sessions.go:21` | (a) | Comments and examples only (code beside them is kind-neutral). |


## (b) items, ranked by value

1. **`setup/rename.go:275-322`: delete (probably dead).** The OpenCode kind no longer puts a config in the lane
   worktree: `kinds/opencode/kind/kind.go:39-40,176,185` writes `opencode.json` as a state file
   (`.obor/state/opencode.json`, `OPENCODE_CONFIG={file:opencode.json}`), regenerated each start with the current kit
   path. No Go code under `internal/` writes `<lane>/opencode.json` (`grep` shows only this reader). The rewrite
   would only ever touch a leftover from the pre-plugin design. The protocol needs no field. Delete `readLanes`,
   `kitPaths`, `laneOpen`, `kitPath`, rename's "3 files" step and the comments in `setup.go:47`/`rename.go:5,15`.
   Caveat: a user's own committed `opencode.json` is not rewritten either way (the regexp only matches `…/bin/obor`),
   but I did not check the rename tests for a fixture that depends on it. Verify by running the rename tests after.
   Value: removes a hard-coded kind filename and a whole rename step plus its failure mode (an unreadable file refuses
   the rename).
2. **`HCRequiredArgs["opencode"]` (`models.go:49`, check at `validate.go:422`) and `KindBaseArgs["opencode"]`
   (`models.go:43`).** These are one concern: OpenCode's launch flags live in core. No manifest field; `start`
   already returns `args`, so the kind can add `--auto --standalone` itself. Smallest move: (i) the kind's `start`
   prepends its base args, `KindBaseArgs["opencode"]` goes; (ii) `HCRequiredArgs` becomes either a manifest field
   (e.g. `hc_required_args`, one flag, read from the cache like `vars`) or goes with (i) if `start` always emits it
   and the hc check is replaced by "the start's args contain it". The compiled kinds' entries (claude, codex) stay in
   Go. Cost: ordering (`models.go:191` puts base args before the model id and before `kinds.<k>.args`) and a
   kind with no cached manifest has no answer, so `config validate` would need the manifest for this check (it
   already needs it for `pluginKind`). Value: a project's own plugin kind cannot get base args or the hc guard today;
   OpenCode's are the only plugin-kind special case in `config`.
3. **`BuiltinModels` `deepseek` row (`models.go:31`).** A model alias naming the OpenCode kind and a provider-specific
   id; no manifest field. This is config data, not behaviour: `models.<name>` already overrides or adds one. Smallest
   move: keep it, or move it to `templates/config.example.json` (init copies the example), which would stop `obor`
   knowing `deepseek` without a config line. It is not a protocol matter. Value low; the move changes what a project
   that uses `deepseek` without listing it gets, so it is a product decision (the lead's).
4. **`KitKinds` (`models.go:65`): keep.** It is the kit's registry of shipped plugin binaries, not OpenCode behaviour:
   the kit ships `obor-kind-opencode` and obor must name it to default it (`config.KitKind` resolves it beside the
   running binary). The only hard-coded name is the single map entry; a second shipped kind adds a line, not a branch.
   A glob over `dist/obor-kind-*` would avoid the entry but would make `agents.kinds` depend on the install layout.
   Value of change: low.

## Small fixes among the (c) lines (not protocol items)

- `config/validate.go:693-697`: the warning prints "opencode" and an OpenCode model id for any kind with
  `Caps.ModelArg`. Use `m.Kind` in the text; drop the example id (or take it from the kind). One line, a visible
  wrong message for a second model-arg plugin kind.
- `config/validate.go:254`: `required (claude, codex, opencode)` should be built from `agents.Names()`.

## Not Go

`internal/web/dist/assets/index-CAZ05-v3.css` (built from `web/src/pages/Setup.svelte:449`, `.k-opencode { --c:
var(--done) }`) colours a kind chip by its name; the web UI also special-cases kind names in CSS only. Not checked
further.

## Unverified

- Whether any rename test depends on `<lane>/opencode.json` (item 1): I did not read the tests.
- Whether deleting `HCRequiredArgs["opencode"]` changes any `config validate` golden output: not run. No code was
  run or changed; this is a read-only census.
