package app

import (
	"context"
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

// T-657: the running line's steps are the open intents of the merge loop's command steps, oldest first — a gate whose
// effect runs now (its intent is open while it runs, not only after a crash), an after_merge, a numbered publish, the
// nightly check — and a pending self-restart last. A step with a result, a prompt and a start are no running step.
func TestRunningStepsAreTheOpenCommandIntents(t *testing.T) {
	root := logRoot(t)
	l, _ := openLog(t, root)
	started, release, errc := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() { // a gate running now: App.do writes the intent, runs the effect, writes the result
		_, err := l.Do(context.Background(), Intent{Key: Key{"T-1", 1, StepGate + ".2"}, Class: ClassGit},
			func() time.Time { return logNow }, func(context.Context) (any, error) {
				close(started)
				<-release
				return nil, nil
			})
		errc <- err
	}()
	<-started
	for _, r := range []Record{
		{Task: "T-4", Step: StepGate, Kind: KindIntent, Class: ClassGit},
		{Task: "T-4", Step: StepGate, Kind: KindResult}, // its result: not running
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
	open, pending := len(l.OpenIntents()), len(l.Pending())
	if open != 6 || pending != 5 || open-pending != 1 {
		t.Fatalf("precondition: %d open intents, %d pending; want 6 and 5: the running gate's intent is open but not pending",
			open, pending)
	}
	want := []RunningStep{
		{Step: StepGate, Task: "T-1", Since: logNow.Unix()},
		{Step: StepAfterMerge, Task: "T-2", Since: logNow.Unix()},
		{Step: StepPublish, Task: "T-3", Since: logNow.Unix()},
		{Step: StepNightly, Since: logNow.Unix()},
	}
	got := runningSteps(l.OpenIntents(), time.Time{})
	if len(got) != len(want) {
		t.Fatalf("running steps %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("running step %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	restart := logNow.Add(90 * time.Second)
	got = runningSteps(l.OpenIntents(), restart)
	if n := len(got); n != len(want)+1 || got[n-1] != (RunningStep{Step: StepServeRestart, Since: restart.Unix()}) {
		t.Errorf("with a pending self-restart: %+v, want the four steps then restarting serve", got)
	}
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if got := runningSteps(l.OpenIntents(), time.Time{}); len(got) != len(want)-1 {
		t.Errorf("after the gate's result: %+v, want its step gone", got)
	}
}
