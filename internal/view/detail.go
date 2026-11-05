package view

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"

	"obor/internal/app/event"
	"obor/internal/store"
)

// Detail is one project's drill-down pages (T-667), for the API of `obor web` and nothing else: the project's root (its
// store, read-only, is opened per page; the sessions' transcripts) and its view (serve's live bits: a task's state, a
// needs-you's answer commands; serve down: none). The store alone answers the rest: a task's spec, report, gate,
// records and jobs; an agent's sessions, briefs, lines and jobs; a job's text, which is a prompts row.
type Detail struct {
	Root    string
	Project Project
}

// TaskDetail is one task's page: the view's Task (T-883; where it is: a lane freed since — the task merged — its
// newest dispatch's lane and agent), its spec, the report it was last submitted with, its last gate, its needs-you,
// its records newest first and the prompts of its jobs.
type TaskDetail struct {
	Task
	DispatchedAt int64        `json:"dispatched_at,omitempty"` // the lane row's dispatch (epoch seconds)
	Spec         string       `json:"spec,omitempty"`          // the current rev's text
	GatedRev     int          `json:"gated_rev,omitempty"`     // the rev the last gate checked
	Report       string       `json:"report,omitempty"`        // the last submission's report
	GateLine     string       `json:"gate_line,omitempty"`     // the last gate's result line
	GateLog      string       `json:"gate_log,omitempty"`      // the check's log, under the project root
	NeedsYou     []NeedsYou   `json:"needs_you"`
	Records      []TaskRecord `json:"records"` // newest first
	Jobs         []Prompt     `json:"jobs"`    // the task's job prompts, newest first
}

// TaskRecord is one record of a task's timeline: its seq, time, step and kind, and the worded line when the view words
// it (an intent or a result the event list does not show has none).
type TaskRecord struct {
	Seq  int64  `json:"seq"`
	TS   string `json:"ts"`
	Step string `json:"step"`
	Kind string `json:"kind"`
	Line string `json:"line,omitempty"`
}

// Prompt is one row of the prompts table as a page shows it (T-665): what serve sent an agent, the job id (jobs only),
// the task (a job's), the kind (job, brief or start), when, and its text.
type Prompt struct {
	Seq   int64  `json:"seq"`
	Kind  string `json:"kind"`
	Job   string `json:"job,omitempty"`
	Task  string `json:"task,omitempty"`
	Agent string `json:"agent"`
	TS    string `json:"ts"`
	Text  string `json:"text"`
}

// AgentDetail is one agent's page: what serve sees it on now (status, lane, task), the sessions obor recorded for it (a transcript path and its last write, never the
// contents), the briefs and lines it was sent, and the jobs it was given, with their text.
type AgentDetail struct {
	Agent    string         `json:"agent"`
	Status   string         `json:"status,omitempty"` // its pane's status as serve sees it, or its role row's "stopped" ("" : no pane)
	Lane     string         `json:"lane,omitempty"`   // the lane serve sees it in
	Task     string         `json:"task,omitempty"`   // the task serve sees it on now
	Sessions []AgentSession `json:"sessions"`         // newest first
	Briefs   []Prompt       `json:"briefs"`           // newest first
	Lines    []AgentLine    `json:"lines"`            // newest first
	Jobs     []Prompt       `json:"jobs"`             // newest first
	// Background is its long-running background processes (T-714; Project.AgentBackground).
	Background []Background `json:"background,omitempty"`
}

// AgentSession is one recorded session of an agent (store.Session) and when its transcript was last written (LastWrite,
// epoch seconds; 0: no transcript, or it cannot be read).
type AgentSession struct {
	ID         string `json:"id"`
	Kind       string `json:"kind,omitempty"`
	Transcript string `json:"transcript,omitempty"`
	LastWrite  int64  `json:"last_write,omitempty"`
	Pane       string `json:"pane,omitempty"`
	Lane       string `json:"lane,omitempty"`
	Task       string `json:"task,omitempty"`
	Started    int64  `json:"started"`
	Source     string `json:"source,omitempty"`
}

// AgentLine is one line posted to an agent (a message fact): its seq, time, who sent it and the line.
type AgentLine struct {
	Seq  int64  `json:"seq"`
	TS   string `json:"ts"`
	From string `json:"from,omitempty"`
	Line string `json:"line"`
}

