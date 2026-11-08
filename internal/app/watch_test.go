package app

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
)

// The watchdogs' case table: each row is (prior state, event or tick, evidence) → the facts and notifications that
// follow. The core runs without Serve: the row calls connect, event and check itself on a clock it moves, over an
// in-memory herdr (fakePanes) and lane table. setup builds the prior state; the event is the first action after it.

type fakePanes struct {
	busy     bool
	busyErr  error
	last     time.Time // the transcript's last write (zero: unknown)
	notifyFn func() error
	notes    []string
	asked    []string // the panes Busy was asked about
	turn     agents.Turn
	turned   bool // Turn knows turn
	// Snapshot's answer: snap when an error does not come first (nil snap, nil err: "not in the table", as for the
	// table's rows); after runs once the answer is read, before it returns (a stream event landing in between);
	// snaps counts the calls (T-747).
	snap    *port.Snapshot
	snapErr error
	after   func()
	snaps   int
}

func (f *fakePanes) Subscribe(context.Context) (*port.Stream, error) {
	return nil, errors.New("not in the table")
}

// Snapshot is the read a lane's start makes again (rereadPanes): snap's panes, unless the context or snapErr says
// otherwise; a row that never set snap gets the error Subscribe gives.
func (f *fakePanes) Snapshot(ctx context.Context) (port.Snapshot, error) {
	f.snaps++
	if err := ctx.Err(); err != nil {
		return port.Snapshot{}, err
	}
	if f.snapErr != nil {
		return port.Snapshot{}, f.snapErr
	}
	if f.snap == nil {
		return port.Snapshot{}, errors.New("not in the table")
	}
	snap := *f.snap
	snap.Panes = append([]port.Pane(nil), f.snap.Panes...)
	if f.after != nil {
		f.after()
	}
	return snap, nil
}

func (f *fakePanes) Busy(_ context.Context, pane string) (bool, error) {
	f.asked = append(f.asked, pane)
	return f.busy, f.busyErr
}

func (f *fakePanes) Activity(context.Context, port.Pane) (time.Time, bool) {
	return f.last, !f.last.IsZero()
}

func (f *fakePanes) Turn(context.Context, port.Pane, string) (agents.Turn, bool) {
	return f.turn, f.turned
}

func (f *fakePanes) Notify(_ context.Context, title, body string) error {
	if f.notifyFn != nil {
		if err := f.notifyFn(); err != nil {
			return err
		}
	}
	f.notes = append(f.notes, title+": "+body)
	return nil
}

// fakeLanes is the lane table; err, when set, fails the whole read instead (T-879).
type fakeLanes struct {
	lanes []port.Lane
	err   error
}

func (l *fakeLanes) Lanes(context.Context, string) ([]port.Lane, error) {
	if l.err != nil {
		return nil, l.err
	}
	return append([]port.Lane(nil), l.lanes...), nil
}

// harness is project pj (workspace w1) with lane L-01 holding T-1, its pane w1:p1 an idle claude; not connected.
type harness struct {
	t        *testing.T
	a        *App
	panes    *fakePanes
	lanes    *fakeLanes
	now      time.Time
	root, wt string
	snap     port.Snapshot     // herdr's session now
	sidMap   map[string]string // sids: agent → session id, put in the store (applySids)
	sidSeq   int64
}

