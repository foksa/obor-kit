package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// The review job carries a candidate's diff the gate kept, said to be the whole diff; without one, the git diff line.
func TestReviewTextDiff(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	diff := "diff --git a/f b/f\n+the change\n"
	for _, c := range []struct {
		name string
		diff string
		want []string
		not  []string
	}{
		{"inline", diff, []string{"stat-line", "its whole diff", "+the change", "no need to run it"},
			[]string{"read it with git diff"}},
		{"too big", "", []string{"stat-line", "read it with git diff b c1."}, []string{"whole diff", "+the change"}},
	} {
		mt := *p.folded.loop["T-1"]
		mt.cand = port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1", Stat: "stat-line", Diff: c.diff}
		text, err := r.a.reviewText(p, mt, r.kit)
		if err != nil {
			t.Fatal(err)
		}
		text, _, _ = strings.Cut(text, "Your role text (") // the integrator's role texts are not the diff part
		for _, w := range c.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s: the review job has no %q:\n%s", c.name, w, text)
			}
		}
		for _, w := range c.not {
			if strings.Contains(text, w) {
				t.Errorf("%s: the review job has %q:\n%s", c.name, w, text)
			}
		}
	}
}

// The replay mismatch reaches the resolve job with the reason, the checked resolution and the file.
func TestResolveJobNamesReplayMismatch(t *testing.T) {
	m := &port.Mismatch{Checked: "0123456789abcdef", Tree: "t", Files: []string{"b/b.go"}, Dir: "/scratch"}
	o := port.GateOutcome{Kind: port.OutcomeConflict, Files: m.Files, Dir: "/scratch", Mismatch: m,
		Line: "CONFLICT in /scratch: b/b.go (the replayed resolution differs from the checked one)"}
	g := gateResult("T-05", o)
	if g.Outcome != outConflict || g.Mismatch != m {
		t.Fatalf("gateResult: %+v; want a conflict carrying the mismatch", g)
	}
	mt := mtask{task: "T-05", lane: "L-01", attempt: 1, state: stConflict}
	mt.line, mt.mismatch = g.Line, g.Mismatch
	text, err := resolveText(&proj{root: roleRoot(t)}, mt, roleKit(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"lost edits of the checked one 0123456", "b/b.go", "git diff 0123456 -- b/b.go",
		"the target's new changes stay"} {
		if !strings.Contains(text, want) {
			t.Errorf("resolve job lacks %q:\n%s", want, text)
		}
	}
}

// Real unmerged paths keep today's conflict line and job.
func TestResolveJobForUnmergedPathsHasNoMismatchWording(t *testing.T) {
	o := port.GateOutcome{Kind: port.OutcomeConflict, Files: []string{"a/a.go"}, Dir: "/scratch"}
	g := gateResult("T-05", o)
	if g.Mismatch != nil || g.Line != "CONFLICT in /scratch: a/a.go" {
		t.Fatalf("gateResult: %+v", g)
	}
	mt := mtask{task: "T-05", lane: "L-01", attempt: 1, state: stConflict, line: g.Line}
	text, err := resolveText(&proj{root: roleRoot(t)}, mt, roleKit(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "CONFLICT in /scratch: a/a.go") || strings.Contains(text, "lost edits") {
		t.Errorf("resolve job: %s", text)
	}
}

// resolveFetched folds a conflict gate result (out) and its resolve intent, then fetches the open job as job_next does.
func resolveFetched(t *testing.T, out gateOut) (string, string) {
	t.Helper()
	l := dispatched().submitted(1).
		result(1, event.StepGate, gateIn{Kind: port.InputLane, Lane: "L-01"}, out).
		add(1, event.StepResolve, event.KindIntent, JobData{Patch: "patch"})
	a, p := blockProj(t, l.recs)
	writeRoleFile(t, filepath.Join(p.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj"}}`)
	kit, _ := filepath.Abs("../..")
	a.opt.Loop = func(string) (*Loop, error) { return &Loop{Kit: kit}, nil }
	mt := p.folded.loop[blockTask]
	if mt == nil || mt.state != stResolving || !mt.openJob() {
		t.Fatalf("folded task %+v; want an open resolve job", mt)
	}
	first, _, err := a.fetchText(p, *mt, mt.jobKey())
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := a.fetchText(p, *mt, mt.jobKey())
	if err != nil {
		t.Fatal(err)
	}
	return first, again
}

// The job job_next fetches after the resolve intent folded (stResolving) names the mismatch too.
func TestFetchedResolveJobNamesReplayMismatch(t *testing.T) {
	m := &port.Mismatch{Checked: "0123456789abcdef", Tree: "t", Files: []string{"b/b.go"}, Dir: "/scratch"}
	first, again := resolveFetched(t, gateOut{Outcome: outConflict, Mismatch: m,
		Line: "CONFLICT in /scratch: b/b.go (the replayed resolution differs from the checked one)"})
	for _, want := range []string{"lost edits of the checked one 0123456", "b/b.go", "git diff 0123456 -- b/b.go"} {
		if !strings.Contains(first, want) {
			t.Errorf("fetched resolve job lacks %q:\n%s", want, first)
		}
	}
	if again != first {
		t.Errorf("refetch differs:\n%s\n--\n%s", again, first)
	}
}

// A real unmerged conflict through the same path keeps today's wording.
func TestFetchedResolveJobForUnmergedPathsHasNoMismatchWording(t *testing.T) {
	first, again := resolveFetched(t, gateOut{Outcome: outConflict, Line: "CONFLICT in /scratch: a/a.go"})
	if !strings.Contains(first, "CONFLICT in /scratch: a/a.go") || strings.Contains(first, "lost edits") {
		t.Errorf("fetched resolve job: %s", first)
	}
	if again != first {
		t.Errorf("refetch differs:\n%s\n--\n%s", again, first)
	}
}

// reviewJob is the integrator's open job (its job_next call, under call id id).
func reviewJob(t *testing.T, h *harness, id string) wire.JobReply {
	t.Helper()
	return nextJob(t, h, wire.JobNextReq{Envelope: wire.Envelope{Root: h.root, Agent: "pj-int", Role: "integrator",
		CallID: id}})
}

// questionsPart is the review job's open-questions part for section q (questionsText).
func questionsPart(q string) string {
	return "the accept note; hold one that changes scope, a contract, config or a protected file):\n" + q +
		"\n(end of the open questions)\n"
}

// A report's open questions do not hold the task: the review job goes out at once, carrying the section verbatim
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
		{"ended by the next section", full + "\nOpen questions: may I edit the hook?\nConcerns: none",
			"may I edit the hook?"},
		{"ended by Blocked", full + "\nOpen questions: may I edit the hook?\nBlocked: may I edit internal/x.go?",
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
			if _, err := h.a.verdict(context.Background(), wire.VerdictReq{Envelope: wire.Envelope{Root: h.root,
				Agent: "pj-int", Role: "integrator", CallID: "h1"}, Verb: wire.MethodHold, Task: "T-1",
				Job: "T-1/1/review", Note: "the question changes the config contract"}); err != nil {
				t.Fatal(err)
			}
			if mt := h.a.p.folded.loop["T-1"]; !mt.onHold() || mt.needs.fact.Fact().Cause != event.CauseHeld {
				t.Fatalf("after the hold: T-1 %s (%s); want the lead's held needs-you", mt.state, mt.needs.fact.Fact().Cause)
			}
			const decision = "keep the schema; add the field later"
			reply, err := h.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Agent: "pj-lead",
				Role: "lead", CallID: "r1"}, Task: "T-1", Text: decision})
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

// An older log's hold of the open questions before review still folds: the hold ends, the task is green and
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
			if mt := p.folded.loop["T-1"]; mt.state != stGreen || mt.needs.fact.Seq != 0 {
				t.Fatalf("folded: T-1 %s, needs %d; want green with no needs-you", mt.state, mt.needs.fact.Seq)
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

// An Open questions section whose first word is none, n/a, no or - leaves nothing open, whatever
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

// A report's section ends only at a heading that names one of the report's own sections, so a real
// Summary's own labels stay in it: a short Name: label of another kind does not end it, and the merge body keeps the
// whole Summary. Both reports are the ones their workers submitted (obor serve's event log).
func TestReportSectionKeepsARealSummary(t *testing.T) {
	for _, c := range []struct {
		name, report string
		bytes        int
		labels       []string // its inner labels: no section, so they stay
		want         string
	}{
		{"T-789", t789Report, 2245,
			[]string{"Deleted (sizes only):", "Added:", "Tests:", "Docs:", "Evidence:"}, t789Summary},
		{"T-802", t802Report, 1892, []string{"Tests:"}, t802Summary},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := summarySection(c.report) // the merge's body (jobstart.go)
			if got != c.want {
				t.Errorf("the Summary section\n%s\nwant the whole section\n%s", got, c.want)
			}
			if n := len(got); n != c.bytes {
				t.Errorf("the Summary section is %d bytes, want all %d", n, c.bytes)
			}
			for _, label := range c.labels {
				if !strings.Contains(got, label) {
					t.Errorf("the Summary section lost its inner label %q", label)
				}
			}
		})
	}
}

// A section ends at any heading form of one of the report's own sections (Name:, ## Name, **Name**, **Name:**,
// any case), and not at a label or a nested heading inside it.
func TestReportSectionEndsOnlyAtAReportSection(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, text, want string
	}{
		{"a Name: label", "Summary: kept.\nSelf-check: ok.", "kept."},
		{"a markdown heading", "Summary: kept.\n## Self-check\nok", "kept."},
		{"a bold heading", "Summary: kept.\n**Self-check**\nok", "kept."},
		{"a bold heading with its colon", "Summary: kept.\n**Self-check:**\nok", "kept."},
		{"any case", "Summary: kept.\nSELF-CHECK: ok", "kept."},
		{"Skipped", "Summary: kept.\nSkipped: none.", "kept."},
		{"Open questions", "Summary: kept.\nOpen questions: none.", "kept."},
		{"Blocked", "Summary: kept.\nBlocked: may I edit x.go?", "kept."},
		{"Concerns", "Summary: kept.\nConcerns: none", "kept."},
		{"the template's Concerns heading", "Summary: kept.\nConcerns (optional): none beyond the open questions.",
			"kept."},
		{"the report's end", "Summary: kept.\n- one", "kept.\n- one"},
		{"an inner label", "Summary: kept.\nTests: two.\nMore.", "kept.\nTests: two.\nMore."},
		{"an inner markdown heading", "Summary: kept.\n## Notes\nMore.", "kept.\n## Notes\nMore."},
		{"an inner bold label", "Summary: kept.\n**Deleted (sizes only):** four files.",
			"kept.\n**Deleted (sizes only):** four files."},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := reportSection(c.text, "Summary"); got != c.want {
				t.Errorf("reportSection = %q, want %q", got, c.want)
			}
		})
	}
}

