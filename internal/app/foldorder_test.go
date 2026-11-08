package app

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// T-944 (RV-132 #1): a record that ends T-1 (an outside merge, a drop's dropping, a reject verdict's result) races
// task_done's submitted: task_done decides first (its checks pass), the other writer appends before task_done's record
// and is held at its fold (hookAppended). Live, the folds follow the log's order as replay does: the live state and the
// task_done receipt equal replay's, and task_done answers superseded iff replay has no receipt.
func TestRecordFoldsInLogOrder(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		end   string // T-1's state after the race, live and replayed
		seed  func(t *testing.T, h *harness, p *proj)
		other func(h *harness, p *proj) error
	}{
		{"outside merged vs submitted", stMerged, seedWorking, func(h *harness, p *proj) error {
			h.a.drive(context.Background(), p, &Loop{Workflow: WorkflowOf(landedScratch{})}, "T-1")
			return nil
		}},
		{"dropping vs submitted", stDropping, seedWorking, func(h *harness, p *proj) error {
			_, err := h.a.drop(wire.Call{Root: h.root, Role: "lead", Agent: "pj-lead", CallID: "d1", Task: "T-1", Note: "not needed"})
			return err
		}},
		{"reject verdict vs submitted", stRejected, seedHeldReview, func(h *harness, p *proj) error {
			_, err := h.a.verdict(context.Background(), wire.MethodReject, wire.Call{Root: h.root, Role: "integrator",
				Agent: "pj-int", CallID: "rj", Task: "T-1", Job: "T-1/1/review", Note: "wrong approach"})
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, p, _ := answerSeed(t, reasonAsks+"which file", "", "", true)
			c.seed(t, h, p)
			from := h.lastSeq()
			doneErr, raced := raceSubmit(t, h, func() error { return c.other(h, p) })
			if !raced {
				t.Fatal("precondition: the other record was never appended during task_done's decision")
			}
			if facts := h.facts(from); len(facts) == 0 {
				t.Fatal("precondition: nothing recorded by the race")
			}
			receiptStep := verdictStep(wire.MethodTaskDone, "s1")
			h.a.mu.Lock()
			live := p.folded.loop["T-1"].state
			_, liveReceipt := p.folded.calls[receiptStep]
			h.a.mu.Unlock()
			q := reloadProj(t, h)
			replayed := q.folded.loop["T-1"].state
			_, replayReceipt := q.folded.calls[receiptStep]
			if replayed != c.end {
				t.Fatalf("replay: T-1 %s, want %s (the other record ended it before the submission)", replayed, c.end)
			}
			if live != replayed {
				t.Errorf("live state: T-1 %s, replay %s", live, replayed)
			}
			if liveReceipt != replayReceipt {
				t.Errorf("task_done receipt: live %v, replay %v", liveReceipt, replayReceipt)
			}
			superseded := doneErr != nil && strings.Contains(doneErr.Error(), "superseded")
			if superseded != !replayReceipt {
				t.Errorf("task_done answered %v; replay's receipt %v (superseded iff none)", doneErr, replayReceipt)
			}
		})
	}
}

// T-955 (RV-132 #1): an effect whose result's write failed keeps it in p.unwritten; the retry appends that result and
// folds it in doBegin's one section (T-944 left the fold after do's relock). A task_done right after that section
// appends its submitted fact next, so the live fold drops the start's try as replay does, not onto the submitted task.
func TestRecoveredStartVsSubmission(t *testing.T) {
	t.Parallel()
	h, p, _ := answerSeed(t, reasonAsks+"which file", "", "", true)
	seedWorking(t, h, p)
	refuseOnce(p, event.StepStartWork)
	in := event.Intent{Key: event.Key{Task: "T-1", Attempt: 1, Step: event.StepStartWork}, Class: event.ClassStart,
		Data: startData{Lane: "L-01", Agent: "pj-l-01", Pane: "w1:p1"}}
	runs := 0
	if _, err := h.a.do(context.Background(), p, in, func(context.Context) (any, error) {
		runs++
		return startOut{Line: "started", Agent: "pj-l-01", Pane: "w1:p1"}, nil
	}); err == nil {
		t.Fatal("precondition: the result's write was not refused")
	}
	h.a.mu.Lock()
	_, lost := p.unwritten[in.Key]
	h.a.mu.Unlock()
	if runs != 1 || !lost {
		t.Fatalf("precondition: effect ran %d times, kept unwritten %v; want 1, true", runs, lost)
	}
	armed := true
	hook := func(point string) {
		if point != hookDoBegin || !armed {
			return
		}
		armed = false
		if _, err := h.a.taskDone(wire.Call{Root: h.root, Role: "worker", Agent: "pj-l-01", Lane: "L-01", CallID: "s1",
			Task: "T-1", Report: "Summary: x\nSelf-check: x\nSkipped: x\nOpen questions: none."}); err != nil {
			t.Errorf("task_done: %v", err)
		}
	}
	h.a.hooked.Store(&hook)
	defer func() { none := func(string) {}; h.a.hooked.Store(&none) }()
	if _, err := h.a.do(context.Background(), p, in, func(context.Context) (any, error) {
		t.Error("the recovered result ran its effect again")
		return nil, nil
	}); err != nil {
		t.Fatalf("the retry: %v", err)
	}
	h.a.mu.Lock()
	live, liveStarted := p.folded.loop["T-1"].state, p.folded.loop["T-1"].started
	h.a.mu.Unlock()
	q := reloadProj(t, h)
	replay, replayStarted := q.folded.loop["T-1"].state, q.folded.loop["T-1"].started
	if replay != stSubmitted || replayStarted {
		t.Fatalf("precondition: replay %s started=%v; want submitted, its start's try dropped", replay, replayStarted)
	}
	if live != replay || liveStarted != replayStarted {
		t.Errorf("live %s started=%v, replay %s started=%v", live, liveStarted, replay, replayStarted)
	}
}

