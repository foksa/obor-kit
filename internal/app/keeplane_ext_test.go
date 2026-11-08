package app_test

import (
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// keepRig is the loop rig with L-01 a keep lane (T-1031): lanes [{"name": "L-01", "keep_agent": true}], handoff_tokens
// 1000, and pj-l-01's sessions sized by past: the first session serve starts (/s/1.jsonl; the rig's own pj-l-01, in
// /s/0.jsonl, is no start of serve's, so the first dispatch restarts it, T-1048) is past the threshold once past is set.
func keepRig(t *testing.T) (*loopRig, *atomic.Bool) {
	t.Helper()
	past := &atomic.Bool{}
	r := newLoopRig(t, dispatching(), withConfig(testkit.Set("lanes", `[{"name": "L-01", "keep_agent": true}]`),
		testkit.Set("handoff_tokens", 1000)))
	r.tokens = func(transcript string) (int, bool) {
		if transcript == "/s/1.jsonl" && past.Load() {
			return 5000, true
		}
		return 10, true
	}
	r.start()
	return r, past
}

// T-1031, end to end on fakes (no git): three tasks dispatched to one keep lane run in dispatch order, T-2 and T-3
// queued behind the lane's task (today a named busy lane is refused), each next one dispatched only once the one before
// merged; T-1 restarts the lane's agent serve never started (T-1048: unknown is no reuse), T-1 and T-2 then run in its
// one session (T-2's wake typed, no agent.start; no agent.stop after their merge); between T-2 and T-3, the session
// past handoff_tokens, the handoff rule stops the idle agent and T-3's dispatch starts a fresh one carrying its job.
// job_next hands each task's job to the lane's agent (by lane: its current task).
func TestKeepLaneRunsATaskListInOneSession(t *testing.T) {
	t.Parallel()
	r, past := keepRig(t)
	if got := r.workerStops(); len(got) != 0 {
		t.Fatalf("precondition: stops %q", got)
	}
	if reply, err := r.dispatchTo("d1", "T-1", "L-01"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
		t.Fatalf("T-1: %q, %v", reply, err)
	}
	// dispatch lane pick: a dispatch naming the busy keep lane queues behind its task, in dispatch order.
	for _, c := range []struct{ id, task, behind string }{{"d2", "T-2", "T-1"}, {"d3", "T-3", "T-2"}} {
		want := "WAITING " + c.task + " (queued): behind " + c.behind + " on keep lane L-01"
		if reply, err := r.dispatchTo(c.id, c.task, "L-01"); err != nil || reply != want {
			t.Fatalf("%s: %q, %v; want %q", c.task, reply, err, want)
		}
	}
	if q := r.app.Status().Project.Queues["L-01"]; !slices.Equal(q, []string{"T-2", "T-3"}) {
		t.Errorf("L-01's queue %q, want T-2, T-3", q)
	}

	r.waitJob("pj-l-01", "T-1/1/work")
	r.runToMerge("T-1", nil)
	r.waitJob("pj-l-01", "T-2/1/work") // job_next by lane: the lane's current task, to the same agent
	if _, ok := r.record("T-3", event.KindFact, event.StepDispatched); ok {
		t.Fatal("T-3 dispatched while T-2 holds the lane")
	}
	past.Store(true) // pj-l-01's session grows past handoff_tokens during T-2: no restart mid-task
	r.runToMerge("T-2", func() {
		r.passes() // the rule pass ran with T-2 in review and the session past the threshold
		if got := r.workerStops(); len(got) != 1 {
			t.Errorf("stops while T-2 is in review: %q, want T-1's restart alone (never mid-task)", got)
		}
	})
	r.waitJob("pj-l-01", "T-3/1/work")

	// The order: each dispatch after the merge of the task before it.
	for _, c := range []struct{ prev, next string }{{"T-1", "T-2"}, {"T-2", "T-3"}} {
		merged, _ := r.record(c.prev, event.KindFact, event.StepMerged)
		disp, ok := r.record(c.next, event.KindFact, event.StepDispatched)
		if !ok || disp.Seq < merged.Seq {
			t.Errorf("%s dispatched at seq %d, %s merged at %d: want it after", c.next, disp.Seq, c.prev, merged.Seq)
		}
	}
	// after_merge step list: a keep lane's merged task stops no agent.
	for _, task := range []string{"T-1", "T-2"} {
		if _, ok := r.record(task, event.KindIntent, event.StepStop); ok {
			t.Errorf("%s ran agent.stop after its merge", task)
		}
	}
	// T-1: the unknown agent stopped, a fresh start carrying its wake; T-2: that session, its wake typed; T-3: the
	// handoff rule's stop, then its own fresh start.
	if _, ok := r.record("T-1", event.KindResult, event.StepWorkerStop); !ok {
		t.Error("T-1 did not stop the lane's agent serve never started")
	}
	if !slices.ContainsFunc(r.prompts(), func(s sent) bool {
		return s.to == "pj-l-01" && s.text == "[obor job T-2/1/work] call the obor job_next tool"
	}) {
		t.Error("T-2/1/work's wake was not typed into pj-l-01's session")
	}
	if _, ok := r.record("T-2", event.KindIntent, event.StepStartWork); ok {
		t.Error("T-2 started an agent: its keep lane had a live one running sonnet")
	}
	if got := r.workerStops(); !slices.Equal(got, []string{"pj-l-01 w1:p1", "pj-l-01 w1:p1"}) {
		t.Errorf("worker stops %q, want T-1's restart and the handoff stop", got)
	}
	worker := r.workerLaunches()
	if len(worker) != 2 || !worker[0].Fresh || worker[0].Prompt != "[obor job T-1/1/work] call the obor job_next tool" ||
		!worker[1].Fresh || worker[1].Prompt != "[obor job T-3/1/work] call the obor job_next tool" {
		t.Errorf("pj-l-01's launches %+v, want T-1's and T-3's fresh starts, each carrying its wake", worker)
	}
	if _, ok := r.record("T-3", event.KindResult, event.StepStartWork); !ok {
		t.Error("T-3 has no agent.start result")
	}
	r.disp.mu.Lock()
	session := r.disp.session
	r.disp.mu.Unlock()
	if session != 2 {
		t.Errorf("pj-l-01's session %d after T-3's start, want 2 (T-1's and T-3's)", session)
	}
	r.waitFor("T-3 working", func() bool { return r.stateOf("T-3") == "working" })
	if q := r.app.Status().Project.Queues["L-01"]; len(q) != 0 {
		t.Errorf("L-01's queue %q after T-3's dispatch, want empty", q)
	}
}

// T-1031: a fresh lane (lanes as a count) still stops its merged task's agent: after_merge, then agent.stop.
func TestFreshLaneStillStopsAfterMerge(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching(), started())
	if reply, err := r.dispatchTo("d1", "T-1", "L-01"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
		t.Fatalf("T-1: %q, %v", reply, err)
	}
	r.waitJob("pj-l-01", "T-1/1/work")
	r.runToMerge("T-1", nil)
	r.waitFor("T-1's agent.stop", func() bool {
		_, ok := r.record("T-1", event.KindResult, event.StepStop)
		return ok
	})
	// a dispatch naming the busy fresh lane is still refused, not queued
	r.setLane(func(l *port.Lane) { l.Task, l.State = "T-1", "working" })
	if reply, err := r.dispatchTo("d2", "T-2", "L-01"); err == nil || reply != "" ||
		!strings.Contains(err.Error(), "lane L-01 is working on T-1") {
		t.Errorf("a dispatch naming a busy fresh lane: %q, %v; want refused", reply, err)
	}
	if got := fmt.Sprint(r.app.Status().Project.Queues); got != "map[]" {
		t.Errorf("queues %s, want none", got)
	}
}

