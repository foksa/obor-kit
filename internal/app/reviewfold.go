// reviewfold.go: the reviews and the merges as the log holds them (docs/plan/architecture-target.md §3 "Lane
// free and review coverage come from the workflow", §6 "Review"). What a review covered, the "review due" count, a
// review's and a verify's task lists are recomputed from this fold on every pass: no git read decides them. The merges are the merge kernel's facts (merged{head}); a merge outside the app (by hand
// in git, merged{outside}) has no head and is not one: a by-hand twin (design rule 4), no review counts it.
package app

import (
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/outbox"
	"slices"
	"strings"
)

// mergeRec is one merge the merge kernel recorded: its task, the target's head after it and its fact's seq.
type mergeRec struct {
	task, sha string
	seq       int64
}

// review is one review the log started, one value (plan §6): its start's seq, id and head; covered, the in-scope
// tasks its start named; fixes, the tasks its finished verifies covered; reviewed, the head the reviewer last
// finished (the start's, then each finished verify's; an open review's only); closed, its review.closed
// fact's seq (its state; 0: open) and subject, its close commit's subject as the close's reply names it ("": open, or
// a close recorded without one); closing, the snapshot its close recorded before the commit (fact review.closing,
// the close's snapshot; nil: none, and once closed). A closed review is frozen at its close's snapshot: a verify ending after it
// changes nothing.
type review struct {
	seq      int64
	id, head string
	covered  []string
	fixes    []string
	reviewed string
	closed   int64
	subject  string
	closing  *closeSnap
}

// closeSnap is a close's snapshot: the reviewed head its commit names and the tasks it covers.
type closeSnap struct {
	head  string
	tasks []string
}

// freeze sets rv's coverage to s, the close's: its fixes are s's tasks beyond its start's. Its reviewed head is not
// set: only an open review's is read (reviewclose.go's reviewCycle), and a close ends that.
func (rv *review) freeze(s closeSnap) {
	rv.fixes = nil
	for _, t := range s.tasks {
		if !slices.Contains(rv.covered, t) && !slices.Contains(rv.fixes, t) {
			rv.fixes = append(rv.fixes, t)
		}
	}
}

// reviewLog is the reviews' and the merges' records folded: every review the log started, in
// order; every merge, in order; per agent its open verify brief, and the last verify brief since the last start that
// ended unfinished.
type reviewLog struct {
	reviews []review
	merges  []mergeRec
	open    map[string]*verifyBrief
	failed  *verifyBrief
}

// fold folds r: a merge; a review start (a new review); its close's snapshot (review.closing,
// the close's snapshot); its close (closes the newest review when it names it or a later one, the first close counting, and freezes
// it at the close's snapshot); a brief (a verify's, when it names tasks and a head; any brief
// replaces the agent's open verify brief); the finished line that ends a brief; a stop that ends one. Any other
// record of a review or a line folds as nothing (a closed cycle needs none of them).
func (l *reviewLog) fold(r event.Record) {
	if l.open == nil {
		l.open = map[string]*verifyBrief{}
	}
	if m, ok := event.MergeOf(r); ok {
		l.merges = append(l.merges, mergeRec{task: m.Task, sha: m.SHA, seq: r.Seq})
		return
	}
	if r.Kind != event.KindFact || r.Task != event.ReviewTask && r.Task != event.MsgTask {
		return
	}
	switch {
	case r.Task == event.ReviewTask && r.Step == event.StepReviewStarted:
		var d ReviewData
		if json.Unmarshal(r.Data, &d) != nil {
			return
		}
		l.reviews = append(l.reviews, review{seq: r.Seq, id: d.ID, head: d.Head, covered: slices.Clone(d.Tasks),
			reviewed: d.Head})
		l.failed = nil
	case r.Task == event.ReviewTask && r.Step == event.StepReviewClosing:
		var d ReviewData
		if json.Unmarshal(r.Data, &d) != nil || d.Head == "" {
			return
		}
		if cur := l.cur(); cur.seq != 0 && cur.closed == 0 && cur.id == d.ID {
			cur.closing = &closeSnap{head: d.Head, tasks: slices.Clone(d.Tasks)}
		}
	case r.Task == event.ReviewTask && r.Step == event.StepReviewClosed:
		var d ReviewData
		_ = json.Unmarshal(r.Data, &d)
		cur := l.cur()
		n, ok := reviewNumber(d.ID)
		if open, isID := reviewNumber(cur.id); cur.seq != 0 && cur.closed == 0 && ok && isID && n >= open {
			cur.closed, cur.subject = r.Seq, strings.TrimPrefix(d.Reply, closedReply)
			switch { // its snapshot: the fact's, else its review.closing's; neither: as folded so far
			case d.Head != "" && d.ID == cur.id:
				cur.freeze(closeSnap{head: d.Head, tasks: d.Tasks})
			case cur.closing != nil:
				cur.freeze(*cur.closing)
			}
			cur.closing = nil // recovery reads it only while the review is open
		}
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
		var d outbox.Stopped
		if json.Unmarshal(r.Data, &d) == nil && d.Ends != "" { // an error stop: no finished line follows
			l.ended(d.Ends, "the reviewer stopped: "+d.Error)
		}
	}
}

