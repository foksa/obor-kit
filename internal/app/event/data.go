package event

import (
	"encoding/json"
	"time"

	"obor/internal/app/port"
	"obor/internal/store"
)

// Pause is a pause fact's data, and the pause in force in the status: why, until when (epoch seconds, 0: until
// cleared), who paused and since when.
type Pause struct {
	Model  string `json:"model,omitempty"` // a model's pause (obor pause --model); "": the project's
	Why    string `json:"why,omitempty"`
	Until  int64  `json:"until,omitempty"`
	Caller string `json:"caller,omitempty"`
	Since  string `json:"since,omitempty"`
	Call   string `json:"call,omitempty"`
	Reply  string `json:"reply"`
}

// ConfigSet is a setup edit's fact data: its changes (each key with its value before and after), who
// made them and where (owner, web), their one commit on the main branch, and the call that recorded it with its reply.
// Only Call and Reply are folded.
type ConfigSet struct {
	Changes []ConfigSetChange `json:"changes"`
	By      string            `json:"by"`
	Source  string            `json:"source,omitempty"`
	Commit  string            `json:"commit"`
	Call    string            `json:"call,omitempty"`
	Reply   string            `json:"reply"`
}

// ConfigSetChange is one change of a ConfigSet: the key and its value before and after (one line of JSON each, ""
// unset).
type ConfigSetChange struct {
	Key string `json:"key"`
	Old string `json:"old"`
	New string `json:"new"`
}

// Message is a line fact's data: the agent it goes to and the line typed, who sent it, the brief it ends, and the
// call that recorded it with its reply.
type Message struct {
	To     string `json:"to"`
	Line   string `json:"line,omitempty"`
	From   string `json:"from,omitempty"`
	Ends   string `json:"ends,omitempty"`
	Task   string `json:"task,omitempty"`   // answered: the task whose question it answers; a verify's brief: the tasks it verifies
	Review int64  `json:"review,omitempty"` // the seq of the review request this line ends (review.go)
	Stop   int64  `json:"stop,omitempty"`   // the seq of the role agent's stop this alarm line is for (stopwake.go)
	Drive  int    `json:"drive,omitempty"`  // the attempt of the drive Task whose come-back this line tells the lead
	// Driver: a line between a drive and the lead: message, the drive agent's to the lead (obor tell-lead);
	// answered, the lead's answer to the drive agent (no question ends; each answer is its own line)
	Driver bool   `json:"driver,omitempty"`
	Call   string `json:"call,omitempty"`
	Reply  string `json:"reply,omitempty"`
	// Supersedes: briefed's, the seqs of the agent's undelivered brief lines this brief ends
	Supersedes []int64 `json:"supersedes,omitempty"`
	// Fresh: briefed's (obor brief --fresh): its line is the first prompt of the agent's fresh start, the
	// running agent stopped first
	Fresh bool `json:"fresh,omitempty"`
	// Reviewed: a verify brief's (with Task, the tasks it verifies), the head it handed the reviewer (its
	// worktree moved there first; obor review close's sha once the verify is done). Its tasks count as covered, and the
	// head as reviewed, once the reviewer finished this brief (its finished line, Ends; never a stop's Ends),
	// never when another brief replaced it first (reviewverify.go).
	Reviewed string `json:"reviewed,omitempty"`
	// Of is a lead line's back-reference: what the line reports. A rule's line carries its did key
	// (rules.go): Kind the rule's name, Seq the record it is keyed by.
	Of Of `json:"of,omitzero"`
}

// Of names the record a lead line reports: a kind and its seq.
type Of struct {
	Kind string `json:"kind"`
	Seq  int64  `json:"seq"`
}

// MergedData is a merged fact's data.
type MergedData struct {
	Head    string `json:"head,omitempty"`
	Outside bool   `json:"outside,omitempty"` // a merge outside the app landed it (by hand in git)
}

// RecordTime is r's time stamp (zero when unreadable).
func RecordTime(r Record) time.Time {
	t, _ := time.Parse(time.RFC3339, r.TS)
	return t
}

// Merge is one merged fact of the merge loop's (a merge outside the app has no sha and is none): its task, sha and
// time.
type Merge struct {
	Task, SHA string
	At        time.Time
}

// MergeOf is r as a Merge (false: no merged fact, or a merge outside the app).
func MergeOf(r Record) (Merge, bool) {
	if r.Kind != KindFact || r.Step != StepMerged {
		return Merge{}, false
	}
	var d MergedData
	if json.Unmarshal(r.Data, &d) != nil || d.Head == "" {
		return Merge{}, false
	}
	return Merge{Task: r.Task, SHA: d.Head, At: RecordTime(r)}, true
}

