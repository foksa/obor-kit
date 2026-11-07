package view

// T-893: the event list, the task page and an agent's lines word one record of each worded kind; the golden is the
// text the view wrote before app.Describe took over (identical after).

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/store"
)

var update = flag.Bool("update", false, "rewrite the view's golden files")

// wordedLog is one record of each kind the view words (and a few it does not), as serve writes them: T-1's dispatch,
// delivery, submission, gate, review job, verdicts, hold, merge, and the project's review and line records.
func wordedLog() []event.Record {
	rec := func(task string, attempt int, step, kind, data string) event.Record {
		r := event.Record{Task: task, Attempt: attempt, Step: step, Kind: kind}
		if data != "" {
			r.Data = json.RawMessage(data)
		}
		return r
	}
	return []event.Record{
		rec("T-1", 1, event.StepDispatched, event.KindFact, `{"lane":"L-01","dir":"/w/L-01","agent":"pj-l-01","branch":"task/T-1","target":"main","type":"opus","reply":"ok"}`),
		rec("T-1", 1, event.StepWork, event.KindIntent, `{"text":"do it"}`),
		rec("T-1", 1, event.StepDelivered, event.KindFact, `{"lane":"L-01"}`),
		rec("T-1", 1, event.StepSubmitted, event.KindFact, `{"lane":"L-01","agent":"pj-l-01","call":"c1","report":"Summary: one\nSelf-check: two"}`),
		rec("T-1", 1, event.StepGate, event.KindIntent, `{"kind":"lane","lane":"L-01"}`),
		rec("T-1", 1, event.StepGate, event.KindResult, `{"outcome":"red","line":"RED T-1: e2e failed","candidate":{},"spec_rev":2}`),
		rec("T-1", 1, event.StepSendback, event.KindFact, `{"note":"fix the e2e"}`),
		rec("T-1", 2, event.StepSubmitted, event.KindFact, `{"lane":"L-01","agent":"pj-l-01","call":"c2","report":"Summary: fixed"}`),
		rec("T-1", 2, event.StepGate, event.KindResult, `{"outcome":"green","line":"check 25 pass, 0 fail","candidate":{},"spec_rev":3}`),
		rec("T-1", 2, "review", event.KindIntent, `{}`),
		rec("T-1", 2, "sendback.c3", event.KindResult, `{"note":"a nit"}`),
		rec("T-1", 3, event.StepGate+".2", event.KindResult, `{"outcome":"green","line":"check 26 pass","candidate":{},"spec_rev":3}`),
		rec("T-1", 3, event.StepNeedsYou, event.KindFact, `{"lane":"L-01","reason":"held: decide the API","merge":true,"cause":"held"}`),
		rec("T-1", 3, "accept.c4", event.KindResult, `{"reply":"ok"}`),
		rec("T-1", 3, event.StepMerged, event.KindFact, `{"head":"abcdef0123456789"}`),
		rec("T-2", 1, event.StepDispatched, event.KindFact, `{"lane":"L-02","agent":"pj-l-02","drive":true}`),
		rec("T-3", 1, event.StepDrive, event.KindFact, `{"lane":"L-03","drive":true}`),
		rec("T-3", 1, event.StepNeedsYou, event.KindFact, `{"lane":"L-03","reason":"asks: may I?"}`),
		rec("T-4", 1, event.StepMerged, event.KindFact, `{"outside":true}`),
		rec(event.ReviewTask, 0, event.StepReviewRequest, event.KindFact, `{"after":["T-1","T-4"],"by":"lead"}`),
		rec(event.ReviewTask, 0, event.StepReviewRequest, event.KindFact, `{"by":"lead"}`),
		rec(event.ReviewTask, 0, event.StepReviewStarted, event.KindFact, `{"id":"RV-9","head":"abc"}`),
		rec(event.ReviewTask, 0, event.StepReviewRm, event.KindFact, `{"by":"lead"}`),
		rec(event.MsgTask, 0, event.StepMessage, event.KindFact, `{"to":"pj-l-01","line":"look at T-1","from":"pj-lead"}`),
		rec(event.MsgTask, 0, event.StepMessage, event.KindFact, `{"to":"pj-int","line":"hello"}`),
	}
}

