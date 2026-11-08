//go:build release

package release

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Tests for T-772: `make release` runs `make upgrade-check` (tests/upgrade) before the tag; a red check stops the
// release with nothing tagged, and SKIP_UPGRADE_CHECK="<why>" (the owner's call) skips it into the tag message.

// upgradeRun is the fake go's log line of the upgrade check's go test ("" when it did not run).
func (f *releaseFixture) upgradeRun(t *testing.T) string {
	t.Helper()
	for _, l := range strings.Split(readFileOr(f.log), "\n") {
		if strings.HasPrefix(l, "test ") && strings.Contains(l, "./tests/upgrade") {
			return l
		}
	}
	return ""
}

// The release runs the upgrade check once, after the full check and before the release build, as the release's
// TestUpgradeCheck (OBOR_UPGRADE_CHECK=1 reaches it).
func TestMakeReleaseRunsUpgradeCheckBeforeTag(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	// The fake go records the environment's OBOR_UPGRADE_CHECK with its argv.
	stub := readFile(t, filepath.Join(f.Root, "stub", "go"))
	writeExec(t, filepath.Join(f.Root, "stub", "go"), strings.Replace(stub, `echo "$*" >>`,
		`[ "$1" != test ] || echo "upgrade env: ${OBOR_UPGRADE_CHECK:-}" >> "`+f.log+`"; echo "$*" >>`, 1))
	f.Git(f.Root, "commit", "-q", "-am", "a go that records OBOR_UPGRADE_CHECK") // a release starts from a clean tree
	out, failed := f.release(t, "V=1.2.4")
	if failed {
		t.Fatalf("make release V=1.2.4 failed:\n%s", out)
	}
	if !strings.Contains(out, "release: running make upgrade-check") || !strings.Contains(out, "upgrade check: OK") {
		t.Errorf("the release does not say it ran the upgrade check:\n%s", out)
	}
	run := f.upgradeRun(t)
	if run != "test -count=1 -tags integration -run ^TestUpgradeCheck$ ./tests/upgrade" {
		t.Fatalf("the upgrade check's go test = %q, want TestUpgradeCheck of ./tests/upgrade once:\n%s", run, readFileOr(f.log))
	}
	log := readFileOr(f.log)
	check, upgrade, build := regexp.MustCompile(`(?m)^test .*./...$`).FindStringIndex(log), strings.Index(log, run), strings.Index(log, "dist/obor-1.2.4-")
	if check == nil || build < 0 || !(check[0] < upgrade && upgrade < build) {
		t.Errorf("want the full check, then the upgrade check, then the release build (at %d, %d, %d):\n%s", check, upgrade, build, log)
	}
	if !strings.Contains(log, "upgrade env: 1\n"+run) {
		t.Errorf("the upgrade check's go test ran without OBOR_UPGRADE_CHECK=1 (TestUpgradeCheck skips):\n%s", log)
	}
	if strings.Count(log, "./tests/upgrade") != 1 {
		t.Errorf("the upgrade check ran more than once:\n%s", log)
	}
}

// A red upgrade check stops the release before the tag, with the check's step and output and the way out; the bump
// stays committed, and the re-run (green) starts at the upgrade check and tags.
func TestMakeReleaseUpgradeCheckFailsAndResumes(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	f.Write(filepath.Join(f.Base, "upgrade-fail"), "FAIL upgrade check v1.2.3 → v1.2.4: step 3: serve of alpha on the new binary is not up\n")
	out, failed := f.release(t, "V=1.2.4")
	if !failed {
		t.Fatalf("a red upgrade check must stop the release:\n%s", out)
	}
	for _, want := range []string{"step 3: serve of alpha on the new binary is not up",
		"FAIL make upgrade-check failed (its step and output above): nothing is tagged",
		`SKIP_UPGRADE_CHECK="<why>" make release V=1.2.4 (the owner's call only)`} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal lacks %q:\n%s", want, out)
		}
	}
	if f.hasTag("v1.2.4") || f.Exists(f.scratch("1.2.4")) {
		t.Error("a red upgrade check made the tag or the scratch worktree")
	}
	if got := f.Git(f.Root, "log", "--format=%s", "-1"); got != "obor v1.2.4: VERSION, plugin version" {
		t.Errorf("main's last commit = %q, want the version commit (the check passed)", got)
	}
	if strings.Contains(readFileOr(f.log), "dist/obor-1.2.4-") {
		t.Error("a red upgrade check still built the release binaries")
	}
	if got := f.Git(f.Root, "status", "--porcelain"); got != "" {
		t.Errorf("a red upgrade check left the tree dirty: %q", got)
	}

	if err := os.Remove(filepath.Join(f.Base, "upgrade-fail")); err != nil {
		t.Fatal(err)
	}
	out, failed = f.release(t, "V=1.2.4")
	if failed {
		t.Fatalf("the re-run after a green upgrade check failed:\n%s", out)
	}
	if !strings.Contains(out, "commit skipped") || !strings.Contains(out, "release: running make upgrade-check") {
		t.Errorf("the re-run does not resume at the upgrade check:\n%s", out)
	}
	if !f.hasTag("v1.2.4") {
		t.Error("the re-run did not tag v1.2.4")
	}
}

// SKIP_UPGRADE_CHECK="<why>" skips the check (a red one too): the run says so and the tag message carries the reason.
func TestMakeReleaseSkipUpgradeCheck(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	f.Write(filepath.Join(f.Base, "upgrade-fail"), "FAIL upgrade check: step 1\n")
	out, failed := f.release(t, "V=1.2.4", "SKIP_UPGRADE_CHECK=the owner: v1.2.3 is not fetchable")
	if failed {
		t.Fatalf("SKIP_UPGRADE_CHECK must let the release through:\n%s", out)
	}
	if !strings.Contains(out, "release: upgrade check skipped (SKIP_UPGRADE_CHECK: the owner: v1.2.3 is not fetchable)") {
		t.Errorf("the release does not say it skipped the upgrade check:\n%s", out)
	}
	if run := f.upgradeRun(t); run != "" {
		t.Errorf("the skipped upgrade check ran: %q", run)
	}
	msg := f.Git(f.Root, "tag", "-l", "--format=%(contents)", "v1.2.4")
	if !strings.Contains(msg, "obor v1.2.4") || !strings.Contains(msg, "Upgrade check skipped: the owner: v1.2.3 is not fetchable") {
		t.Errorf("the tag message does not carry the skip's reason: %q", msg)
	}
}

// --dry-run names the upgrade check and runs nothing.
func TestMakeReleaseDryRunNamesUpgradeCheck(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	out, failed := f.release(t, "V=1.2.4", "DRY=1")
	if failed {
		t.Fatalf("make release V=1.2.4 DRY=1 failed:\n%s", out)
	}
	if !strings.Contains(out, "release: would: make upgrade-check") {
		t.Errorf("the dry run does not name the upgrade check:\n%s", out)
	}
	if f.Exists(f.log) {
		t.Errorf("the dry run ran go: %s", readFileOr(f.log))
	}
}
