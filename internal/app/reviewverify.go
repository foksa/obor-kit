package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/spec"
	"obor/internal/store"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A review's fix round and its verify (T-924; docs/design.md "Role worktrees"; skills/lead/review-brief.md §3–4).
// The link is derived, no record: the linked tasks of review RV-n are the dispatched tasks whose stored spec (current
// rev) carries the line `Fixes: RV-n` (obor dispatch refuses one naming a review that is not the open one). The verify
// is a rule (verifyRule, rules.go): while RV-n is open, its linked tasks have all merged or ended unmerged, at least
// one merged and a finished verify does not cover it, and the reviewer has no open brief and is idle, serve reads the
// integration head, moves the reviewer's worktree there and briefs the verify in the same session. The brief's fact
// (briefed with Task, the tasks it verifies, and Reviewed, that head) is the verify; reviewLog reads the rest from the
// brief facts: open, finished (its finished line, Ends: its tasks covered, its head reviewed), stopped (a stop with
// Ends) or replaced (a newer brief). A verify that did not finish leaves the condition true: the next pass briefs again.
// Its written failure mode: after a crash a verify may be briefed twice, or one pass late. obor review close refuses
// while a linked task is open, or merged and not covered (ReviewCycle.Pending), --force closes anyway.

// ruleVerify is the verify rule's name in the rule pass (rules.go); ruleVerifyGit keys its git failure's line.
const (
	ruleVerify    = "verify"
	ruleVerifyGit = "verify.git"
)

// verifyGitWait bounds a verify's git.
const verifyGitWait = 30 * time.Second

// verifyBrief is a verify's brief: its fact's seq, the tasks it verifies and the head it hands the reviewer; why it
// ended unfinished ("": it did not).
type verifyBrief struct {
	seq   int64
	tasks []string
	head  string
	why   string
}

// reviewLog is the review cycle's records folded (T-856, one fold: serve's and obor review close's alike): the last
// review started (its fact's seq, id and head), the tasks the cycle covered so far (its in-scope tasks, then each
// finished verify's), the head the reviewer last finished (the start's, then each finished verify's: T-857), per
// agent its open verify brief, and the last verify brief since the start that ended unfinished.
type reviewLog struct {
	start     int64
	startID   string
	startHead string
	covered   []string
	reviewed  string
	open      map[string]*verifyBrief
	failed    *verifyBrief
}

// fold folds r: a review start; a brief (a verify's, when it names tasks and a head; any brief replaces the agent's open
// verify brief); the finished line that ends a brief; a stop that ends one. Records of an older obor (review.fix,
// review.verify.request, a brief's verify_of) fold as nothing (T-924: a closed cycle needs none of them).
func (l *reviewLog) fold(r event.Record) {
	if r.Kind != event.KindFact || r.Task != event.ReviewTask && r.Task != event.MsgTask {
		return
	}
	if l.open == nil {
		l.open = map[string]*verifyBrief{}
	}
	switch {
	case r.Task == event.ReviewTask && r.Step == event.StepReviewStarted:
		var d ReviewData
		if json.Unmarshal(r.Data, &d) != nil {
			return
		}
		l.start, l.startID, l.startHead, l.reviewed = r.Seq, d.ID, d.Head, d.Head
		l.covered, l.failed = slices.Clone(d.Tasks), nil
	case r.Task != event.MsgTask:
	case r.Step == event.StepBriefed:
		var m event.Message
		if json.Unmarshal(r.Data, &m) != nil {
			return
		}
		l.ended(m.To, "a newer brief replaced it")
		if m.Task != "" && m.Reviewed != "" {
			l.open[m.To] = &verifyBrief{seq: r.Seq, tasks: strings.Split(m.Task, ","), head: m.Reviewed}
		}
	case r.Step == event.StepMessage:
		var m event.Message
		if json.Unmarshal(r.Data, &m) == nil && m.Ends != "" { // the finished line
			l.ended(m.Ends, "")
		}
	case r.Step == event.StepStopped:
		var d stoppedData
		if json.Unmarshal(r.Data, &d) == nil && d.Ends != "" { // an error stop: no finished line follows
			l.ended(d.Ends, "the reviewer stopped: "+d.Error)
		}
	}
}

// ended folds the end of agent's open verify brief, finished (why "": its tasks covered, its head reviewed) or not
// (why: nothing of it counted, T-857).
func (l *reviewLog) ended(agent, why string) {
	v := l.open[agent]
	delete(l.open, agent)
	if v == nil {
		return
	}
	if why != "" {
		v.why, l.failed = why, v
		return
	}
	if l.start != 0 && v.seq > l.start {
		l.covered = append(l.covered, v.tasks...)
		l.reviewed = v.head
	}
}

