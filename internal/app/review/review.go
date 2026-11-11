// Package review is one project's review cycle (app's proj.folded.rv), its own package: the reviews the log started
// (each one's start, finished verifies, close snapshot and close), the merges the merge kernel recorded, the open
// verify brief per agent, and the fold of the records that change them (Fold). What a review covered and what the
// next one's scope is are decided here only, from the log (no git read): Cycle is the newest review's coverage, Next
// the next review's id and scope, and every reader in package app (obor status, the review brief, the close's
// snapshot and its crash read, the "review due" count, the verify) takes one of the two. Its fields are unexported,
// so only this package names them: package app reads them through the queries (Last, Cycle, Next, Prev, Closing,
// Failed, MergeOf, BriefFile) and keeps the effects (the review's start and brief, the verify's brief, the close's
// commit, the reviewer's stop). Fold returns a review fact's data: the review requests and a call's stored reply are
// package app's state, folded there in its fold order.
//
// A merge outside the app (by hand in git, merged{outside}) has no head and is no merge here: no review counts it.
//
// No lock: a State is part of package app's per-project state and is used under App.mu like the rest of it.
// It imports only the leaves event and outbox (a stop's data), never package app.
package review

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/outbox"
)

// Data is a review fact's data: the request's tasks and note, who asked, the request a firing or start is of,
// the review's id, head and tasks once started (a close's covered tasks and start are the fold's cycle: its
// commit renders them), and the call that recorded it with its reply (a close's: CLOSED <its commit's subject>, also
// when the crash cut's read records it).
type Data struct {
	After   []string `json:"after,omitempty"`
	Note    string   `json:"note,omitempty"`
	By      string   `json:"by,omitempty"`
	Request int64    `json:"request,omitempty"`
	ID      string   `json:"id,omitempty"`
	Head    string   `json:"head,omitempty"`
	Tasks   []string `json:"tasks,omitempty"`
	Call    string   `json:"call,omitempty"`
	Reply   string   `json:"reply,omitempty"`
}

// ClosedReply starts a close's reply, CLOSED <the close commit's subject>: the fold reads the subject back from it.
const ClosedReply = "CLOSED "

// ID is review n's id: "RV-<n>".
func ID(n int) string { return "RV-" + strconv.Itoa(n) }

