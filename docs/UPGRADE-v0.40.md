# Upgrading an obor project from v0.39 to v0.40

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
