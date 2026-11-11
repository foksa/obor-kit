package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// Serve's lanes table is the projection of its fold: after a dispatch (its lane.prepare), a submission, a
// merge (the row landed), a re-dispatch of the lane (not landed) and a drop the table equals the fold's rows and
// what the log says, and a replay writes the same table. The row carries its dispatch's type reference; a dispatch that recorded none has
// none, the next dispatch's row has its own.
func TestLaneRowsProjectTheLog(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"lanes":{"count":2},"agents":{"roles":{"integrator":""}}}`)
	h.connect()
	a, p := h.a, h.proj()
	add := func(task string, attempt int, step, kind string, d any) event.Record {
		t.Helper()
		h.now = h.now.Add(time.Minute)
		raw, _ := json.Marshal(d)
		r, err := p.log.Append(event.Record{Task: task, Attempt: attempt, Step: step, Kind: kind, Data: raw}, h.now)
		if err != nil {
			t.Fatal(err)
		}
		a.mu.Lock()
		p.foldRecord(r)
		a.mu.Unlock()
		return r
	}
	table := func(when string, want ...store.LaneRow) {
		t.Helper()
		got, err := store.ReadLanesAt(h.root)
		if err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		a.mu.Lock()
		fold := p.laneTable()
		a.mu.Unlock()
		byLane := map[string]store.LaneRow{}
		for _, r := range want {
			byLane[r.Lane] = r
		}
		if !reflect.DeepEqual(got, byLane) {
			t.Errorf("%s: table %+v, want %+v", when, got, byLane)
		}
		if want == nil {
			want = []store.LaneRow{}
		}
		if !reflect.DeepEqual(fold, want) {
			t.Errorf("%s: the fold's rows %+v, want %+v", when, fold, want)
		}
	}
	table("load: no records")

	disp := add("T-1", 1, event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"), Agent: "pj-l-01",
		Branch: "task/T-1", Target: "b1"})
	table("dispatched, not prepared")
	add("T-1", 1, event.StepPrepare, event.KindIntent, port.PrepareInput{})
	// An old prepare result still carrying "head" folds: the field is ignored.
	add("T-1", 1, event.StepPrepare, event.KindResult, map[string]string{"base": "b1", "head": "h1"})
	at := event.RecordTime(disp).Unix()
	row := store.LaneRow{Lane: "L-01", Task: "T-1", Branch: "task/T-1", Base: "b1", DispatchedAt: at}
	table("prepared: awaits task_done, no head yet (T-1015)", row)

	sub := add("T-1", 1, event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01", Report: "r"})
	row.Finished = event.RecordTime(sub).Unix()
	table("submitted: finished", row)
	// The submit result's pinned lane commit is the row's head; a resubmission moves it, a refusal does not.
	add("T-1", 1, nth(event.StepSubmit, 1), event.KindResult, submitOut{SHA: "s1"})
	row.Head = "s1"
	table("submit pinned s1", row)
	add("T-1", 1, nth(event.StepSubmit, 2), event.KindResult, submitOut{SHA: "s2"})
	row.Head = "s2"
	table("resubmit pinned s2", row)
	add("T-1", 1, nth(event.StepSubmit, 3), event.KindResult, submitOut{Refused: "no"})
	table("a refused submit keeps s2", row)
	add("T-1", 1, event.StepMerged, event.KindFact, event.MergedData{})
	row.Landed = true // the free rule's landed fact
	table("merged: the row stays, landed", row)

	add("T-2", 1, event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"), Agent: "pj-l-01",
		Branch: "task/T-2", Target: "b2", Effort: "high", Type: "opus:high"})
	add("T-2", 1, event.StepPrepare, event.KindIntent, port.PrepareInput{})
	add("T-2", 1, event.StepPrepare, event.KindResult, prepareOut{Base: "b2"})
	row2 := store.LaneRow{Lane: "L-01", Task: "T-2", Branch: "task/T-2", Base: "b2",
		DispatchedAt: h.now.Add(-2 * time.Minute).Unix(), Type: "opus:high"}
	table("the lane re-dispatched", row2)

	// A replay of the same log writes the same table (the projection is idempotent).
	if err := store.WriteLanes(p.log.DB(), nil); err != nil {
		t.Fatal(err)
	}
	h.a.closeLog()
	a = newTestApp(Options{Root: h.root, Now: func() time.Time { return h.now }}, h.panes, h.lanes)
	a.loadProject()
	p = a.p
	table("replayed", row2)
	defer a.closeLog()

	add("T-2", 1, event.StepDropped, event.KindFact, event.FactData{Lane: "L-01", Reason: "abandoned", Merge: true})
	table("dropped: the row clears")
}

// The row's blocked is serve's own answer whether the task's worker has a question in force: the fold the Stop
// hook reads instead of folding the log itself. A needs-you{asks} sets it; ordinary worker activity (resume, a stall)
// keeps it; an answer ends it, but a drive's answer does not, and neither a dispatch nor a sendback does; an
// unblocked does.
func TestLaneRowsBlockedFollowsTheFold(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"lanes":{"count":2},"agents":{"roles":{"integrator":""}}}`)
	h.connect()
	a, p := h.a, h.proj()
	add := func(task, step, kind string, d any) {
		t.Helper()
		h.now = h.now.Add(time.Minute)
		raw, _ := json.Marshal(d)
		r, err := p.log.Append(event.Record{Task: task, Attempt: 1, Step: step, Kind: kind, Data: raw}, h.now)
		if err != nil {
			t.Fatal(err)
		}
		a.mu.Lock()
		p.foldRecord(r)
		a.mu.Unlock()
	}
	blocked := func(when string, want bool) {
		t.Helper()
		row, ok, err := store.ReadLaneAt(h.root, "L-01")
		if err != nil || !ok || row.Blocked != want {
			t.Errorf("%s: row %+v, %v, %v; want blocked %v", when, row, ok, err, want)
		}
	}
	add("T-1", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"),
		Agent: "pj-l-01", Branch: "task/T-1", Target: "b1"})
	add("T-1", event.StepPrepare, event.KindResult, prepareOut{Base: "b1"})
	blocked("no question", false)
	add("T-1", event.StepNeedsYou, event.KindFact, event.FactData{Lane: "L-01", Reason: reasonAsks + "may I?"})
	blocked("a question in force", true)
	add("T-1", event.StepResumed, event.KindFact, event.FactData{})
	add("T-1", event.StepStalled, event.KindFact, event.FactData{})
	blocked("ordinary activity", true)
	add(event.MsgTask, event.StepAnswered, event.KindFact, event.Message{Task: "T-02"})
	blocked("an answer for another task", true)
	add(event.MsgTask, event.StepAnswered, event.KindFact, event.Message{Task: "T-1", Driver: true})
	blocked("a drive's answer", true)
	add("T-1", event.StepSendback, event.KindFact, noteData{Note: "fix it"})
	add("T-1", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"),
		Agent: "pj-l-01", Branch: "task/T-1", Target: "b1"})
	blocked("a sendback and a dispatch", true)
	add(event.MsgTask, event.StepAnswered, event.KindFact, event.Message{Task: "T-1"})
	blocked("the owner's answer", false)
	add("T-1", event.StepNeedsYou, event.KindFact, event.FactData{Lane: "L-01", Reason: reasonAsks + "and now?"})
	blocked("a fresh question", true)
	add("T-1", event.StepUnblocked, event.KindFact, event.FactData{})
	blocked("an unblocked", false)
}

