package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// obor spec's argument check ends before the command reads its directory or sends a request: specSetArgs is the form of
// spec set's arguments, specCmd's usage line the rest.
func TestSpecArguments(t *testing.T) {
	for _, c := range []struct {
		args             []string
		task, path, note string
	}{
		{[]string{"T-07", "a.md"}, "T-07", "a.md", ""},
		{[]string{"T-07", "a.md", "--note", " widen it "}, "T-07", "a.md", "widen it"},
	} {
		if task, path, note, ok := specSetArgs(c.args); !ok || task != c.task || path != c.path || note != c.note {
			t.Errorf("specSetArgs(%q) = %q, %q, %q, %v; want %q, %q, %q", c.args, task, path, note, ok, c.task, c.path, c.note)
		}
	}
	for _, args := range [][]string{{"T-07"}, {"T-07", "a.md", "--note"}, {"T-07", "a.md", "--note", " "}, {"T-07", "a.md", "x"}} {
		if _, _, _, ok := specSetArgs(args); ok {
			t.Errorf("specSetArgs(%q) is ok, want the usage", args)
		}
		if rc, stdout := callCmd(t, specCmd, append([]string{"set"}, args...)...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor spec") {
			t.Errorf("spec set %q: exit %d, %q; want the usage", args, rc, stdout)
		}
	}
	for _, args := range [][]string{nil, {"edit", "T-07"}, {"show"}, {"show", "T-07", "--x"}, {"show", "T-07", "--report", "x"},
		{"list", "x"}} {
		if rc, stdout := callCmd(t, specCmd, args...); rc != 1 || !strings.HasPrefix(stdout, "FAIL usage: obor spec") {
			t.Errorf("spec %q: exit %d, %q; want the usage", args, rc, stdout)
		}
	}
	for _, c := range []struct {
		args           []string
		task           string
		report, isForm bool
	}{
		{[]string{"T-07"}, "T-07", false, true},
		{[]string{"T-07", "--report"}, "T-07", true, true},
		{[]string{}, "", false, false},
		{[]string{"T-07", "--x"}, "", false, false},
		{[]string{"T-07", "--report", "x"}, "", false, false},
	} {
		task, report, ok := specShowArgs(c.args)
		if task != c.task || report != c.report || ok != c.isForm {
			t.Errorf("specShowArgs(%q) = %q, %v, %v; want %q, %v, %v", c.args, task, report, ok, c.task, c.report, c.isForm)
		}
	}
}

