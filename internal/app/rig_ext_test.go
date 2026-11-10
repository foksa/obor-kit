package app_test

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/store"
	"obor/internal/testkit"
)

// answer is the lead's obor resume of T-1 with call id and text (a question's answer).
func (r *loopRig) answer(role, callID, text string) (string, error) {
	return r.ask(&wire.ResumeReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-lead", Role: role, CallID: callID}, Task: "T-1", Text: text})
}

// answerOf is the commands that answer the fact in force (obor status: serve's Answers; "": none).
func (r *loopRig) answerOf(fact event.Record) string {
	return r.app.Status().Project.Answers[fact.Seq]
}

// needsYou is the one needs-you fact in force (fatal when there is none or more than one).
func (r *loopRig) needsYou() event.Record {
	r.t.Helper()
	var out []event.Record
	for _, f := range r.app.Status().Project.Facts {
		if f.Step == event.StepNeedsYou {
			out = append(out, f)
		}
	}
	if len(out) != 1 {
		r.t.Fatalf("precondition: needs-you in force %+v, want one", out)
	}
	return out[0]
}

// runPath runs, as the lead, the command of fact's answers (what obor status and the lead's needs-you line suggest)
// that is obor <verb> ("resume", "resume --regate", "resume --sendback", "drop": a resume form with no flag is the one
// with none), with text for its quoted placeholder, through obor resume's own parser. Fatal when the path suggests no
// such command.
func (r *loopRig) runPath(fact event.Record, verb, callID, text string) (string, error) {
	r.t.Helper()
	path := r.answerOf(fact)
	for _, cmd := range strings.Split(strings.TrimPrefix(path, "its pane, or "), " | ") {
		bare := cmd
		if i := strings.Index(cmd, ` "<`); i >= 0 {
			bare = cmd[:i] + cmd[strings.Index(cmd, `>"`)+2:]
		}
		args := strings.Fields(bare)
		if len(args) < 3 || strings.Join(append(args[:2:2], args[3:]...), " ") != "obor "+verb {
			continue
		}
		if args[2] != fact.Task {
			r.t.Fatalf("%q names %s, not the task %s", path, args[2], fact.Task)
		}
		id := wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: callID}
		if args[1] == "drop" {
			return r.ask(&wire.DropReq{Envelope: id, Task: args[2], Why: text})
		}
		rc, ok := wire.ResumeArgs(append(args[2:], text))
		if text == "" {
			rc, ok = wire.ResumeArgs(args[2:])
		}
		if !ok {
			r.t.Fatalf("%q: obor resume's parser refuses it", cmd)
		}
		rc.Envelope = id
		return r.ask(&rc)
	}
	r.t.Fatalf("answers %q suggest no obor %s", path, verb)
	return "", nil
}

// bubbles holds the tests running in a bubble (bubble), each with its wall watch: a rig built by one uses in-memory
// connections.
var bubbles sync.Map // *testing.T → *wallWatch

// bubble runs f in a testing/synctest bubble. A rig built inside it has no unix socket: the fake herdr and the app's
// socket are in-memory pipes, so every wait of the test and of the app is on the bubble's clock, which stands still
// while anything runs. Machine load cannot expire such a wait; a wall watch ends it in real time (wallWatch). No
// t.Run, t.Parallel or t.Deadline inside.
func bubble(t *testing.T, f func(t *testing.T)) {
	t.Helper()
	bubbleWatched(t, newWallWatch(t.Name(), waitWall, stallGrace, bubbleWall, func(msg string) { panic(msg) }), f)
}

// bubbleWatched is bubble under the wall watch w, which it stops when the bubble ends.
func bubbleWatched(t *testing.T, w *wallWatch, f func(t *testing.T)) {
	t.Helper()
	defer w.stop()
	synctest.Test(t, func(t *testing.T) {
		bubbles.Store(t, w)
		defer bubbles.Delete(t)
		f(t)
	})
}

func inBubble(t *testing.T) bool { return watchOf(t) != nil }

// watchOf is the wall watch of t's bubble (nil outside one).
func watchOf(t *testing.T) *wallWatch {
	w, _ := bubbles.Load(t)
	watch, _ := w.(*wallWatch)
	return watch
}

// The wall limits of a bubble: a polling wait is over after waitWall of real time; one still asleep stallGrace later,
// or a bubble still running after bubbleWall, is reported as stalled. All are well under go test's timeout, so a
// stuck test fails as itself and not as the package's deadline.
const (
	waitWall   = 2 * time.Minute
	stallGrace = 10 * time.Second
	bubbleWall = 5 * time.Minute
)

// wallWatch ends a bubble's waits in real time. A bubble's clock stands still while one of its goroutines is blocked
// on something that is not the bubble's own (a lock, real I/O), so a wait sleeping on that clock never wakes, and
// nothing outside a bubble can wake it. The watch is a goroutine outside the bubble on the real clock:
//   - a polling wait open for wait is over: its poll sees that at its next try and fails with its records;
//   - a wait still open grace later sleeps on a clock that stands still, and so does a bubble running for whole:
//     stalled gets the report (what was waited for, its records, every goroutine's stack). bubble's stalled panics:
//     the package fails then, with the report, and no other way ends a test whose goroutines cannot run.
type wallWatch struct {
	name               string
	wait, grace, whole time.Duration
	stalled            func(report string)
	quit               chan struct{}

	mu   sync.Mutex
	ids  int
	open map[int]*wallWait
}

// wallWait is one open polling wait of a bubble.
type wallWait struct {
	what    string
	records func() string
	seen    time.Time // the watch's first sight of it, on the real clock
	over    bool
	told    bool // reported as stalled
}

// newWallWatch starts a wall watch for the bubble of the test name. It is called outside the bubble: the watch's
// goroutine and its clock are real.
func newWallWatch(name string, wait, grace, whole time.Duration, stalled func(report string)) *wallWatch {
	w := &wallWatch{name: name, wait: wait, grace: grace, whole: whole, stalled: stalled, quit: make(chan struct{}),
		open: map[int]*wallWait{}}
	go w.run()
	return w
}

func (w *wallWatch) stop() { close(w.quit) }

func (w *wallWatch) run() {
	tick := time.NewTicker(min(w.wait/10, 100*time.Millisecond))
	defer tick.Stop()
	start := time.Now()
	for {
		select {
		case <-w.quit:
			return
		case now := <-tick.C:
			for _, s := range w.check(now) {
				w.report(fmt.Sprintf("waiting for %s for %s of real time and its poll never woke", s.what, now.Sub(s.seen).Round(time.Millisecond)), s.records)
			}
			if now.Sub(start) >= w.whole {
				w.report(fmt.Sprintf("still running after %s of real time", w.whole), nil)
				return
			}
		}
	}
}

// check ages the open waits to now: a wait open for the limit is over, and the ones open grace longer are returned
// once.
func (w *wallWatch) check(now time.Time) []*wallWait {
	w.mu.Lock()
	defer w.mu.Unlock()
	var stalled []*wallWait
	for _, s := range w.open {
		if s.seen.IsZero() {
			s.seen = now
		}
		age := now.Sub(s.seen)
		s.over = s.over || age >= w.wait
		if age >= w.wait+w.grace && !s.told {
			s.told = true
			stalled = append(stalled, s)
		}
	}
	return stalled
}

// report hands stalled what the bubble is stuck in, the wait's records (read for at most a second: a reader may need
// what the bubble holds) and every goroutine's stack: the one blocked outside the bubble is among them.
func (w *wallWatch) report(what string, records func() string) {
	recs := "none"
	if records != nil {
		read := make(chan string, 1)
		go func() { read <- records() }()
		select {
		case recs = <-read:
		case <-time.After(time.Second):
			recs = "not read within 1s"
		}
	}
	stacks := make([]byte, 8<<20)
	stacks = stacks[:runtime.Stack(stacks, true)]
	w.stalled(fmt.Sprintf("%s: %s: the bubble's clock stands still (a goroutine of the bubble is blocked on a lock or "+
		"on real I/O)\n%s\ngoroutines:\n%s", w.name, what, recs, stacks))
}

// begin opens a polling wait for what (records: what its failure prints); end closes it. Outside a bubble (a nil
// watch) there is nothing to open: the test's clock is the real one.
func (w *wallWatch) begin(what string, records func() string) int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ids++
	w.open[w.ids] = &wallWait{what: what, records: records}
	return w.ids
}

func (w *wallWatch) end(id int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.open, id)
}

// over is whether the wait id has been open for the wall limit.
func (w *wallWatch) over(id int) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.open[id].over
}

// poll calls ok every step until it holds, for at most limit on t's clock and, in a bubble, the bubble's wall limit:
// "" once ok held, else how the wait ended ("timed out", for a failure's first words). records is what the wait's
// failure prints (nil: nothing).
func poll(t *testing.T, what string, limit, step time.Duration, records func() string, ok func() bool) string {
	w := watchOf(t)
	id := w.begin(what, records)
	defer w.end(id)
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(step) {
		if ok() {
			return ""
		}
		if w.over(id) {
			return fmt.Sprintf("timed out (%s of real time, the bubble's clock short of %s)", w.wait, limit)
		}
	}
	return "timed out"
}

// serveHerdr is the fake herdr on f: in memory in a bubble, on a unix socket outside one.
func serveHerdr(t *testing.T, f *herdrtest.Fake) *herdrtest.Server {
	t.Helper()
	if inBubble(t) {
		return herdrtest.ServeMem(t, f)
	}
	return herdrtest.Serve(t, f)
}

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
	pace  time.Duration // how long a read takes (0: no time)
}

func (l *laneTable) Lanes(context.Context, string) ([]port.Lane, error) {
	l.mu.Lock()
	ls, pace := append([]port.Lane(nil), l.lanes...), l.pace
	l.mu.Unlock()
	time.Sleep(pace)
	return ls, nil
}

// paced sets how long each read of the table takes.
func (l *laneTable) paced(d time.Duration) {
	l.mu.Lock()
	l.pace = d
	l.mu.Unlock()
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

	loop    func(string) (*app.Loop, error) // Options.Loop (nil: observe only)
	restore port.Restorer                   // Options.Restore (nil: none)
	app     *app.App
	out     *syncBuf
	cancel  context.CancelFunc
	done    chan struct{}
	// The app's socket once serve opened it: sock outside a bubble, mem in one.
	sock string
	mem  *app.MemServe
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
	write(t, filepath.Join(r.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj"}}`)
	r.f.Group("w1", "pj")
	r.f.Group("w2", "other")
	for i, lane := range []string{"L-01", "L-02"} {
		task := []string{"T-1", "T-2"}[i]
		r.lanes.lanes = append(r.lanes.lanes, port.Lane{Name: lane, Dir: r.lane(lane), Task: task})
		p := r.f.Agent("w1:p"+lane[3:], "pj-l-0"+lane[3:], "idle", r.lane(lane))
		p.Label = "pj-l-0" + lane[3:] // labelled by its launch: serve's identity of it
	}
	r.srv = serveHerdr(t, r.f)
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
		}, Loop: r.loop, Restore: r.restore}, panes, r.lanes)
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
	trail := func() string { return "app output:\n" + r.out.String() }
	if how := poll(r.t, what, 5*time.Second, time.Millisecond, trail, ok); how != "" {
		r.t.Fatalf("%s waiting for %s; %s", how, what, trail())
	}
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

// callID is the call id of a request line.
func callID(line []byte) string {
	var req struct {
		Call struct {
			CallID string `json:"call_id"`
		} `json:"call"`
	}
	_ = json.Unmarshal(line, &req)
	return req.Call.CallID
}

// briefs is the open briefs obor status shows.
func (r *loopRig) briefs() []wire.BriefStatus { return r.app.Status().Project.Briefs }

// agentGone changes pane by fn and waits until obor status no longer shows agent.
func (r *loopRig) agentGone(pane, agent string, fn func(p *herdrtest.Pane)) {
	r.t.Helper()
	r.f.Set(pane, fn)
	r.waitFor(agent+" gone", func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == agent {
				r.push()
				return false
			}
		}
		return true
	})
}

// Serve's lines and job wakes reach a running agent as its kind takes them (agents.Kind.Deliver): a
// Claude session gets them posted to its inbox (nothing typed into its pane, no ctrl+u), Codex and OpenCode panes get
// them typed into theirs. A Claude session with no inbox record: a line stays queued, a job wake is typed (the fallback).

// keysTo is the agent.send_keys requests to agent.
func (r *loopRig) keysTo(agent string) int {
	n := 0
	for _, req := range r.srv.Requests() {
		var p struct{ Target string }
		_ = json.Unmarshal(req.Params, &p)
		if req.Method == "agent.send_keys" && p.Target == agent {
			n++
		}
	}
	return n
}

// obor dispatch <T-xx> takes its models from the spec's Difficulty level (config difficulty, built-in
// default), the spec's Model: line first: the first model not paused, on the first free lane (every lane is pooled);
// no free lane: the task waits for one; every model of the request paused: it waits, the reply says why. The rig has
// lanes L-01 and L-02, both free.

// dispatchLevel is the lead's dispatch of T-1 by its spec's level (no lane, no types).
func (r *loopRig) dispatchLevel(callID string, types ...string) (string, error) {
	return r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
		CallID: callID}, Task: "T-1", Types: types})
}

func (r *loopRig) specLevel(level string) {
	r.t.Helper()
	r.setSpec("s-"+level, "# T-1 — Leveled\n\nDifficulty: "+level+"\n")
}

// factData is the data of task's first fact of step.
func (r *loopRig) factData(task, step string) map[string]any {
	r.t.Helper()
	for _, rec := range r.records() {
		if rec.Task == task && rec.Kind == event.KindFact && rec.Step == step {
			var d map[string]any
			if err := json.Unmarshal(rec.Data, &d); err != nil {
				r.t.Fatal(err)
			}
			return d
		}
	}
	r.t.Fatalf("no fact %s of %s: %q", step, task, r.steps())
	return nil
}