// ReadMerges is root's merges, read-only in seq order (obor flakes); none when serve never made its store. The store
// filters them: a reader does not decode the whole log for a handful of merges.
func ReadMerges(root string) ([]Merge, error) {
	db, err := store.OpenAt(root)
	if err != nil || db == nil {
		return nil, err
	}
	defer db.Close()
	recs, err := Facts(db, StepMerged)
	if err != nil {
		return nil, err
	}
	var ms []Merge
	for _, r := range recs {
		if m, ok := MergeOf(r); ok {
			ms = append(ms, m)
		}
	}
	return ms, nil
}

// SpecSet is a spec_set's fact data: the call id, and the reply it got (the reply carries the rev).
type SpecSet struct {
	Call  string `json:"call"`
	Reply string `json:"reply"`
}

// Pruned is fact pruned's data: what one obor prune run deleted, each with the tip it had, so `git branch <name> <sha>`
// (or `git worktree add <dir> <sha>`) brings it back while git still has the commit.
type Pruned struct {
	Branches  []PrunedRef `json:"branches,omitempty"`
	Worktrees []PrunedRef `json:"worktrees,omitempty"`
	Unmerged  bool        `json:"unmerged,omitempty"` // the run also took the branches of tasks that ended unmerged
	Nightly   bool        `json:"nightly,omitempty"`  // the nightly's run
}

// PrunedRef is a deleted branch or worktree (its dir) and its tip sha.
type PrunedRef struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

// Submit is a submission's fact data: the lane, the agent and its pane, the call that made it and the report.
type Submit struct {
	Lane  string `json:"lane"`
	Agent string `json:"agent,omitempty"`
	Pane  string `json:"pane,omitempty"`
	Call  string `json:"call,omitempty"` // task_done's call id ("": a done file's watch)
	// Report is task_done's report ("": a done file's submission).
	Report string `json:"report,omitempty"`
	// Land: obor land's submission of the owner's drive, Report its summary, Call its call id.
	Land bool `json:"land,omitempty"`
}

// Stop is agent.stop's (worker.stop's) data: the lane, the agent and whether a working one is stopped too.
type Stop struct {
	Lane  string `json:"lane"`
	Agent string `json:"agent,omitempty"`
	Force bool   `json:"force,omitempty"` // worker.stop's: a working agent stopped too
}

// GateIn is a gate's intent data (row 18): the kind, the lane and the sha the gate merges (resolved: checks).
type GateIn struct {
	Kind  string `json:"kind"` // lane, resolved, moved
	Lane  string `json:"lane"`
	Input string `json:"input,omitempty"` // the sha the gate merges (resolved: checks)
}

// SubmitOut is a submission's result: the lane's commit pinned, or its final refusal.
type SubmitOut struct {
	SHA     string `json:"sha,omitempty"`
	Refused string `json:"refused,omitempty"` // Scratch.Submit's final refusal
}

// GateOut is a gate's or a publish's result: the outcome, its line, the candidate it made, the spec rev in its
// merge's message and, for a conflict, the replay mismatch that lost edits of the checked one.
type GateOut struct {
	Outcome   string         `json:"outcome"` // a gate's green, red, conflict, fail; a publish's merged, moved, hold
	Line      string         `json:"line"`
	Candidate port.Candidate `json:"candidate"`
	SpecRev   int            `json:"spec_rev,omitempty"` // a gate's: the spec rev in its merge's message
	// Mismatch: a conflict whose replayed resolution lost edits of the checked one
	Mismatch *port.Mismatch `json:"mismatch,omitempty"`
}

// TestIn is a lane test's intent data (row 18b): the candidate it tests, the target it was merged onto, the lane.
type TestIn struct {
	Candidate string `json:"candidate"`
	Target    string `json:"target"`
	Lane      string `json:"lane"`
}

// TestOut is a lane test's result: green, red or fail, its line and the checks' summary.
type TestOut struct {
	Outcome string `json:"outcome"`
	Line    string `json:"line"`
	Checks  string `json:"checks,omitempty"`
}

// Note is a sendback's, a fix job's or a held drive's data: the note it carries.
type Note struct {
	Note string `json:"note"`
}