// obor spec set is one socket request to obor serve: the file's text read once, the task, the caller and its
// role (the session's: a lane's cwd is a worker's whatever OBOR_ROLE says, so serve refuses it); its reply is printed.
// A bad task id or an unreadable file asks nothing; serve down or a refusal is FAIL, exit 1.
func TestSpecSetRequest(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	const text = "# T-07 — Stored\n\n## Files you may touch\n`a.go`\n"
	p.Write(".obor/drafts/T-07.md", text)
	draft := filepath.Join(p.Root, ".obor/drafts/T-07.md")
	home, serve := startFakeServe(t, p.Root, wire.Status{}, "SET T-07 rev 1", "")
	env := []string{"OBOR_STATE_HOME=" + home, "OBOR_ROLE=lead", "OBOR_AGENT=pj-lead"}

	for _, c := range []struct {
		name, dir, role string
		agent           string // derived from the role, whatever OBOR_AGENT says
	}{
		{"the lead", p.Root, "lead", "pj-lead"},
		{"from a lane, OBOR_ROLE=lead", p.LaneDir("L-01"), "worker", "pj-l-01"},
	} {
		rc, stdout, stderr := run(t, c.dir, env, "spec", "set", "T-07", draft)
		if rc != 0 || stdout != "SET T-07 rev 1\n" {
			t.Errorf("%s: exit %d, %q (stderr %q); want the reply", c.name, rc, stdout, stderr)
		}
		calls, _ := serve.requests()
		got := calls[len(calls)-1]
		if got.CallID == "" || got.Task != "T-07" || got.Spec != text || got.Role != c.role || got.Agent != c.agent ||
			got.Root != p.Root {
			t.Errorf("%s: call %+v; want T-07's text, role %s, agent %s", c.name, got, c.role, c.agent)
		}
	}

	// The owner in a plain terminal (role none) is sent as no role.
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home}, "spec", "set", "T-07", draft); rc != 0 ||
		stdout != "SET T-07 rev 1\n" {
		t.Errorf("the owner: exit %d, %q; want the reply", rc, stdout)
	}
	if calls, _ := serve.requests(); calls[len(calls)-1].Role != "" {
		t.Errorf("the owner: call %+v; want no role", calls[len(calls)-1])
	}

	// --note is what changed: sent with the set (serve tells a working worker, and task_done's refusal names it).
	if rc, stdout, _ := run(t, p.Root, env, "spec", "set", "T-07", draft, "--note", "widen the guard"); rc != 0 ||
		stdout != "SET T-07 rev 1\n" {
		t.Errorf("--note: exit %d, %q; want the reply", rc, stdout)
	}
	if calls, _ := serve.requests(); calls[len(calls)-1].Note != "widen the guard" {
		t.Errorf("--note: call %+v; want the note", calls[len(calls)-1])
	}

	_, n := serve.requests()
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"bad task id", []string{"spec", "set", "X-1", draft}, "FAIL X-1 is no task id"},
		{"no such file", []string{"spec", "set", "T-07", filepath.Join(p.Root, "nope.md")}, "FAIL cannot read "},
		{"no path", []string{"spec", "set", "T-07"}, "FAIL usage: obor spec set"}, // the other shapes: TestSpecArguments
	} {
		if rc, stdout, _ := run(t, p.Root, env, c.args...); rc != 1 || !strings.HasPrefix(stdout, c.want) {
			t.Errorf("%s: exit %d, %q; want %q, exit 1", c.name, rc, stdout, c.want)
		}
	}
	if _, after := serve.requests(); after != n {
		t.Errorf("a refused set asked serve (%d requests, %d before)", after, n)
	}

	home2, refusing := startFakeServe(t, p.Root, wire.Status{}, "", "spec_set: the lead or the owner sets a spec, not pj-l1 (role worker)")
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + home2}, "spec", "set", "T-07", draft); rc != 1 ||
		stdout != "FAIL spec_set: the lead or the owner sets a spec, not pj-l1 (role worker)\n" {
		t.Errorf("refusal: exit %d, %q", rc, stdout)
	}
	refusing.stop()
	if rc, stdout, _ := run(t, p.Root, []string{"OBOR_STATE_HOME=" + t.TempDir(), callDown}, "spec", "set", "T-07", draft); rc != 1 ||
		!strings.HasPrefix(stdout, "FAIL obor serve is not running") {
		t.Errorf("serve down: exit %d, %q", rc, stdout)
	}
}

// obor spec show and list read the store read-only, with serve down: show prints the current rev's text as
// set, list one line per task; a task with no spec is FAIL, exit 1.
func TestSpecShowListServeDown(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	env := []string{"OBOR_STATE_HOME=" + t.TempDir()} // no serve
	if rc, stdout, _ := run(t, p.Root, env, "spec", "list"); rc != 0 || stdout != "" {
		t.Errorf("list, no specs: exit %d, %q", rc, stdout)
	}
	p.Spec("T-07", "# T-07 — First\n")
	p.Spec("T-07", "# T-07 — Widened\n\n## Files you may touch\n`a.go`\n")
	p.Spec("T-03", "no title\n")
	if rc, stdout, _ := run(t, p.Root, env, "spec", "show", "T-07"); rc != 0 ||
		stdout != "# T-07 — Widened\n\n## Files you may touch\n`a.go`\n" {
		t.Errorf("show: exit %d, %q; want the current rev", rc, stdout)
	}
	if rc, stdout, _ := run(t, p.Root, env, "spec", "show", "T-09"); rc != 1 ||
		stdout != "FAIL no spec for T-09 in obor serve's store (obor spec set T-09 <path>)\n" {
		t.Errorf("show, none: exit %d, %q", rc, stdout)
	}
	rc, stdout, _ := run(t, p.Root, env, "spec", "list")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if rc != 0 || len(lines) != 2 || !strings.HasPrefix(lines[0], "T-03 rev 1 ") ||
		!strings.HasPrefix(lines[1], "T-07 rev 2 ") || !strings.HasSuffix(lines[1], " lead Widened") {
		t.Errorf("list: exit %d, %q", rc, stdout)
	}
}

