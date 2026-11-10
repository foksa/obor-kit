package app_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
)

func TestIdleStallsThenNeedsYou(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newRig(t)
		r.f.Pane("w1:p2").Status = "working" // L-02 is not idle: only T-1 stalls
		r.start()
		r.clock.Add(9 * time.Minute)
		r.settle()
		eq(t, r.facts(), nil, "facts after 9 min idle")

		r.clock.Add(2 * time.Minute)
		r.waitFacts("T-1 stalled{idle}")
		rec := r.records()[0]
		if rec.Seq != 1 || rec.Kind != "fact" || rec.Fact() != (event.FactData{Lane: "L-01", Machine: "local", Group: "pj",
			Pane: "w1:p1", Agent: "pj-l-01", Reason: "idle"}) {
			t.Errorf("stalled record = %+v", rec)
		}

		r.clock.Add(10 * time.Minute)
		r.waitFacts("T-1 stalled{idle}", "T-1 needs-you{stalled}")
		r.clock.Add(30 * time.Minute)
		r.settle()
		eq(t, r.facts(), []string{"T-1 stalled{idle}", "T-1 needs-you{stalled}"}, "facts 30 min later")

		st := r.app.Status()
		if st.Project == nil || st.Project.Name != "pj" || len(st.Project.Agents) != 2 ||
			len(st.Project.Facts) != 2 {
			t.Fatalf("status = %+v", st)
		}
		a := st.Project.Agents[0]
		if a.Pane != "w1:p1" || a.Lane != "L-01" || a.Task != "T-1" || a.Status != "idle" || a.Age != 51*60 {
			t.Errorf("status agent = %+v, want w1:p1 L-01 T-1 idle for 51 min", a)
		}
	})
}

func TestWorkingAgainResumes(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newRig(t)
		r.f.Pane("w1:p2").Status = "working"
		r.start()
		r.clock.Add(11 * time.Minute)
		r.waitFacts("T-1 stalled{idle}")
		r.srv.WaitSubscribed(t, herdr.EventStatus, "w1:p1")
		r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Status = "working" })
		r.srv.Push(herdr.EventStatus, map[string]any{"pane_id": "w1:p1", "agent_status": "working", "agent": "claude"})
		r.waitFacts("T-1 stalled{idle}", "T-1 resumed")
		if f := r.app.Status().Project.Facts; len(f) != 0 {
			t.Errorf("facts in force after resumed: %+v", f)
		}
	})
}

func TestNotStalledWhileAChildRuns(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newRig(t)
		// L-01's agent is pid 2: an MCP server in its own group (3, does not count) and a Bash tool command in a group of
		// its own (50 → 51). L-02's agent is pid 7 with nothing under it: it stalls, so the watchdogs did look.
		r.f.Pane("w1:p2").Foreground = 7
		r.setPS("2 1 2\n3 2 2\n50 2 50\n51 50 50\n7 1 7\n")
		r.start()
		// Sampled before the clock moves: L-02's baseline at 0, L-01 sampled active (no baseline).
		r.waitQuiet("T-2", r.clock.Now())
		r.waitFor("L-01 sampled while its command runs", func() bool {
			quiet, seen := app.Quiet(r.app, r.root, "T-1")
			return quiet.IsZero() && !seen.IsZero()
		})
		r.clock.Add(11 * time.Minute)
		r.waitFacts("T-2 stalled{idle}")
		r.settle()
		eq(t, r.facts(), []string{"T-2 stalled{idle}"}, "facts while L-01's command runs")
		if r.count("pane.process_info") == 0 {
			t.Fatal("the watchdog never asked herdr for process info")
		}

		if quiet, _ := app.Quiet(r.app, r.root, "T-1"); !quiet.IsZero() {
			t.Fatalf("L-01 has a quiet baseline (%v) while its command runs", quiet)
		}

		r.setPS("2 1 2\n3 2 2\n7 1 7\n")  // the command ended; the MCP server stays
		r.waitQuiet("T-1", r.clock.Now()) // L-01's first quiet sample: its stall timer's baseline
		r.clock.Add(9 * time.Minute)
		r.settle()
		eq(t, r.facts(), []string{"T-2 stalled{idle}"}, "facts 9 min after the command ended")
		r.clock.Add(2 * time.Minute)
		r.waitFacts("T-2 stalled{idle}", "T-1 stalled{idle}", "T-2 needs-you{stalled}")
	})
}

