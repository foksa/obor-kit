package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"obor/internal/app/event"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A verify (T-716, T-850; skills/lead/review-brief.md §4) is one record (T-856), keyed by the seq of its request,
// fact review.verify.request{id, tasks, note, by, round} on the task key review: recorded by a fix round once its
// linked tasks merged (reviewfix.go; round: its fix fact's seq; T-869: no verify by hand). Its lifecycle, folded by
// reviewLog (serve's fold and obor review close's log read alike: nothing else keeps a verify's state):
//
//	requested → admitted → briefed → done | superseded | failed
//	requested → failed
//
//   - admitted: a merge-loop pass (verifyPass) finds the reviewer idle (roleIdle: no open
//     brief, its pane idle or not running) and no publish running; admitted (roleworktree.go), no publish starts until
//     the brief is recorded. Serve reads the integration branch's head and each task's merge in the next review's
//     scope and moves the reviewer's role worktree to that head (T-853). The idle check, the move and the brief's
//     record hold proj.vf, which a brief to the reviewer holds too, so no brief comes between them. Admission is live,
//     no fact: a restart finds the request requested and admits it again (the move is idempotent).
//   - briefed: fact briefed{verify_of: the request's seq, reviewed: the head the reviewer's worktree is at}. A brief
//     the log refuses leaves the request requested.
//   - done: the reviewer finished that brief (T-857: only its finished line, a message fact with Message.Ends). Only
//     now its tasks are verified (the fix round's) and covered (the close commit's Covered list), and its head is the
//     one obor review close names.
//   - superseded: another brief to the reviewer recorded while the verify's brief is open (its line delivered or
//     not): its tasks are pending again; a fix round's next pass requests a new verify.
//   - failed, briefed (T-857): the reviewer's stop that is not a usage limit ends the brief (fact stopped with Ends,
//     stopped.go; overloaded, billing_error, …): nothing verified or covered, no head. The stop's line tells the lead
//     the verify failed (verifyStopLine); the round's tasks stay pending and its next pass requests a new verify,
//     admitted once the reviewer is idle.
//   - failed, requested: the admitted verify's git failing (no open review of its id, a task with no merge in the
//     review's scope, the worktree's move): fact message{verify_fail: the request's seq} with the lead's line. The
//     tasks stay pending; the round waits for another obor review fix.
//
// While a verify of the open review is requested or briefed, no second one is requested: a fix round waits (proj.vf
// orders the check and the request's record). obor review close refuses while one is (ReviewCycle.Pending), --force
// closes anyway.

// verifyGitWait bounds a verify's git once admitted.
const verifyGitWait = 30 * time.Second

// verifyState is where a verify record is in its lifecycle (admitted is live: a request being admitted).
type verifyState int

const (
	verifyRequested verifyState = iota
	verifyBriefed
	verifyDone
	verifySuperseded
	verifyFailed
)

// String is the state as a refusal names it.
func (s verifyState) String() string {
	return [...]string{"requested", "briefed", "done", "superseded", "failed"}[s]
}

// verifyRec is one verify: its request's seq, the review it verifies, its tasks and note, the fix round that
// requested it, its state and, once briefed, the head its brief handed the reviewer.
type verifyRec struct {
	seq   int64
	id    string
	tasks []string
	note  string
	round int64
	state verifyState
	head  string
}

// live: requested or briefed, so no second verify of its review is requested.
func (v *verifyRec) live() bool { return v.state == verifyRequested || v.state == verifyBriefed }

// of: v verifies review id.
func (v *verifyRec) of(id string) bool { return v.id == id }

// reviewLog is the review cycle's records folded (T-856, one fold): the last review started (its fact's seq, id and head),
// the tasks the cycle covered so far (its in-scope tasks, then each done verify's), the head the reviewer last finished
// (the start's, then each done verify's: T-857, never a superseded or failed one's), the fix round (reviewfix.go), the verifies by seq, and per agent the
// verify whose brief is that agent's open brief.
type reviewLog struct {
	start     int64
	startID   string
	startHead string
	covered   []string
	reviewed  string
	fix       *fixRound
	verifies  map[int64]*verifyRec
	open      map[string]int64
}

