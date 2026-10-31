package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/store"
)

// T-604: a green task whose worker's report leaves open questions goes to the lead before the integrator's review: the
// gate runs (it costs no tokens), the review job waits for the lead's decision (obor resume), and a sendback from the
// hold starts the next attempt.

// seedQuestions is project pj with an integrator, lane O-01's T-1 submitted with report and its lane gate green.
func seedQuestions(t *testing.T, report string) (*harness, *schedScratch) {
	t.Helper()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","roles":{"integrator":{}}}`)
	f := &schedScratch{h: h}
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Scratch: f, Agents: f, Typer: f}, nil }
	if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.connect()
	h.a.mu.Lock()
	h.a.p.lr.rows["O-01"] = store.LaneRow{Lane: "O-01", Task: "T-1"} // the lane holds T-1
	h.a.mu.Unlock()
	addRecord(t, h, "T-1", StepSubmitted, KindFact, submitData{Lane: "O-01", Agent: "pj-o-01", Pane: "w1:p1",
		Report: report})
	addRecord(t, h, "T-1", StepSubmit, KindResult, submitOut{SHA: "s1"})
	addRecord(t, h, "T-1", StepGate, KindIntent, gateIn{Kind: InputLane, Lane: "O-01", Input: "s1"})
	addRecord(t, h, "T-1", StepGate, KindResult, gateOut{Outcome: outGreen, SpecRev: 1,
		Candidate: Candidate{Task: "T-1", Lane: "O-01", SHA: "c1", Target: "base1", Patch: "patch1"}})
	if mt := h.a.p.loop["T-1"]; mt.state != stGreen || mt.report != report {
		t.Fatalf("precondition: T-1 %s with report %q; want green with the submitted report", mt.state, mt.report)
	}
	return h, f
}

// pickGreenStep runs the step pickGreen selects for T-1 (live, or after a replay of the log).
func pickGreenStep(t *testing.T, h *harness, f *schedScratch, replay bool) *mtask {
	t.Helper()
	p := h.a.p
	if replay {
		p = reloadProj(t, h)
	}
	s := h.a.pickGreen(p, *p.loop["T-1"])
	if s == nil || !s(context.Background(), &Loop{Agents: f, Typer: f}) {
		t.Fatal("pickGreen: no step, or it failed")
	}
	return p.loop["T-1"]
}

func TestOpenQuestionsHoldGreenBeforeReview(t *testing.T) {
	const full = "Summary: done.\nSelf-check: ok.\nSkipped: none."
	for _, c := range []struct {
		name, report, quote string // quote "": the review job goes out
	}{
		{"section absent", full, ""},
		{"none", full + "\nOpen questions: none", ""},
		{"n/a", full + "\nOpen questions: N/A", ""},
		{"dash", full + "\nOpen questions: -", ""},
		{"no", full + "\nOpen questions: no", ""},
		{"none with its period", full + "\nOpen questions: none.", ""},
		{"none then concerns", full + "\nOpen questions: none\nConcerns: none", ""},
		{"real text", full + "\nOpen questions: should the gate run the full check?", "should the gate run the full check?"},
		{"a trailing none line", full + "\nOpen questions:\n- which DB?\n- none", "- which DB? - none"},
		{"a colon in the text", full + "\nOpen questions:\nWhich DB: sqlite or pg?", "Which DB: sqlite or pg?"},
		{"ended by the next section", full + "\nOpen questions: may I edit the hook?\nConcerns: none",
			"may I edit the hook?"},
		{"ended by a markdown heading", full + "\nOpen questions: may I edit the hook?\n## Notes\nnothing else",
			"may I edit the hook?"},
		{"long text cut to 300", full + "\nOpen questions: " + strings.Repeat("a", questionsMax+100),
			strings.Repeat("a", questionsMax)},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, f := seedQuestions(t, c.report)
			mt := pickGreenStep(t, h, f, false)
			if c.quote == "" {
				if mt.state != stAwaiting || mt.reviews != 1 || mt.job != StepReview || mt.heldQuestions {
					t.Fatalf("reviewed: T-1 %s, reviews %d, job %q, held %v; want the review job out",
						mt.state, mt.reviews, mt.job, mt.heldQuestions)
				}
				if n := countStep(t, h, StepNeedsYou); n != 0 {
					t.Errorf("%d needs-you facts; want none", n)
				}
				return
			}
			want := "open questions: " + c.quote
			if mt.state != stNeedsYou || !mt.heldQuestions || mt.reviews != 0 || mt.job != "" {
				t.Fatalf("held: T-1 %s, reviews %d, job %q, held %v; want needs-you before any review",
					mt.state, mt.reviews, mt.job, mt.heldQuestions)
			}
			if d := mt.needs.Fact(); d.Cause != causeOpen || d.Reason != want {
				t.Errorf("needs-you %+v; want cause %q, reason %q", d, causeOpen, want)
			}
			if h.a.p.log.Open(Key{"T-1", 1, StepReview}) {
				t.Error("the review job is open; want it held for the lead")
			}
		})
	}
}

