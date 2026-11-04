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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The merge loop at the app boundary: the herdr socket client on the fake herdr socket (snapshot, job prompts,
// notifications), a fake Scratch on a git fixture (gitScratch), a scripted lane table, and an integrator start that
// puts pj-int into the fake herdr.

// loopRig is project proj (main with a 10-line shared.txt; .obor/state/ ignored) with lane L-01 on task/T-1 (its
// worker pj-l-01 idle in it; without the dispatch rig T-1's dispatch fact in the log at the first start),
// roles.integrator (pj-int, not running until started) and the app not started yet. check is commands.check ("" for none); edits change the config further.
type loopRig struct {
	t       *testing.T
	p       *testkit.Project
	lane    string
	f       *herdrtest.Fake
	srv     *herdrtest.Server
	lanes   *laneTable
	scratch *gitScratch
	starts  atomic.Int32
	callIDs atomic.Int32 // the rig's own task_done and job_next call ids
	early   bool         // submit before the first start: T-1's task_done is in the log the start replays

	mu       sync.Mutex
	stops    []string      // "<agent> <pane>" per Agents.Stop
	launches []port.Launch // every Agents.Launch, in order
	// onStart, onStop: an error a start or stop gives instead of acting (nil: none; under mu).
	onStart, onStop func() error
	loopErr         error // what the Loop adapters' constructor fails with (nil: none; under mu)
	checkoutErr     error // what the Loop's Checkout says (nil: on the target branch; under mu)

	disp  *dispatchFakes // the dispatch's adapters (nil: none, the loop has no dispatch)
	tick  time.Duration  // the watchdogs' tick, each a merge-loop pass (0: 20 ms); set before start
	night *app.Nightly   // the loop's Nightly (nil: none; off is At "", T-782); set before start
	// nightOf, when set, is the loop's Nightly built on each pass (serve's: from the config as it is, T-781); set
	// before start
	nightOf func() *app.Nightly
	config  app.ConfigFile // the loop's config_set writer (nil: none); set before start
	disk    *app.Disk      // the loop's Disk (nil: none); set before start
	// restart is the loop's Restart, serve's self-restart (nil: none, T-652); set before start
	restart func(ctx context.Context) error
	// binary is Options.Binary, a new obor binary there (nil: not watched, T-719); set before start
	binary func() bool
	// exit is Options.Exit, the socket's exit request (nil: none, T-711); set before start
	exit func()
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

func newLoopRig(t *testing.T, check string, edits ...func(map[string]any)) *loopRig {
	t.Helper()
	p := testkit.New(t)
	p.Write(".gitignore", ".task-done.md\n.obor/state/\n")
	p.Write("shared.txt", "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	p.Git(p.Root, "add", "-A")
	p.Git(p.Root, "commit", "-qm", "fixture")
	r := &loopRig{t: t, p: p, f: herdrtest.New(), lanes: &laneTable{},
		clock: &clock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}, inbox: &rigInbox{none: map[string]bool{"pj-l-01": true, "pj-int": true}}}
	r.lane = p.Lane("L-01", "task/T-1")
	commands := map[string]any{}
	if check != "" {
		commands["check"] = check
	}
	// The lane's worker is pj-l-01 (L-01's derived name, its pane's label): serve's lane rows come from its log (T-439),
	// and T-1's is its submission's.
	p.Config(append([]func(map[string]any){testkit.Set("roles.integrator", ""), testkit.Set("commands", commands)},
		edits...)...)
	r.lanes.lanes = []port.Lane{{Name: "L-01", Dir: r.lane, Task: "T-1", Agent: "pj-l-01"}}
	r.f.Workspaces = []herdr.Workspace{{ID: "w1", Label: "proj"}}
	w := r.f.Agent("w1:p1", "pj-l-01", "idle", r.lane)
	w.Workspace, w.Label = "w1", "pj-l-01" // labelled by its launch: serve's identity of it (registry.go)
	r.srv = herdrtest.Serve(t, r.f)
	r.srv.Reply("notification.show", map[string]any{"type": "notification_show", "shown": true, "reason": "shown"})
	r.srv.Reply("agent.prompt", prompted)
	// Git without the developer's config.
	environ := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=obor test",
		"GIT_AUTHOR_EMAIL=obor-test@example.invalid", "GIT_COMMITTER_NAME=obor test",
		"GIT_COMMITTER_EMAIL=obor-test@example.invalid")
	r.scratch = &gitScratch{p: p, lanes: r.lanes, environ: environ}
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
func (r *loopRig) Stop(_ context.Context, agent, pane string, _ bool) (string, error) {
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
	r.waitFor("the first check", func() bool { checks, _ := app.Counts(r.app); return checks >= 1 })
	r.sock = (&rig{t: r.t, app: r.app}).listen()
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
	tick := 20 * time.Millisecond
	if r.tick != 0 {
		tick = r.tick
	}
	r.app = app.New(app.Options{Root: r.p.Root, Tick: tick, Retry: 5 * time.Millisecond, RetryMax: 40 * time.Millisecond,
		Now: r.clock.Now, Out: r.out, Inboxed: r.inbox.recorded, Exit: r.exit, Binary: r.binary, Follow: tick, Refresh: func(err error) bool {
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
			fx := &app.Loop{Scratch: r.scratch, Agents: r, Typer: panes, Nightly: night, Disk: r.disk,
				Inbox: r.inbox, Restart: r.restart, Config: r.config,
				Checkout: func(context.Context) error {
					r.mu.Lock()
					defer r.mu.Unlock()
					return r.checkoutErr
				}}
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

// work writes path in the lane (uncommitted: the gate commits it).
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

// mainMerges is main's first-parent commits for T-1 ("Merge T-1…").
func (r *loopRig) mainMerges() int {
	n := 0
	for _, s := range strings.Split(r.p.Git(r.p.Root, "log", "--first-parent", "--format=%s", "main"), "\n") {
		if strings.HasPrefix(s, "Merge T-1") {
			n++
		}
	}
	return n
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
	r := newLoopRig(t, "echo 1 pass")
	r.work("feature.txt", "feature\n")
	r.start()
	base := r.p.Git(r.p.Root, "rev-parse", "main")
	r.passes()
	if got := r.steps(); len(got) != 1 || got[0] != "1 fact dispatch" {
		t.Fatalf("precondition: before the task_done only T-1's dispatch: %q", got)
	}

	r.submit()
	job := r.waitJob("pj-int", "T-1/1/review")
	if r.starts.Load() != 1 {
		t.Errorf("integrator starts = %d, want 1 (it did not run)", r.starts.Load())
	}
	for _, want := range []string{"Review T-1 for obor serve (attempt 1, lane L-01): the gate is green (rc=0)",
		"No spec is stored for T-1.\n", "feature.txt", "The worker's report:\n" + report,
		"Patch hash: "} {
		if !strings.Contains(job, want) {
			t.Errorf("review job has no %q:\n%s", want, job)
		}
	}
	if strings.Contains(job, "Answer with one obor tool") {
		t.Errorf("review job repeats the role text's verbs:\n%s", job)
	}
	if r.p.Git(r.p.Root, "rev-parse", "main") != base {
		t.Fatal("main moved before the accept")
	}
	r.accept("c1", r.patch(job))
	r.merged(1)
	if r.mainMerges() != 1 || !r.p.Exists("feature.txt") {
		t.Errorf("main: %d merges of T-1, feature.txt %v; want one merge with the feature", r.mainMerges(), r.p.Exists("feature.txt"))
	}
	var steps []string // the integrator's stop after its verdict (T-672) runs beside the publish: asserted on its own
	for _, s := range r.steps() {
		if !strings.HasPrefix(s, "0 intent stop.") && !strings.HasPrefix(s, "0 result stop.") {
			steps = append(steps, s)
		}
	}
	if !r.has("0 intent stop.pj-int") || !r.has("0 result stop.pj-int") {
		t.Errorf("no stop of pj-int after its verdict (T-672): %q", r.steps())
	}
	eq(t, steps, []string{"1 fact dispatch", "1 fact submitted", "1 intent submit", "1 result submit", "1 intent gate",
		"1 result gate", "1 intent review", "1 intent start.review", "1 result start.review", "1 result review",
		"1 fact delivered", "1 intent accept.c1", "1 result accept.c1", "1 intent publish", "1 result publish",
		"1 fact merged", "1 intent after_merge", "1 result after_merge", "1 intent agent.stop", "1 result agent.stop"},
		"records")
	if got := r.p.Git(r.p.Root, "log", "-1", "--format=%s", "main"); got != "Merge T-1: done." {
		t.Errorf("main's merge subject %q, want the report's summary (GateInput.Summary)", got)
	}
	if r.scratch.afterMerges.Load() != 1 || r.scratch.publishes.Load() != 1 {
		t.Errorf("after_merge runs %d, publishes %d; want 1, 1", r.scratch.afterMerges.Load(), r.scratch.publishes.Load())
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

// A red gate goes back to the worker with the reasons; its next task_done is the next attempt. After K = 2 sendbacks a third red goes to the owner (one notification), not to the worker again.
func TestLoopRedSendsBackTwiceThenNeedsYou(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, `if [ -f red.txt ]; then echo "0 pass, 1 fail"; exit 1; fi; echo 1 pass`)
	r.work("red.txt", "red\n")
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
	r.waitStep("3 result gate")
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

// redLine is the third gate's line, as the needs-you names it.
func (r *loopRig) redLine() string {
	var line string
	for _, rec := range r.records() {
		if rec.Kind == event.KindResult && strings.HasPrefix(rec.Step, "gate") && rec.Attempt == 3 {
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
// says resolved, the app gates that merge, then the review job, accept, merged.
func TestLoopConflictResolvedThenMerged(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.work("shared.txt", "lane\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	r.p.Write("shared.txt", "main\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	r.p.Git(r.p.Root, "commit", "-qam", "main edit")
	r.start()
	r.submit()
	job := r.waitJob("pj-int", "T-1/1/resolve")
	scratch := filepath.Join(r.p.Base, "proj-wt", "merge")
	for _, want := range []string{"Resolve T-1's merge for obor serve", "CONFLICT in " + scratch, "shared.txt"} {
		if !strings.Contains(job, want) {
			t.Errorf("resolve job has no %q:\n%s", want, job)
		}
	}
	if strings.Contains(job, "obor resolved tool") || strings.Contains(job, "git commit -m") {
		t.Errorf("resolve job repeats the role text's steps:\n%s", job)
	}
	if strings.Contains(job, "--ff-retry") {
		t.Errorf("resolve job names obor merge --ff-retry:\n%s", job)
	}
	if _, err := r.verdict(wire.MethodAccept, "c0", "x", ""); err == nil || !strings.Contains(err.Error(), "is a resolve job") {
		t.Errorf("accept on the resolve job: %v", err)
	}
	r.p.Write(filepath.Join(scratch, "shared.txt"), "both\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	r.p.Git(scratch, "add", "shared.txt")
	r.p.Git(scratch, "commit", "-qm", "Merge T-1: resolved")
	if reply, err := r.verdict(wire.MethodResolved, "c1", "", ""); err != nil || reply != "RESOLVED T-1 (job T-1/1/resolve)" {
		t.Fatalf("resolved: %q, %v", reply, err)
	}
	job = r.waitJob("pj-int", "T-1/1/review")
	r.accept("c2", r.patch(job))
	r.merged(1)
	if got := r.p.Read("shared.txt"); got != "both\n2\n3\n4\n5\n6\n7\n8\n9\n10\n" || r.mainMerges() != 1 {
		t.Errorf("main's shared.txt %q, merges %d; want the resolution, merged once", got, r.mainMerges())
	}
	for _, s := range []string{"1 result gate", "1 intent resolve", "1 result resolved.c1", "1 result gate.2",
		"1 intent review"} {
		if !r.has(s) {
			t.Errorf("no record %q: %q", s, r.steps())
		}
	}
	if r.starts.Load() != 2 {
		t.Errorf("integrator starts = %d, want 2 (a fresh one per job: resolve, review; T-602)", r.starts.Load())
	}
	r.noLead()
}

// The target moves between the review job and the accept: publish finds it moved, the gate runs again on the new
// target. An identical patch keeps the accept (published, no second job); a changed patch is a new review job, and
// an accept of the old patch or the old job, or a verdict on an answered job, is refused; a repeated call id gets its
// stored reply.
func TestLoopTargetMoved(t *testing.T) {
	t.Parallel()
	t.Run("identical patch", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t, "echo 1 pass")
		r.work("feature.txt", "feature\n")
		r.start()
		r.submit()
		job := r.waitJob("pj-int", "T-1/1/review")
		submitted := r.p.Git(r.lane, "rev-parse", "HEAD")
		r.p.Write("other.txt", "other\n")
		r.p.Git(r.p.Root, "add", "other.txt")
		r.p.Git(r.p.Root, "commit", "-qm", "unrelated")
		r.accept("c1", r.patch(job))
		r.merged(1)
		for _, s := range []string{"1 result publish", "1 fact target.moved", "1 result gate.2", "1 fact accept.kept",
			"1 result publish.2", "1 fact merged"} {
			if !r.has(s) {
				t.Errorf("no record %q: %q", s, r.steps())
			}
		}
		if n := r.jobPrompts("T-1/1/review"); n != 1 || len(r.prompts()) != 0 || r.mainMerges() != 1 || !r.p.Exists("other.txt") {
			t.Errorf("prompts %d, merges %d; want the one review job, merged once on the moved main", n, r.mainMerges())
		}
		if got := r.p.Git(r.p.Root, "rev-list", "--count", "--merges", "main"); got != "1" {
			t.Errorf("%s merge commits on main, want one for T-1 (T-403)", got)
		}
		if got := r.p.Git(r.p.Root, "rev-parse", "main^2"); got != submitted {
			t.Errorf("main's merge's second parent %s, want the submitted sha %s", got, submitted)
		}
		if got := r.p.Git(r.p.Root, "log", "-1", "--format=%s", "main"); got != "Merge T-1: done." {
			t.Errorf("moved merge subject %q, want the task's subject rule (T-360)", got)
		}
	})
	t.Run("changed patch", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t, "echo 1 pass")
		r.work("shared.txt", "lane\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
		r.start()
		r.submit()
		job := r.waitJob("pj-int", "T-1/1/review")
		old := r.patch(job)
		r.p.Write("shared.txt", "1\n2\nmain\n4\n5\n6\n7\n8\n9\n10\n") // in the patch's context: its text changes
		r.p.Git(r.p.Root, "commit", "-qam", "main edit near the lane's")
		r.accept("c1", old)
		job = r.waitJob("pj-int", "T-1/1/review.2")
		if r.patch(job) == old {
			t.Fatal("precondition: the re-gated patch is the old one")
		}
		if !r.has("1 fact target.moved") || r.has("1 fact accept.kept") {
			t.Errorf("moved, not kept: %q", r.steps())
		}
		if _, err := r.verdict(wire.MethodAccept, "c2", old, ""); err == nil || !strings.Contains(err.Error(), "stale: re-read the candidate") {
			t.Errorf("accept of the old patch: %v", err)
		}
		stale := wire.Call{Root: r.p.Root, Agent: "pj-int", Role: "integrator", CallID: "c3", Task: "T-1",
			Job: "T-1/1/review", Patch: r.patch(job)}
		if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodAccept, stale); err == nil ||
			!strings.Contains(err.Error(), "stale job T-1/1/review") {
			t.Errorf("accept of the old job: %v", err)
		}
		if reply, err := r.verdict(wire.MethodAccept, "c1", old, ""); err != nil || reply != "ACCEPT T-1 (job T-1/1/review)" {
			t.Errorf("the first accept's call id again: %q, %v; want its stored reply", reply, err)
		}
		r.accept("c4", r.patch(job))
		r.merged(1)
		if _, err := r.verdict(wire.MethodReject, "c5", "", "late"); err == nil || !strings.Contains(err.Error(), "no job open") {
			t.Errorf("a verdict after the merge: %v", err)
		}
		if r.mainMerges() != 1 || r.count("1 fact merged") != 1 {
			t.Errorf("merges %d: %q", r.mainMerges(), r.steps())
		}
	})
}

// T-403: a moved gate that conflicts is resolved against the submitted sha (the merge's second parent), then gated as
// committed and merged; the old candidate does not go along.
func TestLoopMovedConflictResolved(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.work("shared.txt", "lane\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	r.start()
	r.submit()
	job := r.waitJob("pj-int", "T-1/1/review")
	submitted := r.p.Git(r.lane, "rev-parse", "HEAD")
	r.p.Write("shared.txt", "main\n2\n3\n4\n5\n6\n7\n8\n9\n10\n") // the same line: the moved merge conflicts
	r.p.Git(r.p.Root, "commit", "-qam", "main edit")
	r.accept("c1", r.patch(job))
	job = r.waitJob("pj-int", "T-1/1/resolve")
	scratch := filepath.Join(r.p.Base, "proj-wt", "merge")
	if !strings.Contains(job, "CONFLICT in "+scratch) {
		t.Fatalf("resolve job does not name the moved gate's conflict:\n%s", job)
	}
	r.p.Write(filepath.Join(scratch, "shared.txt"), "both\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	r.p.Git(scratch, "add", "shared.txt")
	r.p.Git(scratch, "commit", "-qm", "Merge T-1: resolved")
	if reply, err := r.verdict(wire.MethodResolved, "c1", "", ""); err != nil || reply != "RESOLVED T-1 (job T-1/1/resolve)" {
		t.Fatalf("resolved against the submitted sha: %q, %v", reply, err)
	}
	job = r.waitJob("pj-int", "T-1/1/review.2")
	r.accept("c2", r.patch(job))
	r.merged(1)
	if got := r.p.Read("shared.txt"); got != "both\n2\n3\n4\n5\n6\n7\n8\n9\n10\n" {
		t.Errorf("main's shared.txt %q, want the resolution", got)
	}
	if got := r.p.Git(r.p.Root, "rev-list", "--count", "--merges", "main"); got != "1" {
		t.Errorf("%s merge commits on main, want one for T-1 (T-403)", got)
	}
	if got := r.p.Git(r.p.Root, "rev-parse", "main^2"); got != submitted {
		t.Errorf("main's merge's second parent %s, want the submitted sha %s", got, submitted)
	}
}

// An obor merge outside the app while the task waits for its accept (the cutover's race): reconciled to merged, no
// publish and no after-merge work of its own; the integrator's late accept finds no job.
func TestLoopMergedOutsideTheApp(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.work("feature.txt", "feature\n")
	r.start()
	r.submit()
	job := r.waitJob("pj-int", "T-1/1/review")
	r.p.Git(r.p.Root, "merge", "-q", "--no-ff", "-m", "Merge T-1: by hand", "task/T-1")
	r.waitStep("1 fact merged")
	var d struct{ Outside bool }
	for _, rec := range r.records() {
		if rec.Step == "merged" {
			_ = json.Unmarshal(rec.Data, &d)
		}
	}
	r.passes()
	if !d.Outside || r.scratch.publishes.Load() != 0 || r.has("1 intent after_merge") || r.has("1 intent archive") ||
		r.has("1 intent agent.stop") || r.mainMerges() != 1 {
		t.Errorf("outside %v, publishes %d, merges %d: %q", d.Outside, r.scratch.publishes.Load(), r.mainMerges(), r.steps())
	}
	if _, err := r.verdict(wire.MethodAccept, "c1", r.patch(job), ""); err == nil || !strings.Contains(err.Error(), "no job open") {
		t.Errorf("accept after the outside merge: %v", err)
	}
}

// obor serve restarts while the task waits for its accept: the replay settles the review prompt (never sent again),
// and the integrator's verb after the restart merges it.
func TestLoopRestartWhileAwaitingAccept(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
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
	if n := r.jobPrompts("T-1/1/review"); n != 1 || r.mainMerges() != 1 || r.scratch.gates.Load() != 1 {
		t.Errorf("prompts %d, merges %d, prepares %d; want 1 each", n, r.mainMerges(), r.scratch.gates.Load())
	}
}

// A pass that cannot build the loop for a project (a config the binary cannot read) is a fact (T-377): one needs-you
// "loop: <error>" and one notification however many passes fail, a changed error a new one, and the first pass that
// works ends it.
func TestLoopErrorIsAFactOnceAndClears(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
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

// T-425: while the main checkout is off the target branch the project's pass pauses: one loop needs-you, the
// task_done recorded (a request, T-437) but neither submitted nor gated; back on the branch the fact ends and the task gates.
func TestLoopPausedOffTargetBranch(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	set := func(err error) {
		r.mu.Lock()
		r.checkoutErr = err
		r.mu.Unlock()
	}
	const fact = "0 fact needs-you{loop: main checkout is on side, not main}"
	set(errors.New("main checkout is on side, not main"))
	r.work("feature.txt", "feature\n")
	r.start()
	r.submit()
	r.waitStep(fact)
	for i := 0; i < 3; i++ {
		r.passes()
	}
	if n := r.count(fact); n != 1 || !r.has("1 fact submitted") || r.scratch.submits.Load() != 0 || r.scratch.gates.Load() != 0 {
		t.Errorf("paused: %d needs-you, submits %d, gates %d; want 1, 0, 0: %q", n, r.scratch.submits.Load(),
			r.scratch.gates.Load(), r.steps())
	}

	set(nil)
	r.waitJob("pj-int", "T-1/1/review")
	if !r.has("0 fact resumed") || r.scratch.gates.Load() != 1 {
		t.Errorf("back on main: resumed %v, gates %d; want true, 1: %q", r.has("0 fact resumed"), r.scratch.gates.Load(), r.steps())
	}
}

// T-360 (RV-39 #6), T-440, T-521: the lane's commit and its merge take the task's title from the stored spec; the
// title the target has committed changes neither. The review job carries the gated spec rev's text; the merge's
// message carries it, and the gate writes no spec file. (A draft left in the main checkout's tasks dir holds the
// publish: drafts live in .obor/drafts/.)
func TestCommitSubjectFromStoredSpec(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	const committed = "# T-1 — Committed target title"
	r.p.Write("docs/tasks/T-1.md", committed+"\n")
	r.p.Git(r.p.Root, "add", "docs/tasks/T-1.md")
	r.p.Git(r.p.Root, "commit", "-qm", "spec T-1")
	r.work("feature.txt", "feature\n")
	r.start()
	const stored = "# T-1 — Stored title\n\nDifficulty: medium\n\n## Files you may touch\n`feature.txt`\n"
	r.setSpec("s1", stored)
	r.submit()
	r.waitStep("1 result submit")
	if got := r.p.Git(r.lane, "log", "-1", "--format=%s", "HEAD"); got != "T-1: Stored title" {
		t.Errorf("lane commit subject %q, want the stored spec's title", got)
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
	if got := r.p.Git(r.p.Root, "log", "-1", "--format=%B", "main"); got != "Merge T-1: Stored title\n\nSpec rev 1:\n\n"+strings.TrimRight(stored, "\n") {
		t.Errorf("merge message %q, want the stored spec's title, then rev 1 and its text", got)
	}
	if got := r.p.Git(r.p.Root, "show", "main:docs/tasks/T-1.md"); got != committed {
		t.Errorf("main's docs/tasks/T-1.md = %q, want the target's committed one (the gate writes no spec file)", got)
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
