package app

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/outbox"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// TestActivityKeepsAnUnansweredAsk: a worker's activity ends the watchdog facts whose lifecycle activity ends (stalled,
// blocked, exited), never its open question (state model row 12), on the status path (working) and the
// sampled path (a child runs, a transcript write); the question stays after a replay and obor answer still takes it.
func TestActivityKeepsAnUnansweredAsk(t *testing.T) {
	t.Parallel()
	for _, companion := range []string{"stalled", "blocked", "pane exited"} {
		for _, activity := range []string{"working", "child", "transcript"} {
			t.Run(companion+"/"+activity, func(t *testing.T) {
				t.Parallel()
				h, p := askSeed(t)
				if companion != "stalled" {
					h.a.record(p, event.Record{Task: "T-1", Step: event.StepNeedsYou, Kind: event.KindFact}, event.FactData{Lane: "L-01", Reason: companion})
				}
				if companion == "stalled" || activity != "working" { // the sampled path resumes a stall only
					h.tick(11 * time.Minute)
					if !p.has("T-1", event.StepStalled, "idle") {
						t.Fatalf("precondition: no stall in force: %v", h.facts(0))
					}
				}
				if _, err := h.a.ask(p, "T-1", h.lanes.lanes[0], "may I edit x?"); err != nil {
					t.Fatal(err)
				}
				if !p.asks("T-1") || !p.watched("T-1") {
					t.Fatalf("precondition: want the asks and its companion in force: %v", p.folded.active["T-1"])
				}
				switch activity {
				case "working":
					h.status("w1:p1", func(o *port.Pane) { o.Status = "working" })
				case "child":
					h.panes.busy = true
					h.tick(time.Minute)
				case "transcript":
					h.panes.last = h.now.Add(time.Second)
					h.tick(time.Minute)
				}
				if !strings.Contains(strings.Join(h.facts(0), "\n"), "T-1 resumed") {
					t.Fatalf("precondition: the activity recorded no resumed: %v", h.facts(0))
				}
				check := func(when string, p *proj) {
					t.Helper()
					if !p.asks("T-1") {
						t.Errorf("%s: the unanswered question ended: %v", when, p.folded.active["T-1"])
					}
					if p.watched("T-1") {
						t.Errorf("%s: a fact activity ends is still in force: %v", when, p.folded.active["T-1"])
					}
				}
				check("live", p)
				p.log.Close()
				h.a.closeLog()
				p = reloadProj(t, h)
				check("replay", p)
				if _, err := h.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Role: "lead",
					Agent: "pj-lead", CallID: "a1"}, Task: "T-1", Text: "yes, edit x"}); err != nil {
					t.Fatalf("obor answer refused: %v", err)
				}
				if p.asks("T-1") {
					t.Error("the answer left the question in force")
				}
			})
		}
	}
}

// TestResumeKeepsTheAsks: a resumed fact ends the facts it ends but keeps the open question.
func TestResumeKeepsTheAsks(t *testing.T) {
	t.Parallel()
	p := &proj{folded: folded{active: map[string][]event.Record{}}}
	stall := event.Record{Seq: 1, Task: "T-1", Step: event.StepStalled, Kind: event.KindFact, Data: []byte(`{"reason":"idle"}`)}
	ask := event.Record{Seq: 2, Task: "T-1", Step: event.StepNeedsYou, Kind: event.KindFact, Data: []byte(`{"reason":"asks: may I?"}`)}
	p.fold(stall)
	p.fold(ask)
	p.fold(event.Record{Seq: 3, Task: "T-1", Step: event.StepResumed, Kind: event.KindFact})
	if len(p.folded.active["T-1"]) != 1 || p.folded.active["T-1"][0].Seq != 2 {
		t.Errorf("facts in force %v, want the asks only", p.folded.active["T-1"])
	}
}

// closedCases is each way a task closes, live and on replay: the project and its task.
var closedCases = []struct {
	name, proj, task, end string
	live                  bool
}{
	{"live merged", "pq", "T-3", event.StepMerged, true},
	{"live dropped", "pq", "T-3", event.StepDropped, true},
	{"live rejected", "pr", "T-4", stRejected, true},
	{"replay merged", "pq", "T-3", event.StepMerged, false},
	{"replay dropped", "pq", "T-3", event.StepDropped, false},
	{"replay rejected", "pr", "T-4", stRejected, false},
}

