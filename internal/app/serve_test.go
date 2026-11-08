package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/run"
	"obor/internal/store"
)

// A load that fails (no config yet) leaves serve with no project and says why; the next loadProject loads it, and a
// later one does not load it again (the same project goes on).
func TestLoadProjectRetriesAFailedLoad(t *testing.T) {
	t.Parallel()
	root := projectRoot(t)
	cfg := filepath.Join(root, ".obor", "config.json")
	if err := os.Rename(cfg, cfg+".off"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a := rootApp(root, &out)
	a.loadProject()
	if ps := a.Status().Project; ps != nil || !strings.Contains(out.String(), "project "+root+": ") {
		t.Fatalf("failed load: project %+v, output %q; want none and the error", ps, out.String())
	}
	if err := os.Rename(cfg+".off", cfg); err != nil {
		t.Fatal(err)
	}
	a.loadProject()
	defer a.closeLog()
	p := a.p
	if p == nil || p.name != "pj" {
		t.Fatalf("second load: %+v; want pj loaded", p)
	}
	a.loadProject()
	if ps := a.Status().Project; a.p != p || ps == nil || ps.Name != "pj" {
		t.Errorf("third load replaced the project: %+v", ps)
	}
}

// An append failure shows in the project's status (log_error) while it stays; a later good append clears it.
func TestLogAppendErrorInStatus(t *testing.T) {
	t.Parallel()
	root := projectRoot(t)
	a := rootApp(root, &bytes.Buffer{})
	a.loadProject()
	defer a.closeLog()
	p := a.p
	if p == nil {
		t.Fatal("precondition: pj loaded")
	}
	if e := a.Status().Project.LogError; e != "" {
		t.Fatalf("precondition: no log error; got %q", e)
	}
	// The insert fails in SQLite itself, as a full disk's would.
	if _, err := p.log.DB().Exec(`CREATE TRIGGER full BEFORE INSERT ON events
		BEGIN SELECT RAISE(ABORT, 'no space left on device'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.log.Append(event.Record{Task: "T-1", Attempt: 1, Step: "x", Kind: event.KindFact}, time.Now()); err == nil {
		t.Fatal("precondition: the append failed")
	}
	if e := a.Status().Project.LogError; !strings.Contains(e, "no space left on device") {
		t.Errorf("log_error %q; want the append's error", e)
	}
	if _, err := p.log.DB().Exec("DROP TRIGGER full"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.log.Append(event.Record{Task: "T-1", Attempt: 1, Step: "x", Kind: event.KindFact}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if e := a.Status().Project.LogError; e != "" {
		t.Errorf("log_error %q after a good append; want none", e)
	}
}

// RV-38 #9: a needs-you that pauses T-1's conflict or resolve job keeps the scratch reserved, across a reload: a
// submitted T-2 is not gated (the gate would reset the worktree holding T-1's resolution). T-1 resumed, its resolved
// verdict pins with T-1's own input (submit's s1); once T-1's resolution is gated green, T-2 is.
func TestPausedScratchReservation(t *testing.T) {
	t.Parallel()
	for _, cause := range []string{event.CauseHeld, "retry.resolve failed", "start.resolve failed"} {
		t.Run(cause, func(t *testing.T) {
			t.Parallel()
			h, f := seedSched(t, true)
			ctx := context.Background()
			if cause != "start.resolve failed" { // the resolve job is out
				f.Launch(ctx, port.Launch{})
				h.pass(ctx)
			}
			p := h.a.p
			h.a.needsYou(p, p.folded.loop["T-1"], cause, cause)
			addRecord(t, h, "T-2", event.StepSubmitted, event.KindFact, submitData{Lane: "L-02"})
			addRecord(t, h, "T-2", event.StepSubmit, event.KindResult, submitOut{SHA: "s2"})
			p = reloadProj(t, h)
			mt := p.folded.loop["T-1"]
			want := stResolving
			if cause == "start.resolve failed" {
				want = stConflict
			}
			if !mt.onHold() || mt.state != want {
				t.Fatalf("precondition: T-1 %s (held %v), want a needs-you held on %s", mt.state, mt.onHold(), want)
			}
			if p.folded.loop["T-2"].state != stSubmitted || p.folded.loop["T-2"].input != "s2" {
				t.Fatalf("precondition: T-2 submitted at s2: %+v", *p.folded.loop["T-2"])
			}

			h.pass(ctx)
			if len(f.gated) != 0 {
				t.Fatalf("paused: gated %v; want none (T-1 holds the scratch)", f.gated)
			}
			if _, err := h.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Role: "lead", CallID: "r"},
				Task: "T-1", Text: "go on"}); err != nil {
				t.Fatal(err)
			}
			h.pass(ctx)
			if mt := p.folded.loop["T-1"]; mt.state != stResolving || len(f.gated) != 0 {
				t.Fatalf("resumed: T-1 %s, gated %v; want resolving, none", mt.state, f.gated)
			}
			job := JobID(p.folded.loop["T-1"].jobKey())
			if _, err := h.a.verdict(ctx, wire.VerdictReq{Envelope: wire.Envelope{Root: h.root, Role: "integrator",
				Agent: "pj-int", CallID: "v"}, Verb: wire.MethodResolved, Task: "T-1", Job: job}); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(f.resolved, []string{"s1"}) {
				t.Errorf("Resolved inputs %v; want [s1], T-1's submitted sha", f.resolved)
			}
			h.pass(ctx)
			if !slices.Equal(f.gated, []string{"T-1", "T-2"}) {
				t.Errorf("after T-1's resolution: gated %v; want [T-1 T-2]", f.gated)
			}
		})
	}
}

// RV-38 #9: abandoning the paused work releases the scratch: regate, reject, a new attempt.
func TestPausedScratchReleased(t *testing.T) {
	t.Parallel()
	for _, release := range []string{"regate", "reject", "sendback"} {
		t.Run(release, func(t *testing.T) {
			t.Parallel()
			h, f := seedSched(t, true)
			ctx := context.Background()
			f.Launch(ctx, port.Launch{})
			h.pass(ctx)
			p := h.a.p
			cause := event.CauseHeld
			if release == "regate" { // T-775: a hold takes the decision; --regate is the owner's choice past K
				cause = causeSendbacks
			}
			h.a.needsYou(p, p.folded.loop["T-1"], cause, "held")
			addRecord(t, h, "T-2", event.StepSubmitted, event.KindFact, submitData{Lane: "L-02"})
			addRecord(t, h, "T-2", event.StepSubmit, event.KindResult, submitOut{SHA: "s2"})
			if !busy(p, "T-2") {
				t.Fatal("precondition: the paused T-1 holds the scratch")
			}
			switch release {
			case "regate":
				if _, err := h.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Role: "lead", CallID: "r"},
					Task: "T-1", Flag: wire.ResumeRegate}); err != nil {
					t.Fatal(err)
				}
			case "reject":
				if _, err := h.a.verdict(ctx, wire.VerdictReq{Envelope: wire.Envelope{Root: h.root, Role: "integrator",
					Agent: "pj-int", CallID: "v"}, Verb: wire.MethodReject, Task: "T-1", Note: "cannot"}); err != nil {
					t.Fatal(err)
				}
			case "sendback":
				addRecord(t, h, "T-1", event.StepSendback, event.KindFact, noteData{Note: "fix"})
			}
			if busy(p, "T-2") {
				t.Fatalf("%s: T-1 (%s, %+v) still holds the scratch", release, p.folded.loop["T-1"].state, p.folded.loop["T-1"].needs)
			}
		})
	}
}

// Without an integrator role the project is observe-only (no merge loop, Merging false, so Owns is false and the
// heartbeat's done lanes wait: serve down); with one, the loop runs.
func TestMergeLoopNeedsIntegrator(t *testing.T) {
	t.Parallel()
	for _, serve := range []bool{false, true} {
		h, f := seedSched(t, serve)
		p := h.a.p
		if p.merging != serve || h.a.Status().Project.Merging != serve {
			t.Errorf("integrator %v: merging %v, status %v", serve, p.merging, h.a.Status().Project.Merging)
		}
		if serve {
			h.pass(context.Background())
			if st := p.folded.loop["T-1"].state; st != stResolving {
				t.Errorf("integrator: T-1 %s; want resolving (its resolve job sent)", st)
			}
			continue
		}
		addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01"})
		addRecord(t, h, "T-1", event.StepSubmit, event.KindResult, submitOut{SHA: "s1"})
		h.pass(context.Background())
		if len(f.gated) != 0 {
			t.Errorf("no integrator: gated %v; want observe-only", f.gated)
		}
	}
}

// listenMem starts the real socket dispatch (Listen) on an in-memory listener; call is one request and its answer.
func listenMem(t *testing.T, h *harness) func(c wire.Req) (string, error) {
	ln := newMemListener()
	t.Cleanup(func() { ln.Close() })
	go h.a.Listen(ln)
	return func(c wire.Req) (string, error) {
		client := ln.send(t, c)
		defer client.Close()
		ans := read(t, client)
		if ans.Error != "" {
			return ans.Reply, errors.New(ans.Error)
		}
		return ans.Reply, nil
	}
}

// blockScratch is schedScratch whose Resolved blocks until its ctx ends: entered gets the ctx, ended is set once
// Resolved has returned (its cleanup a moment after the cancellation).
type blockScratch struct {
	*schedScratch
	entered chan context.Context
	ended   atomic.Bool
}

func (f *blockScratch) Resolved(ctx context.Context, _, _ string, _ *port.Mismatch) (string, error) {
	f.entered <- ctx
	<-ctx.Done()
	time.Sleep(50 * time.Millisecond) // the process group's cleanup
	f.ended.Store(true)
	return "", ctx.Err()
}

// resolveJobHarness is seedSched with T-1's resolve job out (the integrator started): T-1 resolving, its job id.
func resolveJobHarness(t *testing.T) (*harness, *schedScratch, string) {
	t.Helper()
	h, f := seedSched(t, true)
	f.Launch(context.Background(), port.Launch{})
	h.pass(context.Background())
	mt := h.a.p.folded.loop["T-1"]
	if mt.state != stResolving || !mt.openJob() {
		t.Fatalf("precondition: T-1 %s, open job %v; want resolving with its job open", mt.state, mt.openJob())
	}
	return h, f, JobID(mt.jobKey())
}

// RV-38-verify #12: a socket resolved runs its effect under the daemon's lifetime (with a request deadline):
// Serve canceled, the blocked effect's ctx is canceled and Serve returns only after the effect has ended.
func TestSocketEffectEndsWithServe(t *testing.T) {
	t.Parallel()
	h, f, job := resolveJobHarness(t)
	b := &blockScratch{schedScratch: f, entered: make(chan context.Context, 1)}
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Workflow: WorkflowOf(b), Agents: f, Typer: f}, nil }
	h.a.opt.Retry = time.Millisecond // Serve's subscribe fails on fakePanes: it retries until canceled
	ln := newMemListener()
	defer ln.Close()
	go h.a.Listen(ln)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = h.a.Serve(ctx)
	}()

	client := ln.send(t, &wire.VerdictReq{Envelope: wire.Envelope{Root: h.root, Role: "integrator", Agent: "pj-int",
		CallID: "v"}, Verb: wire.MethodResolved, Task: "T-1", Job: job})
	defer client.Close()
	answered := make(chan string, 1) // the answer is read as it comes: net.Pipe does not buffer the handler's write
	go func() {
		_ = client.SetDeadline(time.Now().Add(10 * time.Second))
		b, _ := bufio.NewReader(client).ReadBytes('\n')
		answered <- string(b)
	}()
	var effect context.Context
	select {
	case effect = <-b.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("precondition: the authorized resolved never reached Scratch.Resolved")
	}
	if effect.Done() == nil {
		t.Fatal("the effect's ctx cannot be canceled (Background)")
	}
	if _, ok := effect.Deadline(); !ok {
		t.Error("the effect's ctx has no request deadline")
	}
	select {
	case <-served:
		t.Fatal("precondition: Serve returned before it was canceled")
	default:
	}

	cancel()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after its cancellation")
	}
	if effect.Err() == nil {
		t.Error("Serve returned; the effect's ctx is not canceled")
	}
	if !b.ended.Load() {
		t.Error("Serve returned before the request's effect ended")
	}
	if ans := <-answered; !strings.Contains(ans, "context canceled") {
		t.Errorf("the canceled resolved's answer: %q; want its cancellation", ans)
	}
}

// T-659: Loaded is closed only once the project's log is replayed (obor serve binds its socket then), and a request
// that reaches an app before its load is answered at once, never held for the replay.
func TestLoadedAfterReplay(t *testing.T) {
	t.Parallel()
	root := projectRoot(t)
	out := &bytes.Buffer{}
	a := rootApp(root, out)
	defer a.closeLog()
	select {
	case <-a.Loaded():
		t.Fatal("Loaded before the project was loaded")
	default:
	}
	ln := newMemListener()
	defer ln.Close()
	go a.Listen(ln)
	start := time.Now()
	c := ln.send(t, &wire.SpecSetReq{Envelope: wire.Envelope{Root: root, CallID: "c1"}, Task: "T-1",
		Spec: "# T-1 — Other\n\nDifficulty: medium\n"})
	defer c.Close()
	if ans := read(t, c); !strings.Contains(ans.Error, "no served project") {
		t.Errorf("a request before the load: %+v; want the refusal", ans)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("refused after %v; want at once (no request waits for the replay)", d)
	}
	a.loadProject()
	select {
	case <-a.Loaded():
	default:
		t.Fatal("Loaded not closed after the project loaded")
	}
	if !regexp.MustCompile(`replayed \d+ records in \d+\.\d\d s`).MatchString(out.String()) {
		t.Errorf("the load's line names no replay time:\n%s", out.String())
	}
}

// serveHarness writes pj's config with the merge loop on (an integrator role) or off (none) and reloads it (a serve
// restart).
func serveHarness(t *testing.T, h *harness, on bool) *proj {
	t.Helper()
	cfg := `{"name":"pj","prefix":"pj","roles":{"integrator":""}}`
	if !on {
		cfg = `{"name":"pj","prefix":"pj"}`
	}
	h.write(filepath.Join(h.root, ".obor", "config.json"), cfg)
	p := reloadProj(t, h)
	if p.merging != on || h.a.Status().Project.Merging != on {
		t.Fatalf("precondition: merge loop %v: merging %v, status %v", on, p.merging, h.a.Status().Project.Merging)
	}
	return p
}

// countRecords is the number of records in pj's event log.
func countRecords(t *testing.T, h *harness) int {
	t.Helper()
	recs, err := event.ReadLog(h.root)
	if err != nil {
		t.Fatal(err)
	}
	return len(recs)
}

const mergeOffReply = "merge loop off for pj (no integrator role)"

// RV-38-verify #13: an open resolve job, the merge loop turned off (the integrator role removed) and reloaded: its resolved is refused before any
// effect or record (no Scratch call, no new record, T-1 still resolving), a call id answered while on gets its stored
// reply; turned on again, the same resolved is answered.
func TestMergeLoopOffRefusesOldVerdict(t *testing.T) {
	t.Parallel()
	h, f, job := resolveJobHarness(t)
	call := listenMem(t, h)
	held := &wire.VerdictReq{Verb: wire.MethodHold, Envelope: wire.Envelope{Root: h.root, Role: "integrator",
		Agent: "pj-int", CallID: "h1"},
		Task: "T-1", Job: job, Note: "ask"}
	want, err := call(held)
	if err != nil {
		t.Fatalf("precondition: hold while on: %v", err)
	}

	p := serveHarness(t, h, false)
	n := countRecords(t, h)
	resolved := &wire.VerdictReq{Verb: wire.MethodResolved, Envelope: wire.Envelope{Root: h.root, Role: "integrator",
		Agent:  "pj-int",
		CallID: "v"}, Task: "T-1", Job: job}
	if reply, err := call(resolved); err == nil || err.Error() != mergeOffReply || reply != "" {
		t.Errorf("resolved while off: %q, %v; want the refusal %q", reply, err, mergeOffReply)
	}
	if len(f.resolved) != 0 {
		t.Errorf("Scratch.Resolved called while off: %v", f.resolved)
	}
	if got := countRecords(t, h); got != n {
		t.Errorf("records while off: %d, want %d (none new)", got, n)
	}
	if mt := p.folded.loop["T-1"]; !mt.onHold() || mt.state != stResolving || !mt.openJob() {
		t.Errorf("T-1 while off: %s (held %v, open %v); want unchanged: held, its resolve job open", mt.state, mt.onHold(),
			mt.openJob())
	}
	if reply, err := call(held); err != nil || reply != want {
		t.Errorf("repeated hold h1 while off: %q, %v; want its stored reply %q", reply, err, want)
	}

	p = serveHarness(t, h, true)
	reply, err := call(resolved)
	if err != nil || !strings.HasPrefix(reply, "RESOLVED T-1") || len(f.resolved) != 1 {
		t.Errorf("resolved on again: %q, %v, Resolved %v; want it answered", reply, err, f.resolved)
	}
	if mt := p.folded.loop["T-1"]; mt.state != stResolved {
		t.Errorf("T-1 on again: %s; want resolved", mt.state)
	}
}

// RV-38-verify #13: all six calls on a job (or needs-you) open before the merge loop is turned off are refused while off
// (no Scratch call, no new record, no state change) and answered once it is on again.
func TestMergeLoopOffRefusesAllCalls(t *testing.T) {
	t.Parallel()
	for _, method := range []string{wire.MethodAccept, wire.MethodSendback, wire.MethodReject, wire.MethodHold, wire.MethodResume, wire.MethodResolved} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			var h *harness
			var f *schedScratch
			var job string
			if method == wire.MethodResolved || method == wire.MethodResume {
				h, f, job = resolveJobHarness(t)
			} else { // a review job: T-1 gated green, its review job out
				h, f = seedSched(t, false)
				serveHarness(t, h, true)
				addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01", Pane: "w1:p1"})
				addRecord(t, h, "T-1", event.StepSubmit, event.KindResult, submitOut{SHA: "s1"})
				f.Launch(context.Background(), port.Launch{})
				h.pass(context.Background())
				mt := h.a.p.folded.loop["T-1"]
				if mt.state != stAwaiting || !mt.openJob() {
					t.Fatalf("precondition: T-1 %s, open %v; want awaiting-accept with its review job", mt.state, mt.openJob())
				}
				job = JobID(mt.jobKey())
			}
			var c wire.Req = &wire.VerdictReq{Verb: method, Envelope: wire.Envelope{Root: h.root, Role: "integrator",
				Agent:  "pj-int",
				CallID: "c"}, Task: "T-1", Job: job, Note: "why", Patch: "p"}
			if method == wire.MethodResume {
				p := h.a.p
				h.a.needsYou(p, p.folded.loop["T-1"], event.CauseHeld, "held")
				c = &wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Role: "lead", CallID: "c"}, Task: "T-1",
					Text: "go on"}
			}
			call := listenMem(t, h)

			p := serveHarness(t, h, false)
			n, gated := countRecords(t, h), len(f.gated)
			before := *p.folded.loop["T-1"]
			if reply, err := call(c); err == nil || err.Error() != mergeOffReply || reply != "" {
				t.Errorf("%s while off: %q, %v; want the refusal %q", method, reply, err, mergeOffReply)
			}
			if len(f.resolved) != 0 || len(f.gated) != gated {
				t.Errorf("%s while off: Scratch called (Resolved %v, gated %v)", method, f.resolved, f.gated)
			}
			if got := countRecords(t, h); got != n {
				t.Errorf("%s while off: %d records, want %d (none new)", method, got, n)
			}
			if mt := p.folded.loop["T-1"]; mt.state != before.state || mt.needs.fact.Seq != before.needs.fact.Seq || mt.job != before.job {
				t.Errorf("%s while off: T-1 %s (needs %d, job %s); want %s (needs %d, job %s)", method, mt.state,
					mt.needs.fact.Seq, mt.job, before.state, before.needs.fact.Seq, before.job)
			}

			serveHarness(t, h, true)
			if reply, err := call(c); err != nil || reply == "" {
				t.Errorf("%s on again: %q, %v; want it answered", method, reply, err)
			}
		})
	}
}

// T-656: serve's self-restart is a graceful exit, not a `launchctl kickstart -k` (a SIGKILL) on itself. The restarter
// (cmd/obor's serveRestarter) cancels serve's context; serve then ends as on a signal — here with a gate running in a
// process group of its own, as checks.Scratch runs one — and Serve returns only after that group is gone.

// openPanes is herdr that is there: a subscription whose stream stays open until its ctx ends.
type openPanes struct {
	*fakePanes
	snap port.Snapshot
}

func (o *openPanes) Subscribe(ctx context.Context) (*port.Stream, error) {
	c := make(chan port.Event)
	go func() {
		<-ctx.Done()
		close(c)
	}()
	return &port.Stream{Snapshot: o.snap, C: c, Err: func() error { return ctx.Err() }, Close: func() {}}, nil
}

// groupGate is a Scratch whose Gate runs a real command in its own process group (as checks.Scratch does) and returns
// once its ctx ended: a check still running when serve stops.
type groupGate struct {
	*schedScratch
	dir string
}

func (g *groupGate) Gate(ctx context.Context, in port.GateInput) (port.GateOutcome, error) {
	// The child writes both pids before the gate waits: the group's leader (the bash run.Execute started) and a sleep
	// of its own, which the TERM to the group reaches only through the group.
	script := `sleep 60 & echo $! > child.pid; echo $$ > pid; wait`
	run.Execute(ctx, config.Command{Name: "gate", Cmd: script, Timeout: 60}, g.dir, nil, nil, io.Discard, io.Discard)
	g.gated = append(g.gated, in.Task)
	return port.GateOutcome{}, errors.New("the gate was cut off: serve is stopping")
}

// gateGroupHarness is pj with an integrator, lane L-01's T-1 submitted and its sha in: T-1's next step is its gate, run
// by g.
func gateGroupHarness(t *testing.T, dir string) (*harness, *groupGate) {
	t.Helper()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","roles":{"integrator":""}}`)
	h.a = New(Options{Root: h.root, Now: func() time.Time { return h.now }, Tick: 20 * time.Millisecond,
		Retry: 5 * time.Millisecond}, &openPanes{fakePanes: h.panes, snap: h.snap}, h.lanes)
	g := &groupGate{schedScratch: &schedScratch{h: h}, dir: dir}
	h.a.opt.Loop = func(string) (*Loop, error) {
		return &Loop{Workflow: WorkflowOf(g), Agents: g.schedScratch, Typer: g.schedScratch}, nil
	}
	if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.connect()
	h.a.mu.Lock()
	h.a.p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"} // the lane holds T-1
	h.a.mu.Unlock()
	addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01", Pane: "w1:p1"})
	addRecord(t, h, "T-1", event.StepSubmit, event.KindResult, submitOut{SHA: "s1"})
	return h, g
}

// pidIn is the pid a test command wrote to path (waited for).
func pidIn(t *testing.T, path string) int {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid > 1 {
				return pid
			}
		}
	}
	t.Fatalf("no pid written to %s", path)
	return 0
}

func TestSelfRestartEndsServeAndItsGateGroup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h, g := gateGroupHarness(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.a.Serve(ctx) }()
	pid := pidIn(t, filepath.Join(dir, "pid"))
	child := pidIn(t, filepath.Join(dir, "child.pid"))

	cancel() // the self-restart: what cmd/obor's serveRestarter does instead of launching kickstart -k
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve = %v; want nil (serve exits 0 for launchd to start the new build)", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return after the restart's cancellation")
	}
	if len(g.gated) != 1 {
		t.Errorf("gates: %v; want the one the restart cut off", g.gated)
	}
	for _, p := range []int{pid, child} {
		if !gone(p) {
			t.Errorf("pid %d of the gate's process group outlived the restart", p)
		}
	}
}