// keepSecond runs T-1 on the keep rig's L-01 as sonnet to its merge (its start: serve's own, sonnet), then setup and
// the lead's dispatch c of T-2 to L-01, and waits until T-2's work job is pj-l-01's (T-1048). sessionless: the worker's
// starts report no session id.
func keepSecond(t *testing.T, sessionless bool, setup func(r *loopRig), c wire.DispatchReq) *loopRig {
	t.Helper()
	r, _ := keepRig(t)
	r.disp.mu.Lock()
	r.disp.sessionless = sessionless
	r.disp.mu.Unlock()
	if reply, err := r.dispatchTo("d1", "T-1", "L-01"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
		t.Fatalf("T-1: %q, %v", reply, err)
	}
	r.waitJob("pj-l-01", "T-1/1/work")
	r.runToMerge("T-1", nil)
	if _, ok := r.record("T-1", event.KindResult, event.StepStartWork); !ok {
		t.Fatal("precondition: T-1's agent.start result (serve's own start of pj-l-01, as sonnet)")
	}
	if setup != nil {
		setup(r)
	}
	c.Root, c.Agent, c.Role, c.CallID, c.Task, c.Lane = r.p.Root, "pj-lead", "lead", "d2", "T-2", "L-01"
	if reply, err := r.dispatchCall(c); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-2 → L-01 (queued)") {
		t.Fatalf("T-2: %q, %v", reply, err)
	}
	r.waitJob("pj-l-01", "T-2/1/work")
	return r
}

