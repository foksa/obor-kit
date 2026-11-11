package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

func TestPromptFreshDelivered(t *testing.T) {
	t.Parallel()
	h, fx, f, _ := promptRig(t, true)
	if !h.step(t, fx) { // work
		t.Fatal("work job not sent")
	}
	if strings.Join(f.jobs, " ") != "T-1/1/work" || !f.clears[0] || h.result(event.StepWork) != `"sent"` {
		t.Fatalf("prompts %v clears %v result %s; want the work job once, cleared first, sent", f.jobs, f.clears,
			h.result(event.StepWork))
	}
	h.a.opt.Loop = func(string) (*Loop, error) { return fx, nil } // job_next renders the worker prompt from its kit
	if _, err := h.a.jobNext(wire.JobNextReq{Envelope: wire.Envelope{Root: h.proj().root, Agent: "pj-l-01",
		Role: "worker", CallID: "n1"}}); err != nil {
		t.Fatal(err) // delivered
	}
	if h.proj().folded.loop["T-1"].state != stWorking {
		t.Errorf("state %s; want working", h.proj().folded.loop["T-1"].state)
	}
}

// Enter lost → submit-only is the Prompter's own row: herdr.TestPanesPromptDelivery ("enter lost: one submit-only").

// The send succeeds, its result's write fails: the next pass writes the
// result, nothing is typed again and no failure is counted.
func TestPromptResultLost(t *testing.T) {
	t.Parallel()
	h, fx, f, _ := promptRig(t, true)
	refuseOnce(h.proj(), event.StepWork)
	if h.step(t, fx) {
		t.Fatal("precondition: the result write failed, the step is not done")
	}
	h.step(t, fx)
	if len(f.jobs) != 1 || h.result(event.StepWork) != `"sent"` || h.count(event.KindFact, event.StepFailed) != 0 {
		t.Errorf("prompts %v, result %s, failed %d; want one prompt, its result sent, nothing counted", f.jobs,
			h.result(event.StepWork), h.count(event.KindFact, event.StepFailed))
	}
}

// A transport error (or a failed Enter) after typing began: sent, final under its key (the delivery timers catch a
// lost wake).
func TestPromptTransportAfterTyping(t *testing.T) {
	t.Parallel()
	h, fx, f, _ := promptRig(t, true)
	f.outcomes = []boxOutcome{{typed: true, err: errors.New("herdr agent.prompt: connection reset")}}
	if !h.step(t, fx) {
		t.Fatal("a prompt whose typing began is not a failure")
	}
	h.step(t, fx) // the delivery timer: nothing due
	if len(f.jobs) != 1 || h.result(event.StepWork) != `"sent: herdr agent.prompt: connection reset"` ||
		h.count(event.KindFact, event.StepFailed) != 0 {
		t.Errorf("prompts %v, result %s, failed %d; want one prompt, sent with the error, nothing counted", f.jobs,
			h.result(event.StepWork), h.count(event.KindFact, event.StepFailed))
	}
}

// Refused before typing (herdr's refusal): counted, sent again under its key, no second intent.
func TestPromptRefusedBeforeTyping(t *testing.T) {
	t.Parallel()
	h, fx, f, _ := promptRig(t, true)
	f.outcomes = []boxOutcome{{err: errors.New("agent_blocked")}}
	if h.step(t, fx) || h.count(event.KindFact, event.StepFailed) != 1 {
		t.Fatalf("precondition: the refusal is a counted failure (%d)", h.count(event.KindFact, event.StepFailed))
	}
	if !h.step(t, fx) {
		t.Fatal("the work job not sent again")
	}
	if strings.Join(f.jobs, " ") != "T-1/1/work T-1/1/work" || h.count(event.KindIntent, event.StepWork) != 1 ||
		h.result(event.StepWork) != `"sent"` || f.box["w1:p1"] != "[obor job T-1/1/work]" {
		t.Errorf("prompts %v, intents %d, result %s, box %q; want the job twice, one intent, sent, the text once",
			f.jobs, h.count(event.KindIntent, event.StepWork), h.result(event.StepWork), f.box["w1:p1"])
	}
}

