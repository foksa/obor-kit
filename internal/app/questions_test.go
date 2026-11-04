package app

import (
	"context"
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/store"
)

// T-649 (reverses T-604): a green task whose worker's report leaves open questions gets its review job, the questions
// in it: the integrator decides the ones inside the spec's goal and holds the rest for the lead.

// seedQuestions is project pj with an integrator, lane L-01's T-1 submitted with report and its lane gate green.
func seedQuestions(t *testing.T, report string) (*harness, *schedScratch) {
	t.Helper()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","roles":{"integrator":""}}`)
	f := &schedScratch{h: h}
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Scratch: f, Agents: f, Typer: f}, nil }
	if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.connect()
	h.a.mu.Lock()
	h.a.p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"} // the lane holds T-1
	h.a.mu.Unlock()
	addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01", Pane: "w1:p1",
		Report: report})
	addRecord(t, h, "T-1", event.StepSubmit, event.KindResult, submitOut{SHA: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outGreen, SpecRev: 1,
		Candidate: port.Candidate{Task: "T-1", Lane: "L-01", SHA: "c1", Target: "base1", Patch: "patch1"}})
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
func fixJob(t *testing.T, h *harness) wire.JobReply {
	t.Helper()
	return nextJob(t, h, wire.Call{Root: h.root, Agent: "pj-l-01", Role: "worker", Lane: "L-01", CallID: "n1"})
}

// reviewJob is the integrator's open job (its job_next call, under call id id).
func reviewJob(t *testing.T, h *harness, id string) wire.JobReply {
	t.Helper()
	return nextJob(t, h, wire.Call{Root: h.root, Agent: "pj-int", Role: "integrator", CallID: id})
}

func nextJob(t *testing.T, h *harness, c wire.Call) wire.JobReply {
	t.Helper()
	raw, err := h.a.jobNext(c)
	if err != nil {
		t.Fatal(err)
	}
	var jr wire.JobReply
	if err := json.Unmarshal([]byte(raw), &jr); err != nil {
		t.Fatal(err)
	}
	return jr
}

// questionsPart is the review job's open-questions part for section q (questionsText).
func questionsPart(q string) string {
	return "the accept note; hold one that changes scope, a contract, config or a protected file):\n" + q +
		"\n(end of the open questions)\n"
}