func newHarness(t *testing.T) *harness {
	base := t.TempDir()
	h := &harness{t: t, panes: &fakePanes{}, lanes: &fakeLanes{}, now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
		root: filepath.Join(base, "pj"), wt: filepath.Join(base, "pj-wt")}
	if err := os.MkdirAll(filepath.Join(h.root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj"}`)
	h.lanes.lanes = []port.Lane{{Name: "L-01", Dir: h.lane("L-01"), Task: "T-1"}}
	h.snap = port.Snapshot{Panes: []port.Pane{{ID: "w1:p1", Group: "pj", Cwd: h.lane("L-01"), Agent: "claude", Label: "pj-l-01", Status: "idle"}}}
	h.a = New(Options{Root: h.root, Now: func() time.Time { return h.now }}, h.panes, h.lanes)
	return h
}

func (h *harness) write(path, text string) {
	h.t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) lane(name string) string { return filepath.Join(h.wt, name) }

// pane is pane id of h.snap (to change it before a connect, or to build an event from).
func (h *harness) pane(id string) *port.Pane {
	for i := range h.snap.Panes {
		if h.snap.Panes[i].ID == id {
			return &h.snap.Panes[i]
		}
	}
	h.t.Fatalf("no pane %s", id)
	return nil
}

// setLane puts task on the lane; done: its task_done is in (fact submitted in the loaded project's log).
func (h *harness) setLane(task string, done bool) {
	h.lanes.lanes[0].Task = task
	if !done {
		return
	}
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	if p := h.a.p; p != nil {
		rec, err := p.log.Append(event.Record{Task: task, Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact,
			Data: []byte(`{"lane":"L-01"}`)}, h.a.opt.Now())
		if err != nil {
			h.t.Fatal(err)
		}
		p.foldRecord(rec)
	}
}

// connect is a subscription: load the project, then work from h.snap.
func (h *harness) connect() {
	h.a.loadProject()
	h.applySids()
	snap := h.snap
	snap.Panes = append([]port.Pane(nil), h.snap.Panes...)
	h.a.connect(context.Background(), snap)
}

// tick moves the clock by d and runs one watchdog tick.
func (h *harness) tick(d time.Duration) {
	h.now = h.now.Add(d)
	h.a.loadProject()
	h.a.check(context.Background(), true)
}

// status changes pane id in h.snap and sends the status event that carries it.
func (h *harness) status(id string, fn func(p *port.Pane)) {
	p := h.pane(id)
	fn(p)
	eventNow(h.a, port.Event{Kind: port.EventStatus, Pane: id, Status: p.Status, Agent: p.Agent, New: *p})
}

// remove drops pane id from h.snap and sends kind (closed or exited).
func (h *harness) remove(id, kind string) {
	var keep []port.Pane
	for _, p := range h.snap.Panes {
		if p.ID != id {
			keep = append(keep, p)
		}
	}
	h.snap.Panes = keep
	eventNow(h.a, port.Event{Kind: kind, Pane: id})
}

// log writes prior records to pj's log (a crash left them).
func (h *harness) log(recs ...event.Record) {
	if err := WriteLog(h.root, h.now, recs...); err != nil {
		h.t.Fatal(err)
	}
}

func factRec(task, step, reason string) event.Record {
	data, _ := json.Marshal(event.FactData{Lane: "L-01", Pane: "w1:p1", Reason: reason})
	return event.Record{Task: task, Step: step, Kind: event.KindFact, Data: data}
}

// records is pj's log.
func (h *harness) records() []event.Record {
	h.t.Helper()
	recs, err := event.ReadLog(h.root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return recs
}

// facts is pj's facts from seq on, as "task step{reason}".
func (h *harness) facts(from int64) []string {
	var out []string
	for _, r := range h.records() {
		if r.Kind == event.KindFact && r.Seq > from {
			out = append(out, r.Task+" "+r.String())
		}
	}
	return out
}

func (h *harness) lastSeq() int64 {
	recs := h.records()
	if len(recs) == 0 {
		return 0
	}
	return recs[len(recs)-1].Seq
}

type row struct {
	name  string
	setup func(h *harness) // the prior state (nil: connect)
	event func(h *harness) // the event or tick the row is about
	facts []string         // the facts the event records
	then  func(h *harness) // later steps (nil: none)
	later []string         // the facts recorded from the event through then
	notes int              // the notifications shown from the event on
	check func(t *testing.T, h *harness)
}

func connect(h *harness) { h.connect() }

func tick(d time.Duration) func(h *harness) { return func(h *harness) { h.tick(d) } }

func steps(fs ...func(h *harness)) func(h *harness) {
	return func(h *harness) {
		for _, f := range fs {
			f(h)
		}
	}
}

// unknownAt5ThenQuiet: Busy fails at 5 min, answers quiet at 10.
func unknownAt5ThenQuiet(h *harness) {
	h.panes.busyErr = errors.New("process_info unavailable")
	h.tick(5 * minute)
	h.panes.busyErr = nil
	h.tick(5 * minute)
}

// workingAt5IdleAt6: connected; T-1's agent works at 5 min and is idle again at 6.
func workingAt5IdleAt6(h *harness) {
	h.connect()
	h.now = h.now.Add(5 * minute)
	h.status("w1:p1", func(p *port.Pane) { p.Status = "working" })
	h.now = h.now.Add(minute)
	h.status("w1:p1", func(p *port.Pane) { p.Status = "idle" })
}

// freshWriteBetweenUnknowns: Busy fails at +1 min, the transcript is written at +1.5, Busy still fails at +2.
func freshWriteBetweenUnknowns(h *harness) {
	h.panes.busyErr = errors.New("process_info unavailable")
	h.tick(minute)
	h.panes.last = h.now.Add(30 * time.Second)
	h.tick(minute)
}

// agentChangedAt5AfterAWriteAt4: connected with an hour-old transcript; written at 4 min, unsampled; at 5 the pane's
// agent changes (a reset event, its check samples at once).
func agentChangedAt5AfterAWriteAt4(h *harness) {
	h.panes.last = h.now.Add(-time.Hour)
	h.connect()
	h.now = h.now.Add(5 * minute)
	h.panes.last = h.now.Add(-minute)
	h.status("w1:p1", func(p *port.Pane) { p.Agent = "codex" })
}

// stalled: connected, T-1 stalled{idle} 11 min in.
func stalled(h *harness) { h.connect(); h.tick(11 * time.Minute) }

// escalated: T-1 stalled, then needs-you{stalled} 10 min after.
func escalated(h *harness) { stalled(h); h.tick(10 * time.Minute) }

const minute = time.Minute

var rows = []row{
	// Rows 10–15 of docs/state-model.md: the baseline.
	{name: "row 10: idle 9 min: nothing yet", event: tick(9 * minute)},
	{name: "row 10, #5: idle 10 min, no child, no transcript known: quiet (Busy is evidence) → stalled",
		event: tick(11 * minute), facts: []string{"T-1 stalled{idle}"},
		check: func(t *testing.T, h *harness) {
			if f := h.records()[0].Fact(); f != (event.FactData{Lane: "L-01", Machine: "local", Group: "pj",
				Pane: "w1:p1", Agent: "pj-l-01", Reason: "idle"}) {
				t.Errorf("stalled data = %+v", f)
			}
		}},
	{name: "row 14: stalled, quiet 10 min more → needs-you{stalled}, notified", setup: stalled,
		event: tick(10 * minute), facts: []string{"T-1 needs-you{stalled}"}},
	{name: "row 14: needs-you{stalled} is notified once", setup: escalated, event: tick(30 * minute)},
	{name: "row 11: blocked 10 min → needs-you{blocked}, notified",
		setup: func(h *harness) { h.pane("w1:p1").Status = "blocked"; h.connect() },
		event: tick(11 * minute), facts: []string{"T-1 needs-you{blocked}"},
		then: tick(20 * minute), later: []string{"T-1 needs-you{blocked}"}},
	{name: "row 12: working event ends a stall → resumed", setup: stalled,
		event: func(h *harness) { h.status("w1:p1", func(p *port.Pane) { p.Status = "working" }) },
		facts: []string{"T-1 resumed"}},
	{name: "row 15: pane closed with the task open → needs-you{pane exited}, notified", setup: connect,
		event: func(h *harness) { h.remove("w1:p1", port.EventClosed) }, facts: []string{"T-1 needs-you{pane exited}"}},
	{name: "row 15: pane exited (shell gone) → needs-you{pane exited}", setup: connect,
		event: func(h *harness) { h.remove("w1:p1", port.EventExited) }, facts: []string{"T-1 needs-you{pane exited}"}},
	{name: "a pane of another group in the lane's worktree is not watched",
		setup: func(h *harness) { h.pane("w1:p1").Group = "other"; h.connect() }, event: tick(30 * minute),
		check: func(t *testing.T, h *harness) {
			if len(h.panes.asked) != 0 {
				t.Errorf("Busy asked of %v", h.panes.asked)
			}
		}},

	// RV-36 #2 (app side): events apply after the snapshot they follow; a new snapshot replaces what came before.
	{name: "#2 a status event after the snapshot wins: blocked replaces the snapshot's idle", setup: connect,
		event: func(h *harness) { h.status("w1:p1", func(p *port.Pane) { p.Status = "blocked" }) },
		then:  tick(11 * minute), later: []string{"T-1 needs-you{blocked}"}},
	{name: "#2 reconnect (lost connection, events_lost): the new snapshot replaces the old stream's blocked",
		setup: func(h *harness) {
			h.connect()
			h.status("w1:p1", func(p *port.Pane) { p.Status = "blocked" })
			h.tick(9 * minute)
			h.pane("w1:p1").Status = "idle"
		},
		event: connect, then: tick(2 * minute)},
	{name: "#2 a created event for a known pane replaces it: the agent it found is watched",
		setup: func(h *harness) { p := h.pane("w1:p1"); p.Agent, p.Status = "", ""; h.connect() },
		event: func(h *harness) {
			p := h.pane("w1:p1")
			p.Agent, p.Label, p.Status = "claude", "pj-l-01", "idle"
			eventNow(h.a, port.Event{Kind: port.EventCreated, Pane: p.ID, New: *p})
		},
		then: tick(11 * minute), later: []string{"T-1 stalled{idle}"}},

	// RV-36 #4: metadata refresh.
	{name: "#4 a status event's metadata replaces the pane's: its new cwd puts it in L-01, its new name in the fact",
		setup: func(h *harness) { h.pane("w1:p1").Cwd = filepath.Join(h.wt, "elsewhere"); h.connect() },
		event: func(h *harness) { h.status("w1:p1", func(p *port.Pane) { p.Cwd, p.Label = h.lane("L-01"), "pj-new" }) },
		then:  tick(11 * minute), later: []string{"T-1 stalled{idle}"},
		check: func(t *testing.T, h *harness) {
			if f := h.records()[0].Fact(); f.Agent != "pj-new" {
				t.Errorf("stalled names agent %q, want pj-new", f.Agent)
			}
		}},
	{name: "#4 a group opened after the connect: the refresh snapshot registers its panes",
		setup: func(h *harness) {
			h.pane("w1:p1").Group = "" // its workspace not listed yet: no group
			h.connect()
			h.tick(11 * minute)
			h.pane("w1:p1").Group = "pj"
		},
		event: connect, then: tick(11 * minute), later: []string{"T-1 stalled{idle}"}},
	{name: "#4 a moved pane (new id in the refresh snapshot): the new id is watched, the old one is no alarm",
		setup: func(h *harness) {
			h.snap.Panes = append(h.snap.Panes, port.Pane{ID: "w2:p9", Group: "other", Cwd: "/x"})
			h.connect()
			h.pane("w1:p1").ID = "w1:p5"
		},
		event: connect, then: tick(11 * minute), later: []string{"T-1 stalled{idle}"},
		check: func(t *testing.T, h *harness) {
			if f := h.records()[0].Fact(); f.Pane != "w1:p5" {
				t.Errorf("stalled names pane %q, want w1:p5", f.Pane)
			}
		}},

	// RV-36 #5: a failed Busy is unknown, never idle.
	{name: "#5 Busy fails: no stalled, however long",
		setup: func(h *harness) { h.panes.busyErr = errors.New("process_info unavailable"); h.connect() },
		event: tick(11 * minute), then: tick(30 * minute)},
	{name: "#5 Busy fails while stalled: no needs-you",
		setup: func(h *harness) { stalled(h); h.panes.busyErr = errors.New("process_info unavailable") },
		event: tick(11 * minute), then: tick(30 * minute)},
	{name: "#5 Busy fails but the transcript was written after the stall: activity → resumed",
		setup: func(h *harness) {
			stalled(h)
			h.panes.busyErr = errors.New("process_info unavailable")
			h.panes.last = h.now.Add(2 * minute)
		},
		event: tick(3 * minute), facts: []string{"T-1 resumed"}},

	// RV-36-verify #10, the timer rule: a timer counts only the time between two consecutive known-quiet
	// observations; an unknown one stops it, and the next known-quiet one starts a fresh period at its own time.
	{name: "#10 unknown before quiet: an hour of failed Busy, then the first quiet check stalls nothing",
		setup: func(h *harness) {
			h.panes.busyErr = errors.New("process_info unavailable")
			h.connect()
			h.tick(60 * minute)
			h.panes.busyErr = nil
		},
		event: tick(0), then: tick(9 * minute)},
	{name: "#10 unknown before quiet: stalled Idle after the first quiet check",
		setup: func(h *harness) {
			h.panes.busyErr = errors.New("process_info unavailable")
			h.connect()
			h.tick(60 * minute)
			h.panes.busyErr = nil
		},
		event: tick(0), then: steps(tick(9*minute), tick(minute)), later: []string{"T-1 stalled{idle}"}},
	{name: "#10 unknown between quiet samples of a stall: no needs-you Idle after the stall",
		setup: func(h *harness) {
			stalled(h) // quiet at 11 min
			h.tick(5 * minute)
			h.panes.busyErr = errors.New("process_info unavailable")
			h.tick(3 * minute) // unknown at 19
			h.panes.busyErr = nil
		},
		event: tick(minute), then: tick(minute)}, // quiet at 20 (fresh period), 21: Idle after the stall, 1 min quiet
	{name: "#10 unknown between quiet samples of a stall: needs-you Idle after the quiet that followed it",
		setup: func(h *harness) {
			stalled(h)
			h.tick(5 * minute)
			h.panes.busyErr = errors.New("process_info unavailable")
			h.tick(3 * minute)
			h.panes.busyErr = nil
		},
		event: tick(minute), then: steps(tick(9*minute), tick(minute)), later: []string{"T-1 needs-you{stalled}"}},
	{name: "#10 unknown during escalation: stalled, an hour of failed Busy, then the first quiet check: no needs-you",
		setup: func(h *harness) {
			stalled(h)
			h.panes.busyErr = errors.New("process_info unavailable")
			h.tick(60 * minute)
			h.panes.busyErr = nil
		},
		event: tick(0), then: tick(9 * minute)},
	{name: "#10 unknown during escalation: needs-you Idle after the first quiet check, notified",
		setup: func(h *harness) {
			stalled(h)
			h.panes.busyErr = errors.New("process_info unavailable")
			h.tick(60 * minute)
			h.panes.busyErr = nil
		},
		event: tick(0), then: steps(tick(9*minute), tick(minute)), later: []string{"T-1 needs-you{stalled}"}},

	// T-335, the stall timer as a state machine over observed quiet time (minutes; Q quiet, U unknown, B busy,
	// A active). The connect samples at 0.
	{name: "T-335 Q0 Q9: nothing", event: tick(9 * minute)},
	{name: "T-335 Q0 Q10 → stalled", event: tick(10 * minute), facts: []string{"T-1 stalled{idle}"}},
	{name: "T-335 Q0 U5 Q10 Q19: nothing (U5 drops the baseline, Q10 is a new one)",
		event: unknownAt5ThenQuiet, then: tick(9 * minute)},
	{name: "T-335 Q0 U5 Q10 Q19 Q20 → stalled",
		event: unknownAt5ThenQuiet, then: steps(tick(9*minute), tick(minute)), later: []string{"T-1 stalled{idle}"}},
	{name: "T-335 B0 Q10 Q19: nothing (Q10 is the first quiet sample: a baseline, not a verdict)",
		setup: func(h *harness) { h.panes.busy = true; h.connect() },
		event: func(h *harness) { h.panes.busy = false; h.tick(10 * minute) }, then: tick(9 * minute)},
	{name: "T-335 B0 Q10 Q19 Q20 → stalled",
		setup: func(h *harness) { h.panes.busy = true; h.connect() },
		event: func(h *harness) { h.panes.busy = false; h.tick(10 * minute) },
		then:  steps(tick(9*minute), tick(minute)), later: []string{"T-1 stalled{idle}"}},
	{name: "T-335 Q0 Q10(stalled) Q19: nothing", setup: func(h *harness) { h.connect(); h.tick(10 * minute) },
		event: tick(9 * minute)},
	{name: "T-335 Q0 Q10(stalled) Q19 Q20 → needs-you{stalled}", setup: func(h *harness) { h.connect(); h.tick(10 * minute) },
		event: tick(9 * minute), then: tick(minute), later: []string{"T-1 needs-you{stalled}"}},
	{name: "T-335 Q0 Q10(stalled) A12 → resumed; Q13 Q22: nothing",
		setup: func(h *harness) { h.connect(); h.tick(10 * minute) },
		event: func(h *harness) { h.panes.last = h.now.Add(time.Minute); h.tick(2 * minute) }, facts: []string{"T-1 resumed"},
		then: steps(tick(minute), tick(9*minute)), later: []string{"T-1 resumed"}},
	{name: "T-335 Q0 Q10(stalled) A12 → resumed; Q13 Q22 Q23 → stalled",
		setup: func(h *harness) { h.connect(); h.tick(10 * minute) },
		event: func(h *harness) { h.panes.last = h.now.Add(time.Minute); h.tick(2 * minute) }, facts: []string{"T-1 resumed"},
		then: steps(tick(minute), tick(9*minute), tick(minute)), later: []string{"T-1 resumed", "T-1 stalled{idle}"}},
	{name: "T-335 replay with stalled in force: Q0 nothing", setup: func(h *harness) { h.log(factRec("T-1", event.StepStalled, "idle")) },
		event: connect},
	{name: "T-335 replay with stalled in force: Q0 Q10 → needs-you{stalled}",
		setup: func(h *harness) { h.log(factRec("T-1", event.StepStalled, "idle")) },
		event: connect, then: tick(10 * minute), later: []string{"T-1 needs-you{stalled}"}},
	{name: "T-335 working at 5, idle at 6: Q6 Q15 nothing", setup: workingAt5IdleAt6, event: tick(9 * minute)},
	{name: "T-335 working at 5, idle at 6: Q6 Q15 Q16 → stalled", setup: workingAt5IdleAt6,
		event: tick(9 * minute), then: tick(minute), later: []string{"T-1 stalled{idle}"}},
	// RV-36-verify-3 #15 (T-337): a transcript watermark (seenWrite) separate from the quiet baseline; an unknown
	// sample drops the baseline, not the watermark (minutes; W a transcript write).
	{name: "#15 FreshTranscriptAfterUnknown: stalled 11, U12, W12.5, U13 → resumed", setup: stalled,
		event: freshWriteBetweenUnknowns, facts: []string{"T-1 resumed"}},
	{name: "#15 RecoveryEscalates: stalled 11, U12, W12.5, U13 → resumed; Q14 Q24 → stalled afresh, no needs-you",
		setup: stalled, event: freshWriteBetweenUnknowns, facts: []string{"T-1 resumed"},
		then:  func(h *harness) { h.panes.busyErr = nil; h.tick(minute); h.tick(10 * minute) },
		later: []string{"T-1 resumed", "T-1 stalled{idle}"}},
	{name: "#15 needs-you{stalled} in force: U22, W22.5, U23 → resumed, no notification", setup: escalated,
		event: freshWriteBetweenUnknowns, facts: []string{"T-1 resumed"}},
	{name: "#15 an old transcript (written before the first sample): Q0 Q10 → stalled",
		setup: func(h *harness) { h.panes.last = h.now.Add(-time.Hour); h.connect() },
		event: tick(10 * minute), facts: []string{"T-1 stalled{idle}"}},
	{name: "#15 reset (agent changed) with an unseen older write: Q5 Q15 → stalled",
		setup: agentChangedAt5AfterAWriteAt4, event: tick(10 * minute), facts: []string{"T-1 stalled{idle}"}},
	{name: "#15 reset (agent changed), a write after it: Q5 W8 A15 → nothing",
		setup: agentChangedAt5AfterAWriteAt4,
		event: func(h *harness) { h.panes.last = h.now.Add(3 * minute); h.tick(10 * minute) }},

	// RV-36-verify-2 #13 (Sol's probes): the first quiet sample is a baseline, never also the verdict.
	{name: "#13 first quiet only: a child ran until 9.5 min, the first quiet sample at 10 stalls nothing",
		setup: func(h *harness) {
			h.panes.busy = true
			h.connect()
			for range 19 {
				h.tick(30 * time.Second) // busy samples through 9.5 min
			}
			h.panes.busy = false
		},
		event: tick(30 * time.Second), then: tick(9*minute + 30*time.Second)},
	{name: "#13 first quiet only: stalled Idle after the first quiet sample",
		setup: func(h *harness) {
			h.panes.busy = true
			h.connect()
			for range 19 {
				h.tick(30 * time.Second)
			}
			h.panes.busy = false
		},
		event: tick(30 * time.Second), then: tick(10 * minute), later: []string{"T-1 stalled{idle}"}},
	{name: "#13 first quiet after busy: busy at 11, the first quiet at 21 stalls nothing; Idle after it → stalled",
		setup: func(h *harness) { h.connect(); h.panes.busy = true; h.tick(11 * minute) },
		event: func(h *harness) { h.panes.busy = false; h.tick(10 * minute) },
		then:  tick(10 * minute), later: []string{"T-1 stalled{idle}"}},

	// RV-36 #6: a needs-you's owner toast is the needs rule's call (T-949, linerules_test.go), not a watchdog's: a
	// replayed fact is a fact in force, and old notification records fold as nothing.

	// RV-36 #7: an exit decides on the lane as it is now, not as the last tick read it.
	{name: "#7 the pane closes after its done file was written: no alarm",
		setup: func(h *harness) { h.connect(); h.tick(minute); h.setLane("T-1", true) },
		event: func(h *harness) { h.remove("w1:p1", port.EventClosed) }},
	{name: "#7 the pane exits after the lane took another task: no alarm",
		setup: func(h *harness) { h.connect(); h.tick(minute); h.setLane("T-9", false) },
		event: func(h *harness) { h.remove("w1:p1", port.EventExited) }, then: tick(minute)},
	{name: "#7 the pane closes after the merge freed the lane: no alarm",
		setup: func(h *harness) { h.connect(); h.tick(minute); h.setLane("", false) },
		event: func(h *harness) { h.remove("w1:p1", port.EventClosed) }},
	{name: "#7 reconnect without the pane after its done file: no alarm",
		setup: func(h *harness) {
			h.snap.Panes = append(h.snap.Panes, port.Pane{ID: "w2:p9", Group: "other", Cwd: "/x"})
			h.connect()
			h.setLane("T-1", true)
			h.snap.Panes = h.snap.Panes[1:]
		},
		event: connect},

	// RV-36 #8: idle time is per task.
	{name: "#8 a taskless idle lane takes a task: no stall at once",
		setup: func(h *harness) { h.setLane("", false); h.connect(); h.tick(60 * minute); h.setLane("T-99", false) },
		event: tick(minute), then: tick(9 * minute)},
	{name: "#8 a taskless idle lane takes a task: stalled Idle after it took it",
		setup: func(h *harness) { h.setLane("", false); h.connect(); h.tick(60 * minute); h.setLane("T-99", false) },
		event: tick(minute), then: tick(10 * minute), later: []string{"T-99 stalled{idle}"}},
	{name: "#8 the task is replaced in the same idle lane: the new task starts afresh",
		setup: func(h *harness) { stalled(h); h.setLane("T-5", false) },
		event: tick(minute), then: steps(tick(9*minute), tick(minute)), later: []string{"T-5 stalled{idle}"}},
	{name: "#8 a blocked taskless pane takes a task: the blocked timer starts with the task",
		setup: func(h *harness) {
			h.pane("w1:p1").Status = "blocked"
			h.setLane("", false)
			h.connect()
			h.tick(60 * minute)
			h.setLane("T-99", false)
		},
		event: tick(minute), then: steps(tick(9*minute), tick(minute)), later: []string{"T-99 needs-you{blocked}"}},

	// RV-36 #9: observed activity ends a stall.
	{name: "#9 a busy child after the stall → resumed; quiet again → stalled afresh, not needs-you",
		setup: func(h *harness) { stalled(h); h.panes.busy = true },
		event: tick(11 * minute), facts: []string{"T-1 resumed"},
		then:  func(h *harness) { h.panes.busy = false; h.tick(minute); h.tick(10 * minute) }, // baseline, then Idle
		later: []string{"T-1 resumed", "T-1 stalled{idle}"}},
	{name: "#9 a busy child seen at once after the stall (any tick) → resumed",
		setup: func(h *harness) { stalled(h); h.panes.busy = true },
		event: tick(30 * time.Second), facts: []string{"T-1 resumed"}},
	{name: "#9 a transcript write after the stall → resumed",
		setup: func(h *harness) { stalled(h); h.panes.last = h.now.Add(2 * minute) },
		event: tick(3 * minute), facts: []string{"T-1 resumed"}},
	{name: "#9 a transcript write within Idle postpones the stall: Idle from the next quiet sample",
		setup: func(h *harness) { h.connect(); h.panes.last = h.now.Add(5 * minute) },
		event: tick(11 * minute), then: steps(tick(minute), tick(10*minute)), later: []string{"T-1 stalled{idle}"}},
	{name: "#9 a busy child after needs-you{stalled} → resumed",
		setup: func(h *harness) { escalated(h); h.panes.busy = true },
		event: tick(minute), facts: []string{"T-1 resumed"}},
	{name: "#9 working after needs-you{blocked} → resumed",
		setup: func(h *harness) { h.pane("w1:p1").Status = "blocked"; h.connect(); h.tick(11 * minute) },
		event: func(h *harness) { h.status("w1:p1", func(p *port.Pane) { p.Status = "working" }) },
		facts: []string{"T-1 resumed"}},

	// Fable A1: the agent exits, its shell stays.
	{name: "A1 the agent exits, its shell stays → needs-you{agent exited} after Gone, notified", setup: connect,
		event: func(h *harness) { h.status("w1:p1", func(p *port.Pane) { p.Agent, p.Status = "", "" }) },
		then:  tick(2 * minute), later: []string{"T-1 needs-you{agent exited}"}},
	{name: "A1 the agent restarts within Gone: no alarm", setup: connect,
		event: func(h *harness) { h.status("w1:p1", func(p *port.Pane) { p.Agent, p.Status = "", "" }) },
		then: func(h *harness) {
			h.tick(30 * time.Second)
			h.status("w1:p1", func(p *port.Pane) { p.Agent, p.Label, p.Status = "claude", "pj-l-01", "idle" })
			h.tick(2 * minute)
		}},
	{name: "A1 a shell that never ran an agent for the task: no alarm",
		setup: func(h *harness) { p := h.pane("w1:p1"); p.Agent, p.Status = "", ""; h.connect() },
		event: tick(11 * minute)},
	{name: "A1 the agent exits after the done file: no alarm",
		setup: func(h *harness) { h.connect(); h.setLane("T-1", true) },
		event: func(h *harness) { h.status("w1:p1", func(p *port.Pane) { p.Agent, p.Status = "", "" }) },
		then:  tick(2 * minute)},

	// Fable A2: herdr restarted.
	{name: "A2 herdr restarted (no pane id shared): no alarm; the lane's new pane is watched",
		setup: func(h *harness) {
			h.connect()
			h.tick(5 * minute)
			h.snap = port.Snapshot{Panes: []port.Pane{{ID: "w3:p7", Group: "pj", Cwd: h.lane("L-01"), Agent: "claude", Label: "pj-l-01",
				Status: "idle"}}}
		},
		event: connect, then: tick(11 * minute), later: []string{"T-1 stalled{idle}"},
		check: func(t *testing.T, h *harness) {
			if f := h.records()[0].Fact(); f.Pane != "w3:p7" || f.Group != "pj" {
				t.Errorf("stalled names %+v, want pane w3:p7 in pj", f)
			}
		}},
	{name: "A2 herdr restarted, the lane's agent did not come back: no alarm",
		setup: func(h *harness) {
			h.connect()
			h.snap = port.Snapshot{Panes: []port.Pane{{ID: "w3:p7", Group: "pj", Cwd: h.lane("L-01")}}}
		},
		event: connect, then: tick(5 * minute)},
	{name: "A2 contrast: a reconnect to the same session without the lane's pane → needs-you{pane exited}",
		setup: func(h *harness) {
			h.snap.Panes = append(h.snap.Panes, port.Pane{ID: "w2:p9", Group: "other", Cwd: "/x"})
			h.connect()
			h.snap.Panes = h.snap.Panes[1:]
		},
		event: connect, facts: []string{"T-1 needs-you{pane exited}"}},
}

func TestWatchdogTable(t *testing.T) {
	t.Parallel()
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			if tc.setup == nil {
				h.connect()
			} else {
				tc.setup(h)
			}
			seq, notes := h.lastSeq(), len(h.panes.notes)
			tc.event(h)
			eq(t, h.facts(seq), tc.facts, "facts the event recorded")
			if tc.then != nil {
				tc.then(h)
				eq(t, h.facts(seq), tc.later, "facts through the later steps")
			}
			if n := len(h.panes.notes) - notes; n != tc.notes {
				t.Errorf("notifications from the event on = %d, want %d (%q)", n, tc.notes, h.panes.notes)
			}
			if tc.check != nil {
				tc.check(t, h)
			}
		})
	}
}

func eq(t *testing.T, got, want []string, what string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}