// obor spec show --report prints the newest of a task's task_done reports with its attempt and time, from the records
// the web task page reads; a task that never submitted one says so.
func TestSpecShowReport(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	env := []string{"OBOR_STATE_HOME=" + t.TempDir()} // no serve
	if rc, stdout, _ := run(t, p.Root, env, "spec", "show", "T-01", "--report"); rc != 2 ||
		stdout != "FAIL no store for T-01 at "+p.Root+"\n" {
		t.Errorf("show --report, no store: exit %d, %q; want the read failure, exit 2", rc, stdout)
	}
	p.SubmitAgo("L-01", 120, "Summary: first.")
	p.SubmitAgo("L-01", 60, "Summary: second.")
	rc, stdout, _ := run(t, p.Root, env, "spec", "show", "T-01", "--report")
	head, body, ok := strings.Cut(stdout, "\n\n")
	fields := strings.Fields(head)
	if rc != 0 || !ok || body != "Summary: second.\n" {
		t.Fatalf("show --report: exit %d, %q; want the second report", rc, stdout)
	}
	if len(fields) != 5 || fields[0] != "T-01" || fields[1] != "attempt" || fields[2] != "1" || fields[3] != "submitted" {
		t.Errorf("report head %q; want T-01 attempt 1 submitted <time>", head)
	} else if _, err := time.Parse(time.RFC3339, fields[4]); err != nil {
		t.Errorf("report time %q: %v", fields[4], err)
	}
	if rc, stdout, _ := run(t, p.Root, env, "spec", "show", "T-02", "--report"); rc != 0 ||
		stdout != "T-02 report: none yet\n" {
		t.Errorf("show --report, none: exit %d, %q; want the none-yet line", rc, stdout)
	}
}

// obor spec show --report says a task's report was pruned only when its end is older than the log's keep: merged past
// it names the commit and the git command that may carry the Summary, dropped past it its date, a merge outside obor
// serve names no commit. An end inside the keep with no submission says none with that end; a task still open with
// none says none yet.
func TestSpecShowReportPruned(t *testing.T) {
	t.Parallel()
	p := servedProject(t)
	env := []string{"OBOR_STATE_HOME=" + t.TempDir()} // no serve
	now := time.Now()
	const day = 24 * time.Hour
	on := func(d time.Duration) string { return now.UTC().Add(-d).Format("2006-01-02") }
	p.Dispatched("L-01")
	p.MergedAt("T-01", "abcdef0123456789", false, now.Add(-40*day).Unix())
	p.MergedAt("T-05", "1234567890abcdef", false, now.Add(-5*day).Unix())
	p.PlainLane("L-02", "task/T-02")
	p.Dispatched("L-02")
	db, err := store.OpenRW(store.File(p.Root))
	if err != nil {
		t.Fatal(err)
	}
	log, _, err := event.OpenLog(db)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	for _, c := range []struct {
		rec event.Record
		at  time.Time
	}{
		{event.Record{Task: "T-03", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact,
			Data: []byte(`{"rev":1,"reason":"not needed"}`)}, now.Add(-7 * day)},
		{event.Record{Task: "T-04", Attempt: 1, Step: event.StepMerged, Kind: event.KindFact,
			Data: []byte(`{"outside":true}`)}, now.Add(-40 * day)},
		{event.Record{Task: "T-06", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact,
			Data: []byte(`{"rev":1,"reason":"not needed"}`)}, now.Add(-40 * day)},
	} {
		if _, err := log.Append(c.rec, c.at); err != nil {
			t.Fatal(err)
		}
	}
	log.Close()
	for _, c := range []struct{ task, want string }{
		{"T-01", "T-01 report: pruned (merged abcdef0 on " + on(40*day) + "; the log keeps 30 days): " +
			"git log -1 --format=%B abcdef0 may carry its Summary\n"},
		{"T-02", "T-02 report: none yet\n"},
		{"T-03", "T-03 report: none (dropped on " + on(7*day) + " before a report)\n"},
		{"T-04", "T-04 report: pruned (merged outside obor serve on " + on(40*day) + "; the log keeps 30 days)\n"},
		{"T-05", "T-05 report: none (merged 1234567 on " + on(5*day) + " with no report)\n"},
		{"T-06", "T-06 report: pruned (dropped on " + on(40*day) + "; the log keeps 30 days)\n"},
	} {
		if rc, stdout, _ := run(t, p.Root, env, "spec", "show", c.task, "--report"); rc != 0 || stdout != c.want {
			t.Errorf("%s: exit %d, %q; want %q", c.task, rc, stdout, c.want)
		}
	}
}
