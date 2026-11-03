package app_test

import (
	"context"
	"errors"
	"obor/internal/app"
	"obor/internal/app/wire"
	"slices"
	"strings"
	"testing"

	"obor/internal/herdr/herdrtest"
)

// T-758 (RV-93 #1): obor drive <T-xx> (a take-over) and obor drop stop the task's worker through serve's identity of it
// (stopAgent, as the post-merge stop), never by a label lookup of the stop's own. One case table, each case run for
// the take-over and for drop: the worker's pane as serve resolves it before the stop, what the stop does, and the
// outcome each caller gives.
func TestWorkerStopByIdentity(t *testing.T) {
	t.Parallel()
	type outcome = stopOutcome
	for _, c := range []struct {
		name  string
		where string           // whereIs before the call (its prefix)
		arm   func(r *loopRig) // the state before the call
		over  outcome          // the take-over's
		drop  outcome          // drop's
	}{{
		name:  "live labelled worker: stopped by pane",
		where: "identified w1:p1",
		arm:   func(r *loopRig) {},
		over:  outcome{ok: true, line: "; STOPPED pj-l-01", stops: []string{"pj-l-01 w1:p1"}},
		drop:  outcome{ok: true, line: "STOPPED pj-l-01", stops: []string{"pj-l-01 w1:p1"}},
	}, {
		name:  "live unlabelled worker: stopped by pane, not not running",
		where: "identified w1:p1 session",
		arm: func(r *loopRig) {
			r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Label = "" })
			r.paneCreated("w1:p1", "")
		},
		over: outcome{ok: true, line: "; STOPPED pj-l-01", stops: []string{"pj-l-01 w1:p1"}},
		drop: outcome{ok: true, line: "STOPPED pj-l-01", stops: []string{"pj-l-01 w1:p1"}},
	}, {
		name:  "worker gone: not running, the take-over records",
		where: "unavailable",
		arm: func(r *loopRig) {
			r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status, p.Transcript = "", "", "", "" })
		},
		over: outcome{ok: true, line: "; pj-l-01 not running"},
		drop: outcome{ok: true},
	}, {
		name:  "ambiguous: refused, nothing stopped",
		where: "ambiguous w1:p1 w1:p3",
		arm: func(r *loopRig) {
			var transcript string
			r.f.Set("w1:p1", func(p *herdrtest.Pane) { transcript = p.Transcript })
			r.f.Agent("w1:p3", "pj-l-01", "idle", r.lane)
			r.f.Set("w1:p3", func(p *herdrtest.Pane) { p.Workspace, p.Label, p.Transcript = "w1", "pj-l-01", transcript })
			r.paneCreated("w1:p3", "pj-l-01")
		},
		over: outcome{line: "pj-l-01 not stopped: not identified: panes w1:p1 and w1:p3 run pj-l-01"},
		drop: outcome{ok: true, line: "worker of L-01 not stopped: not identified: panes w1:p1 and w1:p3 run pj-l-01 " +
			"(close or relabel one)"},
	}, {
		name:  "herdr disconnected: refused, nothing stopped",
		where: "unavailable not connected",
		arm: func(r *loopRig) {
			r.srv.Fail("session.snapshot", "unavailable", "herdr is down")
			r.srv.DropConns()
		},
		over: outcome{line: "pj-l-01 not stopped: serve is not connected to herdr"},
		drop: outcome{ok: true, line: "worker of L-01 not stopped: serve is not connected to herdr"},
	}, {
		name:  "the stop of an identified pane fails: refused",
		where: "identified w1:p1",
		arm:   func(r *loopRig) { r.onStop = func() error { return errors.New("herdr: pane busy") } },
		over:  outcome{line: "pj-l-01 not stopped: herdr: pane busy", stops: []string{"pj-l-01 w1:p1"}},
		drop:  outcome{ok: true, line: "worker of L-01 not stopped: herdr: pane busy", stops: []string{"pj-l-01 w1:p1"}},
	}, {
		name:  "the stop says not found while identity still finds the worker: refused",
		where: "identified w1:p1",
		arm:   func(r *loopRig) { r.onStop = func() error { return notFound{} } },
		over:  outcome{line: "pj-l-01 not stopped: agent_not_found: pj-l-01", stops: []string{"pj-l-01 w1:p1"}},
		drop:  outcome{ok: true, line: "worker of L-01 not stopped: agent_not_found: pj-l-01", stops: []string{"pj-l-01 w1:p1"}},
	}} {
		for _, caller := range []string{"take-over", "drop"} {
			t.Run(caller+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				r := newTakeOverRig(t)
				r.waitFor("pj-l-01 identified at w1:p1 (labelled)", func() bool {
					return strings.HasPrefix(app.WhereOf(r.app, r.p.Root, "pj-l-01"), "identified w1:p1")
				})
				r.mu.Lock()
				c.arm(r)
				r.mu.Unlock()
				if !strings.HasSuffix(c.where, "not connected") {
					// Wait for serve's stream after the arm's change, so the call decides on it and never lands in a
					// resubscribe's gap (T-759). The case whose stream cannot come back (herdr down) has no such wait.
					r.connectedAgain()
				}
				var where string
				// No push here: a push is a refresh, and one landing after the barrier would cut serve off mid-call.
				r.waitFor("whereIs: "+c.where, func() bool { // connected (the barrier above), unless herdr is cut off
					where = app.WhereOf(r.app, r.p.Root, "pj-l-01")
					return strings.HasPrefix(where, c.where) &&
						(r.app.Status().Connected || strings.HasSuffix(c.where, "not connected"))
				})
				if c.where == "identified w1:p1 session" { // the label is not what holds it
					var label string
					r.f.Set("w1:p1", func(p *herdrtest.Pane) { label = p.Label })
					if label != "" {
						t.Fatalf("precondition: w1:p1 still labelled %q", label)
					}
				}
				before := len(r.workerStops())
				if caller == "take-over" {
					checkTakeOver(t, r, c.over, before)
					return
				}
				checkDrop(t, r, c.drop, before)
			})
		}
	}
}