// Dispatch is a dispatch or waiting fact's data: the lane, dir, agent, branch, target and redo (port.Dispatch
// first), the models the request resolved, its flags and the call with its reply.
type Dispatch struct {
	port.Dispatch          // first: the record's lane, dir, agent, branch, target, redo
	Force         bool     `json:"force,omitempty"`
	Level         string   `json:"level,omitempty"`  // the spec's difficulty level the models came from
	Types         []string `json:"types,omitempty"`  // model refs, `<model>[:<effort>]`
	Effort        string   `json:"effort,omitempty"` // the effort the lane's agent starts with ("": its kind's default)
	// Type is the model ref the dispatch chose, `<model>[:<effort>]`: the lane row's type. "": a record
	// that records none, or a waiting one.
	Type  string   `json:"type,omitempty"`
	After []string `json:"after,omitempty"` // waiting: the tasks it waits for
	// Drive: the owner's drive (obor drive): no worker, no work job; after lane.prepare the task is driving.
	Drive bool `json:"drive,omitempty"`
	// Fix: a move's outstanding fix of the attempt it moved, a sendback's or a red gate's text; the moved
	// attempt's work job carries it.
	Fix string `json:"fix,omitempty"`
	// Stop: the worker the merge loop stops (with force) before the step that needs the lane: a move's (the
	// redo's, before lane.prepare), or a keep lane's live agent whose recorded start does not run this dispatch's
	// model (keepStop: stopped between tasks, the dispatch's model started). "": none.
	Stop string `json:"stop,omitempty"`
	// Keep: the lane is a keep lane (config.LaneKeep), as the config said at the dispatch: the merged task's
	// after-merge steps keep its agent (afterList), the start runs only when the lane has no live agent
	// (nextDispatch: only when its recorded start runs this dispatch's model), and a waiting one queues for its
	// lane in the scheduler's order (keepAhead).
	Keep   bool   `json:"keep,omitempty"`
	Caller string `json:"caller,omitempty"`
	Call   string `json:"call,omitempty"`
	Reply  string `json:"reply"`
}

// PrepareOut is lane.prepare's result: the base it prepared, or its refusal.
type PrepareOut struct {
	Base    string `json:"base,omitempty"`
	Refused string `json:"refused,omitempty"`
}

// Start is agent.start's intent: the lane's pane and the agent in it at intent time (Ran; Session herdr's
// session id of it, "": none known), what a replay compares the snapshot with. It runs under its job's intent and
// carries its wake (startJob).
type Start struct {
	Lane    string `json:"lane"`
	Agent   string `json:"agent"`
	Pane    string `json:"pane"`
	Ran     bool   `json:"ran,omitempty"`
	Session string `json:"sid,omitempty"`
}

// StartOut is agent.start's result, and the integrator's start.<job> (not "running"): not refused, its job's
// delivery (startJob). Agent, Pane, Terminal, Kind, Session: the agent started there, the pane's terminal id, its
// kind and herdr's session id of it ("": none reported, or a replay that saw it in the snapshot): the start row of
// the identity table (foldStart). Config is the config revision the start was launched under (config.Config.Rev).
// The start's row keeps it, so a keep lane reuses the agent only while the config is that same config
// (startFits). agent.start's alone: the integrator's start.<job> carries none.
type StartOut struct {
	Line     string `json:"line,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Pane     string `json:"pane,omitempty"`
	Terminal string `json:"terminal,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Session  string `json:"sid,omitempty"`
	Config   string `json:"config,omitempty"`
	Refused  string `json:"refused,omitempty"`
}

// Job is a review or resolve prompt intent's data: the candidate's patch hash an accept must name.
type Job struct {
	Patch string `json:"patch,omitempty"`
}

// Verdict is a verdict's intent data; VerdictResult its result, the stored reply.
type Verdict struct {
	Job    string `json:"job"`
	Caller string `json:"caller"`
	Patch  string `json:"patch,omitempty"`
	Note   string `json:"note,omitempty"`
}

// VerdictResult is a verdict's result: the stored reply.
type VerdictResult struct {
	Job   string `json:"job"`
	Reply string `json:"reply"`
	Patch string `json:"patch,omitempty"`
	Note  string `json:"note,omitempty"`
	SHA   string `json:"sha,omitempty"`  // resolved: the merge pinned at refs/obor/resolved/<task>, the gate's input
	Lead  bool   `json:"lead,omitempty"` // a sendback of the lead's or the owner's (leadSendback)
}

// Version is fact version's data: the tool (an agent kind's name, or the project's terminal kind), its version as
// read, and the one the log held before ("": the first sighting).
type Version struct {
	What    string `json:"what"`
	Version string `json:"version"`
	Was     string `json:"was,omitempty"`
}
