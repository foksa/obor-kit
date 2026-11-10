package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// A task's step logs go when the task ends (logs.go): merged with its after-merge steps done, or dropped; a
// rejected task is not ended, a merged one still running its after-merge steps is not either. The end's
// transition removes them, and serve's load removes the leftovers of every task it folds as ended. The sweep's
// leftovers are internal/project's tests; these are serve's own ends.

// logsHarness is a merging pj with serve's lines captured (a failed log removal's one line).
func logsHarness(t *testing.T) (*harness, *bytes.Buffer) {
	t.Helper()
	h, out := newIdentHarness(t)
	h.withLead()
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{}, nil }
	h.connect()
	t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
	if !h.proj().merging {
		t.Fatal("precondition: the project does not merge")
	}
	return h, out
}

// logFile writes pj's step log task-<step>.log, as a failed check would.
func logFile(t *testing.T, h *harness, task, step string) string {
	t.Helper()
	dir := filepath.Join(h.root, ".obor", "state", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, task+"-"+step+".log")
	if err := os.WriteFile(p, []byte("FAIL the check\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func logThere(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("no %s: %v", path, err)
	}
}

func logGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s is still there", path)
	}
}

// mergedFacts is task's submitted and merged facts at attempt 1 (its after-merge steps not run yet).
func mergedFacts(t *testing.T, h *harness, task string) {
	t.Helper()
	addRecord(t, h, task, event.StepSubmitted, event.KindFact, submitData{Lane: "L-01"})
	addRecord(t, h, task, event.StepMerged, event.KindFact, event.MergedData{Head: "abcdef0"})
}

// runAfter runs task's after-merge step through App.do (the live path), as the loop does.
func runAfter(t *testing.T, h *harness, task, step string) {
	t.Helper()
	h.now = h.now.Add(time.Minute)
	in := event.Intent{Key: event.Key{Task: task, Attempt: 1, Step: step}}
	if _, err := h.a.do(context.Background(), h.proj(), in, func(context.Context) (any, error) { return "done", nil }); err != nil {
		t.Fatal(err)
	}
}

func TestTaskLogsGoWhenTheTaskEnds(t *testing.T) {
	t.Parallel()
	t.Run("merged, after_merge and stop done: the red logs go", func(t *testing.T) {
		t.Parallel()
		h, _ := logsHarness(t)
		mergedFacts(t, h, "T-1")
		check, after := logFile(t, h, "T-1", "check"), logFile(t, h, "T-1", "after_merge")
		logThere(t, check) // merged alone: its steps still run
		for _, step := range afterSteps {
			runAfter(t, h, "T-1", step)
		}
		logGone(t, check)
		logGone(t, after)
	})
	t.Run("merged, after_merge failed: its logs stay until the stop completes the end", func(t *testing.T) {
		t.Parallel()
		h, _ := logsHarness(t)
		mergedFacts(t, h, "T-2")
		check, after := logFile(t, h, "T-2", "check"), logFile(t, h, "T-2", "after_merge")
		h.now = h.now.Add(time.Minute)
		in := event.Intent{Key: event.Key{Task: "T-2", Attempt: 1, Step: event.StepAfterMerge}}
		if _, err := h.a.do(context.Background(), h.proj(), in, func(context.Context) (any, error) {
			return nil, os.ErrPermission // the step stays pending: the loop retries it
		}); err == nil {
			t.Fatal("the failed after_merge's do reported no error")
		}
		if mt := h.proj().folded.loop["T-2"]; mt.ended() {
			t.Fatalf("precondition: T-2 reads ended (%+v) after a failed after_merge", mt)
		}
		logThere(t, check)
		logThere(t, after)
		runAfter(t, h, "T-2", event.StepStop) // the end's last step: the held task ends and its logs go
		logGone(t, check)
		logGone(t, after)
	})
	t.Run("a lane reused by a new task while the former task's steps are pending: its logs stay", func(t *testing.T) {
		t.Parallel()
		h, _ := logsHarness(t)
		mergedFacts(t, h, "T-8")
		former := logFile(t, h, "T-8", "check")
		addRecord(t, h, "T-9", event.StepDispatched, event.KindFact,
			dispatchData{Dispatch: port.Dispatch{Lane: "L-01", Agent: "pj-l-01", Branch: "task/T-9"}})
		if mt := h.proj().folded.loop["T-8"]; mt == nil || mt.ended() {
			t.Fatalf("precondition: T-8 reads ended (%+v) after L-01 took T-9", mt)
		}
		logThere(t, former) // T-9's dispatch says nothing about T-8's end
		for _, step := range afterSteps {
			runAfter(t, h, "T-8", step)
		}
		logGone(t, former)
	})
	t.Run("dropped: its logs go", func(t *testing.T) {
		t.Parallel()
		h, _ := logsHarness(t)
		check := logFile(t, h, "T-3", "check")
		if _, ok := h.a.recordSeq(h.proj(), event.Record{Task: "T-3", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact},
			event.FactData{Reason: "not needed"}); !ok {
			t.Fatal("the dropped fact was not recorded")
		}
		logGone(t, check)
	})
	t.Run("rejected: its logs stay", func(t *testing.T) {
		t.Parallel()
		h, _ := logsHarness(t)
		addRecord(t, h, "T-4", event.StepDispatched, event.KindFact,
			dispatchData{Dispatch: port.Dispatch{Lane: "L-01", Agent: "pj-l-01", Branch: "task/T-4"}})
		addRecord(t, h, "T-4", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01"})
		addRecord(t, h, "T-4", event.StepGate, event.KindIntent, gateIn{})
		addRecord(t, h, "T-4", event.StepGate, event.KindResult,
			gateOut{Outcome: outGreen, Candidate: port.Candidate{Task: "T-4", SHA: "c", Patch: "p"}})
		addRecord(t, h, "T-4", event.StepReview, event.KindIntent, JobData{Patch: "p"})
		if got := h.proj().folded.loop["T-4"].state; got != stAwaiting {
			t.Fatalf("precondition: T-4 is %s, not awaiting the review", got)
		}
		check := logFile(t, h, "T-4", "check")
		// The reject goes through serve's record path, which is where an end's logs would be removed.
		if _, ok := h.a.recordSeq(h.proj(), event.Record{Task: "T-4", Attempt: 1,
			Step: verdictStep(wire.MethodReject, "j-T-4"), Kind: event.KindResult},
			verdictResult{Job: JobID(event.Key{Task: "T-4", Attempt: 1, Step: event.StepReview}), Note: "not this"}); !ok {
			t.Fatal("the reject was not recorded")
		}
		if got := h.proj().folded.loop["T-4"].state; got != stRejected {
			t.Fatalf("T-4 is %s, not rejected", got)
		}
		logThere(t, check)
	})
	t.Run("T-10 ends, T-100 is open: T-100's logs stay (exact id)", func(t *testing.T) {
		t.Parallel()
		h, _ := logsHarness(t)
		t10, t100 := logFile(t, h, "T-10", "check"), logFile(t, h, "T-100", "check")
		if _, ok := h.a.recordSeq(h.proj(), event.Record{Task: "T-10", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact},
			event.FactData{Reason: "not needed"}); !ok {
			t.Fatal("the dropped fact was not recorded")
		}
		logGone(t, t10)
		logThere(t, t100)
	})
	t.Run("a task dispatched again and ended again: its new logs go too", func(t *testing.T) {
		t.Parallel()
		h, _ := logsHarness(t)
		if _, ok := h.a.recordSeq(h.proj(), event.Record{Task: "T-7", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact},
			event.FactData{Reason: "not needed"}); !ok {
			t.Fatal("the dropped fact was not recorded")
		}
		addRecord(t, h, "T-7", event.StepDispatched, event.KindFact,
			dispatchData{Dispatch: port.Dispatch{Lane: "L-01", Agent: "pj-l-01", Branch: "task/T-7"}})
		mergedFacts(t, h, "T-7")
		check := logFile(t, h, "T-7", "check")
		for _, step := range afterSteps {
			runAfter(t, h, "T-7", step)
		}
		logGone(t, check)
	})
	t.Run("a removal that fails is one serve line, and the task still ends", func(t *testing.T) {
		t.Parallel()
		h, out := logsHarness(t)
		blocked := logFile(t, h, "T-5", "check")
		if err := os.Remove(blocked); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(blocked, "x"), 0o755); err != nil { // a log name that cannot be removed
			t.Fatal(err)
		}
		if _, ok := h.a.recordSeq(h.proj(), event.Record{Task: "T-5", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact},
			event.FactData{Reason: "not needed"}); !ok {
			t.Fatal("the dropped fact was not recorded")
		}
		if mt := h.proj().folded.loop["T-5"]; !mt.ended() {
			t.Errorf("T-5 did not end: %+v", mt)
		}
		if n := strings.Count(out.String(), "logs of T-5"); n != 1 {
			t.Errorf("the failed removal printed %d lines, want 1:\n%s", n, out.String())
		}
		logThere(t, blocked)
	})
}

