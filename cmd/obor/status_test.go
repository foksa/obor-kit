package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
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
	return statusWith(t, p, nil, ss, err, now)
}

// statusWith is statusOf with the lane table's git answered by g (nil: real git, for a project made with git).
func statusWith(t *testing.T, p *testkit.Project, g *laneGit, ss wire.Status, err error, now time.Time) (view.Project, string) {
	t.Helper()
	cfg, cerr := config.Load(p.Root)
	if cerr != nil {
		t.Fatal(cerr)
	}
	db := openStore(p.Root)
	if db != nil {
		defer db.Close()
	}
	lt := checks.NewLaneTable(context.Background(), cfg)
	if g != nil {
		lt.Git = g.git
	}
	st := view.Build(view.Input{Status: ss, Err: err, Lanes: lt, DB: db, Now: now, Events: statusEvents})
	var b bytes.Buffer
	writeStatus(&b, st, now)
	return st, b.String()
}

// laneGit is the lane table's git for a plain project (testkit.NewDir): each lane dir is the worktree it was made as,
// on the branch it was made with ("" detached), at one fixed commit, clean unless dirty names it, its branch never
// merged. The table's reads (rev-parse, status, merge-base) are all it answers; any other call fails the test.
type laneGit struct {
	t      *testing.T
	branch map[string]string // lane dir → branch
	dirty  map[string]bool
}

func newLaneGit(t *testing.T) *laneGit {
	return &laneGit{t: t, branch: map[string]string{}, dirty: map[string]bool{}}
}

// lane is p.Lane(name, branch), known to the fake.
func (g *laneGit) lane(p *testkit.Project, name, branch string) string {
	dir := p.Lane(name, branch)
	g.branch[dir] = branch
	return dir
}

func (g *laneGit) git(_ context.Context, dir string, args ...string) (string, error) {
	b, ok := g.branch[dir]
	if !ok {
		return "", fmt.Errorf("fake git: %s is no lane", dir)
	}
	switch args[0] {
	case "rev-parse": // --show-toplevel HEAD --symbolic-full-name HEAD
		ref := "HEAD"
		if b != "" {
			ref = "refs/heads/" + b
		}
		return dir + "\n" + strings.Repeat("a", 40) + "\n" + ref + "\n", nil
	case "status":
		if g.dirty[dir] {
			return " M x.txt\n", nil
		}
		return "", nil
	case "merge-base":
		return "", errors.New("not an ancestor")
	}
	g.t.Fatalf("fake git: %v", args)
	return "", nil
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
			Waiting: ts.Waiting, Phase: ts.Phase, Running: ts.Running}
	}
	writeStatus(&buf, view.Project{Serve: view.Serve{Up: true, Terminal: true, Project: true, Merging: true},
		Tasks: tasks}, time.Now())
	lines := map[string]string{}
	for _, l := range strings.Split(buf.String(), "\n") {
		if f := strings.Fields(l); len(f) > 1 && f[0] == "task" {
			lines[strings.TrimSuffix(f[1], ":")] = l
		}
	}
	return lines
}

// upProject is a literal project with serve up on the terminal and its merge loop on: the formatting rows render it.
func upProject() view.Project {
	return view.Project{Serve: view.Serve{Up: true, Terminal: true, Project: true, Merging: true}}
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

// a waiting task's queue line names what it waits for, no lane or attempt, and why its dispatch is refused once
// all of them merged; it names a lane of its level, or a spec stored and never dispatched.
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
		{view.WaitItem{Task: "T-6", WaitsOn: view.WaitsLane, Level: "medium", Types: []string{"sonnet"},
			Why: "waiting for a free lane (L-01 dirty-free: src/x.ts, out/)"},
			"waiting for a free lane (medium: sonnet) (L-01 dirty-free: src/x.ts, out/)"},
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
	st.Project.Tasks = append(st.Project.Tasks, wire.TaskStatus{Task: "T-4", Attempt: 1, State: "waiting", Waiting: true,
		After: []string{"T-2"}})
	if line, ok := taskLines(t, st)["T-4"]; ok {
		t.Errorf("a waiting task listed as a task line: %q", line)
	}
}

