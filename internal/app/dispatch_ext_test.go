package app_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/herdr/herdrtest"
	"obor/internal/store"
	"obor/internal/testkit"
)

// The level's first model on the first free lane; a spec's Model: line puts its own models first, with the effort it
// names; --model overrides the level (the dispatch fact then names no level).
func TestDifficultyTakesTheLevelsModels(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, spec, want, typ string
		effort, level         any
		model                 string // --model ("" none)
	}{
		{"the level's first model", "# T-1 — Leveled\n\nDifficulty: medium\n", "DISPATCHED T-1 → L-01 (queued) as sonnet",
			"sonnet", nil, "medium", ""},
		{"the spec's Model: line first", "# T-1 — Leveled\n\nDifficulty: medium\nModel: haiku:low, sonnet\n",
			"DISPATCHED T-1 → L-01 (queued) as haiku effort low", "haiku:low", "low", "medium", ""},
		{"--model overrides the level", "# T-1 — Leveled\n\nDifficulty: hard\n",
			"DISPATCHED T-1 → L-01 (queued) as terra effort high", "terra:high", "high", nil, "terra:high"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := newLoopRig(t, pooled())
				r.start()
				r.setSpec("s-"+c.name, c.spec)
				var reply string
				var err error
				if c.model != "" {
					reply, err = r.dispatchLevel("d1", c.model)
				} else {
					reply, err = r.dispatchLevel("d1")
				}
				if err != nil || !strings.HasPrefix(reply, c.want) {
					t.Fatalf("dispatch: %q, %v; want %q", reply, err, c.want)
				}
				if d := r.factData("T-1", event.StepDispatched); d["level"] != c.level || d["lane"] != "L-01" || d["type"] != c.typ ||
					d["effort"] != c.effort {
					t.Errorf("dispatch fact %v, want level %v, %s on L-01 with effort %v", d, c.level, c.typ, c.effort)
				}
			})
		})
	}
}

func TestDifficultyConfigOverridesTheDefault(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, pooled(), withConfig(testkit.Set("agents.difficulty.medium", []string{"luna:low"})))
		r.start()
		r.specLevel("medium")
		if reply, err := r.dispatchLevel("d1"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as luna effort low") {
			t.Fatalf("medium with difficulty.medium = [luna:low]: %q, %v", reply, err)
		}
	})
}

func TestDifficultySpecSetRefusesASpecWithoutALevel(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, pooled())
		r.start()
		for _, text := range []string{"# T-1 — A\n", "# T-1 — A\n\nDifficulty: easy\n", "# T-1 — A\n\nsome text\nDifficulty: hard\n"} {
			_, err := r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
				Role: "lead", CallID: "s"}, Task: "T-1", Spec: text})
			if err == nil || !strings.Contains(err.Error(), "hard, medium, simple") {
				t.Errorf("spec set of %q: %v; want a refusal naming the levels", text, err)
			}
		}
		for text, want := range map[string]string{
			"# T-1 — A\n\nDifficulty: hard\nType: opus\n":          `the "Type:" line is now "Model:`,
			"# T-1 — A\n\nDifficulty: hard\nModel: gpt\n":          `"Model: gpt": "gpt" is not a model`,
			"# T-1 — A\n\nDifficulty: hard\nModel: luna:minimal\n": `effort "minimal" is not one of codex's (low, medium, high, xhigh, max, ultra)`,
		} {
			_, err := r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
				Role: "lead", CallID: "s-" + want}, Task: "T-1", Spec: text})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("spec set of %q: %v; want %q", text, err, want)
			}
		}
	})
}

// The request records one fact and answers at once; the loop prepares the lane, starts a fresh worker in its pane and
// sends the work job's wake. The worker's job_next is delivery (working);
// its task_done then submits the task, and the merge loop takes it to merged.
func TestDispatchHappyPath(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, dispatching())
		r.work("feature.txt", "feature\n")
		r.start()
		const stored = "# T-1 — Happy\n\nDifficulty: medium\n\n## Files you may touch\n`feature.txt`\n"
		r.setSpec("s1", stored)
		r.passes()
		if !slices.Equal(r.steps(), []string{"0 fact spec.set"}) { // the spec's receipt only
			t.Fatalf("precondition: records before the dispatch: %q", r.steps())
		}
		// The CLI's call shape: a task and its types, no lane.
		if reply, err := r.dispatch("d1", "", "sonnet"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
			t.Fatalf("dispatch: %q, %v", reply, err)
		}
		r.waitStep("1 result work")
		eq(t, r.steps(), append([]string{"0 fact spec.set"}, dispatchSteps...), "records") // the spec's receipt first
		if r.taskState() != "dispatched" {
			t.Errorf("state %q before delivery, want dispatched", r.taskState())
		}
		job := r.waitJob("pj-l-01", "T-1/1/work")
		r.waitFor("working", func() bool { return r.taskState() == "working" })
		// The job carries the stored spec (backticks kept: it is fetched, not typed), not a git read of it.
		// It carries the kit's worker role text inline (the rig's project has no project doc).
		for _, want := range []string{"implement the spec of T-1 exactly", "obor spec show T-1", "# Role: worker",
			"Did you mean to type here?", "The spec of T-1 (rev 1", "\n\n" + stored} {
			if !strings.Contains(job, want) {
				t.Errorf("work job has no %q:\n%s", want, job)
			}
		}
		for _, no := range []string{"Read AGENTS.md and your role text", "Also read", "fill in", "## This project"} {
			if strings.Contains(job, no) {
				t.Errorf("work job carries %q (the role text is inline):\n%s", no, job)
			}
		}
		if strings.Contains(job, "git show") {
			t.Errorf("work job still reads the spec from git:\n%s", job)
		}
		r.disp.mu.Lock()
		texts := slices.Clone(r.disp.texts)
		r.disp.mu.Unlock()
		if !slices.Equal(texts, []string{stored}) {
			t.Errorf("Specs.Check got %q, want the stored spec", texts)
		}
		if strings.Contains(job, "{{") || strings.Contains(job, "[obor obor-") {
			t.Errorf("work job has a placeholder or a second sender tag:\n%s", job)
		}
		p, starts := r.disp.applied()
		if p != 1 || starts != 1 {
			t.Errorf("prepares %d, starts %d; want 1 each", p, starts)
		}
		r.disp.mu.Lock()
		in := r.disp.inputs[0]
		r.disp.mu.Unlock()
		if in.Task != "T-1" || in.Lane != "L-01" || in.Dir != r.lane || in.Branch != "task/T-1" || in.Target != "target1" ||
			in.Redo || in.At.IsZero() {
			t.Errorf("prepare input %+v", in)
		}
		if in.Agent != "pj-l-01" { // the lane's first alias: the lane state file's agent
			t.Errorf("prepare input agent %q, want pj-l-01: %+v", in.Agent, in)
		}

		r.submit()
		r.waitStep("1 fact submitted")
		job = r.waitJob("pj-int", "T-1/1/review")
		r.accept("c1", r.patch(job))
		r.waitStep("1 result agent.stop")
		r.passes()
		if r.mainMerges() != 1 {
			t.Errorf("not merged: %d merges: %q", r.mainMerges(), r.steps())
		}
		if n := r.workPrompts(); n != 0 {
			t.Errorf("work prompts = %d, want 0 (the start carried the wake)", n)
		}
		r.noLead()
	})
}

