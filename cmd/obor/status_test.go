package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
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
	st := view.Build(view.Input{Status: ss, Err: err, Lanes: checks.NewLaneTable(cfg), DB: db, Now: now,
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

// Serve up: its start and age, each lane with serve's task state and agent, serve's open tasks (not dropped) and agents
// on no lane, each
// needs-you with the commands serve says answer it (a question obor resume; a red gate obor resume --regate or --sendback, or drop; a
// reject obor drop only; a stalled worker its pane; a loop error none; a line waiting a day none), the lines not typed
// yet (T-466), the last full check and its age, the pause.
func TestStatusOverServe(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	p.Lane("L-02", "")
	p.Config()
	p.Commit(o1)
	now := time.Now()
	p.Write(".obor/state/last-full-check", "abcdef0123 "+strconv.FormatInt(now.Unix()-3*3600, 10)+" RED failed: e2e; 120s\n")
	p.Write(".obor/pause", "-\nold file\n") // T-465: no reader; serve's pause is the one
	since := now.Add(-2 * time.Hour).UTC()
	ss := wire.Status{Since: since.Format(time.RFC3339), Connected: true, Project: &wire.ProjectStatus{
		Name: "pj", Root: p.Root, Merging: true, Paused: &event.Pause{Why: "quota", Caller: "pj-lead"},
		Agents: []wire.AgentStatus{{Pane: "w1:p1", Name: "pj-l1", Agent: "claude", Lane: "L-01", Task: "T-01", Status: "idle"},
			{Pane: "w1:p9", Name: "pj-int", Agent: "claude", Status: "working", Age: 300}},
		Tasks: []wire.TaskStatus{{Task: "T-01", Lane: "L-01", Attempt: 1, State: "working"},
			{Task: "T-05", Lane: "L-09", Attempt: 2, State: "needs-you"}, {Task: "T-04", Lane: "L-09", State: "dropped"},
			{Task: "T-09", Attempt: 1, State: "waiting", After: []string{"T-03"}, Why: "dispatch T-09: lane L-01 is working on T-01"}},
		Facts: []event.Record{
			fact(t, 1, "T-01", event.FactData{Lane: "L-01", Reason: "asks: may I edit x.go?"}),
			fact(t, 2, "T-05", event.FactData{Lane: "L-09", Reason: "gate red after 2 sendbacks", Merge: true, Cause: "red"}),
			fact(t, 3, "T-06", event.FactData{Lane: "L-08", Reason: "rejected by the integrator: no", Merge: true, Cause: "rejected"}),
			fact(t, 4, "T-07", event.FactData{Lane: "L-07", Reason: "stalled"}),
			fact(t, 5, "T-08", event.FactData{Lane: "L-06", Reason: "job not delivered: …", Merge: true, Cause: "job not delivered"}),
			fact(t, 6, event.LoopTask, event.FactData{Reason: "loop: lanes: broken"}),
			fact(t, 8, event.MsgTask, event.FactData{Reason: "line to pj-lead from other waiting over 1d for its delivery: [obor other] hi"}),
			{Seq: 7, Task: "T-01", Step: event.StepStalled, Kind: event.KindFact}, // not a needs-you: not listed
		},
		Messages: []wire.MessageStatus{{To: "pj-lead", From: "other", Line: "[obor other] hi", Age: 90000},
			{To: "pj-rev", From: "pj-lead", Line: "[obor pj-lead] verify 3", Age: 120}},
		Answers: map[int64]string{ // serve's, from each task's state (T-515); a loop error, a line waiting: none
			1: `obor resume T-01 "<answer>"`, 2: `obor resume T-05 --regate | obor resume T-05 --sendback "<why>" | obor drop T-05 "<why>"`,
			3: `obor drop L-08 "<why>"`, 4: `its pane, or obor drop L-07 "<why>"`, 5: `obor resume T-08 | obor drop L-06 "<why>"`}}}
	st, text := statusOf(t, p, ss, nil, now)
	if !st.Serve.Up || len(st.NeedsYou) != 7 || len(st.Messages) != 2 {
		t.Fatalf("precondition: serve up, 7 needs-you, 2 lines: %+v", st)
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if want := "serve: up since " + since.Local().Format("Jan 2 15:04") + " (2h)"; lines[0] != want {
		t.Errorf("first line %q, want %q", lines[0], want)
	}
	matchLine(t, text, `^L-01 +- +task/T-01 +held +- +\(T-01\)  serve: working \(attempt 1\)  agent: pj-l1 \(idle\)$`, "L-01")
	matchLine(t, text, `^L-02 +- +- +free +- +\(-\)$`, "L-02")
	for _, want := range []string{
		"task T-05 (L-09): needs-you (attempt 2)",
		"queued T-09: waiting (after T-03): dispatch T-09: lane L-01 is working on T-01", // DD-v0.12 A1
		"agent pj-int: working 5m",
		`needs-you T-01 (L-01): asks: may I edit x.go? → obor resume T-01 "<answer>"`,
		`needs-you T-05 (L-09): gate red after 2 sendbacks → obor resume T-05 --regate | obor resume T-05 --sendback "<why>" | obor drop T-05 "<why>"`,
		`needs-you T-06 (L-08): rejected by the integrator: no → obor drop L-08 "<why>"`,
		`needs-you T-07 (L-07): stalled → its pane, or obor drop L-07 "<why>"`,
		`needs-you T-08 (L-06): job not delivered: … → obor resume T-08 | obor drop L-06 "<why>"`,
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
	if strings.Contains(text, "task T-01") || strings.Contains(text, "agent pj-l1:") || strings.Contains(text, "T-04") {
		t.Errorf("a lane's task or agent listed again on its own line, or a dropped task listed:\n%s", text)
	}

	// herdr not connected; the event log broken.
	ss.Connected = false
	ss.Project = &wire.ProjectStatus{Name: "pj", Root: p.Root, Merging: true, LogError: "disk full"}
	_, text = statusOf(t, p, ss, nil, now)
	matchLine(t, text, `^serve: up since .*; herdr: not connected$`, "herdr down")
	matchLine(t, text, `^serve: event log broken: disk full$`, "log error")
	ss.Project = nil
	_, text = statusOf(t, p, ss, nil, now)
	matchLine(t, text, `^serve: up since .*; this project is not loaded \(obor serve's output says why\); herdr: not connected$`, "not loaded")
}

// Serve down: one line saying so, first, the lanes from the store and git alone (a done lane with its age, an opencode
// lane without its plugin marker warned), no agents and no role rows (T-664: nothing stale), no full check yet; herdr
// is never asked.
func TestStatusServeDown(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1, o2 := p.Lane("L-01", "task/T-01"), p.Lane("L-02", "task/T-02")
	o3 := p.Lane("L-03", "task/T-03")
	p.Config(testkit.Set("models.oc", `{"kind":"opencode","model":"go/x"}`))
	srv := herdrtest.Serve(t, p.Herdr())
	p.Commit(o1)
	p.SubmitAgo("L-01", 730)
	p.Commit(o2)
	p.Submit("L-02")
	p.Commit(o3)
	p.State("L-03", map[string]any{"task": "T-03", "branch": "task/T-03", "type": "oc"}) // its dispatch runs the opencode oc
	st, text := statusOf(t, p, wire.Status{}, errors.New("obor serve is not running (dial: refused)"), time.Now())
	if st.Serve.Up || len(st.Lanes) != 3 {
		t.Fatalf("precondition: serve down, 3 lanes: %+v", st)
	}
	if len(st.Agents) != 0 || len(st.Roles) != 0 || st.Lanes[0].AgentStatus != "" {
		t.Errorf("serve down: agents %+v, roles %+v, lane agent status %q; want none (not stale rows)", st.Agents,
			st.Roles, st.Lanes[0].AgentStatus)
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	want := []string{`^serve: down \(obor serve is not running \(dial: refused\)\)$`,
		`^running: nothing$`,
		`^L-01 +- +task/T-01 +done +12m +\(T-01\)  agent: pj-l-01$`,
		`^L-02 +- +task/T-02 +done +[0-9]+s +\(T-02\)  agent: pj-l-02$`,
		`^L-03 +oc +task/T-03 +working +- +\(T-03\)  warning: no-plugin$`,
		`^nightly: off \(obor nightly on HH:MM\)$`, // merge.nightly_at unset (T-781)
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
	o1 := p.Lane("L-01", "task/T-01")
	p.Lane("L-02", "")
	p.Config()
	p.Commit(o1)
	p.Submit("L-01")
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
	if len(st.Lanes) != 2 || st.Lanes[0]["lane"] != "L-01" || st.Lanes[0]["task"] != "T-01" || st.Lanes[0]["state"] != "done" ||
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

	p.Config(testkit.Set("roles", `{"leads": [{"name": "mp", "agent": "lead-mp", "lanes": ["L-01"]}]}`))
	_, stdout, _ = run(t, p.Root, nil, "status", "--json")
	st.Lanes = nil
	if err := json.Unmarshal([]byte(stdout), &st); err != nil || len(st.Lanes) != 2 {
		t.Errorf("removed roles.leads: lanes %v (%v)", st.Lanes, err)
	}
}

// heldGateStatus is the app's status while T-1's gate is held open in its own goroutine (a running step on the task),
// with a publish on T-2 numbered publish.2 and T-3 idle: the fixture the CLI renders.
func heldGateStatus() wire.Status {
	return wire.Status{Connected: true, Project: &wire.ProjectStatus{
		Name: "pj", Root: "/work/pj",
		Tasks: []wire.TaskStatus{
			{Task: "T-1", Lane: "L-01", Attempt: 1, State: "submitted", Running: "gate"},
			{Task: "T-2", Lane: "L-02", Attempt: 1, State: "accepted", Running: "publish.2"},
			{Task: "T-3", Lane: "L-03", Attempt: 1, State: "accepted"},
		},
	}}
}

// taskLines returns the rendered task lines of st's project (its tasks on no configured lane), keyed by task id.
func taskLines(t *testing.T, st wire.Status) map[string]string {
	t.Helper()
	var buf bytes.Buffer
	writeStatus(&buf, view.Project{Serve: view.Serve{Up: true, Herdr: true, Project: true, Merging: true},
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
		{view.WaitItem{Task: "T-8", WaitsOn: view.WaitsNever, Rev: 2, Since: now.Add(-3 * time.Hour).Unix(),
			Title: "a title"}, "spec r2 stored 3h ago, never dispatched: a title"},
	} {
		if got := waitLine(c.w, now); got != c.want {
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
	p := testkit.New(t)
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
		{"publish", []wire.RunningStep{{Step: event.StepPublish, Task: "T-4", Since: now.Add(-5 * time.Second).Unix()}},
			"running: publish T-4 (5s)"},
		{"nightly check", []wire.RunningStep{{Step: event.StepNightly, Since: now.Add(-90 * time.Second).Unix()}},
			"running: nightly check (make -s check, 1m30s)"},
		{"restarting serve", []wire.RunningStep{{Step: event.StepServeRestart, Since: now.Unix()}},
			"running: restarting serve"},
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
	p := testkit.New(t)
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
	p := testkit.New(t)
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

// T-657: the binary's --short over a lane and a full check prints neither, only serve's line and `running: nothing`
// when nothing runs.
func TestStatusShortBinary(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	rc, stdout, stderr := run(t, p.Root, nil, "status", "--short")
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if rc != 0 || len(lines) != 2 || !strings.HasPrefix(lines[0], "serve: down (") || lines[1] != "running: nothing" {
		t.Errorf("obor status --short: exit %d, %q, %q; want serve's down line and `running: nothing`", rc, stdout, stderr)
	}
	if strings.Contains(stdout, "L-01") || strings.Contains(stdout, "full check") {
		t.Errorf("--short prints a lane or the full check:\n%s", stdout)
	}
}

// T-664: a store and a served status with one item of each queue kind — the merge loop in serve's order (a review
// job out and not fetched, two submitted tasks waiting for the conflict that holds the scratch, as serve's scheduler
// says: T-669, T-673), dispatches waiting on --after tasks, on a lane
// of their level and a spec never dispatched, the review request, the lead's watch with its note (T-666), a line and a
// brief not done, the pause and the nightly check — give --json's typed arrays (with the role rows, a lane's spec rev
// and open job, and the worded events with their seq) and one plain text line each.
func TestStatusQueue(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-10")
	p.Config(testkit.Set("merge.nightly_at", "03:00"))
	p.Commit(o1)
	p.Dispatched("L-01")
	p.Spec("T-10", "# T-10 — ten\n")
	p.Spec("T-20", "# T-20 — never sent\n")
	now := time.Now().Truncate(time.Second)
	ss := wire.Status{Since: now.Add(-time.Hour).UTC().Format(time.RFC3339), Connected: true, Project: &wire.ProjectStatus{
		Name: "pj", Root: p.Root, Merging: true, Paused: &event.Pause{Why: "quota", Caller: "pj-lead"},
		Agents: []wire.AgentStatus{{Pane: "w1:p1", Name: "pj-int", Agent: "claude", Status: "idle", Age: 120,
			Session: "s1"}},
		Tasks: []wire.TaskStatus{
			{Task: "T-10", Lane: "L-01", Attempt: 1, State: "working", Job: "T-10/1/work", Delivered: true},
			{Task: "T-100", Lane: "L-04", Attempt: 1, State: "submitted"},
			{Task: "T-1000", Lane: "L-05", Attempt: 1, State: "conflict"},
			{Task: "T-11", Lane: "L-02", Attempt: 1, State: "submitted"},
			{Task: "T-12", Attempt: 1, State: "waiting", After: []string{"T-11"}},
			{Task: "T-14", Attempt: 1, State: "waiting", Level: "medium", Types: []string{"sonnet"}},
			{Task: "T-9", Lane: "L-03", Attempt: 1, State: "awaiting-accept", Job: "T-9/1/review"},
		},
		// T-669, T-673: serve's scheduler: the conflict T-1000 holds the scratch, so T-100 and T-11 (in the pass's
		// lexical order) wait on it; no task is next (as data: the view copies what serve says, it does not check it).
		Scratch: &wire.ScratchStatus{Task: "T-1000", Why: wire.ScratchConflict},
		ScratchQueue: []wire.ScratchWait{{Task: "T-100", WaitsOn: "T-1000", Why: wire.ScratchConflict},
			{Task: "T-11", WaitsOn: "T-1000", Why: wire.ScratchConflict}},
		Review: "review request since 3m (after T-11, waiting T-11), note: check x",
		ReviewRequest: &wire.ReviewRequestStatus{After: []string{"T-11"}, Waiting: []string{"T-11"}, Note: "check x",
			By: "pj-lead", Since: now.Add(-3 * time.Minute).Unix()},
		Messages: []wire.MessageStatus{{To: "pj-lead", From: "other", Line: "[obor other] hi", Age: 60}},
		Briefs:   []wire.BriefStatus{{To: "pj-rev", Since: now.Add(-5 * time.Minute).Unix(), Delivered: true}},
		Watches: []wire.WatchStatus{{Task: "T-11,T-20", By: "pj-lead", Note: "release v0.17.4",
			Since: now.Add(-3 * time.Minute).Unix()}},
	}}
	st, text := statusOf(t, p, ss, nil, now)
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var js struct {
		Lanes []struct {
			Lane    string `json:"lane"`
			SpecRev int    `json:"spec_rev"`
			Job     *struct {
				ID        string `json:"id"`
				Delivered bool   `json:"delivered"`
			} `json:"job"`
		} `json:"lanes"`
		Roles []struct {
			Role, Agent, Status, Session string
			Brief                        *struct {
				Delivered bool `json:"delivered"`
			}
		} `json:"roles"`
		Events []struct {
			Seq        int64
			TS         string
			Task, Line string
		} `json:"events"`
		Watches []struct {
			Task, By, Note string
			Since          int64
		} `json:"watches"`
		Queue struct {
			Scratch *struct {
				Task, Why string
			} `json:"scratch"`
			Merge []struct {
				Task, Stage string
				WaitsOn     string `json:"waits_on"`
				Why         string
				Job         *struct {
					ID        string `json:"id"`
					Delivered bool   `json:"delivered"`
				}
			} `json:"merge"`
			Waiting []struct {
				Task    string   `json:"task"`
				WaitsOn string   `json:"waits_on"`
				After   []string `json:"after"`
				Level   string   `json:"level"`
				Rev     int      `json:"rev"`
				Title   string   `json:"title"`
			} `json:"waiting"`
			Review []struct {
				After []string `json:"after"`
				Note  string   `json:"note"`
				State string   `json:"state"`
				Since int64    `json:"since"`
			} `json:"review"`
			Outbox []struct {
				Kind, To  string
				Delivered bool
			} `json:"outbox"`
			Pause *struct {
				Reason string `json:"reason"`
			} `json:"pause"`
			Nightly *struct {
				At   string `json:"at"`
				Next int64  `json:"next"`
			} `json:"nightly"`
		} `json:"queue"`
	}
	if err := json.Unmarshal(b, &js); err != nil {
		t.Fatal(err)
	}
	// T-673: --json's merge items have no next field (the prediction is gone; the nightly's next run stays).
	var raw struct {
		Queue struct {
			Merge []map[string]json.RawMessage `json:"merge"`
		} `json:"queue"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, m := range raw.Queue.Merge {
		if _, ok := m["next"]; ok {
			t.Errorf("queue.merge %s has a next field in --json; want the facts only", m["task"])
		}
	}
	q := js.Queue
	if len(q.Merge) != 4 || q.Merge[0].Task != "T-100" || q.Merge[0].Stage != "submitted" ||
		q.Merge[0].WaitsOn != "T-1000" || q.Merge[0].Why != "conflict" ||
		q.Merge[1].Task != "T-1000" || q.Merge[1].Stage != "review" || q.Merge[1].WaitsOn != "" ||
		q.Merge[2].Task != "T-11" || q.Merge[2].WaitsOn != "T-1000" || q.Merge[2].Why != "conflict" ||
		q.Merge[3].Task != "T-9" || q.Merge[3].Stage != "review" {
		t.Errorf("queue.merge %+v; want serve's order and word: T-100 and T-11 wait on the conflict T-1000, T-1000 "+
			"review, T-9 review", q.Merge)
	}
	if s := q.Scratch; s == nil || s.Task != "T-1000" || s.Why != "conflict" {
		t.Errorf("queue.scratch %+v; want T-1000 conflict", s)
	}
	if j := q.Merge[3].Job; j == nil || j.ID != "T-9/1/review" || j.Delivered {
		t.Errorf("queue.merge T-9 job %+v; want T-9/1/review, not delivered", j)
	}
	if len(q.Waiting) != 3 || q.Waiting[0].Task != "T-12" || q.Waiting[0].WaitsOn != "after" ||
		strings.Join(q.Waiting[0].After, ",") != "T-11" || q.Waiting[1].Task != "T-14" || q.Waiting[1].WaitsOn != "lane" ||
		q.Waiting[1].Level != "medium" || q.Waiting[2].Task != "T-20" || q.Waiting[2].WaitsOn != "never dispatched" ||
		q.Waiting[2].Rev != 1 || q.Waiting[2].Title != "never sent" {
		t.Errorf("queue.waiting %+v; want T-12 after T-11, T-14 a medium lane, T-20 never dispatched (r1, its title)",
			q.Waiting)
	}
	if len(q.Review) != 1 || q.Review[0].State != "waiting" || q.Review[0].Note != "check x" || q.Review[0].Since == 0 ||
		strings.Join(q.Review[0].After, ",") != "T-11" {
		t.Errorf("queue.review %+v; want the request waiting on T-11 with its note and time", q.Review)
	}
	if len(q.Outbox) != 2 || q.Outbox[0].Kind != "line" || q.Outbox[0].To != "pj-lead" || q.Outbox[1].Kind != "brief" ||
		q.Outbox[1].To != "pj-rev" || !q.Outbox[1].Delivered {
		t.Errorf("queue.outbox %+v; want the line to pj-lead and the delivered brief to pj-rev", q.Outbox)
	}
	if q.Pause == nil || q.Pause.Reason != "quota" {
		t.Errorf("queue.pause %+v; want quota", q.Pause)
	}
	if q.Nightly == nil || q.Nightly.At != "03:00" || q.Nightly.Next <= now.Unix() || q.Nightly.Next > now.Add(24*time.Hour).Unix() {
		t.Errorf("queue.nightly %+v; want 03:00 within the next day", q.Nightly)
	}
	if len(js.Watches) != 1 || js.Watches[0].Task != "T-11,T-20" || js.Watches[0].By != "pj-lead" ||
		js.Watches[0].Note != "release v0.17.4" || js.Watches[0].Since != now.Add(-3*time.Minute).Unix() {
		t.Errorf("watches %+v; want T-11,T-20 watched by pj-lead with its note and time", js.Watches)
	}
	if len(js.Lanes) != 1 || js.Lanes[0].SpecRev != 1 || js.Lanes[0].Job == nil || js.Lanes[0].Job.ID != "T-10/1/work" ||
		!js.Lanes[0].Job.Delivered {
		t.Errorf("lanes %+v; want L-01 with spec_rev 1 and job T-10/1/work delivered", js.Lanes)
	}
	if len(js.Roles) != 2 || js.Roles[0].Agent != "pj-int" || js.Roles[0].Status != "idle" || js.Roles[0].Session != "s1" ||
		js.Roles[1].Agent != "pj-rev" || js.Roles[1].Status != "" || js.Roles[1].Brief == nil || !js.Roles[1].Brief.Delivered {
		t.Errorf("roles %+v; want pj-int idle (session s1) and pj-rev with no pane, on its delivered brief", js.Roles)
	}
	var dispatched bool
	for _, e := range js.Events {
		if e.Line == "dispatched T-10 (L-01)" && e.Seq > 0 && e.Task == "T-10" && e.TS != "" {
			dispatched = true
		}
	}
	if !dispatched {
		t.Errorf("events %+v; want the dispatch of T-10 with its seq, time and task", js.Events)
	}
	for _, re := range []string{
		`^merge T-9 \(L-03\): review \(awaiting-accept\), job T-9/1/review not fetched$`,
		`^scratch T-1000: conflict$`,
		`^merge T-100 \(L-04\): submitted, waits for T-1000 \(conflict\)$`,
		`^merge T-11 \(L-02\): submitted, waits for T-1000 \(conflict\)$`,
		`^queued T-12: waiting \(after T-11\)$`,
		`^queued T-14: waiting for a free lane \(medium: sonnet\)$`,
		`^queued T-20: spec r1 stored [0-9]+s ago, never dispatched: never sent$`,
		`^review request since 3m \(after T-11, waiting T-11\), note: check x$`,
		`^watch T-11,T-20 since 3m \(by pj-lead\) — release v0\.17\.4$`,
		`^undelivered to pj-lead \(from other, 1m\): \[obor other\] hi$`,
		`^brief to pj-rev: delivered, since 5m$`,
		`^paused: quota, until cleared`,
		`^nightly check at 03:00 \(in [0-9]+[smhd]\)$`,
	} {
		matchLine(t, text, re, "queue")
	}
	if strings.Contains(text, "queued T-10") {
		t.Errorf("a dispatched task with a spec listed as never dispatched:\n%s", text)
	}
	if strings.Contains(text, ", next") {
		t.Errorf("a merge line claims a task goes next (T-673: the queue states facts only):\n%s", text)
	}
}

// T-672: a role agent serve stopped once its work was done, with no pane running it, is `stopped`, not missing: the
// view's role row and the status line `agent <name>: stopped`; one serve did not stop has no pane as before.
func TestStatusStoppedRole(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
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
	p := testkit.New(t)
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
	p := testkit.New(t)
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

// T-695: the effort a task's dispatch named is on its lane's line and in --json (lanes[].effort); a task without one
// shows none.
func TestStatusShowsTheEffort(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	o2 := p.Lane("L-02", "task/T-02")
	p.Config()
	p.Commit(o1)
	p.Commit(o2)
	now := time.Now()
	ss := wire.Status{Since: now.Add(-time.Hour).UTC().Format(time.RFC3339), Connected: true, Project: &wire.ProjectStatus{
		Name: "pj", Root: p.Root, Merging: true,
		Tasks: []wire.TaskStatus{{Task: "T-01", Lane: "L-01", Attempt: 1, State: "working", Effort: "high"},
			{Task: "T-02", Lane: "L-02", Attempt: 1, State: "working"}}}}
	st, text := statusOf(t, p, ss, nil, now)
	if len(st.Lanes) != 2 {
		t.Fatalf("precondition: two lanes, got %+v", st.Lanes)
	}
	matchLine(t, text, `^L-01 +- .*serve: working \(attempt 1\)  effort: high$`, "L-01 at effort high")
	matchLine(t, text, `^L-02 +- .*serve: working \(attempt 1\)$`, "L-02 with no effort")
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"effort":"high"`) || st.Lanes[0].Effort != "high" || st.Lanes[1].Effort != "" {
		t.Errorf("--json lanes %+v: want L-01's effort high, L-02's none", st.Lanes)
	}
}

// T-721, T-765: a working lane's model is the one its dispatch chose (the lane row's), on its line and in --json
// (lanes[].type); a row from before T-721 (no model) shows none ("-"), as does a free lane.
func TestStatusShowsTheDispatchsType(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	o2 := p.Lane("L-02", "task/T-02")
	p.Lane("L-03", "")
	p.Config()
	p.Commit(o1)
	p.Commit(o2)
	p.State("L-01", map[string]any{"task": "T-01", "branch": "task/T-01", "type": "sonnet:high"})
	p.State("L-02", map[string]any{"task": "T-02", "branch": "task/T-02"})
	now := time.Now()
	ss := wire.Status{Since: now.Add(-time.Hour).UTC().Format(time.RFC3339), Connected: true, Project: &wire.ProjectStatus{
		Name: "pj", Root: p.Root, Merging: true,
		Tasks: []wire.TaskStatus{{Task: "T-01", Lane: "L-01", Attempt: 1, State: "working", Effort: "high"},
			{Task: "T-02", Lane: "L-02", Attempt: 1, State: "working"}}}}
	st, text := statusOf(t, p, ss, nil, now)
	if len(st.Lanes) != 3 || st.Lanes[0].State != "working" || st.Lanes[1].State != "working" ||
		st.Lanes[2].State != "free" {
		t.Fatalf("precondition: L-01 and L-02 working, L-03 free; got %+v", st.Lanes)
	}
	matchLine(t, text, `^L-01 +sonnet .*effort: high$`, "L-01 of its dispatch's type sonnet")
	matchLine(t, text, `^L-02 +- `, "L-02: no model in its row")
	matchLine(t, text, `^L-03 +- `, "L-03 free: no model")
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"type":"sonnet"`) || st.Lanes[0].Type != "sonnet" || st.Lanes[1].Type != "" ||
		st.Lanes[2].Type != "" {
		t.Errorf("--json lanes %+v: want L-01's model sonnet, L-02's and L-03's none", st.Lanes)
	}
}

// T-705: a working Claude lane whose worktree has no enabled obor plugin is warned no-plugin, as an opencode lane
// without its marker is; the testkit's lanes have the plugin enabled.
func TestStatusWarnsAClaudeLaneWithoutThePlugin(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1, o2 := p.Lane("L-01", "task/T-01"), p.Lane("L-02", "task/T-02")
	p.Config()
	p.Commit(o1)
	p.Commit(o2)
	if err := os.Remove(filepath.Join(o2, ".claude", "settings.json")); err != nil {
		t.Fatalf("precondition: L-02 had the settings file: %v", err)
	}
	_, text := statusOf(t, p, wire.Status{}, errors.New("obor serve is not running (dial: refused)"), time.Now())
	matchLine(t, text, `^L-02 +- +task/T-02 +working +- +\(T-02\)  warning: no-plugin$`, "L-02 without the plugin")
	if strings.Contains(text, "L-01 ") && strings.Contains(strings.SplitN(strings.SplitN(text, "L-01 ", 2)[1], "\n", 2)[0], "no-plugin") {
		t.Errorf("L-01 (plugin enabled) warned:\n%s", text)
	}
}