// T-951: the stall watchdog samples the pane it already observed: a ready agent costs no agent.get per tick, while
// the child-process check still runs.
func TestWatchdogAsksNoAgentGetPerTick(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newRig(t)
		r.start()
		gets, procs := r.count("agent.get"), r.count("pane.process_info")
		r.settle() // two more full checks, each sampling both ready agents
		if n := r.count("agent.get") - gets; n != 0 {
			t.Errorf("agent.get over two quiet ticks = %d, want 0 (the sample reads its observed pane)", n)
		}
		if n := r.count("pane.process_info") - procs; n == 0 {
			t.Fatal("no process info over two ticks: the sample did not run (vacuous)")
		}
	})
}

func TestNotStalledWhileTheTranscriptChanges(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newRig(t)
		transcript := filepath.Join(t.TempDir(), "s.jsonl")
		write(t, transcript, "{}\n")
		// Written at 0 (on the test's clock), set before the start so no sample sees the file's real mtime: the first
		// sample only takes it as the watermark.
		if err := os.Chtimes(transcript, r.clock.Now(), r.clock.Now()); err != nil {
			t.Fatal(err)
		}
		r.f.Pane("w1:p1").Transcript = transcript
		r.start()
		if _, seen := app.Quiet(r.app, r.root, "T-1"); !seen.Equal(r.clock.Now()) {
			t.Fatalf("precondition: L-01's watermark %v after the first check, want the write at 0", seen)
		}
		r.clock.Add(9 * time.Minute)
		// The write at 9 and the move to 11 are one step for the watchdogs: a sample must not see the write at 9 and take
		// its baseline there (a loaded machine then stalls L-01 a minute late, at 20 instead of 21).
		r.clock.Move(2*time.Minute, func(now time.Time) {
			if err := os.Chtimes(transcript, now, now); err != nil { // written at 9
				t.Fatal(err)
			}
		})
		r.waitFacts("T-2 stalled{idle}")
		r.settle()
		eq(t, r.facts(), []string{"T-2 stalled{idle}"}, "facts while L-01's transcript is fresh")
		if st := r.app.Status(); st.Project.Agents[0].Pane != "w1:p1" {
			t.Fatalf("status = %+v", st)
		}

		r.clock.Add(9 * time.Minute)
		r.settle()
		eq(t, r.facts(), []string{"T-2 stalled{idle}"}, "facts 9 min after L-01's baseline")
		r.clock.Add(time.Minute) // Idle after L-01's first quiet sample, and after T-2's stall
		r.waitFacts("T-2 stalled{idle}", "T-1 stalled{idle}", "T-2 needs-you{stalled}")
	})
}

func TestBlockedNeedsYou(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newRig(t)
		r.f.Pane("w1:p1").Status = "blocked"
		r.f.Pane("w1:p2").Status = "working"
		r.start()
		r.clock.Add(9 * time.Minute)
		r.settle()
		eq(t, r.facts(), nil, "facts after 9 min blocked")
		r.clock.Add(2 * time.Minute)
		r.waitFacts("T-1 needs-you{blocked}")
		r.clock.Add(20 * time.Minute)
		r.settle()
		eq(t, r.facts(), []string{"T-1 needs-you{blocked}"}, "facts 20 min later")
	})
}

func TestPaneExitedNeedsYou(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newRig(t)
		r.submitted("T-2", "L-02") // T-2 is done: its pane closing is no alarm
		r.start()
		r.srv.WaitSubscribed(t, herdr.EventExited, "")
		r.f.Remove("w1:p2")
		r.srv.Push("pane_closed", map[string]any{"pane_id": "w1:p2"})
		r.f.Remove("w1:p1")
		r.srv.Push("pane_exited", map[string]any{"pane_id": "w1:p1"})
		r.waitFacts("T-2 submitted", "T-1 needs-you{pane exited}")
		r.settle()
		eq(t, r.facts(), []string{"T-2 submitted", "T-1 needs-you{pane exited}"}, "facts after both panes went")
		if f := r.app.Status().Project.Facts; len(f) != 1 {
			t.Errorf("facts in force = %+v, want pane exited (the lane still holds T-1)", f)
		}
	})
}

func TestReplayAfterRestart(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newRig(t)
		r.f.Pane("w1:p2").Status = "working"
		r.start()
		r.clock.Add(11 * time.Minute)
		r.waitFacts("T-1 stalled{idle}")
		r.stop()

		r.clock.Add(30 * time.Minute) // long after the stall: the needs-you timer restarts with the app
		r.start()
		if st := r.app.Status(); len(st.Project.Facts) != 1 || st.Project.Facts[0].String() != "stalled{idle}" {
			t.Fatalf("facts in force after replay = %+v, want stalled{idle}", st.Project.Facts)
		}
		r.clock.Add(9 * time.Minute)
		r.settle()
		eq(t, r.facts(), []string{"T-1 stalled{idle}"}, "facts 9 min after the restart (no second stalled)")
		r.clock.Add(2 * time.Minute)
		r.waitFacts("T-1 stalled{idle}", "T-1 needs-you{stalled}")
		if recs := r.records(); recs[1].Seq != 2 {
			t.Errorf("seq after replay = %d, want 2", recs[1].Seq)
		}
	})
}

