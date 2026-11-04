package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/testkit"
)

type hfx struct{ *testkit.Project }

func newHfx(t *testing.T) hfx { return hfx{testkit.New(t)} }

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

func (f hfx) integrator() { f.Config(testkit.Set("roles", `{"integrator": "opus"}`)) }

// doneLane: lane L-04 on task/T-04 with a commit and its task_done.
func (f hfx) doneLane() string {
	o4 := f.Lane("L-04", "task/T-04")
	f.Commit(o4)
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

// T-232 (replaces T-175's case_lead_handoff_* and T-215's integrator handoff blocks): no Stop block for context size;
// the reminder rides on each prompt (prompt_test.go). Above the limit the lead and integrator stop freely, and their
// nothing is written.
func TestStopAboveHandoffLimitNoBlock(t *testing.T) {
	t.Parallel()
	f := newHfx(t)
	f.Lane("L-01", "task/T-01")
	f.Config(testkit.Set("roles", `{"lead": {"handoff_tokens": 300000}, "integrator": {"type": "opus", "handoff_tokens": 300000}}`))
	tr := transcript(f, f.Base+"/projects/p/s1.jsonl", 900000)
	if ctx, _, _, ok := project.ContextTokens(tr); !ok || ctx != 900000 {
		t.Fatalf("precondition: transcript context %d %v, want 900000", ctx, ok)
	}
	f.stop(t, "lead", f.Root, session("s1", tr)).allow("a lead at 900k > 300k")
	f.stop(t, "lead", f.Root, session("s2", tr), "OBOR_AGENT=pj-lead").allow("another lead session at 900k")
	f.stop(t, "integrator", f.Root, session("i1", tr)).allow("the integrator at 900k > 300k")
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
	f := newHfx(t)
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
	o1 := f.Lane("L-01", "task/T-01")
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

// T-558: a Blocked: task_done records an asks needs-you fact, not a submitted lane timestamp; resuming keeps the
// question open, while an answer for this task ends it.
func TestStopWorkerAfterBlockedReportAllows(t *testing.T) {
	t.Parallel()
	f := newHfx(t)
	o1 := f.Lane("L-01", "task/T-01")
	f.Config()
	f.state(ago(100))
	f.Dispatched("L-01")
	f.stop(t, "-", o1, "").block("precondition: no report yet")
	f.appendFact(t, "T-01", "needs-you", map[string]string{"reason": "asks: need help"})
	f.stop(t, "-", o1, "").allow("Blocked: report recorded")
	f.appendFact(t, "T-01", "resumed", map[string]string{})
	f.stop(t, "-", o1, "").allow("resume keeps the question open")
	f.appendFact(t, "msg", "answered", map[string]string{"task": "T-99"})
	f.stop(t, "-", o1, "").allow("answer for another task leaves the question open")
	f.appendFact(t, "msg", "answered", map[string]string{"task": "T-01"})
	f.stop(t, "-", o1, "").block("an answered question leaves the task awaiting task_done")
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

// blockedReport is the fixture a later attempt inherits: lane L-01's task dispatched, its Blocked: report recorded (the
// needs-you{asks} fact serve's task_done records).
func blockedReport(t *testing.T) (hfx, string) {
	t.Helper()
	f := newHfx(t)
	o1 := f.Lane("L-01", "task/T-01")
	f.Config()
	f.state(ago(100))
	f.Dispatched("L-01")
	f.appendFact(t, "T-01", "needs-you", map[string]string{"reason": "asks: need help"})
	return f, o1
}

// T-560 (RV-58 #1): a Blocked: report lives one attempt. A drop and re-dispatch, or a submission and sendback, each
// end the report the previous attempt made (the drop, the dispatch, the lane.prepare result, the submission and the
// sendback), so the next attempt blocks until it reports again; a fresh report it makes allows the stop.
func TestStopWorkerBlockedReportRows(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		boundary func(t *testing.T, f hfx)
	}{
		{"drop and re-dispatch", func(t *testing.T, f hfx) {
			f.appendFact(t, "T-01", "dropped", map[string]string{"reason": "gone"})
			f.Dispatched("L-01")
			f.appendEvent(t, "T-01", "lane.prepare", "result", map[string]string{"base": "main"})
		}},
		{"submission and sendback", func(t *testing.T, f hfx) {
			f.Submit("L-01")
			f.appendFact(t, "T-01", "sendback", map[string]string{"note": "fix it"})
			f.state(ago(100)) // the sendback's attempt: the lane awaits its task_done again
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f, o1 := blockedReport(t)
			f.stop(t, "-", o1, "").allow("precondition: the attempt's own Blocked: report")
			c.boundary(t, f)
			f.stop(t, "-", o1, "").block("the "+c.name+" attempt has no report of its own").
				has("reason", "no task_done for T-01 yet")
			f.appendFact(t, "T-01", "needs-you", map[string]string{"reason": "asks: may I edit internal/x.go?"})
			f.stop(t, "-", o1, "").allow("the new attempt's own Blocked: report")
		})
	}
}

// T-437, T-439: no task to finish → allow: no lane row (not a lane serve dispatched), or a row whose task_done is in
// (finished); the lanes table unreadable → allow, saying so.
func TestStopWorkerNoServeTaskAllows(t *testing.T) {
	t.Parallel()
	f := newHfx(t)
	o1 := f.Lane("L-01", "task/T-01")
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
	f := newHfx(t)
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

// bash stop case_no_config, case_other_roles
func TestStopNoConfigOrOtherRolesAllow(t *testing.T) {
	t.Parallel()
	f := newHfx(t)
	o1 := f.Lane("L-01", "task/T-01")
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
	f.Lane("L-01", "task/T-01")
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
	o1 := f.Lane("L-01", "task/T-01")
	f.Config()
	f.state(ago(100))
	f.Write(".obor/state/obor.db", "not a database\n")
	f.allowsWithError(t, 2, "-", o1, "")
}

// T-409, T-465: the lead and the integrator stop freely: a done lane (serve's), above the handoff
// limit, with or without an integrator configured, serve down (no socket); nothing is written.
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
