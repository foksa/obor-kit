package app_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/testkit"
)

// Jobs by fetch (docs/state-model.md rows 5–8, 16–17) on the dispatch rig: every
// job's prompt is its wake, the agent's job_next fetches the job (its first fetch is fact delivered via mcp, its only
// delivery), and the worker's task_done, its report the argument, submits its task (T-437).

// fetchRig is the dispatch rig (edits change the config further).
func fetchRig(t *testing.T, edits ...func(map[string]any)) *loopRig {
	t.Helper()
	return newDispatchRig(t, edits...)
}

// task is serve's status of task (zero: none).
func (r *loopRig) task(task string) app.TaskStatus {
	for _, t := range r.app.Status().Project.Tasks {
		if t.Task == task {
			return t
		}
	}
	return app.TaskStatus{}
}

// wake is the one line typed for job (the Prompter's tag, then the wake).
func wake(job string) string { return "[obor job " + job + "] call the obor job_next tool" }

// call is a job tool's socket request by agent (role, lane) with call id; job names the job it answers ("": none).
func (r *loopRig) call(method, agent, role, lane, callID, job string) (string, error) {
	return app.Verdict(context.Background(), r.sock, method, app.Call{Root: r.p.Root, Agent: agent, Role: role, Lane: lane,
		CallID: callID, Task: "T-1", Job: job})
}

// jobNext is job_next by agent with call id, its reply decoded.
func (r *loopRig) jobNext(agent, role, lane, callID, job string) (app.JobReply, string, error) {
	r.t.Helper()
	raw, err := r.call(app.MethodJobNext, agent, role, lane, callID, job)
	var jr app.JobReply
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &jr); err != nil {
			r.t.Fatalf("job_next reply %q: %v", raw, err)
		}
	}
	return jr, raw, err
}

// workerNext is pj-o1's job_next from lane O-01.
func (r *loopRig) workerNext(callID string) app.JobReply {
	r.t.Helper()
	jr, _, err := r.jobNext("pj-o1", "worker", "O-01", callID, "")
	if err != nil {
		r.t.Fatalf("job_next of pj-o1: %v", err)
	}
	return jr
}

// promptsTo is the texts typed to agent, in order.
func (r *loopRig) promptsTo(agent string) []string {
	var out []string
	for _, p := range r.prompts() {
		if p.to == agent {
			out = append(out, p.text)
		}
	}
	return out
}

// deliveries is the log's delivered facts as "<job> <via>".
func (r *loopRig) deliveries() []string {
	var out []string
	for _, rec := range r.records() {
		if rec.Kind == app.KindFact && rec.Step == app.StepDelivered {
			out = append(out, rec.Fact().Job+" "+rec.Fact().Via)
		}
	}
	return out
}

// setConfig sets path in the project's config on disk (read at the app's next load).
func (r *loopRig) setConfig(path string, v any) {
	r.t.Helper()
	var c map[string]any
	if err := json.Unmarshal([]byte(r.p.Read(".obor/config.json")), &c); err != nil {
		r.t.Fatal(err)
	}
	testkit.Set(path, v)(c)
	b, _ := json.Marshal(c)
	r.p.Write(".obor/config.json", string(b)+"\n")
}

