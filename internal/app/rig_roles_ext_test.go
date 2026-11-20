package app_test

import (
	"encoding/json"
	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Codex rollout lines (the shape of a real rollout, 2026-10-15): turn t1's start and an item, its end on the usage
// limit, turn t2's start and its clean end.
const (
	cdxStarted = `{"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message"}}` + "\n"
	cdxLimitMsg = "You’ve hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), visit https://chatgpt.com/settings/usage to purchase more credits or try again at 6:30 PM."
	cdxLimited  = `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","last_agent_message":null,"error":{"message":"` +
		cdxLimitMsg + `","codex_error_info":"usage_limit_exceeded"}}}` + "\n"
	cdxStarted2 = `{"type":"event_msg","payload":{"type":"task_started","turn_id":"t2"}}` + "\n"
	cdxDone2    = `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t2","last_agent_message":"done"}}` + "\n"
)

// codexRig is the fetch rig with T-1's worker a Codex agent working on it, its rollout (append adds lines) holding
// turn t1's start, then the end on the usage limit: the stop in force (inForce counts it, its turn t1).
type codexRig struct {
	*loopRig
	append  func(string)
	reason  string
	inForce func() int
}

// cdxReason is the stop's reason on the usage limit.
const cdxReason = "pj-l-01 stopped: usage_limit_exceeded (" + cdxLimitMsg + ")"

// codexStoppedOn is codexStopped with limited the lines that end turn t1, so the stop in force says reason.
func codexStoppedOn(t *testing.T, limited, reason string) codexRig {
	r := fetchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.workerNext("n1")
	r.waitFor("working", func() bool { return r.taskState() == "working" })
	rollout := filepath.Join(t.TempDir(), "rollout-2026-10-15T13-25-21-c1.jsonl")
	c := codexRig{loopRig: r, reason: reason}
	c.append = func(s string) {
		f, err := os.OpenFile(rollout, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}
	c.inForce = func() int {
		n := 0
		for _, f := range r.app.Status().Project.Facts {
			if f.Task == "T-1" && f.Step == event.StepNeedsYou && f.Fact().Reason == c.reason &&
				f.Fact().Cause == "stopped" && f.Fact().Turn == "t1" {
				n++
			}
		}
		return n
	}
	c.append(cdxStarted)
	r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Kind, p.Transcript = "codex", rollout })
	r.workerIs("idle") // the turn's start is the last event: running, not stopped
	r.passes()
	if r.taskStep(event.MsgTask, "fact stopped") != 0 || r.hasPrefix("0 fact needs-you") {
		t.Fatalf("precondition: a stop with the turn running: %q", r.steps())
	}
	c.append(limited)
	r.push()
	r.waitFor("the stop in force", func() bool { return c.inForce() == 1 })
	return c
}

// A move answers at once; the worker's stop is a step of the merge loop (worker.stop), done
// before the step that needs the lane, a done stop tells the lead nothing; a stop that fails is a needs-you obor resume
// retries. The rig: newLoopRig(t, working()) (T-1 working on L-01 as sonnet, its worker pj-l-01 in w1:p1).

// stopLines is the lead's lines that tell a worker.stop's outcome ("stopped <agent>", "<agent> was not running"):
// A done stop sends none, so the tests below assert it stays empty.
func (r *loopRig) stopLines() []string {
	var out []string
	for _, rec := range r.records() {
		var m event.Message
		_ = json.Unmarshal(rec.Data, &m)
		if rec.Kind == event.KindFact && rec.Step == event.StepMessage &&
			(strings.Contains(m.Line, ": stopped pj-l-") || strings.Contains(m.Line, " was not running (")) {
			out = append(out, m.Line)
		}
	}
	return out
}

// needsLines is the lead's lines announcing a needs-you (row 47: message{of: needs}).
func (r *loopRig) needsLines() []string {
	var out []string
	for _, rec := range r.records() {
		var m event.Message
		_ = json.Unmarshal(rec.Data, &m)
		if rec.Kind == event.KindFact && rec.Step == event.StepMessage && m.Of.Kind == "needs" {
			out = append(out, m.Line)
		}
	}
	return out
}

// holdStops makes the rig's stops wait until release: entered gets one value per stop as it begins.
func (r *loopRig) holdStops() (entered <-chan struct{}, release func()) {
	in, gate := make(chan struct{}, 8), make(chan struct{})
	r.mu.Lock()
	r.onStop = func() error {
		in <- struct{}{}
		<-gate
		return nil
	}
	r.mu.Unlock()
	closed := false
	release = func() {
		if !closed {
			closed = true
			close(gate)
		}
	}
	r.t.Cleanup(release)
	return in, release
}

