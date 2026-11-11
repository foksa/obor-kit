package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"obor/internal/checks"
	"obor/internal/help"
	"obor/internal/testkit"
)

// flakeLine is one .obor/state/flakes.jsonl line.
func flakeLine(t *testing.T, at int64, test, task string, saved bool) string {
	t.Helper()
	b, err := json.Marshal(checks.Flake{Time: at, Test: test, Check: "check", Task: task, SHA: "abc1234",
		Retried: saved, PassedOnRetry: saved})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// obor flakes prints exactly the flaky tests of the last 7 days (a passed_on_retry line, or red lines on two
// tasks), one line each with the tasks and the newest time; none → "no flaky tests (7d)"; read-only, no arguments.
func TestFlakesCommand(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	now := time.Now().Unix()
	day := int64(24 * 60 * 60)
	p.Write(".obor/state/flakes.jsonl", strings.Join([]string{
		flakeLine(t, now-day, "TestTwo", "T-01", false),    // two tasks in 7 days → flaky
		flakeLine(t, now-2*day, "TestTwo", "T-02", false),  // (the older one: the newest time is the other's)
		flakeLine(t, now-day, "TestOnce", "T-01", false),   // one task, no retry → not flaky
		flakeLine(t, now-8*day, "TestOld", "T-01", false),  // older than 7 days → not flaky
		flakeLine(t, now-day, "TestRetried", "T-03", true), // passed on a rerun (release's) → flaky
	}, "\n")+"\n")

	rc, stdout, stderr := run(t, p.Root, nil, "flakes")
	if rc != 0 || stderr != "" {
		t.Fatalf("flakes: exit %d, stderr %q", rc, stderr)
	}
	got := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	want := []string{
		`^FLAKY TestRetried ×1 \(7d\) tasks: T-03 last: .+$`,
		`^FLAKY TestTwo ×2 \(7d\) tasks: T-01,T-02 last: .+$`,
	}
	if len(got) != len(want) {
		t.Fatalf("flakes lines %d, want %d:\n%s", len(got), len(want), stdout)
	}
	for i := range want {
		if !regexp.MustCompile(want[i]).MatchString(got[i]) {
			t.Errorf("line %d = %q, want /%s/", i, got[i], want[i])
		}
	}
	for _, not := range []string{"TestOnce", "TestOld"} {
		if strings.Contains(stdout, not) {
			t.Errorf("%s in the output:\n%s", not, stdout)
		}
	}

	q := testkit.NewPlain(t)
	q.Config()
	if rc, stdout, stderr := run(t, q.Root, nil, "flakes"); rc != 0 || stdout != "no flaky tests (7d)\n" || stderr != "" {
		t.Errorf("no log: exit %d, %q, %q; want 0, no flaky tests (7d)", rc, stdout, stderr)
	}
	if rc, stdout, _ := run(t, p.Root, nil, "flakes", "-h"); rc != 0 || stdout != help.Usage("flakes")+"\n" {
		t.Errorf("flakes -h: exit %d, %q", rc, stdout)
	}
	if rc, _, stderr := run(t, p.Root, nil, "flakes", "x"); rc != 2 || !strings.Contains(stderr, help.Usage("flakes")) {
		t.Errorf("flakes x: exit %d, %q; want 2, the usage", rc, stderr)
	}
}