// A waiting dispatch that held its refusal so long that serve shows it as needs-you is still the queue's, not a task
// line of the lanes section: serve's waiting predicate decides, never the displayed state. This row keeps the
// whole-project build: the task's waiting flag is view.Build's, and this is its reader (same for the first row of
// TestStatusRunningLine, the configured command).
func TestStatusWaitingShownAsNeedsYou(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	now := time.Now()
	ss := wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "pj", Root: p.Root,
		Tasks: []wire.TaskStatus{{Task: "T-2", Attempt: 1, State: wire.PhaseNeedsYou, Waiting: true, Rank: 1,
			Phase: wire.PhaseWaiting, After: []string{"T-1"}, Why: "dispatch T-2: lane of its type is working on T-1",
			Level: "medium", Types: []string{"sonnet"}, Difficulty: "medium", Model: "sonnet"}}}}
	st, text := statusOf(t, p, ss, nil, now)
	if t2 := st.Tasks["T-2"]; !t2.Waiting || !t2.Open {
		t.Fatalf("precondition: T-2 waiting %v, open %v; want a waiting dispatch serve reports", t2.Waiting, t2.Open)
	}
	if strings.Contains(text, "task T-2 ") {
		t.Errorf("a waiting task shown as needs-you listed as a task line:\n%s", text)
	}
	if !strings.Contains(text, "queued T-2 ") {
		t.Errorf("a waiting task shown as needs-you not in the queue:\n%s", text)
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

// the serve line names who runs serve: launchd (its agent's pid is serve's), a pane (any other serve), or
// nobody with no launchd agent (install it); a down serve whose agent is installed keeps the reason.
func TestStatusServeLine(t *testing.T) {
	since := time.Now().Add(-time.Hour)
	at := since.Format(time.RFC3339)
	for _, c := range []struct {
		name string
		s    view.Serve
		want string
	}{
		{"launchd", view.Serve{Up: true, Since: at, Terminal: true, Project: true, Merging: true, PID: 4242, Launchd: "running"},
			"serve: launchd pid 4242 since " + since.Local().Format("Jan 2 15:04") + " (1h)"},
		{"pane, agent installed", view.Serve{Up: true, Since: at, Terminal: true, Project: true, Merging: true, PID: 77,
			Launchd: "loaded"}, "serve: pane pid 77 since " + since.Local().Format("Jan 2 15:04") + " (1h)"},
		{"pane, no agent", view.Serve{Up: true, Since: at, Terminal: true, Project: true, Merging: true, PID: 77,
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

// a serve whose callers still run another obor release gets one line under serve's (the running line follows
// it), and none when no observed agent is behind.
func TestServeLineSkew(t *testing.T) {
	t.Parallel()
	skew := "pj-l-01 runs obor v0.1.0 (serve v0.2.0)"
	var b bytes.Buffer
	writeStatus(&b, view.Project{Serve: view.Serve{Up: true, Terminal: true, Project: true, Merging: true, Skew: skew}},
		time.Now())
	lines := strings.Split(b.String(), "\n")
	want := "serve: another obor release: " + skew + "; restart them (obor restart <agent>)"
	if len(lines) < 2 {
		t.Fatalf("status has %d lines, want serve's line and the skew line under it (in\n%s)", len(lines), b.String())
	}
	if lines[1] != want {
		t.Errorf("second line is %q, want %q (in\n%s)", lines[1], want, b.String())
	}
	var none bytes.Buffer
	writeStatus(&none, view.Project{Serve: view.Serve{Up: true, Terminal: true, Project: true, Merging: true}}, time.Now())
	for _, l := range strings.Split(none.String(), "\n") {
		if strings.HasPrefix(l, "serve: another obor release") {
			t.Errorf("no skew: unexpected line %q (in\n%s)", l, none.String())
		}
	}
	// --json carries the same field, and no key at all when there is no skew.
	bJSON, err := json.Marshal(view.Project{Serve: view.Serve{Up: true, Terminal: true, Project: true, Merging: true,
		Skew: skew}})
	if err != nil {
		t.Fatal(err)
	}
	var js struct {
		Serve struct {
			Skew string `json:"skew"`
		} `json:"serve"`
	}
	if err := json.Unmarshal(bJSON, &js); err != nil {
		t.Fatal(err)
	}
	if js.Serve.Skew != skew {
		t.Errorf("--json serve.skew = %q, want %q (%s)", js.Serve.Skew, skew, bJSON)
	}
	bJSON, err = json.Marshal(view.Project{Serve: view.Serve{Up: true, Terminal: true, Project: true, Merging: true}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bJSON), `"skew"`) {
		t.Errorf("no skew: --json has a skew key: %s", bJSON)
	}
}

// obor status' web line under serve's: launchd runs obor.web (its pid and the page's address), it is not running
// (no agent, or launchd does not have it loaded), and there is no line at all on a host without launchd.
func TestStatusWebLine(t *testing.T) {
	up := view.Serve{Up: true, Terminal: true, Project: true, Merging: true}
	for _, c := range []struct {
		name string
		web  *view.Web
		want string // "" : no web line
	}{
		{"launchd", &view.Web{Launchd: "running", PID: 4242, URL: "http://127.0.0.1:8787"},
			"web: launchd pid 4242 · http://127.0.0.1:8787"},
		{"not running", &view.Web{Launchd: "none"}, "web: not running (obor serve install)"},
		{"no launchd", nil, ""},
	} {
		var b bytes.Buffer
		writeStatus(&b, view.Project{Serve: up, Web: c.web}, time.Now())
		lines := strings.Split(b.String(), "\n")
		if c.want == "" {
			for _, l := range lines {
				if strings.HasPrefix(l, "web:") {
					t.Errorf("%s: unexpected web line %q (in\n%s)", c.name, l, b.String())
				}
			}
			continue
		}
		if len(lines) < 2 || lines[1] != c.want {
			t.Errorf("%s: line 2 is %q, want %q under serve's (in\n%s)", c.name, lines[0], c.want, b.String())
		}
	}
}

// obor status' view of the web agent: none while its plist is missing (launchd is not asked), none while launchd
// does not have it loaded, and running with launchd's pid and the page's address once it does.
func TestWebStatus(t *testing.T) {
	ctx := context.Background()
	callsLog, state := fakeLaunchctl(t)
	home := t.TempDir()
	env := testkit.MapEnv(map[string]string{"HOME": home, "PATH": "/usr/bin"})
	w := webOf(nil, env)
	if !w.Supported() {
		t.Skip("launchd: darwin only")
	}
	if got := webStatus(ctx, env); got == nil || got.Launchd != "none" || got.PID != 0 {
		t.Fatalf("no plist: %+v, want none", got)
	}
	if calls, _ := os.ReadFile(callsLog); len(calls) != 0 {
		t.Errorf("launchd was asked while the plist is missing: %q", calls)
	}
	if err := os.MkdirAll(filepath.Dir(w.PlistPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.PlistPath(), w.Plist(), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := webStatus(ctx, env); got == nil || got.Launchd != "none" {
		t.Fatalf("plist, label not loaded: %+v, want none", got)
	}
	if err := os.WriteFile(state, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := webStatus(ctx, env); got == nil || got.Launchd != "running" || got.PID != 4242 ||
		got.URL != "http://127.0.0.1:8787" {
		t.Errorf("loaded: %+v, want running, pid 4242, http://127.0.0.1:8787", got)
	}
}

// the running line right under serve's says what serve does without an agent: one row through view.Build gives each
// step the configured command it runs, serve's own step name when the config sets none (the line never guesses one);
// on the view's own steps the rest is the wording:
// the gate and what it runs with its elapsed time, two steps at once both, a publish (git alone) and the nightly check,
// a pending self-restart, and `running: nothing` when none is open.
func TestStatusRunningLine(t *testing.T) {
	p := testkit.NewPlain(t)
	p.Config(testkit.Set("merge.check", "make -s check"), testkit.Set("merge.after", "make build"),
		testkit.Set("commands.check_affected", "make -s check-affected"))
	now := time.Now().Truncate(time.Second)
	ss := wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "pj", Root: p.Root, Running: []wire.RunningStep{
		{Step: event.StepGate, Task: "T-656", Command: "check", Since: now.Add(-2*time.Minute - 14*time.Second).Unix()},
		{Step: event.StepTest, Task: "T-5", Command: "check_affected", Since: now.Add(-2 * time.Minute).Unix()},
		// a command the config does not set shows serve's name alone; between two commands (the runner clears it as
		// each ends) serve names none, so the line names none either.
		{Step: event.StepTest, Task: "T-6", Command: "guard", Since: now.Add(-9 * time.Second).Unix()},
		{Step: event.StepGate, Task: "T-657", Since: now.Add(-14 * time.Second).Unix()},
		{Step: event.StepAfterMerge, Task: "T-655", Since: now.Add(-12 * time.Second).Unix()},
		{Step: event.StepNightly, Since: now.Add(-90 * time.Second).Unix()}}}}
	_, text := statusOf(t, p, ss, nil, now)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	want := "running: gate T-656 (make -s check, 2m14s) · test T-5 (make -s check-affected, 2m00s) · " +
		"test T-6 (guard, 9s) · gate T-657 (14s) · after_merge T-655 (make build, 12s) · nightly check (make -s check, 1m30s)"
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "serve: ") {
		t.Fatalf("the first line is not serve's:\n%s", text)
	}
	if lines[1] != want {
		t.Errorf("the line under serve's = %q, want %q\n%s", lines[1], want, text)
	}
	for _, c := range []struct {
		name    string
		running []view.Running
		want    string
	}{
		{"nothing open", nil, "running: nothing"},
		{"publish", []view.Running{{Step: event.StepPublish, Task: "T-4", Since: now.Add(-5 * time.Second).Unix()}},
			"running: publish T-4 (5s)"},
		{"restarting serve", []view.Running{{Step: event.StepServeRestart, Since: now.Unix()}},
			"running: restarting serve"},
		// the restart holds nothing back: lane tests run beside it, and the line says nothing about waiting.
		{"restarting serve held by an admitted writer", []view.Running{{Step: event.StepServeRestart, Since: now.Unix(),
			Waits: "config_set (writer)"}}, "running: restarting serve (waits for config_set (writer))"},
		{"restarting serve past its wait", []view.Running{{Step: event.StepServeRestart, Since: now.Unix(),
			Cuts: "gate T-4"}}, "running: restarting serve (cuts gate T-4)"},
		{"restarting serve beside a lane test", []view.Running{
			{Step: event.StepTest, Task: "T-5", Command: "make -s check-affected", Since: now.Add(-2 * time.Minute).Unix()},
			{Step: event.StepServeRestart, Since: now.Unix()}},
			"running: test T-5 (make -s check-affected, 2m00s) · restarting serve"},
	} {
		st := upProject()
		st.Running = c.running
		var b bytes.Buffer
		writeStatus(&b, st, now)
		lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
		if len(lines) < 2 || !strings.HasPrefix(lines[0], "serve: ") {
			t.Fatalf("%s: the first line is not serve's:\n%s", c.name, b.String())
		}
		if lines[1] != c.want {
			t.Errorf("%s: the line under serve's = %q, want %q\n%s", c.name, lines[1], c.want, b.String())
		}
	}
}

// the pending line under the running one names the command steps whose effect does not run now: a failed step
// with the age of its last failure and its reason (its first line, short), one that never failed as queued; no line at
// all when nothing waits.
func TestStatusPendingLine(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	st := upProject()
	st.Running = []view.Running{{Step: event.StepGate, Task: "T-654", Since: now.Add(-time.Minute).Unix()}}
	st.Pending = []wire.PendingStep{
		{Step: event.StepAfterMerge, Task: "T-655", Since: now.Add(-time.Hour).Unix(),
			Error: "git I/O: index.lock exists", FailedAt: now.Add(-3 * time.Minute).Unix()},
		{Step: event.StepGate, Task: "T-656", Since: now.Add(-time.Minute).Unix()},
	}
	var b bytes.Buffer
	writeStatus(&b, st, now)
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) < 3 || lines[2] != "pending: after_merge T-655 (failed 3m ago: git I/O: index.lock exists) · gate T-656 (queued)" {
		t.Errorf("the line under the running one = %q, want the two pending steps:\n%s", lines, b.String())
	}
	// A reason keeps its first line only.
	st.Pending = []wire.PendingStep{{Step: event.StepAfterMerge, Task: "T-655", FailedAt: now.Unix(),
		Error: "FAIL T-655\nsecond line"}}
	b.Reset()
	writeStatus(&b, st, now)
	if !strings.Contains(b.String(), "pending: after_merge T-655 (failed 0s ago: FAIL T-655)") {
		t.Errorf("a multi-line reason is not shown as its first line only:\n%s", b.String())
	}
	// Nothing waits: no pending line at all.
	st.Pending, st.Running = nil, nil
	b.Reset()
	writeStatus(&b, st, now)
	if strings.Contains(b.String(), "pending:") {
		t.Errorf("the pending line is printed with nothing pending:\n%s", b.String())
	}
}

// --short is the serve line, the running line and needs-you, nothing else (a status bar; the open review's line, when
// one is open: TestStatusReviewOpen).
func TestStatusShort(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	st := upProject()
	st.Serve.Since, st.Serve.SinceTime = now.Add(-time.Hour).UTC().Format(time.RFC3339), now.Add(-time.Hour)
	st.Running = []view.Running{{Step: event.StepGate, Task: "T-1", Since: now.Add(-time.Minute).Unix()}}
	st.Tasks = map[string]view.Task{
		"T-2": {Task: "T-2", Lane: "L-02", Attempt: 1, TaskState: "working", Open: true},
		"T-3": {Task: "T-3", Lane: "L-03"},
	}
	st.Messages = []wire.MessageStatus{{To: "pj-lead", From: "other", Line: "[obor other] hi", Age: 60}}
	st.NeedsYou = []view.NeedsYou{{Task: "T-3", Reason: "stalled"}}
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

// the open review's line per stage: what the stage is about, the file once written, the reviewer with its pane's status
// and the stage's age.
func TestReviewOpenLine(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	since := now.Add(-6 * time.Minute).Unix()
	base := wire.ReviewOpenStatus{ID: "RV-190", Tasks: []string{"T-1254", "T-1255"}, Agent: "ob-rev",
		AgentStatus: "working", Since: since}
	file := "reviews/RV-190.md"
	for _, c := range []struct {
		stage, file string
		fixes       []wire.ReviewFix
		want        string
	}{
		{wire.ReviewReviewing, "", nil, "review RV-190: reviewing T-1254, T-1255 (ob-rev working, 6m)"},
		{wire.ReviewWritten, file, nil, "review RV-190: written: the lead's turn · reviews/RV-190.md (ob-rev working, 6m)"},
		{wire.ReviewFixes, file, []wire.ReviewFix{{Task: "T-1260", State: "merged"}, {Task: "T-1261", State: "open"}},
			"review RV-190: fixes T-1260 merged · T-1261 open · reviews/RV-190.md (ob-rev working, 6m)"},
		{wire.ReviewVerifying, file, []wire.ReviewFix{{Task: "T-1260", State: "merged"}, {Task: "T-1261", State: "dropped"}},
			"review RV-190: verifying T-1260 · reviews/RV-190.md (ob-rev working, 6m)"},
		{wire.ReviewApproved, file, []wire.ReviewFix{{Task: "T-1260", State: "verified"}},
			"review RV-190: approved: close it · reviews/RV-190.md (ob-rev working, 6m)"},
	} {
		ro := base
		ro.Stage, ro.File, ro.Fixes = c.stage, c.file, c.fixes
		var b bytes.Buffer
		writeReviewOpen(&b, &ro, now)
		if got := strings.TrimSuffix(b.String(), "\n"); got != c.want {
			t.Errorf("%s: %q, want %q", c.stage, got, c.want)
		}
	}
	var b bytes.Buffer
	writeReviewOpen(&b, nil, now)
	if b.Len() != 0 {
		t.Errorf("no open review: %q, want no line", b.String())
	}
}

// the open review's line is right under serve's in obor status and in --short, and --json carries it as review_open.
func TestStatusReviewOpen(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	st := upProject()
	st.Serve.Since, st.Serve.SinceTime = now.Add(-time.Hour).UTC().Format(time.RFC3339), now.Add(-time.Hour)
	st.ReviewOpen = &wire.ReviewOpenStatus{ID: "RV-190", Stage: wire.ReviewReviewing, Tasks: []string{"T-1254"},
		Agent: "ob-rev", AgentStatus: "working", Since: now.Add(-6 * time.Minute).Unix()}
	want := "review RV-190: reviewing T-1254 (ob-rev working, 6m)"
	var b bytes.Buffer
	writeStatus(&b, st, now)
	lines := strings.Split(b.String(), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "serve: up since ") || lines[1] != want {
		t.Errorf("obor status:\n%s\nwant serve's line, then %q", b.String(), want)
	}
	b.Reset()
	writeStatusShort(&b, st, now)
	short := strings.Split(b.String(), "\n")
	if len(short) < 2 || !strings.HasPrefix(short[0], "serve: up since ") || short[1] != want {
		t.Errorf("--short:\n%s\nwant serve's line, then %q", b.String(), want)
	}
	js, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ReviewOpen *wire.ReviewOpenStatus `json:"review_open"`
	}
	if err := json.Unmarshal(js, &got); err != nil || got.ReviewOpen == nil || got.ReviewOpen.ID != "RV-190" ||
		got.ReviewOpen.Stage != wire.ReviewReviewing {
		t.Errorf("--json review_open %+v (%v), want RV-190 reviewing", got.ReviewOpen, err)
	}
}

// a role agent done with no open work for an hour carries serve's hint at the end of its line: the command that stops
// it; a worker's line and the lead's have none.
func TestStatusDoneRoleHint(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	st := upProject()
	st.Serve.Since, st.Serve.SinceTime = now.Add(-time.Hour).Format(time.RFC3339), now.Add(-time.Hour)
	st.Agents = []wire.AgentStatus{
		{Machine: "local", Group: "pj", Pane: "w1:p3", Name: "pj-advisor", Agent: "claude", Status: "done", Age: 3600,
			Hint: "obor restart pj-advisor --stop"},
		{Machine: "local", Group: "pj", Pane: "w1:p1", Name: "pj-l-01", Agent: "claude", Status: "done", Age: 3600},
	}
	var b bytes.Buffer
	writeStatus(&b, st, now)
	text := b.String()
	matchLine(t, text, `^agent pj-advisor: done 1h → obor restart pj-advisor --stop$`, "the advisor's line and its hint")
	matchLine(t, text, `^agent pj-l-01: done 1h$`, "the worker's line, no hint")
}

// a role agent serve stopped once its work was done, with no pane running it, is `stopped`: the status line
// `agent <name>: stopped`; a role serve did not stop has no such line.
func TestStatusStoppedRole(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	st := upProject()
	st.Roles = []view.Role{
		{Role: "integrator", Agent: "pj-int", Status: view.RoleStopped},
		{Role: "reviewer", Agent: "pj-rev"},
	}
	var b bytes.Buffer
	writeStatus(&b, st, now)
	text := b.String()
	matchLine(t, text, `^agent pj-int: stopped$`, "the stopped integrator's line")
	if strings.Contains(text, "agent pj-rev") {
		t.Errorf("pj-rev, no pane and not stopped, has a line:\n%s", text)
	}
}

// a role agent the lead stopped (obor restart <agent> --stop), its pane at a shell, is `stopped` for every
// output that reads the field: the view's role row, obor status' text and --json.
func TestStatusStoppedRoleTextJSON(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config(testkit.Set("agents.roles.advisor", ""))
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

// --json's events carry bad only when true: a red gate and a hold do, a green gate whose summary counts a fail
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

// a custom role's agent is a role row of obor status (text and --json) like the advisor's, and obor restart
// <agent> --stop stops it; once its row is removed it is no row and restart does not know it, nothing else changes.
func TestStatusAndStopCustomRole(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Write("docs/agents/designer.md", "# Role: designer\n")
	p.Config(testkit.Set("agents.roles.integrator", "opus"), testkit.Set("agents.roles.designer", `{"model": "opus", "write": ["docs/design/"]}`))
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

	p.Config(testkit.Set("agents.roles.integrator", "opus")) // the row removed
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

// obor status shows an unknown lane's reason on its line, beside the unknown state.
func TestStatusShowsUnknownLaneReason(t *testing.T) {
	why := "git status: exit status 1: fatal: index file smaller than expected"
	line := laneLine(view.Lane{Lane: port.Lane{Name: "L-01", State: "unknown", Reason: why}, Column: "unknown"}, view.Task{})
	if !strings.Contains(line, "unknown") || !strings.Contains(line, "reason: "+why) {
		t.Errorf("lane line %q; want its unknown state and its reason", line)
	}
}

// the queue's text lists the waiting dispatches in the queue's order and marks a high one `high`: the view orders them
// (serve's rank: a high wait before the ordinary ones, then the oldest wait), and the line words each with its
// difficulty and model.
func TestStatusQueueWaitLines(t *testing.T) {
	t.Parallel()
	now := time.Now()
	st := upProject()
	st.Queue.Waiting = []view.WaitItem{
		{Task: "T-3", WaitsOn: view.WaitsLane, High: true, Level: "medium", Types: []string{"sonnet"},
			Difficulty: "medium", Model: "sonnet"},
		{Task: "T-2", WaitsOn: view.WaitsLane, Level: "medium", Types: []string{"sonnet"}, Difficulty: "medium",
			Model: "sonnet"},
		{Task: "T-20", WaitsOn: view.WaitsAfter, After: []string{"T-10"}, Difficulty: "hard", Model: "opus:high"},
	}
	var b bytes.Buffer
	writeStatus(&b, st, now)
	var queued []string
	for _, l := range strings.Split(b.String(), "\n") {
		if strings.HasPrefix(l, "queued ") {
			queued = append(queued, l)
		}
	}
	if len(queued) != 3 || !strings.HasPrefix(queued[0], "queued T-3 (high, medium, sonnet): ") ||
		!strings.HasPrefix(queued[1], "queued T-2 (medium, sonnet): ") ||
		!strings.HasPrefix(queued[2], "queued T-20 (hard, opus:high): waiting (after T-10)") {
		t.Errorf("queued lines %q, want the queue's order with T-3 marked high and each one's difficulty and model", queued)
	}
}

// a keep lane's line says keep, with its queue in dispatch order; a fresh lane's line says neither; a task
// queued on a keep lane is the queue's line "queued <task>: behind <task> on keep lane <lane>".
func TestStatusShowsKeepLaneAndItsQueue(t *testing.T) {
	keep := laneLine(view.Lane{Lane: port.Lane{Name: "L-01", State: "working", Task: "T-1", Mode: config.LaneKeep,
		Agent: "pj-l-01"}, Column: "working", Queue: []string{"T-2", "T-3"}}, view.Task{})
	if !strings.HasSuffix(keep, "  agent: pj-l-01  keep (queue: T-2, T-3)") {
		t.Errorf("keep lane line %q; want its agent, keep and its queue", keep)
	}
	if idle := laneLine(view.Lane{Lane: port.Lane{Name: "L-01", State: "free", Mode: config.LaneKeep}, Column: "free"},
		view.Task{}); !strings.HasSuffix(idle, "  keep") {
		t.Errorf("an idle keep lane's line %q; want keep, no queue", idle)
	}
	if fresh := laneLine(view.Lane{Lane: port.Lane{Name: "L-02", State: "free"}, Column: "free"}, view.Task{}); strings.Contains(fresh, "keep") {
		t.Errorf("a fresh lane's line %q says keep", fresh)
	}
	now := time.Now()
	for _, c := range []struct {
		w    view.WaitItem
		want string
	}{
		{view.WaitItem{Task: "T-3", WaitsOn: view.WaitsLane, Queue: "L-01", Why: "behind T-2 on keep lane L-01"},
			"behind T-2 on keep lane L-01"},
		{view.WaitItem{Task: "T-3", WaitsOn: view.WaitsLane, Queue: "L-01"}, "waiting for keep lane L-01"},
	} {
		if got := waitLine(c.w, view.Task{Task: c.w.Task}, now); got != c.want {
			t.Errorf("%+v: %q, want %q", c.w, got, c.want)
		}
	}
}

// bash status case_no_config_and_usage, the --lead flag of case_two_leads_filter and case_one_lead_unchanged.
func TestStatusExitCodesAndLeadFlag(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.PlainLane("L-01", "task/T-01")
	if rc, _, stderr := run(t, p.Root, nil, "status"); rc != 3 || !strings.Contains(stderr, "not an obor project") {
		t.Errorf("no config: exit %d, %q; want 3, not an obor project", rc, stderr)
	}
	p.Config()
	if rc, _, stderr := run(t, p.Root, nil, "status", "--bogus"); rc != 2 || !strings.Contains(stderr, "usage: obor status [--facts] [--json] [--short]") {
		t.Errorf("obor status --bogus: exit %d, %q; want 2, the usage", rc, stderr)
	}
	for _, gone := range []string{"--herdr", "--lead"} {
		if rc, _, _ := run(t, p.Root, nil, "status", gone); rc != 2 {
			t.Errorf("obor status %s: exit %d; want 2 (removed, T-463)", gone, rc)
		}
	}
}

// obor status shows the idle state: unacknowledged with when it began, or the acknowledgement's why.
func TestStatusShowsIdle(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 4, 9, 5, 0, 0, time.UTC)
	st := upProject()
	st.Idle = &view.Idle{Since: at.Unix(), Last: "T-1 merged abc1234"}
	var b bytes.Buffer
	writeStatus(&b, st, at.Add(time.Hour))
	matchLine(t, b.String(), `^idle, not acknowledged since `+at.Local().Format("Jan 2 15:04")+`$`, "the unacknowledged idle state")

	st.Idle = &view.Idle{Since: at.Unix(), Last: "T-1 merged abc1234", Acked: true, Why: "nothing to dispatch"}
	b.Reset()
	writeStatus(&b, st, at.Add(time.Hour))
	matchLine(t, b.String(), `^idle: nothing to dispatch$`, "the acknowledged idle state")
	if strings.Contains(b.String(), "not acknowledged") {
		t.Errorf("an acknowledged idle state shows as unacknowledged:\n%s", b.String())
	}
}

// Serve up: its start and age, each lane with serve's task state and agent, serve's open tasks (not dropped) and agents
// on no lane, each
// needs-you with the commands serve says answer it (a question obor resume; a red gate obor resume --regate or --sendback, or drop; a
// reject obor drop only; a stalled worker its pane; a loop error none; a line waiting a day none), the lines not typed
// yet, the last full check and its age, the pause.
func TestStatusOverServe(t *testing.T) {
	t.Parallel()
	p := testkit.NewDir(t)
	g := newLaneGit(t)
	g.lane(p, "L-01", "task/T-01")
	g.lane(p, "L-02", "")
	p.Config()
	now := time.Now()
	p.Write(".obor/state/last-full-check", "abcdef0123 "+strconv.FormatInt(now.Unix()-3*3600, 10)+" RED failed: e2e; 120s\n")
	p.Write(".obor/pause", "-\nold file\n") // no reader; serve's pause is the one
	since := now.Add(-2 * time.Hour).UTC()
	ss := wire.Status{Since: since.Format(time.RFC3339), Connected: true, Project: &wire.ProjectStatus{
		Name: "pj", Root: p.Root, Merging: true, Paused: &event.Pause{Why: "quota", Caller: "pj-lead"},
		Agents: []wire.AgentStatus{{Pane: "w1:p1", Name: "pj-l1", Agent: "claude", Lane: "L-01", Task: "T-01", Status: "idle"},
			{Pane: "w1:p9", Name: "pj-int", Agent: "claude", Status: "working", Age: 300}},
		Tasks: []wire.TaskStatus{{Task: "T-01", Lane: "L-01", Attempt: 1, State: "working", Phase: wire.PhaseNeedsYou},
			{Task: "T-05", Lane: "L-09", Attempt: 2, State: "needs-you", Phase: wire.PhaseNeedsYou},
			{Task: "T-04", Lane: "L-09", State: "dropped", Ended: true, Phase: wire.PhaseDropped},
			{Task: "T-09", Attempt: 1, State: "waiting", Waiting: true, Rank: 1, After: []string{"T-03"},
				Why: "dispatch T-09: lane L-01 is working on T-01"}},
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
		Answers: map[int64]string{ // serve's, from each task's state; a loop error, a line waiting: none
			1: `obor resume T-01 "<answer>"`, 2: `obor resume T-05 --regate | obor resume T-05 --sendback "<why>" | obor drop T-05 "<why>"`,
			3: `obor drop L-08 "<why>"`, 4: `its pane, or obor drop L-07 "<why>"`, 5: `obor resume T-08 | obor drop L-06 "<why>"`}}}
	st, text := statusWith(t, p, g, ss, nil, now)
	if !st.Serve.Up || len(st.NeedsYou) != 7 || len(st.Messages) != 2 {
		t.Fatalf("precondition: serve up, 7 needs-you, 2 lines: %+v", st)
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if want := "serve: up since " + since.Local().Format("Jan 2 15:04") + " (2h)"; lines[0] != want {
		t.Errorf("first line %q, want %q", lines[0], want)
	}
	matchLine(t, text, `^L-01 +- +task/T-01 +needs-you +- +\(T-01\)  serve: working \(attempt 1\)  agent: pj-l1 \(idle\)$`, "L-01")
	matchLine(t, text, `^L-02 +- +- +free +- +\(-\)  agent: pj-l-02$`, "L-02")
	for _, want := range []string{
		"task T-05 (L-09): needs-you (attempt 2)",
		"queued T-09: waiting (after T-03): dispatch T-09: lane L-01 is working on T-01",
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

	// the terminal not connected; the event log broken.
	ss.Connected = false
	ss.Project = &wire.ProjectStatus{Name: "pj", Root: p.Root, Merging: true, LogError: "disk full"}
	_, text = statusWith(t, p, g, ss, nil, now)
	matchLine(t, text, `^serve: up since .*; terminal: not connected$`, "terminal down")
	matchLine(t, text, `^serve: event log broken: disk full$`, "log error")
	ss.Project = nil
	_, text = statusWith(t, p, g, ss, nil, now)
	matchLine(t, text, `^serve: up since .*; this project is not loaded \(obor serve's output says why\); terminal: not connected$`, "not loaded")
}

// Serve down: one line saying so, first, the lanes from the store and git alone (no phase, so a submitted lane
// is working, with its task_done's age; an opencode
// lane without its plugin marker warned), no agents and no role rows (nothing stale), no full check yet; herdr
// is never asked.
func TestStatusServeDown(t *testing.T) {
	t.Parallel()
	p := testkit.NewDir(t)
	g := newLaneGit(t)
	g.lane(p, "L-01", "task/T-01")
	g.lane(p, "L-02", "task/T-02")
	g.lane(p, "L-03", "task/T-03")
	p.Config(testkit.Set("agents.models.oc", `{"kind":"opencode","id":"go/x"}`))
	srv := herdrtest.Serve(t, p.Herdr())
	p.SubmitAgo("L-01", 730)
	p.Submit("L-02")
	p.State("L-03", map[string]any{"task": "T-03", "branch": "task/T-03", "type": "oc"}) // its dispatch runs the opencode oc
	st, text := statusWith(t, p, g, wire.Status{}, errors.New("obor serve is not running (dial: refused)"), time.Now())
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
		`^L-03 +oc +task/T-03 +working +- +\(T-03\)  warning: no-plugin  agent: pj-l-03$`,
		`^nightly: off \(obor nightly on HH:MM\)$`, // merge.nightly_at unset
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

// a store and a served status with one item of each queue kind — the merge loop in serve's order (a review
// job out and not fetched, two submitted tasks waiting for the conflict that holds the scratch, as serve's scheduler
// says), dispatches waiting on --after tasks, on a lane
// of their level and a spec never dispatched, the review request, the lead's watch with its note, a line and a
// brief not done, the pause and the nightly check — give --json's typed arrays (with the role rows, a lane's spec rev
// and open job, and the worded events with their seq) and one plain text line each.
func TestStatusQueue(t *testing.T) {
	t.Parallel()
	p := testkit.NewDir(t)
	g := newLaneGit(t)
	g.lane(p, "L-01", "task/T-10")
	p.Config(testkit.Set("nightly.at", "03:00"))
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
			{Task: "T-12", Attempt: 1, State: "waiting", Waiting: true, Rank: 1, Phase: wire.PhaseWaiting,
				After: []string{"T-11"}, Difficulty: "hard", Model: "opus:high"},
			{Task: "T-14", Attempt: 1, State: "waiting", Waiting: true, Rank: 2, Phase: wire.PhaseWaiting,
				Level: "medium", Types: []string{"sonnet"}, Difficulty: "medium", Model: "sonnet"},
			{Task: "T-9", Lane: "L-03", Attempt: 1, State: "awaiting-accept", Phase: wire.PhaseReviewing, Stage: wire.StageReview, Job: "T-9/1/review"},
		},
		// serve's scheduler: the conflict T-1000 holds the scratch, so T-100 and T-11 (in the pass's
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
	st, text := statusWith(t, p, g, ss, nil, now)
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var js struct {
		Lanes []struct {
			Lane string  `json:"lane"`
			Task *string `json:"task"`
		} `json:"lanes"`
		Tasks map[string]struct { // the sections name a task by id, its fields are here
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
	// --json's merge items have no next field (no prediction field; the nightly's next run stays).
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
		`^queued T-12 \(hard, opus:high\): waiting \(after T-11\)$`, // difficulty and model, after stays
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

// the effort a task's dispatch named and the model its lane row holds are on its line and in
// --json (lanes[].effort, lanes[].type); a task without them shows none, as does a free lane.
func TestStatusShowsEffortAndType(t *testing.T) {
	t.Parallel()
	now := time.Now()
	st := upProject()
	st.Lanes = []view.Lane{
		{Lane: port.Lane{Name: "L-01", Type: "sonnet", Branch: "task/T-01", State: "working", Task: "T-01",
			Agent: "pj-l-01"}, Column: "working"},
		{Lane: port.Lane{Name: "L-02", Branch: "task/T-02", State: "working", Task: "T-02", Agent: "pj-l-02"},
			Column: "working"},
		{Lane: port.Lane{Name: "L-03", State: "free"}, Column: "free"},
	}
	st.Tasks = map[string]view.Task{
		"T-01": {Task: "T-01", Lane: "L-01", TaskState: "working", Attempt: 1, Effort: "high", Open: true},
		"T-02": {Task: "T-02", Lane: "L-02", TaskState: "working", Attempt: 1, Open: true},
	}
	var b bytes.Buffer
	writeStatus(&b, st, now)
	text := b.String()
	matchLine(t, text, `^L-01 +sonnet .*serve: working \(attempt 1\)  effort: high  agent: pj-l-01$`, "L-01 of type sonnet at effort high")
	matchLine(t, text, `^L-02 +- .*serve: working \(attempt 1\)  agent: pj-l-02$`, "L-02: no model, no effort")
	matchLine(t, text, `^L-03 +- `, "L-03 free: no model")
	js, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), `"type":"sonnet"`) || !strings.Contains(string(js), `"effort":"high"`) {
		t.Errorf("--json lanes %s: want L-01's model sonnet and the task's effort high", js)
	}
}

// a working Claude lane whose worktree has no enabled obor plugin is warned no-plugin, as an opencode lane
// without its marker is; the testkit's lanes have the plugin enabled.
func TestStatusWarnsAClaudeLaneWithoutThePlugin(t *testing.T) {
	t.Parallel()
	p := testkit.NewDir(t)
	g := newLaneGit(t)
	g.lane(p, "L-01", "task/T-01")
	o2 := g.lane(p, "L-02", "task/T-02")
	p.Config()
	// The rows hold their tasks, not landed: working; no agent recorded.
	p.State("L-01", map[string]any{"task": "T-01", "branch": "task/T-01"})
	p.State("L-02", map[string]any{"task": "T-02", "branch": "task/T-02"})
	if err := os.Remove(filepath.Join(o2, ".claude", "settings.json")); err != nil {
		t.Fatalf("precondition: L-02 had the settings file: %v", err)
	}
	_, text := statusWith(t, p, g, wire.Status{}, errors.New("obor serve is not running (dial: refused)"), time.Now())
	matchLine(t, text, `^L-02 +- +task/T-02 +working +- +\(T-02\)  warning: no-plugin  agent: pj-l-02$`, "L-02 without the plugin")
	if strings.Contains(text, "L-01 ") && strings.Contains(strings.SplitN(strings.SplitN(text, "L-01 ", 2)[1], "\n", 2)[0], "no-plugin") {
		t.Errorf("L-01 (plugin enabled) warned:\n%s", text)
	}
}

// backgroundView is a literal project with a lane agent running a recorded background call, a role agent running an
// unrecorded long command (a foreground one) and the lead running a recorded one with no description.
func backgroundView() view.Project {
	st := upProject()
	st.Lanes = []view.Lane{{Lane: port.Lane{Name: "L-01", Branch: "task/T-01", State: "working", Task: "T-01",
		Agent: "pj-l-01"}, Column: "working",
		Background: []view.Background{{PID: 42, Command: "python convert.py", Background: true, Label: "convert maps",
			Age: 900}}}}
	st.Agents = []wire.AgentStatus{{Pane: "w1:p2", Name: "pj-int", Status: "idle", Age: 60},
		{Pane: "w1:p3", Name: "pj-lead", Status: "working", Age: 60}}
	st.Roles = []view.Role{{Role: "integrator", Agent: "pj-int", Status: "idle", Age: 60,
		Background: []view.Background{{PID: 43, Command: "make -s check", Age: 95}}}}
	st.Lead = &view.Role{Role: "lead", Agent: "pj-lead", Status: "working", Age: 60,
		Background: []view.Background{{PID: 44, Command: "sleep 600", Background: true, Age: 45}}}
	return st
}

// obor status shows each agent's long processes on a line under its own — the lane's under the lane
// line, a role's and the lead's under their agent line — by description, else command, with the age: bg for a
// recorded background call, running for another.
func TestStatusShowsBackground(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	writeStatus(&b, backgroundView(), time.Now())
	text := b.String()
	for _, pair := range [][2]string{{"L-01 ", "  bg: convert maps · 15m"}, {"agent pj-int:", "  running make -s check · 1m"},
		{"agent pj-lead:", "  bg: sleep 600 · 45s"}} {
		i := strings.Index(text, pair[0])
		if i < 0 {
			t.Fatalf("precondition: no line %q in\n%s", pair[0], text)
		}
		rest := text[i:]
		next := rest[strings.Index(rest, "\n")+1:]
		if !strings.HasPrefix(next, pair[1]+"\n") {
			t.Errorf("under %q: %q, want %q in\n%s", pair[0], strings.SplitN(next, "\n", 2)[0], pair[1], text)
		}
	}
}

// goldenStatus is a project with a task of every kind the view knows: one on a lane at work, one merging
// one waiting for a lane, one waiting --after, one needing you on no lane and one only
// specified, with serve's answer for it; and the view's JSON and text of it at an hour after now.
func goldenStatus(t *testing.T) (string, string) {
	t.Helper()
	p := testkit.NewDir(t)
	g := newLaneGit(t)
	g.lane(p, "L-01", "task/T-01")
	g.lane(p, "L-02", "task/T-02")
	g.lane(p, "L-03", "")
	p.Config()
	p.Dispatched("L-01")
	p.Submit("L-02")
	for _, id := range []string{"T-01", "T-02", "T-03", "T-04", "T-05", "T-06"} {
		p.Spec(id, "# "+id+": the "+id+" title\n\nbody\n")
	}
	ss := wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "pj", Root: p.Root, Merging: true,
		Agents: []wire.AgentStatus{{Pane: "w1:p1", Name: "pj-l-01", Agent: "claude", Lane: "L-01", Task: "T-01",
			Status: "working"}},
		Tasks: []wire.TaskStatus{
			{Task: "T-01", Lane: "L-01", Attempt: 1, State: "dispatched", Phase: wire.PhaseWorking, Effort: "high",
				Job: "T-01/1/work", Delivered: true},
			{Task: "T-02", Lane: "L-02", Attempt: 2, State: "accepted", Phase: wire.PhasePublish,
				Stage: wire.StagePublish, Job: "T-02/2/review"},
			{Task: "T-03", State: "waiting", Waiting: true, Rank: 1, Phase: wire.PhaseWaiting, Level: "medium",
				Types: []string{"opus"}, Why: app.LaneWaitFree},
			{Task: "T-04", State: "waiting", Waiting: true, Rank: 2, Phase: wire.PhaseWaiting, After: []string{"T-02"}},
			{Task: "T-05", Lane: "L-09", Attempt: 1, State: "needs-you", Phase: wire.PhaseNeedsYou},
		},
		ScratchQueue: []wire.ScratchWait{{Task: "T-02", WaitsOn: "T-01", Why: wire.ScratchHeld}},
		Facts:        []event.Record{fact(t, 1, "T-05", event.FactData{Lane: "L-09", Reason: "asks: which file?"})},
		Answers:      map[int64]string{1: `obor resume T-05 "<answer>"`},
		Idle:         &wire.IdleStatus{Since: 100, Last: "T-00", Acked: true, Why: "done"},
		ReviewRequest: &wire.ReviewRequestStatus{After: []string{"T-02"}, Waiting: []string{"T-02"}, Note: "after",
			By: "pj-lead", Since: 200},
	}}
	now := time.Now().Add(time.Hour)
	st, text := statusWith(t, p, g, ss, nil, now)
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return scrubGolden(string(b), p), scrubGolden(text, p)
}

// scrubGolden replaces p's paths and the clock's numbers and times in s with fixed words.
func scrubGolden(s string, p *testkit.Project) string {
	s = strings.ReplaceAll(s, p.Base, "$BASE")
	s = strings.ReplaceAll(s, p.Root, "$ROOT")
	s = regexp.MustCompile(`"(since|done_age|next|ts|at)": "?[0-9TZ:+-]+"?`).ReplaceAllString(s, `"$1": 0`)
	s = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ`).ReplaceAllString(s, "TS")
	s = regexp.MustCompile(` +\d+[smhd]  \(T-`).ReplaceAllString(s, "  age  (T-")
	s = regexp.MustCompile(`in \d+[smhd]\)`).ReplaceAllString(s, "in N)")
	s = regexp.MustCompile(`at \d\d:\d\d`).ReplaceAllString(s, "at HH:MM")
	return s
}

// updateGolden is OBOR_UPDATE_GOLDEN set, read before TestMain's testkit.Isolate clears obor's environment.
var updateGolden = os.Getenv("OBOR_UPDATE_GOLDEN") != ""

// `obor status --json` and its text, against testdata/status.json.golden and status.txt.golden
// (OBOR_UPDATE_GOLDEN=1 rewrites them).
func TestStatusGolden(t *testing.T) {
	js, text := goldenStatus(t)
	for _, g := range []struct{ file, got string }{{"status.json.golden", js}, {"status.txt.golden", text}} {
		path := filepath.Join("testdata", g.file)
		if updateGolden {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(g.got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal([]byte(g.got), want) {
			t.Errorf("%s differs (OBOR_UPDATE_GOLDEN=1 rewrites it); got:\n%s", g.file, g.got)
		}
	}
}

// a model paused with and without --for is one line per model beside the project's pause, and a model serve does not
// name (its pause cleared) has no line.
func TestStatusListsModelPauses(t *testing.T) {
	t.Parallel()
	now := time.Now()
	until := now.Add(2 * time.Hour).Unix()
	st := upProject()
	st.Paused = &view.Pause{Reason: "quota", By: "pj-lead"}
	st.Queue.Pause = st.Paused
	st.ModelPauses = map[string]*view.Pause{
		"sonnet":   {Reason: "quota", By: "pj-lead"},
		"deepseek": {Reason: "down", Until: &until, By: "pj-lead"},
	}
	var b bytes.Buffer
	writeStatus(&b, st, now)
	text := b.String()
	matchLine(t, text, `^paused sonnet until cleared \(quota\)$`, "sonnet")
	matchLine(t, text, `^paused deepseek until `+time.Unix(until, 0).Format("15:04")+` \(down\)$`, "deepseek")
	if i, j, k := strings.Index(text, "paused: quota"), strings.Index(text, "paused deepseek"),
		strings.Index(text, "paused sonnet"); i < 0 || j < i || k < j {
		t.Errorf("pause lines out of order (project's first, then models sorted):\n%s", text)
	}
	st.ModelPauses = nil
	b.Reset()
	writeStatus(&b, st, now)
	if strings.Contains(b.String(), "paused sonnet") || strings.Contains(b.String(), "paused deepseek") {
		t.Errorf("a cleared model still has a line:\n%s", b.String())
	}
}