// awaitStop waits for a held stop to begin.
func awaitStop(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the merge loop never began the worker's stop")
	}
}

// before: in the log, the record a precedes the record b (both there).
func (r *loopRig) before(a, b string) bool {
	steps := r.steps()
	i, j := slices.Index(steps, a), slices.Index(steps, b)
	return i >= 0 && j >= 0 && i < j
}

// continueLine is the continue wake T-1's stopped worker gets: its work job's tag, then the text.
const continueLine = "[obor job T-1/1/work] Your usage limit has reset: continue where your turn stopped (call the obor job_next tool for your job)."

// stillStopped is the alarm's addition to the stop's reason.
const stillStopped = "; still stopped 30m after the reset wake"

// stopIn is how many needs-you{reason} of T-1 are in force.
func (r *loopRig) stopIn(reason string) int {
	n := 0
	for _, f := range r.app.Status().Project.Facts {
		if f.Task == "T-1" && f.Step == event.StepNeedsYou && f.Fact().Reason == reason {
			n++
		}
	}
	return n
}

// wakes is how many continue wakes pj-l-01 got.
func (r *loopRig) wakes() int { return r.typedTo("pj-l-01", continueLine) }

// alarms is how many notifications the owner got of T-1's alarm.
func (r *loopRig) alarms() int {
	n := 0
	for _, req := range r.srv.Requests() {
		var p struct{ Body string }
		_ = json.Unmarshal(req.Params, &p)
		if req.Method == "notification.show" && strings.HasPrefix(p.Body, "T-1 ") && strings.HasSuffix(p.Body, stillStopped) {
			n++
		}
	}
	return n
}

// resetIn is a Claude usage limit's details naming the reset d after the rig's clock.
func resetIn(d time.Duration) func(r *loopRig) string {
	return func(r *loopRig) string {
		return "Claude AI usage limit reached|" + strconv.FormatInt(r.clock.Now().Add(d).Unix(), 10)
	}
}

// finishedLines is how many finished lines for pj-rev the lead got.
func finishedLines(r *loopRig) int {
	n := 0
	for _, p := range r.inbox.posted() {
		if strings.HasPrefix(p, "pj-lead: [obor] pj-rev finished (briefed ") {
			n++
		}
	}
	return n
}

// claudeRoleStop is the line rig with pj-rev (Claude) briefed when brief, worked and idle, then its StopFailure hook's
// request: errText with the details details gives at the rig's clock.
func claudeRoleStop(t *testing.T, brief bool, errText string, details func(r *loopRig) string) *loopRig {
	r := lineRig(t, "idle", "idle")
	r.start()
	if brief {
		if _, err := r.line("b1", &wire.BriefReq{To: "pj-rev", Text: "verify finding 3"}); err != nil {
			t.Fatal(err)
		}
		r.waitFor("the brief posted", func() bool { return r.inbox.count("pj-rev", "[obor pj-lead] verify finding 3") == 1 })
		r.agentIs("w1:p6", "pj-rev", "working")
		r.passes()
	}
	d := details(r)
	reply, err := r.line("s1", &wire.StoppedReq{Envelope: wire.Envelope{Agent: "pj-rev", Role: "reviewer"},
		Error: errText, Details: d})
	if err != nil || reply != "STOPPED pj-rev (the lead is told)" {
		t.Fatalf("stopped: %q, %v", reply, err)
	}
	r.agentIs("w1:p6", "pj-rev", "idle")
	stopped := "[obor] pj-rev stopped: " + errText + " (" + d + ")"
	r.waitFor("the stopped line posted", func() bool { return r.inbox.count("pj-lead", stopped) == 1 })
	return r
}

// verbsRig is the loop rig (dispatching, started) with two custom roles: tracker, verbs spec_set and dispatch (an
// integration's), and scout, none.
func verbsRig(t *testing.T) *loopRig {
	t.Helper()
	return newLoopRig(t, dispatching(), started(), withConfig(
		testkit.Set("agents.roles.tracker", `{"verbs": ["spec_set", "dispatch"]}`), testkit.Set("agents.roles.scout", `{"model": ""}`)))
}

// obor dispatch … --after waits for merges (docs/state-model.md row 2a, readiness rule 1). T-2 is the task T-1
// waits for: in the loop as a failed submission (a needs-you), merged once an obor merge lands it on main (row 34,
// outside); T-3 is a task with a stored spec and no loop record.

