// Package view is the project's read model (T-664): one function, Build, joins obor serve's status answer (none when
// serve is down), the git lane table and the store (read-only) into one Project, and Load is that build for a project at
// a root. Every reader of project state goes through this package: `obor status` prints the Project as JSON or renders it
// as text, and the web UI serves it. The drill-down pages read narrow (T-880): LoadTask, LoadAgent and LoadSetup build
// only what their page needs — no git and no ps for a task or an agent page. app never imports view; only entry packages
// do (internal/checks/boundary_test.go).
package view

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/store"
)

// Project is the project's state as one struct (the `obor status --json` shape): serve up or down, what serve does
// without an agent (running, pending), the lanes (the store's rows and what git says of each worktree, with serve's
// task and agent), serve's open tasks and agents, the role agents, needs-you with the commands that answer it, the
// last full check, the pause, the lines not typed yet, the review line, the lead's watches, the queue and the recent
// events.
type Project struct {
	Serve     Serve              `json:"serve"`
	Running   []Running          `json:"running"`
	Pending   []wire.PendingStep `json:"pending"` // the open command steps whose effect does not run now
	Lanes     []Lane             `json:"lanes"`
	Tasks     map[string]Task    `json:"tasks"`          // every task the view knows, by id (tasks, T-883)
	Agents    []wire.AgentStatus `json:"agents"`         // the agents serve sees in the project
	Roles     []Role             `json:"roles"`          // the role agents on no lane (integrator, reviewer, deep dive, advisor)
	Lead      *Role              `json:"lead,omitempty"` // the lead's agent, when serve sees its pane (T-714: its background processes)
	NeedsYou  []NeedsYou         `json:"needs_you"`
	FullCheck *FullCheck         `json:"full_check"`
	// Promote is the promotion as serve's last pass read it (T-974; nil: branches.promote unset, or serve down): obor
	// status' "promoted" line replaces the full check's.
	Promote *wire.PromoteStatus `json:"promote,omitempty"`
	Paused  *Pause              `json:"paused"`
	// ModelPauses is the model pauses in force (obor pause --model, T-765), by model: the Setup page shows them (T-766).
	ModelPauses map[string]*Pause    `json:"-"`
	Messages    []wire.MessageStatus `json:"messages"`          // the lines not typed yet
	Review      string               `json:"review,omitempty"`  // the open review request, or "review due: …" (T-638)
	Watches     []wire.WatchStatus   `json:"watches,omitempty"` // the lead's open watches (obor status, T-658)
	// Idle is the project's idle state (T-803): the pipeline is empty, what ended it and the lead's acknowledgement.
	// obor status and obor web show it; a dispatch or a review request ends it.
	Idle   *Idle          `json:"idle,omitempty"`
	Queue  Queue          `json:"queue"`
	Events []Event        `json:"events"` // the newest worded records, newest first
	Facts  []event.Record `json:"-"`      // serve's facts in force (obor status --facts)
	// Notes are the project's own lines above serve's (T-709): a config key this binary does not know, a stopped
	// project (Notes builds them); then serve's line on a lane table older than its tick (T-879).
	Notes []string `json:"notes,omitempty"`
}

// Running is one step that runs without an agent (wire.RunningStep, T-657) with the configured command it runs ("": a
// step of git alone, such as publish); the text renderers count its elapsed time from Since.
type Running struct {
	Step    string `json:"step"` // gate, publish, after_merge, nightly or restart
	Task    string `json:"task,omitempty"`
	Command string `json:"command,omitempty"`
	Since   int64  `json:"since"`
}