// The lead's resume with a decision sends the review job, the decision in it; the hold survives a restart (replay).
func TestOpenQuestionsResumeSendsReviewWithDecision(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "replay"}[replay], func(t *testing.T) {
			h, f := seedQuestions(t, "Summary: done.\nSelf-check: ok.\nSkipped: none.\n"+
				"Open questions: should it use sqlite?")
			if mt := pickGreenStep(t, h, f, false); mt.state != stNeedsYou {
				t.Fatalf("precondition: T-1 %s; want held for the open questions", mt.state)
			}
			const decision = "use sqlite, it is cgo-free"
			reply, err := h.a.resume(Call{Root: h.root, Agent: "pj-lead", Role: "lead", CallID: "r1", Task: "T-1",
				Note: decision})
			if err != nil || reply != "RESUMED T-1: reprompt (open questions)" {
				t.Fatalf("resume: %q, %v; want the reprompt of the hold", reply, err)
			}
			mt := pickGreenStep(t, h, f, replay)
			if mt.state != stAwaiting || mt.reviews != 1 || mt.job != StepReview {
				t.Fatalf("after the resume: T-1 %s, reviews %d, job %q; want the review job out", mt.state, mt.reviews,
					mt.job)
			}
			raw, err := h.a.jobNext(Call{Root: h.root, Agent: "pj-int", Role: "integrator", CallID: "j1"})
			if err != nil {
				t.Fatal(err)
			}
			var jr JobReply
			if err := json.Unmarshal([]byte(raw), &jr); err != nil {
				t.Fatal(err)
			}
			if jr.State != "job" || jr.Job != "T-1/1/review" {
				t.Fatalf("job_next: %+v; want T-1/1/review", jr)
			}
			if want := "Lead's decision on your open questions: " + decision; !strings.Contains(jr.Text, want) {
				t.Errorf("the review job does not carry %q:\n%s", want, jr.Text)
			}
		})
	}
}

// The lead's sendback from the hold starts the next attempt: the worker's fix job.
func TestOpenQuestionsSendbackStartsNextAttempt(t *testing.T) {
	h, f := seedQuestions(t, "Summary: done.\nSelf-check: ok.\nSkipped: none.\nOpen questions: is this in scope?")
	if mt := pickGreenStep(t, h, f, false); mt.state != stNeedsYou {
		t.Fatalf("precondition: T-1 %s; want held for the open questions", mt.state)
	}
	reply, err := h.a.verdict(context.Background(), MethodSendback, Call{Root: h.root, Agent: "pj-lead", Role: "lead",
		CallID: "sb1", Task: "T-1", Note: "drop that part"})
	if err != nil || !strings.HasPrefix(reply, "SENDBACK T-1") {
		t.Fatalf("sendback: %q, %v", reply, err)
	}
	mt := h.a.p.loop["T-1"]
	if mt.state != stWorking || mt.attempt != 2 || mt.sendbacks != 1 || mt.needs.Seq != 0 {
		t.Errorf("after the sendback: T-1 %s, attempt %d, sendbacks %d, needs %d; want attempt 2 working, no needs-you",
			mt.state, mt.attempt, mt.sendbacks, mt.needs.Seq)
	}
}
