# `obor init` in an empty folder: the flow (advice, 2026-11-20)

Question (lead, owner 11-20: "I just want to run obor init in empty folder, either in herdr or hc, and everything
should be done automatically"): the flow from `cd empty-folder; obor init` to a working lead, how the terminal is
detected, whether init starts the lead, where the model/lanes confirmation goes, what to delete, and a task split.
Read at v0.41.1 (`ce93f413`).

## Recommendation

1. **`obor init` is the one command.** It picks the terminal from the pane it is typed in, sets the project up, makes
   one commit, and ends by starting the lead. `obor lead` stays the command that brings a lead back; with no config
   (or no repository) it runs `obor init` and nothing else.
2. **The terminal is the one the caller sits in.** `HC_TERMINAL_ID` → hc, `HERDR_PANE_ID` → herdr. Outside both:
   the one that answers; both answer → herdr (HD2, the default of record) with a line saying so. `--terminal
   herdr|hc` overrides. obor asks nothing.
3. **One question is left, and the lead asks it**: the models and lanes table with "Keep" as the first option. Init
   asks nothing. Claude's own "Trust this folder?" dialog also appears once at the lead's first start; obor cannot
   answer it and should not pre-write Claude's user file.
4. **The lead starts in the pane `obor init` was typed in** when that pane is alone in its tab. The person never
   has to find the lead, and the leftover shell tab from the trial is gone. Otherwise it gets a new `lead` tab, as
   today.
5. **Init leaves a clean tree in a new project**: every config edit and `.claude/settings.json` are in the
   `obor init` commit. Today init commits early and then edits the config again.

## 1. The flow

### What the person does and sees (both terminals)

```
$ mkdir ~/Work/foo && cd ~/Work/foo && obor init
terminal   herdr: this pane                       (hc: "hc: this terminal, core env clean")
git        git init -b main
project    foo (prefix foo)
commands   skipped: none detected
lanes      L-01, L-02; lead opus:high, integrator opus, reviewer sol:high → opus
plugin     installed obor@…
commit     obor init: .obor/config.json, .claude/settings.json, .gitignore, BACKLOG.md, docs/…
panes      lanes tab: 2 · roles tab: integrator, reviewer
serve      installed and loaded
OK init foo (prefix foo): the lead starts in this pane
$                                                  ← obor exits; the lead's start is typed here
```

Then, in the same pane:

1. Claude starts and shows "Trust this folder?" (Claude's, once per folder). The person picks yes.
2. The lead's first message is the setup table and one question: **Keep these models and 2 lanes?** `Keep
   (Recommended)` / `Change`. Keep: the lead sets `agents.confirmed: true` and commits. Change: the lead edits what
   the person says, then the same.
3. The lead says it is ready. In an empty folder `merge.check` is unset, so it adds: the first task sets up the stack.

Questions from obor: 0 in init, 1 from the lead. Keypresses that are not obor's: Claude's trust dialog.

### Differences per terminal

| | herdr | hc |
|---|---|---|
| Where the lead is | The calling pane, labelled `<prefix>-lead`. Its workspace becomes the project's (it already is by cwd: `internal/herdr/setup.go:105-111`). | The calling terminal, labelled as the lead's; its tab renamed `lead`. The space at the folder is renamed to the project (`internal/herdrapp/layout.go:111-119` does this today). |
| Other tabs | `lanes` and `roles` tabs in that workspace | `lanes` and `roles` tabs in that space; herdr-app closed: terminals made hidden, placed when the window opens (`internal/hc/place.go:76-79`) |
| Extra line | `resume_agents_on_restore = false` hint when herdr's config lacks it (`oborinit.go:255-259`). User-global, obor does not edit it: one manual edit, once per machine. | The env guard runs as init's first step (today the separate `obor setup check`, trial step 4). A core holding `HERDR_`/`OBOR_` names: FAIL with `hc.LeakLine` before any write. |

### When the calling pane cannot be the lead's

The pane is used only when it is a pane of the chosen terminal, carries no `OBOR_ROLE`, is alone in its tab, and
(herdr) its workspace holds no pane of another project. Otherwise the lead gets a new `lead` tab as today, and the
last line names it: `OK init foo: the lead runs in workspace foo, tab lead (pane w7:p1)`. From Terminal.app this is
always the case.

## 2. Terminal detection

Decided once, before any write, only when there is no `.obor/config.json`. An existing config wins (absent key:
herdr, as today); when the caller sits in the other terminal init says so in one line and goes on.

| Caller's env | herdr answers | hc core answers | Result |
|---|---|---|---|
| `--terminal K` | | | K; K not answering → FAIL naming K |
| `HC_TERMINAL_ID` only | any | yes (it must: the caller is in it) | **hc**; a leaked core env → FAIL `LeakLine` |
| `HERDR_PANE_ID` only | yes | any | **herdr** |
| both set | | core env holds `HERDR_` names | FAIL `LeakLine`: the core was started from a herdr pane, hc refuses it anyway (`internal/hc/env.go:86-95`) |
| both set | | core env clean, or no core | **herdr**: `HC_TERMINAL_ID` was inherited by herdr's server |
| neither (Terminal.app, ssh) | yes | no | **herdr** |
| neither | no | yes | **hc** |
| neither | yes | yes | **herdr**, with `terminal herdr (herdr and hc both run; for hc: obor init --terminal hc, or run it in a herdr-app terminal)` |
| neither | no | no | FAIL `no terminal runs: start herdr or open herdr-app, then obor init again` |

Why no question in the "both run, caller outside" row: the owner's sentence is "either in herdr or hc", so the
calling pane is the normal signal and this row is the edge. Herdr is the recorded default (`docs/decisions.md`, 11-20
"hc stays opt-in"), the line is the first one printed, and the flag is the answer.

The result is written into the new config as `project.terminal`, for herdr too: the config says what init chose.
obor still never runs the `hc` CLI from init (only serve starts a core, scrubbed), so typing `obor init` inside any
pane cannot leak that pane's env into a core. The trial's "run it from Terminal.app" rule (`docs/plan/hc-trial.md:15`)
is not needed for init.

Where: `cmd/obor/terminal.go` is the one place that builds the adapter and may import both. Today `setupCmd` builds
the terminal from a nil config before init runs (`cmd/obor/setup.go:22-26`), which is why init always gets herdr.

## 3. Init starts the lead; the confirmation stays with the lead

**Init ends by starting the lead**, in `cmd/obor` (after `setup`'s init returns 0, the existing `leadRun.run`), not
inside `internal/setup`: setup's tests stay as they are. Its outcomes are the ones `obor lead` has today
(`cmd/obor/lead.go:104-134`): the lead already runs → `OK … runs`, nothing touched, so a rerun of init as repair,
by a person or by the lead itself, is still safe.

**`obor lead` with no config delegates.** Today it runs init and then continues (`lead.go:66-76`), and fails outside
a repository (`lead.go:61-64`) where init would have made one. Both branches become "run `obor init`, return its
rc". In a new folder the two commands are the same; the docs teach `obor init` for a new project and `obor lead` to
bring the lead back (`--resume`, after a Claude update).

**The confirmation stays one question at the lead**, not in init and not dropped:
- Not in init: a shell prompt can accept the table but cannot change a model sensibly; the lead can.
- Not dropped: the table decides cost (every role falls to opus when a CLI is missing, `oborinit.go:603-633`), and
  the standing rule is that init writes what it found, not what the owner chose (`skills/lead/SKILL.md:39`).
- Changed: skill step 3 today asks about five things (`SKILL.md:33-36`). It becomes one question with Keep first.
  `agents.confirmed` and `obor setup show` stay as they are.

Init stops printing the table (`oborinit.go:319`): the lead shows it seconds later in the same pane.

## 4. Delete or merge

| Today | Becomes |
|---|---|
| Hand-written `project.terminal`, committed first (trial step 1) | Detection, or `--terminal` |
| Refusal: no commit and a file not byte-equal to init's text (`init.go:214-240`, `onlyFiles` `:267-303`) | By path: in a repository with no commit, files at init's own paths are init's input and go into its commit whatever their bytes; any other file still refuses. `initFiles`' re-render and its "must follow configJSON's inputs" coupling (`init.go:242-265`) go. A config that does not load still fails before any write (`oborinit.go:109-115`). |
| `obor init`, then `obor lead` | One command |
| `obor setup check` as a trial step | Init's first step on hc; the command stays |
| Commit early, then edit the config and install the plugin (`oborinit.go:161` before `:174`, `:206-210`, `:244`) | In a new project: `lanes.count`, commands, roles, difficulty and the plugin install first, then the commit, then the worktrees. `.claude/settings.json` is tracked, so `initPluginLinks` (`:800-840`) is a no-op there and the link-then-commit failure of trial step 5 cannot happen in a new project. An existing repository: unchanged, init commits nothing. |
| Closing prose: merge.check, merge.compile, `--no-verify`, "change any of it" (`oborinit.go:287-298`) | Out of init; `skills/lead/commands.md` holds it. Warnings and hints stay. |
| `next: obor lead` and the skipped-commands hint to the person (`oborinit.go:312-317`) | Gone. `obor setup show` gets a line when `merge.check` is unset, so the lead reads it at step 3. |
| `the terminal (herdr) is not reachable` on any kind (`oborinit.go:90`) | Names the kind |
| Skill step 3's interview | One Keep/Change question |
| Flags | `--name`, `--prefix` stay (a rename later is refused while serve is installed). `--terminal` added. No `--no-lead`. |

Kept on purpose: the panes step (two lanes, integrator, reviewer placed at init). The owner asked for that layout
on 11-13 and the trial checked it; it costs no question.

Probably dead, check before deleting: init's "waiting on a Trust dialog" branch (`oborinit.go:299-302`, `:749-751`).
Init starts no agent (`:766`, `:769` pass `false`), so no pane of its can be held by a dialog.

## 5. Task split

| # | Task | Files | Difficulty | After |
|---|---|---|---|---|
| A | Terminal detection and `--terminal`; hc check as init's first step; unreachable message by kind | `cmd/obor/terminal.go` (detect, build by kind), `cmd/obor/setup.go:19-39`, `internal/setup/init.go` (`initArgs`, `configJSON`, `initFiles` passes the kind), `internal/setup/oborinit.go:72-94`, `internal/config/edit.go:357` (`ProjectDoc` takes the kind), `internal/project/pane.go` (pane variable by kind, exported), `internal/help` | medium | |
| B | One commit, path rule | `internal/setup/init.go:214-328`, `internal/setup/oborinit.go:134-253` (order only) | medium | A (same files) |
| C | Init ends with the lead; `obor lead` delegates | `cmd/obor/setup.go` (`initCmd`), `cmd/obor/lead.go:61-76`, `internal/help` lines for `init` and `lead` | simple | A; parallel with B |
| D | Summary and the one question | `internal/setup/oborinit.go:278-320`, `internal/setup/show.go` (unset `merge.check` line), `skills/lead/SKILL.md:15-17, 33-39`, `skills/lead/commands.md` | simple | B, C |
| E | The lead in the calling pane | `internal/app/port` (one `TerminalSetup` method: take the caller's pane as the lead's home when it qualifies), `internal/herdr/setup.go`, `internal/hc/place.go`, `internal/setup/agent.go` (`rolePane` for the lead) | hard | C |

Tests, per the test rules: A is a table test of the detection over env and two fake probes, plus one unit test that
a new config carries the kind. B changes the existing git tests of the first commit and deletes the byte-equality
ones. C fakes the lead run in `cmd/obor` (the `initRun` seam at `lead.go:29-30` turns around). E needs
`make conformance TIER=live TURN=1 KIND=claude`: it changes where an agent is started.

Lead's own work: `docs/design.md` (init, lead, terminal choice), a `docs/decisions.md` line, `hc-trial.md` steps 1-2
as one. A to D are one release and already give the owner's sentence; E can follow.

E reuses what exists: once the calling pane is labelled with the lead's name, `Locate` finds it as the lead's home
and the start is the scheduled one `obor lead` already uses from the lead's own pane (`lead.go:121-128`,
`internal/launch/self.go:16-22`), on both terminals.

## Risks

- **The both-set row rests on hc passing the core's env to every terminal** (probe P4). `CheckEnv` already rests on
  the same fact.
- **`obor init` now starts Claude.** A script or test that calls the real command gets a lead when none runs. Say
  it in the help line; no flag until someone needs one.
- **The path rule commits a hand-written file at one of init's paths** without showing it. That is the person's input
  to init; the commit line lists every file.
- **E on herdr**: obor's herdr contract has `pane.rename` but no tab rename (`internal/herdr/contract.go:22-28`).
  If herdr has none, the lead's tab keeps the name it had; check before the spec.
- **Claude's trust dialog stays.** Pre-accepting it means writing Claude's undocumented user file; not worth it.

## Flagged, not answered here

- herdr's `resume_agents_on_restore` line is the one manual, user-global edit left on herdr. Whether obor may write
  it is an owner decision.
- On hc the lead's first question shows only as `blocked` in herdr-app, and hc has no notifications (trial steps 2
  and 6). With the lead in the calling pane the person is looking at it; from Terminal.app they are not.
