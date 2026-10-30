package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
)

// watch is a watch request to the rig's serve from the lead (role lead, agent pj-lead).
func (r *loopRig) watch(callID, task string, rm bool) (string, error) {
	return app.Verdict(context.Background(), r.sock, app.MethodWatch, app.Call{Root: r.p.Root, Role: "lead",
		Agent: "pj-lead", CallID: callID, Task: task, Clear: rm})
}

// watchFacts is the watch and unwatch facts of the project's log; watchLines the lead lines a watch recorded.
func (r *loopRig) watchFacts() []app.Record {
	var out []app.Record
	for _, rec := range r.records() {
		if rec.Task == app.WatchTask && rec.Kind == app.KindFact {
			out = append(out, rec)
		}
	}
	return out
}

func (r *loopRig) watchLines() []string {
	var out []string
	for _, rec := range r.records() {
		var m app.Message
		if rec.Task == app.MsgTask && rec.Step == app.StepMessage && json.Unmarshal(rec.Data, &m) == nil && m.Watch != 0 {
			out = append(out, m.Line)
		}
	}
	return out
}

// mergedT1 runs T-1 from its task_done to its merge (the integrator accepts the review job's patch).
func (r *loopRig) mergedT1() {
	r.t.Helper()
	r.submit()
	r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
	r.merged(1)
}

