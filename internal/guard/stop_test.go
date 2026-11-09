package guard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/background"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/testkit"
)

type hfx struct{ *testkit.Project }

// newHfx is a plain project laid out as files (testkit.NewPlain): the guard reads the repository (project.Find) but
// runs no git. newGitHfx is for the test of a git read (the stop hook's lane change time, project.NewestChange).
func newHfx(t *testing.T) hfx { return hfx{testkit.NewPlain(t)} }

func newGitHfx(t *testing.T) hfx { return hfx{testkit.New(t)} }

// result is one Stop hook run: its stdout (the verdict) and stderr.
type result struct {
	t        *testing.T
	out, err string
}

// stop runs the Stop hook with OBOR_ROLE=role ("-" = unset) on an event in cwd plus the extra event fields (a JSON
// object text); env adds VAR=value (OBOR_AGENT=…).
func (f hfx) stop(t *testing.T, role, cwd, extra string, env ...string) result {
	t.Helper()
	ev := map[string]any{"session_id": "s1", "hook_event_name": "Stop", "cwd": cwd, "stop_hook_active": false}
	if extra != "" {
		var x map[string]any
		if err := json.Unmarshal([]byte(extra), &x); err != nil {
			t.Fatal(err)
		}
		for k, v := range x {
			ev[k] = v
		}
	}
	if role != "-" {
		env = append(env, "OBOR_ROLE="+role)
	}
	b, _ := json.Marshal(ev)
	var out, errw bytes.Buffer
	Stop(b, f.Env(env...), &out, &errw)
	return result{t, out.String(), errw.String()}
}

// reason is the block reason ("" when the hook allowed).
func (r result) reason() string {
	if r.out == "" {
		return ""
	}
	var v struct{ Decision, Reason string }
	if err := json.Unmarshal([]byte(r.out), &v); err != nil || v.Decision != "block" {
		r.t.Fatalf("not a block verdict: %q", r.out)
	}
	return v.Reason
}

func (r result) block(what string) result {
	r.t.Helper()
	if r.out == "" {
		r.t.Errorf("%s: allowed, want block (stderr %q)", what, r.err)
	} else {
		r.reason()
	}
	return r
}

func (r result) allow(what string) result {
	r.t.Helper()
	if r.out != "" {
		r.t.Errorf("%s: blocked, want allow: %s", what, r.out)
	}
	return r
}

func (r result) has(what string, needles ...string) result {
	r.t.Helper()
	for _, n := range needles {
		if !strings.Contains(r.reason(), n) {
			r.t.Errorf("%s: no %q in the reason:\n%s", what, n, r.reason())
		}
	}
	return r
}

func (r result) hasNot(what string, needles ...string) result {
	r.t.Helper()
	for _, n := range needles {
		if strings.Contains(r.reason(), n) {
			r.t.Errorf("%s: %q in the reason:\n%s", what, n, r.reason())
		}
	}
	return r
}

// nudges is the old nudge ledger: the hook writes none (T-465).
func (f hfx) nudges() string { return f.Root + "/.obor/state/nudges" }

// state writes lane L-01's row (task T-01, agent pj-l1, base main) dispatched at.
func (f hfx) state(at int64) {
	f.State("L-01", `{"lane":"L-01","task":"T-01","branch":"task/T-01","agent":"pj-l1","base":"main","dispatched_at":`+
		strconv.FormatInt(at, 10)+`,"allowed":["src/**"]}`)
}

func ago(secs int64) int64 { return time.Now().Unix() - secs }

func (f hfx) integrator() { f.Config(testkit.Set("agents.roles", `{"integrator": "opus"}`)) }

// doneLane: lane L-04 on task/T-04 with its task_done (the commit a real lane has is no part of the row).
func (f hfx) doneLane() string {
	o4 := f.PlainLane("L-04", "task/T-04")
	f.Submit("L-04")
	return o4
}

// transcript writes a Claude transcript whose last main-chain assistant line has context ctx (input 10 + cache read
// ctx-1010 + cache creation 1000), after an older line with a small context.
func transcript(f hfx, file string, ctx int64) string {
	f.Write(file, `{"type":"assistant","timestamp":"2026-10-13T10:00:00.000Z","message":{"model":"m","usage":{"input_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":0}}}`+"\n"+
		`{"type":"assistant","timestamp":"2026-10-13T10:05:00.000Z","message":{"model":"m","usage":{"input_tokens":10,"cache_read_input_tokens":`+
		strconv.FormatInt(ctx-1010, 10)+`,"cache_creation_input_tokens":1000}}}`+"\n")
	return file
}

