package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// T-1016, T-1026: obor drop of a task with a stored spec and no loop record records the drop at the spec's rev (its why
// with it) and is the task's outcome: serve lists T-1 dropped (ended), a dispatch of it is refused naming the drop, and
// a restart's replay folds the same; a new obor spec set (a later rev) revives it: not listed, dispatchable. A task
// with neither a spec nor a loop record is refused as before.
func TestDropOfASpecOnlyTaskThenDispatchUntilANewRev(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	if got := r.setSpec("s1", "# T-1 — Stored\n\nDifficulty: medium\n"); got != "SET T-1 rev 1" {
		t.Fatalf("spec set: %q, want SET T-1 rev 1", got)
	}
	if st := r.taskStatus("T-1"); st.Task != "" {
		t.Fatalf("precondition: T-1 listed before the drop: %+v", st)
	}
	if _, err := wire.Verdict(context.Background(), r.sock, &wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", CallID: "x1"}, Task: "T-9", Why: "superseded on 11-07"}); err == nil ||
		!strings.Contains(err.Error(), "T-9 is not open in obor serve") {
		t.Errorf("drop of an unknown task: %v; want refused as before", err)
	}
	r.dropSpec("x2", "T-1", "superseded on 11-07")
	if d := r.factData("T-1", event.StepDropped); d["rev"] != float64(1) || d["reason"] != "superseded on 11-07" {
		t.Errorf("the dropped fact %+v, want rev 1 and its why", d)
	}
	refused := "T-1's spec rev 1 was dropped (superseded on 11-07): obor spec set T-1"
	for _, when := range []string{"live", "after a restart"} {
		if when != "live" {
			r.stop()
			r.start()
		}
		if st := r.taskStatus("T-1"); st.State != "dropped" || !st.Ended {
			t.Errorf("%s: T-1 %+v, want listed dropped (ended)", when, st)
		}
		if _, err := r.dispatch("d1", "L-01"); err == nil || !strings.Contains(err.Error(), refused) {
			t.Errorf("%s: dispatch of the dropped spec: %v; want refused naming the drop", when, err)
		}
		if _, err := r.dropCall("x3", "again"); err == nil || !strings.Contains(err.Error(), "T-1 is dropped already") {
			t.Errorf("%s: a second drop: %v; want refused as dropped", when, err)
		}
	}
	// The same text again is no new rev: still dropped.
	if got := r.setSpec("s2", "# T-1 — Stored\n\nDifficulty: medium\n"); got != "UNCHANGED T-1 rev 1" {
		t.Fatalf("spec set: %q, want UNCHANGED T-1 rev 1", got)
	}
	if st := r.taskStatus("T-1"); st.State != "dropped" {
		t.Errorf("after an unchanged set: T-1 %+v, want still dropped", st)
	}
	// A new rev revives it, also on a replay: not listed, and the same request dispatches.
	if got := r.setSpec("s3", "# T-1 — Stored, widened\n\nDifficulty: medium\n"); got != "SET T-1 rev 2" {
		t.Fatalf("spec set: %q, want SET T-1 rev 2", got)
	}
	if st := r.taskStatus("T-1"); st.Task != "" {
		t.Errorf("after rev 2: T-1 listed %+v, want revived (a spec never dispatched)", st)
	}
	r.stop()
	r.start()
	if st := r.taskStatus("T-1"); st.Task != "" {
		t.Errorf("after rev 2 and a restart: T-1 listed %+v, want revived", st)
	}
	if got, err := r.dispatch("d2", "L-01"); err != nil || !strings.HasPrefix(got, "DISPATCHED T-1 → L-01 (queued)") {
		t.Errorf("dispatch after rev 2: %q, %v; want dispatched", got, err)
	}
}

// T-1026: a spec-only drop of T-1 ends a dispatch waiting --after it as a drop of a dispatched T-1 does (needs-you
// {after}), a new dispatch --after T-1 is refused as for any dropped task, and once a new rev revives T-1 an --after on
// it waits again.
func TestSpecOnlyDropEndsAnAfterWait(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	r.setSpec("s1", "# T-1 — Stored\n\nDifficulty: medium\n")
	after := func(callID, task string) (string, error) {
		return r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
			CallID: callID}, Task: task, Lane: "L-01", After: []string{"T-1"}, Types: []string{"sonnet"}})
	}
	if reply, err := after("d1", "T-2"); err != nil || !strings.HasPrefix(reply, "WAITING T-2 after T-1 (queued)") {
		t.Fatalf("dispatch T-2 --after T-1: %q, %v; want waiting", reply, err)
	}
	r.passes()
	if st := r.taskStatus("T-2"); st.State != "waiting" {
		t.Fatalf("precondition: T-2 %+v, want waiting", st)
	}
	r.dropSpec("x1", "T-1", "superseded")
	r.waitPrefix("1 fact needs-you{after: T-2 waits for T-1, which is dropped")
	if n := r.taskStep("T-2", "fact dispatch"); n != 0 {
		t.Errorf("T-2 dispatched after its dependency was dropped: %q", r.steps())
	}
	if _, err := after("d2", "T-3"); err == nil || err.Error() != "dispatch T-3: T-1 is dropped: it never merges" {
		t.Errorf("dispatch T-3 --after the dropped T-1: %v; want refused as a dropped task", err)
	}
	r.setSpec("s2", "# T-1 — Stored, widened\n\nDifficulty: medium\n")
	if reply, err := after("d3", "T-3"); err != nil || !strings.HasPrefix(reply, "WAITING T-3 after T-1 (queued)") {
		t.Errorf("dispatch T-3 --after T-1 at rev 2: %q, %v; want waiting again", reply, err)
	}
}

