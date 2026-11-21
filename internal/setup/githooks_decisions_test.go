package setup

// obor setup githooks' decisions (which hooks dir core.hooksPath gets, what is chained, what --remove restores), on a
// fake git (newFakeGitFx): its work is git config, which the fake keeps. The hooks themselves, run by real git, are
// githooks_integration_test.go's.

import (
	"os"
	"path/filepath"
	"testing"
)

// tests/githooks.test.sh case_setup_githooks_sets_chains_removes
func TestGithooksSetsChainsRemoves(t *testing.T) {
	t.Parallel()
	x := newFakeGitFx(t)
	hooks := filepath.Join(kit, "githooks")
	chain := filepath.Join(hooks, "chain")
	if out := x.ok(t, "githooks"); out != "OK githooks: core.hooksPath = "+hooks+"\n" {
		t.Errorf("set: %q", out)
	}
	if got := x.Git(x.Root, "config", "--local", "--get", "core.hooksPath"); got != hooks {
		t.Errorf("hooksPath %q", got)
	}
	if got, err := x.gitOut(x.Root, "config", "--local", "--get", "obor.hooksChain"); got != "" || err == nil {
		t.Errorf("no chain for a repo without its own hooks: %q (%v)", got, err)
	}
	has(t, x.ok(t, "githooks"), "OK githooks: core.hooksPath already", "rerun")
	x.ok(t, "githooks", "--remove")
	if got, _ := x.gitOut(x.Root, "config", "--local", "--get", "core.hooksPath"); got != "" {
		t.Errorf("removed: %q", got)
	}
	// The project's own hooksPath is chained, not refused: recorded, then obor's full set.
	x.Git(x.Root, "config", "core.hooksPath", ".githooks")
	if out := x.ok(t, "githooks"); out != "OK githooks: core.hooksPath = "+chain+", chaining .githooks\n" {
		t.Errorf("own hooksPath: %q", out)
	}
	if got := x.Git(x.Root, "config", "--local", "--get", "obor.hooksChain"); got != ".githooks" {
		t.Errorf("obor.hooksChain %q", got)
	}
	if got := x.Git(x.Root, "config", "--local", "--get", "core.hooksPath"); got != chain {
		t.Errorf("chained hooksPath %q", got)
	}
	has(t, x.ok(t, "githooks"), "OK githooks: core.hooksPath already", "rerun chained")
	x.ok(t, "githooks", "--remove")
	if got := x.Git(x.Root, "config", "--local", "--get", "core.hooksPath"); got != ".githooks" {
		t.Errorf("restored hooksPath %q", got)
	}
	if got, err := x.gitOut(x.Root, "config", "--local", "--get", "obor.hooksChain"); got != "" || err == nil {
		t.Errorf("chain unset: %q (%v)", got, err)
	}
	// An absolute chainsPath is recorded as it is.
	x.Git(x.Root, "config", "core.hooksPath", "/elsewhere")
	if out := x.ok(t, "githooks"); out != "OK githooks: core.hooksPath = "+chain+", chaining /elsewhere\n" {
		t.Errorf("absolute hooksPath: %q", out)
	}
	x.ok(t, "githooks", "--remove")
	if got := x.Git(x.Root, "config", "--get", "core.hooksPath"); got != "/elsewhere" {
		t.Errorf("absolute restored: %q", got)
	}
	// Not obor's: --remove leaves it alone.
	x.failing(t, "FAIL core.hooksPath is /elsewhere (not obor's "+hooks+"): left alone", "githooks", "--remove")
	if got := x.Git(x.Root, "config", "--get", "core.hooksPath"); got != "/elsewhere" {
		t.Errorf("someone else's hooksPath kept: %q", got)
	}
}

// The hook in .git/hooks that makes core.hooksPath githooks/chain/ (every hook git runs): an executable one other than
// pre-commit, pre-push and a *.sample, which only chain/ can run once core.hooksPath is set. A rerun moves between the
// two sets as that changes, and --remove works from either. (The decision itself is TestPlanHooks's; the work of each
// run is git config: the fake git keeps it, TestGithooks runs the real hooks.)
func TestGithooksPicksTheHooksDir(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(kit, "githooks")
	chain := filepath.Join(dir, "chain")
	hook := func(x *fx, name string, mode os.FileMode) {
		os.MkdirAll(filepath.Join(x.Root, ".git", "hooks"), 0o755)
		os.Chmod(x.Write(filepath.Join(".git", "hooks", name), "#!/bin/sh\n"), mode)
	}
	for _, c := range []struct {
		name    string
		setup   func(t *testing.T, x *fx)
		want    string
		removed string // core.hooksPath after --remove ("" unset)
	}{
		{"a .git/hooks/post-merge: chain/", func(t *testing.T, x *fx) { hook(x, "post-merge", 0o755) }, chain, ""},
		{"only .git/hooks/pre-commit: githooks/", func(t *testing.T, x *fx) { hook(x, "pre-commit", 0o755) }, dir, ""},
		{"a .git/hooks/post-merge.sample: githooks/", func(t *testing.T, x *fx) {
			hook(x, "post-merge.sample", 0o755)
		}, dir, ""},
		{"a non-executable .git/hooks/post-merge: githooks/", func(t *testing.T, x *fx) {
			hook(x, "post-merge", 0o644)
		}, dir, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newFakeGitFx(t)
			c.setup(t, x)
			has(t, x.ok(t, "githooks"), "core.hooksPath = "+c.want, "hooksPath")
			if got := x.Git(x.Root, "config", "--local", "--get", "core.hooksPath"); got != c.want {
				t.Errorf("core.hooksPath = %q, want %q", got, c.want)
			}
			has(t, x.ok(t, "githooks"), "already "+c.want, "rerun")
			x.ok(t, "githooks", "--remove")
			if got, _ := x.gitOut(x.Root, "config", "--local", "--get", "core.hooksPath"); got != c.removed {
				t.Errorf("--remove: core.hooksPath = %q, want %q", got, c.removed)
			}
		})
	}
}

