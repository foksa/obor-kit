package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for T-643 (RV-70): each step of scripts/release and scripts/ship has explicit done evidence and an explicit undo
// on failure. One test per row of the spec's step table plus each RV-70 finding's failure; the rows already covered by
// release_test.go (bump and check: TestMakeReleaseFullRun, TestMakeReleaseCheckFailsAndResumes; review guard:
// TestMakeReleaseRefusesUnreviewed; ship's happy path: TestMakeShip) are named there.

// hook installs a git hook in the fixture's repo (the scratch worktree shares it) that rejects while marker exists and,
// with match set, only a commit whose message has that prefix (a commit-msg hook gets the message file as $1).
func (f *releaseFixture) hook(t *testing.T, name, marker, match string) {
	t.Helper()
	script := "#!/bin/sh\n[ -f \"" + marker + "\" ] || exit 0\n"
	if match != "" {
		script += "head -n 1 \"$1\" | grep -q '^" + match + "' || exit 0\n"
	}
	script += "echo \"" + name + ": rejected\" >&2\nexit 1\n"
	writeExec(t, filepath.Join(f.Root, ".git", "hooks", name), script)
}

// clean asserts main's working tree and index are clean (the next run's precondition).
func (f *releaseFixture) clean(t *testing.T, when string) {
	t.Helper()
	if got := f.Git(f.Root, "status", "--porcelain"); got != "" {
		t.Errorf("%s: main is not clean: %q", when, got)
	}
}

// Review guard row: the baseline is the newest "RV-n closed" on main's own (first-parent) line. A side branch that
// brings in a commit with that subject does not move it, so the code merged after the real close still counts.
func TestReleaseGuardIgnoresSideBranchClose(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	f.closeReview(t, 1, f.headShort(t))
	f.merge(t, "topic/a", "Merge T-5: code", map[string]string{"internal/a.go": "package internal\n"})
	// A side branch whose own commit is "RV-9 closed", dated after everything else, merged as docs only.
	f.Git(f.Root, "checkout", "-q", "-b", "topic/side")
	writeFile(t, filepath.Join(f.Root, "docs", "side.md"), "side\n")
	f.Git(f.Root, "add", "-A")
	f.GitEnv([]string{"GIT_COMMITTER_DATE=2099-01-01T00:00:00Z", "GIT_AUTHOR_DATE=2099-01-01T00:00:00Z"},
		f.Root, "commit", "-q", "-m", "RV-9 closed · abc1234 · approved")
	f.Git(f.Root, "checkout", "-q", "main")
	f.Git(f.Root, "merge", "-q", "--no-ff", "-m", "Merge T-6: docs", "topic/side")
	// Precondition: without --first-parent the newest close subject is the side branch's (else this is vacuous).
	if got := f.Git(f.Root, "log", "--format=%s", "--grep=^RV-[0-9]* closed", "-1"); !strings.HasPrefix(got, "RV-9 closed") {
		t.Fatalf("precondition: the newest close in full history is %q, want the side branch's RV-9", got)
	}
	head := f.Git(f.Root, "rev-parse", "HEAD")
	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, "FAIL unreviewed since RV-1: T-5 (") {
		t.Fatalf("a side-branch close must not move the baseline past T-5 (failed %v):\n%s", failed, out)
	}
	f.untouched(t, "1.2.4", head)
}

