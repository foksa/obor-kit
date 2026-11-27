# Lead feedback: idle while waiting on the owner; a visual check for UI/game tasks

Research for two items from city-defender's lead feedback (owner 11-27: research both, then BACKLOG). Code and docs
read only; nothing built or run. obor-kit at `staging` 371da823; city-defender read-only.

## Answers first

- **(A) Yes, an ack can stay in force until the owner's next message, and serve can learn of that message** from the
  lead's `user-prompt-submit` hook on Claude and OpenCode (the hook already sees the prompt text and already tells a
  typed prompt from obor's lines). agy maps its first PreInvocation to the same hook, but whether an agentapi line
  is told apart from the owner's input there has not been probed. Codex has no hooks, so serve gets no signal from it. Today
  the lead's hook sends serve nothing about its prompts. The real risk is not the owner ping. It is that the idle line
  is the only thing that wakes the lead after a clean merge. **Recommendation:** an owner-wait ack. While it is in
  force, a new idle key sends a line that needs no ack, and neither the second line nor the notification goes out.
  The wait ends at the owner's next typed prompt (hook), at the lead's next work (dispatch, brief, review request), at
  a plain `obor idle "<why>"`, or after a time bound.
- **(B) Start in the project, not in obor:** a project script (Playwright, run headless) writes deterministic
  screenshots and a frame contact sheet for each task. The spec names the scenes, the worker runs the script before
  `task_done` and looks at the output, and the report names the files. The lead can look at a PNG itself (on Claude),
  so "the lead cannot see the game" is only half true. Models do not watch video, so a clip is for the owner. Only if
  this sticks should obor take on the parts only it can do: a fixed output directory per task and the images on obor
  web's task page.

---

## (A) Idle while waiting on the owner

### Findings

**How the idle state is keyed and acked.**
- The state is keyed by the newest record that ended work the pipeline waited on. That is a merge, a drop, a
  finished role-agent brief, a verify's end, the last after-merge step, or a review request's end
  (`internal/app/idle.go:16-24`, `idleEnd` at `idle.go:178-200`; `skills/lead/commands.md:84-87`).
- `obor idle "<why>"` records one fact, `idle.ack{key, why, by, call, reply}` (`idleCall`, `idle.go:275-318`).
  The fold keeps only the last ack (`foldIdle`, `idle.go:70-82`).
- The state is open when nothing is in flight, the project has a lead, and `idleAck.Key != n.end` (`idleOpen`,
  `idle.go:227-230`). So every new end record re-opens it.
- Two rules act on an open state (`internal/app/rules.go:57-58`):
  - `idle`: after `idleSettle` (1 minute) it sends the lead
    `[obor] idle: nothing in flight (last: …) — dispatch next, or obor idle "<why nothing>"`.
  - `idle.ping`: `idlePingAfter` (30 minutes) after that line, it sends the line again and a herdr notification to
    the owner (`panes.Notify`, title `obor: <project> is idle`; `idlePingRule`, `idle.go:242-255`).
  - Each goes once per key.
- The lead or the owner may ack: `leadOrOwner` in `idleCall`. An ack with no agent is recorded as `by: owner`
  (`idle.go:303-306`).
- The lead's rule for this is `skills/lead/SKILL.md:85-88`: "Never end a turn unwired: dispatch the next task, or
  answer serve's idle line … 30 minutes unanswered: a second line and the owner is notified".

**Why "waiting on owner" must be re-acked.** The lead waits on the owner, but the pipeline keeps producing end
records: in-flight tasks merge, a `Review: on merge` or `Difficulty: hard` spec records a review request after its
merge, and the verify brief and its end follow (`docs/design.md:573-575`). Each one is a new key, so each needs a new
`obor idle`, or the owner is notified 30 minutes later.

**The idle line is the lead's only wake after a clean merge.** "Clean merges stay quiet for you"
(`skills/lead/commands.md:50`). A watch line comes only when the lead set a watch. So muting the idle line also
mutes the lead's nudge to dispatch work that does not depend on the owner.

**What the lead's prompt hook does today.**
- `guard.UserPromptSubmit` (`internal/guard/prompt.go:27-66`) does only two things for the lead:
  - the handoff reminder;
  - `hookLines`, which asks serve for queued lines only when the lead's lines go through hooks
    (`prompt.go:115-124`).
- It tells serve nothing about the prompt. `turn()`, the hook's call to serve, skips the lead:
  `guard.Turn` returns ok only for `role != "lead"` (`internal/guard/stopfailure.go:86`; caller
  `cmd/obor/main.go:303-305`).
- The worker branch already has the classifier this needs. `personTyped` (`prompt.go:183-`) returns false for prompts
  that start with `[obor ` (every obor line, `docs/design.md:592-594`), for slash commands, and for Claude Code's
  envelopes, including `<cross-session-message …>`. Claude Code's auto-continuations are skipped by origin fields
  (`prompt.go:77-88`). The captured payloads in `internal/guard/testdata` are the contract.

