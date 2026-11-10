// reviewclose.go: a review's close is a call to serve. obor review close sends it;
// serve checks it against its own fold, commits the close in the main checkout and records fact review.closed. The
// fact is what "is a review open" reads (review.go: obor review's refusal, a due request's wait, obor status), and what
// the verify rule (reviewverify.go) and the reviewer's stop rule (rolestop.go) are keyed by.
package app

import (
	"context"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
	"strings"
)

// Guarantees (docs/design.md "Change rules" 1): the commit is kernel — it writes main under the publish admission, as
// config_set's commit does (admit; configMu: one main-checkout writer at a time), so no publish or other writer runs
// between its checks and its commit; the fact is recorded only after the commit is in. The refusals are the command's
// (skills/lead/review-brief.md §5). The reviewer's stop is the existing rule, keyed by the fact.

// reviewCycle is the open review's cycle as serve's fold holds it (reviewLog): the tasks it
// covered (its in-scope tasks plus each finished verify's, once each), the head it started at, the head the reviewer
// last finished (never the head of a verify that did not finish; "": the last start is not id's, or it is
// closed) and the linked tasks still open, or merged and not covered (the close's refusal).
type reviewCycle struct {
	covered  []string
	start    string
	reviewed string
	pending  []string
}

// reviewCycle is review id's cycle (a spec that does not read is the error).
func (a *App) reviewCycle(p *proj, id string) (reviewCycle, error) {
	a.mu.Lock()
	cur := p.folded.rv.cur()
	c := reviewCycle{start: cur.head, covered: cur.cycle()}
	ours := cur.id == id && cur.seq != 0 && cur.closed == 0
	var cands []fixLink
	if ours {
		c.reviewed, cands = cur.reviewed, fixCandidates(p.folded.loop, cur.seq)
	}
	a.mu.Unlock()
	if !ours {
		return c, nil
	}
	links, err := linkFixes(cands, id, func(task string) (store.SpecRow, bool, error) { return specOf(p, task) })
	c.pending = pendingFixes(links, c.covered)
	return c, err
}

