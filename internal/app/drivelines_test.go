package app_test

import (
	"context"
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"strings"
	"testing"

	"obor/internal/herdr/herdrtest"
)

// T-761: a drive and the lead message each other. The drive agent's obor tell-lead is a lead line naming its task; the
// lead's obor answer on a driving task is a line to the drive agent (no question open), delivered when its pane is idle.

// tellLead is obor tell-lead's request from agent with role in lane.
func (r *loopRig) tellLead(callID, agent, role, lane, text string) (string, error) {
	return wire.Verdict(context.Background(), r.sock, wire.MethodTellLead, wire.Call{Root: r.p.Root, CallID: callID,
		Agent: agent, Role: role, Lane: lane, Note: text})
}

// driverTold is the facts message{driver} in the log (the drive's lines to the lead).
func (r *loopRig) driverTold() []event.Message {
	var out []event.Message
	for _, rec := range r.records() {
		var m event.Message
		_ = json.Unmarshal(rec.Data, &m)
		if rec.Kind == event.KindFact && rec.Step == event.StepMessage && m.Driver {
			out = append(out, m)
		}
	}
	return out
}

// toDrive is how often line reached the drive agent pj-o1-drive (typed into its pane w1:p5, or posted to its inbox).
func (r *loopRig) toDrive(line string) int {
	return r.typedTo("w1:p5", line) + r.inbox.count("pj-o1-drive", line)
}

// driveStatus sets the drive agent's pane status and waits until the app sees it.
func (r *loopRig) driveStatus(status string) {
	r.t.Helper()
	r.f.Set("w1:p5", func(p *herdrtest.Pane) { p.Status = status })
	r.waitFor("pj-o1-drive "+status, func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == "pj-o1-drive" && a.Status == status {
				return true
			}
		}
		r.push()
		return false
	})
	r.connectedAgain() // the pushes refreshed serve: its stream again before a call that asks its identity
}

// leadAnswer is the lead's obor answer on T-1.
func (r *loopRig) leadAnswer(callID, text string) (string, error) {
	return wire.Verdict(context.Background(), r.sock, wire.MethodAnswer, wire.Call{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: callID, Task: "T-1", Note: text})
}

