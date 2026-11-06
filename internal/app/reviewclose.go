// reviewclose.go: a review's close is a call to serve (T-926; DD-architecture M2, D1-B). obor review close sends it;
// serve checks it against its own fold, commits the close in the main checkout and records fact review.closed. The
// fact is what "is a review open" reads (review.go: obor review's refusal, a due request's wait, obor status), and what
// the verify rule (reviewverify.go) and the reviewer's stop rule (rolestop.go) are keyed by.
package app

import (
	"context"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/store"
	"slices"
	"strings"
)

// Guarantees (docs/design.md "Change rules" 1): the commit is kernel — it writes main under the publish admission, as
// config_set's commit does (admit; configMu: one main-checkout writer at a time), so no publish or other writer runs
// between its checks and its commit; the fact is recorded only after the commit is in. The refusals are the command's
// (skills/lead/review-brief.md §5). The reviewer's stop is the existing rule, keyed by the fact.

// reviewCycle is the open review's cycle as serve's fold holds it (reviewLog, T-856, T-924): the tasks it covered (its
// in-scope tasks plus each finished verify's, once each), the head it started at, the head the reviewer last finished
// (T-857: never the head of a verify that did not finish; "": the last start is not id's, or it is closed) and the
// linked tasks still open, or merged and not covered (the close's refusal).
type reviewCycle struct {
	covered  []string
	start    string
	reviewed string
	pending  []string
}

// reviewCycle is review id's cycle (a spec that does not read is the error).
func (a *App) reviewCycle(p *proj, id string) (reviewCycle, error) {
	a.mu.Lock()
	l, started := p.folded.rv, p.folded.reviewed
	c := reviewCycle{start: l.startHead}
	for _, t := range l.covered {
		if !slices.Contains(c.covered, t) {
			c.covered = append(c.covered, t)
		}
	}
	ours := l.start != 0 && l.startID == id && started.seq == l.start && started.open() != ""
	var cands []fixLink
	if ours {
		c.reviewed, cands = l.reviewed, fixCandidates(p.folded.loop, l.start)
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
// required, a repeated call id gets the stored reply. The call is admitted (admit, as config_set) before its first
// check, and the reply is CLOSED <the close commit's subject>.
func (a *App) reviewCloseCall(c wire.Call) (string, error) {
	summary := strings.Join(strings.Fields(c.Note), " ")
	if c.CallID == "" || summary == "" {
		return "", errors.New(wire.MethodReviewClose + ": a call id and a summary are required")
	}
	p, reply, stored := a.callProj(wire.MethodReviewClose, c)
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
	if err == nil && fx.Review == nil {
		err = errors.New("the merge loop has no review git")
	}
	if err != nil {
		return "", fmt.Errorf("review close: %v", err)
	}
	a.configMu.Lock() // one main-checkout writer at a time
	defer a.configMu.Unlock()
	wait, cancel := context.WithTimeout(a.life, admitWait)
	release, err := a.admit(wait, p) // a publish running ends first; none starts until the commit is in
	cancel()
	if err != nil {
		return "", fmt.Errorf("review close: %v", err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(a.life, requestTimeout)
	defer cancel()
	return a.reviewClose(ctx, p, fx.Review, s, c, summary)
}

// reviewClose checks the open review (admitted): its file in the main checkout (openReview), its start in the log, its
// linked fixes verified (--force: closes anyway); then the main checkout commits it (ReviewGit.Close: its own
// refusals) and fact review.closed{id, sha, covered, start} is recorded, the reviewer's stop rule woken at once. Any
// refusal changes nothing.
func (a *App) reviewClose(ctx context.Context, p *proj, g ReviewGit, s ReviewSetup, c wire.Call, summary string) (string,
	error) {
	o, err := openReview(ctx, g, p.root, s)
	if err != nil {
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
	sha := short(cycle.reviewed) // the head the reviewer last finished (its start's or a done verify's, T-857)
	subject, body := CloseCommit(o.ID, sha, summary, cycle.covered, cycle.start, len(c.Accept))
	if err := g.Close(ctx, ReviewCommit{Branch: s.Branch, ID: o.ID, File: o.File, Reviewed: cycle.reviewed,
		Subject: subject, Body: body, Accepts: c.Accept}); err != nil {
		return "", err
	}
	reply := "CLOSED " + subject
	d := ReviewData{ID: o.ID, SHA: sha, Covered: cycle.covered, Start: cycle.start, Call: c.CallID, Reply: reply}
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

// reviewCloseRead records the close of the last review started from git when the log has none (a pass, once per
// process: closeRead): the newest close commit on the integration branch names that review or a later one. It is
// the bounded read at start, not a poll: a crash (or a refused append) between the close's commit and its fact. Once:
// it reads only while the fold shows the review open, takes configMu (a close call in flight holds it: this pass
// skips and the next reads again) and the fold keeps the first close. Compat until v0.30 (docs/design.md change rule
// 5): a store from before T-926 has its reviews closed by commit and no fact; this read records its last one's on the
// first pass, so no branch of its own is needed and none remains to delete.
func (a *App) reviewCloseRead(ctx context.Context, p *proj, fx *Loop) {
	a.mu.Lock()
	started, done := p.folded.reviewed, p.closeRead
	a.mu.Unlock()
	if done || fx == nil || fx.Review == nil {
		return
	}
	if !a.configMu.TryLock() {
		return
	}
	defer a.configMu.Unlock()
	if err := a.readClose(ctx, p, fx.Review, started); err != nil {
		a.logf("%s: is %s closed in git: %v", p.name, started.id, err)
		return
	}
	a.mu.Lock()
	p.closeRead = true
	a.mu.Unlock()
}

// readClose records fact review.closed from the newest close commit when it closes started, still the open review
// (under configMu).
func (a *App) readClose(ctx context.Context, p *proj, g ReviewGit, started lastReview) error {
	n, ok := reviewNumber(started.open())
	if !ok {
		return nil
	}
	s, err := reviewConfig(p.root)
	if err != nil {
		return nil // reviews are not set up: no close to read
	}
	ctx, cancel := context.WithTimeout(ctx, verifyGitWait)
	defer cancel()
	last, err := g.Last(ctx, s.Branch)
	if err != nil {
		return err
	}
	id, sha, _, covered, start := parseCloseCommit(last.Subject, last.Body)
	if k, ok := reviewNumber(id); !ok || k < n {
		return nil // open: no close commit for it
	}
	a.mu.Lock()
	same := p.folded.reviewed == started
	a.mu.Unlock()
	if !same {
		return nil
	}
	d := ReviewData{ID: id, SHA: sha, Covered: covered, Start: start}
	if !a.record(p, event.Record{Task: event.ReviewTask, Step: event.StepReviewClosed, Kind: event.KindFact}, d) {
		return errors.New("the event log refused fact " + event.StepReviewClosed)
	}
	a.logf("%s: %s closed by commit %q: recorded", p.name, started.id, last.Subject)
	a.wake()
	return nil
}