// Dispatch as state (docs/state-model.md rows 2–9): the dispatch request over the socket, its fact, and the steps the
// merge loop runs after it (lane.prepare, agent.start, the work job), on the merge loop's rig with fake
// LaneSetup and Specs adapters. Lane L-01 starts free (its worker pj-l-01 idle in its pane, session s0); T-1 is the
// task dispatched.

// dispatchFakes is the dispatch's adapters and what they did.
type dispatchFakes struct {
	r  *loopRig
	mu sync.Mutex
	on bool // the loop has the dispatch adapters (its lanes and target in Loop.Workflow, Loop.Specs)
	// The Specs: refusals by task, the target sha, the task branch ahead.
	refuse map[string]string
	texts  []string // the spec text each Check was given (the store's)
	ahead  bool
	// What the LaneSetup and the worker's starts applied (a call cut before it applies counts in calls only).
	prepares, starts int
	resumes          int // the starts without --fresh applied (the worker start rule's)
	calls            map[string]int
	inputs           []port.PrepareInput
	frees            []port.FreeInput
	session          int           // the worker's session number (its transcript /s/<n>.jsonl)
	keepSession      bool          // a start leaves the session as it is
	sessionless      bool          // a start reports no session id for its agent (OpenCode)
	gate             chan struct{} // non-nil: a start, its agent up, waits for it to close before it returns
	// fail: an error a method gives instead of acting (nil: none); cut: where a method blocks until its ctx ends
	// ("before" or "after" it applies), hit: it blocked there.
	fail map[string]func() error
	cut  map[string]string
	hit  map[string]bool
}

func (d *dispatchFakes) loop(fx *app.Loop, panes herdr.Panes) {
	d.mu.Lock()
	defer d.mu.Unlock()
	kit, _ := filepath.Abs("../..")
	if d.on {
		fx.Workflow, fx.Specs = app.WorkflowOf(fx.Workflow, d), d
	}
	fx.Kit = kit
	fx.Typer = cutPrompter{d, panes}
}

// enter counts a call of method and gives its armed failure (nil: act).
func (d *dispatchFakes) enter(method string) error {
	d.mu.Lock()
	d.calls[method]++
	fail := d.fail[method]
	d.mu.Unlock()
	if fail != nil {
		return fail()
	}
	return nil
}

// hold blocks until ctx ends when method is cut at where.
func (d *dispatchFakes) hold(ctx context.Context, method, where string) error {
	d.mu.Lock()
	cut := d.cut[method] == where
	if cut {
		d.hit[method] = true
	}
	d.mu.Unlock()
	if !cut {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func (d *dispatchFakes) Check(_ context.Context, task, text string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.texts = append(d.texts, text)
	return d.refuse[task], nil
}

func (d *dispatchFakes) Target(context.Context, string) (string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return "target1", d.ahead, nil
}

// Prepare puts L-01 on T-1 in the lane table.
func (d *dispatchFakes) Prepare(ctx context.Context, in port.PrepareInput) (port.Prepared, error) {
	if err := d.enter("prepare"); err != nil {
		return port.Prepared{}, err
	}
	if err := d.hold(ctx, "prepare", "before"); err != nil {
		return port.Prepared{}, err
	}
	d.mu.Lock()
	d.prepares++
	d.inputs = append(d.inputs, in)
	d.mu.Unlock()
	d.r.setLane(func(l *port.Lane) { l.Task, l.State = in.Task, "working" })
	if err := d.hold(ctx, "prepare", "after"); err != nil {
		return port.Prepared{}, err
	}
	return port.Prepared{Base: "base-" + in.Target}, nil
}

// Free is the lane given back by a drop: noted, nothing refused.
func (d *dispatchFakes) Free(ctx context.Context, in port.FreeInput) (string, error) {
	if err := d.enter("free"); err != nil {
		return "", err
	}
	d.mu.Lock()
	d.frees = append(d.frees, in)
	d.mu.Unlock()
	return "", nil
}

// startWorker is pj-l-01's start in its pane: a fresh one a new session (unless keepSession), one without --fresh
// (the worker start rule's) the session resumed; idle, seen by the app.
func (d *dispatchFakes) startWorker(ctx context.Context, req port.Launch) (port.Started, error) {
	if err := d.enter("start"); err != nil {
		return port.Started{}, err
	}
	if req.Pane != "w1:p1" {
		return port.Started{}, errors.New("unexpected start of pj-l-01: " + req.Pane)
	}
	if err := d.hold(ctx, "start", "before"); err != nil {
		return port.Started{}, err
	}
	d.mu.Lock()
	d.starts++
	if !req.Fresh {
		d.resumes++
	} else if !d.keepSession {
		d.session++
	}
	transcript := "/s/" + string(rune('0'+d.session)) + ".jsonl"
	sid := transcript // herdr's session id of the agent: what the start reports and the pane shows
	if d.sessionless {
		sid = ""
	}
	d.mu.Unlock()
	d.r.f.Set("w1:p1", func(p *herdrtest.Pane) {
		p.Kind, p.Name, p.Status, p.Transcript, p.Session = "claude", "pj-l-01", "idle", transcript, sid
	})
	d.r.push()
	d.mu.Lock()
	gate := d.gate
	d.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return port.Started{}, ctx.Err()
		}
	}
	if err := d.hold(ctx, "start", "after"); err != nil {
		return port.Started{}, err
	}
	return port.Started{Line: "STARTED pj-l-01", Pane: "w1:p1", Kind: "claude", Session: sid}, nil
}

// cutPrompter is the herdr typer with the work job's cut points.
type cutPrompter struct {
	d     *dispatchFakes
	inner port.Typer
}

func (c cutPrompter) Type(ctx context.Context, pane, text string, clear bool) (bool, error) {
	if !strings.HasPrefix(text, "[obor job ") || !strings.Contains(text, "/work") {
		return c.inner.Type(ctx, pane, text, clear)
	}
	if err := c.d.enter("prompt"); err != nil {
		return false, err
	}
	if err := c.d.hold(ctx, "prompt", "before"); err != nil {
		return false, err
	}
	if typed, err := c.inner.Type(ctx, pane, text, clear); err != nil {
		return typed, err
	}
	return true, c.d.hold(ctx, "prompt", "after")
}

// applied is what the fakes applied: prepares, starts.
func (d *dispatchFakes) applied() (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.prepares, d.starts
}

func (d *dispatchFakes) called(method string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[method]
}

func (d *dispatchFakes) set(fn func(d *dispatchFakes)) {
	d.mu.Lock()
	fn(d)
	d.mu.Unlock()
}

// dispatch is the lead's dispatch of T-1 (lane "" with models: the first free lane for them). A named lane with no
// models runs sonnet: the rig's T-1 has no stored spec, so no level to take one from (every lane is pooled).
func (r *loopRig) dispatch(callID, lane string, types ...string) (string, error) {
	if lane != "" && len(types) == 0 {
		types = []string{"sonnet"}
	}
	return r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
		CallID: callID}, Task: "T-1", Lane: lane, Types: types})
}

func (r *loopRig) dispatchCall(c wire.DispatchReq) (string, error) {
	return r.ask(&c)
}

func (r *loopRig) mustDispatch(callID string) {
	r.t.Helper()
	if reply, err := r.dispatch(callID, "L-01"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
		r.t.Fatalf("dispatch: %q, %v", reply, err)
	}
}

// workerIs sets pj-l-01's status in the fake herdr and waits until the app sees it.
func (r *loopRig) workerIs(status string) {
	r.t.Helper()
	r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Status = status })
	r.waitFor("pj-l-01 "+status, func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == "pj-l-01" && a.Status == status {
				return true
			}
		}
		r.push()
		return false
	})
}

// workPrompts is how many work job prompts pj-l-01 got (job T-1/<attempt>/work…).
func (r *loopRig) workPrompts() int {
	n := 0
	for _, p := range r.prompts() {
		if p.to == "pj-l-01" && strings.HasPrefix(p.text, "[obor job T-1/") && strings.Contains(p.text, "/work") {
			n++
		}
	}
	return n
}

// needsYouState waits until T-1's needs-you is folded (its record is written just before).
func (r *loopRig) needsYouState() {
	r.t.Helper()
	r.waitFor("T-1 needs-you", func() bool { return r.taskState() == "needs-you" })
}

// taskState is T-1's state in obor status --facts ("" when not listed).
func (r *loopRig) taskState() string {
	for _, t := range r.app.Status().Project.Tasks {
		if t.Task == "T-1" {
			return t.State
		}
	}
	return ""
}

// dispatchSteps: the start carries the work job's wake (its intent first, the start's result its delivery).
var dispatchSteps = []string{"1 fact dispatch", "1 intent lane.prepare", "1 result lane.prepare",
	"1 intent work", "1 intent agent.start", "1 result agent.start", "1 result work"}

// addLanes appends lanes to the rig's lane table after L-01 (config order).
func (r *loopRig) addLanes(ls ...port.Lane) {
	r.lanes.mu.Lock()
	r.lanes.lanes = append(r.lanes.lanes, ls...)
	r.lanes.mu.Unlock()
}

// lastClose is a review git with no close commit; what a review covered is the log's (reviewsLog).
type lastClose struct{}

func (lastClose) Head(context.Context, string) (string, error) { return "head1", nil }

func (lastClose) Files(context.Context, string, string) ([]string, error) { return nil, nil }

func (lastClose) Detach(context.Context, string, string) error { return nil }

func (lastClose) Close(context.Context, port.ReviewCommit) error { return nil }

func (lastClose) Closed(context.Context, string, string, string) (string, error) { return "", nil }

// reviewsLog writes the log of a stopped app: RV-4 started and closed, then each of open started (not closed).
func (r *loopRig) reviewsLog(open ...string) {
	r.t.Helper()
	rec := func(step string, d app.ReviewData) event.Record {
		raw, _ := json.Marshal(d)
		return event.Record{Task: event.ReviewTask, Step: step, Kind: event.KindFact, Data: raw}
	}
	recs := []event.Record{rec(event.StepReviewStarted, app.ReviewData{ID: "RV-4", Head: "1111111"}),
		rec(event.StepReviewClosed, app.ReviewData{ID: "RV-4"})}
	for _, id := range open {
		recs = append(recs, rec(event.StepReviewStarted, app.ReviewData{ID: id, Head: "head0"}))
	}
	if err := app.WriteLog(r.p.Root, time.Now(), recs...); err != nil {
		r.t.Fatal(err)
	}
}

// The owner's drive on the dispatch rig: obor drive's request (a new drive on the first free lane; a worker's
// task refused), the driving state, obor land's request, and the merge loop after it. Lane L-01 is made
// free for a new drive; the CLI's start of the drive agent is the rig's pane w1:p5 (driveAgentUp).

// drive is obor drive's request by the owner (no role): a new drive named name (types: its --type), or a task's when
// name is a task id.
func (r *loopRig) drive(callID, name string, types ...string) (string, error) {
	c := &wire.DriveReq{Envelope: wire.Envelope{Root: r.p.Root, CallID: callID}, Name: name, Types: types}
	if strings.HasPrefix(name, "T-") {
		c.Task, c.Name = name, ""
	}
	return r.ask(c)
}

// landDrive is obor land's request by the owner for task with summary.
func (r *loopRig) landDrive(callID, task, summary string) (string, error) {
	return r.ask(&wire.LandReq{Envelope: wire.Envelope{Root: r.p.Root,
		CallID: callID}, Task: task, Report: summary})
}

// stateOf is task's state in obor status --facts ("" when not listed).
func (r *loopRig) stateOf(task string) string { return r.task(task).State }

// driveAgentUp is the CLI's start of the drive agent pj-l-01-drive in the lane (pane w1:p5, labelled), identified by the
// app (seen in a pane is not enough: obor drive's DRIVING answer needs it identified). The pane is announced as created
// rather than with a workspace event: that ends serve's stream (a refresh), and a call that landed in the resubscribe's
// gap answered "its agent is not running" under load.
func (r *loopRig) driveAgentUp() {
	r.t.Helper()
	r.f.Agent("w1:p5", "pj-l-01-drive", "idle", r.lane)
	r.f.Set("w1:p5", func(p *herdrtest.Pane) { p.Label = "pj-l-01-drive" })
	r.waitFor("pj-l-01-drive identified", func() bool {
		if app.IsIdentified(r.app, r.p.Root, "pj-l-01-drive") {
			return true
		}
		r.paneCreated("w1:p5", "pj-l-01-drive")
		return false
	})
}

// paneCreated tells serve pane exists: a pane.created event, which serve answers by reading the pane again, in place (no
// refresh). The pane is the event's own, so serve's proof reads its label and workspace from here.
func (r *loopRig) paneCreated(pane, label string) {
	r.srv.Push("pane_created", map[string]any{"type": "pane_created", "pane_id": pane,
		"pane": map[string]any{"pane_id": pane, "label": label, "workspace_id": "w1"}})
}

// driveLines is the lead's lines that a drive came back to its owner.
func (r *loopRig) driveLines() []string {
	var out []string
	for _, rec := range r.records() {
		var m event.Message
		_ = json.Unmarshal(rec.Data, &m)
		if rec.Kind == event.KindFact && rec.Step == event.StepMessage && m.Drive != 0 {
			out = append(out, m.Line)
		}
	}
	return out
}

// launchesOf is how many Agents.Launch calls started agent.
func (r *loopRig) launchesOf(agent string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, l := range r.launches {
		if l.Agent == agent {
			n++
		}
	}
	return n
}

