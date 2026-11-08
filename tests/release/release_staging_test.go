//go:build release

package release

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ancestor is whether <a> is an ancestor of (or equal to) <b> in the fixture's repo.
func (f *releaseFixture) ancestor(t *testing.T, a, b string) bool {
	t.Helper()
	return exec.Command("git", "-C", f.Root, "merge-base", "--is-ancestor", a, b).Run() == nil
}

// T-1035: release and ship run in the main checkout on the branch the loop merges to (staging), not in a worktree at
// main. release builds in its scratch worktree, copies the four binaries into this checkout's dist/ (where publish
// reads them), commits the catalog on staging and fast-forwards refs/heads/main to that commit; ship then pushes main
// and the tag from staging without a checkout of main. Staging stays checked out, and a re-run of each step is a no-op.
func TestReleaseAndShipOnStaging(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t)
	f.Git(f.Root, "checkout", "-q", "-b", "staging")
	base := f.Git(f.Root, "rev-parse", "refs/heads/main")
	if got := f.Git(f.Root, "rev-parse", "refs/heads/staging"); got != base {
		t.Fatalf("precondition: staging is %s, want main's %s", got, base)
	}

	out, failed := f.release(t, "V=1.2.4")
	if failed {
		t.Fatalf("make release on staging failed:\n%s", out)
	}
	// The checkout is still staging with a clean tree: no step switches or resets it.
	if got := f.Git(f.Root, "symbolic-ref", "--short", "HEAD"); got != "staging" {
		t.Errorf("release left the checkout on %q, want staging", got)
	}
	f.clean(t, "after the release on staging")
	// main is the release's catalog commit, on staging's line.
	head := f.Git(f.Root, "rev-parse", "HEAD")
	if !strings.Contains(out, "fast-forwarded refs/heads/main ") {
		t.Errorf("release does not say it fast-forwarded main:\n%s", out)
	}
	if got := f.Git(f.Root, "rev-parse", "refs/heads/main"); got != head {
		t.Errorf("refs/heads/main = %s, want the release commit %s (staging's HEAD)", got, head)
	}
	if got := f.Git(f.Root, "log", "-1", "--format=%s", "refs/heads/main"); got != "checksums: v1.2.4" {
		t.Errorf("refs/heads/main's subject = %q, want the checksums commit", got)
	}
	if !f.ancestor(t, base, "refs/heads/main") {
		t.Errorf("main moved off its line: %s is not an ancestor of the release commit", base)
	}
	// The four binaries are in this checkout's dist/, matching the tag's catalog: that is where ship's publish reads
	// them.
	sums := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(f.Git(f.Root, "show", "v1.2.4:bin/checksums.txt")), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 {
			sums[fields[1]] = fields[0]
		}
	}
	for _, pl := range publishPlatforms {
		rel := "dist/obor-1.2.4-" + pl
		want, ok := sums[rel]
		if !ok {
			t.Fatalf("precondition: the tag's catalog has no line for %s", rel)
		}
		if got := fileSum(t, filepath.Join(f.Root, rel)); got != want {
			t.Errorf("%s is sha256 %s, not the tag's %s", rel, got, want)
		}
	}

	// ship on staging is no longer refused: it pushes main up to the release commit and the tag, publishes and installs.
	out, failed = f.make(t, "ship")
	if failed {
		t.Fatalf("make ship on staging failed:\n%s", out)
	}
	for _, want := range []string{"pushed main up to ", " (checksums: v1.2.4) and v1.2.4", "published v1.2.4",
		"installed the kit"} {
		if !strings.Contains(out, want) {
			t.Errorf("ship on staging does not say %q:\n%s", want, out)
		}
	}
	if got := f.Git(f.origin, "rev-parse", "refs/heads/main"); got != head {
		t.Errorf("origin's main = %s, want the release commit %s", got, head)
	}
	if got, want := f.Git(f.origin, "rev-parse", "refs/tags/v1.2.4"), f.Git(f.Root, "rev-parse", "refs/tags/v1.2.4"); got != want {
		t.Errorf("origin's v1.2.4 = %s, want the local tag %s", got, want)
	}
	if n := strings.Count(readFile(t, f.ghlog), "release create"); n != 1 {
		t.Errorf("ship created the GitHub release %d times, want once", n)
	}
	if n := strings.Count(readFile(t, f.instlog), "\n"); n != 1 {
		t.Errorf("install.sh ran %d times, want once", n)
	}

	// A re-run of each is a no-op: no commit, no go, no moved main, no second publish.
	goCalls := strings.Count(readFile(t, f.log), "\n")
	out, failed = f.release(t, "V=1.2.4")
	if failed {
		t.Fatalf("the release re-run failed:\n%s", out)
	}
	for _, want := range []string{"commit skipped", "build and tag skipped", "copy-back commit skipped", "main move skipped"} {
		if !strings.Contains(out, want) {
			t.Errorf("the release re-run does not say %q:\n%s", want, out)
		}
	}
	if got := f.Git(f.Root, "rev-parse", "HEAD"); got != head {
		t.Errorf("the release re-run committed: HEAD %s, was %s", got, head)
	}
	if got := f.Git(f.Root, "rev-parse", "refs/heads/main"); got != head {
		t.Errorf("the release re-run moved main to %s, want %s", got, head)
	}
	if n := strings.Count(readFile(t, f.log), "\n"); n != goCalls {
		t.Errorf("the release re-run ran go %d more times, want the check skipped", n-goCalls)
	}
	if got := f.Git(f.Root, "symbolic-ref", "--short", "HEAD"); got != "staging" {
		t.Errorf("the release re-run left the checkout on %q, want staging", got)
	}
	creates, instBefore := strings.Count(readFile(t, f.ghlog), "release create"), readFile(t, f.instlog)
	out, failed = f.make(t, "ship")
	if failed {
		t.Fatalf("the ship re-run failed:\n%s", out)
	}
	for _, want := range []string{"push skipped", "publish skipped", "install skipped"} {
		if !strings.Contains(out, want) {
			t.Errorf("the ship re-run does not say %q:\n%s", want, out)
		}
	}
	if got := strings.Count(readFile(t, f.ghlog), "release create"); got != creates {
		t.Errorf("the ship re-run published again: %d release create calls, want %d", got, creates)
	}
	if got := readFile(t, f.instlog); got != instBefore {
		t.Errorf("the ship re-run installed again: %q", got)
	}
	if got := f.Git(f.origin, "rev-parse", "refs/heads/main"); got != head {
		t.Errorf("the ship re-run moved origin's main to %s, want %s", got, head)
	}
}

