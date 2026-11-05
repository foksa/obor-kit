package app

import (
	"cmp"
	"context"
	"encoding/json"
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
// merged, a merge-loop pass runs what obor review verify runs (verifyText: the head marked, the same reviewer briefed
// with review-brief.md §4's text): the brief's fact names the tasks it verifies (Message.Verify), and its fold marks
// them verified, so a replay never verifies them twice. A linked task dropped or rejected before its merge: the lead
// gets one line (its fact's Message.FixDrop marks it told) and the verify waits for the rest. A verify that fails for
// another reason than a publish running or a verify pending gets the lead one line (Message.FixFail: the round's
// seq) and the round waits for a verify by hand or another obor review fix. A round whose review is closed is not run.
// obor review close refuses while a linked task is neither verified nor dropped (ReviewFixPending), --force closes
// anyway.

// fixRound is the open review's fix round: the review it fixes, its last fix fact's seq, the linked tasks in order,
// the lead's notes, the tasks a verify brief named and those whose drop the lead was told, and the seq of the round
// whose auto verify failed (0: none).
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

// foldFix folds r into the fix round *f: a fix fact links its tasks (another review's starts a new round), a verify
// brief marks its tasks verified, a drop line marks its tasks told, a failure line the round failed. Serve's fold and
// obor review close's log read (ReviewFixPending) both use it.
func foldFix(f **fixRound, r event.Record) {
	if r.Kind != event.KindFact {
		return
	}
	switch {
	case r.Task == event.ReviewTask && r.Step == event.StepReviewFix:
		var d ReviewData
		if json.Unmarshal(r.Data, &d) != nil {
			return
		}
		if *f == nil || (*f).id != d.ID {
			*f = &fixRound{id: d.ID, verified: map[string]bool{}, dropped: map[string]bool{}}
		}
		x := *f
		x.seq = r.Seq
		for _, t := range d.Tasks {
			if !slices.Contains(x.tasks, t) {
				x.tasks = append(x.tasks, t)
			}
		}
		if n := strings.TrimSpace(d.Note); n != "" {
			x.notes = append(x.notes, n)
		}
	case r.Task == event.MsgTask && (r.Step == event.StepBriefed || r.Step == event.StepMessage) && *f != nil:
		var m event.Message
		if json.Unmarshal(r.Data, &m) != nil {
			return
		}
		x := *f
		if r.Step == event.StepBriefed {
			for _, t := range m.Verify {
				x.verified[t] = true
			}
		}
		for _, t := range m.FixDrop {
			x.dropped[t] = true
		}
		if m.FixFail != 0 && m.FixFail == x.seq {
			x.failed = m.FixFail
		}
	}
}

// ReviewFixPending is the open review id's linked fix tasks neither verified nor dropped (obor review close's refusal),
// read from the project's event log; none when the log is unreadable or has no fix round of id.
func ReviewFixPending(root, id string) []string {
	recs, err := event.ReadLog(root)
	if err != nil {
		return nil
	}
	var f *fixRound
	for _, r := range recs {
		foldFix(&f, r)
	}
	if f == nil || f.id != id {
		return nil
	}
	return f.pending()
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
	if f := p.folded.fix; f != nil && f.id == open.ID {
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

// fixPass runs p's fix round: a linked task ended unmerged gets the lead's line once; every pending task merged, the
// verify runs (verifyText, briefed as obor review verify briefs) unless the round's review is closed, a publish runs
// or a verify is pending (the next pass tries again). A failed verify gets the lead's line once.
func (a *App) fixPass(ctx context.Context, p *proj, fx *Loop) {
	a.mu.Lock()
	f := p.folded.fix
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
		return // the round's review is closed: nothing to verify
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
	a.mu.Lock()
	busy := p.publishing() || a.verifyPendingLocked(p, s.Agent)
	if !busy {
		p.admits++ // admitted (roleworktree.go): no publish starts until the brief is recorded
	}
	a.mu.Unlock()
	if busy {
		return
	}
	defer a.unadmit(p)
	vctx, cancel := context.WithTimeout(ctx, verifyGitWait)
	defer cancel()
	_, err = a.briefVerify(vctx, p, fx.Review, s, pending, nil, note, "obor", "")
	if err == nil || cutByServe(ctx, err) {
		return
	}
	a.logf("review fix of %s: %v", p.name, err)
	m := event.Message{To: p.out.lead(), From: "obor", FixFail: seq,
		Line: "[obor] " + id + " fix verify of " + strings.Join(pending, ",") + " failed: " + err.Error() +
			" (obor review verify by hand)"}
	a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m)
}
