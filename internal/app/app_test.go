package app_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/store"
)

// The app at its boundary: the herdr socket client (herdr.Panes) on the fake herdr socket, a scripted lane table and
// a clock the test moves. Ticks run every few ms; the watchdog bound is the real 10 min of the clock.

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// Move runs f with the clock held still, then moves it on by d: with f's file work that is one step for the watchdogs,
// which cannot read a time between them. A test that writes what a watchdog samples must hold the clock across it, or
// a loaded machine lets a sample run between the write and the move and take a baseline at the old time.
func (c *clock) Move(d time.Duration, f func(now time.Time)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(c.t)
	c.t = c.t.Add(d)
}

type laneTable struct {
	mu    sync.Mutex
	lanes []port.Lane
}

func (l *laneTable) Lanes(context.Context, string) ([]port.Lane, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]port.Lane(nil), l.lanes...), nil
}

// syncBuf is the app's output, read while it writes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type rig struct {
	t     *testing.T
	f     *herdrtest.Fake
	srv   *herdrtest.Server
	root  string // the project "pj"
	wt    string // its lanes' parent dir
	lanes *laneTable
	clock *clock

	mu sync.Mutex
	ps string // the process table Busy reads

	loop   func(string) (*app.Loop, error) // Options.Loop (nil: observe only)
	app    *app.App
	out    *syncBuf
	cancel context.CancelFunc
	done   chan struct{}
}

// newRig is project pj (workspace w1) with lanes L-01 (T-1) and L-02 (T-2), each with an idle claude, a workspace
// w2 labelled "other", and the app not started yet.
func newRig(t *testing.T) *rig {
	t.Helper()
	base := t.TempDir()
	r := &rig{t: t, f: herdrtest.New(), root: filepath.Join(base, "pj"), wt: filepath.Join(base, "pj-wt"),
		lanes: &laneTable{},
		clock: &clock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}}
	if err := os.MkdirAll(filepath.Join(r.root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.root, ".obor", "config.json"), `{"name":"pj"}`)
	r.f.Group("w1", "pj")
	r.f.Group("w2", "other")
	for i, lane := range []string{"L-01", "L-02"} {
		task := []string{"T-1", "T-2"}[i]
		r.lanes.lanes = append(r.lanes.lanes, port.Lane{Name: lane, Dir: r.lane(lane), Task: task})
		p := r.f.Agent("w1:p"+lane[3:], "pj-l-0"+lane[3:], "idle", r.lane(lane))
		p.Label = "pj-l-0" + lane[3:] // labelled by its launch: serve's identity of it
	}
	r.srv = herdrtest.Serve(t, r.f)
	r.srv.Reply("notification.show", map[string]any{"type": "notification_show", "shown": true, "reason": "shown"})
	r.ps = "2 1 2\n" // the agent (pid 2, the fake's foreground group) and nothing under it
	return r
}

func (r *rig) lane(name string) string { return filepath.Join(r.wt, name) }

// submitted records task's task_done from lane in the project's log before the app starts (fact submitted).
func (r *rig) submitted(task, lane string) {
	r.t.Helper()
	db, err := store.OpenRW(event.DBFile(r.root))
	if err != nil {
		r.t.Fatal(err)
	}
	log, _, err := event.OpenLog(db)
	if err != nil {
		r.t.Fatal(err)
	}
	defer log.Close()
	if _, err := log.Append(event.Record{Task: task, Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact,
		Data: []byte(`{"lane":"` + lane + `"}`)}, r.clock.Now()); err != nil {
		r.t.Fatal(err)
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) setPS(table string) {
	r.mu.Lock()
	r.ps = table
	r.mu.Unlock()
}

// start runs the app until the test ends (or stop); it returns after the app's first full check (every project and
// open task sampled once).
func (r *rig) start() {
	r.t.Helper()
	panes := herdr.Panes{S: r.srv.Client(), PS: func(context.Context) ([]byte, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		return []byte(r.ps), nil
	}}
	r.out = &syncBuf{}
	r.app = app.New(app.Options{Root: r.root, Tick: 2 * time.Millisecond, Retry: 5 * time.Millisecond,
		Now: r.clock.Now, Out: r.out, Refresh: func(err error) bool {
			var e *herdr.Error
			return errors.As(err, &e) && e.Code == "refresh"
		}, Loop: r.loop}, panes, r.lanes)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	go func() {
		defer close(r.done)
		_ = r.app.Serve(ctx)
	}()
	r.t.Cleanup(r.stop)
	r.waitFor("the first check", func() bool { checks, _ := app.Counts(r.app); return checks >= 1 })
}

func (r *rig) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	<-r.done
	r.cancel = nil
}

