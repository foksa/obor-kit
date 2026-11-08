// reviewfold.go: the reviews and the merges as the log holds them (T-1023; docs/plan/architecture-target.md §3 "Lane
// free and review coverage come from the workflow", §6 "Review"). What a review covered, the "review due" count, a
// review's and a verify's task lists and the promotion's unreviewed tasks are recomputed from this fold on every pass:
// no git read decides them. The merges are the merge kernel's facts (merged{head}); a merge outside the app (by hand
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
// tasks its start named; fixes, the tasks its finished verifies covered (T-924); reviewed, the head the reviewer last
// finished (the start's, then each finished verify's: T-857); closed, its review.closed fact's seq (its state; 0:
// open) and subject, its close commit's subject as the close's reply names it ("": open, or a close recorded without
// one); closing, the snapshot its close recorded before the commit (fact review.closing, T-1032; nil: none). A closed
// review is frozen at its close's snapshot: a verify ending after it changes nothing.
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

// closeSnap is a close's snapshot: the reviewed head its commit names and the tasks it covers (T-1032).
type closeSnap struct {
	head  string
	tasks []string
}

// freeze sets rv's reviewed head and coverage to s, the close's: its fixes are s's tasks beyond its start's.
func (rv *review) freeze(s closeSnap) {
	rv.reviewed, rv.fixes = s.head, nil
	for _, t := range s.tasks {
		if !slices.Contains(rv.covered, t) && !slices.Contains(rv.fixes, t) {
			rv.fixes = append(rv.fixes, t)
		}
	}
}

// reviewLog is the reviews' and the merges' records folded (T-856, T-924, T-1023): every review the log started, in
// order; every merge, in order; heads, each head the log names (a merge's, a review's start or verify, a promotion's
// run or fast-forward) and the seq of the first record naming it; per agent its open verify brief, and the last verify
// brief since the last start that ended unfinished.
//
// A head's position is how the fold tells the merges at or before it: those of seq <= its record's. A merge's head is
// exact (its own fact). A head a later record names (a review's start, a verify's brief) is read from git before its
// record is written, so a merge recorded in between counts as at or before it although it is after: such a merge is
// covered by no review yet, and so is in the next review's scope, and it holds the promotion's R back until a review
// covers it (the safe side of both).
type reviewLog struct {
	reviews []review
	merges  []mergeRec
	heads   map[string]int64
	open    map[string]*verifyBrief
	failed  *verifyBrief
}

