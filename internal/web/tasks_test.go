package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/testkit"
	"obor/internal/view"
)

// TestTasksAPI: /api/tasks lists every task the store knows from the task page's narrow read, newest activity first,
// each row with its spec's title, rev, difficulty and Model: line, the model ref of its last dispatch, its state
// (waiting, running, in the merge loop and needs-you as serve says; merged and dropped as the log says; never
// dispatched for a spec alone), lane, attempts, merge sha and time, the review that covered it and its Fixes: link.
func TestTasksAPI(t *testing.T) {
	p := testkit.New(t)
	p.Config()
	spec := func(id, head string) { p.Spec(id, "# "+id+" — title of "+id+"\n\nDifficulty: medium\n"+head) }
	spec("T-11", "")
	spec("T-12", "Model: opus\n")
	spec("T-12", "Model: opus\n\nrev 2\n")
	spec("T-13", "")
	spec("T-14", "")
	spec("T-15", "Fixes: RV-7 #1 — the drop line\n")
	spec("T-16", "")
	spec("T-17", "")
	spec("T-18", "")
	merged := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	rec := func(task string, attempt int, step string, data any, at time.Time) {
		t.Helper()
		db, err := store.OpenRW(store.File(p.Root))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		b, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO events (seq, ts, task, attempt, step, kind, class, data)
			VALUES ((SELECT COALESCE(MAX(seq), 0) + 1 FROM events), ?, ?, ?, ?, 'fact', 'F', ?)`,
			at.UTC().Format(time.RFC3339), task, attempt, step, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	rec("T-13", 1, event.StepDispatched, map[string]any{"lane": "L-02", "type": "sonnet"}, merged.Add(-2*time.Hour))
	rec("T-13", 2, event.StepDispatched, map[string]any{"lane": "L-03", "type": "opus:high"}, merged.Add(-time.Hour))
	rec("T-13", 2, event.StepMerged, map[string]any{"head": "abc1234def"}, merged)
	rec(event.ReviewTask, 0, event.StepReviewStarted, map[string]any{"id": "RV-7", "tasks": []string{"T-13"}},
		merged.Add(time.Hour))
	rec("T-15", 1, event.StepDispatched, map[string]any{"lane": "L-01", "type": "sonnet"}, merged.Add(2*time.Hour))
	rec("T-15", 1, event.StepMerged, map[string]any{"head": "fee1234"}, merged.Add(3*time.Hour))
	rec(event.ReviewTask, 0, event.StepReviewClosed, map[string]any{"id": "RV-7", "tasks": []string{"T-13", "T-15"}},
		merged.Add(4*time.Hour))
	rec("T-14", 1, event.StepDispatched, map[string]any{"lane": "L-02", "type": "haiku"}, now.Add(-time.Hour))
	rec("T-14", 1, event.StepDropped, map[string]any{"reason": "not needed"}, now.Add(-30*time.Minute))
	rec("T-12", 2, event.StepDispatched, map[string]any{"lane": "L-01", "type": "opus:high"}, now.Add(-10*time.Minute))
	rec("T-17", 1, event.StepDispatched, map[string]any{"lane": "L-02", "type": "sonnet"}, now.Add(-5*time.Minute))
	rec("T-18", 1, event.StepDispatched, map[string]any{"lane": "L-03", "type": "sonnet"}, now.Add(-time.Minute))
	ss := wire.Status{Since: now.Add(-time.Hour).UTC().Format(time.RFC3339), Connected: true,
		Project: &wire.ProjectStatus{Name: "pj", Root: p.Root,
			Tasks: []wire.TaskStatus{
				{Task: "T-11", Attempt: 1, State: "waiting", Phase: wire.PhaseWaiting, Waiting: true, Rank: 1},
				{Task: "T-12", Lane: "L-01", Attempt: 2, State: "working", Phase: wire.PhaseWorking},
				{Task: "T-17", Lane: "L-02", Attempt: 1, State: "gating", Phase: wire.PhaseGating},
				{Task: "T-18", Lane: "L-03", Attempt: 1, State: "working", Phase: wire.PhaseWorking}},
			Facts: []event.Record{fact(t, 1, "T-18", event.FactData{Lane: "L-03", Reason: "asks: which file?"})}}}
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenRO(store.File(p.Root))
	if err != nil {
		t.Fatal(err)
	}
	v := view.Build(view.Input{Status: ss, Lanes: checks.NewLaneTable(context.Background(), cfg), DB: db, Now: now})
	db.Close()
	if !v.Serve.Up || len(v.NeedsYou) != 1 {
		t.Fatalf("precondition: serve up with one needs-you: %+v", v.Serve)
	}
	built := 0
	s := &Server{
		Projects: func() []project.Project { return []project.Project{{Name: "pj", Root: p.Root}} },
		Build: func(context.Context, string) (view.Project, error) {
			built++
			return view.Project{}, nil
		},
		Task: func(context.Context, string) (view.Detail, error) { return view.Detail{Project: v}, nil },
	}
	r := get(t, s.Handler(), "/api/tasks?project=pj")
	if r.Code != http.StatusOK {
		t.Fatalf("/api/tasks: %d %s", r.Code, r.Body.String())
	}
	var rows []view.TaskRow
	decode(t, r, &rows)
	if built != 0 {
		t.Errorf("/api/tasks built the overview %d times; want the task page's narrow read alone", built)
	}
	by := map[string]view.TaskRow{}
	for _, row := range rows {
		by[row.Task] = row
	}
	if len(rows) != 8 || len(by) != 8 {
		t.Fatalf("rows %+v, want T-11 … T-18 once each (no review pseudo-task)", rows)
	}
	want := map[string]view.TaskRow{
		"T-11": {Task: "T-11", Title: "title of T-11", Rev: 1, Difficulty: "medium", State: view.ListWaiting,
			Attempts: 1},
		"T-12": {Task: "T-12", Title: "title of T-12", Rev: 2, Difficulty: "medium", Model: "opus", Ran: "opus:high",
			State: view.ListRunning, Lane: "L-01", Attempts: 2},
		"T-13": {Task: "T-13", Title: "title of T-13", Rev: 1, Difficulty: "medium", Ran: "opus:high",
			State: view.ListMerged, Lane: "L-03", Attempts: 2, MergeSHA: "abc1234def", MergedAt: merged.Unix(),
			Review: "RV-7"},
		"T-14": {Task: "T-14", Title: "title of T-14", Rev: 1, Difficulty: "medium", Ran: "haiku",
			State: view.ListDropped, Lane: "L-02", Attempts: 1},
		"T-15": {Task: "T-15", Title: "title of T-15", Rev: 1, Difficulty: "medium", Ran: "sonnet",
			State: view.ListMerged, Lane: "L-01", Attempts: 1, MergeSHA: "fee1234",
			MergedAt: merged.Add(3 * time.Hour).Unix(), Review: "RV-7",
			Fixes: &view.TaskFixes{Review: "RV-7", Note: "#1 — the drop line", Tasks: []string{"T-13"}}},
		"T-16": {Task: "T-16", Title: "title of T-16", Rev: 1, Difficulty: "medium", State: view.ListNever},
		"T-17": {Task: "T-17", Title: "title of T-17", Rev: 1, Difficulty: "medium", Ran: "sonnet",
			State: view.ListMergeLoop, Lane: "L-02", Attempts: 1},
		"T-18": {Task: "T-18", Title: "title of T-18", Rev: 1, Difficulty: "medium", Ran: "sonnet",
			State: view.ListNeedsYou, Lane: "L-03", Attempts: 1},
	}
	for id, w := range want {
		got := by[id]
		if got.Last == 0 {
			t.Errorf("%s: no last activity", id)
		}
		got.Last = 0
		if g, _ := json.Marshal(got); string(g) != string(must(t, w)) {
			t.Errorf("%s:\n got %s\nwant %s", id, g, must(t, w))
		}
	}
	// newest activity first: T-13's newest record (its merge, Nov 1) is the oldest activity; a task with no record
	// is as new as its spec's set
	if rows[len(rows)-1].Task != "T-13" {
		t.Errorf("last row %s, want T-13 (its newest record is the oldest activity)", rows[len(rows)-1].Task)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].Last > rows[i-1].Last {
			t.Errorf("rows %s then %s: not newest first", rows[i-1].Task, rows[i].Task)
		}
	}
	if r := get(t, s.Handler(), "/api/tasks?project=nope"); r.Code != http.StatusNotFound {
		t.Errorf("/api/tasks of an unknown project: %d, want 404", r.Code)
	}
}

// must is v as JSON.
func must(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
