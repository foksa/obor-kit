package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/outbox"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
)

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
		if got, err := h.a.idleCall(wire.IdleReq{Envelope: wire.Envelope{Root: h.root, Role: "lead", Agent: "pj-lead",
			CallID: "a1"}, Why: "waiting for the owner"}); err != nil || got != "ACKED idle: waiting for the owner" {
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

// T-949: a stopped role agent's alarm is one rule: the lead's line records, and its toast is the action's best-effort
// after, once per stop. A toast herdr does not show is logged and dropped: the line stays, no retry, no rule failure.
func TestStopAlarmRule(t *testing.T) {
	t.Parallel()
	alarm := rule{name: ruleStopAlarm, when: stopAlarmRule}
	pass := func(h *harness, d time.Duration) {
		h.now = h.now.Add(d)
		h.a.rulePass(context.Background(), []rule{alarm}, &Loop{})
	}
	stopped := func(h *harness) {
		stoppedBrief(h.proj(), "pj-rev", 1, 9, h.now.Add(-time.Hour))
	}
	t.Run("one toast and the lead's line", func(t *testing.T) {
		t.Parallel()
		h := ruleHarness(t)
		stopped(h)
		pass(h, 30*time.Minute)
		if len(h.panes.notes) != 1 {
			t.Fatalf("notes %q, want one", h.panes.notes)
		}
		ofs, lines := ruleLines(t, h)
		if len(ofs) != 1 || ofs[0].Kind != ruleStopAlarm || !strings.HasSuffix(lines[0], stillStopped) {
			t.Fatalf("lines %v %q, want the alarm's one", ofs, lines)
		}
		pass(h, time.Hour)
		if len(h.panes.notes) != 1 {
			t.Errorf("notes on a later pass: %q, want still one", h.panes.notes)
		}
	})
	t.Run("rate_limited: the line still records, no retry, no rule failure", func(t *testing.T) {
		t.Parallel()
		h := ruleHarness(t)
		stopped(h)
		calls := 0
		h.panes.notifyFn = func() error {
			calls++
			return &port.NotShown{Reason: "rate_limited"}
		}
		for range ruleFails + 1 {
			pass(h, 30*time.Minute)
		}
		if ofs, lines := ruleLines(t, h); len(ofs) != 1 {
			t.Fatalf("lines %q, want the alarm's line recorded though the toast failed", lines)
		}
		if calls != 1 {
			t.Errorf("toast calls %d, want one for the stop (no retry)", calls)
		}
		if active := h.proj().folded.active[event.RuleTask]; len(active) != 0 {
			t.Errorf("a rule needs-you after failed toasts: %v", active)
		}
	})
	t.Run("two stops: one line and toast each, the lower seq first, no repeat", func(t *testing.T) {
		t.Parallel()
		h := ruleHarness(t)
		p := h.proj()
		stoppedBrief(p, "pj-rev", 1, 9, h.now.Add(-time.Hour))
		stoppedBrief(p, "pj-deep", 2, 10, h.now.Add(-time.Hour))
		pass(h, 30*time.Minute)
		pass(h, 0)
		pass(h, 0)
		ofs, lines := ruleLines(t, h)
		if len(ofs) != 2 || ofs[0].Seq != 9 || ofs[1].Seq != 10 {
			t.Fatalf("lines %v %q, want the two alarms, seq 9 then 10", ofs, lines)
		}
		if !strings.Contains(lines[0], "pj-rev") || !strings.Contains(lines[1], "pj-deep") {
			t.Errorf("lines %q, want pj-rev's then pj-deep's", lines)
		}
		if len(h.panes.notes) != 2 {
			t.Errorf("notes %q, want one toast for each stop", h.panes.notes)
		}
	})
}

// stoppedBrief folds into p's outbox (not the log) agent's open brief of fact seq brief, stopped on a usage limit by fact
// seq stop at at and woken at at: its stop's alarm is due wakeAlarm after at.
func stoppedBrief(p *proj, agent string, brief, stop int64, at time.Time) {
	fold := func(seq int64, step string, data any) {
		d, _ := json.Marshal(data)
		p.out.Fold(event.Record{Seq: seq, TS: at.Format(time.RFC3339Nano), Task: event.MsgTask, Step: step,
			Kind: event.KindFact, Data: d}, p.oldLineOf)
	}
	fold(brief, event.StepBriefed, event.Message{To: agent})
	fold(stop, event.StepStopped, outbox.Stopped{Agent: agent, Error: "usage_limit_exceeded", Brief: brief,
		Message: event.Message{To: "pj-lead", Line: "[obor] " + agent + " stopped: usage_limit_exceeded (try later)"}})
	p.out.Woke(agent, at)
}

// deliverLines is the delivery of every rule line waiting: its post's result recorded now.
func deliverLines(t *testing.T, h *harness) {
	p := h.proj()
	var seqs []int64
	for _, q := range p.out.Queued() {
		if q.Msg().Of.Kind != "" {
			seqs = append(seqs, q.Seq())
		}
	}
	if len(seqs) == 0 {
		t.Fatal("precondition: no rule line waits to be delivered")
	}
	for _, seq := range seqs {
		addRecord(t, h, event.MsgTask, outbox.StepDeliver+strconv.FormatInt(seq, 10), event.KindResult, "sent")
	}
}

// T-927: the needs-you line's rule, one table (condition → action). The first line at once; a repeat 30 min after
// the first line's delivery, then 60 after each repeat's, never while the last line waits; nothing once the needs-you
// is answered, for a closed task's watchdog fact or for the project's own lines' needs-you; a restart sends nothing
// again; a line recorded before T-927 (message{needs}, no of) counts as the rule's.
func TestNeedsRule(t *testing.T) {
	t.Parallel()
	needs := rule{name: ruleNeeds, when: needsRule}
	pass := func(d time.Duration) lineStep { return passOf(needs, d) }
	fact := func(task string) lineStep {
		return func(t *testing.T, h *harness) {
			addRecord(t, h, task, event.StepNeedsYou, event.KindFact, event.FactData{Lane: "L-01", Cause: "lane",
				Reason: "lane: lane_setup\nboom"})
			if !slices.ContainsFunc(h.proj().needsYou(), func(r event.Record) bool { return r.Task == task }) {
				t.Fatalf("precondition: %s's needs-you not in force", task)
			}
		}
	}
	resumed := func(t *testing.T, h *harness) {
		addRecord(t, h, "T-1", event.StepResumed, event.KindFact, event.FactData{Reason: "retry"})
		if len(h.proj().needsYou()) != 0 {
			t.Fatal("precondition: the needs-you is still in force after the resume")
		}
	}
	lined := func(n int) lineStep { // n lines recorded so far
		return func(t *testing.T, h *harness) {
			if ofs, lines := ruleLines(t, h); len(ofs) != n {
				t.Fatalf("at %s: lines %q, want %d", h.now.Format(time.Kitchen), lines, n)
			}
		}
	}
	closedStale := func(t *testing.T, h *harness) { // T-1 merged with its watchdog fact still in force (T-635)
		p := h.proj()
		r := p.folded.active["T-1"]
		taskMerged("T-1", "")(t, h)
		p.folded.active["T-1"] = r
		if !p.closed("T-1") || len(p.needsYou()) != 1 {
			t.Fatalf("precondition: T-1 closed %v, needs-yous in force %v; want closed with its fact", p.closed("T-1"),
				p.needsYou())
		}
	}
	oldLine := func(t *testing.T, h *harness) { // a line of T-1's needs-you as obor before T-927 recorded it
		seq := h.proj().needsYou()[0].Seq
		addRecord(t, h, event.MsgTask, event.StepMessage, event.KindFact, map[string]any{"to": "pj-lead", "from": "obor",
			"line": "[obor] needs-you T-1 (L-01): old", "needs": seq})
	}
	const first = "[obor] needs-you T-1 (L-01): lane: lane_setup boom"
	cases := []struct {
		name  string
		steps []lineStep
		lines []string // the rule's lines: each its task and how it ends ("" the first, else the repeat's suffix)
		ns    []int    // their line numbers (the did key's)
	}{
		{name: "the first line at once, once", steps: []lineStep{fact("T-1"), pass(0), pass(0), pass(time.Minute)},
			lines: []string{"T-1"}, ns: []int{0}},
		{name: "no repeat at 29 min after its delivery",
			steps: []lineStep{fact("T-1"), pass(0), deliverLines, pass(29 * time.Minute), lined(1)},
			lines: []string{"T-1"}, ns: []int{0}},
		{name: "the repeat at 30 min after the first line's delivery, not its record",
			steps: []lineStep{fact("T-1"), pass(0), pass(10 * time.Minute), deliverLines, pass(29 * time.Minute),
				pass(time.Minute)},
			lines: []string{"T-1", "T-1 (again, 40m)"}, ns: []int{0, 1}},
		{name: "the next 60 min after the repeat's delivery",
			steps: []lineStep{fact("T-1"), pass(0), deliverLines, pass(30 * time.Minute), deliverLines,
				pass(59 * time.Minute), lined(2), pass(time.Minute), pass(0)},
			lines: []string{"T-1", "T-1 (again, 30m)", "T-1 (again, 1h)"}, ns: []int{0, 1, 2}},
		{name: "the last line undelivered: no repeat",
			steps: []lineStep{fact("T-1"), pass(0), pass(2 * time.Hour), deliverLines, pass(30 * time.Minute),
				pass(5 * time.Hour)},
			lines: []string{"T-1", "T-1 (again, 2h)"}, ns: []int{0, 1}},
		{name: "answered (resumed): no further line",
			steps: []lineStep{fact("T-1"), pass(0), deliverLines, resumed, pass(30 * time.Minute), pass(2 * time.Hour)},
			lines: []string{"T-1"}, ns: []int{0}},
		{name: "a closed task's watchdog fact: no line",
			steps: []lineStep{fact("T-1"), closedStale, pass(0), pass(time.Hour)}},
		{name: "the project's own lines' needs-you: no line", steps: []lineStep{fact(event.MsgTask), pass(0)}},
		{name: "two needs-yous: one line each, the oldest first",
			steps: []lineStep{fact("T-1"), fact("T-2"), pass(0), pass(0), pass(0)},
			lines: []string{"T-1", "T-2"}, ns: []int{0, 0}},
		{name: "a restart sends no line again and keeps the clock",
			steps: []lineStep{fact("T-1"), pass(0), deliverLines, restartRig, pass(0), pass(29 * time.Minute),
				restartRig, pass(time.Minute), pass(0)},
			lines: []string{"T-1", "T-1 (again, 30m)"}, ns: []int{0, 1}},
		{name: "a line recorded before T-927: no second first line, its repeat counts from its delivery",
			steps: []lineStep{fact("T-1"), oldLine, deliverLines, restartRig, pass(0), pass(29 * time.Minute),
				pass(time.Minute)},
			lines: []string{"T-1 (again, 30m)"}, ns: []int{1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := ruleHarness(t)
			for _, s := range c.steps {
				s(t, h)
			}
			ofs, lines := ruleLines(t, h)
			var got []string
			var ns []int
			for i, of := range ofs {
				if of.Kind != ruleNeeds {
					t.Fatalf("a line of rule %s: %q", of.Kind, lines[i])
				}
				task, _, _ := strings.Cut(strings.TrimPrefix(lines[i], "[obor] needs-you "), " ")
				body := strings.Replace(lines[i], "needs-you "+task, "needs-you T-1", 1)
				if !strings.HasPrefix(body, first+" → ") {
					t.Errorf("line %q, want %q and the commands that answer it", lines[i], first)
				}
				if _, again, ok := strings.Cut(lines[i], " (again, "); ok {
					task += " (again, " + again
				}
				got, ns = append(got, task), append(ns, int(of.Seq%needsSpan))
			}
			if !slices.Equal(got, c.lines) || !slices.Equal(ns, c.ns) {
				t.Errorf("lines %q (line numbers %v), want %q (%v)", got, ns, c.lines, c.ns)
			}
		})
	}
}

// T-949: the needs rule carries the owner's toast as its action's after (a best effort), once for a key's first line;
// a repeat line does not toast again. A toast herdr does not show (busy, rate_limited, disabled) or a Notify error is
// logged once and dropped: the lead's line is recorded all the same, is never retried and is no rule failure.
func TestNeedsRuleToast(t *testing.T) {
	t.Parallel()
	needs := rule{name: ruleNeeds, when: needsRule}
	pass := func(h *harness, d time.Duration) {
		h.now = h.now.Add(d)
		h.a.rulePass(context.Background(), []rule{needs}, &Loop{})
	}
	fact := func(t *testing.T, h *harness) {
		addRecord(t, h, "T-1", event.StepNeedsYou, event.KindFact, event.FactData{Lane: "L-01", Reason: "blocked"})
	}
	t.Run("one toast for the fact, none for its repeat", func(t *testing.T) {
		t.Parallel()
		h := ruleHarness(t)
		fact(t, h)
		pass(h, 0)
		if len(h.panes.notes) != 1 {
			t.Fatalf("notes after the first line: %q, want one", h.panes.notes)
		}
		deliverLines(t, h)
		pass(h, 30*time.Minute)
		pass(h, 0)
		if ofs, lines := ruleLines(t, h); len(ofs) != 2 || !strings.Contains(lines[1], " (again, ") {
			t.Fatalf("lines %q, want the repeat too", lines)
		}
		if len(h.panes.notes) != 1 {
			t.Errorf("notes after the repeat line: %q, want still one", h.panes.notes)
		}
	})
	for _, tc := range []struct {
		name string
		fail func() error
	}{
		{"a Notify error", func() error { return errors.New("herdr: gone") }},
		{"shown false busy", func() error { return &port.NotShown{Reason: "busy"} }},
		{"shown false rate_limited", func() error { return &port.NotShown{Reason: "rate_limited"} }},
		{"shown false disabled", func() error { return &port.NotShown{Reason: "disabled"} }},
	} {
		t.Run(tc.name+": the line still records, no retry, no rule failure", func(t *testing.T) {
			t.Parallel()
			h := ruleHarness(t)
			calls := 0
			h.panes.notifyFn = func() error {
				calls++
				return tc.fail()
			}
			fact(t, h)
			for range ruleFails + 1 {
				pass(h, 0)
			}
			if ofs, lines := ruleLines(t, h); len(ofs) != 1 {
				t.Fatalf("lines %q, want the first one recorded though the toast failed", lines)
			}
			if calls != 1 {
				t.Errorf("toast calls %d, want one for the key (no retry)", calls)
			}
			if n := len(h.panes.notes); n != 0 {
				t.Errorf("notes %q, want none (the fake records a shown toast only)", h.panes.notes)
			}
			if active := h.proj().folded.active[event.RuleTask]; len(active) != 0 {
				t.Errorf("a rule needs-you after failed toasts: %v", active)
			}
		})
	}
}

// T-949: an old log's notify.* and alarm.* records replay and fold as nothing: the needs line it recorded before
// T-927 is what keeps the rule from toasting again, and the old steps are no error.
func TestOldNotifyRecordsReplay(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.withLead()
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{}, nil }
	t.Cleanup(func() {
		if p := h.a.p; p != nil {
			p.log.Close()
		}
		h.a.closeLog()
	})
	factData, _ := json.Marshal(event.FactData{Lane: "L-01", Reason: "blocked"})
	oldLine, _ := json.Marshal(map[string]any{"to": "pj-lead", "from": "obor",
		"line": "[obor] needs-you T-1 (L-01): blocked", "needs": 1})
	h.log(
		event.Record{Task: "T-1", Step: event.StepNeedsYou, Kind: event.KindFact, Data: factData},
		event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact, Data: oldLine},
		event.Record{Task: "T-1", Step: "notify.1", Kind: event.KindIntent, Class: event.ClassFact},
		event.Record{Task: "T-1", Step: "notify.1", Kind: event.KindResult, Data: []byte(`"shown"`)},
		event.Record{Task: event.MsgTask, Step: "alarm.9", Kind: event.KindIntent, Class: event.ClassFact},
		event.Record{Task: event.MsgTask, Step: "alarm.9", Kind: event.KindResult, Data: []byte(`"shown"`)},
	)
	h.connect()
	p := h.proj()
	if !p.merging || p.out.Lead() == "" || len(p.needsYou()) != 1 {
		t.Fatalf("precondition: merging %v, lead %q, needs-yous %d", p.merging, p.out.Lead(), len(p.needsYou()))
	}
	h.a.rulePass(context.Background(), []rule{{name: ruleNeeds, when: needsRule}, {name: ruleStopAlarm, when: stopAlarmRule}}, &Loop{})
	if notes := h.panes.notes; len(notes) != 0 {
		t.Errorf("notifications after the replay: %q, want none", notes)
	}
}

