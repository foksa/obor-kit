package review

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/outbox"
)

// What a review covered is the log's: tables over the fold (records → the queries package app reads).

// foldLog builds records in seq order for a State.
type foldLog struct{ recs []event.Record }

func (f *foldLog) add(task, step, kind string, data any) *foldLog {
	raw, _ := json.Marshal(data)
	f.recs = append(f.recs, event.Record{Seq: int64(len(f.recs) + 1), Task: task, Step: step, Kind: kind, Data: raw})
	return f
}

func (f *foldLog) merged(task, sha string) *foldLog {
	return f.add(task, event.StepMerged, event.KindFact, event.MergedData{Head: sha})
}

func (f *foldLog) outside(task string) *foldLog {
	return f.add(task, event.StepMerged, event.KindFact, event.MergedData{Outside: true})
}

func (f *foldLog) started(id, head string, tasks ...string) *foldLog {
	return f.add(event.ReviewTask, event.StepReviewStarted, event.KindFact, Data{ID: id, Head: head, Tasks: tasks})
}

func (f *foldLog) closed(id string) *foldLog {
	return f.add(event.ReviewTask, event.StepReviewClosed, event.KindFact, Data{ID: id,
		Reply: ClosedReply + id + " closed · x · approved · s"})
}

// verify is a verify brief of tasks at head, then its end: finished (why "") or the reviewer's error stop.
func (f *foldLog) verify(head, why string, tasks ...string) *foldLog {
	f.briefed(head, tasks...)
	if why != "" {
		return f.add(event.MsgTask, event.StepStopped, event.KindFact, outbox.Stopped{Ends: "pj-rev", Error: why})
	}
	return f.ends()
}

// briefed is a verify brief of tasks at head, still open; ends is the finished line that ends it; brief is another
// brief to the reviewer (no tasks, no head: the lead's, or a review's fresh start).
func (f *foldLog) briefed(head string, tasks ...string) *foldLog {
	return f.add(event.MsgTask, event.StepBriefed, event.KindFact, event.Message{To: "pj-rev",
		Task: strings.Join(tasks, ","), Reviewed: head})
}

func (f *foldLog) ends() *foldLog {
	return f.add(event.MsgTask, event.StepMessage, event.KindFact, event.Message{To: "pj-lead", Ends: "pj-rev"})
}

func (f *foldLog) brief() *foldLog {
	return f.add(event.MsgTask, event.StepBriefed, event.KindFact, event.Message{To: "pj-rev", Line: "look at this"})
}

// closing is a close's snapshot before its commit; closedAt a close fact carrying it, as serve's close
// records it.
func (f *foldLog) closing(id, head string, tasks ...string) *foldLog {
	return f.add(event.ReviewTask, event.StepReviewClosing, event.KindFact, Data{ID: id, Head: head, Tasks: tasks})
}

func (f *foldLog) closedAt(id, head string, tasks ...string) *foldLog {
	return f.add(event.ReviewTask, event.StepReviewClosed, event.KindFact, Data{ID: id, Head: head, Tasks: tasks,
		Reply: ClosedReply + id + " closed · x · approved · s"})
}

// fold is the State f's records fold into.
func (f *foldLog) fold() *State {
	var s State
	for _, r := range f.recs {
		s.Fold(r)
	}
	return &s
}

func tasksOf(ms []Merge) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Task())
	}
	return out
}

