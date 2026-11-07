package app

import (
	"context"
	"errors"
	"obor/internal/app/port"
	"sync"
	"testing"
	"time"
)

// eventNow is a.event as Serve completes it: the refresher's pass over the lane table the event marked, then the run
// loop's decision on the table it read (lanescan.go, T-879); for the tests that drive the core without Serve's
// goroutines.
func eventNow(a *App, ev port.Event) {
	ctx := context.Background()
	a.event(ctx, ev)
	if a.rescanDue(ctx) {
		a.check(ctx, false)
	}
}

// T-879 (RV-120 V1): an unknown or stale observation keeps the task's facts but drops every elapsed-time baseline
// (quiet, the stall's escalation, blocked, lost); the next good read starts new ones. Each row: a baseline running,
// then no observation for longer than its interval (the lane's read fails, the whole scan fails, or the table is
// older than its bound), then recovery: no judgement at recovery, the fact only once a new baseline has elapsed, and
// the facts in force before stay through it.
func TestUnknownObservationResetsTheTimers(t *testing.T) {
	t.Parallel()
	baselines := []struct {
		name     string
		setup    func(h *harness)         // after connect: the baseline runs, nothing judged yet
		running  func(w *watch) time.Time // the baseline's time
		inForce  []string                 // the facts in force before the gap (kept through it)
		interval time.Duration            // the time a new baseline needs
		judged   string                   // the fact it gives then
	}{
		{name: "quiet", setup: func(h *harness) { h.tick(5 * minute) },
			running: func(w *watch) time.Time { return w.quietSince }, interval: 10 * minute, judged: "T-1 stalled{idle}"},
		{name: "stalled", setup: func(h *harness) { h.tick(11 * minute) },
			running: func(w *watch) time.Time { return w.quietSince }, inForce: []string{"stalled{idle}"},
			interval: 10 * minute, judged: "T-1 needs-you{stalled}"},
		{name: "blocked", setup: func(h *harness) {
			h.status("w1:p1", func(p *port.Pane) { p.Status = "blocked" })
			h.tick(minute)
		}, running: func(w *watch) time.Time { return w.blocked }, interval: 10 * minute, judged: "T-1 needs-you{blocked}"},
		{name: "lost", setup: func(h *harness) {
			h.status("w1:p1", func(p *port.Pane) { p.Agent, p.Status = "", "" }) // the agent exits, its shell stays
			h.tick(30 * time.Second)
		}, running: func(w *watch) time.Time { return w.lost }, interval: time.Minute, judged: "T-1 needs-you{agent exited}"},
	}
	gaps := []struct {
		name         string
		unknown, end func(h *harness)
		gap          func(h *harness) // the time without an observation (12 min: past Idle and Gone)
	}{
		{name: "lane unknown",
			unknown: func(h *harness) { h.lanes.lanes[0] = unknownLane(h.lanes.lanes[0], unknownReads[0].reason) },
			gap:     func(h *harness) { h.tick(6 * minute); h.tick(6 * minute) },
			end:     func(h *harness) { h.lanes.lanes[0] = port.Lane{Name: "L-01", Dir: h.lane("L-01"), Task: "T-1"} }},
		{name: "scan failed",
			unknown: func(h *harness) { h.lanes.err = errors.New("git: index.lock exists") },
			gap:     func(h *harness) { h.tick(6 * minute); h.tick(6 * minute) },
			end:     func(h *harness) { h.lanes.err = nil }},
		{name: "table older than its bound", // decisions on the cached table only: no scan for 12 min
			unknown: func(h *harness) {},
			gap: func(h *harness) {
				h.now = h.now.Add(12 * minute)
				h.a.check(context.Background(), false)
			},
			end: func(h *harness) {}},
	}
	for _, b := range baselines {
		for _, g := range gaps {
			t.Run(b.name+"/"+g.name, func(t *testing.T) {
				t.Parallel()
				h := newHarness(t)
				h.connect()
				b.setup(h)
				p := h.proj()
				seq := h.lastSeq()
				w := p.tasks["T-1"]
				if w == nil || b.running(w).IsZero() {
					t.Fatalf("precondition: no %s baseline running: %+v", b.name, w)
				}
				for _, f := range b.inForce {
					if !p.has("T-1", factStep(f), factReason(f)) {
						t.Fatalf("precondition: %s not in force: %v", f, p.folded.active["T-1"])
					}
				}

				g.unknown(h)
				g.gap(h)
				if got := h.facts(seq); len(got) != 0 {
					t.Errorf("facts recorded without an observation: %v", got)
				}
				if w := p.tasks["T-1"]; w == nil || !w.quietSince.IsZero() || !w.blocked.IsZero() || !w.lost.IsZero() {
					t.Errorf("the gap kept a baseline: %+v", w)
				}

				g.end(h)
				h.tick(10 * time.Second) // recovery: a new baseline starts, nothing is judged
				if got := h.facts(seq); len(got) != 0 {
					t.Errorf("recovery judged the unobserved interval: %v", got)
				}
				for _, f := range b.inForce {
					if !p.has("T-1", factStep(f), factReason(f)) {
						t.Errorf("%s did not stay in force through the gap: %v", f, p.folded.active["T-1"])
					}
				}
				h.tick(b.interval) // the new baseline elapses
				eq(t, h.facts(seq), []string{b.judged}, "facts once the new baseline elapsed")
			})
		}
	}
}