// Serve: serve answered (Up, since when) or not (Error); the project's state in it.
type Serve struct {
	Up        bool   `json:"up"`
	Since     string `json:"since,omitempty"`
	Error     string `json:"error,omitempty"`      // why it is down
	Herdr     bool   `json:"herdr"`                // serve's herdr subscription is live
	HerdrNote string `json:"herdr_note,omitempty"` // herdr's version and protocol when newer than obor's
	Project   bool   `json:"project"`              // serve has this project loaded
	Merging   bool   `json:"merging"`              // its merge loop runs for it
	LogError  string `json:"log_error,omitempty"`  // its event log's last append error
	PID       int    `json:"pid,omitempty"`        // the serve that answered (its serve.flock's pid)
	// Launchd is the project's launchd agent (T-596; darwin): "none" (not installed), "loaded" (installed, its
	// process not the serve that answered), "running" (the serve that answered is launchd's); "": not asked. The
	// caller fills PID and Launchd (cmd/obor asks the service adapter).
	Launchd   string    `json:"launchd,omitempty"`
	SinceTime time.Time `json:"-"` // Since, parsed (zero: unknown)
}

// Lane is one configured lane: the lane table's row (port.Lane: its worktree as git and the store's row say, its task
// by id, Project.Tasks has the task's own fields, T-883; its Agent is serve's agent in the lane, else the lane row's),
// a restart in progress, the agent's status and whether it is free.
type Lane struct {
	port.Lane
	Op          string `json:"op,omitempty"`
	AgentStatus string `json:"agent_status,omitempty"`
	// Free: no task holds the lane: git says its branch merged (free, dirty-free) and serve has no phase of a task on
	// it (a free lane keeps its last branch's task).
	Free bool `json:"free"`
	// Column is the state column of obor status (T-657): serve's phase of the task (T-788: computed once, by serve),
	// else the lane's State (no task serve has: free, dirty-free, working, missing).
	Column string `json:"column"`
	// Since is when the state column began, as far as the view can tell (epoch seconds; nil: unknown, or a lane with
	// no task): the needs-you's fact, the gate or publish running on the task, the task_done's record (submitted), the
	// lane row's dispatch (a task working). obor web's fleet page shows it (T-658).
	Since *int64 `json:"since,omitempty"`
	// Background is the lane agent's long-running background processes (T-714; Input.Background).
	Background []Background `json:"background,omitempty"`
}

// MarshalJSON writes l flat, the lane table's fields first, with an empty branch, task, reason and warning as null
// (obor status --json and types.ts: string | null).
func (l Lane) MarshalJSON() ([]byte, error) {
	type flat Lane // no MarshalJSON: encoding/json writes the fields
	b, err := json.Marshal(flat(l))
	if err != nil {
		return nil, err
	}
	// These keys come before any nested object, and an unescaped `,"` never occurs inside a JSON string: the first
	// match is the lane's own key.
	for _, k := range []string{"branch", "task", "reason", "warning"} {
		b = bytes.Replace(b, []byte(`,"`+k+`":""`), []byte(`,"`+k+`":null`), 1)
	}
	return b, nil
}

// Job is an open job: its id (<task>/<attempt>/<step>) and whether its agent's job_next fetched it.
type Job struct {
	ID        string `json:"id"`
	Delivered bool   `json:"delivered"`
}

// Role is a role agent on no lane: its role and agent name, its pane's status and for how long (seconds; Status "":
// serve sees no pane of it; RoleStopped: serve or the lead stopped it), the brief it is on (nil: none) and its current
// recorded session.
type Role struct {
	Role    string            `json:"role"`
	Agent   string            `json:"agent"`
	Status  string            `json:"status,omitempty"`
	Age     int64             `json:"age,omitempty"`
	Brief   *wire.BriefStatus `json:"brief,omitempty"`
	Session string            `json:"session,omitempty"`
	// Background is the agent's long-running background processes (T-714; Input.Background).
	Background []Background `json:"background,omitempty"`
}

// NeedsYou is one needs-you fact in force, of a task by id (its lane is the task's, T-883) or of the project (its loop
// error, its lines).
type NeedsYou struct {
	Task   string `json:"task"`
	Reason string `json:"reason"`
	Answer string `json:"answer,omitempty"` // the commands that answer it (serve's ProjectStatus.Answers)
	Since  int64  `json:"since,omitempty"`  // when serve recorded the fact (epoch seconds)
}

