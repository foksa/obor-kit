package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// ruleHarness is a loaded project with a lead and a merge loop, its rule pass run by hand at the harness's clock.
func ruleHarness(t *testing.T) *harness {
	t.Helper()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{}, nil }
	h.connect()
	t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
	if p := h.proj(); !p.merging || p.out.lead() != "pj-lead" {
		t.Fatalf("precondition: merging %v, lead %q", p.merging, p.out.lead())
	}
	return h
}

// ruleLines is the rules' lines the log recorded, in order: their Of and line.
func ruleLines(t *testing.T, h *harness) (ofs []event.Of, lines []string) {
	t.Helper()
	for _, r := range readLog(t, h.root) {
		var m event.Message
		if r.Task == event.MsgTask && r.Step == event.StepMessage && json.Unmarshal(r.Data, &m) == nil && m.Of.Kind != "" {
			ofs, lines = append(ofs, m.Of), append(lines, m.Line)
		}
	}
	return ofs, lines
}

// T-866: the rule pass, one table. A rule's condition is recomputed each pass: it acts once the condition has held
// for its settle; a line records did{key} (its Of) and no second line goes for that key; an action with no line runs
// until its effect shows in state; a failed call records no line, and ruleFails failures in a row put one needs-you
// naming the rule in force, ended by its next success.
func TestRulePass(t *testing.T) {
	t.Parallel()
	type pass struct {
		after time.Duration // the clock moves this far before the pass
		fail  bool          // the action's call fails in this pass
	}
	cases := []struct {
		name   string
		holds  bool
		settle time.Duration
		line   bool // the action has a line (else a call alone, done once it ran: its effect shows in state)
		passes []pass
		calls  int  // calls made (failed ones included)
		lines  int  // lines recorded
		needs  bool // the rule's needs-you in force after the last pass
	}{
		{name: "condition false: nothing", holds: false, line: true, passes: []pass{{}, {after: time.Hour}}},
		{name: "inside its settle: nothing", holds: true, settle: time.Minute, line: true,
			passes: []pass{{}, {after: 59 * time.Second}}},
		{name: "past its settle: one line, did", holds: true, settle: time.Minute, line: true,
			passes: []pass{{}, {after: time.Minute}, {after: time.Minute}, {after: time.Hour}}, calls: 1, lines: 1},
		{name: "a call alone: once its effect shows", holds: true,
			passes: []pass{{}, {}, {}}, calls: 1},
		{name: "fewer failures than ruleFails: no needs-you", holds: true, line: true,
			passes: []pass{{fail: true}, {fail: true}}, calls: 2},
		{name: "ruleFails failures in a row: needs-you", holds: true, line: true,
			passes: []pass{{fail: true}, {fail: true}, {fail: true}}, calls: 3, needs: true},
		{name: "a success ends the needs-you", holds: true, line: true,
			passes: []pass{{fail: true}, {fail: true}, {fail: true}, {}, {}}, calls: 4, lines: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := ruleHarness(t)
			p := h.proj()
			calls, fail := 0, false
			r := rule{name: "test", settle: c.settle, when: func(_ context.Context, _ *App, _ *proj, _ *Loop, _ time.Time) (int64, action, bool) {
				if !c.line && calls > 0 {
					return 0, action{}, false // its effect shows: done
				}
				act := action{call: func(context.Context) error {
					calls++
					if fail {
						return errors.New("refused")
					}
					return nil
				}}
				if c.line {
					act.line = "[obor] test"
				}
				return 7, act, c.holds
			}}
			sawNeeds := false
			for _, ps := range c.passes {
				h.now, fail = h.now.Add(ps.after), ps.fail
				h.a.rulePass(context.Background(), []rule{r}, &Loop{})
				sawNeeds = sawNeeds || p.has(event.RuleTask, event.StepNeedsYou, "rule test failed 3 passes in a row (serve's log names the errors)")
			}
			ofs, _ := ruleLines(t, h)
			if calls != c.calls || len(ofs) != c.lines {
				t.Errorf("calls %d, lines %d (%v), want %d, %d", calls, len(ofs), ofs, c.calls, c.lines)
			}
			if _, did := p.folded.did[event.Of{Kind: "test", Seq: 7}]; did != (c.lines > 0) {
				t.Errorf("did{test 7} %v, want %v", did, c.lines > 0)
			}
			needs := len(p.folded.active[event.RuleTask]) > 0
			if needs != c.needs {
				t.Errorf("needs-you in force %v, want %v (%v)", needs, c.needs, p.folded.active[event.RuleTask])
			}
			if c.name == "a success ends the needs-you" && !sawNeeds {
				t.Errorf("precondition: no needs-you after %d failures", ruleFails)
			}
		})
	}
}

