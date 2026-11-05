# Review brief

A review, from its request to its close: `obor review` builds and sends the brief (§1–2), you triage (§3) and verify
(§4). The reviewer's rules and output format are in the kit's `templates/agents/reviewer.md` (+ the project's
`docs/agents/reviewer.md`).

## 1. What serve collects
`obor review` (§2) gathers these facts itself when the review starts; they are listed so you can read them back:
- **Reviews dir**: `obor config get .paths.reviews`, in the main checkout (T-718: the reviewer runs there, no
  worktree of its own) and gitignored: review files never enter the integration branch (owner 2026-10-30). The **previous review** is the newest `RV-xx closed · <sha>` commit
  (`git log --grep='^RV-[0-9]* closed' -1 --format=%s`); `<prev-sha>` is the sha it names, and, since T-822, its body
  names the tasks its cycle **Covered** and the sha its review **Start**ed at.
- **Next id**: the previous id + 1 (e.g. `RV-55.md`; none yet: `RV-1`).
- **Head**: the integration branch's sha (`<sha>`).
- **Merged tasks since then**: since T-822 the merges on `Start..HEAD` (an older close commit without a `Start:` line:
  `git log --first-parent --merges --format='%h %s' <prev-sha>..HEAD`) that no closed cycle covered: the tasks the
  previous close's `Covered:` line names are left out. So a task merged while the previous review's verify ran (after
  its start, not in its scope) is reviewed now, one a verify covered is not reviewed twice.
- **Files**: `git diff --name-only <the same start>..HEAD` for the whole batch.
- **Out of scope** and **contracts** are yours: tasks in the scope you deliberately leave for later, docs-only
  commits, the previous review's findings (unless verifying), anything the specs mark out of scope, and the
  design docs the specs cite. They go into `--note`.

## 2. Start it: `obor review`
`obor review [--after T-a,T-b] [--note "<out of scope: …; contracts: …>"]` (one open request; a second call replaces
it, `obor review` lists it, `obor review --rm` ends it). Serve starts the review once every `--after` task has merged
(none given: now) and the reviewer (a role pane, `obor setup role reviewer`, in the main checkout; never a worker lane,
never `obor dispatch` a review) is idle: it marks the head reviewed (`refs/obor/reviewed`), starts a fresh reviewer
session with the brief below as its first prompt (your own fresh start is `obor brief <reviewer> --fresh "<text>"`: it
stops a running reviewer, and serve starts it clean on the text), records the brief, and posts no line
to you (T-650; `obor status` shows the review). A `--after` task dropped or rejected ends the request
(`[obor] review request ended: T-b dropped`); a step that fails starts nothing and is a needs-you
(`review: RV-xx: <error>`; a new `obor review` ends it). One review cycle is open at a time: the review serve started
is open until its `RV-xx closed` commit (§5) is on the integration branch. While it is open `obor review` is refused
(`FAIL RV-xx is open (not closed): close it first`) and a request that comes due waits (`obor status`: `waiting RV-xx
to close`): a fresh start would replace the session §4 needs. While serve is starting or ending a request it claimed,
`obor review` and `--rm` are refused (try again once it is done). A fact serve fails to record after the reviewer
started is a needs-you (`review: RV-xx: started but not recorded: <error>`).

The main checkout stays put while the reviewer works (T-718): serve holds each merge's publish while the reviewer (or
the deep dive) has a brief open or its pane is not idle; gates, jobs and dispatch go on, `obor status` shows
`merge hold: reviewer` on the waiting task, and after 30 min the merge goes ahead with one line to you
(`[obor] merge of T-x went ahead: reviewer still working after 30m`). Your own doc commits during a review are fine:
they touch no code. The brief serve sends:
```
Review <RV-xx>. <branch> is at <sha>. In scope: <T-a (<merge sha>), T-b (…)>; files: <list>.
Previous review: <RV-prev> (its close commit: <subject>; its accepted findings are the BACKLOG lines that name
<RV-prev>) — don't repeat them. Report only findings a real config or workflow hits; for each give severity,
likelihood (the real config or workflow that hits it), file:line, probe/evidence, suggested fix and its cost in
added code, and an Extent line: every other entry point / branch that shares the root cause, each marked checked
or unchecked. A finding only a constructed case hits goes in a short "Not reported (constructed)" list instead:
one line each, no fix proposal. Write only <reviews>/<RV-xx>.md (in the main checkout). Do not commit.
Lead's note: <your --note>
```
Serve posts `[obor] <reviewer agent> finished (briefed <age> ago)` to your session once the reviewer has worked and is
idle again: nothing to watch.