func TestPaneInAnotherGroupIgnored(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newRig(t)
		// A pane of group "other" in L-01's worktree, idle; L-01's own pane is working, L-02's idle.
		r.f.Pane("w1:p1").Status = "working"
		r.f.Agent("w2:p1", "x-l-01", "idle", r.lane("L-01"))
		r.f.Agent("w9:p1", "y", "idle", r.lane("L-01")) // a group the terminal did not name
		r.start()
		r.clock.Add(11 * time.Minute)
		r.waitFacts("T-2 stalled{idle}")
		r.settle()
		eq(t, r.facts(), []string{"T-2 stalled{idle}"}, "facts")
		for _, a := range r.app.Status().Project.Agents {
			if a.Pane != "w1:p1" && a.Pane != "w1:p2" {
				t.Errorf("status lists %s of another workspace", a.Pane)
			}
		}
		for _, req := range r.srv.Requests() {
			if req.Method == "pane.process_info" && !strings.Contains(string(req.Params), `"w1:p2"`) {
				t.Errorf("process info asked of %s, want L-02's pane only", req.Params)
			}
		}
		if r.count("pane.process_info") == 0 {
			t.Error("process info never asked of L-02's pane")
		}
	})
}

// T-635: a task's merge or drop ends its watchdog facts, a worker's question (needs-you{asks}) too, so a replay of an
// old log (T-509: asked, then merged; its lane row still naming it) has no fact in force and posts no line or repeat.
// An asks of an open task still repeats as today.
func TestClosedTaskWatchdogFactsEnd(t *testing.T) {
	t.Parallel()
	asks := event.FactData{Lane: "L-01", Agent: "pj-l-01", Reason: "asks: which colour?"}
	submitted := map[string]any{"lane": "L-01", "agent": "pj-l-01"}
	for _, c := range []struct {
		name  string
		end   []event.Record // after the asks
		lines bool           // the asks is in force: its line and repeats are posted
	}{
		{"merged", []event.Record{
			{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact, Data: raw(submitted)},
			{Task: "T-1", Attempt: 1, Step: event.StepMerged, Kind: event.KindFact, Data: raw(map[string]any{"outside": true})},
		}, false},
		{"dropped", []event.Record{
			{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact, Data: raw(submitted)},
			{Task: "T-1", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact,
				Data: raw(event.FactData{Lane: "L-01", Merge: true, Reason: "why"})},
		}, false},
		{"open", nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := newLoopRig(t)
				recs := append([]event.Record{{Task: "T-1", Attempt: 1, Step: event.StepNeedsYou, Kind: event.KindFact,
					Data: raw(asks)}}, c.end...)
				if err := app.WriteLog(r.p.Root, r.clock.Now(), recs...); err != nil {
					t.Fatal(err)
				}
				var seq int64
				for _, rec := range r.records() {
					if rec.Step == event.StepNeedsYou {
						seq = rec.Seq
					}
				}
				if seq == 0 {
					t.Fatalf("precondition: no needs-you{asks} in the log: %q", r.steps())
				}
				r.start()
				r.passes()
				r.later(31 * time.Minute) // past the first repeat's 30 min
				r.later(61 * time.Minute) // and the next's 60
				var inForce []event.Record
				for _, f := range r.app.Status().Project.Facts {
					if f.Seq == seq { // the asks (the rig's static lane row still names T-1: the watchdog may decide anew)
						inForce = append(inForce, f)
					}
				}
				lines := 0
				for _, rec := range r.needsLineFacts() {
					var m event.Message
					if json.Unmarshal(rec.Data, &m) == nil && app.NeedsSeq(m.Of) == seq {
						lines++
					}
				}
				if !c.lines {
					if len(inForce) != 0 || lines != 0 {
						t.Errorf("after the task's %s: needs-you in force %+v, lines %d; want none", c.name, inForce, lines)
					}
					return
				}
				if len(inForce) != 1 {
					t.Errorf("open task: needs-you in force %+v, want its asks", inForce)
				}
				if again := r.againLines(seq); len(again) == 0 || !strings.Contains(again[0], "which colour?") {
					t.Errorf("open task: repeats %q (lines %d), want the asks repeated", again, lines)
				}
			})
		})
	}
}

// raw is v as JSON.
func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