// TestTaskLogsGoAtLoad: serve's load removes the logs of every task the replayed log folds as ended (what an
// upgrade or a serve down at its end left); an open task's logs and a task the log does not know keep theirs.
// load reads the logs dir once for all of them, not once per ended task.
func TestTaskLogsGoAtLoad(t *testing.T) {
	t.Parallel()
	h, _ := logsHarness(t)
	for _, task := range []string{"T-1", "T-2", "T-3"} {
		if _, ok := h.a.recordSeq(h.proj(), event.Record{Task: task, Attempt: 1, Step: event.StepDropped, Kind: event.KindFact},
			event.FactData{Reason: "not needed"}); !ok {
			t.Fatalf("the drop of %s was not recorded", task)
		}
	}
	addRecord(t, h, "T-4", event.StepDispatched, event.KindFact,
		dispatchData{Dispatch: port.Dispatch{Lane: "L-01", Agent: "pj-l-01", Branch: "task/T-4"}})
	addRecord(t, h, "T-4", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01"})
	endedLogs := []string{
		logFile(t, h, "T-1", "check"),
		logFile(t, h, "T-2", "after_merge"),
		logFile(t, h, "T-3", "stop"),
	}
	open, unknown := logFile(t, h, "T-4", "check"), logFile(t, h, "T-5", "check") // T-5: no record at all
	reads := 0
	h.a.opt.ReadDir = func(dir string) ([]os.DirEntry, error) {
		reads++
		return os.ReadDir(dir)
	}
	p := reloadProj(t, h)
	for _, task := range []string{"T-1", "T-2", "T-3"} {
		if mt := p.folded.loop[task]; mt == nil || !mt.ended() {
			t.Fatalf("precondition: the replayed %s is not ended (%+v)", task, p.folded.loop)
		}
	}
	for _, task := range []string{"T-4", "T-5"} {
		if mt := p.folded.loop[task]; mt != nil && mt.ended() {
			t.Fatalf("precondition: the replayed %s reads ended (%+v)", task, mt)
		}
	}
	for _, path := range endedLogs {
		logGone(t, path)
	}
	logThere(t, open)
	logThere(t, unknown)
	if reads != 1 {
		t.Errorf("load read the logs dir %d times, want 1", reads)
	}
}
