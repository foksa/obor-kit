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
	h.a.p.folded.lr.rows["O-01"] = store.LaneRow{Lane: "O-01", Task: "T-1"} // the lane holds T-1
	h.a.mu.Unlock()
	addRecord(t, h, "T-1", StepSubmitted, KindFact, submitData{Lane: "O-01", Agent: "pj-o-01", Pane: "w1:p1",
		Report: report})
	addRecord(t, h, "T-1", StepSubmit, KindResult, submitOut{SHA: "s1"})
	addRecord(t, h, "T-1", StepGate, KindIntent, gateIn{Kind: InputLane, Lane: "O-01", Input: "s1"})
	addRecord(t, h, "T-1", StepGate, KindResult, gateOut{Outcome: outGreen, SpecRev: 1,
		Candidate: Candidate{Task: "T-1", Lane: "O-01", SHA: "c1", Target: "base1", Patch: "patch1"}})
	if mt := h.a.p.folded.loop["T-1"]; mt.state != stGreen || mt.report != report {
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
	s := h.a.pickGreen(p, *p.folded.loop["T-1"])
	if s == nil || !s(context.Background(), &Loop{Agents: f, Typer: f}) {
		t.Fatal("pickGreen: no step, or it failed")
	}
	return p.folded.loop["T-1"]
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
	mt := h.a.p.folded.loop["T-1"]
	if mt.state != stWorking || mt.attempt != 2 || mt.sendbacks != 1 || mt.needs.Seq != 0 {
		t.Errorf("after the sendback: T-1 %s, attempt %d, sendbacks %d, needs %d; want attempt 2 working, no needs-you",
			mt.state, mt.attempt, mt.sendbacks, mt.needs.Seq)
	}
}

// fixStep runs the step pickWorking selects for T-1 (the fix job of the attempt): fatal when there is none or it fails.
func fixStep(t *testing.T, h *harness, f *schedScratch) {
	t.Helper()
	p := h.a.p
	h.a.mu.Lock()
	s := h.a.pickWorking(p, *p.folded.loop["T-1"])
	h.a.mu.Unlock()
	if s == nil || !s(context.Background(), &Loop{Scratch: f, Agents: f, Typer: f}) {
		t.Fatal("pickWorking: no step, or it failed")
	}
}

// fixJob is the text of T-1's worker's open job (its job_next call).
func fixJob(t *testing.T, h *harness) JobReply {
	t.Helper()
	raw, err := h.a.jobNext(Call{Root: h.root, Agent: "pj-o-01", Role: "worker", Lane: "O-01", CallID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	var jr JobReply
	if err := json.Unmarshal([]byte(raw), &jr); err != nil {
		t.Fatal(err)
	}
	return jr
}

// T-611: the lead's decision on the worker's open questions reaches the worker too: after the integrator's sendback,
// the fix job carries it, live and after a replay of the log (the crash between the resume and the fix job).
func TestOpenQuestionsDecisionReachesTheFixJob(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "replay"}[replay], func(t *testing.T) {
			h, f := seedQuestions(t, "Summary: done.\nSelf-check: ok.\nSkipped: none.\n"+
				"Open questions: should it use sqlite?")
			if mt := pickGreenStep(t, h, f, false); mt.state != stNeedsYou {
				t.Fatalf("precondition: T-1 %s; want held for the open questions", mt.state)
			}
			const decision = "use sqlite, it is cgo-free"
			if reply, err := h.a.resume(Call{Root: h.root, Agent: "pj-lead", Role: "lead", CallID: "r1", Task: "T-1",
				Note: decision}); err != nil || reply != "RESUMED T-1: reprompt (open questions)" {
				t.Fatalf("resume: %q, %v; want the reprompt of the hold", reply, err)
			}
			if replay {
				reloadProj(t, h) // the crash between the resume and the fix job
			}
			if mt := pickGreenStep(t, h, f, replay); mt.job != StepReview || mt.state != stAwaiting {
				t.Fatalf("precondition: T-1 %s, job %q; want the review job out", mt.state, mt.job)
			}
			if _, err := h.a.verdict(context.Background(), MethodSendback, Call{Root: h.root, Agent: "pj-int",
				Role: "integrator", CallID: "sb1", Task: "T-1", Note: "the report is missing the test"}); err != nil {
				t.Fatal(err)
			}
			fixStep(t, h, f)
			jr := fixJob(t, h)
			want := "Lead's decision on your open questions (attempt 1): " + decision +
				" — do not list these again under Open questions."
			if jr.Job != "T-1/2/fix" || !strings.Contains(jr.Text, want) {
				t.Errorf("the fix job %s does not carry %q:\n%s", jr.Job, want, jr.Text)
			}
		})
	}
}

