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
	"obor/internal/store"
)

// laneAddCalls is a test's Loop.EnsureLane: it records each lane and its error, and on success makes the lane free in
// the fake lane table, as the worktree would.
type laneAddCalls struct {
	mu    sync.Mutex
	calls []string
	errs  map[string]error
	lanes *fakeLanes
}

func (c *laneAddCalls) ensure(_ context.Context, lane string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, lane)
	if err := c.errs[lane]; err != nil {
		return err
	}
	c.lanes.mu.Lock()
	defer c.lanes.mu.Unlock()
	for i := range c.lanes.lanes {
		if c.lanes.lanes[i].Name == lane {
			c.lanes.lanes[i].State = port.LaneFree
		}
	}
	return nil
}

func (c *laneAddCalls) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// laneAddHarness is pj with a configured lane count and the fake lane table ls, its merge loop's EnsureLane the
// test's (a nil map errs: every add succeeds).
func laneAddHarness(t *testing.T, count int, ls []port.Lane, errs map[string]error) (*harness, *laneAddCalls) {
	t.Helper()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"),
		`{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"lanes":{"count":`+strconv.Itoa(count)+
			`},"agents":{"roles":{"integrator":""}}}`)
	h.lanes.lanes = ls
	calls := &laneAddCalls{errs: errs, lanes: h.lanes}
	h.a.opt.Loop = func(root string) (*Loop, error) {
		return withCfg(root, &Loop{EnsureLane: calls.ensure}), nil
	}
	h.connect()
	return h, calls
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
// a pass at once asks again nothing, a pass past the delay asks once more.
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

	h.a.advance(ctx) // the same clock: inside the backoff
	h.a.effects.Wait()
	if got := calls.all(); len(got) != 1 {
		t.Errorf("EnsureLane calls inside the backoff = %q, want the one", got)
	}

	h.now = h.now.Add(5 * time.Second) // the default StepRetry
	h.a.advance(ctx)
	h.a.effects.Wait()
	if got := calls.all(); len(got) != 2 {
		t.Errorf("EnsureLane calls after the backoff = %q, want a second", got)
	}
}
