package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/review"
	"obor/internal/spec"
	"obor/internal/store"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A review's fix round and its verify (docs/design.md "Role worktrees"; skills/lead/review-brief.md §3–4).
// The link is derived, no record: the linked tasks of review RV-n are the dispatched tasks whose stored spec (current
// rev) carries the line `Fixes: RV-n` (obor dispatch refuses one naming a review that is not the open one). The verify
// is a rule (verifyRule, rules.go): while RV-n is open, its linked tasks have all merged or ended unmerged, at least
// one merged and a finished verify does not cover it, and the reviewer has no open brief and is idle, serve reads the
// integration head, moves the reviewer's worktree there and briefs the verify in the same session. The brief's fact
// (briefed with Task, the tasks it verifies, and Reviewed, that head) is the verify; review.State reads the rest from the
// brief facts: open, finished (its finished line, Ends: its tasks covered, its head reviewed), stopped (a stop with
// Ends) or replaced (a newer brief). A verify that did not finish leaves the condition true: the next pass briefs again.
// Its written failure mode: after a crash a verify may be briefed twice, or one pass late. obor review close refuses
// while a linked task is open, or merged and not covered (review.Cycle.Pending, reviewclose.go), --force closes anyway.

// ruleVerify is the verify rule's name in the rule pass (rules.go); ruleVerifyGit keys its git failure's line.
const (
	ruleVerify    = "verify"
	ruleVerifyGit = "verify.git"
)

// verifyGitWait bounds a verify's git.
const verifyGitWait = 30 * time.Second

// verifyBrief is what a verify's brief fact carries (recordBrief): the tasks it verifies and the head it hands the
// reviewer.
type verifyBrief struct {
	tasks []string
	head  string
}

// fixCandidates is the loop's tasks that can be linked to the review started at start (in task order): those still
// open and those dispatched since it (a fix task is dispatched while its review is open), with their outcome.
func fixCandidates(loop map[string]*mtask, start int64) []review.Link {
	var out []review.Link
	for _, mt := range loop {
		ended := states[mt.state].ended
		if ended && mt.dispSeq <= start {
			continue
		}
		f := review.Link{Task: mt.task, Dispatched: mt.dispSeq}
		switch mt.state {
		case stMerged:
			f.Merged = mt.mergedSeq
		case stDropped, stRejected:
			f.End = mt.state
		}
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b review.Link) int { return cmp.Compare(a.Task, b.Task) })
	return out
}

// linkFixes is the candidates whose spec (read) has the line `Fixes: <id>`, its free text their note.
func linkFixes(cands []review.Link, id string, read func(task string) (store.SpecRow, bool, error)) ([]review.Link, error) {
	var out []review.Link
	for _, f := range cands {
		row, _, err := read(f.Task)
		if err != nil {
			return nil, fmt.Errorf("the spec of %s: %v", f.Task, err)
		}
		if row.Spec.FixesOK && row.Spec.FixesReview == id {
			f.Note = row.Spec.FixesNote
			out = append(out, f)
		}
	}
	return out, nil
}

// verifyRule is the verify: while the review started last is open and has linked tasks, it lines the lead
// once for each linked task that ended unmerged (key: its dispatch's seq) and for each verify brief that did not
// finish (key: its brief's seq); then, every link merged or ended unmerged, one merged that no finished verify covers,
// and the reviewer without an open brief and idle, it briefs the verify of the merged, uncovered links (key: the newest
// of their merges; no line: the brief's fact is its effect). A closed review (its review.closed fact) runs none
// of it and its round leaves obor status: the snapshot it reads is empty.
func verifyRule(_ context.Context, a *App, p *proj, fx *Loop, _ time.Time) (int64, action, bool) {
	if fx == nil || fx.Workflow == nil {
		return 0, action{}, false
	}
	a.mu.Lock()
	started, failed := p.folded.rv.Cycle(), p.folded.rv.Failed()
	covered := started.Covered
	closed := started.Open() == ""
	var cands []review.Link
	if !closed {
		cands = fixCandidates(p.folded.loop, started.Seq)
	}
	a.mu.Unlock()
	links, err := linkFixes(cands, started.ID, func(task string) (store.SpecRow, bool, error) { return specOf(p, task) })
	if err != nil {
		a.logf("%s: verify of %s: %v", p.name, started.ID, err)
		return 0, action{}, false
	}
	a.mu.Lock()
	p.fixes, p.verifyDue = review.Round{ID: started.ID, Links: links, Covered: covered}, false
	a.mu.Unlock()
	if len(links) == 0 {
		return 0, action{}, false
	}
	var tasks, notes []string
	var newest int64
	waits := false
	for _, f := range links {
		switch {
		case f.End != "":
		case f.Merged == 0:
			waits = true
		case !slices.Contains(covered, f.Task):
			tasks, newest = append(tasks, f.Task), max(newest, f.Merged)
			if f.Note != "" {
				notes = append(notes, f.Task+": "+f.Note)
			}
		}
	}
	key, act := int64(0), action{}
	a.mu.Lock()
	for _, f := range links {
		if of := (event.Of{Kind: ruleVerify, Seq: f.Dispatched}); f.End != "" && key == 0 && !p.didLine(of) {
			key, act = f.Dispatched, action{line: "[obor] " + started.ID + " fix " + f.Task + " ended unmerged (" + f.End + "): " +
				fixRest(links, f.Task)}
		}
	}
	if v := failed; key == 0 && v != nil && !p.didLine(event.Of{Kind: ruleVerify, Seq: v.Seq()}) &&
		slices.ContainsFunc(v.Tasks(), func(t string) bool { return !slices.Contains(covered, t) }) {
		key, act = v.Seq(), action{line: "[obor] " + started.ID + " verify of " + strings.Join(v.Tasks(), ",") +
			" did not finish (" + v.Why() + "): serve briefs it again once the reviewer is idle"}
	}
	due := !waits && len(tasks) > 0
	p.verifyDue = due // idle waits for it (idle.go)
	a.mu.Unlock()
	if key == 0 && !due {
		return 0, action{}, false
	}
	s, err := reviewConfig(p.root)
	if err != nil {
		return 0, action{}, false
	}
	if key == 0 {
		a.mu.Lock()
		idle := a.roleIdle(p, s.Agent)
		a.mu.Unlock()
		if !idle {
			return 0, action{}, false
		}
		key, act = newest, verifyAction(a, p, fx.Workflow, s, started.ID, tasks, notes, newest)
	}
	return key, act, true
}