// fold folds r: a merge; a promotion's head; a review start (a new review); its close's snapshot (review.closing,
// T-1032); its close (closes the newest review when it names it or a later one, the first close counting, and freezes
// it at the close's snapshot); a brief (a verify's, when it names tasks and a head; any brief
// replaces the agent's open verify brief); the finished line that ends a brief; a stop that ends one. Records of an
// older obor (review.fix, review.verify.request, a brief's verify_of) fold as nothing (T-924: a closed cycle needs
// none of them).
func (l *reviewLog) fold(r event.Record) {
	if l.open == nil {
		l.open, l.heads = map[string]*verifyBrief{}, map[string]int64{}
	}
	if m, ok := event.MergeOf(r); ok {
		l.merges = append(l.merges, mergeRec{task: m.Task, sha: m.SHA, seq: r.Seq})
		l.head(m.SHA, r.Seq)
		return
	}
	if r.Task == event.PromoteTask {
		var d struct {
			SHA string `json:"sha"`
		}
		if (r.Kind == event.KindIntent && strings.HasPrefix(r.Step, event.StepPromote+".") ||
			r.Kind == event.KindFact && r.Step == event.StepPromoted) && json.Unmarshal(r.Data, &d) == nil {
			l.head(d.SHA, r.Seq)
		}
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
		l.head(d.Head, r.Seq)
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
			switch { // its snapshot: the fact's, else its review.closing's; neither (an older obor): as folded so far
			case d.Head != "" && d.ID == cur.id:
				cur.freeze(closeSnap{head: d.Head, tasks: d.Tasks})
			case cur.closing != nil:
				cur.freeze(*cur.closing)
			}
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
			l.head(m.Reviewed, r.Seq)
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

// head records sha's position at seq, unless an earlier record named it.
func (l *reviewLog) head(sha string, seq int64) {
	if _, ok := l.heads[sha]; sha != "" && !ok {
		l.heads[sha] = seq
	}
}

// ended folds the end of agent's open verify brief, finished (why "": its tasks covered, its head reviewed, unless
// the review is closed: T-1032, its close's snapshot stands) or not (why: nothing of it counted, T-857).
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

// closedHead is the last closed review's reviewed head R, its start's or its last finished verify's (full sha; the
// promotion's one candidate, T-978); "": none closed in the log.
func (l *reviewLog) closedHead() string {
	for i := len(l.reviews) - 1; i >= 0; i-- {
		if l.reviews[i].closed != 0 {
			return l.reviews[i].reviewed
		}
	}
	return ""
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

// nextID is the next review's id: one past the newest review the log started (RV-1: none).
func (l *reviewLog) nextID() string {
	n, _ := reviewNumber(l.cur().id)
	return reviewID(n + 1)
}

// covered is the tasks the reviews since base covered (their starts' and finished verifies'); closedOnly: the closed
// reviews' only.
func (l *reviewLog) covered(closedOnly bool) []string {
	var out []string
	for _, rv := range l.reviews {
		if rv.seq >= l.base() && (!closedOnly || rv.closed != 0) {
			out = append(out, rv.covered...)
			out = append(out, rv.fixes...)
		}
	}
	return out
}

// pos is the seq bound of the merges at or before sha: the first record naming it; ok false: the log names no such
// head.
func (l *reviewLog) pos(sha string) (int64, bool) {
	seq, ok := l.heads[sha]
	return seq, ok && sha != ""
}

// between is the merges after from and at or before to, oldest first: from "" starts at base (the review scope's and
// a verify's merge); a head the log does not name (a promoted branch older than the log's merges, T-1032) at the
// log's first merge; to "" or unknown, every merge since.
func (l *reviewLog) between(from, to string) []mergeRec {
	lo, ok := l.pos(from)
	switch {
	case from == "":
		lo = l.base()
	case !ok:
		lo = 0
	}
	hi, bounded := l.pos(to)
	var out []mergeRec
	for _, m := range l.merges {
		if m.seq > lo && (!bounded || m.seq <= hi) {
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
	return uncovered(l.between("", ""), l.covered(false))
}

// mergeOf is task's last merge since base ({}: none): a verify's fix, merged during the review.
func (l *reviewLog) mergeOf(task string) mergeRec {
	var out mergeRec
	for _, m := range l.between("", "") {
		if m.task == task {
			out = m
		}
	}
	return out
}

// unreviewed is the tasks merged after at (the promoted branch's head) and at or before r (the last closed review's
// reviewed head) that no closed review since base covered, oldest first, each once (T-985: one merged during the
// review, inside its verify's head); at a head the log does not name (T-1032): every merge up to r, less those any
// closed review covered. rv is the last closed review's id, the review they merged during ("the last review": none
// closed).
func (l *reviewLog) unreviewed(at, r string) (tasks []string, rv string) {
	lo, known := l.pos(at)
	covered := l.covered(true)
	switch {
	case at == "" || known && lo < l.base():
		lo = l.base()
	case !known:
		lo, covered = 0, nil
		for _, c := range l.reviews {
			if c.closed != 0 {
				covered = slices.Concat(covered, c.covered, c.fixes)
			}
		}
	}
	hi, ok := l.pos(r)
	if !ok {
		return nil, ""
	}
	var in []mergeRec
	for _, m := range l.merges {
		if m.seq > lo && m.seq <= hi {
			in = append(in, m)
		}
	}
	for _, m := range uncovered(in, covered) {
		tasks = append(tasks, m.task)
	}
	rv = "the last review"
	for i := len(l.reviews) - 1; i >= 0; i-- {
		if l.reviews[i].closed != 0 {
			rv = l.reviews[i].id
			break
		}
	}
	return tasks, rv
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
