package app

import (
	"strings"
	"testing"
)

// T-808 (RV-103 #4): a report's section ends only at a heading that names one of the report's own sections, so a real
// Summary's own labels stay in it. Before, reportSection ended at any short Name: label, and T-789's merge body kept 91
// of its 2,245 Summary bytes. Both reports are the ones their workers submitted (obor serve's event log).
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

// T-808: a section ends at any heading form of one of the report's own sections (Name:, ## Name, **Name**, **Name:**,
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

// T-808: a report with no Summary heading is "" (summarySection keeps its first-paragraph fallback), an Open questions
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

// t789Report is T-789's report as its worker submitted it.
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

// t789Summary is T-789's whole Summary section: its inner labels kept, as the merge's body has it.
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

// t802Report is T-802's report as its worker submitted it.
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

// t802Summary is T-802's whole Summary section, its inner "Tests:" kept.
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