// After two delivery waits obor resume sends work.2; the old
// job's text still in the box is cleared first, so the box holds work.2 once.
func TestPromptResumeClears(t *testing.T) {
	t.Parallel()
	h, fx, f, _ := promptRig(t, true)
	h.step(t, fx) // work
	h.now = h.now.Add(deliveryWait)
	h.step(t, fx) // retry.work
	h.now = h.now.Add(deliveryWait)
	h.step(t, fx) // needs-you{job not delivered}
	if _, err := h.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Role: "lead", CallID: "r1"},
		Task: "T-1"}); err != nil {
		t.Fatal(err)
	}
	h.step(t, fx) // work.2
	if strings.Join(f.jobs, " ") != "T-1/1/work T-1/1/work T-1/1/work.2" || !f.clears[1] || !f.clears[2] ||
		f.box["w1:p1"] != "[obor job T-1/1/work.2]" {
		t.Errorf("prompts %v, clears %v, box %q; want work, its retry and work.2 each cleared first, the box work.2 once",
			f.jobs, f.clears, f.box["w1:p1"])
	}
}

// A replayed log with a pending prompt intent (a crash after typing began, its result unwritten), an old
// log's fact typing after it: the replay sends the wake once more under its key, and the worker's job_next delivers
// the job.
func TestPromptReplayResends(t *testing.T) {
	t.Parallel()
	h, fx, f, _ := promptRig(t, true)
	raw, _ := json.Marshal(promptData{Agent: "pj-l-01", Job: "T-1/1/work"})
	h.log(event.Record{Task: "T-1", Attempt: 1, Step: event.StepWork, Kind: event.KindIntent, Class: event.ClassPrompt, Data: raw})
	h.log(event.Record{Task: "T-1", Attempt: 1, Step: event.StepFailed, Kind: event.KindFact, Data: mustJSON(event.FactData{Machine: Machine,
		Merge: true, Step: event.StepWork, Reason: "work: agent_blocked"})})
	h.log(event.Record{Task: "T-1", Attempt: 1, Step: "typing", Kind: event.KindFact, Data: mustJSON(event.FactData{Machine: Machine,
		Merge: true, Step: event.StepWork})})
	reloadProj(t, h) // the crash
	h.proj().dispatching = true
	if !h.proj().log.Open(event.Key{Task: "T-1", Attempt: 1, Step: event.StepWork}) || h.result(event.StepWork) != "" {
		t.Fatalf("precondition: the work job's prompt is pending after the replay (result %s)", h.result(event.StepWork))
	}
	if !h.step(t, fx) {
		t.Fatal("the pending wake was not sent again")
	}
	h.step(t, fx) // the delivery timer: nothing due
	if strings.Join(f.jobs, " ") != "T-1/1/work" || h.result(event.StepWork) != `"sent"` {
		t.Fatalf("prompts %v, result %s; want the wake sent once more, sent", f.jobs, h.result(event.StepWork))
	}
	h.a.opt.Loop = func(string) (*Loop, error) { return fx, nil }
	if _, err := h.a.jobNext(wire.JobNextReq{Envelope: wire.Envelope{Root: h.proj().root, Agent: "pj-l-01",
		Role: "worker", CallID: "n1"}}); err != nil {
		t.Fatal(err)
	}
	if st := h.proj().folded.loop["T-1"].state; st != stWorking || h.count(event.KindFact, event.StepDelivered) != 1 {
		t.Errorf("state %s, delivered facts %d; want working, the job delivered once", st,
			h.count(event.KindFact, event.StepDelivered))
	}
}

// The integrator's typed wakes go through the same helper: a resolve job (its start done) to an integrator whose box may hold another
// task's unsent job is cleared first; its result lost, it is not typed again. The job goes to its pane (the registry's).
func TestPromptIntegratorJob(t *testing.T) {
	t.Parallel()
	h, s := seedSched(t, true)
	f := &boxPrompter{box: map[string]string{"w1:p9": "[obor job T-2/1/review]"}}
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Workflow: WorkflowOf(s), Agents: s, Prompter: f}, nil }
	p := h.proj()
	raw, _ := json.Marshal(promptData{Agent: "pj-int", Job: "T-2/1/review"})
	r, err := p.log.Append(event.Record{Task: "T-2", Attempt: 1, Step: event.StepReview, Kind: event.KindIntent, Class: event.ClassPrompt,
		Data: raw}, h.now)
	if err != nil {
		t.Fatal(err)
	}
	p.foldRecord(r)
	// A job's first start is a fresh session; this job's start ran already ("running"), so its wake is typed.
	for _, rec := range []event.Record{
		{Task: "T-1", Attempt: 1, Step: stepStart + event.StepResolve, Kind: event.KindIntent, Class: event.ClassStart,
			Data: mustJSON(startData{Agent: "pj-int"})},
		{Task: "T-1", Attempt: 1, Step: stepStart + event.StepResolve, Kind: event.KindResult, Data: mustJSON("running")},
	} {
		r, err := p.log.Append(rec, h.now)
		if err != nil {
			t.Fatal(err)
		}
		p.foldRecord(r)
	}
	refuseOnce(p, event.StepResolve)
	ctx := context.Background()
	s.Launch(ctx, port.Launch{})
	h.tick(0) // the check identifies the running integrator by its label
	h.a.advance(ctx)
	h.a.advance(ctx)
	if strings.Join(f.jobs, " ") != "T-1/1/resolve" || !f.clears[0] || f.box["w1:p9"] != "[obor job T-1/1/resolve]" ||
		h.result(event.StepResolve) != `"sent"` {
		t.Errorf("prompts %v, clears %v, box %q, result %s; want the resolve job once, cleared first, sent", f.jobs,
			f.clears, f.box["w1:p9"], h.result(event.StepResolve))
	}
}

