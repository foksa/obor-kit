package app

import (
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// T-896, T-921: serve ships whether a task ended: the state table's ended column, not a list of state names its
// readers keep. A dispatched task has not ended; a dropped one has, and stays in the list (obor status --json shows
// it).
func TestStatusEndedIsTheStateTablesEnded(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj"}`)
	h.connect()
	for _, task := range []string{"T-1", "T-2"} {
		addRecord(t, h, task, event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"),
			Agent: "pj-l-01", Branch: "task/" + task, Target: "b1"})
	}
	addRecord(t, h, "T-2", event.StepDropped, event.KindFact, event.FactData{Reason: "why"})
	ended := map[string]bool{}
	for _, ts := range h.a.Status().Project.Tasks {
		ended[ts.Task] = ts.Ended
	}
	if got, ok := ended["T-1"]; !ok || got {
		t.Errorf("T-1: ended %v (listed %v), want not ended: dispatched", got, ok)
	}
	if got, ok := ended["T-2"]; !ok || !got {
		t.Errorf("T-2: ended %v (listed %v), want ended: dropped, and still in the list", got, ok)
	}
}

// T-1043: serve ships the queue's order and its waiting predicate, so the view's queue lists the dispatched waits in
// the order serve starts them instead of reconstructing both from the state string: a waiting dispatch is flagged
// Waiting and ranked by passOrder — T-20, dispatched first, ranks before T-10.
func TestStatusShipsTheWaitingRank(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	a, p := lineApp(t, lineRoot(t), &now)
	for _, task := range []string{"T-20", "T-10"} { // the reversed dispatch order: T-20's waiting fact first
		if !a.record(p, event.Record{Task: task, Attempt: 1, Step: event.StepWaiting, Kind: event.KindFact},
			dispatchData{Level: "medium", Types: []string{"sonnet"}}) {
			t.Fatalf("the log refused %s's waiting fact", task)
		}
	}
	ps := a.Status().Project
	if ps == nil || len(ps.Tasks) != 2 {
		t.Fatalf("precondition: serve has the two waiting tasks: %+v", ps)
	}
	rank := map[string]int{}
	for _, ts := range ps.Tasks {
		if !ts.Waiting {
			t.Errorf("%s: Waiting false, want true: its dispatch waits for a lane", ts.Task)
		}
		rank[ts.Task] = ts.Rank
	}
	if rank["T-20"] == 0 || rank["T-10"] == 0 || rank["T-20"] >= rank["T-10"] {
		t.Errorf("ranks %v, want T-20 before T-10: its dispatch came first", rank)
	}
}

// T-896, T-921: serve ships the lanes its config-change rule would not refuse to drop (laneHolds), so obor web's
// setup page warns without a list of lane states: a lane holding a task is not free, a free one is, and an ended task
// frees its lane.
func TestStatusFreeLanesFollowLaneHolds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj"}`)
	h.lanes.lanes = []port.Lane{{Name: "L-01", Dir: h.lane("L-01")}, {Name: "L-02", Dir: h.lane("L-02")}}
	h.connect()
	if ps := h.a.Status().Project; ps == nil {
		t.Fatal("precondition: serve has no project")
	}
	addRecord(t, h, "T-2", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-02", Dir: h.lane("L-02"),
		Agent: "pj-l-02", Branch: "task/T-2", Target: "b1"})
	h.a.mu.Lock()
	h.a.p.lanes = []port.Lane{{Name: "L-01", State: port.LaneFree}, {Name: "L-02", State: port.LaneWorking}}
	h.a.mu.Unlock()
	if free := h.a.Status().Project.Free; len(free) != 1 || free[0] != "L-01" {
		t.Errorf("free %v, want L-01 alone: L-02 holds T-2, L-01 holds nothing", free)
	}
	addRecord(t, h, "T-2", event.StepDropped, event.KindFact, event.FactData{Reason: "why"})
	if free := h.a.Status().Project.Free; len(free) != 2 || free[0] != "L-01" || free[1] != "L-02" {
		t.Errorf("free %v, want both: T-2 ended", free)
	}
}

// RV-49 #4: a waiting task has no lane, so the drop its needs-you advertises names the task (obor drop <T-xx>, T-462),
// whichever dependency ended the wait.
func TestAnswerPathWaitingTaskNamesTheTask(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{
		"after: T-8 waits for T-1, which is dropped",
		"after: T-8 waits for T-1, which is rejected",
	} {
		raw, _ := json.Marshal(event.FactData{Merge: true, Cause: causeAfter, Reason: reason})
		r := event.Record{Seq: 1, Task: "T-8", Step: event.StepNeedsYou, Data: raw}
		path := answerPath(nil, r, mtask{task: "T-8", state: stWaiting, needs: block{fact: r}})
		if strings.Contains(path, "<LANE>") {
			t.Errorf("%q: path %q names <LANE>, want the task", reason, path)
		}
		if !strings.Contains(path, `obor drop T-8 "<why>"`) {
			t.Errorf("%q: path %q, want obor drop T-8", reason, path)
		}
	}
}

