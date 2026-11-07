package checks

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"obor/internal/app/port"
)

// T-521: the gate puts the stored spec (GateInput.Spec, SpecRev) into every lane candidate's merge message, after
// its subject, and writes no spec file into the tree; a lane's own copy stays the task's change. No stored spec: the
// subject alone. One scratch repo carries the rows (the lane's own copy, which leaves the file in the lane, last).
func TestGateSpecInMessage(t *testing.T) {
	t.Parallel()
	const stored = "# T-05 — Stored\n\nfeature.txt\n"
	y := newSx(t, cmd("check", "echo 1 pass"))
	rows := []struct {
		name, spec string
		laneCopy   bool // the lane edits docs/tasks/T-05.md itself
	}{
		{"a stored spec", stored, false},
		{"no stored spec", "", false},
		{"the lane's own copy is its change", stored, true},
	}
	want := func(spec string) string {
		msg := "Merge T-05: Stored"
		if spec != "" {
			msg += "\n\nSpec rev 3:\n\n# T-05 — Stored\n\nfeature.txt"
		}
		return msg + "\n\nSummary: feature\n\nCo-Authored-By: Test <test@example.invalid>" // the trailer stays last
	}
	for i, c := range rows {
		if c.laneCopy {
			y.Write(y.lane+"/docs/tasks/T-05.md", "# T-05 — Rewritten by the worker\n")
		}
		sha, err := y.s.Submit(ctx, "T-05", strconv.Itoa(i+1), "L-01", "feature", "pj-l1")
		if err != nil {
			t.Fatalf("%s: submit: %v", c.name, err)
		}
		in := lane(sha)
		in.Spec, in.SpecRev, in.Title = c.spec, 3, "Stored"
		o := y.gate(in)
		if o.Kind != port.OutcomeGreen {
			t.Fatalf("%s: gate: %s %q; want green\n%s", c.name, o.Kind, o.Line, y.out.String())
		}
		cand := o.Candidate
		names := y.Git(y.Root, "diff", "--name-only", cand.Target, cand.SHA)
		y.contains(names, "feature.txt", c.name+": the candidate's patch")
		if c.laneCopy {
			if o, err := y.test(ctx, cand); err != nil || o.Kind != port.OutcomeGreen { // T-961: the lane test lists them
				t.Fatalf("%s: test: %+v %v", c.name, o, err)
			}
			changed := y.Read(".obor/state/lane-changed-files-L-01")
			y.contains(names, "docs/tasks/T-05.md", c.name+": the patch (the lane's own copy)")
			y.contains(changed, "docs/tasks/T-05.md", c.name+": OBOR_CHANGED_FILES (the lane's own copy)")
			y.eq(y.Git(y.Root, "show", cand.SHA+":docs/tasks/T-05.md"), "# T-05 — Rewritten by the worker",
				c.name+": the lane's copy, not the stored spec")
		} else {
			y.lacks(names, "docs/tasks/T-05.md", c.name+": the patch (the gate writes no spec file)")
		}
		y.eq(y.Git(y.Root, "log", "-1", "--format=%B", cand.SHA), want(c.spec), c.name+": the merge's message")
		if i == len(rows)-1 {
			if o, err := y.s.Publish(ctx, cand); err != nil || o.Kind != port.PublishMerged {
				t.Fatalf("publish: %+v %v", o, err)
			}
			y.eq(y.Git(y.Root, "log", "-1", "--format=%B", "main"), want(c.spec), "main's merge message")
		}
	}
}

// T-521, T-802: a resolved gate amends the integrator's merge message: the subject "Merge T-xx: <title>" in place of
// the integrator's own, the spec, the rest of its message, the worker's summary; same parents and tree, the same
// commit on a repeat. No title stored: the summary is the subject.
func TestGateResolvedSpecInMessage(t *testing.T) {
	t.Parallel()
	const spec = "# T-05 — Stored\n\nfeature.txt, shared.txt\n"
	y := newSx(t, cmd("check", "echo 1 pass"))
	input := y.conflicted()
	y.Write(filepath.Join(y.serve, "shared.txt"), "both\n")
	y.Git(y.serve, "add", "shared.txt")
	y.Git(y.serve, "commit", "-qm", "Merge T-05: resolved\n\nkept both sides")
	res, err := y.s.Resolved(ctx, "T-05", input, nil)
	if err != nil {
		t.Fatal(err)
	}
	in := port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputResolved, Input: res, Spec: spec, SpecRev: 2,
		Title: "Stored", Summary: "feature"}
	c := y.green(in)
	if c.SHA == res {
		t.Fatalf("candidate %s is the resolution: its message was not amended", c.SHA)
	}
	parents := func(sha string) string { return y.Git(y.Root, "rev-parse", sha+"^1", sha+"^2", sha+"^{tree}") }
	y.eq(parents(c.SHA), parents(res), "the amended merge's parents and tree")
	y.eq(c.Target, y.Git(y.Root, "rev-parse", res+"^1"), "the candidate's target")
	y.eq(y.Git(y.Root, "log", "-1", "--format=%B", c.SHA),
		"Merge T-05: Stored\n\nSpec rev 2:\n\n# T-05 — Stored\n\nfeature.txt, shared.txt\n\nkept both sides\n\nSummary: feature",
		"the amended merge's message")
	if gitOK(y.Root, "cat-file", "-e", c.SHA+":docs/tasks/T-05.md") {
		t.Error("the resolved candidate has a spec file")
	}
	y.eq(y.Git(y.Root, "show", c.SHA+":shared.txt"), "both", "the resolution kept")
	y.eq(y.green(in).SHA, c.SHA, "a repeated resolved gate makes the same commit")

	in.Spec, in.Title = "", "" // nothing stored: the summary is the subject
	y.eq(y.Git(y.Root, "log", "-1", "--format=%B", y.green(in).SHA), "Merge T-05: feature\n\nkept both sides",
		"the message without a stored spec or title")
}