// --model runs its first model on the first free lane; a name that is no model is refused.
func TestDispatchByModels(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, dispatching())
		r.start()
		if _, err := r.dispatch("d0", "", "gpt", "sonnet"); err == nil || !strings.Contains(err.Error(), `"gpt" is not a model`) {
			t.Fatalf("an unknown model: %v", err)
		}
		if reply, err := r.dispatch("d1", "", "opus", "sonnet"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as opus" {
			t.Fatalf("by models: %q, %v", reply, err)
		}
		r.waitStep("1 result work")
	})
}

// A re-dispatch onto the lane that still holds the task is a redo again, through the CLI's call shape (a task and its
// types, no lane). A drop with the work kept leaves the lane working on the task; the re-dispatch takes that lane,
// whatever its type, and lane.prepare keeps its branch. With no other lane of the level free it is still the redo, not
// a wait for a lane that frees only when the task leaves it.
func TestDispatchRedoOntoTheTasksLane(t *testing.T) {
	t.Parallel()
	// T-1 dropped with its lane, branch and work kept: obor drop frees the lane, so the dropped fact is seeded into the
	// log of a stopped serve.
	drop := func(t *testing.T, r *loopRig) {
		t.Helper()
		r.waitFor("T-1 settled", func() bool { return app.Settled(r.app, r.p.Root, "T-1") })
		r.stop()
		r.writeLog([]logRec{{1, event.StepDropped, event.KindFact, "", event.FactData{Lane: "L-01", Merge: true,
			Reason: "continue later"}}})
		r.start()
		r.waitFor("T-1 dropped", func() bool { return r.taskState() == "dropped" })
	}
	for _, state := range []string{"working"} { // "blocked" is no lane state (git facts only)
		t.Run("drop with the work kept, lane "+state+": the same lane, its branch kept", func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := newLoopRig(t, dispatching())
				r.start()
				if reply, err := r.dispatch("d1", "", "sonnet"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
					t.Fatalf("the first dispatch: %q, %v", reply, err)
				}
				r.waitStep("1 result work")
				r.passes()
				r.disp.mu.Lock()
				first := r.disp.inputs[0]
				r.disp.mu.Unlock()
				drop(t, r)
				// The drop kept the work: L-01 still holds T-1 on its branch (no lane was freed).
				r.setLane(func(l *port.Lane) { l.State = state })
				r.lanes.mu.Lock()
				l := r.lanes.lanes[0]
				r.lanes.mu.Unlock()
				if l.Task != "T-1" || l.State != state {
					t.Fatalf("precondition: after the drop lane L-01 is %q on %q, want %s on T-1", l.State, l.Task, state)
				}
				reply, err := r.dispatch("d2", "", "sonnet")
				if err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
					t.Fatalf("the re-dispatch onto the %s lane: %q, %v; want L-01, the lane that holds the branch", state,
						reply, err)
				}
				r.waitStep("2 result work")
				r.disp.mu.Lock()
				in := r.disp.inputs[1]
				r.disp.mu.Unlock()
				if !in.Redo || in.Lane != "L-01" || in.Branch != first.Branch || in.Target != first.Target {
					t.Errorf("the redo's prepare %+v; want lane L-01 on %s at %s with redo", in, first.Branch, first.Target)
				}
			})
		})
	}
	t.Run("no other lane of the level free: the redo, not a wait", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := newLoopRig(t, pooled())
			r.lanes.mu.Lock()
			r.lanes.lanes[1].State, r.lanes.lanes[1].Task = "working", "T-9" // L-02 is busy
			r.lanes.mu.Unlock()
			r.start()
			r.specLevel("simple") // deepseek
			if reply, err := r.dispatchLevel("d1"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01") {
				t.Fatalf("the first dispatch: %q, %v", reply, err)
			}
			r.waitStep("1 result work")
			r.passes()
			r.lanes.mu.Lock()
			var free []string
			for _, l := range r.lanes.lanes {
				if l.State == "free" {
					free = append(free, l.Name)
				}
			}
			r.lanes.mu.Unlock()
			if len(free) != 0 {
				t.Fatalf("precondition: free lanes %v, want none (L-01 holds T-1)", free)
			}
			drop(t, r)
			reply, err := r.dispatchLevel("d2")
			if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01") {
				t.Fatalf("the re-dispatch with no free lane: %q, %v; want the redo onto L-01, not a wait", reply, err)
			}
			r.waitStep("2 result work")
			if n := r.taskStep("T-1", "fact waiting"); n != 0 {
				t.Errorf("the re-dispatch waited %d times: %q", n, r.steps())
			}
		})
	})
}

