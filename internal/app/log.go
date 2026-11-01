package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"obor/internal/project"
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
	// Rev is the spec rev a worker's fetched job carried (0: none, or a delivery before T-440).
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

// String is the record as `obor serve status` shows it: step{reason}.
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

func (r Record) key() Key { return Key{r.Task, r.Attempt, r.Step} }

// DBFile is a project's database: <root>/.obor/state/obor.db (internal/store). Its events table is the event log.
func DBFile(root string) string { return store.File(root) }

// Log is the append-only event log: the events table of a project's database. Open it with OpenLog (replay); one
// writer per database: the serve holding the project's serve.flock (App.load), so two serves (two kits) never append
// to one project's log.
type Log struct {
	db *sql.DB
	// refuse, when set, refuses the append of a record it returns an error for (a seam for crash tests: the
	// effect ran, its result never reached the log).
	refuse func(Record) error

	mu       sync.Mutex
	seq      int64            // the last record's seq in the table
	err      error            // the last append's error (nil: it succeeded, or none yet); obor serve status shows it
	results  map[Key]Record   // by key
	pending  map[Key]Record   // intents without a result, by key
	intents  map[Key]Record   // every intent, with or without its result (its seq orders open jobs), by key
	inflight map[Key]struct{} // keys whose effect runs now
}

