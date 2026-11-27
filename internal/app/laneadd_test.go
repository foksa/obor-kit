package app

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// laneAddCalls is a test's Loop.EnsureLane: it records each lane and its error. As setup.EnsureLane with the lane
// table over its marker: a failed add leaves the lane missing in the fake lane table (its worktree may be there, its
// marker keeps it missing), a successful one makes it free. A gate (non-nil) holds each add until it is closed.
type laneAddCalls struct {
	mu    sync.Mutex
	calls []string
	errs  map[string]error
	gate  chan struct{}
	lanes *fakeLanes
}

func (c *laneAddCalls) ensure(_ context.Context, lane string) error {
	c.mu.Lock()
	c.calls = append(c.calls, lane)
	err, gate := c.errs[lane], c.gate
	c.mu.Unlock()
	if gate != nil {
		<-gate
	}
	st := port.LaneFree
	if err != nil {
		st = port.LaneMissing
	}
	setLaneState(c.lanes, lane, st)
	return err
}

// setLaneState sets lane's state in the fake lane table (as a fresh read of the real one would say it).
func setLaneState(ls *fakeLanes, lane, state string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	for i := range ls.lanes {
		if ls.lanes[i].Name == lane {
			ls.lanes[i].State = state
		}
	}
}

func (c *laneAddCalls) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// laneAddHarness is pj with a configured lane count and the fake lane table ls, its merge loop's EnsureLane the
// test's (a nil map errs: every add succeeds) and dispatch adapters (staleFx) with spec T-1 (medium) to dispatch.
func laneAddHarness(t *testing.T, count int, ls []port.Lane, errs map[string]error) (*harness, *laneAddCalls) {
	t.Helper()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"),
		`{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"lanes":{"count":`+strconv.Itoa(count)+
			`},"agents":{"roles":{"integrator":""},`+harnessModels+`}}`)
	for i := range ls {
		if ls[i].Dir == "" {
			ls[i].Dir = h.lane(ls[i].Name)
		}
	}
	h.lanes.lanes = ls
	calls := &laneAddCalls{errs: errs, lanes: h.lanes}
	laneAddLoop(h, calls)
	h.connect()
	t.Cleanup(func() { h.a.effects.Wait(); h.a.closeLog() })
	if _, _, err := store.SetSpec(h.a.p.log.DB(), "T-1", "# T-1 — Test\n\nDifficulty: medium\n", "", "lead", h.now); err != nil {
		t.Fatal(err)
	}
	return h, calls
}

// laneAddLoop makes calls h's merge loop's EnsureLane, beside the dispatch adapters.
func laneAddLoop(h *harness, calls *laneAddCalls) {
	h.a.opt.Loop = func(root string) (*Loop, error) {
		return withCfg(root, &Loop{EnsureLane: calls.ensure, Workflow: WorkflowOf(staleFx{}), Specs: staleFx{}}), nil
	}
}

// laneAddPass runs h's lane add pass alone (no task driven), the cached lane table set to cached first (nil: as it is),
// and waits for the adds it starts unless calls holds them.
func laneAddPass(t *testing.T, h *harness, calls *laneAddCalls, cached []port.Lane) {
	t.Helper()
	p := h.a.p
	fx, err := h.a.loopOf(p)
	if err != nil {
		t.Fatal(err)
	}
	if cached != nil {
		h.a.mu.Lock()
		p.setLanes(cached)
		h.a.mu.Unlock()
	}
	h.a.laneAddPass(context.Background(), p, fx)
	calls.mu.Lock()
	gated := calls.gate != nil
	calls.mu.Unlock()
	if !gated {
		h.a.effects.Wait()
	}
}

// fresh is h's fake lane table now (what a fresh read says).
func fresh(h *harness) []port.Lane {
	ls, _ := h.lanes.Lanes(context.Background(), "")
	return ls
}

// laneDispatch is the lead's dispatch of T-1 onto lane.
func laneDispatch(h *harness, call, lane string) (string, error) {
	return h.a.dispatchCall(context.Background(), wire.DispatchReq{Envelope: wire.Envelope{Root: h.root, Role: "lead",
		CallID: call}, Task: "T-1", Lane: lane})
}

