# OSC 7501 in obor: a terminal-neutral home, as the base for Rex (advisor, 2026-11-25)

Question (lead, for the owner 11-25): herdr's TUI stays as it is; architect OSC 7501 properly in obor before any Rex
task. A plan to read, nothing built. Facts: `docs/research/osc7501.md`, `docs/plan/terminal-adapters.md`,
`docs/research/terminal-port-advice.md`, `docs/plan/herdr-core.md`, `internal/hc`, `internal/app/port`, T-1563
(`3cd3397e`). Line numbers are at `3cd3397e`.

**Not done.** No Rex, hc or herdr terminal was driven and no Rex server was contacted. The Rex facts new here are from
the installed bundle (0.1.0, build 2061): `--help` of the `rex` binary in it and the Go struct tags in that binary.
They say what the API's fields are, not how a live agent behaves in a block.

## Recommendation

1. **One small leaf package holds what obor makes of a 7501 record; adapters only decode into it.**
   `internal/progstatus`: the record's four fields and the three rules on them (word, note, sanitizer). No parser of
   the escape sequence: hc and Rex both relay decoded JSON, and nothing gives obor raw bytes.
2. **`port.Pane` does not change.** It keeps `Status` (the word every decision reads) and `Note` (shown only). The
   record itself does not cross the port.
3. **On a terminal that relays 7501 it is the only source of the status word and of blocked.** Hooks are not a
   second status source. D3a ("hooks, then 7501") is revised to "7501; never the screen", which is what HD3 and HD4
   already decided for hc.
4. **The blocked guard is one rule in front of every terminal's prompt**, applied where the terminal is built
   (`cmd/obor/terminal.go`), reading `port.Pane`. A terminal's own refusal (herdr, hc) stays behind it as the atomic
   backstop.
5. **Of plan step 7, only the guard and (perhaps) a shared typed start are still needed for Rex.** obor's own status
   fold, a blocked hook and `Caps.Status` as a per-kind status source are not.
6. **Order: extraction, then the guard, both with no live Rex; then a cut probe run; then the adapter.**
7. **There is a `rex` CLI on this machine**, inside the bundle, not on `PATH`:
   `/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex`. D4's "the CLI" holds; how obor
   finds it is a new small decision.

8. **No library.** obor never sees the escape sequence, so a parser, scanner or stripper has no caller; the record
   is four strings. The one case that would justify a dependency is named in §5.
9. **obor does not emit 7501 for its own processes now.** Where the owner works nothing would show it, and a serve
   that says `blocked` points at a pane where nothing can be answered (§6).

## 1. The home

### Package and types

```go
// Package progstatus is a program's own status as a terminal relays it (OSC 7501): the root record's fields and what
// obor makes of them. An adapter decodes its terminal's wire into a Report; nothing here knows a terminal.
package progstatus

type Report struct{ State, Kind, Msg, App string } // the root record

func (r Report) Word() string // idle, working, blocked, done as they are; error is idle; any other word unknown
func (r Report) Note() string // port.Pane.Note: "<kind>: <msg>" when blocked, "error: <msg>", else msg; shown text
func Shown(s string) string   // text a program wrote, as obor shows it: one line, no control characters, 160 runes
func TurnEnded(word string, until []string) bool // idle and done each meet the other (OpenCode never says done)
```

- **In the record: `state`, `kind`, `msg`, `app`.** These are what hc relays and obor reads
  (`internal/hc/panes.go:35-40`), and Rex's record has the same four names (below).
- **Not in it: `id`, `progress`, `title`.** No consumer exists. Only the root record is read (`id` absent). Each is
  added by the task whose card or decision needs it, with that consumer named.
- **`error` stays idle.** The mapping moves as it is. A sixth port word is a separate question (it touches every
  reader of `Ready()`); flagged, not proposed.
- It imports nothing of obor's. The kind lookup is a fact about kinds, so it goes to `internal/agents` as
  `agents.ByApp(kinds, app)` (it reads only `Caps.App`, `internal/agents/agents.go:499-501`).

### What moves out of `internal/hc`

