package app_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/herdr/herdrtest"
)

// The plugin's exact StopFailure error shape reaches the OpenCode Reset through both stoppedCall paths: worker
// NeedsYou and a briefed role's roleStop keep the reset; billing_error does not. Claude is the control.
func TestOpenCodeStoppedReset(t *testing.T) {
	t.Parallel()
	const details = "You exceeded your current quota; resets at 6pm (provider.rate-limit)|1793365200"
	for _, c := range []struct {
		name, agentKind, role, errText, details string
		roleAgent, wantLimit                    bool
		wantReset                               int64
	}{
		{"OpenCode worker rate limit", "opencode", "worker", "rate_limit", details, false, true, 1793365200},
		{"OpenCode role rate limit", "opencode", "reviewer", "rate_limit", details, true, true, 1793365200},
		{"OpenCode billing error", "opencode", "worker", "billing_error", "budget spent (provider.budget-limit)", false, false, 0},
		{"Claude worker control", "claude", "worker", "rate_limit", "Claude AI usage limit reached|1793365200", false, true, 1793365200},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				var r *loopRig
				if c.roleAgent {
					r = lineRig(t, "idle", "idle")
					r.f.Set("w1:p6", func(p *herdrtest.Pane) { p.Kind = c.agentKind })
					r.start()
					if _, err := r.line("b1", &wire.BriefReq{To: "pj-rev", Text: "review"}); err != nil {
						t.Fatal(err)
					}
					r.waitFor("role brief posted", func() bool { return r.typedTo("pj-rev", "[obor pj-lead] review") == 1 })
					r.agentIs("w1:p6", "pj-rev", "working")
					r.passes()
				} else {
					r = fetchRig(t)
					r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Kind = c.agentKind })
					r.start()
					r.mustDispatch("d1")
					r.waitStep("1 result work")
					r.workerNext("n1")
					r.waitFor("worker working", func() bool { return r.taskState() == "working" })
				}

				agent, lane := "pj-l-01", "L-01"
				if c.roleAgent {
					agent, lane = "pj-rev", ""
				}
				if _, err := r.stoppedCallWith(agent, c.role, lane, "stop", c.errText, c.details); err != nil {
					t.Fatal(err)
				}
				var stopped struct {
					NeedsYou *event.FactData `json:"needs_you"`
					Brief    int64           `json:"brief"`
					Reset    int64           `json:"reset"`
				}
				for _, rec := range r.records() {
					if rec.Task == event.MsgTask && rec.Kind == event.KindFact && rec.Step == event.StepStopped {
						if err := json.Unmarshal(rec.Data, &stopped); err != nil {
							t.Fatal(err)
						}
					}
				}
				if c.roleAgent {
					if stopped.Brief == 0 || (stopped.Reset != c.wantReset) {
						t.Errorf("role stop = brief %d, reset %d; want an open brief and reset %d", stopped.Brief, stopped.Reset, c.wantReset)
					}
					return
				}
				if stopped.NeedsYou == nil || stopped.NeedsYou.Limit != c.wantLimit || stopped.NeedsYou.Reset != c.wantReset {
					t.Errorf("worker stop needs_you = %+v; want limit %v, reset %d", stopped.NeedsYou, c.wantLimit, c.wantReset)
				}
			})
		})
	}
}

