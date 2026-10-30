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
// SessionStart hook, launch's Codex rollout, OpenCode's plugin, the import of a .sid file).
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
// nor when id is another agent's current session (a log that holds such a duplicate keeps one owner, the earlier).
func Admits(ss []Session, s Session) bool {
	if s.Agent == "" || s.ID == "" {
		return false
	}
	for _, r := range ss {
		if r.Agent == s.Agent && r.ID == s.ID {
			return false
		}
	}
	return Holder(ss, s.Agent, s.ID) == ""
}

// Owned is ss, session facts in log order, as recorded: each that Admits the ones before it.
func Owned(ss []Session) []Session {
	var out []Session
	for _, s := range ss {
		if Admits(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// ReadSessionsAt is every session fact of the project at root in log order, as recorded (Owned: a refused duplicate
// is not one), read-only (launch, hooks: serve may be down). No database yet: none.
func ReadSessionsAt(root string) ([]Session, error) {
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
		if json.Unmarshal(data, &s) == nil && s.Agent != "" && s.ID != "" {
			out = append(out, s)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sessions: %w", err)
	}
	return Owned(out), nil
}

// CurrentAt is agent's current session in the project at root (ReadSessionsAt, Current); ok false: none, or the
// store cannot be read.
func CurrentAt(root, agent string) (Session, bool) {
	ss, err := ReadSessionsAt(root)
	if err != nil {
		return Session{}, false
	}
	return Current(ss, agent)
}

// TranscriptAt is the recorded transcript of session id in the project at root ("": none recorded).
func TranscriptAt(root, id string) string {
	ss, _ := ReadSessionsAt(root)
	for i := len(ss) - 1; i >= 0; i-- {
		if ss[i].ID == id && ss[i].Transcript != "" {
			return ss[i].Transcript
		}
	}
	return ""
}