// The detail pages read at most these many rows each (a page, not a dump).
const (
	detailRecords = 200
	detailLines   = 100
	detailPrompts = 50
)

// Task is task's page: ok false when the project knows no such task (no record, spec, lane row, needs-you or prompt).
func (d Detail) Task(task string) (TaskDetail, bool) {
	if task == "" {
		return TaskDetail{}, false
	}
	vt, found := d.Project.Tasks[task]
	vt.Task = task
	t := TaskDetail{Task: vt, NeedsYou: []NeedsYou{}, Records: []TaskRecord{}, Jobs: []Prompt{}}
	for _, n := range d.Project.NeedsYou {
		if n.Task == task {
			t.NeedsYou, found = append(t.NeedsYou, n), true
		}
	}
	db, close := d.open()
	defer close()
	if db == nil {
		return t, found
	}
	if row, ok, err := store.ReadSpec(db, task); err == nil && ok {
		t.Spec, found = row.Text, true
	}
	if rows, err := store.ReadLanes(db); err == nil {
		for _, r := range rows {
			if r.Task != task {
				continue
			}
			found = true
			t.DispatchedAt = r.DispatchedAt
			if t.Lane == "" {
				t.Lane = r.Lane
			}
			if t.Agent == "" {
				t.Agent = r.Agent
			}
		}
	}
	recs, err := taskRecords(db, task, detailRecords)
	if err != nil {
		return t, found
	}
	for _, r := range recs {
		t.Records, found = append(t.Records, r.TaskRecord), true
	}
	for _, r := range recs { // newest first: the last submission and gate win
		base, _ := event.StepN(r.Step)
		switch {
		case r.Kind == event.KindFact && r.Step == event.StepSubmitted && t.Report == "":
			t.Report = dataString(r.data, "report")
		case r.Kind == event.KindResult && base == event.StepGate && t.GateLine == "":
			t.GateLine = dataString(r.data, "line")
			t.GatedRev = int(dataNumber(r.data, "spec_rev"))
			t.GateLog = gateLog(task)
		case r.Kind == event.KindFact && r.Step == event.StepDispatched && t.Lane == "" && t.Agent == "":
			// The lane was freed (the task merged, its row cleared): where its newest dispatch put it.
			t.Lane, t.Agent = dataString(r.data, "lane"), dataString(r.data, "agent")
		}
	}
	if rows, err := store.ReadPrompts(db, store.PromptQuery{Task: task}, detailPrompts); err == nil {
		for _, r := range rows {
			if r.Kind != store.PromptJob {
				continue
			}
			t.Jobs, found = append(t.Jobs, promptOf(r)), true
		}
	}
	return t, found
}

// Agent is agent's page: ok false when nothing records the agent (no role row, session, prompt or line).
func (d Detail) Agent(agent string) (AgentDetail, bool) {
	a := AgentDetail{Agent: agent, Sessions: []AgentSession{}, Briefs: []Prompt{}, Lines: []AgentLine{},
		Jobs: []Prompt{}}
	if agent == "" {
		return AgentDetail{}, false
	}
	found := false
	a.Background = d.Project.AgentBackground(agent)
	for _, s := range d.Project.Agents {
		if s.Name == agent {
			a.Status, a.Lane, a.Task, found = s.Status, s.Lane, s.Task, true
		}
	}
	// A role agent on no lane is in the roles, not the agents: its page is still a page (stopped when serve stopped it,
	// no pane when nothing runs it), and an active agent's status, lane and task keep precedence (T-680).
	for _, r := range d.Project.Roles {
		if r.Agent != agent {
			continue
		}
		found = true
		if a.Status == "" {
			a.Status = r.Status
		}
	}
	if d.Root != "" {
		if ss, err := store.ReadSessionsAt(d.Root); err == nil {
			for i := len(ss) - 1; i >= 0; i-- { // newest first
				s := ss[i]
				if s.Agent != agent {
					continue
				}
				found = true
				a.Sessions = append(a.Sessions, sessionOf(s))
			}
		}
	}
	db, close := d.open()
	defer close()
	if db == nil {
		return a, found
	}
	if rows, err := store.ReadPrompts(db, store.PromptQuery{Agent: agent}, detailPrompts); err == nil {
		for _, r := range rows {
			found = true
			switch r.Kind {
			case store.PromptJob:
				a.Jobs = append(a.Jobs, promptOf(r))
			case store.PromptBrief:
				a.Briefs = append(a.Briefs, promptOf(r))
			}
		}
	}
	if lines, err := agentLines(db, agent, detailLines); err == nil && len(lines) > 0 {
		a.Lines, found = lines, true
	}
	return a, found
}

