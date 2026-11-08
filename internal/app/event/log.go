// Package event is the event log's leaf: the records a project's serve appends, their keys, steps and fact data, the
// append-only Log over the store's events table, and the readers (ReadLog, Dump, ReadMerges). It imports no other
// obor package but store, so readers and adapters take the log without the core (T-691, app split 2/5).
package event

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"obor/internal/store"
)

// Record is one line of a project's event log (docs/state-model.md "The log"). Data is typed per kind by its
// writer: FactData for the watchdog facts, the caller's value for an intent or a result.
type Record struct {
	Seq     int64           `json:"seq"`
	TS      string          `json:"ts"`
	Task    string          `json:"task"`
	Attempt int             `json:"attempt"`
	Step    string          `json:"step"`
	Kind    string          `json:"kind"`            // intent, result or fact
	Class   string          `json:"class,omitempty"` // an intent's crash class: F, S or G; a prompt's: prompt (older logs: P)
	Data    json.RawMessage `json:"data,omitempty"`
}

// Record kinds, crash classes (docs/state-model.md "Crash classes") and the steps this phase records.
const (
	KindFact   = "fact"
	KindIntent = "intent"
	KindResult = "result"

	ClassFact   = "F"      // pure fact
	ClassPrompt = "prompt" // agent.prompt: no crash class, sent again by the step that reaches it (older logs: "P")
	ClassStart  = "S"      // start/stop, checked via the snapshot
	ClassGit    = "G"      // git effect, checked via ancestry

	StepStalled  = "stalled"   // data reason: idle
	StepNeedsYou = "needs-you" // data reason: stalled, blocked, pane exited
	StepResumed  = "resumed"   // the lane's agent works again: the task's watchdog facts but a question end
)