// A report's open questions no longer hold the task: the review job goes out at once, carrying the section verbatim
// (cut like the diff); a section that leaves nothing open adds no part.
func TestOpenQuestionsGoWithTheReviewJob(t *testing.T) {
	t.Parallel()
	const full = "Summary: done.\nSelf-check: ok.\nSkipped: none."
	long := strings.Repeat("a", port.InlineDiff+100)
	for _, c := range []struct {
		name, report, quote string // quote "": no open-questions part
	}{
		{"section absent", full, ""},
		{"none", full + "\nOpen questions: none", ""},
		{"n/a", full + "\nOpen questions: N/A", ""},
		{"dash", full + "\nOpen questions: -", ""},
		{"no", full + "\nOpen questions: no", ""},
		{"none with its period", full + "\nOpen questions: none.", ""},
		{"none new", full + "\nOpen questions: none new.", ""},
		{"none with a parenthesis", full + "\nOpen questions: none (the lead decided X).", ""},
		{"None with a dash", full + "\nOpen questions: None — keep as built.", ""},
		{"none then concerns", full + "\nOpen questions: none\nConcerns: none", ""},
		{"real text", full + "\nOpen questions: should the gate run the full check?", "should the gate run the full check?"},
		{"bullets verbatim", full + "\nOpen questions:\n- which DB?\n- none", "- which DB?\n- none"},
		{"a colon in the text", full + "\nOpen questions:\nWhich DB: sqlite or pg?", "Which DB: sqlite or pg?"},
		{"ended by the next section", full + "\nOpen questions: may I edit the hook?\nBlocked: may I edit internal/x.go?",
			"may I edit the hook?"},
		{"ended by a markdown heading", full + "\nOpen questions: may I edit the hook?\n## Self-check\nok",
			"may I edit the hook?"},
		{"an inner label stays", full + "\nOpen questions: may I edit the hook?\nTests: none.", "may I edit the hook?\nTests: none."},
		{"an inner markdown heading stays", full + "\nOpen questions: may I edit the hook?\n## Notes\nnothing else",
			"may I edit the hook?\n## Notes\nnothing else"},
		{"long text cut like the diff", full + "\nOpen questions: " + long, long[:port.InlineDiff] +
			"\n(cut at 15360 bytes: the whole section is in the report above)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, f := seedQuestions(t, c.report)
			mt := pickGreenStep(t, h, f, false)
			if mt.state != stAwaiting || mt.reviews != 1 || mt.job != event.StepReview {
				t.Fatalf("T-1 %s, reviews %d, job %q; want the review job out", mt.state, mt.reviews, mt.job)
			}
			if n := countStep(t, h, event.StepNeedsYou); n != 0 {
				t.Errorf("%d needs-you facts; want none", n)
			}
			jr := reviewJob(t, h, "j1")
			if jr.Job != "T-1/1/review" {
				t.Fatalf("job_next: %+v; want T-1/1/review", jr)
			}
			if c.quote == "" {
				if strings.Contains(jr.Text, "The worker's open questions") {
					t.Errorf("the review job has an open-questions part; want none:\n%s", jr.Text)
				}
				return
			}
			if !strings.Contains(jr.Text, "The worker's open questions (decide") ||
				!strings.Contains(jr.Text, questionsPart(c.quote)) {
				t.Errorf("the review job does not carry the open questions %q:\n%s", c.quote, jr.Text)
			}
		})
	}
}

// The integrator holds a question it may not decide: the lead gets the needs-you, and its resume sends the next review
// job carrying the decision (row 40), live and after a replay of the log.
func TestOpenQuestionsHoldReachesTheLead(t *testing.T) {
	t.Parallel()
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "replay"}[replay], func(t *testing.T) {
			t.Parallel()
			const q = "should the hook change the config's schema?"
			h, f := seedQuestions(t, "Summary: done.\nSelf-check: ok.\nSkipped: none.\nOpen questions: "+q)
			if mt := pickGreenStep(t, h, f, false); mt.state != stAwaiting || mt.job != event.StepReview {
				t.Fatalf("precondition: T-1 %s, job %q; want the review job out", mt.state, mt.job)
			}
			if jr := reviewJob(t, h, "j1"); jr.Job != "T-1/1/review" || !strings.Contains(jr.Text, questionsPart(q)) {
				t.Fatalf("precondition: job %s does not carry the question:\n%s", jr.Job, jr.Text)
			}
			if _, err := h.a.verdict(context.Background(), wire.MethodHold, wire.Call{Root: h.root, Agent: "pj-int",
				Role: "integrator", CallID: "h1", Task: "T-1", Job: "T-1/1/review",
				Note: "the question changes the config contract"}); err != nil {
				t.Fatal(err)
			}
			if mt := h.a.p.folded.loop["T-1"]; mt.state != stNeedsYou || mt.needs.Fact().Cause != event.CauseHeld {
				t.Fatalf("after the hold: T-1 %s (%s); want the lead's held needs-you", mt.state, mt.needs.Fact().Cause)
			}
			const decision = "keep the schema; add the field later"
			reply, err := h.a.resume(wire.Call{Root: h.root, Agent: "pj-lead", Role: "lead", CallID: "r1", Task: "T-1",
				Note: decision})
			if err != nil || reply != "RESUMED T-1: reprompt (held)" {
				t.Fatalf("resume: %q, %v; want the reprompt of the hold", reply, err)
			}
			mt := pickGreenStep(t, h, f, replay)
			if mt.state != stAwaiting || mt.reviews != 2 || mt.job != event.StepReview+".2" {
				t.Fatalf("after the resume: T-1 %s, reviews %d, job %q; want review.2 out", mt.state, mt.reviews, mt.job)
			}
			jr := reviewJob(t, h, "j2")
			if jr.Job != "T-1/1/review.2" {
				t.Fatalf("job_next: %+v; want T-1/1/review.2", jr)
			}
			if want := "Lead's decision on your hold: " + decision; !strings.Contains(jr.Text, want) {
				t.Errorf("the review job does not carry %q:\n%s", want, jr.Text)
			}
		})
	}
}