// T-611: a task the lead never answered has no such line in its fix job.
func TestFixJobWithoutADecisionHasNoLine(t *testing.T) {
	h, f := seedQuestions(t, "Summary: done.\nSelf-check: ok.\nSkipped: none.")
	if mt := pickGreenStep(t, h, f, false); mt.job != StepReview || mt.state != stAwaiting {
		t.Fatalf("precondition: T-1 %s, job %q; want the review job out", mt.state, mt.job)
	}
	if _, err := h.a.verdict(context.Background(), MethodSendback, Call{Root: h.root, Agent: "pj-int",
		Role: "integrator", CallID: "sb1", Task: "T-1", Note: "the report is missing the test"}); err != nil {
		t.Fatal(err)
	}
	fixStep(t, h, f)
	jr := fixJob(t, h)
	if jr.Job != "T-1/2/fix" || !strings.Contains(jr.Text, "the report is missing the test") {
		t.Fatalf("the fix job %s does not carry the sendback's why:\n%s", jr.Job, jr.Text)
	}
	if strings.Contains(jr.Text, "Lead's decision") {
		t.Errorf("a task never held carries a lead's decision:\n%s", jr.Text)
	}
}

// unblockedFact is T-1's last unblocked fact in the log.
func unblockedFact(t *testing.T, h *harness) FactData {
	t.Helper()
	recs, err := ReadLog(h.root)
	if err != nil {
		t.Fatal(err)
	}
	var d FactData
	found := false
	for _, r := range recs {
		if r.Task == "T-1" && r.Step == StepUnblocked {
			if err := json.Unmarshal(r.Data, &d); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no unblocked fact in the log")
	}
	return d
}

// T-612: resuming an open-questions hold needs the lead's decision: with no note or a blank one it is refused before
// anything is recorded, in the default mode and explicit ones; a note is recorded with the decision. Another cause's
// resume without a note goes on as before.
func TestResumeOpenQuestionsNeedsADecision(t *testing.T) {
	const hold = "Summary: done.\nSelf-check: ok.\nSkipped: none.\nOpen questions: should it use sqlite?"
	for _, mode := range []struct{ name, to string }{
		{"default", ""}, {ResumeRegate, ResumeRegate}, {ResumeReprompt, ResumeReprompt},
	} {
		for _, c := range []struct {
			name, note string
			refused    bool
		}{
			{"no note", "", true},
			{"blank note", " \t ", true},
			{"a decision", "use sqlite, it is cgo-free", false},
		} {
			t.Run(mode.name+"/"+c.name, func(t *testing.T) {
				h, f := seedQuestions(t, hold)
				if mt := pickGreenStep(t, h, f, false); mt.state != stNeedsYou || !mt.heldQuestions {
					t.Fatalf("precondition: T-1 %s (held %v); want the open-questions hold", mt.state, mt.heldQuestions)
				}
				reply, err := h.a.resume(Call{Root: h.root, Agent: "pj-lead", Role: "lead", CallID: "r1", Task: "T-1",
					To: mode.to, Note: c.note})
				if c.refused {
					want := `resume: T-1 waits on the lead's answer to its open questions: obor resume T-1 "<decision>"`
					if err == nil || err.Error() != want {
						t.Fatalf("resume: %q, %v; want the refusal %q", reply, err, want)
					}
					if n := countStep(t, h, StepUnblocked); n != 0 {
						t.Errorf("%d unblocked facts; want none", n)
					}
					if mt := h.a.p.folded.loop["T-1"]; mt.state != stNeedsYou || mt.needs.Fact().Cause != causeOpen {
						t.Errorf("after the refused resume: T-1 %s (%s); want the hold in force", mt.state,
							mt.needs.Fact().Cause)
					}
					return
				}
				to := mode.to
				if to == "" {
					to = ResumeReprompt
				}
				if want := "RESUMED T-1: " + to + " (open questions)"; err != nil || reply != want {
					t.Fatalf("resume: %q, %v; want %q", reply, err, want)
				}
				d := unblockedFact(t, h)
				if d.Cause != causeOpen || d.Reason != to || d.Note != c.note {
					t.Errorf("unblocked %+v; want cause %q, %q, the decision %q", d, causeOpen, to, c.note)
				}
				if mt := h.a.p.folded.loop["T-1"]; mt.needs.Seq != 0 {
					t.Errorf("T-1 still holds a needs-you; want the hold ended")
				}
			})
		}
	}
	t.Run("another cause with no note", func(t *testing.T) {
		h, f := seedQuestions(t, "Summary: done.\nSelf-check: ok.\nSkipped: none.")
		if mt := pickGreenStep(t, h, f, false); mt.state != stAwaiting {
			t.Fatalf("precondition: T-1 %s; want the review job out", mt.state)
		}
		addRecord(t, h, "T-1", StepNeedsYou, KindFact, FactData{Lane: "O-01", Reason: "held: the owner's call",
			Merge: true, Cause: causeHeld})
		if mt := h.a.p.folded.loop["T-1"]; mt.state != stNeedsYou || mt.needs.Fact().Cause != causeHeld {
			t.Fatalf("precondition: T-1 %s; want the held needs-you", mt.state)
		}
		reply, err := h.a.resume(Call{Root: h.root, Agent: "pj-lead", Role: "lead", CallID: "r1", Task: "T-1"})
		if want := "RESUMED T-1: reprompt (held)"; err != nil || reply != want {
			t.Fatalf("resume: %q, %v; want %q", reply, err, want)
		}
		if d := unblockedFact(t, h); d.Cause != causeHeld || d.Note != "" {
			t.Errorf("unblocked %+v; want cause %q, no note", d, causeHeld)
		}
	})
}

// addAttempt is addRecord for a record of another attempt (the worker's next submission).
func addAttempt(t *testing.T, h *harness, attempt int, step, kind string, d any) {
	t.Helper()
	p := h.a.p
	raw, _ := json.Marshal(d)
	r, err := p.log.Append(Record{Task: "T-1", Attempt: attempt, Step: step, Kind: kind, Data: raw}, h.now)
	if err != nil {
		t.Fatal(err)
	}
	p.foldRecord(r)
}

// T-611: a new attempt does not clear the decision: the worker's next submission (another attempt) keeps it, and the
// fix job after the next sendback carries it still.
func TestOpenQuestionsDecisionOutlivesANewAttempt(t *testing.T) {
	h, f := seedQuestions(t, "Summary: done.\nSelf-check: ok.\nSkipped: none.\nOpen questions: should it use sqlite?")
	if mt := pickGreenStep(t, h, f, false); mt.state != stNeedsYou {
		t.Fatalf("precondition: T-1 %s; want held for the open questions", mt.state)
	}
	const decision = "use sqlite, it is cgo-free"
	if _, err := h.a.resume(Call{Root: h.root, Agent: "pj-lead", Role: "lead", CallID: "r1", Task: "T-1",
		Note: decision}); err != nil {
		t.Fatal(err)
	}
	if mt := pickGreenStep(t, h, f, false); mt.job != StepReview {
		t.Fatalf("precondition: T-1 job %q; want the review job out", mt.job)
	}
	if _, err := h.a.verdict(context.Background(), MethodSendback, Call{Root: h.root, Agent: "pj-int",
		Role: "integrator", CallID: "sb1", Task: "T-1", Note: "the report is missing the test"}); err != nil {
		t.Fatal(err)
	}
	// attempt 2's task_done: the worker fixed it, its report leaving no open questions.
	addAttempt(t, h, 2, StepSubmitted, KindFact, submitData{Lane: "O-01", Agent: "pj-o-01", Pane: "w1:p1",
		Report: "Summary: done.\nSelf-check: ok.\nSkipped: none."})
	addAttempt(t, h, 2, StepSubmit, KindResult, submitOut{SHA: "s2"})
	addAttempt(t, h, 2, StepGate, KindIntent, gateIn{Kind: InputLane, Lane: "O-01", Input: "s2"})
	addAttempt(t, h, 2, StepGate, KindResult, gateOut{Outcome: outGreen, SpecRev: 1,
		Candidate: Candidate{Task: "T-1", Lane: "O-01", SHA: "c2", Target: "base1", Patch: "patch2"}})
	if mt := h.a.p.folded.loop["T-1"]; mt.attempt != 2 || mt.state != stGreen || mt.openDecision != decision {
		t.Fatalf("after the second submission: T-1 attempt %d, %s, decision %q; want attempt 2 green with the decision",
			mt.attempt, mt.state, mt.openDecision)
	}
	if mt := pickGreenStep(t, h, f, false); mt.job != StepReview || mt.state != stAwaiting {
		t.Fatalf("precondition: T-1 %s, job %q; want the review job out", mt.state, mt.job)
	}
	if _, err := h.a.verdict(context.Background(), MethodSendback, Call{Root: h.root, Agent: "pj-int",
		Role: "integrator", CallID: "sb2", Task: "T-1", Note: "the test still fails"}); err != nil {
		t.Fatal(err)
	}
	fixStep(t, h, f)
	jr := fixJob(t, h)
	want := "Lead's decision on your open questions (attempt 1): " + decision
	if jr.Job != "T-1/3/fix" || !strings.Contains(jr.Text, want) {
		t.Errorf("the fix job %s does not carry %q:\n%s", jr.Job, want, jr.Text)
	}
}

// T-616: an Open questions section whose first word is none, n/a, no or - leaves nothing open, whatever follows.
func TestOpenQuestionsFirstWord(t *testing.T) {
	for _, c := range []struct {
		section string
		open    bool
	}{
		{"none", false}, {"None.", false}, {"none (the lead decided X).", false}, {"None — answered", false},
		{"n/a", false}, {"-", false}, {"", false}, {"No.", false}, {"no (settled)", false},
		{"Nonetheless, should X?", true}, {"Should X?", true}, {"no idea whether X", true},
	} {
		got := openQuestions("Summary: s\nOpen questions: " + c.section + "\n")
		if (got != "") != c.open {
			t.Errorf("section %q: openQuestions = %q, want open=%v", c.section, got, c.open)
		}
	}
	if got := openQuestions("Open questions: Should X?\n"); got != "Should X?" {
		t.Errorf("a question must come back as its text, got %q", got)
	}
}
