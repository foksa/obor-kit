package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/checks"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// obor serve's merge loop as serve wires it (mergeLoop: checks.Scratch on the project's config, the herdr socket for
// the job prompts) on a git fixture, end to end: the real adapter in merge.scratch_dir, today's lane table,
// the fake herdr socket. Only the integrator's start, the worker's stop and the queue dispatch are the test's: they
// would reach real panes.

// serveRig is project proj (main with a 10-line shared.txt) with lane O-01 on task/T-1 (its worker pj-o1 idle in it)
// and roles.integrator (pj-int, not running until started); the app is not started yet.
type serveRig struct {
	t      *testing.T
	p      *testkit.Project
	lane   string
	f      *herdrtest.Fake
	srv    *herdrtest.Server
	starts atomic.Int32
	// gate wraps the adapter's Gate (nil: the adapter's own).
	gate func(ctx context.Context, s checks.Scratch, in app.GateInput) (app.GateOutcome, error)
	// alive is the adapter's GitAlive (nil: the system's).
	alive func(dir string) bool

	app    *app.App
	out    *bytes.Buffer
	mu     sync.Mutex // out
	sock   string
	cancel context.CancelFunc
	done   chan struct{}
}

func newServeRig(t *testing.T) *serveRig {
	t.Helper()
	p := testkit.New(t)
	p.Write(".gitignore", ".task-done.md\n.obor/state/\n")
	p.Write("shared.txt", "1\n"+sharedLines)
	p.Git(p.Root, "add", "-A")
	p.Git(p.Root, "commit", "-qm", "fixture")
	r := &serveRig{t: t, p: p, f: herdrtest.New(), out: &bytes.Buffer{}}
	r.lane = p.Lane("O-01", "task/T-1")
	p.Config(testkit.Set("roles.integrator", map[string]any{}),
		testkit.Set("commands", map[string]any{"check": "echo 1 pass"}))
	p.State("O-01", map[string]any{"lane": "O-01", "task": "T-1", "branch": "task/T-1", "agent": "pj-o1",
		"base": "main", "dispatched_at": 1})
	r.f.Workspaces = []herdr.Workspace{{ID: "w1", Label: "proj"}}
	w := r.f.Agent("w1:p1", "pj-o1", "idle", r.lane)
	w.Workspace, w.Label = "w1", "pj-o1" // labelled by its launch: serve's identity of it
	r.srv = herdrtest.Serve(t, r.f)
	r.srv.Reply("notification.show", map[string]any{"type": "notification_show", "shown": true, "reason": "shown"})
	r.srv.Reply("agent.prompt", map[string]any{"type": "agent_prompted", "agent": map[string]any{"name": "x",
		"pane_id": "w1:p1", "agent_status": "working"}})
	return r
}

const sharedLines = "2\n3\n4\n5\n6\n7\n8\n9\n10\n"

// Write keeps the app's output for a failure message.
func (r *serveRig) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.Write(b)
}

// serveDir is serve's own worktree: merge.scratch_dir (<Base>/proj-wt/merge).
func (r *serveRig) serveDir() string { return filepath.Join(r.p.Base, "proj-wt", "merge") }

// wrapped is the adapter with the rig's Gate.
type wrapped struct {
	checks.Scratch
	r *serveRig
}

func (w wrapped) Gate(ctx context.Context, in app.GateInput) (app.GateOutcome, error) {
	if w.r.gate != nil {
		return w.r.gate(ctx, w.Scratch, in)
	}
	return w.Scratch.Gate(ctx, in)
}

// start runs the app (a replay of the log so far) with serve's merge loop and its socket.
func (r *serveRig) start() {
	r.t.Helper()
	panes := herdr.Panes{S: r.srv.Client(), PS: func(context.Context) ([]byte, error) { return []byte("2 1 2\n"), nil }}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	wired := mergeLoop(r.p.Env(), panes, cancel) // cancel: serve's self-restart ends it as a signal does (T-656)
	r.app = app.New(app.Options{Root: r.p.Root, Tick: 20 * time.Millisecond, Retry: 5 * time.Millisecond,
		Out: r, Refresh: refreshed, Inboxed: inboxed(r.p.Env()), Loop: func(root string) (*app.Loop, error) {
			l, err := wired(root)
			if err != nil {
				return nil, err
			}
			s, ok := l.Scratch.(checks.Scratch)
			if !ok {
				return nil, errors.New("serve's merge loop is not on checks.Scratch")
			}
			// Git with the fixture's identity; progress lines are noise here.
			s.Environ = append(os.Environ(), "GIT_AUTHOR_NAME=obor test", "GIT_AUTHOR_EMAIL=obor-test@example.invalid",
				"GIT_COMMITTER_NAME=obor test", "GIT_COMMITTER_EMAIL=obor-test@example.invalid")
			s.Out, s.GitAlive = io.Discard, r.alive
			l.Scratch, l.Agents = wrapped{s, r}, r
			// No low-disk watch (T-606): it would hold this loop on the host's free space, a gate that runs while
			// the host is near the 5 GiB bound then fails to start its jobs; the guard has its own tests (internal/app).
			l.Disk = nil
			return l, nil
		}}, panes, laneTable{})
	go func() {
		defer close(r.done)
		_ = r.app.Serve(ctx)
	}()
	r.t.Cleanup(r.stop)
	dir, err := os.MkdirTemp("", "hs") // short: a unix socket path
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { os.RemoveAll(dir) })
	r.sock = filepath.Join(dir, "s")
	ln, err := net.Listen("unix", r.sock)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { ln.Close() })
	go r.app.Listen(ln)
}