// OpenLog replays db's event log: its records in seq order.
func OpenLog(db *sql.DB) (*Log, []Record, error) {
	l := &Log{db: db, results: map[Key]Record{}, pending: map[Key]Record{}, intents: map[Key]Record{},
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

// RecentEvents is db's newest limit records, newest first: obor monitor's event list (T-658). The caller opens the
// store read-only (store.OpenRO) and keeps it while it polls; limit ≤ 0: none. Events must exist for it to have any.
func RecentEvents(db *sql.DB, limit int) ([]Record, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := db.Query("SELECT seq, ts, task, attempt, step, kind, class, data FROM events ORDER BY seq DESC LIMIT ?", limit)
	if err != nil {
		return nil, fmt.Errorf("event log: %w", err)
	}
	defer rows.Close()
	return scanRecords(rows)
}

// records is every row of db's events table in seq order.
func records(db *sql.DB) ([]Record, error) {
	rows, err := db.Query("SELECT seq, ts, task, attempt, step, kind, class, data FROM events ORDER BY seq")
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
		l.pending[r.key()] = r
		l.intents[r.key()] = r
	case KindResult:
		l.results[r.key()] = r
		delete(l.pending, r.key())
	}
}

// Append writes r with the next seq and the time now, on disk before it returns.
func (l *Log) Append(r Record, now time.Time) (Record, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.append(r, now)
}

// append is Append under l.mu; its error is kept (Err) until an append succeeds.
func (l *Log) append(r Record, now time.Time) (Record, error) {
	r, err := l.write(r, now)
	l.err = err
	return r, err
}

// Err is the last append's error: nil once an append succeeded after it.
func (l *Log) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// write inserts r. A failed insert rolls back: the record is not in the log and its seq is reused.
func (l *Log) write(r Record, now time.Time) (Record, error) {
	if l.refuse != nil {
		if err := l.refuse(r); err != nil {
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
	return r, nil
}

// Intent is an outside effect about to happen: its key, crash class and data (the effect's inputs replay needs).
type Intent struct {
	Key
	Class string
	Data  any
}

// Do runs effect once for in.Key: it writes the intent, runs effect without holding the log, and writes its result.
// A key that has a result returns the stored result and does not run effect. A failed effect writes no result: the
// intent stays pending (replay lists it) and the caller settles or retries it: a pending key's intent is reused (no
// second intent line) and its effect runs again. A key whose effect runs now is refused.
func (l *Log) Do(ctx context.Context, in Intent, now func() time.Time, effect func(context.Context) (any, error)) (json.RawMessage, error) {
	l.mu.Lock()
	if r, ok := l.results[in.Key]; ok {
		l.mu.Unlock()
		return r.Data, nil
	}
	if _, ok := l.inflight[in.Key]; ok {
		l.mu.Unlock()
		return nil, fmt.Errorf("%s/%d/%s: its effect is running", in.Task, in.Attempt, in.Step)
	}
	if _, ok := l.pending[in.Key]; !ok {
		rec := Record{Task: in.Task, Attempt: in.Attempt, Step: in.Step, Kind: KindIntent, Class: in.Class}
		if in.Data != nil {
			b, err := json.Marshal(in.Data)
			if err != nil {
				l.mu.Unlock()
				return nil, err
			}
			rec.Data = b
		}
		if _, err := l.append(rec, now()); err != nil {
			l.mu.Unlock()
			return nil, err
		}
	}
	l.inflight[in.Key] = struct{}{}
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.inflight, in.Key)
		l.mu.Unlock()
	}()

	v, err := effect(ctx)
	if err != nil {
		return nil, err
	}
	res := Record{Task: in.Task, Attempt: in.Attempt, Step: in.Step, Kind: KindResult}
	if v != nil {
		if res.Data, err = json.Marshal(v); err != nil {
			return nil, err
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.append(res, now()); err != nil {
		return res.Data, err
	}
	return res.Data, nil
}

// Open: k has an intent without a result (a crash or a failed effect left it; its effect does not run now).
func (l *Log) Open(k Key) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, pending := l.pending[k]
	_, running := l.inflight[k]
	return pending && !running
}

// Intent is k's intent while it has no result (false: none, or it has a result).
func (l *Log) Intent(k Key) (Record, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.pending[k]
	return r, ok
}

// IntentOf is k's intent as written, with or without its result (false: none): its seq orders job_next's open jobs,
// the oldest first (T-606).
func (l *Log) IntentOf(k Key) (Record, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.intents[k]
	return r, ok
}

// Done: k has a result.
func (l *Log) Done(k Key) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.results[k]
	return ok
}

// OpenIntents is every intent without a result, running or not, in seq order: the steps whose effect runs now, or was
// left to run again after a crash (App.do). obor status' running line lists the command steps among them whose effect
// runs now, its pending line the others (T-657, T-662).
func (l *Log) OpenIntents() []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
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
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Record
	for k, r := range l.pending {
		if _, ok := l.inflight[k]; !ok {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// MethodBackup asks serve for a backup of Call.Root's database (obor nightly, T-435): serve, its one writer, takes it.
const MethodBackup = "backup"

// backupKeep is how many daily backups a project keeps.
const backupKeep = 7

// BackupDir is a project's database backups: <root>/.obor/state/backup/obor-<YYYY-MM-DD>.db.
func BackupDir(root string) string { return filepath.Join(root, ".obor", "state", "backup") }

// backup is MethodBackup: the backup of a project this serve has loaded.
func (a *App) backup(c Call) (string, error) {
	root := filepath.Clean(c.Root)
	a.mu.Lock()
	p := a.projAt(root)
	a.mu.Unlock()
	if p == nil {
		return "", fmt.Errorf("backup: %s is not loaded by this serve", root)
	}
	now := a.opt.Now()
	path, err := store.BackupDaily(p.log.db, BackupDir(root), now, backupKeep)
	if err != nil {
		return "", err
	}
	pruneStopFailure(root, now)
	return "BACKUP " + path, nil
}

// Backup is obor nightly's backup of root's database (keeping the newest 7): through the serve at sock when it has
// the project loaded, else directly on a read-only open (WAL: a consistent copy while another serve writes). A
// project without a database ("": serve never loaded it) has nothing to back up.
func Backup(ctx context.Context, sock, root string, now time.Time) (string, error) {
	if reply, err := Verdict(ctx, sock, MethodBackup, Call{Root: root}); err == nil {
		return reply, nil
	}
	if _, err := os.Stat(DBFile(root)); os.IsNotExist(err) {
		return "", nil
	}
	db, err := store.OpenRO(DBFile(root))
	if err != nil {
		return "", err
	}
	defer db.Close()
	path, err := store.BackupDaily(db, BackupDir(root), now, backupKeep)
	if err != nil {
		return "", err
	}
	pruneStopFailure(root, now)
	return "BACKUP " + path, nil
}

// pruneStopFailure drops stop-failure entries older than the check logs' 14-day retention period.
func pruneStopFailure(root string, now time.Time) {
	path := filepath.Join(project.StateDir(root), project.StopFailureLog)
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	cutoff := now.Add(-14 * 24 * time.Hour)
	var kept bytes.Buffer
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry struct {
			At string `json:"at"`
		}
		if json.Unmarshal(line, &entry) == nil {
			if at, err := time.Parse(time.RFC3339, entry.At); err == nil && at.Before(cutoff) {
				continue
			}
		}
		kept.Write(line)
		kept.WriteByte('\n')
	}
	_ = os.WriteFile(path, kept.Bytes(), 0o600)
}
