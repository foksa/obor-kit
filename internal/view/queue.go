package view

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/store"
)

// Queue is what waits (T-664, the owner: "not only what is going on but also what is queued"), as typed arrays the
// text renderers word: who holds serve's worktree (Scratch, serve's own word: T-669), the merge loop's tasks with their
// stage and what each waits for (never which one goes next, T-673), the dispatches waiting and on what, the review
// request, the lines and briefs not done, the pause and the next nightly run.
type Queue struct {
	Scratch *wire.ScratchStatus `json:"scratch"`
	Merge   []MergeItem         `json:"merge"`
	Waiting []WaitItem          `json:"waiting"`
	Review  []ReviewItem        `json:"review"`
	Outbox  []OutboxItem        `json:"outbox"`
	Pause   *Pause              `json:"pause"`
	Nightly *Nightly            `json:"nightly"`
}

// MergeItem is one task in the merge loop by id (Project.Tasks has its title, state, phase, lane and job, T-883), in
// the merge loop's order: its stage (wire.PhaseStage: the pipeline's column) and, when its next step needs the
// scratch, what it waits for and why (T-669: copied from serve's scheduler, which names no task as next, T-673).
type MergeItem struct {
	Task    string `json:"task"`
	Stage   string `json:"stage"`
	WaitsOn string `json:"waits_on,omitempty"`
	Why     string `json:"why,omitempty"`
}

// What a waiting dispatch waits on (WaitItem.WaitsOn).
const (
	WaitsAfter = "after"            // tasks to merge first (--after)
	WaitsLane  = "lane"             // a free lane of its level
	WaitsNever = "never dispatched" // a spec stored, never dispatched
)

// WaitItem is a dispatch that waits, of a task by id (Project.Tasks has its title and spec rev, T-883): for its
// --after tasks (After; Why: its last refused dispatch once they all merged), for a lane of its level (Level, Types,
// Why), or a stored spec never dispatched (Since: the spec's set time). A dispatch's carries serve's Difficulty and
// the Model it will run on (wire.TaskStatus', T-977; "": serve has none).
type WaitItem struct {
	Task       string   `json:"task"`
	WaitsOn    string   `json:"waits_on"`
	After      []string `json:"after,omitempty"`
	Why        string   `json:"why,omitempty"`
	Level      string   `json:"level,omitempty"`
	Types      []string `json:"types,omitempty"`
	Difficulty string   `json:"difficulty,omitempty"`
	Model      string   `json:"model,omitempty"`
	Since      int64    `json:"since,omitempty"`
}

// ReviewItem is the open review request as serve sends it (the tasks it waits to merge, those not merged yet, the task
// it waits to close, its note, who asked, since when) and its state: waiting (on Waiting or Held) or ready.
type ReviewItem struct {
	wire.ReviewRequestStatus
	State string `json:"state"`
}

// The outbox's kinds (OutboxItem.Kind).
const (
	OutLine  = "line"  // a line not delivered yet
	OutBrief = "brief" // a role agent's brief in progress
)

// OutboxItem is a line not delivered yet (To, From, Line, Since: its fact's time, Held: why serve holds it, T-746) or
// a brief in progress (To, Since, Delivered: its line was, Worked: the agent's pane worked on it since).
type OutboxItem struct {
	Kind      string `json:"kind"`
	To        string `json:"to"`
	From      string `json:"from,omitempty"`
	Line      string `json:"line,omitempty"`
	Since     int64  `json:"since"`
	Delivered bool   `json:"delivered,omitempty"`
	Worked    bool   `json:"worked,omitempty"`
	Held      string `json:"held,omitempty"`
}

// Nightly is the configured nightly check (merge.nightly_at): its local time and its next run (epoch seconds).
type Nightly struct {
	At   string `json:"at"`
	Next int64  `json:"next"`
}