// The next review's scope (the brief's in-scope tasks and the "review due" count), its id, and what the newest
// review covered.
func TestFoldScopeAndCoverage(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		log   *foldLog
		scope []string // the next review's in-scope tasks
		next  string   // the next review's id
		cycle []string // the newest review's covered tasks (nil: not checked)
	}{
		{name: "an empty log", log: &foldLog{}, next: "RV-1"},
		{name: "merges, no review yet: every merge, the first is RV-1",
			log:   (&foldLog{}).merged("T-1", "m1").merged("T-2", "m2").merged("T-3", "m3").merged("T-4", "m4"),
			scope: []string{"T-1", "T-2", "T-3", "T-4"}, next: "RV-1"},
		{name: "a merge outside the app (by hand in git) is no review's: a by-hand twin",
			log:   (&foldLog{}).merged("T-1", "m1").outside("T-2"),
			scope: []string{"T-1"}, next: "RV-1"},
		{name: "RV-4 closed covering T-1, T-2: T-3 merged since is the scope",
			log: (&foldLog{}).merged("T-1", "m1").merged("T-2", "m2").started("RV-4", "m2", "T-1", "T-2").
				merged("T-3", "m3").closed("RV-4"),
			scope: []string{"T-3"}, next: "RV-5"},
		{name: "a merge recorded between a start's head read and its fact: no review covered it, the next one's scope",
			log: (&foldLog{}).merged("T-1", "m1").started("RV-4", "m1", "T-1").closed("RV-4").
				merged("T-2", "m2").merged("T-3", "m3").started("RV-5", "m2", "T-2").closed("RV-5"),
			scope: []string{"T-3"}, next: "RV-6"},
		{name: "a re-merged task is in scope once, at its last merge",
			log:   (&foldLog{}).merged("T-1", "m1").merged("T-2", "m2").merged("T-1", "m3"),
			scope: []string{"T-2", "T-1"}, next: "RV-1"},
		{name: "RV-5 open, its fix verified: the fix covered, a merge during it not",
			log: (&foldLog{}).merged("T-1", "m1").started("RV-4", "m1", "T-1").closed("RV-4").
				merged("T-2", "m2").started("RV-5", "m2", "T-2").merged("T-fix", "m3").merged("T-x", "m4").
				verify("m3", "", "T-fix"),
			scope: []string{"T-x"}, next: "RV-6", cycle: []string{"T-2", "T-fix"}},
		{name: "RV-5 closed at its verify's head: T-x merged before the verify, not covered, is the next scope",
			log: (&foldLog{}).merged("T-1", "m1").started("RV-4", "m1", "T-1").closed("RV-4").
				merged("T-2", "m2").started("RV-5", "m2", "T-2").merged("T-x", "m3").merged("T-fix", "m4").
				verify("m4", "", "T-fix").closed("RV-5"),
			scope: []string{"T-x"}, next: "RV-6"},
		{name: "a merge after a closed review is in the next scope",
			log: (&foldLog{}).merged("T-1", "m1").started("RV-4", "m1", "T-1").closed("RV-4").
				merged("T-2", "m2").started("RV-5", "m2", "T-2").closed("RV-5").merged("T-3", "m3"),
			scope: []string{"T-3"}, next: "RV-6"},
		{name: "a verify that did not finish covers nothing",
			log: (&foldLog{}).merged("T-1", "m1").started("RV-4", "m1", "T-1").merged("T-fix", "m2").
				verify("m2", "herdr gone", "T-fix").closed("RV-4"),
			scope: []string{"T-fix"}, next: "RV-5", cycle: []string{"T-1"}},
		{name: "a verify a newer brief replaced covers nothing",
			log: (&foldLog{}).merged("T-1", "m1").started("RV-4", "m1", "T-1").merged("T-fix", "m2").
				briefed("m2", "T-fix").brief().ends(),
			scope: []string{"T-fix"}, next: "RV-5", cycle: []string{"T-1"}},
		{name: "a verify briefed before the review started is not its: nothing covered",
			log: (&foldLog{}).merged("T-fix", "m1").briefed("m1", "T-fix").merged("T-1", "m2").
				started("RV-4", "m2", "T-1").ends(),
			scope: []string{"T-fix"}, next: "RV-5", cycle: []string{"T-1"}},
		{name: "a close naming an earlier review closes nothing; one naming a later review closes the open one",
			log:   (&foldLog{}).started("RV-4", "h4").closed("RV-3").started("RV-5", "h5").closed("RV-6"),
			next:  "RV-6",
			scope: nil},
		// The close's snapshot stands; a verify finishing after it changes nothing.
		{name: "a verify finishing after the close: RV-1 covered T-1, T-fix is the next scope",
			log: (&foldLog{}).merged("T-1", "h1").started("RV-1", "h1", "T-1").merged("T-fix", "h2").
				briefed("h2", "T-fix").closing("RV-1", "h1", "T-1").closedAt("RV-1", "h1", "T-1").ends(),
			scope: []string{"T-fix"}, next: "RV-2",
			cycle: []string{"T-1"}},
		{name: "a verify finishing between the snapshot and a close fact without one (a crash cut): the snapshot's",
			log: (&foldLog{}).merged("T-1", "h1").started("RV-1", "h1", "T-1").merged("T-fix", "h2").
				briefed("h2", "T-fix").closing("RV-1", "h1", "T-1").ends().closed("RV-1"),
			scope: []string{"T-fix"}, next: "RV-2",
			cycle: []string{"T-1"}},
		{name: "a verify finishing between the snapshot and the close fact that carries it: the fact's snapshot",
			log: (&foldLog{}).merged("T-1", "h1").started("RV-1", "h1", "T-1").merged("T-fix", "h2").
				briefed("h2", "T-fix").closing("RV-1", "h1", "T-1").ends().closedAt("RV-1", "h1", "T-1"),
			scope: []string{"T-fix"}, next: "RV-2",
			cycle: []string{"T-1"}},
		{name: "a verify finished before the snapshot is in it: its fix covered",
			log: (&foldLog{}).merged("T-1", "h1").started("RV-1", "h1", "T-1").merged("T-fix", "h2").
				verify("h2", "", "T-fix").closing("RV-1", "h2", "T-1", "T-fix").closedAt("RV-1", "h2", "T-1", "T-fix"),
			next: "RV-2", cycle: []string{"T-1", "T-fix"}},
		{name: "a snapshot for another review than the open one is not its",
			log: (&foldLog{}).merged("T-1", "h1").started("RV-1", "h1", "T-1").merged("T-fix", "h2").
				verify("h2", "", "T-fix").closing("RV-0", "h1", "T-1").closed("RV-1"),
			next: "RV-2", cycle: []string{"T-1", "T-fix"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := c.log.fold()
			next := s.Next()
			if got := tasksOf(next.Scope); !slices.Equal(got, c.scope) {
				t.Errorf("scope %q, want %q", got, c.scope)
			}
			if next.ID != c.next {
				t.Errorf("next id %q, want %q", next.ID, c.next)
			}
			if next.After != s.Last() {
				t.Errorf("the next review follows %+v, want the newest %+v", next.After, s.Last())
			}
			if got := s.Cycle().Covered; c.cycle != nil && !slices.Equal(got, c.cycle) {
				t.Errorf("the newest review covered %q, want %q", got, c.cycle)
			}
		})
	}
}