**Per kind: does the lead's hook see the owner's message?**

| kind | prompt hook | sees the owner's text | obor's lines also fire it? | source |
|---|---|---|---|---|
| claude | native `UserPromptSubmit` (`hooks/hooks.json:3-4`) | yes, `prompt` holds the full text | yes, inbox posts fire it (`docs/research/job-in-first-prompt-advice.md:234`), tagged `[obor ` so `personTyped` drops them | `docs/research/agent-cli-and-herdr-capabilities.md:107` |
| opencode | obor's plugin: the session prompt hook runs `obor hook user-prompt-submit` | yes | yes, inbox lines become prompts (metadata `obor: "line"`, text `[obor …`); a stop's block reason comes back as a prompt marked `"stop"` | `opencode/obor.js:10-38` |
| agy | PreInvocation with `invocationNum 0` → `user-prompt-submit`, `lastUserInput` → `prompt` | yes for a typed prompt | **unverified**: a line arrives through `agentapi send-message` "as a system message" (`docs/design.md:918-920`); whether its PreInvocation's `lastUserInput` is that line, empty, or the owner's last input has not been probed | `kinds/agy/kind/hook.go:31`, `docs/research/agy-probes.md` P3 |
| codex | none (`HooksNone`, `internal/agents/codex/codex.go:926`) | no | — | — |

The owner can also answer outside the lead's prompt:
- `obor idle "<why>"` from their own shell (already allowed).
- From the phone, through the lead's own Claude session (T-1619 named the sessions for that). Whether a prompt sent
  from claude.ai or the mobile app fires `UserPromptSubmit` locally is **unverified**. It is the same session, so it
  most likely does.
- obor web has no verb for this (`skills/lead/commands.md:46`: only the Setup page's edits).

### What breaks

- **The 30-minute owner notification.** While the owner is the one being waited on, a ping that says "idle" is noise.
  A note that says what the lead waits on is useful. A sticky ack that drops the ping silently loses that note: the
  owner may never learn that work stopped for them.
- **A merge while waiting.** If a merge wave's new key is fully covered, the lead gets no wake after it
  (`commands.md:50`). Work that does not depend on the owner then waits as well, which is exactly the "unwired"
  failure the rule guards against.
- **A missed clear.** If the wait is cleared only by the hook, a lost signal keeps it forever:
  - serve is down during the prompt (`leadLines` and `turn` skip when there is no socket);
  - the prompt hook times out (10 s);
  - a Codex lead (no hook);
  - an agy lead, if its lines and prompts cannot be told apart.
  A dispatch already ends idle (`idleNow` is not ok), but the wait must also be cleared then, or it covers the next
  quiet spell.
- **Classifier mistakes.** If an untagged message counts as the owner's (a raw post to the socket, or a new
  Claude Code envelope not yet in `personTyped`), the wait clears early. That is safe: it falls back to today's
  behaviour. If the owner's prompt is not counted, the wait stays, so a time bound is needed.
- **The owner's slash commands** (`/clear`, `/obor:lead`) are not "typed" by `personTyped`. `/clear` resets the lead,
  whose next `obor status` shows the state anyway. Acceptable.

### Options

1. **An owner-wait ack, cleared by the owner's next typed prompt (hook), with fallbacks.**
   - `obor idle --owner "<why>"` (name to be chosen) records `idle.ack{…, wait: "owner", since}`.
   - While it is in force, a new key gets a line that needs no ack:
     `[obor] idle: nothing in flight (last: …); waiting on owner: <why> — dispatch what does not need them`.
   - No `idle.ping` line and no idle notification. Instead, one herdr notification to the owner when the wait is
     recorded: `obor: <project> waits on you: <why>`.
   - The lead's `user-prompt-submit` hook, for a prompt that `personTyped` accepts, sends serve one new request
     (e.g. `owner_spoke{call id}`; serve down: nothing). This records a fact that ends the wait.
   - The wait also ends at the lead's next dispatch, brief or review request, at a plain `obor idle "<why>"`, and after
     a bound (e.g. 24 h; serve then treats the state as unacked: line, ping, notification).
   - Kinds: works on Claude and OpenCode. agy after a probe of what PreInvocation carries for an agentapi line (until
     then, treat agy like Codex). On Codex only the fallbacks apply.
   - Cost:
     - a wire request, a fact and its fold;
     - `idleOpen` and the two rules;
     - the hook call: the lead branch of `UserPromptSubmit`, reusing `personTyped`;
     - the `obor idle` flag and help;
     - `commands.md` and `SKILL.md`, `docs/state-model.md` rows;
     - tests with the captured payloads.
     - Roughly 200–300 lines with tests, plus one agy probe.