// factStep and factReason split "step{reason}".
func factStep(f string) string {
	for i := range f {
		if f[i] == '{' {
			return f[:i]
		}
	}
	return f
}

func factReason(f string) string {
	s := factStep(f)
	if len(s) == len(f) {
		return ""
	}
	return f[len(s)+1 : len(f)-1]
}

// T-879: a failed scan keeps a worker's question (T-897's rule for an unknown lane, for the whole table): obor
// answer still takes it once the scan recovers.
func TestFailedScanKeepsFacts(t *testing.T) {
	t.Parallel()
	h, p := askSeed(t)
	if _, err := h.a.ask(p, "T-1", h.lanes.lanes[0], "may I edit x?"); err != nil {
		t.Fatal(err)
	}
	h.tick(time.Minute)
	if !p.asks("T-1") {
		t.Fatalf("precondition: want the question in force: %v", p.folded.active["T-1"])
	}
	h.lanes.err = errors.New("git: index.lock exists")
	h.tick(11 * time.Minute)
	h.tick(11 * time.Minute)
	if !p.asks("T-1") || p.tasks["T-1"] == nil {
		t.Errorf("the failed scan dropped the question or the task's watch: %v %v", p.folded.active["T-1"], p.tasks)
	}
	h.lanes.err = nil
	h.tick(time.Minute)
	if !p.asks("T-1") {
		t.Errorf("recovered: the question is gone: %v", p.folded.active["T-1"])
	}
}

// T-879: the lane table is an observation while it is at most lanesBound old and its last scan read it.
func TestLaneTableBound(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	ls := []port.Lane{{Name: "L-01"}}
	for _, c := range []struct {
		name string
		s    laneScan
		now  time.Time
		ok   bool
	}{
		{"never read", laneScan{}, at, false},
		{"just read", laneScan{ls: ls, at: at}, at, true},
		{"at the bound", laneScan{ls: ls, at: at}, at.Add(time.Minute), true},
		{"past the bound", laneScan{ls: ls, at: at}, at.Add(time.Minute + time.Nanosecond), false},
		{"its last scan failed", laneScan{ls: ls, at: at, err: errors.New("x")}, at, false},
	} {
		if got, ok := c.s.observed(c.now, time.Minute); ok != c.ok || ok && len(got) != 1 {
			t.Errorf("%s: observed = %v, %v; want ok %v", c.name, got, ok, c.ok)
		}
	}
}

// countLanes counts the lane table's scans (each one is git per lane in serve); when gate is set a scan signals
// entered and waits for gate to close.
type countLanes struct {
	mu      sync.Mutex
	lanes   *fakeLanes
	calls   int
	gate    chan struct{}
	entered chan struct{}
}

