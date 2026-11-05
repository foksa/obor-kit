# Upgrading an obor project from v0.26 to v0.27

<!-- Draft from T-853; the lead completes this guide at tag time (RELEASING.md step 1). -->

## v0.27.0 (reviewer, deep dive and integrator out of the main checkout; no merge hold)
Upgrade from v0.26.x. Nothing to run in the project before restarting serve:

```sh
# nothing to do
```

Then re-run the installer and `obor serve restart` in every project.

What changes:
- **The reviewer and the deep dive run in their own worktree**, `<lanes_dir>/reviewer` and `<lanes_dir>/deep_dive`,
  which serve owns: it detaches the reviewer's at the reviewed head when a review starts and at each `obor review
  verify`, and the deep dive's at the integration branch's head when it is briefed while idle (adding the worktree
  when it is missing). They may check out any commit there. Their files still go to the main checkout's reviews dir
  (`paths.reviews`); a Codex reviewer's start adds that dir (`--add-dir`), since its sandbox writes its cwd only.
- **The integrator runs in its own worktree**, `<lanes_dir>/integrator`: a review job may check the candidate out
  there. Its resolve jobs still work in the merge scratch (`merge.scratch_dir`), as before.
- **Merges no longer wait for the reviewer or the deep dive**: the merge hold (`merge hold: <role>` in `obor status`
  and web, the lead's `merge of T-x went ahead` line) is gone.
- **The main checkout's HEAD rows are gone from the guard**: no role but the lead runs there any more (custom roles
  there read git only), so a non-lead's `git checkout` / `reset` / … in the main checkout is no longer refused by them.

Moving the panes of a running project: a pane moves at its agent's next start (obor cds it to the new place and adds
the worktree when it is missing). To move one now, stop its agent and place it again, for each of the integrator,
the reviewer and the deep dive you run:

```
obor restart <prefix>-int --stop          # likewise <prefix>-rev, <prefix>-deep-dive
obor setup role integrator --pane <its pane>   # adds <lanes_dir>/integrator when missing, cds the pane there
```

Without `obor setup role`, serve starts each where it belongs on first use: the integrator at its next job, the
reviewer at the next review's start, the deep dive at its next brief. Older role worktrees of other names
(`<lanes_dir>/review`, `<lanes_dir>/deep-dive`) stay unused: `git worktree remove` them once nothing runs there.
