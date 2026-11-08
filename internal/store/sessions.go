package store

import (
	"encoding/json"
	"fmt"
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

// Current is agent's newest session in ss (by Started; of two at the same second, the later in ss); ok false: none.
func Current(ss []Session, agent string) (Session, bool) {
	var cur Session
	ok := false
	for _, s := range ss {
		if s.Agent == agent && (!ok || s.Started >= cur.Started) {
			cur, ok = s, true
		}
	}
	return cur, ok
}

// Holder is the agent other than agent whose current session in ss is id ("": none).
func Holder(ss []Session, agent, id string) string {
	seen := map[string]bool{agent: true}
	for _, s := range ss {
		if seen[s.Agent] {
			continue
		}
		seen[s.Agent] = true
		if cur, _ := Current(ss, s.Agent); cur.ID == id {
			return s.Agent
		}
	}
	return ""
}

// Admits: s joins ss, the sessions recorded so far, as its agent's session (T-586, T-588): the one ownership rule, for
// serve live and on replay (internal/app) and for the store's readers. Not when ss records s's (agent, id) already,
// nor when id is another agent's current session (a log that holds such a duplicate keeps one owner, the earlier) —
// except a record that names no transcript and is followed by one that does: the SessionStart hook's confirmation of
// the id obor chose before the start fills in its transcript_path (T-813), and Owned keeps the confirmation.
func Admits(ss []Session, s Session) bool {
	f := sessionsOf(ss)
	return f.add(s)
}

// Owned is ss, session facts in log order, as recorded: each that Admits the ones before it, a confirmation of an
// earlier record (T-813: same agent and id, now with the transcript) in that record's place, so an agent's sessions
// stay one per id. One fold over ss (T-881: the rule's rescans are a lookup in each of the fold's maps).
func Owned(ss []Session) []Session {
	f := foldSessions(ss)
	return f.all
}

// sessions is one fold over a log's session facts (T-881): the records the rule admitted in log order, the (agent, id)
// records by their place in it, each agent's current session and each session id's owners. It is the ownership rule's
// only implementation: a record joins when its (agent, id) is unknown and no other agent's current session is its id,
// and a known one (agent, id) takes its record's place only when it is the confirmation T-813 names. Its zero value is
// an empty log.
type sessions struct {
	all    []Session                  // the admitted records, in log order
	known  map[sessionKey]int         // (agent, id) → its index in all
	cur    map[string]int             // agent → the index in all of its current session
	owners map[string]map[string]bool // session id → the agents whose current session it is
}

// sessionKey is a record's identity: one (agent, id) is one record.
type sessionKey struct{ agent, id string }

// sessionsOf is ss as a fold, taking its records as recorded — one per (agent, id), which Owned and the app's fold
// keep: the maps are built without the rule, so a predicate asked of the fold sees exactly the records a scan of ss
// does (T-881: Admits asks the fold instead of scanning ss).
func sessionsOf(ss []Session) sessions {
	var f sessions
	f.init()
	for _, s := range ss {
		if s.Agent == "" || s.ID == "" {
			continue
		}
		f.all = append(f.all, s)
		i := len(f.all) - 1
		f.known[sessionKey{s.Agent, s.ID}] = i
		if cur, ok := f.cur[s.Agent]; !ok || f.better(i, cur) {
			f.setCurrent(i)
		}
	}
	return f
}

// init makes the fold's maps (its zero value is an empty log).
func (f *sessions) init() {
	if f.known == nil {
		f.known, f.cur, f.owners = map[sessionKey]int{}, map[string]int{}, map[string]map[string]bool{}
	}
}

// add folds s: whether the rule admits it. A new (agent, id) is appended; a confirmation replaces its record in place,
// so its position (and with it every tie-break) stays.
func (f *sessions) add(s Session) bool {
	if s.Agent == "" || s.ID == "" {
		return false
	}
	f.init()
	k := sessionKey{s.Agent, s.ID}
	i, known := f.known[k]
	if known && (f.all[i].Transcript != "" || s.Transcript == "") {
		return false
	}
	if f.holder(s.Agent, s.ID) != "" {
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
func (f *sessions) better(i, j int) bool {
	return f.all[i].Started > f.all[j].Started ||
		(f.all[i].Started == f.all[j].Started && i > j)
}

// current is agent's current session in f (ok false: none).
func (f *sessions) current(agent string) (Session, bool) {
	if i, ok := f.cur[agent]; ok {
		return f.all[i], true
	}
	return Session{}, false
}

// holder is the agent other than agent whose current session is id ("": none). The rule only asks whether there
// is one; of several (a confirmation can leave two agents on one id, as the rule it replaces does) the earliest record
// names it.
func (f *sessions) holder(agent, id string) string {
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
func (f *sessions) setCurrent(i int) {
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
func (f *sessions) unown(id, agent string) {
	delete(f.owners[id], agent)
	if len(f.owners[id]) == 0 {
		delete(f.owners, id)
	}
}

// confirm replaces record i with s, a confirmation of it (T-813): the same (agent, id), its transcript now known. The
// record keeps its place, so it stays its agent's current session unless the confirmation starts earlier than what it
// replaces, which hands the choice to the agent's newest record (the same selection Current makes).
func (f *sessions) confirm(i int, s Session) {
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
func (f *sessions) choose(agent string) {
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

// foldSessions is ss folded: one pass of the ownership rule (T-881).
func foldSessions(ss []Session) sessions {
	var f sessions
	for _, s := range ss {
		f.add(s)
	}
	return f
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
	f := foldSessions(ss)
	return f.current(agent)
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