// FactData is a watchdog fact's data: where the agent is and why the fact fired.
type FactData struct {
	Lane        string `json:"lane,omitempty"`
	Machine     string `json:"machine,omitempty"`
	Workspace   string `json:"workspace,omitempty"` // its label
	WorkspaceID string `json:"workspace_id,omitempty"`
	Pane        string `json:"pane,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Merge       bool   `json:"merge,omitempty"` // a merge-loop fact, not a watchdog's
	// A merge-loop needs-you's cause (what obor resume does by default), an unblocked's too (of a watchdog's, only a
	// worker's stop has one: causeStopped, T-532); Job is the integrator job
	// it names, Call the call id of the hold or resume that recorded it, Step the key of the step a failed fact counts.
	Cause string `json:"cause,omitempty"`
	Job   string `json:"job,omitempty"`
	Call  string `json:"call,omitempty"`
	Step  string `json:"step,omitempty"`
	// Via is how a job was delivered: "mcp" when the agent's job_next fetched it ("": its herdr status, T-339).
	Via string `json:"via,omitempty"`
	// Rev is the spec rev a worker's fetched job carried, a newer one task_done's refusal showed it (spec.seen,
	// T-819), or the one a drop of a spec stored and never dispatched recorded (T-1016; 0: none, or a delivery before
	// T-440).
	Rev int `json:"rev,omitempty"`
	// Note is an unblocked's decision: the text of obor resume T-xx [regate|reprompt] "<decision>" (T-511).
	Note string `json:"note,omitempty"`
	// Turn is the turn a worker's stop read from its transcript ended (T-563): a later turn ends the stop.
	Turn string `json:"turn,omitempty"`
	// Limit: a worker's stop is a usage or rate limit, so serve wakes the worker once it resets (T-568); Reset is when
	// (Unix seconds; 0: unknown, a wake every wakeEvery).
	Limit bool  `json:"limit,omitempty"`
	Reset int64 `json:"reset,omitempty"`
}

// Fact is r's data as a fact's (zero when it has none or another shape).
func (r Record) Fact() FactData {
	var d FactData
	if len(r.Data) > 0 {
		_ = json.Unmarshal(r.Data, &d)
	}
	return d
}

// String is the record as `obor status --facts` shows it: step{reason}.
func (r Record) String() string {
	if reason := r.Fact().Reason; reason != "" {
		return r.Step + "{" + reason + "}"
	}
	return r.Step
}

// Key is an intent's idempotency key.
type Key struct {
	Task    string
	Attempt int
	Step    string
}

func (r Record) Key() Key { return Key{r.Task, r.Attempt, r.Step} }

// DBFile is a project's database: <root>/.obor/state/obor.db (internal/store). Its events table is the event log.
func DBFile(root string) string { return store.File(root) }

// Log is the append-only event log: the events table of a project's database. Open it with OpenLog (replay); one
// writer per database: the serve holding the project's serve.flock (App.load), so two serves (two kits) never append
// to one project's log.
type Log struct {
	db *sql.DB
	// Refuse, when set, refuses the append of a record it returns an error for (a seam for crash tests: the
	// effect ran, its result never reached the log). Set it under Mu.
	Refuse func(Record) error

	// Mu guards the fields below; the tests of package app also read Results under it.
	Mu       sync.Mutex
	seq      int64            // the last record's seq in the table
	err      error            // the last append's error (nil: it succeeded, or none yet); obor status --facts shows it
	Results  map[Key]Record   // by key
	pending  map[Key]Record   // intents without a result, by key
	intents  map[Key]Record   // every intent, with or without its result (its seq orders open jobs), by key
	inflight map[Key]struct{} // keys whose effect runs now
	changed  chan struct{}    // closed (and replaced) by each record written: the feed's wait (Changed)
}

// DB is the log's database: the lanes, specs and prompts tables share it with the events table.
func (l *Log) DB() *sql.DB { return l.db }

// OpenLog replays db's event log: its records in seq order.
func OpenLog(db *sql.DB) (*Log, []Record, error) {
	l := &Log{db: db, Results: map[Key]Record{}, pending: map[Key]Record{}, intents: map[Key]Record{},
		inflight: map[Key]struct{}{}}
	recs, err := records(db)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range recs {
		l.index(r)
	}
	return l, recs, nil
}

// Close closes the log's database (serve ends).
func (l *Log) Close() error { return l.db.Close() }

// ReadLog is root's event log read-only, in seq order (`obor serve dump`, tests): it works while serve is down or
// writing.
func ReadLog(root string) ([]Record, error) {
	if _, err := os.Stat(DBFile(root)); err != nil {
		return nil, err // a project serve never loaded: errors.Is(err, fs.ErrNotExist)
	}
	db, err := store.OpenRO(DBFile(root))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return records(db)
}

// Dump writes root's event log as the JSONL serve wrote before phase 3, one record per line (`obor serve dump`: a
// rollback's events.jsonl).
func Dump(w io.Writer, root string) error {
	recs, err := ReadLog(root)
	if err != nil {
		return err
	}
	for _, r := range recs {
		b, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("record %d: %w", r.Seq, err)
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// RecentEvents is db's newest limit records, newest first: the view's event list (T-658). The caller opens the
// store read-only (store.OpenRO) and keeps it while it polls; limit ≤ 0: none. Events must exist for it to have any.
func RecentEvents(db *sql.DB, limit int) ([]Record, error) {
	if limit <= 0 {
		return nil, nil
	}
	return queryRecords(db, recordQuery+" ORDER BY seq DESC LIMIT ?", limit)
}

// The view's other reads (T-893): each is one query of the events table, so no reader outside this package knows its
// columns or a record's keys.

// RecordsOf is task's newest limit records, newest first (a task's page).
func RecordsOf(db *sql.DB, task string, limit int) ([]Record, error) {
	return queryRecords(db, recordQuery+" WHERE task = ? ORDER BY seq DESC LIMIT ?", task, limit)
}

// MessagesTo is the newest limit lines posted to agent (message facts with that To), newest first (an agent's page).
func MessagesTo(db *sql.DB, agent string, limit int) ([]Record, error) {
	return queryRecords(db, recordQuery+" WHERE kind = ? AND step = ? AND json_extract(data, '$.to') = ? ORDER BY seq DESC LIMIT ?",
		KindFact, StepMessage, agent, limit)
}

// Since is the records newer than seq, at most limit, oldest first (the event cursor's long poll).
func Since(db *sql.DB, seq int64, limit int) ([]Record, error) {
	return queryRecords(db, recordQuery+" WHERE seq > ? ORDER BY seq LIMIT ?", seq, limit)
}

// Facts is every fact of step, in seq order (the setup page's dispatches; ReadMerges' merges).
func Facts(db *sql.DB, step string) ([]Record, error) {
	return queryRecords(db, recordQuery+" WHERE kind = ? AND step = ? ORDER BY seq", KindFact, step)
}

// ModelPauses is the model pauses in force in db at now (obor pause --model, T-765), by model name: the newest pause
// fact per model, one cleared or past its end dropped (T-979: obor setup show marks them).
func ModelPauses(db *sql.DB, now time.Time) (map[string]*Pause, error) {
	recs, err := queryRecords(db, recordQuery+" WHERE kind = ? AND task = ? AND step IN (?, ?) ORDER BY seq",
		KindFact, PauseTask, StepPaused, StepUnpaused)
	if err != nil {
		return nil, err
	}
	out := map[string]*Pause{}
	for _, r := range recs {
		var d Pause
		if json.Unmarshal(r.Data, &d) != nil || d.Model == "" {
			continue
		}
		delete(out, d.Model)
		if r.Step == StepPaused && (d.Until == 0 || now.Unix() < d.Until) {
			out[d.Model] = &d
		}
	}
	return out, nil
}

// FactTasks is every task that has a fact of one of steps (the queue's dispatched tasks), without reading their data.
func FactTasks(db *sql.DB, steps ...string) (map[string]bool, error) {
	if len(steps) == 0 {
		return map[string]bool{}, nil
	}
	args := []any{KindFact}
	for _, s := range steps {
		args = append(args, s)
	}
	rows, err := db.Query("SELECT DISTINCT task FROM events WHERE kind = ? AND step IN (?"+
		strings.Repeat(", ?", len(steps)-1)+")", args...)
	if err != nil {
		return nil, fmt.Errorf("event log: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var task string
		if err := rows.Scan(&task); err != nil {
			return nil, fmt.Errorf("event log: %w", err)
		}
		out[task] = true
	}
	return out, rows.Err()
}

// recordQuery is every column of the events table, in Record's order: the prefix of a reader's query.
const recordQuery = "SELECT seq, ts, task, attempt, step, kind, class, data FROM events"

// records is every row of db's events table in seq order.
func records(db *sql.DB) ([]Record, error) { return queryRecords(db, recordQuery+" ORDER BY seq") }

// queryRecords is the events rows q selects (in q's order), decoded.
func queryRecords(db *sql.DB, q string, args ...any) ([]Record, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("event log: %w", err)
	}
	defer rows.Close()
	return scanRecords(rows)
}

// scanRecords is rows of the events table's columns, in their order.
func scanRecords(rows *sql.Rows) ([]Record, error) {
	var recs []Record
	for rows.Next() {
		var r Record
		var data []byte
		if err := rows.Scan(&r.Seq, &r.TS, &r.Task, &r.Attempt, &r.Step, &r.Kind, &r.Class, &data); err != nil {
			return nil, fmt.Errorf("event log: %w", err)
		}
		if len(data) > 0 {
			r.Data = data
		}
		recs = append(recs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("event log: %w", err)
	}
	return recs, nil
}

// insert writes recs in one transaction (synchronous=FULL: on disk when it returns).
func insert(db *sql.DB, recs ...Record) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, r := range recs {
		var data any // NULL: no data
		if len(r.Data) > 0 {
			data = string(r.Data)
		}
		if _, err := tx.Exec(`INSERT INTO events (seq, ts, task, attempt, step, kind, class, data)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, r.Seq, r.TS, r.Task, r.Attempt, r.Step, r.Kind, r.Class, data); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// index folds r into the seq and the intent/result maps.