// A briefed role agent whose turn ends on an API error (the StopFailure hook's request) gets fact stopped and
// the lead's line "[obor] <agent> stopped: <error> (<details>)" instead of the brief's finished line; a repeated call
// id gets the stored reply and records nothing. Its next brief (a later turn) ends with the finished line again.
func TestStoppedRoleAgentLine(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "idle")
		r.start()
		if _, err := r.line("b1", &wire.BriefReq{To: "pj-rev", Text: "verify finding 3"}); err != nil {
			t.Fatal(err)
		}
		r.waitFor("the brief posted", func() bool { return r.inbox.count("pj-rev", "[obor pj-lead] verify finding 3") == 1 })
		r.agentIs("w1:p6", "pj-rev", "working")
		r.passes()

		reply, err := r.stoppedCall("pj-rev", "reviewer", "", "s1")
		if err != nil || reply != "STOPPED pj-rev (the lead is told)" {
			t.Fatalf("stopped: %q, %v", reply, err)
		}
		if again, err := r.stoppedCall("pj-rev", "reviewer", "", "s1"); err != nil || again != reply {
			t.Errorf("a repeated call id: %q, %v; want the stored reply", again, err)
		}
		if n := r.taskStep(event.MsgTask, "fact stopped"); n != 1 {
			t.Fatalf("stopped facts %d, want 1: %q", n, r.steps())
		}
		r.agentIs("w1:p6", "pj-rev", "idle")
		const stopped = "[obor] pj-rev stopped: rate_limit (429 Too Many Requests)"
		r.waitFor("the stopped line posted", func() bool { return r.inbox.count("pj-lead", stopped) == 1 })
		r.passes()
		r.passes()
		if n := r.taskStep(event.MsgTask, "fact message"); n != 0 {
			t.Errorf("a finished line besides the stopped one: %q", r.steps())
		}
		for _, p := range r.inbox.posted() {
			if strings.Contains(p, "finished") {
				t.Errorf("posted %q, want no finished line for the stopped brief", p)
			}
		}

		// The agent's next turn: a new brief, worked and idle → its finished line.
		if _, err := r.line("b2", &wire.BriefReq{To: "pj-rev", Text: "go on"}); err != nil {
			t.Fatal(err)
		}
		r.waitFor("the second brief posted", func() bool { return r.inbox.count("pj-rev", "[obor pj-lead] go on") == 1 })
		r.agentIs("w1:p6", "pj-rev", "working")
		r.agentIs("w1:p6", "pj-rev", "idle")
		r.waitFor("the finished line of the next brief", func() bool {
			return r.inbox.count("pj-lead", "[obor] pj-rev finished (briefed 0s ago)") == 1
		})
		if n := r.inbox.count("pj-lead", stopped); n != 1 {
			t.Errorf("stopped line posted %d times, want once", n)
		}
	})
}

// stoppedWorker is the fetch rig with T-1's worker working, then its turn ended on an API error (the stopped call
// s1): the stop is in force.
func stoppedWorker(t *testing.T) *loopRig {
	r := fetchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.workerNext("n1")
	r.waitFor("working", func() bool { return r.taskState() == "working" })
	r.workerIs("working")
	if reply, err := r.stoppedCall("pj-l-01", "worker", "L-01", "s1"); err != nil || reply != "STOPPED pj-l-01 (needs-you T-1)" {
		t.Fatalf("stopped: %q, %v", reply, err)
	}
	if n := r.stopsInForce(); n != 1 {
		t.Fatalf("precondition: stops in force %d, want 1: %+v", n, r.app.Status().Project.Facts)
	}
	return r
}

// A worker whose turn ends on an API error gets one fact stopped carrying its task, and the watchdog's
// needs-you{<agent> stopped: <error> (<details>)} of the task folded from it, posted to the lead as
// "[obor] needs-you T-1 (L-01): …"; its working status does not end it, nor does an idle wait stall it; its next
// turn's hook (the turn request) ends it: fact resumed.
func TestStoppedWorkerNeedsYou(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := stoppedWorker(t)
		if n := r.taskStep(event.MsgTask, "fact stopped"); n != 1 {
			t.Fatalf("stopped facts %d, want 1: %q", n, r.steps())
		}
		if r.hasPrefix("0 fact needs-you") {
			t.Errorf("a second record for the stop: %q, want the needs-you folded from stopped", r.steps())
		}
		const line = "[obor] needs-you T-1 (L-01): " + stopReason
		r.waitFor("the needs-you line posted", func() bool {
			for _, p := range r.inbox.posted() {
				if strings.HasPrefix(p, "pj-lead: "+line) {
					return true
				}
			}
			return false
		})

		r.push()
		r.passes() // herdr still shows the stopped turn working
		r.workerIs("idle")
		r.later(25 * time.Minute)
		r.push()
		r.passes()
		r.later(time.Second)
		r.workerIs("working") // working again: not evidence of a new turn
		r.push()
		r.passes()
		if r.hasPrefix("0 fact stalled") || r.hasPrefix("0 fact resumed") || r.stopsInForce() != 1 {
			t.Fatalf("while stopped: %q, want only the stop in force", r.steps())
		}
		reply, err := r.turnCall()
		if err != nil || reply != "TURN pj-l-01 (the stop of T-1 ended)" {
			t.Fatalf("turn: %q, %v", reply, err)
		}
		if !r.has("0 fact resumed") {
			t.Errorf("no fact resumed after the next turn: %q", r.steps())
		}
		if f := r.app.Status().Project.Facts; len(f) != 0 {
			t.Errorf("facts in force %+v after the next turn, want none", f)
		}
		if again, err := r.turnCall(); err != nil || again != "TURN pj-l-01 (no stop)" || r.count("0 fact resumed") != 1 {
			t.Errorf("a later turn: %q, %v, %d resumed; want nothing recorded", again, err, r.count("0 fact resumed"))
		}
	})
}