// fixRest is what a link's unmerged end leaves: the links still to merge, or nothing.
func fixRest(links []review.Link, task string) string {
	var rest []string
	for _, f := range links {
		if f.Task != task && f.End == "" && f.Merged == 0 {
			rest = append(rest, f.Task)
		}
	}
	if len(rest) == 0 {
		return "not counted, nothing waits for it"
	}
	return "not counted, the verify waits for " + strings.Join(rest, ",")
}

// verifyAction is the verify's brief: the integration head read once, the reviewer's worktree detached there
// (port.Review.Detach, idempotent), the verify brief recorded to the same session. Its git failing is the error
// and, once per key, the lead's line (Of{ruleVerifyGit, key}); a brief to the reviewer recorded meanwhile skips it
// (recordBrief).
func verifyAction(a *App, p *proj, g port.Review, s ReviewSetup, id string, tasks, notes []string, key int64) action {
	return action{call: func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, verifyGitWait)
		defer cancel()
		text, head, err := a.verifyText(ctx, p, g, s, id, tasks, notes)
		if err == nil {
			err = g.Detach(ctx, s.Worktree, head)
		}
		if err != nil {
			if of := (event.Of{Kind: ruleVerifyGit, Seq: key}); !cutByServe(ctx, err) && !a.didLine(p, of) {
				line := "[obor] " + id + " verify of " + strings.Join(tasks, ",") + " failed: " + err.Error() +
					" (serve tries again on its next pass)"
				if lerr := a.lineLead(p, of, line); lerr != nil {
					a.logf("%s: verify of %s: its line: %v", p.name, id, lerr)
				}
			}
			return err
		}
		if _, ok := a.recordBrief(p, s.Agent, "obor", text, "", "", false, &verifyBrief{tasks: tasks, head: head}); !ok {
			return fmt.Errorf("the verify brief of %s was not recorded (the log refused it, or the reviewer was briefed"+
				" meanwhile)", id)
		}
		a.wake()
		return nil
	}}
}

// didLine: a rule's line of key of is recorded (under mu).
func (p *proj) didLine(of event.Of) bool {
	_, did := p.folded.did[of]
	return did
}

// didLine is p.didLine under mu.
func (a *App) didLine(p *proj, of event.Of) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return p.didLine(of)
}

// openCycle is the open review by the fold: its id (the newest review started, not closed), the review
// before it ("": none) and its review file in the main checkout.
type openCycle struct {
	ID, Prev, File string
}

// errNoOpenReview is openReview's answer when no review is open, or the open one has no review file.
var errNoOpenReview = errors.New("no open review")

// openReview is p's open review (obor dispatch's Fixes: check, the verify, obor review close): none open in the fold,
// or no review file <reviews>/RV-<n>.md in the main checkout (the reviewer writes it there), is no open review,
// an error (errNoOpenReview). hint, when not "", is the way out the missing-file refusal names (the close's --force).
func (a *App) openReview(p *proj, s ReviewSetup, hint string) (openCycle, error) {
	a.mu.Lock()
	id, prev := p.folded.rv.Last().Open(), p.folded.rv.Prev()
	a.mu.Unlock()
	if id == "" {
		return openCycle{}, fmt.Errorf("%w (the last review is closed)", errNoOpenReview)
	}
	c := openCycle{ID: id, Prev: prev, File: filepath.Join(p.root, s.Reviews, id+".md")}
	if _, err := os.Stat(c.File); err != nil {
		why := c.ID + " is not written yet"
		if hint != "" {
			why += ": " + hint
		}
		return c, fmt.Errorf("%w: no %s/%s.md in %s (%s)", errNoOpenReview, s.Reviews, c.ID, p.root, why)
	}
	return c, nil
}

