package app

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"strings"
	"testing"
)

// T-775: a busy claim on a task refuses every task verb whose state takes it, with busy's why: a step holding it
// (p.held), its scratch step running (p.running), its next gate intent open. Each verb on a working T-1 (resume of its
// stalled worker, drop, move) and land on a driving one; never submit, since the step holding a working task is the
// one that sent the worker its job (TestEarlyTaskDone), and a drop decides and records in one mu section.
func TestBusyRefusesEveryVerb(t *testing.T) {
	t.Parallel()
	claims := []struct {
		name string
		set  func(h *harness, p *proj)
		why  string
	}{
		{"a step holding it", func(h *harness, p *proj) { p.held["T-1"] = "the fix" }, "T-1 has the fix running"},
		{"its scratch step running", func(h *harness, p *proj) {
			p.running = event.Key{Task: "T-1", Attempt: 1, Step: event.StepGate}
		}, "T-1 has gate running"},
		{"a pending gate intent", func(h *harness, p *proj) {
			h.a.mu.Unlock()
			addRecord(h.t, h, "T-1", event.StepGate, event.KindIntent, gateIn{Kind: "lane", Lane: "L-01"})
			h.a.mu.Lock()
		}, "T-1 has gate pending"},
	}
	verbs := map[string]func(h *harness) error{
		"resume": func(h *harness) error {
			_, err := h.a.resume(wire.Call{Root: h.root, Role: "lead", CallID: "r1", Task: "T-1", Note: "go on"})
			return err
		},
		"drop": func(h *harness) error {
			_, err := h.a.drop(wire.Call{Root: h.root, Role: "lead", CallID: "d1", Task: "T-1", Note: "why"})
			return err
		},
		"move": func(h *harness) error {
			_, err := h.a.dispatchCall(context.Background(), wire.Call{Root: h.root, Role: "lead", CallID: "m1", Task: "T-1",
				Types: []string{"sonnet"}})
			return err
		},
		"land": func(h *harness) error {
			_, err := h.a.landCall(wire.Call{Root: h.root, Role: "lead", CallID: "l1", Task: "T-1", Report: "done"})
			return err
		},
	}
	for _, c := range claims {
		for name, call := range verbs {
			t.Run(c.name+"/"+name, func(t *testing.T) {
				t.Parallel()
				h, p, _ := answerSeed(t, reasonStalled, "", "", true)
				h.a.mu.Lock()
				mt := p.folded.loop["T-1"]
				if name == "land" {
					mt.state, mt.drive = stDriving, true
				}
				if st := mt.state; st != stWorking && st != stDispatched && st != stDriving || !mt.may(map[string]verb{
					"resume": vResume, "drop": vDrop, "move": vMove, "land": vLand}[name]) {
					h.a.mu.Unlock()
					t.Fatalf("precondition: T-1 is %s, which does not take %s", st, name)
				}
				if p.busy("T-1") != "" {
					h.a.mu.Unlock()
					t.Fatalf("precondition: T-1 busy before the claim: %s", p.busy("T-1"))
				}
				c.set(h, p)
				h.a.mu.Unlock()
				err := call(h)
				if err == nil || !strings.Contains(err.Error(), c.why+"; retry when it settles") {
					t.Errorf("%s with %s: %v; want refused %q", name, c.name, err, c.why)
				}
				h.a.mu.Lock()
				defer h.a.mu.Unlock()
				if st := p.folded.loop["T-1"].state; st != mt.state {
					t.Errorf("%s with %s: a refused verb moved T-1 to %s", name, c.name, st)
				}
			})
		}
	}
}

