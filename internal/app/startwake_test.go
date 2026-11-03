package app_test

import (
	"testing"
	"time"

	"obor/internal/testkit"
)

// T-387: a fresh start whose first prompt is the work job's wake delivers that job (no second send through herdr),
// also after a replay that finds the start ran (seen in the snapshot); a work.N with no start keeps the typed path. A lane of a kind obor
// has no bridge for is not dispatched (DD-v0.13 D1: every bridged kind takes a first prompt).

func TestStartPromptDelivery(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name string
		run  func(t *testing.T)
	}{
		{"fresh start with wake: work sent (start prompt), nothing typed", func(t *testing.T) {
			r := fetchRig(t)
			r.start()
			r.mustDispatch("d1")
			r.waitStep("1 result work")
			r.passes()
			if r.disp.called("start") != 1 {
				t.Fatalf("precondition: one start: %d", r.disp.called("start"))
			}
			same(t, r.result("work"), `"sent (start prompt)"`, "work result")
			same(t, r.disp.called("prompt"), 0, "Prompter calls for the work job")
			same(t, len(r.prompts()), 0, "herdr prompts")
		}},
		{"fetch while the start runs: the job, delivered once; work sent (start prompt), nothing typed", func(t *testing.T) {
			r := fetchRig(t)
			gate := make(chan struct{})
			r.disp.set(func(d *dispatchFakes) { d.gate = gate })
			r.start()
			r.mustDispatch("d1")
			r.waitStep("1 intent agent.start")
			r.waitFor("pj-l-01 up", func() bool { r.disp.mu.Lock(); defer r.disp.mu.Unlock(); return r.disp.starts == 1 })
			if r.has("1 result agent.start") {
				t.Fatalf("precondition: the start pending: %q", r.steps())
			}
			if jr := r.workerNext("n1"); jr.State != "job" || jr.Job != "T-1/1/work" {
				t.Fatalf("job_next during the start: %+v, want T-1/1/work", jr)
			}
			r.waitFor("delivered", func() bool { return len(r.deliveries()) == 1 })
			close(gate)
			r.waitStep("1 result work")
			r.passes()
			r.workerNext("n2")
			r.passes()
			eq(t, r.deliveries(), []string{"T-1/1/work mcp"}, "delivered facts")
			same(t, r.result("work"), `"sent (start prompt)"`, "work result")
			same(t, len(r.prompts()), 0, "herdr prompts")
			r.waitFor("working", func() bool { return r.taskState() == "working" })
			r.later(20 * time.Minute)
			if r.has("1 intent retry.work") || r.hasPrefix("1 fact needs-you") {
				t.Errorf("a timer fired for a job fetched during its start: %q", r.steps())
			}
		}},
		{"replay after the start, seen started: work sent (start prompt), nothing typed", func(t *testing.T) {
			r := fetchRig(t)
			r.disp.set(func(d *dispatchFakes) { d.cut["start"] = "after" })
			r.start()
			r.mustDispatch("d1")
			r.waitFor("the start cut", func() bool { r.disp.mu.Lock(); defer r.disp.mu.Unlock(); return r.disp.hit["start"] })
			r.stop()
			if !r.has("1 intent agent.start") || r.has("1 result agent.start") {
				t.Fatalf("precondition: the start pending: %q", r.steps())
			}
			r.disp.set(func(d *dispatchFakes) { delete(d.cut, "start") })
			r.start()
			r.waitStep("1 result work")
			r.passes()
			same(t, r.result("agent.start"), `{"line":"started (seen in the snapshot)","agent":"pj-l-01","pane":"w1:p1"}`,
				"the replayed start")
			same(t, r.disp.called("start"), 1, "starts")
			same(t, r.result("work"), `"sent (start prompt)"`, "work result")
			same(t, r.disp.called("prompt"), 0, "Prompter calls for the work job")
		}},
		{"a lane whose kind has no bridge: the dispatch refused, nothing recorded (DD-v0.13 D1)", func(t *testing.T) {
			r := newDispatchRig(t, testkit.Set("models.sonnet", map[string]any{"kind": "pi", "model": "x"}))
			r.start()
			reply, err := r.dispatch("d1", "L-01")
			if err == nil || err.Error() != "dispatch T-1: model sonnet's kind pi has no bridge (obor config validate)" {
				t.Fatalf("dispatch: %q, %v; want refused: no bridge", reply, err)
			}
			r.passes()
			same(t, len(r.records()), 0, "records")
			same(t, r.disp.called("start"), 0, "starts")
		}},
		{"work.N after a sendback (no start): sent through herdr as today", func(t *testing.T) {
			r := fetchRig(t)
			r.start()
			r.mustDispatch("d1")
			r.waitStep("1 result work")
			r.later(5 * time.Minute)
			r.waitStep("1 result retry.work")
			r.later(5 * time.Minute)
			r.needsYouState()
			r.mustResume("r1", "", "RESUMED T-1: retry (job not delivered)")
			r.waitStep("1 result work.2")
			same(t, r.disp.called("start"), 1, "precondition: no second start")
			same(t, r.result("work.2"), `"sent"`, "work.2 result")
			eq(t, r.promptsTo("pj-l-01"), []string{wake("T-1/1/work"), wake("T-1/1/work.2")}, "typed to pj-l-01")
		}},
	} {
		t.Run(row.name, row.run)
	}
}

// same fails t when got is not want.
func same[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}
