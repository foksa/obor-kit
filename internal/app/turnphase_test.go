package app

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
)

// T-571 (RV-60 #1, #3): a turn's start and its end are different evidence, decided by one table (turnDecide). The
// rows below run the socket-free core (lineApp: pj-rev, a briefed reviewer, over the fake herdr) with the real log and
// folds; each ends with a replay that must give no second line and the same state.

// TestTurnDecide is the table itself, row by row (stopped.go).
func TestTurnDecide(t *testing.T) {
	for _, c := range []struct {
		name string
		in   turnCase
		want turnAct
	}{
		{"stop, start: resume", turnCase{stopped: true, ended: false}, turnResume},
		{"stop, start, work seen: still resume (a lingering status)", turnCase{stopped: true, worked: true}, turnResume},
		{"stop, end: finish", turnCase{stopped: true, ended: true}, turnFinish},
		{"stop, end on an error: stop", turnCase{stopped: true, ended: true, failed: true}, turnStop},
		{"followed, start: none", turnCase{followed: true}, turnNone},
		{"followed, end: finish", turnCase{followed: true, ended: true}, turnFinish},
		{"followed, end on an error: stop", turnCase{followed: true, ended: true, failed: true}, turnStop},
		{"neither, start: none", turnCase{worked: true}, turnNone},
		{"neither, end: none (seen work arms the finish)", turnCase{worked: true, ended: true}, turnNone},
		{"neither, end on an error after work: stop", turnCase{worked: true, ended: true, failed: true}, turnStop},
		{"neither, end on an error, no work: none", turnCase{ended: true, failed: true}, turnNone},
	} {
		if got := turnDecide(c.in); got != c.want {
			t.Errorf("%s: turnDecide(%+v) = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}

// turnRig is the line core with pj-rev (kind) briefed, its work seen, then stopped on a usage limit resetting at
// now+1h: a Codex reviewer's by its rollout's turn "old" (roleTurns), a Claude one's by its StopFailure hook.
type turnRig struct {
	t    *testing.T
	a    *App
	p    *proj
	now  *time.Time
	kind string
	turn agents.Turn // the rollout's last turn (Codex)
	out  *linerSpy
}

func newTurnRig(t *testing.T, kind string) *turnRig {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	r := &turnRig{t: t, now: &now, kind: kind, out: &linerSpy{}}
	r.a, r.p = lineApp(t, lineRoot(t), r.now)
	r.setKind()
	queueLine(t, r.a, r.p, "finished", now) // briefed, worked, idle
	if kind == "codex" {
		r.turn = agents.Turn{ID: "old", Ended: true, Error: "usage_limit_exceeded", Details: "limit",
			Reset: now.Add(time.Hour)}
	} else if _, err := r.a.stoppedCall(Call{Root: r.p.root, CallID: "limit", Agent: "pj-rev", Role: "reviewer",
		Error: "rate_limit", Details: "limit|" + strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}); err != nil {
		t.Fatal(err)
	}
	r.deliver()
	if b := r.p.briefs["pj-rev"]; b == nil || b.stop == nil || r.stops() != 1 || r.finished() != 0 {
		t.Fatalf("precondition: one stop on an open brief, no finished line: %+v, %d stops, %d finished",
			b, r.stops(), r.finished())
	}
	return r
}

// setKind makes pj-rev's pane of r's kind (an event: observed).
func (r *turnRig) setKind() {
	pane := r.a.obs["w:rev"].pane
	pane.Agent = r.kind
	r.a.event(context.Background(), Event{Kind: EventStatus, Pane: pane.ID, Status: pane.Status, Agent: r.kind, New: pane})
}

// deliver is one delivery pass, the rollout's last turn r.turn (Codex).
func (r *turnRig) deliver() {
	f := r.a.panes.(*fakePanes)
	f.turn, f.turned = r.turn, r.kind == "codex" && r.turn.ID != ""
	r.a.deliver(context.Background(), r.p, linerLoop(r.out))
}

// status is pj-rev's pane status as herdr reports it.
func (r *turnRig) status(s string) { lineStatus(r.a, "w:rev", s, *r.now) }

// hook is pj-rev's turn request: its Stop hook's (ended), else its UserPromptSubmit's.
func (r *turnRig) hook(ended bool) string {
	reply, err := r.a.turnCall(Call{Root: r.p.root, Agent: "pj-rev", Role: "reviewer", Ended: ended})
	if err != nil {
		r.t.Fatal(err)
	}
	return reply
}

// facts counts the log's facts of the lines with step whose data contains sub.
func (r *turnRig) facts(step, sub string) int {
	recs, err := ReadLog(r.p.root)
	if err != nil {
		r.t.Fatal(err)
	}
	n := 0
	for _, rec := range recs {
		if rec.Task == MsgTask && rec.Kind == KindFact && rec.Step == step && strings.Contains(string(rec.Data), sub) {
			n++
		}
	}
	return n
}

func (r *turnRig) stops() int    { return r.facts(StepStopped, `"agent":"pj-rev"`) }
func (r *turnRig) finished() int { return r.facts(StepMessage, "pj-rev finished") }

// state is pj-rev's brief as the folds hold it: open, stopped (its turn), followed, worked.
func (r *turnRig) state() string {
	b := r.p.briefs["pj-rev"]
	if b == nil {
		return "closed"
	}
	s := "open"
	if b.stop != nil {
		s += " stopped(" + b.stop.turn + ")"
	}
	if b.followed {
		s += " followed"
	}
	if b.worked {
		s += " worked"
	}
	return s
}

// replayed restarts serve (the log replayed, the pane seen anew) and runs two delivery passes on the same evidence:
// no new line or fact, the same state.
func (r *turnRig) replayed() {
	r.t.Helper()
	state, stops, finished, turned := r.state(), r.stops(), r.finished(), r.facts(StepTurned, "")
	r.a, r.p = lineReload(r.t, r.a, r.p, r.now)
	r.setKind()
	r.deliver()
	r.deliver()
	if r.state() != state || r.stops() != stops || r.finished() != finished || r.facts(StepTurned, "") != turned {
		r.t.Errorf("after a replay: %s, %d stops, %d finished, %d turned; want %s, %d, %d, %d", r.state(), r.stops(),
			r.finished(), r.facts(StepTurned, ""), state, stops, finished, turned)
	}
}

// RV-60 #1: a Codex role's next turn starts while herdr still says idle → the stop ends, the brief stays open, no
// finished line; that turn's task_complete → the finished line, once.
func TestTurnCodexStartWhileIdle(t *testing.T) {
	r := newTurnRig(t, "codex")
	r.turn = agents.Turn{ID: "new"} // task_started; the pane still idle
	r.deliver()
	r.deliver()
	if s := r.state(); s != "open followed" || r.finished() != 0 || r.facts(StepTurned, "") != 1 {
		t.Fatalf("at the start: %s, %d finished, %d turned; want open followed, 0, 1", s, r.finished(),
			r.facts(StepTurned, ""))
	}
	r.replayed()
	r.turn = agents.Turn{ID: "new", Ended: true} // task_complete
	r.deliver()
	r.deliver()
	if s := r.state(); s != "closed" || r.finished() != 1 {
		t.Fatalf("at the end: %s, %d finished; want closed, 1", s, r.finished())
	}
	r.replayed()
}

// RV-60 #1: the resumed turn starts, then hits its limit too → a second stop, the brief open, its wake due after the
// new reset (and not before).
func TestTurnCodexStartThenLimit(t *testing.T) {
	r := newTurnRig(t, "codex")
	r.turn = agents.Turn{ID: "new"}
	r.deliver()
	reset := r.now.Add(2 * time.Hour)
	r.turn = agents.Turn{ID: "new", Ended: true, Error: "usage_limit_exceeded", Details: "limit", Reset: reset}
	r.deliver()
	r.deliver()
	if s := r.state(); s != "open stopped(new)" || r.stops() != 2 || r.finished() != 0 {
		t.Fatalf("after the second limit: %s, %d stops, %d finished; want open stopped(new), 2, 0", s, r.stops(),
			r.finished())
	}
	r.replayed()
	*r.now = reset.Add(time.Minute)
	r.a.roleWakes(context.Background(), r.p, linerLoop(r.out))
	if n := typedTo(r.out, roleContinue); n != 0 {
		t.Fatalf("woken %d times before the new reset+2m", n)
	}
	*r.now = reset.Add(wakeAfter)
	r.a.roleWakes(context.Background(), r.p, linerLoop(r.out))
	r.a.roleWakes(context.Background(), r.p, linerLoop(r.out))
	if n := typedTo(r.out, roleContinue); n != 1 {
		t.Fatalf("at the new reset+2m: %d wakes, want 1", n)
	}
	r.replayed()
	r.a.roleWakes(context.Background(), r.p, linerLoop(r.out))
	if n := typedTo(r.out, roleContinue); n != 1 {
		t.Errorf("after a replay: %d wakes, want 1", n)
	}
}

func typedTo(l *linerSpy, line string) int {
	n := 0
	for _, c := range l.calls {
		if strings.HasSuffix(c, ": "+line) {
			n++
		}
	}
	return n
}

// Control: the Codex role is seen working first (its start read with it), then idle with the turn's end → the
// finished line once.
func TestTurnCodexWorkingFirst(t *testing.T) {
	r := newTurnRig(t, "codex")
	r.status("working")
	r.turn = agents.Turn{ID: "new"}
	r.deliver()
	if s := r.state(); s != "open followed" || r.finished() != 0 {
		t.Fatalf("working: %s, %d finished; want open followed, 0", s, r.finished())
	}
	r.status("idle")
	r.turn = agents.Turn{ID: "new", Ended: true}
	r.deliver()
	r.deliver()
	if s := r.state(); s != "closed" || r.finished() != 1 {
		t.Fatalf("idle, the turn ended: %s, %d finished; want closed, 1", s, r.finished())
	}
	r.replayed()
}

// RV-60 #3: a Claude role's start hook was missed (serve down, the request timed out); its work is seen while the
// stop is in force, then its Stop hook → the stop ends and the finished line follows, once.
func TestTurnClaudeMissedStart(t *testing.T) {
	r := newTurnRig(t, "claude")
	r.status("working")
	r.deliver() // the work logged (fact worked); held by the stop
	if s := r.state(); s != "open stopped() worked" || r.finished() != 0 {
		t.Fatalf("working, start missed: %s, %d finished; want open stopped() worked, 0", s, r.finished())
	}
	if reply := r.hook(true); reply != "TURN pj-rev (its brief's turn ended)" {
		t.Fatalf("Stop hook: %q", reply)
	}
	if r.p.briefs["pj-rev"].stop != nil {
		t.Fatal("the stop still in force after the Stop hook")
	}
	r.status("idle")
	r.deliver()
	r.deliver()
	if s := r.state(); s != "closed" || r.finished() != 1 {
		t.Fatalf("after the Stop hook, idle: %s, %d finished; want closed, 1", s, r.finished())
	}
	r.replayed()
}

// Control: the Claude role's start hook, then its Stop hook (no status seen between) → the finished line once.
func TestTurnClaudeStartThenStop(t *testing.T) {
	r := newTurnRig(t, "claude")
	if reply := r.hook(false); reply != "TURN pj-rev (the stop of its brief ended)" {
		t.Fatalf("start hook: %q", reply)
	}
	r.deliver()
	if s := r.state(); s != "open followed" || r.finished() != 0 {
		t.Fatalf("after the start hook: %s, %d finished; want open followed, 0", s, r.finished())
	}
	r.replayed()
	if reply := r.hook(true); reply != "TURN pj-rev (its brief's turn ended)" {
		t.Fatalf("Stop hook: %q", reply)
	}
	r.deliver()
	r.deliver()
	if s := r.state(); s != "closed" || r.finished() != 1 {
		t.Fatalf("after the Stop hook: %s, %d finished; want closed, 1", s, r.finished())
	}
	r.replayed()
	if reply := r.hook(true); reply != "TURN pj-rev (no stop)" || r.facts(StepTurned, "") != 2 {
		t.Errorf("a later Stop hook: %q, %d turned facts; want no stop, 2", reply, r.facts(StepTurned, ""))
	}
}

// An aborted turn after the stop, its work seen → finished; a turn that ends on a new error at once → a new stop.
func TestTurnCodexAbortedAndNewError(t *testing.T) {
	t.Run("aborted after work", func(t *testing.T) {
		r := newTurnRig(t, "codex")
		r.status("working")
		r.status("idle")
		r.turn = agents.Turn{ID: "new", Ended: true} // turn_aborted
		r.deliver()
		if s := r.state(); s != "closed" || r.finished() != 1 {
			t.Fatalf("aborted: %s, %d finished; want closed, 1", s, r.finished())
		}
		r.replayed()
	})
	t.Run("new error at once", func(t *testing.T) {
		r := newTurnRig(t, "codex")
		r.turn = agents.Turn{ID: "new", Ended: true, Error: "usage_limit_exceeded", Details: "again",
			Reset: r.now.Add(3 * time.Hour)}
		r.deliver()
		if s := r.state(); s != "open stopped(new)" || r.stops() != 2 || r.finished() != 0 {
			t.Fatalf("new error: %s, %d stops, %d finished; want open stopped(new), 2, 0", s, r.stops(), r.finished())
		}
		r.replayed()
	})
}

// Workers have no brief: their hooks' start and end of the resumed turn each end the stop (fact resumed); a Codex
// worker's rollout the same, by its next turn's start or its end. No brief is made or read.
func TestTurnWorkerPhases(t *testing.T) {
	for _, ended := range []bool{false, true} {
		name := map[bool]string{false: "start", true: "end"}[ended]
		t.Run("hook "+name, func(t *testing.T) {
			h, _, _, _ := promptRig(t, true)
			addRecord(t, h, "T-1", StepWork, KindIntent, promptData{Agent: "pj-o-01", Job: "T-1/1/work"})
			h.connect()
			if _, err := h.a.stoppedCall(Call{Root: h.root, CallID: "limit", Agent: "pj-o-01", Role: "worker",
				Lane: "O-01", Error: "rate_limit", Details: "429"}); err != nil {
				t.Fatal(err)
			}
			p := h.proj()
			if !p.stops("T-1") {
				t.Fatal("precondition: T-1 stopped")
			}
			reply, err := h.a.turnCall(Call{Root: h.root, Agent: "pj-o-01", Role: "worker", Lane: "O-01", Ended: ended})
			if err != nil || reply != "TURN pj-o-01 (the stop of T-1 ended)" || p.stops("T-1") || len(p.briefs) != 0 {
				t.Errorf("%s hook: %q, %v, stopped %v, %d briefs; want the stop ended, none", name, reply, err,
					p.stops("T-1"), len(p.briefs))
			}
		})
		t.Run("rollout "+name, func(t *testing.T) {
			h, _, _, _ := promptRig(t, true)
			addRecord(t, h, "T-1", StepWork, KindIntent, promptData{Agent: "pj-o-01", Job: "T-1/1/work"})
			h.connect()
			p := h.proj()
			h.a.mu.Lock()
			defer h.a.mu.Unlock()
			w := p.tasks["T-1"]
			at, _ := h.a.agentOf(p, w.lane)
			if at.State != Identified || !Ready(at.Pane.Status) {
				t.Fatalf("precondition: the worker identified and idle: %+v", at)
			}
			turn := func(tu agents.Turn) bool {
				return h.a.turnStop(p, "T-1", w, map[string]*evidence{at.Pane.ID: {turn: tu, turned: true}})
			}
			if !turn(agents.Turn{ID: "t1", Ended: true, Error: "usage_limit_exceeded", Details: "limit"}) || !p.stops("T-1") {
				t.Fatal("precondition: the rollout's limit is T-1's stop")
			}
			if turn(agents.Turn{ID: "t1", Ended: true, Error: "usage_limit_exceeded", Details: "limit"}) {
				t.Fatal("the stopped turn read again decided something")
			}
			if !turn(agents.Turn{ID: "t2", Ended: ended}) || p.stops("T-1") || len(p.briefs) != 0 {
				t.Errorf("the next turn's %s: stopped %v, %d briefs; want the stop ended, none", name, p.stops("T-1"),
					len(p.briefs))
			}
		})
	}
}