// fixesCheck is obor dispatch's refusal of a spec whose Fixes: line names a review that is not the open one;
// nil without such a line, or when it names the open review.
func (a *App) fixesCheck(p *proj, task string, sp spec.Spec) error {
	if !sp.FixesOK {
		return nil
	}
	id := sp.FixesReview
	s, err := reviewConfig(p.root)
	if err != nil {
		return fmt.Errorf("%s: Fixes: %s: %v", task, id, err)
	}
	c, err := a.openReview(p, s, "")
	switch {
	case errors.Is(err, errNoOpenReview) && c.ID == "":
		return fmt.Errorf("%s: Fixes: %s is not the open review (none is open)", task, id)
	case err != nil:
		return fmt.Errorf("%s: Fixes: %s: %v", task, id, err)
	case c.ID != id:
		return fmt.Errorf("%s: Fixes: %s is not the open review (%s is open)", task, id, c.ID)
	}
	return nil
}

// verifyText finds the open review (it must be id) and each task's merge in the fold since the review before it
// (a task merged after the review started, which it does not cover, is allowed), then reads the integration
// branch's head: the verify brief and that head.
func (a *App) verifyText(ctx context.Context, p *proj, g port.Review, s ReviewSetup, id string, tasks, notes []string) (text,
	head string, err error) {
	c, err := a.openReview(p, s, "")
	if err != nil {
		return "", "", err
	}
	if c.ID != id {
		return "", "", fmt.Errorf("%s is closed (the open review is %s)", id, c.ID)
	}
	fixed := make([]string, 0, len(tasks))
	a.mu.Lock()
	for _, task := range tasks {
		m := p.folded.rv.MergeOf(task)
		if m.SHA() == "" {
			a.mu.Unlock()
			return "", "", fmt.Errorf("%s has no merge on %s since %s", task, s.Branch, cmp.Or(c.Prev, "the first review"))
		}
		fixed = append(fixed, task+" ("+short(m.SHA())+")")
	}
	a.mu.Unlock()
	if head, err = g.Head(ctx, s.Branch); err != nil {
		return "", "", err
	}
	return verifyBriefText(c.ID, s, short(head), fixed, notes), head, nil
}

// verifyBriefText is review-brief.md §4's text: the fixes, the head, every finding to re-run, the verify file, the
// redundancy question (docs/design.md "Change rules" rule 6), then the lead's note: one line per task whose Fixes:
// line has free text.
func verifyBriefText(id string, s ReviewSetup, short string, fixed, notes []string) string {
	text := fmt.Sprintf("The %s findings were fixed in %s; %s is at %s (your worktree too). Re-run your probes for every"+
		" finding and report fixed / incomplete / regressed per finding. %s Write %s/%s-verify.md in the main checkout %s."+
		" %s Do not commit.", id, strings.Join(fixed, ", "), s.Branch, short, redundantLine, s.Reviews, id, s.Root, resultLine)
	for _, n := range notes {
		text += "\nLead's note: " + n
	}
	return text
}

// resultScan is how many of a review file's first non-empty lines are searched for the Result label: the reviewer puts
// it near the top.
const resultScan = 10

// resultWidth is the longest result text a finished line carries.
const resultWidth = 100

// resultText is the text after the first "Result:" among b's first resultScan non-empty lines ("" when there is none),
// wherever the label sits on its line: a review file starts it, a verify file writes it mid-sentence. Bold
// markers are stripped.
func resultText(b []byte) string {
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if n++; n > resultScan {
			break
		}
		_, after, ok := strings.Cut(line, "Result:")
		if !ok {
			continue
		}
		if r := strings.TrimSpace(strings.ReplaceAll(after, "**", "")); r != "" {
			return r
		}
	}
	return ""
}

// finishedReview is the reviewer's finished line: "[obor] <agent> finished <RV-n>: <reviews>/<file>.md —
// <result>", the result the file's Result label text (bold markers stripped, cut to resultWidth runes), or "(no Result
// line)" when the file is missing or has none. The file is read once, here.
func finishedReview(root, reviews, file, agent string) string {
	id, _, _ := strings.Cut(file, "-verify")
	rel := path.Join(filepath.ToSlash(reviews), file+".md")
	line := "[obor] " + agent + " finished " + id + ": " + rel
	if b, err := os.ReadFile(filepath.Join(root, reviews, file+".md")); err == nil {
		if r := resultText(b); r != "" {
			if rs := []rune(r); len(rs) > resultWidth {
				r = strings.TrimSpace(string(rs[:resultWidth])) + "…"
			}
			return line + " — " + r
		}
	}
	return line + " (no Result line)"
}