// Number is id's number (ok false: id is not "RV-<n>").
func Number(id string) (n int, ok bool) {
	digits, found := strings.CutPrefix(id, "RV-")
	if !found || digits == "" {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(digits)
	return n, err == nil
}

// Merge is one merge the merge kernel recorded: its task, the target's head after it and its fact's seq.
type Merge struct {
	task, sha string
	seq       int64
}

// Task is the merged task.
func (m Merge) Task() string { return m.task }

// SHA is the target's head after the merge ("": the zero Merge, no merge).
func (m Merge) SHA() string { return m.sha }

// Verify is a verify's brief: its fact's seq, the tasks it verifies and the head it hands the reviewer; why it
// ended unfinished ("": it did not).
type Verify struct {
	seq   int64
	tasks []string
	head  string
	why   string
}

// Seq is the seq of the verify brief's fact.
func (v *Verify) Seq() int64 { return v.seq }

// Tasks is the tasks the brief verifies.
func (v *Verify) Tasks() []string { return v.tasks }

// Why is why the verify ended unfinished ("": it did not).
func (v *Verify) Why() string { return v.why }

// review is one review the log started, one value: its start's seq, time, id and head; brief, the seq of the brief
// serve sent at its start (0: none folded); covered, the in-scope tasks its start named; fixes, the tasks its finished
// verifies covered; reviewed, the head the reviewer last finished (the start's, then each finished verify's; an open
// review's only); closed, its review.closed fact's seq (its state; 0: open) and subject, its close commit's subject as
// the close's reply names it ("": open, or a close recorded without one); closing, the snapshot its close recorded
// before the commit (fact review.closing; nil: none, and once closed). A closed review is frozen at its close's
// snapshot: a verify ending after it changes nothing.
type review struct {
	seq      int64
	at       time.Time
	id, head string
	brief    int64
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
// set: only an open review's is read (Cycle), and a close ends that.
func (rv *review) freeze(s closeSnap) {
	rv.fixes = nil
	for _, t := range s.tasks {
		if !slices.Contains(rv.covered, t) && !slices.Contains(rv.fixes, t) {
			rv.fixes = append(rv.fixes, t)
		}
	}
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

// State is the reviews' and the merges' records folded: every review the log started, in
// order; every merge, in order; per agent its open verify brief and its newest brief's seq, and when the newest
// review's own brief or its verify last ended (its finished line, or a stop that ended it); and the last verify brief
// since the last start that ended unfinished. The zero State is an empty one.
type State struct {
	reviews []review
	merges  []Merge
	open    map[string]*Verify
	briefs  map[string]int64
	endAt   map[string]time.Time
	failed  *Verify
}

// Fold folds r: a merge; a review start (a new review, and the brief that follows it is its own); its close's
// snapshot (review.closing); its close (closes the newest review when it names it or a later one, the first close
// counting, and freezes it at the close's snapshot); a brief (a verify's, when it names tasks and a head; any brief
// replaces the agent's open verify brief); the finished line that ends a brief; a stop that ends one. Any other record
// of a review or a line folds as nothing here (a closed cycle needs none of them). For a fact of the review key it
// returns the fact's data (ok): a request, its end, a firing and a call's stored reply are package app's to fold.
func (s *State) Fold(r event.Record) (d Data, ok bool) {
	if s.open == nil {
		s.open, s.briefs, s.endAt = map[string]*Verify{}, map[string]int64{}, map[string]time.Time{}
	}
	if m, merged := event.MergeOf(r); merged {
		s.merges = append(s.merges, Merge{task: m.Task, sha: m.SHA, seq: r.Seq})
		return Data{}, false
	}
	if r.Kind != event.KindFact || r.Task != event.ReviewTask && r.Task != event.MsgTask {
		return Data{}, false
	}
	if r.Task == event.ReviewTask {
		err := json.Unmarshal(r.Data, &d)
		s.foldReview(r, d, err == nil)
		return d, true
	}
	switch r.Step {
	case event.StepBriefed:
		var m event.Message
		if json.Unmarshal(r.Data, &m) != nil {
			return Data{}, false
		}
		s.ended(m.To, "a newer brief replaced it")
		s.briefs[m.To] = r.Seq
		if m.Task != "" && m.Reviewed != "" {
			s.open[m.To] = &Verify{seq: r.Seq, tasks: strings.Split(m.Task, ","), head: m.Reviewed}
			break
		}
		// Serve records the review's own brief right after its start, without a line (its fresh start carried it):
		// the first such brief of an open review that has none yet is that review's.
		if cur := s.cur(); m.Line == "" && cur.seq != 0 && cur.closed == 0 && cur.brief == 0 {
			cur.brief = r.Seq
		}
	case event.StepMessage:
		var m event.Message
		if json.Unmarshal(r.Data, &m) == nil && m.Ends != "" { // the finished line
			own := s.own(m.Ends)
			s.ended(m.Ends, "")
			if own {
				s.endAt[m.Ends] = event.RecordTime(r)
			}
		}
	case event.StepStopped:
		var st outbox.Stopped
		if json.Unmarshal(r.Data, &st) == nil && st.Ends != "" { // an error stop: no finished line follows
			own := s.own(st.Ends)
			s.ended(st.Ends, "the reviewer stopped: "+st.Error)
			if own {
				s.endAt[st.Ends] = event.RecordTime(r)
			}
		}
	}
	return Data{}, false
}

// own is whether agent's newest brief belongs to the open review: it is that review's own start brief, or its open
// verify's.
func (s *State) own(agent string) bool {
	cur := s.cur()
	return cur.seq != 0 && cur.closed == 0 && (s.open[agent] != nil || s.briefs[agent] != 0 && s.briefs[agent] == cur.brief)
}

// foldReview folds the review key's fact r, its data d (read: it decoded whole; a start and a snapshot that did not
// fold as nothing, a close folds with what decoded).
func (s *State) foldReview(r event.Record, d Data, read bool) {
	switch r.Step {
	case event.StepReviewStarted:
		if !read {
			return
		}
		s.reviews = append(s.reviews, review{seq: r.Seq, at: event.RecordTime(r), id: d.ID, head: d.Head, covered: slices.Clone(d.Tasks),
			reviewed: d.Head})
		s.failed = nil
	case event.StepReviewClosing:
		if !read || d.Head == "" {
			return
		}
		if cur := s.cur(); cur.seq != 0 && cur.closed == 0 && cur.id == d.ID {
			cur.closing = &closeSnap{head: d.Head, tasks: slices.Clone(d.Tasks)}
		}
	case event.StepReviewClosed:
		cur := s.cur()
		n, isID := Number(d.ID)
		if open, curID := Number(cur.id); cur.seq != 0 && cur.closed == 0 && isID && curID && n >= open {
			cur.closed, cur.subject = r.Seq, strings.TrimPrefix(d.Reply, ClosedReply)
			switch { // its snapshot: the fact's, else its review.closing's; neither: as folded so far
			case d.Head != "" && d.ID == cur.id:
				cur.freeze(closeSnap{head: d.Head, tasks: d.Tasks})
			case cur.closing != nil:
				cur.freeze(*cur.closing)
			}
			cur.closing = nil // recovery reads it only while the review is open
		}
	}
}

// ended folds the end of agent's open verify brief, finished (why "": its tasks covered, its head reviewed, unless
// the review is closed: its close's snapshot stands) or not (why: nothing of it counted).
func (s *State) ended(agent, why string) {
	v := s.open[agent]
	delete(s.open, agent)
	if v == nil {
		return
	}
	if why != "" {
		v.why, s.failed = why, v
		return
	}
	if cur := s.cur(); cur.seq != 0 && cur.closed == 0 && v.seq > cur.seq {
		cur.fixes = append(cur.fixes, v.tasks...)
		cur.reviewed = v.head
	}
}

// cur is the newest review the log started (a zero review, not in the list: none).
func (s *State) cur() *review {
	if len(s.reviews) == 0 {
		return &review{}
	}
	return &s.reviews[len(s.reviews)-1]
}

// prev is the review before the newest one (a zero review: none).
func (s *State) prev() review {
	if len(s.reviews) < 2 {
		return review{}
	}
	return s.reviews[len(s.reviews)-2]
}

// base is the seq the next review's scope starts after: the start of the review before the newest (0: fewer than two
// reviews, every merge). A merge before it was in the newest review's scope or an earlier one's.
func (s *State) base() int64 { return s.prev().seq }

// covered is the tasks the reviews since base covered (their starts' and finished verifies').
func (s *State) covered() []string {
	var out []string
	for _, rv := range s.reviews {
		if rv.seq >= s.base() {
			out = append(out, rv.covered...)
			out = append(out, rv.fixes...)
		}
	}
	return out
}

// sinceBase is the merges after base, oldest first (the review scope's and a verify's merges).
func (s *State) sinceBase() []Merge {
	var out []Merge
	for _, m := range s.merges {
		if m.seq > s.base() {
			out = append(out, m)
		}
	}
	return out
}

// uncovered is merges whose task none of covered names, each task once (its last merge), oldest first.
func uncovered(merges []Merge, covered []string) []Merge {
	var out []Merge
	for _, m := range merges {
		if slices.Contains(covered, m.task) {
			continue
		}
		out = slices.DeleteFunc(out, func(o Merge) bool { return o.task == m.task })
		out = append(out, m)
	}
	return out
}

// Last is the newest review the log started: its fact's seq, its id and the head it reviews (Seq 0: none), and
// the seq of the review.closed fact that closed it (0: open). Two of them are equal while no start or close folded
// between.
type Last struct {
	Seq      int64
	ID, Head string
	Closed   int64
}

// Open is the review's id while it is open: started and no review.closed fact for it ("": none open).
func (l Last) Open() string {
	if l.Seq == 0 || l.Closed != 0 {
		return ""
	}
	return l.ID
}

// Last is the newest review the log started (Seq 0: none).
func (s *State) Last() Last {
	c := s.cur()
	return Last{Seq: c.seq, ID: c.id, Head: c.head, Closed: c.closed}
}

// Prev is the id of the review before the newest one ("": none).
func (s *State) Prev() string { return s.prev().id }

// Cycle is the newest review's cycle, what it covered: the review (Head, the head it started at; At, its start's
// time); Covered, its
// in-scope tasks plus each finished verify's, once each (a closed review's: its close's snapshot); Reviewed, the head
// the reviewer last finished while it is open (its start's, then each finished verify's, never the head of a verify
// that did not finish; "": closed, or none started).
type Cycle struct {
	Last
	At       time.Time
	Reviewed string
	Covered  []string
}

// Cycle is the newest review's cycle (the zero Cycle: none started).
func (s *State) Cycle() Cycle {
	cur := s.cur()
	c := Cycle{Last: s.Last(), At: cur.at, Covered: cur.cycle()}
	if c.Open() != "" {
		c.Reviewed = cur.reviewed
	}
	return c
}

// Pending is the links of the cycle neither ended unmerged nor merged and covered: what obor review close waits for.
func (c Cycle) Pending(links []Link) []string {
	var out []string
	for _, f := range links {
		if f.End == "" && (f.Merged == 0 || !slices.Contains(c.Covered, f.Task)) {
			out = append(out, f.Task)
		}
	}
	return out
}

// Closing is the snapshot the open review's close recorded before its commit (fact review.closing): the reviewed head
// and the tasks the commit names (ok false: none, or the review is closed).
func (s *State) Closing() (head string, tasks []string, ok bool) {
	c := s.cur().closing
	if c == nil {
		return "", nil, false
	}
	return c.head, c.tasks, true
}

// Next is the next review as the log decides it: its id, one past the newest review the log started (RV-1 when it
// started none); After, that newest review (the brief's range starts at its head; Seq 0: none) and Subject, its close
// commit's subject ("": open, or closed without one); Scope, the merges since the start of the review before After
// that no review since covered (After included, open or closed), each task once at its last merge, oldest first: the
// brief's in-scope tasks and the "review due" count.
type Next struct {
	ID      string
	After   Last
	Subject string
	Scope   []Merge
}

// Next is the next review.
func (s *State) Next() Next {
	n, _ := Number(s.cur().id)
	return Next{ID: ID(n + 1), After: s.Last(), Subject: s.cur().subject, Scope: uncovered(s.sinceBase(), s.covered())}
}

// MergeOf is task's last merge in the range a verify reads, since the start of the review before the newest (the
// zero Merge: none): a verify's fix, merged during the review.
func (s *State) MergeOf(task string) Merge {
	var out Merge
	for _, m := range s.sinceBase() {
		if m.task == task {
			out = m
		}
	}
	return out
}

// Ended is when agent's last brief that belongs to a review ended: the review's own start brief, or a verify's, by its
// finished line or the stop that ended it (zero: none folded). A brief that belongs to no review, or a newer brief
// replacing an open one, is no end here.
func (s *State) Ended(agent string) time.Time { return s.endAt[agent] }

// Verifying: agent has an open verify brief.
func (s *State) Verifying(agent string) bool { return s.open[agent] != nil }

// Failed is the last verify brief since the newest review's start that ended unfinished (nil: none).
func (s *State) Failed() *Verify { return s.failed }

// BriefFile is the review file the brief (its fact's seq) of agent asked for, as "RV-<n>" or "RV-<n>-verify"
// ("": none known): a verify's brief names the newest review's verify file, the brief the newest review started
// with (the one serve sent at its start) the review file.
func (s *State) BriefFile(agent string, seq int64) string {
	cur := s.cur()
	switch {
	case s.open[agent] != nil && cur.id != "":
		return cur.id + "-verify"
	case cur.id != "" && seq != 0 && seq == cur.brief:
		return cur.id
	}
	return ""
}

// Link is one task linked to a review by its spec's Fixes: line: the task, its note (the line's free text), the
// seq of its newest dispatch, of its merge (0: not merged), and how it ended unmerged (dropped, rejected; "": it did
// not). Package app derives the links (the merge loop's tasks, their stored specs); none is folded.
type Link struct {
	Task, Note string
	Dispatched int64
	Merged     int64
	End        string
}

// Round is a review's fix round as the review card shows it: the review, its links and the tasks its cycle covered.
type Round struct {
	ID      string
	Links   []Link
	Covered []string
}

// State is link l's state in the round: how it ended unmerged (dropped, rejected), open (not merged), verified (merged
// and the cycle covers it) or merged.
func (f Round) State(l Link) string {
	switch {
	case l.End != "":
		return l.End
	case l.Merged == 0:
		return "open"
	case slices.Contains(f.Covered, l.Task):
		return "verified"
	}
	return "merged"
}