// hardType is the rig config's hard level's first model ref (a new drive's default).
func (r *loopRig) hardType() string {
	r.t.Helper()
	cfg, err := config.Load(r.p.Root)
	if err != nil {
		r.t.Fatal(err)
	}
	types := cfg.DifficultyList("hard")
	if len(types) == 0 {
		r.t.Fatal("precondition: the rig config has no hard type")
	}
	return types[0]
}

// A drive and the lead message each other. The drive agent's obor tell-lead is a lead line naming its task; the
// lead's obor answer on a driving task is a line to the drive agent (no question open), delivered when its pane is idle.

// tellLead is obor tell-lead's request from agent with role in lane.
func (r *loopRig) tellLead(callID, agent, role, lane, text string) (string, error) {
	return r.ask(&wire.TellLeadReq{Envelope: wire.Envelope{Root: r.p.Root,
		CallID: callID, Agent: agent, Role: role}, Lane: lane, Text: text})
}

// driverTold is the facts message{driver} in the log (the drive's lines to the lead).
func (r *loopRig) driverTold() []event.Message {
	var out []event.Message
	for _, rec := range r.records() {
		var m event.Message
		_ = json.Unmarshal(rec.Data, &m)
		if rec.Kind == event.KindFact && rec.Step == event.StepMessage && m.Driver {
			out = append(out, m)
		}
	}
	return out
}

// toDrive is how often line reached the drive agent pj-l-01-drive (typed into its pane w1:p5, or posted to its inbox).
func (r *loopRig) toDrive(line string) int {
	return r.typedTo("w1:p5", line) + r.inbox.count("pj-l-01-drive", line)
}

// driveStatus sets the drive agent's pane status and waits until the app sees it.
func (r *loopRig) driveStatus(status string) {
	r.t.Helper()
	r.f.Set("w1:p5", func(p *herdrtest.Pane) { p.Status = status })
	r.waitFor("pj-l-01-drive "+status, func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == "pj-l-01-drive" && a.Status == status {
				return true
			}
		}
		r.push()
		return false
	})
	r.connectedAgain() // the pushes refreshed serve: its stream again before a call that asks its identity
}

// leadAnswer is the lead's obor answer on T-1.
func (r *loopRig) leadAnswer(callID, text string) (string, error) {
	return r.ask(&wire.ResumeReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-lead", Role: "lead", CallID: callID}, Task: "T-1", Text: text})
}

// dropSpec is the lead's obor drop of task (a spec stored and never dispatched) with why; fatal unless DROPPED.
func (r *loopRig) dropSpec(callID, task, why string) {
	r.t.Helper()
	reply, err := r.ask(&wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", CallID: callID}, Task: task, Why: why})
	if err != nil || reply != "DROPPED "+task {
		r.t.Fatalf("drop of %s: %q, %v; want DROPPED %s", task, reply, err, task)
	}
}

// launchOf is the first Agents.Launch of agent (ok false: none yet).
func (r *loopRig) launchOf(agent string) (port.Launch, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.launches {
		if l.Agent == agent {
			return l, true
		}
	}
	return port.Launch{}, false
}

// An intent of an ended task (merged, dropped) is not pending on replay: it is settled as nothing ("not run"),
// never run again; one of an open task replays as today. The logs are what an earlier build left (dropped with
// agent.start and work open; merged with an earlier attempt's work open).

// logRec is one record a case writes to the log before the app starts.
type logRec struct {
	attempt         int
	step, kind, cls string
	data            any
}

// writeLog appends recs to T-1's log of a stopped app.
func (r *loopRig) writeLog(recs []logRec) {
	r.t.Helper()
	var out []event.Record
	for _, x := range recs {
		var raw json.RawMessage
		if x.data != nil {
			raw, _ = json.Marshal(x.data)
		}
		out = append(out, event.Record{Task: "T-1", Attempt: x.attempt, Step: x.step, Kind: x.kind, Class: x.cls, Data: raw})
	}
	if err := app.WriteLog(r.p.Root, time.Now(), out...); err != nil {
		r.t.Fatal(err)
	}
}

// resultOf is the data of T-1's result of step at attempt ("": none).
func (r *loopRig) resultOf(attempt int, step string) string {
	for _, rec := range r.records() {
		if rec.Kind == event.KindResult && rec.Attempt == attempt && rec.Step == step {
			return string(rec.Data)
		}
	}
	return ""
}

// The failure rule (docs/state-model.md "Crash classes"): an effect gives a result only for an outcome final
// for its step; anything else leaves the step pending, tried again, and its third counted failure is a needs-you
// during which the step is not tried (also after a restart); obor resume's default retries it.

// countPrefix is how many records' lines start with s.
func (r *loopRig) countPrefix(s string) int {
	n := 0
	for _, line := range r.steps() {
		if strings.HasPrefix(line, s) {
			n++
		}
	}
	return n
}

// gitIO is the error an armed Scratch method gives: git could not tell.
func gitIO(context.Context) error { return errors.New("git I/O: index.lock exists") }

// armScratch makes the Scratch's method fail; the heal is returned.
func (r *loopRig) armScratch(method string) func() {
	r.scratch.Arm(method, gitIO)
	return func() { r.scratch.Arm(method, nil) }
}

// notFound is an adapter's "no such agent" (app.NotFound).
type notFound struct{}

func (notFound) Error() string { return "agent_not_found: pj-l-01" }

func (notFound) NotFound() bool { return true }

// startsWere fails the test unless pj-int was started n times, each fresh with the job's wake, forced or not as
// force says, and nothing was typed to it.
func (r *loopRig) startsWere(n int, force bool) {
	r.t.Helper()
	r.mu.Lock()
	var starts []port.Launch
	for _, l := range r.launches {
		if l.Agent == "pj-int" {
			starts = append(starts, l)
		}
	}
	r.mu.Unlock()
	if len(starts) != n || len(r.prompts()) != 0 {
		r.t.Fatalf("want %d starts of pj-int and no prompt: %+v, prompts %d, %q", n, starts, len(r.prompts()), r.steps())
	}
	for _, l := range starts {
		if !l.Fresh || l.Force != force || l.Prompt != "[obor job T-1/1/review] call the obor job_next tool" {
			r.t.Errorf("start %+v: want fresh, force %v, the review job's wake", l, force)
		}
	}
}

// Jobs by fetch (docs/state-model.md rows 5–8, 16–17) on the dispatch rig: every
// job's prompt is its wake, the agent's job_next fetches the job (its first fetch is fact delivered via mcp, its only
// delivery), and the worker's task_done, its report the argument, submits its task.

// fetchRig is the dispatch rig (edits change the config further).
func fetchRig(t *testing.T, edits ...func(map[string]any)) *loopRig {
	t.Helper()
	return newLoopRig(t, dispatching(), withConfig(edits...))
}

// task is serve's status of task (zero: none).
func (r *loopRig) task(task string) wire.TaskStatus {
	for _, t := range r.app.Status().Project.Tasks {
		if t.Task == task {
			return t
		}
	}
	return wire.TaskStatus{}
}

// wake is the one line typed for job (the Prompter's tag, then the wake).
func wake(job string) string { return "[obor job " + job + "] call the obor job_next tool" }

// call is a job tool's socket request by agent (role, lane) with call id: job_next, or a verdict on T-1; job names the
// job it answers ("": none).
func (r *loopRig) call(method, agent, role, lane, callID, job string) (string, error) {
	id := wire.Envelope{Root: r.p.Root, Agent: agent, Role: role, CallID: callID}
	var c wire.Req = &wire.VerdictReq{Envelope: id, Verb: method, Task: "T-1", Job: job}
	if method == wire.MethodJobNext {
		c = &wire.JobNextReq{Envelope: id, Job: job, Lane: lane}
	}
	return r.ask(c)
}

// jobNext is job_next by agent with call id, its reply decoded.
func (r *loopRig) jobNext(agent, role, lane, callID, job string) (wire.JobReply, string, error) {
	r.t.Helper()
	raw, err := r.call(wire.MethodJobNext, agent, role, lane, callID, job)
	var jr wire.JobReply
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &jr); err != nil {
			r.t.Fatalf("job_next reply %q: %v", raw, err)
		}
	}
	return jr, raw, err
}

// workerNext is pj-l-01's job_next from lane L-01.
func (r *loopRig) workerNext(callID string) wire.JobReply {
	r.t.Helper()
	jr, _, err := r.jobNext("pj-l-01", "worker", "L-01", callID, "")
	if err != nil {
		r.t.Fatalf("job_next of pj-l-01: %v", err)
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
		if rec.Kind == event.KindFact && rec.Step == event.StepDelivered {
			out = append(out, rec.Fact().Job+" "+rec.Fact().Via)
		}
	}
	return out
}

// setConfig sets path in the project's config on disk (read at the app's next load). The file is shape 2, so path is
// one of its names.
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
	eq(t, r.promptsTo("pj-int"), []string(nil), "typed to pj-int (its start carried the wake)")
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

// callDone is agent's task_done for T-1 (role, lane L-01) with call id and report.
func (r *loopRig) callDone(agent, role, callID, report string) (string, error) {
	return r.ask(&wire.TaskDoneReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: agent, Role: role, CallID: callID}, Lane: "L-01", Task: "T-1", Report: report})
}

// A task whose move's stop of its worker is pending (recorded, not settled) is busy for every other
// verb: a drop is refused, "retry when it settles". A stop that failed (its needs-you) is settled, and a drop then
// stops the halt's recorded agent (the old worker) before it frees the lane. The rig: newLoopRig(t, working()) (T-1 working on L-01, its worker pj-l-01 in w1:p1).

// dropCall is obor drop of T-1 by the lead (its reply or refusal).
func (r *loopRig) dropCall(callID, why string) (string, error) {
	return r.ask(&wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", Agent: "pj-lead", CallID: callID}, Task: "T-1", Why: why})
}

// refusedWhileStopPending: verb's call (T-1, its stop pending, not running: cutStops) is refused busy, naming the
// pending worker.stop of pj-l-01, and records nothing. It is called until a call lands between two tries of the stop,
// where the app rests (cutStops); a call during a try is busy too, as a held step. Any call accepted fails the test.
func (r *loopRig) refusedWhileStopPending(verb string, call func() (string, error)) {
	r.t.Helper()
	want := verb + " T-1: T-1 has worker.stop of pj-l-01 pending; retry when it settles"
	r.waitFor(verb+" refused for the pending stop", func() bool {
		reply, err := call()
		if err == nil {
			r.t.Fatalf("%s while the stop is pending: accepted (%q); want refused busy", verb, reply)
		}
		if !strings.Contains(err.Error(), "retry when it settles") {
			r.t.Fatalf("%s while the stop is pending: %v; want refused busy", verb, err)
		}
		return err.Error() == want
	})
	if r.hasPrefix("1 fact dropping") || r.hasPrefix("2 fact dropping") || r.hasPrefix("1 fact submitted") {
		r.t.Errorf("%s while the stop is pending recorded: %q", verb, r.steps())
	}
}

// cutStops makes each stop cut (serve's own stop cut it: no result, its intent pending, tried again on the next
// pass) until the returned settle lets the stops act; tried gets one value per try. The app tries a cut step again
// as soon as it has read the lane table the step marked, on no timer: each read takes a millisecond while the stops
// are cut, so the app rests between two tries with the stop pending and the test's clock moves (a loop that never
// rests holds a bubble's clock still, and every wait on it with it).
func (r *loopRig) cutStops() (tried <-chan struct{}, settle func()) {
	in := make(chan struct{}, 1)
	r.lanes.paced(time.Millisecond)
	r.mu.Lock()
	r.onStop = func() error {
		select {
		case in <- struct{}{}:
		default:
		}
		return context.Canceled
	}
	r.mu.Unlock()
	return in, func() {
		r.mu.Lock()
		r.onStop = nil
		r.mu.Unlock()
		r.lanes.paced(0)
	}
}

// intentData is the data of T-1's last intent of step at attempt (nil: none).
func (r *loopRig) intentData(attempt int, step string) map[string]any {
	var d map[string]any
	for _, rec := range r.records() {
		if rec.Kind == event.KindIntent && rec.Attempt == attempt && rec.Step == step {
			d = nil
			_ = json.Unmarshal(rec.Data, &d)
		}
	}
	return d
}

// failStops makes the rig's stops fail (on) or act (off).
func (r *loopRig) failStops(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onStop = nil
	if on {
		r.onStop = func() error { return errors.New("herdr: pane busy") }
	}
}

// dropFrees: the drop answers DROPPING naming stopping, then T-1 is dropped with its lane freed once.
func (r *loopRig) dropFrees(callID, stopping string) {
	r.t.Helper()
	reply, err := r.dropCall(callID, "not needed")
	if want := "DROPPING T-1 from L-01 (not needed; stopping " + stopping + ")"; err != nil || reply != want {
		r.t.Fatalf("drop: %q, %v; want %q", reply, err, want)
	}
	r.waitFor("T-1 dropped", func() bool { return r.stateOf("T-1") == "dropped" })
	if n := r.disp.called("free"); n != 1 {
		r.t.Errorf("lane freed %d times, want once", n)
	}
}

// oldWorkerStoppedFirst: the drop's worker.stop.2 of attempt (the old worker, with force) settled before its
// lane.free, the old worker's stop tried twice, the drive agent never.
func (r *loopRig) oldWorkerStoppedFirst(attempt int) {
	r.t.Helper()
	at := string(rune('0' + attempt))
	if d := r.factData("T-1", event.StepDropping); d["worker"] != "pj-l-01" || d["force"] != true {
		r.t.Errorf("the dropping fact %v; want worker pj-l-01 with force (the halt's)", d)
	}
	if d := r.intentData(attempt, "worker.stop.2"); d["agent"] != "pj-l-01" || d["force"] != true {
		r.t.Errorf("the drop's worker.stop.2 intent %v; want pj-l-01 with force", d)
	}
	if !r.before(at+" result worker.stop.2", at+" intent lane.free") {
		r.t.Errorf("the steps %q; want the old worker's stop settled, then lane.free", r.steps())
	}
	if got := r.workerStops(); !slices.Equal(got, []string{"pj-l-01 w1:p1", "pj-l-01 w1:p1"}) {
		r.t.Errorf("stops %q; want pj-l-01 twice (the failed one, the drop's), never the drive agent", got)
	}
}

// The idle rules end to end (their table is rules_test.go's TestIdleRules): what keeps the pipeline from
// being idle, the line through the merge loop and the acknowledgement through the socket, and the line's order after a
// watch's.

// idleLine is the idle line after task's merge (idleLineFor) or a drop.
func idleLineFor(r *loopRig, task string) string {
	r.t.Helper()
	sha := r.scratch.TargetHead()[:7]
	return idleLineText(task + " merged " + sha)
}

func idleLineText(last string) string {
	return `[obor] idle: nothing in flight (last: ` + last + `) — dispatch next, or obor idle "<why nothing>"`
}

// idleSettle is the window: the idle line waits until the pipeline has stayed idle this long.
const idleSettle = time.Minute

// settleIdle lets the idle window pass: two passes start its clock, then the clock moves past idleSettle, so the next
// idle check posts the line.
func (r *loopRig) settleIdle() {
	r.t.Helper()
	r.passes()          // the idle check that starts the window
	r.later(idleSettle) // the first check past it: the line goes out
}

// idlePosts is the idle lines the lead's inbox got, in order.
func (r *loopRig) idlePosts() []string {
	var out []string
	for _, p := range r.inbox.posted() {
		if strings.HasPrefix(p, "pj-lead: [obor] idle:") {
			out = append(out, strings.TrimPrefix(p, "pj-lead: "))
		}
	}
	return out
}

// logRecords appends recs to the project's event log before the app starts.
func (r *loopRig) logRecords(recs ...event.Record) {
	r.t.Helper()
	if err := app.WriteLog(r.p.Root, time.Now(), recs...); err != nil {
		r.t.Fatal(err)
	}
}

// record is one fact or intent of task at attempt, its data as JSON.
func record(t *testing.T, task string, attempt int, step, kind, class string, data any) event.Record {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return event.Record{Task: task, Attempt: attempt, Step: step, Kind: kind, Class: class, Data: raw}
}

// mergedTask runs T-1 from its task_done to its merge (as mergedT1, for a rig that has no dispatch to make).
func (r *loopRig) mergedTask() {
	r.t.Helper()
	r.work("feature.txt", "feature\n")
	r.mergedT1()
}

// idleAcked runs T-1 to its merge, waits for its idle line and acknowledges it; it returns the line.
func (r *loopRig) idleAcked() string {
	r.t.Helper()
	r.start()
	r.mergedTask()
	line := idleLineFor(r, "T-1")
	r.settleIdle()
	r.waitFor("the idle line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	if reply, err := r.line("i1", &wire.IdleReq{Why: "nothing to dispatch"}); err != nil ||
		reply != "ACKED idle: nothing to dispatch" {
		r.t.Fatalf("idle ack: %q, %v", reply, err)
	}
	return line
}

// The integrator's jobs: their delivery and stall timers on the app's clock, obor hold and obor resume.

// integratorIs sets pj-int's status in the fake herdr and waits until the app sees it.
func (r *loopRig) integratorIs(status string) {
	r.t.Helper()
	r.f.Set("w1:p9", func(p *herdrtest.Pane) { p.Status = status })
	r.waitFor("pj-int "+status, func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == "pj-int" && a.Status == status {
				return true
			}
		}
		r.push()
		return false
	})
}