func session(sid, tr string) string {
	return `{"session_id":"` + sid + `","transcript_path":"` + tr + `"}`
}

// T-232 (replaces T-175's case_lead_handoff_*): no Stop block for context size; the reminder rides on each prompt
// (prompt_test.go). Above the limit the lead stops freely, and nothing is written.
func TestStopAboveHandoffLimitNoBlock(t *testing.T) {
	t.Parallel()
	f := newHfx(t)
	f.PlainLane("L-01", "task/T-01")
	f.Config(testkit.Set("agents.handoff_tokens", 300000))
	tr := transcript(f, f.Base+"/projects/p/s1.jsonl", 900000)
	if ctx, _, _, ok := project.ContextTokens(tr); !ok || ctx != 900000 {
		t.Fatalf("precondition: transcript context %d %v, want 900000", ctx, ok)
	}
	f.stop(t, "lead", f.Root, session("s1", tr)).allow("a lead at 900k > 300k")
	f.stop(t, "lead", f.Root, session("s2", tr), "OBOR_AGENT=pj-lead").allow("another lead session at 900k")
	if f.Exists(f.nudges()) {
		t.Error("a nudge ledger written")
	}
}

// bash stop case_worker_handoff_unaffected, case_worker_background_task; T-437: a worker's stop before its task_done.
// One block per stop loop (stop_hook_active true allows, its look-alikes are a new loop); a running background task
// allows, a finished one does not; at 900k context the block is still the task_done one (a worker gets no handoff
// block); its task_done allows the stop, and a new dispatch blocks again.
func TestStopWorkerBlockRows(t *testing.T) {
	t.Parallel()
	f := newGitHfx(t) // one case runs with a background task: the hook reads the lane's newest change (git)
	o1 := f.Lane("L-01", "task/T-01")
	f.Config()
	f.state(ago(100))
	f.Dispatched("L-01")
	for _, c := range []struct {
		name  string
		extra string
		block bool
	}{
		{"a new loop", `{"stop_hook_active": false}`, true},
		{"the same loop: the worker asked", `{"stop_hook_active": true}`, false},
		{"stop_hook_active null is a new loop", `{"stop_hook_active": null}`, true},
		{`stop_hook_active "true" is not active`, `{"stop_hook_active": "true"}`, true},
		{"a finished background task", `{"background_tasks":[{"id":"b1","type":"shell","status":"completed","command":"x"}]}`, true},
		{"a running background task", `{"background_tasks":[{"id":"b2","type":"shell","status":"running","command":"make check"}]}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := f.stop(t, "-", o1, c.extra)
			if c.block {
				r.block(c.name)
				return
			}
			r.allow(c.name)
		})
	}
	tr := transcript(f, f.Base+"/projects/p/w1.jsonl", 900000)
	f.stop(t, "-", o1, session("w1", tr)).block("a worker at 900k without its task_done").
		hasNot("the worker's own block", "context is")
	f.Submit("L-01")
	f.stop(t, "-", o1, session("w1", tr)).allow("a worker at 900k with its task_done")
	f.Dispatched("L-01")
	f.stop(t, "-", o1, "").block("a new attempt awaits its task_done")
}

// T-437: a worker whose task awaits its task_done (dispatched in the event log, no submission) is blocked with the
// hint, the sections named; the hook writes nothing (no nudges ledger, the lane row as it was).
func TestStopWorkerNoTaskDoneBlocks(t *testing.T) {
	t.Parallel()
	f := newHfx(t)
	o1 := f.PlainLane("L-01", "task/T-01")
	f.Config()
	f.state(ago(100))
	f.Dispatched("L-01")
	before, _ := f.Row("L-01")
	f.stop(t, "-", o1, "").block("no task_done yet").
		has("reason", "no task_done for T-01 yet", "call the obor task_done tool", "report argument",
			"Summary, Self-check, Skipped, Open questions", "call task_done with a Blocked: report naming what you need", "Already sent one? Stop again.").
		hasNot("no done file text", "done file", ".task-done.md")
	if f.Exists(f.nudges()) {
		t.Errorf("the worker hook wrote the nudges ledger: %s", f.Read(f.nudges()))
	}
	if after, _ := f.Row("L-01"); after != before {
		t.Errorf("the lane row changed: %+v, was %+v", after, before)
	}
}

// T-894 (replaces T-558's and T-560's own fold of the log): the Stop hook reads the lane row's blocked, serve's
// fold's answer (lanerows_test.go), so with serve down the row still says whether a question is in force. A question
// in force allows the stop; the fold's ended question blocks again; a drive's answer, a dispatch and a sendback leave
// the question in force (the SQL this replaces ended it on each), and an unblocked ends it.
func TestStopWorkerBlockedRowRows(t *testing.T) {
	t.Parallel()
	type fact struct {
		task, step, kind string
		data             any
	}
	for _, c := range []struct {
		name    string
		blocked bool // the row's blocked: serve's fold's answer
		events  []fact
		block   bool
	}{
		{"a question in force", true, []fact{{"T-01", "needs-you", "fact", map[string]string{"reason": "asks: need help"}}}, false},
		{"the question answered", false, []fact{{"T-01", "needs-you", "fact", map[string]string{"reason": "asks: need help"}},
			{"msg", "answered", "fact", map[string]any{"task": "T-01"}}}, true},
		{"a drive's answer answers no question", true, []fact{{"T-01", "needs-you", "fact", map[string]string{"reason": "asks: need help"}},
			{"msg", "answered", "fact", map[string]any{"task": "T-01", "driver": true}}}, false},
		{"a dispatch leaves it in force", true, []fact{{"T-01", "needs-you", "fact", map[string]string{"reason": "asks: need help"}},
			{"T-01", "dispatch", "fact", nil}}, false},
		{"a sendback leaves it in force", true, []fact{{"T-01", "needs-you", "fact", map[string]string{"reason": "asks: need help"}},
			{"T-01", "sendback", "fact", nil}}, false},
		{"an unblocked ends it", false, []fact{{"T-01", "needs-you", "fact", map[string]string{"reason": "asks: need help"}},
			{"T-01", "unblocked", "fact", nil}}, true},
		{"serve down: the row set", true, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newHfx(t)
			o1 := f.PlainLane("L-01", "task/T-01")
			f.Config()
			row := map[string]any{"task": "T-01", "branch": "task/T-01", "agent": "pj-l1", "base": "main",
				"dispatched_at": ago(100), "blocked": c.blocked}
			f.State("L-01", row)
			for _, e := range c.events {
				f.appendEvent(t, e.task, e.step, e.kind, e.data)
			}
			if got := f.stop(t, "-", o1, ""); c.block {
				got.block(c.name).has("reason", "no task_done for T-01 yet")
			} else {
				got.allow(c.name)
			}
		})
	}
}

// appendEvent writes one event of task (attempt 1) with data to the project's event log, as serve records it: the
// lane projection is not updated (the caller states its row).
func (f hfx) appendEvent(t *testing.T, task, step, kind string, data any) {
	t.Helper()
	db, err := store.OpenRW(store.File(f.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO events (seq, ts, task, attempt, step, kind, class, data)
		VALUES ((SELECT COALESCE(MAX(seq), 0) + 1 FROM events), ?, ?, 1, ?, ?, '', ?)`,
		time.Now().UTC().Format(time.RFC3339), task, step, kind, string(b)); err != nil {
		t.Fatal(err)
	}
}

// appendFact is appendEvent of a fact.
func (f hfx) appendFact(t *testing.T, task, step string, data any) {
	t.Helper()
	f.appendEvent(t, task, step, "fact", data)
}

// T-437, T-439: no task to finish → allow: no lane row (not a lane serve dispatched), or a row whose task_done is in
// (finished); the lanes table unreadable → allow, saying so.
func TestStopWorkerNoServeTaskAllows(t *testing.T) {
	t.Parallel()
	f := newHfx(t)
	o1 := f.PlainLane("L-01", "task/T-01")
	f.Config()
	f.Dispatched("L-01")
	f.stop(t, "-", o1, "").block("precondition: the dispatched lane's row awaits task_done")
	f.ClearState("L-01")
	f.stop(t, "-", o1, "").allow("no row: not a dispatched lane")
	f.State("L-01", `{"lane":"L-01","task":"T-09","agent":"pj-l1","finished":1}`)
	f.stop(t, "-", o1, "").allow("a row whose task_done is in")
	f.State("L-01", `{"lane":"L-01","task":"T-09","agent":"pj-l1"}`)
	f.stop(t, "-", o1, "").block("precondition: a row awaiting task_done blocks")
	f.Write(".obor/state/obor.db", "not a database\n")
	f.stop(t, "-", o1, "").allow("the lanes table unreadable: allow (the hook says so on stderr)")
}

// bash stop case_worker_background_task_stale (T-167); T-437: once per stop loop, no ledger.
func TestStopWorkerStaleBackgroundTasks(t *testing.T) {
	t.Parallel()
	f := newGitHfx(t) // the stale read is project.NewestChange: git log and ls-files
	o1 := f.Lane("L-01", "task/T-01")
	f.Config()
	f.state(ago(100))
	f.Dispatched("L-01")
	bg := `{"background_tasks":[{"id":"b1","type":"shell","status":"running","command":"cat <<EOF"}]}`
	old := "@" + strconv.FormatInt(ago(4000), 10)
	f.Write(o1+"/work.txt", "work\n")
	f.Git(o1, "add", "work.txt")
	f.GitEnv([]string{"GIT_COMMITTER_DATE=" + old, "GIT_AUTHOR_DATE=" + old}, o1, "commit", "-q", "-m", "old")
	// One mtime for all: a second ticking between two ages would make the marker the newest change.
	aged := time.Unix(ago(4000), 0)
	for _, file := range strings.Fields(f.Git(o1, "ls-files", "-co", "--exclude-standard")) {
		os.Chtimes(filepath.Join(o1, file), aged, aged)
	}
	// The lane-root run marker (internal/run; gitignored in a real project, aged here so it is no lane change).
	mark := o1 + "/.task-run"
	run, stopRun := testkit.Sleeper(t)
	f.Write(mark, strconv.Itoa(run)+"\n")
	os.Chtimes(mark, aged, aged)
	f.stop(t, "-", o1, bg).allow("stale lane + live obor run marker")
	stopRun()
	f.stop(t, "-", o1, bg).block("stale lane + a marker whose pid is gone").
		has("reason", "Your background tasks look stale: check them (/ps)", "no task_done for T-01 yet")
	os.Remove(mark)
	if f.Exists(f.nudges()) {
		t.Errorf("a stale block wrote the ledger: %s", f.Read(f.nudges()))
	}
	f.stop(t, "-", o1, strings.TrimSuffix(bg, "}")+`,"stop_hook_active":true}`).allow("the same loop again")
	testkit.Age(t, o1+"/work.txt", 0)
	f.stop(t, "-", o1, bg).allow("a fresh lane change")
}

// T-1142: while a background Bash call recorded for this session still runs (its process in the table, project's
// record), every agent's stop — the worker's, a role agent's, the lead's — is blocked with the call's description, and
// stop_hook_active blocks too: the agent is waiting, and the block ends with the call. Its process ended, another
// session's or another agent's call, no record, and an unreadable record file allow the stop. T-1143 (RV-169 #1): only
// the stopping agent's own processes count — the agent that ran the hook (its ancestry) and that agent's descendants:
// another agent's process running the same command, with this agent's own run alive or ended, allows the stop, and so
// does a hook whose agent process cannot be found.
func TestStopBackgroundRunStillGoing(t *testing.T) {
	f := newHfx(t)
	o1 := f.PlainLane("L-01", "task/T-01")
	f.Config()
	oldProcs, oldSelf := stopProcs, stopSelf
	t.Cleanup(func() { stopProcs, stopSelf = oldProcs, oldSelf })
	stopSelf = func() int { return 601 }
	// The hook (601) runs in its shell (600) under this agent's CLI (500); another agent's CLI is 800 (its hook 850).
	agents := []background.Proc{{PID: 500, PPID: 1, Age: time.Hour, Command: "/usr/local/bin/claude"},
		{PID: 600, PPID: 500, Age: 0, Command: "/bin/sh -c OBOR_NO_SELF_HOST=1 bash obor hook stop"},
		{PID: 601, PPID: 600, Age: 0, Command: "/Users/x/.obor/bin/obor hook stop"},
		{PID: 800, PPID: 1, Age: time.Hour, Command: "/usr/local/bin/claude"},
		{PID: 850, PPID: 800, Age: 0, Command: "/Users/x/.obor/bin/obor hook stop"}}
	run := func(agent, pid int, command string) []background.Proc { // a Bash call's shell and its process
		return []background.Proc{{PID: pid, PPID: agent, Age: 6 * time.Second, Command: "/bin/zsh -c eval '" + command + "'"},
			{PID: pid + 1, PPID: pid, Age: 6 * time.Second, Command: command}}
	}
	procs := func(runs ...[]background.Proc) {
		table := append([]background.Proc(nil), agents...)
		for _, r := range runs {
			table = append(table, r...)
		}
		stopProcs = func(context.Context) ([]background.Proc, error) { return table, nil }
	}
	record := func(c project.BackgroundCall) {
		os.Remove(project.BackgroundFile(f.Root))
		if c.Command != "" {
			project.AppendBackground(f.Root, c)
		}
	}
	call := project.BackgroundCall{At: ago(6), Agent: "pj-l-01", Session: "s1", Pane: "w1:p1",
		Description: "bench the build", Command: "go test -bench . ./..."}
	mine, theirs := run(500, 700, call.Command), run(800, 810, call.Command)

	record(project.BackgroundCall{})
	procs(mine)
	f.stop(t, "-", o1, "").allow("no background record")

	record(call)
	f.stop(t, "-", o1, "").block("a live recorded call").has("reason", "a background run you started is still running",
		"bench the build", "started ", " ago", "wait for it in this turn")
	f.stop(t, "-", o1, `{"stop_hook_active":true}`).block("the same loop again: the agent is waiting").
		has("reason", "bench the build")
	procs(mine, theirs)
	f.stop(t, "-", o1, "").block("its own run alive beside another agent's").has("reason", "bench the build")

	procs()
	f.stop(t, "-", o1, "").allow("the call's process ended")
	procs(theirs)
	f.stop(t, "-", o1, "").allow("own run ended, another agent's same command alive")
	stopSelf = func() int { return 850 } // precondition: the same table, from the other agent's side, is live
	f.stop(t, "-", o1, "").block("the other agent's own run, seen from it").has("reason", "bench the build")
	stopSelf = func() int { return 601 }

	procs(mine)
	stopSelf = func() int { return 999 }
	f.stop(t, "-", o1, "").allow("the hook's agent process not found")
	stopSelf = func() int { return 601 }

	other := call
	other.Session = "s2"
	record(other)
	f.stop(t, "-", o1, "").allow("another session's call")
	other = call
	other.Agent = "pj-l2"
	record(other)
	f.stop(t, "-", o1, "").allow("another agent's call")

	other = call
	other.Agent = "" // recorded in the session's pane: still this session's
	record(other)
	f.stop(t, "-", o1, "", "OBOR_PANE=w1:p1").block("a call recorded without an agent in our pane").
		has("reason", "bench the build")

	// T-1147 (RV-169 verify): a run whose shell started 5 s after the agent (within the view's helper slack of its
	// start) still blocks: the helper filter is the view's alone.
	record(call)
	early := append([]background.Proc(nil), agents...)
	early[0].Age = 11 * time.Second
	early = append(early, mine...)
	stopProcs = func(context.Context) ([]background.Proc, error) { return early, nil }
	for _, f := range background.NewProcIndex(early).Descendants(500, -1) {
		if f.PID == 701 {
			t.Fatalf("precondition: the view's walk hides the early run, found %+v", f)
		}
	}
	f.stop(t, "-", o1, "").block("a recorded run started in the agent's first seconds").has("reason", "bench the build")

	record(call)
	procs(mine)
	if os.Geteuid() != 0 { // root ignores the mode: the file reads as a live record
		if err := os.Chmod(project.BackgroundFile(f.Root), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(project.BackgroundFile(f.Root), 0o644) })
		f.stop(t, "-", o1, "").allow("the record file unreadable")
	}

	record(project.BackgroundCall{At: ago(6), Agent: "pj-int", Session: "s1", Description: "run the gate",
		Command: "make check"})
	procs(run(500, 900, "make check"))
	f.stop(t, "integrator", f.Root, "").block("a role agent's live call").has("reason", "run the gate")
	record(project.BackgroundCall{At: ago(6), Agent: "pj-lead", Session: "s1", Description: "read the logs",
		Command: "tail -f serve.log"})
	procs(run(500, 900, "tail -f serve.log"))
	f.stop(t, "lead", f.Root, "").block("the lead's live call").has("reason", "read the logs")
}

