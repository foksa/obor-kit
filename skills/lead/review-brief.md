# Review brief

Builds the brief you (the lead) send to the reviewer, and later its verification prompt. The reviewer's rules and
output format are in the kit's `templates/agents/reviewer.md` (+ the project's `docs/agents/reviewer.md`); this file
only collects the facts the brief needs.

## 1. Collect
- **Reviews dir**: `obor config get .paths.reviews`, in the review worktree only: review files never enter the
  integration branch (owner 2026-10-30). The **previous review** is the newest `RV-xx closed · <sha>` commit
  (`git log --grep='^RV-[0-9]* closed' -1 --format=%s`); `<prev-sha>` is the sha it names.
- **Next id**: the previous id + 1 (e.g. `RV-55.md`).
- **Head**: `git rev-parse --short HEAD` on the integration branch (`<sha>`).
- **Merged tasks since then**:
  `git log --first-parent --merges --format='%h %s' <prev-sha>..HEAD` → one `Merge T-xx: …` line per task.
- **Files per task**: `git diff --stat <merge>^1 <merge>` for each merge commit (or
  `git diff --name-only <prev-sha>..HEAD` for the whole batch).
- **Out of scope**: tasks merged since `<prev-sha>` you deliberately leave for later, docs-only commits, the previous
  review's findings (unless verifying), and anything the specs mark out of scope. List them explicitly.
- **Contracts**: the design docs the specs cite.

## 2. Brief (one prompt to the reviewer)
The reviewer is a role pane (`obor setup role reviewer`: its own detached worktree, `roles.reviewer.worktree`,
default `review`), never a worker lane: never `obor dispatch` a review. You first put its worktree at the commit to
review (a Codex reviewer can't): `git -C <review worktree> checkout -q --detach <sha>`. Each new review starts in a
fresh reviewer session: `obor restart <reviewer agent> --fresh` first.
```
Review <RV-xx>. <branch> is at <sha>. In scope: <T-a (<merge sha>), T-b (…)>; files: <list>. Out of scope: <list>.
Previous review: <RV-prev> (git show <its removal's parent>:<reviews>/<RV-prev>.md, or the file while open) — don't
repeat its findings. Contracts: <design docs>. For every finding give severity, likelihood (does a real config or workflow hit it,
or only a constructed one?), file:line, probe/evidence, suggested fix and its cost in added code, and an
Extent line: every other entry point / branch that shares the root cause, each marked checked or unchecked. Write
only <reviews>/<RV-xx>.md in your worktree. Do not commit.
```
Send it as a fresh session's first prompt: `obor restart <reviewer agent> --fresh --prompt "…"` (default
`<prefix>-rev`; no backticks inside a double-quoted message). Serve records the brief and posts
`[obor] <reviewer agent> finished (briefed <age> ago)` to your session once the reviewer has worked and is idle again:
nothing to watch.

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