// stop ends the app; Serve returns once its merge loop has stopped.
func (r *serveRig) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	<-r.done
	r.cancel = nil
}

// Restart is the integrator's start: pj-int appears in the project's workspace and the app sees it.
func (r *serveRig) Launch(_ context.Context, req app.Launch) (app.Started, error) {
	r.starts.Add(1)
	if req.Agent != "pj-int" {
		return app.Started{}, errors.New("unexpected start of " + req.Agent)
	}
	if !r.f.Set("w1:p9", func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status = "claude", "pj-int", "idle" }) {
		in := r.f.Agent("w1:p9", "pj-int", "idle", r.p.Root)
		in.Workspace, in.Label = "w1", "pj-int"
	}
	return app.Started{Line: "STARTED pj-int"}, r.seen("pj-int", true)
}

// Stop is the worker's stop: the agent leaves its pane and the app sees it gone.
func (r *serveRig) Stop(_ context.Context, agent, pane string, _ bool) (string, error) {
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
	return "STOPPED " + agent, r.seen(agent, false)
}

// seen waits until the app's snapshot has agent (or, !want, has it no more): a workspace event makes it read anew.
func (r *serveRig) seen(agent string, want bool) error {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		has := false
		for _, a := range r.app.Status().Project.Agents {
			has = has || a.Name == agent
		}
		if has == want {
			return nil
		}
		r.srv.Push("workspace_created", map[string]any{"type": "workspace_created",
			"workspace": map[string]any{"workspace_id": "w1", "label": "proj"}})
	}
	return errors.New(agent + " never seen")
}

func (r *serveRig) waitFor(what string, ok func() bool) {
	r.t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.t.Fatalf("timed out waiting for %s; records:\n%s\napp output:\n%s", what, strings.Join(r.steps(), "\n"), r.out)
}

// steps is the log as "<attempt> <kind> <step>[{reason}]" lines.
func (r *serveRig) steps() []string {
	recs, _ := app.ReadLog(r.p.Root)
	var out []string
	for _, rec := range recs {
		out = append(out, strings.Join([]string{string(rune('0' + rec.Attempt)), rec.Kind, rec.String()}, " "))
	}
	return out
}

func (r *serveRig) count(step string) int {
	n := 0
	for _, s := range r.steps() {
		if s == step {
			n++
		}
	}
	return n
}

func (r *serveRig) waitStep(step string) {
	r.t.Helper()
	r.waitFor("record "+step, func() bool { return r.count(step) > 0 })
}

// job is the text of the prompt to pj-int for job (waited for).
func (r *serveRig) job(job string) string {
	r.t.Helper()
	var text string
	n := 0
	r.waitFor("job "+job+" fetched", func() bool {
		n++
		raw, err := app.Verdict(context.Background(), r.sock, app.MethodJobNext, app.Call{Root: r.p.Root,
			Agent: "pj-int", Role: "integrator", CallID: job + "-" + strconv.Itoa(n)})
		var jr app.JobReply
		if err != nil || json.Unmarshal([]byte(raw), &jr) != nil || jr.Job != job {
			return false
		}
		text = "[obor job " + job + "] " + jr.Text
		return true
	})
	return text
}

var servePatchRe = regexp.MustCompile(`Patch hash: ([0-9a-f]{64})`)

// accept answers the review job text with an accept of the patch it names.
func (r *serveRig) accept(callID, text string) {
	r.t.Helper()
	m := servePatchRe.FindStringSubmatch(text)
	if m == nil {
		r.t.Fatalf("no patch hash in the review job:\n%s", text)
	}
	r.verdict(app.MethodAccept, callID, m[1], "ACCEPT T-1")
}

// verdict is pj-int's verdict on T-1; its reply starts with want.
func (r *serveRig) verdict(method, callID, patch, want string) {
	r.t.Helper()
	reply, err := app.Verdict(context.Background(), r.sock, method, app.Call{Root: r.p.Root, Agent: "pj-int",
		Role: "integrator", CallID: callID, Task: "T-1", Patch: patch})
	if err != nil || !strings.HasPrefix(reply, want) {
		r.t.Fatalf("%s: %q, %v; want %s…", method, reply, err, want)
	}
}