// Bump row, RV-70 #2: the version commit rejected by a pre-commit hook puts both paths' index and tree back to HEAD
// (git checkout -- restored from the index, which held the bump), so the re-run starts clean and finishes.
func TestReleaseBumpRejectedCommitResumes(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	marker := filepath.Join(f.Base, "reject")
	f.Write(marker, "")
	f.hook(t, "pre-commit", marker, "")
	head := f.Git(f.Root, "rev-parse", "HEAD")

	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, "cannot commit the version bump on main") {
		t.Fatalf("a rejected version commit must stop the release (failed %v):\n%s", failed, out)
	}
	if !strings.Contains(out, "pre-commit: rejected") {
		t.Fatalf("precondition: the hook did not run:\n%s", out)
	}
	if got := f.Git(f.Root, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
	f.clean(t, "after the rejected version commit")
	if got := strings.TrimSpace(f.Read("VERSION")); got != "1.2.3" {
		t.Errorf("VERSION is %q, want 1.2.3 restored", got)
	}
	if f.hasTag("v1.2.4") {
		t.Error("a rejected version commit made the tag")
	}

	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	out, failed = f.release(t, "V=1.2.4")
	if failed {
		t.Fatalf("the re-run after the hook passes failed:\n%s", out)
	}
	if got := f.Git(f.Root, "log", "--format=%s", "-2"); got != "checksums: v1.2.4\nobor v1.2.4: VERSION, plugin version" {
		t.Errorf("the re-run's commits = %q", got)
	}
	f.clean(t, "after the re-run")
}

// Bump row: VERSION and the plugin version at HEAD alone are not the done evidence; without the "obor v<V>: …" commit
// on main the release refuses instead of skipping the check.
func TestReleaseBumpNeedsVersionCommit(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	f.Write("VERSION", "1.2.4\n")
	f.Write(".claude-plugin/plugin.json", `{"name":"obor","version":"1.2.4"}`+"\n")
	f.Git(f.Root, "commit", "-q", "-am", "hand bump")
	head := f.Git(f.Root, "rev-parse", "HEAD")
	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, "no 'obor v1.2.4: …' commit on main") {
		t.Fatalf("a hand bump must not count as the version commit (failed %v):\n%s", failed, out)
	}
	if got := f.Git(f.Root, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
	if f.hasTag("v1.2.4") || f.Exists(f.log) {
		t.Error("the refused release tagged or ran go")
	}
}

// Tag row: a failed make release in the scratch worktree removes that worktree and tags nothing; the version commit
// stays (its evidence holds), and the re-run skips it and finishes.
func TestReleaseTagFailureRemovesScratch(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	marker := filepath.Join(f.Base, "build-fail")
	f.Write(marker, "")
	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, "make release failed in") {
		t.Fatalf("a red release build must stop the release (failed %v):\n%s", failed, out)
	}
	if got := f.Git(f.Root, "log", "--format=%s", "-1"); got != "obor v1.2.4: VERSION, plugin version" {
		t.Fatalf("precondition: the version commit is not there: %q", got)
	}
	if f.Exists(f.scratch("1.2.4")) {
		t.Error("the scratch worktree is still on disk")
	}
	if got := f.Git(f.Root, "worktree", "list", "--porcelain"); strings.Count(got, "worktree ") != 1 {
		t.Errorf("git still lists the scratch worktree:\n%s", got)
	}
	if f.hasTag("v1.2.4") {
		t.Error("a red release build made the tag")
	}
	f.clean(t, "after the red build")

	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	out, failed = f.release(t, "V=1.2.4")
	if failed {
		t.Fatalf("the re-run failed:\n%s", out)
	}
	if !strings.Contains(out, "commit skipped") || !strings.Contains(out, "tagged v1.2.4") {
		t.Errorf("the re-run must skip the bump and tag:\n%s", out)
	}
}

// copyBackFailed is a release whose copy-back commit a commit-msg hook rejected: the tag and the scratch worktree are
// there, main has only the version commit. reject is the hook's marker (still present).
func copyBackFailed(t *testing.T) (f *releaseFixture, reject, head string) {
	t.Helper()
	f = newReleaseFixture(t)
	reject = filepath.Join(f.Base, "reject")
	f.Write(reject, "")
	f.hook(t, "commit-msg", reject, "checksums:")
	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, "cannot commit bin/checksums.txt on main") {
		t.Fatalf("precondition: the copy-back commit must be rejected (failed %v):\n%s", failed, out)
	}
	if !f.hasTag("v1.2.4") || !f.Exists(f.scratch("1.2.4")) {
		t.Fatalf("precondition: the tag and the scratch worktree are there:\n%s", out)
	}
	return f, reject, f.Git(f.Root, "rev-parse", "HEAD")
}

