package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
)

// T-400: an intent of an ended task (merged, dropped) is not pending on replay: it is settled as nothing ("not run"),
// never run again; one of an open task replays as today. The logs are what an earlier build left (T-385: dropped with
// agent.start and work open; T-386: merged with an earlier attempt's work open).

// logRec is one record a case writes to the log before the app starts.
type logRec struct {
	attempt         int
	step, kind, cls string
	data            any
}

// writeLog appends recs to T-1's log of a stopped app.
func (r *loopRig) writeLog(recs []logRec) {
	r.t.Helper()
	var out []event.Record
	for _, x := range recs {
		var raw json.RawMessage
		if x.data != nil {
			raw, _ = json.Marshal(x.data)
		}
		out = append(out, event.Record{Task: "T-1", Attempt: x.attempt, Step: x.step, Kind: x.kind, Class: x.cls, Data: raw})
	}
	if err := app.WriteLog(r.p.Root, time.Now(), out...); err != nil {
		r.t.Fatal(err)
	}
}

// resultOf is the data of T-1's result of step at attempt ("": none).
func (r *loopRig) resultOf(attempt int, step string) string {
	for _, rec := range r.records() {
		if rec.Kind == event.KindResult && rec.Attempt == attempt && rec.Step == step {
			return string(rec.Data)
		}
	}
	return ""
}

func TestReplayEndedTaskIntents(t *testing.T) {
	t.Parallel()
	submitted := func(n int) logRec {
		return logRec{n, event.StepSubmitted, event.KindFact, "", map[string]any{"lane": "L-01", "agent": "pj-l-01"}}
	}
	submit := logRec{1, event.StepSubmit, event.KindIntent, event.ClassGit, map[string]any{"lane": "L-01", "agent": "pj-l-01"}}
	work := func(n int) logRec {
		return logRec{n, event.StepWork, event.KindIntent, event.ClassPrompt, map[string]any{"agent": "pj-l-01",
			"job": "T-1/" + string(rune('0'+n)) + "/work"}}
	}
	refused := logRec{1, event.StepFailed, event.KindFact, "", event.FactData{Lane: "L-01", Merge: true, Step: event.StepWork,
		Reason: "work: refused"}}
	dropped := func(n int) logRec {
		return logRec{n, event.StepDropped, event.KindFact, "", event.FactData{Lane: "L-01", Merge: true, Reason: "why"}}
	}
	merged := func(n int) logRec {
		return logRec{n, event.StepMerged, event.KindFact, "", map[string]any{"outside": true}}
	}
	start := logRec{1, event.StepStartWork, event.KindIntent, event.ClassStart, map[string]any{"lane": "L-01",
		"agent": "pj-l-01", "pane": "w1:p1"}}
	for _, c := range []struct {
		name    string
		recs    []logRec
		attempt int    // the open intent's
		step    string // the open intent's
		want    string // its result after the replay
		settled int    // results the first replay writes
	}{
		{"dropped task's submit (G)", []logRec{submitted(1), submit, dropped(1)}, 1, event.StepSubmit,
			`"not run: T-1 dropped"`, 1},
		{"dropped task's typed work (P)", []logRec{work(1), submitted(1), dropped(1)}, 1, event.StepWork,
			`"not run: T-1 dropped"`, 1},
		{"dropped task's earlier agent.start (S)", []logRec{start, submitted(2), dropped(2)}, 1, event.StepStartWork,
			`"not run: T-1 dropped"`, 1},
		{"merged task's earlier refused work (P)", []logRec{work(1), refused, submitted(2), merged(2)}, 1,
			event.StepWork, `"not run: T-1 merged"`, 1},
		{"merged task's submit (G)", []logRec{submitted(1), submit, merged(1)}, 1, event.StepSubmit,
			`"not run: T-1 merged"`, 1},
		{"open task's submit (G): re-run", []logRec{submitted(1), submit}, 1, event.StepSubmit, `{"sha":`, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t)
			r.work("feature.txt", "feature\n")
			r.writeLog(c.recs)
			if r.resultOf(c.attempt, c.step) != "" || len(r.open()) != 1 {
				t.Fatalf("precondition: want one open intent, %s's: %q", c.step, r.open())
			}
			head := r.scratch.LaneHead("L-01")
			starts := func() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.launches) }
			r.start()
			r.waitFor("the replayed intent's result", func() bool { return r.resultOf(c.attempt, c.step) != "" })
			r.passes()
			if got := r.resultOf(c.attempt, c.step); !strings.HasPrefix(got, c.want) {
				t.Errorf("%s's result %s, want %s…", c.step, got, c.want)
			}
			if want := fmt.Sprintf("1 pending intents (%d settled)", c.settled); !strings.Contains(r.out.String(), want) {
				t.Errorf("the replay's line: want %s:\n%s", want, r.out)
			}
			ended := strings.HasPrefix(c.want, `"not run`)
			if ended {
				if got := r.scratch.LaneHead("L-01"); got != head || len(r.prompts()) != 0 ||
					starts() != 0 || r.has("1 intent gate") {
					t.Errorf("an effect ran: lane HEAD %s (was %s), prompts %d, starts %d: %q", got, head,
						len(r.prompts()), starts(), r.steps())
				}
				if out := r.scratch.Pins(); len(out) != 0 {
					t.Errorf("a pin was written: %s", out)
				}
			} else if c.step == event.StepSubmit && r.scratch.LaneHead("L-01") == head {
				t.Errorf("the open task's submit did not run again: lane HEAD unchanged: %q", r.steps())
			}
			if c.step == event.StepWork && len(r.prompts()) != 0 {
				t.Errorf("a work prompt was typed again: %d", len(r.prompts()))
			}
			r.stop()
			r.start()
			if want := "0 pending intents (0 settled)"; ended && !strings.Contains(r.out.String(), want) {
				t.Errorf("the second replay: want %s:\n%s", want, r.out)
			}
			if r.count("1 result "+c.step) != 1 {
				t.Errorf("%d results of %s, want 1: %q", r.count("1 result "+c.step), c.step, r.steps())
			}
		})
	}
}

