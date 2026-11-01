package view

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"obor/internal/app"
)

// Event is one record of the log as the view words it: its seq (a UI's cursor: "changed since seq X"; 0 for serve's
// own start, which is no record), its time, its task and its line.
type Event struct {
	Seq  int64     `json:"seq"`
	TS   string    `json:"ts"`
	Task string    `json:"task,omitempty"`
	Line string    `json:"line"`
	At   time.Time `json:"-"` // TS, parsed
}

// eventsRead is how many records are read per worded event kept: most records (intents, deliveries) are not worded.
const eventsRead = 4

// events is the newest n worded records of db's log (among its newest n*eventsRead), and serve's start while it is up
// (its restart is no record), newest first.
func events(db *sql.DB, n int, s Serve) []Event {
	out := []Event{}
	if n <= 0 {
		return out
	}
	if db != nil {
		recs, _ := app.RecentEvents(db, n*eventsRead)
		for _, r := range recs {
			if line := eventLine(r); line != "" {
				at, _ := time.Parse(time.RFC3339, r.TS)
				out = append(out, Event{Seq: r.Seq, TS: r.TS, Task: r.Task, Line: line, At: at})
			}
		}
	}
	if s.Up && !s.SinceTime.IsZero() {
		out = append(out, Event{TS: s.Since, Line: "serve restarted", At: s.SinceTime})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out[:min(n, len(out))]
}

// EventsPage is the API's event cursor (T-667): the worded records newer than the since it was asked for, oldest first,
// and Seq, the newest record read — the next since (unchanged when nothing is newer).
type EventsPage struct {
	Seq    int64   `json:"seq"`
	Events []Event `json:"events"`
}

// EventsSince is the log's records newer than since, at most limit, as EventsPage: the client's long poll, one page per
// read (a record the event list does not word still advances Seq). No store: the page's cursor, no events.
func EventsSince(db *sql.DB, since int64, limit int) (EventsPage, error) {
	page := EventsPage{Seq: since, Events: []Event{}}
	if db == nil || limit <= 0 {
		return page, nil
	}
	rows, err := db.Query(`SELECT seq, ts, task, kind, step, data FROM events WHERE seq > ? ORDER BY seq LIMIT ?`,
		since, limit)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var r app.Record
		var data []byte
		if err := rows.Scan(&r.Seq, &r.TS, &r.Task, &r.Kind, &r.Step, &data); err != nil {
			return page, err
		}
		if len(data) > 0 {
			r.Data = data
		}
		page.Seq = r.Seq
		if line := eventLine(r); line != "" {
			at, _ := time.Parse(time.RFC3339, r.TS)
			page.Events = append(page.Events, Event{Seq: r.Seq, TS: r.TS, Task: r.Task, Line: line, At: at})
		}
	}
	return page, rows.Err()
}

// eventData is what the view reads of a record's data: each kind of record fills the fields it has.
type eventData struct {
	Lane    string   `json:"lane"`
	Reason  string   `json:"reason"`
	Cause   string   `json:"cause"`
	Outcome string   `json:"outcome"`
	Line    string   `json:"line"`
	Note    string   `json:"note"`
	Head    string   `json:"head"`
	After   []string `json:"after"`
	ID      string   `json:"id"`
	To      string   `json:"to"`
}

// eventLine is r as the event list words it ("" for a record the view does not list): dispatched, submitted, a gate's
// pass or fail with its summary, accepted, a sendback, a hold, a merge with its sha, a review's request and start, or
// a line posted to an agent.
func eventLine(r app.Record) string {
	var d eventData
	if len(r.Data) > 0 {
		_ = json.Unmarshal(r.Data, &d)
	}
	if r.Kind == app.KindResult {
		if baseStep(r.Step) == app.StepGate {
			if d.Outcome == "green" {
				return "gate " + r.Task + " pass (" + d.Line + ")"
			}
			return "gate " + r.Task + " fail (" + d.Line + ")"
		}
		if method, _, _ := strings.Cut(r.Step, "."); method == app.MethodAccept {
			return "accepted " + r.Task
		} else if method == app.MethodSendback {
			return "sendback " + r.Task + noteIn(d.Note)
		}
		return ""
	}
	if r.Kind != app.KindFact {
		return ""
	}
	switch r.Step {
	case app.StepDispatched:
		return "dispatched " + r.Task + laneIn(d.Lane)
	case app.StepSubmitted:
		return "submitted " + r.Task
	case app.StepSendback:
		return "sendback " + r.Task + noteIn(d.Note)
	case app.StepNeedsYou:
		if d.Cause == app.CauseHeld {
			return "held " + r.Task + noteIn(strings.TrimPrefix(d.Reason, "held: "))
		}
	case app.StepMerged:
		if d.Head == "" {
			return "merged " + r.Task + " (outside obor serve)"
		}
		return "merged " + r.Task + " " + d.Head[:min(7, len(d.Head))]
	case app.StepReviewRequest:
		if len(d.After) > 0 {
			return "review requested (after " + strings.Join(d.After, ",") + ")"
		}
		return "review requested"
	case app.StepReviewStarted:
		return "review started " + d.ID
	case app.StepReviewRm:
		return "review request ended"
	case app.StepMessage:
		return "line to " + d.To + ": " + d.Line
	}
	return ""
}

// laneIn is " (O-01)" for a dispatch's lane, "" without one.
func laneIn(lane string) string {
	if lane == "" {
		return ""
	}
	return " (" + lane + ")"
}

// noteIn is ": note" for a sendback's or hold's why, "" without one.
func noteIn(note string) string {
	if note == "" {
		return ""
	}
	return ": " + note
}

// baseStep is a numbered step's step (gate.2 → gate; a verdict's accept.<call id> stays whole).
func baseStep(step string) string {
	if i := strings.LastIndexByte(step, '.'); i > 0 {
		if _, err := strconv.Atoi(step[i+1:]); err == nil {
			return step[:i]
		}
	}
	return step
}