// Row 4's replay: a pending agent.start reads the snapshot's session id, never the transcript file.
func TestStartReplay(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name                string
		ran                 bool
		atIntent, now       string // the session ids at the intent and in the snapshot now
		recorded            string // the agent's session recorded from the pane now ("": none)
		agentNow            bool
		restarts            int
		refused, transcript string
	}{
		{name: "same session: started again", ran: true, atIntent: "old", now: "old", agentNow: true, restarts: 1,
			transcript: "/old-session-found-late.jsonl"},
		{name: "another session: started", ran: true, atIntent: "old", now: "fresh", agentNow: true},
		{name: "no agent at the intent, one now: started", now: "fresh", agentNow: true},
		{name: "no agent now: started again", ran: true, atIntent: "old", restarts: 1},
		{name: "no session id at the intent: cannot tell", ran: true, now: "fresh", agentNow: true,
			refused: "cannot tell"},
		{name: "no session id now: cannot tell", ran: true, atIntent: "old", agentNow: true, refused: "cannot tell"},
		{name: "no herdr session, another session recorded from the pane: started", ran: true, atIntent: "old",
			recorded: "fresh", agentNow: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, fx, _, ag := promptRig(t, false)
			d := startData{Lane: "L-01", Agent: "pj-l-01", Pane: "w1:p1", Ran: c.ran, Session: c.atIntent}
			raw, _ := json.Marshal(d)
			h.log(event.Record{Task: "T-1", Attempt: 1, Step: event.StepStartWork, Kind: event.KindIntent, Class: event.ClassStart, Data: raw})
			reloadProj(t, h) // the crash
			h.proj().dispatching = true
			o := h.a.obs["w1:p1"]
			o.pane.Agent, o.pane.Session, o.pane.Transcript = "", c.now, c.transcript
			if c.agentNow {
				o.pane.Agent = "claude"
			}
			if c.recorded != "" {
				h.session("pj-l-01", c.recorded, "/t/"+c.recorded+".jsonl", 100)
			}
			h.step(t, fx)
			mt := h.proj().folded.loop["T-1"]
			if ag.restarts != c.restarts || !strings.Contains(mt.refused, c.refused) ||
				(c.refused == "") != mt.started {
				t.Fatalf("restarts %d, refused %q, started %v; want %d, %q", ag.restarts, mt.refused, mt.started,
					c.restarts, c.refused)
			}
			if c.refused != "" {
				h.step(t, fx)
				if !h.has("needs-you{start: cannot tell") {
					t.Errorf("no needs-you{start: cannot tell …}: %v", h.facts(0))
				}
			}
		})
	}
}

// The intent records herdr's session id, not the transcript (none
// found); the start succeeds, its result's write fails: the next pass writes the result and does not start a second
// agent, even while the snapshot still shows the old session.
func TestStartResultLost(t *testing.T) {
	t.Parallel()
	h, fx, _, ag := promptRig(t, false)
	ag.lag = true
	o := h.a.obs["w1:p1"]
	o.pane.Session, o.pane.Transcript = "old", ""
	refuseOnce(h.proj(), event.StepStartWork)
	if h.step(t, fx) {
		t.Fatal("precondition: the result write failed")
	}
	r, _ := h.proj().log.Intent(event.Key{Task: "T-1", Attempt: 1, Step: event.StepStartWork})
	var d startData
	_ = json.Unmarshal(r.Data, &d)
	if !d.Ran || d.Session != "old" {
		t.Fatalf("intent %s; want the agent and its session id old recorded", r.Data)
	}
	h.step(t, fx)
	if ag.restarts != 1 || !h.proj().folded.loop["T-1"].started {
		t.Errorf("restarts %d, started %v; want one start, its result written", ag.restarts, h.proj().folded.loop["T-1"].started)
	}
}
