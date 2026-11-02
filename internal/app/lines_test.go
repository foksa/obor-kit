package app

import (
	"context"
	"encoding/json"
	"errors"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// RV-50 #1–#3 (T-473): the project's lines survive a restart (orderly and SIGKILL) and an off-target checkout.

// linerSpy is a Typer and a Poster that records each line typed or posted (the lead's, T-485); refuse: refused before
// typing; hang: typing begins and never returns (the process is killed there).
type linerSpy struct {
	calls  []string
	refuse bool
	hang   func()
}

func (l *linerSpy) Type(_ context.Context, agent, text string, _ bool) (bool, error) {
	l.calls = append(l.calls, agent+": "+text)
	switch {
	case l.hang != nil:
		l.hang()
		park()
	case l.refuse:
		return false, errors.New("agent_not_ready: no typing")
	}
	return true, nil
}

func (l *linerSpy) Post(ctx context.Context, _, agent, line string) (bool, error) {
	return l.Type(ctx, agent, line, false)
}

// linerLoop is a loop whose lines go to l, typed and posted.
func linerLoop(l *linerSpy) *Loop { return &Loop{Typer: l, Inbox: l} }

// lineRoot is a merging project with a reviewer role.
func lineRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"name":"proj","prefix":"pj","roles":{"integrator":{},"reviewer":{}}}`
	if err := os.WriteFile(filepath.Join(root, ".obor/config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// lineApp loads root's log and connects to a snapshot with the lead and the reviewer pj-rev idle.
func lineApp(t *testing.T, root string, now *time.Time) (*App, *proj) {
	t.Helper()
	a := New(Options{Now: func() time.Time { return *now }, Loop: func(string) (*Loop, error) { return &Loop{}, nil }},
		&fakePanes{}, &fakeLanes{})
	p, err := a.load(root)
	if err != nil {
		t.Fatal(err)
	}
	a.p = p
	t.Cleanup(func() { p.log.Close(); a.closeLog() })
	a.connect(context.Background(), port.Snapshot{Workspaces: map[string]string{"w": "proj"}, Panes: []port.Pane{
		{ID: "w:lead", Workspace: "w", Cwd: root, Agent: "claude", Label: p.out.lead(), Status: "idle"},
		{ID: "w:rev", Workspace: "w", Cwd: root, Agent: "codex", Label: "pj-rev", Status: "idle"},
	}})
	return a, p
}

// lineReload closes a's log and loads it again (an orderly restart).
func lineReload(t *testing.T, a *App, p *proj, now *time.Time) (*App, *proj) {
	t.Helper()
	p.log.Close()
	a.closeLog()
	return lineApp(t, p.root, now)
}

// lineStatus sets pane id's status as herdr reports it (an event at a's clock: observed, reconciled, its work noted).
func lineStatus(a *App, id, status string, _ time.Time) {
	pane := a.obs[id].pane
	pane.Status = status
	a.event(context.Background(), port.Event{Kind: port.EventStatus, Pane: id, Status: status, Agent: pane.Agent, New: pane})
}

// lineKinds are the three kinds of line: an obor send line to the lead, an obor brief text to the reviewer, and the
// lead's finished line of a brief whose reviewer worked and is idle again.
var lineKinds = []string{"send", "brief", "finished"}

// queueLine queues one line of kind (nothing typed yet).
func queueLine(t *testing.T, a *App, p *proj, kind string, now time.Time) {
	t.Helper()
	var err error
	switch kind {
	case "send":
		_, err = a.sendCall(wire.Call{Root: p.root, CallID: "send1", From: "other", Note: "hello", Role: "lead"})
	case "brief":
		_, err = a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-rev", Note: "verify finding 1", Role: "lead"})
	case "finished":
		_, err = a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-rev", Role: "lead"}) // a fresh start's brief
		lineStatus(a, "w:rev", "working", now)
		lineStatus(a, "w:rev", "idle", now)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// lineTyped: calls is the line of kind typed once (a finished line's age aside; a brief typed into pj-rev's pane).
func lineTyped(calls []string, kind string) bool {
	want := map[string]string{"send": "pj-lead: " + sendLine("other", "hello"), "brief": "w:rev: [obor owner] verify finding 1",
		"finished": "pj-lead: [obor] pj-rev finished (briefed "}[kind]
	return len(calls) == 1 && strings.HasPrefix(calls[0], want)
}

// queuedAt is p's undelivered line of fact seq (nil: none), by its outbox's queuedLines.
func (p *proj) queuedAt(seq int64) *queued {
	for _, q := range p.queuedLines() {
		if q.seq == seq {
			return q
		}
	}
	return nil
}

// briefCount is the number of p's open briefs, by its outbox's allBriefs.
func (p *proj) briefCount() int {
	n := 0
	for range p.out.allBriefs() {
		n++
	}
	return n
}

// TestRefusedLineReplay: a line refused before typing stays queued across a restart (orderly and SIGKILL) and is
// typed once after it, for each kind of line (RV-50 #1).
func TestRefusedLineReplay(t *testing.T) {
	t.Parallel()
	for _, kind := range lineKinds {
		t.Run(kind+"/reload", func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			a, p := lineApp(t, lineRoot(t), &now)
			queueLine(t, a, p, kind, now)
			l := &linerSpy{refuse: true}
			a.deliver(context.Background(), p, linerLoop(l))
			if len(l.calls) != 1 || len(p.queuedLines()) != 1 {
				t.Fatalf("precondition: one refused try of one queued line; calls %v, queued %d", l.calls, len(p.queuedLines()))
			}
			b, q := lineReload(t, a, p, &now)
			if len(q.queuedLines()) != 1 {
				t.Fatalf("the refused line is not queued after reload: %d", len(q.queuedLines()))
			}
			good := &linerSpy{}
			b.deliver(context.Background(), q, linerLoop(good))
			b.deliver(context.Background(), q, linerLoop(good))
			if !lineTyped(good.calls, kind) || len(q.queuedLines()) != 0 {
				t.Fatalf("after reload: want the %s line typed once, got %v (queued %d)", kind, good.calls, len(q.queuedLines()))
			}
			c, s := lineReload(t, b, q, &now)
			c.deliver(context.Background(), s, linerLoop(good))
			if len(good.calls) != 1 {
				t.Fatalf("typed again after a second reload: %v", good.calls)
			}
		})
		t.Run(kind+"/kill", func(t *testing.T) {
			t.Parallel()
			calls := killedLines(t, kind, "refused")
			if !lineTyped(calls, kind) {
				t.Fatalf("after SIGKILL: want the %s line typed once, got %v", kind, calls)
			}
		})
	}
}

// TestRefusedLineRetryKilled: a refused line's retry cut by a SIGKILL once its typing began is delivered once more
// after the restart (T-524: a line is never lost; it may reach its agent twice); a line queued without a delivery is
// typed once (control).
func TestRefusedLineRetryKilled(t *testing.T) {
	t.Parallel()
	for _, kind := range lineKinds {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			if calls := killedLines(t, kind, "retry"); !lineTyped(calls, kind) {
				t.Fatalf("a retry cut while typing: want the %s line delivered once after the restart, got %v", kind,
					calls)
			}
		})
	}
	t.Run("queued control", func(t *testing.T) {
		t.Parallel()
		if calls := killedLines(t, "send", "queued"); !lineTyped(calls, "send") {
			t.Fatalf("a queued line: want one delivery, got %v", calls)
		}
	})
}

// killedLines runs TestRefusedLineKillChild for kind and mode, kills it with SIGKILL once it is ready, and is the lines
// typed by two reloads after it (two deliver passes each).
func killedLines(t *testing.T, kind, mode string) []string {
	t.Helper()
	root := lineRoot(t)
	child := exec.Command(os.Args[0], "-test.run=^TestRefusedLineKillChild$")
	child.Env = append(os.Environ(), "LINE_ROOT="+root, "LINE_KIND="+kind, "LINE_MODE="+mode)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	ready := false
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(root, "kill-ready")); err == nil {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatal("the child did not get ready")
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	if st := child.ProcessState.Sys().(syscall.WaitStatus); !st.Signaled() || st.Signal() != syscall.SIGKILL {
		t.Fatalf("the child did not end by SIGKILL: %v", child.ProcessState)
	}
	now := time.Now()
	l := &linerSpy{}
	a, p := lineApp(t, root, &now)
	if p.out.lead() != "pj-lead" {
		t.Fatalf("precondition: lead pj-lead, got %s", p.out.lead())
	}
	a.deliver(context.Background(), p, linerLoop(l))
	a.deliver(context.Background(), p, linerLoop(l))
	b, q := lineReload(t, a, p, &now)
	b.deliver(context.Background(), q, linerLoop(l))
	b.deliver(context.Background(), q, linerLoop(l))
	return l.calls
}

// TestRefusedLineKillChild is killedLines's child: it queues a line of LINE_KIND; refused: its delivery is refused; retry:
// refused, then its retry hangs once typing began; queued: nothing delivered; worked (no line queued): a fresh
// start's brief whose work a pass saw. Then it writes kill-ready and waits.
func TestRefusedLineKillChild(t *testing.T) {
	t.Parallel()
	root := os.Getenv("LINE_ROOT")
	if root == "" {
		t.Skip("killedLines's child only")
	}
	now := time.Now()
	a, p := lineApp(t, root, &now)
	if os.Getenv("LINE_MODE") != "worked" {
		queueLine(t, a, p, os.Getenv("LINE_KIND"), now)
	}
	ready := func() { _ = os.WriteFile(filepath.Join(root, "kill-ready"), []byte("ready"), 0o600) }
	switch os.Getenv("LINE_MODE") {
	case "refused":
		a.deliver(context.Background(), p, linerLoop(&linerSpy{refuse: true}))
	case "retry":
		a.deliver(context.Background(), p, linerLoop(&linerSpy{refuse: true}))
		a.deliver(context.Background(), p, linerLoop(&linerSpy{hang: ready}))
	case "worked":
		if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-rev", Role: "lead"}); err != nil {
			t.Fatal(err)
		}
		lineStatus(a, "w:rev", "working", now)
		a.deliver(context.Background(), p, linerLoop(&linerSpy{}))
	}
	ready()
	park()
}

// park blocks the calling goroutine with a pending timer, so the runtime's deadlock detector leaves the process for the
// parent's SIGKILL. A bare select{} let a loaded child exit 2 ("all goroutines are asleep") before the kill landed, red
// in a gate run (T-554).
func park() {
	t := time.NewTimer(time.Hour)
	defer t.Stop()
	<-t.C
}

// TestBriefCompletionReplay: a brief whose work serve saw before a restart (orderly and SIGKILL) gets its
// finished line when its agent is idle on reconnect, for a brief with a text and a fresh start's brief (RV-50 #3).
func TestBriefCompletionReplay(t *testing.T) {
	t.Parallel()
	for _, note := range []string{"verify finding 1", ""} {
		form := map[bool]string{true: "verify", false: "fresh"}[note != ""]
		t.Run(form+"/reload", func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			a, p := lineApp(t, lineRoot(t), &now)
			if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-rev", Note: note, Role: "lead"}); err != nil {
				t.Fatal(err)
			}
			l := &linerSpy{}
			a.deliver(context.Background(), p, linerLoop(l))
			lineStatus(a, "w:rev", "working", now)
			a.deliver(context.Background(), p, linerLoop(l)) // the pass that records the work
			lines := 0                                       // the brief's own line
			if note != "" {
				lines = 1
			}
			if !p.out.brief("pj-rev").worked || len(l.calls) != lines {
				t.Fatalf("precondition: work seen, no finished line yet; worked %v, calls %v", p.out.brief("pj-rev").worked, l.calls)
			}
			b, q := lineReload(t, a, p, &now) // the reviewer idle on reconnect
			if q.out.brief("pj-rev") == nil || !q.out.brief("pj-rev").worked {
				t.Fatal("the brief's observed work is not replayed")
			}
			out := &linerSpy{}
			b.deliver(context.Background(), q, linerLoop(out))
			b.deliver(context.Background(), q, linerLoop(out))
			if !lineTyped(out.calls, "finished") || q.briefCount() != 0 {
				t.Fatalf("want one finished line after reload, got %v (open briefs %d)", out.calls, q.briefCount())
			}
		})
	}
	t.Run("fresh/kill", func(t *testing.T) {
		t.Parallel()
		calls := killedLines(t, "", "worked")
		if !lineTyped(calls, "finished") {
			t.Fatalf("want one finished line after SIGKILL, got %v", calls)
		}
	})
}

// TestWorkedNotNewerBrief: the recorded work of a brief does not arm a newer brief that replaced it.
func TestWorkedNotNewerBrief(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	l := &linerSpy{}
	if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-rev", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	lineStatus(a, "w:rev", "working", now)
	a.deliver(context.Background(), p, linerLoop(l))
	if !p.out.brief("pj-rev").worked {
		t.Fatal("precondition: the first brief's work is seen")
	}
	if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b2", To: "pj-rev", Note: "again", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	b, q := lineReload(t, a, p, &now)
	if br := q.out.brief("pj-rev"); br == nil || br.worked {
		t.Fatalf("the newer brief is armed by the older one's work: %+v", br)
	}
	b.deliver(context.Background(), q, linerLoop(l))
	b.deliver(context.Background(), q, linerLoop(l))
	if len(l.calls) != 1 || l.calls[0] != "w:rev: [obor owner] again" {
		t.Fatalf("want only the newer brief's line, got %v", l.calls)
	}
}

// TestBriefKeepsAStartedDelivery (T-575): a line whose delivery is in flight (its intent is pending: it may have
// landed) is not superseded by a newer brief.
func TestBriefKeepsAStartedDelivery(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	lineStatus(a, "w:rev", "working", now) // the brief's line waits
	if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-rev", Note: "first", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	if len(p.queuedLines()) != 1 {
		t.Fatalf("precondition: one brief queued, got %d", len(p.queuedLines()))
	}
	q := p.queuedLines()[0]
	a.record(p, event.Record{Task: event.MsgTask, Attempt: 1, Step: stepDeliver + strconv.FormatInt(q.seq, 10),
		Kind: event.KindIntent, Class: event.ClassPrompt}, lineData{To: "pj-rev"})
	if m := p.queuedAt(q.seq); m == nil || m.state != lineSending {
		t.Fatalf("precondition: the delivery in flight; queued %+v", p.queuedLines())
	}
	reply, err := a.briefCall(wire.Call{Root: p.root, CallID: "b2", To: "pj-rev", Note: "second", Role: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply, "replaces") {
		t.Errorf("reply %q says it replaced a line whose delivery may have landed", reply)
	}
	if m := p.queuedAt(q.seq); m == nil || m.state != lineSending {
		t.Errorf("the in-flight line was superseded: queued %+v", p.queuedLines())
	}
	if n := len(p.queuedLines()); n != 2 {
		t.Errorf("queued lines %d, want the in-flight one and the newer brief's", n)
	}
	recs, err := event.ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs {
		var m event.Message
		_ = json.Unmarshal(rec.Data, &m)
		if rec.Task == event.MsgTask && (rec.Step == event.StepSuperseded || rec.Step == event.StepBriefed && len(m.Supersedes) > 0) {
			t.Errorf("a brief superseded the in-flight line: %+v", rec)
		}
	}
}

// TestBriefSupersededEndsNeedsYou (T-575): the 24 h needs-you of a brief line ends when a newer brief supersedes it;
// the newer brief's own line waits as usual.
func TestBriefSupersededEndsNeedsYou(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	lineStatus(a, "w:rev", "working", now)
	if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-rev", Note: "first", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	l := &linerSpy{}
	a.opt.Loop = func(string) (*Loop, error) { return &Loop{Typer: l, Inbox: l}, nil }
	now = now.Add(25 * time.Hour)
	a.advance(context.Background())
	if len(p.folded.active[event.MsgTask]) != 1 {
		t.Fatalf("precondition: the 25 h brief line's needs-you, got %d", len(p.folded.active[event.MsgTask]))
	}
	if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b2", To: "pj-rev", Note: "second", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	a.advance(context.Background())
	a.advance(context.Background())
	if len(p.folded.active[event.MsgTask]) != 0 {
		t.Errorf("the needs-you of the superseded line is still in force: %+v", p.folded.active[event.MsgTask])
	}
	if q := p.queuedLines(); len(q) != 1 || q[0].msg.Line != "[obor owner] second" || len(l.calls) != 0 {
		t.Errorf("queued %v, typed %v; want the newer brief's line alone, waiting", q, l.calls)
	}
}

// TestBriefSupersedeSurvivesACrash (RV-61 #2): a brief's supersede is its own fact: with the log refusing every
// append after the new brief's, a restart right after it keeps the old line retired (never delivered), with a line
// or a fresh start's brief; a retry of the new brief's call id gets the stored reply.
func TestBriefSupersedeSurvivesACrash(t *testing.T) {
	t.Parallel()
	for _, note := range []string{"second", ""} {
		t.Run("note="+note, func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			a, p := lineApp(t, lineRoot(t), &now)
			lineStatus(a, "w:rev", "working", now) // brief A waits
			if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-rev", Note: "first", Role: "lead"}); err != nil {
				t.Fatal(err)
			}
			if len(p.queuedLines()) != 1 {
				t.Fatalf("precondition: brief A queued, got %d", len(p.queuedLines()))
			}
			briefed := false
			p.log.Refuse = func(r event.Record) error { // the crash cut: nothing after the new brief reaches the log
				if briefed {
					return errors.New("cut")
				}
				briefed = r.Step == event.StepBriefed
				return nil
			}
			reply, err := a.briefCall(wire.Call{Root: p.root, CallID: "b2", To: "pj-rev", Note: note, Role: "lead"})
			if err != nil || !strings.HasSuffix(reply, "(replaces 1 undelivered brief)") {
				t.Fatalf("brief B: %q, %v; want it recorded, replacing A", reply, err)
			}
			if !briefed {
				t.Fatal("precondition: brief B's fact is in the log")
			}
			b, q := lineReload(t, a, p, &now)
			for _, m := range q.queuedLines() {
				if m.msg.Line == "[obor owner] first" {
					t.Errorf("brief A is queued again after the restart: %+v", m)
				}
			}
			again, err := b.briefCall(wire.Call{Root: q.root, CallID: "b2", To: "pj-rev", Note: note, Role: "lead"})
			if err != nil || again != reply {
				t.Errorf("retry of b2: %q, %v; want the stored reply %q", again, err, reply)
			}
			l := &linerSpy{}
			lineStatus(b, "w:rev", "idle", now)
			b.deliver(context.Background(), q, linerLoop(l))
			b.deliver(context.Background(), q, linerLoop(l))
			want := []string{}
			if note != "" {
				want = []string{"w:rev: [obor owner] second"}
			}
			if strings.Join(l.calls, "|") != strings.Join(want, "|") {
				t.Errorf("delivered %v, want %v: brief A never", l.calls, want)
			}
		})
	}
}

// duringSpy is a linerSpy that runs during once inside its first delivery (the line's claim is set then).
type duringSpy struct {
	linerSpy
	during func()
}

func (d *duringSpy) Type(ctx context.Context, agent, text string, x bool) (bool, error) {
	if f := d.during; f != nil {
		d.during = nil
		f()
	}
	return d.linerSpy.Type(ctx, agent, text, x)
}

func (d *duringSpy) Post(ctx context.Context, _, agent, line string) (bool, error) {
	return d.Type(ctx, agent, line, false)
}

// TestBriefRetiresARoutedLine (RV-61 #3): a line routed for delivery (no intent yet) that a newer brief supersedes
// before its claim is not transmitted, and only the newer one is delivered; the inverse, a line claimed first, is
// kept by the newer brief and delivered once, and the newer brief after it.
func TestBriefRetiresARoutedLine(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	lineStatus(a, "w:rev", "working", now)
	if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-rev", Note: "first", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	lineStatus(a, "w:rev", "idle", now)
	q := *p.queuedLines()[0]
	l := &linerSpy{}
	fx := linerLoop(l)
	a.mu.Lock()
	post, target, ok := a.route(p, fx, q)
	a.mu.Unlock()
	if !ok {
		t.Fatal("precondition: brief A routed")
	}
	if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b2", To: "pj-rev", Note: "second", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	a.deliverLine(context.Background(), p, fx, q, post, target)
	if len(l.calls) != 0 {
		t.Errorf("the retired line was transmitted: %v", l.calls)
	}
	if _, written := p.log.Intent(event.Key{Task: event.MsgTask, Step: stepDeliver + strconv.FormatInt(q.seq, 10)}); written {
		t.Errorf("an intent was written for the retired line")
	}
	a.deliver(context.Background(), p, fx)
	a.deliver(context.Background(), p, fx)
	if len(l.calls) != 1 || l.calls[0] != "w:rev: [obor owner] second" {
		t.Errorf("delivered %v, want brief B alone", l.calls)
	}

	t.Run("claimed first", func(t *testing.T) {
		t.Parallel()
		now := time.Now()
		a, p := lineApp(t, lineRoot(t), &now)
		lineStatus(a, "w:rev", "working", now)
		if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-rev", Note: "first", Role: "lead"}); err != nil {
			t.Fatal(err)
		}
		lineStatus(a, "w:rev", "idle", now)
		var reply string
		d := &duringSpy{}
		d.during = func() { // A's claim is set: B arrives now
			var err error
			if reply, err = a.briefCall(wire.Call{Root: p.root, CallID: "b2", To: "pj-rev", Note: "second", Role: "lead"}); err != nil {
				t.Error(err)
			}
		}
		fx := &Loop{Typer: d, Inbox: d}
		a.deliver(context.Background(), p, fx)
		if reply == "" || strings.Contains(reply, "replaces") {
			t.Errorf("brief B during A's delivery: %q; want it recorded, replacing nothing", reply)
		}
		a.deliver(context.Background(), p, fx)
		a.deliver(context.Background(), p, fx)
		if got := strings.Join(d.calls, "|"); got != "w:rev: [obor owner] first|w:rev: [obor owner] second" {
			t.Errorf("delivered %q, want A once, then B once", got)
		}
	})
}

// TestBriefSupersedesSeveralLines (T-580): every undelivered brief line of the agent is retired by the one briefed
// fact, live and on replay.
func TestBriefSupersedesSeveralLines(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	lineStatus(a, "w:rev", "working", now)
	if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-rev", Note: "first", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	first := p.queuedLines()[0].seq
	step := stepDeliver + strconv.FormatInt(first, 10)
	a.record(p, event.Record{Task: event.MsgTask, Attempt: 1, Step: step, Kind: event.KindIntent, Class: event.ClassPrompt}, lineData{To: "pj-rev"})
	if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b2", To: "pj-rev", Note: "second", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	a.record(p, event.Record{Task: event.MsgTask, Step: event.StepFailed, Kind: event.KindFact}, // A's delivery refused: undelivered again
		event.FactData{Machine: Machine, Reason: step + ": refused", Step: step})
	if len(p.queuedLines()) != 2 || p.queuedAt(first) == nil || p.queuedAt(first).state != lineWaiting {
		t.Fatalf("precondition: two undelivered brief lines, none claimed; queued %+v", p.queuedLines())
	}
	reply, err := a.briefCall(wire.Call{Root: p.root, CallID: "b3", To: "pj-rev", Note: "third", Role: "lead"})
	if err != nil || !strings.HasSuffix(reply, "(replaces 2 undelivered briefs)") {
		t.Fatalf("brief C: %q, %v", reply, err)
	}
	check := func(when string, p *proj) {
		if q := p.queuedLines(); len(q) != 1 || q[0].msg.Line != "[obor owner] third" {
			t.Errorf("%s: queued %+v, want brief C alone", when, q)
		}
	}
	check("live", p)
	recs, err := event.ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	facts := 0
	for _, rec := range recs {
		var m event.Message
		_ = json.Unmarshal(rec.Data, &m)
		if rec.Task == event.MsgTask && rec.Step == event.StepBriefed && len(m.Supersedes) > 0 {
			facts++
			if len(m.Supersedes) != 2 {
				t.Errorf("briefed %+v supersedes %v, want both lines", rec, m.Supersedes)
			}
		}
		if rec.Step == event.StepSuperseded {
			t.Errorf("a separate superseded fact was recorded: %+v", rec)
		}
	}
	if facts != 1 {
		t.Errorf("briefed facts that supersede: %d, want the one", facts)
	}
	_, q := lineReload(t, a, p, &now)
	check("replay", q)
}

// TestOlderSupersededFactReplays (T-580): an older log's fact superseded{seq, by} still retires its line on replay.
func TestOlderSupersededFactReplays(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	lineStatus(a, "w:rev", "working", now)
	a.record(p, event.Record{Task: event.MsgTask, Step: event.StepBriefed, Kind: event.KindFact}, event.Message{To: "pj-rev", Line: "[obor owner] first"})
	a.record(p, event.Record{Task: event.MsgTask, Step: event.StepBriefed, Kind: event.KindFact}, event.Message{To: "pj-rev", Line: "[obor owner] second"})
	q := p.queuedLines()
	if len(q) != 2 {
		t.Fatalf("precondition: an older log's two brief lines queued, got %d", len(q))
	}
	a.record(p, event.Record{Task: event.MsgTask, Step: event.StepSuperseded, Kind: event.KindFact}, supersededData{Seq: q[0].seq, By: q[1].seq})
	_, r := lineReload(t, a, p, &now)
	if got := r.queuedLines(); len(got) != 1 || got[0].msg.Line != "[obor owner] second" {
		t.Errorf("replayed queue %+v, want the second brief alone", got)
	}
}

// TestLinesBehindCheckout: a main checkout off the target branch holds dispatch, not the lines: each kind of
// line is typed into its idle pane, and a line waiting 25 h has its needs-you (RV-50 #2).
func TestLinesBehindCheckout(t *testing.T) {
	t.Parallel()
	off := func(context.Context) error { return errors.New("main checkout is on release/v0.12, not main") }
	for _, kind := range lineKinds {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			a, p := lineApp(t, lineRoot(t), &now)
			queueLine(t, a, p, kind, now)
			l := &linerSpy{}
			a.opt.Loop = func(string) (*Loop, error) { return &Loop{Typer: l, Inbox: l, Checkout: off}, nil }
			if !p.merging || !a.connected {
				t.Fatal("precondition: a merging project, connected")
			}
			a.advance(context.Background())
			if !lineTyped(l.calls, kind) {
				t.Fatalf("want the %s line typed behind the checkout, got %v", kind, l.calls)
			}
		})
	}
	t.Run("needs-you", func(t *testing.T) {
		t.Parallel()
		now := time.Now()
		a, p := lineApp(t, lineRoot(t), &now)
		queueLine(t, a, p, "brief", now)
		lineStatus(a, "w:rev", "working", now)
		now = now.Add(25 * time.Hour)
		l := &linerSpy{}
		a.opt.Loop = func(string) (*Loop, error) { return &Loop{Typer: l, Inbox: l, Checkout: off}, nil }
		a.advance(context.Background())
		if len(l.calls) != 0 || len(p.folded.active[event.MsgTask]) != 1 {
			t.Fatalf("want the working reviewer's 25 h line held with a needs-you: calls %v, needs-you %d", l.calls,
				len(p.folded.active[event.MsgTask]))
		}
	})
}

// TestBriefWorkDurableAtOnce: the work a brief's agent is seen doing is in the log as soon as its caller (a herdr status
// event, a snapshot, a fresh start's brief) returns, before any delivery pass: a restart right then still gets the
// finished line once the agent is idle on reconnect, for a brief with a text and a fresh start's brief (RV-50 #3,
// the retained observation → reload probes).
func TestBriefWorkDurableAtOnce(t *testing.T) {
	t.Parallel()
	working := func(a *App, p *proj) {
		pane := a.obs["w:rev"].pane
		pane.Status = "working"
		a.event(context.Background(), port.Event{Kind: port.EventStatus, Pane: pane.ID, New: pane})
	}
	snapshot := func(a *App, p *proj) {
		lead, rev := a.obs["w:lead"].pane, a.obs["w:rev"].pane
		rev.Status = "working"
		a.connect(context.Background(), port.Snapshot{Workspaces: map[string]string{"w": "proj"}, Panes: []port.Pane{lead, rev}})
	}
	for _, c := range []struct {
		name string
		note string
		seen func(*App, *proj)
	}{
		{"verify/event", "verify finding 1", working},
		{"fresh/event", "", working},
		{"verify/snapshot", "verify finding 1", snapshot},
		{"fresh/snapshot", "", snapshot},
		{"fresh/brief", "", nil}, // the pane works already when the fresh start's brief is recorded
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			a, p := lineApp(t, lineRoot(t), &now)
			if c.seen == nil {
				lineStatus(a, "w:rev", "working", now)
			}
			if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-rev", Note: c.note, Role: "lead"}); err != nil {
				t.Fatal(err)
			}
			l := &linerSpy{}
			if c.note != "" {
				a.deliver(context.Background(), p, linerLoop(l)) // the brief's line typed
				if len(l.calls) != 1 || p.out.brief("pj-rev").worked {
					t.Fatalf("precondition: the line typed, no work seen yet; calls %v", l.calls)
				}
			}
			if c.seen != nil {
				c.seen(a, p)
			}
			if b := p.out.brief("pj-rev"); !b.worked || !b.logged {
				t.Fatalf("the work seen is not in the log once its caller returns: %+v", *b)
			}
			b, q := lineReload(t, a, p, &now) // no pass since; the reviewer idle on reconnect
			if q.out.brief("pj-rev") == nil || !q.out.brief("pj-rev").worked {
				t.Fatal("the brief's observed work is not replayed")
			}
			out := &linerSpy{}
			b.deliver(context.Background(), q, linerLoop(out))
			b.deliver(context.Background(), q, linerLoop(out))
			if !lineTyped(out.calls, "finished") || q.briefCount() != 0 {
				t.Fatalf("want one finished line after reload, got %v (open briefs %d)", out.calls, q.briefCount())
			}
		})
	}
}

// TestFinishedLineAgeFromTyping: a finished line's age counts from when the brief's line was typed, also across a
// restart, not from when the brief was recorded (a brief queued for an hour is not "briefed 1h ago").
func TestFinishedLineAgeFromTyping(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	lineStatus(a, "w:rev", "working", now) // busy: the brief waits
	if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-rev", Note: "verify finding 1", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	l := &linerSpy{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 0 {
		t.Fatalf("precondition: the line waits for the working pane; typed %v", l.calls)
	}
	now = now.Add(time.Hour)
	lineStatus(a, "w:rev", "idle", now)
	a.deliver(context.Background(), p, linerLoop(l)) // typed an hour after the brief
	if len(l.calls) != 1 {
		t.Fatalf("precondition: the brief's line typed; calls %v", l.calls)
	}
	now = now.Add(2 * time.Minute)
	pane := a.obs["w:rev"].pane
	pane.Status = "working"
	a.event(context.Background(), port.Event{Kind: port.EventStatus, Pane: pane.ID, New: pane})
	now = now.Add(time.Minute)
	b, q := lineReload(t, a, p, &now) // idle on reconnect, 3 minutes after the typing
	out := &linerSpy{}
	b.deliver(context.Background(), q, linerLoop(out))
	b.deliver(context.Background(), q, linerLoop(out))
	if want := "pj-lead: [obor] pj-rev finished (briefed 3m ago)"; len(out.calls) != 1 || out.calls[0] != want {
		t.Fatalf("want %q, got %v", want, out.calls)
	}
}

// registerLead rewrites root's config with lead as roles.lead.agent.
func registerLead(t *testing.T, root, lead string) {
	t.Helper()
	cfg := `{"name":"proj","prefix":"pj","roles":{"lead":{"agent":"` + lead + `"},"integrator":{},"reviewer":{}}}`
	if err := os.WriteFile(filepath.Join(root, ".obor/config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// inboxSpy records each line posted to an inbox.
type inboxSpy struct{ calls []string }

func (s *inboxSpy) Post(_ context.Context, _, agent, line string) (bool, error) {
	s.calls = append(s.calls, agent+": "+line)
	return true, nil
}

// TestQueuedLeadLineAfterRename: a lead line (a send, a finished line) queued for want of an inbox is
// neither typed into the old lead's idle pane after the lead is renamed and serve restarts, nor lost: it is posted
// once its inbox is recorded (V1).
func TestQueuedLeadLineAfterRename(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"send", "finished"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			a, p := lineApp(t, lineRoot(t), &now)
			a.opt.Inboxed = func(string) (string, bool) { return "", false }
			queueLine(t, a, p, kind, now)
			first := &linerSpy{}
			a.deliver(context.Background(), p, &Loop{Typer: first, Inbox: &inboxSpy{}}) // records the finished line
			if len(first.calls) != 0 || len(p.queuedLines()) != 1 || !p.queuedLines()[0].lead {
				t.Fatalf("precondition: one lead line queued, nothing typed; typed %v, queued %d", first.calls, len(p.queuedLines()))
			}

			registerLead(t, p.root, "pj-new-lead")
			b, q := lineReload(t, a, p, &now)
			if q.out.lead() != "pj-new-lead" || len(q.queuedLines()) != 1 {
				t.Fatalf("precondition: renamed lead %q with one line queued, got %d", q.out.lead(), len(q.queuedLines()))
			}
			pane := b.obs["w:lead"].pane
			pane.Label = "pj-lead" // the old lead's session still open and idle under its old name
			b.event(context.Background(), port.Event{Kind: port.EventStatus, Pane: pane.ID, New: pane})
			if w := b.whereIs(q, "pj-lead"); w.State != Identified || !Ready(w.Pane.Status) {
				t.Fatal("precondition: the old lead's pane is idle under its old name")
			}

			b.opt.Inboxed = func(string) (string, bool) { return "", false }
			lines, posts := &linerSpy{}, &inboxSpy{}
			b.deliver(context.Background(), q, &Loop{Typer: lines, Inbox: posts})
			if len(lines.calls) != 0 || len(posts.calls) != 0 || len(q.queuedLines()) != 1 {
				t.Fatalf("no inbox: typed %q, posted %q, queued %d; want nothing tried, still queued", lines.calls,
					posts.calls, len(q.queuedLines()))
			}

			b.opt.Inboxed = func(string) (string, bool) { return "", true }
			b.deliver(context.Background(), q, &Loop{Typer: lines, Inbox: posts})
			b.deliver(context.Background(), q, &Loop{Typer: lines, Inbox: posts})
			if len(lines.calls) != 0 || len(posts.calls) != 1 || !strings.HasPrefix(posts.calls[0], "pj-lead: [obor") ||
				len(q.queuedLines()) != 0 {
				t.Errorf("inbox recorded: typed %q, posted %q, queued %d; want posted once, never typed", lines.calls,
					posts.calls, len(q.queuedLines()))
			}
		})
	}
}

// TestBriefOfAgentSinceLead: a brief waiting for a role agent that has since become the lead keeps the
// inbox path: never typed, posted once its inbox is recorded (V1's kept protection).
func TestBriefOfAgentSinceLead(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	lineStatus(a, "w:rev", "working", now) // busy: the brief's line waits
	queueLine(t, a, p, "brief", now)
	if len(p.queuedLines()) != 1 || p.queuedLines()[0].lead {
		t.Fatalf("precondition: one role brief queued; queued %d", len(p.queuedLines()))
	}

	registerLead(t, p.root, "pj-rev")
	b, q := lineReload(t, a, p, &now)
	if q.out.lead() != "pj-rev" || len(q.queuedLines()) != 1 {
		t.Fatalf("precondition: pj-rev the lead with the brief queued; lead %q, queued %d", q.out.lead(), len(q.queuedLines()))
	}
	lead := b.obs["w:lead"].pane
	lead.Label = "pj-lead" // the old lead's pane keeps its label: pj-rev runs in w:rev alone
	b.event(context.Background(), port.Event{Kind: port.EventStatus, Pane: lead.ID, New: lead})
	if w := b.whereIs(q, "pj-rev"); w.State != Identified || w.Pane.ID != "w:rev" || !Ready(w.Pane.Status) {
		t.Fatal("precondition: pj-rev's pane is idle")
	}
	b.opt.Inboxed = func(string) (string, bool) { return "", false }
	lines, posts := &linerSpy{}, &inboxSpy{}
	b.deliver(context.Background(), q, &Loop{Typer: lines, Inbox: posts})
	if len(lines.calls) != 0 || len(posts.calls) != 0 || len(q.queuedLines()) != 1 {
		t.Fatalf("no inbox: typed %q, posted %q, queued %d; want nothing tried", lines.calls, posts.calls, len(q.queuedLines()))
	}
	b.opt.Inboxed = func(string) (string, bool) { return "", true }
	b.deliver(context.Background(), q, &Loop{Typer: lines, Inbox: posts})
	if len(lines.calls) != 0 || len(posts.calls) != 1 || posts.calls[0] != "pj-rev: [obor owner] verify finding 1" {
		t.Errorf("inbox recorded: typed %q, posted %q; want posted once, never typed", lines.calls, posts.calls)
	}
}

// TestRunningLeadHint: the queued send's reply names `obor restart <lead>` (obor lead starts nothing for a
// running lead), and the upgrade guide and state-model row 47 say the same (V2).
func TestRunningLeadHint(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	a.opt.Inboxed = func(string) (string, bool) { return "", false }
	reply, err := a.sendCall(wire.Call{Root: p.root, CallID: "s1", From: "other", Note: "hello"})
	hint := "`obor restart pj-lead` once it is idle registers it, or quit it and run `obor lead`"
	if err != nil || !strings.Contains(reply, hint) || strings.Contains(reply, "obor lead registers") {
		t.Errorf("reply %q (%v); want it to name %s", reply, err, hint)
	}
	for file, want := range map[string]string{
		"state-model.md": strings.ReplaceAll(hint, "pj-lead", "<lead>"),
	} {
		b, err := os.ReadFile(filepath.Join("..", "..", "docs", file))
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(strings.Fields(string(b)), " ") // the guide wraps its lines
		if !strings.Contains(text, want) || strings.Contains(text, "obor lead registers it") ||
			strings.Contains(text, "restarted once (`obor lead`)") {
			t.Errorf("docs/%s: want %q, and no recipe that has obor lead register a running lead", file, want)
		}
	}
}

// TestBriefStatus (T-664): the app's status lists a role agent's open brief: not delivered while its line is queued,
// delivered once typed, worked on once its pane worked; none once it finished.
func TestBriefStatus(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	if got := a.Status().Project.Briefs; len(got) != 0 {
		t.Fatalf("precondition: no brief, got %+v", got)
	}
	if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-rev", Note: "verify 1", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	if got := a.Status().Project.Briefs; len(got) != 1 || got[0].To != "pj-rev" || got[0].Delivered || got[0].Since == 0 {
		t.Errorf("briefed, its line queued: %+v; want pj-rev, not delivered, its time", got)
	}
	l := &linerSpy{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 1 {
		t.Fatalf("precondition: the brief's line typed, got %v", l.calls)
	}
	if got := a.Status().Project.Briefs; len(got) != 1 || !got[0].Delivered || got[0].Worked {
		t.Errorf("its line typed: %+v; want delivered, not worked on", got)
	}
	lineStatus(a, "w:rev", "working", now)
	a.deliver(context.Background(), p, linerLoop(l))
	if got := a.Status().Project.Briefs; len(got) != 1 || !got[0].Worked {
		t.Errorf("its pane worked: %+v; want worked on", got)
	}
}
