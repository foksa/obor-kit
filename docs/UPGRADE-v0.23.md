# Upgrading an obor project from v0.22 to v0.23

## v0.23.0 (the config names models directly)
The config shape changes, and v0.23 refuses the old one ("run obor setup migrate"). Serve does not start, and every
`obor` command in the project fails, until it is migrated. Do these in this order, in each project:

1. Re-run the installer.
2. Wait until every lane is free (no task working, gating or merging): `obor status`. Lanes not named `L-NN` (D-01,
   OH-01, O-01 …) are renamed, worktrees and pane labels included, only while every lane is free.
3. `obor setup migrate` in the main checkout. It prints the old and new `.obor/config.json` and commits it. Run it
   again if `.obor/local.json` holds lane types or role objects (it rewrites that file too, not committed).
4. `obor serve restart`, then `obor status` and `obor setup show`.

What changes:
- `difficulty` levels and `roles` are model refs (`opus:high`, `deepseek`, `terra:xhigh`); the first entry of a level
  is the default. A spec's `Type:` line is now `Model:` (`obor spec set` refuses `Type:`); `obor dispatch --types` is
  now `--model`. `obor pause --model <name> "<why>"` skips one model.
- `lanes` is a count. Lane types, typed lanes and caps (`max`) are gone; lane-type `settings` are dropped.
- Roles are strings: their agent names derive from the prefix (`<prefix>-rev`, `<prefix>-advisor` …) and they run in
  the main checkout; `roles.<role>.agent` / `.worktree` overrides are dropped (an integrator that ran in its own
  worktree, like axis-alies' O-02, now runs in the main checkout). `migrate` relabels the role panes it renames.
- Shared args per agent kind go to `kinds.<kind>.args`; a model obor does not know goes to `models.<name>`.
- Restart every Claude pane once (hooks read the new config).
