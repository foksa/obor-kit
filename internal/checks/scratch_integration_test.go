//go:build integration

package checks

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/testkit"
)

// Scratch on real git (T-1011): the scenario table whose names match internal/app's FakeScratch cases, and the cases
// only real git shows (rerere's replay, a canceled git's process group, --no-verify against a project's hooks, the
// gate's and lane test's commands). Behind the integration tag: plain go test runs the fake-backed decision tests
// (decide_test.go) alone.

// T-913: a conflict the integrator resolved is not resolved again when the target moves. The gate's merge records the
// conflict (rerere), the integrator's plain resolve commit records the resolution in the repository's common rr-cache
// (serve's worktree shares it), and the next merge of the same sha onto a moved target replays it: a clean merge that
// compiles and says so. A target move that changes the conflicted hunk differently is another preimage: a
// conflict as today.
func TestScratchRerereReplaysResolvedConflict(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("compile", "echo 1 pass"))
	sha := y.conflicted()

	// The cache is the main checkout's own (its common git dir), not a per-worktree one: the two replay each other's
	// resolutions.
	cache := func(dir string) string {
		p := y.Git(dir, "rev-parse", "--git-path", "rr-cache")
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		return p
	}
	here, shared := cache(y.serve), cache(y.Root)
	a, err := os.Stat(here)
	if err != nil {
		t.Fatalf("precondition: no rr-cache at %s: %v", here, err)
	}
	b, err := os.Stat(shared)
	if err != nil {
		t.Fatalf("precondition: no rr-cache at %s: %v", shared, err)
	}
	if !os.SameFile(a, b) {
		t.Errorf("the scratch's rr-cache %s is not the main checkout's %s: the resolution would not be shared", here, shared)
	}
	if pre, _ := filepath.Glob(filepath.Join(here, "*", "preimage")); len(pre) == 0 {
		t.Fatalf("precondition: the conflicting gate recorded no preimage in %s", here)
	}

	res := y.resolveServe() // the integrator's own commit: no obor flag, git turns rerere on once rr-cache exists
	if post, _ := filepath.Glob(filepath.Join(here, "*", "postimage")); len(post) == 0 {
		t.Fatalf("precondition: the resolve commit recorded no resolution in %s", here)
	}

	y.Write("other.txt", "other\n")
	y.Git(y.Root, "add", "other.txt")
	y.Git(y.Root, "commit", "-qm", "unrelated move")
	moved := y.head(y.Root)
	o := y.gate(port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputMoved, Input: sha, Summary: "feature", Resolved: res})
	if o.Kind != port.OutcomeGreen {
		t.Fatalf("the moved gate: %s %q; want green (the recorded resolution replayed)\noutput:\n%s", o.Kind, o.Line, y.out.String())
	}
	c := o.Candidate
	y.eq(c.Target, moved, "merged onto the moved target")
	y.eq(y.Git(y.Root, "rev-parse", c.SHA+"^2"), sha, "the merge of the submitted sha (T-403)")
	y.eq(y.Git(y.Root, "show", c.SHA+":shared.txt"), "both", "the candidate holds the recorded resolution")
	y.contains(y.Git(y.Root, "log", "-1", "--format=%B", c.SHA), "(conflict resolution reused)", "the merge's message")
	y.eq(c.Checks, "compile 1 pass", "the replayed merge compiled (T-961: its lane test runs next)")
	y.clean("the replayed merge")

	z := newSx(t, cmd("check", "echo 1 pass"))
	sha = z.conflicted()
	res = z.resolveServe()
	z.Write("shared.txt", "other main\n")
	z.Git(z.Root, "commit", "-qam", "a different main edit")
	o = z.gate(port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputMoved, Input: sha, Summary: "feature", Resolved: res})
	if o.Kind != port.OutcomeConflict {
		t.Fatalf("a move touching the same hunk differently: %s %q; want a conflict", o.Kind, o.Line)
	}
	z.eq(strings.Join(o.Files, " "), "shared.txt", "the path still unmerged")
	if strings.Contains(o.Line, "differs") {
		t.Errorf("unresolved paths after the replay: a conflict as before T-915, not a differing replay: %q", o.Line)
	}
}