// A task dropped live with a prompt refused before typing open: the drop settles it at once (no restart needed), and
// nothing is typed afterwards.
func TestDropSettlesOpenIntents(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.writeLog([]logRec{
		{1, event.StepSubmitted, event.KindFact, "", map[string]any{"lane": "L-01", "agent": "pj-l-01"}},
		{1, event.StepNeedsYou, event.KindFact, "", event.FactData{Lane: "L-01", Merge: true, Cause: "gate", Reason: "owner"}},
		{1, event.StepWork, event.KindIntent, event.ClassPrompt, map[string]any{"agent": "pj-l-01", "job": "T-1/1/work"}},
		{1, event.StepFailed, event.KindFact, "", event.FactData{Lane: "L-01", Merge: true, Step: event.StepWork,
			Reason: "work: refused"}},
	})
	r.start()
	r.passes()
	if r.resultOf(1, event.StepWork) != "" || len(r.open()) != 1 {
		t.Fatalf("precondition: the work intent open while the needs-you holds: %q", r.steps())
	}
	reply, err := wire.Verdict(context.Background(), r.sock, wire.MethodDrop, wire.Call{Root: r.p.Root, Agent: "ob-lead",
		Role: "lead", CallID: "d1", Task: "T-1", Note: "why"})
	if err != nil || reply != "DROPPED T-1" {
		t.Fatalf("drop: %q, %v", reply, err)
	}
	// The idle announce's line is the idle state's own intent (T-803): the drop's intents are what this test decides.
	var rest []string
	for _, o := range r.open() {
		if !strings.HasPrefix(o, event.MsgTask+" 0 deliver.") {
			rest = append(rest, o)
		}
	}
	if got := r.resultOf(1, event.StepWork); got != `"not run: T-1 dropped"` || len(rest) != 0 {
		t.Errorf("after the drop: work's result %s, open %q; want not run, none open", got, rest)
	}
	r.passes()
	if len(r.prompts()) != 0 {
		t.Errorf("a prompt was typed after the drop: %d", len(r.prompts()))
	}
}