// laneNeedsOpen: lane's needs-you is open in p (folded: not settled).
func laneNeedsOpen(h *harness, lane string) bool {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	return len(h.a.p.folded.active[event.LaneTaskPrefix+lane]) > 0
}

// laneNeeds is the reason of lane's needs-you in p's log ("" none).
func laneNeeds(t *testing.T, p *proj, lane string) string {
	t.Helper()
	recs, err := event.ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	reason := ""
	for _, r := range recs {
		if r.Task != event.LaneTaskPrefix+lane || r.Step != event.StepNeedsYou || r.Kind != event.KindFact {
			continue
		}
		reason = r.Fact().Reason
	}
	return reason
}

// A count raised since the lane table's last read is read again, and the lane it adds is added once; the lane is read
// again once the add returns.
func TestLaneAddRaisesTheTableAndAddsOnce(t *testing.T) {
	h, calls := laneAddHarness(t, 3, []port.Lane{
		{Name: "L-01", State: port.LaneFree},
		{Name: "L-02", State: port.LaneFree},
	}, nil)
	// The config now counts L-03; the table (the last read) does not know it yet.
	scans := h.lanes.scans()
	h.lanes.mu.Lock()
	h.lanes.lanes = append(h.lanes.lanes, port.Lane{Name: "L-03", Dir: h.lane("L-03"), State: port.LaneMissing})
	h.lanes.mu.Unlock()

	if !h.a.p.merging {
		t.Fatalf("precondition: the project has no merge loop")
	}
	h.a.advance(context.Background())
	h.a.effects.Wait()
	got := calls.all()
	if len(got) != 1 || got[0] != "L-03" {
		t.Fatalf("EnsureLane calls = %q, want [L-03]", got)
	}
	if h.lanes.scans() <= scans {
		t.Errorf("the lane table was not read again to name the configured lane")
	}
	h.a.mu.Lock()
	dirty := h.a.p.scan.dirty
	h.a.mu.Unlock()
	if !dirty {
		t.Errorf("the lane table was not marked for a read after the add")
	}

	h.a.advance(context.Background())
	h.a.effects.Wait()
	if got := calls.all(); len(got) != 1 {
		t.Errorf("EnsureLane calls after a second pass = %q, want the one", got)
	}
}

// A missing lane a task holds is left as it is: its worktree is the worker's.
func TestLaneAddLeavesAMissingLaneItsTask(t *testing.T) {
	h, calls := laneAddHarness(t, 2, []port.Lane{
		{Name: "L-01", State: port.LaneFree},
		{Name: "L-02", State: port.LaneMissing},
	}, nil)
	h.a.mu.Lock()
	h.a.p.folded.lr.rows["L-02"] = store.LaneRow{Lane: "L-02", Task: "T-2"}
	h.a.mu.Unlock()

	h.a.advance(context.Background())
	h.a.effects.Wait()
	if got := calls.all(); len(got) != 0 {
		t.Errorf("EnsureLane calls = %q, want none for a lane a task holds", got)
	}
}

