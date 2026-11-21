//go:build integration

package checks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/testkit"
)

// checkoutFx is a lane whose worker branch holds obor's link at path (a symlink to the main checkout's untracked file
// of that path), and cand, a commit that tracks path (and the files of extra) on top of the worker branch's commit.
type checkoutFx struct {
	p      *testkit.Project
	cfg    *config.Config
	lane   string
	path   string
	base   string // the worker branch's commit: it does not track path
	cand   string
	branch string
}

// newCheckoutFx: path is the settings file or a lanes.links entry (the settings' ignore lines of the fixture go: a
// link git ignores is not obor's way). extra: files the candidate tracks beside path.
func newCheckoutFx(t *testing.T, path string, extra ...string) checkoutFx {
	t.Helper()
	var edits []func(map[string]any)
	if path != project.PluginSettings {
		edits = append(edits, testkit.Set("lanes.links", []any{path}))
	}
	p, lane, base := laneFx(t, edits...)
	exclude := filepath.Join(p.Root, ".git", "info", "exclude")
	var kept []string
	for _, line := range strings.Split(p.Read(".git/info/exclude"), "\n") {
		if !strings.Contains(line, ".claude") {
			kept = append(kept, line)
		}
	}
	p.Write(exclude, strings.Join(kept, "\n"))
	if err := os.RemoveAll(filepath.Join(lane, ".claude")); err != nil {
		t.Fatal(err)
	}
	p.Write(path, "main's own\n")
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	fx := checkoutFx{p: p, cfg: cfg, lane: lane, path: path, base: base, branch: "worker"}
	p.Git(lane, "checkout", "-q", "-B", fx.branch, base)
	fx.link(t)
	fx.cand = fx.candidate(t, append([]string{path}, extra...)...)
	return fx
}

// link makes obor's link at fx.path in the lane (nothing is there).
func (fx checkoutFx) link(t *testing.T) {
	t.Helper()
	dst := filepath.Join(fx.lane, fx.path)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(fx.p.Root, fx.path), dst); err != nil {
		t.Fatal(err)
	}
	if st := fx.p.Git(fx.lane, "status", "--porcelain", "--untracked-files=all", "--", fx.path); st != "?? "+fx.path {
		t.Fatalf("precondition: the link's status %q, want it untracked", st)
	}
}

// candidate is a commit on fx.base that tracks each of paths with the content "tracked\n", made in a temporary index:
// no checkout, the main checkout's own files stay as they are.
func (fx checkoutFx) candidate(t *testing.T, paths ...string) string {
	t.Helper()
	env := append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(t.TempDir(), "index"))
	git := func(args ...string) string {
		t.Helper()
		out, err := project.GitCtxEnv(context.Background(), fx.p.Root, env, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(out)
	}
	content := filepath.Join(t.TempDir(), "tracked")
	if err := os.WriteFile(content, []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blob := git("hash-object", "-w", content)
	git("read-tree", fx.base)
	for _, path := range paths {
		git("update-index", "--add", "--cacheinfo", "100644,"+blob+","+path)
	}
	return git("-c", "user.name=obor test", "-c", "user.email=obor-test@example.invalid", "commit-tree", git("write-tree"),
		"-p", fx.base, "-m", "tracks "+strings.Join(paths, " "))
}

// checkout is project.CheckoutWorktree over the lane with git checkout -q args…, as the callers run it; lines is
// what it said.
func (fx checkoutFx) checkout(target string, args ...string) (lines []string, err error) {
	git := func(dir string, a ...string) (string, error) { return project.Git(dir, a...) }
	say := func(line string) { lines = append(lines, line) }
	err = project.CheckoutWorktree(fx.cfg, fx.lane, target, git, say, func() error {
		_, err := project.Git(fx.lane, append([]string{"checkout", "-q"}, args...)...)
		return err
	})
	return lines, err
}

// isLink asserts fx.path in the lane is obor's link: a symlink to the main checkout's file.
func (fx checkoutFx) isLink(t *testing.T, when string) {
	t.Helper()
	dst := filepath.Join(fx.lane, fx.path)
	if fi, err := os.Lstat(dst); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s: %s is no link (%v)", when, dst, err)
	}
	if got, err := os.Readlink(dst); err != nil || got != filepath.Join(fx.p.Root, fx.path) {
		t.Errorf("%s: %s -> %q, %v; want %s", when, dst, got, err, filepath.Join(fx.p.Root, fx.path))
	}
}

// isTracked asserts fx.path in the lane is the regular file git wrote, no link.
func (fx checkoutFx) isTracked(t *testing.T, when string) {
	t.Helper()
	keptRegular(t, filepath.Join(fx.lane, fx.path), "tracked\n")
	if st := fx.p.Git(fx.lane, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Errorf("%s: the lane is not clean: %q", when, st)
	}
}