// fold folds r: a review start, a fix fact, a verify's request; a brief (a verify's briefed step; any brief supersedes
// the verify whose brief its agent had open); the finished line that ends a brief (done), a fix round's drop line, a
// verify's failure; a stop that ends a brief (failed).
func (l *reviewLog) fold(r event.Record) {
	if r.Kind != event.KindFact || r.Task != event.ReviewTask && r.Task != event.MsgTask {
		return
	}
	if l.verifies == nil {
		l.verifies, l.open = map[int64]*verifyRec{}, map[string]int64{}
	}
	if r.Task == event.ReviewTask {
		var d ReviewData
		if json.Unmarshal(r.Data, &d) != nil {
			return
		}
		switch r.Step {
		case event.StepReviewStarted:
			l.start, l.startID, l.startHead, l.reviewed = r.Seq, d.ID, d.Head, d.Head
			l.covered = slices.Clone(d.Tasks)
		case event.StepReviewFix:
			l.fix = l.fix.link(r.Seq, d)
		case event.StepReviewVerifyRequest:
			l.verifies[r.Seq] = &verifyRec{seq: r.Seq, id: d.ID, tasks: d.Tasks, note: d.Note, round: d.Round}
		}
		return
	}
	switch r.Step {
	case event.StepBriefed:
		var m event.Message
		if json.Unmarshal(r.Data, &m) != nil {
			return
		}
		if v := l.verifies[l.open[m.To]]; v != nil && v.state == verifyBriefed { // a newer brief replaced its brief
			v.state = verifySuperseded
		}
		delete(l.open, m.To)
		if v := l.verifies[m.VerifyOf]; v != nil && v.state == verifyRequested {
			v.state, v.head = verifyBriefed, m.Reviewed
			l.open[m.To] = v.seq
		}
	case event.StepMessage:
		var m event.Message
		if json.Unmarshal(r.Data, &m) != nil {
			return
		}
		if m.Ends != "" { // the finished line
			l.ended(m.Ends, true)
		}
		if f := l.fix; f != nil {
			for _, t := range m.FixDrop {
				f.dropped[t] = true
			}
		}
		if v := l.verifies[m.VerifyFail]; v != nil && v.state == verifyRequested {
			v.state = verifyFailed
			if f := l.fix; f != nil && v.round != 0 && v.round == f.seq {
				f.failed = v.round
			}
		}
	case event.StepStopped:
		var d stoppedData
		if json.Unmarshal(r.Data, &d) == nil && d.Ends != "" { // an error stop: no finished line follows
			l.ended(d.Ends, false)
		}
	}
}

// ended folds the end of agent's open brief, finished (its finished line) or not (an error stop): the verify it briefed
// is done, its tasks verified and covered and its head reviewed, or failed, nothing of it counted (T-857).
func (l *reviewLog) ended(agent string, finished bool) {
	v := l.verifies[l.open[agent]]
	delete(l.open, agent)
	if v == nil || v.state != verifyBriefed {
		return
	}
	if !finished {
		v.state = verifyFailed
		return
	}
	v.state = verifyDone
	if l.start != 0 && v.seq > l.start {
		l.covered = append(l.covered, v.tasks...)
		l.reviewed = cmp.Or(v.head, l.reviewed)
	}
	if f := l.fix; f != nil && v.of(f.id) {
		for _, t := range v.tasks {
			f.verified[t] = true
		}
	}
}

// briefedTo is the verify whose brief is agent's open brief (nil: none).
func (l *reviewLog) briefedTo(agent string) *verifyRec {
	if v := l.verifies[l.open[agent]]; v != nil && v.state == verifyBriefed {
		return v
	}
	return nil
}

// retried: a fix round's next pass requests a verify again once v failed (fixPass): v verifies the round's review, a
// task of v is linked and pending, and the round's verify did not fail its git.
func (l *reviewLog) retried(v *verifyRec) bool {
	f := l.fix
	if f == nil || !v.of(f.id) || f.failed == f.seq {
		return false
	}
	pending := f.pending()
	return slices.ContainsFunc(v.tasks, func(t string) bool { return slices.Contains(pending, t) })
}

// waiting is a verify the pipeline still owes (T-858): one requested or admitted (admission is live, the fold still has
// it requested) whose brief has not been recorded. A failed one its fix round requests again is requested again within
// the pass, inside idle's settle window (T-859, T-869); a done or superseded verify waits for nothing.
func (l *reviewLog) waiting() bool {
	for _, v := range l.verifies {
		if v.state == verifyRequested {
			return true
		}
	}
	return false
}

// verifyStopLine is the lead's line on agent's stop (said: "<agent> stopped: <error> (<details>)") that ends the brief
// of a verify (T-857): the verify failed, retried by its fix round or by another obor review fix; "": agent's open brief
// is no verify's.
func (l *reviewLog) verifyStopLine(agent, said string) string {
	v := l.briefedTo(agent)
	if v == nil {
		return ""
	}
	retry := "obor review fix " + strings.Join(v.tasks, ",") + " to verify again"
	if l.retried(v) {
		retry = "serve retries when the reviewer is idle"
	}
	return "[obor] " + cmp.Or(v.id, "the review's") + " verify failed: " + said + "; " + retry
}