// A report with no Summary heading is "" (summarySection keeps its first-paragraph fallback), an Open questions
// section with inner labels comes back whole, and "none" still leaves nothing open (questions_test.go's cases).
func TestReportSectionMissingSummaryAndInnerLabels(t *testing.T) {
	t.Parallel()
	if got := reportSection("Self-check: ok.\nSkipped: none.", "Summary"); got != "" {
		t.Errorf("no Summary heading: %q, want \"\"", got)
	}
	if got, want := summarySection("Did it in two files.\n\nSelf-check: ok."), "Did it in two files."; got != want {
		t.Errorf("no Summary heading: summarySection %q, want the first paragraph %q", got, want)
	}
	if got, want := openQuestions("Summary: s.\nOpen questions: may I edit the hook?\nTests: none.\n- and Y?"),
		"may I edit the hook?\nTests: none.\n- and Y?"; got != want {
		t.Errorf("an Open questions section with inner labels: %q, want all of it %q", got, want)
	}
	if got, want := openQuestions("Summary: s.\nOpen questions: may I edit the hook?\nConcerns: none"), "may I edit the hook?"; got != want {
		t.Errorf("Concerns after the open questions: %q, want just the question %q", got, want)
	}
	if got := openQuestions("Summary: s.\nOpen questions: none\nConcerns: none"); got != "" {
		t.Errorf("none with a Concerns line below: %q, want nothing open", got)
	}
}

