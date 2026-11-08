//go:build integration

package main

import (
	"testing"

	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/testkit"
)

// specs: Check wants a task id and a stored spec (T-440: the text the app read from the store, "" none; a spec in
// git counts for nothing; T-496: a files section is no longer needed). The target is the workflow's (T-1029: the
// contract table, testkit.WorkflowContract).
func TestSpecs(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Lane("L-01", "task/T-01")
	p.Config()
	const stored = "# T\n\na spec\n"
	p.Write("docs/tasks/T-5.md", stored) // committed, not stored
	p.Git(p.Root, "add", "docs/tasks/T-5.md")
	p.Git(p.Root, "commit", "-q", "-m", "spec in git")

	load := func(edits ...func(map[string]any)) specs {
		p.Config(edits...)
		cfg, err := config.Load(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		return specs{cfg: cfg, env: p.Env()}
	}
	ctx := t.Context()
	s := load()
	for _, c := range []struct {
		name, task, text string
		want             string
	}{
		{"stored spec", "T-1", stored, ""},
		{"stored spec without a files section", "T-2", "# T\n\nno files section\n", ""},
		{"no stored spec", "T-4", "", "no spec for T-4 in obor serve's store (obor spec set T-4 <path>)"},
		{"committed in git, not stored", "T-5", "", "no spec for T-5 in obor serve's store (obor spec set T-5 <path>)"},
		{"not a task id", "X-1", stored, "X-1 is no task id (task_id_pattern T-[0-9]+[a-z]?)"},
	} {
		got, err := s.Check(ctx, c.task, c.text)
		if err != nil || got != c.want {
			t.Errorf("Check %s (%s): %q, %v; want %q", c.task, c.name, got, err, c.want)
		}
	}

}

// T-425, T-1023: the dispatch target is branches.main's sha whatever the main checkout has checked out (another
// branch, a detached HEAD); the merge loop no longer pauses off it (no Checkout).
func TestDispatchTargetOffBranch(t *testing.T) {
	t.Parallel()
	p := servedGitProject(t)
	l, err := mergeLoop(p.Env(), herdr.Panes{}, &serveEnd{stop: func() {}}, &kindCache{})(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	main := p.Git(p.Root, "rev-parse", "main")
	check := func(name string) {
		t.Helper()
		if sha, _, err := l.Workflow.Target(ctx, "T-1"); err != nil || sha != main {
			t.Errorf("%s: Target %q %v; want main's %q", name, sha, err, main)
		}
	}
	check("on main")
	p.Git(p.Root, "switch", "-q", "-c", "side")
	p.Commit(p.Root)
	check("on side")
	p.Git(p.Root, "switch", "-q", "--detach", "side")
	check("detached")
}
