package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/herdr/herdrtest"
	"obor/internal/launch"
	"obor/internal/testkit"
)

// statusOf is obor status of p's project with serve's answer ss (err: serve gave none), and its text at now.
func statusOf(t *testing.T, p *testkit.Project, ss app.Status, err error, now time.Time) (statusModel, string) {
	t.Helper()
	cfg, cerr := config.Load(p.Root)
	if cerr != nil {
		t.Fatal(cerr)
	}
	st := buildStatus(checks.NewLaneTable(cfg), ss, err)
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

// fact is a needs-you of task with data d, as serve's status lists it.
func fact(t *testing.T, seq int64, task string, d app.FactData) app.Record {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return app.Record{Seq: seq, Task: task, Step: app.StepNeedsYou, Kind: app.KindFact, Data: b}
}

// Serve up: its start and age, each lane with serve's task state and agent, serve's open tasks (not dropped) and agents
// on no lane, each
// needs-you with the commands serve says answer it (a question obor answer; a red gate obor sendback, resume or drop; a
// reject obor drop only; a stalled worker its pane; a loop error none; a line waiting a day none), the lines not typed
// yet (T-466), the last full check and its age, the pause.
func TestStatusOverServe(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("O-01", "task/T-01")
	p.Lane("O-02", "")
	p.Config()
	p.Commit(o1)
	now := time.Now()
	p.Write(".obor/state/last-full-check", "abcdef0123 "+strconv.FormatInt(now.Unix()-3*3600, 10)+" RED failed: e2e; 120s\n")
	p.Write(".obor/pause", "-\nold file\n") // T-465: no reader; serve's pause is the one
	since := now.Add(-2 * time.Hour).UTC()
	ss := app.Status{Since: since.Format(time.RFC3339), Connected: true, Project: &app.ProjectStatus{
		Name: "pj", Root: p.Root, Merging: true, Paused: &app.Pause{Why: "quota", Caller: "pj-lead"},
		Agents: []app.AgentStatus{{Pane: "w1:p1", Name: "pj-o1", Agent: "claude", Lane: "O-01", Task: "T-01", Status: "idle"},
			{Pane: "w1:p9", Name: "pj-int", Agent: "claude", Status: "working", Age: 300}},
		Tasks: []app.TaskStatus{{Task: "T-01", Lane: "O-01", Attempt: 1, State: "working"},
			{Task: "T-05", Lane: "O-09", Attempt: 2, State: "needs-you"}, {Task: "T-04", Lane: "O-09", State: "dropped"},
			{Task: "T-09", Attempt: 1, State: "waiting", After: []string{"T-03"}, Why: "dispatch T-09: lane O-01 is working on T-01"}},
		Facts: []app.Record{
			fact(t, 1, "T-01", app.FactData{Lane: "O-01", Reason: "asks: may I edit x.go?"}),
			fact(t, 2, "T-05", app.FactData{Lane: "O-09", Reason: "gate red after 2 sendbacks", Merge: true, Cause: "red"}),
			fact(t, 3, "T-06", app.FactData{Lane: "O-08", Reason: "rejected by the integrator: no", Merge: true, Cause: "rejected"}),
			fact(t, 4, "T-07", app.FactData{Lane: "O-07", Reason: "stalled"}),
			fact(t, 5, "T-08", app.FactData{Lane: "O-06", Reason: "job not delivered: …", Merge: true, Cause: "job not delivered"}),
			fact(t, 6, app.LoopTask, app.FactData{Reason: "loop: lanes: broken"}),
			fact(t, 8, app.MsgTask, app.FactData{Reason: "line to pj-lead from other waiting over 1d for its delivery: [obor other] hi"}),
			{Seq: 7, Task: "T-01", Step: app.StepStalled, Kind: app.KindFact}, // not a needs-you: not listed
		},
		Messages: []app.MessageStatus{{To: "pj-lead", From: "other", Line: "[obor other] hi", Age: 90000},
			{To: "pj-rev", From: "pj-lead", Line: "[obor pj-lead] verify 3", Age: 120}},
		Answers: map[int64]string{ // serve's, from each task's state (T-515); a loop error, a line waiting: none
			1: `obor answer T-01 "<text>"`, 2: `obor sendback O-09 "<why>" | obor resume T-05 | obor drop O-09 "<why>"`,
			3: `obor drop O-08 "<why>"`, 4: `its pane, or obor drop O-07 "<why>"`, 5: `obor resume T-08 | obor drop O-06 "<why>"`}}}
	st, text := statusOf(t, p, ss, nil, now)
	if !st.Serve.Up || len(st.NeedsYou) != 7 || len(st.Messages) != 2 {
		t.Fatalf("precondition: serve up, 7 needs-you, 2 lines: %+v", st)
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if want := "serve: up since " + since.Local().Format("Jan 2 15:04") + " (2h)"; lines[0] != want {
		t.Errorf("first line %q, want %q", lines[0], want)
	}
	matchLine(t, text, `^O-01 +opus +task/T-01 +held +- +\(T-01\)  serve: working \(attempt 1\)  agent: pj-o1 \(idle\)$`, "O-01")
	matchLine(t, text, `^O-02 +opus +- +free +- +\(-\)$`, "O-02")
	for _, want := range []string{
		"task T-05 (O-09): needs-you (attempt 2)",
		"task T-09: waiting (after T-03): dispatch T-09: lane O-01 is working on T-01", // DD-v0.12 A1
		"agent pj-int: working 5m",
		`needs-you T-01 (O-01): asks: may I edit x.go? → obor answer T-01 "<text>"`,
		`needs-you T-05 (O-09): gate red after 2 sendbacks → obor sendback O-09 "<why>" | obor resume T-05 | obor drop O-09 "<why>"`,
		`needs-you T-06 (O-08): rejected by the integrator: no → obor drop O-08 "<why>"`,
		`needs-you T-07 (O-07): stalled → its pane, or obor drop O-07 "<why>"`,
		`needs-you T-08 (O-06): job not delivered: … → obor resume T-08 | obor drop O-06 "<why>"`,
		"needs-you loop: loop: lanes: broken",
		"needs-you msg: line to pj-lead from other waiting over 1d for its delivery: [obor other] hi",
		"undelivered to pj-lead (from other, 1d): [obor other] hi",
		"undelivered to pj-rev (from pj-lead, 2m): [obor pj-lead] verify 3",
		"full check: RED abcdef0, 3h ago (failed: e2e; 120s)",
		"paused: quota, until cleared (obor pause --clear ends it; no dispatch meanwhile)",
	} {
		if !strings.Contains(text, want+"\n") {
			t.Errorf("no line %q in\n%s", want, text)
		}
	}
	if st.Paused == nil || st.Paused.Reason != "quota" || st.Paused.Until != nil || st.Paused.By != "pj-lead" {
		t.Errorf("paused %+v, want serve's: quota, until cleared, by pj-lead ", st.Paused)
	}
	if strings.Contains(text, "task T-01") || strings.Contains(text, "agent pj-o1:") || strings.Contains(text, "T-04") {
		t.Errorf("a lane's task or agent listed again on its own line, or a dropped task listed:\n%s", text)
	}

	// herdr not connected; the event log broken.
	ss.Connected = false
	ss.Project = &app.ProjectStatus{Name: "pj", Root: p.Root, Merging: true, LogError: "disk full"}
	_, text = statusOf(t, p, ss, nil, now)
	matchLine(t, text, `^serve: up since .*; herdr: not connected$`, "herdr down")
	matchLine(t, text, `^serve: event log broken: disk full$`, "log error")
	ss.Project = nil
	_, text = statusOf(t, p, ss, nil, now)
	matchLine(t, text, `^serve: up since .*; this project is not loaded \(obor serve's output says why\); herdr: not connected$`, "not loaded")
}

// Serve down: one line saying so, the lanes from the store and git alone (a done lane with its age,
// an opencode lane without its plugin marker warned), no full check yet; herdr is never asked.
func TestStatusServeDown(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1, o2 := p.Lane("O-01", "task/T-01"), p.Lane("O-02", "task/T-02")
	o3 := p.Lane("O-03", "task/T-03")
	p.Config(testkit.Set("lanes.O-03", "oc"), testkit.Set("lane_types.oc", `{"kind":"opencode"}`))
	srv := herdrtest.Serve(t, p.Herdr())
	p.Commit(o1)
	p.SubmitAgo("O-01", 730)
	p.Commit(o2)
	p.Submit("O-02")
	p.Commit(o3)
	st, text := statusOf(t, p, app.Status{}, errors.New("obor serve is not running (dial: refused)"), time.Now())
	if st.Serve.Up || len(st.Lanes) != 3 {
		t.Fatalf("precondition: serve down, 3 lanes: %+v", st)
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	want := []string{`^serve: down \(obor serve is not running \(dial: refused\)\)$`,
		`^running: nothing$`,
		`^O-01 +opus +task/T-01 +done +12m +\(T-01\)  agent: pj-o-01$`,
		`^O-02 +opus +task/T-02 +done +[0-9]+s +\(T-02\)  agent: pj-o-02$`,
		`^O-03 +oc +task/T-03 +working +- +\(T-03\)  warning: no-plugin$`,
		`^full check: none yet$`}
	if len(lines) != len(want) {
		t.Fatalf("%d lines, want %d:\n%s", len(lines), len(want), text)
	}
	for i, re := range want {
		matchLine(t, lines[i], re, "line "+strconv.Itoa(i+1))
	}
	if reqs := srv.Requests(); len(reqs) != 0 {
		t.Errorf("herdr called: %+v", reqs)
	}
}

// --json (the binary, serve down): indented, serve.up false, lanes[].lane, task, state, paused.reason, messages [];
// no ready rows (T-470: the queue went); a removed roles.leads changes nothing.
func TestStatusJSON(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("O-01", "task/T-01")
	p.Lane("O-02", "")
	p.Config()
	p.Commit(o1)
	p.Submit("O-01")
	p.Write(".obor/pause", "-\nhold\n") // T-465: no reader; serve (down here) holds the pause
	rc, stdout, stderr := run(t, p.Root, nil, "status", "--json")
	if rc != 0 || !strings.HasPrefix(stdout, "{\n  \"serve\": {") {
		t.Fatalf("--json: exit %d, %s\n%s", rc, stdout, stderr)
	}
	var st struct {
		Serve struct {
			Up    bool   `json:"up"`
			Error string `json:"error"`
		} `json:"serve"`
		Lanes []map[string]any `json:"lanes"`
		Ready any              `json:"ready"`
		Pause *struct {
			Reason string `json:"reason"`
		} `json:"paused"`
		NeedsYou []any `json:"needs_you"`
		Messages []any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(stdout), &st); err != nil {
		t.Fatal(err)
	}
	if st.Serve.Up || !strings.Contains(st.Serve.Error, "obor serve is not running") || st.NeedsYou == nil || st.Messages == nil {
		t.Errorf("serve: %+v, needs_you %v, messages %v; want down with its error, needs_you [], messages []", st.Serve,
			st.NeedsYou, st.Messages)
	}
	if len(st.Lanes) != 2 || st.Lanes[0]["lane"] != "O-01" || st.Lanes[0]["task"] != "T-01" || st.Lanes[0]["state"] != "done" ||
		st.Lanes[1]["task"] != nil || st.Lanes[1]["state"] != "free" {
		t.Errorf("lanes: %v", st.Lanes)
	}
	if st.Ready != nil || strings.Contains(stdout, `"ready"`) {
		t.Errorf("ready: %v; want no ready field (T-470)", st.Ready)
	}
	if st.Pause != nil {
		t.Errorf("paused %+v; want none (serve down; .obor/pause is not read)", st.Pause)
	}
	if _, text, _ := run(t, p.Root, nil, "status"); strings.Contains(text, "ready:") {
		t.Errorf("a ready line in the text:\n%s", text)
	}

	p.Config(testkit.Set("roles", `{"leads": [{"name": "mp", "agent": "lead-mp", "lanes": ["O-01"]}]}`))
	_, stdout, _ = run(t, p.Root, nil, "status", "--json")
	st.Lanes = nil
	if err := json.Unmarshal([]byte(stdout), &st); err != nil || len(st.Lanes) != 2 {
		t.Errorf("removed roles.leads: lanes %v (%v)", st.Lanes, err)
	}
}

// heldGateStatus is the app's status while T-1's gate is held open in its own goroutine (a running step on the task),
// with a publish on T-2 numbered publish.2 and T-3 idle: the fixture the CLI renders.
func heldGateStatus() app.Status {
	return app.Status{Connected: true, Project: &app.ProjectStatus{
		Name: "pj", Root: "/work/pj",
		Tasks: []app.TaskStatus{
			{Task: "T-1", Lane: "O-01", Attempt: 1, State: "submitted", Running: "gate"},
			{Task: "T-2", Lane: "O-02", Attempt: 1, State: "accepted", Running: "publish.2"},
			{Task: "T-3", Lane: "O-03", Attempt: 1, State: "accepted"},
		},
	}}
}

// taskLines returns the rendered task lines of st's project (its tasks on no configured lane), keyed by task id.
func taskLines(t *testing.T, st app.Status) map[string]string {
	t.Helper()
	var buf bytes.Buffer
	writeStatus(&buf, statusModel{Serve: serveJSON{Up: true, Herdr: true, Project: true, Merging: true},
		Tasks: st.Project.Tasks}, time.Now())
	lines := map[string]string{}
	for _, l := range strings.Split(buf.String(), "\n") {
		if f := strings.Fields(l); len(f) > 1 && f[0] == "task" {
			lines[strings.TrimSuffix(f[1], ":")] = l
		}
	}
	return lines
}

func TestStatusShowsRunningGate(t *testing.T) {
	st := heldGateStatus()
	if st.Project.Tasks[0].Running != "gate" {
		t.Fatalf("precondition: T-1 must be held in its gate, Running = %q", st.Project.Tasks[0].Running)
	}
	line := taskLines(t, st)["T-1"]
	if !strings.HasSuffix(line, "submitted (attempt 1), running gate") {
		t.Errorf("held gate line = %q, want the state then \"running gate\"", line)
	}
}

func TestStatusShowsRunningPublish(t *testing.T) {
	st := heldGateStatus()
	if st.Project.Tasks[1].Running != "publish.2" {
		t.Fatalf("precondition: T-2 must be publishing, Running = %q", st.Project.Tasks[1].Running)
	}
	line := taskLines(t, st)["T-2"]
	if !strings.HasSuffix(line, "accepted (attempt 1), running publish.2") {
		t.Errorf("publishing line = %q, want the state then \"running publish.2\"", line)
	}
}

func TestStatusNumberedStepKeepsStep(t *testing.T) {
	st := heldGateStatus()
	st.Project.Tasks[0].Running = "gate.3"
	line := taskLines(t, st)["T-1"]
	if !strings.HasSuffix(line, "submitted (attempt 1), running gate.3") {
		t.Errorf("numbered gate line = %q, want \"running gate.3\" with its number", line)
	}
}

func TestStatusIdleTaskHasNoRunning(t *testing.T) {
	st := heldGateStatus()
	if st.Project.Tasks[2].Running != "" {
		t.Fatalf("precondition: T-3 must be idle, Running = %q", st.Project.Tasks[2].Running)
	}
	line := taskLines(t, st)["T-3"]
	if strings.Contains(line, "running") {
		t.Errorf("idle task line = %q, want no running step", line)
	}
}

// T-462: a waiting task's line names what it waits for, no lane or attempt; DD-v0.12 A1: and why its dispatch is
// refused once all of them merged.
func TestStatusShowsWaiting(t *testing.T) {
	st := heldGateStatus()
	st.Project.Tasks = append(st.Project.Tasks, app.TaskStatus{Task: "T-4", Attempt: 1, State: "waiting",
		After: []string{"T-2", "T-3"}}, app.TaskStatus{Task: "T-5", Attempt: 1, State: "waiting", After: []string{"T-2"},
		Why: "dispatch T-5: no free lane of the types"},
		app.TaskStatus{Task: "T-6", Attempt: 1, State: "waiting", Level: "simple", Types: []string{"deepseek", "luna"}})
	lines := taskLines(t, st)
	if line := lines["T-4"]; line != "task T-4: waiting (after T-2, T-3)" {
		t.Errorf("waiting line = %q, want %q", line, "task T-4: waiting (after T-2, T-3)")
	}
	if line, want := lines["T-5"], "task T-5: waiting (after T-2): dispatch T-5: no free lane of the types"; line != want {
		t.Errorf("refused waiting line = %q, want %q", line, want)
	}
	if line, want := lines["T-6"], "task T-6: waiting for a simple lane (deepseek, luna)"; line != want {
		t.Errorf("lane-wait line = %q, want %q", line, want)
	}
}

// --facts adds one line per fact in force: its seq, time, task, step{reason} and lane.
func TestStatusFactLine(t *testing.T) {
	var buf bytes.Buffer
	writeFacts(&buf, "", []app.Record{{Seq: 7, TS: "2026-10-28T10:00:00Z", Task: "T-1", Step: app.StepNeedsYou,
		Kind: app.KindFact, Data: json.RawMessage(`{"lane":"O-01","reason":"held"}`)}})
	if got := buf.String(); !strings.HasPrefix(got, "fact #7 2026-10-28T10:00:00Z T-1 ") || !strings.HasSuffix(got, "(O-01)\n") {
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
		s    serveJSON
		want string
	}{
		{"launchd", serveJSON{Up: true, Since: at, Herdr: true, Project: true, Merging: true, PID: 4242, Launchd: "running"},
			"serve: launchd pid 4242 since " + since.Local().Format("Jan 2 15:04") + " (1h)"},
		{"pane, agent installed", serveJSON{Up: true, Since: at, Herdr: true, Project: true, Merging: true, PID: 77,
			Launchd: "loaded"}, "serve: pane pid 77 since " + since.Local().Format("Jan 2 15:04") + " (1h)"},
		{"pane, no agent", serveJSON{Up: true, Since: at, Herdr: true, Project: true, Merging: true, PID: 77,
			Launchd: "none"}, "serve: pane pid 77 since " + since.Local().Format("Jan 2 15:04") + " (1h)"},
		{"down, no agent", serveJSON{Error: "obor serve is not running", Launchd: "none"},
			"serve: down (obor serve install)"},
		{"down, agent installed", serveJSON{Error: "obor serve is not running", Launchd: "loaded"},
			"serve: down (obor serve is not running)"},
	} {
		c.s.sinceTime, _ = time.Parse(time.RFC3339, c.s.Since)
		var b bytes.Buffer
		writeStatus(&b, statusModel{Serve: c.s}, time.Now())
		if got := strings.SplitN(b.String(), "\n", 2)[0]; got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// T-192, T-500: a lane whose restart lock is held has op restart (obor status); a lock file nobody holds (its restart
// was killed) is no op.
func TestRestartOpIsTheHeldRestartLock(t *testing.T) {
	root := t.TempDir()
	lock := launch.RestartLock(root, "O-01", "")
	if ok, err := lock.Try(); !ok || err != nil {
		t.Fatalf("Try = %v, %v", ok, err)
	}
	if got := restartOp(root, "O-01"); got != "restart" {
		t.Errorf("held: op = %q, want restart", got)
	}
	if got := restartOp(root, "O-02"); got != "" {
		t.Errorf("another lane's lock: op = %q, want none", got)
	}
	lock.Unlock()
	if got := restartOp(root, "O-01"); got != "" {
		t.Errorf("freed: op = %q, want none", got)
	}
}

// T-657: the running line right under serve's says what serve does without an agent: an open gate intent with the
// command it runs and its elapsed time, two steps at once both, a publish (git alone) and the nightly check, a pending
// self-restart, and `running: nothing` when none is open.
func TestStatusRunningLine(t *testing.T) {
	p := testkit.New(t)
	p.Config(testkit.Set("commands.check", "make -s check"), testkit.Set("commands.after_merge", "make build"))
	now := time.Now().Truncate(time.Second)
	for _, c := range []struct {
		name    string
		running []app.RunningStep
		want    string
	}{
		{"nothing open", nil, "running: nothing"},
		{"gate", []app.RunningStep{{Step: app.StepGate, Task: "T-656",
			Since: now.Add(-2*time.Minute - 14*time.Second).Unix()}}, "running: gate T-656 (make -s check, 2m14s)"},
		{"two steps at once", []app.RunningStep{
			{Step: app.StepGate, Task: "T-656", Since: now.Add(-2*time.Minute - 14*time.Second).Unix()},
			{Step: app.StepAfterMerge, Task: "T-655", Since: now.Add(-12 * time.Second).Unix()}},
			"running: gate T-656 (make -s check, 2m14s) · after_merge T-655 (make build, 12s)"},
		{"publish", []app.RunningStep{{Step: app.StepPublish, Task: "T-4", Since: now.Add(-5 * time.Second).Unix()}},
			"running: publish T-4 (5s)"},
		{"nightly check", []app.RunningStep{{Step: app.StepNightly, Since: now.Add(-90 * time.Second).Unix()}},
			"running: nightly check (make -s check, 1m30s)"},
		{"restarting serve", []app.RunningStep{{Step: app.StepServeRestart, Since: now.Unix()}},
			"running: restarting serve"},
	} {
		ss := app.Status{Connected: true, Project: &app.ProjectStatus{Name: "pj", Root: p.Root, Running: c.running}}
		_, text := statusOf(t, p, ss, nil, now)
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		if len(lines) < 2 || !strings.HasPrefix(lines[0], "serve: ") {
			t.Fatalf("%s: the first line is not serve's:\n%s", c.name, text)
		}
		if lines[1] != c.want {
			t.Errorf("%s: the line under serve's = %q, want %q\n%s", c.name, lines[1], c.want, text)
		}
	}
}

// T-662: the pending line under the running one names the command steps whose effect does not run now: a failed step
// with the age of its last failure and its reason (its first line, short), one that never failed as queued; no line at
// all when nothing waits, and --json carries the same steps.
func TestStatusPendingLine(t *testing.T) {
	p := testkit.New(t)
	p.Config()
	now := time.Now().Truncate(time.Second)
	pending := []app.PendingStep{
		{Step: app.StepAfterMerge, Task: "T-655", Since: now.Add(-time.Hour).Unix(),
			Error: "git I/O: index.lock exists", FailedAt: now.Add(-3 * time.Minute).Unix()},
		{Step: app.StepGate, Task: "T-656", Since: now.Add(-time.Minute).Unix()},
	}
	ss := app.Status{Connected: true, Project: &app.ProjectStatus{Name: "pj", Root: p.Root,
		Running: []app.RunningStep{{Step: app.StepGate, Task: "T-654", Since: now.Add(-time.Minute).Unix()}},
		Pending: pending}}
	st, text := statusOf(t, p, ss, nil, now)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) < 3 || lines[2] != "pending: after_merge T-655 (failed 3m ago: git I/O: index.lock exists) · gate T-656 (queued)" {
		t.Errorf("the line under the running one = %q, want the two pending steps:\n%s", lines, text)
	}
	if len(st.Pending) != 2 || st.Pending[0].Error != pending[0].Error || st.Pending[1].Step != app.StepGate {
		t.Errorf("--json pending %+v, want serve's two steps", st.Pending)
	}
	// A reason keeps its first line only.
	ss.Project.Pending = []app.PendingStep{{Step: app.StepAfterMerge, Task: "T-655", FailedAt: now.Unix(),
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

// T-657: a lane's state column says what serve does for its task — gating, merging, reviewing (an integrator job is
// out), held (needs-you) — else the lane table's state (done). T-662: gating and merging only while the effect runs; a
// gate or publish whose intent waits reads gate pending / publish pending, not progress.
func TestStatusLaneStateColumn(t *testing.T) {
	now := time.Now()
	str := func(s string) *string { return &s }
	for _, c := range []struct {
		name    string
		lane    laneJSON
		running []runningJSON
		pending []app.PendingStep
		needs   []needsYouJSON
		want    string
	}{
		{"gating", laneJSON{Lane: "O-01", State: "done", Task: str("T-1"), TaskState: "submitted"},
			[]runningJSON{{Step: app.StepGate, Task: "T-1"}}, nil, nil, "gating"},
		{"merging", laneJSON{Lane: "O-01", State: "done", Task: str("T-1"), TaskState: "accepted"},
			[]runningJSON{{Step: app.StepPublish, Task: "T-1"}}, nil, nil, "merging"},
		{"gate pending", laneJSON{Lane: "O-01", State: "working", Task: str("T-1"), TaskState: "submitted"},
			nil, []app.PendingStep{{Step: app.StepGate, Task: "T-1"}}, nil, "gate pending"},
		{"publish pending", laneJSON{Lane: "O-01", State: "working", Task: str("T-1"), TaskState: "accepted"},
			nil, []app.PendingStep{{Step: app.StepPublish, Task: "T-1"}}, nil, "publish pending"},
		{"pending on another task", laneJSON{Lane: "O-01", State: "working", Task: str("T-1"), TaskState: "working"},
			nil, []app.PendingStep{{Step: app.StepGate, Task: "T-2"}}, nil, "working"},
		{"reviewing", laneJSON{Lane: "O-01", State: "done", Task: str("T-1"), TaskState: "awaiting-accept"}, nil, nil, nil,
			"reviewing"},
		{"resolving", laneJSON{Lane: "O-01", State: "done", Task: str("T-1"), TaskState: "resolving"}, nil, nil, nil,
			"reviewing"},
		{"held", laneJSON{Lane: "O-01", State: "done", Task: str("T-1"), TaskState: "needs-you"}, nil, nil, nil, "held"},
		{"held by a watchdog fact", laneJSON{Lane: "O-01", State: "working", Task: str("T-1"), TaskState: "working"},
			nil, nil, []needsYouJSON{{Task: "T-9", Lane: "O-01", Reason: "stalled"}}, "held"},
		{"done", laneJSON{Lane: "O-01", State: "done", Task: str("T-1"), TaskState: "published"}, nil, nil, nil, "done"},
	} {
		c.lane.Type = "opus"
		var b bytes.Buffer
		writeStatus(&b, statusModel{Serve: serveJSON{Up: true, Herdr: true, Project: true, Merging: true},
			Lanes: []laneJSON{c.lane}, Running: c.running, Pending: c.pending, NeedsYou: c.needs}, now)
		lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
		lane := ""
		for _, l := range lines {
			if strings.HasPrefix(l, "O-01") {
				lane = l
			}
		}
		if cols := regexp.MustCompile(`\s{2,}`).Split(strings.TrimSpace(lane), -1); len(cols) < 4 || cols[3] != c.want {
			t.Errorf("%s: lane line %q, want state %q", c.name, lane, c.want)
		}
	}
}

// T-657: --short is the serve line, the running line and needs-you, nothing else (a status bar).
func TestStatusShort(t *testing.T) {
	p := testkit.New(t)
	p.Config()
	now := time.Now().Truncate(time.Second)
	ss := app.Status{Since: now.Add(-time.Hour).UTC().Format(time.RFC3339), Connected: true, Project: &app.ProjectStatus{
		Name: "pj", Root: p.Root, Merging: true,
		Running:  []app.RunningStep{{Step: app.StepGate, Task: "T-1", Since: now.Add(-time.Minute).Unix()}},
		Tasks:    []app.TaskStatus{{Task: "T-2", Lane: "O-02", Attempt: 1, State: "working"}},
		Messages: []app.MessageStatus{{To: "pj-lead", From: "other", Line: "[obor other] hi", Age: 60}},
		Facts:    []app.Record{fact(t, 1, "T-3", app.FactData{Lane: "O-03", Reason: "stalled"})},
	}}
	st, _ := statusOf(t, p, ss, nil, now)
	var b bytes.Buffer
	writeStatusShort(&b, st, now)
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("--short text:\n%s\nwant serve's line, the running line and one needs-you", b.String())
	}
	if !strings.HasPrefix(lines[0], "serve: up since ") || lines[1] != "running: gate T-1 (1m00s)" ||
		!strings.HasPrefix(lines[2], "needs-you T-3 (O-03): stalled") {
		t.Errorf("--short lines %q; want serve: up…, the running gate and needs-you T-3", lines)
	}
	for _, gone := range []string{"O-02", "task T-2", "undelivered", "full check"} {
		if strings.Contains(b.String(), gone) {
			t.Errorf("--short prints %q:\n%s", gone, b.String())
		}
	}
}

// T-657: the binary's --short over a lane and a full check prints neither, only serve's line and `running: nothing`
// when nothing runs.
func TestStatusShortBinary(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("O-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	rc, stdout, stderr := run(t, p.Root, nil, "status", "--short")
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if rc != 0 || len(lines) != 2 || !strings.HasPrefix(lines[0], "serve: down (") || lines[1] != "running: nothing" {
		t.Errorf("obor status --short: exit %d, %q, %q; want serve's down line and `running: nothing`", rc, stdout, stderr)
	}
	if strings.Contains(stdout, "O-01") || strings.Contains(stdout, "full check") {
		t.Errorf("--short prints a lane or the full check:\n%s", stdout)
	}
}