// integratorStops ends pj-int's turn after a verdict that left it no job, and waits for serve's stop of it:
// its idle is gone again once the next pass stops it, so a wait on obor status's "idle" can miss it;
// the stop is a rule now and is done when the pane is at its shell.
//
// One refresh, then no push while it waits: the rule stops pj-int only on a pass that stays on one herdr stream
// from its start to the rule (whereIs is "not connected" in a resubscribe's gap), and a push ends the stream. Pushed
// on every poll, the streams were shorter than a pass under load, so the stop waited a minute and more; serve's tick
// runs the passes, and the stop's own refreshes (the rig's Stop) end once serve sees pj-int gone.
func (r *loopRig) integratorStops() {
	r.t.Helper()
	r.f.Set("w1:p9", func(p *herdrtest.Pane) { p.Status = "idle" })
	r.connectedAgain() // serve reads the idle from a snapshot taken after the Set
	r.waitFor("the stop of pj-int", func() bool { return slices.Contains(r.app.Status().Project.Stopped, "pj-int") })
}

// integratorGone makes pj-int's agent exit and waits until the merge loop has seen it gone for T-1's open job, job
// (its Gone grace counts from that pass, on the fake clock).
func (r *loopRig) integratorGone(job string) {
	r.t.Helper()
	r.f.Set("w1:p9", func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status = "", "", "" })
	r.waitFor("pj-int gone", func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == "pj-int" {
				r.push()
				return false
			}
		}
		return true
	})
	r.waitTimer("pj-int seen gone with "+job, func(j string, gone, _ time.Time) bool { return j == job && !gone.IsZero() })
}

// waitTimer waits until T-1's job timer is as ok says (the merge loop has taken the sample a test step depends on).
func (r *loopRig) waitTimer(what string, ok func(job string, gone, quiet time.Time) bool) {
	r.t.Helper()
	r.waitFor(what, func() bool { return ok(app.JobTimer(r.app, r.p.Root, "T-1")) })
}

// quietBaseline waits for the stall timer's first quiet sample of T-1's job.
func (r *loopRig) quietBaseline(job string) {
	r.t.Helper()
	r.waitTimer("the quiet baseline of "+job, func(j string, _, quiet time.Time) bool { return j == job && !quiet.IsZero() })
}

// workspaceEvent is the rig's workspace-created event: pushed, it makes the app read a new snapshot.
var workspaceEvent = map[string]any{"type": "workspace_created",
	"workspace": map[string]any{"workspace_id": "w1", "label": "proj"}}

// push makes the app read a new snapshot: a workspace event ends serve's stream (a refresh) and serve subscribes again.
func (r *loopRig) push() { r.srv.Push("workspace_created", workspaceEvent) }

// connectedAgain waits, as an event, until serve works from a herdr stream that began after the rig's pushes: a refresh
// reaches the stream serve has now, serve reads it and ends that stream, and subscribes again — the wait is that new
// stream's lifecycle subscription and serve's connection to it. Status().Connected alone is no such event: it
// is still true in the instant before serve ends a stream (serve closes the stream, then clears the flag), and a push
// is only a socket write that the stream may read after the caller looked — the call then lands in the resubscribe's
// gap and is refused "serve is not connected to herdr".
func (r *loopRig) connectedAgain() {
	r.t.Helper()
	var next func() bool // that stream ended and a stream after it subscribed: true once it connected too
	r.waitFor("serve connected to herdr on the stream after the rig's pushes", func() bool {
		if next == nil {
			var reached bool
			if reached, next = r.srv.Refresh(workspaceEvent); !reached { // serve is between two streams: none to end
				next = nil
				return false
			}
		}
		return r.app.Status().Connected && next()
	})
}

// passes waits for two more passes of the merge loop: the second one started after the call, so it saw the clock and
// the state the test set before it. Only passes that ran connected to one herdr stream count (StreamPasses): the
// rig's pushes end serve's stream, and a pass in the resubscribe's gap skips the project.
func (r *loopRig) passes() {
	r.t.Helper()
	n := app.StreamPasses(r.app)
	r.waitFor("two more passes", func() bool { return app.StreamPasses(r.app) >= n+2 })
}

// later moves the app's clock on by d, then waits for a few passes.
func (r *loopRig) later(d time.Duration) {
	r.clock.Add(d)
	r.passes()
}

// jobPrompts is how many prompts pj-int got for job.
func (r *loopRig) jobPrompts(job string) int {
	n := 0
	for _, p := range r.prompts() {
		if p.to == "pj-int" && strings.HasPrefix(p.text, "[obor job "+job+"] ") {
			n++
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.launches { // a start's first prompt: the job's wake
		if l.Agent == "pj-int" && l.Prompt == "[obor job "+job+"] call the obor job_next tool" {
			n++
		}
	}
	return n
}

// candidatePatch is the patch hash of the attempt's last green candidate (the gate's result), what a review job names.
func (r *loopRig) candidatePatch() string {
	r.t.Helper()
	var patch string
	for _, rec := range r.records() {
		if rec.Kind == event.KindResult && strings.HasPrefix(rec.Step, "gate") {
			var g struct{ Candidate struct{ Patch string } }
			_ = json.Unmarshal(rec.Data, &g)
			patch = g.Candidate.Patch
		}
	}
	if patch == "" {
		r.t.Fatalf("no green candidate: %q", r.steps())
	}
	return patch
}

// hasPrefix: a record's "<attempt> <kind> <step>{reason}" line starts with s.
func (r *loopRig) hasPrefix(s string) bool {
	for _, line := range r.steps() {
		if strings.HasPrefix(line, s) {
			return true
		}
	}
	return false
}

func (r *loopRig) waitPrefix(s string) {
	r.t.Helper()
	r.waitFor("a record "+s+"…", func() bool { return r.hasPrefix(s) })
}

// resume is the lead's obor resume of T-1 (to "": the cause's default).
func (r *loopRig) resume(callID, to string) (string, error) {
	return r.resumeWith(callID, to, "")
}

// resumeWith is the lead's obor resume of T-1 with flag to and text note.
func (r *loopRig) resumeWith(callID, to, note string) (string, error) {
	return r.ask(&wire.ResumeReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-lead", Role: "lead", CallID: callID}, Task: "T-1", Flag: to, Text: note})
}

// mustResume is resume with the reply it must give.
func (r *loopRig) mustResume(callID, to, want string) {
	r.t.Helper()
	if reply, err := r.resume(callID, to); err != nil || reply != want {
		r.t.Fatalf("resume %s: %q, %v; want %q", to, reply, err, want)
	}
}

// facts is how many facts are in force for the project.
func (r *loopRig) facts() int { return len(r.app.Status().Project.Facts) }

// reviewed is the rig at T-1's first review job fetched (delivered; the integrator idle, the clock where the job went
// out): the job's text.
func reviewed(t *testing.T) (*loopRig, string) {
	r := woke(t)
	return r, r.waitJob("pj-int", "T-1/1/review")
}

// woke is the rig at T-1's first review job sent, its wake carried by the integrator's start, not fetched.
func woke(t *testing.T) *loopRig {
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	r.submit()
	r.waitWake("pj-int", "T-1/1/review")
	return r
}

// dispatchTo is the lead's dispatch of task to lane, as sonnet.
func (r *loopRig) dispatchTo(callID, task, lane string) (string, error) {
	return r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
		CallID: callID}, Task: task, Lane: lane, Types: []string{"sonnet"}})
}

// record is task's newest record of step and kind (ok false: none).
func (r *loopRig) record(task, kind, step string) (event.Record, bool) {
	var out event.Record
	ok := false
	for _, rec := range r.records() {
		if rec.Task == task && rec.Kind == kind && rec.Step == step {
			out, ok = rec, true
		}
	}
	return out, ok
}

// runToMerge takes task, its work job fetched by pj-l-01, through its task_done, the review and the accept to merged,
// then frees L-01 in the lane table (its row's task landed: what the lane table reads after a merge). inReview runs
// while its review job is out (nil: nothing).
func (r *loopRig) runToMerge(task string, inReview func()) {
	r.t.Helper()
	r.work(strings.ToLower(task)+".txt", task+"\n")
	r.waitFor(task+" working", func() bool { return r.stateOf(task) == "working" })
	reply, err := r.ask(&wire.TaskDoneReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-l-01", Role: "worker", CallID: "done-" + task}, Lane: "L-01", Task: task, Report: report})
	if err != nil || reply != "submitted" {
		r.t.Fatalf("%s's task_done: %q, %v", task, reply, err)
	}
	job := r.waitJob("pj-int", task+"/1/review")
	if inReview != nil {
		inReview()
	}
	if reply, err := r.ask(&wire.VerdictReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-int", Role: "integrator", CallID: "accept-" + task}, Verb: wire.MethodAccept, Task: task,
		Patch: r.patch(job)}); err != nil ||
		!strings.HasPrefix(reply, "ACCEPT "+task) {
		r.t.Fatalf("%s's accept: %q, %v", task, reply, err)
	}
	r.waitFor(task+"'s after_merge", func() bool {
		_, ok := r.record(task, event.KindResult, event.StepAfterMerge)
		return ok
	})
	r.setLane(func(l *port.Lane) { l.Task, l.State = "", "free" })
}

// A redo runs the model ref its dispatch ran unless its --model names another; a model ref naming no
// model of the project is refused before a lane is picked. The rig: lanes L-01 and L-02, both free.

// specOf stores task's spec (obor spec set).
func (r *loopRig) specOf(task, text string) {
	r.t.Helper()
	if _, err := r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "lead", Role: "lead", CallID: "s-" + task}, Task: task, Spec: text}); err != nil {
		r.t.Fatalf("spec set %s: %v", task, err)
	}
}

// dispatchOf is the lead's dispatch of task by its spec's level.
func (r *loopRig) dispatchOf(task string) (string, error) {
	return r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
		CallID: "d-" + task}, Task: task})
}