// bash stop case_no_config, case_other_roles
func TestStopNoConfigOrOtherRolesAllow(t *testing.T) {
	t.Parallel()
	f := newHfx(t)
	o1 := f.PlainLane("L-01", "task/T-01")
	f.stop(t, "-", o1, "").allow("worker lane without config")
	if r := f.stop(t, "integrator", f.Root, "").allow("integrator without config"); r.err != "" {
		t.Errorf("stderr: %q", r.err)
	}
	f.Config()
	f.stop(t, "reviewer", f.Root, "").allow("reviewer")
	f.stop(t, "-", f.Root, "").allow("no role")
	f.state(ago(100))
	f.Dispatched("L-01")
	f.stop(t, "-", o1, "").block("the same lane with a config")
}

// bash stop case_internal_error_fails_open
func TestStopInternalErrorAllowsWithStderr(t *testing.T) {
	t.Parallel()
	f := newHfx(t)
	f.PlainLane("L-01", "task/T-01")
	f.Config()
	f.Write(".obor/config.json", "{broken\n")
	if r := f.stop(t, "integrator", f.Root, "").allow("broken config"); !strings.HasPrefix(r.err, "obor stop hook: internal error") ||
		strings.Count(r.err, "\n") != 1 {
		t.Errorf("stderr: %q, want one internal-error line", r.err)
	}
	for _, in := range []string{"", "not json", "[]", `{"cwd": 5}`} {
		var out, errw bytes.Buffer
		Stop([]byte(in), f.Env(), &out, &errw)
		if out.Len()+errw.Len() != 0 {
			t.Errorf("input %q: %q %q", in, out.String(), errw.String())
		}
	}
}

