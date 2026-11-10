# Plan 007: `obor review close --accept` writes `docs/accepted.md`, not `BACKLOG.md`

> **Executor instructions**: Follow this plan step by step. Run every verification command and confirm the expected
> result before moving to the next step. If anything in the "STOP conditions" section occurs, stop and report; do not
> improvise. When done, update the status row for this plan in `docs/research/improve-plans.md`, unless the lead
> dispatched you and keeps the index.
>
> **Drift check (run first)**: `git diff --stat 65389c8c..HEAD -- cmd/obor/reviewcycle.go cmd/obor/reviewcycle_integration_test.go internal/app/reviewclose.go internal/app/review.go internal/app/port/workflow.go skills/lead templates/README.md templates/BACKLOG.md`
> If any of these changed since this plan was written, compare the "Current state" excerpts against the live code
> before proceeding; on a mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S-M
- **Risk**: LOW
- **Depends on**: none
- **Category**: process
- **Planned at**: commit `65389c8c`, 2026-11-10 (idea A7 of `docs/research/improve-architecture.md`)

Spec lines, if the lead turns this into an obor spec: `Difficulty: medium` · `Guarantee: none (where a line is
written; the close's refusals stay)` · `Replaces: app.AcceptLines and the close's "BACKLOG.md has no Open section"
refusal, deleted here` · `Adds: docs/accepted.md, written by obor review close --accept (created when missing)`.

## Why this matters

`BACKLOG.md` is the file every lead session reads first. In obor-kit it is 38 KB, and 67 of the 91 bullets of its
"Open" section are accepted review findings: each is to be fixed only if its symptom shows again. They are written
there by `obor review close --accept "<line>"`. A lead re-reads them at every session start and after every `/clear`,
and they bury the items that are really open. After this plan the close writes accepted lines to `docs/accepted.md`,
a file no role reads at session start; `BACKLOG.md` keeps the handoff, the owner's pending decisions, open work,
concerns and ideas. An accepted line is looked up (grep) when its symptom shows.

## Current state

Files:

- `cmd/obor/reviewcycle.go`: `reviewGit.Close`, the close's commit in the main checkout, and `commitBacklog`.
- `internal/app/reviewclose.go:224-242`: `AcceptLines`, the text change to `BACKLOG.md`.
- `internal/app/review.go:538`: the reviewer's brief names where the previous review's accepted findings are.
- `internal/app/port/workflow.go:29-44`: the comments of `Review.Close` and `ReviewCommit`.
- `cmd/obor/reviewcycle_integration_test.go`: the close's tests on a real git fixture.
- `internal/app/review_test.go:49`: asserts the brief's sentence.
- `skills/lead/SKILL.md`, `skills/lead/commands.md`, `skills/lead/review-brief.md`, `templates/README.md`,
  `templates/BACKLOG.md`: the texts that say where accepted lines go.

`internal/app/reviewclose.go:224-242`:

```go
// AcceptLines is backlog with one line "- (<id>, accepted) <line>" per accepted finding at the top of its "## Open"
// section (the close's BACKLOG lines, rendered here and committed by port.Review.Close); no such section: an error.
func AcceptLines(backlog, id string, accepts []string) (string, error) {
	ls := strings.SplitAfter(backlog, "\n")
	for i, l := range ls {
		if strings.TrimSpace(l) != "## Open" {
			continue
		}
		...
	}
	return "", errors.New("--accept: BACKLOG.md has no \"## Open\" section: nothing changed")
}
```

`cmd/obor/reviewcycle.go`, the part of `Close` this plan changes (lines 40-70):

```go
	st, err := g.git(ctx, "status", "--porcelain", "--untracked-files=all")
	...
	backlogDirty := false
	for _, l := range porcelainLines(st) {
		switch {
		case l[3:] == "BACKLOG.md":
			backlogDirty = true
		case strings.HasPrefix(l, "?? ") && own[l[3:]]:
		default:
			return fmt.Errorf("the main checkout is dirty outside BACKLOG.md and %s's files (%s): nothing changed", c.ID, l)
		}
	}
	backlog := ""
	if len(c.Accepts) > 0 {
		b, err := os.ReadFile(filepath.Join(root, "BACKLOG.md"))
		if err != nil {
			return fmt.Errorf("--accept: %v", err)
		}
		if backlog, err = app.AcceptLines(string(b), c.ID, c.Accepts); err != nil {
			return err
		}
	}
	...
	if backlogDirty || backlog != "" {
		return g.commitBacklog(ctx, c, backlog, files)
	}
```