// obor mcp's task_done names only the caller's lane: serve resolves it to its row's task; a lane with no row
// is refused, nothing recorded.
func TestTaskDoneByLane(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"agents":{"roles":{"integrator":""}}}`)
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{}, nil }
	h.connect()
	p := h.proj()
	h.a.mu.Lock()
	p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"}
	h.a.mu.Unlock()
	if _, err := h.a.taskDone(wire.TaskDoneReq{Envelope: wire.Envelope{Root: h.root, Role: "worker", Agent: "pj-l-01",
		CallID: "c0"}, Lane: "L-02",
		Report: "Summary: x"}); err == nil || err.Error() != "task_done: lane L-02 holds no task in obor serve" {
		t.Errorf("a lane with no row: %v", err)
	}
	_, err := h.a.taskDone(wire.TaskDoneReq{Envelope: wire.Envelope{Root: h.root, Role: "worker", Agent: "pj-l-01",
		CallID: "c1"}, Lane: "L-01", Report: "Summary: x"})
	if err == nil || err.Error() == "task_done: lane L-01 holds no task in obor serve" {
		t.Errorf("L-01's row holds T-1: the call reaches the report check, got %v", err)
	}
}

// A task merged by hand, outside serve, lands its lane row too: drive's merge-evidence read (Scratch.Landed)
// records the merged fact, and the row serve writes says landed; before it, the submitted task's row has not.
func TestLaneRowLandedByAMergeOutside(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"lanes":{"count":2},"agents":{"roles":{"integrator":""}}}`)
	h.connect()
	a, p := h.a, h.proj()
	add := func(step, kind string, d any) {
		t.Helper()
		h.now = h.now.Add(time.Minute)
		raw, _ := json.Marshal(d)
		r, err := p.log.Append(event.Record{Task: "T-1", Attempt: 1, Step: step, Kind: kind, Data: raw}, h.now)
		if err != nil {
			t.Fatal(err)
		}
		a.mu.Lock()
		p.foldRecord(r)
		a.mu.Unlock()
	}
	add(event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"), Agent: "pj-l-01",
		Branch: "task/T-1", Target: "b1"})
	add(event.StepPrepare, event.KindResult, prepareOut{Base: "b1"})
	add(event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01", Report: "r"})
	if row, ok, err := store.ReadLaneAt(h.root, "L-01"); err != nil || !ok || row.Task != "T-1" || row.Landed {
		t.Fatalf("precondition: L-01's row %+v, %v, %v; want T-1 submitted, not landed", row, ok, err)
	}
	a.drive(context.Background(), p, &Loop{Workflow: WorkflowOf(landedScratch{})}, "T-1")
	a.mu.Lock()
	state := p.folded.loop["T-1"].state
	a.mu.Unlock()
	if state != stMerged {
		t.Fatalf("after drive with merge evidence: T-1 %s, want merged (a merge outside)", state)
	}
	row, ok, err := store.ReadLaneAt(h.root, "L-01")
	if err != nil || !ok || row.Task != "T-1" || !row.Landed {
		t.Errorf("L-01's row %+v, %v, %v; want T-1 landed", row, ok, err)
	}
	// No submit result recorded a head: free only when the merge evidence proves the lane's HEAD merged.
	if row.Head != "" {
		t.Fatalf("precondition: L-01's row has head %q with no submit result, want none", row.Head)
	}
	if got := port.FreeState(&row, "h9", false, func() bool { return true }); got != port.LaneFree {
		t.Errorf("FreeState of the landed row, HEAD merged = %q, want free", got)
	}
	if got := port.FreeState(&row, "h9", false, func() bool { return false }); got != port.LaneWorking {
		t.Errorf("FreeState of the landed row, HEAD not merged = %q, want working", got)
	}
}

