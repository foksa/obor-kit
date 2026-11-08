package main

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/herdr/herdrtest"
	"obor/internal/store"
	"obor/internal/testkit"
	"obor/internal/view"
)

// statusOf is obor status of p's project with serve's answer ss (err: serve gave none), and its text at now.
func statusOf(t *testing.T, p *testkit.Project, ss wire.Status, err error, now time.Time) (view.Project, string) {
	t.Helper()
	cfg, cerr := config.Load(p.Root)
	if cerr != nil {
		t.Fatal(cerr)
	}
	db := openStore(p.Root)
	if db != nil {
		defer db.Close()
	}
	st := view.Build(view.Input{Status: ss, Err: err, Lanes: checks.NewLaneTable(context.Background(), cfg), DB: db, Now: now,
		Events: statusEvents})
	var b bytes.Buffer
	writeStatus(&b, st, now)
	return st, b.String()
}

func matchLine(t *testing.T, text, re, what string) {
	t.Helper()
	if !regexp.MustCompile("(?m)" + re).MatchString(text) {
		t.Errorf("%s: no line matching %s in\n%s", what, re, text)
	}
}

// addEvent appends one record to the project's store at at (attempt 1).
func addEvent(t *testing.T, root string, at time.Time, kind, task, step string, data any) {
	t.Helper()
	db, err := store.OpenRW(store.File(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b, _ := json.Marshal(data)
	if _, err := db.Exec(`INSERT INTO events (seq, ts, task, attempt, step, kind, class, data)
		VALUES ((SELECT COALESCE(MAX(seq), 0) + 1 FROM events), ?, ?, 1, ?, ?, '', ?)`,
		at.UTC().Format(time.RFC3339), task, step, kind, string(b)); err != nil {
		t.Fatal(err)
	}
}

// fact is a needs-you of task with data d, as serve's status lists it.
func fact(t *testing.T, seq int64, task string, d event.FactData) event.Record {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return event.Record{Seq: seq, Task: task, Step: event.StepNeedsYou, Kind: event.KindFact, Data: b}
}

// heldGateStatus is the app's status while T-1's gate is held open in its own goroutine (a running step on the task),
// with a publish on T-2 numbered publish.2 and T-3 idle: the fixture the CLI renders.
func heldGateStatus() wire.Status {
	return wire.Status{Connected: true, Project: &wire.ProjectStatus{
		Name: "pj", Root: "/work/pj",
		Tasks: []wire.TaskStatus{
			{Task: "T-1", Lane: "L-01", Attempt: 1, State: "submitted", Phase: wire.PhaseGating, Running: "gate"},
			{Task: "T-2", Lane: "L-02", Attempt: 1, State: "accepted", Phase: wire.PhaseMerging, Running: "publish.2"},
			{Task: "T-3", Lane: "L-03", Attempt: 1, State: "accepted", Phase: wire.PhasePublish},
		},
	}}
}

// taskLines returns the rendered task lines of st's project (its tasks on no configured lane), keyed by task id.
func taskLines(t *testing.T, st wire.Status) map[string]string {
	t.Helper()
	var buf bytes.Buffer
	tasks := map[string]view.Task{}
	for _, ts := range st.Project.Tasks { // what view.Build takes of serve's tasks for these lines
		tasks[ts.Task] = view.Task{Task: ts.Task, Lane: ts.Lane, Attempt: ts.Attempt, TaskState: ts.State, Open: !ts.Ended,
			Phase: ts.Phase, Running: ts.Running}
	}
	writeStatus(&buf, view.Project{Serve: view.Serve{Up: true, Herdr: true, Project: true, Merging: true},
		Tasks: tasks}, time.Now())
	lines := map[string]string{}
	for _, l := range strings.Split(buf.String(), "\n") {
		if f := strings.Fields(l); len(f) > 1 && f[0] == "task" {
			lines[strings.TrimSuffix(f[1], ":")] = l
		}
	}
	return lines
}

// T-921 (RV-124 #1): a task an older serve lists without an ended field is open and gets its task line; the new
// serve's ended: true (a dropped task) keeps it off the lanes section.
func TestStatusTaskLinesReadAnOlderServesTaskAsOpen(t *testing.T) {
	t.Parallel()
	var older wire.Status
	if err := json.Unmarshal([]byte(`{"project":{"tasks":[{"task":"T-7","lane":"L-07","attempt":1,"state":"working",`+
		`"phase":"working"}]}}`), &older); err != nil {
		t.Fatal(err)
	}
	if len(older.Project.Tasks) != 1 || older.Project.Tasks[0].Ended {
		t.Fatalf("precondition: %+v, want one task with no ended", older.Project.Tasks)
	}
	line, ok := taskLines(t, older)["T-7"]
	if !ok || !strings.HasSuffix(line, ": working (attempt 1)") {
		t.Errorf("T-7 without ended: line %q (listed %v), want an open task line", line, ok)
	}
	var ended wire.Status
	if err := json.Unmarshal([]byte(`{"project":{"tasks":[{"task":"T-7","lane":"L-07","attempt":1,"state":"dropped",`+
		`"ended":true}]}}`), &ended); err != nil {
		t.Fatal(err)
	}
	if !ended.Project.Tasks[0].Ended {
		t.Fatalf("precondition: %+v, want ended", ended.Project.Tasks[0])
	}
	if line, ok := taskLines(t, ended)["T-7"]; ok {
		t.Errorf("T-7 ended: line %q, want none", line)
	}
}

// A task line ends with the step it runs: a held gate or publish, numbered steps kept, and nothing on an idle task.
func TestStatusTaskLines(t *testing.T) {
	for _, c := range []struct {
		name, task, set, want string
		running               bool
	}{
		{"held gate", "T-1", "", "gating (submitted, attempt 1), running gate", true},
		{"publishing", "T-2", "", "merging (accepted, attempt 1), running publish.2", true},
		{"numbered gate", "T-1", "gate.3", "gating (submitted, attempt 1), running gate.3", true},
		{"idle task", "T-3", "", "", false},
	} {
		st := heldGateStatus()
		if c.set != "" {
			st.Project.Tasks[0].Running = c.set
		}
		line, ok := taskLines(t, st)[c.task]
		if !ok || line == "" {
			t.Fatalf("%s: no task line of %s in %+v", c.name, c.task, st.Project.Tasks)
		}
		if c.running && !strings.HasSuffix(line, c.want) {
			t.Errorf("%s: line = %q, want the phase, the state, then %q", c.name, line, c.want)
		}
		if !c.running && strings.Contains(line, "running") {
			t.Errorf("%s: line = %q, want no running step", c.name, line)
		}
	}
}

// T-462: a waiting task's queue line names what it waits for, no lane or attempt; DD-v0.12 A1: and why its dispatch
// is refused once all of them merged; T-538: a lane of its level; T-664: a spec stored and never dispatched.
func TestStatusShowsWaiting(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		w    view.WaitItem
		want string
	}{
		{view.WaitItem{Task: "T-4", WaitsOn: view.WaitsAfter, After: []string{"T-2", "T-3"}}, "waiting (after T-2, T-3)"},
		{view.WaitItem{Task: "T-5", WaitsOn: view.WaitsAfter, After: []string{"T-2"},
			Why: "dispatch T-5: no free lane of the types"}, "waiting (after T-2): dispatch T-5: no free lane of the types"},
		{view.WaitItem{Task: "T-6", WaitsOn: view.WaitsLane, Level: "simple", Types: []string{"deepseek", "luna"}},
			"waiting for a free lane (simple: deepseek, luna)"},
		{view.WaitItem{Task: "T-6", WaitsOn: view.WaitsLane, Level: "simple", Types: []string{"deepseek"},
			Why: "waiting for a free lane"}, "waiting for a free lane (simple: deepseek)"},
		{view.WaitItem{Task: "T-6", WaitsOn: view.WaitsLane, Level: "hard", Types: []string{"opus:high"},
			Why: "opus at its max 4"}, "waiting (hard: opus:high): opus at its max 4"},
		{view.WaitItem{Task: "T-7", WaitsOn: view.WaitsLane}, "waiting for a lane"},
		{view.WaitItem{Task: "T-8", WaitsOn: view.WaitsNever, Since: now.Add(-3 * time.Hour).Unix()},
			"spec r2 stored 3h ago, never dispatched: a title"},
	} {
		if got := waitLine(c.w, view.Task{Task: c.w.Task, SpecRev: 2, Title: "a title"}, now); got != c.want {
			t.Errorf("%s: %q, want %q", c.w.Task, got, c.want)
		}
	}
	// A waiting task is the queue's: no task line of its own in the lanes section.
	st := heldGateStatus()
	st.Project.Tasks = append(st.Project.Tasks, wire.TaskStatus{Task: "T-4", Attempt: 1, State: "waiting",
		After: []string{"T-2"}})
	if line, ok := taskLines(t, st)["T-4"]; ok {
		t.Errorf("a waiting task listed as a task line: %q", line)
	}
}

// --facts adds one line per fact in force: its seq, time, task, step{reason} and lane.
func TestStatusFactLine(t *testing.T) {
	var buf bytes.Buffer
	writeFacts(&buf, "", []event.Record{{Seq: 7, TS: "2026-10-28T10:00:00Z", Task: "T-1", Step: event.StepNeedsYou,
		Kind: event.KindFact, Data: json.RawMessage(`{"lane":"L-01","reason":"held"}`)}})
	if got := buf.String(); !strings.HasPrefix(got, "fact #7 2026-10-28T10:00:00Z T-1 ") || !strings.HasSuffix(got, "(L-01)\n") {
		t.Errorf("fact line %q", got)
	}
}

// T-596: the serve line names who runs serve: launchd (its agent's pid is serve's), a pane (any other serve), or
// nobody with no launchd agent (install it); a down serve whose agent is installed keeps the reason.
func TestStatusServeLine(t *testing.T) {
	since := time.Now().Add(-time.Hour)
	at := since.Format(time.RFC3339)
	for _, c := range []struct {
		name string
		s    view.Serve
		want string
	}{
		{"launchd", view.Serve{Up: true, Since: at, Herdr: true, Project: true, Merging: true, PID: 4242, Launchd: "running"},
			"serve: launchd pid 4242 since " + since.Local().Format("Jan 2 15:04") + " (1h)"},
		{"pane, agent installed", view.Serve{Up: true, Since: at, Herdr: true, Project: true, Merging: true, PID: 77,
			Launchd: "loaded"}, "serve: pane pid 77 since " + since.Local().Format("Jan 2 15:04") + " (1h)"},
		{"pane, no agent", view.Serve{Up: true, Since: at, Herdr: true, Project: true, Merging: true, PID: 77,
			Launchd: "none"}, "serve: pane pid 77 since " + since.Local().Format("Jan 2 15:04") + " (1h)"},
		{"down, no agent", view.Serve{Error: "obor serve is not running", Launchd: "none"},
			"serve: down (obor serve install)"},
		{"down, agent installed", view.Serve{Error: "obor serve is not running", Launchd: "loaded"},
			"serve: down (obor serve is not running)"},
	} {
		c.s.SinceTime, _ = time.Parse(time.RFC3339, c.s.Since)
		var b bytes.Buffer
		writeStatus(&b, view.Project{Serve: c.s}, time.Now())
		if got := strings.SplitN(b.String(), "\n", 2)[0]; got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// T-657: the running line right under serve's says what serve does without an agent: an open gate intent with the
// command it runs and its elapsed time, two steps at once both, a publish (git alone) and the nightly check, a pending
// self-restart, and `running: nothing` when none is open.
func TestStatusRunningLine(t *testing.T) {
	p := testkit.NewPlain(t)
	p.Config(testkit.Set("commands.check", "make -s check"), testkit.Set("commands.after_merge", "make build"),
		testkit.Set("commands.check_affected", "make -s check-affected"))
	now := time.Now().Truncate(time.Second)
	for _, c := range []struct {
		name    string
		running []wire.RunningStep
		want    string
	}{
		{"nothing open", nil, "running: nothing"},
		{"gate", []wire.RunningStep{{Step: event.StepGate, Task: "T-656", Command: "check",
			Since: now.Add(-2*time.Minute - 14*time.Second).Unix()}}, "running: gate T-656 (make -s check, 2m14s)"},
		// T-669 (RV-75 #4), T-673: a partial gate shows the class's command serve says it runs; between two commands
		// (the runner clears it as each ends) none, so the line names no command either.
		{"partial gate", []wire.RunningStep{{Step: event.StepGate, Task: "T-656", Command: "check_affected",
			Since: now.Add(-14 * time.Second).Unix()}}, "running: gate T-656 (make -s check-affected, 14s)"},
		{"gate between two commands", []wire.RunningStep{{Step: event.StepGate, Task: "T-656",
			Since: now.Add(-14 * time.Second).Unix()}}, "running: gate T-656 (14s)"},
		{"two steps at once", []wire.RunningStep{
			{Step: event.StepGate, Task: "T-656", Command: "check", Since: now.Add(-2*time.Minute - 14*time.Second).Unix()},
			{Step: event.StepAfterMerge, Task: "T-655", Since: now.Add(-12 * time.Second).Unix()}},
			"running: gate T-656 (make -s check, 2m14s) · after_merge T-655 (make build, 12s)"},
		// T-961: the slot's gate and two lanes' tests at once, each test with the class's command it runs.
		{"lane tests beside the slot", []wire.RunningStep{
			{Step: event.StepGate, Task: "T-7", Command: "compile", Since: now.Add(-3 * time.Second).Unix()},
			{Step: event.StepTest, Task: "T-5", Command: "check_affected", Since: now.Add(-2 * time.Minute).Unix()},
			{Step: event.StepTest, Task: "T-6", Command: "guard", Since: now.Add(-9 * time.Second).Unix()}},
			"running: gate T-7 (compile, 3s) · test T-5 (make -s check-affected, 2m00s) · test T-6 (guard, 9s)"},
		{"publish", []wire.RunningStep{{Step: event.StepPublish, Task: "T-4", Since: now.Add(-5 * time.Second).Unix()}},
			"running: publish T-4 (5s)"},
		{"nightly check", []wire.RunningStep{{Step: event.StepNightly, Since: now.Add(-90 * time.Second).Unix()}},
			"running: nightly check (make -s check, 1m30s)"},
		{"restarting serve", []wire.RunningStep{{Step: event.StepServeRestart, Since: now.Unix()}},
			"running: restarting serve"},
		// T-972: the restart holds nothing back: lane tests run beside it, and the line says nothing about waiting.
		{"restarting serve beside a lane test", []wire.RunningStep{
			{Step: event.StepTest, Task: "T-5", Command: "check_affected", Since: now.Add(-2 * time.Minute).Unix()},
			{Step: event.StepServeRestart, Since: now.Unix()}},
			"running: test T-5 (make -s check-affected, 2m00s) · restarting serve"},
		{"restarting serve beside two lane tests", []wire.RunningStep{
			{Step: event.StepTest, Task: "T-5", Command: "check_affected", Since: now.Add(-2 * time.Minute).Unix()},
			{Step: event.StepTest, Task: "T-6", Command: "guard", Since: now.Add(-9 * time.Second).Unix()},
			{Step: event.StepServeRestart, Since: now.Unix()}},
			"running: test T-5 (make -s check-affected, 2m00s) · test T-6 (guard, 9s) · restarting serve"},
	} {
		ss := wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "pj", Root: p.Root, Running: c.running}}
		st, text := statusOf(t, p, ss, nil, now)
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		if len(lines) < 2 || !strings.HasPrefix(lines[0], "serve: ") {
			t.Fatalf("%s: the first line is not serve's:\n%s", c.name, text)
		}
		if lines[1] != c.want {
			t.Errorf("%s: the line under serve's = %q, want %q\n%s", c.name, lines[1], c.want, text)
		}
		for _, r := range st.Running { // --json: the same command the text names
			if r.Step != event.StepServeRestart && r.Step != event.StepPublish && r.Command != "" &&
				!strings.Contains(lines[1], "("+r.Command+", ") {
				t.Errorf("%s: --json running %+v, its command not on the text line %q", c.name, r, lines[1])
			}
			if r.Step == event.StepGate && r.Command == "" && strings.Contains(lines[1], "make") {
				t.Errorf("%s: --json has no command, the text guesses one: %q", c.name, lines[1])
			}
		}
	}
}

// T-662: the pending line under the running one names the command steps whose effect does not run now: a failed step
// with the age of its last failure and its reason (its first line, short), one that never failed as queued; no line at
// all when nothing waits, and --json carries the same steps.
func TestStatusPendingLine(t *testing.T) {
	p := testkit.NewPlain(t)
	p.Config()
	now := time.Now().Truncate(time.Second)
	pending := []wire.PendingStep{
		{Step: event.StepAfterMerge, Task: "T-655", Since: now.Add(-time.Hour).Unix(),
			Error: "git I/O: index.lock exists", FailedAt: now.Add(-3 * time.Minute).Unix()},
		{Step: event.StepGate, Task: "T-656", Since: now.Add(-time.Minute).Unix()},
	}
	ss := wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "pj", Root: p.Root,
		Running: []wire.RunningStep{{Step: event.StepGate, Task: "T-654", Since: now.Add(-time.Minute).Unix()}},
		Pending: pending}}
	st, text := statusOf(t, p, ss, nil, now)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) < 3 || lines[2] != "pending: after_merge T-655 (failed 3m ago: git I/O: index.lock exists) · gate T-656 (queued)" {
		t.Errorf("the line under the running one = %q, want the two pending steps:\n%s", lines, text)
	}
	if len(st.Pending) != 2 || st.Pending[0].Error != pending[0].Error || st.Pending[1].Step != event.StepGate {
		t.Errorf("--json pending %+v, want serve's two steps", st.Pending)
	}
	// A reason keeps its first line only.
	ss.Project.Pending = []wire.PendingStep{{Step: event.StepAfterMerge, Task: "T-655", FailedAt: now.Unix(),
		Error: "FAIL T-655\nsecond line"}}
	_, text = statusOf(t, p, ss, nil, now)
	if !strings.Contains(text, "pending: after_merge T-655 (failed 0s ago: FAIL T-655)") {
		t.Errorf("a multi-line reason is not shown as its first line only:\n%s", text)
	}
	// Nothing waits: no pending line at all.
	ss.Project.Pending, ss.Project.Running = nil, nil
	if _, text = statusOf(t, p, ss, nil, now); strings.Contains(text, "pending:") {
		t.Errorf("the pending line is printed with nothing pending:\n%s", text)
	}
}

