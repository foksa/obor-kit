//go:build release

package release

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// scripts/release keeps its own full log and reruns the failed go test packages of a red check once.

// redPackage is go test's output for one failed test in one package.
const redPackage = "--- FAIL: TestRace (0.01s)\n    race_test.go:9: timing\nFAIL\nFAIL\tobor/internal/racy\t0.02s\nFAIL\n"

// failInLog is go test's output for a red test whose own log prints a FAIL line of its own (a hanging test's message
// "FAIL herdr agent start …" inside the package's output), beside go test's package line.
const failInLog = "--- FAIL: TestHerder (60.01s)\n" +
	"    herdr_test.go:42: the agent did not start\n" +
	"             FAIL herdr agent start pj-l-01 failed (pane w1:p1)\n" +
	"FAIL\n" +
	"FAIL\tobor/internal/setup\t15s\n" +
	"FAIL\n"

// goTests is how many go tests the check and its rerun ran (the upgrade check's and check-release's
// go test are not ones of them).
func (f *releaseFixture) goTests() int {
	n := 0
	for _, l := range strings.Split(readFileOr(f.log), "\n") {
		if strings.HasPrefix(l, "test ") && !strings.Contains(l, "./tests/upgrade") && !strings.Contains(l, "./tests/release") {
			n++
		}
	}
	return n
}

func readFileOr(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func TestMakeReleaseFlakyPackagePassesOnRerun(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	f.Write(filepath.Join(f.Base, "go-fail-once"), redPackage)
	out, failed := f.release(t, "V=1.2.4")
	if failed {
		t.Fatalf("a package that passes on rerun must not stop the release:\n%s", out)
	}
	if !strings.Contains(out, "FLAKY: obor/internal/racy TestRace (failed, passed on rerun)") {
		t.Errorf("no FLAKY line:\n%s", out)
	}
	if !f.hasTag("v1.2.4") {
		t.Error("the release did not go on to the tag")
	}
	// The check ran go test once, and the rerun ran exactly the failed package, without -count.
	if n := f.goTests(); n != 2 {
		t.Errorf("go test ran %d times, want 2 (the check, the rerun):\n%s", n, readFileOr(f.log))
	}
	if !regexp.MustCompile(`(?m)^test -tags integration (-p [0-9]+ )?obor/internal/racy$`).MatchString(readFileOr(f.log)) {
		t.Errorf("the rerun is not `go test -tags integration obor/internal/racy` with nothing else:\n%s", readFileOr(f.log))
	}
	flakes := f.Read(".obor/state/flakes.jsonl")
	for _, want := range []string{`"test":"TestRace"`, `"passed_on_retry":true`, `"retried":true`, `"task":"release"`} {
		if !strings.Contains(flakes, want) {
			t.Errorf("flake record lacks %s: %q", want, flakes)
		}
	}
}

// Only go test's own package line names a package to rerun: the FAIL text of a test's own log is no package, so the
// rerun asks go test for obor/internal/setup alone and the flake is that package's test.
func TestMakeReleaseRerunIgnoresFailTextInTestLog(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	f.Write(filepath.Join(f.Base, "go-fail-once"), failInLog)
	out, failed := f.release(t, "V=1.2.4")
	if failed {
		t.Fatalf("a package that passes on rerun must not stop the release:\n%s", out)
	}
	if !regexp.MustCompile(`(?m)^test -tags integration (-p [0-9]+ )?obor/internal/setup$`).MatchString(readFileOr(f.log)) {
		t.Errorf("the rerun is not `go test -tags integration obor/internal/setup` with nothing else:\n%s", readFileOr(f.log))
	}
	if !strings.Contains(out, "FLAKY: obor/internal/setup TestHerder (failed, passed on rerun)") {
		t.Errorf("the flake is not obor/internal/setup's TestHerder:\n%s", out)
	}
}

func TestMakeReleaseFailingTwiceIsRefused(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	f.Write(filepath.Join(f.Base, "go-fail"), redPackage)
	head := f.Git(f.Root, "rev-parse", "HEAD")
	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, "failed again on rerun") || !strings.Contains(out, "obor/internal/racy TestRace") {
		t.Fatalf("a package failing twice must be refused naming the test (failed %v):\n%s", failed, out)
	}
	if strings.Contains(out, "FLAKY:") {
		t.Errorf("a package that failed twice is no flake:\n%s", out)
	}
	if n := f.goTests(); n != 2 {
		t.Errorf("go test ran %d times, want exactly one rerun", n)
	}
	if got := f.Git(f.Root, "rev-parse", "HEAD"); got != head || f.hasTag("v1.2.4") {
		t.Errorf("a refused release committed or tagged (HEAD %s)", got)
	}
	if got := f.Git(f.Root, "status", "--porcelain"); got != "" {
		t.Errorf("tree dirty after the refusal: %q", got)
	}
	if f.Exists(".obor/state/flakes.jsonl") {
		t.Errorf("a refusal recorded a flake: %s", f.Read(".obor/state/flakes.jsonl"))
	}
}

func TestMakeReleaseVetFailureIsRefusedWithoutRerun(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	f.Write(filepath.Join(f.Base, "go-vet-fail"), "")
	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, "not in go test, so no rerun") || !strings.Contains(out, "FAIL go vet") {
		t.Fatalf("a vet failure must refuse at once (failed %v):\n%s", failed, out)
	}
	if n := f.goTests(); n != 0 {
		t.Errorf("go test ran %d times after a vet failure, want 0", n)
	}
	if f.hasTag("v1.2.4") {
		t.Error("tagged after a vet failure")
	}
}

// a refused release keeps its full log (its FAIL lines name it); a release that succeeds removes it.
func TestMakeReleaseKeepsItsFullLog(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	f.Write(filepath.Join(f.Base, "go-fail"), redPackage)
	out, failed := f.release(t, "V=1.2.4")
	if !failed {
		t.Fatalf("expected a refusal:\n%s", out)
	}
	const logRel = ".obor/state/logs/release-1.2.4.log"
	if !f.Exists(logRel) {
		t.Fatalf("no %s", logRel)
	}
	log := f.Read(logRel)
	for _, want := range []string{"--- FAIL: TestRace", "race_test.go:9: timing", "rerunning once", "failed again on rerun"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %q:\n%s", want, log)
		}
	}
	if !strings.Contains(out, "(full log: "+logRel+")") {
		t.Errorf("the FAIL line does not name the log:\n%s", out)
	}
	// The re-run appends while it goes, then removes the log: a release that succeeds has nothing left to read.
	if err := os.Remove(filepath.Join(f.Base, "go-fail")); err != nil {
		t.Fatal(err)
	}
	if out, failed := f.release(t, "V=1.2.4"); failed {
		t.Fatalf("resume failed:\n%s", out)
	}
	if f.Exists(logRel) {
		t.Errorf("a successful release left its log:\n%s", f.Read(logRel))
	}
}