| Today | To |
|---|---|
| `statusWord` (`panes.go:101-109`) | `progstatus.Report.Word` |
| `(*Status).note`, `noteMax`, `sanitized` (`panes.go:114-146`) | `Report.Note`, `Shown` |
| `joined` (`control.go:59-67`) | private to `progstatus` (its two other callers print raw `kind`, `msg` in an error: `control.go:44`, `start.go:123`; they use `Note()` instead, so no unsanitized program text reaches a message) |
| `kindOf` (`panes.go:83-97`) | `agents.ByApp` |
| `turnEnded` (`control.go:151-154`) | `progstatus.TurnEnded` |

The tables in `internal/hc/record_test.go` for these move with them and are deleted there; hc keeps one row that
its record reaches the pane.

### What an adapter still does

Decode its wire into a `Report`, then three lines, the same in each adapter:

```go
p.Agent = cmp.Or(agents.ByApp(kinds, rep.App), <the kind obor recorded at its start>)
p.Status, p.Note = rep.Word(), rep.Note()
```

- **hc** keeps its wire structs (`Record`, `Status`, `Foreground`: its schema check lists them,
  `internal/hc/contract.go:36`), the `obor.kind` label as the fallback kind, and "a program other than the shell in
  the foreground with no status is `unknown`" (`panes.go:70-71`).
- **Rex** reads the root record of a block's `program_status` list and the `program_status_changed` /
  `program_status_removed` events. From the binary's struct tags (build 2061): a record has `id` (left out for the
  root), `state`, `kind`, `msg`, `title`, `progress`, `app`, `effective_app`, `updated_at`, `owner` (the foreground
  process when the report arrived); the changed event carries the whole new `record` with `id` and `reason`; the
  removed event carries `ids` and a `reason` of `clear`, `child_exited`, `reset`, `prompt`, `foreground` or
  `evicted`. So Rex drops a record itself when the program exits or leaves the foreground.
- **herdr** fills `Status` from its own detection and leaves `Note` empty, as now. It does not import the package.

### `port.Pane`

Unchanged: `Status`, `Note`, `Agent`. Only the `Note` comment's example stops naming hc alone. Everything T-1563
wired (the status line, the needs-you text, the web card, `wire.AgentStatus.Note`, `event` `PaneNote`) reads a
string and keeps reading it.

## 2. Which facts come from where

| Fact | Source on hc and Rex | Note |
|---|---|---|
| idle, working, done | 7501 root record | all four kinds emit natively (`osc7501.md` §4) |
| blocked, its kind and msg | 7501 | never the screen; `hooks/hooks.json` registers no permission hook and needs none |
| Which kind runs | 7501 `app` through `Caps.App`, else the kind obor recorded at the start | agy reports `antigravity`: its manifest needs `"app"` (one line; today the label covers it on hc) |
| Up after a start | foreground is not the shell and a first status arrived | hc's rule (`internal/hc/start.go:114-130`) |
| A dialog that reports nothing (Codex trust) | the start's one screen read, as today | the only screen use; never for status |
| A turn's start and end for the stop watchdog | obor's hooks (`TurnReq`, `internal/app/wire/request.go:280`), Codex's transcript | job facts, not pane status; unchanged |
| Session | obor's | 7501 has none |
| A prompt was delivered | the agent's `job_next` | unchanged |

Why not hooks first: a hook exists for one kind (Codex has none, `internal/agents/codex/codex.go:926`), so it is not
agent-neutral, and a hook's blocked beside the record's is two sources for one fact with no rule for which clears
it. Known holes of 7501 alone, accepted on hc already: Codex's update dialog is silent, OpenCode shows an API error
as idle, Claude stays `working` on an unreachable API (`osc7501.md` §1, `herdr-core.md:71`).

### The guarded send

Today the rule is each adapter's: herdr's `agent.prompt` refuses natively; hc's `Prompt` reads the record and
refuses, then hc refuses again natively (`internal/hc/control.go:34-56`). A Rex adapter would be the third copy and
the only one with no native refusal behind it.

One rule, in `internal/app/port`, put in front of the adapter's `Prompt` at the one place a terminal is built:

```go
// GuardPrompts is c with obor's rule in front of Prompt: a pane that runs no agent is refused as NotFound, a blocked
// one as Blocked (its Note in the message), before any key; then c's own Prompt types.
func GuardPrompts(c PaneControl) PaneControl
func Blocked(err error) bool // a predicate beside NotFound, PaneBusy, NotReady
```

- It reads `Pane(ctx, pane)`: `Agent == ""` and `Status == "blocked"`. The same two facts on every terminal.
- Every typed prompt passes it: the loop's (`internal/app/prompt.go:90`) and launch's first wake
  (`internal/launch/launch.go:640`), with no change at either call site.
- An adapter's `Prompt` becomes its terminal's way to type to an agent: herdr `agent.prompt`, hc `terminal.send`
  without `force`, Rex clear then paste and Enter. hc's own read and refusal (`control.go:35-45`) is deleted. herdr's
  and hc's native refusals answer `Blocked` too.
- **The gap on Rex.** Read, then write: a dialog that opens in between gets the text. herdr and hc close it inside
  one call; Rex cannot. How long a kind takes to report blocked after its dialog shows is a probe item (R3).

## 3. `terminal-adapters.md` steps 7-9, as of today

### Step 7 (obor's agent layer)

| Part of step 7 | For Rex |
|---|---|
| Status folded from hooks and transcripts; unknown until a source speaks | **Not needed.** The record is the status |
| A blocked hook; `Caps.Status` naming a kind's own source | **Not needed** |
| The comparison log on herdr; the live run with `AgentStarter` off | **Not needed** (dropped by HD3) |
| Which kind runs | **Small, in the adapter**: `agents.ByApp`, else the recorded kind |
| Prompt refused when blocked | **Needed**: the guard above, for every terminal |
| Start: command line at the shell, up on foreground and first status, `NotReady` from the screen | **Needed only if D5 picks the shell model.** hc has exactly this (`internal/hc/start.go`). Rex must not copy it: the start then moves to one owner over `PaneControl` when the Rex adapter needs it. If D5 picks "agent as the block's command", Rex's start is its own and hc's stays |

Step 7 becomes: **7a** the `progstatus` extraction, **7b** the guard. Both run on hc and herdr today.

### Step 8 (probe): what is still unknown

On a throwaway server only, as the plan says. Dropped: R6 (seen 11-11: the server outlives the app), R11 (measure in
use), R13 (hc's macOS notifier answers D6; it needs one owner outside `internal/hc` when Rex uses it), R3's payload
shape (read from the binary) and R3's hook-written record (no kind writes 7501: `osc7501.md`, order 4).

| # | Still unknown | Decides |
|---|---|---|
| R10 | the scratch server leaves the owner's server, config and app untouched | whether any other probe runs |
| R12 | blocks made through the API show in the app with nobody attaching; `focus: false` | go or no-go |
| R1 | `rex api call` time per call; `rex events --all --json` shape and its end when the server stops; `rex api describe` for the exact method and event names | D4 |
| R3 | each kind in a block: is there a root record, the words over a turn, blocked with kind and msg at a permission prompt, how soon after the dialog; Codex, which sends no query; removal when the agent exits | the adapter's read, the guard's gap |
| R4, R8 | a new block's env (and whether `OBOR_*`, `HERDR_*` leak from the server's); a block kept after its program exits | D5, identity |
| R7 | a multi-line paste and Enter into Claude's and Codex's input (`rex send`, `rex send-key` exist) | `Write` |
| R9 | `process` for shell, agent, tool | `AtShell`, "up" |
| R2, R5 | the event of a relabel; what a server restart brings back | label merge, restore |

### Step 9 (adapter)

As written, with: the status is `progstatus.Report` from the block's root record (no agent code in `internal/rex`
beyond the three lines); `Prompt` is clear, paste, Enter behind the guard; the sub-steps follow hc's
(`herdr-core.md` steps 2-4, 6: client and read side, write side and start, selectable with identity, a real project).

**D4.** The CLI exists but is inside the app bundle and not on `PATH`. The transport choice stands (the WebSocket's
framing is undocumented and needs a dependency). New: obor needs `rex` on `PATH`; `obor setup check` on a Rex
project fails closed when it is missing and names the bundle path as the hint. obor does not guess a path inside an
app bundle.