// Each refusal answers FAIL … (the error) and records nothing; a repeated call id gets the stored reply.
func TestDispatchRefusals(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name  string
		setup func(r *loopRig)
		call  func(c *wire.DispatchReq)
		want  string
	}{
		{name: "no call id", call: func(c *wire.DispatchReq) { c.CallID = "" }, want: "a call id and a task are required"},
		{name: "a worker calls", call: func(c *wire.DispatchReq) { c.Role, c.Agent = "worker", "pj-l-02" },
			want: "the lead or the owner dispatches, not pj-l-02"},
		{name: "no lane adapter", setup: func(r *loopRig) { r.disp.set(func(d *dispatchFakes) { d.on = false }) },
			want: "dispatch off for proj (no lane adapter)"},
		{name: "bad task id", setup: func(r *loopRig) {
			r.disp.set(func(d *dispatchFakes) { d.refuse["T-1"] = "bad task id T-1 (task_id_pattern)" })
		}, want: "dispatch T-1: bad task id T-1 (task_id_pattern)"},
		{name: "spec not on the target", setup: func(r *loopRig) {
			r.disp.set(func(d *dispatchFakes) { d.refuse["T-1"] = "no spec for T-1 in obor serve's store" })
		}, want: "no spec for T-1 in obor serve's store"},
		{name: "unknown lane", call: func(c *wire.DispatchReq) { c.Lane = "L-09" }, want: "unknown lane L-09"},
		{name: "no level and no models", call: func(c *wire.DispatchReq) { c.Lane, c.Types = "", nil }, want: "no valid \"Difficulty: <level>\" line"},
		{name: "no free lane for the models", setup: func(r *loopRig) { r.setLane(func(l *port.Lane) { l.State, l.Task = "working", "T-9" }) },
			call: func(c *wire.DispatchReq) { c.Lane, c.Types = "", []string{"opus"} }, want: "no free lane for the models opus"},
		{name: "dirty-free without force", setup: func(r *loopRig) { r.setLane(func(l *port.Lane) { l.State = "dirty-free" }) },
			want: "lane L-01 is dirty-free (uncommitted changes; --force to dispatch anyway)"},
		{name: "working on another task", setup: func(r *loopRig) {
			r.setLane(func(l *port.Lane) { l.State, l.Task = "working", "T-9" })
		}, want: "lane L-01 is working on T-9"},
		{name: "blocked on another task", setup: func(r *loopRig) {
			r.setLane(func(l *port.Lane) { l.State, l.Task = "blocked", "T-9" })
		}, want: "lane L-01 is blocked"},
		{name: "done", setup: func(r *loopRig) { r.setLane(func(l *port.Lane) { l.State, l.Task = "done", "T-9" }) },
			want: "lane L-01 is done"},
		// A lane whose git or store read failed is unknown: never picked, and the refusal names it and why.
		{name: "unknown lane state", setup: func(r *loopRig) {
			r.setLane(func(l *port.Lane) { l.State, l.Reason = "unknown", "git status: exit status 1" })
		}, call: func(c *wire.DispatchReq) { c.Lane, c.Types = "", []string{"sonnet"} },
			want: "no free lane for the models sonnet; L-01 unknown: git status: exit status 1"},
		{name: "named lane unknown", setup: func(r *loopRig) {
			r.setLane(func(l *port.Lane) { l.State, l.Reason = "unknown", "store: no such table: lanes" })
		}, want: "lane L-01 is unknown: store: no such table: lanes"},
		// The CLI's shape: a task and its types, no lane; the branch's commits make only a redo legal.
		{name: "task branch ahead", setup: func(r *loopRig) { r.disp.set(func(d *dispatchFakes) { d.ahead = true }) },
			call: func(c *wire.DispatchReq) { c.Lane, c.Types = "", []string{"sonnet"} },
			want: "task/T-1 exists with commits not in the target"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := newLoopRig(t, dispatching())
				if row.setup != nil {
					row.setup(r)
				}
				r.start()
				c := wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: "d1"},
					Task: "T-1",
					Lane: "L-01", Types: []string{"sonnet"}}
				if row.call != nil {
					row.call(&c)
				}
				reply, err := r.dispatchCall(c)
				if err == nil || !strings.Contains(err.Error(), row.want) || reply != "" {
					t.Fatalf("reply %q, error %v; want an error with %q", reply, err, row.want)
				}
				r.passes()
				if n := len(r.records()); n != 0 {
					t.Errorf("a refusal recorded %q", r.steps())
				}
				if n := r.disp.called("prepare"); n != 0 {
					t.Errorf("prepared %d times after a refusal", n)
				}
			})
		})
	}
	t.Run("task open, then the call id again", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := newLoopRig(t, dispatching())
			r.start()
			r.mustDispatch("d1")
			r.waitStep("1 result work")
			r.setLane(func(l *port.Lane) { l.State, l.Task = "free", "" })
			if _, err := r.dispatch("d2", "L-01"); err == nil || !strings.Contains(err.Error(), "T-1 is open in obor serve (dispatched)") {
				t.Errorf("a second dispatch of the open task: %v", err)
			}
			if reply, err := r.dispatch("d1", "L-01"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
				t.Errorf("the first call id again: %q, %v; want its stored reply", reply, err)
			}
			r.passes()
			if n := r.count("1 fact dispatch") + r.count("2 fact dispatch"); n != 1 {
				t.Errorf("dispatch facts %d, want 1: %q", n, r.steps())
			}
		})
	})
	t.Run("lane held by another task's dispatch", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := newLoopRig(t, dispatching())
			r.disp.set(func(d *dispatchFakes) { d.fail["prepare"] = func() error { return errors.New("git I/O") } })
			r.start()
			r.mustDispatch("d1")
			r.waitStep("1 fact failed{lane.prepare: git I/O}")
			c := wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Role: "lead", CallID: "d2"}, Task: "T-2",
				Lane:  "L-01",
				Types: []string{"sonnet"}}
			if _, err := r.dispatchCall(c); err == nil || !strings.Contains(err.Error(), "lane L-01 is dispatched to T-1") {
				t.Errorf("a dispatch onto the held lane: %v", err)
			}
		})
	})
}

