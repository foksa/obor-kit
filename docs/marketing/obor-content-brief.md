# obor: brief for a blog post and a product page

## What obor is (one line)
obor runs a small team of coding agents on one repository: you talk to one lead, workers build tasks in their own
git worktrees, an integrator reviews and merges, a reviewer from another model family checks the merged work.

## Who it is for
Developers who already use Claude Code, Codex or OpenCode and have hit the limit of one agent in one chat: lost
context, work that "finished" but broke main, babysitting several terminals by hand.

## The problem (say it plainly)
- Agents forget. Plans, decisions and task state live in a chat that gets compacted or closed.
- One agent is serial; several agents in parallel collide in the same checkout and on main.
- "Done" from an agent is a claim, not a merge: nobody ran the checks on the merged result.
- You end up the scheduler, the merge queue and the reviewer.

## How obor answers it (key messages)
1. **You talk to one agent.** The lead writes short specs and decides; it never merges and never polls.
2. **State lives outside the chat.** A background process (`obor serve`) keeps an append-only log of every task,
   gate and merge; a crashed serve replays it and carries on. Agents can be restarted at any time.
3. **Parallel without collisions.** A pool of lanes, each its own git worktree and branch. A spec's difficulty picks
   the model (hard → strongest, simple → cheapest); dispatch takes the first free lane.
4. **Nothing reaches main unchecked.** Each task is compiled onto the target branch, tested in its lane, reviewed by
   the integrator, then merged. A nightly runs the full check.
5. **A second opinion from another model family.** Reviews run every few merges (and on every hard task); findings
   become fix tasks, and the same reviewer verifies the fixes.
6. **Everything visible.** Every agent runs in a terminal pane you can watch and type into (herdr); a local web page
   shows lanes, queue, reviews and what needs you.
7. **Guards, not trust.** Hooks keep workers in their lane: no push, no edits outside the worktree, no protected files.
8. **Mixed models.** Workers can be Claude Code, Codex or OpenCode (e.g. DeepSeek through OpenCode); models per
   difficulty level are config.

## Proof points (real, from the project)
- obor is built with obor: the kit develops itself through its own lanes, reviews and releases (task ids past T-1400).
- Example day (2026-11-21): a census found the test suite started 5,351 git processes per run; the lead specced two
  waves of tasks across four lanes, each reviewed by a Codex reviewer, and the count fell to 2,671 the same day with
  no behaviour change. Worth using as a concrete story of the loop: research → specs → parallel lanes → review → release.
- Setup is one command: `obor init` in an empty folder sets up git, config, lanes, panes and serve, then starts the lead.

## Honest limits (must appear somewhere)
- Early: built and used daily by one person; breaking changes between minor versions (each has an upgrade guide).
- macOS first; Linux binaries built but untested.
- Needs herdr (terminal workspace) and Claude Code for the lead and integrator.
- Several agents work at once (lead, workers, integrator, reviewer), so it uses your Claude/Codex plan's usage
  limits (or API spend) several times faster than one agent in one chat.

## Do not claim
- No benchmarks, speed-up factors or "replaces developers". No team/company usage. No security guarantees beyond
  the guards described. No features not listed above.

## Blog post: suggested shape (800–1,200 words)
- Hook: a personal moment where one agent was not enough (owner supplies the anecdote).
- The three problems: forgetting, collisions, unchecked "done".
- What obor does, in the order of one task's life (spec → lane → gate → review → merge), with the diagram idea below.
- The example day above as the story.
- What it costs and where it is rough (limits list).
- Close: install line and link.

## Product page: suggested sections
1. Headline + subline (options below), install command, GitHub link.
2. "How it works": one task's path as a 5-step strip: Spec · Lane · Gate · Review · Merge.
3. Six feature tiles: one lead · state outside chat · parallel lanes · checked merges · cross-model review · visible panes.
4. "Bring your agents": Claude Code, Codex, OpenCode.
5. Status and requirements (limits, plainly).
6. Footer: docs, upgrade guides, license.

Headline ideas: "A team of coding agents, one conversation." / "Parallel agents. Nothing reaches main unchecked." /
"Your agents forget. obor doesn't."

## Tone
Plain, concrete, first person for the blog (the owner built it for themself). Short sentences, real commands, no
hype words ("revolutionary", "10x", "autonomous").

## Facts for the writer
- Install: `curl -fsSL https://raw.githubusercontent.com/foksa/obor-kit/main/install.sh | sh`
- Start: `obor init` in a project folder inside herdr
- Repo: https://github.com/foksa/obor-kit · current release v0.42.1
- Ships as a Claude Code plugin plus one Go binary.