// T-1048 (RV-148 #1): a keep lane reuses its agent for the next task only when the agent's recorded start (serve's own
// agent.start: its dispatch's model ref, the kind it reported, its session) runs the next dispatch's model and effort.
// Another model (by the spec's Model:, its difficulty, --model) or another effort only, a paused old model with an
// unpaused alternative selected, or a kind whose start reports no session id (it cannot prove the live agent is the
// one it started): agent.stop between the tasks (recorded), then agent.start of the dispatch's model carrying T-2's
// wake. The same model and effort: T-2's wake typed into the session, no stop, no start.
func TestKeepLaneReusesItsAgentOnlyForTheSameModel(t *testing.T) {
	pause := func(model string) func(r *loopRig) {
		return func(r *loopRig) {
			if _, err := r.pause("", "p-"+model, wire.PauseReq{Model: model, Why: "quota"}); err != nil {
				r.t.Fatal(err)
			}
		}
	}
	spec := func(text string) func(r *loopRig) { return func(r *loopRig) { r.specOf("T-2", text) } }
	for _, c := range []struct {
		name        string
		sessionless bool
		setup       func(r *loopRig)
		types       []string
		restart     bool
		typ, effort string // the restart's launch
	}{
		{name: "the same model and effort", types: []string{"sonnet"}},
		{name: "opus by the spec's Model:", setup: spec("# T-2 — Next\n\nDifficulty: medium\nModel: opus\n"),
			restart: true, typ: "opus"},
		{name: "opus by its difficulty", setup: spec("# T-2 — Next\n\nDifficulty: hard\n"), restart: true, typ: "opus",
			effort: "high"},
		{name: "opus by --model", types: []string{"opus"}, restart: true, typ: "opus"},
		{name: "another effort only", types: []string{"sonnet:high"}, restart: true, typ: "sonnet", effort: "high"},
		{name: "the old model paused, an alternative selected", setup: pause("sonnet"), types: []string{"sonnet", "opus"},
			restart: true, typ: "opus"},
		{name: "a kind that reports no session id", sessionless: true, types: []string{"sonnet"}, restart: true,
			typ: "sonnet"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := keepSecond(t, c.sessionless, c.setup, wire.DispatchReq{Types: c.types})
			launches := r.workerLaunches()
			if len(launches) == 0 || launches[0].Prompt != "[obor job T-1/1/work] call the obor job_next tool" ||
				launches[0].Type != "sonnet" {
				t.Fatalf("precondition: T-1's start of sonnet first: %+v", launches)
			}
			_, stopped := r.record("T-2", event.KindResult, event.StepWorkerStop)
			_, started := r.record("T-2", event.KindResult, event.StepStartWork)
			typed := slices.ContainsFunc(r.prompts(), func(s sent) bool {
				return s.to == "pj-l-01" && s.text == "[obor job T-2/1/work] call the obor job_next tool"
			})
			if !c.restart {
				if stopped || started || !typed || len(launches) != 1 {
					t.Errorf("T-2 stopped %v, started %v, its wake typed %v, launches %+v; want T-1's agent reused",
						stopped, started, typed, launches)
				}
				return
			}
			if !stopped || !started || typed {
				t.Errorf("T-2 stopped %v, started %v, its wake typed %v; want pj-l-01 stopped, then started fresh",
					stopped, started, typed)
			}
			if got := r.workerStops(); len(got) != 2 { // T-1's restart of the rig's agent, then T-2's
				t.Errorf("worker stops %q, want T-1's and T-2's", got)
			}
			if len(launches) != 2 || !launches[1].Fresh || launches[1].Type != c.typ || launches[1].Effort != c.effort ||
				launches[1].Prompt != "[obor job T-2/1/work] call the obor job_next tool" {
				t.Errorf("launches %+v; want T-2's fresh start of %s effort %q carrying its wake", launches, c.typ, c.effort)
			}
			stop, _ := r.record("T-2", event.KindIntent, event.StepWorkerStop)
			start, _ := r.record("T-2", event.KindIntent, event.StepStartWork)
			if stop.Seq == 0 || start.Seq < stop.Seq {
				t.Errorf("T-2's stop intent at seq %d, its start's at %d: want the stop first", stop.Seq, start.Seq)
			}
		})
	}
}