2. **An owner-wait ack with no hook: sticky until the lead's next work or a time bound.**
   - The same fact and line, but nothing reports the owner's message. The wait ends at a dispatch, brief or review
     request, at a new `obor idle`, or after the bound.
   - Agent-neutral (Codex included) and no new transport. Roughly 100–150 lines.
   - What breaks: when the owner answers and the lead ends its turn without acting, nothing reminds anyone until the
     bound.
3. **Keep the keys; only soften the ping (docs plus a small rule change).**
   - Leave every new key needing an ack, but let an ack carry `wait: owner`. While the last ack is an owner-wait,
     `idle.ping` sends the owner's notification as "waits on you: <why>" and sends no second line.
   - The lead still re-acks each wave: one command per wave, with the why repeated.
   - Roughly 40–80 lines. It removes the misleading ping, not the friction the lead reported.

### Recommendation

Option 1, built so that it falls back to option 2 per kind. The hook signal is there on the two kinds that matter now
(Claude, OpenCode), the classifier already exists, and the fallbacks (lead work, explicit ack, time bound) cover Codex,
agy, and a missed hook call. Keep a line for each new key during the wait, wording it so it needs no ack: that keeps
the wake after a merge wave, the one thing the current design gets right. Move the owner's notification from "idle,
unanswered after 30 min" to "waits on you", sent once.

Before building:
- probe agy: what PreInvocation's `lastUserInput` holds for an `agentapi send-message` line;
- confirm that a prompt sent from the Claude phone app fires the local `UserPromptSubmit`.

Unknowns: both points above, and the time bound's value (owner's call).

---

## (B) A visual check for UI/game tasks

### Findings

**obor carries no images today.**
- A task's report is text: `task_done{report}` with the sections Summary, Self-check, Skipped and Open questions
  (`docs/design.md:480-483`; `docs/state-model.md` row 16).
- The merge commit's body carries the spec and the Summary (`skills/lead/commands.md:118-120`).
- obor web serves JSON APIs, plus project docs from tracked files. There is no route for images
  (`internal/web/web.go:104-119`).
- obor web is loopback only: remote access is an SSH tunnel (`skills/lead/commands.md:36-46`). So "the owner on the
  phone" cannot reach obor web today without a tunnel.

**Merge flow.**
- Serve's gate runs `merge.compile` and `merge.check`, then the integrator, then the merge (`docs/design.md:522-581`).
- `after_merge` commands run after a merge on the target (`internal/config/config.go:629`, `:982`).
- A spec line can opt a task into a step: `Review: on merge` records a review request (`docs/design.md:573-575`).
- The lead does not approve each merge, so any look before a merge has to be the worker's or the gate's.

**Workers.**
- "No installs" (`templates/agents/worker.md:18`).
- Scratch scripts are written with the file-write tool and then run (`worker.md:34`).
- A browser binary is therefore a one-time machine install by the owner. A devDependency added in a task reaches the
  lanes through `lanes.setup` (city-defender: `npm ci`).

**The designer role.**
- `docs/agents/designer.md` exists in obor-kit only, for obor web's pages:
  - "Look at the live page first", with screenshots to a temp dir;
  - "if a tool you need is missing (… a browser), say so";
  - output is a claude.ai design artifact plus `docs/ui/<page>.md`.
- It looks before work starts, not after a task. Claude artifacts are Claude-only.
- city-defender has no designer role (`docs/agents/`: advisor, deep-dive, integrator, lead, researcher, reviewer,
  triage, worker).

**city-defender.**
- Vite 8 plus Pixi.js 8 on a canvas (`package.json`; `index.html`: `#game` canvas, `#ui` overlay).
- Scripts: `dev`, `build`, `preview`, `check` = typecheck + vitest + build. That is also `merge.check`
  (`.obor/config.json`).
- No Playwright or Puppeteer in `node_modules`.
- Workers are Claude (`opus`, `sonnet`, `haiku`); the reviewer is Codex (`terra`).
- What helps determinism:
  - The sim is pure and steps at a fixed 60 Hz (`src/loop.ts:1-12`).
  - The seed is the only nondeterminism, but it comes from `Math.random` in `src/main.ts:48-57`, with no URL seed.
- URL flags exist:
  - `?debug`, the renderer's debug view (`src/ui/dev-menu-model.ts:48-51`);
  - `?tune`, the tuning panel;
  - `?barrel=` (`src/view/sprite-choice.ts:35-37`).
- `tools/fx/gif.ts` is already a dependency-free GIF encoder, used for fx previews.
- The bugs found in playtests: scale and speed (`docs/decisions.md:62-64`, "guns have 50 metres range"), bombs hit at
  the nose not the tail (`decisions.md:12-13`), and size and speed tuning (`decisions.md:14-15`). All of them show in
  a still frame with `?debug` (hit point against sprite) or in a strip of frames at known sim times (speed, scale). No
  real-time video is needed.