// T-1035: main moves by release, never by a checkout, and a release never rewinds it: a main with commits the release
// lacks (a promotion moved it past staging's line) stops the release at its last step with main where it was.
func TestReleaseRefusesToRewindMain(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t)
	f.Git(f.Root, "checkout", "-q", "-b", "staging")
	// main moves ahead on its own line, as a promotion moves it: a merge staging does not have.
	f.Git(f.Root, "checkout", "-q", "main")
	f.merge(t, "topic/mainline", "Merge T-9: main-only work", map[string]string{"internal/mainline.go": "package internal\n"})
	onMain := f.Git(f.Root, "rev-parse", "refs/heads/main")
	f.Git(f.Root, "checkout", "-q", "staging")

	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, "has commits the release (") || !strings.Contains(out, "a release never rewinds main") {
		t.Fatalf("a main past the release must stop it, naming the rule (failed %v):\n%s", failed, out)
	}
	if got := f.Git(f.Root, "rev-parse", "refs/heads/main"); got != onMain {
		t.Errorf("the refused release moved main from %s to %s", onMain, got)
	}
	// The release got as far as its last step: the tag and the catalog commit are made (a re-run fails the same way).
	if !f.hasTag("v1.2.4") {
		t.Error("precondition: the release did not reach the main step (no tag)")
	}
	if got := f.Git(f.Root, "log", "-1", "--format=%s"); got != "checksums: v1.2.4" {
		t.Errorf("staging's tip is %q, want the catalog commit", got)
	}
	f.clean(t, "after the refused main move")
	if f.Exists(f.scratch("1.2.4")) {
		t.Error("the refused release left the scratch worktree")
	}
}