// T-523: obor watch T-1, then T-1 merges: the lead gets exactly one "[obor] T-1 merged <short sha>" (main's merge);
// the watch ends with it (obor watch lists none), and a restart's replay posts no second line.
func TestWatchedMergePostsOneLine(t *testing.T) {
	r := newLoopRig(t, "echo 1 pass")
	r.work("feature.txt", "feature\n")
	r.start()
	if reply, err := r.watch("w1", "T-1", false); err != nil || reply != "WATCHING T-1" {
		t.Fatalf("watch: %q, %v", reply, err)
	}
	if reply, err := r.watch("w2", "", false); err != nil || reply != "WATCH T-1 since 0s" {
		t.Fatalf("the list: %q, %v", reply, err)
	}
	if n := len(r.watchFacts()); n != 1 {
		t.Fatalf("precondition: %d watch facts, want 1", n)
	}
	r.passes()
	if l := r.watchLines(); len(l) != 0 {
		t.Fatalf("a line before the merge: %q", l)
	}
	r.mergedT1()
	line := "[obor] T-1 merged " + r.p.Git(r.p.Root, "rev-parse", "--short=7", "main")
	r.waitFor("the merge line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	r.passes()
	if l := r.watchLines(); len(l) != 1 || l[0] != line {
		t.Errorf("watch lines %q, want one %q", l, line)
	}
	if reply, err := r.watch("w3", "", false); err != nil || reply != "NO WATCHES" {
		t.Errorf("the list after the line: %q, %v; want the watch ended", reply, err)
	}
	r.stop()
	r.start()
	r.passes()
	r.passes()
	if n, l := r.inbox.count("pj-lead", line), r.watchLines(); n != 1 || len(l) != 1 {
		t.Errorf("after a restart: posted %d, lines %q; want once", n, l)
	}
	if reply, err := r.watch("w4", "", false); err != nil || reply != "NO WATCHES" {
		t.Errorf("the list after a restart: %q, %v", reply, err)
	}
}

// T-523: a watched task dropped gets "[obor] T-1 dropped: <why>" once.
func TestWatchedDropPostsTheDroppedLine(t *testing.T) {
	r := newLoopRig(t, "echo 1 pass")
	r.start()
	if reply, err := r.watch("w1", "T-1", false); err != nil || reply != "WATCHING T-1" {
		t.Fatalf("watch: %q, %v", reply, err)
	}
	if reply, err := app.Verdict(context.Background(), r.sock, app.MethodDrop, app.Call{Root: r.p.Root, Role: "lead",
		CallID: "x1", Task: "T-1", Note: "superseded\nby T-2"}); err != nil || reply != "DROPPED T-1" {
		t.Fatalf("drop: %q, %v", reply, err)
	}
	const line = "[obor] T-1 dropped: superseded by T-2"
	r.waitFor("the dropped line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	r.passes()
	if l := r.watchLines(); len(l) != 1 || l[0] != line {
		t.Errorf("watch lines %q, want one %q", l, line)
	}
}

// T-523: a watch of a task merged already is answered with its outcome at once and records nothing; an unknown task
// (no dispatch, no spec) is refused; a task with only a spec may be watched; a repeated call id gets its stored reply.
func TestWatchEndedOrUnknownTask(t *testing.T) {
	r := newLoopRig(t, "echo 1 pass")
	r.work("feature.txt", "feature\n")
	r.start()
	r.mergedT1()
	want := "T-1 merged " + r.p.Git(r.p.Root, "rev-parse", "--short=7", "main")
	if reply, err := r.watch("w1", "T-1", false); err != nil || reply != want {
		t.Errorf("watch of a merged task: %q, %v; want %q", reply, err, want)
	}
	if _, err := r.watch("w2", "T-9", false); err == nil || !strings.Contains(err.Error(), "unknown task T-9") {
		t.Errorf("watch of an unknown task: %v; want refused", err)
	}
	if n := len(r.watchFacts()); n != 0 {
		t.Errorf("%d watch facts recorded, want none", n)
	}
	if _, err := app.Verdict(context.Background(), r.sock, app.MethodSpecSet, app.Call{Root: r.p.Root, Role: "lead",
		CallID: "s1", Task: "T-9", Spec: "# T-9\n\nDifficulty: medium\n"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if reply, err := r.watch("w3", "T-9", false); err != nil || reply != "WATCHING T-9" {
			t.Errorf("watch of a specified task: %q, %v", reply, err)
		}
	}
	if n := len(r.watchFacts()); n != 1 {
		t.Errorf("%d watch facts, want 1 (a repeated call id records nothing)", n)
	}
	if _, err := app.Verdict(context.Background(), r.sock, app.MethodWatch, app.Call{Root: r.p.Root, Role: "worker",
		Agent: "pj-o1", CallID: "w4", Task: "T-9"}); err == nil {
		t.Error("a worker's watch accepted")
	}
}

// T-523: obor watch --rm ends a watch: the merge then posts nothing; --rm of a task not watched records nothing.
func TestUnwatchedMergePostsNothing(t *testing.T) {
	r := newLoopRig(t, "echo 1 pass")
	r.work("feature.txt", "feature\n")
	r.start()
	if reply, err := r.watch("w1", "T-1", false); err != nil || reply != "WATCHING T-1" {
		t.Fatalf("watch: %q, %v", reply, err)
	}
	if reply, err := r.watch("w2", "T-1", true); err != nil || reply != "UNWATCHED T-1" {
		t.Fatalf("--rm: %q, %v", reply, err)
	}
	if reply, err := r.watch("w3", "T-1", true); err != nil || reply != "NOT WATCHING T-1" {
		t.Errorf("--rm again: %q, %v", reply, err)
	}
	if n := len(r.watchFacts()); n != 2 {
		t.Errorf("%d watch facts, want watch and unwatch", n)
	}
	r.mergedT1()
	r.passes()
	r.passes()
	if l := r.watchLines(); len(l) != 0 {
		t.Errorf("lines after --rm: %q", l)
	}
	r.inbox.mu.Lock()
	defer r.inbox.mu.Unlock()
	for _, p := range r.inbox.posts {
		if strings.Contains(p, "T-1 merged") {
			t.Errorf("posted %q", p)
		}
	}
}

// seedFailed writes, to the stopped app's log, T-1's dispatch on O-01 and each of tasks as an open attempt (submitted
// on its own lane, its submit refused: a needs-you, merged once land puts it on main).
func (r *loopRig) seedFailed(tasks ...string) {
	r.t.Helper()
	r.p.Dispatched("O-01")
	var recs []app.Record
	refused, _ := json.Marshal(map[string]any{"refused": "no commit"})
	for i, task := range tasks {
		sub, _ := json.Marshal(map[string]any{"lane": fmt.Sprintf("O-%02d", i+2), "agent": fmt.Sprintf("pj-o%d", i+2)})
		recs = append(recs, app.Record{Task: task, Attempt: 1, Step: app.StepSubmitted, Kind: app.KindFact, Data: sub},
			app.Record{Task: task, Attempt: 1, Step: app.StepSubmit, Kind: app.KindIntent, Class: app.ClassGit},
			app.Record{Task: task, Attempt: 1, Step: app.StepSubmit, Kind: app.KindResult, Data: refused})
	}
	if err := app.WriteLog(r.p.Root, time.Now(), recs...); err != nil {
		r.t.Fatal(err)
	}
}

// land is an obor merge of task on main outside the app: the loop records merged{outside}.
func (r *loopRig) land(task string) {
	r.t.Helper()
	r.p.Git(r.p.Root, "commit", "-q", "--allow-empty", "-m", "Merge "+task+": by hand")
	r.waitFor(task+" merged", func() bool { return r.taskStep(task, "fact merged") > 0 })
}

// T-528: obor watch T-1,T-2,T-3 is one watch: the list names the tasks not merged yet; merges one by one post
// nothing until the last, then one "[obor] T-1,T-2,T-3 merged (<the last merge's short sha>)"; a restart's replay
// posts it no second time.
func TestWatchedSetPostsOneLineAfterTheLast(t *testing.T) {
	r := newLoopRig(t, "echo 1 pass")
	r.work("feature.txt", "feature\n")
	r.seedFailed("T-2", "T-3")
	r.start()
	if reply, err := r.watch("w1", "T-1,T-2,T-3", false); err != nil || reply != "WATCHING T-1,T-2,T-3" {
		t.Fatalf("watch: %q, %v", reply, err)
	}
	if reply, err := r.watch("w2", "", false); err != nil || reply != "WATCH T-1,T-2,T-3 since 0s, waiting T-1,T-2,T-3" {
		t.Fatalf("the list: %q, %v", reply, err)
	}
	r.land("T-2")
	r.passes()
	if reply, err := r.watch("w3", "", false); err != nil || reply != "WATCH T-1,T-2,T-3 since 0s, waiting T-1,T-3" {
		t.Errorf("the list after T-2: %q, %v", reply, err)
	}
	r.land("T-3")
	r.passes()
	if l := r.watchLines(); len(l) != 0 {
		t.Fatalf("a line before the last merge: %q", l)
	}
	r.mergedT1()
	line := "[obor] T-1,T-2,T-3 merged (" + r.p.Git(r.p.Root, "rev-parse", "--short=7", "main") + ")"
	r.waitFor("the set's line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	r.passes()
	if l := r.watchLines(); len(l) != 1 || l[0] != line {
		t.Errorf("watch lines %q, want one %q", l, line)
	}
	if reply, err := r.watch("w4", "", false); err != nil || reply != "NO WATCHES" {
		t.Errorf("the list after the line: %q, %v", reply, err)
	}
	r.stop()
	r.start()
	r.passes()
	r.passes()
	if n, l := r.inbox.count("pj-lead", line), r.watchLines(); n != 1 || len(l) != 1 {
		t.Errorf("after a restart: posted %d, lines %q; want once", n, l)
	}
}

// T-528: a task of a watched set dropped ends the watch at once with its line; the set's later merges post nothing.
func TestWatchedSetDropEndsIt(t *testing.T) {
	r := newLoopRig(t, "echo 1 pass")
	r.work("feature.txt", "feature\n")
	r.seedFailed("T-2", "T-3")
	r.start()
	if reply, err := r.watch("w1", "T-1,T-2,T-3", false); err != nil || reply != "WATCHING T-1,T-2,T-3" {
		t.Fatalf("watch: %q, %v", reply, err)
	}
	if reply, err := app.Verdict(context.Background(), r.sock, app.MethodDrop, app.Call{Root: r.p.Root, Role: "lead",
		CallID: "x1", Task: "T-2", Note: "superseded"}); err != nil || reply != "DROPPED T-2" {
		t.Fatalf("drop: %q, %v", reply, err)
	}
	const line = "[obor] T-2 dropped: superseded (watch T-1,T-2,T-3 ended)"
	r.waitFor("the dropped line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	if reply, err := r.watch("w2", "", false); err != nil || reply != "NO WATCHES" {
		t.Errorf("the list after the drop: %q, %v", reply, err)
	}
	r.land("T-3")
	r.mergedT1()
	r.passes()
	r.passes()
	if l := r.watchLines(); len(l) != 1 || l[0] != line {
		t.Errorf("watch lines %q, want only %q", l, line)
	}
}

// T-528: a set's task merged already counts: the watch waits for the rest; a set all merged is answered at once with
// its line and records nothing; a set with an unknown task is refused naming it; --rm takes the comma list.
func TestWatchSetMergedOrUnknown(t *testing.T) {
	r := newLoopRig(t, "echo 1 pass")
	r.seedFailed("T-2", "T-3", "T-4")
	r.start()
	r.land("T-2")
	if reply, err := r.watch("w1", "T-2,T-3", false); err != nil || reply != "WATCHING T-2,T-3" {
		t.Fatalf("watch: %q, %v", reply, err)
	}
	if reply, err := r.watch("w2", "", false); err != nil || reply != "WATCH T-2,T-3 since 0s, waiting T-3" {
		t.Errorf("the list: %q, %v", reply, err)
	}
	r.land("T-3")
	const line = "[obor] T-2,T-3 merged (outside obor serve)"
	r.waitFor("the set's line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	if n := len(r.watchFacts()); n != 1 {
		t.Fatalf("precondition: %d watch facts, want 1", n)
	}
	if reply, err := r.watch("w3", "T-2,T-3", false); err != nil || reply != "T-2,T-3 merged (outside obor serve)" {
		t.Errorf("watch of a merged set: %q, %v", reply, err)
	}
	if _, err := r.watch("w4", "T-2,T-9", false); err == nil || !strings.Contains(err.Error(), "unknown task T-9") {
		t.Errorf("watch of a set with an unknown task: %v; want refused", err)
	}
	if n := len(r.watchFacts()); n != 1 {
		t.Errorf("%d watch facts, want 1 (an ended or refused set records nothing)", n)
	}
	if reply, err := r.watch("w5", "T-1,T-4", false); err != nil || reply != "WATCHING T-1,T-4" {
		t.Fatalf("watch: %q, %v", reply, err)
	}
	if reply, err := r.watch("w6", "T-1,T-4", true); err != nil || reply != "UNWATCHED T-1,T-4" {
		t.Errorf("--rm of the set: %q, %v", reply, err)
	}
	if reply, err := r.watch("w7", "", false); err != nil || reply != "NO WATCHES" {
		t.Errorf("the list after --rm: %q, %v", reply, err)
	}
}

// T-528: a task of a watched set rejected ends the watch at once with "[obor] T-1 rejected: <note> (watch … ended)".
func TestWatchedSetRejectEndsIt(t *testing.T) {
	r := newLoopRig(t, "echo 1 pass")
	r.work("feature.txt", "feature\n")
	r.seedFailed("T-2")
	r.start()
	if reply, err := r.watch("w1", "T-1,T-2", false); err != nil || reply != "WATCHING T-1,T-2" {
		t.Fatalf("watch: %q, %v", reply, err)
	}
	r.submit()
	if reply, err := r.verdict(app.MethodReject, "j1", r.patch(r.waitJob("pj-int", "T-1/1/review")), "out of scope"); err != nil ||
		!strings.HasPrefix(reply, "REJECT") {
		t.Fatalf("reject: %q, %v", reply, err)
	}
	const line = "[obor] T-1 rejected: out of scope (watch T-1,T-2 ended)"
	r.waitFor("the rejected line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	if reply, err := r.watch("w2", "", false); err != nil || reply != "NO WATCHES" {
		t.Errorf("the list after the reject: %q, %v", reply, err)
	}
}