// t789Report is a long report as its worker submitted it, its Summary full of Name: labels.
const t789Report = "Summary: deleted the one-time `obor setup migrate` code and made a pre-v0.23 config plainly invalid.\n" +
	"Deleted (sizes only): internal/setup/migrate.go (238 lines), internal/config/migrate.go (669), and their tests\n" +
	"internal/setup/migrate_test.go (211) and internal/config/migrate_test.go (274) — 1392 lines in four files. With them:\n" +
	"config.Migrate, config.Migration, config.AgentRename, config.HasAgents, config.OldShape, config.ErrOldShape,\n" +
	"config.Data.Agents and the `.agents` FAIL in internal/config/validate.go (validator.removed); `setup migrate` in\n" +
	"cmd/obor/main.go's command table, in setup.Usage() and in Setup.Run.\n" +
	"Added: config.OldShapeHint (\"this config predates v0.23: migrate it with obor v0.24 (`obor setup migrate`), then\n" +
	"upgrade\") and oldShapeKey; config.readWith now refuses an unknown `lane_types` or `agents` key in config.json or\n" +
	"local.json with that sentence, for every reader (the shape's other markers, `lanes` as an object and\n" +
	"`roles.<role>` objects, are ordinary type errors).\n" +
	"Tests: internal/config/load_test.go TestOldShapeRefused (both files, both keys, plus the current shape loading);\n" +
	"validate_test.go: TestShapeErrorsFailTheLoad rows for `agents` and `lane_types` (the old `.agents` validate case is\n" +
	"gone with the key), cmd/obor/config_test.go TestConfigValidateOldShape (`obor config validate` exits 1 with the exact\n" +
	"sentence), cmd/obor/main_test.go: `has setup migrate` → 1. Fixtures on the current shape: internal/setup/oborinit_test.go\n" +
	"(no config.OldShape call; asserts no agents key) and internal/config/config_test.go TestAgentLookupPrecedence (its\n" +
	"`agents` entry dropped).\n" +
	"Docs: docs/design.md (protected file, named by the spec) and skills/lead/SKILL.md start-of-session step 3 now say the\n" +
	"old shape is refused with that message instead of \"run obor setup migrate\".\n" +
	"Evidence: the pre-change build (dist/obor, rev 5a55d44b) prints \"lane_types is the old config shape (…): run obor\n" +
	"setup migrate: old config shape\" on the fixture; a build of this tree prints \"unknown field \\\"lane_types\\\": this config\n" +
	"predates v0.23: migrate it with obor v0.24 (`obor setup migrate`), then upgrade\" (exit 1) — so the new tests fail on\n" +
	"the old code. `obor run check_affected`: 18 packages, 18 pass, 0 fail, on the tree submitted.\n" +
	"\n" +
	"Self-check: deviations and files the spec did not name.\n" +
	"- The spec names internal/store/*_test.go for old-shape fixtures; no store test builds a config fixture (its\n" +
	"  \"migrate\" tests are the DB schema ones, T-665/T-721, which stay). The old-shape config fixtures that do exist are in\n" +
	"  cmd/obor/send_test.go and internal/project/projects_test.go; they test \"a project whose config this binary cannot\n" +
	"  load\" (T-767), not migration, and still hold because an old-shape key is refused by every reader — left unchanged,\n" +
	"  they pass.\n" +
	"- \"the removed(…) keys in validate.go\": only validator.removed() (the `.agents` refusal) existed there; the\n" +
	"  removed(...) WARN calls in warnings() are the older v0.12/v0.17 removals and are untouched.\n" +
	"- I kept a two-key recognition (oldShapeKey) rather than OldShape(doc): without it the prescribed sentence cannot be\n" +
	"  produced, since the key is now just an unknown one. It is a key test, not a document walk.\n" +
	"- A config whose only old-shape markers are `lanes` as an object or `roles.<role>` objects gets the type error, not\n" +
	"  the hint; every real pre-v0.23 config carries lane_types, so it gets the hint.\n" +
	"- Files the spec did not mention, changed for the wiring/tests: cmd/obor/main_test.go (the `has` row),\n" +
	"  cmd/obor/config_test.go (the new end-to-end test), internal/config/config_test.go (fixture).\n" +
	"- How this report was submitted: this lane's opencode MCP client cannot identify itself (see Concerns), so\n" +
	"  tools.obor.task_done is refused before it records anything; I fetched T-789 and submit here through a build of this\n" +
	"  tree, `go build -o $TMPDIR/obor-t789 ./cmd/obor` fed the MCP handshake on stdin — the same call and wire request\n" +
	"  the tool makes.\n" +
	"- tests/upgrade is untouched: it only checks the v0.23 guide text and runs no migrate code.\n" +
	"\n" +
	"Skipped: none.\n" +
	"\n" +
	"Open questions: none.\n" +
	"\n" +
	"Concerns: every opencode worker in this project is blocked, not just this lane. ~/.config/opencode/opencode.json\n" +
	"registers the obor MCP server with OBOR_NO_SELF_HOST=1 pointing at ~/.obor/kit, which has no dist build, so it execs\n" +
	"the released v0.24.2 binary; that binary predates T-786 and sends OBOR_AGENT, which nothing exports any more, so\n" +
	"job_next/task_done arrive with no agent name (\"not your job … not -\"). Recommendation for the owner/lead (not this\n" +
	"task): point the opencode mcp entry at the kit's bin/obor without OBOR_NO_SELF_HOST, or have serve accept the lane's\n" +
	"derived name when a client sends none — the cross-version assumption (\"a release with T-786 ships\") is what is\n" +
	"fragile, and it silently blocks a whole agent kind."