// stopOutcome is a caller's outcome in TestWorkerStopByIdentity.
type stopOutcome struct {
	ok    bool     // take-over: answered (and recorded); drop: its reply
	line  string   // take-over: the reply's suffix, or the refusal's text; drop: a line its reply has
	stops []string // the worker's Agents.Stop calls since the call: "<agent> <pane>"
}

// checkTakeOver runs obor drive T-1 and checks its outcome: answered with the worker stopped (or not running) and fact
// drive recorded, or refused with nothing recorded and T-1 still its worker's.
func checkTakeOver(t *testing.T, r *loopRig, want stopOutcome, before int) {
	t.Helper()
	reply, err := r.drive("v1", "T-1")
	if stops := r.workerStops()[before:]; !slices.Equal(stops, want.stops) {
		t.Errorf("worker stops %q, want %q", stops, want.stops)
	}
	if want.ok {
		if err != nil || !strings.HasPrefix(reply, "DRIVE T-1 → L-01 (pj-l-01-drive, sonnet) taking over from pj-l-01") ||
			!strings.HasSuffix(reply, want.line) {
			t.Fatalf("take-over: %q, %v; want it answered, ending %q", reply, err, want.line)
		}
		r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
		if n := r.count("1 fact drive"); n != 1 {
			t.Errorf("drive facts %d, want 1", n)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), want.line) || !strings.Contains(err.Error(), "nothing recorded") {
		t.Fatalf("take-over: %q, %v; want refused with %q, nothing recorded", reply, err, want.line)
	}
	if r.app.Status().Connected { // the merge loop passes only while connected
		r.passes()
	}
	if r.has("1 fact drive") || r.stateOf("T-1") != "working" {
		t.Errorf("refused, yet a drive fact (%t) or T-1 %s (want working): %q", r.has("1 fact drive"), r.stateOf("T-1"),
			r.steps())
	}
}

// checkDrop runs obor drop L-01 and checks its reply: the worker's stop line (or none) and T-1 dropped.
func checkDrop(t *testing.T, r *loopRig, want stopOutcome, before int) {
	t.Helper()
	reply, err := wire.Verdict(context.Background(), r.sock, wire.MethodDrop, wire.Call{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: "x1", Lane: "L-01", Note: "not needed"})
	if stops := r.workerStops()[before:]; !slices.Equal(stops, want.stops) {
		t.Errorf("worker stops %q, want %q", stops, want.stops)
	}
	if err != nil || !strings.Contains(reply, "DROPPED T-1 from L-01") {
		t.Fatalf("drop: %q, %v; want T-1 dropped", reply, err)
	}
	lines := strings.Split(reply, "\n")
	if want.line != "" && !slices.Contains(lines, want.line) {
		t.Errorf("drop's reply %q, want the line %q", reply, want.line)
	}
	if want.line == "" && len(lines) != 1 { // DROPPED only: no line about the worker
		t.Errorf("drop's reply %q, want no line about the worker", reply)
	}
}
