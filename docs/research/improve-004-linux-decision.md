# Plan 004: Decide Linux: run the suite there once, then test it or stop claiming it

> **Executor instructions**: This is an investigation with a written result, not a code change. Follow the steps in
> order, run each command, and write what you measured, not what you expect. If anything in the "STOP conditions"
> section occurs, stop and report. When done, update the status row for this plan in
> `docs/research/improve-plans.md`, unless the lead dispatched you and keeps the index.
>
> **Drift check (run first)**: `git diff --stat a73e814c..HEAD -- README.md scripts/release-lib.sh Makefile internal/launch/session.go`
> If these changed, re-run the fact commands of step 1 and use what they print now.

## Status

- **Priority**: P3
- **Effort**: S (the write-up) to M (if a Linux machine has to be set up for the run)
- **Risk**: LOW (nothing in the product changes)
- **Depends on**: none
- **Category**: direction
- **Planned at**: commit `a73e814c`, 2026-11-10

Spec lines, if the lead turns this into a brief for the researcher role: writes `docs/research/` only; no lane.

## Why this matters

The README says obor runs on "macOS and Linux" and every release ships four Linux binaries, but no test has ever
executed on Linux: there is no CI, and the merge checks and the nightly run on the owner's Mac. One code path exists
only for Linux and so has never run. That is a claim the project cannot back today. The owner's parked idea "More
machines" (a Steam Deck, which is Linux, for heavy merge checks) would make Linux a real target. The owner needs
facts to pick one of three answers: stop claiming Linux, test it in CI, or test it by hand on a real machine before
each release. This plan produces those facts and a recommendation.

## Current state

Facts measured at commit `a73e814c` (re-check each in step 1):

- The claim: `README.md:12` ends `` each release has an upgrade guide in `docs/UPGRADE-v<x.y>.md`. macOS and Linux. ``
  `AGENTS.md:25` says `` `bin/obor` is the only bash (bash 3.2 compatible, macOS and Linux) ``.
- The binaries: `scripts/release-lib.sh:7` is `platforms="darwin-arm64 darwin-amd64 linux-arm64 linux-amd64"`;
  `Makefile:42` is `GOOS_LIST := darwin linux`. So Linux builds compile at every release.
- Type-check for Linux, tests included: `GOOS=linux go vet -tags integration ./...` exits 0 with no output. The code
  and every test compile for Linux; nothing has been *run* there.
- Linux-only code: `internal/launch/session.go:229` (`if runtime.GOOS == "linux"`): the files a process holds open
  are read from `/proc/<pid>/fd` on Linux and from `lsof` elsewhere. It is how a fresh Codex start's session is
  found.
- macOS-only feature: `internal/service` (launchd). Off macOS each `obor serve install|uninstall|restart` prints one
  line telling the user to run `obor serve` themselves; several tests skip with `launchd: darwin only`
  (`cmd/obor/service_test.go`, `cmd/obor/sleep_test.go`).
- Platform tools the code shells out to: `ps` (`internal/background/background.go:40`, `internal/herdr/panes.go:250`,
  `internal/launch/launch.go:388`, `internal/checks/gitlock.go:98`), `lsof` (`internal/checks/gitlock.go:152`,
  not behind a Linux branch), `stat` with both spellings in the Makefile's `trim-go-cache`.
- No container runtime on the owner's Mac at planning time: `command -v docker podman colima` printed nothing.
- No CI: the repository has no `.github/` directory. The repo is public (`foksa/obor-kit`); the project rule is that
  only the owner pushes.
- The tests stub herdr and the agent CLIs (`AGENTS.md`, "Tests stub herdr"), so a Linux test run needs only Go
  1.27.3, git, bash, jq and make.

Unknown, to find out or to ask: whether herdr itself and the three agent CLIs (Claude Code, Codex, OpenCode) run on
the Linux machine the owner has in mind. obor is useless without herdr.

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Compile check for Linux | `GOOS=linux go vet -tags integration ./...` | exit 0, no output |
| A container runtime | `command -v docker podman colima` | a path, or nothing |
| Export the tree (never test in the main checkout) | `d=$(mktemp -d) && git archive HEAD \| tar -x -C "$d" && echo "$d"` | a temp dir holding the tree |
| Unit tests on Linux (inside the Linux machine or container, in that dir) | `go test ./...` | per-package `ok` / `FAIL` lines |
| All tests on Linux (same place, afterwards) | `go test -tags integration ./...` | per-package `ok` / `FAIL` lines |

Run one test command at a time. Do not pass `-race` or `-count`.

## Scope

**In scope** (the only file you create):
- `docs/research/linux-support.md`

**Out of scope** (do NOT do):
- Changing `README.md`, `Makefile`, `scripts/`, any `.go` file or adding `.github/`: the result is a recommendation;
  the owner decides, then a task follows.
- Fixing a test that fails on Linux. List it.
- Installing a container runtime or anything else on the owner's Mac. If none is there, say so and skip the run
  (step 2 says how).
- Pushing anything, or running a workflow on GitHub.

## Git workflow

