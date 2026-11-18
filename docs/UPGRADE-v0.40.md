# Upgrading an obor project from v0.39 to v0.40

## v0.40.7 (a terminal restart restores only agents with work; no stale lane locks)

**Restore after a terminal restart.** When the terminal comes back, serve restarts an agent only where work is open:
the lead, a worker whose lane is busy, and a role agent with an open brief, the integrator's open job or the
reviewer's open review or verify. A role agent that was stopped on purpose stays stopped, and its next brief starts
a fresh session. A role agent at work when the terminal went down keeps its brief until the restore has restarted
it, so its work is not reported finished. A pane serve skips is logged in `~/.obor/restore.log` as "no open work".

**Lane locks.** obor's git calls run with `GIT_OPTIONAL_LOCKS=0`, so a read such as `git status` takes no
`index.lock` and a killed read can no longer leave one behind. A dispatch's lane checkout that meets a stale lock
(older than a minute, no git running) removes it once, logs it and goes on, as the merge gate already did; a live
lock is waited on.

```sh
# nothing to do
```

## v0.40.6 (a merged task's lane is free once its commit is in the target branch)

**Lanes.** A lane whose task has merged is free when the lane's HEAD is the commit serve recorded at submit, or when
that HEAD is in the target branch's history. Lanes no longer stay `working` after a history rewrite (`git
filter-repo`, a rebase of merged branches) changed the recorded commit ids. A commit made on the lane after its
submit is not in the target, so it still holds the lane.

```sh
# nothing to do (lanes stuck working after a history rewrite read free once serve runs v0.40.6)
```

## v0.40.5 (the monitor shows a new version; review briefs say the gate passed)

**Web monitor.** When `obor web` starts serving a new page (it follows a new obor binary by itself), an open tab
shows a "New version: reload" link in the nav instead of running the old page. Unsaved edits stay until you click it.
A tab opened before v0.40.5 cannot learn this: reload it by hand once after upgrading.

**Reviews.** The review and verify briefs tell the reviewer that each merge in scope passed serve's merge gate, so it
runs probes and the tests a finding names, not the project's check. The brief names `docs/accepted.md` only once the
file exists.

**Lead commands.** `skills/lead/commands.md` names `obor serve restart`, and says that a task's report is in its merge
commit's body after the spec.

```sh
# nothing to do (reload open obor web tabs once)
```

## v0.40.4 (serve opens a missing lane pane; trust dialogs; `obor idle` on a fresh project; config reload rule)

**Lane panes.** A worker start in a lane with no pane (`obor setup lane` makes only the worktree) no longer stops at
needs-you "no pane in lane": serve opens the lane's pane in a lane tab, the way `obor setup pane` does, and the worker
starts there. The needs-you is left for a pane serve could not open, with the error and the `obor setup pane` command.

**Trust dialogs.** A fresh start (a review, a worker's dispatch, a role brief) that meets the agent's "Trust this
folder?" dialog is recorded as started: the agent has its prompt and runs it once the dialog is answered. A needs-you
names the pane and clears by itself once the dialog is gone. For Claude, select "Yes, I trust this folder" (the
default is "No, exit"). A resumed start held by the dialog still fails as before.

**Lead commands.** `obor idle "<why>"` works on a project that has not merged anything yet. `obor spec set` lists the
models dispatch would try, in order. `obor config validate` says when a change counts: at serve's next pass, except
the keys it names (project name, agent prefix, terminal, the integrator and lead roles, the lead model's kind), which
need `obor serve` restarted. Its "merge.compile is empty" warning is now given by `obor init` only.

**Releases (obor-kit).** The upgrade check and the release catalog take the previous release from origin's main, so
a tagged version that was never shipped no longer counts as released.

```sh
# nothing to do
```

## v0.40.3 (lead start fix; `obor restart --handoff`; nightly needs-you follows the last check; web nav shows agents at work)

v0.40.2 was tagged but never published; v0.40.3 carries its changes.

**Nightly.** A RED nightly's needs-you now follows the last full check, a hand `obor nightly` included: GREEN ends it,
RED at another commit replaces the commit it names. `obor resume nightly "<why>"` acknowledges a RED nightly: the
needs-you and its hourly repeat end, the why is recorded, and the next RED run raises a fresh one.

**Lead start.** `obor lead` started the lead with `/obor:lead handoff` when `roles.lead` named an effort
(`opus:high`), so the lead continued the Handoff's next steps instead of asking you. A lead you start now always
gets the plain `/obor:lead`. The lead's own handoff restart is now asked for by name: `obor restart <lead> --handoff`
(it implies `--fresh`). A plain `obor restart <lead> --fresh` gets the plain prompt; `--handoff` on any other agent
is refused. `obor restart` outside a project (no `.obor/config.json`) prints one line instead of crashing.

**Web.** The left nav shows a project as working while its lead or a role agent works, not only while a lane is busy.

```sh
# nothing to do: the lead skill and the handoff reminder already say `obor restart <you> --handoff`
```

## v0.40.1 (submit fix; panes placed by kind)

**Submit fix.** Since v0.40.0, a worker's submit failed with `git add … exit status 1` when one of obor's links
(`lanes.links`, the auto-linked `.claude/settings.json`) is also in the project's `.gitignore`: git exits 1 when an
exclude pathspec names an ignored path. Submit now excludes only the links git does not already ignore. A task stuck
on it continues with `obor resume <T-xx>` after the upgrade.

**Pane placement.** obor never adds a pane to the lead's tab, even when the lead's pane was relabelled. New lane panes
go to a tab holding only lanes, and role panes to a tab holding only roles. A full tab (4 panes) opens another tab of
the same kind (`lanes`, `roles`). This is the default with no flag; `--tab` and `--new-tab` still choose, except into
the lead's tab. Existing panes are not moved.

```sh
# nothing to do
```

## v0.40.0 (a lead started by you asks first; a redesigned Docs page; merge-gate and lane fixes)

**Lead start.** A lead you start (`obor lead`, `/clear`, a typed `/obor:lead`) inspects the project and presents the
state and options, then waits for you. Only the lead's own handoff restart (`obor restart <lead> --fresh`, which now
sends `/obor:lead handoff`) continues from the Handoff's next steps on its own.

**`obor review`.** Bare `obor review` now shows the review state and changes nothing. Start a review with
`obor review start [--after T-a,T-b] [--note "<text>"]`. A script or habit that ran bare `obor review` to start one
must say `start`.

**Merge gate.** When the target moves under a candidate, the re-gate runs `merge.compile`. With no `merge.compile`
configured it now runs `merge.check` there, so a moved candidate is never merged with nothing run. Projects without
a compile step see slower moved re-gates. Set `merge.compile` to a command that type-checks what the merge check
covers (e.g. `npm run typecheck`) to make them fast again. `obor config validate` warns when it is empty and the
target is the repo's default branch. `obor init` no longer sets `merge.compile`; it suggests a `typecheck` script.
For package.json it composes `merge.check` from lint, typecheck, check and test, each when present, and warns on a
script that writes files or when no test runs.

**Lanes.** A lane holding only obor's own links (`lanes.links` and the auto-linked `.claude/settings.json`) is clean
at dispatch, at the lane test and at drop, with nothing written to `.gitignore` or `info/exclude`. A dispatch waiting
for a lane names each dirty lane and its paths.

**Smaller:** `obor review close` ignores untracked files outside the review; `NOT IDLE` names what is still open; a
blocked needs-you for a permission dialog keeps the dialog's command and options.

**obor web.** The Docs page is redesigned:
- a filter (`/`) with a "Search the text" mode;
- pinned cards, Recently changed, and a folder tree;
- a reading view with an outline that follows the scroll, and section links;
- titles in the list, and doc-kind colors;
- a drawer and a sheet on narrow screens.

In the light theme, the tone colors reach 4.5:1 as text on every page.

```sh
# nothing to do
```
