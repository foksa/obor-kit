# Releasing obor

Install (a new machine; re-running is the upgrade):

```sh
curl -fsSL https://raw.githubusercontent.com/foksa/obor-kit/main/install.sh | sh
```

Everything that stays goes on main first: `VERSION`, the plugin `version` and `docs/UPGRADE-v<x.y>.md` (step 1, committed
on main). Only the marketplace `name` flip, `make release` and the tag happen in a scratch worktree at that commit
(`git worktree add --detach <dir> main`), so main keeps marketplace `obor-kit-dev` (RV-64 #4). Afterwards copy
`bin/checksums.txt` from the tag to main and commit it; nothing else of the tag's commit goes to main.

1. On main: update `VERSION` and `.claude-plugin/plugin.json`'s `version` (the plugin version keys Claude's cache dir,
   so the two must match), write the release's `docs/UPGRADE-v<x.y>.md` at tag time from `git log v<prev>..` (every
   merge's spec is in its message), not task by task: a guide written mid-milestone misses the last merges (DD-v0.14
   U1). Review and commit on main.
   In the scratch worktree at that commit: set `.claude-plugin/marketplace.json`'s `name` to `obor-kit` (main's is
   `obor-kit-dev`, the dev checkout's; the tag ships the name users install, and `make publish` refuses a tag that
   does not say `obor-kit`).
2. Run `make release`. It creates four files in `dist/` and updates `bin/checksums.txt`. The checksum file is cumulative: each release replaces that version's four entries and keeps entries for every other version.
3. In the scratch worktree commit the marketplace name and the updated `bin/checksums.txt`, tag that commit `v$(VERSION)` and push
   the tag, then run `make publish`: `scripts/publish` checks the tag's `VERSION`, plugin version and `bin/checksums.txt`
   against the four `dist/obor-$(VERSION)-<os>-<arch>` files (darwin/linux × arm64/amd64), then `gh release create`
   uploads exactly those four to the GitHub release for that tag. Publishing is its own explicit step; nothing else calls it.
   A pushed tag never moves: a release that needs other bytes or another catalog is a new (patch) version (RV-63 #4:
   publish checks the local tag, so a moved local tag could upload bytes the remote tag's catalog rejects).
4. Restart serve after a schema change: a release that changes the store's schema (`docs/state-model.md`) needs `obor serve` restarted on every project at once, because a running serve keeps the old schema in memory.