// A dispatch with the switch on types nothing: the start carries the wake as its first prompt, the work job's result
// is "sent (start prompt)" (T-387); the worker's status turning working is no delivery; its job_next is: the work job's text, fact delivered via mcp once, the task
// working. A repeated call id gets the stored reply, a new one the same job with no second record, an older job id
// superseded{current}; another agent's call is refused, another lane has none.
func TestFetchWorkJob(t *testing.T) {
	t.Parallel()
	r := fetchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	eq(t, r.promptsTo("pj-o1"), []string(nil), "typed to pj-o1")
	same(t, r.result("work"), `"sent (start prompt)"`, "the work job's result")
	r.mu.Lock()
	launches := append([]app.Launch(nil), r.launches...)
	r.mu.Unlock()
	if len(launches) != 1 || launches[0].Agent != "pj-o1" || launches[0].Prompt != wake("T-1/1/work") {
		t.Errorf("launches %+v, want pj-o1's fresh start with the wake as its first prompt", launches)
	}
	var intent app.Record
	for _, rec := range r.records() {
		if rec.Kind == app.KindIntent && rec.Step == "work" {
			intent = rec
		}
	}
	if string(intent.Data) != `{"agent":"pj-o1","job":"T-1/1/work"}` {
		t.Errorf("work intent %s, want {agent, job}", intent.Data)
	}

	r.workerIs("working")
	r.passes()
	if r.has("1 fact delivered") || r.taskState() != "dispatched" {
		t.Fatalf("delivered by the status with the switch on: %q (state %s)", r.steps(), r.taskState())
	}
	if _, _, err := r.jobNext("pj-o2", "worker", "O-01", "x1", ""); err == nil ||
		!strings.Contains(err.Error(), "not your job: T-1/1/work is pj-o1's in lane O-01, not pj-o2") {
		t.Errorf("another agent's job_next: %v", err)
	}
	if jr, _, err := r.jobNext("pj-o2", "worker", "O-02", "x2", ""); err != nil || jr.State != "none" {
		t.Errorf("another lane's job_next: %+v, %v; want none", jr, err)
	}
	if _, _, err := r.jobNext("pj-lead", "lead", "", "x3", ""); err == nil || !strings.Contains(err.Error(), "no jobs for the role lead") {
		t.Errorf("the lead's job_next: %v", err)
	}
	if jr, _, err := r.jobNext("pj-int", "integrator", "", "x4", ""); err != nil || jr.State != "none" {
		t.Errorf("the integrator's job_next with no job: %+v, %v; want none", jr, err)
	}
	r.f.Agent("w1:p5", "pj-x", "idle", r.p.Root).Workspace = "w1"
	r.push()
	r.waitFor("pane w1:p5", func() bool { return len(r.app.Status().Project.Agents) == 2 })
	if _, err := app.Verdict(context.Background(), r.sock, app.MethodJobNext, app.Call{Root: r.p.Root, Agent: "pj-o1",
		Role: "worker", Lane: "O-01", CallID: "x5", Pane: "w1:p5"}); err == nil ||
		!strings.Contains(err.Error(), "pane w1:p5 is not in lane O-01") {
		t.Errorf("job_next from a pane outside the lane: %v", err)
	}
	if n := len(r.deliveries()); n != 0 {
		t.Fatalf("a refused or empty job_next recorded: %q", r.steps())
	}
	if ts := r.task("T-1"); ts.Job != "T-1/1/work" || ts.Delivered { // T-664: obor status' open job
		t.Errorf("status before the fetch: job %q delivered %v; want T-1/1/work, not delivered", ts.Job, ts.Delivered)
	}

	jr, raw, err := r.jobNext("pj-o1", "worker", "O-01", "n1", "")
	if err != nil || jr.State != "job" || jr.Job != "T-1/1/work" || jr.Task != "T-1" || jr.Attempt != 1 || jr.Step != "work" ||
		!strings.Contains(jr.Text, "implement the spec of T-1 exactly") {
		t.Fatalf("job_next: %+v, %v", jr, err)
	}
	r.waitFor("working", func() bool { return r.taskState() == "working" })
	eq(t, r.deliveries(), []string{"T-1/1/work mcp"}, "delivered facts")
	if ts := r.task("T-1"); ts.Job != "T-1/1/work" || !ts.Delivered {
		t.Errorf("status after the fetch: job %q delivered %v; want T-1/1/work, delivered", ts.Job, ts.Delivered)
	}
	if _, again, err := r.jobNext("pj-o1", "worker", "O-01", "n1", ""); err != nil || again != raw {
		t.Errorf("repeated call id: %q, %v; want the stored reply", again, err)
	}
	if jr := r.workerNext("n2"); jr.Job != "T-1/1/work" || jr.State != "job" {
		t.Errorf("a second fetch: %+v, want the same job", jr)
	}
	if jr, _, err := r.jobNext("pj-o1", "worker", "O-01", "n3", "T-1/0/work"); err != nil || jr.State != "superseded" ||
		jr.Current != "T-1/1/work" || jr.Text != "" {
		t.Errorf("an older job id: %+v, %v; want superseded{T-1/1/work}", jr, err)
	}
	r.passes()
	eq(t, r.deliveries(), []string{"T-1/1/work mcp"}, "delivered facts after the repeats")
	r.later(20 * time.Minute)
	if r.has("1 intent retry.work") || r.hasPrefix("1 fact needs-you") || len(r.promptsTo("pj-o1")) != 0 {
		t.Errorf("a timer fired for a fetched job: %q", r.steps())
	}
}

