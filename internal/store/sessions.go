package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// An agent's sessions (T-577): serve records fact session{…} on task key SessionTask for each session an agent
// starts, one per (agent, id); internal/app writes them (sessions.go there), launch, the hooks and the transcript
// readers read them here. History stays: an agent's current session is its newest (Current).
const (
	SessionTask = "session" // the task key of the session facts
	StepSession = "session" // fact: an agent session began (data: Session)
)

// Session is one agent session as obor recorded it: the agent, its kind, the session id and its transcript file
// ("": not known), the pane, lane and task it started in, when it started (Unix seconds) and who reported it (the
// SessionStart hook, launch's Codex rollout, OpenCode's plugin). Task is the task it started on: a keep lane's session
// spans several (T-1031), its current task is its lane row's (serve's agent status), never this record's.
type Session struct {
	Agent      string `json:"agent"`
	Kind       string `json:"kind,omitempty"`
	ID         string `json:"id"`
	Transcript string `json:"transcript,omitempty"`
	Pane       string `json:"pane,omitempty"`
	Lane       string `json:"lane,omitempty"`
	Task       string `json:"task,omitempty"`
	Started    int64  `json:"started"`
	Source     string `json:"source,omitempty"`
}

// Owned is ss, session facts in log order, as recorded: each that the rule admits given the ones before it, a
// confirmation of an earlier record (T-813: same agent and id, now with the transcript) in that record's place, so an
// agent's sessions stay one per id. One fold over ss (T-881: the rule's rescans are a lookup in each of the fold's
// maps).
func Owned(ss []Session) []Session { return FoldSessions(ss).All() }

// SessionFold is one fold over a log's session facts (T-881): the records the ownership rule admitted in log order,
// their (agent, id) by their place in it, each agent's current session and each session id's owners. It is the rule's
// only implementation: a record joins when its (agent, id) is unknown and no other agent's current session is its id,
// and a known one (agent, id) takes its record's place only when it is the confirmation T-813 names. Its zero value is
// an empty log, so a fold built record by record keeps its index (T-1088: serve's replay calls Add; it no longer
// rebuilds the index for every fact).
type SessionFold struct {
	all    []Session                  // the admitted records, in log order
	known  map[sessionKey]int         // (agent, id) → its index in all
	cur    map[string]int             // agent → the index in all of its current session
	owners map[string]map[string]bool // session id → the agents whose current session it is
}

// sessionKey is a record's identity: one (agent, id) is one record.
type sessionKey struct{ agent, id string }

// FoldSessions is ss folded: one pass of the ownership rule (Owned's fold).
func FoldSessions(ss []Session) SessionFold {
	var f SessionFold
	for _, s := range ss {
		f.Add(s)
	}
	return f
}

// init makes the fold's maps (its zero value is an empty log).
func (f *SessionFold) init() {
	if f.known == nil {
		f.known, f.cur, f.owners = map[sessionKey]int{}, map[string]int{}, map[string]map[string]bool{}
	}
}

// All is the records the rule admitted, in log order.
func (f SessionFold) All() []Session { return f.all }

// Add folds s: whether the rule admits it. A new (agent, id) is appended; a confirmation replaces its record in place,
// so its position (and with it every tie-break) stays.
func (f *SessionFold) Add(s Session) bool {
	if s.Agent == "" || s.ID == "" {
		return false
	}
	f.init()
	k := sessionKey{s.Agent, s.ID}
	i, known := f.known[k]
	if known && (f.all[i].Transcript != "" || s.Transcript == "") {
		return false
	}
	if f.Holder(s.Agent, s.ID) != "" {
		return false
	}
	if !known {
		f.all = append(f.all, s)
		i = len(f.all) - 1
		f.known[k] = i
		if cur, ok := f.cur[s.Agent]; !ok || f.better(i, cur) {
			f.setCurrent(i)
		}
		return true
	}
	f.confirm(i, s)
	return true
}