// RV-38 #12: the adapter's git ends with its ctx. Canceled before entry: no git call runs, nothing changes. Canceled
// while a git call blocks (a hook's child, say): the call's process group is killed, Submit returns promptly with no
// child left, and run again it completes.
func TestGitEndsWithCtx(t *testing.T) {
	t.Parallel()
	y := newSx(t)
	y.change("feature2.txt", "more")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	y.arm(0)
	if _, err := y.s.Submit(canceled, "T-05", "1", "L-01", "feature", "w"); err == nil {
		t.Fatal("canceled Submit: no error")
	}
	y.eq(strings.Join(y.calls(), "|"), "", "no git call ran")
	y.eq(y.ref(submittedRefs+"T-05/1"), "", "no pin")
	y.contains(y.Git(y.lane, "status", "--porcelain"), "feature2.txt", "the lane not committed")

	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	child, blocked := filepath.Join(y.Base, "child.pid"), filepath.Join(y.Base, "blocked")
	shim := y.stub("git-block", fmt.Sprintf(`case "$*" in *update-ref*)
  if [ ! -f '%[2]s' ]; then sleep 3 & echo $! > '%[1]s'; : > '%[2]s'; wait; fi;; esac
exec '%[3]s' "$@"`, child, blocked, real))
	o := y.opts()
	o.Git = shim
	c, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		y.waitFor(blocked)
		cancel()
	}()
	at := time.Now()
	if _, err := (Scratch{Options: o}).Submit(c, "T-05", "1", "L-01", "feature", "w"); err == nil {
		t.Fatal("Submit canceled during a blocked git call: no error")
	}
	if d := time.Since(at); d > 10*time.Second {
		t.Errorf("Submit returned after %s; want promptly", d)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(y.Read(child)))
	if err != nil {
		t.Fatal(err)
	}
	gone := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && !gone; time.Sleep(20 * time.Millisecond) {
		gone = syscall.Kill(pid, 0) != nil
	}
	if !gone {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the blocked git's child %d outlived the cancel", pid)
	}
	sha, err := (Scratch{Options: o}).Submit(ctx, "T-05", "1", "L-01", "feature", "w")
	if err != nil {
		t.Fatal(err)
	}
	y.eq(sha, y.head(y.lane), "run again, the submit completes")
	y.eq(y.ref(submittedRefs+"T-05/1"), sha, "and pins")
}