// T-657: --short is the serve line, the running line and needs-you, nothing else (a status bar).
func TestStatusShort(t *testing.T) {
	p := testkit.NewPlain(t)
	p.Config()
	now := time.Now().Truncate(time.Second)
	ss := wire.Status{Since: now.Add(-time.Hour).UTC().Format(time.RFC3339), Connected: true, Project: &wire.ProjectStatus{
		Name: "pj", Root: p.Root, Merging: true,
		Running:  []wire.RunningStep{{Step: event.StepGate, Task: "T-1", Since: now.Add(-time.Minute).Unix()}},
		Tasks:    []wire.TaskStatus{{Task: "T-2", Lane: "L-02", Attempt: 1, State: "working"}},
		Messages: []wire.MessageStatus{{To: "pj-lead", From: "other", Line: "[obor other] hi", Age: 60}},
		Facts:    []event.Record{fact(t, 1, "T-3", event.FactData{Lane: "L-03", Reason: "stalled"})},
	}}
	st, _ := statusOf(t, p, ss, nil, now)
	var b bytes.Buffer
	writeStatusShort(&b, st, now)
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("--short text:\n%s\nwant serve's line, the running line and one needs-you", b.String())
	}
	if !strings.HasPrefix(lines[0], "serve: up since ") || lines[1] != "running: gate T-1 (1m00s)" ||
		!strings.HasPrefix(lines[2], "needs-you T-3 (L-03): stalled") {
		t.Errorf("--short lines %q; want serve: up…, the running gate and needs-you T-3", lines)
	}
	for _, gone := range []string{"L-02", "task T-2", "undelivered", "full check"} {
		if strings.Contains(b.String(), gone) {
			t.Errorf("--short prints %q:\n%s", gone, b.String())
		}
	}
}