// merged waits for the merge's after-merge work (its last step, the worker's stop) and checks the end state: one
// merge of T-1 on main, its fact merged (main's head; no merges.log, T-525), no done-file archive, no pin left.
func (r *serveRig) merged() {
	r.t.Helper()
	r.waitStep("1 result agent.stop")
	n := 0
	for _, s := range strings.Split(r.p.Git(r.p.Root, "log", "--first-parent", "--format=%s", "main"), "\n") {
		if strings.HasPrefix(s, "Merge T-1") {
			n++
		}
	}
	if n != 1 {
		r.t.Errorf("main has %d merges of T-1, want 1", n)
	}
	head := r.p.Git(r.p.Root, "rev-parse", "main")
	if ms, err := app.ReadMerges(r.p.Root); err != nil || len(ms) != 1 || ms[0].Task != "T-1" || ms[0].SHA != head ||
		r.p.Exists(".obor/state/merges.log") {
		r.t.Errorf("merges %+v, %v, merges.log %v; want T-1's at %s, no merges.log", ms, err,
			r.p.Exists(".obor/state/merges.log"), head)
	}
	if r.p.Exists(".obor/state/done") {
		r.t.Errorf("a done-file archive dir (gone with T-437)")
	}
	if pins := r.p.Git(r.p.Root, "for-each-ref", "refs/obor/"); pins != "" {
		r.t.Errorf("pins left after the publish:\n%s", pins)
	}
}

// From a task_done to merged through serve's own worktree: the lane's work is committed and gated there, the
// integrator's accept publishes it.
func TestServeLoopHappyPath(t *testing.T) {
	r := newServeRig(t)
	r.p.Write(filepath.Join(r.lane, "feature.txt"), "feature\n")
	r.p.Submit("O-01") // T-1 dispatched and its task_done in, before serve replays the log
	r.start()
	base := r.p.Git(r.p.Root, "rev-parse", "main")
	job := r.job("T-1/1/review")
	if r.p.Git(r.p.Root, "rev-parse", "main") != base {
		t.Fatal("main moved before the accept")
	}
	if got := r.p.Git(r.serveDir(), "log", "-1", "--format=%s"); got != "Merge T-1: done." {
		t.Errorf("serve's worktree HEAD %q, want the gate's merge", got)
	}
	r.accept("c1", job)
	r.merged()
	if !r.p.Exists("feature.txt") || r.p.Git(r.p.Root, "log", "-1", "--format=%s", "main") != "Merge T-1: done." {
		t.Errorf("main lacks the lane's feature or its merge subject")
	}
	if r.p.Exists(r.serveDir() + "-serve") {
		t.Errorf("serve used its pre-T-409 worktree")
	}
	if r.starts.Load() != 1 {
		t.Errorf("integrator starts = %d, want 1", r.starts.Load())
	}
}

// A conflict is a resolve job naming serve's worktree (no --ff-retry): the integrator commits the resolution there,
// says resolved, the app gates it, then review, accept, merged with the resolution.
func TestServeLoopConflictResolved(t *testing.T) {
	r := newServeRig(t)
	r.p.Write(filepath.Join(r.lane, "shared.txt"), "lane\n"+sharedLines)
	r.p.Write("shared.txt", "main\n"+sharedLines)
	r.p.Git(r.p.Root, "commit", "-qam", "main edit")
	r.p.Submit("O-01") // T-1 dispatched and its task_done in, before serve replays the log
	r.start()
	job := r.job("T-1/1/resolve")
	if !strings.Contains(job, "CONFLICT in "+r.serveDir()+": shared.txt") || strings.Contains(job, "--ff-retry") {
		t.Errorf("resolve job does not name serve's worktree, or names --ff-retry:\n%s", job)
	}
	r.p.Write(filepath.Join(r.serveDir(), "shared.txt"), "both\n"+sharedLines)
	r.p.Git(r.serveDir(), "add", "shared.txt")
	r.p.Git(r.serveDir(), "commit", "-qm", "Merge T-1: done.")
	r.verdict(app.MethodResolved, "c1", "", "RESOLVED T-1")
	r.accept("c2", r.job("T-1/1/review"))
	r.merged()
	if got := r.p.Read("shared.txt"); got != "both\n"+sharedLines {
		t.Errorf("main's shared.txt %q, want the resolution", got)
	}
}

