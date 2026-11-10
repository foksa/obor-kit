package view

import (
	"database/sql"
	"os"
	"strings"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// Detail is one project's drill-down pages, for the API of `obor web` and nothing else: the project's root (its
// store, read-only, is opened per page; the sessions' transcripts) and its view (serve's live bits: a task's state, a
// needs-you's answer commands; serve down: none). The store alone answers the rest: a task's spec, report, gate,
// records and jobs; an agent's sessions, briefs, lines and jobs; a job's text, which is a prompts row.
type Detail struct {
	Root    string
	Project Project
}

// TaskDetail is one task's page: the view's Task (where it is: a lane freed since — the task merged — its
// newest dispatch's lane and agent), its spec, the report it was last submitted with, its last gate, its needs-you,
// its records newest first and the prompts of its jobs.
type TaskDetail struct {
	Task
	DispatchedAt int64        `json:"dispatched_at,omitempty"` // the lane row's dispatch (epoch seconds)
	Spec         string       `json:"spec,omitempty"`          // the current rev's text
	GatedRev     int          `json:"gated_rev,omitempty"`     // the rev the last gate checked
	Report       string       `json:"report,omitempty"`        // the last submission's report
	GateLine     string       `json:"gate_line,omitempty"`     // the last gate's result line
	GatePass     bool         `json:"gate_pass,omitempty"`     // the last gate's pass: its outcome is green (the page reads this, never the line's wording)
	NeedsYou     []NeedsYou   `json:"needs_you"`
	Records      []TaskRecord `json:"records"` // newest first
	Jobs         []Prompt     `json:"jobs"`    // the task's job prompts, newest first
}

// TaskRecord is one record of a task's timeline: its seq, time, step and kind, the worded line when the view words
// it (an intent or a result the event list does not show has none), a gate's outcome (green, red …; the
// page reads it, never the line's wording), and a gate's pass (the outcome as the page's yes or no).
type TaskRecord struct {
	Seq     int64  `json:"seq"`
	TS      string `json:"ts"`
	Step    string `json:"step"`
	Kind    string `json:"kind"`
	Line    string `json:"line,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	Pass    bool   `json:"pass,omitempty"`
}

// Prompt is one row of the prompts table as a page shows it: what serve sent an agent, the job id (jobs only),
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
	// Background is its long-running background processes (Project.AgentBackground).
	Background []Background `json:"background,omitempty"`
}

// AgentSession is one recorded session of an agent (store.Session, the one session shape) and when its transcript was
// last written (LastWrite, epoch seconds; 0: no transcript, or it cannot be read).
type AgentSession struct {
	store.Session
	LastWrite int64 `json:"last_write,omitempty"`
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
	db, close := storeAt(d.Root)
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
		}
	}
	recs, err := event.RecordsOf(db, task, detailRecords)
	if err != nil {
		return t, found
	}
	reached := -1
	for _, r := range recs { // newest first: the last submission and gate win
		d := app.Describe(r)
		t.Records, found = append(t.Records, TaskRecord{Seq: r.Seq, TS: r.TS, Step: r.Step, Kind: r.Kind, Line: d.Line,
			Outcome: d.Outcome, Pass: d.Kind == app.KindGate && d.Outcome == port.OutcomeGreen}), true
		reached = max(reached, recordStep(r, d))
		base, _ := event.StepN(r.Step)
		switch {
		case r.Kind == event.KindFact && r.Step == event.StepSubmitted && t.Report == "":
			t.Report = d.Report
		case r.Kind == event.KindResult && base == event.StepGate && t.GateLine == "":
			t.GateLine, t.GatedRev, t.GatePass = d.Text, d.SpecRev, d.Outcome == port.OutcomeGreen
		case r.Kind == event.KindFact && r.Step == event.StepDispatched && t.Lane == "" && t.Agent == "":
			// The lane was freed (the task merged, its row cleared): where its newest dispatch put it.
			t.Lane, t.Agent = d.Lane, d.Agent
		}
	}
	if t.Step == nil && reached >= 0 { // needs-you, held or ended: its phase places it on none, its records do
		t.Step = &reached
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
	// no pane when nothing runs it), and an active agent's status, lane and task keep precedence.
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
	db, close := storeAt(d.Root)
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
	db, close := storeAt(d.Root)
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

// recordStep is the step record r (described as d) shows its task reached (wire.TaskStep*; -1: none): the dispatch,
// the job fetched or a sendback (working), the submission, a gate (the gate: its lane test or its review next), a
// lane test (red: the gate; green: the review), the review job, an accept or a publish (merging). The merged fact is the page's own "every step done".
func recordStep(r event.Record, d app.Described) int {
	base, _ := event.StepN(r.Step)
	method, _, _ := strings.Cut(r.Step, ".")
	switch {
	case r.Kind == event.KindFact && r.Step == event.StepDispatched:
		return wire.TaskStepDispatched
	case r.Kind == event.KindFact && (r.Step == event.StepDelivered || r.Step == event.StepSendback):
		return wire.TaskStepWorking
	case r.Kind == event.KindFact && r.Step == event.StepSubmitted:
		return wire.TaskStepSubmitted
	case r.Kind != event.KindResult:
		return -1
	case method == wire.MethodAccept || method == event.StepPublish:
		return wire.TaskStepMerging
	case base == event.StepTest && d.Outcome == "green":
		return wire.TaskStepReview
	case base == event.StepGate, base == event.StepTest:
		return wire.TaskStepGate
	case method == event.StepReview:
		return wire.TaskStepReview
	}
	return -1
}

// agentLines is the newest n lines posted to agent, newest first.
func agentLines(db *sql.DB, agent string, n int) ([]AgentLine, error) {
	recs, err := event.MessagesTo(db, agent, n)
	if err != nil {
		return nil, err
	}
	out := []AgentLine{}
	for _, r := range recs {
		d := app.Describe(r)
		out = append(out, AgentLine{Seq: r.Seq, TS: r.TS, From: d.From, Line: d.Text})
	}
	return out, nil
}

// sessionOf is s as the agent page shows it, with its transcript's last write.
func sessionOf(s store.Session) AgentSession {
	a := AgentSession{Session: s}
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