// A failed add is a needs-you naming the lane, its error and the command that does it; the retry waits the backoff:
// a pass at once asks again nothing, a pass past the delay asks once more. Its setup failed after the worktree was
// made, so a fresh read says the lane missing (its marker): no dispatch takes it and the needs-you stays through a pass
// over that read; the retry that succeeds frees it and settles the needs-you.
func TestLaneAddFailureNeedsYouAndBacksOff(t *testing.T) {
	h, calls := laneAddHarness(t, 2, []port.Lane{
		{Name: "L-01", State: port.LaneFree},
		{Name: "L-02", State: port.LaneMissing},
	}, map[string]error{"L-02": errors.New("boom")})
	ctx := context.Background()

	h.a.advance(ctx)
	h.a.effects.Wait()
	if got := calls.all(); len(got) != 1 {
		t.Fatalf("EnsureLane calls = %q, want the one attempt", got)
	}
	reason := laneNeeds(t, h.a.p, "L-02")
	for _, want := range []string{"L-02", "boom", "obor setup lane L-02"} {
		if !strings.Contains(reason, want) {
			t.Errorf("needs-you reason %q lacks %q", reason, want)
		}
	}
	if _, err := laneDispatch(h, "d1", "L-02"); err == nil || !strings.Contains(err.Error(), "lane L-02 is missing") {
		t.Errorf("dispatch onto the lane whose setup failed: %v, want refused (missing)", err)
	}

	laneAddPass(t, h, calls, fresh(h)) // the same clock: inside the backoff, over a fresh read
	if got := calls.all(); len(got) != 1 {
		t.Errorf("EnsureLane calls inside the backoff = %q, want the one", got)
	}
	if !laneNeedsOpen(h, "L-02") {
		t.Errorf("the needs-you settled while the lane reads missing")
	}

	calls.mu.Lock()
	calls.errs = nil
	calls.mu.Unlock()
	h.now = h.now.Add(5 * time.Second) // the default StepRetry
	laneAddPass(t, h, calls, nil)
	if got := calls.all(); len(got) != 2 {
		t.Errorf("EnsureLane calls after the backoff = %q, want a second", got)
	}
	if laneNeedsOpen(h, "L-02") {
		t.Errorf("the needs-you is open after the retry succeeded")
	}
	if reply, err := laneDispatch(h, "d2", "L-02"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-02") {
		t.Errorf("dispatch onto the added lane: %q, %v; want DISPATCHED", reply, err)
	}
}

// A serve that restarts with an add pending (its marker: the lane reads missing, its worktree there or not) keeps the
// lane from dispatch and its needs-you, and adds it again at once (the backoff was the stopped serve's).
func TestLaneAddPendingAcrossARestart(t *testing.T) {
	h, calls := laneAddHarness(t, 2, []port.Lane{
		{Name: "L-01", State: port.LaneFree},
		{Name: "L-02", State: port.LaneMissing},
	}, map[string]error{"L-02": errors.New("boom")})
	laneAddPass(t, h, calls, fresh(h))
	if got := calls.all(); len(got) != 1 || !laneNeedsOpen(h, "L-02") {
		t.Fatalf("precondition: one failed add (%q) and its needs-you", got)
	}

	h.a.closeLog()
	h.a = newTestApp(Options{Root: h.root, Now: h.a.opt.Now, Out: h.a.opt.Out}, h.panes, h.lanes)
	calls.mu.Lock()
	calls.errs = nil
	calls.mu.Unlock()
	laneAddLoop(h, calls)
	h.connect()
	if !laneNeedsOpen(h, "L-02") {
		t.Errorf("the restart settled the needs-you of a lane that reads missing")
	}
	if _, err := laneDispatch(h, "d1", "L-02"); err == nil || !strings.Contains(err.Error(), "lane L-02 is missing") {
		t.Errorf("dispatch after the restart: %v, want refused (missing)", err)
	}
	laneAddPass(t, h, calls, fresh(h)) // the same clock as the failure
	if got := calls.all(); len(got) != 2 {
		t.Errorf("EnsureLane calls after the restart = %q, want the add again", got)
	}
	if laneNeedsOpen(h, "L-02") {
		t.Errorf("the needs-you is open after the add succeeded")
	}
}

// A pass after a successful add, its cached row still the missing one (no refresh yet), reads the lane afresh: no
// second add, the setup ran once.
func TestLaneAddDoneStaleRowAddsNoMore(t *testing.T) {
	h, calls := laneAddHarness(t, 2, []port.Lane{
		{Name: "L-01", State: port.LaneFree},
		{Name: "L-02", State: port.LaneMissing},
	}, nil)
	stale := fresh(h)
	laneAddPass(t, h, calls, stale)
	if got := calls.all(); len(got) != 1 || fresh(h)[1].State != port.LaneFree {
		t.Fatalf("precondition: one add (%q) and L-02 free after it", got)
	}
	laneAddPass(t, h, calls, stale)
	if got := calls.all(); len(got) != 1 {
		t.Errorf("EnsureLane calls after a pass over the stale row = %q, want the one", got)
	}
}