// A level none of whose models the config knows names the shape-2 config path in its hint.
func TestNoModelOfLevelNamesShape2Path(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, dispatching(), withConfig(testkit.Set("agents.difficulty.hard", []string{})))
		r.start()
		r.specOf("T-1", "# T-1 — Leveled\n\nDifficulty: hard\n")
		reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
			Role: "lead", CallID: "d1"}, Task: "T-1", Lane: "L-01"})
		if err == nil || !strings.Contains(err.Error(), "no model of level hard is known to this project (obor config get .agents.difficulty)") || reply != "" {
			t.Fatalf("dispatch with no hard model: %q, %v; want the shape-2 config path", reply, err)
		}
	})
}

// A crash at each step (before its effect, or after it with its result lost: the effect is cut off by obor serve's
// stop and the result never written) replays to one effect and the same final state. lane.prepare (G) runs again and
// converges; agent.start (S) reads the snapshot: a new session in the pane is the start, the same one
// starts again; the work job's wake rides on the start, never typed on replay.
func TestDispatchCrashAtEachStep(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		method, where string
		// want: what the fakes applied in all (prepares, starts), the calls of the method.
		prepares, starts, calls int
	}{
		{"prepare", "before", 1, 1, 2},
		{"prepare", "after", 2, 1, 2},
		{"start", "before", 1, 1, 2}, // the same session in the pane: started again
		{"start", "after", 1, 1, 1},  // a new session: started, no second Restart
	} {
		t.Run(row.method+" "+row.where, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := newLoopRig(t, dispatching())
				r.disp.set(func(d *dispatchFakes) { d.cut[row.method] = row.where })
				r.start()
				r.mustDispatch("d1")
				r.waitFor(row.method+" cut "+row.where, func() bool {
					r.disp.mu.Lock()
					defer r.disp.mu.Unlock()
					return r.disp.hit[row.method]
				})
				r.stop()
				r.disp.set(func(d *dispatchFakes) { delete(d.cut, row.method) })
				r.start()
				r.waitStep("1 result work")
				r.passes()
				eq(t, r.steps(), dispatchSteps, "records")
				p, starts := r.disp.applied()
				if p != row.prepares || starts != row.starts || r.disp.called(row.method) != row.calls {
					t.Errorf("prepares %d, starts %d, %s calls %d; want %d, %d, %d", p, starts,
						row.method, r.disp.called(row.method), row.prepares, row.starts, row.calls)
				}
				same(t, r.result("work"), `"sent (start prompt)"`, "work result")
				if n := r.workPrompts(); n != 0 {
					t.Errorf("work prompts %d, want 0 (the start carried the wake)", n)
				}
			})
		})
	}
}