func (l *Log) index(r Record) {
	l.seq = max(l.seq, r.Seq)
	switch r.Kind {
	case KindIntent:
		l.pending[r.Key()] = r
		l.intents[r.Key()] = r
	case KindResult:
		l.Results[r.Key()] = r
		delete(l.pending, r.Key())
	}
}

// Append writes r with the next seq and the time now, on disk before it returns.
func (l *Log) Append(r Record, now time.Time) (Record, error) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	return l.append(r, now)
}

// append is Append under l.Mu; its error is kept (Err) until an append succeeds.
func (l *Log) append(r Record, now time.Time) (Record, error) {
	r, err := l.write(r, now)
	l.err = err
	return r, err
}

// Err is the last append's error: nil once an append succeeded after it.
func (l *Log) Err() error {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	return l.err
}

// write inserts r. A failed insert rolls back: the record is not in the log and its seq is reused.
func (l *Log) write(r Record, now time.Time) (Record, error) {
	if l.Refuse != nil {
		if err := l.Refuse(r); err != nil {
			return r, err
		}
	}
	r.Seq, r.TS = l.seq+1, now.UTC().Format(time.RFC3339)
	if len(r.Data) > 0 && !json.Valid(r.Data) {
		return r, fmt.Errorf("event log: record %s/%d/%s: data is not JSON", r.Task, r.Attempt, r.Step)
	}
	if err := insert(l.db, r); err != nil {
		return r, fmt.Errorf("event log: %w", err)
	}
	l.index(r)
	if l.changed != nil {
		close(l.changed)
	}
	l.changed = make(chan struct{})
	return r, nil
}