// The worker's stop is one record. With every needs-you append refused the stop is in force all the same; after a
// replay (serve cut right after stopped) it is still, and an
// identical retry of the call gets the same reply and records nothing, the needs-you in force once.
func TestStoppedWorkerOneRecordReplay(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := fetchRig(t)
		r.start()
		r.mustDispatch("d1")
		r.waitStep("1 result work")
		r.workerNext("n1")
		r.waitFor("working", func() bool { return r.taskState() == "working" })
		r.workerIs("working")
		app.Refuse(r.app, r.p.Root, func(rec event.Record) error {
			if rec.Step == event.StepNeedsYou {
				return errors.New("needs-you refused")
			}
			return nil
		})

		reply, err := r.stoppedCall("pj-l-01", "worker", "L-01", "s1")
		if err != nil || reply != "STOPPED pj-l-01 (needs-you T-1)" {
			t.Fatalf("stopped: %q, %v", reply, err)
		}
		if n := r.stopsInForce(); n != 1 {
			t.Fatalf("stops in force %d with the needs-you append refused, want 1", n)
		}
		r.stop() // the cut: nothing after stopped
		r.start()
		if n := r.stopsInForce(); n != 1 {
			t.Fatalf("after replay: stops in force %d, want 1: %q", n, r.steps())
		}
		again, err := r.stoppedCall("pj-l-01", "worker", "L-01", "s1")
		if err != nil || again != reply {
			t.Errorf("identical retry: %q, %v; want %q", again, err, reply)
		}
		if n, m := r.taskStep(event.MsgTask, "fact stopped"), r.stopsInForce(); n != 1 || m != 1 {
			t.Errorf("after the retry: %d stopped facts, %d stops in force; want 1, 1: %q", n, m, r.steps())
		}
	})
}

// Serve restarts while herdr shows the stopped turn working (the same snapshot, seen later than the stop): the stop
// stays.
func TestStoppedWorkerRestartSameWorking(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := stoppedWorker(t)
		r.later(5 * time.Second)
		r.stop()
		r.start()
		r.workerIs("working")
		r.later(5 * time.Second)
		r.push()
		r.passes()
		if r.hasPrefix("0 fact resumed") || r.stopsInForce() != 1 {
			t.Fatalf("after a restart on the same working pane: %q, %d stops; want the stop in force", r.steps(),
				r.stopsInForce())
		}
	})
}

// Two turns within one second, the clock not moved: idle, working again, then the next turn's
// UserPromptSubmit (the turn request) ends the stop.
func TestStoppedWorkerNextTurnSameSecond(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := stoppedWorker(t)
		r.workerIs("idle")
		r.workerIs("working")
		r.passes()
		if r.stopsInForce() != 1 {
			t.Fatalf("precondition: the stop in force before the hook: %q", r.steps())
		}
		if reply, err := r.turnCall(); err != nil || reply != "TURN pj-l-01 (the stop of T-1 ended)" {
			t.Fatalf("turn: %q, %v", reply, err)
		}
		if r.stopsInForce() != 0 || !r.has("0 fact resumed") {
			t.Errorf("after the next turn's prompt: %q, %d stops; want resumed, none", r.steps(), r.stopsInForce())
		}
	})
}

func codexStopped(t *testing.T) codexRig {
	return codexStoppedOn(t, cdxLimited, cdxReason)
}