// An older log's hold of the open questions before review (T-604) still folds: the hold ends, the task is green and
// gets its review job carrying the questions (and the lead's decision when the old log has one), live and on replay.
func TestOldPreReviewHoldFolds(t *testing.T) {
	t.Parallel()
	const q = "should it use sqlite?"
	for _, c := range []struct {
		name     string
		resumed  bool
		replay   bool
		decision string
	}{
		{"held, live", false, false, ""},
		{"held, replay", false, true, ""},
		{"resumed, live", true, false, "use sqlite, it is cgo-free"},
		{"resumed, replay", true, true, "use sqlite, it is cgo-free"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, f := seedQuestions(t, "Summary: done.\nSelf-check: ok.\nSkipped: none.\nOpen questions: "+q)
			addRecord(t, h, "T-1", event.StepNeedsYou, event.KindFact, event.FactData{Lane: "L-01", Merge: true, Cause: causeOpen,
				Reason: "open questions: " + q})
			if c.resumed {
				addRecord(t, h, "T-1", event.StepUnblocked, event.KindFact, event.FactData{Lane: "L-01", Merge: true, Cause: causeOpen,
					Reason: wire.ResumeReprompt, Agent: "pj-lead", Call: "r1", Note: c.decision})
			}
			if n := countStep(t, h, event.StepNeedsYou); n != 1 {
				t.Fatalf("precondition: %d needs-you facts in the log; want the old hold", n)
			}
			p := h.a.p
			if c.replay {
				p = reloadProj(t, h)
			}
			if mt := p.folded.loop["T-1"]; mt.state != stGreen || mt.needs.Seq != 0 {
				t.Fatalf("folded: T-1 %s, needs %d; want green with no needs-you", mt.state, mt.needs.Seq)
			}
			mt := pickGreenStep(t, h, f, false)
			if mt.state != stAwaiting || mt.job != event.StepReview {
				t.Fatalf("T-1 %s, job %q; want the review job out", mt.state, mt.job)
			}
			jr := reviewJob(t, h, "j1")
			if jr.Job != "T-1/1/review" || !strings.Contains(jr.Text, questionsPart(q)) {
				t.Errorf("job %s does not carry the question:\n%s", jr.Job, jr.Text)
			}
			if c.decision != "" && !strings.Contains(jr.Text, c.decision) {
				t.Errorf("the review job does not carry the old decision %q:\n%s", c.decision, jr.Text)
			}
		})
	}
}

// T-616, T-649: an Open questions section whose first word is none, n/a, no or - leaves nothing open, whatever
// follows; one that leaves something open comes back verbatim.
func TestOpenQuestionsFirstWord(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		section string
		open    bool
	}{
		{"none", false}, {"None.", false}, {"none new.", false}, {"none (the lead decided X).", false},
		{"None — answered", false}, {"None — keep as built.", false},
		{"n/a", false}, {"-", false}, {"", false}, {"No.", false}, {"no (settled)", false},
		{"Nonetheless, should X?", true}, {"Should X?", true}, {"no idea whether X", true},
	} {
		got := openQuestions("Summary: s\nOpen questions: " + c.section + "\n")
		if (got != "") != c.open {
			t.Errorf("section %q: openQuestions = %q, want open=%v", c.section, got, c.open)
		}
	}
	if got := openQuestions("Open questions: Should X?\n- and Y?\n"); got != "Should X?\n- and Y?" {
		t.Errorf("a question must come back verbatim, got %q", got)
	}
}