// t789Summary is the same report's whole Summary section: its inner labels kept, as the merge's body has it.
const t789Summary = "deleted the one-time `obor setup migrate` code and made a pre-v0.23 config plainly invalid.\n" +
	"Deleted (sizes only): internal/setup/migrate.go (238 lines), internal/config/migrate.go (669), and their tests\n" +
	"internal/setup/migrate_test.go (211) and internal/config/migrate_test.go (274) — 1392 lines in four files. With them:\n" +
	"config.Migrate, config.Migration, config.AgentRename, config.HasAgents, config.OldShape, config.ErrOldShape,\n" +
	"config.Data.Agents and the `.agents` FAIL in internal/config/validate.go (validator.removed); `setup migrate` in\n" +
	"cmd/obor/main.go's command table, in setup.Usage() and in Setup.Run.\n" +
	"Added: config.OldShapeHint (\"this config predates v0.23: migrate it with obor v0.24 (`obor setup migrate`), then\n" +
	"upgrade\") and oldShapeKey; config.readWith now refuses an unknown `lane_types` or `agents` key in config.json or\n" +
	"local.json with that sentence, for every reader (the shape's other markers, `lanes` as an object and\n" +
	"`roles.<role>` objects, are ordinary type errors).\n" +
	"Tests: internal/config/load_test.go TestOldShapeRefused (both files, both keys, plus the current shape loading);\n" +
	"validate_test.go: TestShapeErrorsFailTheLoad rows for `agents` and `lane_types` (the old `.agents` validate case is\n" +
	"gone with the key), cmd/obor/config_test.go TestConfigValidateOldShape (`obor config validate` exits 1 with the exact\n" +
	"sentence), cmd/obor/main_test.go: `has setup migrate` → 1. Fixtures on the current shape: internal/setup/oborinit_test.go\n" +
	"(no config.OldShape call; asserts no agents key) and internal/config/config_test.go TestAgentLookupPrecedence (its\n" +
	"`agents` entry dropped).\n" +
	"Docs: docs/design.md (protected file, named by the spec) and skills/lead/SKILL.md start-of-session step 3 now say the\n" +
	"old shape is refused with that message instead of \"run obor setup migrate\".\n" +
	"Evidence: the pre-change build (dist/obor, rev 5a55d44b) prints \"lane_types is the old config shape (…): run obor\n" +
	"setup migrate: old config shape\" on the fixture; a build of this tree prints \"unknown field \\\"lane_types\\\": this config\n" +
	"predates v0.23: migrate it with obor v0.24 (`obor setup migrate`), then upgrade\" (exit 1) — so the new tests fail on\n" +
	"the old code. `obor run check_affected`: 18 packages, 18 pass, 0 fail, on the tree submitted."

// t802Report is another long report as its worker submitted it.
const t802Report = "Summary: Every merge's subject is now \"Merge T-xx: <spec title>\" on the normal, moved and resolved paths. With no stored title it falls back to the summary's first line.\n" +
	"- internal/checks/scratch.go: new `mergeMessage(in, moved, resolver)`, used by `Gate` (normal and moved) and `specResolved`. Message order: subject, then `specBody`, then the \"Merged onto the moved …\" line, then the resolver's body with git's `#` lines dropped, then \"Summary: <worker summary>\". The Summary line is left out when the summary is already the subject. `specResolved` now always amends, with the same parents, tree and author and the committer date unchanged. Before, it amended only when a spec was stored.\n" +
	"- internal/app/port/effects.go: `GateInput.Title` added (doc comment updated).\n" +
	"- internal/app/steps.go: the gate sets `Title = spec.Title(row.Text)` and `Summary = summaryOf(report)` for all three kinds. `subjectOf` is still used by the lane commit.\n" +
	"- internal/app/scratch_test.go: the fake scratch builds the subject from Title, falling back to Summary.\n" +
	"- Tests:\n" +
	"  - internal/checks/specgate_test.go: `TestGateSpecInMessage` now checks the title subject and the Summary line, with and without a stored spec. `TestGateResolvedSpecInMessage` is rewritten for the title subject, spec, resolver body and Summary, a repeat giving the same commit, and the no-title fallback to the summary. The new `TestGateResolvedGitDefaultMessage` uses a real conflict resolved with `commit --no-edit`; its precondition asserts \"Merge commit … into HEAD\" plus \"# Conflicts:\". The result has the title subject, neither of those, and `git log --grep='^Merge T-'` finds it.\n" +
	"  - internal/checks/scratch_test.go: `TestRV39MovedSubject` uses Title and checks the \"Summary: worker words\" body line.\n" +
	"  - internal/checks/lanetable_test.go: `TestMergeEvidenceResolvedMerge`.\n" +
	"  - internal/app/review_test.go: `TestMergeSubjectResolved`.\n" +
	"\n" +
	"Self-check:\n" +
	"- Files the spec did not mention: internal/app/port/effects.go, internal/app/steps.go and internal/app/scratch_test.go, all needed for the wiring.\n" +
	"- Existing tests changed: three internal/checks/scratch_test.go tests (the resolved-gate cut-step, `TestScratchGateResolvedAfterRed` and the resolved test near line 248) asserted candidate == resolution sha. They now assert the same tree, parents and first parent, with the amended sha in the pins and in serve's worktree.\n" +
	"- Readers: `mergeRE`, `reviewverify.go` and `lanetable.go` already match on the \"Merge T-xx:\" prefix, so they needed no code change. The reader tests cover the lane table, `mergeRE` and the git grep. `reviewverify.go` has no reader test: `verifyText` needs a written review file, and my attempt skipped, so I removed it instead of keeping a vacuous test. It uses the same `HasPrefix(\"Merge T-xx:\")` check.\n" +
	"- Test results:\n" +
	"  - Green on the final tree: `go test ./internal/checks/` and `./internal/app/` (earlier run, before the last test-only edit to review_test.go), `./internal/spec/` (cached), plus a re-run of `TestMergeSubjectResolved` alone. `gofmt` is clean.\n" +
	"  - New tests against the old code: I did not revert and run them. The old `specResolved` kept the integrator's subject and skipped the amend with no spec, so the new assertions on the subject, the dropped `#` lines and the Summary line would fail on it.\n" +
	"- `obor run check_affected` did NOT complete. It sat at \"waiting: machine load > 20\" and my 580s timeout killed it (make: Terminated: 15) before any test ran. It is not green evidence. I did not re-run it; the focused package tests above are what I have.\n" +
	"\n" +
	"Skipped: Nothing from the spec. Past merges are not rewritten (out of scope).\n" +
	"\n" +
	"Open questions: none."

