# Upgrading an obor project from v0.46 to v0.47

## v0.47.5 (serve adds a raised lane count's worktrees)

From v0.47.4. Nothing to change.

```sh
# after updating the kit, in the project
# nothing to do
```

A lane added by raising `lanes.count` no longer needs `obor init` again: serve adds its worktree and runs its setup.
`obor setup lane L-NN` still works and repairs a lane whose setup failed.

## v0.47.4 (obor init in a folder with files and no commit)

From v0.47.3. Nothing to change.

```sh
# after updating the kit, in the project
# nothing to do
```

Only new projects see it: `obor init` in a folder with files and no commit commits obor's own files, and the lead's
first session asks whether to commit the rest. If you commit them yourself instead, run `obor init` again so the lanes
move to that commit and get their setup.

## v0.47.3 (Claude sessions named after the project and agent)

From v0.47.2. Nothing to change.

```sh
# after updating the kit, in the project
obor lead --resume   # in a shell in the project: the running lead picks up its name
```

Every Claude agent obor starts or resumes is named `<project> <agent>`. Running agents keep their old title until their
next start; workers and role agents get theirs at their next job or brief. `/rename` names a session by hand meanwhile.

## v0.47.2 (probe dir per reviewer and deep dive; a tracked path is never linked)

From v0.47.1. Nothing to change.

```sh
# after updating the kit, in the project
# nothing to do
```

Reviewers and deep dives are told one probe directory in their brief; serve empties it when it stops them. Leftover
probe directories from earlier versions in the temp dir (`rv*-probe`, `obor-rv*-go-cache`) can be deleted by hand.

## v0.47.1 (fix attempts start from the target; a never-run role agent starts on restart)

From v0.47.0. Nothing to change.

```sh
# after updating the kit, in the project
# nothing to do
```

A sent-back task's next attempt starts from the current integration branch: serve merges it into the lane before the
fix job (a conflict goes to the worker in that attempt). `obor restart` of a role agent with nothing recorded and no
agent in its pane starts it fresh instead of asking for `--fresh`.

## v0.47.0 (Rex as an experimental terminal; agents' own status on hc; one blocked-prompt guard)

From v0.46.x. Nothing to change in a herdr or hc project.

```sh
# after updating the kit, in the project
# nothing to do
```

**Rex (experimental)** can be a project's terminal: `project.terminal: "rex"`. To try it:

1. Put Rex's CLI on your `PATH` (it ships inside the app; obor never runs it from there):
   ```text
   ln -s "/Applications/Rex.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex" ~/.local/bin/rex
   ```
2. In a Rex block, in an empty folder: `obor lead`. `obor init` picks Rex because it runs in a Rex block
   (`REX_BLOCK`), and `obor setup check` fails with the bundle path if `rex` is not on `PATH`.

obor lays the project out in Rex as in herdr (one session per project, lead, lane and role tabs; new tabs do not take
focus). Each agent's status is its own OSC 7501 report; a status counts only while its process holds the block's
foreground. Blocks inherit the Rex server's environment, so obor starts every agent's shell with other terminals' and
obor's identity removed. Workers may run only `rex` read commands. The live tier does not gate.

**On hc**, the agent's own status text shows on its `obor status` line, a blocked agent's needs-you line and the web
card (`permission: approve Bash: …`, `error: …`), as shown text only.

**Every terminal**: obor refuses to type into a blocked agent before any key, with the agent's note in the refusal;
herdr's and hc's own refusals stay behind it. An hc core started from another terminal's pane (herdr, Rex) or an obor
pane is refused with the restart line, so its terminals never carry another terminal's identity.