// A failed lane-row write fails the step that caused it (no worker on a missing row) and shows in status;
// once the write works the step goes on.
func TestLaneRowWriteFailureStopsStep(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	defer r.a.closeLog()
	dispatchT2(r)
	p, f := r.proj("pj"), r.fakes["pj"]
	if _, err := p.log.DB().Exec(`CREATE TRIGGER lanes_fail BEFORE INSERT ON lanes WHEN NEW.lane = 'L-02' BEGIN SELECT RAISE(FAIL, 'injected lane-row failure'); END`); err != nil {
		t.Fatal(err)
	}
	fx, err := r.a.loopOf(p)
	if err != nil {
		t.Fatal(err)
	}
	r.a.drive(context.Background(), p, fx, "T-2")
	if f.restarts != 0 {
		t.Error("worker started on a missing lane row")
	}
	if got := r.status("pj").LogError; !strings.Contains(got, "injected lane-row failure") {
		t.Errorf("status log_error = %q, want the lane-row error", got)
	}
	if _, err := p.log.DB().Exec("DROP TRIGGER lanes_fail"); err != nil {
		t.Fatal(err)
	}
	r.a.drive(context.Background(), p, fx, "T-2")
	if row, ok, err := store.ReadLaneAt(p.root, "L-02"); err != nil || !ok || row.Task != "T-2" {
		t.Errorf("row after the retry = %+v %v %v, want T-2", row, ok, err)
	}
	if f.restarts != 1 {
		t.Errorf("worker starts after the retry = %d, want 1", f.restarts)
	}
	if got := r.status("pj").LogError; got != "" {
		t.Errorf("status log_error after the retry = %q, want none", got)
	}
}

