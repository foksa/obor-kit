# Releasing obor

Install (a new machine; re-running is the upgrade):

```sh
curl -fsSL https://raw.githubusercontent.com/foksa/obor-kit/main/install.sh | sh
```

The lead, in a clean checkout on main:

1. Write `docs/UPGRADE-v<x.y>.md` with a `## v<x.y.z>` heading — the release's guide, at tag time from
   `git log v<prev>..` (every merge's spec is in its message), not task by task: a guide written mid-milestone misses
   the last merges (DD-v0.14 U1). A project's upgrade steps go in a fenced `sh` block under their version's heading
   (what the lead's project runs in its main checkout after the installer, before `obor serve restart`; the upgrade
   check below runs it): a release that changes the minor (`x.y.0`) must have one, `# nothing to do` when there is
   nothing; a patch may. Also write `docs/releases/v<x.y.z>.md`, the release's GitHub notes: plain
   user-facing Markdown, a summary line, then `- **Name:** what changed` bullets, then an optional `Upgrading:` line,
   no task ids. `scripts/publish` titles the release `obor <x.y.z>`, publishes this file (as tagged) and appends the
   Full Changelog link. Commit both on main.
2. `make release V=x.y.z` (`DRY=1` prints the steps and changes nothing). It refuses off main, on a dirty tree, when
   `V` is not above `VERSION`, when the guide has no `v<x.y.z>` heading, when `docs/releases/v<x.y.z>.md` is missing or
   empty, or when a merge after the last `RV-<n> closed` commit changes anything but docs (`docs/` or a `.md` file):
   run `obor review` and close it first. `UNREVIEWED="<why>"` overrides the last refusal (the owner's call only) and
   puts the reason in the tag message. Then it sets `VERSION` and
   the plugin `version`, runs the full `make check` (a failure commits nothing), and commits them on main. The whole run is also written to
   `.obor/state/logs/release-<x.y.z>.log` (a re-run appends; `--dry-run` writes none), and every `FAIL` line names that log.
   When `make check` fails only in `go test` packages, those packages run once more in one `go test <pkgs>`: if all
   pass, the release goes on, prints `FLAKY: <pkg> <test> (failed, passed on rerun)` and records it in the flake log
   (`obor flakes`); if any fails again it is refused, naming the tests. A failure outside `go test` (build, vet, gofmt,
   deadcode) refuses at once, with no rerun. Then `make upgrade-check` (runnable alone; `PREV=x.y.z` another
   previous release, `0.23.0` or later: an older `PREV` is refused before anything runs, as the check's fixture names
   its spec's `Model:`): the catalog's newest release below the new version (its binary from `~/.obor/bin`, fetched and
   checked against `bin/checksums.txt` when missing; its kit the tag's tree) runs `obor init` for two projects in a
   temp `HOME` and `OBOR_STATE_HOME` (herdr, claude and launchctl stubbed: no agents, no launchd) and serve for both as
   child processes, with a line to each lead held (no session yet) and a dispatch waiting (`--after`, its spec's
   `Model:`); then the guide's `sh` blocks of the versions above it run in each project with the new `obor`, and both
   serves start again on the new build (`dist/obor`); then both must answer, `obor projects` list both, `obor status`
   and `obor setup show` load, each lead (its SessionStart hook under its current name) get its held line, the dispatch
   still wait with its model first, and `obor send` work both ways. A failure prints the step and its output and tags
   nothing (the version commit stays: the re-run starts at the upgrade check). `SKIP_UPGRADE_CHECK="<why>"` skips it,
   the owner's call only, like `UNREVIEWED`, and puts the reason in the tag message. In a scratch
   worktree at that commit it sets the marketplace `name` to `obor-kit`, builds the four binaries and `bin/checksums.txt`,
   commits and tags `v<x.y.z>`. It copies the four files and the catalog back to main, commits them, and removes the
   worktree. Each step has its done evidence and its undo (`scripts/release`'s header lists them): a re-run skips a step
   whose evidence holds (the `obor v<x.y.z>: …` version commit, the tag, main's catalog with the four files matching it)
   and a failed step is undone (the bump's or the catalog's index and tree back to `HEAD`, the scratch worktree removed),
   so the re-run starts from a clean tree. An existing tag is never touched: one whose bytes differ, whose tree has no
   non-blank `docs/releases/v<x.y.z>.md` (notes committed on main after the tag do not repair it), or that predates
   code merged on main after it (anything but docs and `bin/checksums.txt`), is refused — release a new patch version.

The owner:

3. `make ship` (with `OBOR_ALLOW_PUSH=1` inside): first the tag's own checks (`scripts/publish --check`) and the same
   stale-tag check as `make release`, against the release's `checksums: v<x.y.z>` commit on main instead of HEAD; then
   pushes main up to that commit and the tag, runs `scripts/publish` (the GitHub release), then `install.sh` on this
   machine. Each step is skipped when it is already done and checked after it runs: origin has the tag (the same
   object) and a main containing that commit; the GitHub release is not a draft and has the four assets (a release
   missing some gets only those, via `scripts/publish --upload`; a draft stops with the `gh release edit` command); the
   installed `obor` is this version. Ship publishes main up to the release; later merges wait for the next release.
   Its last step tells the leads, one `obor send <project> "obor v<x.y.z> is installed. <docs/releases/v<x.y.z>.md at
   the tag>"` per project of `obor projects` but the kit's own; a send that fails is reported, the rest go out, and ship
   then fails with the repair command. No config advice is generated: a release whose projects need a config change
   says so in its notes, and the lead sends that change to the project's lead before shipping.
4. `obor serve restart` in each project (`make ship` names the ones launchd knows): a project picks the new kit up
   at restart.

Guarantees: main keeps marketplace `obor-kit-dev` (only the tag ships `obor-kit`); `scripts/publish` titles each GitHub
release `obor <x.y.z>` with the notes of `docs/releases/v<x.y.z>.md` at the tag and a Full Changelog link to the newest
earlier numbered release — a failed `gh release list` stops it before it creates anything, and a prerelease or
non-`x.y.z` tag is never that baseline; it uploads exactly the four binaries the tag's `bin/checksums.txt` names, and the
catalog stays cumulative. A pushed tag never moves: a release that needs other bytes or another catalog is a new
(patch) version.