// No fetch 5 min after the start's wake → the wake typed (retry.work, the line cleared first; the worker working is
// no delivery); 5 more → needs-you{job not delivered}.
func TestFetchUndelivered(t *testing.T) {
	t.Parallel()
	r := fetchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.workerIs("working")
	r.later(4*time.Minute + 59*time.Second)
	if r.has("1 intent retry.work") {
		t.Fatalf("retried before 5 min: %q", r.steps())
	}
	r.later(time.Second)
	r.waitStep("1 result retry.work")
	eq(t, r.promptsTo("pj-o1"), []string{wake("T-1/1/work")}, "typed to pj-o1")
	if got := r.cleared("pj-o1"); len(got) != 1 || !got[0] {
		t.Errorf("input line cleared before prompts %v, want [true]", got)
	}
	r.later(4*time.Minute + 59*time.Second)
	if r.hasPrefix("1 fact needs-you") {
		t.Fatalf("needs-you before 5 min after the retry: %q", r.steps())
	}
	r.later(time.Second)
	r.waitStep("1 fact needs-you{job not delivered: T-1/1/work prompted twice to pj-o1}")
	if r.has("1 fact delivered") {
		t.Errorf("delivered without a fetch: %q", r.steps())
	}
}

// fetched is the fetch rig with T-1 dispatched, its work job fetched and its task_done submitting it,
// the integrator's review job out and fetched: the review job's text.
func fetched(t *testing.T) (*loopRig, string) {
	r := fetchRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.workerNext("n1")
	r.submit()
	r.waitStep("1 fact submitted")
	r.waitStep("1 result review")
	eq(t, r.promptsTo("pj-int"), []string(nil), "typed to pj-int (its start carried the wake, T-393)")
	same(t, r.result("review"), `"sent (start prompt)"`, "review result")
	r.integratorIs("working")
	r.passes()
	if len(r.deliveries()) != 1 {
		t.Fatalf("the review job delivered by the status: %q", r.steps())
	}
	jr, _, err := r.jobNext("pj-int", "integrator", "", "i1", "")
	if err != nil || jr.State != "job" || jr.Job != "T-1/1/review" || !strings.Contains(jr.Text, "Patch hash: ") {
		t.Fatalf("the integrator's job_next: %+v, %v", jr, err)
	}
	r.waitFor("delivered", func() bool { return len(r.deliveries()) == 2 })
	eq(t, r.deliveries(), []string{"T-1/1/work mcp", "T-1/1/review mcp"}, "delivered facts")
	return r, jr.Text
}

// The integrator's start carries its job's wake; its review job is a wake, fetched by job_next (no status rule), and
// accepted as today.
func TestFetchReviewJob(t *testing.T) {
	t.Parallel()
	r, text := fetched(t)
	r.mu.Lock()
	var prompt string
	for _, l := range r.launches {
		if l.Agent == "pj-int" {
			prompt = l.Prompt
		}
	}
	r.mu.Unlock()
	if prompt != wake("T-1/1/review") {
		t.Errorf("the integrator's start prompt %q, want the review job's wake", prompt)
	}
	if ts := r.task("T-1"); ts.Job != "T-1/1/review" || !ts.Delivered { // T-664: the integrator's job, fetched
		t.Errorf("status with the review job fetched: job %q delivered %v; want T-1/1/review, delivered", ts.Job,
			ts.Delivered)
	}
	r.accept("c1", r.patch(text))
	r.waitStep("1 result agent.stop")
	if r.mainMerges() != 1 {
		t.Errorf("not merged: %q", r.steps())
	}
}