// ended folds the end of agent's open verify brief, finished (why "": its tasks covered, its head reviewed, unless
// the review is closed: its close's snapshot stands) or not (why: nothing of it counted).
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
	if cur := l.cur(); cur.seq != 0 && cur.closed == 0 && v.seq > cur.seq {
		cur.fixes = append(cur.fixes, v.tasks...)
		cur.reviewed = v.head
	}
}

// cur is the newest review the log started (a zero review, not in the list: none).
func (l *reviewLog) cur() *review {
	if len(l.reviews) == 0 {
		return &review{}
	}
	return &l.reviews[len(l.reviews)-1]
}

// last is the newest review the log started, as its summary (seq 0: none).
func (l *reviewLog) last() lastReview {
	c := l.cur()
	return lastReview{seq: c.seq, id: c.id, head: c.head, closed: c.closed}
}

// prev is the review before the newest one (a zero review: none).
func (l *reviewLog) prev() review {
	if len(l.reviews) < 2 {
		return review{}
	}
	return l.reviews[len(l.reviews)-2]
}

// base is the seq the next review's scope starts after: the start of the review before the newest (0: fewer than two
// reviews, every merge). A merge before it was in the newest review's scope or an earlier one's.
func (l *reviewLog) base() int64 { return l.prev().seq }

// nextID is the next review's id: one past the newest review the log started (RV-1 when it started none).
func (l *reviewLog) nextID() string {
	n, _ := reviewNumber(l.cur().id)
	return reviewID(n + 1)
}

// covered is the tasks the reviews since base covered (their starts' and finished verifies').
func (l *reviewLog) covered() []string {
	var out []string
	for _, rv := range l.reviews {
		if rv.seq >= l.base() {
			out = append(out, rv.covered...)
			out = append(out, rv.fixes...)
		}
	}
	return out
}

// sinceBase is the merges after base, oldest first (the review scope's and a verify's merges).
func (l *reviewLog) sinceBase() []mergeRec {
	var out []mergeRec
	for _, m := range l.merges {
		if m.seq > l.base() {
			out = append(out, m)
		}
	}
	return out
}

// uncovered is merges whose task none of covered names, each task once (its last merge), oldest first.
func uncovered(merges []mergeRec, covered []string) []mergeRec {
	var out []mergeRec
	for _, m := range merges {
		if slices.Contains(covered, m.task) {
			continue
		}
		out = slices.DeleteFunc(out, func(o mergeRec) bool { return o.task == m.task })
		out = append(out, m)
	}
	return out
}

// scope is the next review's merges: those since base that no review since base covered (the newest included, open
// or closed), each task once, oldest first. The review due count, the brief's in-scope tasks.
func (l *reviewLog) scope() []mergeRec {
	return uncovered(l.sinceBase(), l.covered())
}

// mergeOf is task's last merge since base ({}: none): a verify's fix, merged during the review.
func (l *reviewLog) mergeOf(task string) mergeRec {
	var out mergeRec
	for _, m := range l.sinceBase() {
		if m.task == task {
			out = m
		}
	}
	return out
}

// cycle is the tasks the review's cycle covered so far: its in-scope tasks, then each finished verify's, each once.
func (rv *review) cycle() []string {
	var out []string
	for _, t := range slices.Concat(rv.covered, rv.fixes) {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}
