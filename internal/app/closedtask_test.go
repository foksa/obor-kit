package app

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"strings"
	"testing"
)

// closeTask ends task of project name as c says: merged or dropped (a fact), or rejected (the integrator's verdict on
// its open review job; T-4 of pr has one). Live: as the app does (record, verdict); replay: appended and folded only,
// so the next load settles it.
func (r *concRig) closeTask(name, task, end string, live bool) {
	r.t.Helper()
	a, p := r.app(name), r.proj(name)
	switch {
	case end != stRejected && live:
		if !a.record(p, event.Record{Task: task, Attempt: 1, Step: end, Kind: event.KindFact}, nil) {
			r.t.Fatal("the terminal fact was not recorded")
		}
	case end != stRejected:
		r.add(name, task, end, event.KindFact, nil)
	case live:
		if _, err := a.verdict(context.Background(), wire.MethodReject, wire.Call{Root: p.root, Agent: p.integrator,
			Role: "integrator", Task: task, Job: task + "/1/review", Patch: "p", Note: "why", CallID: "rj"}); err != nil {
			r.t.Fatalf("reject: %v", err)
		}
	default:
		step := verdictStep(wire.MethodReject, "rj")
		r.add(name, task, step, event.KindIntent, verdictData{Job: task + "/1/review", Caller: p.integrator, Patch: "p", Note: "why"})
		r.add(name, task, step, event.KindResult, verdictResult{Job: task + "/1/review", Patch: "p", Note: "why"})
	}
	if got := r.state(name, task); got != end {
		r.t.Fatalf("%s is %s, want %s", task, got, end)
	}
}

// reload ends name's serve and loads its log again (the replay), the new app connected.
func (r *concRig) reload(name string) (*App, *proj) {
	r.t.Helper()
	a := r.app(name)
	a.closeLog()
	b := New(Options{Now: a.opt.Now, Loop: a.opt.Loop}, &fakePanes{}, &fakeLanes{})
	p, err := b.load(r.roots[name])
	if err != nil {
		r.t.Fatal(err)
	}
	b.p, b.connected = p, true
	r.apps[name] = b
	return b, p
}

// pendingNotify appends the notification intent of fact f (interrupted before its send) and folds it.
func pendingNotify(t *testing.T, a *App, p *proj, f event.Record) event.Key {
	t.Helper()
	k := notifyKey(f)
	in, err := p.log.Append(event.Record{Task: k.Task, Attempt: k.Attempt, Step: k.Step, Kind: event.KindIntent, Class: event.ClassFact},
		a.opt.Now())
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	p.foldRecord(in)
	a.mu.Unlock()
	if _, pending := p.log.Intent(k); !pending {
		t.Fatal("precondition: the notification is not pending")
	}
	return k
}

// closedCases is each way a task closes, live and on replay: the project and its task.
var closedCases = []struct {
	name, proj, task, end string
	live                  bool
}{
	{"live merged", "pq", "T-3", event.StepMerged, true},
	{"live dropped", "pq", "T-3", event.StepDropped, true},
	{"live rejected", "pr", "T-4", stRejected, true},
	{"replay merged", "pq", "T-3", event.StepMerged, false},
	{"replay dropped", "pq", "T-3", event.StepDropped, false},
	{"replay rejected", "pr", "T-4", stRejected, false},
}

// T-636 (RV-68 #1): a worker's stop recorded after its task closed (picked before the merge, drop or reject) puts no
// watchdog needs-you in force under the closed task, live or on replay, nor does a watchdog's stalled fact: nothing
// is notified, no lead line. Both of an open task still do.
func TestStopAfterCloseNoFact(t *testing.T) {
	t.Parallel()
	for _, c := range append(closedCases, struct {
		name, proj, task, end string
		live                  bool
	}{"open", "pq", "T-3", "", true}) {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			a, p := r.app(c.proj), r.proj(c.proj)
			if c.end != "" {
				r.closeTask(c.proj, c.task, c.end, c.live)
			}
			d := stoppedData{Agent: c.proj + "-o-0x", Error: "rate_limit",
				NeedsYou: &event.FactData{Lane: "O-0x", Reason: "stopped: rate_limit"}, Message: event.Message{Task: c.task}}
			if !a.record(p, event.Record{Task: event.MsgTask, Step: event.StepStopped, Kind: event.KindFact}, d) {
				t.Fatal("the stop was not recorded")
			}
			if r.count(c.proj, event.MsgTask, event.KindFact, event.StepStopped) != 1 {
				t.Fatal("precondition: the stopped fact is not in the log")
			}
			// a watchdog's own fact decided before the close, recorded after it (fold)
			if !a.record(p, event.Record{Task: c.task, Attempt: 1, Step: event.StepStalled, Kind: event.KindFact},
				event.FactData{Lane: "O-0x", Reason: "no activity"}) {
				t.Fatal("the stalled fact was not recorded")
			}
			if !c.live {
				a, p = r.reload(c.proj)
			}
			a.mu.Lock()
			facts := len(p.folded.active[c.task])
			a.mu.Unlock()
			a.notify(context.Background())
			notes := a.panes.(*fakePanes).notes
			lines := 0
			for _, m := range needsMessages(t, p) {
				if strings.Contains(m.Line, "stopped: rate_limit") {
					lines++
				}
			}
			if c.end == "" {
				if facts != 2 || len(notes) != 1 {
					t.Errorf("open task: watchdog facts %d, notifications %v; want the stop and the stall, one shown",
						facts, notes)
				}
				return
			}
			if facts != 0 || len(notes) != 0 || lines != 0 {
				t.Errorf("after %s: watchdog facts %d, notifications %v, lead lines %d; want none", c.end, facts, notes, lines)
			}
		})
	}
}

