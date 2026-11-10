package app

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/outbox"
)

// What a review covered is the log's, one table over the fold (records → the next review's scope, which is
// both the brief's in-scope tasks and the "review due" count; the next id; the closed head R).

// foldLog builds records in seq order for a reviewLog.
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
	return f.add(event.ReviewTask, event.StepReviewStarted, event.KindFact, ReviewData{ID: id, Head: head, Tasks: tasks})
}

func (f *foldLog) closed(id string) *foldLog {
	return f.add(event.ReviewTask, event.StepReviewClosed, event.KindFact, ReviewData{ID: id,
		Reply: closedReply + id + " closed · x · approved · s"})
}

// verify is a verify brief of tasks at head, then its end: finished (why "") or the reviewer's error stop.
func (f *foldLog) verify(head, why string, tasks ...string) *foldLog {
	f.add(event.MsgTask, event.StepBriefed, event.KindFact, event.Message{To: "pj-rev", Task: strings.Join(tasks, ","),
		Reviewed: head})
	if why != "" {
		return f.add(event.MsgTask, event.StepStopped, event.KindFact, outbox.Stopped{Ends: "pj-rev", Error: why})
	}
	return f.add(event.MsgTask, event.StepMessage, event.KindFact, event.Message{To: "pj-lead", Ends: "pj-rev"})
}

// briefed is a verify brief of tasks at head, still open; ends is the finished line that ends it.
func (f *foldLog) briefed(head string, tasks ...string) *foldLog {
	return f.add(event.MsgTask, event.StepBriefed, event.KindFact, event.Message{To: "pj-rev",
		Task: strings.Join(tasks, ","), Reviewed: head})
}

func (f *foldLog) ends() *foldLog {
	return f.add(event.MsgTask, event.StepMessage, event.KindFact, event.Message{To: "pj-lead", Ends: "pj-rev"})
}

// closing is a close's snapshot before its commit; closedAt a close fact carrying it, as serve's close
// records it.
func (f *foldLog) closing(id, head string, tasks ...string) *foldLog {
	return f.add(event.ReviewTask, event.StepReviewClosing, event.KindFact, ReviewData{ID: id, Head: head, Tasks: tasks})
}

func (f *foldLog) closedAt(id, head string, tasks ...string) *foldLog {
	return f.add(event.ReviewTask, event.StepReviewClosed, event.KindFact, ReviewData{ID: id, Head: head, Tasks: tasks,
		Reply: closedReply + id + " closed · x · approved · s"})
}

func TestReviewFold(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		log   *foldLog
		scope []string // the next review's in-scope tasks (more than reviewDue: review due)
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
			scope: []string{"T-x"}, next: "RV-6"},
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
			scope: []string{"T-fix"}, next: "RV-5"},
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
			var l reviewLog
			for _, r := range c.log.recs {
				l.fold(r)
			}
			if got := tasksOf(l.scope()); !slices.Equal(got, c.scope) {
				t.Errorf("scope %q, want %q", got, c.scope)
			}
			if got := l.nextID(); got != c.next {
				t.Errorf("next id %q, want %q", got, c.next)
			}
			if got := l.cur().cycle(); c.cycle != nil && !slices.Equal(got, c.cycle) {
				t.Errorf("the newest review covered %q, want %q", got, c.cycle)
			}
		})
	}
}

// obor status' "review due" line is the fold's scope, counted on every pass: at 3 merges none, at 4 "review
// due: 4 merges since RV-4", none once RV-5 started on them.
func TestReviewDueFromTheFold(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t) // RV-4 closed, T-a and T-b merged since
	r.mergedAt("T-c", "ccccccc3")
	r.tick()
	if got := r.a.Status().Project.Review; got != "" {
		t.Fatalf("at 3 merges: %q, want none", got)
	}
	r.mergedAt("T-d", "ddddddd4")
	r.tick()
	if got := r.a.Status().Project.Review; got != "review due: 4 merges since RV-4" {
		t.Errorf("at 4 merges: %q", got)
	}
	r.mustCall("r1", nil, "", false)
	r.pass()
	if l := r.agents.launches; len(l) != 1 ||
		!strings.Contains(l[0].Prompt, "In scope: T-a (aaaaaaa), T-b (bbbbbbb), T-c (ccccccc), T-d (ddddddd);") {
		t.Fatalf("RV-5's brief: %+v, want the 4 merges in scope", l)
	}
	r.tick()
	if got := r.a.Status().Project.Review; got != "" {
		t.Errorf("RV-5 started on them: %q, want none", got)
	}
}