// T-1050 (RV-148 verify #1): the reuse also requires the config revision the kept agent's start recorded to be the
// config's now. An alias is mutable config: another models.<model>.model (another model id behind the same ref) or
// another kinds.<kind>.args between the tasks makes a new start of the same ref another launch, so pj-l-01 is stopped
// and started fresh (the dispatch's stop, then its start). A config written back as it was (other bytes, the same
// effective config) changes nothing: T-2's wake typed, no stop, no start.
func TestKeepLaneRestartsWhenTheConfigChangedSinceItsStart(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		change  func(r *loopRig)
		restart bool
	}{
		{name: "another model id for sonnet (models.sonnet.model)", restart: true, change: func(r *loopRig) {
			r.setConfig("models.sonnet", map[string]any{"kind": "claude", "model": "sonnet-4-6"})
		}},
		{name: "another kind's args (kinds.claude.args)", restart: true, change: func(r *loopRig) {
			r.setConfig("kinds.claude.args", []string{"--fallback-model", "haiku"})
		}},
		{name: "nothing changed (the same config written back)", change: func(r *loopRig) {
			r.setConfig("name", "proj")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := keepSecond(t, false, c.change, wire.DispatchReq{Types: []string{"sonnet"}})
			launches := r.workerLaunches()
			if len(launches) == 0 || launches[0].Prompt != "[obor job T-1/1/work] call the obor job_next tool" ||
				launches[0].Type != "sonnet" {
				t.Fatalf("precondition: T-1's start of sonnet first: %+v", launches)
			}
			_, stopped := r.record("T-2", event.KindResult, event.StepWorkerStop)
			_, started := r.record("T-2", event.KindResult, event.StepStartWork)
			typed := slices.ContainsFunc(r.prompts(), func(s sent) bool {
				return s.to == "pj-l-01" && s.text == "[obor job T-2/1/work] call the obor job_next tool"
			})
			if !c.restart {
				if stopped || started || !typed || len(launches) != 1 {
					t.Errorf("T-2 stopped %v, started %v, its wake typed %v, launches %+v; want T-1's agent reused",
						stopped, started, typed, launches)
				}
				return
			}
			if !stopped || !started || typed {
				t.Errorf("T-2 stopped %v, started %v, its wake typed %v; want pj-l-01 stopped, then started fresh",
					stopped, started, typed)
			}
			if len(launches) != 2 || !launches[1].Fresh || launches[1].Type != "sonnet" ||
				launches[1].Prompt != "[obor job T-2/1/work] call the obor job_next tool" {
				t.Errorf("launches %+v; want T-2's fresh start of sonnet carrying its wake", launches)
			}
		})
	}
}