// sorted is the verifies in log order.
func (l *reviewLog) sorted() []*verifyRec {
	return slices.SortedFunc(maps.Values(l.verifies), func(a, b *verifyRec) int { return cmp.Compare(a.seq, b.seq) })
}

// inFlight is review id's verify requested or briefed (nil: none).
func (l *reviewLog) inFlight(id string) *verifyRec {
	for _, v := range l.sorted() {
		if v.live() && v.of(id) {
			return v
		}
	}
	return nil
}

// requested is the oldest verify still requested (nil: none): the next to admit.
func (l *reviewLog) requested() *verifyRec {
	for _, v := range l.sorted() {
		if v.state == verifyRequested {
			return v
		}
	}
	return nil
}

// pending is review id's linked fix tasks neither verified nor dropped, then the tasks of its verify in flight, once
// each.
func (l *reviewLog) pending(id string) []string {
	var out []string
	if f := l.fix; f != nil && f.id == id {
		out = f.pending()
	}
	if v := l.inFlight(id); v != nil {
		for _, t := range v.tasks {
			if !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	return out
}

// ReviewCycle is review id's state from the project's event log (T-856: reviewLog, the fold serve keeps). Covered: the
// last start's in-scope tasks plus each done verify's since, once each. Start: the head that review started at.
// Reviewed: the head the reviewer last finished when the last start is id's (its last done verify's, else its start's; T-857: never the head of a verify
// superseded or failed; "": the log has no start of id, which obor review close refuses). Pending: id's linked
// fix tasks neither verified nor dropped and the tasks of its verify requested or briefed (obor review close's
// refusal). An unreadable log is the zero state.
type ReviewCycle struct {
	Covered  []string
	Start    string
	Reviewed string
	Pending  []string
}

// ReadReviewCycle is review id's ReviewCycle from the log of the project at root.
func ReadReviewCycle(root, id string) ReviewCycle {
	recs, err := event.ReadLog(root)
	if err != nil {
		return ReviewCycle{}
	}
	var l reviewLog
	for _, r := range recs {
		l.fold(r)
	}
	c := ReviewCycle{Start: l.startHead, Pending: l.pending(id)}
	for _, t := range l.covered {
		if !slices.Contains(c.Covered, t) {
			c.Covered = append(c.Covered, t)
		}
	}
	if l.start != 0 && l.startID == id {
		c.Reviewed = l.reviewed
	}
	return c
}

// OpenCycle is the open review by git: its id (the newest "RV-<n> closed" commit + 1), the previous review's id,
// reviewed sha and coverage start ("" when it has neither: no close commit yet), and its review file in the main
// checkout.
type OpenCycle struct {
	ID, Prev, PrevSHA, From, File string
}

// OpenReview is the open review of the project at root (obor review fix and close): no review file
// <reviews>/RV-<n>.md in the main checkout (T-718: the reviewer writes it there) is no open review, an error.
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
		return c, fmt.Errorf("no open review: no %s/%s.md in %s (%s is not written yet, or the last review is closed)",
			s.Reviews, c.ID, root, c.ID)
	}
	return c, nil
}

// errVerifyPending refuses a second verify while one of the review is requested or briefed.
var errVerifyPending = errors.New("a verify is pending")

// requestVerify records d, a verify's request (the merge loop's: it only tries p.vf, the next pass looks again): its
// seq. Refused, nothing recorded: a verify of d.ID requested or briefed already, or the log refusing it.
func (a *App) requestVerify(p *proj, d ReviewData) (int64, error) {
	if !p.vf.TryLock() {
		return 0, fmt.Errorf("%w (one is being admitted now)", errVerifyPending)
	}
	defer p.vf.Unlock()
	a.mu.Lock()
	v := p.folded.rv.inFlight(d.ID)
	a.mu.Unlock()
	if v != nil {
		return 0, fmt.Errorf("%w (%s, %s): wait for it or close the review", errVerifyPending, strings.Join(v.tasks, ","), v.state)
	}
	seq, ok := a.recordSeq(p, event.Record{Task: event.ReviewTask, Step: event.StepReviewVerifyRequest, Kind: event.KindFact}, d)
	if !ok {
		return 0, fmt.Errorf("the event log of %s refused it", p.name)
	}
	a.wake()
	return seq, nil
}