func (r *rig) waitConnected() {
	r.t.Helper()
	r.waitFor("connected", func() bool { return r.app.Status().Connected })
}

func (r *rig) waitFor(what string, ok func() bool) {
	r.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if ok() {
			return
		}
	}
	r.t.Fatalf("timed out waiting for %s; app output:\n%s", what, r.out)
}

// records is pj's event log.
func (r *rig) records() []event.Record {
	r.t.Helper()
	recs, err := event.ReadLog(r.root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return recs
}

// facts is the log's facts as "task step{reason}" lines (not the notifications' intents and results).
func (r *rig) facts() []string {
	var out []string
	for _, rec := range r.records() {
		if rec.Kind != event.KindFact {
			continue
		}
		out = append(out, rec.Task+" "+rec.String())
	}
	return out
}

func (r *rig) waitFacts(want ...string) {
	r.t.Helper()
	r.waitFor("facts "+strings.Join(want, ", "), func() bool { return strings.Join(r.facts(), "|") == strings.Join(want, "|") })
}

func (r *rig) count(method string) int {
	n := 0
	for _, req := range r.srv.Requests() {
		if req.Method == method {
			n++
		}
	}
	return n
}

// waitQuiet waits until task's stall timer has its baseline at at: the watchdogs took that quiet sample.
func (r *rig) waitQuiet(task string, at time.Time) {
	r.t.Helper()
	r.waitFor(task+"'s quiet baseline at "+at.Format("15:04"), func() bool {
		quiet, _ := app.Quiet(r.app, r.root, task)
		return quiet.Equal(at)
	})
}

// settle waits for two more full checks: the second one started after the call, so it saw the clock and the state
// the test set before it.
func (r *rig) settle() {
	r.t.Helper()
	n, _ := app.Counts(r.app)
	r.waitFor("two more checks", func() bool { checks, _ := app.Counts(r.app); return checks >= n+2 })
}

func eq(t *testing.T, got, want []string, what string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

func TestIdleStallsThenNeedsYou(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.f.Pane("w1:p2").Status = "working" // L-02 is not idle: only T-1 stalls
	r.start()
	r.clock.Add(9 * time.Minute)
	r.settle()
	eq(t, r.facts(), nil, "facts after 9 min idle")

	r.clock.Add(2 * time.Minute)
	r.waitFacts("T-1 stalled{idle}")
	rec := r.records()[0]
	if rec.Seq != 1 || rec.Kind != "fact" || rec.Fact() != (event.FactData{Lane: "L-01", Machine: "local", Group: "pj",
		Pane: "w1:p1", Agent: "pj-l-01", Reason: "idle"}) {
		t.Errorf("stalled record = %+v", rec)
	}

	r.clock.Add(10 * time.Minute)
	r.waitFacts("T-1 stalled{idle}", "T-1 needs-you{stalled}")
	r.clock.Add(30 * time.Minute)
	r.settle()
	eq(t, r.facts(), []string{"T-1 stalled{idle}", "T-1 needs-you{stalled}"}, "facts 30 min later")

	st := r.app.Status()
	if st.Project == nil || st.Project.Name != "pj" || len(st.Project.Agents) != 2 ||
		len(st.Project.Facts) != 2 {
		t.Fatalf("status = %+v", st)
	}
	a := st.Project.Agents[0]
	if a.Pane != "w1:p1" || a.Lane != "L-01" || a.Task != "T-1" || a.Status != "idle" || a.Age != 51*60 {
		t.Errorf("status agent = %+v, want w1:p1 L-01 T-1 idle for 51 min", a)
	}
}

func TestWorkingAgainResumes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.f.Pane("w1:p2").Status = "working"
	r.start()
	r.clock.Add(11 * time.Minute)
	r.waitFacts("T-1 stalled{idle}")
	r.srv.WaitSubscribed(t, herdr.EventStatus, "w1:p1")
	r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Status = "working" })
	r.srv.Push(herdr.EventStatus, map[string]any{"pane_id": "w1:p1", "agent_status": "working", "agent": "claude"})
	r.waitFacts("T-1 stalled{idle}", "T-1 resumed")
	if f := r.app.Status().Project.Facts; len(f) != 0 {
		t.Errorf("facts in force after resumed: %+v", f)
	}
}