## 4. Order and sizes

| # | Task | Live Rex | Size | Notes |
|---|---|---|---|---|
| 1 | Extraction: `internal/progstatus`, `agents.ByApp`, hc decodes into it | no | simple | no behaviour change; tests move, none added; boundary and module-map rows |
| 2 | The guard: `port.GuardPrompts`, `port.Blocked`, hc's pre-read deleted | no | medium | touches the first wake: live tier `TURN=1` per kind on hc and herdr |
| 3 | agy's `"app": "antigravity"` in its manifest | no | trivial | can ride with 1 |
| 4 | Rex probe, the table above; `docs/research/rex-probes.md` | **yes** | half a day | no product code; lead or researcher, the owner's scratch server rule |
| 5 | Rex adapter | yes | hard, 3-4 tasks | after 4 and D4, D5 |

1 before 2 (the guard's message uses `Note`). 1-3 need nothing from Rex and can run now; 4 is independent of 1-3.
The extraction is its own task and comes before Rex: with it the Rex adapter starts as a decoder, and without it
the first Rex task would copy `statusWord` and the sanitizer or do the extraction inside a hard task.

## 5. A library, or obor's own record type (owner follow-up 11-25)

**obor's own type; no dependency.** Checked on the Go module proxy today (source read from the proxy's zips, nothing
built or run):

| Module | Latest | Spec rev | What it is | Notes |
|---|---|---|---|---|
| `github.com/wolfeidau/osc7501` | v0.4.0 (11-12) | 0.4 | `Reporter` (emit), `Parse`, `Scanner` (reports and the query in a byte stream), `Stripper`; 741 lines, fuzz tests | Apache-2.0, standard library only (testify for tests). **Its `go.mod` declares `github.com/buildkite/osc7501` from v0.3.0 on**, so the wolfeidau path cannot be required at 0.3 or 0.4: the usable path is buildkite's |
| `github.com/SamiNassim/osc7501` | v0.1.0 (10-31), its only tag | 0.4 | `Encoder`, `Decoder`, the query and its reply (`Capabilities`); 817 lines | MIT, standard library only |
| `tuios.dev/progstatus` | v0.1.1 (10-23) | **0.2** | parser, a record store (limits, eviction, lifetime), a summary rule, encoder; 766 lines | MIT, standard library only; two revisions behind |

The dependency rule (`AGENTS.md`) would allow one: a published spec, cgo-free. The reason against is that nothing in
the design calls what they offer:

| Part of the design | Needs a library? |
|---|---|
| hc's status | No. hc sends a decoded object (`internal/hc/panes.go:35-40`) |
| Rex's payload | No. Decoded JSON records, read from the binary's struct tags (§1); R3 is about behaviour, not format |
| The sanitizer and limits | No. The libraries enforce the wire's rules (byte limits, base64, control characters on a report); hc and Rex already did that before obor sees a record. obor's `Shown` is a display rule (one line, 160 runes), which no library has |
| The record type | No. Four strings. Each library's `Report` carries `id`, `progress`, `title` and emit-side validation obor would not use |
| An emitter (§6) | No. A report is `key=value` pairs, base64 for `msg`, `ESC \`: about 25 lines beside the record. The query is not needed to send (spec 0.3) |

**When one would be right:** an adapter for a terminal that hands obor a pane's raw output instead of records (a
tmux-like multiplexer), where obor would have to find reports split across reads and apply the spec's discard rules
itself. That is the part worth not writing: take `github.com/buildkite/osc7501` then (rev 0.4, a scanner with fuzz
tests), as its own task with the owner's approval. No such adapter is planned.

Against all three today: each is weeks old with one maintainer, and the spec moved from 0.1 to 0.4 in twelve days.
A name note: tuios's package is also called `progstatus`; obor's is `internal/progstatus`, with no relation.

## 6. Should obor emit 7501 for its own processes (owner follow-up 11-25)

**Not now.** The case is real only for a person's one-shot command at a terminal that shows the record; nothing the
owner runs today meets both halves.

| obor process | Holds a terminal? | What it could say | Verdict |
|---|---|---|---|
| `obor serve` under launchd (obor-kit) | no tty | nothing | nothing to emit to |
| `obor serve` in a pane (adopters) | yes | `working` while a task is in flight, `blocked` on a needs-you, `idle` | **No.** `blocked` means "this terminal waits for its user": typing in serve's pane answers nothing, and the agent's pane that really waits already reports itself. One word for a whole project is lossy, and serve is never `done` |
| The full check, the nightly | the nightly runs inside serve; a lane's check runs under an agent's tool | `working`, then `done` or `error` with the RED line | **No.** No tty in the first; in the second it is the agent's pane |
| `make release`, `make ship` | yes, when the owner types them at a shell | `working` (with `progress` per step), `done`, `error` with the failed step | the one honest case; small value (below) |

**The hazard is the second writer again.** A command run by an agent's tool that does reach the pane's tty would
replace the agent's root record (`osc7501.md` §4): the pane would read `done` with `app=obor` while the agent still
works, and obor's own wait would take the turn as ended. Claude's tool has no tty here; the other kinds' are not
probed. A record under its own `id` is safe but invisible: hc's `status` and obor read only the root.

**Where it would be seen.** herdr's TUI: nowhere (it does not consume 7501, and stays so). herdr-app and hc: the
terminal's status. Rex: session picker and tab headers. kitty: yes (0.4). Ghostty: parsed, not shown yet.

**Worth it?** Not today: the default terminal shows nothing, obor-kit's serve has no tty, and a needs-you already
reaches a person by the lead's line, `obor status`, the web page and a notification. If the owner later works in Rex
or herdr-app and runs releases there by hand, the small version is: `make release` and `make ship` (through one
`obor` verb, not `printf` in a recipe) send a root record `working` → `done` or `error`, and `clear` on a signal;
only when stdout is a terminal and no `OBOR_ROLE` is set (so never inside an agent's pane); obor's own encoder in
`internal/progstatus`; no query. Simple, one task. Serve stays silent in every version.

## Decisions for the owner

| # | Decision | Recommended |
|---|---|---|
| A1 | The home: a leaf package `internal/progstatus` (record, word, note, sanitizer), or the same in `internal/app/port` | the package: the port stays contracts; the core never imports it |
| A2 | `port.Pane` keeps `Status` + `Note`, or carries the record (`kind`, `msg` as fields) | keep; add a field when a decision or a card needs `kind` alone |
| A3 | D3a revised: on a terminal that relays 7501, it alone gives status and blocked; no hook source; the screen only for the start's silent dialog | yes |
| A4 | The guard as one rule in front of every terminal, herdr included (one more pane read per prompt there), or only where the terminal has no native refusal | every terminal: one rule, one test table |
| A5 | Tasks 1-3 now, before the probe | yes |
| A6 | D4: the `rex` CLI, required on `PATH` | yes, after R1 |
| A7 | A library for OSC 7501, or obor's own record type | own type; a library only with an adapter that reads raw pane output (then buildkite's) |
| A8 | obor emits 7501 for its own processes | no; revisit for `make release` / `make ship` only, when the owner runs them in a terminal that shows it |

## Flagged, not expanded

- **The guard hook has no rule for `rex`.** As for `hc` (`herdr-core.md:374`): inside a block an agent can run
  `rex send`, `rex run`, `rex kill` on every other block. A deny rule is needed before a worker runs on Rex.
- **A refused prompt counts as a failed step** (`internal/app/prompt.go:70`, `loop.go:539`). With `port.Blocked` the
  loop could wait instead of count; the watchdog raises the blocked needs-you either way. Not part of task 2.
- **hc's `WaitStatus` and its notifier** (`internal/hc/control.go:161`, `panes.go:204-241`) are the next two things a
  Rex adapter would copy. Each moves to one owner in the Rex task that needs it, not before.
- **Rex says "treat a state you don't know as idle"**; obor's word for it is `unknown` (no prompt is typed). Keep obor's.
- **`docs/plan/terminal-adapters.md` steps 7-9 and its D3a row** still read as on 11-11. This file is the revision;
  the plan's text should be replaced from §3 once the owner answers.