// The newest review through its round: open from its start to its close, the head the reviewer last finished, the
// close's snapshot while its commit is not recorded, its close commit's subject for the next brief, the review
// before it.
func TestFoldRound(t *testing.T) {
	t.Parallel()
	rv4 := (&foldLog{}).merged("T-1", "h1").started("RV-4", "h1", "T-1")
	for _, c := range []struct {
		name     string
		log      *foldLog
		open     string // the open review's id
		last     string // the newest review's id
		reviewed string // the head its reviewer last finished
		closing  string // its close's snapshot: "<head> <tasks>" ("": none)
		subject  string // its close commit's subject
		prev     string // the review before it
		failed   string // the unfinished verify: "<tasks>: <why>" ("": none)
		file     string // the review file a brief of the reviewer open now asked for (a brief newer than every record)
	}{
		{name: "no review started", log: (&foldLog{}).merged("T-1", "h1")},
		{name: "started: open, reviewed at its start's head, its brief asked for the review file",
			log: rv4, open: "RV-4", last: "RV-4", reviewed: "h1", file: "RV-4"},
		{name: "a verify briefed: its head is not reviewed yet, its brief asked for the verify file",
			log:  (&foldLog{}).merged("T-1", "h1").started("RV-4", "h1", "T-1").merged("T-fix", "h2").briefed("h2", "T-fix"),
			open: "RV-4", last: "RV-4", reviewed: "h1", file: "RV-4-verify"},
		{name: "the verify finished: reviewed at its head",
			log:  (&foldLog{}).merged("T-1", "h1").started("RV-4", "h1", "T-1").merged("T-fix", "h2").verify("h2", "", "T-fix"),
			open: "RV-4", last: "RV-4", reviewed: "h2", file: "RV-4"},
		{name: "the reviewer stopped on an error: the verify did not finish, reviewed stays the start's",
			log: (&foldLog{}).merged("T-1", "h1").started("RV-4", "h1", "T-1").merged("T-fix", "h2").
				verify("h2", "herdr gone", "T-fix"),
			open: "RV-4", last: "RV-4", reviewed: "h1", failed: "T-fix: the reviewer stopped: herdr gone", file: "RV-4"},
		{name: "a newer brief replaced the verify's: it did not finish",
			log: (&foldLog{}).merged("T-1", "h1").started("RV-4", "h1", "T-1").merged("T-fix", "h2").
				briefed("h2", "T-fix").brief(),
			open: "RV-4", last: "RV-4", reviewed: "h1", failed: "T-fix: a newer brief replaced it", file: "RV-4"},
		{name: "the next start forgets the unfinished verify",
			log: (&foldLog{}).merged("T-1", "h1").started("RV-4", "h1", "T-1").verify("h2", "herdr gone", "T-fix").
				closed("RV-4").started("RV-5", "h3"),
			open: "RV-5", last: "RV-5", reviewed: "h3", prev: "RV-4", file: "RV-5"},
		{name: "the close's snapshot recorded, its commit not yet: open, the snapshot readable",
			log:  (&foldLog{}).merged("T-1", "h1").started("RV-4", "h1", "T-1").closing("RV-4", "h1", "T-1"),
			open: "RV-4", last: "RV-4", reviewed: "h1", closing: "h1 T-1", file: "RV-4"},
		{name: "a snapshot without a head is none",
			log:  (&foldLog{}).merged("T-1", "h1").started("RV-4", "h1", "T-1").closing("RV-4", "", "T-1"),
			open: "RV-4", last: "RV-4", reviewed: "h1", file: "RV-4"},
		{name: "closed: not open, no reviewed head, no snapshot, the subject its reply's",
			log: (&foldLog{}).merged("T-1", "h1").started("RV-4", "h1", "T-1").closing("RV-4", "h1", "T-1").
				closedAt("RV-4", "h1", "T-1"),
			last: "RV-4", subject: "RV-4 closed · x · approved · s", file: "RV-4"},
		{name: "a second close of the same review does not count: the first's subject stays",
			log: (&foldLog{}).started("RV-4", "h1").closed("RV-4").add(event.ReviewTask, event.StepReviewClosed,
				event.KindFact, Data{ID: "RV-4", Reply: ClosedReply + "another"}),
			last: "RV-4", subject: "RV-4 closed · x · approved · s", file: "RV-4"},
		{name: "a close naming an earlier review closes nothing",
			log:  (&foldLog{}).started("RV-4", "h1").closed("RV-3"),
			open: "RV-4", last: "RV-4", reviewed: "h1", file: "RV-4"},
		{name: "a close naming a later review closes the open one",
			log:  (&foldLog{}).started("RV-4", "h1").closed("RV-5"),
			last: "RV-4", subject: "RV-5 closed · x · approved · s", file: "RV-4"},
		{name: "a close whose id is not a review's closes nothing",
			log:  (&foldLog{}).started("RV-4", "h1").closed("the review"),
			open: "RV-4", last: "RV-4", reviewed: "h1", file: "RV-4"},
		{name: "a review request, its firing and its end start and close nothing",
			log: (&foldLog{}).started("RV-4", "h1").add(event.ReviewTask, event.StepReviewRequest, event.KindFact, Data{By: "pj-lead"}).
				add(event.ReviewTask, event.StepReviewFire, event.KindFact, Data{Request: 2}).
				add(event.ReviewTask, event.StepReviewRm, event.KindFact, Data{By: "pj-lead"}),
			open: "RV-4", last: "RV-4", reviewed: "h1", file: "RV-4"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := c.log.fold()
			last, cycle := s.Last(), s.Cycle()
			if last.Open() != c.open || last.ID != c.last || (last.Closed == 0) != (c.open != "" || c.last == "") {
				t.Errorf("the newest review %+v (open %q), want %q open of %q", last, last.Open(), c.open, c.last)
			}
			if cycle.Last != last || cycle.Reviewed != c.reviewed {
				t.Errorf("its cycle %+v, want %+v reviewed at %q", cycle, last, c.reviewed)
			}
			closing := ""
			if head, tasks, ok := s.Closing(); ok {
				closing = head + " " + strings.Join(tasks, ",")
			}
			if closing != c.closing {
				t.Errorf("the close's snapshot %q, want %q", closing, c.closing)
			}
			if got := s.Next().Subject; got != c.subject {
				t.Errorf("its close commit's subject %q, want %q", got, c.subject)
			}
			if got := s.Prev(); got != c.prev {
				t.Errorf("the review before it %q, want %q", got, c.prev)
			}
			failed := ""
			if v := s.Failed(); v != nil {
				failed = strings.Join(v.Tasks(), ",") + ": " + v.Why()
				if v.Seq() == 0 {
					t.Errorf("the unfinished verify has no seq: %+v", v)
				}
			}
			if failed != c.failed {
				t.Errorf("the unfinished verify %q, want %q", failed, c.failed)
			}
			if got := s.BriefFile("pj-rev", int64(len(c.log.recs))+1); got != c.file {
				t.Errorf("the reviewer's brief asked for %q, want %q", got, c.file)
			}
			if got := s.BriefFile("pj-rev", 0); c.file != c.last+"-verify" && got != "" {
				t.Errorf("a brief older than the review asked for %q, want no review file", got)
			}
		})
	}
}