// After a sendback the fix job is a wake, fetched by job_next (its text the notes). task_done with a full report
// submits the next attempt, its report and call id on the fact, and the next review job quotes the report; a repeated
// call id gets the stored reply; once submitted, another task_done is refused. The guards: the caller's role and
// agent, the report (empty, a section missing).
func TestFetchFixAndTaskDone(t *testing.T) {
	t.Parallel()
	r, _ := fetched(t)
	if reply, err := r.verdict(app.MethodSendback, "s1", "", "add a test"); err != nil || !strings.HasPrefix(reply, "SENDBACK T-1") {
		t.Fatalf("sendback: %q, %v", reply, err)
	}
	r.integratorStops() // it ends its turn after the verdict, and serve stops it: no job is left for it (T-672)
	r.waitStep("2 result fix")
	eq(t, r.promptsTo("pj-o1"), []string{wake("T-1/2/fix")}, "typed to pj-o1")
	jr := r.workerNext("f1")
	if jr.Job != "T-1/2/fix" || !strings.Contains(jr.Text, "T-1 is sent back to you (attempt 2): add a test") ||
		!strings.Contains(jr.Text, "call task_done again") || strings.Contains(jr.Text, "done file") {
		t.Fatalf("the fix job: %+v", jr)
	}
	r.waitFor("delivered", func() bool { return len(r.deliveries()) == 3 })

	fixed := "Summary: added the test.\nSelf-check: ok.\nSkipped: none.\nOpen questions: none."
	for _, row := range []struct{ name, agent, role, report, want string }{
		{"another agent", "pj-o2", "worker", fixed, "not your task (T-1 is pj-o1's in lane O-01, not pj-o2's)"},
		{"the integrator", "pj-int", "integrator", fixed, "a worker submits its task, not pj-int"},
		{"an empty report", "pj-o1", "worker", "  \n", "the report is empty: pass your final report (Summary, " +
			"Self-check, Skipped, Open questions) as report"},
		{"a section missing", "pj-o1", "worker", "Summary: x\nSelf-check: x\nSkipped: x", "the report has no section Open questions"},
	} {
		if _, err := r.callDone(row.agent, row.role, "t-"+row.name, row.report); err == nil ||
			!strings.Contains(err.Error(), row.want) {
			t.Errorf("%s: task_done %v, want %q", row.name, err, row.want)
		}
	}
	r.passes()
	if r.has("2 fact submitted") {
		t.Fatalf("a refused task_done recorded: %q", r.steps())
	}

	if reply, err := r.callDone("pj-o1", "worker", "t1", fixed); err != nil || reply != "submitted" {
		t.Fatalf("task_done: %q, %v", reply, err)
	}
	if reply, err := r.callDone("pj-o1", "worker", "t1", fixed); err != nil || reply != "submitted" {
		t.Errorf("repeated call id: %q, %v; want the stored reply", reply, err)
	}
	if _, err := r.callDone("pj-o1", "worker", "t2", fixed); err == nil ||
		!strings.Contains(err.Error(), "not open for a submission") {
		t.Errorf("a second task_done: %v", err)
	}
	review := r.waitJob("pj-int", "T-1/2/review")
	if !strings.Contains(review, "The worker's report:\n"+fixed) {
		t.Errorf("the review job does not quote the report:\n%s", review)
	}
	if n := r.count("2 fact submitted"); n != 1 {
		t.Errorf("submitted facts of attempt 2: %d, want 1: %q", n, r.steps())
	}
	var d struct{ Call, Report string }
	for _, rec := range r.records() {
		if rec.Kind == app.KindFact && rec.Step == app.StepSubmitted && rec.Attempt == 2 {
			_ = json.Unmarshal(rec.Data, &d)
		}
	}
	if d.Call != "t1" || d.Report != fixed {
		t.Errorf("the submitted fact's call id %q, report %q; want t1 and the report", d.Call, d.Report)
	}
}