// Copy-back row, RV-70 #3: a rejected catalog commit puts bin/checksums.txt's index and tree back to HEAD (it stayed
// staged and blocked the re-run); the re-run then makes only that commit from the scratch worktree's bytes.
func TestReleaseCopyBackRejectedCommitResumes(t *testing.T) {
	t.Parallel()
	f, reject, head := copyBackFailed(t)
	f.clean(t, "after the rejected catalog commit")
	if got := f.Git(f.Root, "log", "--format=%s", "-1"); got != "obor v1.2.4: VERSION, plugin version" {
		t.Errorf("main's last commit = %q", got)
	}
	goCalls := strings.Count(readFile(t, f.log), "\n")
	if err := os.Remove(reject); err != nil {
		t.Fatal(err)
	}
	out, failed := f.release(t, "V=1.2.4")
	if failed {
		t.Fatalf("the re-run failed:\n%s", out)
	}
	if got := f.Git(f.Root, "rev-parse", "HEAD~1"); got != head {
		t.Errorf("the re-run made more than the catalog commit (HEAD~1 %s, was HEAD %s)", got, head)
	}
	if got := f.Git(f.Root, "log", "--format=%s", "-1"); got != "checksums: v1.2.4" {
		t.Errorf("the re-run's commit = %q", got)
	}
	if n := strings.Count(readFile(t, f.log), "\n"); n != goCalls {
		t.Errorf("the re-run rebuilt (%d more go calls)", n-goCalls)
	}
	if f.Exists(f.scratch("1.2.4")) {
		t.Error("the scratch worktree is still there after the copy-back")
	}
	f.clean(t, "after the re-run")
}

// Copy-back row, RV-70 #3: all four files are checked before anything is written. A mismatched or missing file stops
// the copy-back with main's catalog untouched (it was written before the check), the missing one saying the bytes are
// gone; the tag never moves.
func TestReleaseCopyBackChecksBeforeWriting(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, f *releaseFixture)
		want    []string
	}{
		{"mismatched", func(t *testing.T, f *releaseFixture) {
			writeFile(t, filepath.Join(f.scratch("1.2.4"), "dist", "obor-1.2.4-linux-amd64"), "other bytes\n")
		}, []string{"obor-release-1.2.4/dist/obor-1.2.4-linux-amd64 is sha256", "git tag -d v1.2.4"}},
		{"missing", func(t *testing.T, f *releaseFixture) {
			for _, p := range []string{filepath.Join(f.scratch("1.2.4"), "dist", "obor-1.2.4-linux-amd64"),
				filepath.Join(f.Root, "dist", "obor-1.2.4-linux-amd64")} {
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
		}, []string{"dist/obor-1.2.4-linux-amd64 is gone", "cannot recreate identical bytes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, _, head := copyBackFailed(t)
			// A no-op after the rejected commit's own undo (TestReleaseCopyBackRejectedCommitResumes); it keeps this test
			// on the check-before-write half of #3 alone.
			f.Git(f.Root, "checkout", "-q", "HEAD", "--", "bin/checksums.txt")
			// Main's catalog before: the old one (the main-side copy, not the tag's).
			before := f.Read("bin/checksums.txt")
			if strings.Contains(before, "dist/obor-1.2.4-") {
				t.Fatalf("precondition: main's catalog already has 1.2.4's lines:\n%s", before)
			}
			tagBefore := f.Git(f.Root, "rev-parse", "v1.2.4")
			tc.prepare(t, f)
			out, failed := f.release(t, "V=1.2.4")
			if !failed {
				t.Fatalf("the copy-back must refuse:\n%s", out)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("the refusal does not say %q:\n%s", w, out)
				}
			}
			if got := f.Read("bin/checksums.txt"); got != before {
				t.Errorf("main's catalog was written before the check:\n%s", got)
			}
			f.clean(t, "after the refused copy-back")
			if got := f.Git(f.Root, "rev-parse", "HEAD"); got != head {
				t.Errorf("HEAD moved to %s", got)
			}
			if got := f.Git(f.Root, "rev-parse", "v1.2.4"); got != tagBefore {
				t.Errorf("the tag moved from %s to %s", tagBefore, got)
			}
		})
	}
}