// Fold hands package app a review fact's data, and nothing for any other record.
func TestFoldReturnsTheReviewFactsData(t *testing.T) {
	t.Parallel()
	var s State
	log := (&foldLog{}).merged("T-1", "h1").
		add(event.ReviewTask, event.StepReviewRequest, event.KindFact, Data{After: []string{"T-1"}, Note: "n", By: "pj-lead", Call: "c1", Reply: "REVIEW REQUESTED (after T-1)"}).
		add(event.ReviewTask, event.StepReviewFire, event.KindFact, Data{Request: 2}).
		started("RV-1", "h1", "T-1").
		briefed("h1", "T-1").
		add(event.ReviewTask, event.StepReviewRequest, event.KindIntent, Data{By: "pj-lead"}).
		add(event.ReviewTask, event.StepReviewClosed, event.KindFact, json.RawMessage(`"not an object"`))
	var got []string
	for _, r := range log.recs {
		if d, ok := s.Fold(r); ok {
			got = append(got, r.Step+" "+d.By+d.ID+d.Call+strings.Join(d.After, ","))
			if r.Step == event.StepReviewFire && d.Request != 2 {
				t.Errorf("the firing's request %d, want 2", d.Request)
			}
		} else if !slices.Equal([]any{d.ID, d.By, d.Call, d.Reply}, []any{"", "", "", ""}) {
			t.Errorf("%s of %s is no review fact, yet its data is %+v", r.Step, r.Task, d)
		}
	}
	want := []string{"review.request pj-leadc1T-1", "review.fire ", "review.started RV-1", "review.closed "}
	if !slices.Equal(got, want) {
		t.Errorf("review facts handed on: %q, want %q", got, want)
	}
	if s.Last().Open() != "RV-1" {
		t.Errorf("a close whose data does not read closed %+v", s.Last())
	}
}