// reviewCloseCall answers the socket's review_close request (obor review close): a call id and a summary are
// required, a repeated call id waits for the close in flight and gets its stored reply. The call takes configMu (one
// main-checkout writer at a time) before its stored-reply lookup, and is admitted (admit, as config_set) before its
// first check; the reply is CLOSED <the close commit's subject>.
func (a *App) reviewCloseCall(c wire.ReviewCloseReq) (string, error) {
	summary := strings.Join(strings.Fields(c.Summary), " ")
	if c.CallID == "" || summary == "" {
		return "", errors.New(wire.MethodReviewClose + ": a call id and a summary are required")
	}
	if c.Role != "" && c.Role != "lead" {
		return "", fmt.Errorf("review close: only the lead closes a review (%s)", c.Role)
	}
	a.configMu.Lock() // one main-checkout writer at a time; a repeated call id waits for the close in flight before its
	// stored-reply lookup
	defer a.configMu.Unlock()
	p, reply, stored := a.callProj(wire.MethodReviewClose, c.Envelope)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodReviewClose, c.Root)
	case stored:
		return reply, nil
	case !a.merges(p):
		return "", mergeOff(p)
	}
	s, err := reviewConfig(p.root)
	if err != nil {
		return "", err
	}
	fx, err := a.loopOf(p)
	if err == nil && fx.Workflow == nil {
		err = errors.New("the merge loop has no workflow")
	}
	if err != nil {
		return "", fmt.Errorf("review close: %v", err)
	}
	wait, cancel := context.WithTimeout(a.life, admitWait)
	release, err := a.admit(wait, p) // a publish running ends first; none starts until the commit is in
	cancel()
	if err != nil {
		return "", fmt.Errorf("review close: %v", err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(a.life, requestTimeout)
	defer cancel()
	return a.reviewClose(ctx, p, fx.Workflow, s, c, summary)
}

// closedReply starts a close's reply, CLOSED <the close commit's subject>: the fold reads the subject back from it.
const closedReply = "CLOSED "

// closeUnwrittenHint is the way out an unwritten review's file refusal names (openReview's missing-file refusal).
const closeUnwrittenHint = `obor review close "<summary>" --force closes it unwritten`

// reviewClose checks the open review (admitted): its own close commit not in git already (closeCommitted: one a crash
// cut from its fact is recorded now, and the call refused as no review is open), the fold's open review, its file in
// the main checkout (openReview; --force closes a review whose file was never written: nothing to delete, the reviewed
// sha its start's head), its linked fixes verified (--force: closes anyway); then the main checkout commits
// it (port.Review.Close: its own refusals, the branch named) and fact review.closed{id} is recorded, the reviewer's stop
// rule woken at once. Before the commit, fact review.closing{id, head, tasks} records its snapshot: the head
// and the tasks the commit names, the fact carries and the fold freezes the review at. Any refusal changes nothing
// (a review.closing whose commit was refused is replaced by the next close's).
func (a *App) reviewClose(ctx context.Context, p *proj, g port.Review, s ReviewSetup, c wire.ReviewCloseReq, summary string) (string,
	error) {
	a.mu.Lock()
	open := p.folded.rv.last()
	a.mu.Unlock()
	if err := a.closeCommitted(ctx, p, g, open); err != nil { // a commit whose fact a crash cut: never committed twice
		return "", fmt.Errorf("review close: is %s closed in git: %v: nothing changed", open.id, err)
	}
	o, err := a.openReview(p, s, closeUnwrittenHint)
	unwritten := err != nil && errors.Is(err, errNoOpenReview) && o.ID != ""
	if err != nil && !(unwritten && c.Force) {
		return "", err
	}
	cycle, err := a.reviewCycle(p, o.ID)
	if err != nil {
		return "", fmt.Errorf("%s's fixes: %v: nothing changed", o.ID, err)
	}
	if cycle.reviewed == "" {
		return "", fmt.Errorf("no review start for %s in the log: nothing changed", o.ID)
	}
	if len(cycle.pending) > 0 && !c.Force {
		return "", fmt.Errorf("%s's fixes are not verified: %s (open, or merged and its verify not finished; --force"+
			" closes anyway): nothing changed", o.ID, strings.Join(cycle.pending, ", "))
	}
	// The snapshot, decided once: the commit, the fact and a fact recovered from the commit after a crash all carry it
	// (a verify ending after it changes nothing of the close).
	snap := ReviewData{ID: o.ID, Head: cycle.reviewed, Tasks: cycle.covered}
	if !a.record(p, event.Record{Task: event.ReviewTask, Step: event.StepReviewClosing, Kind: event.KindFact}, snap) {
		return "", fmt.Errorf("review close: the event log of %s refused fact %s: nothing changed", p.name,
			event.StepReviewClosing)
	}
	sha := short(cycle.reviewed) // the head the reviewer last finished (its start's or a done verify's)
	subject, body := CloseCommit(o.ID, sha, summary, cycle.covered, cycle.start, len(c.Accept), unwritten)
	if err := g.Close(ctx, port.ReviewCommit{Branch: s.Branch, ID: o.ID, File: o.File, Reviewed: cycle.reviewed,
		Subject: subject, Body: body, Accepts: c.Accept}); err != nil {
		return "", err
	}
	reply := closedReply + subject
	d := ReviewData{ID: o.ID, Call: c.CallID, Reply: reply, Head: snap.Head, Tasks: snap.Tasks}
	if !a.record(p, event.Record{Task: event.ReviewTask, Step: event.StepReviewClosed, Kind: event.KindFact}, d) {
		a.mu.Lock()
		p.closeRead = false // the next pass records it from the commit (reviewCloseRead)
		a.mu.Unlock()
		a.wake()
		return "", fmt.Errorf("review close: committed %s, but the event log of %s refused its record (serve records it"+
			" from the commit on its next pass)", o.ID, p.name)
	}
	a.wake() // the reviewer's stop rule, a request waiting on the close
	return reply, nil
}

// reviewCloseRead is the close's crash cut (kernel: its commit and its fact exactly once): once per process
// (closeRead) while the fold shows a review open, the one git read of a close: review id's own close commit on the
// integration branch since its start (port.Review.Closed). Found, fact review.closed is recorded from it: a crash (or a
// refused append) between the close's commit and its fact. It decides only whether that close happened, never what
// the review covered (the fold's cycle). It takes configMu (a close call in flight holds it: this pass skips and the
// next reads again) and records only while the fold still shows the same review open.
func (a *App) reviewCloseRead(ctx context.Context, p *proj, fx *Loop) {
	a.mu.Lock()
	open, done := p.folded.rv.last(), p.closeRead
	a.mu.Unlock()
	if done || fx == nil || fx.Workflow == nil {
		return
	}
	if !a.configMu.TryLock() {
		return
	}
	defer a.configMu.Unlock()
	if err := a.closeCommitted(ctx, p, fx.Workflow, open); err != nil {
		a.logf("%s: is %s closed in git: %v", p.name, open.id, err)
		return
	}
	a.mu.Lock()
	p.closeRead = true
	a.mu.Unlock()
}

// closeCommitted records fact review.closed from open's own close commit when there is one (under configMu).
func (a *App) closeCommitted(ctx context.Context, p *proj, g port.Review, open lastReview) error {
	if open.open() == "" {
		return nil
	}
	s, err := reviewConfig(p.root)
	if err != nil {
		return nil // reviews are not set up: no close to read
	}
	ctx, cancel := context.WithTimeout(ctx, verifyGitWait)
	defer cancel()
	subject, err := g.Closed(ctx, s.Branch, open.head, open.id)
	if err != nil || !ClosesReview(subject, open.id) {
		return err // open: no close commit for it
	}
	d := ReviewData{ID: open.id, Reply: closedReply + subject}
	a.mu.Lock()
	same := p.folded.rv.last() == open
	if s := p.folded.rv.cur().closing; s != nil { // the snapshot the close recorded before its commit
		d.Head, d.Tasks = s.head, s.tasks
	}
	a.mu.Unlock()
	if !same {
		return nil
	}
	if !a.record(p, event.Record{Task: event.ReviewTask, Step: event.StepReviewClosed, Kind: event.KindFact}, d) {
		return errors.New("the event log refused fact " + event.StepReviewClosed)
	}
	a.logf("%s: %s closed by commit %q: recorded", p.name, open.id, subject)
	a.wake()
	return nil
}

// ClosesReview: subject is review id's close commit's (CloseCommit's "<id> closed · …"; port.Review.Closed's filter).
func ClosesReview(subject, id string) bool {
	return strings.HasPrefix(subject, id+" closed · ")
}

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
