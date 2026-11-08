package app

import (
	"encoding/json"
	"obor/internal/app/event"
	"slices"
	"strings"
	"testing"
)

// T-1023: what a review covered is the log's, one table over the fold (records → the next review's scope, which is
// both the brief's in-scope tasks and the "review due" count; the next id; the closed head R; the promotion's
// unreviewed tasks and batch). It replaces the tests that drove these through a port.Review fake's Merges, Closes and
// Last.

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
		return f.add(event.MsgTask, event.StepStopped, event.KindFact, stoppedData{Ends: "pj-rev", Error: why})
	}
	return f.add(event.MsgTask, event.StepMessage, event.KindFact, event.Message{To: "pj-lead", Ends: "pj-rev"})
}
func (f *foldLog) promoted(sha string) *foldLog {
	return f.add(event.PromoteTask, event.StepPromoted, event.KindFact, promotedData{SHA: sha})
}

func tasksOf(ms []mergeRec) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.task)
	}
	return out
}

func TestReviewFold(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		log        *foldLog
		scope      []string // the next review's in-scope tasks (more than reviewDue: review due)
		next, r    string   // the next review's id; R, the closed head
		at         string   // the promoted branch's head, for unreviewed and between (to R; "": every merge)
		unreviewed []string
		rv         string
		batch      []string // between(at, "")
	}{
		{name: "an empty log", log: &foldLog{}, next: "RV-1"},
		{name: "merges, no review yet: every merge, the first is RV-1",
			log:   (&foldLog{}).merged("T-1", "m1").merged("T-2", "m2").merged("T-3", "m3").merged("T-4", "m4"),
			scope: []string{"T-1", "T-2", "T-3", "T-4"}, next: "RV-1", batch: []string{"T-1", "T-2", "T-3", "T-4"}},
		{name: "a merge outside the app (by hand in git) is no review's: a by-hand twin",
			log:   (&foldLog{}).merged("T-1", "m1").outside("T-2"),
			scope: []string{"T-1"}, next: "RV-1", batch: []string{"T-1"}},
		{name: "RV-4 closed covering T-1, T-2: T-3 merged since is the scope, R its head",
			log: (&foldLog{}).merged("T-1", "m1").merged("T-2", "m2").started("RV-4", "m2", "T-1", "T-2").
				merged("T-3", "m3").closed("RV-4"),
			scope: []string{"T-3"}, next: "RV-5", r: "m2", at: "m1", batch: []string{"T-2", "T-3"}},
		{name: "a merge recorded between a start's head read and its fact: no review covered it, the next one's scope",
			log: (&foldLog{}).merged("T-1", "m1").started("RV-4", "m1", "T-1").closed("RV-4").
				merged("T-2", "m2").merged("T-3", "m3").started("RV-5", "m2", "T-2").closed("RV-5"),
			scope: []string{"T-3"}, next: "RV-6", r: "m2", at: "m1", batch: []string{"T-2", "T-3"}},
		{name: "a re-merged task is in scope once, at its last merge",
			log:   (&foldLog{}).merged("T-1", "m1").merged("T-2", "m2").merged("T-1", "m3"),
			scope: []string{"T-2", "T-1"}, next: "RV-1", batch: []string{"T-1", "T-2", "T-1"}},
		{name: "RV-5 open, its fix verified: the fix covered, a merge during it not; R is still RV-4's",
			log: (&foldLog{}).merged("T-1", "m1").started("RV-4", "m1", "T-1").closed("RV-4").
				merged("T-2", "m2").started("RV-5", "m2", "T-2").merged("T-fix", "m3").merged("T-x", "m4").
				verify("m3", "", "T-fix"),
			scope: []string{"T-x"}, next: "RV-6", r: "m1", at: "m1", batch: []string{"T-2", "T-fix", "T-x"}},
		{name: "RV-5 closed at its verify's head: R = m4; T-x merged before the verify, not covered, holds R",
			log: (&foldLog{}).merged("T-1", "m1").started("RV-4", "m1", "T-1").closed("RV-4").
				merged("T-2", "m2").started("RV-5", "m2", "T-2").merged("T-x", "m3").merged("T-fix", "m4").
				verify("m4", "", "T-fix").closed("RV-5"),
			scope: []string{"T-x"}, next: "RV-6", r: "m4", at: "m1", unreviewed: []string{"T-x"}, rv: "RV-5",
			batch: []string{"T-2", "T-x", "T-fix"}},
		{name: "a merge after R does not hold it",
			log: (&foldLog{}).merged("T-1", "m1").started("RV-4", "m1", "T-1").closed("RV-4").
				merged("T-2", "m2").started("RV-5", "m2", "T-2").closed("RV-5").merged("T-3", "m3"),
			scope: []string{"T-3"}, next: "RV-6", r: "m2", at: "m1", rv: "RV-5", batch: []string{"T-2", "T-3"}},
		{name: "a verify that did not finish covers nothing and moves no head",
			log: (&foldLog{}).merged("T-1", "m1").started("RV-4", "m1", "T-1").merged("T-fix", "m2").
				verify("m2", "herdr gone", "T-fix").closed("RV-4"),
			scope: []string{"T-fix"}, next: "RV-5", r: "m1", batch: []string{"T-1", "T-fix"}},
		{name: "a close naming an earlier review closes nothing; one naming a later review closes the open one",
			log:   (&foldLog{}).started("RV-4", "h4").closed("RV-3").started("RV-5", "h5").closed("RV-6"),
			next:  "RV-6",
			r:     "h5",
			scope: nil},
		{name: "the promoted head is known: the batch is the merges after it",
			log: (&foldLog{}).merged("T-1", "m1").started("RV-4", "m1", "T-1").closed("RV-4").promoted("m1").
				merged("T-2", "m2"),
			scope: []string{"T-2"}, next: "RV-5", r: "m1", at: "m1", rv: "RV-4", batch: []string{"T-2"}},
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
			if got := l.closedHead(); got != c.r {
				t.Errorf("R %q, want %q", got, c.r)
			}
			if c.r != "" {
				got, rv := l.unreviewed(c.at, c.r)
				if !slices.Equal(got, c.unreviewed) || c.rv != "" && rv != c.rv {
					t.Errorf("unreviewed %q (during %q), want %q (%q)", got, rv, c.unreviewed, c.rv)
				}
			}
			if got := tasksOf(l.between(c.at, "")); !slices.Equal(got, c.batch) {
				t.Errorf("batch since %q: %q, want %q", c.at, got, c.batch)
			}
		})
	}
}

// T-1023: obor status' "review due" line is the fold's scope, counted on every pass: at 3 merges none, at 4 "review
// due: 4 merges since RV-4", none once a review started on them.
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