// Tag row and ship's push, RV-70 #4: an existing tag is accepted only when its source parent and HEAD differ in docs and
// bookkeeping. Code merged after the tag (v0.17.2's shape: tagged, then T-642 merged and reviewed) is refused by
// release and by ship before it pushes; a docs-only merge is accepted by both.
func TestReleaseStaleTag(t *testing.T) {
	t.Parallel()
	t.Run("code merged after the tag", func(t *testing.T) {
		t.Parallel()
		f := newShipFixture(t)
		if out, failed := f.release(t, "V=1.2.4"); failed {
			t.Fatalf("make release V=1.2.4 failed:\n%s", out)
		}
		tagBefore := f.Git(f.Root, "rev-parse", "v1.2.4")
		f.merge(t, "topic/late", "Merge T-9: late code", map[string]string{"internal/late.go": "package internal\n"})
		f.closeReview(t, 1, f.headShort(t)) // reviewed: the review guard passes, only the tag is stale
		head := f.Git(f.Root, "rev-parse", "HEAD")

		out, failed := f.release(t, "V=1.2.4")
		if !failed || !strings.Contains(out, "FAIL v1.2.4 predates T-9: release a new patch version") {
			t.Errorf("release must refuse the stale tag (failed %v):\n%s", failed, out)
		}
		if strings.Contains(out, "unreviewed since") {
			t.Errorf("precondition: the review guard refused instead:\n%s", out)
		}
		if got := f.Git(f.Root, "rev-parse", "HEAD"); got != head {
			t.Errorf("release committed: HEAD %s", got)
		}
		if got := f.Git(f.Root, "rev-parse", "v1.2.4"); got != tagBefore {
			t.Errorf("the tag moved from %s to %s", tagBefore, got)
		}

		out, failed = f.make(t, "ship")
		if !failed || !strings.Contains(out, "FAIL v1.2.4 predates T-9: release a new patch version") {
			t.Errorf("ship must refuse the stale tag (failed %v):\n%s", failed, out)
		}
		for _, ref := range []string{"refs/heads/main", "refs/tags/v1.2.4"} {
			if exec.Command("git", "-C", f.origin, "rev-parse", "-q", "--verify", ref).Run() == nil {
				t.Errorf("%s was pushed despite the stale tag", ref)
			}
		}
		if f.Exists(f.ghlog) || f.Exists(f.instlog) {
			t.Error("ship published or installed despite the stale tag")
		}
	})
	t.Run("docs merged after the tag", func(t *testing.T) {
		t.Parallel()
		f := newShipFixture(t)
		if out, failed := f.release(t, "V=1.2.4"); failed {
			t.Fatalf("make release V=1.2.4 failed:\n%s", out)
		}
		f.merge(t, "topic/docs", "Merge T-10: docs", map[string]string{"docs/later.md": "later\n", "BACKLOG.md": "x\n"})
		out, failed := f.release(t, "V=1.2.4")
		if failed || !strings.Contains(out, "build and tag skipped") {
			t.Errorf("release must accept a docs-only merge after the tag (failed %v):\n%s", failed, out)
		}
		out, failed = f.make(t, "ship")
		if failed || !strings.Contains(out, "pushed main and v1.2.4") {
			t.Errorf("ship must accept a docs-only merge after the tag (failed %v):\n%s", failed, out)
		}
	})
}

