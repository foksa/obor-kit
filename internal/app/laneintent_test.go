package app

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/store"
)

// T-891: the lane intents embed the dispatch's lane fields (port.Dispatch). The dispatch fact keeps its stored bytes;
// the intents' change to the fact's keys, and an intent stored before decodes into the same input.
const (
	oldPrepare = `{"Task":"T-1","Lane":"L-01","Dir":"/wt/L-01","Branch":"task/T-1","Target":"b1","Agent":"pj-l-01","Redo":false,"At":"2026-11-04T21:34:06Z"}`
	newPrepare = `{"Task":"T-1","lane":"L-01","dir":"/wt/L-01","agent":"pj-l-01","branch":"task/T-1","target":"b1","At":"2026-11-04T21:34:06Z"}`
	oldFree    = `{"Lane":"L-01","Dir":"","Branch":"","Target":"","Discard":true}`
	newFree    = `{"lane":"L-01","dir":"","branch":"","target":"","Discard":true}`
)

var prepareIn = port.PrepareInput{Task: "T-1", Dispatch: port.Dispatch{Lane: "L-01", Dir: "/wt/L-01", Agent: "pj-l-01",
	Branch: "task/T-1", Target: "b1"}, At: time.Date(2026, 11, 4, 21, 34, 6, 0, time.UTC)}

var freeIn = port.FreeInput{Dispatch: port.Dispatch{Lane: "L-01"}, Discard: true}

func TestLaneRecordShapes(t *testing.T) {
	t.Parallel()
	// The dispatch fact as a log of 11-04 holds it (its keys, order and omitted empties).
	d := dispatchData{Dispatch: port.Dispatch{Lane: "L-03", Dir: "/wt/L-03", Agent: "ob-l-03", Branch: "task/T-849",
		Target: "4cac"}, Level: "simple", Types: []string{"deepseek", "opus:medium"}, Type: "deepseek", Caller: "ob-lead",
		Call: "c00d", Reply: "DISPATCHED"}
	const fact = `{"lane":"L-03","dir":"/wt/L-03","agent":"ob-l-03","branch":"task/T-849","target":"4cac","level":"simple",` +
		`"types":["deepseek","opus:medium"],"type":"deepseek","caller":"ob-lead","call":"c00d","reply":"DISPATCHED"}`
	for _, c := range []struct {
		what string
		v    any
		want string
	}{{"dispatch fact", d, fact}, {"prepare intent", prepareIn, newPrepare}, {"free intent", freeIn, newFree}} {
		if b, _ := json.Marshal(c.v); string(b) != c.want {
			t.Errorf("%s stored as %s, want %s", c.what, b, c.want)
		}
	}
	var p port.PrepareInput
	if err := json.Unmarshal([]byte(oldPrepare), &p); err != nil || p != prepareIn {
		t.Errorf("an old prepare intent decodes as %+v, %v; want %+v", p, err, prepareIn)
	}
	var f port.FreeInput
	if err := json.Unmarshal([]byte(oldFree), &f); err != nil || f != freeIn {
		t.Errorf("an old free intent decodes as %+v, %v; want %+v", f, err, freeIn)
	}
}

// A log whose lane.prepare and lane.free intents are stored in the old shape folds, and replays into a new app, the
// same as one in the new shape: the task's fold and the lanes table.
func TestOldLaneIntentsFoldTheSame(t *testing.T) {
	t.Parallel()
	fold := func(prepare, free string) (mtask, map[string]store.LaneRow) {
		h := newHarness(t)
		h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","lanes":1,"roles":{"integrator":""}}`)
		h.connect()
		p := h.proj()
		add := func(step, kind string, raw string) {
			t.Helper()
			h.now = h.now.Add(time.Minute)
			r, err := p.log.Append(event.Record{Task: "T-1", Attempt: 1, Step: step, Kind: kind, Data: json.RawMessage(raw)}, h.now)
			if err != nil {
				t.Fatal(err)
			}
			h.a.mu.Lock()
			p.foldRecord(r)
			h.a.mu.Unlock()
		}
		disp, _ := json.Marshal(dispatchData{Dispatch: prepareIn.Dispatch, Type: "opus"})
		add(event.StepDispatched, event.KindFact, string(disp))
		add(event.StepPrepare, event.KindIntent, prepare)
		add(event.StepPrepare, event.KindResult, `{"base":"b1","head":"h1"}`)
		drop, _ := json.Marshal(dropData{Lane: "L-01", Reason: "abandoned", Discard: true})
		add(event.StepDropping, event.KindFact, string(drop))
		add(event.StepFree, event.KindIntent, free)
		add(event.StepFree, event.KindResult, `{"line":"freed"}`)
		h.a.closeLog()
		a := New(Options{Root: h.root, Now: func() time.Time { return h.now }}, h.panes, h.lanes)
		a.loadProject()
		defer a.closeLog()
		rows, err := store.ReadLanesAt(h.root)
		if err != nil {
			t.Fatal(err)
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		mt := a.p.folded.loop["T-1"]
		if mt == nil {
			t.Fatal("T-1 not folded")
		}
		return *mt, rows
	}
	oldMt, oldRows := fold(oldPrepare, oldFree)
	newMt, newRows := fold(newPrepare, newFree)
	if !oldMt.is(stDropping) || oldMt.prepares != 1 || oldMt.frees != 1 || oldRows["L-01"].Task != "T-1" {
		t.Fatalf("precondition: T-1 dropping after one prepare and one free, its row kept: %+v, rows %+v", oldMt, oldRows)
	}
	if !reflect.DeepEqual(oldMt, newMt) || !reflect.DeepEqual(oldRows, newRows) {
		t.Errorf("the old intents fold to %+v, rows %+v; the new ones to %+v, rows %+v", oldMt, oldRows, newMt, newRows)
	}
}