// A worker's stop recorded after its task closed (picked before the merge, drop or reject) puts no
// watchdog needs-you in force under the closed task, live or on replay, nor does a watchdog's stalled fact: no lead
// line and nothing for the needs rule to toast. Both of an open task still do.
func TestStopAfterCloseNoFact(t *testing.T) {
	t.Parallel()
	for _, c := range append(closedCases, struct {
		name, proj, task, end string
		live                  bool
	}{"open", "pq", "T-3", "", true}) {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			a, p := r.app(c.proj), r.proj(c.proj)
			if c.end != "" {
				r.closeTask(c.proj, c.task, c.end, c.live)
			}
			d := outbox.Stopped{Agent: c.proj + "-o-0x", Error: "rate_limit",
				NeedsYou: &event.FactData{Lane: "O-0x", Reason: "stopped: rate_limit"}, Message: event.Message{Task: c.task}}
			if !a.record(p, event.Record{Task: event.MsgTask, Step: event.StepStopped, Kind: event.KindFact}, d) {
				t.Fatal("the stop was not recorded")
			}
			if r.count(c.proj, event.MsgTask, event.KindFact, event.StepStopped) != 1 {
				t.Fatal("precondition: the stopped fact is not in the log")
			}
			// a watchdog's own fact decided before the close, recorded after it (fold)
			if !a.record(p, event.Record{Task: c.task, Attempt: 1, Step: event.StepStalled, Kind: event.KindFact},
				event.FactData{Lane: "O-0x", Reason: "no activity"}) {
				t.Fatal("the stalled fact was not recorded")
			}
			if !c.live {
				a, p = r.reload(c.proj)
			}
			a.mu.Lock()
			facts := len(p.folded.active[c.task])
			a.mu.Unlock()
			lines := 0
			for _, m := range needsMessages(t, p) {
				if strings.Contains(m.Line, "stopped: rate_limit") {
					lines++
				}
			}
			if c.end == "" {
				if facts != 2 {
					t.Errorf("open task: watchdog facts %d; want the stop and the stall", facts)
				}
				return
			}
			if facts != 0 || lines != 0 {
				t.Errorf("after %s: watchdog facts %d, lead lines %d; want none", c.end, facts, lines)
			}
		})
	}
}

// The old case: a closed task's merge-loop needs-you in force (an after-merge step's, a reject's) posts its
// lead line and toasts the owner once, live and on replay.
func TestClosedTaskMergeNeedsYouLine(t *testing.T) {
	t.Parallel()
	for _, c := range closedCases {
		if c.end == event.StepDropped { // the loop runs no step of a dropped task: it has none
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			a, p := r.app(c.proj), r.proj(c.proj)
			if p.out.Lead() == "" || !p.merging {
				t.Fatal("precondition: no lead agent or no merge loop: the needs rule lines nothing")
			}
			r.closeTask(c.proj, c.task, c.end, c.live)
			a.mu.Lock()
			mt := *p.folded.loop[c.task]
			a.mu.Unlock()
			if !a.needsYou(p, &mt, "unpin"+failedSuffix, "unpin failed: busy") {
				t.Fatal("the merge-loop needs-you was not recorded")
			}
			a.mu.Lock()
			needs := p.folded.loop[c.task].needs.fact
			a.mu.Unlock()
			if needs.Seq == 0 {
				t.Fatal("precondition: the merge-loop needs-you is not in force")
			}
			if !c.live {
				a, p = r.reload(c.proj)
			}
			a.rulePass(context.Background(), []rule{{name: ruleNeeds, when: needsRule}}, &Loop{})
			lines := 0
			for _, m := range needsMessages(t, p) {
				if m.Of == (event.Of{Kind: ruleNeeds, Seq: needsKey(needs.Seq, 0)}) && strings.Contains(m.Line, "unpin failed: busy") {
					lines++
				}
			}
			if lines != 1 {
				t.Errorf("lead lines of the merge-loop needs-you: %d, want 1", lines)
			}
			if notes := a.panes.(*fakePanes).notes; len(notes) != 1 {
				t.Errorf("notifications shown: %v, want one", notes)
			}
		})
	}
}

// A record that ends T-1 (an outside merge, a drop's dropping, a reject verdict's result) races
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
			_, err := h.a.drop(wire.DropReq{Envelope: wire.Envelope{Root: h.root, Role: "lead", Agent: "pj-lead",
				CallID: "d1"}, Task: "T-1", Why: "not needed"})
			return err
		}},
		{"reject verdict vs submitted", stRejected, seedHeldReview, func(h *harness, p *proj) error {
			_, err := h.a.verdict(context.Background(), wire.VerdictReq{Envelope: wire.Envelope{Root: h.root,
				Role: "integrator", Agent: "pj-int", CallID: "rj"}, Verb: wire.MethodReject, Task: "T-1",
				Job: "T-1/1/review", Note: "wrong approach"})
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

// An effect whose result's write failed keeps it in p.unwritten; the retry appends that result and
// folds it in doBegin's one section (the fold stays inside the relock). A task_done right after that section
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
		if _, err := h.a.taskDone(wire.TaskDoneReq{Envelope: wire.Envelope{Root: h.root, Role: "worker",
			Agent: "pj-l-01", CallID: "s1"}, Lane: "L-01", Task: "T-1",
			Report: "Summary: x\nSelf-check: x\nSkipped: x\nOpen questions: none."}); err != nil {
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
	if _, err := h.a.verdict(context.Background(), wire.VerdictReq{Envelope: wire.Envelope{Root: h.root,
		Role: "integrator", Agent: "pj-int", CallID: "h1"}, Verb: wire.MethodHold, Task: "T-1", Job: "T-1/1/review",
		Note: "owner decides"}); err != nil {
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
// task_done's answer when it holds no a.mu (the boundary where task_done's record folds first); under
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
	_, err := h.a.taskDone(wire.TaskDoneReq{Envelope: wire.Envelope{Root: h.root, Role: "worker", Agent: "pj-l-01",
		CallID: "s1"}, Lane: "L-01", Task: "T-1",
		Report: "Summary: x\nSelf-check: x\nSkipped: x\nOpen questions: none."})
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
