package app_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/store"
	"obor/internal/testkit"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// The merge loop at the app boundary: the rig's terminal (snapshot, job prompts, notifications), the package's
// in-memory Scratch (app.FakeWorkflow: no git), a scripted lane table, and an integrator start that puts pj-int into
// the terminal.

// loopRig is project proj (a plain dir: its config written, no git) with lane L-01 on task/T-1 (a plain dir the fake
// Scratch snapshots; its worker pj-l-01 idle in it; without the dispatch option T-1's dispatch fact in the log at the
// first start), roles.integrator (pj-int, not running until started) and the app not started yet (unless an option
// starts it). merge.check is configured ("echo 1 pass": its outcome is the fake's, Script).
type loopRig struct {
	*term
	t       *testing.T
	p       *testkit.Project
	lane    string
	lanes   *laneTable
	scratch *app.FakeWorkflow
	starts  atomic.Int32
	callIDs atomic.Int32 // the rig's own task_done and job_next call ids
	early   bool         // submit before the first start: T-1's task_done is in the log the start replays

	// agents is the loop's Agents: it records every start and stop, which act on the rig (startAgent, stopAgent).
	agents *app.FakeAgents

	mu sync.Mutex
	// places: the Loop's PlaceLane calls, in order; placeErr: what PlaceLane fails with instead of placing (nil: it
	// places). placeErr is set before start.
	places   []placedPane
	placeErr error
	// onStart, onStop: an error a start or stop gives instead of acting (nil: none; under mu).
	onStart, onStop func() error
	loopErr         error // what the Loop adapters' constructor fails with (nil: none; under mu)

	disp *dispatch     // the dispatch's adapters (nil: none, the loop has no dispatch)
	tick time.Duration // the watchdogs' tick, each a merge-loop pass (0: rigTick); set before start
	// stepRetry, stepRetryMax: the wait before an uncounted step's next try and its cap (0: 5 ms, 40 ms); set
	// before start
	stepRetry, stepRetryMax time.Duration
	driveWait               time.Duration // how long a new drive's reply waits for its lane's prepare (0: serve's minute); set before start
	night                   *app.Nightly  // the loop's Nightly (nil: none; off is At ""); set before start
	// nightOf, when set, is the loop's Nightly built on each pass (serve's: from the config as it is); set
	// before start
	nightOf func() *app.Nightly
	config  app.ConfigFile // the loop's config_set writer (nil: none); set before start
	review  port.Review    // the loop's review git (nil: the fake workflow's own); set before start
	disk    *app.Disk      // the loop's Disk (nil: none); set before start
	// restart is the loop's Restart, serve's self-restart (nil: none); set before start
	restart func(ctx context.Context) error
	// binary is Options.Binary, a new obor binary there (nil: not watched); set before start
	binary func() (app.NewBinary, bool)
	// exit is Options.Exit, the socket's exit request (nil: none); set before start
	exit func()
	// tokens is Options.Tokens, a transcript's context size (nil: the file read); set before start
	tokens func(transcript string) (int, bool)
	// inbox is the loop's Inbox: the lead's lines posted, kept across restarts. The worker pj-l-01 and the
	// integrator pj-int start with no inbox record, so their job wakes are typed; a test of posted
	// wakes records them (inbox.record).
	inbox *rigInbox

	home string    // Options.Home: the agents' home, under the test's temp dir (a transcript a test needs goes here)
	cfg  rigConfig // the config the Loop factory hands each pass, read again when the files change

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
	r := &loopRig{term: newTerm(map[string]string{"w1": "proj"}), t: t, p: p, lanes: &laneTable{}, home: t.TempDir(),
		clock: &clock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}, inbox: &rigInbox{none: map[string]bool{"pj-l-01": true, "pj-int": true}}}
	r.agents = &app.FakeAgents{OnLaunch: r.startAgent, OnStop: r.stopAgent}
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
	r.addPane(agentPane("w1:p1", "pj-l-01", r.lane)) // labelled by its launch: serve's identity of it (registry.go)
	head := ""
	if o.dispatch {
		head = "target1" // the target a dispatch's prepare reads
	}
	r.scratch = app.NewFakeWorkflow(head)
	r.scratch.Dir = filepath.Join(p.Base, "proj-wt", "merge")
	if !o.check {
		r.scratch.Checks = "no check"
	}
	r.scratch.AddLane("L-01", r.lane, "task/T-1")
	if o.dispatch {
		r.disp = &dispatch{r: r, on: true, refuse: map[string]string{}, calls: map[string]int{},
			fail: map[string]func() error{}, cut: map[string]string{}, hit: map[string]bool{}}
		r.setLane(func(l *port.Lane) { l.Task, l.Agent, l.State = "", "", "free" })
		r.setPane("w1:p1", func(p *port.Pane) { p.Transcript = "/s/0.jsonl" })
		// The lane's row: what the workflow's prepare writes in the lane table.
		r.scratch.OnPrepare(func(in port.PrepareInput) {
			r.setLane(func(l *port.Lane) { l.Task, l.State = in.Task, "working" })
		})
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

// startAgent (the agents' OnLaunch) is the integrator's start: pj-int (session int-1) appears in the project's group and
// the app sees it (a refresh makes it read a new snapshot) before the start returns, as launch's start returns once the
// agent is ready.
func (r *loopRig) startAgent(ctx context.Context, req port.Launch) (port.Started, error) {
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
	int1 := agentPane("w1:p9", "pj-int", r.p.Root) // launch labels the pane
	int1.Session = "int-1"
	r.addPane(int1)
	seen := false
	poll(r.t, "pj-int in the app's status", 5*time.Second, 5*time.Millisecond, nil, func() bool {
		if ctx.Err() != nil { // the app is shutting down: its main loop is gone, no snapshot will show the pane
			return true
		}
		for _, a := range r.app.Status().Project.Agents {
			seen = seen || a.Name == "pj-int"
		}
		if !seen {
			r.refresh()
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

// stopAgent (the agents' OnStop) is the worker's stop: the agent leaves its pane (""; the pane its agent's name is in) in
// the terminal and the app sees it gone (a refresh makes it read a new snapshot) before the stop returns.
func (r *loopRig) stopAgent(ctx context.Context, agent, pane string, _ bool) error {
	r.mu.Lock()
	fail := r.onStop
	r.mu.Unlock()
	if fail != nil {
		if err := fail(); err != nil {
			return err
		}
	}
	if pane == "" {
		for _, p := range r.panes() {
			if r.nameOf(p.ID) == agent {
				pane = p.ID
			}
		}
	}
	r.setPane(pane, func(p *port.Pane) { p.Agent, p.Status = "", "" })
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
			r.refresh()
		}
		return gone
	})
	switch {
	case gone:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	}
	return errors.New(agent + " never seen gone")
}

// stopped is the Agents.Stop calls so far: "<agent> <pane>".
func (r *loopRig) stopped() []string {
	var out []string
	for _, s := range r.agents.Stops() {
		out = append(out, s[:strings.LastIndex(s, " force=")])
	}
	return out
}

// placedPane is one Loop.PlaceLane call: the lane, and the pane it answered.
type placedPane struct{ lane, pane string }

// placeLane is the Loop's PlaceLane on the rig: a shell pane in the lane, labelled the lane's worker (obor setup pane
// <LANE>'s placement is setup's, which the app does not run), and its created event, so the app starts the worker
// there. placeErr fails it instead.
func (r *loopRig) placeLane(_ context.Context, lane string) (string, error) {
	r.mu.Lock()
	err := r.placeErr
	id := fmt.Sprintf("w1:p%d", 70+len(r.places)+1)
	if err == nil {
		r.places = append(r.places, placedPane{lane: lane, pane: id})
	}
	r.mu.Unlock()
	if err != nil {
		return "", err
	}
	r.addPane(port.Pane{ID: id, Label: "pj-" + strings.ToLower(lane), Cwd: r.p.LaneDir(lane)})
	r.announce(id)
	return id, nil
}

// placements is the Loop's PlaceLane calls so far.
func (r *loopRig) placements() []placedPane {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]placedPane(nil), r.places...)
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

// launch runs the app as start does, without waiting for a check and without its socket (the terminal may be down).
func (r *loopRig) launch() {
	r.t.Helper()
	switch recs := r.records(); {
	case r.disp != nil || len(recs) > 0: // the dispatch rig dispatches; a test that wrote the log has its records
	case r.early:
		r.p.SubmitAgo("L-01", 0, report) // T-1 dispatched and its task_done in
	default:
		r.p.Dispatched("L-01") // serve holds T-1 open: its task_done submits it
	}
	at, c := r.adapter(), r.tt
	r.out = &syncBuf{}
	tick := rigTick(r.t)
	if r.tick != 0 {
		tick = r.tick
	}
	retry, retryMax := 5*time.Millisecond, 40*time.Millisecond
	if r.stepRetry != 0 {
		retry, retryMax = r.stepRetry, r.stepRetryMax
	}
	r.app = app.New(app.Options{Root: r.p.Root, Tick: tick, Retry: 5 * time.Millisecond, RetryMax: 40 * time.Millisecond,
		StepRetry: retry, StepRetryMax: retryMax, DriveWait: r.driveWait,
		Now: r.clock.Now, Out: r.out, Inboxed: r.inbox.recorded, Exit: r.exit, Binary: r.binary, Follow: tick, Tokens: r.tokens,
		Info: at.Info, Screen: at.Screen, Procs: quietProcs, Refresh: port.Refreshed, Home: r.home,
		Loop: func(root string) (*app.Loop, error) {
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
			cfg, err := r.cfg.load(root)
			if err != nil {
				return nil, err
			}
			fx := &app.Loop{Cfg: cfg, Workflow: app.WorkflowOf(r.scratch, r.review), Agents: r.agents, Prompter: c, Kit: "../..", Nightly: night, Disk: r.disk,
				Inbox: r.inbox, Restart: r.restart, Config: r.config, PlaceLane: r.placeLane}
			if r.disp != nil {
				r.disp.loop(fx)
			}
			return fx, nil
		}},
		at.Panes, r.lanes)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	go func() {
		defer close(r.done)
		_ = r.app.Serve(ctx)
	}()
	r.t.Cleanup(r.stop)
}

// rigConfig is the config a rig's Loop factory hands each pass: config.Load once, and again when .obor/config.json
// or .obor/local.json changed since (its size or mtime), so a test's config edit reaches the next pass as serve's
// per-pass read would. Serve's own reads (its tick's reconcile) are serve's, not this.
type rigConfig struct {
	mu    sync.Mutex
	root  string
	stamp string
	cfg   *config.Config
}

func (c *rigConfig) load(root string) (*config.Config, error) {
	stamp := configStamp(root) // before the read: a write between the two reads again on the next pass
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg != nil && c.root == root && c.stamp == stamp {
		return c.cfg, nil
	}
	cfg, err := config.Load(root)
	if err != nil {
		c.cfg = nil
		return nil, err
	}
	c.root, c.stamp, c.cfg = root, stamp, cfg
	return cfg, nil
}

// configStamp is the size and mtime of the project's two config files ("-" for one that is missing).
func configStamp(root string) string {
	var b strings.Builder
	for _, name := range []string{"config.json", "local.json"} {
		fi, err := os.Stat(filepath.Join(root, ".obor", name))
		if err != nil {
			b.WriteString("-;")
			continue
		}
		fmt.Fprintf(&b, "%d/%d;", fi.Size(), fi.ModTime().UnixNano())
	}
	return b.String()
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
	trail := func(recs []event.Record) string {
		return fmt.Sprintf("records:\n%s\napp output:\n%s", strings.Join(stepLines(recs), "\n"), r.out)
	}
	// The wall watch reads the records on its own goroutine, outside the bubble: from the file, never through
	// serve's connection (a bubble's channels are not for goroutines outside it).
	watched := func() string {
		recs, _ := event.ReadLog(r.p.Root)
		return trail(recs)
	}
	if how := poll(r.t, what, r.patience(), 5*time.Millisecond, watched, ok); how != "" {
		r.t.Fatalf("%s waiting for %s; %s", how, what, trail(r.records()))
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

// records is proj's event log (withStore; none: no database yet).
func (r *loopRig) records() []event.Record {
	var recs []event.Record
	_ = r.withStore(func(db *sql.DB) (err error) {
		recs, err = allRecords(db)
		return err
	})
	return recs
}

// withStore is readStore of proj and the rig's serve: every read and write of the store while the app runs goes
// through it.
func (r *loopRig) withStore(read func(*sql.DB) error) error {
	return readStore(r.app, r.done, r.p.Root, read)
}

// laneRows is the store's lanes table by lane (none: no database yet).
func (r *loopRig) laneRows() (map[string]store.LaneRow, error) {
	out := map[string]store.LaneRow{}
	err := r.withStore(func(db *sql.DB) error {
		rows, err := store.ReadLanes(db)
		for _, row := range rows {
			out[row.Lane] = row
		}
		return err
	})
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	return out, err
}

// laneRow is lane's row in the store's lanes table (ok false: none).
func (r *loopRig) laneRow(lane string) (store.LaneRow, bool, error) {
	rows, err := r.laneRows()
	row, ok := rows[lane]
	return row, ok, err
}

// specRow is task's spec in the store (ok false: none).
func (r *loopRig) specRow(task string) (row store.SpecRow, ok bool, err error) {
	err = r.withStore(func(db *sql.DB) (err error) {
		row, ok, err = store.ReadSpec(db, task)
		return err
	})
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	return row, ok, err
}

// steps is the log as "<attempt> <kind> <step>[{reason}]" lines.
func (r *loopRig) steps() []string { return stepLines(r.records()) }

// stepLines is recs as steps' lines.
func stepLines(recs []event.Record) []string {
	var out []string
	for _, rec := range recs {
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

// TestMain is the hook testkit.New requires: it removes the project template the run copies. Run, not Main:
// internal/app's tests re-exec their own binary carrying OBOR_TEST_DO_CRASH and rewrite goldens under OBOR_UPDATE_GOLDEN,
// and Main's Isolate clears every OBOR_* variable.
func TestMain(m *testing.M) { os.Exit(testkit.Run(m)) }