// project.CheckoutWorktree, the cases on the plugin's settings file; the case that links a file the target lacks runs on
// a lanes.links entry instead (the same results for both paths: one link set, one way to link).
func TestCheckoutWorktree(t *testing.T) {
	t.Parallel()
	path := project.PluginSettings

	t.Run("a link the target tracks gives way to the tracked file", func(t *testing.T) {
		t.Parallel()
		fx := newCheckoutFx(t, path)
		lines, err := fx.checkout(fx.cand, "--detach", fx.cand)
		if err != nil {
			t.Fatalf("checkout: %v", err)
		}
		fx.isTracked(t, "after the checkout")
		if want := "removed link " + path + ": " + fx.cand[:7] + " tracks it"; strings.Join(lines, "\n") != want {
			t.Errorf("said %q, want %q", lines, want)
		}
		if h := fx.p.Git(fx.lane, "rev-parse", "HEAD"); h != fx.cand {
			t.Errorf("lane at %s, want the candidate %s", h, fx.cand)
		}
	})

	t.Run("a tracked file the target lacks is linked", func(t *testing.T) {
		t.Parallel()
		path := "conf/local.env" // the one case on the second path: a lanes.links entry, its directory made with the link
		fx := newCheckoutFx(t, path)
		if _, err := fx.checkout(fx.cand, "--detach", fx.cand); err != nil {
			t.Fatalf("to the candidate: %v", err)
		}
		fx.isTracked(t, "precondition: on the candidate")
		lines, err := fx.checkout(fx.base, "--detach", fx.base)
		if err != nil {
			t.Fatalf("to the base: %v", err)
		}
		fx.isLink(t, "after the checkout")
		if want := "linked " + path; strings.Join(lines, "\n") != want {
			t.Errorf("said %q, want %q", lines, want)
		}
	})

	t.Run("a round trip worker branch, candidate, worker branch ends with the link back", func(t *testing.T) {
		t.Parallel()
		fx := newCheckoutFx(t, path)
		fx.isLink(t, "precondition: on the worker branch")
		if _, err := fx.checkout(fx.cand, "--detach", fx.cand); err != nil {
			t.Fatalf("to the candidate: %v", err)
		}
		fx.isTracked(t, "on the candidate")
		if _, err := fx.checkout(fx.branch, fx.branch); err != nil {
			t.Fatalf("back to the worker branch: %v", err)
		}
		fx.isLink(t, "back on the worker branch")
		if b := fx.p.Git(fx.lane, "branch", "--show-current"); b != fx.branch {
			t.Errorf("lane on %q, want %q", b, fx.branch)
		}
		if st := fx.p.Git(fx.lane, "status", "--porcelain", "--untracked-files=all"); st != "?? "+path {
			t.Errorf("status %q, want only the link as untracked", st)
		}
	})

	t.Run("a regular file at the path fails the checkout as git does and stays", func(t *testing.T) {
		t.Parallel()
		fx := newCheckoutFx(t, path)
		dst := filepath.Join(fx.lane, path)
		if err := os.Remove(dst); err != nil {
			t.Fatal(err)
		}
		fx.p.Write(dst, "the lane's own\n")
		lines, err := fx.checkout(fx.cand, "--detach", fx.cand)
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("checkout: %v, want git's refusal naming %s", err, path)
		}
		keptRegular(t, dst, "the lane's own\n")
		if len(lines) != 0 {
			t.Errorf("said %q for a path that is no link, want nothing", lines)
		}
		if h := fx.p.Git(fx.lane, "rev-parse", "HEAD"); h != fx.base {
			t.Errorf("lane at %s, want it left at %s", h, fx.base)
		}
	})

	t.Run("a failed checkout puts the removed link back and returns git's error", func(t *testing.T) {
		t.Parallel()
		fx := newCheckoutFx(t, path, "other.txt")
		fx.p.Write(filepath.Join(fx.lane, "other.txt"), "the lane's own\n")
		lines, err := fx.checkout(fx.cand, "--detach", fx.cand)
		if err == nil || !strings.Contains(err.Error(), "other.txt") {
			t.Fatalf("checkout: %v, want git's refusal naming other.txt", err)
		}
		fx.isLink(t, "after the failed checkout")
		keptRegular(t, filepath.Join(fx.lane, "other.txt"), "the lane's own\n")
		want := []string{"removed link " + path + ": " + fx.cand[:7] + " tracks it", "linked " + path}
		if strings.Join(lines, "\n") != strings.Join(want, "\n") {
			t.Errorf("said %q, want %q", lines, want)
		}
		if h := fx.p.Git(fx.lane, "rev-parse", "HEAD"); h != fx.base {
			t.Errorf("lane at %s, want it left at %s", h, fx.base)
		}
	})
}

// Each checkout obor runs in a worktree it made keeps the links: a lane's drop (detach), its prepare (checkout -B) and
// a lane test's candidate and restore go through project.CheckoutWorktree, so a worker branch that held the link has
// it again after each; the review detach's and the scratch's sites are the same call (their own tests).
func TestLaneCheckoutsPutTheLinkBack(t *testing.T) {
	t.Parallel()
	fx := newCheckoutFx(t, project.PluginSettings)
	l := Lane{Cfg: fx.cfg, Out: os.Stderr}
	ctx := context.Background()

	if err := l.checkout(ctx, fx.lane, fx.cand, "--detach", fx.cand); err != nil {
		t.Fatalf("detach at the candidate: %v", err)
	}
	fx.isTracked(t, "on the candidate")
	if err := l.checkout(ctx, fx.lane, fx.base, "-B", fx.branch, fx.base); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	fx.isLink(t, "after prepare")

	g := Scratch{Options: Options{Cfg: fx.cfg, Out: os.Stderr}}.gio()
	if err := g.Detach(fx.lane, fx.cand); err != nil {
		t.Fatalf("lane test's candidate: %v", err)
	}
	fx.isTracked(t, "on the lane test's candidate")
	if err := g.Switch(fx.lane, fx.branch); err != nil {
		t.Fatalf("lane test's restore: %v", err)
	}
	fx.isLink(t, "after the lane test's restore")
}
