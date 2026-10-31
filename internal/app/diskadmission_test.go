package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// RV-59 #1: the free-space reading alone holds a gate or nightly; recording the needs-you is best effort, so a refused
// append (a full disk) must not let either start.

// diskCount is a Loop's Disk: the free bytes scripted, each reading counted (a test asserts the admission read it).
type diskCount struct {
	free  atomic.Uint64
	calls atomic.Int32
}

func (f *diskCount) disk() *Disk {
	return &Disk{Volume: "/vol/pj", Free: func() (uint64, error) {
		f.calls.Add(1)
		return f.free.Load(), nil
	}}
}

// refuseLogs makes every event-log append of name's project fail with err; nil heals it.
func (r *concRig) refuseLogs(name string, err error) {
	p := r.proj(name)
	p.log.mu.Lock()
	defer p.log.mu.Unlock()
	if err == nil {
		p.log.refuse = nil
		return
	}
	p.log.refuse = func(Record) error { return err }
}

// concLoop is name's app's Loop (the rig's one fake for it).
func (r *concRig) concLoop(name string) *Loop {
	a, p := r.app(name), r.proj(name)
	fx, err := a.opt.Loop(p.root)
	if err != nil {
		r.t.Fatal(err)
	}
	return fx
}

// addNightly appends a nightly record (its key: attempt 0) at at and folds it.
func (r *concRig) addNightly(step, kind string, d any, at time.Time) {
	r.t.Helper()
	a, p := r.app("pj"), r.proj("pj")
	raw, _ := json.Marshal(d)
	rec, err := p.log.Append(Record{Task: NightlyTask, Step: step, Kind: kind, Data: raw}, at)
	if err != nil {
		r.t.Fatal(err)
	}
	a.mu.Lock()
	p.foldRecord(rec)
	a.mu.Unlock()
}

// runStep runs name's task's next production step once (App.next: begin, the step, release) and waits for the effects
// it started. A gate step always returns false: it waits, whether it is held or its gate runs in its own goroutine.
func (r *concRig) runStep(name, task string) {
	r.t.Helper()
	a := r.app(name)
	fx := r.concLoop(name)
	a.mu.Lock()
	p := a.p
	st := a.next(p, p.folded.loop[task])
	a.mu.Unlock()
	if st == nil {
		r.t.Fatalf("%s: no step for %s: state %s", name, task, r.state(name, task))
	}
	st(context.Background(), fx)
	a.effects.Wait()
}

// diskFactsInForce is name's project's low-disk facts in force.
func (r *concRig) diskFactsInForce(name string) []Record {
	a, p := r.app(name), r.proj(name)
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Record(nil), p.folded.active[DiskTask]...)
}

// RV-59 #1, direct: free 1 GiB with the needs-you append refused is still low and leaves no fact in force; the next
// reading, appends working again, records the needs-you.
func TestRV59DiskRecordFailure(t *testing.T) {
	const gib = uint64(1 << 30)
	r := newConcRig(t)
	p := r.proj("pj")
	fx := r.concLoop("pj")
	f := &diskCount{}
	f.free.Store(gib)
	fx.Disk = f.disk()
	r.refuseLogs("pj", errors.New("database or disk is full"))

	if !r.a.diskIsLow(p, fx) {
		t.Errorf("free 1 GiB with the needs-you refused: not low; want low")
	}
	if fs := r.diskFactsInForce("pj"); len(fs) != 0 {
		t.Errorf("facts in force %+v with the append refused, want none", fs)
	}

	r.refuseLogs("pj", nil)
	if !r.a.diskIsLow(p, fx) {
		t.Errorf("the next reading with appends working: not low; want low")
	}
	fs := r.diskFactsInForce("pj")
	if len(fs) != 1 || fs[0].Step != StepNeedsYou || !strings.HasPrefix(fs[0].Fact().Reason, "disk low: 1.0 GiB free on /vol/pj") {
		t.Errorf("facts in force %+v, want the needs-you telling the volume and 1.0 GiB", fs)
	}
	if n := f.calls.Load(); n != 2 {
		t.Errorf("free space read %d times, want 2 (the written record is not re-checked)", n)
	}
}