// t802Summary is that report's whole Summary section, its inner "Tests:" kept.
const t802Summary = "Every merge's subject is now \"Merge T-xx: <spec title>\" on the normal, moved and resolved paths. With no stored title it falls back to the summary's first line.\n" +
	"- internal/checks/scratch.go: new `mergeMessage(in, moved, resolver)`, used by `Gate` (normal and moved) and `specResolved`. Message order: subject, then `specBody`, then the \"Merged onto the moved …\" line, then the resolver's body with git's `#` lines dropped, then \"Summary: <worker summary>\". The Summary line is left out when the summary is already the subject. `specResolved` now always amends, with the same parents, tree and author and the committer date unchanged. Before, it amended only when a spec was stored.\n" +
	"- internal/app/port/effects.go: `GateInput.Title` added (doc comment updated).\n" +
	"- internal/app/steps.go: the gate sets `Title = spec.Title(row.Text)` and `Summary = summaryOf(report)` for all three kinds. `subjectOf` is still used by the lane commit.\n" +
	"- internal/app/scratch_test.go: the fake scratch builds the subject from Title, falling back to Summary.\n" +
	"- Tests:\n" +
	"  - internal/checks/specgate_test.go: `TestGateSpecInMessage` now checks the title subject and the Summary line, with and without a stored spec. `TestGateResolvedSpecInMessage` is rewritten for the title subject, spec, resolver body and Summary, a repeat giving the same commit, and the no-title fallback to the summary. The new `TestGateResolvedGitDefaultMessage` uses a real conflict resolved with `commit --no-edit`; its precondition asserts \"Merge commit … into HEAD\" plus \"# Conflicts:\". The result has the title subject, neither of those, and `git log --grep='^Merge T-'` finds it.\n" +
	"  - internal/checks/scratch_test.go: `TestRV39MovedSubject` uses Title and checks the \"Summary: worker words\" body line.\n" +
	"  - internal/checks/lanetable_test.go: `TestMergeEvidenceResolvedMerge`.\n" +
	"  - internal/app/review_test.go: `TestMergeSubjectResolved`."

// The one start sequence (startJob). An integrator that does not run is started with its
// job's wake as the first prompt, the job open from its own intent, written before the start: a fetch during the start gets the job and
// records delivered once, the job's step is sent (start prompt), nothing typed. The same for review and resolve, a
// numbered job after job.lost and the owner's reprompt; an integrator that runs (idle, no other job out) is started
// fresh as well.
func TestStartJobIntegrator(t *testing.T) {
	t.Parallel()
	gate := func(outcome string) func(r *concRig) {
		return func(r *concRig) {
			r.add("pj", "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
			r.add("pj", "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outcome, Candidate: port.Candidate{Task: "T-1", SHA: "c",
				Target: "b", Patch: "p"}})
		}
	}
	sent := func(step string) func(r *concRig) {
		return func(r *concRig) {
			r.add("pj", "T-1", step, event.KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/" + step, Patch: "p"})
			r.add("pj", "T-1", step, event.KindResult, "sent")
		}
	}
	lost := func(step string) func(r *concRig) {
		return func(r *concRig) {
			r.add("pj", "T-1", event.StepLost, event.KindFact, event.FactData{Merge: true, Job: "T-1/1/" + step})
		}
	}
	reprompt := func(r *concRig) {
		r.add("pj", "T-1", event.StepNeedsYou, event.KindFact, event.FactData{Merge: true, Cause: causeUndelivered,
			Reason: "job not delivered"})
		r.add("pj", "T-1", event.StepUnblocked, event.KindFact, event.FactData{Merge: true, Reason: wire.ResumeReprompt})
	}
	for _, row := range []struct {
		name    string
		setup   []func(r *concRig)
		running bool   // the integrator's pane stays in the snapshot
		job     string // the job the step sends
		start   string // the start that runs ("": none)
		typed   int    // Typer calls
	}{
		{name: "review: fresh start", setup: []func(*concRig){gate(outGreen)}, job: "review", start: "start.review"},
		{name: "resolve: fresh start", setup: []func(*concRig){gate(outConflict)}, job: "resolve",
			start: "start.resolve"},
		{name: "review.2 after job.lost", setup: []func(*concRig){gate(outGreen), sent("review"), lost("review")},
			job: "review.2", start: "start.review.2"},
		{name: "resolve.2 after job.lost", setup: []func(*concRig){gate(outConflict), sent("resolve"), lost("resolve")},
			job: "resolve.2", start: "start.resolve.2"},
		{name: "owner reprompt starts an absent integrator",
			setup: []func(*concRig){gate(outGreen), sent("review"), reprompt}, job: "review.2", start: "start.review.2"},
		{name: "running integrator: a fresh start too (T-602)", setup: []func(*concRig){gate(outGreen)},
			running: true, job: "review", start: "start.review"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			p := r.proj("pj")
			for _, s := range row.setup {
				s(r)
			}
			if !row.running {
				delete(r.a.obs, "w1:p9")
			}
			job := "T-1/1/" + row.job
			f := &startJobFakes{concFakes: r.fakes["pj"]}
			var early wire.JobReply
			f.start = func(port.Launch) {
				raw, err := r.a.jobNext(wire.JobNextReq{Envelope: wire.Envelope{Root: p.root, Agent: p.integrator,
					Role: "integrator", CallID: "early"}})
				if err != nil {
					t.Fatal(err)
				}
				_ = json.Unmarshal([]byte(raw), &early)
			}
			r.a.mu.Lock()
			step := r.a.next(p, p.folded.loop["T-1"])
			r.a.mu.Unlock()
			if step == nil {
				t.Fatal("no step")
			}
			if !step(context.Background(), &Loop{Agents: f, Typer: f}) {
				t.Fatal("the job step failed")
			}

			if row.start == "" {
				same(t, len(f.launches), 0, "starts")
			} else {
				if len(f.launches) != 1 || f.launches[0].Prompt != wakeLine(job) || !f.launches[0].Fresh {
					t.Fatalf("launches %+v, want one fresh with the first prompt %q", f.launches, wakeLine(job))
				}
				same(t, done(p, event.Key{Task: "T-1", Attempt: 1, Step: row.start}), true, row.start+" done")
				same(t, early.State+" "+early.Job, "job "+job, "job_next during the start")
			}
			same(t, len(f.typed), row.typed, "Typer calls")
			want := `"sent (start prompt)"`
			if row.typed > 0 {
				want = `"sent"`
			}
			same(t, resultOf(p, event.Key{Task: "T-1", Attempt: 1, Step: row.job}), want, row.job+" result")
			if row.start != "" {
				same(t, deliveredOf(r, job), 1, "delivered facts of "+job)
				r.a.mu.Lock()
				mt := *p.folded.loop["T-1"]
				r.a.mu.Unlock()
				same(t, mt.job+" "+mt.state, row.job+" "+mt.jobState(true), "the job out")
				same(t, mt.delivered, true, "the job delivered (fetched during its start)")
				// a later fetch is a repeat: the same job, nothing recorded
				raw, err := r.a.jobNext(wire.JobNextReq{Envelope: wire.Envelope{Root: p.root, Agent: p.integrator,
					Role: "integrator", CallID: "later"}})
				var again wire.JobReply
				_ = json.Unmarshal([]byte(raw), &again)
				if err != nil || again.Job != job || deliveredOf(r, job) != 1 {
					t.Errorf("a repeat fetch: %s %v, delivered facts %d", raw, err, deliveredOf(r, job))
				}
			}
		})
	}
}

