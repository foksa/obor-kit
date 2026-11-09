# Upgrading an obor project from v0.31 to v0.32

## v0.32.0 (the staging→main promotion and the baseline, gate and e2e commands are gone)

The promotion (v0.30, `branches.promote`) is deleted. Tasks merge into `branches.main` as before. A project that set
`branches: {main: staging, promote: main}` keeps `staging` as its target, and `main` no longer moves on its own: move it
when you release. `commands.after_merge` runs after every merge in the main checkout, and obor no longer sets
`OBOR_ROOT` or `OBOR_PROMOTION`. A promotion needs-you still open from v0.31 ends at the first pass; a
`[obor] needs-you promote: …` line queued before the upgrade may still reach the lead once: ignore it.

`commands.baseline`, `gate` and `e2e` are no longer run by name at merge or nightly, and `OBOR_BASELINE` / `OBOR_GATE`
are no longer set. To keep running one, name it in a merge class's `checks`, in `merge.guard` or in
`merge.nightly_commands`, or move it to `check_milestone` (the nightly). A command's `dir`, `paths` and `at` keys are
gone.

A key this obor does not know no longer stops any command: serve, the CLI and the hooks warn once and go on. Only
`obor config validate` refuses one, so **a project whose `merge.guard` runs `obor config validate` fails every merge's
guard until the keys are gone**. Re-run the installer, then in every project, before `obor serve restart`:

```sh
# Keys v0.32 no longer reads: the promotion's (branches.promote, merge.promote_weight, merge.weights), v0.29's
# (merge.receipt_max_age, checks), a command's dir, paths and at, and commands.baseline, gate and e2e unless a merge
# class, merge.guard or merge.nightly_commands still runs one by name.
prog='def used($n): (.merge.guard == $n) or (((.merge.nightly_commands // []) | index($n)) != null)
    or (([.merge.classes[]?.checks[]?] | index($n)) != null);
  del(.branches.promote, .merge.promote_weight, .merge.weights, .merge.receipt_max_age, .checks)
  | if (.commands | type) == "object" then .commands |= with_entries(
      if (.value | type) == "object" then .value |= del(.dir, .paths, .at) else . end) else . end
  | reduce ("baseline", "gate", "e2e") as $n (.; if .commands[$n]? == null or used($n) then . else del(.commands[$n]) end)'
for f in .obor/config.json .obor/local.json; do
  [ -f "$f" ] || continue
  jq "$prog" "$f" > "$f.new"
  if [ "$(jq -S . "$f")" = "$(jq -S . "$f.new")" ]; then rm "$f.new"; else mv "$f.new" "$f"; echo "updated $f: commit it"; fi
done
obor config validate
```

Commit `.obor/config.json` if the block says it changed it, then `obor serve restart`. A `WARN commands.<name>: obor
no longer runs it…` left by `obor config validate` names a command you set but nothing runs: delete it, or name it as
above.

A project that ran the promotion may delete what it left behind: its worktree (`git worktree remove --force
<merge.scratch_dir>-promote`, obor-kit's `../obor-kit-wt/merge-promote`), `.obor/state/last-promotion` and
`.obor/state/logs/promote-check.log`.

## v0.32.0 (`kinds.opencode.command` stays optional until v0.33)

v0.31 said a config without `kinds.opencode.command` would stop running the kit's OpenCode binary in v0.32. It still
runs it in v0.32; v0.33.0 drops that default. A project whose `obor config validate` prints `WARN
kinds.opencode.command` runs `obor setup agents` (it writes `kinds.opencode.command = "obor-kind-opencode"`,
uncommitted) and commits `.obor/config.json`.
