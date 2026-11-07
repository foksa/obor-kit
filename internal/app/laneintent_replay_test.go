package app_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/store"
)

// T-891: a lane.prepare intent stored before T-891 (PrepareInput untagged: Task, Lane, Dir, …) whose effect a crash
// cut off replays as one stored now: its Prepare runs once more with the input the intent recorded, and the dispatch
// ends in the same steps.
func TestOldPrepareIntentReplays(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.disp.set(func(d *dispatchFakes) { d.cut["prepare"] = "before" })
	r.start()
	r.mustDispatch("d1")
	r.waitFor("prepare cut before", func() bool {
		r.disp.mu.Lock()
		defer r.disp.mu.Unlock()
		return r.disp.hit["prepare"]
	})
	r.stop()

	// The pending intent, rewritten in the shape it had before T-891.
	db, err := store.OpenRW(store.File(r.p.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var stored string
	if err := db.QueryRow(`SELECT data FROM events WHERE step = 'lane.prepare' AND kind = 'intent'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var old struct {
		Task, Lane, Dir, Branch, Target, Agent string
		Redo                                   bool
		At                                     time.Time
	}
	if err := json.Unmarshal([]byte(stored), &old); err != nil || !strings.Contains(stored, `"lane":"L-01"`) || old.Lane != "L-01" {
		t.Fatalf("precondition: the intent stored now names L-01 by its fact's key: %s, %v", stored, err)
	}
	oldRaw, _ := json.Marshal(old)
	if _, err := db.Exec(`UPDATE events SET data = ? WHERE step = 'lane.prepare' AND kind = 'intent'`, string(oldRaw)); err != nil {
		t.Fatal(err)
	}
	var recorded port.PrepareInput
	if err := json.Unmarshal(oldRaw, &recorded); err != nil || !strings.Contains(string(oldRaw), `"Lane":"L-01"`) {
		t.Fatalf("precondition: the old shape %s, %v", oldRaw, err)
	}

	r.disp.set(func(d *dispatchFakes) { delete(d.cut, "prepare") })
	r.start()
	r.waitStep("1 result work")
	r.passes()
	eq(t, r.steps(), dispatchSteps, "records")
	r.disp.mu.Lock()
	inputs := append([]port.PrepareInput(nil), r.disp.inputs...)
	r.disp.mu.Unlock()
	if len(inputs) != 1 || inputs[0] != recorded {
		t.Errorf("the replay's prepares %+v; want one, with the input the old intent recorded %+v", inputs, recorded)
	}
}