// T-403: the target moves between the accept and the publish. The moved gate merges the lane's submitted commit (not
// the old candidate): main gets one merge commit for T-1, whose second parent is the lane's, and the same patch keeps
// the accept (no second review).
func TestServeLoopMovedTarget(t *testing.T) {
	r := newServeRig(t)
	r.p.Write(filepath.Join(r.lane, "feature.txt"), "feature\n")
	r.p.Submit("O-01") // T-1 dispatched and its task_done in, before serve replays the log
	r.start()
	job := r.job("T-1/1/review")
	submitted := r.p.Git(r.lane, "rev-parse", "HEAD")
	r.p.Write("other.txt", "other\n")
	r.p.Git(r.p.Root, "add", "other.txt")
	r.p.Git(r.p.Root, "commit", "-qm", "unrelated")
	r.accept("c1", job)
	r.merged()
	if got := r.p.Git(r.p.Root, "rev-list", "--count", "--merges", "main"); got != "1" {
		t.Errorf("%s merge commits on main, want one for T-1 (T-403)", got)
	}
	if got := r.p.Git(r.p.Root, "rev-parse", "main^2"); got != submitted {
		t.Errorf("main's merge's second parent %s, want the lane's submitted %s", got, submitted)
	}
	if r.count("1 fact target.moved") != 1 || r.count("1 fact accept.kept") != 1 {
		t.Errorf("moved %d, kept %d; want the moved target kept, no second review: %q", r.count("1 fact target.moved"),
			r.count("1 fact accept.kept"), r.steps())
	}
}

// The flake of T-382: obor's own lane read (git status, each tick) holds the lane's index.lock while submit's git add
// runs. Here a live git holds it at the submit: submit waits for it, nothing counted, merged as without the lock.
func TestServeLoopLaneLockNotCounted(t *testing.T) {
	r := newServeRig(t)
	r.p.Write(filepath.Join(r.lane, "feature.txt"), "feature\n")
	lock := r.p.Git(r.lane, "rev-parse", "--path-format=absolute", "--git-path", "index.lock")
	r.p.Write(lock, "")
	old := time.Now().Add(-2 * time.Minute) // past the stale bound: only its live holder keeps it
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	var asked atomic.Int32
	r.alive = func(string) bool {
		if asked.Add(1) == 2 { // the holder ends after a look
			os.Remove(lock)
		}
		return true
	}
	r.p.Submit("O-01") // T-1 dispatched and its task_done in, before serve replays the log
	r.start()
	r.accept("c1", r.job("T-1/1/review"))
	r.merged()
	if asked.Load() < 2 {
		t.Errorf("precondition: the submit never met the lock (asked %d times)", asked.Load())
	}
	for _, s := range r.steps() {
		if strings.Contains(s, " fact failed") {
			t.Errorf("the lock was counted as a failure: %s", s)
		}
	}
}

// obor serve dies between the gate's commit (pinned candidate) and its result: the restart gates again and ends as
// without the crash, on the same candidate: one gate result, one merge, the pinned sha published.
func TestServeLoopCrashAfterGateCommit(t *testing.T) {
	r := newServeRig(t)
	r.p.Write(filepath.Join(r.lane, "feature.txt"), "feature\n")
	var crashed atomic.Bool
	var first app.GateOutcome
	r.gate = func(ctx context.Context, s checks.Scratch, in app.GateInput) (app.GateOutcome, error) {
		o, err := s.Gate(ctx, in)
		if err == nil && crashed.CompareAndSwap(false, true) {
			first = o
			r.cancel() // the process died: the result is never written
		}
		return o, err
	}
	r.p.Submit("O-01") // T-1 dispatched and its task_done in, before serve replays the log
	r.start()
	<-r.done
	r.cancel = nil
	if !crashed.Load() || first.Kind != app.OutcomeGreen {
		t.Fatalf("precondition: no green gate before the crash (%+v)", first)
	}
	if pin := r.p.Git(r.p.Root, "rev-parse", "refs/obor/candidate/T-1"); pin != first.Candidate.SHA {
		t.Fatalf("precondition: candidate pin %s, want the gate's commit %s", pin, first.Candidate.SHA)
	}
	if n := r.count("1 result gate"); n != 0 || r.count("1 intent gate") != 1 {
		t.Fatalf("precondition: the gate's result was written, or no intent: %q", r.steps())
	}

	r.start()
	r.accept("c1", r.job("T-1/1/review"))
	r.merged()
	if n := r.count("1 result gate"); n != 1 {
		t.Errorf("gate results %d, want 1: %q", n, r.steps())
	}
	for _, s := range r.steps() {
		if strings.Contains(s, " fact failed") {
			t.Errorf("the crash was counted as a failure: %s", s)
		}
	}
	if head := r.p.Git(r.p.Root, "rev-parse", "main"); head != first.Candidate.SHA {
		t.Errorf("main at %s, want the candidate gated before the crash %s", head, first.Candidate.SHA)
	}
}