`commitBacklog` (lines 77-116) writes `BACKLOG.md` when `backlog != ""`, runs `git add -- BACKLOG.md` and the close
commit, and on any failure restores the file's content and its index entry as they were (`restore`), leaving the
review's files untouched. Two behaviours of it stay: a `BACKLOG.md` the lead left dirty is committed with the close
(T-725), and a failed commit changes nothing.

`internal/app/review.go:538`:

```go
		fmt.Fprintf(&b, " Previous review: %s (its close commit: %s; its accepted findings are the BACKLOG lines that name %s)"+
			" — don't repeat them.", prev, last, prev)
```

Conventions: comments say what a thing is and why, in full sentences, with a task id where one exists; a refusal's
error ends `: nothing changed`; cmd/obor's close tests use the fixture `reviewFixture(t)` and `closeOf(p, head,
accepts...)` in `cmd/obor/reviewcycle_integration_test.go` (file tag `//go:build integration`).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `make -s build` | exit 0 |
| The close's git tests | `go test -count=1 -tags integration -run 'TestReviewClose\|TestReviewGit' ./cmd/obor` | `ok  	obor/cmd/obor` |
| The app's review tests | `go test -count=1 -tags integration -run 'Review' ./internal/app` | `ok  	obor/internal/app` |
| Your lane's check | `make -s check-affected` | last line `go test: N pass, 0 fail` |
| No reference left | `grep -rn 'AcceptLines' cmd internal` | no output |

Run one test command at a time (the guard denies two at once). Never run `make check` yourself. In a table cell
`\|` stands for a plain `|`.

## Scope

**In scope** (the only files you should modify):
- `cmd/obor/reviewcycle.go`, `cmd/obor/reviewcycle_integration_test.go`
- `cmd/obor/review.go` (comment only, lines 50-56)
- `internal/app/reviewclose.go`, `internal/app/reviewclose_test.go` (only if a test there names the changed error text)
- `internal/app/review.go` (the one sentence of the brief), `internal/app/review_test.go` (its assert)
- `internal/app/port/workflow.go` (comments only)
- `skills/lead/SKILL.md`, `skills/lead/commands.md`, `skills/lead/review-brief.md`
- `templates/README.md`, `templates/BACKLOG.md` (its header comment)

**Out of scope** (do NOT touch):
- `BACKLOG.md` of this repository: the lead edits it in the main checkout all day, and a lane's change to it would
  conflict. The one-time move of the existing accepted lines is the lead's, after this merges (Maintenance notes
  give the command).
- `docs/accepted.md` of this repository: the lead's move creates it.
- `docs/design.md`: protected, the lead's. Its sentences are listed in Maintenance notes.
- `internal/setup/init.go` and a template for `docs/accepted.md`: the close creates the file when it is missing, so
  `obor init` writes none.
- The wire request, the close's subject (`accepted <n>`), `reviewfold.go`: nothing about what a close is changes.

## Git workflow

- If you are an obor worker (your job came through `job_next`): work in your lane, make no commits, finish with
  `task_done`.
- Otherwise: a branch `advisor/007-accepted-findings-file` cut from `staging`, in a separate git worktree. Commit
  message style: one sentence saying what is true now. Do not push.

## Steps

### Step 1: `AcceptedLines` replaces `AcceptLines`

In `internal/app/reviewclose.go` delete `AcceptLines` and add:

```go
// AcceptedFile is where a close's accepted findings go (the main checkout's docs/accepted.md): a file no role reads
// at session start, looked up when a finding's symptom shows.
const AcceptedFile = "docs/accepted.md"

// acceptedHead starts a new AcceptedFile.
const acceptedHead = "# Accepted findings\n\n" +
	"Review findings accepted instead of fixed, one line each, written by `obor review close --accept` (newest\n" +
	"last). Nobody reads this file at session start: look a line up when its symptom shows, and fix it then.\n\n"

// AcceptedLines is the accepted-findings file old ("": none yet, started with its head) with one line
// "- (<id>, accepted) <line>" per accepted finding at its end (rendered here, committed by port.Review.Close).
func AcceptedLines(old, id string, accepts []string) string {
	if old == "" {
		old = acceptedHead
	}
	if !strings.HasSuffix(old, "\n") {
		old += "\n"
	}
	for _, a := range accepts {
		old += "- (" + id + ", accepted) " + a + "\n"
	}
	return old
}
```

Remove the `errors` import if nothing else in the file uses it.

**Verify**: `go build ./internal/app` fails only in `cmd/obor` callers, not in `internal/app` (`go vet ./internal/app`
exits 0).

### Step 2: `Close` writes and commits `docs/accepted.md`

In `cmd/obor/reviewcycle.go`:

1. The dirty check allows both files. Replace `backlogDirty` with a set of the two paths that the checkout has
   changes in:

   ```go
   	docs := map[string]bool{} // of BACKLOG.md and app.AcceptedFile: the ones the close commit carries
   	for _, l := range porcelainLines(st) {
   		switch {
   		case l[3:] == "BACKLOG.md" || l[3:] == app.AcceptedFile:
   			docs[l[3:]] = true
   		case strings.HasPrefix(l, "?? ") && own[l[3:]]:
   		default:
   			return fmt.Errorf("the main checkout is dirty outside BACKLOG.md, %s and %s's files (%s): nothing changed",
   				app.AcceptedFile, c.ID, l)
   		}
   	}
   ```

2. With `--accept` lines, read `docs/accepted.md` (a missing file is `""`, any other read error is
   `fmt.Errorf("--accept: %v", err)`), render `accepted := app.AcceptedLines(string(b), c.ID, c.Accepts)` and set
   `docs[app.AcceptedFile] = true`. `BACKLOG.md` is no longer read here.

3. Replace `commitBacklog(ctx, c, backlog, files)` with `commitDocs(ctx, c, paths, accepted, files)`, where `paths`
   is the paths of `docs` in the fixed order `BACKLOG.md`, `app.AcceptedFile`. `commitDocs` is `commitBacklog`
   generalised to that list. It must keep these properties, each of which an existing test checks for `BACKLOG.md`:
   - Before any change it records, per path: the file's content and whether it existed, and its index entry
     (`git ls-files --stage -- <path>`).
   - It writes `accepted` to `docs/accepted.md` when `accepted != ""` (`os.MkdirAll` its directory first), then
     `git add -- <paths...>`, then the commit (`commit -q --allow-empty -m c.Subject -m c.Body`), then
     `removeFiles(files)`.
   - On a failed `add` or commit it restores every path: the content as it was (a file that did not exist is
     removed), and the index entry as it was (`update-index --cacheinfo <mode>,<blob>,<path>` when there was one,
     else `reset -q -- <path>`), under a context that is not the cancelled one (as `restore` does today). The
     review's files stay.

4. Update the comments of `Close` and `commitDocs` to say what they do now (the accepted lines go to
   `docs/accepted.md`, created when missing; a dirty `BACKLOG.md` or `docs/accepted.md` is committed with the
   close). Same for `cmd/obor/review.go:50-56` and `internal/app/port/workflow.go:29-44` (`Review.Close`,
   `ReviewCommit`): drop "no `## Open` section" from the refusals, name the file.

**Verify**: `make -s build` exits 0; `grep -rn 'AcceptLines' cmd internal` prints nothing.

### Step 3: The reviewer's brief names the file

In `internal/app/review.go:538` replace `its accepted findings are the BACKLOG lines that name %s` with
`` its accepted findings are the lines of docs/accepted.md that name %s `` (use `AcceptedFile` in the format
arguments, not the literal). Update the assert in `internal/app/review_test.go:49` to the new sentence.

