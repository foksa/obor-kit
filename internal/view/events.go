package view

import (
	"database/sql"
	"sort"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
)

// Event is one record of the log as the view words it: its seq (a UI's cursor: "changed since seq X"; 0 for serve's
// own start, which is no record), its time, its task, its line, whether that line is bad news (T-688), and its kind
// when serve names one (app.KindPromotion, T-977; "": the page reads the line).
type Event struct {
	Seq  int64     `json:"seq"`
	TS   string    `json:"ts"`
	Task string    `json:"task,omitempty"`
	Line string    `json:"line"`
	Bad  bool      `json:"bad,omitempty"`
	Kind string    `json:"kind,omitempty"`
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
			if e, ok := eventOf(r); ok {
				out = append(out, e)
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
	recs, err := event.Since(db, since, limit)
	if err != nil {
		return page, err
	}
	for _, r := range recs {
		page.Seq = r.Seq
		if e, ok := eventOf(r); ok {
			page.Events = append(page.Events, e)
		}
	}
	return page, nil
}

// eventOf is r as the event list shows it (app.Describe's line; false: a record the list does not word).
func eventOf(r event.Record) (Event, bool) {
	d := app.Describe(r)
	if d.Line == "" {
		return Event{}, false
	}
	at, _ := time.Parse(time.RFC3339, r.TS)
	return Event{Seq: r.Seq, TS: r.TS, Task: r.Task, Line: d.Line, Bad: d.Bad, Kind: d.Kind, At: at}, true
}