// A Blocked: report submits nothing: it records needs-you{asks: <what it needs>} (the task stays working), notifies
// the owner and tells the worker to wait; no stall is added while the worker waits, and its activity does not clear it
// (T-511: the Stop hook's turn after the report is no answer): obor answer (answer_test.go) or its submission does. A
// full report then submits, and the question ends with it.
func TestFetchBlockedAsks(t *testing.T) {
	t.Parallel()
	r := fetchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.workerNext("n1")
	r.waitFor("working", func() bool { return r.taskState() == "working" })
	r.workerIs("working")
	asks := "0 fact needs-you{asks: may I edit internal/x.go?}"
	reply, err := r.callDone("pj-o1", "worker", "b1", "Summary: half done\nBlocked: may I edit internal/x.go?\n")
	if err != nil || !strings.Contains(reply, "the owner is told") || !strings.Contains(reply, "stop and wait") {
		t.Fatalf("task_done Blocked: %q, %v", reply, err)
	}
	r.waitStep(asks)
	r.waitFor("one notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
	if r.hasPrefix("1 fact submitted") || r.taskState() != "working" {
		t.Fatalf("a Blocked: report submitted (%s): %q", r.taskState(), r.steps())
	}
	r.push()
	r.passes() // still the asking turn: working is no answer
	r.workerIs("idle")
	r.later(25 * time.Minute)
	r.push()
	r.passes()
	if r.hasPrefix("0 fact stalled") || r.hasPrefix("0 fact resumed") || r.count(asks) != 1 {
		t.Fatalf("while the worker waits: %q, want only the asks fact", r.steps())
	}
	if f := r.app.Status().Project.Facts; len(f) != 1 {
		t.Fatalf("facts in force %+v, want the asks fact", f)
	}
	r.workerIs("working") // the owner answered in its pane, or the Stop hook's turn
	r.push()
	r.passes()
	if r.hasPrefix("0 fact resumed") || len(r.app.Status().Project.Facts) != 1 {
		t.Fatalf("the worker's activity ended its question: %q", r.steps())
	}
	full := "Summary: done.\nSelf-check: ok.\nSkipped: none.\nOpen questions: none."
	if reply, err := r.callDone("pj-o1", "worker", "t1", full); err != nil || reply != "submitted" {
		t.Errorf("task_done after the answer: %q, %v", reply, err)
	}
	r.waitFor("the question ended by the submission", func() bool { return r.facts() == 0 })
}

// callDone is agent's task_done for T-1 (role, lane O-01) with call id and report.
func (r *loopRig) callDone(agent, role, callID, report string) (string, error) {
	return app.Verdict(context.Background(), r.sock, app.MethodTaskDone, app.Call{Root: r.p.Root, Agent: agent, Role: role,
		Lane: "O-01", CallID: callID, Task: "T-1", Report: report})
}

// An unfetched fix job: its wake again after 5 min, the owner after 5 more; obor resume (its default: retry) sends it
// anew as fix.2, whose fetch delivers it.
func TestFetchFixUndelivered(t *testing.T) {
	t.Parallel()
	r, _ := fetched(t)
	if _, err := r.verdict(app.MethodSendback, "s1", "", "add a test"); err != nil {
		t.Fatal(err)
	}
	r.waitStep("2 result fix")
	r.later(5 * time.Minute)
	r.waitStep("2 result retry.fix")
	r.later(5 * time.Minute)
	r.waitStep("2 fact needs-you{job not delivered: T-1/2/fix prompted twice to pj-o1}")
	r.needsYouState()
	r.mustResume("r1", "", "RESUMED T-1: retry (job not delivered)")
	r.waitStep("2 result fix.2")
	eq(t, r.promptsTo("pj-o1"), []string{wake("T-1/2/fix"), wake("T-1/2/fix"), wake("T-1/2/fix.2")},
		"typed to pj-o1")
	if jr := r.workerNext("f2"); jr.Job != "T-1/2/fix.2" {
		t.Errorf("job_next after the resume: %+v, want T-1/2/fix.2", jr)
	} else if !strings.Contains(jr.Text, "If this session did not get the work job (a fresh start), read ") ||
		!strings.Contains(jr.Text, "templates/agents/worker.md") {
		t.Errorf("the fix job names no role text for a worker started fresh:\n%s", jr.Text)
	}
	r.waitFor("delivered", func() bool { return len(r.deliveries()) == 3 })
	r.later(20 * time.Minute)
	if r.has("2 intent retry.fix.2") {
		t.Errorf("a fetched fix job retried: %q", r.steps())
	}
}

// T-437: a lane type's obsolete jobs: typed changes nothing: its worker's start carries the wake and its job_next
// fetches the work job; the integrator fetches too.
func TestFetchObsoleteTypedKind(t *testing.T) {
	t.Parallel()
	r := fetchRig(t, testkit.Set("lane_types.opus.jobs", "typed"))
	r.work("feature.txt", "feature\n")
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.mu.Lock()
	prompt := r.launches[0].Prompt
	r.mu.Unlock()
	same(t, prompt, wake("T-1/1/work"), "the start prompt")
	if jr := r.workerNext("n1"); jr.Job != "T-1/1/work" || !strings.Contains(jr.Text, "implement the spec of T-1 exactly") {
		t.Errorf("job_next: %+v, want the work job", jr)
	}
	r.submit()
	if job := r.waitJob("pj-int", "T-1/1/review"); !strings.Contains(job, "Patch hash: ") {
		t.Errorf("the review job: %q", job)
	}
}

// Replay with a wake pending: the crash came after the typing of the retry's wake began; the replay sends the wake once
// more (T-524: harmless twice) and the worker's job_next gets the job.
func TestFetchReplayWake(t *testing.T) {
	t.Parallel()
	r := fetchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.disp.set(func(d *dispatchFakes) { d.cut["prompt"] = "after" })
	r.clock.Add(5 * time.Minute)
	r.push()
	r.waitFor("the wake typed", func() bool { return len(r.promptsTo("pj-o1")) == 1 })
	r.waitFor("the prompt cut", func() bool { r.disp.mu.Lock(); defer r.disp.mu.Unlock(); return r.disp.hit["prompt"] })
	r.stop()
	if !r.has("1 intent retry.work") || r.has("1 result retry.work") {
		t.Fatalf("precondition: the wake's intent pending: %q", r.steps())
	}
	r.disp.set(func(d *dispatchFakes) { delete(d.cut, "prompt") })
	r.start()
	r.waitStep("1 result retry.work")
	r.passes()
	eq(t, r.promptsTo("pj-o1"), []string{wake("T-1/1/work"), wake("T-1/1/work")}, "typed to pj-o1 after the replay")
	if jr := r.workerNext("n1"); jr.Job != "T-1/1/work" {
		t.Errorf("job_next after the replay: %+v", jr)
	}
}

// T-437: a Codex and an OpenCode lane finish as a Claude lane does: the start carries the work job's wake, job_next
// fetches it, task_done (its report) submits the task, and the merge loop takes it to merged.
func TestFetchCodexOpenCodeFinishByTaskDone(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"codex", "opencode"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := fetchRig(t, testkit.Set("lane_types.opus.kind", kind))
			r.work("feature.txt", "feature\n")
			r.start()
			r.mustDispatch("d1")
			r.waitStep("1 result work")
			r.mu.Lock()
			prompt := r.launches[0].Prompt
			r.mu.Unlock()
			same(t, prompt, wake("T-1/1/work"), kind+"'s start prompt")
			if jr := r.workerNext("n1"); jr.Job != "T-1/1/work" {
				t.Fatalf("job_next: %+v, want the work job", jr)
			}
			r.waitFor("working", func() bool { return r.taskState() == "working" })
			r.submit()
			r.waitStep("1 fact submitted")
			r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
			r.waitStep("1 result agent.stop")
			if r.mainMerges() != 1 {
				t.Errorf("not merged: %q", r.steps())
			}
		})
	}
}
