//go:build integration

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

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
		Tasks: []wire.TaskStatus{{Task: "T-01", Lane: "L-01", Attempt: 1, State: "working", Phase: wire.PhaseNeedsYou},
			{Task: "T-05", Lane: "L-09", Attempt: 2, State: "needs-you", Phase: wire.PhaseNeedsYou},
			{Task: "T-04", Lane: "L-09", State: "dropped", Ended: true, Phase: wire.PhaseDropped},
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
	matchLine(t, text, `^L-01 +- +task/T-01 +needs-you +- +\(T-01\)  serve: working \(attempt 1\)  agent: pj-l1 \(idle\)$`, "L-01")
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

// Serve down: one line saying so, first, the lanes from the store and git alone (T-788: no phase, so a submitted lane
// is working, with its task_done's age; an opencode
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
		`^L-01 +- +task/T-01 +working +12m +\(T-01\)  agent: pj-l-01$`,
		`^L-02 +- +task/T-02 +working +[0-9]+s +\(T-02\)  agent: pj-l-02$`,
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
	if len(st.Lanes) != 2 || st.Lanes[0]["lane"] != "L-01" || st.Lanes[0]["task"] != "T-01" || st.Lanes[0]["state"] != "working" || st.Lanes[0]["done_age"] == nil ||
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

// T-979: a model paused with and without --for is one line per model beside the project's pause, --json carries them
// as model_pauses, and a model serve no longer names (its pause cleared) is gone.
func TestStatusListsModelPauses(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Lane("L-01", "task/T-01")
	p.Config()
	now := time.Now()
	until := now.Add(2 * time.Hour).Unix()
	ss := wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "pj", Root: p.Root, Merging: true,
		Paused: &event.Pause{Why: "quota", Caller: "pj-lead"},
		ModelPauses: map[string]*event.Pause{
			"sonnet":   {Why: "quota", Caller: "pj-lead"},
			"deepseek": {Why: "down", Until: until, Caller: "pj-lead"},
		}}}
	st, text := statusOf(t, p, ss, nil, now)
	if len(st.ModelPauses) != 2 {
		t.Fatalf("precondition: status model pauses %+v, want sonnet's and deepseek's", st.ModelPauses)
	}
	matchLine(t, text, `^paused sonnet until cleared \(quota\)$`, "sonnet")
	matchLine(t, text, `^paused deepseek until `+time.Unix(until, 0).Format("15:04")+` \(down\)$`, "deepseek")
	if i, j, k := strings.Index(text, "paused: quota"), strings.Index(text, "paused deepseek"),
		strings.Index(text, "paused sonnet"); i < 0 || j < i || k < j {
		t.Errorf("pause lines out of order (project's first, then models sorted):\n%s", text)
	}

	// --json: serve's answer through the client (serve ships it, T-979)
	home, _ := startFakeServe(t, p.Root, ss, "", "")
	env := []string{"OBOR_STATE_HOME=" + home}
	rc, stdout, stderr := run(t, p.Root, env, "status", "--json")
	if rc != 0 {
		t.Fatalf("status --json: exit %d, %s", rc, stderr)
	}
	var got struct {
		ModelPauses map[string]struct {
			Reason string `json:"reason"`
			Until  *int64 `json:"until"`
		} `json:"model_pauses"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.ModelPauses) != 2 || got.ModelPauses["sonnet"].Reason != "quota" || got.ModelPauses["sonnet"].Until != nil ||
		got.ModelPauses["deepseek"].Until == nil || *got.ModelPauses["deepseek"].Until != until {
		t.Errorf("--json model_pauses %+v, want sonnet's (quota, until cleared) and deepseek's (down, until %d)",
			got.ModelPauses, until)
	}

	// cleared: serve names none, so neither the text nor the JSON has one
	ss.Project.ModelPauses = nil
	_, text = statusOf(t, p, ss, nil, now)
	if strings.Contains(text, "paused sonnet") || strings.Contains(text, "paused deepseek") {
		t.Errorf("a cleared model still has a line:\n%s", text)
	}
	startFakeServeAt(t, wire.SocketPath(p.Root), ss, "", "")
	_, stdout, _ = run(t, p.Root, env, "status", "--json")
	if strings.Contains(stdout, "model_pauses") {
		t.Errorf("--json names model_pauses after the clear:\n%s", stdout)
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
			{Task: "T-10", Lane: "L-01", Attempt: 1, State: "working", Phase: wire.PhaseWorking, Job: "T-10/1/work",
				Delivered: true},
			{Task: "T-100", Lane: "L-04", Attempt: 1, State: "submitted", Phase: wire.PhaseSubmitted, Stage: wire.StageSubmitted},
			{Task: "T-1000", Lane: "L-05", Attempt: 1, State: "conflict", Phase: wire.PhaseReview, Stage: wire.StageReview},
			{Task: "T-11", Lane: "L-02", Attempt: 1, State: "submitted", Phase: wire.PhaseSubmitted, Stage: wire.StageSubmitted},
			{Task: "T-12", Attempt: 1, State: "waiting", Phase: wire.PhaseWaiting, After: []string{"T-11"},
				Difficulty: "hard", Model: "opus:high"},
			{Task: "T-14", Attempt: 1, State: "waiting", Phase: wire.PhaseWaiting, Level: "medium", Types: []string{"sonnet"},
				Difficulty: "medium", Model: "sonnet"},
			{Task: "T-9", Lane: "L-03", Attempt: 1, State: "awaiting-accept", Phase: wire.PhaseReviewing, Stage: wire.StageReview, Job: "T-9/1/review"},
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
			Lane string  `json:"lane"`
			Task *string `json:"task"`
		} `json:"lanes"`
		Tasks map[string]struct { // T-883: the sections name a task by id, its fields are here
			Title   string `json:"title"`
			Lane    string `json:"lane"`
			SpecRev int    `json:"spec_rev"`
			Job     *struct {
				ID        string `json:"id"`
				Delivered bool   `json:"delivered"`
			} `json:"job"`
		} `json:"tasks"`
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
			} `json:"merge"`
			Waiting []struct {
				Task       string   `json:"task"`
				WaitsOn    string   `json:"waits_on"`
				After      []string `json:"after"`
				Level      string   `json:"level"`
				Difficulty string   `json:"difficulty"`
				Model      string   `json:"model"`
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
	if j := js.Tasks[q.Merge[3].Task].Job; j == nil || j.ID != "T-9/1/review" || j.Delivered {
		t.Errorf("queue.merge T-9 job %+v; want T-9/1/review, not delivered", j)
	}
	if len(q.Waiting) != 3 || q.Waiting[0].Task != "T-12" || q.Waiting[0].WaitsOn != "after" ||
		strings.Join(q.Waiting[0].After, ",") != "T-11" || q.Waiting[1].Task != "T-14" || q.Waiting[1].WaitsOn != "lane" ||
		q.Waiting[1].Level != "medium" || q.Waiting[0].Difficulty != "hard" || q.Waiting[0].Model != "opus:high" ||
		q.Waiting[1].Difficulty != "medium" || q.Waiting[1].Model != "sonnet" || q.Waiting[2].Task != "T-20" || q.Waiting[2].WaitsOn != "never dispatched" ||
		js.Tasks["T-20"].SpecRev != 1 || js.Tasks["T-20"].Title != "never sent" {
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
	if tk := js.Tasks["T-10"]; len(js.Lanes) != 1 || js.Lanes[0].Task == nil || *js.Lanes[0].Task != "T-10" ||
		tk.Lane != "L-01" || tk.SpecRev != 1 || tk.Job == nil || tk.Job.ID != "T-10/1/work" || !tk.Job.Delivered {
		t.Errorf("lanes %+v, task T-10 %+v; want L-01 on T-10, with spec_rev 1 and job T-10/1/work delivered",
			js.Lanes, tk)
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
		`^merge T-9 \(L-03\): reviewing \(awaiting-accept\), job T-9/1/review not fetched$`,
		`^merge T-1000 \(L-05\): review \(conflict\)$`,
		`^scratch T-1000: conflict$`,
		`^merge T-100 \(L-04\): submitted, waits for T-1000 \(conflict\)$`,
		`^merge T-11 \(L-02\): submitted, waits for T-1000 \(conflict\)$`,
		`^queued T-12 \(hard, opus:high\): waiting \(after T-11\)$`, // T-977: difficulty and model, after stays
		`^queued T-14 \(medium, sonnet\): waiting for a free lane \(medium: sonnet\)$`,
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

// T-695, T-721, T-765: the effort a task's dispatch named and the model its lane row holds are on its line and in
// --json (lanes[].effort, lanes[].type); a task without them shows none, as does a free lane.
func TestStatusShowsEffortAndType(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	o2 := p.Lane("L-02", "task/T-02")
	p.Lane("L-03", "")
	p.Config()
	p.Commit(o1)
	p.Commit(o2)
	p.State("L-01", map[string]any{"task": "T-01", "branch": "task/T-01", "type": "sonnet:high"})
	p.State("L-02", map[string]any{"task": "T-02", "branch": "task/T-02"}) // a row from before T-721: no model
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
	matchLine(t, text, `^L-01 +sonnet .*serve: working \(attempt 1\)  effort: high$`, "L-01 of type sonnet at effort high")
	matchLine(t, text, `^L-02 +- .*serve: working \(attempt 1\)$`, "L-02: no model, no effort")
	matchLine(t, text, `^L-03 +- `, "L-03 free: no model")
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"type":"sonnet"`) || !strings.Contains(string(b), `"effort":"high"`) ||
		st.Lanes[0].Type != "sonnet" || st.TaskOn(st.Lanes[0]).Effort != "high" || st.Lanes[1].Type != "" ||
		st.TaskOn(st.Lanes[1]).Effort != "" || st.Lanes[2].Type != "" {
		t.Errorf("--json lanes %+v: want L-01's model sonnet and effort high, L-02's and L-03's none", st.Lanes)
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
	// The rows hold their tasks, not landed: working (T-1012); no agent recorded.
	p.State("L-01", map[string]any{"task": "T-01", "branch": "task/T-01"})
	p.State("L-02", map[string]any{"task": "T-02", "branch": "task/T-02"})
	if err := os.Remove(filepath.Join(o2, ".claude", "settings.json")); err != nil {
		t.Fatalf("precondition: L-02 had the settings file: %v", err)
	}
	_, text := statusOf(t, p, wire.Status{}, errors.New("obor serve is not running (dial: refused)"), time.Now())
	matchLine(t, text, `^L-02 +- +task/T-02 +working +- +\(T-02\)  warning: no-plugin$`, "L-02 without the plugin")
	if strings.Contains(text, "L-01 ") && strings.Contains(strings.SplitN(strings.SplitN(text, "L-01 ", 2)[1], "\n", 2)[0], "no-plugin") {
		t.Errorf("L-01 (plugin enabled) warned:\n%s", text)
	}
}
