package view

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// The states of a task on the tasks page (TaskRow.State).
const (
	ListWaiting   = "waiting"
	ListRunning   = "running"
	ListNeedsYou  = "needs-you"
	ListMergeLoop = "in the merge loop"
	ListMerged    = "merged"
	ListDropped   = "dropped"
	ListNever     = "never dispatched"
)

// TaskRow is one task of the tasks page: its id, its spec's title, rev, difficulty and Model: refs (as written), the
// model ref its last dispatch chose, its state (List*), its lane (serve's or its branch's, else its last dispatch's),
// its attempts, its merge (sha and time; a merge outside obor serve has no sha), the review that covered it, the
// review its Fixes: line names, and when it was last active (its newest record, else its spec's set; epoch
// seconds).
type TaskRow struct {
	Task       string     `json:"task"`
	Title      string     `json:"title,omitempty"`
	Rev        int        `json:"rev,omitempty"`
	Difficulty string     `json:"difficulty,omitempty"`
	Model      string     `json:"model,omitempty"`
	Ran        string     `json:"ran,omitempty"`
	State      string     `json:"state"`
	Lane       string     `json:"lane,omitempty"`
	Attempts   int        `json:"attempts,omitempty"`
	MergeSHA   string     `json:"merge_sha,omitempty"`
	MergedAt   int64      `json:"merged_at,omitempty"`
	Review     string     `json:"review,omitempty"`
	Fixes      *TaskFixes `json:"fixes,omitempty"`
	Last       int64      `json:"last,omitempty"`
}

// TaskFixes is a fix task's Fixes: line: the review it fixes, the line's text after it, and the tasks that review
// started on (what it reviewed).
type TaskFixes struct {
	Review string   `json:"review"`
	Note   string   `json:"note,omitempty"`
	Tasks  []string `json:"tasks,omitempty"`
}

// taskFacts is what the store's facts say of one task: its last dispatch's lane and model ref, its newest merge, and
// the newest of its dispatch, waiting, merged and dropped facts (its place when serve does not say).
type taskFacts struct {
	lane, ran, sha string
	merged         bool
	mergedAt       int64
	last           string
}

// TaskList is every task the store knows, newest activity first: the view's tasks (specs, lane rows, serve's tasks,
// needs-you) and every task its log saw dispatched, waiting, merged or dropped. One read each of the specs, those
// facts with the reviews' and the log's activity per task, whatever the number of tasks.
func (d Detail) TaskList() []TaskRow {
	db, close := storeAt(d.Root)
	defer close()
	specs := readSpecs(db)
	facts := map[string]*taskFacts{}
	started := map[string][]string{} // a review's id → the tasks it started on
	covered := map[string]string{}   // a task → the newest review that covered it
	activity := map[string]event.Activity{}
	if db != nil {
		recs, _ := event.Facts(db, event.StepDispatched, event.StepWaiting, event.StepMerged, event.StepDropped,
			event.StepReviewStarted, event.StepReviewClosed)
		for _, r := range recs { // seq order: the newest wins
			switch r.Step {
			case event.StepReviewStarted, event.StepReviewClosed:
				var rd app.ReviewData
				if json.Unmarshal(r.Data, &rd) != nil || rd.ID == "" {
					continue
				}
				if r.Step == event.StepReviewStarted {
					started[rd.ID] = rd.Tasks
				}
				for _, t := range rd.Tasks {
					covered[t] = rd.ID
				}
				continue
			}
			f := facts[r.Task]
			if f == nil {
				f = &taskFacts{}
				facts[r.Task] = f
			}
			f.last = r.Step
			switch r.Step {
			case event.StepDispatched:
				desc := app.Describe(r)
				f.lane, f.ran = desc.Lane, desc.Type
			case event.StepMerged:
				var m event.MergedData
				_ = json.Unmarshal(r.Data, &m)
				f.merged, f.sha, f.mergedAt = true, m.Head, event.RecordTime(r).Unix()
			}
		}
		activity, _ = event.TaskActivity(db)
	}
	needs := map[string]bool{}
	for _, n := range d.Project.NeedsYou {
		needs[n.Task] = true
	}
	ids := map[string]bool{}
	for id := range d.Project.Tasks {
		ids[id] = true
	}
	for id := range facts {
		ids[id] = true
	}
	out := make([]TaskRow, 0, len(ids))
	for id := range ids {
		t := d.Project.Tasks[id]
		s := specs[id]
		f := facts[id]
		if f == nil {
			f = &taskFacts{}
		}
		row := TaskRow{Task: id, Title: s.Spec.Title, Rev: s.Rev, Difficulty: s.Spec.Level,
			Model: strings.Join(s.Spec.Models, ", "), Ran: f.ran, Lane: t.Lane, Attempts: t.Attempt,
			MergeSHA: f.sha, Review: covered[id]}
		if row.Lane == "" {
			row.Lane = f.lane
		}
		if f.merged {
			row.MergedAt = f.mergedAt
		}
		row.State = listState(t, f.last, needs[id])
		if s.Spec.FixesOK && s.Spec.FixesReview != "" {
			row.Fixes = &TaskFixes{Review: s.Spec.FixesReview, Note: s.Spec.FixesNote, Tasks: started[s.Spec.FixesReview]}
		}
		a := activity[id]
		row.Attempts = max(row.Attempts, a.Attempts)
		row.Last = unixOf(a.Last)
		if row.Last == 0 {
			row.Last = unixOf(s.SetAt)
		}
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b TaskRow) int {
		switch {
		case a.Last != b.Last:
			return int(b.Last - a.Last)
		case lessTask(a.Task, b.Task):
			return 1
		case lessTask(b.Task, a.Task):
			return -1
		}
		return 0
	})
	return out
}

// listState is a task's state on the tasks page: needs-you when one is in force; serve's phase while serve has it in
// its merge loop; its end when serve ended it (rejected is dropped: no merge); else what its newest dispatch,
// waiting, merged or dropped fact says (last, its step; a dispatch with no word from serve is running), and never
// dispatched when it has none.
func listState(t Task, last string, needsYou bool) string {
	switch t.Phase {
	case wire.PhaseNeedsYou, wire.PhaseHeld:
		return ListNeedsYou
	}
	if needsYou {
		return ListNeedsYou
	}
	if t.TaskState != "" {
		switch t.Phase {
		case wire.PhaseMerged:
			return ListMerged
		case wire.PhaseDropped, wire.PhaseRejected, wire.PhaseDropping:
			return ListDropped
		}
		if t.Open {
			switch t.Phase {
			case wire.PhaseWaiting:
				return ListWaiting
			case wire.PhaseDispatching, wire.PhaseWorking, wire.PhaseDriving:
				return ListRunning
			case "":
			default:
				return ListMergeLoop
			}
		}
	}
	switch last {
	case event.StepMerged:
		return ListMerged
	case event.StepDropped:
		return ListDropped
	case event.StepWaiting:
		return ListWaiting
	case event.StepDispatched:
		return ListRunning
	}
	return ListNever
}

// unixOf is an RFC 3339 time as epoch seconds (0: none or unreadable).
func unixOf(ts string) int64 {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return 0
	}
	return t.Unix()
}