// T-1026: a spec-only drop of T-1 ends a watch of T-1 and a set watch T-1,T-2 with the dropped line, and a watch made
// after the drop is answered with that outcome at once.
func TestSpecOnlyDropEndsWatches(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	r.setSpec("s1", "# T-1 — Stored\n\nDifficulty: medium\n")
	if reply, err := r.setSpecTask("s2", "T-2", "# T-2 — Other\n\nDifficulty: medium\n", ""); err != nil || reply != "SET T-2 rev 1" {
		t.Fatalf("spec set T-2: %q, %v", reply, err)
	}
	for _, set := range []string{"T-1", "T-1,T-2"} {
		if reply, err := r.watch("w-"+set, set, false); err != nil || reply != "WATCHING "+set {
			t.Fatalf("watch %s: %q, %v", set, reply, err)
		}
	}
	r.dropSpec("x1", "T-1", "superseded")
	if d := r.factData("T-1", event.StepDropped); d["rev"] != float64(1) {
		t.Fatalf("precondition: the dropped fact %+v, want a spec-only drop at rev 1", d)
	}
	one, set := "[obor] T-1 dropped: superseded", "[obor] T-1 dropped: superseded (watch T-1,T-2 ended)"
	r.waitFor("the dropped lines posted", func() bool {
		return r.inbox.count("pj-lead", one) == 1 && r.inbox.count("pj-lead", set) == 1
	})
	if reply, err := r.watch("w3", "", false); err != nil || reply != "NO WATCHES" {
		t.Errorf("the list after the drop: %q, %v; want both ended", reply, err)
	}
	for set, want := range map[string]string{"T-1": "T-1 dropped: superseded",
		"T-1,T-2": "T-1 dropped: superseded (watch T-1,T-2 ended)"} {
		if reply, err := r.watch("w4-"+set, set, false); err != nil || reply != want {
			t.Errorf("watch %s after the drop: %q, %v; want %q at once", set, reply, err, want)
		}
	}
}

