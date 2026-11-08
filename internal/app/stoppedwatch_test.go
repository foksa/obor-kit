package app

import (
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"testing"
)

// T-617 (RV-66): a worker's stop, and its next turn clearing it, are recorded against the lane's current task — the
// one the loop has awaiting its task_done there — never an old watch: the lane's old task ended and T-1 was dispatched
// before the watchdog's next look, its watch of the old task still on the lane. No watch yet (T-612) still works; a
// watch of the current task is the one used. Of two tasks awaiting task_done on the lane, the newest dispatch is the
// current one, though its name sorts later (T-615's rule).
func TestStoppedCurrentTaskNotAnOldWatch(t *testing.T) {
	t.Parallel()
	old := func() *watch { return &watch{lane: port.Lane{Name: "L-01", Agent: "pj-l-01", Task: "T-0"}} }
	cur := func() *watch { return &watch{lane: port.Lane{Name: "L-01", Agent: "pj-l-01", Task: "T-1"}} }
	for _, c := range []struct {
		name  string
		then  string               // a second task dispatched to L-01 after T-1 ("" none)
		want  string               // the lane's current task
		watch func(p *proj) *watch // the watches on L-01 before each call; the one to keep (nil: a new one)
	}{
		{"old watch, new task", "", "T-1",
			func(p *proj) *watch { p.tasks["T-0"] = old(); delete(p.tasks, "T-1"); return nil }},
		{"no watch yet", "", "T-1",
			func(p *proj) *watch { delete(p.tasks, "T-0"); delete(p.tasks, "T-1"); return nil }},
		{"watch of the current task", "", "T-1", func(p *proj) *watch { w := cur(); p.tasks["T-1"] = w; return w }},
		{"two awaiting, newest dispatch wins", "T-2", "T-2",
			func(p *proj) *watch { p.tasks["T-1"] = cur(); delete(p.tasks, "T-2"); return nil }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","roles":{"integrator":""}}`)
			f := &schedScratch{h: h}
			h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Workflow: WorkflowOf(f), Agents: f, Typer: f}, nil }
			if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
				t.Fatal(err)
			}
			h.lanes.lanes[0].Task = "" // the last check saw the lane free (its old task over); T-1 dispatched after
			h.connect()
			h.lanes.lanes[0].Task = c.want
			tasks := []string{"T-1"}
			if c.then != "" {
				tasks = append(tasks, c.then)
			}
			for _, task := range tasks {
				addRecord(t, h, task, event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"),
					Agent: "pj-l-01", Branch: "task/" + task, Target: "b"})
				addRecord(t, h, task, event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
			}
			p := h.a.p
			for _, task := range tasks {
				if mt := p.folded.loop[task]; mt == nil || !mt.awaitsTaskDone() || mt.lane != "L-01" {
					t.Fatalf("precondition: %s %+v; want waiting on its worker in L-01", task, mt)
				}
			}
			if c.then != "" && !(p.folded.loop[c.then].dispSeq > p.folded.loop["T-1"].dispSeq && "T-1" < c.then) {
				t.Fatalf("precondition: %s not the newer dispatch sorting after T-1", c.then)
			}
			stale := "T-0" // the watch on L-01 not of the current task
			if c.then != "" {
				stale = "T-1"
			}

			// the stop is recorded against the current task
			h.a.mu.Lock()
			keep := c.watch(p)
			h.a.mu.Unlock()
			staleWatch := keep == nil && c.name != "no watch yet" // the case leaves a stale watch on L-01
			if p.tasks[c.want] != keep || (p.tasks[stale] != nil) != staleWatch {
				t.Fatalf("precondition: watches %+v; want the case's", p.tasks)
			}
			reply, err := h.a.stoppedCall(wire.StoppedReq{Envelope: wire.Envelope{Root: h.root, Agent: "pj-l-01",
				Role: "worker", CallID: "s1"}, Lane: "L-01", Error: "rate_limit", Details: "429 Too Many Requests"})
			if err != nil || reply != "STOPPED pj-l-01 (needs-you "+c.want+")" {
				t.Fatalf("stopped: %q, %v; want %s's needs-you", reply, err, c.want)
			}
			if !p.stops(c.want) || p.stops(stale) {
				t.Fatalf("stop in force: %s %v, %s %v; want %s's only", c.want, p.stops(c.want), stale, p.stops(stale),
					c.want)
			}
			w := p.tasks[c.want]
			if w == nil || w.lane.Name != "L-01" || w.lane.Task != c.want || (keep != nil && w != keep) {
				t.Errorf("%s's watch %+v; want L-01's (the current one kept: %v)", c.want, w, keep != nil)
			}
			if p.tasks[stale] != nil {
				t.Errorf("the stale watch of %s still in force after the stop", stale)
			}

			// the next turn clears the stop, the same watches on the lane again
			h.a.mu.Lock()
			keep = c.watch(p)
			h.a.mu.Unlock()
			reply, err = h.a.turnCall(wire.TurnReq{Envelope: wire.Envelope{Root: h.root, Agent: "pj-l-01",
				Role: "worker"}, Lane: "L-01"})
			if err != nil || reply != "TURN pj-l-01 (the stop of "+c.want+" ended)" {
				t.Fatalf("turn: %q, %v; want %s's stop ended", reply, err, c.want)
			}
			if p.stops(c.want) {
				t.Errorf("%s's stop still in force after the next turn", c.want)
			}
			if keep != nil && p.tasks[c.want] != keep {
				t.Errorf("the turn replaced %s's watch; want the current one used", c.want)
			}
			if p.tasks[stale] != nil {
				t.Errorf("the stale watch of %s still in force after the turn", stale)
			}
		})
	}
}
