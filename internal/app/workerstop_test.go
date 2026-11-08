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

// T-758 (RV-93 #1), T-852: obor drop answers at once; the merge loop's worker.stop stops the task's worker through
// serve's identity of it (stopAgent, as the post-merge stop), never by a label lookup of the stop's own. One case
// table: the worker's pane as serve resolves it before the call, what the stop does, and its outcome. A stop that fails
// is a needs-you (RV-98 #2: else a live agent stays in the lane): nothing goes on until obor resume.
func TestWorkerStopByIdentity(t *testing.T) {
	t.Parallel()
	type outcome = stopOutcome
	for _, c := range []struct {
		name  string
		where string           // whereIs before the call (its prefix)
		arm   func(r *loopRig) // the state before the call
		want  outcome
	}{{
		name:  "live labelled worker: stopped by pane",
		where: "identified w1:p1",
		arm:   func(r *loopRig) {},
		want:  outcome{ok: true, stops: []string{"pj-l-01 w1:p1"}},
	}, {
		name:  "live unlabelled worker: stopped by pane, not not running",
		where: "identified w1:p1 session",
		arm: func(r *loopRig) {
			r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Label = "" })
			r.paneCreated("w1:p1", "")
		},
		want: outcome{ok: true, stops: []string{"pj-l-01 w1:p1"}},
	}, {
		name:  "worker gone: not running, the next step goes on",
		where: "unavailable",
		arm: func(r *loopRig) {
			r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status, p.Transcript = "", "", "", "" })
		},
		want: outcome{ok: true},
	}, {
		name:  "ambiguous: refused, nothing stopped",
		where: "ambiguous w1:p1 w1:p3",
		arm: func(r *loopRig) {
			var transcript string
			r.f.Set("w1:p1", func(p *herdrtest.Pane) { transcript = p.Transcript })
			r.f.Agent("w1:p3", "pj-l-01", "idle", r.lane)
			r.f.Set("w1:p3", func(p *herdrtest.Pane) { p.Label, p.Transcript = "pj-l-01", transcript })
			r.paneCreated("w1:p3", "pj-l-01")
		},
		want: outcome{why: "pj-l-01 not stopped: not identified: panes w1:p1 and w1:p3 run pj-l-01"},
	}, {
		name:  "herdr disconnected: nothing stopped, the stop waits",
		where: "unavailable not connected",
		arm: func(r *loopRig) {
			r.srv.Fail("session.snapshot", "unavailable", "herdr is down")
			r.srv.DropConns()
		},
		want: outcome{waits: true},
	}, {
		name:  "the stop of an identified pane fails: refused",
		where: "identified w1:p1",
		arm:   func(r *loopRig) { r.onStop = func() error { return errors.New("herdr: pane busy") } },
		want: outcome{why: "pj-l-01 not stopped: herdr: pane busy; obor resume T-1 retries",
			stops: []string{"pj-l-01 w1:p1"}},
	}, {
		name:  "the stop says not found while identity still finds the worker: refused",
		where: "identified w1:p1",
		arm:   func(r *loopRig) { r.onStop = func() error { return notFound{} } },
		want: outcome{why: "pj-l-01 not stopped: agent_not_found: pj-l-01; obor resume T-1 retries",
			stops: []string{"pj-l-01 w1:p1"}},
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t, working())
			r.waitFor("pj-l-01 identified at w1:p1 (labelled)", func() bool {
				return strings.HasPrefix(app.WhereOf(r.app, r.p.Root, "pj-l-01"), "identified w1:p1")
			})
			r.mu.Lock()
			c.arm(r)
			r.mu.Unlock()
			if !strings.HasSuffix(c.where, "not connected") {
				// Wait for serve's stream after the arm's change, so the stop decides on it and never lands in a
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
			checkStop(t, r, c.want, len(r.workerStops()))
		})
	}
}

// stopOutcome is the worker.stop's outcome in TestWorkerStopByIdentity.
type stopOutcome struct {
	ok    bool     // the stop done (stopped, or no agent): the drop goes on to dropped
	waits bool     // serve is cut off from herdr: no pass, so the stop waits (nothing stopped, no needs-you)
	why   string   // a refused stop's needs-you reason (its prefix): its line is the lead's one (no stop line)
	stops []string // the worker's Agents.Stop calls since the call: "<agent> <pane>"
}

// checkStop runs obor drop T-1 (T-775: serve frees its lane L-01), answered at once, and checks the merge loop's
// worker.stop: done (the drop dropped), waiting, or refused (a needs-you, the task still dropping, its stop pending).
func checkStop(t *testing.T, r *loopRig, want stopOutcome, before int) {
	t.Helper()
	reply, err := wire.Verdict(context.Background(), r.sock, wire.MethodDrop, wire.Call{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: "x1", Task: "T-1", Note: "not needed"})
	if want := "DROPPING T-1 from L-01 (not needed; stopping pj-l-01)"; err != nil || reply != want {
		t.Fatalf("drop: %q, %v; want %q", reply, err, want)
	}
	switch {
	case want.waits:
		if ts := r.task("T-1"); ts.Stopping != "pj-l-01" || ts.Phase == wire.PhaseNeedsYou {
			t.Errorf("T-1 %+v with serve cut off; want its stop pending, no needs-you", ts)
		}
	case want.ok:
		r.waitFor("T-1 dropped", func() bool { return r.stateOf("T-1") == "dropped" })
	default:
		r.waitFor("the stop's needs-you", func() bool { return r.task("T-1").Phase == wire.PhaseNeedsYou })
		if ts := r.task("T-1"); ts.Stopping != "pj-l-01" || r.has("1 fact dropped") {
			t.Errorf("refused, yet T-1 %+v (dropped %t): want its stop pending", ts, r.has("1 fact dropped"))
		}
		if why := r.needsYou().Fact().Reason; !strings.HasPrefix(why, want.why) {
			t.Errorf("needs-you %q, want %q", why, want.why)
		}
	}
	if stops := r.workerStops()[before:]; !slices.Equal(stops, want.stops) {
		t.Errorf("worker stops %q, want %q", stops, want.stops)
	}
	if !want.waits { // cut off from herdr, serve makes no pass
		r.passes()
	}
	if lines := r.stopLines(); len(lines) != 0 { // T-943: a done stop sends none, a refused one has its needs-you's
		t.Errorf("the lead's lines %q, want none", lines)
	}
}