**Who can look.**
- A Claude lead or worker reads a PNG file directly (its Read tool is multimodal), so a screenshot path in a report is
  enough for a Claude lead.
- Codex has an image tool, but it is not verified inside obor's roles. OpenCode and agy depend on the model.
- No agent watches video. A clip is for the owner. For the lead, a **contact sheet** (N frames at fixed sim times,
  tiled into one PNG) carries speed and scale.
- Wall-clock capture is unreliable on this machine (load 20–50 on 10 cores, `docs/design.md:577-580`), so frames should
  be stepped, not recorded live.

### Options

1. **Project only: a capture script plus a spec section.** No obor change.
   - **Script.** city-defender adds `npm run shots -- <scene…>`:
     - Playwright (a devDependency; the owner runs `npx playwright install chromium` once) loads `vite preview` with
       `?seed=<n>&scene=<name>[&debug]`.
     - It steps the sim by a fixed number of steps through a small test hook (or Playwright's fake clock;
       **unverified** that it drives Pixi's ticker) and writes PNGs plus one contact sheet. A GIF can use
       `tools/fx/gif.ts`.
     - Output goes to an ignored dir in the worktree (`shots/`).
   - **Spec.** The task-spec template gets a `Visual:` section that names the scenes and what to check ("bomb hit at
     the nose with `?debug`", "wave 3 at t = 0, 2, 4 s").
   - **Worker.** Runs the script before `task_done`, looks at the images (Claude), and names the files and what it saw
     in Self-check.
   - **Lead.** Reads the contact sheet in the lane worktree when the report arrives, or reruns the script on the
     target after the merge.
   - **Owner.** Unchanged (playtest). The GIF or PNGs can be opened from the machine.
   - Cost: project only, about 150–250 lines:
     - the script, the `?seed`/`?scene` params and the step hook in `src/main.ts`;
     - the template and the worker doc;
     - the Chromium install (about 150–200 MB, shared by the lanes).
   - Agent-neutral as a command. Seeing the images depends on the model.
2. **obor carries the output: a per-task directory, a gate step, and the images on obor web.**
   - Config `merge.visual: "<command>"`, run for tasks whose spec says `Visual:`. It runs as a non-gating step after
     `merge.check` passes, with `OBOR_TASK` and `OBOR_OUT=<root>/.obor/state/visual/<task>/`.
   - Serve records the files, the task page in obor web shows them (a new route that serves images from that directory
     only), and the lead gets `[obor] T-xx visual: 4 shots → <dir>`.
   - The owner sees them on obor web, which today means only at the machine or through a tunnel.
   - Cost, roughly 400–700 lines with tests:
     - the config key and the step in `internal/checks`;
     - the fact and line;
     - the web route plus a Svelte component and the `internal/web/dist` rebuild;
     - pruning old images;
     - the docs in `design.md`, `state-model.md` and `commands.md`.
   - Still needs option 1's project script. This adds visibility, not capture.
3. **A role looks: a playtester or designer brief after the merge.**
   - The lead briefs a read-only role (designer.md adapted per project) after a UI task merges. The role runs the
     project's script on the target, compares the shots with the spec and `docs/decisions.md`, and writes
     `docs/ui/T-xx.md` with verdicts.
   - On Claude it can publish a private claude.ai artifact with the images, which the owner can open on the phone
     (Claude-only).
   - Cost: obor about 0, since roles and briefs exist. Per project: a role text and its write list. A role turn per UI
     task.
   - It looks after the merge, so a fix becomes a new task.

### Recommendation

Option 1 now, in city-defender. It needs no obor change, and it targets the bugs actually found, which a
deterministic still or a frame strip shows (the hit point with `?debug`; speed and scale at fixed sim times). The worker
is the first to look, before `task_done`, and the lead reads the contact sheet named in the report.

Put two things in obor docs only:
- an optional `Visual:` section in `templates/` task-spec guidance;
- a line in the worker role saying that a capture script, when the project has one, runs before `task_done` and its
  files are named in Self-check.

Revisit option 2 when a second UI project wants it, or when the owner wants the images on obor web. Option 3 fits a
milestone playtest rather than every task, and its phone path is Claude-only.

Before building:
- add a `?seed` to city-defender's URL;
- check that headless Chromium renders Pixi 8 (WebGL through SwiftShader, or a WebGPU fallback);
- check that Playwright's fake clock or a test hook can step the sim.

Unknowns:
- Codex's image viewing inside obor roles;
- whether headless Chromium starts inside Codex's sandbox (it matters if a Codex worker or reviewer runs the script);
- whether an iPhone plays a Playwright `.webm` clip (a GIF or MP4 is safer).