// FullCheck is the last full check (.obor/state/last-full-check).
type FullCheck struct {
	SHA     string `json:"sha"`
	Verdict string `json:"verdict"` // GREEN | RED
	At      int64  `json:"at"`
	Summary string `json:"summary"`
}

// Pause is serve's pause in force (event.Pause): until (epoch seconds; null: until cleared), why, who paused.
type Pause struct {
	Until  *int64 `json:"until"`
	Reason string `json:"reason"`
	By     string `json:"by,omitempty"`
}

// Idle is the project's idle state (T-803): since when the pipeline is empty, what ended it, and the lead's
// acknowledgement, as serve sends it.
type Idle = wire.IdleStatus

// Input is what Build joins: serve's answer (Err: it gave none), the lane table, the store (nil: none yet), the
// clock and how many worded events to keep.
type Input struct {
	Status wire.Status
	Err    error
	Lanes  *checks.LaneTable
	DB     *sql.DB
	Now    time.Time
	Events int
	Notes  []string // the project's own lines (Notes)
	// Background is each agent's long-running background processes by agent name (ScanBackground; nil: none read).
	Background map[string][]Background
}

// Notes is the project's own lines for Input.Notes: warn (config.LoadReader's, "" for none), then
// project.StoppedLine when root is stopped (obor stop, T-709).
func Notes(root, warn string) []string {
	var out []string
	if warn != "" {
		out = append(out, warn)
	}
	if project.Stopped(root) {
		out = append(out, project.StoppedLine)
	}
	return out
}

// RoleStopped is a Role's Status when its only labelled pane is at its shell: serve stopped the agent once its work was
// done (T-672) or the lead did (T-677), and no pane runs it.
const RoleStopped = "stopped"

// Build is the project's view: serve's answer joined to the lane table and the store. Serve down: its error, no
// agents and no roles (nothing stale), the lanes from git and the store alone.
func Build(in Input) Project {
	p := in.Lanes
	v := Project{Lanes: []Lane{}, Agents: []wire.AgentStatus{}, Roles: []Role{},
		NeedsYou: []NeedsYou{}, Messages: []wire.MessageStatus{}, Running: []Running{}, Pending: []wire.PendingStep{},
		Events: []Event{}, Notes: in.Notes}
	ps := serveOf(&v, in.Status, in.Err, p, in.Background)
	specs, starts := readSpecs(in.DB), startsOf(ps)
	states := p.States()
	rows, _ := p.Rows()
	v.Tasks = tasks(ps, states, rows, specs)
	for _, s := range states {
		l := lane(p, s, &v, ps, rows[s.Name], starts)
		if l.Agent != "" {
			l.Background = in.Background[l.Agent]
		}
		v.Lanes = append(v.Lanes, l)
	}
	v.FullCheck = readFullCheck(p.Root)
	v.Queue = queue(&v, ps, p.Cfg.Data.Merge.NightlyAt, specs, dispatchedTasks(in.DB), in.Now)
	v.Events = events(in.DB, in.Events, v.Serve)
	return v
}