func TestNotStalledWhileAChildRuns(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// L-01's agent is pid 2: an MCP server in its own group (3, does not count) and a Bash tool command in a group of
	// its own (50 → 51). L-02's agent is pid 7 with nothing under it: it stalls, so the watchdogs did look.
	r.f.Pane("w1:p2").Foreground = 7
	r.setPS("2 1 2\n3 2 2\n50 2 50\n51 50 50\n7 1 7\n")
	r.start()
	// Sampled before the clock moves: L-02's baseline at 0, L-01 sampled active (no baseline).
	r.waitQuiet("T-2", r.clock.Now())
	r.waitFor("L-01 sampled while its command runs", func() bool {
		quiet, seen := app.Quiet(r.app, r.root, "T-1")
		return quiet.IsZero() && !seen.IsZero()
	})
	r.clock.Add(11 * time.Minute)
	r.waitFacts("T-2 stalled{idle}")
	r.settle()
	eq(t, r.facts(), []string{"T-2 stalled{idle}"}, "facts while L-01's command runs")
	if r.count("pane.process_info") == 0 {
		t.Fatal("the watchdog never asked herdr for process info")
	}

	if quiet, _ := app.Quiet(r.app, r.root, "T-1"); !quiet.IsZero() {
		t.Fatalf("L-01 has a quiet baseline (%v) while its command runs", quiet)
	}

	r.setPS("2 1 2\n3 2 2\n7 1 7\n")  // the command ended; the MCP server stays
	r.waitQuiet("T-1", r.clock.Now()) // L-01's first quiet sample: its stall timer's baseline
	r.clock.Add(9 * time.Minute)
	r.settle()
	eq(t, r.facts(), []string{"T-2 stalled{idle}"}, "facts 9 min after the command ended")
	r.clock.Add(2 * time.Minute)
	r.waitFacts("T-2 stalled{idle}", "T-1 stalled{idle}", "T-2 needs-you{stalled}")
}

// T-951: the stall watchdog samples the pane it already observed: a ready agent costs no agent.get per tick, while
// the child-process check still runs.
func TestWatchdogAsksNoAgentGetPerTick(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.start()
	gets, procs := r.count("agent.get"), r.count("pane.process_info")
	r.settle() // two more full checks, each sampling both ready agents
	if n := r.count("agent.get") - gets; n != 0 {
		t.Errorf("agent.get over two quiet ticks = %d, want 0 (the sample reads its observed pane)", n)
	}
	if n := r.count("pane.process_info") - procs; n == 0 {
		t.Fatal("no process info over two ticks: the sample did not run (vacuous)")
	}
}

