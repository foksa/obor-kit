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
	matchLine(t, text, `^O-01 +opus +task/T-01 +working +- +\(T-01\)  serve: working \(attempt 1\)  agent: pj-o1 \(idle\)$`, "O-01")
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