// unwritable makes dir read-only (skips as root, who ignores the mode).
func unwritable(t *testing.T, dir string) {
	t.Helper()
	os.Chmod(dir, 0o555)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if f, err := os.Create(dir + "/.probe"); err == nil {
		f.Close()
		os.Remove(dir + "/.probe")
		t.Skip("running as root: modes are not enforced")
	}
}

func (f hfx) allowsWithError(t *testing.T, n int, role, cwd, extra string) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if r := f.stop(t, role, cwd, extra).allow("Stop " + strconv.Itoa(i)); !strings.Contains(r.err, "obor stop hook: internal error") {
			t.Errorf("Stop %d: stderr %q", i, r.err)
		}
	}
}

// T-437: an event log the worker hook cannot read allows the stop with the error on stderr.
func TestStopRoleErrorWorkerLogUnreadable(t *testing.T) {
	t.Parallel()
	f := newHfx(t)
	o1 := f.PlainLane("L-01", "task/T-01")
	f.Config()
	f.state(ago(100))
	f.Write(".obor/state/obor.db", "not a database\n")
	f.allowsWithError(t, 2, "-", o1, "")
}

// T-409, T-465: the lead and the integrator stop freely: a done lane (serve's), with or without an integrator
// configured, serve down (no socket); nothing is written.
func TestStopLeadAndIntegratorNeverBlock(t *testing.T) {
	t.Parallel()
	for _, withInt := range []bool{false, true} {
		for _, role := range []string{"lead", "integrator"} {
			t.Run(fmt.Sprintf("%s/integrator=%v", role, withInt), func(t *testing.T) {
				t.Parallel()
				f := newHfx(t)
				f.doneLane()
				if withInt {
					f.integrator()
				} else {
					f.Config()
				}
				if row, ok, err := store.ReadLaneAt(f.Root, "L-04"); err != nil || !ok || row.Finished == 0 {
					t.Fatalf("precondition: L-04 a done lane: %+v %v %v", row, ok, err)
				}
				f.stop(t, role, f.Root, "", "OBOR_AGENT=pj-"+role).allow("a done lane")
				f.stop(t, role, f.Root, "").allow("no agent name")
				if f.Exists(f.nudges()) {
					t.Error("a nudge ledger written")
				}
			})
		}
	}
}