// A start alone hands out no job (no lookup beside the open job); the job's intent, which its step
// writes before the start, does, with the job's patch.
func TestStartJobOtherPatch(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	r.add("pj", "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	r.add("pj", "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{Task: "T-1", SHA: "c",
		Target: "b", Patch: "p"}})
	r.add("pj", "T-1", "start.review", event.KindIntent, startData{Agent: "pj-int"})
	raw, err := r.a.jobNext(wire.JobNextReq{Envelope: wire.Envelope{Root: p.root, Agent: p.integrator,
		Role: "integrator", CallID: "n1"}})
	if err != nil || raw != `{"state":"none"}` {
		t.Fatalf("job_next with a start and no job intent: %s %v, want none", raw, err)
	}
	r.add("pj", "T-1", event.StepReview, event.KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/review", Patch: "p"})
	raw, err = r.a.jobNext(wire.JobNextReq{Envelope: wire.Envelope{Root: p.root, Agent: p.integrator,
		Role: "integrator", CallID: "n3"}})
	var jr wire.JobReply
	_ = json.Unmarshal([]byte(raw), &jr)
	if err != nil || jr.Job != "T-1/1/review" {
		t.Errorf("job_next with the job's intent: %s %v, want T-1/1/review", raw, err)
	}
	r.a.mu.Lock()
	patch := p.folded.loop["T-1"].patch
	r.a.mu.Unlock()
	same(t, patch, "p", "the open job's patch")
}

// A fresh integrator's job is fetchable while the start that
// carries its wake runs, and no second wake is typed.
func TestIntegratorJobFetchableDuringStart(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{event.StepReview, event.StepResolve} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			p := r.proj("pj")
			outcome := outGreen
			if kind == event.StepResolve {
				outcome = outConflict
			}
			r.add("pj", "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
			r.add("pj", "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outcome, Candidate: port.Candidate{Task: "T-1", SHA: "c",
				Target: "b", Patch: "p"}})
			delete(r.a.obs, "w1:p9")
			f := &startEffects{concFakes: r.fakes["pj"]}
			var early wire.JobReply
			f.start = func(l port.Launch) {
				raw, err := r.a.jobNext(wire.JobNextReq{Envelope: wire.Envelope{Root: p.root, Agent: p.integrator,
					Role: "integrator", CallID: "early"}})
				if err != nil {
					t.Fatal(err)
				}
				_ = json.Unmarshal([]byte(raw), &early)
				t.Logf("first prompt=%q; job_next during start=%s", l.Prompt, raw)
			}
			fx := &Loop{Agents: f, Typer: f}
			mt := *p.folded.loop["T-1"]
			if !r.a.job(context.Background(), p, fx, mt, kind) {
				t.Fatal("job failed")
			}
			t.Logf("Prompter calls after first prompt=%d", len(f.typed))
			if early.State != "job" {
				t.Errorf("job_next during first prompt=%s, want job", early.State)
			}
			if len(f.typed) != 0 {
				t.Errorf("typed a second wake %q", f.typed)
			}
		})
	}
}