// writeLog appends recs to a store under a new root, one second apart from at.
func writeLog(t *testing.T, recs []event.Record, at time.Time) string {
	t.Helper()
	root := t.TempDir()
	db, err := store.OpenRW(store.File(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log, _, err := event.OpenLog(db)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range recs {
		if _, err := log.Append(r, at.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestWordedRecordsGolden: the event list, the event cursor, T-1's task page and pj-l-01's lines, as text.
func TestWordedRecordsGolden(t *testing.T) {
	recs := wordedLog()
	root := writeLog(t, recs, time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC))
	db, closeDB := storeAt(root)
	defer closeDB()
	if db == nil {
		t.Fatal("precondition: the store was written")
	}
	var b strings.Builder
	b.WriteString("# events\n")
	evs := events(db, len(recs), Serve{})
	if len(evs) == 0 {
		t.Fatal("precondition: the event list words some records")
	}
	for _, e := range evs {
		fmt.Fprintf(&b, "%d %s bad=%v %q\n", e.Seq, e.Task, e.Bad, e.Line)
	}
	b.WriteString("# since 3\n")
	page, err := EventsSince(db, 3, len(recs))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&b, "seq %d\n", page.Seq)
	for _, e := range page.Events {
		fmt.Fprintf(&b, "%d %s bad=%v %q\n", e.Seq, e.Task, e.Bad, e.Line)
	}
	b.WriteString("# task T-1\n")
	td, ok := Detail{Root: root}.Task("T-1")
	if !ok {
		t.Fatal("T-1 has no page")
	}
	fmt.Fprintf(&b, "lane %q agent %q report %q gate %q rev %d\n", td.Lane, td.Agent, td.Report, td.GateLine, td.GatedRev)
	for _, r := range td.Records {
		fmt.Fprintf(&b, "%d %s %s %q\n", r.Seq, r.Kind, r.Step, r.Line)
	}
	b.WriteString("# agent pj-l-01\n")
	ad, ok := Detail{Root: root}.Agent("pj-l-01")
	if !ok {
		t.Fatal("pj-l-01 has no page")
	}
	for _, l := range ad.Lines {
		fmt.Fprintf(&b, "%d from %q %q\n", l.Seq, l.From, l.Line)
	}
	golden(t, "worded.golden", b.String())
}

// golden compares got with testdata/name (-update rewrites it).
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n--- got\n%s--- want\n%s", name, got, want)
	}
}

// T-893: a task page whose task's phase places it on no step (needs-you, held, ended, serve down) is placed by its
// records, the furthest step they reached: a gate by its outcome, never its line's wording; a phase's step wins.
func TestTaskPageStepFromRecords(t *testing.T) {
	recs := []event.Record{
		{Task: "T-1", Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact, Data: json.RawMessage(`{"lane":"L-01"}`)},
		{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact, Data: json.RawMessage(`{"lane":"L-01"}`)},
		{Task: "T-1", Attempt: 1, Step: event.StepGate, Kind: event.KindResult,
			Data: json.RawMessage(`{"outcome":"red","line":"RED: 3 pass 1 fail"}`)},
	}
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	step := func(root string, d Detail) *int {
		t.Helper()
		d.Root = root
		td, ok := d.Task("T-1")
		if !ok {
			t.Fatal("T-1 has no page")
		}
		return td.Step
	}
	red := writeLog(t, recs, at)
	if got := step(red, Detail{}); got == nil || *got != wire.TaskStepGate {
		t.Errorf("a red gate whose line counts passes: step %v, want the gate (%d)", got, wire.TaskStepGate)
	}
	greenGate := append(recs[:2:2], event.Record{Task: "T-1", Attempt: 1, Step: event.StepGate, Kind: event.KindResult,
		Data: json.RawMessage(`{"outcome":"green","line":"GREEN"}`)})
	if got := step(writeLog(t, greenGate, at), Detail{}); got == nil || *got != wire.TaskStepGate {
		t.Errorf("a green gate (T-961: its lane test next): step %v, want the gate (%d)", got, wire.TaskStepGate)
	}
	redTest := append(greenGate[:3:3], event.Record{Task: "T-1", Attempt: 1, Step: event.StepTest, Kind: event.KindResult,
		Data: json.RawMessage(`{"outcome":"red","line":"RED check (0 pass, 1 fail)"}`)})
	if got := step(writeLog(t, redTest, at), Detail{}); got == nil || *got != wire.TaskStepGate {
		t.Errorf("a red lane test: step %v, want the gate (%d)", got, wire.TaskStepGate)
	}
	greenRecs := append(greenGate[:3:3], event.Record{Task: "T-1", Attempt: 1, Step: event.StepTest, Kind: event.KindResult,
		Data: json.RawMessage(`{"outcome":"green","line":"GREEN T-1 (check 1 pass)"}`)})
	green := writeLog(t, greenRecs, at)
	if got := step(green, Detail{}); got == nil || *got != wire.TaskStepReview {
		t.Errorf("a green lane test: step %v, want the review (%d)", got, wire.TaskStepReview)
	}
	working := wire.TaskStepWorking
	placed := Detail{Project: Project{Tasks: map[string]Task{"T-1": {Task: "T-1", Phase: wire.PhaseWorking, Step: &working}}}}
	if got := step(green, placed); got == nil || *got != wire.TaskStepWorking {
		t.Errorf("a task its phase places: step %v, want the phase's (%d)", got, wire.TaskStepWorking)
	}
	if got := step(writeLog(t, recs[:0], at), Detail{Project: Project{Tasks: map[string]Task{"T-1": {Task: "T-1"}}}}); got != nil {
		t.Errorf("no records, no phase: step %v, want none", *got)
	}
}
