package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"slices"
	"strconv"
	"strings"
)

// A review's fix round (T-850; skills/lead/review-brief.md §3–4): `obor review fix <T-a,T-b> [--note "<text>"]` (the
// lead or the owner) links fix tasks to the open review, fact review.fix{id, tasks, note, by} on the task key review; a
// second call adds tasks (a fix fact of another review starts a new round). Once every linked task not verified yet
// merged, a merge-loop pass requests a verify of them (fact review.verify.request{round: the round's seq}; T-856:
// reviewverify.go admits it once the reviewer is idle and briefs it with review-brief.md §4's text), unless a verify of
// the review is requested or briefed already: one at a time. The tasks are verified once the reviewer finished that
// verify's brief; one superseded or failed leaves them pending, and the next pass requests again (T-857: one the
// reviewer's error stop failed too; one whose git failed: the round waits for a verify by hand or another obor review
// fix, the lead told once). A linked task dropped or rejected
// before its merge: the lead gets one line (its fact's Message.FixDrop marks it told) and the verify waits for the
// rest. A round whose review is closed is not run. obor review close refuses while a linked task is neither verified
// nor dropped (ReviewCycle.Pending), --force closes anyway.

// fixRound is the open review's fix round: the review it fixes, its last fix fact's seq, the linked tasks in order,
// the lead's notes, the tasks a done verify verified and those whose drop the lead was told, and the seq of the round
// whose verify failed (0: none). reviewLog folds it.
type fixRound struct {
	id       string
	seq      int64
	tasks    []string
	notes    []string
	verified map[string]bool
	dropped  map[string]bool
	failed   int64
}

// pending is the linked tasks neither verified nor dropped, in link order.
func (f *fixRound) pending() []string {
	var out []string
	for _, t := range f.tasks {
		if !f.verified[t] && !f.dropped[t] {
			out = append(out, t)
		}
	}
	return out
}

// link folds fix fact seq of d into the round f: its tasks linked, its note kept; a fact of another review (or none
// before) starts a new round.
func (f *fixRound) link(seq int64, d ReviewData) *fixRound {
	if f == nil || f.id != d.ID {
		f = &fixRound{id: d.ID, verified: map[string]bool{}, dropped: map[string]bool{}}
	}
	f.seq = seq
	for _, t := range d.Tasks {
		if !slices.Contains(f.tasks, t) {
			f.tasks = append(f.tasks, t)
		}
	}
	if n := strings.TrimSpace(d.Note); n != "" {
		f.notes = append(f.notes, n)
	}
	return f
}