// redoTypes is the type of each of T-1's dispatch facts, in order.
func (r *loopRig) redoTypes() []any {
	var types []any
	for _, rec := range r.records() {
		if rec.Task == "T-1" && rec.Kind == event.KindFact && rec.Step == event.StepDispatched {
			var m map[string]any
			_ = json.Unmarshal(rec.Data, &m)
			types = append(types, m["type"])
		}
	}
	return types
}

// The merge loop at the app boundary: the herdr socket client on the fake herdr socket (snapshot, job prompts,
// notifications), the package's in-memory Scratch (app.FakeWorkflow: no git), a scripted lane table, and an
// integrator start that puts pj-int into the fake herdr.

// loopRig is project proj (a plain dir: its config written, no git) with lane L-01 on task/T-1 (a plain dir the fake
// Scratch snapshots; its worker pj-l-01 idle in it; without the dispatch option T-1's dispatch fact in the log at the
// first start), roles.integrator (pj-int, not running until started) and the app not started yet (unless an option
// starts it). merge.check is configured ("echo 1 pass": its outcome is the fake's, Script).
type loopRig struct {
	t       *testing.T
	p       *testkit.Project
	lane    string
	f       *herdrtest.Fake
	srv     *herdrtest.Server
	lanes   *laneTable
	scratch *app.FakeWorkflow
	starts  atomic.Int32
	callIDs atomic.Int32 // the rig's own task_done and job_next call ids
	early   bool         // submit before the first start: T-1's task_done is in the log the start replays

	mu       sync.Mutex
	stops    []string      // "<agent> <pane>" per Agents.Stop
	launches []port.Launch // every Agents.Launch, in order
	// onStart, onStop: an error a start or stop gives instead of acting (nil: none; under mu).
	onStart, onStop func() error
	loopErr         error // what the Loop adapters' constructor fails with (nil: none; under mu)

	disp  *dispatchFakes // the dispatch's adapters (nil: none, the loop has no dispatch)
	tick  time.Duration  // the watchdogs' tick, each a merge-loop pass (0: 2 ms); set before start
	night *app.Nightly   // the loop's Nightly (nil: none; off is At ""); set before start
	// nightOf, when set, is the loop's Nightly built on each pass (serve's: from the config as it is); set
	// before start
	nightOf func() *app.Nightly
	config  app.ConfigFile // the loop's config_set writer (nil: none); set before start
	review  port.Review    // the loop's review git (nil: the fake workflow's own); set before start
	disk    *app.Disk      // the loop's Disk (nil: none); set before start
	// restart is the loop's Restart, serve's self-restart (nil: none); set before start
	restart func(ctx context.Context) error
	// binary is Options.Binary, a new obor binary there (nil: not watched); set before start
	binary func() bool
	// exit is Options.Exit, the socket's exit request (nil: none); set before start
	exit func()
	// tokens is Options.Tokens, a transcript's context size (nil: the file read); set before start
	tokens func(transcript string) (int, bool)
	// inbox is the loop's Inbox: the lead's lines posted, kept across restarts. The worker pj-l-01 and the
	// integrator pj-int start with no inbox record, so their job wakes are typed; a test of posted
	// wakes records them (inbox.record).
	inbox *rigInbox

	clock  *clock // the app's clock, kept across restarts
	app    *app.App
	out    *syncBuf
	sock   string        // the app's socket outside a bubble ("" in one, and before the first start)
	mem    *app.MemServe // the app's socket in a bubble (nil outside one)
	cancel context.CancelFunc
	done   chan struct{}
}

// rigOpts is what newLoopRig's options ask for (one rig constructor, its variants options).
type rigOpts struct {
	check    bool                   // merge.check configured (a green lane test says rc=0; else no check)
	edits    []func(map[string]any) // config edits
	dispatch bool                   // the dispatch adapters, L-01 free
	pool     bool                   // lanes 2: L-02 free too (the dispatch adapters on)
	start    bool                   // started
	working  bool                   // T-1 dispatched by its level onto L-01, its worker working (started)
}

type rigOpt func(*rigOpts)

// noCheck: no merge.check (a green lane test says "no check").
func noCheck() rigOpt { return func(o *rigOpts) { o.check = false } }

// withConfig changes the config further.
func withConfig(edits ...func(map[string]any)) rigOpt {
	return func(o *rigOpts) { o.edits = append(o.edits, edits...) }
}

// dispatching is the rig with the dispatch adapters (the workflow's lanes and target, Loop.Specs) and L-01 free.
func dispatching() rigOpt { return func(o *rigOpts) { o.dispatch = true } }

// started is the rig started.
func started() rigOpt { return func(o *rigOpts) { o.start = true } }

// working is dispatching, started, T-1's spec set (medium) and dispatched onto L-01, its worker working on the work
// job, and serve subscribed again after the worker's start (the stream a move or a drop decides on).
func working() rigOpt { return func(o *rigOpts) { o.dispatch, o.start, o.working = true, true, true } }

func newLoopRig(t *testing.T, opts ...rigOpt) *loopRig {
	t.Helper()
	o := rigOpts{check: true}
	for _, opt := range opts {
		opt(&o)
	}
	p := testkit.NewDir(t)
	r := &loopRig{t: t, p: p, f: herdrtest.New(), lanes: &laneTable{},
		clock: &clock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}, inbox: &rigInbox{none: map[string]bool{"pj-l-01": true, "pj-int": true}}}
	r.lane = p.Lane("L-01", "task/T-1")
	edits := []func(map[string]any){testkit.Set("agents.roles.integrator", "")}
	if o.check {
		edits = append(edits, testkit.Set("merge.check", "echo 1 pass"))
	}
	if o.pool {
		edits = append(edits, testkit.Set("lanes.count", 2))
	}
	// The lane's worker is pj-l-01 (L-01's derived name, its pane's label): serve's lane rows come from its log,
	// and T-1's is its submission's.
	p.Config(append(edits, o.edits...)...)
	r.lanes.lanes = []port.Lane{{Name: "L-01", Dir: r.lane, Task: "T-1", Agent: "pj-l-01"}}
	r.f.Group("w1", "proj")
	w := r.f.Agent("w1:p1", "pj-l-01", "idle", r.lane)
	w.Label = "pj-l-01" // labelled by its launch: serve's identity of it (registry.go)
	r.srv = serveHerdr(t, r.f)
	r.srv.Reply("notification.show", map[string]any{"type": "notification_show", "shown": true, "reason": "shown"})
	r.srv.Reply("agent.prompt", prompted)
	r.scratch = app.NewFakeWorkflow("")
	r.scratch.Dir = filepath.Join(p.Base, "proj-wt", "merge")
	if !o.check {
		r.scratch.Checks = "no check"
	}
	r.scratch.AddLane("L-01", r.lane, "task/T-1")
	if o.dispatch {
		r.disp = &dispatchFakes{r: r, on: true, refuse: map[string]string{}, calls: map[string]int{},
			fail: map[string]func() error{}, cut: map[string]string{}, hit: map[string]bool{}}
		r.setLane(func(l *port.Lane) { l.Task, l.Agent, l.State = "", "", "free" })
		r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Transcript = "/s/0.jsonl" })
	}
	if o.pool {
		r.addLanes(port.Lane{Name: "L-02", Dir: p.LaneDir("L-02"), State: "free"})
	}
	if o.start {
		r.start()
	}
	if o.working {
		r.setSpec("s1", "# T-1 — Work\n\nDifficulty: medium\n")
		r.mustDispatch("d1")
		r.waitJob("pj-l-01", "T-1/1/work")
		r.waitFor("working", func() bool { return r.stateOf("T-1") == "working" })
		// The worker's start pushed a refresh, so serve subscribes again: wait for that stream, the one a move or a drop
		// decides by serve's identity.
		r.connectedAgain()
	}
	return r
}

// prompted is the fake herdr's answer to agent.prompt.
var prompted = map[string]any{"type": "agent_prompted", "agent": map[string]any{"name": "x", "pane_id": "w1:p1",
	"agent_status": "working"}}

// Launch is the integrator's start: pj-int (session int-1) appears in the project's workspace and the app sees it (a workspace event
// makes it read a new snapshot) before the start returns, as launch's start returns once the agent is ready.
func (r *loopRig) Launch(ctx context.Context, req port.Launch) (port.Started, error) {
	r.mu.Lock()
	r.launches = append(r.launches, req)
	r.mu.Unlock()
	if req.Agent == "pj-l-01" && r.disp != nil {
		return r.disp.startWorker(ctx, req)
	}
	r.starts.Add(1)
	if req.Agent != "pj-int" {
		return port.Started{}, errors.New("unexpected start of " + req.Agent)
	}
	r.mu.Lock()
	fail := r.onStart
	r.mu.Unlock()
	if fail != nil {
		if err := fail(); err != nil {
			return port.Started{}, err
		}
	}
	if !r.f.Set("w1:p9", func(p *herdrtest.Pane) {
		p.Kind, p.Name, p.Label, p.Status, p.Session = "claude", "pj-int", "pj-int", "idle", "int-1"
	}) {
		a := r.f.Agent("w1:p9", "pj-int", "idle", r.p.Root)
		a.Label, a.Session = "pj-int", "int-1" // launch labels the pane
	}
	seen := false
	poll(r.t, "pj-int in the app's status", 5*time.Second, 5*time.Millisecond, nil, func() bool {
		if ctx.Err() != nil { // the app is shutting down: its main loop is gone, no snapshot will show the pane
			return true
		}
		for _, a := range r.app.Status().Project.Agents {
			seen = seen || a.Name == "pj-int"
		}
		if !seen {
			r.srv.Push("workspace_created", map[string]any{"type": "workspace_created",
				"workspace": map[string]any{"workspace_id": "w1", "label": "proj"}})
		}
		return seen
	})
	switch {
	case seen:
		return port.Started{Line: "STARTED pj-int", Pane: "w1:p9", Kind: "claude", Session: "int-1"}, nil
	case ctx.Err() != nil:
		return port.Started{}, ctx.Err()
	}
	return port.Started{}, errors.New("pj-int never seen")
}

// Stop is the worker's stop: the agent leaves its pane (""; the pane labelled agent) in the fake herdr and the app
// sees it gone (a workspace event makes it read a new snapshot) before the stop returns.
func (r *loopRig) Stop(ctx context.Context, agent, pane string, _ bool) (string, error) {
	r.mu.Lock()
	r.stops = append(r.stops, agent+" "+pane)
	fail := r.onStop
	r.mu.Unlock()
	if fail != nil {
		if err := fail(); err != nil {
			return "", err
		}
	}
	if pane == "" {
		r.f.Locked(func() {
			for _, p := range r.f.Panes {
				if p.Name == agent {
					pane = p.ID
				}
			}
		})
	}
	r.f.Set(pane, func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status = "", "", "" })
	gone := false
	poll(r.t, agent+" gone from the app's status", 5*time.Second, 5*time.Millisecond, nil, func() bool {
		if ctx.Err() != nil { // the app is shutting down: its main loop is gone, no snapshot will show the pane gone
			return true
		}
		gone = true
		for _, a := range r.app.Status().Project.Agents {
			gone = gone && a.Name != agent
		}
		if !gone {
			r.srv.Push("workspace_created", map[string]any{"type": "workspace_created",
				"workspace": map[string]any{"workspace_id": "w1", "label": "proj"}})
		}
		return gone
	})
	switch {
	case gone:
		return "STOPPED " + agent, nil
	case ctx.Err() != nil:
		return "", ctx.Err()
	}
	return "", errors.New(agent + " never seen gone")
}

// stopped is the Agents.Stop calls so far.
func (r *loopRig) stopped() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.stops...)
}

// workerStops is the stops of agents but the integrator (serve stops pj-int after each verdict that leaves no
// job for it).
func (r *loopRig) workerStops() []string {
	var out []string
	for _, s := range r.stopped() {
		if !strings.HasPrefix(s, "pj-int ") {
			out = append(out, s)
		}
	}
	return out
}

// start runs the app (a replay of the log so far) until stop or the test's end, with its socket; it returns after the
// app's first full check.
func (r *loopRig) start() {
	r.t.Helper()
	r.launch()
	r.firstCheck()
	r.serve()
}

// serve opens the running app's socket: in memory in a bubble (mem), on a new socket path outside one (sock).
func (r *loopRig) serve() {
	r.t.Helper()
	if inBubble(r.t) {
		r.mem = app.ServeMem(r.t, r.app)
		return
	}
	r.sock = (&rig{t: r.t, app: r.app}).listen()
}

// answering is whether the app's socket was opened (the rig started once).
func (r *loopRig) answering() bool { return r.sock != "" || r.mem != nil }

// ask sends c to the app's socket as wire.Verdict does: the reply line, or the app's refusal as the error. In a
// bubble it is one request on an in-memory connection, with none of the wire client's retries or deadlines.
func (r *loopRig) ask(c wire.Req) (string, error) {
	r.t.Helper()
	if r.mem != nil {
		return r.mem.Call(r.t, c)
	}
	return wire.Verdict(context.Background(), r.sock, c)
}

// firstCheck waits for the app's first full check (every project and open task sampled once).
func (r *loopRig) firstCheck() {
	r.t.Helper()
	r.waitFor("the first check", func() bool { checks, _ := app.Counts(r.app); return checks >= 1 })
}

