# Releasing obor

Install (a new machine; re-running is the upgrade):

```sh
curl -fsSL https://raw.githubusercontent.com/foksa/obor-kit/main/install.sh | sh
```

Steps 1–3 run in a scratch worktree at main (`git worktree add --detach <dir> main`), never in the main checkout:
main keeps marketplace `obor-kit-dev` (RV-64 #4). Afterwards copy `bin/checksums.txt` and `VERSION` to main and commit
them there; nothing else of the release commit goes to main.

1. Update `VERSION` and `.claude-plugin/plugin.json`'s `version` (the plugin version keys Claude's cache dir, so the two
   must match) and set `.claude-plugin/marketplace.json`'s `name` to `obor-kit` (main's is `obor-kit-dev`, the dev
   checkout's; the release tag ships the name users install, and `make publish` refuses a tag that does not say
   `obor-kit`) and review the changes. Write the release's `docs/UPGRADE-v<x.y>.md` at tag time from
   `git log v<prev>..` (every merge's spec is in its message), not task by task: a guide written mid-milestone misses
   the last merges (DD-v0.14 U1).
2. Run `make release`. It creates four files in `dist/` and updates `bin/checksums.txt`. The checksum file is cumulative: each release replaces that version's four entries and keeps entries for every other version.
3. Commit the updated `bin/checksums.txt` (with `VERSION` and the plugin version), tag that commit `v$(VERSION)` and push
   the tag, then run `make publish`: `scripts/publish` checks the tag's `VERSION`, plugin version and `bin/checksums.txt`
   against the four `dist/obor-$(VERSION)-<os>-<arch>` files (darwin/linux × arm64/amd64), then `gh release create`
   uploads exactly those four to the GitHub release for that tag. Publishing is its own explicit step; nothing else calls it.
   A pushed tag never moves: a release that needs other bytes or another catalog is a new (patch) version (RV-63 #4:
   publish checks the local tag, so a moved local tag could upload bytes the remote tag's catalog rejects).
4. Restart serve after a schema change: a release that changes the store's schema (`docs/state-model.md`) needs `obor serve` restarted on every project at once, because a running serve keeps the old schema in memory.