// driving is the drive rig with T-1 a new drive on O-01, driving, its agent pj-o1-drive up in w1:p5.
func driving(t *testing.T) *loopRig {
	t.Helper()
	r := newDriveRig(t)
	r.connectedAgain()
	if _, err := r.drive("v1", "web"); err != nil {
		t.Fatal(err)
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.driveAgentUp()
	return r
}

// The drive agent's obor tell-lead: one fact message{driver} to the lead, its line naming the task and the reply, posted
// to the lead's session once; a repeated call id gets the stored reply and records nothing.
func TestTellLeadReachesTheLead(t *testing.T) {
	t.Parallel()
	r := driving(t)
	if n := len(r.driverTold()); n != 0 {
		t.Fatalf("precondition: drive lines before tell-lead: %d", n)
	}
	want := "TOLD pj-lead (T-1; its answer arrives here as an [obor answer] line)"
	reply, err := r.tellLead("t1", "pj-o1-drive", "driver", "O-01", "may I change the  API contract?")
	if err != nil || reply != want {
		t.Fatalf("tell-lead: %q, %v; want %q", reply, err, want)
	}
	line := `[obor T-1 drive] may I change the API contract? (reply: obor answer T-1 "…")`
	r.waitFor("the line posted to the lead", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	if again, err := r.tellLead("t1", "pj-o1-drive", "driver", "O-01", "may I change the  API contract?"); err != nil ||
		again != want {
		t.Errorf("a repeated call id: %q, %v; want the stored reply", again, err)
	}
	r.passes()
	if told := r.driverTold(); len(told) != 1 || told[0].To != "pj-lead" || told[0].Task != "T-1" {
		t.Errorf("drive lines %+v, want one to pj-lead for T-1", told)
	}
	if n := r.inbox.count("pj-lead", line); n != 1 {
		t.Errorf("the line posted %d times, want once", n)
	}
}

// Anyone but a drive agent is refused, naming who it is for: a worker, a reviewer, the lead itself, the owner; a drive
// agent of a lane with no open drive is refused too. Nothing is recorded.
func TestTellLeadRefusesOthers(t *testing.T) {
	t.Parallel()
	r := driving(t)
	n := len(r.records())
	for i, c := range []struct{ agent, role, lane string }{
		{"pj-o1", "worker", "O-01"}, {"pj-rev", "reviewer", ""}, {"pj-lead", "lead", ""}, {"", "", ""},
	} {
		_, err := r.tellLead("r"+string(rune('0'+i)), c.agent, c.role, c.lane, "hello")
		if err == nil || !strings.Contains(err.Error(), "for a drive agent to tell its lead") {
			t.Errorf("tell-lead by %q (role %q): %v; want refused naming the drive agent", c.agent, c.role, err)
		}
	}
	if _, err := r.tellLead("r9", "pj-o2-drive", "driver", "O-02", "hello"); err == nil ||
		!strings.Contains(err.Error(), "no open drive in lane O-02") {
		t.Errorf("tell-lead from a lane with no drive: %v; want refused", err)
	}
	r.passes()
	if len(r.records()) != n || len(r.driverTold()) != 0 {
		t.Errorf("a refused tell-lead recorded %q", r.steps()[n:])
	}
}

// The lead's obor answer on a driving task needs no question: held while the drive agent is busy, delivered once when
// it is idle; a second answer is its own line (the first is not ended by it). A serve restart between an answer and its
// delivery delivers it once.
func TestAnswerToDriveDelivered(t *testing.T) {
	t.Parallel()
	r := driving(t)
	r.driveStatus("working")
	want := "ANSWERED T-1: obor serve sends pj-o1-drive the answer"
	if reply, err := r.leadAnswer("a1", "yes, change it"); err != nil || reply != want {
		t.Fatalf("answer on the drive: %q, %v; want %q", reply, err, want)
	}
	if reply, err := r.leadAnswer("a2", "and bump the version"); err != nil || reply != want {
		t.Fatalf("a second answer: %q, %v; want %q", reply, err, want)
	}
	first, second := "[obor answer] yes, change it", "[obor answer] and bump the version"
	r.passes()
	if n, m := r.toDrive(first), r.toDrive(second); n != 0 || m != 0 {
		t.Fatalf("delivered while the drive agent is busy: %d, %d", n, m)
	}
	if q := r.app.Status().Project.Messages; len(q) != 2 || !strings.Contains(q[0].Held, "busy") {
		t.Errorf("queued lines %+v, want both held, its agent busy", q)
	}
	r.stop()
	r.start() // the restart between the answers and their delivery
	r.waitFor("T-1 driving after the replay", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.connectedAgain()
	r.driveStatus("idle")
	r.waitFor("both answers delivered", func() bool { return r.toDrive(first) == 1 && r.toDrive(second) == 1 })
	r.passes()
	r.stop()
	r.start()
	r.waitFor("T-1 driving after the second replay", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.passes()
	if n, m := r.toDrive(first), r.toDrive(second); n != 1 || m != 1 {
		t.Errorf("answers delivered %d, %d times, want once each", n, m)
	}
	if q := r.app.Status().Project.Messages; len(q) != 0 {
		t.Errorf("lines still queued %+v", q)
	}
}

// An answer to a task that is not driving keeps today's behaviour: a working task with no question is refused.
func TestAnswerNotDrivingUnchanged(t *testing.T) {
	t.Parallel()
	r := newTakeOverRig(t)
	n := len(r.records())
	if _, err := r.leadAnswer("a1", "yes"); err == nil || !strings.Contains(err.Error(), "T-1 has no open question") {
		t.Errorf("answer on a working task with no question: %v; want refused", err)
	}
	if len(r.records()) != n {
		t.Errorf("a refused answer recorded %q", r.steps()[n:])
	}
}

// An answer still undelivered when the drive lands or is dropped closes unsent: never delivered, no line queued.
func TestAnswerToDriveClosedOnLandOrDrop(t *testing.T) {
	t.Parallel()
	for _, end := range []string{"land", "drop"} {
		t.Run(end, func(t *testing.T) {
			t.Parallel()
			r := driving(t)
			r.driveStatus("working")
			if _, err := r.leadAnswer("a1", "later"); err != nil {
				t.Fatal(err)
			}
			r.passes()
			if q := r.app.Status().Project.Messages; len(q) != 1 {
				t.Fatalf("precondition: queued lines %+v, want the answer", q)
			}
			switch end {
			case "land":
				r.work("page.html", "x\n")
				if _, err := r.landDrive("l1", "T-1", "page"); err != nil {
					t.Fatal(err)
				}
				r.waitStep("1 fact submitted")
			case "drop":
				if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodDrop, wire.Call{Root: r.p.Root,
					Agent: "pj-lead", Role: "lead", CallID: "x1", Task: "T-1", Note: "not needed"}); err != nil {
					t.Fatal(err)
				}
				r.waitFor("dropped", func() bool { return r.stateOf("T-1") == "dropped" })
			}
			r.waitFor("the answer closed", func() bool { return len(r.app.Status().Project.Messages) == 0 })
			r.driveStatus("idle")
			r.passes()
			if n := r.toDrive("[obor answer] later"); n != 0 {
				t.Errorf("the answer delivered %d times after the %s, want never", n, end)
			}
		})
	}
}