// launch runs the app as start does, without waiting for a check and without its socket (herdr may be down).
func (r *loopRig) launch() {
	r.t.Helper()
	switch recs, _ := event.ReadLog(r.p.Root); {
	case r.disp != nil || len(recs) > 0: // the dispatch rig dispatches; a test that wrote the log has its records
	case r.early:
		r.p.SubmitAgo("L-01", 0, report) // T-1 dispatched and its task_done in
	default:
		r.p.Dispatched("L-01") // serve holds T-1 open: its task_done submits it
	}
	panes := herdr.Panes{S: r.srv.Client(), PS: func(context.Context) ([]byte, error) { return []byte("2 1 2\n"), nil }}
	r.out = &syncBuf{}
	tick := 2 * time.Millisecond
	if r.tick != 0 {
		tick = r.tick
	}
	r.app = app.New(app.Options{Root: r.p.Root, Tick: tick, Retry: 5 * time.Millisecond, RetryMax: 40 * time.Millisecond,
		Now: r.clock.Now, Out: r.out, Inboxed: r.inbox.recorded, Exit: r.exit, Binary: r.binary, Follow: tick, Tokens: r.tokens,
		Refresh: func(err error) bool {
			var e *herdr.Error
			return errors.As(err, &e) && e.Code == "refresh"
		}, Loop: func(string) (*app.Loop, error) {
			r.mu.Lock()
			err := r.loopErr
			r.mu.Unlock()
			if err != nil {
				return nil, err
			}
			night := r.night
			if r.nightOf != nil {
				night = r.nightOf()
			}
			fx := &app.Loop{Workflow: app.WorkflowOf(r.scratch, r.review), Agents: r, Typer: panes, Kit: "../..", Nightly: night, Disk: r.disk,
				Inbox: r.inbox, Restart: r.restart, Config: r.config}
			if r.disp != nil {
				r.disp.loop(fx, panes)
			}
			return fx, nil
		}},
		panes, r.lanes)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	go func() {
		defer close(r.done)
		_ = r.app.Serve(ctx)
	}()
	r.t.Cleanup(r.stop)
}

// stop ends the app; Serve returns once its merge loop has stopped.
func (r *loopRig) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	<-r.done
	r.cancel = nil
}

// patience is how long a wait may take: the time left to the package's test deadline less a margin (the serve pass
// runs on a fake clock, so only the poll is real time, and a loaded machine must slow a test, not fail it); 20 s
// when the test has no deadline. At most waitCap: a test whose event never comes fails its own wait after a minute
// instead of hanging until the package's deadline, which the parallel tests beside it share. Under 1 s left it is
// 1 s, so the wait ends in a t.Fatalf before the package panics.
const waitCap = 60 * time.Second

func (r *loopRig) patience() time.Duration {
	if inBubble(r.t) { // the wait is on the bubble's clock, which load cannot move; t.Deadline is not allowed in one
		return waitCap
	}
	d, ok := r.t.Deadline()
	if !ok {
		return 20 * time.Second
	}
	if left := time.Until(d) - 10*time.Second; left > time.Second {
		return min(left, waitCap)
	}
	return time.Second
}

func (r *loopRig) waitFor(what string, ok func() bool) {
	r.t.Helper()
	trail := func() string {
		return fmt.Sprintf("records:\n%s\napp output:\n%s", strings.Join(r.steps(), "\n"), r.out)
	}
	if how := poll(r.t, what, r.patience(), 5*time.Millisecond, trail, ok); how != "" {
		r.t.Fatalf("%s waiting for %s; %s", how, what, trail())
	}
}

// setLane changes L-01's row in the lane table.
func (r *loopRig) setLane(fn func(l *port.Lane)) {
	r.lanes.mu.Lock()
	fn(&r.lanes.lanes[0])
	r.lanes.mu.Unlock()
}

// report is the rig worker's task_done report: every section.
const report = "Summary: done.\nSelf-check: ok.\nSkipped: none.\nOpen questions: none."

// submit is pj-l-01's task_done for T-1 with the full report: it must submit. Before the first start it is in the log
// the start replays (unless the test writes the log itself).
func (r *loopRig) submit() {
	r.t.Helper()
	if !r.answering() {
		r.early = true
		return
	}
	if reply, err := r.taskDone(report); err != nil || reply != "submitted" {
		r.t.Fatalf("task_done: %q, %v; records:\n%s", reply, err, strings.Join(r.steps(), "\n"))
	}
}

// taskDone is pj-l-01's task_done for T-1 with text as its report, under a new call id.
func (r *loopRig) taskDone(text string) (string, error) {
	return r.ask(&wire.TaskDoneReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-l-01", Role: "worker", CallID: fmt.Sprintf("rig-%d", r.callIDs.Add(1))}, Lane: "L-01", Task: "T-1",
		Report: text})
}

// work writes path in the lane (uncommitted: the submit commits it).
func (r *loopRig) work(path, text string) { r.p.Write(filepath.Join(r.lane, path), text) }

// records is proj's event log.
func (r *loopRig) records() []event.Record {
	recs, _ := event.ReadLog(r.p.Root)
	return recs
}

// steps is the log as "<attempt> <kind> <step>[{reason}]" lines.
func (r *loopRig) steps() []string {
	var out []string
	for _, rec := range r.records() {
		out = append(out, strings.Join([]string{string(rune('0' + rec.Attempt)), rec.Kind, rec.String()}, " "))
	}
	return out
}

// has: the log has the record "<attempt> <kind> <step>".
func (r *loopRig) has(step string) bool {
	for _, s := range r.steps() {
		if s == step {
			return true
		}
	}
	return false
}

// count is how many records the log has with this "<attempt> <kind> <step>" line.
func (r *loopRig) count(step string) int {
	n := 0
	for _, s := range r.steps() {
		if s == step {
			n++
		}
	}
	return n
}

func (r *loopRig) waitStep(step string) {
	r.t.Helper()
	r.waitFor("record "+step, func() bool { return r.has(step) })
}

type sent struct{ to, text string }

// prompts is every agent.prompt the fake herdr got, to the agent the rig runs in its target (serve addresses a job
// prompt to the agent's pane by its registry: w1:p1 is pj-l-01's, w1:p9 pj-int's).
func (r *loopRig) prompts() []sent {
	var out []sent
	for _, req := range r.srv.Requests() {
		if req.Method != "agent.prompt" {
			continue
		}
		var p struct{ Target, Text string }
		_ = json.Unmarshal(req.Params, &p)
		out = append(out, sent{rigAgent(p.Target), p.Text})
	}
	return out
}

// rigAgent is the agent the rig runs in pane target (target itself when it is no pane of the rig: a name).
func rigAgent(target string) string {
	return cmp.Or(map[string]string{"w1:p1": "pj-l-01", "w1:p9": "pj-int"}[target], target)
}

// cleared is, for each agent.prompt to agent in order, whether an agent.send_keys ctrl+u to it came since the one
// before (a retry clears the input line before it types).
func (r *loopRig) cleared(agent string) []bool {
	var out []bool
	clear := false
	for _, req := range r.srv.Requests() {
		var p struct {
			Target string
			Keys   []string
		}
		_ = json.Unmarshal(req.Params, &p)
		switch {
		case rigAgent(p.Target) != agent:
		case req.Method == "agent.send_keys" && strings.Join(p.Keys, " ") == "ctrl+u":
			clear = true
		case req.Method == "agent.prompt":
			out, clear = append(out, clear), false
		}
	}
	return out
}

// waitJob is the text of the prompt to agent for job (waited for).
func (r *loopRig) waitJob(agent, job string) string {
	r.t.Helper()
	r.waitWake(agent, job)
	var text string
	r.waitFor("job "+job+" fetched by "+agent, func() bool {
		var ok bool
		text, ok = r.fetch(agent, job)
		return ok
	})
	return text
}

// waitWake waits until job's wake reached agent: typed into its pane, or carried by its start, and the job's prompt
// has its result (job_next could hand the job out from its intent on, before the start that carries it returns). The
// result is the job's own task's: on a lane that runs task after task, an earlier task's job has the same attempt and
// step.
func (r *loopRig) waitWake(agent, job string) {
	r.t.Helper()
	w := "[obor job " + job + "] call the obor job_next tool"
	parts := strings.SplitN(job, "/", 3)
	delivered := func() bool {
		return slices.ContainsFunc(r.records(), func(rec event.Record) bool {
			return rec.Task == parts[0] && strconv.Itoa(rec.Attempt) == parts[1] && rec.Kind == event.KindResult &&
				rec.String() == parts[2]
		})
	}
	r.waitFor("the wake of "+job+" to "+agent, func() bool {
		sent := false
		for _, p := range r.prompts() {
			sent = sent || p.to == agent && p.text == w
		}
		r.mu.Lock()
		for _, l := range r.launches {
			sent = sent || l.Agent == agent && l.Prompt == w
		}
		r.mu.Unlock()
		return sent && delivered()
	})
}

var patchRe = regexp.MustCompile(`Patch hash: ([0-9a-f]{64})`)

// patch is the patch hash a review job names.
func (r *loopRig) patch(text string) string {
	r.t.Helper()
	m := patchRe.FindStringSubmatch(text)
	if m == nil {
		r.t.Fatalf("no patch hash in the review job:\n%s", text)
	}
	return m[1]
}

// verdict is pj-int's verdict on T-1 (no job id: the current job, as the CLI sends it).
func (r *loopRig) verdict(method, callID, patch, note string) (string, error) {
	return r.ask(&wire.VerdictReq{Verb: method,
		Envelope: wire.Envelope{Root: r.p.Root,
			Agent: "pj-int", Role: "integrator", CallID: callID}, Task: "T-1", Patch: patch, Note: note})
}

func (r *loopRig) accept(callID, patch string) {
	r.t.Helper()
	if reply, err := r.verdict(wire.MethodAccept, callID, patch, ""); err != nil || !strings.HasPrefix(reply, "ACCEPT T-1") {
		r.t.Fatalf("accept: %q, %v", reply, err)
	}
}

// mainMerges is how many merges of T-1 the target holds (a publish, or one by hand).
func (r *loopRig) mainMerges() int { return r.scratch.Merges("T-1") }

// conflicting: T-1's lane edits shared.txt and main moves on, and the gate of the lane's merge conflicts in shared.txt
// (scripted: the conflict git finds is internal/checks', TestUnmergedConflictHasNoMismatch).
func (r *loopRig) conflicting() {
	r.work("shared.txt", "lane\n"+sharedLines)
	r.scratch.Move()
	r.scratch.Script("Gate lane", "T-1", port.OutcomeConflict+" shared.txt")
}

// movedConflict: main moves on, and the gate of the lane's merge onto it conflicts (scripted).
func (r *loopRig) movedConflict() {
	r.scratch.Move()
	r.scratch.Script("Gate moved", "T-1", port.OutcomeConflict+" shared.txt")
}

// resolveInScratch is the integrator's resolution of the open conflict, committed in the scratch: its sha.
func (r *loopRig) resolveInScratch() string {
	r.t.Helper()
	sha := r.scratch.Resolve()
	if sha == "" {
		r.t.Fatalf("no conflict open in the scratch: %q", r.steps())
	}
	return sha
}

// published is T-1's candidate the last publish merged.
func (r *loopRig) published() port.Candidate {
	r.t.Helper()
	c, ok := r.scratch.Published("T-1")
	if !ok {
		r.t.Fatalf("T-1 not published: %q", r.steps())
	}
	return c
}

// merged waits for the merge and its after-merge work (its last step, the worker's stop).
func (r *loopRig) merged(attempt int) {
	r.t.Helper()
	r.waitStep(string(rune('0'+attempt)) + " result agent.stop")
}

// noLead: the lead's chat got nothing: every prompt went to the integrator or the worker.
func (r *loopRig) noLead() {
	r.t.Helper()
	for _, p := range r.prompts() {
		if p.to != "pj-int" && p.to != "pj-l-01" {
			r.t.Errorf("a prompt to %s: %q", p.to, p.text)
		}
	}
}

// redLine is the third attempt's red line (its lane test's), as the needs-you names it.
func (r *loopRig) redLine() string {
	var line string
	for _, rec := range r.records() {
		if rec.Kind == event.KindResult && strings.HasPrefix(rec.Step, "test") && rec.Attempt == 3 {
			var g struct{ Line string }
			_ = json.Unmarshal(rec.Data, &g)
			line = g.Line
		}
	}
	return line
}

func countReq(srv *herdrtest.Server, method string) int {
	n := 0
	for _, req := range srv.Requests() {
		if req.Method == method {
			n++
		}
	}
	return n
}

// setSpec stores text as T-1's spec through serve's socket, as the lead (obor spec set).
func (r *loopRig) setSpec(callID, text string) string {
	r.t.Helper()
	reply, err := r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "lead", Role: "lead", CallID: callID}, Task: "T-1", Spec: text})
	if err != nil {
		r.t.Fatalf("spec set: %v", err)
	}
	return reply
}

// TestMain is the hook testkit.New requires: it removes the project template the run copies. Run, not Main:
// internal/app's tests re-exec their own binary carrying OBOR_TEST_DO_CRASH and rewrite goldens under OBOR_UPDATE_GOLDEN,
// and Main's Isolate clears every OBOR_* variable.
func TestMain(m *testing.M) { os.Exit(testkit.Run(m)) }

