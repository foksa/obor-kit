# Upgrading an obor project from v0.26 to v0.27

## v0.27.0 (role worktrees, no merge hold, review fix rounds in serve, verbs that answer first)
Upgrade from v0.26.x. Nothing to change in the project: re-run the installer, then `obor serve restart` in every
project. Close an open review first (`obor review close`), or let it finish on the new serve.

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
- **A review's fix round runs in serve**: after dispatching the fix tasks, `obor review fix T-a,T-b` links them to the
  open review; once they merged, serve briefs the same reviewer session to verify. A verify counts only when the
  reviewer finished it (an error stop or a superseded brief proves nothing), and `obor review close` is refused while a
  linked fix is unverified (`--force`: the owner's word). `obor review verify` still works by hand.
- **`obor dispatch T-xx --model`, `obor drop` and `obor drive T-xx` answer at once** (`MOVING …`, `DROPPING …`,
  `DRIVING …`); serve stops the old agent as a step of its loop and posts `[obor] T-xx: stopped <agent>` (or a
  needs-you when the stop fails). While that stop is pending, other verbs on the task answer "retry when it settles".
- **Help:** `obor help` lists every command by group with its usage, and obor web has a Help page with the same table
  and README's install and setup steps.
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