// admitVerify admits the verify requested at seq and briefs it. Not admitted, nil: no longer requested, the reviewer
// not idle or another admission holding p.vf (the next pass looks again); a publish running does not hold it (T-872:
// verifyText reads the head once, roleworktree.go). Admitted, a failure (verifyText, the move) records the verify
// failed, with the lead's line, and is the error; a brief the log refuses is the error too, the verify still
// requested.
func (a *App) admitVerify(ctx context.Context, p *proj, g ReviewGit, s ReviewSetup, seq int64) error {
	// p.vf is held from the idle check through the brief's record.
	if !p.vf.TryLock() {
		return nil
	}
	defer p.vf.Unlock()
	var v verifyRec
	a.mu.Lock()
	cur := p.folded.rv.verifies[seq]
	if cur != nil && cur.state == verifyRequested && p.out.brief(s.Agent) == nil && a.roleIdle(p, s.Agent) {
		v = *cur
	}
	a.mu.Unlock()
	if v.seq == 0 {
		return nil
	}
	text, head, err := verifyText(ctx, g, p.root, s, v.id, v.tasks, v.note)
	if err == nil {
		err = g.Detach(ctx, s.Worktree, head) // T-853: the reviewer's worktree at the head it verifies
	}
	if err != nil {
		if !cutByServe(ctx, err) {
			a.verifyFailed(p, &v, err)
		}
		return err
	}
	if _, ok := a.recordBrief(p, s.Agent, "obor", text, "", "", false, &verifyOf{seq: v.seq, head: head}); !ok {
		return fmt.Errorf("the event log of %s refused it (the verify stays requested: serve tries again)", p.name)
	}
	a.wake()
	return nil
}

// verifyFailed records the verify v failed with err, the lead's line on it.
func (a *App) verifyFailed(p *proj, v *verifyRec, err error) {
	a.logf("review verify of %s: %v", p.name, err)
	m := event.Message{To: p.out.lead(), From: "obor", VerifyFail: v.seq,
		Line: "[obor] " + v.id + " fix verify of " + strings.Join(v.tasks, ",") + " failed: " + err.Error() +
			" (obor review fix " + strings.Join(v.tasks, ",") + " asks again)"}
	if !a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m) {
		a.logf("review verify of %s: the event log refused its failure (the verify stays requested)", p.name)
	}
}

// verifyPass admits p's oldest requested verify (admitVerify); a failure tells the lead.
func (a *App) verifyPass(ctx context.Context, p *proj, fx *Loop) {
	a.mu.Lock()
	v := p.folded.rv.requested()
	run := v != nil && p.merging && fx.Review != nil
	a.mu.Unlock()
	if !run {
		return
	}
	s, err := reviewConfig(p.root)
	if err != nil {
		a.logf("review verify of %s: %v", p.name, err)
		return
	}
	vctx, cancel := context.WithTimeout(ctx, verifyGitWait)
	defer cancel()
	if err := a.admitVerify(vctx, p, fx.Review, s, v.seq); err != nil && !cutByServe(ctx, err) {
		a.logf("review verify of %s: %v", p.name, err)
	}
}

// verifyText finds the open review (it must be id, when named) and each task's merge in the next review's scope
// (T-822: a task merged after the review started, which it does not cover, is allowed) on the integration branch's
// head: the verify brief and that head.
func verifyText(ctx context.Context, g ReviewGit, root string, s ReviewSetup, id string, tasks []string,
	note string) (text, head string, err error) {
	c, err := OpenReview(ctx, g, root, s)
	if err != nil {
		return "", "", err
	}
	if id != "" && c.ID != id {
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
	return verifyBrief(c.ID, s, head[:min(len(head), 7)], fixed, note), head, nil
}

// verifyBrief is review-brief.md §4's text: the fixes, the head, every finding to re-run, the verify file, the
// redundancy question (docs/design.md "Change rules" rule 6), the lead's note last.
func verifyBrief(id string, s ReviewSetup, short string, fixed []string, note string) string {
	text := fmt.Sprintf("The %s findings were fixed in %s; %s is at %s (your worktree too). Re-run your probes for every"+
		" finding and report fixed / incomplete / regressed per finding. %s Write %s/%s-verify.md in the main checkout %s."+
		" Do not commit.", id, strings.Join(fixed, ", "), s.Branch, short, redundantLine, s.Reviews, id, s.Root)

	if note = strings.TrimSpace(note); note != "" {
		text += "\nLead's note: " + note
	}
	return text
}