// fixCall answers the socket's review_fix request (the lead or the owner): Call.After the fix tasks linked to the
// open review, Call.Note what to check. A repeated call id gets the stored reply. Refused, nothing recorded: no fix
// task, a task not dispatched or ended unmerged already, no reviewer setup, no open review (OpenReview).
func (a *App) fixCall(c wire.Call) (string, error) {
	if c.CallID == "" {
		return "", errors.New(wire.MethodFix + ": a call id is required")
	}
	p, reply, stored := a.callProj(wire.MethodFix, c)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodFix, c.Root)
	case stored:
		return reply, nil
	case !a.merges(p):
		return "", mergeOff(p)
	case c.Role != "" && c.Role != "lead":
		return "", fmt.Errorf("review fix: the lead or the owner links fixes, not %s (role %s)", Dash(c.Agent), c.Role)
	case len(c.After) == 0:
		return "", errors.New("review fix: the fix tasks are required")
	}
	a.mu.Lock()
	var bad error
	for _, task := range c.After {
		if p.folded.loop[task] == nil {
			bad = fmt.Errorf("review fix: %s is not dispatched", task)
			break
		}
		if end := p.reviewOutcome([]string{task}); end != "" {
			bad = fmt.Errorf("review fix: %s already", end)
			break
		}
	}
	a.mu.Unlock()
	if bad != nil {
		return "", bad
	}
	s, err := reviewConfig(p.root)
	if err != nil {
		return "", fmt.Errorf("review fix: %v", err)
	}
	fx, err := a.opt.Loop(p.root)
	if err == nil && fx.Review == nil {
		err = errors.New("the merge loop has no review git")
	}
	if err != nil {
		return "", fmt.Errorf("review fix: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), verifyGitWait)
	defer cancel()
	open, err := OpenReview(ctx, fx.Review, p.root, s)
	if err != nil {
		return "", fmt.Errorf("review fix: %v", err)
	}
	a.mu.Lock()
	var linked []string
	if f := p.folded.rv.fix; f != nil && f.id == open.ID {
		linked = slices.Clone(f.tasks)
	}
	a.mu.Unlock()
	for _, t := range c.After {
		if !slices.Contains(linked, t) {
			linked = append(linked, t)
		}
	}
	d := ReviewData{ID: open.ID, Tasks: c.After, Note: c.Note, By: cmp.Or(c.Agent, "owner"), Call: c.CallID,
		Reply: "FIX LINKED to " + open.ID + ": " + strings.Join(linked, ",") + " (serve briefs the reviewer to verify once they merged)"}
	if !a.record(p, event.Record{Task: event.ReviewTask, Step: event.StepReviewFix, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("review fix: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}

// fixPass runs p's fix round: a linked task ended unmerged gets the lead's line once; every pending task merged, it
// requests their verify (reviewverify.go admits it) unless the round's review is closed (its pending tasks recorded
// dropped, once), its verify failed, or a verify of the review is requested or briefed (the next pass looks again).
func (a *App) fixPass(ctx context.Context, p *proj, fx *Loop) {
	a.mu.Lock()
	f := p.folded.rv.fix
	run := f != nil && f.failed != f.seq && p.merging && p.out.lead() != "" && fx.Review != nil
	var pending, ended, waiting []string
	var seq int64
	var id, note string
	if run {
		seq, id, note = f.seq, f.id, strings.Join(f.notes, "; ")
		pending = f.pending()
		for _, t := range pending {
			if end := p.reviewOutcome([]string{t}); end != "" {
				ended = append(ended, t)
			}
		}
		waiting = p.reviewWaits(pending)
	}
	a.mu.Unlock()
	if !run || len(pending) == 0 {
		return
	}
	s, err := reviewConfig(p.root)
	if err != nil {
		return
	}
	last, err := fx.Review.Last(ctx, s.Branch)
	if err != nil {
		a.logf("review fix of %s: %v", p.name, err)
		return
	}
	if n, _, _ := PreviousReview(last.Subject); "RV-"+strconv.Itoa(n) != id {
		// The round's review closed under it (a forced close): nothing left to verify. Record the pending tasks
		// dropped so the fold stops counting the round, once: pending() empties and later passes return early
		// (T-858: a failed verify no longer keeps idle off after the close).
		if !a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact},
			event.Message{To: p.out.lead(), From: "obor", FixDrop: pending}) {
			a.logf("review fix of %s: the event log refused the closed round's record", p.name)
		}
		return
	}
	if len(ended) > 0 {
		rest := slices.DeleteFunc(slices.Clone(pending), func(t string) bool { return slices.Contains(ended, t) })
		line := "[obor] " + id + " fix " + strings.Join(ended, ",") + " ended unmerged: "
		if len(rest) > 0 {
			line += "the verify waits for " + strings.Join(rest, ",")
		} else {
			line += "nothing left to verify"
		}
		m := event.Message{To: p.out.lead(), From: "obor", Line: line, FixDrop: ended}
		if !a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m) {
			return
		}
		pending = rest
	}
	if len(pending) == 0 || len(waiting) > 0 {
		return
	}
	// One verify at a time (errVerifyPending): the round's tasks wait for it to be done, superseded or failed.
	d := ReviewData{ID: id, Tasks: pending, Note: note, By: "obor", Round: seq}
	if _, err := a.requestVerify(p, d, true); err != nil && !errors.Is(err, errVerifyPending) {
		a.logf("review fix of %s: %v", p.name, err)
	}
}