// better reports whether record i outranks j as a current session: the later start, and of two at one second the one
// later in the log (Current's rule).
func (f SessionFold) better(i, j int) bool {
	return f.all[i].Started > f.all[j].Started ||
		(f.all[i].Started == f.all[j].Started && i > j)
}

// Current is agent's current session in f (ok false: none): the newest by Started, of two at the same second the
// later in the log.
func (f SessionFold) Current(agent string) (Session, bool) {
	if i, ok := f.cur[agent]; ok {
		return f.all[i], true
	}
	return Session{}, false
}

// Transcript is the recorded transcript of agent's session id ("": none recorded).
func (f SessionFold) Transcript(agent, id string) string {
	if i, ok := f.known[sessionKey{agent, id}]; ok {
		return f.all[i].Transcript
	}
	return ""
}

// Holder is the agent other than agent whose current session is id ("": none). The rule only asks whether
// there is one; of several (a confirmation can leave two agents on one id, as the rule it replaces does) the earliest record
// names it.
func (f SessionFold) Holder(agent, id string) string {
	best := ""
	for a := range f.owners[id] {
		if a == agent {
			continue
		}
		if best == "" || f.cur[a] < f.cur[best] {
			best = a
		}
	}
	return best
}

// setCurrent makes record i its agent's current session, with the id's ownership following it: the id the record it
// replaces held stops being that agent's, and i's id becomes its (the rule let the record in).
func (f *SessionFold) setCurrent(i int) {
	agent, id := f.all[i].Agent, f.all[i].ID
	if old, ok := f.cur[agent]; ok && old != i {
		f.unown(f.all[old].ID, agent)
	}
	f.cur[agent] = i
	if f.owners[id] == nil {
		f.owners[id] = map[string]bool{}
	}
	f.owners[id][agent] = true
}

// unown drops agent from id's owners.
func (f *SessionFold) unown(id, agent string) {
	delete(f.owners[id], agent)
	if len(f.owners[id]) == 0 {
		delete(f.owners, id)
	}
}

// confirm replaces record i with s, a confirmation of it (T-813): the same (agent, id), its transcript now known. The
// record keeps its place, so it stays its agent's current session unless the confirmation starts earlier than what it
// replaces, which hands the choice to the agent's newest record (the same selection Current makes).
func (f *SessionFold) confirm(i int, s Session) {
	was := f.all[i].Started
	f.all[i] = s
	cur, ok := f.cur[s.Agent]
	switch {
	case !ok || f.better(i, cur):
		f.setCurrent(i)
	case cur == i && s.Started < was:
		f.choose(s.Agent)
	}
}

// choose makes agent's newest record its current session: confirmed to start earlier than its record, its old current
// may lose to another. Only that confirmation scans (and only this agent's records); every other step is a lookup.
func (f *SessionFold) choose(agent string) {
	best := -1
	for i, s := range f.all {
		if s.Agent == agent && (best < 0 || f.better(i, best)) {
			best = i
		}
	}
	if best >= 0 {
		f.setCurrent(best)
	}
}

// sessionFacts is the project at root's session facts in log order, as the store recorded them (read-only: launch,
// hooks and serve's readers use it while serve may be down). No database yet: none.
func sessionFacts(root string) ([]Session, error) {
	db, err := openROAt(root)
	if err != nil || db == nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT data FROM events WHERE task = ? AND kind = 'fact' AND step = ? ORDER BY seq",
		SessionTask, StepSession)
	if err != nil {
		return nil, fmt.Errorf("sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("sessions: %w", err)
		}
		var s Session
		if json.Unmarshal(data, &s) == nil {
			out = append(out, s)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sessions: %w", err)
	}
	return out, nil
}

