//go:build release

package release

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Tests for T-902: `make release` runs `make check-release` (tests/release) before the tag; a red check stops the
// release with nothing tagged, and the re-run (green) starts at that check.

// checkReleaseTest is check-release's go test line: the release build tag (T-1001), the integration tag (T-1014) and
// CHECK_CPUS (-p, the gate exports it), nothing else, so a fixture run inherits them.
var checkReleaseTest = regexp.MustCompile(`^test -count=1 -tags release,integration (-p [0-9]+ )?\./tests/release$`)

// releaseCheckRun is the fake go's log line of check-release's go test ("" when it did not run).
func (f *releaseFixture) releaseCheckRun(t *testing.T) string {
	t.Helper()
	for _, l := range strings.Split(readFileOr(f.log), "\n") {
		if strings.HasPrefix(l, "test ") && strings.Contains(l, "./tests/release") {
			return l
		}
	}
	return ""
}

// The release runs check-release once, after the upgrade check and before the release build.
func TestMakeReleaseRunsCheckReleaseBeforeTag(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	out, failed := f.release(t, "V=1.2.4")
	if failed {
		t.Fatalf("make release V=1.2.4 failed:\n%s", out)
	}
	if !strings.Contains(out, "release: running make check-release (the kit's release tooling tests: the Makefile, scripts/ and install.sh)") {
		t.Errorf("the release does not say it ran check-release:\n%s", out)
	}
	run := f.releaseCheckRun(t)
	if !checkReleaseTest.MatchString(run) {
		t.Fatalf("check-release's go test = %q, want ./tests/release once:\n%s", run, readFileOr(f.log))
	}
	log := readFileOr(f.log)
	upgrade, check, build := strings.Index(log, "./tests/upgrade"), strings.Index(log, run), strings.Index(log, "dist/obor-1.2.4-")
	if check < 0 || upgrade < 0 || build < 0 || !(upgrade < check && check < build) {
		t.Errorf("want the upgrade check, then check-release, then the release build (at %d, %d, %d):\n%s",
			upgrade, check, build, log)
	}
	if n := strings.Count(log, "./tests/release"); n != 1 {
		t.Errorf("check-release's go test ran %d times, want once:\n%s", n, log)
	}
	if !f.hasTag("v1.2.4") {
		t.Error("the release did not go on to the tag")
	}
}

// A red check-release stops the release before the tag, with the check's output; the bump stays committed, and the
// re-run (green) starts at that check and tags. Nothing is tagged and the tree is left clean.
func TestMakeReleaseCheckReleaseFailsAndResumes(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	f.Write(filepath.Join(f.Base, "check-release-fail"), "--- FAIL: TestMakeReleaseFullRun\nFAIL\nFAIL\tobor/tests/release\t0.02s\nFAIL\n")
	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, "FAIL make check-release failed") || !strings.Contains(out, "nothing is tagged") {
		t.Fatalf("a red check-release must stop the release before the tag (failed %v):\n%s", failed, out)
	}
	if !strings.Contains(out, "--- FAIL: TestMakeReleaseFullRun") {
		t.Errorf("the refusal does not show the check's output:\n%s", out)
	}
	if f.hasTag("v1.2.4") {
		t.Error("a red check-release made the tag")
	}
	if got := f.Exists(f.scratch("1.2.4")); got {
		t.Error("a red check-release made the scratch worktree")
	}
	if got := f.Git(f.Root, "log", "--format=%s", "-1"); got != "obor v1.2.4: VERSION, plugin version" {
		t.Errorf("the bump must stay committed (last commit %q)", got)
	}
	if got := f.Git(f.Root, "status", "--porcelain"); got != "" {
		t.Errorf("a red check-release left the tree dirty: %q", got)
	}

	if err := os.Remove(filepath.Join(f.Base, "check-release-fail")); err != nil {
		t.Fatal(err)
	}
	out, failed = f.release(t, "V=1.2.4")
	if failed {
		t.Fatalf("the re-run after a green check-release failed:\n%s", out)
	}
	if !strings.Contains(out, "commit skipped") || !strings.Contains(out, "release: running make check-release") {
		t.Errorf("the re-run does not resume at check-release:\n%s", out)
	}
	if !f.hasTag("v1.2.4") {
		t.Error("the re-run did not tag v1.2.4")
	}
}

// --dry-run names check-release and runs nothing.
func TestMakeReleaseDryRunNamesCheckRelease(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	out, failed := f.release(t, "V=1.2.4", "DRY=1")
	if failed {
		t.Fatalf("make release V=1.2.4 DRY=1 failed:\n%s", out)
	}
	if !strings.Contains(out, "release: would: make check-release") {
		t.Errorf("the dry run does not name check-release:\n%s", out)
	}
	if f.Exists(f.log) {
		t.Errorf("the dry run ran go: %s", readFileOr(f.log))
	}
}