// A final refusal from Prepare or the launch is a needs-you (lane: …, start: …); three counted failures of a step are
// one too. obor resume retries (its default): a refused step runs again under its next key, a failed one under its
// own.
func TestDispatchNeedsYouThenResume(t *testing.T) {
	t.Parallel()
	t.Run("prepare refused", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := newLoopRig(t, dispatching())
			r.disp.set(func(d *dispatchFakes) {
				d.fail["prepare"] = func() error { return port.Refused{Line: "lane_setup in L-01: rc 1: boom"} }
			})
			r.start()
			r.mustDispatch("d1")
			r.waitStep("1 fact needs-you{lane: lane_setup in L-01: rc 1: boom}")
			r.passes()
			if n := r.disp.called("prepare"); n != 1 || r.has("1 intent agent.start") {
				t.Errorf("prepare calls %d (want 1), or a start after the refusal: %q", n, r.steps())
			}
			r.needsYouState()
			if _, err := r.resume("r0", wire.ResumeRegate); err == nil || !strings.Contains(err.Error(), "--regate does not fit: obor resume T-1 retries the failed step") {
				t.Errorf("regate of a dispatch's needs-you: %v", err)
			}
			r.disp.set(func(d *dispatchFakes) { delete(d.fail, "prepare") })
			r.needsYouState()
			r.mustResume("r1", "", "RESUMED T-1: retry (lane)")
			r.waitStep("1 result lane.prepare.2")
			r.waitStep("1 result work")
		})
	})
	t.Run("start refused", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := newLoopRig(t, dispatching())
			r.disp.set(func(d *dispatchFakes) {
				d.fail["start"] = func() error { return errors.New("pj-l-01 has background tasks (--force stops them)") }
			})
			r.start()
			r.mustDispatch("d1")
			r.waitPrefix("1 fact needs-you{start: pj-l-01 has background tasks")
			r.disp.set(func(d *dispatchFakes) { delete(d.fail, "start") })
			r.needsYouState()
			r.mustResume("r1", "", "RESUMED T-1: retry (start)")
			r.waitStep("1 result agent.start.2")
			r.waitStep("1 result work")
		})
	})
	t.Run("start refused by the launch's check: one needs-you, no retries", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := newLoopRig(t, dispatching())
			const line = "codex is quarantined: /c/0.167.0/bin/codex-code-mode-host carry com.apple.quarantine; a Codex start " +
				"is refused until the owner clears the flag: xattr -dr com.apple.quarantine /c/0.167.0 (obor applies no fix)"
			r.disp.set(func(d *dispatchFakes) {
				d.fail["start"] = func() error { return port.Refused{Line: line} }
			})
			r.start()
			r.mustDispatch("d1")
			r.waitPrefix("1 fact needs-you{start: " + line + "}")
			r.passes()
			if n := r.disp.called("start"); n != 1 {
				t.Errorf("start calls %d while the refusal holds, want 1 (a final refusal, not a counted failure)", n)
			}
			r.needsYouState()
			r.disp.set(func(d *dispatchFakes) { delete(d.fail, "start") })
			r.mustResume("r1", "", "RESUMED T-1: retry (start)")
			r.waitStep("1 result agent.start.2")
			r.waitStep("1 result work")
		})
	})
	t.Run("three counted failures", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := newLoopRig(t, dispatching())
			r.disp.set(func(d *dispatchFakes) { d.fail["prepare"] = func() error { return errors.New("git I/O") } })
			r.start()
			r.mustDispatch("d1")
			r.waitStep("1 fact needs-you{lane.prepare failed: git I/O}")
			r.passes()
			if n := r.disp.called("prepare"); n != 3 || r.count("1 intent lane.prepare") != 1 {
				t.Errorf("prepare calls %d (want 3) under one intent: %q", n, r.steps())
			}
			r.disp.set(func(d *dispatchFakes) { delete(d.fail, "prepare") })
			r.needsYouState()
			r.mustResume("r1", "", "RESUMED T-1: retry (lane.prepare failed)")
			r.waitStep("1 result lane.prepare")
			r.waitStep("1 result work")
			if r.count("1 intent lane.prepare") != 1 {
				t.Errorf("the retry wrote a second intent: %q", r.steps())
			}
		})
	})
	t.Run("no pane in the lane", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := newLoopRig(t, dispatching())
			r.f.Locked(func() {
				r.f.Panes = slices.DeleteFunc(r.f.Panes, func(p *herdrtest.Pane) bool { return p.ID == "w1:p1" })
			})
			r.start()
			r.mustDispatch("d1")
			r.waitStep("1 result lane.prepare")
			r.passes()
			r.later(time.Minute)
			r.waitPrefix("1 fact needs-you{no pane in lane L-01")
			if r.has("1 intent agent.start") {
				t.Errorf("a start with no pane: %q", r.steps())
			}
		})
	})
	// obor setup pane moved a pane into the lane after serve started (its shell cd'd there, and a cd gives no herdr
	// event), so the snapshot keeps its old cwd. Herdr's panes are read again before the needs-you: the start goes
	// ahead. When herdr has no pane in the lane either, the needs-you goes out.
	t.Run("a pane moved into the lane", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := newLoopRig(t, dispatching())
			r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Cwd = r.p.Root }) // the snapshot serve took: the pane is outside
			r.start()
			r.mustDispatch("d1")
			r.waitStep("1 result lane.prepare")
			r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Cwd = r.lane }) // moved with no event: the snapshot's cwd is stale
			r.passes()
			r.later(time.Minute)
			r.waitStep("1 result agent.start")
			if r.hasPrefix("1 fact needs-you{no pane in lane") {
				t.Errorf("needs-you with a pane in the lane: %q", r.steps())
			}
			r.waitStep("1 result work")
		})
	})
	t.Run("herdr has no pane in the lane either", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := newLoopRig(t, dispatching())
			r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Cwd = r.p.Root })
			r.start()
			r.mustDispatch("d1")
			r.waitStep("1 result lane.prepare")
			r.passes()
			r.later(time.Minute)
			r.waitPrefix("1 fact needs-you{no pane in lane L-01")
			if r.has("1 intent agent.start") {
				t.Errorf("a start with no pane: %q", r.steps())
			}
		})
	})
}

// agent.start names the lane's worker by the lane's derived name (<prefix>-<lane lower>: pj-l-01; no lane row stores
// one). A pane's label is no name, and herdr's live agent name never counts.
func TestDispatchKeepsTheLaneAgentName(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name         string
		label, agent string // w1:p1's label and the agent running in it ("": a shell)
		want         string
	}{
		{"a pane labelled an old alias: the derived name", "pj-l1", "", "pj-l-01"},
		{"live name and label conflict: the derived name", "pj-l1", "pj-l-01", "pj-l-01"},
		{"label a lane name, a live name the old alias: the derived name", "pj-l-01", "pj-l1", "pj-l-01"},
		{"a live name, no label: the derived name", "", "pj-l1", "pj-l-01"},
		{"label not a lane name", "notes", "", "pj-l-01"},
		{"neither", "", "", "pj-l-01"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := newLoopRig(t, dispatching())
				r.f.Set("w1:p1", func(p *herdrtest.Pane) {
					p.Label, p.Name = row.label, row.agent
					if row.agent == "" {
						p.Kind, p.Status = "", ""
					}
				})
				r.start()
				r.mustDispatch("d1")
				r.waitStep("1 intent agent.start")
				r.disp.mu.Lock()
				inputs := slices.Clone(r.disp.inputs)
				r.disp.mu.Unlock()
				if len(inputs) != 1 {
					t.Fatalf("prepares %d, want 1", len(inputs))
				}
				if inputs[0].Agent != row.want {
					t.Errorf("prepare agent %q, want %q", inputs[0].Agent, row.want)
				}
				r.waitFor("the start", func() bool { r.mu.Lock(); defer r.mu.Unlock(); return len(r.launches) > 0 })
				r.mu.Lock()
				launch := r.launches[0]
				r.mu.Unlock()
				if launch.Agent != row.want || launch.Pane != "w1:p1" {
					t.Errorf("agent.start launched %q in %q, want %q in w1:p1", launch.Agent, launch.Pane, row.want)
				}
			})
		})
	}
}