// An add's start and a dispatch claiming the lane are one lock's (a.verdicts): whichever decides first holds it while
// it decides, and the other then loses. Add first: the dispatch is refused (the add runs); dispatch first: no add
// starts (the lane is the task's).
func TestLaneAddAndDispatchExclusive(t *testing.T) {
	held := func(h *harness) bool {
		if h.a.verdicts.TryLock() {
			h.a.verdicts.Unlock()
			return false
		}
		return true
	}
	hookOnce := func(h *harness, point string, f func()) *bool {
		ran := false
		hook := func(at string) {
			if at == point && !ran {
				ran = true
				f()
			}
		}
		h.a.hooked.Store(&hook)
		return &ran
	}
	t.Run("add first", func(t *testing.T) {
		h, calls := laneAddHarness(t, 2, []port.Lane{
			{Name: "L-01", State: port.LaneWorking, Task: "T-9"},
			{Name: "L-02", State: port.LaneMissing},
		}, nil)
		calls.gate = make(chan struct{})
		var release sync.Once
		open := func() { release.Do(func() { close(calls.gate) }) }
		t.Cleanup(open) // before the harness's effects wait: a failed check leaves no add held
		locked := false
		ran := hookOnce(h, hookLaneAddRead, func() {
			locked = held(h)
			setLaneState(h.lanes, "L-02", port.LaneFree) // the add's worktree there, its setup still running
		})
		laneAddPass(t, h, calls, fresh(h))
		if !*ran || !locked {
			t.Fatalf("the add decided outside dispatch's lock (hook ran %v, locked %v)", *ran, locked)
		}
		_, err := laneDispatch(h, "d1", "L-02")
		open()
		h.a.effects.Wait()
		if got := calls.all(); len(got) != 1 {
			t.Errorf("EnsureLane calls = %q, want the one add", got)
		}
		if err == nil || !strings.Contains(err.Error(), "serve is adding the lane's worktree") {
			t.Errorf("dispatch while the add runs: %v, want refused", err)
		}
	})
	t.Run("dispatch first", func(t *testing.T) {
		h, calls := laneAddHarness(t, 2, []port.Lane{
			{Name: "L-01", State: port.LaneWorking, Task: "T-9"},
			{Name: "L-02", State: port.LaneFree},
		}, nil)
		stale := fresh(h)
		stale[1].State = port.LaneMissing // the cached row of the lane: an old read
		locked := false
		ran := hookOnce(h, hookDispatchPicked, func() {
			locked = held(h)
			setLaneState(h.lanes, "L-02", port.LaneMissing) // the next read says it missing
		})
		reply, err := laneDispatch(h, "d1", "L-02")
		if !*ran || !locked {
			t.Fatalf("the dispatch decided outside the add's lock (hook ran %v, locked %v)", *ran, locked)
		}
		if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-02") {
			t.Fatalf("dispatch: %q, %v; want DISPATCHED", reply, err)
		}
		laneAddPass(t, h, calls, stale)
		if got := calls.all(); len(got) != 0 {
			t.Errorf("EnsureLane calls = %q, want none on the dispatched lane", got)
		}
	})
}

// `obor setup lane` repairs a lane whose add failed (its marker gone, the lane free): the next pass over that read ends
// the retry and settles the needs-you, with no add of its own.
func TestLaneAddRepairedByHandSettles(t *testing.T) {
	h, calls := laneAddHarness(t, 2, []port.Lane{
		{Name: "L-01", State: port.LaneFree},
		{Name: "L-02", State: port.LaneMissing},
	}, map[string]error{"L-02": errors.New("boom")})
	laneAddPass(t, h, calls, fresh(h))
	if got := calls.all(); len(got) != 1 || !laneNeedsOpen(h, "L-02") {
		t.Fatalf("precondition: one failed add (%q) and its needs-you", got)
	}
	setLaneState(h.lanes, "L-02", port.LaneFree) // obor setup lane L-02 succeeded
	laneAddPass(t, h, calls, fresh(h))           // inside the backoff
	if got := calls.all(); len(got) != 1 {
		t.Errorf("EnsureLane calls = %q, want none after the repair", got)
	}
	if laneNeedsOpen(h, "L-02") {
		t.Errorf("the needs-you is open after the repair")
	}
	h.a.mu.Lock()
	_, retry := h.a.p.laneAdds["L-02"]
	h.a.mu.Unlock()
	if retry {
		t.Errorf("the retry state outlived the repair")
	}
}
