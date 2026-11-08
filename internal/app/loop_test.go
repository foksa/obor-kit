package app_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The merge loop at the app boundary: the herdr socket client on the fake herdr socket (snapshot, job prompts,
// notifications), the package's in-memory Scratch (app.FakeWorkflow: no git, T-996), a scripted lane table, and an
// integrator start that puts pj-int into the fake herdr.

// loopRig is project proj (a plain dir: its config written, no git) with lane L-01 on task/T-1 (a plain dir the fake
// Scratch snapshots; its worker pj-l-01 idle in it; without the dispatch option T-1's dispatch fact in the log at the
// first start), roles.integrator (pj-int, not running until started) and the app not started yet (unless an option
// starts it). commands.check is configured ("echo 1 pass": its outcome is the fake's, Script).
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
	night *app.Nightly   // the loop's Nightly (nil: none; off is At "", T-782); set before start
	// nightOf, when set, is the loop's Nightly built on each pass (serve's: from the config as it is, T-781); set
	// before start
	nightOf func() *app.Nightly
	config  app.ConfigFile // the loop's config_set writer (nil: none); set before start
	review  port.Review    // the loop's review git (nil: the fake workflow's own, T-1029); set before start
	disk    *app.Disk      // the loop's Disk (nil: none); set before start
	// restart is the loop's Restart, serve's self-restart (nil: none, T-719); set before start
	restart func(ctx context.Context) error
	// binary is Options.Binary, a new obor binary there (nil: not watched, T-719); set before start
	binary func() bool
	// exit is Options.Exit, the socket's exit request (nil: none, T-711); set before start
	exit func()
	// tokens is Options.Tokens, a transcript's context size (nil: the file read, T-1031); set before start
	tokens func(transcript string) (int, bool)
	// inbox is the loop's Inbox: the lead's lines posted (T-485), kept across restarts. The worker pj-l-01 and the
	// integrator pj-int start with no inbox record, so their job wakes are typed (T-499's fallback); a test of posted
	// wakes records them (inbox.record).
	inbox *rigInbox

	clock  *clock // the app's clock, kept across restarts
	app    *app.App
	out    *syncBuf
	sock   string
	cancel context.CancelFunc
	done   chan struct{}
}

// rigOpts is what newLoopRig's options ask for (T-996: one rig constructor, its variants options).
type rigOpts struct {
	check    bool                   // commands.check configured (a green lane test says rc=0; else no check)
	edits    []func(map[string]any) // config edits
	dispatch bool                   // the dispatch adapters, L-01 free
	pool     bool                   // lanes 2: L-02 free too (the dispatch adapters on)
	start    bool                   // started
	working  bool                   // T-1 dispatched by its level onto L-01, its worker working (started)
}

type rigOpt func(*rigOpts)

// noCheck: no commands.check (a green lane test says "no check").
func noCheck() rigOpt { return func(o *rigOpts) { o.check = false } }

// withConfig changes the config further.
func withConfig(edits ...func(map[string]any)) rigOpt {
	return func(o *rigOpts) { o.edits = append(o.edits, edits...) }
}

// dispatching is the rig with the dispatch adapters (the workflow's lanes and target, Loop.Specs) and L-01 free.
func dispatching() rigOpt { return func(o *rigOpts) { o.dispatch = true } }

// pooled is dispatching with lanes 2: L-02 free too.
func pooled() rigOpt { return func(o *rigOpts) { o.dispatch, o.pool = true, true } }

// started is the rig started.
func started() rigOpt { return func(o *rigOpts) { o.start = true } }