func (c *countLanes) Lanes(ctx context.Context, root string) ([]port.Lane, error) {
	c.mu.Lock()
	c.calls++
	gate, entered := c.gate, c.entered
	c.mu.Unlock()
	if gate != nil {
		entered <- struct{}{}
		<-gate
	}
	return c.lanes.Lanes(ctx, root)
}

func (c *countLanes) n() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// burst is n status events of w1:p1 (working and idle in turn), straight on the event path.
func burst(h *harness, n int) {
	for i := range n {
		p := h.pane("w1:p1")
		p.Status = []string{"working", "idle"}[i%2]
		h.a.event(context.Background(), port.Event{Kind: port.EventStatus, Pane: p.ID, Status: p.Status, Agent: p.Agent, New: *p})
	}
}

// T-879: a pane event never scans the lanes; a burst of them marks the table once and the refresher's next pass
// scans it once; the decision follows that scan.
func TestEventBurstSharesOneScan(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := &countLanes{lanes: h.lanes}
	h.a.lanes = c
	h.connect()
	if c.n() != 1 {
		t.Fatalf("precondition: the connect's check scanned %d times, want 1", c.n())
	}
	burst(h, 20)
	if c.n() != 1 {
		t.Errorf("the burst's events scanned %d times on the event path", c.n()-1)
	}
	if !h.a.rescanDue(context.Background()) || c.n() != 2 {
		t.Fatalf("the refresher's pass after the burst: %d scans, want 1", c.n()-1)
	}
	if h.a.rescanDue(context.Background()) || c.n() != 2 {
		t.Errorf("a second pass scanned again with nothing marked: %d scans", c.n()-1)
	}
	seq := h.lastSeq()
	h.a.check(context.Background(), false) // the run loop's decision on the scanned table
	h.now = h.now.Add(30 * time.Second)
	h.a.check(context.Background(), false)
	if c.n() != 2 || len(h.facts(seq)) != 0 {
		t.Errorf("the decisions on the cached table scanned (%d) or judged (%v)", c.n()-2, h.facts(seq))
	}
}

// T-879: with the refresher running, events while it scans never wait for git: they return at once, and they make
// one more scan, one per pass, however many they are; an own operation's mark does too.
func TestEventsDuringAScanWaitForNoGit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := &countLanes{lanes: h.lanes}
	h.a.lanes = c
	h.connect()
	c.mu.Lock()
	c.gate, c.entered = make(chan struct{}), make(chan struct{}, 1)
	c.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); h.a.refresher(ctx) }()
	defer func() { cancel(); <-done }()

	burst(h, 1)
	select {
	case <-c.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the refresher did not scan the marked table")
	}
	returned := make(chan struct{})
	go func() { burst(h, 20); close(returned) }()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("events waited for the scan in flight")
	}
	if c.n() != 2 {
		t.Fatalf("scans while the first is in flight: %d, want 1", c.n()-1)
	}
	c.mu.Lock()
	gate := c.gate
	c.gate = nil
	c.mu.Unlock()
	close(gate)
	waitFor(t, "the burst's one more scan", func() bool { return c.n() == 3 })
	time.Sleep(50 * time.Millisecond)
	if c.n() != 3 {
		t.Errorf("scans after the burst: %d, want 2 (one per pass)", c.n()-1)
	}
	select {
	case <-h.a.scanned:
	default:
		t.Error("the refresher handed the run loop no decision")
	}

	h.a.mu.Lock()
	h.a.markLanes(h.a.p) // an own operation (App.do)
	h.a.mu.Unlock()
	waitFor(t, "the own operation's scan", func() bool { return c.n() == 4 })
}

// waitFor polls cond for up to 10 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// T-879: obor status shows the lane table's age and its last scan's error once it is older than the tick.
func TestLanesNote(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect()
	p := h.proj()
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	if got := h.a.lanesNote(p, h.now.Add(30*time.Second)); got != "" {
		t.Errorf("a table one tick old: %q, want none", got)
	}
	p.scan.err = errors.New("git: index.lock exists")
	if got, want := h.a.lanesNote(p, h.now.Add(2*time.Minute)), "lane table read 2m ago (last scan: git: index.lock exists)"; got != want {
		t.Errorf("an old table: %q, want %q", got, want)
	}
}
