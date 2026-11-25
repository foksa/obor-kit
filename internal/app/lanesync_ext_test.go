package app_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/port"
)

// A red lane test sends T-1 back: serve merges the target into its lane (lane.sync, row 24a) before the fix job,
// the lane row's head is the merge commit, and the fix job says what serve merged.
func TestLoopSendbackSyncsTheLaneFirst(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t)
		r.work("red.txt", "red\n")
		r.scratch.Script("Test", "T-1", port.OutcomeRed) // every lane test of T-1
		r.scratch.Script("Sync", "T-1", port.SyncMerged)
		r.start()
		r.submit()
		job := r.waitJob("pj-l-01", "T-1/2/fix")
		syncs := r.scratch.Syncs()
		if len(syncs) != 1 || syncs[0].Attempt != 2 || syncs[0].Lane != "L-01" || syncs[0].Branch != "task/T-1" ||
			syncs[0].Target != r.scratch.TargetHead() || syncs[0].Dir == "" {
			t.Fatalf("Sync inputs %+v; want attempt 2's of L-01's task/T-1 onto the target %s", syncs, r.scratch.TargetHead())
		}
		if want := "serve merged main (" + syncs[0].Target[:7] + ") into your branch first."; !strings.Contains(job, want) {
			t.Errorf("the fix job has no %q:\n%s", want, job)
		}
		steps := r.steps()
		if s, f := slices.Index(steps, "2 result lane.sync"), slices.Index(steps, "2 intent fix"); s < 0 || f < s {
			t.Errorf("lane.sync's result at %d, the fix job's intent at %d; want the sync first: %q", s, f, steps)
		}
		var out struct{ SHA string }
		for _, rec := range r.records() {
			if rec.Step == event.StepSync && rec.Kind == event.KindResult {
				_ = json.Unmarshal(rec.Data, &out)
			}
		}
		row, ok, err := r.laneRow("L-01")
		if err != nil || !ok || out.SHA == "" || row.Head != out.SHA || row.Base != syncs[0].Target {
			t.Errorf("L-01's row %+v (%v, %v); want head the merge %q, base the target", row, ok, err, out.SHA)
		}
	})
}

// A task_done while lane.sync runs is refused busy: lane.sync is the step holding the attempt and it sends no job, so
// the worker's report is early. Nothing is recorded; once the sync settles, the fix job goes out.
func TestLoopTaskDoneDuringSyncRefused(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t)
		r.work("red.txt", "red\n")
		r.scratch.Script("Test", "T-1", port.OutcomeRed) // every lane test of T-1
		r.scratch.Script("Sync", "T-1", port.SyncMerged)
		release := make(chan struct{})
		r.scratch.Arm("Sync", func(ctx context.Context) error {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil
		})
		r.start()
		r.submit()
		r.waitFor("lane.sync to begin", func() bool { return r.scratch.Called("Sync") == 1 })
		n := len(r.records())
		_, err := r.taskDone(report)
		if err == nil || !strings.Contains(err.Error(), "task_done T-1: T-1 has lane.sync running; retry when it settles") {
			t.Fatalf("task_done during lane.sync: %v, want refused busy", err)
		}
		if got := len(r.records()); got != n || r.has("2 fact submitted") {
			t.Errorf("a refused task_done recorded %d records: %q", got-n, r.steps())
		}
		close(release)
		r.waitJob("pj-l-01", "T-1/2/fix")
		steps := r.steps()
		if s, f := slices.Index(steps, "2 result lane.sync"), slices.Index(steps, "2 intent fix"); s < 0 || f < s {
			t.Errorf("lane.sync's result at %d, the fix job's intent at %d; want the sync first: %q", s, f, steps)
		}
	})
}