// The control for the early-fetch probe: the worker's fetch during its start is delivered, and the work step is sent
// with nothing typed.
func TestWorkerEarlyFetchDuringStart(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	r.add("pj", "T-2", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-02", Dir: r.a.obs["w1:p2"].pane.Cwd, Agent: "pj-l-02"})
	r.add("pj", "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	f := &startEffects{concFakes: r.fakes["pj"]}
	f.start = func(port.Launch) {
		raw, err := r.a.jobNext(wire.JobNextReq{Envelope: wire.Envelope{Root: p.root, Agent: "pj-l-02", Role: "worker",
			CallID: "early"}, Lane: "L-02"})
		if err != nil {
			t.Fatal(err)
		}
		var jr wire.JobReply
		_ = json.Unmarshal([]byte(raw), &jr)
		if jr.State != "job" || jr.Job != "T-2/1/work" {
			t.Fatalf("early worker fetch=%s", raw)
		}
	}
	fx := &Loop{Agents: f, Typer: f, Kit: r.kit}
	mt := *p.folded.loop["T-2"]
	d := startData{Agent: mt.agent, Pane: "w1:p2"}
	if !r.a.startWorker(context.Background(), p, fx, mt, event.Key{Task: mt.task, Attempt: mt.attempt, Step: event.StepStartWork}, d,
		event.Key{Task: mt.task, Attempt: mt.attempt, Step: event.StepWork}) {
		t.Fatal("start failed")
	}
	if !r.a.sendWork(context.Background(), p, fx, *p.folded.loop[mt.task], event.StepWork) {
		t.Fatal("work failed")
	}
	if len(f.typed) != 0 || p.folded.loop[mt.task].state != stWorking {
		t.Fatalf("typed=%q state=%s", f.typed, p.folded.loop[mt.task].state)
	}
}

// A start runs in its step: the step returns when the launch has, so the
// task, the merge loop pass and its shutdown own the whole launch, two tasks never start the integrator at once, and a
// typed integrator's start goes on to its job's send in the same step.

// heldStart is a project's adapters whose start of agent (every start when agent is "") enters, then returns only once
// release is closed; the integrator, once started, shows in the app's snapshot (its herdr event).
type heldStart struct {
	*concFakes
	a       *App
	root    string
	agent   string
	entered chan string
	release chan struct{}

	mu             sync.Mutex
	inflight, most int
	starts         int
	returned       bool
	typed          []string // agent and job of each prompt
}

func (f *heldStart) Launch(ctx context.Context, l port.Launch) (port.Started, error) {
	if f.agent != "" && l.Agent != f.agent {
		return f.concFakes.Launch(ctx, l)
	}
	f.mu.Lock()
	f.starts++
	f.inflight++
	f.most = max(f.most, f.inflight)
	f.mu.Unlock()
	f.entered <- l.Agent
	<-f.release
	if strings.HasSuffix(l.Agent, "-int") {
		f.a.mu.Lock()
		f.a.obs["w1:p9"] = &seen{pane: port.Pane{ID: "w1:p9", Group: "pj", Cwd: f.root, Label: l.Agent, Agent: "claude",
			Status: "idle"}, since: f.a.opt.Now()}
		f.a.mu.Unlock()
	}
	f.mu.Lock()
	f.inflight--
	f.returned = true
	f.mu.Unlock()
	return port.Started{Line: "STARTED " + l.Agent}, nil
}

func (f *heldStart) Type(_ context.Context, agent, text string, _ bool) (bool, error) {
	job := jobOf(text)
	f.mu.Lock()
	f.typed = append(f.typed, agent+" "+job)
	f.mu.Unlock()
	return true, nil
}

// heldRig is newConcRig with pj's adapters f (its gate holds nothing).
func heldRig(t *testing.T, agent string) (*concRig, *proj, *heldStart) {
	r := newConcRig(t)
	r.fakes["pj"].hold = ""
	p := r.proj("pj")
	f := &heldStart{concFakes: r.fakes["pj"], a: r.a, root: p.root, agent: agent, entered: make(chan string, 4),
		release: make(chan struct{})}
	loop := r.a.opt.Loop
	r.a.opt.Loop = func(root string) (*Loop, error) {
		fx, err := loop(root)
		if err == nil && root == p.root {
			fx.Agents, fx.Typer = f, f
		}
		return fx, err
	}
	return r, p, f
}

// gateDone records task's lane gate with outcome (green, conflict): its integrator job is next.
func gateDone(r *concRig, task, outcome string) {
	r.add("pj", task, event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Input: "s"})
	r.add("pj", task, event.StepGate, event.KindResult, gateOut{Outcome: outcome, Candidate: port.Candidate{Task: task, SHA: "c",
		Target: "b", Patch: "p"}})
}

// The reviewer's TestRV44DeliveryReleasesLiveLaunch, fixed: a worker that fetches its job while its start runs gets
// the job, but the start goes on in its step: no result before the launch's, a drop is refused, and the merge
// loop's shutdown waits for the launch.
func TestDeliveryKeepsTheStartOwned(t *testing.T) {
	t.Parallel()
	r, p, f := heldRig(t, "pj-l-02")
	dispatchT2(r)
	r.add("pj", "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	r.run()
	select {
	case <-f.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("T-2's start never began")
	}

	raw, err := r.a.jobNext(wire.JobNextReq{Envelope: wire.Envelope{Root: p.root, Agent: "pj-l-02", Role: "worker",
		CallID: "fetched"}, Lane: "L-02"})
	var job wire.JobReply
	_ = json.Unmarshal([]byte(raw), &job)
	if err != nil || job.State != "job" || job.Job != "T-2/1/work" {
		t.Fatalf("job_next during the start: %s, %v; want T-2/1/work", raw, err)
	}
	time.Sleep(200 * time.Millisecond) // a delivery shortcut would have ended the start by now
	if n := r.count("pj", "T-2", event.KindResult, event.StepStartWork); n != 0 {
		t.Errorf("the start's result written while its launch runs (%d)", n)
	}
	if a := r.dropWithin("pj", "T-2", "drop"); a.err == nil || !strings.Contains(a.err.Error(), "T-2 has the dispatch running; retry when it settles") {
		t.Errorf("drop while the launch runs: %q, %v; want refused", a.reply, a.err)
	}
	r.cancel()
	select {
	case <-r.done:
		t.Error("the merge loop's shutdown returned while the launch runs")
	case <-time.After(200 * time.Millisecond):
	}
	close(f.release)
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the merge loop did not stop once the launch returned")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.returned || f.starts != 1 {
		t.Errorf("launch returned %v, starts %d; want the one launch ended before the loop", f.returned, f.starts)
	}
}

// The reviewer's TestRV44IntegratorStartOwnership, fixed: two tasks whose jobs need the integrator, which does not
// run: one pass starts it once (the second task's step waits for the first's start); the second job then waits for
// the first job's verdict (nothing typed into the session the first job runs in, no second start).
func TestIntegratorStartsOneAtATime(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, first, second, job string }{
		{"review + review", outGreen, outGreen, "review"},
		{"review + resolve", outGreen, outConflict, "resolve"},
		{"resolve + review", outConflict, outGreen, "review"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r, p, f := heldRig(t, "")
			gateDone(r, "T-1", c.first)
			gateDone(r, "T-5", c.second)
			r.a.mu.Lock()
			delete(r.a.obs, "w1:p9") // no integrator runs
			r.a.mu.Unlock()
			passed := make(chan struct{})
			go func() { r.a.advance(context.Background()); close(passed) }()
			select {
			case agent := <-f.entered:
				if agent != "pj-int" {
					t.Fatalf("started %s; want pj-int", agent)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("no integrator start began")
			}
			select {
			case <-f.entered:
				t.Error("a second integrator start while the first runs")
			case <-passed:
				t.Error("the pass ended while its start runs")
			case <-time.After(200 * time.Millisecond):
			}
			close(f.release)
			select {
			case <-passed:
			case <-time.After(10 * time.Second):
				t.Fatal("the pass did not end")
			}
			r.a.effects.Wait()
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.starts != 1 || f.most != 1 {
				t.Errorf("integrator starts %d (at once: %d); want 1", f.starts, f.most)
			}
			if len(f.typed) != 0 {
				t.Errorf("typed %q; want nothing while T-1's job is out", f.typed)
			}
			if p.log.Open(event.Key{Task: "T-5", Attempt: 1, Step: c.job}) || p.log.Done(event.Key{Task: "T-5", Attempt: 1, Step: c.job}) {
				t.Errorf("T-5's %s recorded while T-1's job is out", c.job)
			}
		})
	}
}