**Verify**: `go test -count=1 -tags integration -run 'Review' ./internal/app` → `ok`.

### Step 4: Tests of the close

In `cmd/obor/reviewcycle_integration_test.go`:

- **Changed**: every assert that the accepted lines are in `BACKLOG.md` (in `TestReviewGitClose` and `TestReviewGitCloseDirtyBacklog`)
  now reads `git show HEAD:docs/accepted.md` and expects the head text followed by the lines in call order:
  `- (RV-4, accepted) rare case\n- (RV-4, accepted) other one\n`. The same tests also assert that `BACKLOG.md` at
  `HEAD` does **not** contain `accepted)`, and that a lead's dirty `BACKLOG.md` edit is still in the commit (T-725).
- **Changed**: the two error-text asserts (`dirty outside BACKLOG.md and RV-4's files`) to the new text.
- **Deleted**: the "no Open section" case of `TestReviewGitClose`: the refusal is gone. In its place, one case: `BACKLOG.md`
  without an `## Open` section and no `docs/accepted.md` → the close succeeds and creates `docs/accepted.md`.
- **Added**: a second close (another review id) appends below the first one's lines and keeps the head once.
- **Added** to the failed-commit test (`TestReviewGitCloseCommitFails`): with `--accept`
  lines and no `docs/accepted.md` before, a failed commit leaves no `docs/accepted.md` and nothing staged for it;
  with a committed `docs/accepted.md` before, its content and index entry are as before.

No new test starts git for a decision: these are the existing git tests of this write path, changed.

**Verify**: `go test -count=1 -tags integration -run 'TestReviewClose|TestReviewGit' ./cmd/obor` → `ok`.

### Step 5: The texts

Change each sentence that says accepted lines go to `BACKLOG.md`'s Open section. Keep each file's style and line
width; change nothing else in them.

| File | Now | Becomes |
|---|---|---|
| `skills/lead/SKILL.md:102` | `then accept the rest in `BACKLOG.md`` | accept the rest with `obor review close --accept` (they go to `docs/accepted.md`) |
| `skills/lead/commands.md:134-139` | `adds each accepted line to `BACKLOG.md`'s Open section naming the RV`; `dirty outside `BACKLOG.md`` | `appends each accepted line to `docs/accepted.md` naming the RV (created when missing)`; `dirty outside `BACKLOG.md`, `docs/accepted.md`` |
| `skills/lead/review-brief.md:45` | `its accepted findings are the BACKLOG lines that name` | the sentence of step 3 |
| `skills/lead/review-brief.md:62, 72` | `accept it in `BACKLOG.md` with one line why`; `is accepted in `BACKLOG.md`` | accepted with `--accept` at the close (`docs/accepted.md`), one line with why / likelihood and fix cost |
| `skills/lead/review-brief.md:107-112, 127` | the close `adds each `--accept` line to `BACKLOG.md`'s Open section`, commits `with `BACKLOG.md``; `dirty outside `BACKLOG.md`` | appends to `docs/accepted.md`; commits it (and a dirty `BACKLOG.md`); the new dirty rule |
| `templates/README.md:8` | `` `obor review close --accept` also writes its accepted lines under Open `` | delete that clause; add one clause that accepted findings are in `docs/accepted.md`, written by the close |
| `templates/BACKLOG.md:3-4` | `nothing else writes except `obor review close --accept "<line>"`, which adds its accepted lines under Open and commits them with the close.` | `nothing else writes. Accepted review findings are not here: `obor review close --accept` writes them to docs/accepted.md.` |

`skills/lead/SKILL.md:25` (the session-start reading list) stays as it is: the point is that `docs/accepted.md`
is not on it. Add one sentence after the changed sentence at line 102:
`` Before filing a bug or a finding, `grep` its symptom in `docs/accepted.md`: an accepted line names its fix cost. ``

**Verify**: `grep -rn "BACKLOG.*Open section\|accepted lines under Open\|BACKLOG lines that name" skills templates internal cmd`
prints nothing.