// T-866: the idle rules, one table. The pipeline empties (T-1 merged): the line once it stayed empty for idleSettle;
// work inside the window: none; acknowledged: no ping; unacknowledged: the ping and the owner's notification
// idlePingAfter after the line; a new emptying (T-2 merged) is a new key with its own line.
func TestIdleRules(t *testing.T) {
	t.Parallel()
	type step func(t *testing.T, h *harness)
	pass := func(d time.Duration) step {
		return func(_ *testing.T, h *harness) {
			h.now = h.now.Add(d)
			h.a.rulePass(context.Background(), rules, &Loop{})
		}
	}
	merged := func(task string) step { // task submitted and merged outside the app: no after-merge step left
		return func(t *testing.T, h *harness) {
			addRecord(t, h, task, event.StepSubmitted, event.KindFact, submitData{Lane: "L-01"})
			addRecord(t, h, task, event.StepMerged, event.KindFact, event.MergedData{Outside: true})
		}
	}
	dispatch := func(task string) step { // work in flight
		return func(t *testing.T, h *harness) {
			addRecord(t, h, task, event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"),
				Agent: "pj-l-01", Branch: "task/" + task, Target: "main"})
		}
	}
	ack := func(t *testing.T, h *harness) {
		if got, err := h.a.idleCall(wire.Call{Root: h.root, Role: "lead", Agent: "pj-lead", CallID: "a1",
			Note: "waiting for the owner"}); err != nil || got != "ACKED idle: waiting for the owner" {
			t.Fatalf("obor idle: %q, %v", got, err)
		}
	}
	line := func(task string) string {
		return idleLine(task + " merged (outside obor serve)")
	}
	cases := []struct {
		name  string
		steps []step
		lines []string // the idle rules' lines, in order
		keys  []string // their Of kinds, in order
		notes int      // the owner's notifications
	}{
		{name: "empties: the line after idleSettle",
			steps: []step{merged("T-1"), pass(0), pass(idleSettle - time.Second), pass(time.Second), pass(time.Minute)},
			lines: []string{line("T-1")}, keys: []string{ruleIdle}},
		{name: "work inside the window: none",
			steps: []step{merged("T-1"), pass(0), pass(30 * time.Second), dispatch("T-2"), pass(0), pass(idleSettle),
				pass(idlePingAfter)}},
		{name: "acknowledged: no ping",
			steps: []step{merged("T-1"), pass(0), pass(idleSettle), ack, pass(idlePingAfter), pass(time.Hour)},
			lines: []string{line("T-1")}, keys: []string{ruleIdle}},
		{name: "acknowledged in the window: no line",
			steps: []step{merged("T-1"), pass(0), ack, pass(idleSettle), pass(idlePingAfter)}},
		{name: "unacknowledged: the ping at idlePingAfter",
			steps: []step{merged("T-1"), pass(0), pass(idleSettle), pass(idlePingAfter - time.Second), pass(time.Second),
				pass(time.Hour)},
			lines: []string{line("T-1"), line("T-1") + " (again, 30m)"}, keys: []string{ruleIdle, ruleIdlePing}, notes: 1},
		{name: "a new emptying: a new key, a new line",
			steps: []step{merged("T-1"), pass(0), pass(idleSettle), dispatch("T-2"), pass(0), merged("T-2"), pass(0),
				pass(idleSettle)},
			lines: []string{line("T-1"), line("T-2")}, keys: []string{ruleIdle, ruleIdle}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := ruleHarness(t)
			for _, s := range c.steps {
				s(t, h)
			}
			ofs, lines := ruleLines(t, h)
			var keys []string
			for _, of := range ofs {
				keys = append(keys, of.Kind)
			}
			if !slices.Equal(lines, c.lines) || !slices.Equal(keys, c.keys) {
				t.Errorf("lines %q (%v), want %q (%v)", lines, keys, c.lines, c.keys)
			}
			if len(ofs) == 2 && ofs[0].Kind == ofs[1].Kind && ofs[0].Seq == ofs[1].Seq {
				t.Errorf("two lines of one key: %v", ofs)
			}
			if n := len(h.panes.notes); n != c.notes {
				t.Errorf("%d notifications (%q), want %d", n, h.panes.notes, c.notes)
			}
		})
	}
}