// T-672: a role agent serve stopped once its work was done, with no pane running it, is `stopped`, not missing: the
// view's role row and the status line `agent <name>: stopped`; one serve did not stop has no pane as before.
func TestStatusStoppedRole(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	ss := wire.Status{Since: now.Add(-time.Hour).Format(time.RFC3339), Connected: true, Project: &wire.ProjectStatus{
		Name: "proj", Root: p.Root, Merging: true, Agents: []wire.AgentStatus{}, Tasks: []wire.TaskStatus{},
		Facts: []event.Record{}, Stopped: []string{"pj-int"}}}
	st, text := statusOf(t, p, ss, nil, now)
	roles := map[string]string{}
	for _, r := range st.Roles {
		roles[r.Agent] = r.Status
	}
	if roles["pj-int"] != view.RoleStopped {
		t.Errorf("pj-int's role status %q, want %q (roles %+v)", roles["pj-int"], view.RoleStopped, st.Roles)
	}
	if s, ok := roles["pj-rev"]; !ok || s != "" {
		t.Errorf("pj-rev's role status %q (listed %v), want listed with no pane", s, ok)
	}
	matchLine(t, text, `^agent pj-int: stopped$`, "the stopped integrator's line")
	if strings.Contains(text, "agent pj-rev: stopped") {
		t.Errorf("pj-rev shown stopped:\n%s", text)
	}
}