// Changed is closed by the next record written (T-1039: the event feed waits on it). Take it before reading the
// records after a cursor: a record written between the read and the wait still closes it.
func (l *Log) Changed() <-chan struct{} {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	if l.changed == nil {
		l.changed = make(chan struct{})
	}
	return l.changed
}

// Intent is an outside effect about to happen: its key, crash class and data (the effect's inputs replay needs).
type Intent struct {
	Key
	Class string
	Data  any
}

// Begin starts an effect under in.Key: the stored result when in.Key has one (done: no effect runs); else the key's
// effect runs from now until its Finish, with its intent: written now (wrote) or the pending one reused. A key whose
// effect runs now is refused.
func (l *Log) Begin(in Intent, now time.Time) (r Record, done, wrote bool, err error) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	if r, ok := l.Results[in.Key]; ok {
		return r, true, false, nil
	}
	if _, ok := l.inflight[in.Key]; ok {
		return Record{}, false, false, fmt.Errorf("%s/%d/%s: its effect is running", in.Task, in.Attempt, in.Step)
	}
	r, ok := l.pending[in.Key]
	if !ok {
		r = Record{Task: in.Task, Attempt: in.Attempt, Step: in.Step, Kind: KindIntent, Class: in.Class}
		if in.Data != nil {
			if r.Data, err = json.Marshal(in.Data); err != nil {
				return Record{}, false, false, err
			}
		}
		if r, err = l.append(r, now); err != nil {
			return Record{}, false, false, err
		}
	}
	l.inflight[in.Key] = struct{}{}
	return r, false, !ok, nil
}

// Finish ends the effect of k that Begin started: when ok, its result v is written (the record returned, its Data
// also when the write failed); a failed effect writes none (the intent stays pending).
func (l *Log) Finish(k Key, v any, ok bool, now time.Time) (Record, error) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	delete(l.inflight, k)
	if !ok {
		return Record{}, nil
	}
	res := Record{Task: k.Task, Attempt: k.Attempt, Step: k.Step, Kind: KindResult}
	if v != nil {
		var err error
		if res.Data, err = json.Marshal(v); err != nil {
			return Record{}, err
		}
	}
	return l.append(res, now)
}

// Open: k has an intent without a result (a crash or a failed effect left it; its effect does not run now).
func (l *Log) Open(k Key) bool {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	_, pending := l.pending[k]
	_, running := l.inflight[k]
	return pending && !running
}

// Intent is k's intent while it has no result (false: none, or it has a result).
func (l *Log) Intent(k Key) (Record, bool) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	r, ok := l.pending[k]
	return r, ok
}

// IntentOf is k's intent as written, with or without its result (false: none): its seq orders job_next's open jobs,
// the oldest first (T-606).
func (l *Log) IntentOf(k Key) (Record, bool) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	r, ok := l.intents[k]
	return r, ok
}

// Done: k has a result.
func (l *Log) Done(k Key) bool {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	_, ok := l.Results[k]
	return ok
}

// Result is k's result record (false: none): its time, or an effect's stored answer.
func (l *Log) Result(k Key) (Record, bool) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	r, ok := l.Results[k]
	return r, ok
}

// OpenIntents is every intent without a result, running or not, in seq order: the steps whose effect runs now, or was
// left to run again after a crash (App.do). obor status' running line lists the command steps among them whose effect
// runs now, its pending line the others (T-657, T-662).
func (l *Log) OpenIntents() []Record {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	out := make([]Record, 0, len(l.pending))
	for _, r := range l.pending {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Pending is every intent without a result, in seq order: the effect may or may not have happened. Its Class says
// how to settle it (docs/state-model.md "Crash classes"); settling is the caller's job.
func (l *Log) Pending() []Record {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	var out []Record
	for k, r := range l.pending {
		if _, ok := l.inflight[k]; !ok {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}