## 3. When the review is written
Read `<reviews>/<RV-xx>.md` in the main checkout; do not copy or commit it. Triage each finding (owner rule
2026-10-27): likely, or unlikely with a small fix → a fix task (one goal each; the fix spec lists every Extent entry,
one test each, and carries what it needs from the finding: the file is not on the branch). Unlikely and the fix adds
complexity → no fix: accept it in `BACKLOG.md` with one line why, or remove the case instead (refuse the config) when
that makes the code smaller. A probe that proves a constructed case is not by itself a reason to add code.
Dispatch the fix tasks, then link them to the open review at once: `obor review fix <T-x,…> [--note "<what to
check>"]` (T-850; a second call adds tasks). Refused: no open review, a task not dispatched. Do not close the review
now: its verify (§4) needs the reviewer's session and the findings' context.
At most two review rounds per feature (owner 2026-11-02): after the second verify, what is still open is accepted
in `BACKLOG.md` (one line each, with its likelihood and fix cost), unless it is likely and its fix is small.

## 4. Verification (same session — never `/new` the reviewer)
Once every task linked by `obor review fix` merged, serve runs the verify itself (T-850): what `obor review verify
<the linked tasks> --note "<your fix notes>"` runs, below; you get the reviewer's finished line as for any brief. A
linked task dropped or rejected: one line `[obor] <RV-xx> fix <T-x> ended unmerged: the verify waits for <the rest>`.
A verify serve cannot run (git refusing, a task with no merge in the review's scope): one line `[obor] <RV-xx> fix
verify of … failed: <error>`; run it by hand.
By hand (a verify of tasks you did not link, or after that failure): `obor review verify <T-x,…> [--findings 1,3]
[--note "<text>"]`. It asks serve, which waits
for a publish in flight (T-730: no merge moves the head between its read and the brief), then marks the integration
branch's head reviewed (`refs/obor/reviewed`; nothing to detach: the reviewer reads the main checkout, held still
while it works) and briefs the open review's reviewer in its same, still-running session (`obor brief`: serve delivers
it once the pane is idle and tells you when it has finished). The open review is the next RV by git whose
`<reviews>/<RV-xx>.md` is in the main checkout; none, or a task with no merge since the previous review started (the
previous close commit's `Start:` sha — a task merged after the review started, which it does not cover, is allowed
and joins the cycle's covered set): refused, nothing sent. The brief (`--findings` default: every finding):
```
The <RV-xx> findings were fixed in <T-x (<sha>), …>; <branch> is at <sha>. Re-run your probes for #<n>, … and report
fixed / incomplete / regressed per finding. Write <reviews>/<RV-xx>-verify.md. Do not commit.
Lead's note: <your --note>
```
Read the verify file there. Incomplete or regressed → a new fix task; second finding in the same function or
invariant → the two-strike rule in the lead skill.

## 5. Close the cycle
When a verify approves (or the review found nothing): `obor review close "<summary>" [--accept "<line>"]…
[--force]`. It
deletes `<RV-xx>.md` and `<RV-xx>-verify.md` from the main checkout, adds each `--accept` line to `BACKLOG.md`'s Open
section as `- (<RV-xx>, accepted) <line>`, commits `RV-xx closed · <reviewed sha> · approved · <summary>` on the
integration branch in the main checkout (with `BACKLOG.md`; the reviewed sha is `refs/obor/reviewed`: the last
verify's head, else the review's, not your doc commits since) and stops the reviewer (`obor restart <reviewer>
--stop`). Since T-822 the commit's body records the tasks the cycle covered (its in-scope tasks and each verify's)
and the sha its review started at:

```
Covered: T-a, T-b, T-c
Start: <the review start sha>
```

so the next review's scope takes the merges this cycle did not cover (a task merged while your verify ran stays
unreviewed until a cycle covers it; `obor status`' `review due` and `make release` count it). A close commit from an
older obor has no such body: the next review falls back to the sha range. Refused before any change: no open review,
no `refs/obor/reviewed` (a review started before T-718: `git
update-ref refs/obor/reviewed <sha>` by hand), the main checkout not on the integration branch or dirty outside
`BACKLOG.md` and the review's files, or a task linked by `obor review fix` neither verified nor dropped (named;
`--force`, the owner's word, closes anyway). Nothing else of the review
is kept; the reviewer's evidence lives only in its session.