// seedWorking: T-1 working (its work job delivered), so an outside merge's evidence is checked (mtask.landed).
func seedWorking(t *testing.T, h *harness, p *proj) {
	t.Helper()
	addRecord(t, h, "T-1", event.StepWork, event.KindIntent, promptData{Agent: "pj-l-01"})
	addRecord(t, h, "T-1", event.StepDelivered, event.KindFact, event.FactData{Job: "T-1/1/work"})
	if st := p.folded.loop["T-1"].state; st != stWorking {
		t.Fatalf("precondition: T-1 %s, want working", st)
	}
}

// seedHeldReview: T-1 submitted, gated green, its review job out and held by the integrator: the paused task takes a
// task_done (the next attempt) and the held job a reject.
func seedHeldReview(t *testing.T, h *harness, p *proj) {
	t.Helper()
	addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01"})
	addRecord(t, h, "T-1", event.StepSubmit, event.KindResult, submitOut{SHA: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outGreen,
		Candidate: port.Candidate{Task: "T-1", SHA: "c", Target: "b", Patch: "p"}})
	addRecord(t, h, "T-1", event.StepReview, event.KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/review", Patch: "p"})
	addRecord(t, h, "T-1", event.StepReview, event.KindResult, "sent")
	if _, err := h.a.verdict(context.Background(), wire.MethodHold, wire.Call{Root: h.root, Role: "integrator",
		Agent: "pj-int", CallID: "h1", Task: "T-1", Job: "T-1/1/review", Note: "owner decides"}); err != nil {
		t.Fatalf("precondition: hold: %v", err)
	}
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	if mt := p.folded.loop["T-1"]; mt.state != stAwaiting || !mt.onHold() || !mt.may(vSubmit) {
		t.Fatalf("precondition: T-1 %s (held %v), want its review job held", mt.state, mt.onHold())
	}
}

// raceSubmit runs T-1's task_done (call id s1) and, at its decision (hookTaskDoneCheck), other in a goroutine:
// task_done records only once other's record is in the log (hookAppended). Held there, other's writer waits for
// task_done's answer when it holds no a.mu (the boundary before T-944, where task_done's record folded first); under
// mu, task_done's record waits for this one's fold. raced: other's record was appended during the decision.
func raceSubmit(t *testing.T, h *harness, other func() error) (error, bool) {
	t.Helper()
	appended, answered := make(chan struct{}), make(chan struct{})
	otherErr := make(chan error, 1)
	var armed, fired atomic.Bool
	var start sync.Once
	wait := func(ch chan struct{}, what string) {
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Errorf("%s never came", what)
		}
	}
	hook := func(point string) {
		switch {
		case point == hookTaskDoneCheck:
			start.Do(func() {
				armed.Store(true)
				go func() { otherErr <- other() }()
				wait(appended, "the other record")
			})
		case point == hookAppended && armed.Load() && fired.CompareAndSwap(false, true):
			close(appended)
			if h.a.mu.TryLock() { // this writer holds no mu between its append and its fold
				h.a.mu.Unlock()
				wait(answered, "task_done's answer")
			}
		}
	}
	h.a.hooked.Store(&hook)
	_, err := h.a.taskDone(wire.Call{Root: h.root, Role: "worker", Agent: "pj-l-01", Lane: "L-01", CallID: "s1",
		Task: "T-1", Report: "Summary: x\nSelf-check: x\nSkipped: x\nOpen questions: none."})
	close(answered)
	select {
	case e := <-otherErr:
		if e != nil {
			t.Fatalf("the other call: %v", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the other call never answered")
	}
	none := func(string) {}
	h.a.hooked.Store(&none)
	return err, fired.Load()
}