// The persistent case: while the lane-row write keeps failing, no task effect starts, on any pass; the step's
// result is folded but the row is neither missing nor stale for a worker. Removing the failure repairs the row and
// the task goes on. The control is the failure removed: the worker starts.
func TestLaneRowWriteFailurePersistent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		prev bool // the lane holds a previous task's row the failed writes cannot replace
	}{
		{"absent row", false},
		{"previous task's row", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			defer r.a.closeLog()
			dispatchT2(r)
			p, f := r.proj("pj"), r.fakes["pj"]
			if c.prev {
				prev := []store.LaneRow{{Lane: "L-02", Task: "T-0", Branch: "task/T-0", Base: "b",
					Head: "h", DispatchedAt: 1}}
				if err := store.WriteLanes(p.log.DB(), prev); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := p.log.DB().Exec(`CREATE TRIGGER lanes_fail BEFORE INSERT ON lanes WHEN NEW.lane = 'L-02' BEGIN SELECT RAISE(FAIL, 'injected lane-row failure'); END`); err != nil {
				t.Fatal(err)
			}
			fx, err := r.a.loopOf(p)
			if err != nil {
				t.Fatal(err)
			}
			for pass := 1; pass <= 4; pass++ {
				r.a.drive(context.Background(), p, fx, "T-2")
				if got := r.status("pj").LogError; !strings.Contains(got, "injected lane-row failure") {
					t.Fatalf("pass %d: status log_error = %q, want the lane-row error", pass, got)
				}
				if row, ok, err := store.ReadLaneAt(p.root, "L-02"); err != nil || (ok && row.Task == "T-2") {
					t.Fatalf("pass %d: row = %+v %v %v; want not T-2 while the write fails", pass, row, ok, err)
				}
				if f.restarts != 0 {
					t.Fatalf("pass %d: worker started on a missing or stale lane row (restarts %d)", pass, f.restarts)
				}
			}
			if _, err := p.log.DB().Exec("DROP TRIGGER lanes_fail"); err != nil {
				t.Fatal(err)
			}
			r.a.drive(context.Background(), p, fx, "T-2")
			if row, ok, err := store.ReadLaneAt(p.root, "L-02"); err != nil || !ok || row.Task != "T-2" {
				t.Errorf("row after the failure is removed = %+v %v %v, want T-2", row, ok, err)
			}
			if f.restarts != 1 {
				t.Errorf("worker starts after the failure is removed = %d, want 1", f.restarts)
			}
			if got := r.status("pj").LogError; got != "" {
				t.Errorf("status log_error after the repair = %q, want none", got)
			}
		})
	}
}

// The lane intents embed the dispatch's lane fields (port.Dispatch). The dispatch fact keeps its stored bytes;
// the intents' change to the fact's keys, and an intent stored before decodes into the same input.
const (
	oldPrepare = `{"Task":"T-1","Lane":"L-01","Dir":"/wt/L-01","Branch":"task/T-1","Target":"b1","Agent":"pj-l-01","Redo":false,"At":"2026-11-04T21:34:06Z"}`
	newPrepare = `{"Task":"T-1","lane":"L-01","dir":"/wt/L-01","agent":"pj-l-01","branch":"task/T-1","target":"b1","At":"2026-11-04T21:34:06Z"}`
	oldFree    = `{"Lane":"L-01","Dir":"","Branch":"","Target":"","Discard":true}`
	newFree    = `{"lane":"L-01","dir":"","branch":"","target":"","Discard":true}`
)

var prepareIn = port.PrepareInput{Task: "T-1", Dispatch: port.Dispatch{Lane: "L-01", Dir: "/wt/L-01", Agent: "pj-l-01",
	Branch: "task/T-1", Target: "b1"}, At: time.Date(2026, 11, 4, 21, 34, 6, 0, time.UTC)}

var freeIn = port.FreeInput{Dispatch: port.Dispatch{Lane: "L-01"}, Discard: true}

func TestLaneRecordShapes(t *testing.T) {
	t.Parallel()
	// The dispatch fact as an older log holds it (its keys, order and omitted empties).
	d := dispatchData{Dispatch: port.Dispatch{Lane: "L-03", Dir: "/wt/L-03", Agent: "ob-l-03", Branch: "task/T-849",
		Target: "4cac"}, Level: "simple", Types: []string{"deepseek", "opus:medium"}, Type: "deepseek", Caller: "ob-lead",
		Call: "c00d", Reply: "DISPATCHED"}
	const fact = `{"lane":"L-03","dir":"/wt/L-03","agent":"ob-l-03","branch":"task/T-849","target":"4cac","level":"simple",` +
		`"types":["deepseek","opus:medium"],"type":"deepseek","caller":"ob-lead","call":"c00d","reply":"DISPATCHED"}`
	for _, c := range []struct {
		what string
		v    any
		want string
	}{{"dispatch fact", d, fact}, {"prepare intent", prepareIn, newPrepare}, {"free intent", freeIn, newFree}} {
		if b, _ := json.Marshal(c.v); string(b) != c.want {
			t.Errorf("%s stored as %s, want %s", c.what, b, c.want)
		}
	}
	var p port.PrepareInput
	if err := json.Unmarshal([]byte(oldPrepare), &p); err != nil || p != prepareIn {
		t.Errorf("an old prepare intent decodes as %+v, %v; want %+v", p, err, prepareIn)
	}
	var f port.FreeInput
	if err := json.Unmarshal([]byte(oldFree), &f); err != nil || f != freeIn {
		t.Errorf("an old free intent decodes as %+v, %v; want %+v", f, err, freeIn)
	}
}
