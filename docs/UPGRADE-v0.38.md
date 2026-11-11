# Upgrading an obor project from v0.37 to v0.38

## v0.38.0 (every start asks who already runs; the terminal behind one interface; a running review is visible)

Every agent start (`obor restart`, `obor lead`, `obor setup pane/role`, `obor drive`, and serve's own starts) now asks
serve's identity table whether the agent already runs, and where, before it stops, labels or types into any pane. A
relabelled agent is recognised by its session or start record: `obor lead` on a running lead prints `OK <lead> runs
(pane <id>[, labelled <l>])` and touches nothing, and a start that would make a second copy is refused, naming the
pane. Only a label naming another configured agent reserves a pane; a shell labelled `notes` is free to start into.

With serve down, a start that would add an agent is refused while a live agent pane of the project carries no
configured agent's label; the refusal names the pane and both ways out (`obor serve restart`, or `obor restart <agent>
--pane <id>` to replace it). A restart in place always goes.

The core speaks of "the terminal", not herdr: `obor status --json` has `serve.terminal` and `serve.terminal_note`
where it had `herdr` and `herdr_note` (update any tooling that reads them); the status line reads `terminal: …`;
serve's log has `terminal: <version>` and `terminal: connected`; `.obor/state/herdr-instance` becomes
`terminal-instance` (serve reads the old file once and writes the new one: no action). Hints that printed a herdr
command print an obor command.

`obor status` and `obor web` show an open review's stage (reviewing, the lead's turn, fixes, verifying, approved)
with the tasks it covers, the reviewer's status and how long the stage has run.

```sh
# nothing to do
```
