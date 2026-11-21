//go:build integration

package config_test

import (
	"reflect"
	"testing"

	"obor/internal/config"
	"obor/internal/spec"
	"obor/internal/testkit"
)

// Validate's trunk check, with no probe given, asks git (the other tests give one): refs/heads/<merge.target_branch>
// must resolve in the main checkout, and a directory that is no repository has no trunk to check. NoCompileWarning
// names the default branch, never another.
func TestValidateTrunkRefIsGits(t *testing.T) {
	git := config.ValidateOptions{Overlap: spec.Overlap}
	p := testkit.New(t)
	p.Lane("L-01", "")
	if got := validateWith(t, p, git); len(got) != 0 {
		t.Fatalf("trunk main present: %q", got)
	}
	want := "FAIL .merge.target_branch: refs/heads/trunk does not exist: set merge.target_branch: your trunk is not trunk"
	if got := validateWith(t, p, git, set("merge.target_branch", "trunk")); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("trunk missing: %q, want %q", got, want)
	}
	p.Git(p.Root, "branch", "trunk")
	if got := validateWith(t, p, git, set("merge.target_branch", "trunk")); len(got) != 0 {
		t.Errorf("trunk created: %q", got)
	}
	noCompile := set("merge.compile", nil)
	if got := validateWith(t, p, git, set("merge.target_branch", "main"), noCompile); len(got) != 0 {
		t.Errorf("no compile, target the default branch: %q, want no validate line (obor init warns)", got)
	}
	c, err := config.Load(p.Root) // the config the last validate wrote: target main, no compile
	if err != nil {
		t.Fatal(err)
	}
	want = "merge.compile is empty and merge.target_branch main is the default branch: a moved candidate re-runs " +
		"the full check (no compile)"
	if got := c.NoCompileWarning(""); got != want {
		t.Errorf("obor init's warning: %q, want %q", got, want)
	}
	if got := validateWith(t, p, git, set("merge.target_branch", "trunk"), noCompile); len(got) != 0 {
		t.Errorf("no compile, target a branch other than the default: %q, want none", got)
	}
	if c, err = config.Load(p.Root); err != nil { // target trunk, no compile
		t.Fatal(err)
	}
	if got := c.NoCompileWarning(""); got != "" {
		t.Errorf("obor init's warning, target a branch other than the default: %q, want none", got)
	}
	bare := newProject(t) // no repository: nothing to look in, target main or not
	if got := validateWith(t, bare, git, set("merge.target_branch", "trunk")); len(got) != 0 {
		t.Errorf("no repository: %q, want no line", got)
	}
}