// ReadSessionsAt is every session fact of the project at root in log order, as recorded (Owned: a refused duplicate
// is not one), read-only (launch, hooks: serve may be down). No database yet: none.
func ReadSessionsAt(root string) ([]Session, error) {
	ss, err := sessionFacts(root)
	if err != nil {
		return nil, err
	}
	return Owned(ss), nil
}

// CurrentAt is agent's current session in the project at root (the fold's selection, Current's rule); ok false: none,
// or the store cannot be read.
func CurrentAt(root, agent string) (Session, bool) {
	ss, err := sessionFacts(root)
	if err != nil {
		return Session{}, false
	}
	f := FoldSessions(ss)
	return f.Current(agent)
}

// TranscriptAt is the recorded transcript of session id in the project at root ("": none recorded).
func TranscriptAt(root, id string) string {
	ss, _ := sessionFacts(root)
	owned := Owned(ss)
	for i := len(owned) - 1; i >= 0; i-- {
		if owned[i].ID == id && owned[i].Transcript != "" {
			return owned[i].Transcript
		}
	}
	return ""
}

// PruneSessions deletes, in one transaction, the session facts of every session id that no agent's current session is
// and whose records were all written before cutoff and below horizon (T-1089: serve's nightly prune, 30 days on;
// horizon is the log's last seq when the prune began, so the table's newest row stays). An id goes with all its
// records or none: a current session keeps its start and its T-813 confirmation. The fold of what stays must admit
// every kept record as the whole log did (the ownership rule asks other agents' current sessions); when it would not,
// nothing is deleted and that is the error. The count of rows deleted.
func PruneSessions(db *sql.DB, cutoff time.Time, horizon int64) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("sessions: prune: %w", err)
	}
	defer tx.Rollback()
	type fact struct {
		seq int64
		old bool // written before cutoff and below horizon
		s   Session
		ok  bool // its data reads as a session
	}
	rows, err := tx.Query("SELECT seq, ts, data FROM events WHERE task = ? AND kind = 'fact' AND step = ? ORDER BY seq",
		SessionTask, StepSession)
	if err != nil {
		return 0, fmt.Errorf("sessions: prune: %w", err)
	}
	var facts []fact
	for rows.Next() {
		var f fact
		var ts string
		var data []byte
		if err := rows.Scan(&f.seq, &ts, &data); err != nil {
			rows.Close()
			return 0, fmt.Errorf("sessions: prune: %w", err)
		}
		at, err := time.Parse(time.RFC3339, ts)
		f.old = err == nil && at.Before(cutoff) && f.seq < horizon
		f.ok = json.Unmarshal(data, &f.s) == nil
		facts = append(facts, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("sessions: prune: %w", err)
	}
	var ss []Session
	for _, f := range facts {
		if f.ok {
			ss = append(ss, f.s)
		}
	}
	whole := FoldSessions(ss)
	kept := map[string]bool{} // the session ids that stay
	for _, i := range whole.cur {
		kept[whole.all[i].ID] = true
	}
	for _, f := range facts {
		if f.ok && !f.old {
			kept[f.s.ID] = true
		}
	}
	var gone []int64
	var rest []Session
	for _, f := range facts {
		switch {
		case !f.ok: // not a session's record: not this rule's to delete
		case kept[f.s.ID]:
			rest = append(rest, f.s)
		default:
			gone = append(gone, f.seq)
		}
	}
	if len(gone) == 0 {
		return 0, nil
	}
	want := slices.DeleteFunc(slices.Clone(whole.All()), func(s Session) bool { return !kept[s.ID] })
	if !slices.Equal(FoldSessions(rest).All(), want) {
		return 0, fmt.Errorf("sessions: prune: the sessions left would fold differently; %d records kept", len(gone))
	}
	for _, seq := range gone {
		if _, err := tx.Exec("DELETE FROM events WHERE seq = ?", seq); err != nil {
			return 0, fmt.Errorf("sessions: prune: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("sessions: prune: %w", err)
	}
	return int64(len(gone)), nil
}