// Job is the prompt a job was sent (a prompts row with that id): ok false when there is none.
func (d Detail) Job(job string) (Prompt, bool) {
	if job == "" {
		return Prompt{}, false
	}
	db, close := d.open()
	defer close()
	if db == nil {
		return Prompt{}, false
	}
	rows, err := store.ReadPrompts(db, store.PromptQuery{Job: job}, 1)
	if err != nil || len(rows) == 0 {
		return Prompt{}, false
	}
	return promptOf(rows[0]), true
}

// open is the project's store, read-only, and the func that closes it: nil when the project has none (or Root is "").
func (d Detail) open() (*sql.DB, func()) {
	if d.Root == "" {
		return nil, func() {}
	}
	db, _ := store.OpenAt(d.Root)
	if db == nil {
		return nil, func() {}
	}
	return db, func() { db.Close() }
}

// taskRecord is a task's record with the raw data the report and the gate read.
type taskRecord struct {
	TaskRecord
	data json.RawMessage
}

// taskRecords is task's newest n records, newest first.
func taskRecords(db *sql.DB, task string, n int) ([]taskRecord, error) {
	rows, err := db.Query(`SELECT seq, ts, step, kind, data FROM events WHERE task = ? ORDER BY seq DESC LIMIT ?`,
		task, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []taskRecord{}
	for rows.Next() {
		var r taskRecord
		var data []byte
		if err := rows.Scan(&r.Seq, &r.TS, &r.Step, &r.Kind, &data); err != nil {
			return nil, err
		}
		if len(data) > 0 {
			r.data = data
		}
		r.Line, _ = eventLine(event.Record{Task: task, Step: r.Step, Kind: r.Kind, Data: r.data})
		out = append(out, r)
	}
	return out, rows.Err()
}

// agentLines is the newest n lines posted to agent (message facts with that To), newest first.
func agentLines(db *sql.DB, agent string, n int) ([]AgentLine, error) {
	rows, err := db.Query(`SELECT seq, ts, data FROM events WHERE kind = ? AND step = ?
		AND json_extract(data, '$.to') = ? ORDER BY seq DESC LIMIT ?`, event.KindFact, event.StepMessage, agent, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentLine{}
	for rows.Next() {
		var l AgentLine
		var data []byte
		if err := rows.Scan(&l.Seq, &l.TS, &data); err != nil {
			return nil, err
		}
		l.From, l.Line = dataString(data, "from"), dataString(data, "line")
		out = append(out, l)
	}
	return out, rows.Err()
}

// sessionOf is s as the agent page shows it, with its transcript's last write.
func sessionOf(s store.Session) AgentSession {
	a := AgentSession{ID: s.ID, Kind: s.Kind, Transcript: s.Transcript, Pane: s.Pane, Lane: s.Lane, Task: s.Task,
		Started: s.Started, Source: s.Source}
	if s.Transcript != "" {
		if fi, err := os.Stat(s.Transcript); err == nil {
			a.LastWrite = fi.ModTime().Unix()
		}
	}
	return a
}

// promptOf is r as a page shows it.
func promptOf(r store.PromptRow) Prompt {
	return Prompt{Seq: r.Seq, Kind: r.Kind, Job: r.Job, Task: r.Task, Agent: r.Agent, TS: r.TS, Text: r.Text}
}

// gateLog is where a task's gate keeps the check command's full output.
func gateLog(task string) string {
	return filepath.Join(".obor", "state", "logs", task+"-check.log")
}

// dataString is key in a record's data as a string ("" when the data has none).
func dataString(data json.RawMessage, key string) string {
	var d map[string]json.RawMessage
	if len(data) == 0 || json.Unmarshal(data, &d) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(d[key], &s) != nil {
		return ""
	}
	return s
}

// dataNumber is key in a record's data as a number (0 when the data has none).
func dataNumber(data json.RawMessage, key string) float64 {
	var d map[string]json.RawMessage
	if len(data) == 0 || json.Unmarshal(data, &d) != nil {
		return 0
	}
	var n float64
	if json.Unmarshal(d[key], &n) != nil {
		return 0
	}
	return n
}