// A Codex worker (no StopFailure hook) whose rollout's last turn ended on its usage limit is read when it goes
// idle with its task open: one fact stopped with the turn, its needs-you{pj-l-01 stopped: usage_limit_exceeded
// (<message>)} folded from it and posted to the lead, as the hook's (while the turn's start was the last event,
// nothing: codexStopped); more idle checks and a replay record nothing more; a later turn's start in the rollout (the
// pane working) ends it: fact resumed, and its clean end records nothing.
func TestStoppedCodexUsageLimit(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := codexStopped(t)
		line := "pj-lead: [obor] needs-you T-1 (L-01): " + r.reason
		r.waitFor("the needs-you line posted", func() bool {
			for _, p := range r.inbox.posted() {
				if strings.HasPrefix(p, line) {
					return true
				}
			}
			return false
		})
		r.later(25 * time.Minute) // idle long after: no stall, no second stop
		r.push()
		r.passes()
		if n := r.taskStep(event.MsgTask, "fact stopped"); n != 1 || r.inForce() != 1 || r.hasPrefix("0 fact needs-you") ||
			r.hasPrefix("0 fact stalled") {
			t.Fatalf("after more idle checks: %d stopped facts, %d in force: %q; want one of each, nothing else",
				n, r.inForce(), r.steps())
		}

		r.stop() // a replay
		r.start()
		r.push()
		r.passes()
		if n := r.taskStep(event.MsgTask, "fact stopped"); n != 1 || r.inForce() != 1 {
			t.Fatalf("after a replay: %d stopped facts, %d in force: %q; want 1, 1", n, r.inForce(), r.steps())
		}

		r.workerIs("working") // working alone is no new turn
		r.passes()
		if r.hasPrefix("0 fact resumed") || r.inForce() != 1 {
			t.Fatalf("working, the rollout's last turn still t1: %q", r.steps())
		}
		r.append(cdxStarted2)
		r.push()
		r.waitFor("fact resumed", func() bool { return r.has("0 fact resumed") })
		if r.inForce() != 0 {
			t.Errorf("the stop in force after the next turn: %+v", r.app.Status().Project.Facts)
		}
		r.append(cdxDone2)
		r.workerIs("idle")
		r.passes()
		if n, m := r.taskStep(event.MsgTask, "fact stopped"), r.count("0 fact resumed"); n != 1 || m != 1 {
			t.Errorf("after the next turn's clean end: %d stopped, %d resumed; want 1, 1: %q", n, m, r.steps())
		}
	})
}

// Once per turn. The Codex worker's stop ended another way (here a turn request) while its rollout's last turn
// is still the one that ended on the limit: later idle checks, and a replay, record no second stop for that turn.
func TestStoppedCodexOncePerTurn(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := codexStopped(t)
		if reply, err := r.turnCall(); err != nil || reply != "TURN pj-l-01 (the stop of T-1 ended)" {
			t.Fatalf("turn: %q, %v", reply, err)
		}
		if r.inForce() != 0 {
			t.Fatalf("precondition: the stop ended: %q", r.steps())
		}
		r.later(time.Minute)
		r.push()
		r.passes()
		r.stop()
		r.start()
		r.push()
		r.passes()
		if n := r.taskStep(event.MsgTask, "fact stopped"); n != 1 || r.inForce() != 0 {
			t.Errorf("idle on the same turn after its stop ended: %d stopped facts, %d in force: %q; want 1, 0",
				n, r.inForce(), r.steps())
		}
	})
}

// The Codex worker's next turn starts, then writes more than the adapter's first read (one 300 KiB output)
// while serve is down (a replay) or its herdr connection is lost (a reconnect): the stop still ends on that start.
func TestStoppedCodexNextTurnPastTheTail(t *testing.T) {
	t.Parallel()
	next := cdxStarted2 + `{"type":"response_item","payload":{"output":"` + strings.Repeat("x", 300<<10) + `"}}` + "\n"
	t.Run("replay", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := codexStopped(t)
			r.stop()
			r.append(next)
			r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Status = "working" })
			if r.inForce() != 1 || r.has("0 fact resumed") {
				t.Fatalf("precondition: the stop in force before the replay: %q", r.steps())
			}
			r.start()
			r.waitFor("fact resumed", func() bool { return r.has("0 fact resumed") })
			if r.inForce() != 0 {
				t.Errorf("the stop in force after the replay: %+v", r.app.Status().Project.Facts)
			}
		})
	})
	t.Run("reconnect", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := codexStopped(t)
			if r.inForce() != 1 || r.has("0 fact resumed") {
				t.Fatalf("precondition: the stop in force before the reconnect: %q", r.steps())
			}
			r.srv.DropConns()
			r.append(next)
			r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Status = "working" })
			r.waitFor("fact resumed", func() bool { return r.has("0 fact resumed") })
			if r.inForce() != 0 {
				t.Errorf("the stop in force after the reconnect: %+v", r.app.Status().Project.Facts)
			}
		})
	})
}
