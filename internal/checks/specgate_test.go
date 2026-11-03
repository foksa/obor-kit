package checks

import (
	"testing"

	"obor/internal/app/port"
)

// T-521: the gate puts the stored spec (GateInput.Spec, SpecRev) into every lane candidate's merge message, after
// its subject, and writes no spec file into the tree; a lane's own copy stays the task's change. No stored spec: the
// subject alone.
func TestGateSpecInMessage(t *testing.T) {
	t.Parallel()
	const stored = "# T-05 — Stored\n\nfeature.txt\n"
	for _, c := range []struct {
		name, spec string
		laneCopy   bool // the lane edits docs/tasks/T-05.md itself
	}{
		{"stored spec", stored, false},
		{"the lane's own copy is its change", stored, true},
		{"no stored spec", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			y := newSx(t, cmd("check", "echo 1 pass"))
			if c.laneCopy {
				y.Write(y.lane+"/docs/tasks/T-05.md", "# T-05 — Rewritten by the worker\n")
			}
			in := lane(y.submit())
			in.Spec, in.SpecRev = c.spec, 3
			o := y.gate(in)
			if o.Kind != port.OutcomeGreen {
				t.Fatalf("gate: %s %q; want green\n%s", o.Kind, o.Line, y.out.String())
			}
			cand := o.Candidate
			names := y.Git(y.Root, "diff", "--name-only", cand.Target, cand.SHA)
			changed := y.Read(".obor/state/serve-changed-files")
			y.contains(names, "feature.txt", "the candidate's patch")
			if c.laneCopy {
				y.contains(names, "docs/tasks/T-05.md", "the patch (the lane's own copy)")
				y.contains(changed, "docs/tasks/T-05.md", "OBOR_CHANGED_FILES (the lane's own copy)")
				y.eq(y.Git(y.Root, "show", cand.SHA+":docs/tasks/T-05.md"), "# T-05 — Rewritten by the worker",
					"the lane's copy, not the stored spec")
			} else {
				y.lacks(names, "docs/tasks/T-05.md", "the patch (the gate writes no spec file)")
			}
			want := "Merge T-05: feature"
			if c.spec != "" {
				want += "\n\nSpec rev 3:\n\n# T-05 — Stored\n\nfeature.txt"
			}
			want += "\n\nCo-Authored-By: Test <test@example.invalid>" // the trailer stays last
			y.eq(y.Git(y.Root, "log", "-1", "--format=%B", cand.SHA), want, "the merge's message")
			if o, err := y.s.Publish(ctx, cand); err != nil || o.Kind != port.PublishMerged {
				t.Fatalf("publish: %+v %v", o, err)
			}
			y.eq(y.Git(y.Root, "log", "-1", "--format=%B", "main"), want, "main's merge message")
		})
	}
}

// T-521: a resolved gate amends the integrator's merge message the same way (its subject, the spec, then the rest of
// its message): same parents and tree, the same commit on a repeat; no stored spec: gated as committed.
func TestGateResolvedSpecInMessage(t *testing.T) {
	t.Parallel()
	const spec = "# T-05 — Stored\n\nfeature.txt, shared.txt\n"
	y := newSx(t, cmd("check", "echo 1 pass"))
	input := y.conflicted()
	y.resolveServe()
	res, err := y.s.Resolved(ctx, "T-05", input)
	if err != nil {
		t.Fatal(err)
	}
	subject := y.Git(y.Root, "log", "-1", "--format=%s", res)
	if body := y.Git(y.Root, "log", "-1", "--format=%b", res); body != "" {
		t.Fatalf("precondition: the resolution's message has a body %q", body)
	}
	in := port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputResolved, Input: res, Spec: spec, SpecRev: 2}
	c := y.green(in)
	if c.SHA == res {
		t.Fatalf("candidate %s is the resolution: its message was not amended", c.SHA)
	}
	parents := func(sha string) string { return y.Git(y.Root, "rev-parse", sha+"^1", sha+"^2", sha+"^{tree}") }
	y.eq(parents(c.SHA), parents(res), "the amended merge's parents and tree")
	y.eq(c.Target, y.Git(y.Root, "rev-parse", res+"^1"), "the candidate's target")
	y.eq(y.Git(y.Root, "log", "-1", "--format=%B", c.SHA),
		subject+"\n\nSpec rev 2:\n\n# T-05 — Stored\n\nfeature.txt, shared.txt", "the amended merge's message")
	if gitOK(y.Root, "cat-file", "-e", c.SHA+":docs/tasks/T-05.md") {
		t.Error("the resolved candidate has a spec file")
	}
	y.eq(y.Git(y.Root, "show", c.SHA+":shared.txt"), "both", "the resolution kept")
	y.eq(y.green(in).SHA, c.SHA, "a repeated resolved gate makes the same commit")

	in.Spec = "" // none stored: the resolution as committed
	y.eq(y.green(in).SHA, res, "a resolution without a stored spec is not amended")
}
