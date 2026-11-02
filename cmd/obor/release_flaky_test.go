package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Tests for T-717: scripts/release keeps its own full log and reruns the failed go test packages of a red check once.

// redPackage is go test's output for one failed test in one package.
const redPackage = "--- FAIL: TestRace (0.01s)\n    race_test.go:9: timing\nFAIL\nFAIL\tobor/internal/racy\t0.02s\nFAIL\n"

func (f *releaseFixture) goTests() int {
	n := 0
	for _, l := range strings.Split(readFileOr(f.log), "\n") {
		if strings.HasPrefix(l, "test ") {
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
	if !regexp.MustCompile(`(?m)^test (-p [0-9]+ )?obor/internal/racy$`).MatchString(readFileOr(f.log)) {
		t.Errorf("the rerun is not `go test obor/internal/racy` with nothing else:\n%s", readFileOr(f.log))
	}
	flakes := f.Read(".obor/state/flakes.jsonl")
	for _, want := range []string{`"test":"TestRace"`, `"passed_on_retry":true`, `"retried":true`, `"task":"release"`} {
		if !strings.Contains(flakes, want) {
			t.Errorf("flake record lacks %s: %q", want, flakes)
		}
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
	// A re-run appends: the first run's text stays.
	if err := os.Remove(filepath.Join(f.Base, "go-fail")); err != nil {
		t.Fatal(err)
	}
	if out, failed := f.release(t, "V=1.2.4"); failed {
		t.Fatalf("resume failed:\n%s", out)
	}
	if again := f.Read(logRel); !strings.Contains(again, "--- FAIL: TestRace") || !strings.Contains(again, "next (owner): make ship") {
		t.Errorf("the re-run did not append to the log:\n%s", again)
	}
}