// countPoster is a Poster and Liner that counts: the lines posted, and how many were typed.
type countPoster struct {
	posts []string
	typed int
}

func (c *countPoster) Post(_ context.Context, _, to, line string) (bool, error) {
	c.posts = append(c.posts, to+": "+line)
	return true, nil
}

func (c *countPoster) Type(context.Context, string, string, bool) (bool, error) {
	c.typed++
	return true, nil
}

// RV-52-verify V1: a needs-you's line whose append succeeds while the lanes table's write fails (record false) stays
// recorded: a later pass appends no second line, and once the table writes again the lead gets the line once.
func TestRV52VerifyProjectionFailureDoesNotDuplicate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","roles":{"integrator":""}}`)
	in := &countPoster{}
	fx := &Loop{Inbox: in, Typer: in}
	h.a.opt.Loop = func(string) (*Loop, error) { return fx, nil }
	h.a.opt.Inboxed = func(string) (string, bool) { return "", true }
	h.connect()
	p := h.proj()
	t.Cleanup(func() { p.log.Close(); h.a.closeLog() })

	needs := []rule{{name: ruleNeeds, when: needsRule}}
	d := event.FactData{Lane: "L-01", Cause: "lane", Reason: "lane: prepare\nfailed"}
	if !h.a.record(p, event.Record{Task: "T-1", Step: event.StepNeedsYou, Kind: event.KindFact}, d) {
		t.Fatal("the needs-you was refused")
	}
	r := p.folded.active["T-1"][len(p.folded.active["T-1"])-1]
	p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"} // a row to write: the write fails
	if _, err := p.log.DB().Exec(`CREATE TRIGGER lanes_fail BEFORE INSERT ON lanes BEGIN SELECT RAISE(ABORT, 'injected lane-row failure'); END`); err != nil {
		t.Fatal(err)
	}

	h.a.rulePass(context.Background(), needs, fx)
	if ms := needsMessages(t, p); len(ms) != 1 || p.folded.lr.err == nil {
		t.Fatalf("precondition: needs-you lines in the log = %d, lane-row error = %v; want 1 and an error", len(ms), p.folded.lr.err)
	}
	h.a.rulePass(context.Background(), needs, fx)
	if ms := needsMessages(t, p); len(ms) != 1 {
		t.Errorf("needs-you lines for one needs-you after a second pass = %d, want 1", len(ms))
	}

	if _, err := p.log.DB().Exec("DROP TRIGGER lanes_fail"); err != nil {
		t.Fatal(err)
	}
	h.a.rulePass(context.Background(), needs, fx)
	for range 4 {
		h.a.deliver(context.Background(), p, fx)
	}
	if len(in.posts) != 1 || in.typed != 0 {
		t.Errorf("after the table writes again: posted %d, typed %d; want 1 posted, none typed", len(in.posts), in.typed)
	}
	for _, m := range needsMessages(t, p) {
		if m.Of != (event.Of{Kind: ruleNeeds, Seq: needsKey(r.Seq, 0)}) || !strings.Contains(m.Line, "prepare failed") {
			t.Errorf("unexpected line %+v", m)
		}
	}
}