// fixLink is one task linked to a review by its spec's Fixes: line: the task, its note (the line's free text), the
// seq of its newest dispatch, of its merge (0: not merged), and how it ended unmerged (dropped, rejected; "": it did
// not).
type fixLink struct {
	task, note string
	disp       int64
	merged     int64
	end        string
}

// fixCandidates is the loop's tasks that can be linked to the review started at start (in task order): those still
// open and those dispatched since it (a fix task is dispatched while its review is open), with their outcome.
func fixCandidates(loop map[string]*mtask, start int64) []fixLink {
	var out []fixLink
	for _, mt := range loop {
		ended := states[mt.state].ended
		if ended && mt.dispSeq <= start {
			continue
		}
		f := fixLink{task: mt.task, disp: mt.dispSeq}
		switch mt.state {
		case stMerged:
			f.merged = mt.mergedSeq
		case stDropped, stRejected:
			f.end = mt.state
		}
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b fixLink) int { return cmp.Compare(a.task, b.task) })
	return out
}

// linkFixes is the candidates whose spec (read) has the line `Fixes: <id>`, its free text their note.
func linkFixes(cands []fixLink, id string, read func(task string) (store.SpecRow, bool, error)) ([]fixLink, error) {
	var out []fixLink
	for _, f := range cands {
		row, ok, err := read(f.task)
		if err != nil {
			return nil, fmt.Errorf("the spec of %s: %v", f.task, err)
		}
		if review, note, has := spec.Fixes(row.Text); ok && has && review == id {
			f.note = note
			out = append(out, f)
		}
	}
	return out, nil
}

// pendingFixes is the links neither ended unmerged nor merged and covered: what obor review close waits for.
func pendingFixes(links []fixLink, covered []string) []string {
	var out []string
	for _, f := range links {
		if f.end == "" && (f.merged == 0 || !slices.Contains(covered, f.task)) {
			out = append(out, f.task)
		}
	}
	return out
}

// fixState is the open review's fix round as the verify rule last read it (live, for obor status): the review, its
// links and the tasks a finished verify covered.
type fixState struct {
	id      string
	links   []fixLink
	covered []string
}

// line is the round as obor status' review line shows it: "RV-n fixes: T-a merged, verified · T-b open" ("": no link).
func (f fixState) line() string {
	if len(f.links) == 0 {
		return ""
	}
	items := make([]string, 0, len(f.links))
	for _, l := range f.links {
		switch {
		case l.end != "":
			items = append(items, l.task+" "+l.end)
		case l.merged == 0:
			items = append(items, l.task+" open")
		case slices.Contains(f.covered, l.task):
			items = append(items, l.task+" verified")
		default:
			items = append(items, l.task+" merged")
		}
	}
	return f.id + " fixes: " + strings.Join(items, " · ")
}

// verifyRule is the verify (T-924): while the review started last is open and has linked tasks, it lines the lead
// once for each linked task that ended unmerged (key: its dispatch's seq) and for each verify brief that did not
// finish (key: its brief's seq); then, every link merged or ended unmerged, one merged that no finished verify covers,
// and the reviewer without an open brief and idle, it briefs the verify of the merged, uncovered links (key: the newest
// of their merges; no line: the brief's fact is its effect). A closed review (a forced close) runs none of it.
func verifyRule(ctx context.Context, a *App, p *proj, fx *Loop, _ time.Time) (int64, action, bool) {
	if fx == nil || fx.Review == nil {
		return 0, action{}, false
	}
	a.mu.Lock()
	started, l := p.folded.reviewed, p.folded.rv
	covered, failed := slices.Clone(l.covered), l.failed
	closed := started.seq == 0 || p.fixClosed == started.seq || l.start != started.seq
	var cands []fixLink
	if !closed {
		cands = fixCandidates(p.folded.loop, started.seq)
	}
	a.mu.Unlock()
	links, err := linkFixes(cands, started.id, func(task string) (store.SpecRow, bool, error) { return specOf(p, task) })
	if err != nil {
		a.logf("%s: verify of %s: %v", p.name, started.id, err)
		return 0, action{}, false
	}
	a.mu.Lock()
	p.fixes, p.verifyDue = fixState{id: started.id, links: links, covered: covered}, false
	a.mu.Unlock()
	if len(links) == 0 {
		return 0, action{}, false
	}
	var tasks, notes []string
	var newest int64
	waits := false
	for _, f := range links {
		switch {
		case f.end != "":
		case f.merged == 0:
			waits = true
		case !slices.Contains(covered, f.task):
			tasks, newest = append(tasks, f.task), max(newest, f.merged)
			if f.note != "" {
				notes = append(notes, f.task+": "+f.note)
			}
		}
	}
	key, act := int64(0), action{}
	a.mu.Lock()
	for _, f := range links {
		if of := (event.Of{Kind: ruleVerify, Seq: f.disp}); f.end != "" && key == 0 && !p.didLine(of) {
			key, act = f.disp, action{line: "[obor] " + started.id + " fix " + f.task + " ended unmerged (" + f.end + "): " +
				fixRest(links, f.task)}
		}
	}
	if v := failed; key == 0 && v != nil && !p.didLine(event.Of{Kind: ruleVerify, Seq: v.seq}) &&
		slices.ContainsFunc(v.tasks, func(t string) bool { return !slices.Contains(covered, t) }) {
		key, act = v.seq, action{line: "[obor] " + started.id + " verify of " + strings.Join(v.tasks, ",") +
			" did not finish (" + v.why + "): serve briefs it again once the reviewer is idle"}
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
		key, act = newest, verifyAction(a, p, fx.Review, s, started.id, tasks, notes, newest)
	}
	open, err := reviewOpen(ctx, fx.Review, s.Branch, started)
	if err != nil {
		a.logf("%s: verify of %s: is it closed: %v", p.name, started.id, err)
		return 0, action{}, false
	}
	if open == "" {
		a.mu.Lock()
		p.fixClosed, p.fixes, p.verifyDue = started.seq, fixState{}, false // a close is for good: no read again
		a.mu.Unlock()
		return 0, action{}, false
	}
	return key, act, true
}