func TestReplayEndedTaskIntents(t *testing.T) {
	t.Parallel()
	submitted := func(n int) logRec {
		return logRec{n, event.StepSubmitted, event.KindFact, "", map[string]any{"lane": "L-01", "agent": "pj-l-01"}}
	}
	submit := logRec{1, event.StepSubmit, event.KindIntent, event.ClassGit, map[string]any{"lane": "L-01", "agent": "pj-l-01"}}
	work := func(n int) logRec {
		return logRec{n, event.StepWork, event.KindIntent, event.ClassPrompt, map[string]any{"agent": "pj-l-01",
			"job": "T-1/" + string(rune('0'+n)) + "/work"}}
	}
	refused := logRec{1, event.StepFailed, event.KindFact, "", event.FactData{Lane: "L-01", Merge: true, Step: event.StepWork,
		Reason: "work: refused"}}
	dropped := func(n int) logRec {
		return logRec{n, event.StepDropped, event.KindFact, "", event.FactData{Lane: "L-01", Merge: true, Reason: "why"}}
	}
	merged := func(n int) logRec {
		return logRec{n, event.StepMerged, event.KindFact, "", map[string]any{"outside": true}}
	}
	start := logRec{1, event.StepStartWork, event.KindIntent, event.ClassStart, map[string]any{"lane": "L-01",
		"agent": "pj-l-01", "pane": "w1:p1"}}
	for _, c := range []struct {
		name    string
		recs    []logRec
		attempt int    // the open intent's
		step    string // the open intent's
		want    string // its result after the replay
		settled int    // results the first replay writes
	}{
		{"dropped task's submit (G)", []logRec{submitted(1), submit, dropped(1)}, 1, event.StepSubmit,
			`"not run: T-1 dropped"`, 1},
		{"dropped task's typed work (P)", []logRec{work(1), submitted(1), dropped(1)}, 1, event.StepWork,
			`"not run: T-1 dropped"`, 1},
		{"dropped task's earlier agent.start (S)", []logRec{start, submitted(2), dropped(2)}, 1, event.StepStartWork,
			`"not run: T-1 dropped"`, 1},
		{"merged task's earlier refused work (P)", []logRec{work(1), refused, submitted(2), merged(2)}, 1,
			event.StepWork, `"not run: T-1 merged"`, 1},
		{"merged task's submit (G)", []logRec{submitted(1), submit, merged(1)}, 1, event.StepSubmit,
			`"not run: T-1 merged"`, 1},
		{"open task's submit (G): re-run", []logRec{submitted(1), submit}, 1, event.StepSubmit, `{"sha":`, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t)
			r.work("feature.txt", "feature\n")
			r.writeLog(c.recs)
			if r.resultOf(c.attempt, c.step) != "" || len(r.open()) != 1 {
				t.Fatalf("precondition: want one open intent, %s's: %q", c.step, r.open())
			}
			head := r.scratch.LaneHead("L-01")
			starts := func() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.launches) }
			r.start()
			r.waitFor("the replayed intent's result", func() bool { return r.resultOf(c.attempt, c.step) != "" })
			r.passes()
			if got := r.resultOf(c.attempt, c.step); !strings.HasPrefix(got, c.want) {
				t.Errorf("%s's result %s, want %s…", c.step, got, c.want)
			}
			if want := fmt.Sprintf("1 pending intents (%d settled)", c.settled); !strings.Contains(r.out.String(), want) {
				t.Errorf("the replay's line: want %s:\n%s", want, r.out)
			}
			ended := strings.HasPrefix(c.want, `"not run`)
			if ended {
				if got := r.scratch.LaneHead("L-01"); got != head || len(r.prompts()) != 0 ||
					starts() != 0 || r.has("1 intent gate") {
					t.Errorf("an effect ran: lane HEAD %s (was %s), prompts %d, starts %d: %q", got, head,
						len(r.prompts()), starts(), r.steps())
				}
				if out := r.scratch.Pins(); len(out) != 0 {
					t.Errorf("a pin was written: %s", out)
				}
			} else if c.step == event.StepSubmit && r.scratch.LaneHead("L-01") == head {
				t.Errorf("the open task's submit did not run again: lane HEAD unchanged: %q", r.steps())
			}
			if c.step == event.StepWork && len(r.prompts()) != 0 {
				t.Errorf("a work prompt was typed again: %d", len(r.prompts()))
			}
			r.stop()
			r.start()
			if want := "0 pending intents (0 settled)"; ended && !strings.Contains(r.out.String(), want) {
				t.Errorf("the second replay: want %s:\n%s", want, r.out)
			}
			if r.count("1 result "+c.step) != 1 {
				t.Errorf("%d results of %s, want 1: %q", r.count("1 result "+c.step), c.step, r.steps())
			}
		})
	}
}

// A task dropped live with a prompt refused before typing open: the drop settles it at once (no restart needed), and
// nothing is typed afterwards.
func TestDropSettlesOpenIntents(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.writeLog([]logRec{
		{1, event.StepSubmitted, event.KindFact, "", map[string]any{"lane": "L-01", "agent": "pj-l-01"}},
		{1, event.StepNeedsYou, event.KindFact, "", event.FactData{Lane: "L-01", Merge: true, Cause: "gate", Reason: "owner"}},
		{1, event.StepWork, event.KindIntent, event.ClassPrompt, map[string]any{"agent": "pj-l-01", "job": "T-1/1/work"}},
		{1, event.StepFailed, event.KindFact, "", event.FactData{Lane: "L-01", Merge: true, Step: event.StepWork,
			Reason: "work: refused"}},
	})
	r.start()
	r.passes()
	if r.resultOf(1, event.StepWork) != "" || len(r.open()) != 1 {
		t.Fatalf("precondition: the work intent open while the needs-you holds: %q", r.steps())
	}
	reply, err := wire.Verdict(context.Background(), r.sock, &wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "ob-lead", Role: "lead", CallID: "d1"}, Task: "T-1", Why: "why"})
	if err != nil || reply != "DROPPED T-1" {
		t.Fatalf("drop: %q, %v", reply, err)
	}
	// The idle announce's line is the idle state's own intent (T-803): the drop's intents are what this test decides.
	var rest []string
	for _, o := range r.open() {
		if !strings.HasPrefix(o, event.MsgTask+" 0 deliver.") {
			rest = append(rest, o)
		}
	}
	if got := r.resultOf(1, event.StepWork); got != `"not run: T-1 dropped"` || len(rest) != 0 {
		t.Errorf("after the drop: work's result %s, open %q; want not run, none open", got, rest)
	}
	r.passes()
	if len(r.prompts()) != 0 {
		t.Errorf("a prompt was typed after the drop: %d", len(r.prompts()))
	}
}

