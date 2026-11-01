# Review brief

A review, from its request to its close: `obor review` builds and sends the brief (§1–2), you triage (§3) and verify
(§4). The reviewer's rules and output format are in the kit's `templates/agents/reviewer.md` (+ the project's
`docs/agents/reviewer.md`).

## 1. What serve collects
`obor review` (§2) gathers these facts itself when the review starts; they are listed so you can read them back:
- **Reviews dir**: `obor config get .paths.reviews`, in the review worktree only: review files never enter the
  integration branch (owner 2026-10-30). The **previous review** is the newest `RV-xx closed · <sha>` commit
  (`git log --grep='^RV-[0-9]* closed' -1 --format=%s`); `<prev-sha>` is the sha it names.
- **Next id**: the previous id + 1 (e.g. `RV-55.md`; none yet: `RV-1`).
- **Head**: the integration branch's sha (`<sha>`).
- **Merged tasks since then**:
  `git log --first-parent --merges --format='%h %s' <prev-sha>..HEAD` → one `Merge T-xx: …` line per task.
- **Files**: `git diff --name-only <prev-sha>..HEAD` for the whole batch.
- **Out of scope** and **contracts** are yours: tasks merged since `<prev-sha>` you deliberately leave for later,
  docs-only commits, the previous review's findings (unless verifying), anything the specs mark out of scope, and the
  design docs the specs cite. They go into `--note`.

## 2. Start it: `obor review`
`obor review [--after T-a,T-b] [--note "<out of scope: …; contracts: …>"]` (one open request; a second call replaces
it, `obor review` lists it, `obor review --rm` ends it). Serve starts the review once every `--after` task has merged
(none given: now) and the reviewer (a role pane, `obor setup role reviewer`, its own detached worktree
`roles.reviewer.worktree`, default `review`; never a worker lane, never `obor dispatch` a review) is idle: it detaches
the review worktree at the head, starts a fresh reviewer session with the brief below as its first prompt (as
`obor restart <reviewer> --fresh --prompt` does), records the brief, and tells you
`[obor] review RV-xx started at <sha7> (T-a, T-b, …)`. A `--after` task dropped or rejected ends the request
(`[obor] review request ended: T-b dropped`); a step that fails starts nothing and is a needs-you
(`review: RV-xx: <error>`; a new `obor review` ends it). One review cycle is open at a time: the review serve started
is open until its `RV-xx closed` commit (§5) is on the integration branch. While it is open `obor review` is refused
(`FAIL RV-xx is open (not closed): close it first`) and a request that comes due waits (`obor status`: `waiting RV-xx
to close`): a fresh start would replace the session §4 needs. While serve is starting or ending a request it claimed,
`obor review` and `--rm` are refused (try again once it is done). A fact serve fails to record after the reviewer
started is a needs-you (`review: RV-xx: started but not recorded: <error>`). The brief serve sends:
```
Review <RV-xx>. <branch> is at <sha>. In scope: <T-a (<merge sha>), T-b (…)>; files: <list>.
Previous review: <RV-prev> (its close commit: <subject>; its accepted findings are the BACKLOG lines that name
<RV-prev>) — don't repeat them. For every finding give severity, likelihood (does a real config or workflow hit it,
or only a constructed one?), file:line, probe/evidence, suggested fix and its cost in added code, and an
Extent line: every other entry point / branch that shares the root cause, each marked checked or unchecked. Write
only <reviews>/<RV-xx>.md in your worktree. Do not commit.
Lead's note: <your --note>
```
Serve posts `[obor] <reviewer agent> finished (briefed <age> ago)` to your session once the reviewer has worked and is
idle again: nothing to watch.

## 3. When the review is written
Read `<review worktree>/<reviews>/<RV-xx>.md` there; do not copy or commit it. Triage each finding (owner rule
2026-10-27): likely, or unlikely with a small fix → a fix task (one goal each; the fix spec lists every Extent entry,
one test each, and carries what it needs from the finding: the file is not on the branch). Unlikely and the fix adds
complexity → no fix: accept it in `BACKLOG.md` with one line why, or remove the case instead (refuse the config) when
that makes the code smaller. A probe that proves a constructed case is not by itself a reason to add code.

## 4. Verification (same session — never `/new` the reviewer)
After the fix tasks merge, detach the review worktree at the new head (`git -C <review worktree> checkout -q --detach
<sha>`; the untracked review file stays) and `obor brief <reviewer agent> "…"` into the same, still-running session
(never a worker lane; serve delivers it once the pane is idle and tells you when it has finished):
```
The <RV-xx> findings were fixed in <T-x (<sha>), …>; <branch> is at <sha>. Re-run your probes for #<n>, … and report
fixed / incomplete / regressed per finding. Write <reviews>/<RV-xx>-verify.md. Do not commit.
```
Read the verify file there. Incomplete or regressed → a new fix task; second finding in the same function or
invariant → the two-strike rule in the lead skill.

## 5. Close the cycle
When a verify approves: delete both files from the review worktree (`git -C <review worktree> status --short` must
then be empty) and record the close as an empty commit on the integration branch:
`git commit --allow-empty -m "RV-xx closed · <reviewed sha> · approved · <fix tasks>"`. Accepted findings are
BACKLOG lines. Nothing else of the review is kept; the reviewer's evidence lives only in its session.
