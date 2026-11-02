package view

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// Event is one record of the log as the view words it: its seq (a UI's cursor: "changed since seq X"; 0 for serve's
// own start, which is no record), its time, its task, its line, and whether that line is bad news (T-688).
type Event struct {
	Seq  int64     `json:"seq"`
	TS   string    `json:"ts"`
	Task string    `json:"task,omitempty"`
	Line string    `json:"line"`
	Bad  bool      `json:"bad,omitempty"`
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
		recs, _ := event.RecentEvents(db, n*eventsRead)
		for _, r := range recs {
			if line, bad := eventLine(r); line != "" {
				at, _ := time.Parse(time.RFC3339, r.TS)
				out = append(out, Event{Seq: r.Seq, TS: r.TS, Task: r.Task, Line: line, Bad: bad, At: at})
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
		var r event.Record
		var data []byte
		if err := rows.Scan(&r.Seq, &r.TS, &r.Task, &r.Kind, &r.Step, &data); err != nil {
			return page, err
		}
		if len(data) > 0 {
			r.Data = data
		}
		page.Seq = r.Seq
		if line, bad := eventLine(r); line != "" {
			at, _ := time.Parse(time.RFC3339, r.TS)
			page.Events = append(page.Events, Event{Seq: r.Seq, TS: r.TS, Task: r.Task, Line: line, Bad: bad, At: at})
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

// eventLine is r as the event list words it and whether that line is bad news ("" for a record the view does not list):
// dispatched, submitted, a gate's pass or fail with its summary, accepted, a sendback, a hold, a merge with its sha, a
// review's request and start, or a line posted to an agent. A gate that is not green and a hold are the bad ones; a
// green gate is not, however its summary counts its checks (T-688).
func eventLine(r event.Record) (string, bool) {
	var d eventData
	if len(r.Data) > 0 {
		_ = json.Unmarshal(r.Data, &d)
	}
	if r.Kind == event.KindResult {
		if base, _ := event.StepN(r.Step); base == event.StepGate {
			if d.Outcome == "green" {
				return "gate " + r.Task + " pass (" + d.Line + ")", false
			}
			return "gate " + r.Task + " fail (" + d.Line + ")", true
		}
		if method, _, _ := strings.Cut(r.Step, "."); method == wire.MethodAccept {
			return "accepted " + r.Task, false
		} else if method == wire.MethodSendback {
			return "sendback " + r.Task + noteIn(d.Note), false
		}
		return "", false
	}
	if r.Kind != event.KindFact {
		return "", false
	}
	switch r.Step {
	case event.StepDispatched:
		return "dispatched " + r.Task + laneIn(d.Lane), false
	case event.StepSubmitted:
		return "submitted " + r.Task, false
	case event.StepSendback:
		return "sendback " + r.Task + noteIn(d.Note), false
	case event.StepNeedsYou:
		if d.Cause == event.CauseHeld {
			return "held " + r.Task + noteIn(strings.TrimPrefix(d.Reason, "held: ")), true
		}
	case event.StepMerged:
		if d.Head == "" {
			return "merged " + r.Task + " (outside obor serve)", false
		}
		return "merged " + r.Task + " " + d.Head[:min(7, len(d.Head))], false
	case event.StepReviewRequest:
		if len(d.After) > 0 {
			return "review requested (after " + strings.Join(d.After, ",") + ")", false
		}
		return "review requested", false
	case event.StepReviewStarted:
		return "review started " + d.ID, false
	case event.StepReviewRm:
		return "review request ended", false
	case event.StepMessage:
		return "line to " + d.To + ": " + d.Line, false
	}
	return "", false
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