// RV-59 #1: a gate whose intent a crash or refused result left pending runs on the next pass; with every append
// refused and free 1 GiB it does not, whether its step is a submitted lane's gate, a resolved merge's or a moved
// target's (all pickGate). Once the log takes appends and space is back, the same pending gate runs and reuses its
// intent.
func TestRV59LowDiskPendingGate(t *testing.T) {
	const gib = uint64(1 << 30)
	for _, c := range []struct {
		name  string
		state string
		kind  string
		seed  func(r *concRig)
	}{{
		name: "submitted", state: stSubmitted, kind: InputLane,
		seed: func(*concRig) {},
	}, {
		name: "resolved", state: stResolved, kind: InputResolved,
		seed: func(r *concRig) {
			r.add("pj", "T-1", StepGate, KindResult, gateOut{Outcome: outConflict,
				Candidate: Candidate{Task: "T-1", SHA: "c", Target: "b", Patch: "p"}})
			r.add("pj", "T-1", StepResolve, KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/resolve", Patch: "p"})
			r.add("pj", "T-1", MethodResolved+".v1", KindResult,
				verdictResult{Job: "T-1/1/resolve", Reply: "RESOLVED T-1", SHA: "rs"})
		},
	}, {
		name: "moved", state: stMoved, kind: InputMoved,
		seed: func(r *concRig) {
			r.add("pj", "T-1", StepMoved, KindFact, FactData{Merge: true, Reason: "target moved"})
		},
	}} {
		t.Run(c.name, func(t *testing.T) {
			r := newConcRig(t)
			r.fakes["pj"].hold = "" // the gate is not held: its result is what the test counts
			p := r.proj("pj")
			c.seed(r)
			if got := r.state("pj", "T-1"); got != c.state {
				t.Fatalf("precondition: T-1 is %s, want %s", got, c.state)
			}
			r.a.mu.Lock()
			gates := p.folded.loop["T-1"].gates
			r.a.mu.Unlock()
			step := nth(StepGate, gates+1)
			r.add("pj", "T-1", step, KindIntent, gateIn{Kind: c.kind, Lane: "O-01", Input: "s1"})

			f := &diskCount{}
			f.free.Store(gib)
			fx := r.concLoop("pj")
			fx.Disk = f.disk()
			r.refuseLogs("pj", errors.New("database or disk is full"))

			r.runStep("pj", "T-1")
			if n := f.calls.Load(); n == 0 {
				t.Fatalf("the gate's admission did not read the disk")
			}
			if n := r.fakes["pj"].count(r.fakes["pj"].gates, "T-1"); n != 0 {
				t.Fatalf("the gate ran %d times with every append refused and free 1 GiB; want 0", n)
			}
			if n := r.count("pj", "T-1", KindResult, step); n != 0 {
				t.Errorf("the refused gate left a result: %d", n)
			}
			if got := r.state("pj", "T-1"); got != c.state {
				t.Errorf("state %s after the held gate, want %s", got, c.state)
			}

			// Space back and the log taking appends: the pending gate runs, its intent reused.
			f.free.Store(40 << 30)
			r.refuseLogs("pj", nil)
			r.runStep("pj", "T-1")
			if n := r.fakes["pj"].count(r.fakes["pj"].gates, "T-1"); n != 1 {
				t.Fatalf("the gate ran %d times after recovery, want 1", n)
			}
			if n := r.count("pj", "T-1", KindIntent, step); n != 1 {
				t.Errorf("gate intents = %d, want 1 (the pending one reused)", n)
			}
			if n := r.count("pj", "T-1", KindResult, step); n != 1 {
				t.Errorf("gate results = %d, want 1", n)
			}
		})
	}
}

// RV-59 #1: the nightly's admission is the same call. With every append refused and free 1 GiB neither a pending
// nightly intent (a crash mid-run) nor a retry after a failed try starts the run; once appends work and space is back
// it runs.
func TestRV59LowDiskPendingNightly(t *testing.T) {
	const gib = uint64(1 << 30)
	const step = StepNightly + ".2026-10-25"
	for _, c := range []struct {
		name string
		seed func(r *concRig, now time.Time)
	}{{
		name: "pending intent",
		seed: func(r *concRig, now time.Time) {
			r.addNightly(step, KindIntent, nil, now)
		},
	}, {
		name: "retry",
		seed: func(r *concRig, now time.Time) {
			r.addNightly(step, KindIntent, nil, now)
			a, p := r.app("pj"), r.proj("pj")
			rec, err := p.log.Append(Record{Task: NightlyTask, Step: StepFailed, Kind: KindFact,
				Data: mustJSON(FactData{Machine: Machine, Reason: step + ": a git lock", Step: step})},
				now.Add(-nightlyRetry-time.Minute))
			if err != nil {
				r.t.Fatal(err)
			}
			a.mu.Lock()
			p.foldRecord(rec)
			a.mu.Unlock()
		},
	}} {
		t.Run(c.name, func(t *testing.T) {
			r := newConcRig(t)
			c.seed(r, r.a.opt.Now())

			var runs atomic.Int32
			f := &diskCount{}
			f.free.Store(gib)
			fx := r.concLoop("pj")
			fx.Disk = f.disk()
			fx.Nightly = &Nightly{At: "13:00",
				Due: func([]Merge, bool) (bool, error) { return true, nil },
				Run: func(context.Context) (NightlyOut, error) {
					runs.Add(1)
					return NightlyOut{SHA: "abc1234def", Verdict: "GREEN", Line: "NIGHTLY GREEN abc1234"}, nil
				}}
			r.refuseLogs("pj", errors.New("database or disk is full"))

			p := r.proj("pj")
			r.a.nightly(context.Background(), p, fx)
			r.a.effects.Wait()
			if n := f.calls.Load(); n == 0 {
				t.Fatalf("the nightly's admission did not read the disk")
			}
			if n := runs.Load(); n != 0 {
				t.Fatalf("the nightly ran %d times with every append refused and free 1 GiB; want 0", n)
			}
			if n := r.count("pj", NightlyTask, KindResult, step); n != 0 {
				t.Errorf("a refused nightly left a result: %d", n)
			}

			f.free.Store(40 << 30)
			r.refuseLogs("pj", nil)
			r.a.nightly(context.Background(), p, fx)
			r.a.effects.Wait()
			if n := runs.Load(); n != 1 {
				t.Fatalf("the nightly ran %d times after recovery, want 1", n)
			}
			if n := r.count("pj", NightlyTask, KindResult, step); n != 1 {
				t.Errorf("nightly results = %d, want 1", n)
			}
		})
	}
}
