# hc trial: a scratch project on herdr-core (plan step 6)

Step 6 of `docs/plan/herdr-core.md`, run by the owner and the obor-kit lead together, not a worker task: it
installs a launchd job, registers a project in `~/.obor` and lays out spaces in the owner's herdr-app window.
Findings go to `docs/research/hc-probes.md` ("Trial" section, one fact per step); bugs become tasks in obor-kit.
hc stays experimental (HD7) whatever the result; obor-kit moves to hc only on the owner's word after this.

Built and reviewed for it: T-1357/T-1359/T-1361 (hc adapter, restore by lost panes), T-1360/T-1363/T-1370/T-1373
(herdr-app layout), T-1364 (selection, pane variable by kind, env guard, hc guard rule), T-1368 (hidden terminals
placed at serve connect).

## Before

- obor v0.42.0 or later installed (`obor version` outside the obor-kit checkout: elsewhere `obor` runs the pinned
  release). Owner: herdr-app open; `hc --version` ≥ 0.1.21. Run `hc` commands from **Terminal.app, outside herdr and
  outside any obor pane** (an `hc` call from an obor pane while no core runs carries that pane's env into the core);
  `obor init` itself runs no `hc` and may be typed in any herdr-app terminal.
- `obor init` checks the core's env first: if it refuses ("started from an obor pane"), run
  `hc server restart --fresh` from Terminal.app, then `obor init` again.

## Steps

| # | Who | Do | Expect |
|---|---|---|---|
| 1 | owner | `mkdir ~/Work/hc-trial && cd ~/Work/hc-trial && obor init` in a herdr-app terminal | init picks hc, one commit, serve installed; the lead starts in that terminal, its tab renamed `lead`, the space `hc-trial` |
| 2 | — | (merged into 1) | |
| 3 | trial lead + owner | the lead's one setup question: Keep | `obor setup show` confirmed |
| 4 | trial lead | `obor status`; `obor setup check` | serve up on hc, env guard passes, lanes and roles placed: `lanes` and `roles` tabs, one terminal per pane, each in its worktree |
| 5 | trial lead | a tiny spec (add a README line), `obor dispatch` | worker starts on hc, `task_done`, gate, integrator merge |
| 6 | trial lead | `obor review start`, close | reviewer starts in `roles`, writes, close stops it |
| 7 | owner | `hc server restart` (Terminal.app) | every agent keeps running; serve reconnects; no restore (T-1361) |
| 8 | owner | kill one holder process (`ps` for `herdr-core hold`, the newest) | only its panes restarted, others untouched |
| 9 | owner | `hc server stop`, then `hc server start` | kept terminals back as shells; serve restores the agents with open work, as after a herdr restart |
| 10 | owner | quit herdr-app, trial lead dispatches a second tiny task, reopen herdr-app | worker runs hidden, then appears in `lanes` at serve's next connect or the next placement (T-1368) |
| 11 | trial lead | `obor uninstall --purge` (from Terminal.app) | herdr-app space gone, launchd job gone, worktrees gone; `~/Work/hc-trial` keeps its committed files |

After each step the obor-kit lead records the fact in `docs/research/hc-probes.md` and files a task for any bug.

## Done when

Steps 1-11 pass, or each failure has a task. Then the owner decides: obor-kit on hc for a few days (Stop B →
plan step 6's second half), or hc stays opt-in for other projects.