// T-775 (the BACKLOG deadlock): a worker's Blocked "re-gate, not my diff" is a question in force; obor resume
// --regate gates the lane as it is (fact unblocked{regate}: submitted, the lane submitted afresh), the question ends;
// with no flag the text is the answer (the question ends, the task stays working), and --sendback does not fit.
func TestResumeRegateOfABlockedReport(t *testing.T) {
	t.Parallel()
	seed := func(t *testing.T) (*harness, *proj) {
		h, p, fact := answerSeed(t, reasonAsks+"re-gate, not my diff", "", "", true)
		h.a.mu.Lock()
		defer h.a.mu.Unlock()
		if !isAsk(fact) || !p.asks("T-1") || p.folded.loop["T-1"].state != stDispatched && p.folded.loop["T-1"].state != stWorking {
			t.Fatalf("precondition: T-1 %s with its question in force: %+v", p.folded.loop["T-1"].state, fact)
		}
		if path := p.answerPath(fact); path != `obor resume T-1 "<answer>" | obor resume T-1 --regate` {
			t.Errorf("the question's answers %q; want the answer and --regate", path)
		}
		return h, p
	}
	resume := func(h *harness, id, to, note string) (string, error) {
		return h.a.resume(wire.Call{Root: h.root, Role: "lead", Agent: "pj-lead", CallID: id, Task: "T-1", To: to, Note: note})
	}
	t.Run("--regate", func(t *testing.T) {
		t.Parallel()
		h, p := seed(t)
		if _, err := resume(h, "x", wire.ResumeSendback, "fix it"); err == nil ||
			!strings.Contains(err.Error(), "--sendback does not fit: obor resume T-1 sends the worker the answer") {
			t.Errorf("--sendback of a question: %v; want refused naming the answer", err)
		}
		reply, err := resume(h, "r1", wire.ResumeRegate, "")
		if want := "RESUMED T-1: regate (-)"; err != nil || reply != want {
			t.Fatalf("resume --regate: %q, %v; want %q", reply, err, want)
		}
		h.a.mu.Lock()
		mt, asks := *p.folded.loop["T-1"], p.asks("T-1")
		h.a.mu.Unlock()
		if mt.state != stSubmitted || mt.input != "" || mt.attempt != 1 {
			t.Errorf("after --regate: %s, input %q, attempt %d; want submitted afresh in attempt 1", mt.state, mt.input, mt.attempt)
		}
		if asks {
			t.Error("the question is still in force after --regate")
		}
		if again, err := h.a.resume(wire.Call{Root: h.root, Role: "lead", CallID: "r1", Task: "T-1", To: wire.ResumeRegate}); err != nil ||
			again != reply {
			t.Errorf("a repeated call id: %q, %v; want the stored reply", again, err)
		}
	})
	t.Run("the answer", func(t *testing.T) {
		t.Parallel()
		h, p := seed(t)
		reply, err := resume(h, "a1", "", "yes, it is a flake")
		if want := "ANSWERED T-1: obor serve sends pj-l-01 the answer"; err != nil || !strings.HasPrefix(reply, want) {
			t.Fatalf("resume: %q, %v; want %q", reply, err, want)
		}
		h.a.mu.Lock()
		defer h.a.mu.Unlock()
		if mt := p.folded.loop["T-1"]; p.asks("T-1") || mt.attempt != 1 || mt.state == stSubmitted {
			t.Errorf("after the answer: question %v, %s attempt %d; want none, the same attempt, not submitted",
				p.asks("T-1"), mt.state, mt.attempt)
		}
	})
}