### Step 6: Your lane's check

**Verify**: `make -s check-affected` → last line `go test: N pass, 0 fail`.

## Test plan

- Changed: the close's git tests (step 4) and the brief's assert (step 3).
- Deleted: the "no Open section" refusal case.
- Added: file created when missing; a second close appends; a failed commit leaves no new file.
- `AcceptedLines` gets no test of its own: step 4's asserts on the committed file cover its three branches (no
  file, a file, a file without a final newline is not reachable through the close; if you want it covered, one
  table test of the pure function in `internal/app/reviewclose_test.go`, three rows).

## Done criteria

- [ ] `grep -rn 'AcceptLines' cmd internal` prints nothing
- [ ] `grep -rn 'no \\"## Open\\" section' cmd internal` prints nothing
- [ ] `go test -count=1 -tags integration -run 'TestReviewClose|TestReviewGit' ./cmd/obor` passes
- [ ] `go test -count=1 -tags integration -run 'Review' ./internal/app` passes
- [ ] step 5's grep prints nothing
- [ ] `make -s check-affected` ends `0 fail`
- [ ] `git status --short` lists only in-scope files; `BACKLOG.md` and `docs/accepted.md` are not among them

## STOP conditions

Stop and report back (do not improvise) if:

- `Close` or `commitBacklog` no longer look like the excerpts (another task changed the close).
- A test outside the two named test files asserts the old `BACKLOG.md` behaviour of the close.
- The failed-commit test cannot be made to pass with two files without changing what `restore` guarantees for
  `BACKLOG.md` (content and index entry exactly as before).
- You find a reader of accepted lines in code (something that parses `(RV-n, accepted)` out of `BACKLOG.md`):
  `grep -rn 'accepted)' cmd internal --include='*.go' | grep -v _test` should show only `AcceptedLines`.

## Maintenance notes

For the lead, after this merges:

- **The one-time move** in the main checkout (BACKLOG.md clean and committed first). It moves every bullet of the
  "Open" section whose first line's opening parenthesis names `accepted`, with its indented continuation lines, to
  `docs/accepted.md`, in file order:

  ```sh
  awk -v moved=/tmp/accepted-moved.md '
    /^## /        { open = ($0 == "## Open"); acc = 0 }
    open && /^- / { acc = ($0 ~ /^- \([^)]*accepted/) }
    open && acc && (/^- / || /^  /) { print > moved; next }
    { print }
  ' BACKLOG.md > /tmp/BACKLOG.new
  ```

  Dry-run at planning time (`65389c8c`): it moves 67 of the 91 Open bullets (81 lines), and `BACKLOG.md` goes from
  38 KB to 20 KB. Read both outputs before using them: bullets written by hand vary (`accepted: rare`, `accepted by
  rule`, `2nd verify #3, accepted`), a continuation line that is not indented by two spaces stays behind, and five
  lines that name "accepted" are outside the Open section (Later candidates, Concerns) and are not touched.
  Then: `docs/accepted.md` = the head text of `AcceptedLines` (step 1) + `/tmp/accepted-moved.md`; `BACKLOG.md` =
  `/tmp/BACKLOG.new`; one commit, `accepted findings moved out of BACKLOG.md`.
- Other bullets worth moving by hand in the same pass: "design concern, logged" lines of closed reviews and flake
  notes of tests that no longer exist. Not part of the script.
- `docs/design.md` (protected): line 68 lists the templates (no change needed, no template was added); the review
  section's sentence on what a close commits (search `review close`) should name `docs/accepted.md`.
- `docs/state-model.md` row 52a says a close carries "accept lines"; it does not say where they go. No change.
- Adopters: a project with an older kit keeps its accepted lines in `BACKLOG.md`; after the upgrade new ones go to
  `docs/accepted.md`. A line for the release notes: "accepted findings move to docs/accepted.md; move the old ones
  by hand or leave them".
- The reviewer reads `docs/accepted.md` from its role worktree (it is committed), so "don't repeat them" keeps
  working. If a reviewer re-reports an accepted finding, check that its brief names the file.