// T-622: serve's own commits pass --no-verify. The project's own hooks dir (obor.hooksChain, chained by the kit's
// commit-msg since T-620) rejects every message, yet submit's commit and the gate's merge commit are made.
func TestScratchOwnCommitsSkipProjectCommitHooks(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 1 pass"))
	// The kit's commit-msg lives in githooks/chain/ and sources ../_hook (T-623): copy the two files into the same
	// shape and point core.hooksPath at the chain dir.
	hooks := filepath.Join(y.Base, "hooks")
	for _, c := range []struct{ dir, name string }{{"", "_hook"}, {"chain", "commit-msg"}} {
		b, err := os.ReadFile(filepath.Join(kitDir, "githooks", c.dir, c.name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(hooks, c.dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(hooks, c.dir, c.name), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	y.Git(y.Root, "config", "core.hooksPath", filepath.Join(hooks, "chain"))
	y.Git(y.Root, "config", "obor.hooksChain", ".githooks")
	chained := y.Write(".githooks/commit-msg", "#!/bin/sh\necho 'commit-msg: rejects every message' >&2\nexit 1\n")
	if err := os.Chmod(chained, 0o755); err != nil {
		t.Fatal(err)
	}

	// Precondition: a plain commit runs the chained hook and fails.
	y.Write("probe.txt", "probe\n")
	y.Git(y.Root, "add", "probe.txt")
	probe := exec.Command("git", "-C", y.Root, "-c", "user.name=obor test", "-c", "user.email=obor-test@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "-q", "-m", "T-05: probe")
	out, err := probe.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "rejects every message") {
		t.Fatalf("precondition: the chained commit-msg must reject a plain commit: %v\n%s", err, out)
	}
	y.Git(y.Root, "reset", "-q")

	sha := y.submit()
	y.eq(y.Git(y.lane, "log", "-1", "--format=%s"), "T-05: feature", "submit's commit")
	c := y.green(lane(sha))
	y.eq(y.Git(y.Root, "log", "-1", "--format=%s", c.SHA), "Merge T-05: feature", "the gate's merge commit")
	parents := strings.Fields(y.Git(y.Root, "rev-list", "--parents", "-n", "1", c.SHA))
	if len(parents) != 3 || parents[2] != sha {
		t.Errorf("the merge commit's parents %q, want the submitted %s on the target", parents, sha)
	}
}

// T-669 (RV-75 #4), T-673: serve's gate tells GateInput.Command each command as it starts and "" as it ends (T-961: its
// lane_setup and compile), and the lane test tells TestInput.Command its own: a partial merge its guard then its class's
// check (check_affected), never check; a full merge check; each runs on with no command between two.
func TestGateReportsItsCommands(t *testing.T) {
	t.Parallel()
	partial := testkit.Set("merge", map[string]any{"trailer": trailer, "summary_regex": "[0-9]+ pass", "guard": "guard",
		"classes": []any{map[string]any{"name": "any", "paths": []any{"**"}, "checks": []any{"check_affected"}}}})
	for _, tc := range []struct {
		name  string
		edits []func(map[string]any)
		want  []string
	}{
		{"partial", []func(map[string]any){partial, cmd("guard", "echo 1 pass"), cmd("check_affected", "echo 2 pass"),
			cmd("check", "echo 3 pass")}, []string{"guard", "", "check_affected", ""}},
		{"full", []func(map[string]any){cmd("check", "echo 3 pass")}, []string{"check", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			y := newSx(t, append(tc.edits, cmd("compile", "echo 1 pass"), cmd("lane_setup", "true"))...)
			in := lane(y.submit())
			var got []string
			in.Command = func(name string) { got = append(got, name) }
			c := y.green(in)
			if want := []string{"lane_setup", "", "compile", ""}; !slices.Equal(got, want) {
				t.Errorf("the gate told %q, want %q", got, want)
			}
			got = nil
			ti := port.TestInput{Task: "T-05", Lane: "L-01", SHA: c.SHA, Target: c.Target,
				Command: func(name string) { got = append(got, name) }}
			if o, err := y.s.Test(ctx, ti); err != nil || o.Kind != port.OutcomeGreen {
				t.Fatalf("test: %+v %v", o, err)
			}
			if want := append([]string{"lane_setup", ""}, tc.want...); !slices.Equal(got, want) {
				t.Errorf("the lane test told %q, want %q", got, want)
			}
		})
	}
}

// The real-git scenario table (T-1011): Scratch's decisions are tested on fakeGit (decide_test.go); these few cases per
// gitIO method show real git gives the answers the fake scripts. Each name is the internal/app FakeScratch case it
// stands behind ("Gate lane conflict": Script("Gate lane", task, port.OutcomeConflict)); the lock cases are
// TestSubmitLocks', the lane test's TestLaneTest*'s, the replayed resolution's TestScratchRerereReplaysResolvedConflict
// and TestRV123RerereDropsOtherResolvedEdits'.
func TestScratchGitScenarios(t *testing.T) {
	t.Parallel()
	t.Run("Submit, Gate lane green, Publish hold, merged, already, Landed, Unpin", func(t *testing.T) {
		t.Parallel()
		y := newSx(t, cmd("check", "echo 1 pass"))
		if landed, err := y.s.Landed(ctx, "T-05"); landed || err != nil {
			t.Errorf("Landed before the merge: %v %v; want false", landed, err)
		}
		base, target := y.head(y.lane), y.head(y.Root)
		sha := y.submit()
		y.eq(y.Git(y.lane, "rev-list", "--count", base+".."+sha), "1", "Submit: one commit")
		y.eq(y.Git(y.lane, "log", "-1", "--format=%s", sha), "T-05: feature", "Submit: the commit's subject")
		y.eq(y.Git(y.lane, "status", "--porcelain"), "", "Submit: the lane committed")
		y.eq(y.ref(submittedRefs+"T-05/1"), sha, "Submit: pinned")
		y.lane2() // on task/T-20 at the target: its work is not there
		if _, err := y.s.Submit(ctx, "T-20", "1", "L-02", "x", "w"); !errors.As(err, new(port.Refused)) {
			t.Errorf("Submit of a lane already in the target: %v; want Refused", err)
		}

		c := y.green(lane(sha))
		y.eq(y.Git(y.Root, "rev-parse", c.SHA+"^1")+" "+y.Git(y.Root, "rev-parse", c.SHA+"^2"), target+" "+sha,
			"Gate lane green: the candidate merges the submitted sha onto the target")
		y.eq(y.Git(y.Root, "log", "-1", "--format=%s", c.SHA), "Merge T-05: feature", "Gate lane green: the subject")
		y.eq(y.ref(candidateRefs+"T-05"), c.SHA, "Gate lane green: pinned")
		y.eq(y.head(y.serve), c.SHA, "Gate lane green: serve's worktree at the candidate")
		y.eq(y.Git(y.serve, "status", "--porcelain"), "", "Gate lane green: serve's worktree clean")
		y.eq(c.Base, y.Git(y.Root, "merge-base", sha, target), "Gate lane green: the base, the lane's fork point")
		y.eq(c.Patch, fmt.Sprintf("%x", sha256.Sum256([]byte(y.Git(y.Root, "diff", c.Base, sha)))),
			"Gate lane green: the patch, the own diff's hash")
		y.contains(c.Diff, "+feature", "Gate lane green: the diff kept")
		y.eq(c.Diff, y.Git(y.Root, "diff", c.Target, c.SHA), "Gate lane green: the candidate's diff")
		y.contains(c.Stat, "feature.txt", "Gate lane green: the stat")
		y.eq(y.green(lane(sha)).SHA, c.SHA, "Gate lane green: a repeat makes the same commit")

		y.Write("feature.txt", "mine\n")
		o, err := y.s.Publish(ctx, c)
		y.eq(fmt.Sprintf("%s %v %v", o.Kind, o.Files, err), "hold [feature.txt] <nil>", "Publish hold: a dirty merged file")
		y.eq(y.head(y.Root), target, "Publish hold: nothing merged")
		os.Remove(y.Path("feature.txt"))
		o, err = y.s.Publish(ctx, c)
		y.eq(fmt.Sprintf("%s %v", o.Kind, err), "merged <nil>", "Publish merged")
		y.eq(y.head(y.Root), c.SHA, "Publish merged: fast-forwarded")
		y.eq(y.pins("T-05"), "submitted=- candidate=- resolved=-", "Publish merged: unpinned")
		o, err = y.s.Publish(ctx, c)
		y.eq(fmt.Sprintf("%s %v", o.Kind, err), "already <nil>", "Publish already")
		if landed, err := y.s.Landed(ctx, "T-05"); !landed || err != nil {
			t.Errorf("Landed after the publish: %v %v; want true", landed, err)
		}

		y.Git(y.Root, "update-ref", submittedRefs+"T-05/2", c.SHA)
		y.Git(y.Root, "update-ref", submittedRefs+"T-050/1", c.SHA)
		if err := y.s.Unpin(ctx, "T-05"); err != nil {
			t.Fatal(err)
		}
		y.eq(y.Git(y.Root, "for-each-ref", "--format=%(refname)", "refs/obor/"), submittedRefs+"T-050/1",
			"Unpin: T-05's pins gone, T-050's kept")
	})
	t.Run("Gate lane green from a foreign state", func(t *testing.T) {
		t.Parallel()
		y := newSx(t, cmd("check", "echo 1 pass"))
		sha := y.submit()
		want := y.green(lane(sha))
		y.Write(filepath.Join(y.lane2(), "other.txt"), "other\n")
		y.Git(y.lane2(), "add", "other.txt")
		y.Git(y.lane2(), "commit", "-qm", "other")
		for _, c := range []struct {
			name  string
			state func()
		}{
			{"an open merge, a dirty tree", func() {
				y.Git(y.serve, "merge", "-q", "--no-commit", "--no-ff", "task/T-20")
				y.Write(filepath.Join(y.serve, "shared.txt"), "dirty\n")
				y.Write(filepath.Join(y.serve, "stray.txt"), "stray\n")
			}},
			{"the worktree removed", func() {
				if err := os.RemoveAll(y.serve); err != nil {
					t.Fatal(err)
				}
			}},
		} {
			c.state()
			got := y.green(lane(sha))
			y.eq(fmt.Sprintf("%+v", got), fmt.Sprintf("%+v", want), c.name+": the same candidate")
			y.eq(y.Git(y.serve, "status", "--porcelain"), "", c.name+": serve's worktree clean")
		}
	})
	t.Run("Gate lane conflict, Resolved, Gate resolved green", func(t *testing.T) {
		t.Parallel()
		y, input := conflict.openConflict(t)
		if !gitOK(y.serve, "rev-parse", "-q", "--verify", "MERGE_HEAD") {
			t.Fatal("Gate lane conflict: the merge is not open in serve's worktree")
		}
		y.Write(filepath.Join(y.serve, "shared.txt"), "both\n")
		y.Git(y.serve, "add", "shared.txt")
		y.Git(y.serve, "commit", "-qm", "Merge T-05: resolved\n\nkept both sides")
		res := y.head(y.serve)
		got, err := y.s.Resolved(ctx, "T-05", input, nil)
		if err != nil {
			t.Fatalf("Resolved: %v", err)
		}
		y.eq(got, res, "Resolved: the resolution")
		y.eq(y.ref(resolvedRefs+"T-05"), res, "Resolved: pinned")
		in := port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputResolved, Input: res, Title: "Stored", Summary: "feature"}
		c := y.green(in)
		parents := func(sha string) string { return y.Git(y.Root, "rev-parse", sha+"^1", sha+"^2", sha+"^{tree}") }
		y.eq(parents(c.SHA), parents(res), "Gate resolved green: the amended merge's parents and tree")
		y.eq(c.Target, y.Git(y.Root, "rev-parse", res+"^1"), "Gate resolved green: the target")
		y.eq(y.Git(y.Root, "log", "-1", "--format=%B", c.SHA), "Merge T-05: Stored\n\nkept both sides\n\nSummary: feature",
			"Gate resolved green: the amended message")
		y.eq(y.ref(candidateRefs+"T-05"), c.SHA, "Gate resolved green: pinned")
		y.eq(y.green(in).SHA, c.SHA, "Gate resolved green: a repeat makes the same commit")
	})
	t.Run("Publish moved, Gate moved green, Publish merged", func(t *testing.T) {
		t.Parallel()
		y := newSx(t, cmd("check", "echo 1 pass"))
		sha := y.submit()
		old := y.green(lane(sha))
		y.Write("other.txt", "other\n")
		y.Git(y.Root, "add", "other.txt")
		y.Git(y.Root, "commit", "-qm", "unrelated")
		o, err := y.s.Publish(ctx, old)
		y.eq(fmt.Sprintf("%s %s %v", o.Kind, o.Head, err), "moved "+y.head(y.Root)+" <nil>", "Publish moved")
		y.eq(y.ref(candidateRefs+"T-05"), old.SHA, "Publish moved: the candidate stays pinned")
		c := y.green(port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputMoved, Input: sha, Summary: "feature"})
		y.eq(c.Target, y.head(y.Root), "Gate moved green: on the moved target")
		y.eq(y.Git(y.Root, "rev-parse", c.SHA+"^2"), sha, "Gate moved green: merges the submitted sha (T-403)")
		if gitOK(y.Root, "merge-base", "--is-ancestor", old.SHA, c.SHA) {
			t.Errorf("Gate moved green: the old candidate %s went along", old.SHA)
		}
		y.eq(c.Patch, old.Patch, "Gate moved green: an unrelated move keeps the patch")
		y.contains(y.Git(y.Root, "log", "-1", "--format=%b", c.SHA), "Merged onto the moved main at "+c.Target,
			"Gate moved green: the body")
		if o, err := y.s.Publish(ctx, c); err != nil || o.Kind != port.PublishMerged {
			t.Fatalf("Publish merged: %+v %v", o, err)
		}
		y.eq(y.Git(y.Root, "rev-list", "--count", "--merges", "main"), "1", "Publish merged: one merge commit on main")
	})
	t.Run("Gate moved conflict", func(t *testing.T) {
		t.Parallel()
		y := newSx(t, cmd("check", "echo 1 pass"))
		y.Write(filepath.Join(y.lane, "shared.txt"), "lane\n")
		sha := y.submit()
		y.green(lane(sha))
		y.Write("shared.txt", "main\n")
		y.Git(y.Root, "commit", "-qam", "main edit")
		o := y.gate(port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputMoved, Input: sha, Summary: "feature"})
		if o.Kind != port.OutcomeConflict {
			t.Fatalf("Gate moved conflict: %s %q", o.Kind, o.Line)
		}
		y.eq(strings.Join(o.Files, " "), "shared.txt", "Gate moved conflict: the conflicting file")
		y.eq(y.Git(y.serve, "rev-parse", "MERGE_HEAD"), sha, "Gate moved conflict: the submitted sha's merge open")
	})
}

// T-915 (RV-123 #1): git rerere replays conflict hunks, not the whole resolution commit. A moved gate whose merge
// needed a replay is clean only when its patch equals the attempt's checked resolution's (GateInput.Resolved); else,
// or with none, a conflict naming the files whose patch differs, nothing compiled.

// The review's fixture: the resolution also changed b/b.go, main then moves in disjoint d/d.go; the replay drops the
// b/b.go edit. Before T-915 the moved gate went green with that edit lost.
func TestRV123RerereDropsOtherResolvedEdits(t *testing.T) {
	t.Parallel()
	y, sha, res, old := resolvedTmpl(true).resolved(t, true)
	y.eq(y.Git(y.Root, "show", old.SHA+":b/b.go"), "package b\n\nfunc B() int { return 40 }", "precondition: the checked resolution holds b's edit")
	y.edit(y.Root, "d/d.go", "return 1", "return 20")
	in := moved(sha)
	in.Resolved = res
	o := y.gate(in)
	if o.Kind != port.OutcomeConflict {
		t.Fatalf("moved gate: %s %q; want a conflict", o.Kind, o.Line)
	}
	y.eq(strings.Join(o.Files, " "), "b/b.go", "the conflict names the file whose patch differs")
	y.eq(fmt.Sprint(y.runs()), "1", "nothing compiled")
	if !strings.Contains(o.Line, "differs from the checked one") {
		t.Errorf("the line says why: %q", o.Line)
	}
	y.eq(o.Dir, y.serve, "the merge stays open in serve's worktree")
}

// T-917 (RV-123 V1): a replay mismatch has no unmerged path, so the gate outcome carries the checked resolution and the
// files, and Scratch.Resolved refuses the staged merge committed as it stands.

// Committing the staged merge unchanged is refused; restoring the lost edit is accepted and gated.
func TestResolvedRefusesMismatchUntilEditRestored(t *testing.T) {
	t.Parallel()
	y, sha, res, _ := resolvedTmpl(true).resolved(t, true)
	y.edit(y.Root, "d/d.go", "return 1", "return 20")
	in := moved(sha)
	in.Resolved = res
	o := y.gate(in)
	if o.Mismatch == nil {
		t.Fatalf("precondition: a mismatch, got %s %q", o.Kind, o.Line)
	}
	y.Git(y.serve, "commit", "-qm", "as it stands")
	_, err := y.s.Resolved(ctx, "T-05", sha, o.Mismatch)
	var ref port.Refused
	if !errors.As(err, &ref) {
		t.Fatalf("the unchanged staged merge: err %v; want a refusal", err)
	}
	y.contains(ref.Line, "b/b.go", "the refusal names the file")
	y.contains(ref.Line, res[:7], "and the checked resolution")
	if _, err := y.s.Resolved(ctx, "T-05", sha, nil); err != nil {
		t.Fatalf("precondition: without the mismatch the same commit is accepted (the old behaviour): %v", err)
	}

	y.Write(filepath.Join(y.serve, "b/b.go"), "package b\n\nfunc B() int { return 40 }\n")
	y.Git(y.serve, "commit", "-qa", "--amend", "-m", "restored")
	fixed, err := y.s.Resolved(ctx, "T-05", sha, o.Mismatch)
	if err != nil {
		t.Fatalf("the restored edit: %v", err)
	}
	before := y.runs()
	c := y.green(port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputResolved, Input: fixed})
	y.eq(y.Git(y.Root, "show", c.SHA+":b/b.go"), "package b\n\nfunc B() int { return 40 }", "the candidate holds the edit")
	y.eq(fmt.Sprint(y.runs()), fmt.Sprint(before+1), "the resolved gate compiled it")
}