- The researcher role and the lead write `docs/research/` in the main checkout; an obor worker writes in its lane and
  finishes with `task_done`. Anyone else: a branch `advisor/004-linux-decision` cut from `staging`. Do not push.

## Steps

### Step 1: Re-check the facts

Run: `sed -n 12p README.md`, `sed -n 7p scripts/release-lib.sh`, `grep -n 'runtime.GOOS == "linux"' -r internal cmd`,
`grep -rn 'darwin only' --include='*_test.go' cmd internal | wc -l`, and the compile check from the table. Note each
result. They are the "Facts" section of your document.

**Verify**: the compile check exits 0. If it does not, that is finding number one; record its output and continue.

### Step 2: Run the suite on Linux once, if a Linux is at hand

Use, in this order of preference: a Linux machine the owner names; a container (`docker` or `podman`) with the
official Go image of the version in `go.mod`'s `toolchain` line, the exported tree mounted, and `git`, `jq`, `make`
and `bash` present. Inside it, in the exported tree: `git init -q && git add -A && git commit -qm tree` first if the
tests need a repository around them (many build their own fixture repos, some read `../..`), then the unit run, then
the integration run. Save both outputs.

If neither exists, do not install one. Write "not run: no Linux machine or container runtime available" in the
document and go to step 3; the options below still stand, with the first run as option B's or C's first act.

**Verify**: you have either two saved outputs with a per-package verdict line each, or the "not run" sentence.

### Step 3: Ask the two things only the owner knows

Put them in the document's last section as questions, each with your recommended answer: which Linux machine is
meant (the Steam Deck of BACKLOG's "More machines"? a server?), and whether herdr and the agent CLIs are known to
run there.

### Step 4: Write `docs/research/linux-support.md`

Follow the shape of the other notes in `docs/research/` (for example `docs/research/adopter-test-flow-advice.md`):
a title, a "Recommendation" first, then facts, then options. At most about 120 lines. Sections:

1. **Recommendation**: one paragraph, decided by this rule. If the Linux run was made and fewer than five tests
   failed: option B. If it was made and more failed, or it was not made: option A now, and B or C when "More
   machines" starts.
2. **Facts**: step 1's results, and step 2's per-package table (package, unit verdict, integration verdict, the
   failing tests by name with one line of their output).
3. **Options**, each with what it costs and what it adds to maintain:
   - **A. Stop claiming it.** `README.md:12` says "macOS; Linux binaries are built but not tested". The release keeps
     building them (no script change). Cost: one line. Nothing new to maintain. Honest, and reversible.
   - **B. Test it in CI.** One workflow file, `.github/workflows/test.yml`: on a push to `main`, on `ubuntu-latest`,
     set up the Go version from `go.mod`, run `make test` (the unit tests; add `-tags integration` only if step 2
     showed they pass). Cost: about 20 lines of YAML, a first run that may be red, a second place where a red needs
     attention, and it runs only after the owner pushes, so it reports on a release after the fact. It is the
     project's first CI.
   - **C. Test it by hand before a release.** A step in `RELEASING.md`: on the Linux machine, the two test commands
     of this plan against the release commit. Cost: one manual step per release on a machine that must be kept
     ready; no new infrastructure.
4. **What a Linux user is missing today**, from the facts: no launchd service (they run `obor serve` in a pane),
   and whatever step 2 found.
5. **Questions for the owner** (step 3).

**Verify**: `test -f docs/research/linux-support.md && wc -l docs/research/linux-support.md` → the file exists, at
most about 120 lines; `grep -c '^## ' docs/research/linux-support.md` → at least `4`.

## Test plan

None: no product code or test changes. The measurement is step 2's two runs, recorded in the document.

## Done criteria

- [ ] `docs/research/linux-support.md` exists with the five sections
- [ ] Its Facts section quotes the output of every step 1 command
- [ ] It says either what ran on Linux, with a per-package table, or "not run" and why
- [ ] Its Recommendation follows the rule in step 4 and names one option
- [ ] `git status --short` lists only that file (and the index, if you keep it)

## STOP conditions

Stop and report back (do not improvise) if:

- Running the suite on Linux would need a secret, a network service, or a real herdr or agent CLI: the tests are
  meant to stub all of them, so that would be a finding about the tests; report it before going further.
- A container run needs the main checkout mounted read-write. Use the exported copy only.
- You are tempted to fix a failing test or edit `README.md` to make the document's recommendation true.

## Maintenance notes

- The decision is the owner's. After it: option A is a one-line docs task; option B is a small task whose first red
  run becomes its own list of fixes; option C is a `RELEASING.md` edit.
- If "More machines" starts with remote merge checks only (BACKLOG, Ideas), the Linux machine runs `make check`, not
  agents: then only the checks' platform tools matter (`ps`, `lsof` in `internal/checks/gitlock.go`), a much smaller
  surface than full Linux support. Say so in the document if the owner's answer points that way. The order of the
  multi-device steps is in `docs/research/improve-architecture.md`, "One serve, several devices".
- `GOOS=linux go vet -tags integration ./...` costs seconds and could join the compile gate if Linux stays a target;
  it catches a Linux build break at merge time instead of at release.
