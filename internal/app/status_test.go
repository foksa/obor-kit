package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// RV-49 #4: a waiting task has no lane, so the drop its needs-you advertises names the task (obor drop <T-xx>, T-462),
// whichever dependency ended the wait.
func TestAnswerPathWaitingTaskNamesTheTask(t *testing.T) {
	for _, reason := range []string{
		"after: T-8 waits for T-1, which is dropped",
		"after: T-8 waits for T-1, which is rejected",
	} {
		raw, _ := json.Marshal(FactData{Merge: true, Cause: causeAfter, Reason: reason})
		r := Record{Seq: 1, Task: "T-8", Step: StepNeedsYou, Data: raw}
		path := answerPath(r, mtask{task: "T-8", state: stNeedsYou, left: stWaiting, needs: r})
		if strings.Contains(path, "<LANE>") {
			t.Errorf("%q: path %q names <LANE>, want the task", reason, path)
		}
		if !strings.Contains(path, `obor drop T-8 "<why>"`) {
			t.Errorf("%q: path %q, want obor drop T-8", reason, path)
		}
	}
}

// A stalled worker's needs-you keeps its lane in the drop command.
func TestAnswerPathLaneKeepsTheLane(t *testing.T) {
	raw, _ := json.Marshal(FactData{Lane: "O-02", Reason: "stalled"})
	path := answerPath(Record{Task: "T-8", Step: StepNeedsYou, Data: raw}, mtask{})
	if !strings.Contains(path, `obor drop O-02 "<why>"`) {
		t.Errorf("path %q, want obor drop O-02", path)
	}
}