// watch is a watch request to the rig's serve from the lead (role lead, agent pj-lead).
func (r *loopRig) watch(callID, task string, rm bool) (string, error) {
	return r.ask(&wire.WatchReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", Agent: "pj-lead", CallID: callID}, Task: task, Clear: rm})
}

// watchNote is a watch request carrying the lead's note.
func (r *loopRig) watchNote(callID, task, note string) (string, error) {
	return r.ask(&wire.WatchReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", Agent: "pj-lead", CallID: callID}, Task: task, Note: note})
}

// watchFacts is the watch and unwatch facts of the project's log; watchLines the lead lines a watch recorded.
func (r *loopRig) watchFacts() []event.Record {
	var out []event.Record
	for _, rec := range r.records() {
		if rec.Task == event.WatchTask && rec.Kind == event.KindFact {
			out = append(out, rec)
		}
	}
	return out
}

func (r *loopRig) watchLines() []string {
	var out []string
	for _, rec := range r.records() {
		var m event.Message
		if rec.Task == event.MsgTask && rec.Step == event.StepMessage && json.Unmarshal(rec.Data, &m) == nil && m.Of.Kind == "watch" {
			out = append(out, m.Line)
		}
	}
	return out
}

// mergedT1 runs T-1 from its task_done to its merge (the integrator accepts the review job's patch).
func (r *loopRig) mergedT1() {
	r.t.Helper()
	r.submit()
	r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
	r.merged(1)
}

// seedFailed writes, to the stopped app's log, T-1's dispatch on L-01 and each of tasks as an open attempt (submitted
// on its own lane, its submit refused: a needs-you, merged once land puts it on main).
func (r *loopRig) seedFailed(tasks ...string) {
	r.t.Helper()
	r.p.Dispatched("L-01")
	var recs []event.Record
	refused, _ := json.Marshal(map[string]any{"refused": "no commit"})
	for i, task := range tasks {
		sub, _ := json.Marshal(map[string]any{"lane": fmt.Sprintf("O-%02d", i+2), "agent": fmt.Sprintf("pj-o%d", i+2)})
		recs = append(recs, event.Record{Task: task, Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact, Data: sub},
			event.Record{Task: task, Attempt: 1, Step: event.StepSubmit, Kind: event.KindIntent, Class: event.ClassGit},
			event.Record{Task: task, Attempt: 1, Step: event.StepSubmit, Kind: event.KindResult, Data: refused})
	}
	if err := app.WriteLog(r.p.Root, time.Now(), recs...); err != nil {
		r.t.Fatal(err)
	}
}

// land is an obor merge of task on main outside the app: the loop records merged{outside}.
func (r *loopRig) land(task string) {
	r.t.Helper()
	r.scratch.Land(task) // obor merge by hand
	r.waitFor(task+" merged", func() bool { return r.taskStep(task, "fact merged") > 0 })
}

// dropSettled is the lead's drop of task once the failing step seedFailed left running has settled: serve refuses a
// drop while a step runs ("retry when it settles"), and under load the step outlasts the test's first call.
func (r *loopRig) dropSettled(task, note string) {
	r.t.Helper()
	var reply string
	var err error
	r.waitFor("the drop of "+task+" accepted", func() bool {
		reply, err = r.ask(&wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
			Role: "lead", CallID: "x1"}, Task: task, Why: note})
		return err == nil || !strings.Contains(err.Error(), "retry when it settles")
	})
	if err != nil || reply != "DROPPED "+task {
		r.t.Fatalf("drop: %q, %v", reply, err)
	}
}

// rigInbox is the rig's Poster: the lead's lines posted, the agents with no inbox record, and how a post
// fails (refused: before a byte, as a dead socket's dial; partial: after one).
type rigInbox struct {
	mu       sync.Mutex
	posts    []string        // "<agent>: <line>" per post that went out (began)
	attempts []string        // every post tried
	none     map[string]bool // agents with no record
	refused  bool
	partial  bool
	once     bool // refused and partial end after the next post (failOnce)
	cutting  bool // the next post waits for serve's ctx and fails with its error (cut)
}

func (in *rigInbox) Post(ctx context.Context, _, agent, line string) (bool, error) {
	in.mu.Lock()
	in.attempts = append(in.attempts, agent+": "+line)
	cut := in.cutting
	if cut {
		in.cutting = false
	}
	in.mu.Unlock()
	if cut { // a post serve's own stop cut before a byte went out, as its inbox's ctx ends
		<-ctx.Done()
		return false, ctx.Err()
	}
	// A post on an ended ctx fails before a byte goes out, as the real poster's dial does: a retry the stopping serve
	// makes after a cut must not count as posted (a red TestShutdownDeliveryNotRecorded under load: posted twice).
	if err := ctx.Err(); err != nil {
		return false, err
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	switch {
	case in.none[agent]:
		return false, errors.New("no session recorded for " + agent)
	case in.refused:
		if in.once {
			in.refused = false
		}
		return false, errors.New("dial unix: connection refused")
	}
	for _, row := range strings.Split(line, "\n") { // a batched post counts as one entry per row
		in.posts = append(in.posts, agent+": "+row)
	}
	if in.partial {
		if in.once {
			in.partial = false
		}
		return true, errors.New("broken pipe")
	}
	return true, nil
}

// recorded is Options.Inboxed: agent has an inbox record (naming no session).
func (in *rigInbox) recorded(agent string) (string, bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return "", !in.none[agent]
}

func (in *rigInbox) unrecord(agent string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.none == nil {
		in.none = map[string]bool{}
	}
	in.none[agent] = true
}

func (in *rigInbox) record(agent string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	delete(in.none, agent)
}

// fail makes the next posts fail: partial false before a byte went out, true after one; ok ends both.
func (in *rigInbox) fail(partial bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.refused, in.partial, in.once = !partial, partial, false
}

// failOnce makes the next post alone fail (partial false before a byte went out, true after one): the posts after it
// go out. A test that means to see one refusal before the failure rule counts three needs no race with the merge
// loop's passes over the post.
func (in *rigInbox) failOnce(partial bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.refused, in.partial, in.once = !partial, partial, true
}

func (in *rigInbox) ok() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.refused, in.partial, in.once = false, false, false
}

// cut makes the next post alone wait for serve's ctx and fail with its error before a byte went out: a delivery serve's
// own stop cut.
func (in *rigInbox) cut(on bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.cutting = on
}

// count is how many posts of line to agent went out; tries how many were tried.
func (in *rigInbox) count(agent, line string) int { return in.n(&in.posts, agent+": "+line) }

func (in *rigInbox) tries(agent, line string) int { return in.n(&in.attempts, agent+": "+line) }

func (in *rigInbox) n(list *[]string, s string) int {
	in.mu.Lock()
	defer in.mu.Unlock()
	n := 0
	for _, x := range *list {
		if x == s {
			n++
		}
	}
	return n
}

// lineResults is the results of the project's line deliveries, in order (their data: sent, sent: <error>).
func (r *loopRig) lineResults() []string {
	var out []string
	for _, rec := range r.records() {
		if rec.Task == event.MsgTask && rec.Kind == event.KindResult && strings.HasPrefix(rec.Step, "deliver.") {
			var v string
			_ = json.Unmarshal(rec.Data, &v)
			out = append(out, v)
		}
	}
	return out
}

// lineRig is the loop rig with the lead pj-lead (w1:p5) and the reviewer pj-rev (w1:p6) in the project's workspace,
// each in status lead / rev.
func lineRig(t *testing.T, lead, rev string) *loopRig {
	r := newLoopRig(t, noCheck())
	for _, a := range []*herdrtest.Pane{r.f.Agent("w1:p5", "pj-lead", lead, r.p.Root), r.f.Agent("w1:p6", "pj-rev", rev, r.p.Root)} {
		a.Label = a.Name // labelled by its launch
	}
	return r
}

// agentIs sets the status of the agent in pane and waits until the app sees it.
func (r *loopRig) agentIs(pane, name, status string) {
	r.t.Helper()
	r.f.Set(pane, func(p *herdrtest.Pane) { p.Status = status })
	r.waitFor(name+" "+status, func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == name && a.Status == status {
				return true
			}
		}
		r.push()
		return false
	})
}

// line is a send or brief request to the rig's serve from the lead (role lead, agent pj-lead).
func (r *loopRig) line(callID string, c wire.Req) (string, error) {
	e := c.Env()
	e.Root, e.CallID = r.p.Root, callID
	if e.Role == "" {
		e.Role, e.Agent = "lead", "pj-lead"
	}
	return r.ask(c)
}

// typedTo is the prompts the fake herdr got for agent (its name, or its pane: typed lines address the pane id, the
// line rig's role panes w1:p5 and w1:p6 count as pj-lead's and pj-rev's) with exactly text.
func (r *loopRig) typedTo(agent, text string) int {
	n := 0
	for _, p := range r.prompts() {
		if (p.to == agent || map[string]string{"w1:p5": "pj-lead", "w1:p6": "pj-rev"}[p.to] == agent) && p.text == text {
			n++
		}
	}
	return n
}

// msgFacts is the facts in force of the project's lines.
func (r *loopRig) msgFacts() []event.Record {
	var out []event.Record
	for _, f := range r.app.Status().Project.Facts {
		if f.Task == event.MsgTask {
			out = append(out, f)
		}
	}
	return out
}

// supersededLine is one undelivered brief line a newer brief ended: its seq and the newer brief's.
type supersededLine struct {
	Seq int64
	By  int64
}

// supersededLines is the brief lines the project's briefed facts in the log supersede, in order.
func (r *loopRig) supersededLines() []supersededLine {
	var out []supersededLine
	for _, rec := range r.records() {
		if rec.Task == event.MsgTask && rec.Kind == event.KindFact && rec.Step == event.StepBriefed {
			var m event.Message
			_ = json.Unmarshal(rec.Data, &m)
			for _, seq := range m.Supersedes {
				out = append(out, supersededLine{Seq: seq, By: rec.Seq})
			}
		}
	}
	return out
}

// waitBriefDelivered waits until the one brief line is delivered: its result in the log and folded (nothing waiting).
// The brief's age counts from that result's time, so the clock moves only after it; the fake inbox's or pane's count
// of the line is earlier than the result record.
func (r *loopRig) waitBriefDelivered() {
	r.t.Helper()
	r.waitFor("the brief's delivery recorded", func() bool { return len(r.lineResults()) >= 1 && len(r.waiting()) == 0 })
}

// waiting is the lines serve has not delivered yet (obor status).
func (r *loopRig) waiting() []wire.MessageStatus { return r.app.Status().Project.Messages }

// sentLine is the line obor send posts in the target project for text from the project from: the sender, the text, and
// the reply in one command.
func sentLine(from, text string) string {
	return "[obor " + from + "] " + text + ` (reply: obor send ` + from + ` "…")`
}

// obor dispatch T-xx --model <ref> on a task its worker works on moves it: serve keeps the lane, branch and
// spec and records the redo dispatch of the next attempt with the new model; the merge loop stops the worker by its
// identity (worker.stop, stopstep_test.go), then the redo's agent.start launches that model with the new attempt's work job. The rig: newLoopRig(t, working()) (T-1 working on
// L-01 as sonnet, its worker pj-l-01 in w1:p1).

// moveTo is the lead's obor dispatch T-1 --model types.
func (r *loopRig) moveTo(callID string, types ...string) (string, error) {
	return r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
		CallID: callID}, Task: "T-1", Types: types})
}

// workerLaunches is the Agents.Launch calls of pj-l-01, in order.
func (r *loopRig) workerLaunches() []port.Launch {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []port.Launch
	for _, l := range r.launches {
		if l.Agent == "pj-l-01" {
			out = append(out, l)
		}
	}
	return out
}

// dispatchFacts is how many dispatch facts the log has (a move's, any attempt).
func (r *loopRig) dispatchFacts() int {
	n := 0
	for _, rec := range r.records() {
		if rec.Kind == event.KindFact && rec.Step == event.StepDispatched {
			n++
		}
	}
	return n
}

// advance moves the fake clock on by d (0: not at all), then waits for two more passes of serve's loop (wake).
func (r *loopRig) advance(d time.Duration) {
	r.t.Helper()
	n := app.StreamPasses(r.app) // passes in a resubscribe's gap skip the project
	r.clock.Add(d)
	r.waitFor("two more passes", func() bool { return app.StreamPasses(r.app) >= n+2 })
}

// needsLineFacts is the message facts of the project's log that announce a needs-you.
func (r *loopRig) needsLineFacts() []event.Record {
	var out []event.Record
	for _, rec := range r.records() {
		if rec.Task == event.MsgTask && rec.Kind == event.KindFact && rec.Step == event.StepMessage {
			var m event.Message
			if json.Unmarshal(rec.Data, &m) == nil && m.Of.Kind == "needs" {
				out = append(out, rec)
			}
		}
	}
	return out
}

// againLines is the needs-you lines that repeat fact seq: the lines ending in " (again, <age>)".
func (r *loopRig) againLines(seq int64) []string {
	var out []string
	for _, rec := range r.needsLineFacts() {
		var m event.Message
		if json.Unmarshal(rec.Data, &m) == nil && app.NeedsSeq(m.Of) == seq && strings.Contains(m.Line, " (again, ") {
			out = append(out, m.Line)
		}
	}
	return out
}

// refusedRig is the dispatch rig with T-1's lane preparation refused: T-1 has the needs-you "lane: lane_setup in L-01:
// rc 1: boom". It returns that fact's line to the lead, as serve's answers name the commands that answer it.
func refusedRig(t *testing.T) (*loopRig, string) {
	t.Helper()
	r := newLoopRig(t, dispatching())
	r.disp.set(func(d *dispatchFakes) {
		d.fail["prepare"] = func() error { return port.Refused{Line: "lane_setup in L-01: rc 1: boom"} }
	})
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 fact needs-you{lane: lane_setup in L-01: rc 1: boom}")
	r.waitFor("needs-you fact folded into status", func() bool {
		for _, f := range r.app.Status().Project.Facts {
			if f.Step == event.StepNeedsYou {
				return true
			}
		}
		return false
	})
	var fact event.Record
	for _, f := range r.app.Status().Project.Facts {
		if f.Step == event.StepNeedsYou {
			fact = f
		}
	}
	if fact.Seq == 0 {
		t.Fatalf("precondition: no needs-you in force: %+v", r.app.Status().Project.Facts)
	}
	ans := r.answerOf(fact)
	if ans == "" {
		t.Fatal("precondition: the needs-you has no answer commands")
	}
	return r, "[obor] needs-you T-1 (L-01): lane: lane_setup in L-01: rc 1: boom \u2192 " + ans
}

var (
	green = app.NightlyOut{SHA: "abc1234def", Verdict: "GREEN", Line: "NIGHTLY GREEN abc1234"}
	red   = app.NightlyOut{SHA: "abc1234def", Verdict: "RED", Line: "NIGHTLY RED abc1234"}
)