func TestNotStalledWhileTheTranscriptChanges(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	transcript := filepath.Join(t.TempDir(), "s.jsonl")
	write(t, transcript, "{}\n")
	// Written at 0 (on the test's clock), set before the start so no sample sees the file's real mtime: the first
	// sample only takes it as the watermark.
	if err := os.Chtimes(transcript, r.clock.Now(), r.clock.Now()); err != nil {
		t.Fatal(err)
	}
	r.f.Pane("w1:p1").Transcript = transcript
	r.start()
	if _, seen := app.Quiet(r.app, r.root, "T-1"); !seen.Equal(r.clock.Now()) {
		t.Fatalf("precondition: L-01's watermark %v after the first check, want the write at 0", seen)
	}
	r.clock.Add(9 * time.Minute)
	// The write at 9 and the move to 11 are one step for the watchdogs: a sample must not see the write at 9 and take
	// its baseline there (a loaded machine then stalls L-01 a minute late, at 20 instead of 21).
	r.clock.Move(2*time.Minute, func(now time.Time) {
		if err := os.Chtimes(transcript, now, now); err != nil { // written at 9
			t.Fatal(err)
		}
	})
	r.waitFacts("T-2 stalled{idle}")
	r.settle()
	eq(t, r.facts(), []string{"T-2 stalled{idle}"}, "facts while L-01's transcript is fresh")
	if st := r.app.Status(); st.Project.Agents[0].Pane != "w1:p1" {
		t.Fatalf("status = %+v", st)
	}

	r.clock.Add(9 * time.Minute)
	r.settle()
	eq(t, r.facts(), []string{"T-2 stalled{idle}"}, "facts 9 min after L-01's baseline")
	r.clock.Add(time.Minute) // Idle after L-01's first quiet sample, and after T-2's stall
	r.waitFacts("T-2 stalled{idle}", "T-1 stalled{idle}", "T-2 needs-you{stalled}")
}

func TestBlockedNeedsYou(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.f.Pane("w1:p1").Status = "blocked"
	r.f.Pane("w1:p2").Status = "working"
	r.start()
	r.clock.Add(9 * time.Minute)
	r.settle()
	eq(t, r.facts(), nil, "facts after 9 min blocked")
	r.clock.Add(2 * time.Minute)
	r.waitFacts("T-1 needs-you{blocked}")
	r.clock.Add(20 * time.Minute)
	r.settle()
	eq(t, r.facts(), []string{"T-1 needs-you{blocked}"}, "facts 20 min later")
}

func TestPaneExitedNeedsYou(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.submitted("T-2", "L-02") // T-2 is done: its pane closing is no alarm
	r.start()
	r.srv.WaitSubscribed(t, herdr.EventExited, "")
	r.f.Remove("w1:p2")
	r.srv.Push("pane_closed", map[string]any{"pane_id": "w1:p2"})
	r.f.Remove("w1:p1")
	r.srv.Push("pane_exited", map[string]any{"pane_id": "w1:p1"})
	r.waitFacts("T-2 submitted", "T-1 needs-you{pane exited}")
	r.settle()
	eq(t, r.facts(), []string{"T-2 submitted", "T-1 needs-you{pane exited}"}, "facts after both panes went")
	if f := r.app.Status().Project.Facts; len(f) != 1 {
		t.Errorf("facts in force = %+v, want pane exited (the lane still holds T-1)", f)
	}
}

func TestReconnectReadsANewSnapshot(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.start()
	if n := r.count("session.snapshot"); n == 0 {
		t.Fatal("no snapshot on connect")
	}
	before := r.count("session.snapshot")
	// herdr restarts; meanwhile L-01's pane is gone and L-02's agent got blocked.
	r.f.Remove("w1:p1")
	r.f.Pane("w1:p2").Status = "blocked"
	r.srv.DropConns()
	r.waitFacts("T-1 needs-you{pane exited}")
	if n := r.count("session.snapshot"); n <= before {
		t.Errorf("snapshots = %d, want more than the %d before the drop", n, before)
	}
	r.waitConnected()
	r.clock.Add(11 * time.Minute)
	r.waitFacts("T-1 needs-you{pane exited}", "T-2 needs-you{blocked}")

	// events_lost ends the stream too: subscribe again, from a new snapshot.
	before = r.count("session.snapshot")
	r.srv.PushError("events_lost", "subscriber lagged")
	r.waitFor("a snapshot after events_lost", func() bool { return r.count("session.snapshot") > before })
	if !strings.Contains(r.out.String(), "events_lost") {
		t.Errorf("app output does not say why the stream ended:\n%s", r.out)
	}
}

