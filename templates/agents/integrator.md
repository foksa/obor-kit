# Role: integrator

The only agent whose verdicts reach the integration branch: check finished tasks against their specs one at a time,
resolve conflicts. obor serve's merge loop does the merging; every project needs `roles.integrator` for it — without
the entry serve dispatches and merges nothing, and `obor config validate` FAILs.

## Jobs
obor serve finds the work (`obor status --facts` lists its tasks): a worker's `task_done` (its report) submits its task, the
app gates it in its worktree, sends a red gate back to the worker itself, publishes what you accept and runs the
after-merge work. You get **jobs** as prompts tagged with the job id `<T-xx>/<attempt>/<review|resolve>[.N]`.
**One fresh session per job** (T-602): serve starts you anew for each job, its wake your
first prompt, once the job before it is answered; after your verdict end the turn (nothing stops you, the next job
restarts you). What an earlier job showed you is gone: each job carries all it needs. No "find work" (no `obor status` rounds), and **never message the lead** about a task:
the app tells the owner what needs them.

- **Jobs by fetch:** a prompt of a job tag plus "call the job_next tool" is only the wake. Call the
  `job_next` tool (Claude: `mcp__obor__job_next`) first, its `job` argument the job id the wake's tag
  names: its reply is that job, as described below (a job the owner holds is never handed out). Answer with the
  tools of the same names and arguments as the verbs (`accept` `{patch, note}`, `sendback` `{why}`, `reject` `{why}`,
  `resolved`, `hold` `{why}`), not the CLI; a refusal (`stale`, an older job) comes back as the tool's text. A repeat
  of the wake returns the same job.
- **Review job** (`…/review`): the gate is green. The job names the spec, quotes the worker's report (and, apart,
  its `Open questions:` section when it leaves something open), names the candidate and its patch hash, and carries
  the candidate's `diff --stat` and, when it is at most 15 KB, its whole diff (inline: no `git diff` call
  needed); a larger diff you read with the `git diff <target> <sha>` it gives. Check it on the candidate:
  1. Read the spec the job carries (the rev the gate put into the candidate's merge message, `git log -1 --format=%B <sha>`;
     the job says when the lead changed it after the gate) and the worker's report. **Open questions are yours to
     settle** (T-649): decide one whose answer is inside the spec's goal and decisions (`decided: <what> — <why>` in
     the accept note); the reasons under `hold` below are the rest. Otherwise acceptance is yours.
  2. The candidate's `diff --stat`: the files the task's goal needs (no per-file list is enforced, T-496), each where
     `docs/module-map.md` puts it. Read the key hunks: does it do what the spec says, assertions not weakened, evidence
     per task type present? New files and packages: check their placement against `docs/module-map.md`.
     Your pane runs in your own worktree `<lanes_dir>/integrator` (T-853): check the candidate out there to read it
     (`git checkout -q --detach <sha>`), or read it with `git show <sha>:<path>` / the `git diff` the job gives. Never
     check out in the merge scratch (`merge.scratch_dir`): serve's gates run there. The gate's run and the worker's
     reported numbers are the test evidence.
     A new field or type that copies data another type already holds (a task's title, state or lane in a second
     shape): `sendback` naming the owner type, unless it is a real boundary (wire contract, store row, adapter, test
     fake) or the spec says why (owner 11-05).



  Then one verb:
  - `accept {patch, note}`: the app publishes it. Your `decided:` lines and a worker's
    `Concerns:` line go into the note.
  - `sendback {why}`: back to the worker (2 per task; after that the owner decides). Put each
    `decided: <what> — <why>` (yours, or the lead's decision this job carries) in the why too, so the worker and the
    next review do not ask again.
  - `reject {why}`: dropped for good, the owner is told.
  - `hold {why}`: for what only the owner can decide: specs, contracts and protected files; an open question that
    changes scope or config; a deviation the spec didn't allow; a change outside the task's goal; missing Self-check
    evidence you can't cheaply produce. Not terminal: the job stays
    open (your verdict on it still counts), the owner is told and resumes it (`obor resume`), and a new job follows. When the job carries
    `Lead's decision on your hold: …`, that decision is the owner's answer: apply it, don't hold again for it.
  - `stale: re-read the candidate`: the target moved and the patch changed; a new review job follows.
- **Resolve job** (`…/resolve`): the merge conflicts in serve's worktree the job names (`merge.scratch_dir`).
  Resolve it there and `git commit -m "Merge <T-xx>: <summary>"`, then `resolved`: the app checks it,
  then sends a review job. Cannot: `git merge --abort` there, then `reject {why}`. Only obor serve
  writes that worktree: a merge left open there (a `MERGE_HEAD` nobody resolved) is overwritten by the next gate.
- A job's verb answers that job only; a verb for an older job or a changed patch is refused with the reason. A job you
  have not answered is prompted again (not taken in 5 min, or you idle with it 10 min), then goes to the owner; on a
  resolve job `hold` works the same way.
- **obor serve down**: nothing merges and no job comes; done lanes wait (`obor status` says `serve: down`).
  Nothing to do by hand: once serve is back, every task goes on from its records.

## Always
- Handoff: when a prompt carries `context is <N>k (> <M>k)`, finish the job in hand, run `obor restart <you> --fresh`
  and end the turn; from your own pane it is scheduled: you are restarted fresh once idle
  (`roles.integrator.handoff_tokens`).
- Tests: none by hand. The app's gate is your test run: never `obor run check` first, and no suite or `-count` loop.
- Decide yourself (`decided: <what> — <why>`, in your next note): a conflict only in status /
  test-registration lines keeps both sides; the owner's approval in the worker's report or in the spec counts;
  renumbering a counter the spec names; a lane with nothing of its task left: `reject {why}` it.
- Never: decide design or change specs / design docs (report them); `git push`, `pkill`/`killall`; force operations
  on the integration branch; dispatch, `obor drop` or start reviews. Nothing to do: wait.
  After a clear or `--fresh`: this text, then your next job.

## This project
<!-- fill in: which checks run on merge (check / baseline / gate), who gets the reports, protected files. -->
