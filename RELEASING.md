# Releasing obor

Install (a new machine; re-running is the upgrade):

```sh
curl -fsSL https://raw.githubusercontent.com/foksa/obor-kit/main/install.sh | sh
```

The lead, in a clean checkout on main:

1. Write `docs/UPGRADE-v<x.y>.md` with a `## v<x.y.z>` heading — the release's guide, at tag time from
   `git log v<prev>..` (every merge's spec is in its message), not task by task: a guide written mid-milestone misses
   the last merges (DD-v0.14 U1). Commit it on main.
2. `make release V=x.y.z` (`DRY=1` prints the steps and changes nothing). It refuses off main, on a dirty tree, when
   `V` is not above `VERSION`, when the guide has no `v<x.y.z>` heading, or when a merge after the last `RV-<n> closed`
   commit changes anything but docs (`docs/` or a `.md` file): run `obor review` and close it first. `UNREVIEWED="<why>"`
   overrides the last refusal (the owner's call only) and puts the reason in the tag message. Then it sets `VERSION` and
   the plugin `version`, runs the full `make check` (a failure commits nothing), and commits them on main. In a scratch
   worktree at that commit it sets the marketplace `name` to `obor-kit`, builds the four binaries and `bin/checksums.txt`,
   commits and tags `v<x.y.z>`. It copies the four files and the catalog back to main, commits them, and removes the
   worktree. A re-run resumes where it stopped (the version commit, the tag and the catalog commit are not redone); a
   local tag whose bytes differ is refused, and a pushed tag never moves.

The owner:

3. `make ship` (with `OBOR_ALLOW_PUSH=1` inside): pushes main and the tag, runs `scripts/publish` (the GitHub release),
   then `install.sh` on this machine. Each step is skipped when it is already done (origin has the tag, the GitHub
   release exists, the installed `obor` is this version).
4. `obor serve restart` in each project (`make ship` names the ones launchd knows): a project picks the new kit up
   at restart.

Guarantees: main keeps marketplace `obor-kit-dev` (only the tag ships `obor-kit`); `scripts/publish` uploads exactly
the four binaries the tag's `bin/checksums.txt` names, and the catalog stays cumulative. A pushed tag never moves: a
release that needs other bytes or another catalog is a new (patch) version.
