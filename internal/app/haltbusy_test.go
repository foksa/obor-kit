package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"slices"
	"strings"
	"testing"
)

// T-855 (RV-113 #1): a task whose move's stop of its worker is pending (recorded, not settled) is busy for every other
// verb: a drop is refused, "retry when it settles". A stop that failed (its needs-you) is settled, and a drop then
// stops the halt's recorded agent (the old worker) before it frees the lane. The rig: newLoopRig(t, working()) (T-1 working on L-01, its worker pj-l-01 in w1:p1).

// dropCall is obor drop of T-1 by the lead (its reply or refusal).
func (r *loopRig) dropCall(callID, why string) (string, error) {
	return wire.Verdict(context.Background(), r.sock, &wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", Agent: "pj-lead", CallID: callID}, Task: "T-1", Why: why})
}

// refusedWhileStopPending: verb's call (T-1, its stop pending, not running: cutStops) is refused busy, naming the
// pending worker.stop of pj-l-01, and records nothing. It is called until a call lands between two tries of the stop
// (the one running is busy too, as a held step); any call accepted fails the test.
func (r *loopRig) refusedWhileStopPending(verb string, call func() (string, error)) {
	r.t.Helper()
	want := verb + " T-1: T-1 has worker.stop of pj-l-01 pending; retry when it settles"
	r.waitFor(verb+" refused for the pending stop", func() bool {
		reply, err := call()
		if err == nil {
			r.t.Fatalf("%s while the stop is pending: accepted (%q); want refused busy", verb, reply)
		}
		if !strings.Contains(err.Error(), "retry when it settles") {
			r.t.Fatalf("%s while the stop is pending: %v; want refused busy", verb, err)
		}
		return err.Error() == want
	})
	if r.hasPrefix("1 fact dropping") || r.hasPrefix("2 fact dropping") || r.hasPrefix("1 fact submitted") {
		r.t.Errorf("%s while the stop is pending recorded: %q", verb, r.steps())
	}
}

// cutStops makes each stop cut (serve's own stop cut it: no result, its intent pending, tried again on the next
// pass) until the returned settle lets the stops act; tried gets one value per try.
func (r *loopRig) cutStops() (tried <-chan struct{}, settle func()) {
	in := make(chan struct{}, 1)
	r.mu.Lock()
	r.onStop = func() error {
		select {
		case in <- struct{}{}:
		default:
		}
		return context.Canceled
	}
	r.mu.Unlock()
	return in, func() {
		r.mu.Lock()
		r.onStop = nil
		r.mu.Unlock()
	}
}

// intentData is the data of T-1's last intent of step at attempt (nil: none).
func (r *loopRig) intentData(attempt int, step string) map[string]any {
	var d map[string]any
	for _, rec := range r.records() {
		if rec.Kind == event.KindIntent && rec.Attempt == attempt && rec.Step == step {
			d = nil
			_ = json.Unmarshal(rec.Data, &d)
		}
	}
	return d
}

// failStops makes the rig's stops fail (on) or act (off).
func (r *loopRig) failStops(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onStop = nil
	if on {
		r.onStop = func() error { return errors.New("herdr: pane busy") }
	}
}

// dropFrees: the drop answers DROPPING naming stopping, then T-1 is dropped with its lane freed once.
func (r *loopRig) dropFrees(callID, stopping string) {
	r.t.Helper()
	reply, err := r.dropCall(callID, "not needed")
	if want := "DROPPING T-1 from L-01 (not needed; stopping " + stopping + ")"; err != nil || reply != want {
		r.t.Fatalf("drop: %q, %v; want %q", reply, err, want)
	}
	r.waitFor("T-1 dropped", func() bool { return r.stateOf("T-1") == "dropped" })
	if n := r.disp.called("free"); n != 1 {
		r.t.Errorf("lane freed %d times, want once", n)
	}
}

// oldWorkerStoppedFirst: the drop's worker.stop.2 of attempt (the old worker, with force) settled before its
// lane.free, the old worker's stop tried twice, the drive agent never.
func (r *loopRig) oldWorkerStoppedFirst(attempt int) {
	r.t.Helper()
	at := string(rune('0' + attempt))
	if d := r.factData("T-1", event.StepDropping); d["worker"] != "pj-l-01" || d["force"] != true {
		r.t.Errorf("the dropping fact %v; want worker pj-l-01 with force (the halt's)", d)
	}
	if d := r.intentData(attempt, "worker.stop.2"); d["agent"] != "pj-l-01" || d["force"] != true {
		r.t.Errorf("the drop's worker.stop.2 intent %v; want pj-l-01 with force", d)
	}
	if !r.before(at+" result worker.stop.2", at+" intent lane.free") {
		r.t.Errorf("the steps %q; want the old worker's stop settled, then lane.free", r.steps())
	}
	if got := r.workerStops(); !slices.Equal(got, []string{"pj-l-01 w1:p1", "pj-l-01 w1:p1"}) {
		r.t.Errorf("stops %q; want pj-l-01 twice (the failed one, the drop's), never the drive agent", got)
	}
}

// Move, drop while its stop is pending: refused; the move goes on.
func TestMoveDropWhileStopPending(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, working())
	tried, settle := r.cutStops()
	if reply, err := r.moveTo("m1", "opus"); err != nil || !strings.HasPrefix(reply, "MOVING T-1 on L-01") {
		t.Fatalf("move: %q, %v", reply, err)
	}
	awaitStop(t, tried)
	if ts := r.task("T-1"); ts.Stopping != "pj-l-01" || ts.Attempt != 2 || r.has("2 result worker.stop") {
		t.Fatalf("precondition: T-1 %+v, steps %q; want attempt 2 stopping pj-l-01, no stop result", ts, r.steps())
	}
	r.refusedWhileStopPending("drop", func() (string, error) { return r.dropCall("x1", "not needed") })
	settle()
	r.waitJob("pj-l-01", "T-1/2/work")
	if r.disp.called("free") != 0 || r.stateOf("T-1") == "dropped" {
		t.Errorf("the refused drop freed or dropped T-1: %q", r.steps())
	}
}

// Move whose stop fails (needs-you), then drop: the old worker stopped (the halt's agent, with force), then the lane
// freed; the move's start never runs.
func TestMoveStopFailedThenDrop(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, working())
	r.failStops(true)
	if reply, err := r.moveTo("m1", "opus"); err != nil || !strings.HasPrefix(reply, "MOVING T-1 on L-01") {
		t.Fatalf("move: %q, %v", reply, err)
	}
	r.waitFor("the stop's needs-you", func() bool { return r.task("T-1").Phase == wire.PhaseNeedsYou })
	// the refused stop records its needs-you inside its step, which holds T-1 ("the drive") until it returns: a drop
	// in that instant is refused busy (T-907's sendback)
	r.waitFor("the refused stop's step ended", func() bool { return app.Settled(r.app, r.p.Root, "T-1") })
	r.failStops(false)
	r.dropFrees("x1", "pj-l-01")
	r.oldWorkerStoppedFirst(2)
	if r.has("2 intent agent.start") || len(r.workerLaunches()) != 1 {
		t.Errorf("the move's start ran: %q, %d launches", r.steps(), len(r.workerLaunches()))
	}
}