// working is dispatching, started, T-1's spec set (medium) and dispatched onto L-01, its worker working on the work
// job, and serve subscribed again after the worker's start (the stream a move or a drop decides on, T-758, T-759).
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
	commands := map[string]any{}
	if o.check {
		commands["check"] = "echo 1 pass"
	}
	edits := []func(map[string]any){testkit.Set("roles.integrator", ""), testkit.Set("commands", commands)}
	if o.pool {
		edits = append(edits, testkit.Set("lanes", 2))
	}
	// The lane's worker is pj-l-01 (L-01's derived name, its pane's label): serve's lane rows come from its log (T-439),
	// and T-1's is its submission's.
	p.Config(append(edits, o.edits...)...)
	r.lanes.lanes = []port.Lane{{Name: "L-01", Dir: r.lane, Task: "T-1", Agent: "pj-l-01"}}
	r.f.Workspaces = []herdr.Workspace{{ID: "w1", Label: "proj"}}
	w := r.f.Agent("w1:p1", "pj-l-01", "idle", r.lane)
	w.Workspace, w.Label = "w1", "pj-l-01" // labelled by its launch: serve's identity of it (registry.go)
	r.srv = herdrtest.Serve(t, r.f)
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
		// decides by serve's identity on (T-758, T-759).
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
		a.Workspace, a.Label, a.Session = "w1", "pj-int", "int-1" // launch labels the pane
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ctx.Err() != nil { // the app is shutting down: its main loop is gone, no snapshot will show the pane
			return port.Started{}, ctx.Err()
		}
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == "pj-int" {
				return port.Started{Line: "STARTED pj-int", Pane: "w1:p9", Kind: "claude", Session: "int-1"}, nil
			}
		}
		r.srv.Push("workspace_created", map[string]any{"type": "workspace_created",
			"workspace": map[string]any{"workspace_id": "w1", "label": "proj"}})
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
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ctx.Err() != nil { // the app is shutting down: its main loop is gone, no snapshot will show the pane gone
			return "", ctx.Err()
		}
		gone := true
		for _, a := range r.app.Status().Project.Agents {
			gone = gone && a.Name != agent
		}
		if gone {
			return "STOPPED " + agent, nil
		}
		r.srv.Push("workspace_created", map[string]any{"type": "workspace_created",
			"workspace": map[string]any{"workspace_id": "w1", "label": "proj"}})
	}
	return "", errors.New(agent + " never seen gone")
}

// stopped is the Agents.Stop calls so far.
func (r *loopRig) stopped() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.stops...)
}

// workerStops is the stops of agents but the integrator (T-672: serve stops pj-int after each verdict that leaves no
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
	r.sock = (&rig{t: r.t, app: r.app}).listen()
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
	for deadline := time.Now().Add(r.patience()); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	r.t.Fatalf("timed out waiting for %s; records:\n%s\napp output:\n%s", what, strings.Join(r.steps(), "\n"), r.out)
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
	if r.sock == "" {
		r.early = true
		return
	}
	if reply, err := r.taskDone(report); err != nil || reply != "submitted" {
		r.t.Fatalf("task_done: %q, %v; records:\n%s", reply, err, strings.Join(r.steps(), "\n"))
	}
}

