package app

import (
	"slices"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/store"
)

// waitSpec is task's waiting spec at level, with its head lines added.
func waitSpec(task, level string, lines ...string) string {
	text := "# " + task + " — Waiting\n\nDifficulty: " + level + "\n"
	for _, l := range lines {
		text += l + "\n"
	}
	return text
}

// T-1030: the pass order's waiting part is the lane's pick order (a freed lane goes to the first waiting task there):
// a high task before every ordinary one — its spec's `Priority: high`, or a Fixes: task of the review the promotion
// waits for (p.prom.waitRV) — then the waiting fact's order (dispatch order). Its first is the task that starts on the
// next free lane. T-2 waits before T-3, and status marks exactly the high ones.
func TestPassOrderPutsAHighWaitingTaskFirst(t *testing.T) {
	t.Parallel()
	plain2, plain3 := waitSpec("T-2", "medium"), waitSpec("T-3", "medium")
	high2, high3 := waitSpec("T-2", "medium", "Priority: high"), waitSpec("T-3", "medium", "Priority: high")
	fix3 := waitSpec("T-3", "medium", "Fixes: RV-1")
	for _, c := range []struct {
		name   string
		specs  map[string]string // task → its spec
		open   string            // a review the fold has open ("" none)
		waitRV string            // the review the promotion waits for ("" none: promotion off, or nothing promotes)
		want   []string
		high   []string // the tasks obor status marks high
	}{
		{name: "ordinary: the dispatch order", specs: map[string]string{"T-2": plain2, "T-3": plain3},
			want: []string{"T-2", "T-3"}},
		{name: "high first though dispatched second", specs: map[string]string{"T-2": plain2, "T-3": high3},
			want: []string{"T-3", "T-2"}, high: []string{"T-3"}},
		{name: "both high: the dispatch order", specs: map[string]string{"T-2": high2, "T-3": high3},
			want: []string{"T-2", "T-3"}, high: []string{"T-2", "T-3"}},
		{name: "a fix of the review the promotion waits for is high",
			specs: map[string]string{"T-2": plain2, "T-3": fix3}, open: "RV-1", waitRV: "RV-1",
			want: []string{"T-3", "T-2"}, high: []string{"T-3"}},
		{name: "a fix with an open review the promotion does not wait for is ordinary",
			specs: map[string]string{"T-2": plain2, "T-3": fix3}, open: "RV-1", want: []string{"T-2", "T-3"}},
		{name: "a fix of another review is ordinary",
			specs: map[string]string{"T-2": plain2, "T-3": fix3}, open: "RV-2", waitRV: "RV-2",
			want: []string{"T-2", "T-3"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
			a, p := lineApp(t, lineRoot(t), &now)
			for task, text := range c.specs {
				if _, _, err := store.SetSpec(p.log.DB(), task, text, "", "lead", now); err != nil {
					t.Fatal(err)
				}
			}
			if c.open != "" {
				startReview(t, a, p, c.open)
			}
			if c.waitRV != "" {
				a.mu.Lock()
				p.prom.waitRV = c.waitRV // promoteHeads' record, T-977: a GREEN run waits for its close
				a.mu.Unlock()
			}
			// The waiting facts in dispatch order: T-2's first.
			for _, task := range []string{"T-2", "T-3"} {
				if !a.record(p, event.Record{Task: task, Attempt: 1, Step: event.StepWaiting, Kind: event.KindFact},
					dispatchData{Level: "medium", Types: []string{"sonnet"}}) {
					t.Fatalf("the log refused %s's waiting fact", task)
				}
			}
			if got := waitingOrder(a, p); !slices.Equal(got, c.want) {
				t.Errorf("passOrder's waiting tasks %v, want %v: the order the next free lane is offered in", got, c.want)
			}
			if got := statusHigh(a); !slices.Equal(got, c.high) {
				t.Errorf("status marks %v high, want %v: it names the tasks the pass picks first", got, c.high)
			}
		})
	}
}

// startReview records a review's start for id: an open review in the fold.
func startReview(t *testing.T, a *App, p *proj, id string) {
	t.Helper()
	if !a.record(p, event.Record{Task: event.ReviewTask, Step: event.StepReviewStarted, Kind: event.KindFact},
		ReviewData{ID: id, Head: "h1"}) {
		t.Fatalf("the log refused the review start %s", id)
	}
}

// waitingOrder is a's pass order with its non-waiting tasks dropped: its waiting part.
func waitingOrder(a *App, p *proj) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, task := range p.passOrder() {
		if mt := p.folded.loop[task]; mt != nil && mt.state == stWaiting {
			out = append(out, task)
		}
	}
	return out
}

// statusHigh is the waiting tasks a's status marks high, by task id.
func statusHigh(a *App) []string {
	var out []string
	for _, ts := range a.Status().Project.Tasks {
		if ts.State == "waiting" && ts.High {
			out = append(out, ts.Task)
		}
	}
	slices.Sort(out)
	return out
}
