// reviewonmerge.go: the Review: on merge rule (T-878, T-932): a hard or risky task's spec asks serve for a review as
// soon as it merges. It replaces the lead's per-task watch and hand-run obor review for these tasks
// (skills/lead/SKILL.md).
package app

import (
	"cmp"
	"context"
	"errors"
	"obor/internal/app/event"
	"slices"
	"time"
)

// ruleReviewOnMerge is the rule's name in the rule pass (rules.go).
const ruleReviewOnMerge = "review.on.merge"

// reviewOnMergeRule requests a review for the newest task merged after the last review start whose current spec asks
// for one, while no review request is open (T-878, T-932): the line `Review: on merge` or `Difficulty: hard`, and no
// `Fixes:` line (a fix is covered by its review's verify). Fact review.request{note: "Review: on merge (T-xx)", by:
// obor}, no --after. Key: that merge's seq. Done when a request is open (the lead's covers it too: a
// review's scope is every merge since the previous one) or a review started after the merge. A request recorded while
// a review cycle is open waits for its close in reviewPass (review.go); the call path's "RV-n is open: close it first"
// is the lead's refusal, which this rule does not go through. After a crash the request may be recorded twice: the
// second replaces the first, harmless. A request that ended after the merge (the lead's --rm, or a fire, reviewEnd)
// also holds it: the lead can cancel the rule's request, and a failed start is not retried every pass. A request that
// ended because a task of its --after was dropped or rejected does not hold it (T-906): the merges it waited on still
// need their review.
func reviewOnMergeRule(_ context.Context, a *App, p *proj, _ *Loop, _ time.Time) (int64, action, bool) {
	a.mu.Lock()
	last, open, ended := p.folded.rv.last().seq, p.folded.review != nil, p.folded.reviewEnd.seq
	var merges []mergedTask
	if !open {
		for _, task := range p.loopTasks() {
			if mt := p.folded.loop[task]; mt.state == stMerged && mt.mergedSeq > last && mt.mergedSeq > ended {
				merges = append(merges, mergedTask{task, mt.mergedSeq})
			}
		}
	}
	a.mu.Unlock()
	if open {
		return 0, action{}, false
	}
	slices.SortFunc(merges, func(x, y mergedTask) int { return cmp.Compare(y.seq, x.seq) })
	for _, m := range merges {
		row, ok, err := specOf(p, m.task)
		if err != nil {
			a.logf("%s: review on merge %s: spec: %v", p.name, m.task, err)
			return 0, action{}, false
		}
		if !ok || !row.Spec.OnMerge {
			continue
		}
		return m.seq, reviewOnMergeAction(a, p, m.task), true
	}
	return 0, action{}, false
}

// mergedTask is a merge-loop task and the seq of the fact that merged it.
type mergedTask struct {
	task string
	seq  int64
}

// reviewOnMergeAction records the rule's review request for task's merge: no --after, the note naming the task, obor
// as the asker.
func reviewOnMergeAction(a *App, p *proj, task string) action {
	return action{call: func(context.Context) error {
		d := ReviewData{Note: "Review: on merge (" + task + ")", By: "obor"}
		if !a.record(p, event.Record{Task: event.ReviewTask, Step: event.StepReviewRequest, Kind: event.KindFact}, d) {
			return errors.New("the event log refused the review request")
		}
		a.wake()
		return nil
	}}
}