// queue is v's queue at now: from serve's tasks (in its merge loop's order), its scheduler's state, review request,
// lines and briefs (ps), the specs stored (their set time) and the tasks the store saw dispatched (a spec of neither is
// never dispatched), the specs the lead dropped (dropped: their rev, T-1016) and merge.nightly_at.
func queue(v *Project, ps wire.ProjectStatus, nightlyAt string, specs map[string]store.SpecRow,
	dispatched map[string]bool, dropped map[string]int, now time.Time) Queue {
	q := Queue{Scratch: ps.Scratch, Merge: []MergeItem{}, Waiting: []WaitItem{}, Review: []ReviewItem{},
		Outbox: []OutboxItem{}, Pause: v.Paused, Nightly: nightly(nightlyAt, now)}
	waits := map[string]wire.ScratchWait{}
	for _, w := range ps.ScratchQueue {
		waits[w.Task] = w
	}
	known := map[string]bool{}
	for _, t := range ps.Tasks {
		known[t.Task] = true
		if t.Ended {
			continue // ended (rejected, dropped): the tasks log lists it, the queue has nothing to wait for
		}
		if t.State == "waiting" {
			w := WaitItem{Task: t.Task, WaitsOn: WaitsLane, Why: t.Why, Level: t.Level, Types: t.Types}
			if len(t.After) > 0 {
				w = WaitItem{Task: t.Task, WaitsOn: WaitsAfter, After: t.After, Why: t.Why}
			}
			w.Difficulty, w.Model = t.Difficulty, t.Model
			q.Waiting = append(q.Waiting, w)
			continue
		}
		if stage := t.Stage; stage != "" {
			w := waits[t.Task]
			q.Merge = append(q.Merge, MergeItem{Task: t.Task, Stage: stage, WaitsOn: w.WaitsOn, Why: w.Why})
		}
	}
	for task, s := range specs {
		if known[task] || dispatched[task] || s.Rev <= dropped[task] { // a spec-only drop: not queued until a new rev
			continue
		}
		w := WaitItem{Task: task, WaitsOn: WaitsNever}
		if t, err := time.Parse(time.RFC3339, s.SetAt); err == nil {
			w.Since = t.Unix()
		}
		q.Waiting = append(q.Waiting, w)
	}
	sortTasks(q.Waiting, func(w WaitItem) string { return w.Task })
	if r := ps.ReviewRequest; r != nil {
		state := "ready"
		if len(r.Waiting) > 0 || r.Held != "" {
			state = "waiting"
		}
		q.Review = append(q.Review, ReviewItem{ReviewRequestStatus: *r, State: state})
	}
	for _, m := range ps.Messages {
		q.Outbox = append(q.Outbox, OutboxItem{Kind: OutLine, To: m.To, From: m.From, Line: m.Line,
			Since: now.Unix() - m.Age, Held: m.Held})
	}
	for _, b := range ps.Briefs {
		q.Outbox = append(q.Outbox, OutboxItem{Kind: OutBrief, To: b.To, Since: b.Since, Delivered: b.Delivered,
			Worked: b.Worked})
	}
	return q
}

// sortTasks sorts items by their task id (idOf), numerically: T-9 before T-10.
func sortTasks[T any](items []T, idOf func(T) string) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := idOf(items[i]), idOf(items[j])
		na, ea := idNum(a)
		nb, eb := idNum(b)
		if ea == nil && eb == nil && na != nb {
			return na < nb
		}
		return a < b
	})
}

// idNum is a task id's number (T-664 → 664).
func idNum(id string) (int, error) {
	_, n, _ := strings.Cut(id, "-")
	return strconv.Atoi(n)
}

// nightly is the nightly check configured at at ("HH:MM", local) with its next run after now; nil when none is.
func nightly(at string, now time.Time) *Nightly {
	h, m, ok := config.ParseClock(at)
	if !ok {
		return nil
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return &Nightly{At: at, Next: next.Unix()}
}

// readSpecs is the store's specs by task (none: no store, or its specs table cannot be read).
func readSpecs(db *sql.DB) map[string]store.SpecRow {
	out := map[string]store.SpecRow{}
	if db == nil {
		return out
	}
	rows, _ := store.ReadSpecs(db)
	for _, r := range rows {
		out[r.Task] = r
	}
	return out
}

// dispatchedTasks is every task the store's log saw dispatched, waiting or merged (a spec of none of them was never
// dispatched).
func dispatchedTasks(db *sql.DB) map[string]bool {
	if db == nil {
		return map[string]bool{}
	}
	out, err := event.FactTasks(db, event.StepDispatched, event.StepWaiting, event.StepMerged)
	if err != nil {
		return map[string]bool{}
	}
	return out
}

// droppedSpecs is each task's spec-only drop (T-1016): the spec rev its dropped fact recorded, so a spec at that rev or
// an older one is never queued and a later obor spec set's rev revives it. A drop of a task the merge loop held
// records no rev: it is not here.
func droppedSpecs(db *sql.DB) map[string]int {
	out := map[string]int{}
	if db == nil {
		return out
	}
	rows, err := db.Query("SELECT task, data FROM events WHERE kind = ? AND step = ? ORDER BY seq", event.KindFact,
		event.StepDropped)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var task string
		var data []byte
		if err := rows.Scan(&task, &data); err != nil {
			return out
		}
		var f event.FactData
		if json.Unmarshal(data, &f) == nil && f.Rev > 0 {
			out[task] = f.Rev
		}
	}
	return out
}