// serveOf is Build's mapping of serve's answer into v (up or down, running, pending, agents, the role rows and the
// lead, messages, review, watches, idle, facts, needs-you, the pauses and the notes it carries) and the status it read.
// p is the project's lane table: the configured command a running step runs is p's, and a serve of another project
// answers none of it. bg is each agent's background processes (nil: none read).
func serveOf(v *Project, ss wire.Status, err error, p *checks.LaneTable, bg map[string][]Background) wire.ProjectStatus {
	var ps wire.ProjectStatus
	if err != nil {
		v.Serve.Error = err.Error()
		v.NeedsYou = needsYou(ps)
		return ps
	}
	v.Serve.Up, v.Serve.Since, v.Serve.Herdr, v.Serve.HerdrNote = true, ss.Since, ss.Connected, ss.HerdrNote
	v.Serve.SinceTime, _ = time.Parse(time.RFC3339, ss.Since)
	if x := ss.Project; x != nil && filepath.Clean(x.Root) == filepath.Clean(p.Root) {
		ps, v.Serve.Project = *x, true
	}
	for _, r := range ps.Running {
		v.Running = append(v.Running, Running{Step: r.Step, Task: r.Task, Command: runningCommand(p, r),
			Since: r.Since})
	}
	v.Pending = append(v.Pending, ps.Pending...)
	v.Serve.Merging, v.Serve.LogError = ps.Merging, ps.LogError
	if ps.Lanes != "" { // serve's lane table is older than its tick (T-879): obor status and the page show its age
		v.Notes = append(slices.Clip(v.Notes), ps.Lanes)
	}
	v.Agents = append(v.Agents, ps.Agents...)
	v.Messages = append(v.Messages, ps.Messages...)
	v.Review, v.Watches = ps.Review, ps.Watches
	v.Idle = ps.Idle
	v.Promote = ps.Promote
	v.Facts = ps.Facts
	v.NeedsYou = needsYou(ps)
	v.Roles, v.Lead = roles(p.Cfg, ps, bg), lead(p.Cfg, ps, bg)
	pausesOf(v, ps)
	return ps
}

// needsYou is serve's needs-you facts in force with the commands that answer each.
func needsYou(ps wire.ProjectStatus) []NeedsYou {
	out := []NeedsYou{}
	for _, f := range ps.Facts {
		if f.Step != event.StepNeedsYou {
			continue
		}
		n := NeedsYou{Task: f.Task, Reason: f.Fact().Reason, Answer: ps.Answers[f.Seq]}
		if t, err := time.Parse(time.RFC3339, f.TS); err == nil {
			n.Since = t.Unix()
		}
		out = append(out, n)
	}
	return out
}

// pausesOf is serve's pause and model pauses as the view shows them (T-765, T-766).
func pausesOf(v *Project, ps wire.ProjectStatus) {
	if ps.Paused != nil {
		v.Paused = pauseOf(ps.Paused)
	}
	for m, mp := range ps.ModelPauses {
		if v.ModelPauses == nil {
			v.ModelPauses = map[string]*Pause{}
		}
		v.ModelPauses[m] = pauseOf(mp)
	}
}

// lane is s's row of the view: the lane table's facts, its task's id, the agent in it, whether it is free, its state
// column and since when (v.Tasks has its task). starts is when serve last tried to start a task's lane agent
// (startsOf).
func lane(p *checks.LaneTable, s port.Lane, v *Project, ps wire.ProjectStatus, row store.LaneRow,
	starts map[string]int64) Lane {
	l := Lane{Lane: s, Op: restartOp(p.Root, s.Name)}
	// T-969: a lane serve owns shows the task its row records and the dispatch's branch, whatever git's HEAD says: a
	// lane test checks the candidate out detached, so git names no branch. An unowned lane keeps git's reading.
	if ts := laneTaskOn(ps, s.Name); ts != nil && row.Task == ts.Task {
		l.Task = ts.Task
		if row.Branch != "" {
			l.Branch = row.Branch
		}
	}
	if l.Warning == "" && s.State == port.LaneWorking && pluginMissing(p, s, starts) {
		l.Warning = "no-plugin"
	}
	l.Agent, l.AgentStatus = laneAgent(ps, row, s.Name)
	phase := v.TaskOn(l).Phase
	l.Free = phase == "" && (l.State == port.LaneFree || l.State == port.LaneDirtyFree)
	l.Column = l.State
	if phase != "" {
		l.Column = phase
	}
	l.Since = laneSince(l, v, p.Now, row.DispatchedAt)
	return l
}

// laneTaskOn is serve's open task on lane (nil: none): the task whose record owns the lane's row, even while git has
// the lane detached at its candidate (T-969).
func laneTaskOn(ps wire.ProjectStatus, lane string) *wire.TaskStatus {
	for i := range ps.Tasks {
		if ts := &ps.Tasks[i]; ts.Lane == lane && !ts.Ended {
			return ts
		}
	}
	return nil
}