// T-807: the merge's body is the worker report's whole Summary section (an opening sentence and its bullets), on every
// path — the gate's own merge, a moved target, the integrator's resolved merge — while the no-title subject stays its
// first line alone.
func TestMergeMessageSummaryBody(t *testing.T) {
	t.Parallel()
	const summary = "Cut the subject at 100."
	const body = "Cut the subject at 100.\n" +
		"- internal/app/steps.go: the whole section into GateInput.Body\n" +
		"- internal/checks/scratch.go: the message"
	for _, c := range []struct {
		name, title, moved, resolver, want string
	}{
		{"lane, a stored title", "Stored", "", "", "Merge T-05: Stored\n\nSummary: " + body},
		{"lane, no title", "", "", "", "Merge T-05: Cut the subject at 100.\n\nSummary: " + body},
		{"moved", "", "Merged onto the moved main at abc", "",
			"Merge T-05: Cut the subject at 100.\n\nMerged onto the moved main at abc\n\nSummary: " + body},
		{"resolved", "Stored", "", "kept both sides", "Merge T-05: Stored\n\nkept both sides\n\nSummary: " + body},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			in := port.GateInput{Task: "T-05", Title: c.title, Summary: summary, Body: body}
			if got := mergeMessage(in, c.moved, c.resolver); got != c.want {
				t.Errorf("message\n%s\nwant\n%s", got, c.want)
			}
		})
	}
	// No Body (an old record): the summary's first line is the body, as before.
	if got, want := mergeMessage(port.GateInput{Task: "T-05", Title: "Stored", Summary: summary}, "", ""),
		"Merge T-05: Stored\n\nSummary: "+summary; got != want {
		t.Errorf("without a Body: message\n%s\nwant\n%s", got, want)
	}
}

// T-802: a resolved merge that keeps git's own message ("Merge commit '<sha>' into HEAD", "# Conflicts:" lines) comes
// out with the gate's subject and neither of them, and the readers of "Merge T-xx:" subjects find it.
func TestGateResolvedGitDefaultMessage(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 1 pass"))
	input := y.conflicted()
	y.Write(filepath.Join(y.serve, "shared.txt"), "both\n")
	y.Git(y.serve, "add", "shared.txt")
	y.Git(y.serve, "commit", "-q", "--no-edit")
	res, err := y.s.Resolved(ctx, "T-05", input, nil)
	if err != nil {
		t.Fatal(err)
	}
	orig := y.Git(y.Root, "log", "-1", "--format=%B", res)
	if !strings.HasPrefix(orig, "Merge ") || strings.HasPrefix(orig, "Merge T-05:") || !strings.Contains(orig, "# Conflicts:") {
		t.Fatalf("precondition: the resolution's message is not git's default with conflict lines:\n%s", orig)
	}
	c := y.green(port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputResolved, Input: res,
		Spec: "# T-05 — Stored\n\nx\n", SpecRev: 1, Title: "Stored", Summary: "feature"})
	got := y.Git(y.Root, "log", "-1", "--format=%B", c.SHA)
	y.eq(got, "Merge T-05: Stored\n\nSpec rev 1:\n\n# T-05 — Stored\n\nx\n\nSummary: feature", "the amended message")
	if strings.Contains(got, "Merge commit") || strings.Contains(got, "Conflicts") || strings.Contains(got, "\n#\t") {
		t.Errorf("message keeps git's default text:\n%s", got)
	}
	y.eq(y.Git(y.Root, "log", "-1", "--format=%s", c.SHA), "Merge T-05: Stored", "the subject")
	y.eq(y.Git(y.Root, "log", "--format=%h", "--grep=^Merge T-", "--max-count=1", c.SHA), y.Git(y.Root, "rev-parse", "--short", c.SHA),
		"git log --grep='^Merge T-' finds it")
}