func TestReplayAfterRestart(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.f.Pane("w1:p2").Status = "working"
	r.start()
	r.clock.Add(11 * time.Minute)
	r.waitFacts("T-1 stalled{idle}")
	r.stop()

	r.clock.Add(30 * time.Minute) // long after the stall: the needs-you timer restarts with the app
	r.start()
	if st := r.app.Status(); len(st.Project.Facts) != 1 || st.Project.Facts[0].String() != "stalled{idle}" {
		t.Fatalf("facts in force after replay = %+v, want stalled{idle}", st.Project.Facts)
	}
	r.clock.Add(9 * time.Minute)
	r.settle()
	eq(t, r.facts(), []string{"T-1 stalled{idle}"}, "facts 9 min after the restart (no second stalled)")
	r.clock.Add(2 * time.Minute)
	r.waitFacts("T-1 stalled{idle}", "T-1 needs-you{stalled}")
	if recs := r.records(); recs[1].Seq != 2 {
		t.Errorf("seq after replay = %d, want 2", recs[1].Seq)
	}
}

func TestPaneInAnotherGroupIgnored(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// A pane of group "other" in L-01's worktree, idle; L-01's own pane is working, L-02's idle.
	r.f.Pane("w1:p1").Status = "working"
	r.f.Agent("w2:p1", "x-l-01", "idle", r.lane("L-01"))
	r.f.Agent("w9:p1", "y", "idle", r.lane("L-01")) // a group the terminal did not name
	r.start()
	r.clock.Add(11 * time.Minute)
	r.waitFacts("T-2 stalled{idle}")
	r.settle()
	eq(t, r.facts(), []string{"T-2 stalled{idle}"}, "facts")
	for _, a := range r.app.Status().Project.Agents {
		if a.Pane != "w1:p1" && a.Pane != "w1:p2" {
			t.Errorf("status lists %s of another workspace", a.Pane)
		}
	}
	for _, req := range r.srv.Requests() {
		if req.Method == "pane.process_info" && !strings.Contains(string(req.Params), `"w1:p2"`) {
			t.Errorf("process info asked of %s, want L-02's pane only", req.Params)
		}
	}
	if r.count("pane.process_info") == 0 {
		t.Error("process info never asked of L-02's pane")
	}
}

func TestStatusSocket(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.f.Pane("w1:p2").Status = "working"
	r.start()
	r.clock.Add(11 * time.Minute)
	r.waitFacts("T-1 stalled{idle}")
	dir, err := os.MkdirTemp("", "app")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "serve.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go r.app.Listen(ln)
	st, err := wire.Ask(context.Background(), sock)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Connected || st.Project == nil || len(st.Project.Agents) != 2 || len(st.Project.Facts) != 1 ||
		st.Project.Agents[1].Status != "working" {
		t.Errorf("status over the socket = %+v", st)
	}
	if _, err := wire.Ask(context.Background(), filepath.Join(dir, "none.sock")); err == nil ||
		!strings.Contains(err.Error(), "not running") {
		t.Errorf("Ask without a server: %v", err)
	}
}

// RV-36 #4: a workspace opened after the connect ends the stream (Code refresh); the app subscribes again at once,
// with no stream-ended line, and its new snapshot registers the workspace's panes.
func TestRefreshSubscribesAgainQuietly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.f.Ungroup("w1") // pj's workspace w1 is not open yet
	r.f.Pane("w1:p2").Status = "working"
	r.start()
	if n := len(r.app.Status().Project.Agents); n != 0 {
		t.Fatalf("agents before pj's workspace opened: %d, want 0", n)
	}
	before := r.count("session.snapshot")
	r.srv.WaitSubscribed(t, "workspace.created", "")
	r.f.Group("w1", "pj")
	r.srv.Push("workspace_created", map[string]any{"type": "workspace_created",
		"workspace": map[string]any{"workspace_id": "w1", "label": "pj"}})
	r.waitFor("a snapshot after the refresh", func() bool { return r.count("session.snapshot") > before })
	r.waitFor("pj's agents", func() bool { return len(r.app.Status().Project.Agents) == 2 })
	if strings.Contains(r.out.String(), "stream ended") {
		t.Errorf("a refresh logged a stream end:\n%s", r.out)
	}
	// T-1's quiet baseline at 0 before the clock moves: the stall counts from the first sample of the identified
	// agent, and the refresh's check is not the test's to order (a sample taken at 11 min never stalls).
	r.waitQuiet("T-1", r.clock.Now())
	r.clock.Add(11 * time.Minute)
	r.waitFacts("T-1 stalled{idle}")
}