// The subject is the spec's title (read from the target branch); without one the done file's summary,
// list marker stripped, cut on a word.
func TestSubjectOf(t *testing.T) {
	t.Parallel()
	long := "- " + strings.Repeat("word ", 30) // 150 characters
	cases := []struct {
		name, title, done, want string
	}{
		{"spec title", "Cutover follow-ups: clean subjects", "Summary: ignored", "Cutover follow-ups: clean subjects"},
		{"no spec, marker stripped", "", "Summary: - did the thing", "did the thing"},
		{"spec without a title", "", "Summary: * did it", "did it"},
		{"no spec, long summary cut on a word", "", "Summary: " + long, strings.TrimSpace(strings.Repeat("word ", 20))},
		{"no spec, no summary", "", "", "merged by obor serve"},
	}
	for _, c := range cases {
		got := subjectOf(c.title, c.done)
		if got != c.want {
			t.Errorf("%s: subject %q, want %q", c.name, got, c.want)
		}
		if c.title == "" && len(got) > 100 {
			t.Errorf("%s: %d characters, want at most 100", c.name, len(got))
		}
	}
}

// The merge's body is the report's whole Summary section (an opening sentence and its bullets stay whole),
// while summaryOf is still the one-line subject; a report with no Summary heading gives its first paragraph.
func TestSummarySection(t *testing.T) {
	t.Parallel()
	const report = "Summary: Cut the subject at 100.\n" +
		"- internal/app/steps.go: the whole section into GateInput.Body\n" +
		"- internal/app/subject_test.go: these cases\n" +
		"\nSelf-check: ok.\nSkipped: none.\nOpen questions: none."
	want := "Cut the subject at 100.\n" +
		"- internal/app/steps.go: the whole section into GateInput.Body\n" +
		"- internal/app/subject_test.go: these cases"
	if got := summarySection(report); got != want {
		t.Errorf("summarySection = %q, want the whole section %q", got, want)
	}
	if got := summaryOf(report); got != "Cut the subject at 100." {
		t.Errorf("summaryOf = %q, want the one-line subject", got)
	}
	if got, want := summarySection("Summary:\n- one\n- two\nSelf-check: ok."), "- one\n- two"; got != want {
		t.Errorf("a heading alone on its line: %q, want its bullets %q", got, want)
	}
	if got, want := summarySection("Did it in two files.\nAnd a test.\n\nSelf-check: ok."), "Did it in two files.\nAnd a test."; got != want {
		t.Errorf("no Summary heading: %q, want the first paragraph %q", got, want)
	}
}

// The integrator's review and resolve jobs carry its role texts the way the work job carries the worker's.

// roleRoot is a project root like roleFixture's, with the integrator's project doc.
func roleRoot(t *testing.T) string {
	t.Helper()
	p, _ := roleFixture(t, "# Role: worker\n", "")
	writeRoleFile(t, filepath.Join(p.root, "doc/roles/integrator.md"), "# Integrator here\nproject integrator rules\n")
	return p.root
}

// roleKit is a kit with the integrator's role text.
func roleKit(t *testing.T) string {
	t.Helper()
	_, kit := roleFixture(t, "# Role: worker\n", "")
	writeRoleFile(t, filepath.Join(kit, "templates", "agents", "integrator.md"), "# Role: integrator\nkit integrator rules\n")
	return kit
}