// taskDone is pj-l-01's task_done for T-1 with text as its report, under a new call id.
func (r *loopRig) taskDone(text string) (string, error) {
	return wire.Verdict(context.Background(), r.sock, wire.MethodTaskDone, wire.Call{Root: r.p.Root, Agent: "pj-l-01",
		Role: "worker", Lane: "L-01", Task: "T-1", CallID: fmt.Sprintf("rig-%d", r.callIDs.Add(1)), Report: text})
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
// before (T-365: a retry clears the input line before it types).
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
// has its result (job_next could hand the job out from its intent on, before the start that carries it returns).
func (r *loopRig) waitWake(agent, job string) {
	r.t.Helper()
	w := "[obor job " + job + "] call the obor job_next tool"
	parts := strings.SplitN(job, "/", 3)
	result := parts[1] + " result " + parts[2]
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
		return sent && r.has(result)
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
	return wire.Verdict(context.Background(), r.sock, method, wire.Call{Root: r.p.Root, Agent: "pj-int", Role: "integrator",
		CallID: callID, Task: "T-1", Patch: patch, Note: note})
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

// From a task_done to merged with no one but the integrator in it: the integrator is not running, so it is
// started for its review job; it accepts the patch the job names; the app publishes and runs the after-merge work.
func TestLoopHappyPath(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	base := r.scratch.TargetHead()
	r.passes()
	if got := r.steps(); len(got) != 1 || got[0] != "1 fact dispatch" {
		t.Fatalf("precondition: before the task_done only T-1's dispatch: %q", got)
	}

	// T-1005: the captured GateInput's body is the report's whole Summary section, bullets and all (RV-139 #4).
	body := "done.\n" +
		"- internal/app/steps.go: the whole Summary into GateInput.Body\n" +
		"- internal/app/loop_test.go: this assertion"
	rep := "Summary: " + body + "\nSelf-check: ok.\nSkipped: none.\nOpen questions: none."
	if reply, err := r.taskDone(rep); err != nil || reply != "submitted" {
		t.Fatalf("task_done: %q, %v; records:\n%s", reply, err, strings.Join(r.steps(), "\n"))
	}
	job := r.waitJob("pj-int", "T-1/1/review")
	if got := r.scratch.Tests.Load(); got != 1 {
		t.Errorf("lane tests %d before the review, want 1 (T-961: gate, candidate, test, then the review)", got)
	}
	if got := r.scratch.LaneAt("L-01"); got != "" {
		t.Errorf("the lane is at %s after its test, want its branch back", got)
	}
	if r.starts.Load() != 1 {
		t.Errorf("integrator starts = %d, want 1 (it did not run)", r.starts.Load())
	}
	for _, want := range []string{"Review T-1 for obor serve (attempt 1, lane L-01): the slot compiled it (no commands.compile) " +
		"and its lane test is green (rc=0)", "No spec is stored for T-1.\n", "feature.txt", "The worker's report:\n" + rep,
		"Patch hash: ", "the task's own diff"} {
		if !strings.Contains(job, want) {
			t.Errorf("review job has no %q:\n%s", want, job)
		}
	}
	if strings.Contains(job, "Answer with one obor tool") {
		t.Errorf("review job repeats the role text's verbs:\n%s", job)
	}
	if r.scratch.TargetHead() != base {
		t.Fatal("main moved before the accept")
	}
	r.accept("c1", r.patch(job))
	r.merged(1)
	if c := r.published(); r.mainMerges() != 1 || !strings.Contains(c.Stat, "feature.txt") || r.scratch.TargetHead() != c.SHA {
		t.Errorf("main: %d merges of T-1, at %s; want one merge, its candidate %+v with the feature", r.mainMerges(),
			r.scratch.TargetHead(), c)
	}
	var steps []string // the idle state's own records are not the merge's (T-803): each asserted on its own
	for _, s := range r.steps() {
		switch {
		case strings.HasSuffix(s, " fact idle"), strings.HasSuffix(s, " fact message"):
		case strings.Contains(s, " intent deliver."), strings.Contains(s, " result deliver."):
		default:
			steps = append(steps, s)
		}
	}
	eq(t, steps, []string{"1 fact dispatch", "1 fact submitted", "1 intent submit", "1 result submit", "1 intent gate",
		"1 result gate", "1 intent test", "1 result test", "1 intent review", "1 intent start.review", "1 result start.review", "1 result review",
		"1 fact delivered", "1 intent accept.c1", "1 result accept.c1", "1 intent publish", "1 result publish",
		"1 fact merged", "1 intent after_merge", "1 result after_merge", "1 intent agent.stop", "1 result agent.stop"},
		"records")
	if in := r.scratch.Inputs(); len(in) != 1 || in[0].Summary != "done." || in[0].Title != "" || in[0].Body != body {
		t.Errorf("gate inputs %+v, want one with the report's summary, the whole Summary section as its body and no title (the merge's subject)", in)
	}
	if r.scratch.AfterMerges.Load() != 1 || r.scratch.Publishes.Load() != 1 {
		t.Errorf("after_merge runs %d, publishes %d; want 1, 1", r.scratch.AfterMerges.Load(), r.scratch.Publishes.Load())
	}
	if r.p.Exists(".obor/state/done") {
		t.Errorf("a done-file archive dir (gone with T-437)")
	}
	if got := strings.Join(r.workerStops(), ","); got != "pj-l-01 w1:p1" {
		t.Errorf("stops %q; want pj-l-01 in its submitted pane", got)
	}
	if n := len(r.stopped()) - len(r.workerStops()); n != 1 {
		t.Errorf("integrator stops %d (%q), want 1: after its verdict, no job left for it (T-672)", n, r.stopped())
	}
	r.noLead()
	if n := len(r.prompts()); n != 0 {
		t.Errorf("prompts = %d, want none (the integrator's start carried the review job's wake)", n)
	}
}

// A red lane test (T-961) goes back to the worker with the reasons; its next task_done is the next attempt. After K = 2 sendbacks a third red goes to the owner (one notification), not to the worker again.
func TestLoopRedSendsBackTwiceThenNeedsYou(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("red.txt", "red\n")
	r.scratch.Script("Test", "T-1", port.OutcomeRed) // every lane test of T-1
	r.start()
	r.submit()
	for attempt := 2; attempt <= 3; attempt++ {
		job := r.waitJob("pj-l-01", "T-1/"+string(rune('0'+attempt))+"/fix")
		for _, want := range []string{"T-1 is sent back to you", "RED check", "call task_done again"} {
			if !strings.Contains(job, want) {
				t.Errorf("fix job %d has no %q:\n%s", attempt, want, job)
			}
		}
		r.waitStep(string(rune('0'+attempt-1)) + " fact sendback")
		r.submit()
		r.waitStep(string(rune('0'+attempt)) + " fact submitted")
	}
	r.waitStep("3 result test") // T-961: the class's check runs in the lane test, not the gate
	r.waitStep("3 fact needs-you{gate red after 2 sendbacks: " + r.redLine() + "}")
	r.waitFor("one notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
	if n := r.count("3 fact sendback"); n != 0 {
		t.Errorf("a third sendback recorded")
	}
	fixes := 0
	for _, p := range r.prompts() {
		if p.to == "pj-l-01" && strings.HasSuffix(p.text, "/fix] call the obor job_next tool") {
			fixes++
		}
	}
	if fixes != 2 || r.mainMerges() != 0 {
		t.Errorf("fix jobs %d, merges %d; want 2, 0", fixes, r.mainMerges())
	}
	if st := r.app.Status().Project; len(st.Tasks) != 1 || st.Tasks[0].State != "needs-you" || len(st.Facts) != 1 {
		t.Errorf("status: %+v", st)
	}
	r.noLead()
}

// redLine is the third attempt's red line (its lane test's, T-961), as the needs-you names it.
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

// A conflict is a resolve job for the integrator (not the owner): it resolves and commits the merge in the scratch,
// says resolved, the app gates that merge, tests it once in the lane, and reviews it (T-973: the resolve job carries
// only the conflict, so a first-gate conflict gets the task's review), then publishes after the accept.
func TestLoopConflictResolvedThenMerged(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.conflicting()
	r.start()
	r.submit()
	job := r.waitJob("pj-int", "T-1/1/resolve")
	scratch := filepath.Join(r.p.Base, "proj-wt", "merge")
	for _, want := range []string{"Resolve T-1's merge for obor serve", "CONFLICT in " + scratch, "shared.txt"} {
		if !strings.Contains(job, want) {
			t.Errorf("resolve job has no %q:\n%s", want, job)
		}
	}
	own, _, _ := strings.Cut(job, "Your role text (") // the job's own part: the role texts (T-941) come after it
	if strings.Contains(own, "obor resolved tool") || strings.Contains(own, "git commit -m") {
		t.Errorf("resolve job repeats the role text's steps:\n%s", job)
	}
	if strings.Contains(own, "--ff-retry") {
		t.Errorf("resolve job names obor merge --ff-retry:\n%s", job)
	}
	if _, err := r.verdict(wire.MethodAccept, "c0", "x", ""); err == nil || !strings.Contains(err.Error(), "is a resolve job") {
		t.Errorf("accept on the resolve job: %v", err)
	}
	resolution := r.resolveInScratch()
	if reply, err := r.verdict(wire.MethodResolved, "c1", "", ""); err != nil || reply != "RESOLVED T-1 (job T-1/1/resolve)" {
		t.Fatalf("resolved: %q, %v", reply, err)
	}
	// T-973: a first-gate conflict had no accept, so the resolved candidate's green lane test is reviewed once.
	review := r.waitJob("pj-int", "T-1/1/review")
	r.accept("c2", r.patch(review))
	r.merged(1)
	if c := r.published(); c.SHA != resolution || r.mainMerges() != 1 {
		t.Errorf("published %s, merges %d; want the resolution %s, merged once", c.SHA, r.mainMerges(), resolution)
	}
	for _, s := range []string{"1 result gate", "1 intent resolve", "1 result resolved.c1", "1 result gate.2",
		"1 intent test", "1 result test", "1 intent review", "1 result review", "1 result publish", "1 fact merged"} {
		if !r.has(s) {
			t.Errorf("no record %q: %q", s, r.steps())
		}
	}
	if n := r.count("1 intent resolve"); n != 1 {
		t.Errorf("resolve jobs %d, want one: %q", n, r.steps())
	}
	if n := r.count("1 intent review"); n != 1 {
		t.Errorf("review jobs %d, want one (a first-gate conflict's single review): %q", n, r.steps())
	}
	if r.has("1 fact accept.kept") {
		t.Errorf("a kept accept with no earlier accept: %q", r.steps())
	}
	if r.starts.Load() != 2 {
		t.Errorf("integrator starts = %d, want 2 (the resolve's and the review's)", r.starts.Load())
	}
	r.noLead()
}

// The target moves between the review job and the accept: publish finds it moved, the gate runs again on the new
// target (T-961: the slot merges and compiles the task onto it again, gate.2). The accept binds to the task's own diff
// Base..S, which a move leaves as it was: accept.kept, then publish.2, no lane test and no review job on a move; a
// verdict after the merge finds no job. (That a move in the patch's context keeps the patch and both edits is git's:
// internal/checks' TestGateCandidateIsTheTasksOwnDiff.)
func TestLoopTargetMoved(t *testing.T) {
	t.Parallel()
	t.Run("identical patch", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t)
		r.work("feature.txt", "feature\n")
		r.start()
		r.submit()
		job := r.waitJob("pj-int", "T-1/1/review")
		if n := r.scratch.Tests.Load(); n != 1 {
			t.Fatalf("precondition: one lane test before the review, got %d", n)
		}
		submitted := r.scratch.LaneHead("L-01")
		moved := r.scratch.Move() // an unrelated commit on main
		r.accept("c1", r.patch(job))
		r.merged(1)
		for _, s := range []string{"1 result publish", "1 fact target.moved", "1 result gate.2", "1 fact accept.kept",
			"1 result publish.2", "1 fact merged"} {
			if !r.has(s) {
				t.Errorf("no record %q: %q", s, r.steps())
			}
		}
		c := r.published()
		if n := r.jobPrompts("T-1/1/review"); n != 1 || len(r.prompts()) != 0 || r.mainMerges() != 1 || c.Target != moved {
			t.Errorf("prompts %d, merges %d, onto %s; want the one review job, merged once on the moved main %s", n,
				r.mainMerges(), c.Target, moved)
		}
		if got := r.scratch.Merges(""); got != 1 {
			t.Errorf("%d merges on main, want one for T-1 (T-403)", got)
		}
		if got := r.scratch.Parents(r.scratch.TargetHead()); len(got) != 2 || got[1] != submitted {
			t.Errorf("main's merge's parents %q, want the submitted sha %s second", got, submitted)
		}
		in := r.scratch.Inputs()
		if len(in) != 2 || in[1].Kind != port.InputMoved || in[1].Input != submitted || in[1].Summary != "done." {
			t.Errorf("gate inputs %+v; want the moved gate of the submitted sha, the task's subject rule (T-360)", in)
		}
		for _, s := range []string{"1 intent test.2", "1 intent review.2"} {
			if r.has(s) {
				t.Errorf("a move ran %q: %q", s, r.steps())
			}
		}
		if n := r.scratch.Tests.Load(); n != 1 {
			t.Errorf("lane tests %d, want the one before the review (none on the move)", n)
		}
		if _, err := r.verdict(wire.MethodReject, "c5", "", "late"); err == nil || !strings.Contains(err.Error(), "no job open") {
			t.Errorf("a verdict after the merge: %v", err)
		}
	})
}

// T-403: a moved gate that conflicts is resolved against the submitted sha (the merge's second parent), then gated as
// committed, tested in the lane and merged (T-961: no second review); the old candidate does not go along.
func TestLoopMovedConflictResolved(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("shared.txt", "lane\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	r.start()
	r.submit()
	job := r.waitJob("pj-int", "T-1/1/review")
	submitted := r.scratch.LaneHead("L-01")
	r.movedConflict() // main edits the lane's line: the moved merge conflicts
	r.accept("c1", r.patch(job))
	job = r.waitJob("pj-int", "T-1/1/resolve")
	scratch := filepath.Join(r.p.Base, "proj-wt", "merge")
	if !strings.Contains(job, "CONFLICT in "+scratch) {
		t.Fatalf("resolve job does not name the moved gate's conflict:\n%s", job)
	}
	resolution := r.resolveInScratch()
	if reply, err := r.verdict(wire.MethodResolved, "c1", "", ""); err != nil || reply != "RESOLVED T-1 (job T-1/1/resolve)" {
		t.Fatalf("resolved against the submitted sha: %q, %v", reply, err)
	}
	// T-961: the resolution is compiled (gate.3), tested once in the lane (test.2) and not reviewed again: the accept
	// binds to the task's own diff, which the resolution leaves as it was (accept.kept).
	r.merged(1)
	for _, s := range []string{"1 result gate.3", "1 intent test.2", "1 result test.2", "1 fact accept.kept"} {
		if !r.has(s) {
			t.Errorf("no record %q: %q", s, r.steps())
		}
	}
	if n := r.scratch.Tests.Load(); n != 2 {
		t.Errorf("lane tests %d, want 2: the lane merge's and the resolution's, the move's none", n)
	}
	if n := r.jobPrompts("T-1/1/review.2"); n != 0 || r.has("1 intent review.2") {
		t.Errorf("the resolution was reviewed again (%d prompts): %q", n, r.steps())
	}
	if got := r.scratch.TargetHead(); got != resolution || r.scratch.Merges("") != 1 {
		t.Errorf("main at %s with %d merges, want the resolution %s, one merge for T-1 (T-403)", got,
			r.scratch.Merges(""), resolution)
	}
	if got := r.scratch.Parents(resolution); len(got) != 2 || got[1] != submitted {
		t.Errorf("the resolution's parents %q, want the submitted sha %s second", got, submitted)
	}
}

// An obor merge outside the app while the task waits for its accept (the cutover's race): reconciled to merged, no
// publish and no after-merge work of its own; the integrator's late accept finds no job.
func TestLoopMergedOutsideTheApp(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	r.submit()
	job := r.waitJob("pj-int", "T-1/1/review")
	r.scratch.Land("T-1") // obor merge by hand: "Merge T-1: by hand" on main
	r.waitStep("1 fact merged")
	var d struct{ Outside bool }
	for _, rec := range r.records() {
		if rec.Step == "merged" {
			_ = json.Unmarshal(rec.Data, &d)
		}
	}
	r.passes()
	if !d.Outside || r.scratch.Publishes.Load() != 0 || r.has("1 intent after_merge") || r.has("1 intent archive") ||
		r.has("1 intent agent.stop") || r.mainMerges() != 1 {
		t.Errorf("outside %v, publishes %d, merges %d: %q", d.Outside, r.scratch.Publishes.Load(), r.mainMerges(), r.steps())
	}
	if _, err := r.verdict(wire.MethodAccept, "c1", r.patch(job), ""); err == nil || !strings.Contains(err.Error(), "no job open") {
		t.Errorf("accept after the outside merge: %v", err)
	}
}

// obor serve restarts while the task waits for its accept: the replay settles the review prompt (never sent again),
// and the integrator's verb after the restart merges it.
func TestLoopRestartWhileAwaitingAccept(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	r.submit()
	job := r.waitJob("pj-int", "T-1/1/review")
	r.waitStep("1 result review")
	r.stop()
	r.start()
	r.passes()
	if st := r.app.Status().Project; len(st.Tasks) != 1 || st.Tasks[0].State != "awaiting-accept" {
		t.Fatalf("after the restart: %+v", st.Tasks)
	}
	r.accept("c1", r.patch(job))
	r.merged(1)
	if n := r.jobPrompts("T-1/1/review"); n != 1 || r.mainMerges() != 1 || r.scratch.Gates.Load() != 1 {
		t.Errorf("prompts %d, merges %d, prepares %d; want 1 each", n, r.mainMerges(), r.scratch.Gates.Load())
	}
}

// A pass that cannot build the loop for a project (a config the binary cannot read) is a fact (T-377): one needs-you
// "loop: <error>" and one notification however many passes fail, a changed error a new one, and the first pass that
// works ends it.
func TestLoopErrorIsAFactOnceAndClears(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	setErr := func(err error) {
		r.mu.Lock()
		r.loopErr = err
		r.mu.Unlock()
	}
	setErr(errors.New("config.json: boom"))
	r.start()
	r.waitStep("0 fact needs-you{loop: config.json: boom}")
	r.waitFor("the notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
	for i := 0; i < 3; i++ {
		r.passes()
	}
	if n, m := r.count("0 fact needs-you{loop: config.json: boom}"), countReq(r.srv, "notification.show"); n != 1 || m != 1 {
		t.Errorf("after repeated failing passes: %d needs-you, %d notifications; want 1, 1", n, m)
	}
	if st := r.app.Status().Project; len(st.Facts) != 1 || st.Facts[0].Task != event.LoopTask {
		t.Errorf("status facts: %+v", st.Facts)
	}

	setErr(errors.New("config.json: other"))
	r.waitStep("0 fact needs-you{loop: config.json: other}")
	r.waitFor("the second notification", func() bool { return countReq(r.srv, "notification.show") == 2 })
	if st := r.app.Status().Project; len(st.Facts) != 1 {
		t.Errorf("a changed error must replace the fact, status facts: %+v", st.Facts)
	}

	setErr(nil)
	r.waitFor("the fact to clear", func() bool { return len(r.app.Status().Project.Facts) == 0 })
	if !r.has("0 fact resumed") {
		t.Errorf("no resumed record: %q", r.steps())
	}
	r.passes()
	r.passes()
	if n := r.count("0 fact resumed"); n != 2 { // one for the replaced error, one for the end
		t.Errorf("resumed records = %d, want 2 (no more once the loop works): %q", n, r.steps())
	}
}

// T-360 (RV-39 #6), T-440, T-521: the lane's commit and its merge take the task's title from the stored spec; the
// title in the main checkout's spec file changes neither. The review job carries the gated spec rev's text; the gate
// gets that rev and its text for the merge's message (the message itself, and that the gate writes no spec file, are
// internal/checks': TestGateDecisions).
func TestCommitSubjectFromStoredSpec(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	const committed = "# T-1 — Committed target title"
	r.p.Write("docs/tasks/T-1.md", committed+"\n")
	r.work("feature.txt", "feature\n")
	r.start()
	const stored = "# T-1 — Stored title\n\nDifficulty: medium\n\n## Files you may touch\n`feature.txt`\n"
	r.setSpec("s1", stored)
	r.submit()
	r.waitStep("1 result submit")
	if got := r.scratch.Subjects(); len(got) != 1 || got[0] != "T-1: Stored title" {
		t.Errorf("lane commit subjects %q, want the stored spec's title", got)
	}
	job := r.waitJob("pj-int", "T-1/1/review")
	if want := "Spec to review: rev 1 (the one the candidate's merge message contains):\n\nThe spec of T-1 (rev 1, " +
		"as obor serve stores it; obor spec show T-1 prints the current rev):\n\n" + stored; !strings.Contains(job, want) {
		t.Errorf("review job has no %q:\n%s", want, job)
	}
	if strings.Contains(job, "docs/tasks") {
		t.Errorf("review job names a spec file:\n%s", job)
	}
	// The lead changes the spec after the gate: the job names the gated rev and where its text is, then the current one.
	const later = "# T-1 — Stored title\n\nDifficulty: medium\n\nwidened\n"
	r.setSpec("s2", later)
	again, ok := r.fetch("pj-int", "T-1/1/review")
	if !ok {
		t.Fatal("review job not fetched again")
	}
	if want := "Spec to review: rev 1, in the candidate's merge message (git log -1 --format=%B "; !strings.Contains(again, want) ||
		!strings.Contains(again, "(rev 2, as obor serve stores it") || !strings.Contains(again, later) {
		t.Errorf("review job after the spec changed has no %q nor rev 2's text:\n%s", want, again)
	}
	r.accept("c1", r.patch(job))
	r.merged(1)
	if in := r.scratch.Inputs(); len(in) != 1 || in[0].Title != "Stored title" || in[0].SpecRev != 1 || in[0].Spec != stored {
		t.Errorf("gate inputs %+v, want the stored spec's title, then rev 1 and its text", in)
	}
	if got := r.p.Read("docs/tasks/T-1.md"); got != committed+"\n" {
		t.Errorf("the main checkout's docs/tasks/T-1.md = %q, want it as it was", got)
	}
}

// setSpec stores text as T-1's spec through serve's socket, as the lead (obor spec set).
func (r *loopRig) setSpec(callID, text string) string {
	r.t.Helper()
	reply, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Agent: "lead",
		Role: "lead", CallID: callID, Task: "T-1", Spec: text})
	if err != nil {
		r.t.Fatalf("spec set: %v", err)
	}
	return reply
}