// T-775: a stalled worker's needs-you names the task, never its lane: obor resume with the line, obor drop by task id.
func TestAnswerPathNamesTheTaskNotTheLane(t *testing.T) {
	t.Parallel()
	raw, _ := json.Marshal(event.FactData{Lane: "L-02", Reason: "stalled"})
	r := event.Record{Seq: 1, Task: "T-8", Step: event.StepNeedsYou, Data: raw}
	p := &proj{folded: folded{active: map[string][]event.Record{"T-8": {r}}}}
	path := answerPath(p, r, mtask{task: "T-8", state: stWorking, lane: "L-02", agent: "pj-l-02"})
	if want := `its pane, or obor resume T-8 "<text>" | obor drop T-8 "<why>"`; path != want {
		t.Errorf("path %q, want %q", path, want)
	}
}

// T-657/T-662: the running line's steps are the merge loop's command steps whose effect runs now (p.exec), in seq
// order — a numbered gate, a numbered publish, the nightly check — and a pending self-restart last, elapsed from the
// start of each execution. An open intent whose effect does not run now is pending, not running; an intent with a
// result, a prompt and a start are neither.
func TestRunningStepsAreTheCommandStepsWhoseEffectRuns(t *testing.T) {
	t.Parallel()
	root := logRoot(t)
	l, _ := openLog(t, root)
	for _, r := range []event.Record{
		{Task: "T-1", Attempt: 1, Step: event.StepGate + ".2", Kind: event.KindIntent, Class: event.ClassGit},
		{Task: "T-4", Step: event.StepGate, Kind: event.KindIntent, Class: event.ClassGit},
		{Task: "T-4", Step: event.StepGate, Kind: event.KindResult}, // its result: not an open intent
		{Task: "T-2", Step: event.StepAfterMerge, Kind: event.KindIntent, Class: event.ClassGit},
		{Task: "T-3", Step: event.StepPublish + ".2", Kind: event.KindIntent, Class: event.ClassGit},
		{Task: event.NightlyTask, Step: event.StepNightly + ".2026-11-01", Kind: event.KindIntent, Class: event.ClassGit},
		{Task: "T-5", Step: event.StepFix, Kind: event.KindIntent, Class: event.ClassPrompt}, // a prompt: no command
		{Task: "T-5", Step: event.StepStop, Kind: event.KindIntent, Class: event.ClassStart}, // a start: no command
	} {
		if _, err := l.Append(r, logNow); err != nil {
			t.Fatal(err)
		}
	}
	open := l.OpenIntents()
	if n := len(open); n != 6 {
		t.Fatalf("precondition: %d open intents, want 6: T-4's gate has its result", n)
	}
	// The gate, the publish and the nightly check run now, each from its own start (not from its intent's write time):
	// T-2's after_merge does not.
	gated, published := logNow.Add(30*time.Second), logNow.Add(2*time.Minute)
	night := logNow.Add(3 * time.Minute)
	p := &proj{exec: map[event.Key]time.Time{
		{Task: "T-1", Attempt: 1, Step: event.StepGate + ".2"}:                         gated,
		{Task: "T-3", Attempt: 0, Step: event.StepPublish + ".2"}:                      published,
		{Task: event.NightlyTask, Attempt: 0, Step: event.StepNightly + ".2026-11-01"}: night,
		{Task: "T-5", Attempt: 0, Step: event.StepStop}:                                published, // a start: no command the line shows
	}, folded: newFolded()}
	want := []wire.RunningStep{
		{Step: event.StepGate, Task: "T-1", Since: gated.Unix()},
		{Step: event.StepPublish, Task: "T-3", Since: published.Unix()},
		{Step: event.StepNightly, Since: night.Unix()},
	}
	got := p.runningSteps(open)
	if len(got) != len(want) {
		t.Fatalf("running steps %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("running step %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if pending := p.pendingSteps(open); len(pending) != 1 || pending[0] != (wire.PendingStep{Step: event.StepAfterMerge,
		Task: "T-2", Since: logNow.Unix()}) {
		t.Errorf("pending steps %+v, want after_merge T-2 queued and nothing else", pending)
	}
	restart := logNow.Add(90 * time.Second)
	p.restarting = restart
	got = p.runningSteps(open)
	if n := len(got); n != len(want)+1 || got[n-1] != (wire.RunningStep{Step: event.StepServeRestart, Since: restart.Unix()}) {
		t.Errorf("with a pending self-restart: %+v, want the three steps then restarting serve", got)
	}
	// Its result in: the gate's step is gone from both lines.
	if _, err := l.Append(event.Record{Task: "T-1", Attempt: 1, Step: event.StepGate + ".2", Kind: event.KindResult}, logNow); err != nil {
		t.Fatal(err)
	}
	open = l.OpenIntents()
	if got, want := p.runningSteps(open), []wire.RunningStep{{Step: event.StepPublish, Task: "T-3", Since: published.Unix()},
		{Step: event.StepNightly, Since: night.Unix()}, {Step: event.StepServeRestart, Since: restart.Unix()}}; len(got) != len(want) {
		t.Errorf("after the gate's result: %+v, want its step gone: %+v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("after the gate's result: step %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	}
	if got := p.pendingSteps(open); len(got) != 1 || got[0].Step != event.StepAfterMerge {
		t.Errorf("after the gate's result: pending %+v, want only after_merge T-2", got)
	}
}

// T-662: a pending step names its last failure (the fold's reason without the step's prefix, and the fact's time) and
// reads queued when it never failed; an intent written before a restart is pending until its effect starts, then
// running, elapsed from that start — not from the intent's older time.
func TestPendingStepsWaitForTheirEffect(t *testing.T) {
	t.Parallel()
	root := logRoot(t)
	l, _ := openLog(t, root)
	failedAt := logNow.Add(time.Minute)
	for _, r := range []event.Record{
		{Task: "T-2", Step: event.StepAfterMerge, Kind: event.KindIntent, Class: event.ClassGit}, // written before the restart
		{Task: event.NightlyTask, Step: event.StepNightly + ".2026-11-01", Kind: event.KindIntent, Class: event.ClassGit},
		{Task: "T-6", Step: event.StepGate, Kind: event.KindIntent, Class: event.ClassGit}, // replayed: no failure recorded for it
	} {
		if _, err := l.Append(r, logNow); err != nil {
			t.Fatal(err)
		}
	}
	p := &proj{exec: map[event.Key]time.Time{}, folded: newFolded()}
	p.folded.loop["T-2"] = &mtask{task: "T-2"}
	p.foldLoop(event.Record{Task: "T-2", Step: event.StepFailed, Kind: event.KindFact, TS: failedAt.Format(time.RFC3339),
		Data: mustJSON(event.FactData{Merge: true, Step: event.StepAfterMerge, Reason: "after_merge: git I/O: index.lock exists"})})
	p.foldNightly(event.Record{Task: event.NightlyTask, Step: event.StepFailed, Kind: event.KindFact, TS: failedAt.Format(time.RFC3339),
		Data: mustJSON(event.FactData{Machine: Machine, Step: event.StepNightly + ".2026-11-01",
			Reason: event.StepNightly + ".2026-11-01: worktree add: locked"})})
	open := l.OpenIntents()
	want := []wire.PendingStep{
		{Step: event.StepAfterMerge, Task: "T-2", Since: logNow.Unix(), Error: "git I/O: index.lock exists", FailedAt: failedAt.Unix()},
		{Step: event.StepNightly, Since: logNow.Unix(), Error: "worktree add: locked", FailedAt: failedAt.Unix()},
		{Step: event.StepGate, Task: "T-6", Since: logNow.Unix()},
	}
	got := p.pendingSteps(open)
	if len(got) != len(want) {
		t.Fatalf("pending steps %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pending step %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got := p.runningSteps(open); len(got) != 0 {
		t.Fatalf("running steps %+v, want none: no effect runs yet", got)
	}
	// T-2's after_merge runs now, three minutes after its intent: running from that start, out of the pending line.
	started := logNow.Add(3 * time.Minute)
	p.exec[event.Key{Task: "T-2", Attempt: 0, Step: event.StepAfterMerge}] = started
	if got, want := p.runningSteps(open), []wire.RunningStep{{Step: event.StepAfterMerge, Task: "T-2", Since: started.Unix()}}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("running steps %+v, want after_merge T-2 from %d (its intent's time is %d)", got, started.Unix(), logNow.Unix())
	}
	if got := p.pendingSteps(open); len(got) != len(want)-1 || got[0] != want[1] {
		t.Errorf("pending steps %+v, want after_merge gone and the rest", got)
	}
}

// T-1022: a task's status carries its spec's Ref: line as ref (obor status --json only); a task whose spec has none
// ships no ref key at all.
func TestStatusCarriesTheSpecsRef(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj"}`)
	h.connect()
	for _, task := range []string{"T-1", "T-2"} {
		addRecord(t, h, task, event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"),
			Agent: "pj-l-01", Branch: "task/" + task, Target: "b1"})
	}
	if _, _, err := store.SetSpec(h.a.p.log.DB(), "T-1", "# T-1 — A\n\nDifficulty: medium\nRef: JIRA-12\n", "", "lead",
		h.now); err != nil {
		t.Fatalf("precondition: set T-1's spec: %v", err)
	}
	seen := map[string]string{}
	for _, ts := range h.a.Status().Project.Tasks {
		raw, err := json.Marshal(ts)
		if err != nil {
			t.Fatalf("marshal %s: %v", ts.Task, err)
		}
		seen[ts.Task] = string(raw)
	}
	for _, c := range []struct {
		task string
		ref  bool
	}{
		{"T-1", true},
		{"T-2", false},
	} {
		raw, ok := seen[c.task]
		if !ok {
			t.Fatalf("precondition: %s is not in the status", c.task)
		}
		if got := strings.Contains(raw, `"ref":`); got != c.ref {
			t.Errorf("%s's status has a ref key %v, want %v: %s", c.task, got, c.ref, raw)
		}
	}
	if raw := seen["T-1"]; !strings.Contains(raw, `"ref":"JIRA-12"`) {
		t.Errorf("T-1's status %s, want ref JIRA-12", raw)
	}
}