// T-657/T-662: the running line's steps are the merge loop's command steps whose effect runs now (p.exec), in seq
// order — a numbered gate, a numbered publish, the nightly check — and a pending self-restart last, elapsed from the
// start of each execution. An open intent whose effect does not run now is pending, not running; an intent with a
// result, a prompt and a start are neither.
func TestRunningStepsAreTheCommandStepsWhoseEffectRuns(t *testing.T) {
	root := logRoot(t)
	l, _ := openLog(t, root)
	for _, r := range []Record{
		{Task: "T-1", Attempt: 1, Step: StepGate + ".2", Kind: KindIntent, Class: ClassGit},
		{Task: "T-4", Step: StepGate, Kind: KindIntent, Class: ClassGit},
		{Task: "T-4", Step: StepGate, Kind: KindResult}, // its result: not an open intent
		{Task: "T-2", Step: StepAfterMerge, Kind: KindIntent, Class: ClassGit},
		{Task: "T-3", Step: StepPublish + ".2", Kind: KindIntent, Class: ClassGit},
		{Task: NightlyTask, Step: StepNightly + ".2026-11-01", Kind: KindIntent, Class: ClassGit},
		{Task: "T-5", Step: StepFix, Kind: KindIntent, Class: ClassPrompt}, // a prompt: no command
		{Task: "T-5", Step: StepStop, Kind: KindIntent, Class: ClassStart}, // a start: no command
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
	p := &proj{exec: map[Key]time.Time{
		{"T-1", 1, StepGate + ".2"}:                   gated,
		{"T-3", 0, StepPublish + ".2"}:                published,
		{NightlyTask, 0, StepNightly + ".2026-11-01"}: night,
		{"T-5", 0, StepStop}:                          published, // a start: no command the line shows
	}, folded: newFolded()}
	want := []RunningStep{
		{Step: StepGate, Task: "T-1", Since: gated.Unix()},
		{Step: StepPublish, Task: "T-3", Since: published.Unix()},
		{Step: StepNightly, Since: night.Unix()},
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
	if pending := p.pendingSteps(open); len(pending) != 1 || pending[0] != (PendingStep{Step: StepAfterMerge,
		Task: "T-2", Since: logNow.Unix()}) {
		t.Errorf("pending steps %+v, want after_merge T-2 queued and nothing else", pending)
	}
	restart := logNow.Add(90 * time.Second)
	p.restarting = restart
	got = p.runningSteps(open)
	if n := len(got); n != len(want)+1 || got[n-1] != (RunningStep{Step: StepServeRestart, Since: restart.Unix()}) {
		t.Errorf("with a pending self-restart: %+v, want the three steps then restarting serve", got)
	}
	// Its result in: the gate's step is gone from both lines.
	if _, err := l.Append(Record{Task: "T-1", Attempt: 1, Step: StepGate + ".2", Kind: KindResult}, logNow); err != nil {
		t.Fatal(err)
	}
	open = l.OpenIntents()
	if got, want := p.runningSteps(open), []RunningStep{{Step: StepPublish, Task: "T-3", Since: published.Unix()},
		{Step: StepNightly, Since: night.Unix()}, {Step: StepServeRestart, Since: restart.Unix()}}; len(got) != len(want) {
		t.Errorf("after the gate's result: %+v, want its step gone: %+v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("after the gate's result: step %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	}
	if got := p.pendingSteps(open); len(got) != 1 || got[0].Step != StepAfterMerge {
		t.Errorf("after the gate's result: pending %+v, want only after_merge T-2", got)
	}
}

// T-662: a pending step names its last failure (the fold's reason without the step's prefix, and the fact's time) and
// reads queued when it never failed; an intent written before a restart is pending until its effect starts, then
// running, elapsed from that start — not from the intent's older time.
func TestPendingStepsWaitForTheirEffect(t *testing.T) {
	root := logRoot(t)
	l, _ := openLog(t, root)
	failedAt := logNow.Add(time.Minute)
	for _, r := range []Record{
		{Task: "T-2", Step: StepAfterMerge, Kind: KindIntent, Class: ClassGit}, // written before the restart
		{Task: NightlyTask, Step: StepNightly + ".2026-11-01", Kind: KindIntent, Class: ClassGit},
		{Task: "T-6", Step: StepGate, Kind: KindIntent, Class: ClassGit}, // replayed: no failure recorded for it
	} {
		if _, err := l.Append(r, logNow); err != nil {
			t.Fatal(err)
		}
	}
	p := &proj{exec: map[Key]time.Time{}, folded: newFolded()}
	p.folded.loop["T-2"] = &mtask{task: "T-2"}
	p.foldLoop(Record{Task: "T-2", Step: StepFailed, Kind: KindFact, TS: failedAt.Format(time.RFC3339),
		Data: mustJSON(FactData{Merge: true, Step: StepAfterMerge, Reason: "after_merge: git I/O: index.lock exists"})})
	p.foldNightly(Record{Task: NightlyTask, Step: StepFailed, Kind: KindFact, TS: failedAt.Format(time.RFC3339),
		Data: mustJSON(FactData{Machine: Machine, Step: StepNightly + ".2026-11-01",
			Reason: StepNightly + ".2026-11-01: worktree add: locked"})})
	open := l.OpenIntents()
	want := []PendingStep{
		{Step: StepAfterMerge, Task: "T-2", Since: logNow.Unix(), Error: "git I/O: index.lock exists", FailedAt: failedAt.Unix()},
		{Step: StepNightly, Since: logNow.Unix(), Error: "worktree add: locked", FailedAt: failedAt.Unix()},
		{Step: StepGate, Task: "T-6", Since: logNow.Unix()},
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
	p.exec[Key{"T-2", 0, StepAfterMerge}] = started
	if got, want := p.runningSteps(open), []RunningStep{{Step: StepAfterMerge, Task: "T-2", Since: started.Unix()}}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("running steps %+v, want after_merge T-2 from %d (its intent's time is %d)", got, started.Unix(), logNow.Unix())
	}
	if got := p.pendingSteps(open); len(got) != len(want)-1 || got[0] != want[1] {
		t.Errorf("pending steps %+v, want after_merge gone and the rest", got)
	}
}