// A task that merged takes no new attempt, whatever flags the request carries: refused, nothing recorded. A dropped
// task is dispatched again.
func TestDispatchOfAnEndedTask(t *testing.T) {
	t.Parallel()
	t.Run("merged: refused with each flag, nothing recorded", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := newLoopRig(t, dispatching())
			r.writeLog([]logRec{{1, event.StepSubmitted, event.KindFact, "", map[string]any{"lane": "L-01", "agent": "pj-l-01"}}, {1, event.StepMerged, event.KindFact, "", event.MergedData{Head: "0123456789abcdef"}}})
			r.start()
			r.passes()
			if st := r.taskState(); st != "" && st != "merged" {
				t.Fatalf("precondition: state %q", st)
			}
			r.waitFor("the idle state of the merged task", func() bool { return r.app.Status().Project.Idle != nil })
			before := len(r.records())
			if r.count("1 fact merged") != 1 {
				t.Fatalf("precondition: no merged fact in %q", r.steps())
			}
			for i, c := range []wire.DispatchReq{
				{Task: "T-1"},
				{Task: "T-1", Types: []string{"sonnet"}},
				{Task: "T-1", Types: []string{"sonnet"}, Lane: "L-01"},
				{Task: "T-1", Types: []string{"sonnet"}, After: []string{"T-2"}},
				{Task: "T-1", Types: []string{"sonnet"}, Lane: "L-01", Force: true},
			} {
				c.Root, c.Agent, c.Role, c.CallID = r.p.Root, "pj-lead", "lead", fmt.Sprintf("d%d", i)
				reply, err := r.dispatchCall(c)
				if err == nil || err.Error() != "T-1 merged 0123456: a new attempt needs a new task id" || reply != "" {
					t.Errorf("call %+v: reply %q, error %v; want the merged refusal", c, reply, err)
				}
			}
			r.passes()
			for _, s := range r.steps()[before:] { // the idle announce and its line are the idle state's own
				if strings.Contains(s, "idle") || strings.HasSuffix(s, " fact message") || strings.Contains(s, "deliver.") {
					continue
				}
				t.Errorf("a refusal recorded %q", s)
			}
			if n := r.disp.called("prepare"); n != 0 {
				t.Errorf("prepared %d times after a refusal", n)
			}
		})
	})
	t.Run("dropped: dispatched again", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := newLoopRig(t, dispatching())
			r.writeLog([]logRec{{1, event.StepSubmitted, event.KindFact, "", map[string]any{"lane": "L-01", "agent": "pj-l-01"}}, {1, event.StepDropped, event.KindFact, "", event.FactData{Lane: "L-01", Merge: true, Reason: "why"}}})
			r.start()
			r.passes()
			if reply, err := r.dispatch("d1", "L-01"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
				t.Fatalf("dispatch of a dropped task: %q, %v", reply, err)
			}
		})
	})
}

// A spec's Fixes: line must name the open review (RV-5 once started and its file written), or obor dispatch refuses it
// with nothing recorded; naming it, the dispatch goes on.
func TestDispatchFixesLine(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, line string
		open       bool
		want       string // "": dispatched
	}{
		{name: "none open", line: "Fixes: RV-5", want: "T-1: Fixes: RV-5 is not the open review (none is open)"},
		{name: "another open", line: "Fixes: RV-4 #2", open: true,
			want: "T-1: Fixes: RV-4 is not the open review (RV-5 is open)"},
		{name: "the open one", line: "Fixes: RV-5 #1, #3 — re-run the probe", open: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := newLoopRig(t, dispatching(), withConfig(testkit.Set("agents.roles.reviewer", ""), testkit.Set("reviews.dir", "docs/reviews")))
				r.review = lastClose{}
				if c.open {
					r.reviewsLog("RV-5")
					r.p.Write("docs/reviews/RV-5.md", "# RV-5\n")
				} else {
					r.reviewsLog()
				}
				r.start()
				r.setSpec("s1", "# T-1 — Fix\n\nDifficulty: medium\n"+c.line+"\n\n## Do\nfix it\n")
				reply, err := r.dispatch("d1", "L-01", "sonnet")
				if c.want == "" {
					if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01") {
						t.Fatalf("dispatch: %q, %v; want dispatched", reply, err)
					}
					return
				}
				if err == nil || err.Error() != c.want || reply != "" {
					t.Fatalf("dispatch: %q, %v; want the refusal %q", reply, err, c.want)
				}
				want := []string{"0 fact review.started", "0 fact review.closed", "0 fact spec.set"} // reviewsLog's, the spec's receipt
				if c.open {
					want = slices.Insert(want, 2, "0 fact review.started")
				}
				if !slices.Equal(r.steps(), want) {
					t.Errorf("a refused dispatch recorded %q", r.steps())
				}
			})
		})
	}
}