// A verify reads a task's last merge since the review before the newest started: a fix merged during the review is
// found, a merge before that range is not.
func TestMergeOf(t *testing.T) {
	t.Parallel()
	s := (&foldLog{}).merged("T-old", "m0").started("RV-3", "m0", "T-old").closed("RV-3").
		merged("T-1", "m1").started("RV-4", "m1", "T-1").closed("RV-4").
		merged("T-2", "m2").started("RV-5", "m2", "T-2").merged("T-fix", "m3").merged("T-fix", "m4").fold()
	for task, want := range map[string]string{"T-fix": "m4", "T-2": "m2", "T-1": "", "T-old": "", "T-none": ""} {
		if m := s.MergeOf(task); m.SHA() != want || want != "" && m.Task() != task {
			t.Errorf("MergeOf(%s) = %s at %q, want at %q", task, m.Task(), m.SHA(), want)
		}
	}
}

// A review's id and its number.
func TestIDAndNumber(t *testing.T) {
	t.Parallel()
	for id, want := range map[string]int{"RV-1": 1, "RV-183": 183, "RV-": -1, "RV-1a": -1, "rv-1": -1, "RV--1": -1, "": -1} {
		n, ok := Number(id)
		if ok != (want >= 0) || ok && n != want {
			t.Errorf("Number(%q) = %d, %v; want %d", id, n, ok, want)
		}
		if ok && ID(n) != id {
			t.Errorf("ID(%d) = %q, want %q", n, ID(n), id)
		}
	}
}

// The fix round: what the close waits for, and obor status' line.
func TestLinksPendingAndLine(t *testing.T) {
	t.Parallel()
	links := []Link{
		{Task: "T-a", Dispatched: 5, Merged: 9},
		{Task: "T-b", Dispatched: 6, Merged: 10},
		{Task: "T-c", Dispatched: 7},
		{Task: "T-d", Dispatched: 8, End: "dropped"},
	}
	c := Cycle{Covered: []string{"T-start", "T-a"}}
	if got := c.Pending(links); !slices.Equal(got, []string{"T-b", "T-c"}) {
		t.Errorf("pending %q, want the merged uncovered T-b and the open T-c", got)
	}
	if got := c.Pending(nil); got != nil {
		t.Errorf("pending of no link: %q", got)
	}
	r := Round{ID: "RV-5", Links: links, Covered: c.Covered}
	if got, want := r.Line(), "RV-5 fixes: T-a verified · T-b merged · T-c open · T-d dropped"; got != want {
		t.Errorf("the status line %q, want %q", got, want)
	}
	if got := (Round{ID: "RV-5", Covered: c.Covered}).Line(); got != "" {
		t.Errorf("a round without links: %q, want no line", got)
	}
}