// T-677: a role agent the lead stopped (obor restart <agent> --stop), its pane at a shell, is `stopped` for every
// output that reads the field: the view's role row, obor status' text and --json.
func TestStatusStoppedRoleTextJSON(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config(testkit.Set("roles.advisor", ""))
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	ss := wire.Status{Since: now.Add(-time.Hour).Format(time.RFC3339), Connected: true, Project: &wire.ProjectStatus{
		Name: "proj", Root: p.Root, Merging: true, Agents: []wire.AgentStatus{}, Tasks: []wire.TaskStatus{},
		Facts: []event.Record{}, Stopped: []string{"pj-advisor"}}}
	st, text := statusOf(t, p, ss, nil, now)
	r := roleRowOf(t, st, "pj-advisor")
	if r == nil {
		t.Fatalf("no advisor role row (roles %+v)", st.Roles)
	}
	if r.Status != view.RoleStopped {
		t.Fatalf("view role pj-advisor %q, want %q", r.Status, view.RoleStopped)
	}
	matchLine(t, text, `^agent pj-advisor: stopped$`, "the lead-stopped advisor's status line")

	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Roles []view.Role `json:"roles"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	stopped := 0
	for _, r := range wire.Roles {
		if r.Agent == "pj-advisor" && r.Status == view.RoleStopped {
			stopped++
		}
	}
	if stopped != 1 {
		t.Errorf("--json carries %d stopped rows of pj-advisor, want 1: %s", stopped, b)
	}
}

// roleRowOf is st's role row of agent (nil: none).
func roleRowOf(t *testing.T, st view.Project, agent string) *view.Role {
	t.Helper()
	for i, r := range st.Roles {
		if r.Agent == agent {
			return &st.Roles[i]
		}
	}
	return nil
}

// T-688: --json's events carry bad only when true: a red gate and a hold do, a green gate whose summary counts a fail
// does not (its record has no bad key at all).
func TestStatusEventsBad(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	now := time.Now().Truncate(time.Second)
	addEvent(t, p.Root, now.Add(-3*time.Minute), event.KindResult, "T-1", event.StepGate,
		map[string]any{"outcome": "green", "line": "check 25 pass, 0 fail"})
	addEvent(t, p.Root, now.Add(-2*time.Minute), event.KindResult, "T-2", event.StepGate,
		map[string]any{"outcome": "red", "line": "e2e failed"})
	addEvent(t, p.Root, now.Add(-time.Minute), event.KindFact, "T-3", event.StepNeedsYou,
		map[string]any{"reason": "held: decide", "cause": "held"})
	ss := wire.Status{Since: now.Add(-time.Hour).UTC().Format(time.RFC3339), Connected: true,
		Project: &wire.ProjectStatus{Name: "pj", Root: p.Root, Merging: true}}
	st, _ := statusOf(t, p, ss, nil, now)
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var js struct {
		Events []map[string]json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(b, &js); err != nil {
		t.Fatal(err)
	}
	// Newest first, then serve's own start (up here), which is no bad record.
	want := []struct {
		task string
		bad  bool
	}{{"T-3", true}, {"T-2", true}, {"T-1", false}, {"", false}}
	if len(js.Events) != len(want) {
		t.Fatalf("--json has %d events, want the three records and serve's start: %s", len(js.Events), b)
	}
	for i, w := range want {
		e := js.Events[i]
		task := ""
		if raw, ok := e["task"]; ok {
			if err := json.Unmarshal(raw, &task); err != nil {
				t.Fatal(err)
			}
		}
		raw, ok := e["bad"]
		if task != w.task || ok != w.bad {
			t.Errorf("event %d task %q, bad key %v; want %q, present %v", i, task, ok, w.task, w.bad)
			continue
		}
		if ok {
			var bad bool
			if err := json.Unmarshal(raw, &bad); err != nil || !bad {
				t.Errorf("event %d bad %s, want true", i, raw)
			}
		}
	}
}

// T-801: a custom role's agent is a role row of obor status (text and --json) like the advisor's, and obor restart
// <agent> --stop stops it; once its row is removed it is no row and restart no longer knows it, nothing else changes.
func TestStatusAndStopCustomRole(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Write("docs/agents/designer.md", "# Role: designer\n")
	p.Config(testkit.Set("roles.integrator", "opus"), testkit.Set("roles.designer", `{"model": "opus", "write": ["docs/design/"]}`))
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	ss := wire.Status{Since: now.Add(-time.Hour).Format(time.RFC3339), Connected: true, Project: &wire.ProjectStatus{
		Name: "proj", Root: p.Root, Agents: []wire.AgentStatus{{Name: "pj-designer", Status: "working"}},
		Tasks: []wire.TaskStatus{}, Facts: []event.Record{}}}
	st, text := statusOf(t, p, ss, nil, now)
	if r := roleRowOf(t, st, "pj-designer"); r == nil || r.Role != "designer" || r.Status != "working" {
		t.Fatalf("designer role row %+v (roles %+v), want role designer, working", r, st.Roles)
	}
	matchLine(t, text, `^agent pj-designer: working`, "the custom role's status line")

	f := p.Herdr()
	f.Shell("w1:p2", "pj-designer", p.Root)
	srv := herdrtest.Serve(t, f)
	env := []string{"HERDR_SOCKET_PATH=" + srv.Path, "HOME=" + p.Base + "/home", "OBOR_STATE_HOME=" + p.Base + "/state"}
	rc, stdout, stderr := run(t, p.Root, env, "restart", "pj-designer", "--stop")
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if rc != 0 || lines[len(lines)-1] != "STOPPED pj-designer (pane w1:p2, already at a shell)" {
		t.Errorf("restart pj-designer --stop: exit %d\n%s---\n%s", rc, stdout, stderr)
	}

	p.Config(testkit.Set("roles.integrator", "opus")) // the row removed
	st, _ = statusOf(t, p, ss, nil, now)
	if r := roleRowOf(t, st, "pj-designer"); r != nil {
		t.Errorf("a removed custom role still has a row: %+v", r)
	}
	if r := roleRowOf(t, st, "pj-int"); r == nil {
		t.Errorf("the integrator's row is gone with the custom role's (roles %+v)", st.Roles)
	}
	rc, stdout, _ = run(t, p.Root, env, "restart", "pj-designer", "--stop")
	if rc != 1 || !strings.Contains(stdout, "FAIL unknown agent pj-designer") {
		t.Errorf("restart of a removed role's agent: exit %d\n%s", rc, stdout)
	}
}

// T-871: obor status shows an unknown lane's reason on its line, beside the unknown state.
func TestStatusShowsUnknownLaneReason(t *testing.T) {
	why := "git status: exit status 1: fatal: index file smaller than expected"
	line := laneLine(view.Lane{Lane: port.Lane{Name: "L-01", State: "unknown", Reason: why}, Column: "unknown"}, view.Task{})
	if !strings.Contains(line, "unknown") || !strings.Contains(line, "reason: "+why) {
		t.Errorf("lane line %q; want its unknown state and its reason", line)
	}
}

// T-974: with a promotion, the "promoted <sha7> (<n> merges since)" line replaces the full check's, with why the
// promotion waits (the open review, T-990; a GREEN run); without one the full check's line stays.
func TestStatusPromotedLine(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fc := &view.FullCheck{SHA: "abcdef0123", Verdict: "GREEN", At: now.Unix() - 60, Summary: "promote: check 1 pass, 9s"}
	cases := []struct {
		pr   *wire.PromoteStatus
		want string
	}{
		{nil, "full check: GREEN abcdef0, 1m ago (promote: check 1 pass, 9s)"},
		{&wire.PromoteStatus{Branch: "main", SHA: "1234567890", Merges: 3}, "promoted 1234567 (3 merges since)"},
		{&wire.PromoteStatus{Branch: "main", SHA: "1234567890", Merges: 2, Wait: "waiting for review RV-5"},
			"promoted 1234567 (2 merges since); promotion waiting for review RV-5"},
		{&wire.PromoteStatus{Branch: "main", SHA: "1234567890", Merges: 2, Wait: "GREEN fedcba9 waits: RV-5 open (not closed)"},
			"promoted 1234567 (2 merges since); promotion GREEN fedcba9 waits: RV-5 open (not closed)"},
	}
	for _, c := range cases {
		got := fullCheckSection(view.Project{FullCheck: fc, Promote: c.pr}, now)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("promote %+v: %q, want [%q]", c.pr, got, c.want)
		}
	}
	// T-978: the last promotion run (last-promotion) on the promoted line, never the nightly's full check.
	run := &view.FullCheck{SHA: "fedcba9876", Verdict: "RED", At: now.Unix() - 120, Summary: "failed: check; 9s"}
	pr := &wire.PromoteStatus{Branch: "main", SHA: "1234567890", Merges: 1}
	got := fullCheckSection(view.Project{FullCheck: fc, Promote: pr, Promotion: run}, now)
	if want := "promoted 1234567 (1 merges since); last run RED fedcba9, 2m ago"; len(got) != 1 || got[0] != want {
		t.Errorf("with a last promotion run: %q, want [%q]", got, want)
	}
}
