package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// An unknown or stale observation keeps the task's facts but drops every elapsed-time baseline
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

// A failed scan keeps a worker's question (the rule for an unknown lane, for the whole table): obor
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

// The lane table is an observation while it is at most lanesBound old and its last scan read it.
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

// A pane event never scans the lanes; a burst of them marks the table once and the refresher's next pass
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

// With the refresher running, events while it scans never wait for git: they return at once, and they make
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

// obor status shows the lane table's age and its last scan's error once it is older than the tick.
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

// unknownReads are the failed lane reads that make a lane unknown (checks.Lanes, TestLaneUnknownOnFailedRead
// proves each one reads as an unknown lane with no task); a failed store read also loses the row's agent.
var unknownReads = []struct{ name, reason string }{
	{"git status", "git status: fatal: index file corrupt"},
	{"merge-base", "git merge-base: fatal: unable to read tree"},
	{"rev-parse", "git rev-parse: fatal: not a git repository"},
	{"store", "store: file is not a database"},
}

// unknownLane is lane l as a failed read reports it: unknown, with reason, no task (and, the store unread, no agent).
func unknownLane(l port.Lane, reason string) port.Lane {
	u := port.Lane{Name: l.Name, Dir: l.Dir, State: port.LaneUnknown, Reason: reason, Agent: l.Agent}
	if strings.HasPrefix(reason, "store:") {
		u.Agent = ""
	}
	return u
}

// A lane whose read failed is no observation of its task: the task's watchdog facts (a worker's
// question, a stopped worker, a pending notification retry) stay in force through it on each entry point that reaches
// decideLanes (the periodic check, the check on connect, a restart's first check), and once the read recovers obor
// resume still takes them. A restart with the lane unknown keeps the folded dispatch's task on it.
func TestUnknownLaneKeepsItsTasksFacts(t *testing.T) {
	t.Parallel()
	for _, fact := range []string{"asks", "stopped"} {
		for _, entry := range []string{"tick", "connect", "restart"} {
			for _, read := range unknownReads {
				t.Run(fact+"/"+entry+"/"+read.name, func(t *testing.T) {
					t.Parallel()
					h, p := askSeed(t)
					known := h.lanes.lanes[0]
					switch fact {
					case "asks":
						if _, err := h.a.ask(p, "T-1", known, "may I edit x?"); err != nil {
							t.Fatal(err)
						}
					case "stopped":
						h.a.record(p, event.Record{Task: "T-1", Step: event.StepNeedsYou, Kind: event.KindFact},
							event.FactData{Lane: "L-01", Reason: "stopped: rate_limit", Cause: causeStopped})
					}
					h.tick(time.Minute) // the fact in force
					inForce := func() bool {
						if fact == "stopped" {
							return p.stops("T-1")
						}
						return p.asks("T-1")
					}
					if !inForce() || p.tasks["T-1"] == nil {
						t.Fatalf("precondition: %s not in force or T-1 unwatched: %v %v", fact, p.folded.active["T-1"], p.tasks)
					}
					before := h.lastSeq()

					// one failing read
					h.lanes.lanes[0] = unknownLane(known, read.reason)
					switch entry {
					case "tick":
						h.tick(time.Minute)
					case "connect":
						h.connect()
					case "restart":
						p.log.Close()
						h.a.closeLog()
						p = reloadProj(t, h)
						h.a.mu.Lock()
						w := p.tasks["T-1"]
						h.a.mu.Unlock()
						if w == nil || w.lane.Name != "L-01" || w.lane.Task != "T-1" {
							t.Errorf("restart: the folded dispatch's task is not kept on the unknown lane: %+v", w)
						}
						h.tick(time.Minute)
					}
					if p != h.a.p {
						t.Fatal("precondition: the project checked is not the one loaded")
					}
					if !inForce() {
						t.Errorf("%s: the failed read ended the %s fact: %v", entry, fact, p.folded.active["T-1"])
					}
					if w := p.tasks["T-1"]; w == nil || !w.unread() || w.lane.Task != "T-1" {
						t.Errorf("%s: T-1 not kept on its unknown lane: %+v", entry, w)
					}
					if got := h.facts(before); len(got) != 0 {
						t.Errorf("%s: facts recorded while the lane is unknown: %v", entry, got)
					}

					// the read recovers
					h.lanes.lanes[0] = known
					h.tick(time.Minute)
					if !inForce() {
						t.Fatalf("recovered: the %s fact is not in force: %v", fact, p.folded.active["T-1"])
					}
					h.a.mu.Lock()
					act, _, err := p.resumeFits(*p.folded.loop["T-1"])
					h.a.mu.Unlock()
					want := actAnswer
					if fact == "stopped" {
						want = actLine
					}
					if err != nil || act != want {
						t.Fatalf("recovered: obor resume fits %q, %v; want %q", act, err, want)
					}
					if fact != "stopped" {
						if _, err := h.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Role: "lead",
							Agent: "pj-lead", CallID: "a1"}, Task: "T-1", Text: "yes, edit x"}); err != nil {
							t.Fatalf("recovered: obor resume refused: %v", err)
						}
						if p.asks("T-1") {
							t.Error("recovered: obor resume left the question in force")
						}
					}
				})
			}
		}
	}
}

// An unknown lane is not judged: its pane closing while its read fails records nothing (no needs-you{pane
// exited}); once the read recovers the watchdog sees the closed pane.
func TestUnknownLaneIsNotJudged(t *testing.T) {
	t.Parallel()
	h, p := askSeed(t)
	h.tick(time.Minute)
	if w := p.tasks["T-1"]; w == nil || w.agent.ID != "w1:p1" {
		t.Fatalf("precondition: T-1's worker not seen in w1:p1: %+v", w)
	}
	known := h.lanes.lanes[0]
	before := h.lastSeq()
	h.lanes.lanes[0] = unknownLane(known, unknownReads[0].reason)
	h.remove("w1:p1", "closed")
	h.tick(time.Minute)
	h.tick(11 * time.Minute)
	if got := h.facts(before); len(got) != 0 {
		t.Errorf("facts recorded on the unknown lane: %v", got)
	}
	h.lanes.lanes[0] = known
	h.tick(time.Minute)
	if !p.has("T-1", event.StepNeedsYou, "pane exited") {
		t.Errorf("recovered: the closed pane is not seen: %v", h.facts(before))
	}
}