// T-874, T-908: the worker start rule's condition, one table (condition → action). T-1 on L-01, its worker pj-l-01's
// pane w1:p1 labelled, at its shell unless the case's worker runs: a worker job outstanding, no block, no claim and no
// project pause start pj-l-01 there without --fresh, with that job's wake, keyed by the dispatch fact; a model's pause
// holds nothing.
func TestWorkerStartRuleCondition(t *testing.T) {
	t.Parallel()
	working := func(mt *mtask) { mt.state, mt.work, mt.started = stWorking, "work", true }
	cases := []struct {
		name string
		runs bool // pj-l-01 runs in w1:p1
		edit func(p *proj, mt *mtask)
		wake string // the start's job ("": no start)
	}{
		{name: "working, its work job out: started with its wake", edit: func(_ *proj, mt *mtask) { working(mt) },
			wake: "T-1/1/work"},
		{name: "dispatched, its worker started: started with its work job's wake",
			edit: func(_ *proj, mt *mtask) { mt.state, mt.work, mt.started = stDispatched, "work", true }, wake: "T-1/1/work"},
		{name: "dispatched, its worker not started yet: none (the dispatch's start)",
			edit: func(_ *proj, mt *mtask) { mt.state, mt.work = stDispatched, "work" }},
		{name: "its fix job out: started with its wake",
			edit: func(_ *proj, mt *mtask) { mt.state, mt.fixed, mt.fixes = stWorking, true, 2 }, wake: "T-1/1/fix.2"},
		{name: "a sendback's fix job not sent yet: started with the next fix job's wake",
			edit: func(_ *proj, mt *mtask) { mt.state, mt.fixes = stWorking, 1 }, wake: "T-1/1/fix.2"},
		{name: "its worker runs: none", runs: true, edit: func(_ *proj, mt *mtask) { working(mt) }},
		{name: "a stop of its worker pending (busy): none",
			edit: func(_ *proj, mt *mtask) { working(mt); mt.halt = halt{agent: "pj-l-01"} }},
		{name: "a drop being recorded (busy): none",
			edit: func(p *proj, mt *mtask) { working(mt); p.ended["T-1"] = true }},
		{name: "a needs-you holds it: none",
			edit: func(_ *proj, mt *mtask) { working(mt); mt.needs = block{fact: event.Record{Seq: 9}} }},
		{name: "submitted: none", edit: func(_ *proj, mt *mtask) { working(mt); mt.state = stSubmitted }},
		{name: "obor stop's pause: none", edit: func(p *proj, mt *mtask) {
			working(mt)
			p.folded.pause = &event.Pause{Why: wire.StopPause}
		}},
		{name: "a project pause (the owner's reason): none", edit: func(p *proj, mt *mtask) {
			working(mt)
			p.folded.pause = &event.Pause{Why: "quota"}
		}},
		{name: "a model's pause (another model): started", edit: func(p *proj, mt *mtask) {
			working(mt)
			p.folded.models = map[string]*event.Pause{"opus": {Model: "opus", Why: "quota"}}
		}, wake: "T-1/1/work"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, _ := newIdentHarness(t)
			h.withLead()
			if !c.runs {
				h.pane("w1:p1").Agent = ""
			}
			h.connect()
			t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
			p := h.proj()
			mt := &mtask{task: "T-1", lane: "L-01", agent: "pj-l-01", attempt: 1, dispSeq: 5}
			h.a.mu.Lock()
			p.dispatching = true
			c.edit(p, mt)
			p.folded.loop["T-1"] = mt
			_, shell := h.a.labelledShell(p, "pj-l-01")
			h.a.mu.Unlock()
			if shell == c.runs {
				t.Fatalf("precondition: pj-l-01's pane at its shell %v, want %v", shell, !c.runs)
			}
			spy := &agentsSpy{}
			key, act, ok := workerStartRule(context.Background(), h.a, p, &Loop{Agents: spy}, h.now)
			if ok != (c.wake != "") {
				t.Fatalf("condition %v, want %v", ok, c.wake != "")
			}
			if !ok {
				return
			}
			if err := act.call(context.Background()); err != nil || act.line != "" {
				t.Fatalf("action: %v, line %q", err, act.line)
			}
			want := port.Launch{Agent: "pj-l-01", Pane: "w1:p1", Prompt: wakeLine(c.wake)}
			if key != 5 || len(spy.launches) != 1 || spy.launches[0] != want {
				t.Errorf("key %d, starts %+v; want key 5, one start %+v", key, spy.launches, want)
			}
		})
	}
}