// roles is one row per role agent on no lane that the config names (config.RoleAgents: an optional role, a custom one
// too, only when configured; the worker's are the lanes, the lead is the reader), with its pane's status and session as
// serve sees it and its open brief.
func roles(cfg *config.Config, ps wire.ProjectStatus, bg map[string][]Background) []Role {
	out := []Role{}
	for _, rc := range cfg.RoleAgents() {
		name, agent := rc.Role.Name, rc.Agent
		if name == "lead" || agent == "" {
			continue
		}
		r := Role{Role: name, Agent: agent, Background: bg[agent]}
		for _, a := range ps.Agents {
			if a.Name == agent && a.Lane == "" {
				r.Status, r.Age, r.Session = a.Status, a.Age, a.Session
			}
		}
		if r.Status == "" && slices.Contains(ps.Stopped, agent) {
			r.Status = RoleStopped // serve or the lead stopped it: not missing (T-672, T-677)
		}
		for _, b := range ps.Briefs {
			if b.To == agent {
				b := b
				r.Brief = &b
			}
		}
		out = append(out, r)
	}
	return out
}

// lead is the lead's row (its status and session as serve sees its pane, its background processes); nil when the
// config names no lead agent or serve sees no pane of it.
func lead(cfg *config.Config, ps wire.ProjectStatus, bg map[string][]Background) *Role {
	agent := cfg.AgentName("lead", "")
	if agent == "" {
		return nil
	}
	for _, a := range ps.Agents {
		if a.Name == agent && a.Lane == "" {
			return &Role{Role: "lead", Agent: agent, Status: a.Status, Age: a.Age, Session: a.Session, Background: bg[agent]}
		}
	}
	return nil
}

// laneSince is when l's state column began, as far as the view can tell (nil: a free lane, no task, or nothing
// recorded): the needs-you's fact (needs-you, held), the gate, lane test or publish running on its task (a waiting
// one: its failure's time, else its intent's), the task_done's record (a task submitted) or the lane row's dispatch.
func laneSince(l Lane, v *Project, now, dispatched int64) *int64 {
	at := func(n int64) *int64 { return &n }
	switch l.Column {
	case wire.PhaseNeedsYou, wire.PhaseHeld:
		var best *int64
		for _, n := range v.NeedsYou {
			if n.Since == 0 || v.Tasks[n.Task].Lane != l.Name && (l.Task == "" || n.Task != l.Task) {
				continue
			}
			if best == nil || n.Since > *best {
				best = at(n.Since)
			}
		}
		return best
	case wire.PhaseGating, wire.PhaseTesting, wire.PhaseMerging:
		for _, r := range v.Running {
			if l.Task != "" && r.Task == l.Task && r.Since != 0 {
				return at(r.Since)
			}
		}
	case wire.PhaseGatePending, wire.PhaseTestPending, wire.PhasePublishPending:
		for _, p := range v.Pending {
			if l.Task == "" || p.Task != l.Task {
				continue
			}
			if p.FailedAt != 0 {
				return at(p.FailedAt)
			}
			if p.Since != 0 {
				return at(p.Since)
			}
		}
	}
	if l.DoneAge != nil {
		return at(now - *l.DoneAge)
	}
	if l.Task != "" && dispatched != 0 {
		return at(dispatched)
	}
	return nil
}

// runningCommand is the configured command r runs, for the running line: for a gate or a lane test the command serve
// says it runs now (T-669, T-961: compile, the class's, the guard or check; its name when not configured; "" while
// unknown, never a guess), the full check for the nightly check and a promotion (T-974), commands.after_merge for
// after_merge and after_promote; "" for a step of
// git alone (publish) or an unset command.
func runningCommand(p *checks.LaneTable, r wire.RunningStep) string {
	name := ""
	switch r.Step {
	case event.StepGate, event.StepTest:
		if name = r.Command; name == "" {
			return ""
		}
	case event.StepNightly, event.StepPromote:
		name = "check"
	case event.StepAfterMerge, event.StepAfterPromote:
		name = event.StepAfterMerge
	}
	cmd, ok := p.Cfg.Command(name)
	if !ok && (r.Step == event.StepGate || r.Step == event.StepTest) {
		return name
	}
	return cmd.Cmd
}