// fixRest is what a link's unmerged end leaves: the links still to merge, or nothing.
func fixRest(links []fixLink, task string) string {
	var rest []string
	for _, f := range links {
		if f.task != task && f.end == "" && f.merged == 0 {
			rest = append(rest, f.task)
		}
	}
	if len(rest) == 0 {
		return "not counted, nothing waits for it"
	}
	return "not counted, the verify waits for " + strings.Join(rest, ",")
}

// verifyAction is the verify's brief: the integration head read once, the reviewer's worktree detached there
// (ReviewGit.Detach, idempotent; T-853), the verify brief recorded to the same session. Its git failing is the error
// and, once per key, the lead's line (Of{ruleVerifyGit, key}); a brief to the reviewer recorded meanwhile skips it
// (recordBrief).
func verifyAction(a *App, p *proj, g ReviewGit, s ReviewSetup, id string, tasks, notes []string, key int64) action {
	return action{call: func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, verifyGitWait)
		defer cancel()
		text, head, err := verifyText(ctx, g, p.root, s, id, tasks, notes)
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

// ReviewCycle is review id's state from the project's event log and store (T-856, T-924: reviewLog, the fold serve
// keeps, and the linked tasks, fixLinks). Covered: the last start's in-scope tasks plus each finished verify's since,
// once each. Start: the head that review started at. Reviewed: the head the reviewer last finished when the last start
// is id's (its last finished verify's, else its start's; T-857: never the head of a verify that did not finish; "": the
// log has no start of id, which obor review close refuses). Pending: id's linked tasks open, or merged and not covered
// (obor review close's refusal). An unreadable log is the zero state; a spec that does not read is the error.
type ReviewCycle struct {
	Covered  []string
	Start    string
	Reviewed string
	Pending  []string
}

// ReadReviewCycle is review id's ReviewCycle from the log and the store of the project at root (obor review close).
func ReadReviewCycle(root, id string) (ReviewCycle, error) {
	recs, err := event.ReadLog(root)
	if err != nil {
		return ReviewCycle{}, nil
	}
	p := &proj{root: root, folded: newFolded()}
	var l reviewLog
	for _, r := range recs {
		p.foldLoop(r)
		l.fold(r)
	}
	c := ReviewCycle{Start: l.startHead}
	for _, t := range l.covered {
		if !slices.Contains(c.Covered, t) {
			c.Covered = append(c.Covered, t)
		}
	}
	if l.start == 0 || l.startID != id {
		return c, nil
	}
	c.Reviewed = l.reviewed
	db, err := store.OpenRO(event.DBFile(root))
	if err != nil {
		return c, err
	}
	defer db.Close()
	links, err := linkFixes(fixCandidates(p.folded.loop, l.start), id, func(task string) (store.SpecRow, bool, error) {
		return store.ReadSpec(db, task)
	})
	c.Pending = pendingFixes(links, c.Covered)
	return c, err
}

// OpenCycle is the open review by git: its id (the newest "RV-<n> closed" commit + 1), the previous review's id,
// reviewed sha and coverage start ("" when it has neither: no close commit yet), and its review file in the main
// checkout.
type OpenCycle struct {
	ID, Prev, PrevSHA, From, File string
}

// errNoOpenReview is OpenReview's answer when the next review has no review file.
var errNoOpenReview = errors.New("no open review")

// OpenReview is the open review of the project at root (obor dispatch's Fixes: check, the verify, obor review close):
// no review file <reviews>/RV-<n>.md in the main checkout (T-718: the reviewer writes it there) is no open review, an
// error (errNoOpenReview).
func OpenReview(ctx context.Context, g ReviewGit, root string, s ReviewSetup) (OpenCycle, error) {
	last, err := g.Last(ctx, s.Branch)
	if err != nil {
		return OpenCycle{}, err
	}
	n, prev, prevSHA := PreviousReview(last.Subject)
	from, _ := reviewScope(last)
	c := OpenCycle{ID: reviewID(n), Prev: prev, PrevSHA: prevSHA, From: from}
	c.File = filepath.Join(root, s.Reviews, c.ID+".md")
	if _, err := os.Stat(c.File); err != nil {
		return c, fmt.Errorf("%w: no %s/%s.md in %s (%s is not written yet, or the last review is closed)",
			errNoOpenReview, s.Reviews, c.ID, root, c.ID)
	}
	return c, nil
}

// fixesCheck is obor dispatch's refusal of a spec whose Fixes: line names a review that is not the open one (T-924);
// nil without such a line, or when it names the open review.
func fixesCheck(ctx context.Context, fx *Loop, root, task, text string) error {
	id, _, ok := spec.Fixes(text)
	if !ok {
		return nil
	}
	s, err := reviewConfig(root)
	if err != nil {
		return fmt.Errorf("%s: Fixes: %s: %v", task, id, err)
	}
	if fx.Review == nil {
		return fmt.Errorf("%s: Fixes: %s: the merge loop has no review git", task, id)
	}
	ctx, cancel := context.WithTimeout(ctx, verifyGitWait)
	defer cancel()
	c, err := OpenReview(ctx, fx.Review, root, s)
	switch {
	case errors.Is(err, errNoOpenReview):
		return fmt.Errorf("%s: Fixes: %s is not the open review (none is open)", task, id)
	case err != nil:
		return fmt.Errorf("%s: Fixes: %s: %v", task, id, err)
	case c.ID != id:
		return fmt.Errorf("%s: Fixes: %s is not the open review (%s is open)", task, id, c.ID)
	}
	return nil
}

// verifyText finds the open review (it must be id) and each task's merge in the next review's scope (T-822: a task
// merged after the review started, which it does not cover, is allowed) on the integration branch's head: the verify
// brief and that head.
func verifyText(ctx context.Context, g ReviewGit, root string, s ReviewSetup, id string, tasks, notes []string) (text,
	head string, err error) {
	c, err := OpenReview(ctx, g, root, s)
	if err != nil {
		return "", "", err
	}
	if c.ID != id {
		return "", "", fmt.Errorf("%s is closed (the open review is %s)", id, c.ID)
	}
	if head, err = g.Head(ctx, s.Branch); err != nil {
		return "", "", err
	}
	merges, err := g.Merges(ctx, c.From, head)
	if err != nil {
		return "", "", err
	}
	fixed := make([]string, 0, len(tasks))
	for _, task := range tasks {
		sha := ""
		for _, m := range merges { // oldest first: the last merge of the task wins
			if h, subject, _ := strings.Cut(m, " "); parseMergeSubject(subject) == task {
				sha = h[:min(len(h), 7)]
			}
		}
		if sha == "" {
			return "", "", fmt.Errorf("%s has no merge on %s since %s", task, s.Branch, cmp.Or(c.Prev, "the first commit"))
		}
		fixed = append(fixed, task+" ("+sha+")")
	}
	return verifyBriefText(c.ID, s, head[:min(len(head), 7)], fixed, notes), head, nil
}

// verifyBriefText is review-brief.md §4's text: the fixes, the head, every finding to re-run, the verify file, the
// redundancy question (docs/design.md "Change rules" rule 6), then the lead's note: one line per task whose Fixes:
// line has free text.
func verifyBriefText(id string, s ReviewSetup, short string, fixed, notes []string) string {
	text := fmt.Sprintf("The %s findings were fixed in %s; %s is at %s (your worktree too). Re-run your probes for every"+
		" finding and report fixed / incomplete / regressed per finding. %s Write %s/%s-verify.md in the main checkout %s."+
		" Do not commit.", id, strings.Join(fixed, ", "), s.Branch, short, redundantLine, s.Reviews, id, s.Root)
	for _, n := range notes {
		text += "\nLead's note: " + n
	}
	return text
}