// T-636 (RV-68 #2): a reject settles the pending notification of its task's watchdog needs-you as "not run", live
// and on replay, as a merge or drop does; nothing is shown for it.
func TestRejectSettlesWatchdogNotification(t *testing.T) {
	t.Parallel()
	for _, live := range []bool{true, false} {
		t.Run(map[bool]string{true: "live", false: "replay"}[live], func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			a, p := r.app("pr"), r.proj("pr")
			r.add("pr", "T-4", event.StepNeedsYou, event.KindFact, event.FactData{Lane: "L-04", Reason: "pane exited"})
			if len(p.folded.active["T-4"]) != 1 {
				t.Fatalf("precondition: watchdog facts of T-4: %v, want one", p.folded.active["T-4"])
			}
			k := pendingNotify(t, a, p, p.folded.active["T-4"][0])
			r.closeTask("pr", "T-4", stRejected, live)
			if !live {
				a, p = r.reload("pr")
			}
			if _, pending := p.log.Intent(k); pending {
				t.Error("the notification of the rejected task's watchdog fact is still pending")
			}
			if got := string(p.log.Results[k].Data); got != `"not run: T-4 rejected"` {
				t.Errorf("its result = %s, want not run", got)
			}
			a.notify(context.Background())
			if notes := a.panes.(*fakePanes).notes; len(notes) != 0 {
				t.Errorf("notifications shown: %v, want none", notes)
			}
		})
	}
}

// T-636 (T-401's old case): a closed task's merge-loop needs-you in force (an after-merge step's, a reject's) keeps
// its pending notification and posts its lead line, live and on replay; notify shows it once.
func TestClosedTaskMergeNeedsYouLine(t *testing.T) {
	t.Parallel()
	for _, c := range closedCases {
		if c.end == event.StepDropped { // the loop runs no step of a dropped task: it has none
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			a, p := r.app(c.proj), r.proj(c.proj)
			if p.out.leadAgent == "" || !p.merging {
				t.Fatal("precondition: no lead agent or no merge loop: the needs rule lines nothing")
			}
			r.closeTask(c.proj, c.task, c.end, c.live)
			a.mu.Lock()
			mt := *p.folded.loop[c.task]
			a.mu.Unlock()
			if !a.needsYou(p, &mt, "unpin"+failedSuffix, "unpin failed: busy") {
				t.Fatal("the merge-loop needs-you was not recorded")
			}
			a.mu.Lock()
			needs := p.folded.loop[c.task].needs.fact
			a.mu.Unlock()
			if needs.Seq == 0 {
				t.Fatal("precondition: the merge-loop needs-you is not in force")
			}
			k := pendingNotify(t, a, p, needs)
			if !c.live {
				a, p = r.reload(c.proj)
			}
			if _, pending := p.log.Intent(k); !pending {
				t.Errorf("the notification of the merge-loop needs-you was settled: %s", p.log.Results[k].Data)
			}
			a.rulePass(context.Background(), []rule{{name: ruleNeeds, when: needsRule}}, &Loop{})
			lines := 0
			for _, m := range needsMessages(t, p) {
				if m.Of == (event.Of{Kind: ruleNeeds, Seq: needsKey(needs.Seq, 0)}) && strings.Contains(m.Line, "unpin failed: busy") {
					lines++
				}
			}
			if lines != 1 {
				t.Errorf("lead lines of the merge-loop needs-you: %d, want 1", lines)
			}
			a.notify(context.Background())
			a.notify(context.Background())
			if notes := a.panes.(*fakePanes).notes; len(notes) != 1 {
				t.Errorf("notifications shown: %v, want one", notes)
			}
		})
	}
}