// startWithAgents starts the app as start does, with agents as its loop's Agents: a real launcher, so the start's
// args reach the fake herdr.
func (r *loopRig) startWithAgents(agents port.Agents) {
	r.t.Helper()
	panes := herdr.Panes{S: r.srv.Client(), PS: func(context.Context) ([]byte, error) { return []byte("2 1 2\n"), nil }}
	r.out = &syncBuf{}
	r.app = app.New(app.Options{Root: r.p.Root, Tick: 20 * time.Millisecond, Retry: 5 * time.Millisecond,
		Now: r.clock.Now, Out: r.out, Refresh: func(err error) bool {
			var e *herdr.Error
			return errors.As(err, &e) && e.Code == "refresh"
		}, Loop: func(string) (*app.Loop, error) {
			fx := &app.Loop{Workflow: app.WorkflowOf(r.scratch), Agents: agents, Typer: panes}
			r.disp.loop(fx, panes)
			return fx, nil
		}},
		panes, r.lanes)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	go func() {
		defer close(r.done)
		_ = r.app.Serve(ctx)
	}()
	r.t.Cleanup(r.stop)
	r.waitFor("the first check", func() bool { checks, _ := app.Counts(r.app); return checks >= 1 })
	r.serve()
}

// pause is a pause request to the rig's serve (role "": the owner).
func (r *loopRig) pause(role, callID string, c wire.PauseReq) (string, error) {
	c.Root, c.Role, c.CallID = r.p.Root, role, callID
	if role != "" {
		c.Agent = "pj-" + role
	}
	return r.ask(&c)
}

// The prompts table: the text of every job, brief and start prompt serve sent is recorded right after
// its fact, by the fact's seq; every distinct text a job's fetch returned gets its own row (the job's newest), and a
// failed write never fails the job.

// promptRows is the project's prompts for q, newest first.
func (r *loopRig) promptRows(t *testing.T, q store.PromptQuery) []store.PromptRow {
	t.Helper()
	rows, err := store.ReadPromptsAt(r.p.Root, q, 100)
	if err != nil {
		t.Fatalf("ReadPromptsAt %+v: %v", q, err)
	}
	return rows
}

// deliveredSeq is the seq of the log's delivered fact of job.
func (r *loopRig) deliveredSeq(t *testing.T, job string) int64 {
	t.Helper()
	for _, rec := range r.records() {
		if rec.Kind == event.KindFact && rec.Step == event.StepDelivered && rec.Fact().Job == job {
			return rec.Seq
		}
	}
	t.Fatalf("no delivered fact of %s", job)
	return 0
}

// briefedSeq is the seq of the nth briefed fact of pj-rev.
func (r *loopRig) briefedSeq(t *testing.T, n int) int64 {
	t.Helper()
	seen := 0
	for _, rec := range r.records() {
		if rec.Task == event.MsgTask && rec.Kind == event.KindFact && rec.Step == event.StepBriefed {
			if seen++; seen == n {
				return rec.Seq
			}
		}
	}
	t.Fatalf("no briefed fact %d", n)
	return 0
}

// specTexts are the T-1 specs a test sets: a new rev says something else.
const (
	specOne = "# T-1 — Test\n\nDifficulty: medium\n\nDo the thing.\n"
	specTwo = "# T-1 — Test\n\nDifficulty: medium\n\nDo the thing, again.\n"
)

// spec sets spec as T-1's spec, by the lead with call id.
func (r *loopRig) spec(callID, text string) string {
	r.t.Helper()
	reply, err := r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-lead", Role: "lead", CallID: callID}, Task: "T-1", Spec: text})
	if err != nil {
		r.t.Fatalf("spec set %s: %v", callID, err)
	}
	return reply
}

// Recovery: obor serve dies at any point of a merge-loop step, before its effect or after it with the
// result lost, and the restart takes the task on from there with the in-memory Scratch (app.FakeWorkflow: an effect
// that ran is in it, as it would be in git).

// crashAt makes the app's log refuse the record kind/step and every record after it, and ends the app there (the
// process died: nothing after it runs); the flag says it happened.
func (r *loopRig) crashAt(kind, step string) *atomic.Bool {
	var crashed atomic.Bool
	cancel := r.cancel
	app.Refuse(r.app, r.p.Root, func(rec event.Record) error {
		if crashed.Load() || rec.Kind == kind && rec.Step == step {
			crashed.Store(true)
			cancel()
			return errors.New("crash")
		}
		return nil
	})
	return &crashed
}

// appendIntent writes the intent of step for T-1's attempt 1 to the log of a stopped app: the crash came after the
// intent, before its effect.
func (r *loopRig) appendIntent(step, class string) {
	r.t.Helper()
	if err := app.WriteLog(r.p.Root, time.Now(),
		event.Record{Task: "T-1", Attempt: 1, Step: step, Kind: event.KindIntent, Class: class}); err != nil {
		r.t.Fatal(err)
	}
}

var jobRe = regexp.MustCompile(`^\[obor job ([^\]]+)\] `)

// sent: the prompt of job (<task>/<attempt>/<step>) has its result.
// woken is the jobs whose wake reached agent (typed, or a start's first prompt), each once, in order.
func (r *loopRig) woken(agent string) []string {
	var texts, out []string
	for _, p := range r.prompts() {
		if p.to == agent {
			texts = append(texts, p.text)
		}
	}
	r.mu.Lock()
	for _, l := range r.launches {
		if l.Agent == agent {
			texts = append(texts, l.Prompt+" ")
		}
	}
	r.mu.Unlock()
	seen := map[string]bool{}
	for _, text := range texts {
		if m := jobRe.FindStringSubmatch(text); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// fetch is agent's job_next: the text of job with its tag when job_next hands out job (false: another job, or none).
func (r *loopRig) fetch(agent, job string) (string, bool) {
	role, lane := "worker", "L-01"
	if agent == "pj-int" {
		role, lane = "integrator", ""
	}
	raw, err := r.ask(&wire.JobNextReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: agent, Role: role, CallID: fmt.Sprintf("rig-%d", r.callIDs.Add(1))}, Lane: lane})
	var jr wire.JobReply
	if err != nil || json.Unmarshal([]byte(raw), &jr) != nil || jr.Job != job {
		return "", false
	}
	return "[obor job " + job + "] " + jr.Text, true
}

func (r *loopRig) sent(job string) bool {
	for _, rec := range r.records() {
		if rec.Kind == event.KindResult && app.JobID(event.Key{Task: rec.Task, Attempt: rec.Attempt, Step: rec.Step}) == job {
			return true
		}
	}
	return false
}

// foreign is lane L-02 on task T-20 with its worker pj-l-02 in the project's workspace: no step of T-1 may touch it.
func (r *loopRig) foreign() string {
	r.t.Helper()
	lane := r.p.Lane("L-02", "task/T-20")
	r.scratch.AddLane("L-02", lane, "task/T-20")
	r.p.State("L-02", map[string]any{"lane": "L-02", "task": "T-20", "branch": "task/T-20", "agent": "pj-l-02",
		"base": "main", "dispatched_at": 1})
	w := r.f.Agent("w1:p2", "pj-l-02", "idle", lane)
	w.Label = "pj-l-02" // labelled by its launch
	return lane
}

const sharedLines = "2\n3\n4\n5\n6\n7\n8\n9\n10\n"

// open is the steps of the log's intents that have no result.
func (r *loopRig) open() []string {
	pending := map[string]bool{}
	var order []string
	for _, rec := range r.records() {
		key := fmt.Sprint(rec.Task, " ", rec.Attempt, " ", rec.Step)
		switch rec.Kind {
		case event.KindIntent:
			pending[key] = true
			order = append(order, key)
		case event.KindResult:
			delete(pending, key)
		}
	}
	var out []string
	for _, k := range order {
		if pending[k] {
			out = append(out, k)
		}
	}
	return out
}

// result is the data of T-1's attempt-1 result of step.
func (r *loopRig) result(step string) string {
	for _, rec := range r.records() {
		if rec.Kind == event.KindResult && rec.Attempt == 1 && rec.Step == step {
			return string(rec.Data)
		}
	}
	return ""
}

// settled waits until serve has subscribed again after the snapshot count before and has run some checks on that
// stream: a restore it started on the reconnect has been called by then.
func (r *rig) settled(before int) {
	r.t.Helper()
	r.waitFor("a new snapshot", func() bool { return r.count("session.snapshot") > before })
	r.waitConnected()
	checks, _ := app.Counts(r.app)
	r.waitFor("checks on the new stream", func() bool { c, _ := app.Counts(r.app); return c >= checks+3 })
}

// A failure or needs-you an error would record is not recorded when the error is serve's own cancellation (its
// ctx ended: a restart, a stop). serve logged it as "shutting down: <what> not recorded" instead, and the intent stays
// pending, so the next serve's replay runs it as after a crash.

// msgFailed is how many delivery failures of the project's lines are in the log.
func (r *loopRig) msgFailed() int {
	n := 0
	for _, rec := range r.records() {
		if rec.Task == event.MsgTask && rec.Kind == event.KindFact && rec.Step == event.StepFailed {
			n++
		}
	}
	return n
}

// setSpecTask is the lead's obor spec set of task with note, text as its spec.
func (r *loopRig) setSpecTask(callID, task, text, note string) (string, error) {
	return r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "lead", Role: "lead", CallID: callID}, Task: task, Spec: text, Changed: note})
}

// setSpecT is setSpecTask of T-1, fatal on an error.
func (r *loopRig) setSpecT(callID, text, note string) string {
	r.t.Helper()
	reply, err := r.setSpecTask(callID, "T-1", text, note)
	if err != nil {
		r.t.Fatalf("spec set: %v", err)
	}
	return reply
}

// started is T-1's TaskStatus.Started (0: none, or no such task).
func (r *loopRig) started() int64 {
	for _, t := range r.app.Status().Project.Tasks {
		if t.Task == "T-1" {
			return t.Started
		}
	}
	return 0
}

// same fails t when got is not want.
func same[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// stoppedCall is the StopFailure hook's request for agent (role, lane) with call id: a rate_limit, 429.
func (r *loopRig) stoppedCall(agent, role, lane, callID string) (string, error) {
	return r.stoppedCallWith(agent, role, lane, callID, "rate_limit", "429 Too Many Requests")
}

func (r *loopRig) stoppedCallWith(agent, role, lane, callID, errText, details string) (string, error) {
	return r.line(callID, &wire.StoppedReq{Envelope: wire.Envelope{Agent: agent, Role: role}, Lane: lane,
		Error: errText, Details: details})
}

// turnCall is a worker's UserPromptSubmit or Stop hook telling serve its next turn began or ended.
func (r *loopRig) turnCall() (string, error) {
	return r.line(wire.NewCallID(), &wire.TurnReq{Envelope: wire.Envelope{Agent: "pj-l-01", Role: "worker"},
		Lane: "L-01"})
}

// stopReason is the worker's stop the rig's stoppedCall records.
const stopReason = "pj-l-01 stopped: rate_limit (429 Too Many Requests)"

// stopsInForce is how many needs-you{stopReason} of T-1 are in force.
func (r *loopRig) stopsInForce() int {
	n := 0
	for _, f := range r.app.Status().Project.Facts {
		if f.Task == "T-1" && f.Step == event.StepNeedsYou && f.Fact().Reason == stopReason {
			n++
		}
	}
	return n
}

// posted is the posts that went out ("<agent>: <line>"), in order.
func (in *rigInbox) posted() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string(nil), in.posts...)
}

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

// serve opens the running app's socket for ask: in memory in a bubble (mem), on a new socket path outside one
// (sock).
func (r *rig) serve() {
	r.t.Helper()
	if inBubble(r.t) {
		r.mem = app.ServeMem(r.t, r.app)
		return
	}
	r.sock = r.listen()
}

// ask sends c to the app's socket (serve opened it) as wire.Verdict does; see loopRig.ask.
func (r *rig) ask(c wire.Req) (string, error) {
	r.t.Helper()
	if r.mem != nil {
		return r.mem.Call(r.t, c)
	}
	return wire.Verdict(context.Background(), r.sock, c)
}

// listen answers the app socket of the running app on a new socket path. Not in a bubble (a unix socket's accept
// stalls its clock): serve and ask are the pair that follows the bubble.
func (r *rig) listen() string {
	r.t.Helper()
	if inBubble(r.t) {
		r.t.Fatal("listen opens a unix socket: in a bubble use serve and ask")
	}
	dir, err := os.MkdirTemp("", "app") // short: a unix socket path is limited
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "serve.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { ln.Close() })
	go r.app.Listen(ln)
	return sock
}

// call is an integrator's verdict verb on T-1 from pj-int; edit changes it.
func (r *rig) call(verb, id string, edit ...func(*wire.VerdictReq)) *wire.VerdictReq {
	c := &wire.VerdictReq{Verb: verb, Envelope: wire.Envelope{Root: r.root, Agent: "pj-int", Role: "integrator",
		CallID: id},
		Task: "T-1", Job: "T-1/2/review", Patch: "p1", Note: "why"}
	for _, e := range edit {
		e(c)
	}
	return c
}

// record is the log's record of kind and step (zero when none).
func (r *rig) record(kind, step string) event.Record {
	for _, rec := range r.records() {
		if rec.Kind == kind && rec.Step == step {
			return rec
		}
	}
	return event.Record{}
}

// verdicts is the log's verdict records as "kind step".
func (r *rig) verdicts() []string {
	var out []string
	for _, rec := range r.records() {
		if strings.Contains(rec.Step, ".c") {
			out = append(out, rec.Kind+" "+rec.Step)
		}
	}
	return out
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