// T-775: resumeAct by state: each state that takes no resume is refused by its state; a hold's decision reaches the
// integrator (reprompt, its text required); red past K with neither flag is refused naming both, each flag fits; a
// flag that does not fit names what resume does; a driving task's line goes to the drive agent.
func TestResumeActByState(t *testing.T) {
	t.Parallel()
	withLane := func(mt mtask) mtask { mt.task, mt.attempt, mt.lane, mt.agent = "T-1", 1, "L-01", "pj-l-01"; return mt }
	for _, c := range []struct {
		name string
		mt   mtask
		flag string
		act  string // "": refused
		err  string
	}{
		{"held", mtask{state: stAwaiting, needs: heldOf(event.CauseHeld), attemptVars: attemptVars{job: "review"}}, "", wire.ResumeReprompt, ""},
		{"held --sendback", mtask{state: stAwaiting, needs: heldOf(event.CauseHeld), attemptVars: attemptVars{job: "review"}},
			wire.ResumeSendback, "", "--sendback does not fit: obor resume T-1 sends the integrator its job anew"},
		{"red past K", mtask{state: stRed, needs: heldOf(causeRed)}, "", "", "say --regate or --sendback (red: why)"},
		{"red past K --regate", mtask{state: stRed, needs: heldOf(causeRed)}, wire.ResumeRegate, wire.ResumeRegate, ""},
		{"red past K --sendback", mtask{state: stRed, needs: heldOf(causeRed)}, wire.ResumeSendback, actSendback, ""},
		{"sendbacks past K", mtask{state: stAwaiting, needs: closedOf(causeSendbacks), attemptVars: attemptVars{job: "review"}}, "", "",
			"say --regate or --sendback"},
		{"a failed step", mtask{state: stSubmitted, needs: heldOf("submit" + failedSuffix)}, "", wire.ResumeRetry, ""},
		{"a failed step --regate", mtask{state: stSubmitted, needs: heldOf("submit" + failedSuffix)}, wire.ResumeRegate, "",
			"--regate does not fit: obor resume T-1 retries the failed step"},
		{"a gate failure", mtask{state: stSubmitted, needs: closedOf(causeGate)}, "", wire.ResumeRegate, ""},
		{"a gate failure --regate", mtask{state: stSubmitted, needs: closedOf(causeGate)}, wire.ResumeRegate, wire.ResumeRegate, ""},
		{"a gate failure due", mtask{state: stSubmitted, needs: block{cause: causeGate, why: "gate: x"}}, "", "",
			"T-1 is fail; retry when it settles"},
		{"rejected", mtask{state: stRejected, needs: heldOf(causeRejected)}, "", "", "rejected (rejected: why): obor drop T-1 frees its lane"},
		{"green", mtask{state: stGreen}, "", actSendback, ""},
		{"awaiting", mtask{state: stAwaiting, attemptVars: attemptVars{job: "review"}}, "", actSendback, ""},
		{"awaiting --regate", mtask{state: stAwaiting, attemptVars: attemptVars{job: "review"}}, wire.ResumeRegate, "",
			"--regate does not fit: obor resume T-1 sends it back to its worker"},
		{"driving", mtask{state: stDriving, drive: true}, "", actDrive, ""},
		{"driving --sendback", mtask{state: stDriving, drive: true}, wire.ResumeSendback, "", "sends the drive agent the line"},
		{"a landed drive", mtask{state: stSubmitted, drive: true}, "", "", "T-1 is submitted; retry when it settles"},
		{"a drive dispatched", mtask{state: stDispatched, drive: true}, "", "", "the lead cannot answer a drive until it is driving"},
		{"a drive's needs-you", mtask{state: stDispatched, drive: true, needs: heldOf("lane")}, "", wire.ResumeRetry, ""},
		{"working, nothing asked", mtask{state: stWorking}, "", "", "T-1 is working with nothing to resume"},
		{"gating", mtask{state: stSubmitted}, "", "", "T-1 is submitted; retry when it settles"},
		{"dropped", mtask{state: stDropped}, "", "", "resume: T-1 is dropped"},
		{"a keyword flag", mtask{state: stGreen}, wire.ResumeReprompt, "", "--reprompt is no flag of obor resume"},
	} {
		p := &proj{folded: folded{active: map[string][]event.Record{}}}
		act, err := p.resumeAct(withLane(c.mt), c.flag)
		switch {
		case c.act != "" && (err != nil || act != c.act):
			t.Errorf("%s: %q, %v; want %s", c.name, act, err, c.act)
		case c.act == "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: %q, %v; want refused %q", c.name, act, err, c.err)
		}
	}
	// a hold's decision is required; a gate failure's is not
	p := &proj{}
	if !p.resumeNeedsText(withLane(mtask{state: stAwaiting, needs: heldOf(event.CauseHeld)}), wire.ResumeReprompt) ||
		p.resumeNeedsText(withLane(mtask{state: stSubmitted, needs: closedOf(causeGate)}), wire.ResumeRegate) {
		t.Error("resumeNeedsText: want a hold's decision required, a regate's not")
	}
}