// Ship push row: done is origin having the tag (same object) and a main that contains the local one. An origin main
// ahead of the local main (a docs commit pushed from elsewhere) is done: no push, origin's main is kept.
func TestShipPushDoneWhenOriginContainsMain(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t)
	if out, failed := f.release(t, "V=1.2.4"); failed {
		t.Fatalf("make release V=1.2.4 failed:\n%s", out)
	}
	if out, failed := f.make(t, "ship"); failed {
		t.Fatalf("make ship failed:\n%s", out)
	}
	// Another clone pushes a commit on top of main.
	clone := filepath.Join(f.Base, "clone")
	f.Git(f.Base, "clone", "-q", "-b", "main", f.origin, clone)
	writeFile(t, filepath.Join(clone, "docs", "elsewhere.md"), "x\n")
	f.Git(clone, "add", "-A")
	f.Git(clone, "commit", "-q", "-m", "docs from elsewhere")
	f.Git(clone, "push", "-q", "origin", "main")
	ahead := f.Git(f.origin, "rev-parse", "refs/heads/main")
	if ahead == f.Git(f.Root, "rev-parse", "main") {
		t.Fatal("precondition: origin's main is not ahead")
	}
	f.Git(f.Root, "fetch", "-q", "origin") // the ancestry check needs origin's main locally (local main stays behind)
	out, failed := f.make(t, "ship")
	if failed || !strings.Contains(out, "push skipped") {
		t.Errorf("ship must see origin's main as containing the local one (failed %v):\n%s", failed, out)
	}
	if got := f.Git(f.origin, "rev-parse", "refs/heads/main"); got != ahead {
		t.Errorf("origin's main moved from %s to %s", ahead, got)
	}
}

// Ship publish row, RV-70 #5: done is a release that is not a draft and has all four assets. A release with two of
// them gets only the other two, after scripts/publish's byte checks; a draft stops with the exact repair command.
func TestShipPublishEvidence(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t)
	if out, failed := f.release(t, "V=1.2.4"); failed {
		t.Fatalf("make release V=1.2.4 failed:\n%s", out)
	}
	if out, failed := f.make(t, "ship"); failed {
		t.Fatalf("make ship failed:\n%s", out)
	}
	// The release lost two assets (an interrupted upload).
	writeFile(t, filepath.Join(f.ghstate, "assets"), "obor-1.2.4-darwin-arm64\nobor-1.2.4-darwin-amd64\n")
	creates := strings.Count(readFile(t, f.ghlog), "release create")
	out, failed := f.make(t, "ship")
	if failed {
		t.Fatalf("ship with two missing assets failed:\n%s", out)
	}
	if !strings.Contains(out, "uploaded the missing assets of v1.2.4: linux-arm64 linux-amd64") {
		t.Errorf("ship does not say it uploaded the two missing assets:\n%s", out)
	}
	gh := readFile(t, f.ghlog)
	if !strings.Contains(gh, "release upload v1.2.4 dist/obor-1.2.4-linux-arm64 dist/obor-1.2.4-linux-amd64\n") {
		t.Errorf("gh was not given an upload of only the two missing files:\n%s", gh)
	}
	if strings.Contains(gh, "release upload v1.2.4 dist/obor-1.2.4-darwin") {
		t.Errorf("ship re-uploaded an asset the release had:\n%s", gh)
	}
	if n := strings.Count(gh, "release create"); n != creates {
		t.Errorf("ship created the release again (%d creates)", n)
	}

	writeFile(t, filepath.Join(f.ghstate, "draft"), "true\n")
	out, failed = f.make(t, "ship")
	if !failed || !strings.Contains(out, "is a draft: gh release edit v1.2.4 --draft=false") {
		t.Errorf("a draft release must stop ship with the repair command (failed %v):\n%s", failed, out)
	}
	if strings.Contains(out, "install") {
		t.Errorf("ship went on to install after a draft release:\n%s", out)
	}
}

// Ship install row: done is `obor version` reporting the release after install.sh; an install that does not land stops
// ship instead of claiming it installed.
func TestShipInstallProven(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t)
	if out, failed := f.release(t, "V=1.2.4"); failed {
		t.Fatalf("make release V=1.2.4 failed:\n%s", out)
	}
	f.Write(filepath.Join(f.Base, "install-broken"), "")
	out, failed := f.make(t, "ship")
	if !strings.Contains(readFile(t, f.instlog), "ran") {
		t.Fatalf("precondition: install.sh did not run:\n%s", out)
	}
	if !failed || !strings.Contains(out, "install.sh ran but obor version is 'v1.2.3', not v1.2.4") {
		t.Errorf("an install that did not land must stop ship (failed %v):\n%s", failed, out)
	}
	if strings.Contains(out, "installed the kit") {
		t.Errorf("ship claims it installed:\n%s", out)
	}
}