// verdictRig is the rig with an integrator (pj-int) configured, T-1 submitted and its review job (attempt 2, patch
// "p1") in the log before the app starts, and the app socket listening. Its merge loop is on (an integrator role) but has
// no adapters: the verdicts are answered, the loop does not act on them.
func verdictRig(t *testing.T) (*rig, string) {
	t.Helper()
	r := newRig(t)
	write(t, filepath.Join(r.root, ".obor", "config.json"),
		`{"name":"pj","prefix":"pj","roles":{"integrator":""}}`)
	r.loop = func(string) (*app.Loop, error) { return nil, errors.New("no adapters") }
	data, _ := json.Marshal(app.JobData{Patch: "p1"})
	if err := app.WriteLog(r.root, r.clock.Now(),
		event.Record{Task: "T-1", Attempt: 2, Step: "submitted", Kind: event.KindFact, Data: []byte(`{"lane":"L-01"}`)},
		event.Record{Task: "T-1", Attempt: 2, Step: event.StepReview, Kind: event.KindIntent, Class: event.ClassPrompt, Data: data},
	); err != nil {
		t.Fatal(err)
	}
	r.start()
	return r, r.listen()
}

func TestVerdictRefusals(t *testing.T) {
	t.Parallel()
	r, sock := verdictRig(t)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		call *wire.VerdictReq
		want string
	}{
		{"a worker", r.call(wire.MethodAccept, "c1", func(c *wire.VerdictReq) { c.Agent, c.Role = "pj-l-01", "worker" }),
			"only the integrator (pj-int) gives verdicts, not pj-l-01 (role worker)"},
		{"the integrator's role with another name", r.call(wire.MethodAccept, "c1", func(c *wire.VerdictReq) { c.Agent = "pj-lead" }),
			"only the integrator"},
		{"no role", r.call(wire.MethodReject, "c1", func(c *wire.VerdictReq) { c.Role = "" }), "only the integrator"},
		{"a stale job", r.call(wire.MethodAccept, "c1", func(c *wire.VerdictReq) { c.Job = "T-1/1/review" }),
			"accept: stale job T-1/1/review (T-1's job is T-1/2/review)"},
		{"a task with no job", r.call(wire.MethodSendback, "c1", func(c *wire.VerdictReq) { c.Task, c.Job = "T-2", "" }),
			"sendback: no job open for T-2"},
		{"a stale patch", r.call(wire.MethodAccept, "c1", func(c *wire.VerdictReq) { c.Patch = "p0" }),
			"stale: re-read the candidate (T-1's patch is p1, not p0)"},
		{"resolved on a review job", r.call(wire.MethodResolved, "c1"), "is a review job"},
		{"a sendback without why", r.call(wire.MethodSendback, "c1", func(c *wire.VerdictReq) { c.Note = " " }), "say why"},
		{"no call id", r.call(wire.MethodAccept, ""), "a call id and a task are required"},
		{"another project", r.call(wire.MethodAccept, "c1", func(c *wire.VerdictReq) { c.Root = "/elsewhere" }),
			"no served project at /elsewhere"},
	} {
		reply, err := wire.Verdict(ctx, sock, c.call)
		if err == nil || !strings.Contains(err.Error(), c.want) || reply != "" {
			t.Errorf("%s: %q, %v; want the error %q", c.name, reply, err, c.want)
		}
	}
	eq(t, r.verdicts(), nil, "verdict records after refusals")

	// The refused call id c1 is not taken: the integrator's own call with it is answered.
	reply, err := wire.Verdict(ctx, sock, r.call(wire.MethodAccept, "c1"))
	if err != nil || reply != "ACCEPT T-1 (job T-1/2/review)" {
		t.Fatalf("accept after refusals: %q, %v", reply, err)
	}
}