// seedT2 writes T-2's open attempt to the log of the stopped app: submitted on L-02, its submit refused (fail).
func (r *loopRig) seedT2(more ...event.Record) {
	r.t.Helper()
	sub, _ := json.Marshal(map[string]any{"lane": "L-02", "agent": "pj-l-02"})
	refused, _ := json.Marshal(map[string]any{"refused": "no commit"})
	recs := append([]event.Record{{Task: "T-2", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact, Data: sub},
		{Task: "T-2", Attempt: 1, Step: event.StepSubmit, Kind: event.KindIntent, Class: event.ClassGit},
		{Task: "T-2", Attempt: 1, Step: event.StepSubmit, Kind: event.KindResult, Data: refused}}, more...)
	if err := app.WriteLog(r.p.Root, time.Now(), recs...); err != nil {
		r.t.Fatal(err)
	}
}

// landT2 is an obor merge of T-2 on main outside the app: the loop records merged{outside}.
func (r *loopRig) landT2() {
	r.t.Helper()
	r.scratch.Land("T-2") // obor merge by hand
	r.waitFor("T-2 merged", func() bool { return r.taskStep("T-2", "fact merged") > 0 })
}

// taskStep is how many of task's records read "<kind> <step>".
func (r *loopRig) taskStep(task, step string) int {
	n := 0
	for _, rec := range r.records() {
		if rec.Task == task && rec.Kind+" "+rec.String() == step {
			n++
		}
	}
	return n
}

// waitAfter is the lead's dispatch of T-1 onto L-01 after the tasks.
func (r *loopRig) waitAfter(callID string, after ...string) (string, error) {
	return r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
		CallID: callID}, Task: "T-1", Lane: "L-01", After: after, Types: []string{"sonnet"}})
}

// taskStatus is task's row in obor status --facts (Task "": not listed).
func (r *loopRig) taskStatus(task string) wire.TaskStatus {
	for _, t := range r.app.Status().Project.Tasks {
		if t.Task == task {
			return t
		}
	}
	return wire.TaskStatus{}
}

// startWithT2 starts the rig with T-2 seeded and waits until its needs-you is folded and its fail step settled: drop
// refuses a task whose step still runs ("retry when it settles"), and under load that step outlasts the test's first
// call.
func startWithT2(t *testing.T, more ...event.Record) *loopRig {
	t.Helper()
	r := newLoopRig(t, dispatching())
	r.seedT2(more...)
	r.start()
	r.waitFor("T-2 needs-you", func() bool { return r.taskStatus("T-2").State == "needs-you" })
	r.waitFor("T-2 settled", func() bool { return app.Settled(r.app, r.p.Root, "T-2") })
	return r
}

// workerExits leaves pj-l-01's labelled pane at its shell (its agent quit: obor stop's stop, or a crash) and waits
// until serve sees it gone.
func (r *loopRig) workerExits() {
	r.t.Helper()
	r.agentGone("w1:p1", "pj-l-01", func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status = "", "", "" })
}

// ruleStarts is pj-l-01's starts without --fresh: the worker start rule's (a dispatch's own start is fresh).
func (r *loopRig) ruleStarts() []port.Launch {
	var out []port.Launch
	for _, l := range r.workerLaunches() {
		if !l.Fresh {
			out = append(out, l)
		}
	}
	return out
}

// pastSettle runs a pass that sees the condition, then moves the clock past the rule's settle.
func (r *loopRig) pastSettle() {
	r.passes()
	r.later(app.WorkerStartSettle)
}

// resumes is the rule's starts applied (a failed one is in ruleStarts only).
func (r *loopRig) resumes() int {
	r.disp.mu.Lock()
	defer r.disp.mu.Unlock()
	return r.disp.resumes
}

// startedWith waits for the rule's first start applied, moving the clock past the settle while there is none (a herdr
// resubscribe gap, which the rig's pushes cause, holds no condition for a pass and so starts the settle again), then
// for pj-l-01 up again; that start was in w1:p1 with job's wake.
func (r *loopRig) startedWith(job string) {
	r.t.Helper()
	r.waitFor("the rule's start of pj-l-01", func() bool {
		if r.resumes() > 0 {
			return true
		}
		r.later(app.WorkerStartSettle)
		return false
	})
	starts := r.ruleStarts()
	l := starts[len(starts)-1]
	if want := "[obor job " + job + "] call the obor job_next tool"; l.Pane != "w1:p1" || l.Prompt != want {
		r.t.Errorf("the rule's start %+v; want pane w1:p1, prompt %q", l, want)
	}
	r.waitFor("pj-l-01 up again", func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == "pj-l-01" {
				return true
			}
		}
		r.push()
		return false
	})
}

// ruleNeedsYou: the log has the worker start rule's needs-you.
func (r *loopRig) ruleNeedsYou() bool {
	return slices.ContainsFunc(r.records(), func(rec event.Record) bool {
		return rec.Task == event.RuleTask && rec.Step == event.StepNeedsYou &&
			strings.Contains(rec.String(), "rule worker.start failed 3 passes in a row")
	})
}