// T-1048 (RV-148 #2): a busy keep lane's next task is the scheduler's choice (passOrder): with an ordinary T-2 and then
// a high T-3 waiting behind T-1, obor status lists T-3 first and T-3 takes the lane when T-1 merges, T-2 queued behind
// it.
func TestKeepLaneHighWaitRunsBeforeAnOlderOrdinaryOne(t *testing.T) {
	t.Parallel()
	r, _ := keepRig(t)
	r.specOf("T-3", "# T-3 — High\n\nDifficulty: medium\nPriority: high\n")
	if reply, err := r.dispatchTo("d1", "T-1", "L-01"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
		t.Fatalf("T-1: %q, %v", reply, err)
	}
	for _, c := range []struct{ id, task string }{{"d2", "T-2"}, {"d3", "T-3"}} {
		want := "WAITING " + c.task + " (queued): behind T-1 on keep lane L-01"
		if reply, err := r.dispatchTo(c.id, c.task, "L-01"); err != nil || reply != want {
			t.Fatalf("%s: %q, %v; want %q", c.task, reply, err, want)
		}
	}
	if !r.taskStatus("T-3").High || r.taskStatus("T-2").High {
		t.Fatalf("precondition: T-3 high, T-2 not: %+v, %+v", r.taskStatus("T-3"), r.taskStatus("T-2"))
	}
	if q := r.app.Status().Project.Queues["L-01"]; !slices.Equal(q, []string{"T-3", "T-2"}) {
		t.Errorf("L-01's queue %q, want T-3, T-2 (the high one first)", q)
	}
	r.waitJob("pj-l-01", "T-1/1/work")
	r.runToMerge("T-1", nil)
	r.waitJob("pj-l-01", "T-3/1/work")
	if _, ok := r.record("T-2", event.KindFact, event.StepDispatched); ok {
		t.Error("T-2 dispatched while T-3 holds the lane")
	}
	r.waitFor("T-2 behind T-3", func() bool { return r.taskStatus("T-2").Why == "behind T-3 on keep lane L-01" })
	if q := r.app.Status().Project.Queues["L-01"]; !slices.Equal(q, []string{"T-2"}) {
		t.Errorf("L-01's queue %q after T-3's dispatch, want T-2", q)
	}
}

// T-1048 (RV-148 #2): an older wait for a keep lane whose models are all paused does not hold the lane: when T-1
// merges, the later T-3 (sonnet) takes it and T-2 (opus, paused) keeps waiting.
func TestKeepLaneWaitOnAPausedModelDoesNotBlockTheNext(t *testing.T) {
	t.Parallel()
	r, _ := keepRig(t)
	if reply, err := r.dispatchTo("d1", "T-1", "L-01"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
		t.Fatalf("T-1: %q, %v", reply, err)
	}
	if reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: "d2"}, Task: "T-2", Lane: "L-01",
		Types: []string{"opus"}}); err != nil || reply != "WAITING T-2 (queued): behind T-1 on keep lane L-01" {
		t.Fatalf("T-2: %q, %v", reply, err)
	}
	if reply, err := r.dispatchTo("d3", "T-3", "L-01"); err != nil || reply != "WAITING T-3 (queued): behind T-2 on keep lane L-01" {
		t.Fatalf("T-3: %q, %v (T-2 ahead while opus is not paused)", reply, err)
	}
	if _, err := r.pause("", "p-opus", wire.PauseReq{Model: "opus", Why: "quota"}); err != nil {
		t.Fatal(err)
	}
	r.waitJob("pj-l-01", "T-1/1/work")
	r.runToMerge("T-1", nil)
	r.waitJob("pj-l-01", "T-3/1/work")
	if _, ok := r.record("T-2", event.KindFact, event.StepDispatched); ok {
		t.Error("T-2 dispatched on its paused model")
	}
	if !r.taskStatus("T-2").Waiting {
		t.Errorf("T-2 %+v, want still waiting", r.taskStatus("T-2"))
	}
}