// Effort beside the model. A model ref `opus:high` (the built-in hard level, a spec's Model: line, --model):
// the dispatch records and replies the effort, the worker's start request carries it, and status shows it.

// effortRig is a dispatch rig whose lane L-01 (agent pj-l-01) is free.
func effortRig(t *testing.T, edits ...func(map[string]any)) *loopRig {
	t.Helper()
	r := newLoopRig(t, dispatching(), withConfig(edits...))
	r.setLane(func(l *port.Lane) { l.Agent = "pj-l-01" })
	return r
}

func TestEffortHardLevelStartsAnOpusLaneAtHigh(t *testing.T) {
	bubble(t, func(t *testing.T) {
		r := effortRig(t)
		r.start()
		r.specLevel("hard") // the built-in hard level is opus:high
		reply, err := r.dispatchLevel("d1")
		if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as opus effort high") {
			t.Fatalf("hard dispatch: %q, %v; want L-01 at effort high", reply, err)
		}
		if d := r.factData("T-1", event.StepDispatched); d["effort"] != "high" || d["lane"] != "L-01" || d["type"] != "opus:high" {
			t.Errorf("dispatch fact %v, want effort high and type opus:high on L-01 (T-721)", d)
		}
		r.waitFor("the worker's start", func() bool { _, ok := r.launchOf("pj-l-01"); return ok })
		if row, ok := r.p.Row("L-01"); !ok || row.Task != "T-1" || row.Type != "opus:high" {
			t.Errorf("lane row %+v (%v), want T-1 with type opus:high (T-721)", row, ok)
		}
		if l, _ := r.launchOf("pj-l-01"); l.Effort != "high" || !l.Fresh || l.Type != "opus" {
			t.Errorf("worker start %+v, want a fresh start of the dispatch's type opus at effort high (T-722)", l)
		}
		if st := r.taskStatus("T-1"); st.Effort != "high" {
			t.Errorf("status %+v, want effort high", st)
		}
	})
}

// A spec's Model: line or --model names the effort; a plain type runs the kind's default (no effort).
func TestEffortFromTheSpecAndTheFlag(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, spec, typ, want, effort string
		flag, exact                   bool
	}{
		{"the spec's Model: line", "# T-1 — Typed\n\nDifficulty: medium\nModel: opus:max\n", "", "DISPATCHED T-1 → L-01 (queued) as opus effort max", "max", false, false},
		{"--model with an effort", "", "opus:low", "DISPATCHED T-1 → L-01 (queued) as opus effort low", "low", true, false},
		{"a plain type", "", "opus", "DISPATCHED T-1 → L-01 (queued) as opus", "", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := effortRig(t)
				r.start()
				if c.spec != "" {
					r.setSpec("s1", c.spec)
				} else {
					r.specLevel("medium")
				}
				var reply string
				var err error
				if c.flag {
					reply, err = r.dispatchLevel("d1", c.typ)
				} else {
					reply, err = r.dispatchLevel("d1")
				}
				if err != nil || (c.exact && reply != c.want) || (!c.exact && !strings.HasPrefix(reply, c.want)) {
					t.Fatalf("dispatch: %q, %v; want %q", reply, err, c.want)
				}
				d := r.factData("T-1", event.StepDispatched)
				wantType := "opus"
				if c.effort != "" {
					wantType += ":" + c.effort
				}
				if d["type"] != wantType || (c.effort == "" && d["effort"] != nil) || (c.effort != "" && d["effort"] != c.effort) {
					t.Errorf("dispatch fact %v, want type %s with effort %q", d, wantType, c.effort)
				}
				r.waitFor("the worker's start", func() bool { _, ok := r.launchOf("pj-l-01"); return ok })
				if l, _ := r.launchOf("pj-l-01"); l.Effort != c.effort || l.Type != "opus" {
					t.Errorf("worker start %+v, want type opus and effort %q", l, c.effort)
				}
			})
		})
	}
}

func TestEffortRefusals(t *testing.T) {
	bubble(t, func(t *testing.T) {
		r := effortRig(t)
		r.start()
		r.specLevel("medium")
		if _, err := r.dispatchLevel("d1", "opus:turbo"); err == nil || !strings.Contains(err.Error(), `effort "turbo" is not one of claude's`) {
			t.Errorf("--types opus:turbo: %v; want a refusal naming claude's efforts", err)
		}
		if n := r.taskStep("T-1", "fact dispatch"); n != 0 {
			t.Errorf("a refused dispatch recorded %d dispatch facts", n)
		}
		set := func(callID, typ string) error {
			_, err := r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
				Role: "lead", CallID: callID}, Task: "T-2",
				Spec: "# T-2 — A\n\nDifficulty: medium\nModel: " + typ + "\n"})
			return err
		}
		if err := set("s1", "opus:turbo"); err == nil || !strings.Contains(err.Error(), `effort "turbo" is not one of claude's`) {
			t.Errorf("spec set Model: opus:turbo: %v; want a refusal", err)
		}
		if err := set("s2", "ghost:high"); err == nil || !strings.Contains(err.Error(), `"ghost" is not a model`) {
			t.Errorf("spec set Model: ghost:high: %v; want the unknown model refused", err)
		}
		if err := set("s3", "opus:high"); err != nil {
			t.Errorf("spec set Model: opus:high: %v", err)
		}
	})
}