func TestVerdictRepeatedCallIDGetsStoredReply(t *testing.T) {
	t.Parallel()
	r, sock := verdictRig(t)
	ctx := context.Background()
	reply, err := wire.Verdict(ctx, sock, r.call(wire.MethodSendback, "c7", func(c *wire.VerdictReq) { c.Job = "" }))
	if err != nil || reply != "SENDBACK T-1 (job T-1/2/review)" {
		t.Fatalf("sendback (the current job): %q, %v", reply, err)
	}
	eq(t, r.verdicts(), []string{"intent sendback.c7", "result sendback.c7"}, "recorded once, a fact under Log.Do")
	var d struct{ Job, Caller, Note string }
	intent := r.record(event.KindIntent, "sendback.c7")
	if err := json.Unmarshal(intent.Data, &d); err != nil || d.Job != "T-1/2/review" || d.Caller != "pj-int" ||
		d.Note != "why" {
		t.Errorf("sendback intent data = %s (%v)", intent.Data, err)
	}

	// The job is answered: the same call again gets the stored reply and records nothing; a new call id is refused.
	reply, err = wire.Verdict(ctx, sock, r.call(wire.MethodSendback, "c7"))
	if err != nil || reply != "SENDBACK T-1 (job T-1/2/review)" {
		t.Errorf("repeated call id: %q, %v; want the stored reply", reply, err)
	}
	if _, err := wire.Verdict(ctx, sock, r.call(wire.MethodAccept, "c8")); err == nil ||
		!strings.Contains(err.Error(), "no job open for T-1") {
		t.Errorf("a second verdict on the answered job: %v", err)
	}
	eq(t, r.verdicts(), []string{"intent sendback.c7", "result sendback.c7"}, "nothing more recorded")

	// After a restart (replay) the call id still gets its reply, and the job stays answered.
	r.stop()
	r.start()
	sock = r.listen()
	reply, err = wire.Verdict(ctx, sock, r.call(wire.MethodSendback, "c7"))
	if err != nil || reply != "SENDBACK T-1 (job T-1/2/review)" {
		t.Errorf("repeated call id after replay: %q, %v", reply, err)
	}
	if _, err := wire.Verdict(ctx, sock, r.call(wire.MethodReject, "c9")); err == nil ||
		!strings.Contains(err.Error(), "no job open for T-1") {
		t.Errorf("a verdict on a job answered before the restart: %v", err)
	}
}

func TestVerdictServeNotRunning(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := wire.Verdict(ctx, filepath.Join(t.TempDir(), "none.sock"), &wire.VerdictReq{Verb: wire.MethodAccept})
	if err == nil || !strings.Contains(err.Error(), "obor serve is not running") {
		t.Errorf("verdict without obor serve: %v", err)
	}
}

// agents records the restarts asked of it.
type agents struct{ restarts []port.Launch }

func (a *agents) Launch(_ context.Context, req port.Launch) (port.Started, error) {
	a.restarts = append(a.restarts, req)
	return port.Started{Line: "STARTED " + req.Agent}, nil
}

func (a *agents) Stop(context.Context, string, string, bool) (string, error) { return "", nil }

func TestStartIntegrator(t *testing.T) {
	t.Parallel()
	r, _ := verdictRig(t)
	ag := &agents{}
	p := r.f.Agent("w1:p9", "pj-int", "idle", r.root)
	p.Label = "pj-int"
	r.stop()
	r.start() // its snapshot has the integrator
	if n := len(r.app.Status().Project.Agents); n != 3 {
		t.Fatalf("precondition: %d agents seen, want 3 with the integrator", n)
	}
	s, err := app.StartIntegrator(r.app, context.Background(), ag, r.root, port.Launch{})
	line := s.Line
	if err != nil || line != "running" || len(ag.restarts) != 0 {
		t.Fatalf("integrator running: %q, %v, restarts %+v; want none", line, err, ag.restarts)
	}

	r.f.Remove("w1:p9")
	r.stop()
	r.start()
	if n := len(r.app.Status().Project.Agents); n != 2 {
		t.Fatalf("precondition: %d agents seen, want 2 without the integrator", n)
	}
	s, err = app.StartIntegrator(r.app, context.Background(), ag, r.root, port.Launch{})
	line = s.Line
	if err != nil || line != "STARTED pj-int" || len(ag.restarts) != 1 || ag.restarts[0] != (port.Launch{Agent: "pj-int"}) {
		t.Errorf("integrator not running: %q, %v, restarts %+v; want one start of pj-int", line, err, ag.restarts)
	}
}