// What `obor setup githooks` decides, from the values git would return (planHooks): the set core.hooksPath gets, whether
// the current value is the project's own (chained), and the chain recorded. The set is githooks/ (pre-commit, pre-push and
// the runner) unless there is something to chain: a recorded obor.hooksChain, or a hook in .git/hooks the small set lacks;
// then it is githooks/chain/.
func TestPlanHooks(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(kit, "githooks")
	chainDir := filepath.Join(dir, "chain")
	oldKit := t.TempDir()
	if err := os.MkdirAll(filepath.Join(oldKit, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldKit, ".claude-plugin", "plugin.json"), []byte(`{"name": "obor"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name       string
		cur, chain string
		dotgit     bool
		own        bool
		wantChain  string
		want       string
	}{
		{"nothing set, no .git/hooks hook: githooks/", "", "", false, false, "", dir},
		{"a chain recorded: chain/", "", ".githooks", false, false, ".githooks", chainDir},
		{"a .git/hooks hook the small set lacks: chain/", "", "", true, false, "", chainDir},
		{"already githooks/: kept, not chained", dir, "", false, false, "", dir},
		{"already chain/ with its chain recorded: kept", chainDir, ".githooks", false, false, ".githooks", chainDir},
		{"chain/ whose chain was removed: back to githooks/", chainDir, "", false, false, "", dir},
		{"the project's own relative hooks dir: chained", ".githooks", "", false, true, ".githooks", chainDir},
		{"the project's own absolute hooks dir: chained as it is", "/elsewhere", "", false, true, "/elsewhere", chainDir},
		{"the project's own dir replaces a recorded chain", "/usr/local/hooks", ".old", false, true, "/usr/local/hooks", chainDir},
		{"another obor kit's githooks: re-pointed, not chained", filepath.Join(oldKit, "githooks"), "", false, false, "", dir},
		{"another obor kit's chain set: re-pointed, chain kept", filepath.Join(oldKit, "githooks", "chain"), ".githooks", false,
			false, ".githooks", chainDir},
		{"a kit that is gone, named like one: re-pointed", "/nowhere/obor-kit/githooks", "", false, false, "", dir},
		{"a githooks dir that is not obor's: chained", "/nowhere/husky/githooks", "", false, true, "/nowhere/husky/githooks",
			chainDir},
	} {
		asked := false
		own, chain, target := planHooks(dir, chainDir, c.cur, c.chain, func() bool { asked = true; return c.dotgit })
		if own != c.own || chain != c.wantChain || target != c.want {
			t.Errorf("%s: own %v chain %q target %q, want own %v chain %q target %q", c.name, own, chain, target, c.own,
				c.wantChain, c.want)
		}
		if asked && (c.chain != "" || c.own) {
			t.Errorf("%s: .git/hooks was read though something is already chained", c.name)
		}
	}
}

// A githooks path of another obor kit (a checkout whose plugin.json names obor, or one that is gone) is re-pointed at
// this kit's; anyone else's hooks dir is the project's own: chained, not refused, so obor's full set runs
func TestGithooksRepointsAnotherOborKitsHooksAndChainsOthers(t *testing.T) {
	x := newFakeGitFx(t)
	mine := filepath.Join(kit, "githooks")
	chain := filepath.Join(mine, "chain")
	// an obor kit's githooks (its plugin.json names obor): re-pointed
	old := filepath.Join(x.Base, "obor-kit-stable")
	x.Write(old+"/.claude-plugin/plugin.json", `{"name": "obor"}`)
	x.Git(x.Root, "config", "--local", "core.hooksPath", old+"/githooks")
	out := x.ok(t, "githooks")
	has(t, out, "core.hooksPath = "+mine, "output")
	if got := x.Git(x.Root, "config", "--local", "core.hooksPath"); got != mine {
		t.Errorf("core.hooksPath = %s, want %s", got, mine)
	}
	// an obor kit's chain set is re-pointed too
	x.Git(x.Root, "config", "--local", "core.hooksPath", old+"/githooks/chain")
	has(t, x.ok(t, "githooks"), "core.hooksPath = "+mine, "a kit's chain set re-pointed")
	if got := x.Git(x.Root, "config", "--local", "core.hooksPath"); got != mine {
		t.Errorf("a kit's chain set: core.hooksPath = %s, want %s", got, mine)
	}
	// a kit that is gone, named like one: re-pointed
	x.Git(x.Root, "config", "--local", "core.hooksPath", "/nowhere/obor-kit/githooks")
	x.ok(t, "githooks")
	// anyone else's hooks dir is the project's own: chained, not refused
	for _, other := range []string{"/nowhere/husky/githooks", "/usr/local/hooks", x.Base + "/githooks"} {
		x.Git(x.Root, "config", "--local", "core.hooksPath", other)
		has(t, x.ok(t, "githooks"), "chaining "+other, "chained hooks "+other)
		if got := x.Git(x.Root, "config", "--local", "--get", "obor.hooksChain"); got != other {
			t.Errorf("obor.hooksChain = %s, want %s", got, other)
		}
		if got := x.Git(x.Root, "config", "--local", "core.hooksPath"); got != chain {
			t.Errorf("core.hooksPath %s changed to %s, want %s", other, got, chain)
		}
	}
}