// redoRig is the pool rig whose lane table reads L-01 as serve's own adapter does once T-1's work is committed there:
// working on T-1, its type the lane row's bare model (opus; checks.Lanes over git: internal/checks' TestLaneStates).
// T-1's dispatch ran opus:high on L-01 (prepared, so its lane row holds it with that type) and left a needs-you.
func redoRig(t *testing.T) *loopRig {
	t.Helper()
	r := newLoopRig(t, pooled())
	r.setLane(func(l *port.Lane) { l.State, l.Task, l.Type = "working", "T-1", "opus" })
	d, _ := json.Marshal(map[string]any{"lane": "L-01", "agent": "pj-l-01", "branch": "task/T-1", "type": "opus:high",
		"level": "hard", "types": []string{"opus:high"}})
	prep, _ := json.Marshal(map[string]any{"base": "b"})
	stop, _ := json.Marshal(event.FactData{Lane: "L-01", Reason: "stopped: api error", Merge: true, Cause: "stopped"})
	if err := app.WriteLog(r.p.Root, time.Now(),
		event.Record{Task: "T-1", Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact, Data: d},
		event.Record{Task: "T-1", Attempt: 1, Step: event.StepPrepare, Kind: event.KindResult, Data: prep},
		event.Record{Task: "T-1", Attempt: 1, Step: event.StepNeedsYou, Kind: event.KindFact, Data: stop}); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.waitFor("T-1's needs-you", func() bool { return r.taskStatus("T-1").State == "needs-you" })
	r.waitFor("L-01's row with T-1 as opus:high", func() bool {
		row, ok, _ := store.ReadLaneAt(r.p.Root, "L-01")
		return ok && row.Task == "T-1" && row.Type == "opus:high"
	})
	r.waitFor("L-01 working on T-1 as opus in serve's lane table", func() bool {
		l, ok := app.CachedLane(r.app, r.p.Root, "L-01")
		return ok && l.State == "working" && l.Task == "T-1" && l.Type == "opus"
	})
	r.specOf("T-1", "# T-1 — Redone\n\nDifficulty: medium\n")
	return r
}

// A redo by task ID through serve's real lane table keeps the full model ref its dispatch ran (opus:high), not the lane
// row's bare model, nor the spec's level (medium: sonnet); a pause of opus does not hold it (its own model).
func TestRedoViaTheRealLaneTableKeepsItsTypeAndEffort(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := redoRig(t)
		if _, err := r.ask(&wire.PauseReq{Envelope: wire.Envelope{Root: r.p.Root,
			CallID: "p1"}, Model: "opus", Why: "quota"}); err != nil {
			t.Fatal(err)
		}
		if reply, err := r.dispatchOf("T-1"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as opus effort high") {
			t.Fatalf("redo of T-1: %q, %v; want L-01 as opus effort high", reply, err)
		}
		if types := r.redoTypes(); len(types) != 2 || types[1] != "opus:high" {
			t.Errorf("T-1's dispatch facts' types %v, want the redo's opus:high after the first", types)
		}
	})
}

// A redo with --model sonnet runs sonnet on its lane; to a paused model it is refused, as a fresh dispatch of that
// model is.
func TestRedoWithExplicitTypesRunsThem(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := redoRig(t)
		if _, err := r.ask(&wire.PauseReq{Envelope: wire.Envelope{Root: r.p.Root,
			CallID: "p1"}, Model: "haiku", Why: "quota"}); err != nil {
			t.Fatal(err)
		}
		if reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
			Role: "lead", CallID: "d-T-1h"}, Task: "T-1",
			Types: []string{"haiku"}}); err == nil || !strings.Contains(err.Error(), "haiku paused: quota") {
			t.Fatalf("redo of T-1 --model haiku while haiku is paused: %q, %v; want refused naming the pause", reply, err)
		}
		reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
			Role: "lead", CallID: "d-T-1"}, Task: "T-1", Types: []string{"sonnet"}})
		if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as sonnet") {
			t.Fatalf("redo of T-1 --model sonnet: %q, %v; want L-01 as sonnet", reply, err)
		}
		if types := r.redoTypes(); len(types) != 2 || types[1] != "sonnet" {
			t.Errorf("T-1's dispatch facts' types %v, want the redo's sonnet after the first", types)
		}
	})
}

// --model naming no model of the project, bare or with an effort, is refused before a lane is picked: no dispatch
// fact, nor, with --after, a waiting fact.
func TestUnknownExplicitTypeIsRefused(t *testing.T) {
	for _, c := range []struct {
		name  string
		types []string
		after []string
	}{
		{"bare", []string{"lunna"}, nil},
		{"with an effort", []string{"lunna:high"}, nil},
		{"after a known one", []string{"sonnet", "lunna"}, nil},
		{"with --after", []string{"lunna"}, []string{"T-9"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := newLoopRig(t, pooled())
				r.start()
				r.specOf("T-9", "# T-9 — First\n\nDifficulty: medium\n")
				r.specOf("T-1", "# T-1 — Typed\n\nDifficulty: medium\n")
				reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
					Role: "lead", CallID: "d-T-1"}, Task: "T-1", Types: c.types, After: c.after})
				if err == nil || !strings.Contains(err.Error(), `"lunna" is not a model (models: `) {
					t.Fatalf("dispatch --model %v: %q, %v; want refused naming lunna", c.types, reply, err)
				}
				for _, step := range []string{"fact dispatch", "fact waiting"} {
					if n := r.taskStep("T-1", step); n != 0 {
						t.Errorf("%s recorded for the refused dispatch: %q", step, r.steps())
					}
				}
				// The same project takes a known model: the refusal is the model's.
				if reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
					Role: "lead", CallID: "d-T-1b"}, Task: "T-1", Types: []string{"sonnet"}, After: c.after}); err != nil ||
					!strings.HasPrefix(reply, "DISPATCHED T-1") && !strings.HasPrefix(reply, "WAITING T-1") {
					t.Errorf("dispatch --model sonnet: %q, %v; want it taken", reply, err)
				}
			})
		})
	}
}
