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
// unreviewed tasks and batch). It replaces the tests that drove these through a ReviewGit fake's Merges, Closes and
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

// briefed is a verify brief of tasks at head, still open; ends is the finished line that ends it.
func (f *foldLog) briefed(head string, tasks ...string) *foldLog {
	return f.add(event.MsgTask, event.StepBriefed, event.KindFact, event.Message{To: "pj-rev",
		Task: strings.Join(tasks, ","), Reviewed: head})
}
func (f *foldLog) ends() *foldLog {
	return f.add(event.MsgTask, event.StepMessage, event.KindFact, event.Message{To: "pj-lead", Ends: "pj-rev"})
}

// closing is a close's snapshot before its commit (T-1032); closedAt a close fact carrying it, as serve's close
// records it.
func (f *foldLog) closing(id, head string, tasks ...string) *foldLog {
	return f.add(event.ReviewTask, event.StepReviewClosing, event.KindFact, ReviewData{ID: id, Head: head, Tasks: tasks})
}
func (f *foldLog) closedAt(id, head string, tasks ...string) *foldLog {
	return f.add(event.ReviewTask, event.StepReviewClosed, event.KindFact, ReviewData{ID: id, Head: head, Tasks: tasks,
		Reply: closedReply + id + " closed · x · approved · s"})
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
		cycle      []string // the newest review's covered tasks (nil: not checked)
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
		// RV-144 #1 (T-1032): the close's snapshot stands; a verify finishing after it changes nothing.
		{name: "a verify finishing after the close: R stays the close's h1, RV-1 covered T-1, T-fix is the next scope",
			log: (&foldLog{}).merged("T-1", "h1").started("RV-1", "h1", "T-1").merged("T-fix", "h2").
				briefed("h2", "T-fix").closing("RV-1", "h1", "T-1").closedAt("RV-1", "h1", "T-1").ends(),
			scope: []string{"T-fix"}, next: "RV-2", r: "h1", at: "h1", rv: "RV-1", batch: []string{"T-fix"},
			cycle: []string{"T-1"}},
		{name: "a verify finishing between the snapshot and a close fact without one (a crash cut): the snapshot's",
			log: (&foldLog{}).merged("T-1", "h1").started("RV-1", "h1", "T-1").merged("T-fix", "h2").
				briefed("h2", "T-fix").closing("RV-1", "h1", "T-1").ends().closed("RV-1"),
			scope: []string{"T-fix"}, next: "RV-2", r: "h1", at: "h1", rv: "RV-1", batch: []string{"T-fix"},
			cycle: []string{"T-1"}},
		{name: "a verify finished before the snapshot is in it: R its head, its fix covered",
			log: (&foldLog{}).merged("T-1", "h1").started("RV-1", "h1", "T-1").merged("T-fix", "h2").
				verify("h2", "", "T-fix").closing("RV-1", "h2", "T-1", "T-fix").closedAt("RV-1", "h2", "T-1", "T-fix"),
			next: "RV-2", r: "h2", at: "h1", rv: "RV-1", batch: []string{"T-fix"}, cycle: []string{"T-1", "T-fix"}},
		{name: "a snapshot for another review than the open one is not its",
			log: (&foldLog{}).merged("T-1", "h1").started("RV-1", "h1", "T-1").merged("T-fix", "h2").
				verify("h2", "", "T-fix").closing("RV-0", "h1", "T-1").closed("RV-1"),
			next: "RV-2", r: "h2", at: "h1", rv: "RV-1", batch: []string{"T-fix"}, cycle: []string{"T-1", "T-fix"}},
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
			if got := l.cur().cycle(); c.cycle != nil && !slices.Equal(got, c.cycle) {
				t.Errorf("the newest review covered %q, want %q", got, c.cycle)
			}
		})
	}
}

// RV-144 #2 (T-1032): a promoted head the log never named (a promoted branch older than obor's merge records) starts
// promotion's range at the log's first merge, not at the review cutoff: the batch and unreviewed count every logged
// merge (those a closed review covered stay covered); the review scope and a verify's merge keep their cutoff.
func TestReviewFoldUnknownPromotedHead(t *testing.T) {
	t.Parallel()
	fold := func(f *foldLog) *reviewLog {
		var l reviewLog
		for _, r := range f.recs {
			l.fold(r)
		}
		return &l
	}
	t.Run("T-1 merged, two review starts: between an unknown head and h1 is T-1", func(t *testing.T) {
		t.Parallel()
		l := fold((&foldLog{}).merged("T-1", "h1").started("RV-1", "h1", "T-1").started("RV-2", "h1"))
		if l.base() <= l.merges[0].seq {
			t.Fatalf("precondition: base %d not after T-1's merge %d", l.base(), l.merges[0].seq)
		}
		if got := tasksOf(l.between("unknown", "h1")); !slices.Equal(got, []string{"T-1"}) {
			t.Errorf("between(unknown, h1) = %q, want [T-1]", got)
		}
		if got := tasksOf(l.between("", "h1")); len(got) != 0 {
			t.Errorf("between(\"\", h1) = %q, want none (the review scope's cutoff)", got)
		}
	})
	t.Run("unreviewed with an unknown base: every merge up to R less any closed review's; the scope unchanged",
		func(t *testing.T) {
			t.Parallel()
			// T-x merged at h2 and no review covered it; RV-1 covered T-1; base is RV-2's start, after both merges.
			l := fold((&foldLog{}).merged("T-1", "h1").merged("T-x", "h2").started("RV-1", "h2", "T-1").closed("RV-1").
				started("RV-2", "h2").closed("RV-2").started("RV-3", "h2").closed("RV-3"))
			if l.base() <= l.merges[1].seq || l.closedHead() != "h2" {
				t.Fatalf("precondition: base %d, merges %+v, R %q", l.base(), l.merges, l.closedHead())
			}
			got, rv := l.unreviewed("old-main", "h2")
			if !slices.Equal(got, []string{"T-x"}) || rv != "RV-3" {
				t.Errorf("unreviewed(old-main, h2) = %q during %q, want [T-x] during RV-3 (T-1 covered by RV-1)", got, rv)
			}
			if got, _ := l.unreviewed("h2", "h2"); len(got) != 0 {
				t.Errorf("unreviewed(h2, h2) = %q, want none (a known head keeps its range)", got)
			}
			if got := tasksOf(l.between("old-main", "")); !slices.Equal(got, []string{"T-1", "T-x"}) {
				t.Errorf("the batch since old-main = %q, want [T-1 T-x]", got)
			}
			if got := tasksOf(l.scope()); len(got) != 0 {
				t.Errorf("scope %q, want none: the review scope keeps its cutoff", got)
			}
			if m := l.mergeOf("T-x"); m.task != "" {
				t.Errorf("mergeOf(T-x) = %+v, want none: a verify's merge keeps the cutoff", m)
			}
		})
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