// readFullCheck is .obor/state/last-full-check ("<sha> <epoch> GREEN|RED <summary>", obor nightly);
// nil when there is none.
func readFullCheck(root string) *FullCheck {
	b, err := os.ReadFile(filepath.Join(project.StateDir(root), "last-full-check"))
	if err != nil {
		return nil
	}
	f := strings.SplitN(project.FirstLine(string(b)), " ", 4)
	if len(f) < 3 {
		return nil
	}
	at, _ := strconv.ParseInt(f[1], 10, 64)
	fc := &FullCheck{SHA: f[0], At: at, Verdict: f[2]}
	if len(f) == 4 {
		fc.Summary = f[3]
	}
	return fc
}

// startsOf is when serve last tried to start each task's lane agent (TaskStatus.Started, by task; T-893: serve's own
// fold, not the view's guess from the log): a resume runs the failed step again under its next key, so it is the current
// attempt's (pluginMissing). Serve down: none.
func startsOf(ps wire.ProjectStatus) map[string]int64 {
	out := map[string]int64{}
	for _, t := range ps.Tasks {
		if t.Started != 0 {
			out[t.Task] = t.Started
		}
	}
	return out
}

// pluginSettle is how long after serve's last attempt to start a lane's agent (the dispatch, or a resume's retry) the
// agent's plugin may still check in: it runs the session-start hook on its session's first event, which comes after the
// agent started — and obor restart starts an agent outside any dispatch (T-783: a lane restarted by hand and resumed
// was flagged while its plugin was fine and checked in a minute later).
const pluginSettle = 2 * time.Minute

// A Claude lane is missing it when its worktree has no .claude/settings.json enabling obor's plugin (T-705).
// pluginMissing (T-307): a lane whose kind runs obor's hooks through obor's plugin (agents.HooksPlugin: opencode) has
// no plugin marker newer than the dispatch that started its work (obor status warns: the lane runs with no obor
// guard). None at all, or one older than the lane row's dispatched_at (a leftover from before this dispatch): missing.
// No row: a marker at all is fresh, nothing to compare it against. A start the lane's agent has not had the time to
// check in from yet (pluginSettle after the dispatch and after its last agent.start, starts) is no answer either.
func pluginMissing(p *checks.LaneTable, s port.Lane, starts map[string]int64) bool {
	kind := project.RunningKind(p.Cfg, config.AgentInfo{Type: s.Type})
	if k, known := agents.Lookup(kind); known && k.Caps().ProjectPlugin { // Claude loads the plugin from the worktree's own .claude/settings.json (T-705)
		return !config.PluginEnabledIn(s.Dir)
	}
	if k, known := agents.Lookup(kind); !known || k.Caps().Hooks != agents.HooksPlugin {
		return false
	}
	rows, _ := p.Rows()
	row, rowOK := rows[s.Name]
	task := s.Task
	if task == "" {
		task = row.Task
	}
	started := row.DispatchedAt
	if st := starts[task]; st > started {
		started = st
	}
	if started > 0 && p.Now-started < int64(pluginSettle/time.Second) {
		return false
	}
	m, ok := project.PluginMarkerAt(p.Root, s.Name)
	if !ok {
		return true
	}
	return rowOK && m.At < row.DispatchedAt
}

// restartOp is "restart" while a restart of lane holds its restart lock (T-192, T-500), else "".
func restartOp(root, lane string) string {
	if launch.RestartLock(root, lane, "").Held() {
		return "restart"
	}
	return ""
}

// pauseOf is serve's pause record as the view shows it: its reason, who paused and its end (nil: until cleared).
func pauseOf(ps *event.Pause) *Pause {
	p := &Pause{Reason: ps.Why, By: ps.Caller}
	if n := ps.Until; n != 0 {
		p.Until = &n
	}
	return p
}
